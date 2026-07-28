import XCTest
@testable import OculusUI
import Foundation

/// Tests the pure Design-Mode helpers: decoding the injected picker's message payload and formatting
/// a picked element into a prompt block. The WKWebView interaction itself is native UI, but these
/// two functions carry the actual logic and are fully testable headlessly.
final class DesignModeTests: XCTestCase {
    func testDecodePickedElementFromPickerPayload() throws {
        // The shape the injected JS posts via webkit.messageHandlers.oculusPick.
        let json = """
        {"selector":"div.card > button.cta","html":"<button class=\\"cta\\">Buy</button>","css":"color: rgb(255,255,255);\\nbackground: #D9A520;","text":"Buy"}
        """.data(using: .utf8)!
        let el = try JSONDecoder().decode(PickedElement.self, from: json)
        XCTAssertEqual(el.selector, "div.card > button.cta")
        XCTAssertEqual(el.text, "Buy")
        XCTAssertTrue(el.html.contains("<button"))
    }

    func testPromptBlockContainsHTMLAndCSSFences() {
        let el = PickedElement(selector: "header nav", html: "<nav>…</nav>", css: "display: flex;", text: "Home About")
        let block = designPromptBlock(el)
        XCTAssertTrue(block.contains("Selector: `header nav`"))
        XCTAssertTrue(block.contains("```html"))
        XCTAssertTrue(block.contains("<nav>…</nav>"))
        XCTAssertTrue(block.contains("```css"))
        XCTAssertTrue(block.contains("display: flex;"))
        XCTAssertTrue(block.contains("Home About"))
    }

    func testPromptBlockTruncatesHugeHTML() {
        let huge = String(repeating: "x", count: 5000)
        let block = designPromptBlock(PickedElement(selector: "x", html: huge, css: "", text: nil))
        XCTAssertTrue(block.contains("truncated"), "oversized HTML should be truncated")
        XCTAssertLessThan(block.count, 3000)
    }

    func testPromptBlockOmitsEmptyText() {
        let block = designPromptBlock(PickedElement(selector: "x", html: "<x/>", css: "", text: "   "))
        XCTAssertFalse(block.contains("Visible text"))
    }
}
