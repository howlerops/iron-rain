import XCTest
@testable import OculusUI
import Foundation
#if canImport(WebKit)
import WebKit
#endif

/// Runs the snapshot/click/fill scripts in a REAL WKWebView against a REAL page.
///
/// Everything else about these scripts is tested by inspecting the strings they generate, which
/// proves they are escaped and says nothing about whether they work. A regex over source cannot tell
/// you that the ref survives a re-render, that the native value setter reaches React, or that the
/// snapshot walk terminates on a page with nested interactive elements. Only the engine can.
///
/// These run in the same isolated content world the app uses, so this also exercises the boundary
/// added after the page-world finding.
#if canImport(WebKit)
final class PreviewDOMLiveTests: XCTestCase {

    private var webView: WKWebView!

    private static let page = """
    <!doctype html>
    <html><head><title>E2E Checkout</title></head>
    <body>
      <h1>Checkout</h1>
      <form>
        <label for="email">Email address</label>
        <input id="email" name="email" type="email" placeholder="you@example.com">
        <input id="pw" type="password" name="pw" value="hunter2-must-not-appear">
        <button id="pay" type="button" onclick="document.getElementById('out').textContent='PAID'">Pay now</button>
      </form>
      <div id="out"></div>
      <div style="display:none"><button id="hidden">Invisible</button></div>
    </body></html>
    """

    override func setUp() {
        super.setUp()
        webView = WKWebView(frame: .init(x: 0, y: 0, width: 600, height: 800))
        let loaded = expectation(description: "page loaded")
        let probe = LoadProbe { loaded.fulfill() }
        webView.navigationDelegate = probe
        objc_setAssociatedObject(webView!, "probe", probe, .OBJC_ASSOCIATION_RETAIN)
        webView.loadHTMLString(Self.page, baseURL: URL(string: "http://localhost:4321/"))
        wait(for: [loaded], timeout: 20)
    }

    /// Evaluates in the isolated world the app uses, and returns the script's JSON result decoded.
    @discardableResult
    private func run(_ js: String, file: StaticString = #filePath, line: UInt = #line) -> [String: Any] {
        let done = expectation(description: "js")
        var out: [String: Any] = [:]
        webView.evaluateJavaScript(js, in: nil, in: .defaultClient) { result in
            switch result {
            case .success(let value):
                if let s = value as? String,
                   let d = s.data(using: .utf8),
                   let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any] {
                    out = obj
                } else {
                    XCTFail("script did not return JSON: \(value)", file: file, line: line)
                }
            case .failure(let err):
                XCTFail("script threw: \(err)", file: file, line: line)
            }
            done.fulfill()
        }
        wait(for: [done], timeout: 20)
        return out
    }

    // MARK: - Snapshot

    func testSnapshotSeesTheRealPage() {
        let snap = run(previewSnapshotJS)
        XCTAssertEqual(snap["title"] as? String, "E2E Checkout")
        let elements = snap["elements"] as? [[String: Any]] ?? []
        XCTAssertFalse(elements.isEmpty, "the snapshot found nothing on a page full of controls")

        let names = elements.compactMap { $0["name"] as? String }.joined(separator: " | ")
        XCTAssertTrue(names.contains("Pay now"), "the button should be listed; got \(names)")
        XCTAssertTrue(names.contains("Checkout"), "the heading should be listed; got \(names)")

        // Every listed element carries a ref, or the agent has nothing to act on.
        for e in elements {
            XCTAssertNotNil(e["ref"] as? String, "an element was listed with no ref: \(e)")
        }
    }

    func testSnapshotSkipsHiddenElements() {
        let snap = run(previewSnapshotJS)
        let names = (snap["elements"] as? [[String: Any]] ?? []).compactMap { $0["name"] as? String }
        XCTAssertFalse(names.contains("Invisible"), "a display:none control must not be offered as clickable")
    }

    /// A snapshot enters the agent's context and the durable transcript. A password has no use there.
    func testSnapshotWithholdsThePasswordValue() {
        let snap = run(previewSnapshotJS)
        let json = String(data: try! JSONSerialization.data(withJSONObject: snap), encoding: .utf8)!
        XCTAssertFalse(json.contains("hunter2-must-not-appear"), "the password value reached the snapshot")
        // …while the field itself is still described, so the agent knows it is there.
        XCTAssertTrue(json.contains("password"))
    }

    // MARK: - Click

    func testClickActuallyClicks() {
        let snap = run(previewSnapshotJS)
        let elements = snap["elements"] as? [[String: Any]] ?? []
        guard let pay = elements.first(where: { ($0["name"] as? String) == "Pay now" }),
              let ref = pay["ref"] as? String else {
            return XCTFail("no ref for the Pay button")
        }

        let res = run(previewClickJS(ref: ref))
        XCTAssertNil(res["error"], "click reported \(res["error"] ?? "")")
        XCTAssertEqual(res["clicked"] as? String, ref)

        // The page's own handler ran — this is the whole point of using a real engine.
        let after = run("(function(){ return JSON.stringify({ out: document.getElementById('out').textContent }); })()")
        XCTAssertEqual(after["out"] as? String, "PAID", "the click did not reach the page's handler")
    }

    // MARK: - Fill

    func testFillSetsTheValueAndFiresEvents() {
        let snap = run(previewSnapshotJS)
        let elements = snap["elements"] as? [[String: Any]] ?? []
        guard let email = elements.first(where: { ($0["type"] as? String) == "email" }),
              let ref = email["ref"] as? String else {
            return XCTFail("no ref for the email input")
        }

        // Listen for the events a framework would rely on.
        run("(function(){ window.__evts=[]; document.getElementById('email')" +
            ".addEventListener('input',function(){window.__evts.push('input')});" +
            "document.getElementById('email').addEventListener('change',function(){window.__evts.push('change')});" +
            "return JSON.stringify({ok:true}); })()")

        let res = run(previewFillJS(ref: ref, value: "someone@example.com"))
        XCTAssertNil(res["error"], "fill reported \(res["error"] ?? "")")

        let after = run("(function(){ return JSON.stringify({ v: document.getElementById('email').value, e: (window.__evts||[]).join(',') }); })()")
        XCTAssertEqual(after["v"] as? String, "someone@example.com", "the value was not set")
        XCTAssertTrue((after["e"] as? String ?? "").contains("input"), "no input event — a framework would never notice")
        XCTAssertTrue((after["e"] as? String ?? "").contains("change"), "no change event")
    }

    func testFillRefusesAnElementWithNoValue() {
        let snap = run(previewSnapshotJS)
        let elements = snap["elements"] as? [[String: Any]] ?? []
        guard let heading = elements.first(where: { ($0["tag"] as? String) == "h1" }),
              let ref = heading["ref"] as? String else {
            return XCTFail("no ref for the heading")
        }
        let res = run(previewFillJS(ref: ref, value: "x"))
        XCTAssertNotNil(res["error"], "typing into a heading should be refused")
    }

    // MARK: - Staleness — the silent mis-click this exists to prevent

    func testARefFromAnEarlierSnapshotIsRefused() {
        let first = run(previewSnapshotJS)
        let elements = first["elements"] as? [[String: Any]] ?? []
        guard let ref = elements.first?["ref"] as? String else { return XCTFail("no refs") }

        // Take a second snapshot, exactly as an agent does after a change.
        run(previewSnapshotJS)

        let res = run(previewClickJS(ref: ref))
        let err = res["error"] as? String ?? ""
        XCTAssertTrue(err.contains("Stale"), "an old ref must be refused, not acted on; got \(res)")
    }

    func testMalformedRefsAreRefused() {
        for bad in ["e1", "", "notaref", "s0e0"] {
            let res = run(previewClickJS(ref: bad))
            XCTAssertNotNil(res["error"], "ref \(bad) should have been refused")
        }
    }

    func testRefsAreUniqueWithinASnapshot() {
        let snap = run(previewSnapshotJS)
        let refs = (snap["elements"] as? [[String: Any]] ?? []).compactMap { $0["ref"] as? String }
        XCTAssertEqual(Set(refs).count, refs.count, "duplicate refs would make a click ambiguous")
    }

    // MARK: - Injection, in the engine rather than in a regex

    func testHostileRefAndValueDoNotExecute() {
        run("(function(){ window.__pwned = false; return JSON.stringify({ok:true}); })()")

        _ = run(previewClickJS(ref: "s1e1\"]); window.__pwned = true; ([\""))
        _ = run(previewFillJS(ref: "s1e1", value: "\"); window.__pwned = true; (\""))

        let after = run("(function(){ return JSON.stringify({ pwned: window.__pwned }); })()")
        XCTAssertEqual(after["pwned"] as? Bool, false, "agent-supplied text executed as code")
    }
}

/// Fulfils an expectation once the page has finished loading.
private final class LoadProbe: NSObject, WKNavigationDelegate {
    private let onFinish: () -> Void
    init(onFinish: @escaping () -> Void) { self.onFinish = onFinish }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { onFinish() }
}
#endif
