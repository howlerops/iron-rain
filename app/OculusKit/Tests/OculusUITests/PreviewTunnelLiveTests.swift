import XCTest
@testable import OculusUI
import Foundation
import OculusKit
#if canImport(WebKit)
import WebKit
#endif

/// Renders a real page THROUGH the tunnel, in a real WKWebView.
///
/// Everything else about this path was verified without a browser: unit tests on the daemon handler,
/// a wire probe against a live daemon, and tests on the URL helpers. None of that answers the only
/// question that matters — whether WebKit accepts what the scheme handler hands it and paints a page.
/// A response can satisfy every assertion about its shape and still fail to render.
#if canImport(WebKit)
final class PreviewTunnelLiveTests: XCTestCase {

    /// Serves a small site, recording which paths the web view asked for.
    private final class FakeSite {
        private(set) var requested: [String] = []
        private let lock = NSLock()

        func fetch(path: String, method: String, headers: [String: String], body: Data?) -> PreviewFetchResp {
            lock.lock(); requested.append(path); lock.unlock()

            func ok(_ body: String, _ type: String) -> PreviewFetchResp {
                PreviewFetchResp(status: 200,
                                 headers: ["Content-Type": type],
                                 body: Data(body.utf8).base64EncodedString())
            }
            switch path.split(separator: "?").first.map(String.init) ?? path {
            case "/":
                return ok("""
                <!doctype html><html><head><title>Tunnelled</title>
                <link rel="stylesheet" href="/app.css">
                </head><body>
                  <h1 id="heading">Hello from the tunnel</h1>
                  <button id="go">Go</button>
                  <script src="/app.js"></script>
                </body></html>
                """, "text/html; charset=utf-8")
            case "/app.css":
                return ok("#heading { color: rgb(1, 2, 3); }", "text/css")
            case "/app.js":
                // Marks the DOM rather than a global. Content worlds share the document but NOT
                // window, so a global set here is invisible to the isolated world the DOM tools run
                // in — which is the isolation working, not a failure. Observing it the way the tools
                // observe everything keeps the test honest about what they can actually see.
                return ok("document.body.setAttribute('data-script-ran', 'yes');", "application/javascript")
            default:
                return PreviewFetchResp(status: 404, headers: [:], body: "")
            }
        }

        func wasRequested(_ path: String) -> Bool {
            lock.lock(); defer { lock.unlock() }
            return requested.contains { $0 == path || $0.hasPrefix(path + "?") }
        }
    }

    private var site: FakeSite!
    private var webView: WKWebView!

    override func setUp() {
        super.setUp()
        site = FakeSite()
        let handler = PreviewSchemeHandler { [site] path, method, headers, body in
            site!.fetch(path: path, method: method, headers: headers, body: body)
        }
        let cfg = WKWebViewConfiguration()
        cfg.setURLSchemeHandler(handler, forURLScheme: previewTunnelScheme)
        webView = WKWebView(frame: .init(x: 0, y: 0, width: 900, height: 700), configuration: cfg)

        let loaded = expectation(description: "loaded")
        let probe = TunnelLoadProbe { loaded.fulfill() }
        webView.navigationDelegate = probe
        objc_setAssociatedObject(webView!, "probe", probe, .OBJC_ASSOCIATION_RETAIN)
        webView.load(URLRequest(url: previewTunnelURL()!))
        wait(for: [loaded], timeout: 25)
    }

    @discardableResult
    private func js(_ script: String, file: StaticString = #filePath, line: UInt = #line) -> Any? {
        let done = expectation(description: "js")
        var out: Any?
        webView.evaluateJavaScript(script, in: nil, in: .defaultClient) { result in
            if case .success(let v) = result { out = v }
            done.fulfill()
        }
        wait(for: [done], timeout: 20)
        return out
    }

    /// The document itself arrived and parsed.
    func testTheDocumentRenders() {
        XCTAssertEqual(js("document.title") as? String, "Tunnelled")
        XCTAssertEqual(js("document.getElementById('heading').innerText") as? String,
                       "Hello from the tunnel")
    }

    /// Relative sub-resources inherit the document's scheme and host, which is the whole reason the
    /// tunnel serves everything under one constant authority.
    func testRelativeSubResourcesComeThroughTheTunnel() {
        XCTAssertTrue(site.wasRequested("/app.css"), "the stylesheet never reached the handler")
        XCTAssertTrue(site.wasRequested("/app.js"), "the script never reached the handler")
    }

    /// CSS delivered through the tunnel is applied, not merely fetched — the difference between the
    /// bytes arriving and the page actually being styled.
    func testStylesheetIsApplied() {
        let colour = js("getComputedStyle(document.getElementById('heading')).color") as? String
        XCTAssertEqual(colour, "rgb(1, 2, 3)", "the tunnelled stylesheet did not take effect")
    }

    /// JavaScript delivered through the tunnel executes.
    ///
    /// Checked through the DOM, because the page's script runs in the PAGE world and the tools read
    /// the isolated one; those share a document and not a window. Reading a global here first showed
    /// nil and looked like the script had never run, when it had.
    func testScriptExecutes() {
        XCTAssertEqual(js("document.body.getAttribute('data-script-ran')") as? String, "yes")
    }

    /// The DOM tools work against a tunnelled page, which is the combination that matters: an agent
    /// inspecting a page its own daemon fetched.
    func testSnapshotWorksOnATunnelledPage() {
        guard let raw = js(previewSnapshotJS) as? String,
              let data = raw.data(using: .utf8),
              let snap = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return XCTFail("snapshot did not return JSON")
        }
        XCTAssertEqual(snap["title"] as? String, "Tunnelled")
        let names = (snap["elements"] as? [[String: Any]] ?? []).compactMap { $0["name"] as? String }
        XCTAssertTrue(names.contains("Go"), "the button was not listed; got \(names)")
    }

    /// Can a tunnelled page open a WEBSOCKET back to its dev server? This is what HMR does, and the
    /// answer has been asserted in comments and release notes without ever being observed.
    ///
    /// A scheme handler is request/response, so it cannot service an upgrade — and a ws:// URL is a
    /// different origin anyway, so it goes to the device's own network stack, where on a phone there
    /// is no dev server. Measured rather than reasoned: the point is to know, and to notice if a
    /// future WebKit changes it.
    func testWebSocketFromATunnelledPageDoesNotReachTheTunnel() {
        js("""
        window.__wsState = 'pending';
        try {
          var ws = new WebSocket('ws://localhost:65531/hmr');
          ws.onopen  = function () { window.__wsState = 'open'; };
          ws.onerror = function () { window.__wsState = 'error'; };
          ws.onclose = function () { if (window.__wsState === 'pending') window.__wsState = 'closed'; };
        } catch (e) { window.__wsState = 'threw: ' + e.name; }
        """)
        let settled = expectation(description: "ws settles")
        DispatchQueue.main.asyncAfter(deadline: .now() + 3.0) { settled.fulfill() }
        wait(for: [settled], timeout: 8)

        let state = js("window.__wsState") as? String
        XCTAssertNotEqual(state, "open", "a websocket reached a dev server the tunnel cannot serve")
        XCTAssertFalse(site.wasRequested("/hmr"), "the scheme handler cannot service an upgrade")
        // Recorded so the actual behaviour is visible when this runs, not just the assertion.
        print("TUNNELLED_WEBSOCKET_STATE: \(state ?? "nil")")
    }

    /// A page that hardcodes an absolute http:// URL does NOT come through the tunnel — that request
    /// goes to the device's own network stack, where on a phone there is no dev server.
    ///
    /// Asserted rather than merely documented, because it is the known limit of this design and a
    /// future change that quietly fixed or worsened it should show up here.
    func testAbsoluteURLsBypassTheTunnel() {
        js("""
        var img = document.createElement('img');
        img.src = 'http://localhost:65533/absolute.png';
        document.body.appendChild(img);
        """)
        // Give the load a moment to be attempted.
        let waited = expectation(description: "settle")
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) { waited.fulfill() }
        wait(for: [waited], timeout: 5)
        XCTAssertFalse(site.wasRequested("/absolute.png"),
                       "an absolute URL reached the handler — the tunnel's scope has changed")
    }
}

private final class TunnelLoadProbe: NSObject, WKNavigationDelegate {
    private let onFinish: () -> Void
    init(onFinish: @escaping () -> Void) { self.onFinish = onFinish }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { onFinish() }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { onFinish() }
}
#endif
