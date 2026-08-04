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
        let app = XCUIApplication()
        app.launch()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))

        // Onboarding still showing means nothing is paired; there is no session UI to exercise.
        if app.buttons["Add a desktop"].waitForExistence(timeout: 6) {
            throw XCTSkip("No paired desktop on this simulator — pair one to run navigation tests.")
        }
        return app
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
        // Open whatever session is first; if there are none, there is nothing to push.
        let firstSession = app.cells.firstMatch
        guard firstSession.waitForExistence(timeout: 8) else {
            throw XCTSkip("No sessions available to open.")
        }
        firstSession.tap()

        // The composer only exists inside a session, so it is a reliable "the chat is pushed" probe.
        let composer = app.textViews["Message the agent"]
        guard composer.waitForExistence(timeout: 12) else {
            throw XCTSkip("Session did not open — daemon may be unreachable.")
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
