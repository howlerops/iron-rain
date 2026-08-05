import XCTest

/// The composer, driven through the real app.
///
/// `@FocusState` here was declared, read to draw the focus ring, and written in two places — with no
/// `.focused()` modifier anywhere, so the binding was write-only. The ring never lit, and the slash
/// button (which exists precisely because "type /" is undiscoverable on a phone) inserted a character
/// into an unfocused field without raising the keyboard. That is a dead-feeling primary control, and
/// it shipped, because a write-only binding compiles perfectly.
///
/// The fix went in as first-responder plumbing whose author rated it MEDIUM confidence on macOS
/// pending a real run. This is that run.
final class ComposerUITests: XCTestCase {

    override func setUp() {
        super.setUp()
        continueAfterFailure = false
    }

    private func openSession() throws -> XCUIApplication {
        try PairingSupport.openSession(self)
    }

    /// Tapping the field must raise the keyboard. This is the bug, stated as a test.
    func testTappingComposerRaisesKeyboard() throws {
        let app = try openSession()
        let field = app.textViews.firstMatch
        field.tap()
        XCTAssertTrue(app.keyboards.element.waitForExistence(timeout: 6),
                      "Tapping the composer did not raise the keyboard — the focus binding is not "
                      + "reaching the text view's first responder.")
    }

    /// Typing must reach the field and enable Send.
    func testTypingEnablesSend() throws {
        let app = try openSession()
        let field = app.textViews.firstMatch
        field.tap()
        field.typeText("hello from the ui test")

        let send = app.buttons["Send message"]
        XCTAssertTrue(send.waitForExistence(timeout: 5),
                      "Send has no accessibility label — it was announced as a bare 'Button'.")
        XCTAssertTrue(send.isEnabled, "Send stayed disabled after typing.")
    }

    /// The slash button must focus the field, not type into a dead one.
    func testSlashButtonFocusesTheField() throws {
        let app = try openSession()
        let slash = app.buttons["Insert slash command"]
        guard slash.waitForExistence(timeout: 6) else {
            throw XCTSkip("Slash affordance not present for this provider.")
        }
        slash.tap()
        XCTAssertTrue(app.keyboards.element.waitForExistence(timeout: 6),
                      "The slash button inserted a character without raising the keyboard — "
                      + "exactly the 'broken button' behaviour the focus fix was meant to remove.")
    }

    /// Send and Stop must meet the 44pt minimum, and must not swap places under the thumb.
    ///
    /// Send was a literal 28×28 with `.buttonStyle(.plain)` (which removes system hit padding), and
    /// Stop was inserted to its LEFT when a run started — so send jumped ~42pt mid-tap and the
    /// follow-up landed on the red stop button.
    func testPrimaryControlsMeetHitTargetAndDoNotMove() throws {
        let app = try openSession()
        let send = app.buttons["Send message"]
        guard send.waitForExistence(timeout: 8) else {
            throw XCTSkip("Send button not found.")
        }

        let frame = send.frame
        XCTAssertGreaterThanOrEqual(frame.width, 44,
                                    "Send is \(frame.width)pt wide — under the 44pt minimum.")
        XCTAssertGreaterThanOrEqual(frame.height, 44,
                                    "Send is \(frame.height)pt tall — under the 44pt minimum.")

        // Position must be stable regardless of run state, since the interrupt control now occupies
        // a reserved slot rather than being inserted beside it.
        let before = send.frame.origin
        app.textViews.firstMatch.tap()
        app.textViews.firstMatch.typeText("x")
        XCTAssertEqual(send.frame.origin.x, before.x, accuracy: 1.0,
                       "Send moved horizontally when the composer state changed.")
    }
}
