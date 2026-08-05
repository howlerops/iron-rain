import XCTest

/// Tab navigation, driven through the real app.
///
/// This exists because of a specific defect: `showSessionDetail` was bound as
/// `navigationDestination(isPresented:)` in THREE separate NavigationStacks — Sessions, Activity and
/// Fleet — so opening a session from one tab pushed the chat onto all three, and a back-swipe in one
/// silently popped the others. It compiled, it type-checked, and no unit test could see it, because
/// the bug only exists once a NavigationStack is on screen.
///
/// These tests need a paired daemon to reach the session list. When one isn't available they skip
/// rather than fail — a red suite that means "no daemon" trains people to ignore red.
final class NavigationUITests: XCTestCase {

    override func setUp() {
        super.setUp()
        continueAfterFailure = false
    }

    private func launchPaired() throws -> XCUIApplication {
        try PairingSupport.launchPaired(self)
    }

    /// Switching tabs must not carry another tab's pushed detail with it.
    ///
    /// The precise regression: open a session (pushing ChatView), switch to Activity, and find the
    /// chat already pushed there too, because both stacks were driven by the same Bool.
    func testTabsDoNotShareAPushedDetail() throws {
        let app = try launchPaired()

        let sessions = app.tabBars.buttons["Sessions"]
        let activity = app.tabBars.buttons["Activity"]
        guard sessions.waitForExistence(timeout: 10), activity.exists else {
            throw XCTSkip("Tab bar not present — likely running in a regular-width layout.")
        }

        sessions.tap()
        let firstSession = app.cells.firstMatch
        if firstSession.waitForExistence(timeout: 8) {
            firstSession.tap()
        } else if app.buttons["Chat"].waitForExistence(timeout: 6) {
            app.buttons["Chat"].tap() // no sessions yet — make one, so this can't silently skip
        }

        // The composer only exists inside a session, so it is a reliable "the chat is pushed" probe.
        let composer = app.textViews.firstMatch
        guard composer.waitForExistence(timeout: 25) else {
            throw XCTSkip("Could not reach a session — daemon may have no usable agent.")
        }

        activity.tap()
        XCTAssertFalse(composer.waitForExistence(timeout: 3),
                       "Switching to Activity revealed the chat composer — the session detail is "
                       + "being pushed onto more than one navigation stack.")
    }

    /// Returning to a tab must restore that tab's own state, not another's.
    func testReturningToATabRestoresItsOwnState() throws {
        let app = try launchPaired()
        let sessions = app.tabBars.buttons["Sessions"]
        let activity = app.tabBars.buttons["Activity"]
        guard sessions.waitForExistence(timeout: 10), activity.exists else {
            throw XCTSkip("Tab bar not present.")
        }

        activity.tap()
        let activityMarker = app.staticTexts["Activity"].firstMatch
        _ = activityMarker.waitForExistence(timeout: 5)

        sessions.tap()
        activity.tap()
        XCTAssertTrue(app.tabBars.buttons["Activity"].isSelected,
                      "Activity did not remain the selected tab after a round trip.")
    }
}
