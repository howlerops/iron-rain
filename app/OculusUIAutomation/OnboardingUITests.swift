import XCTest

/// First-run, driven through the real app.
///
/// This is the surface with the worst failure mode in the product: a new user who cannot get past
/// it has no app at all, and every bug here was invisible to the unit tests because none of them
/// launch anything. It also had two real defects — a permanently black camera sheet with no exit,
/// and an onboarding screen whose entire explanation was one muted centred line — neither of which
/// a compiler could have caught.
final class OnboardingUITests: XCTestCase {

    override func setUp() {
        super.setUp()
        continueAfterFailure = false
    }

    /// Launches and returns the app, skipping if a desktop is already paired.
    ///
    /// Deliberately NOT done by adding a "reset state" launch argument: that means shipping a code
    /// path in the production app whose only purpose is to let a test win, and it is exactly the
    /// kind of hook that later gets triggered by accident. Adapting to real state costs a skip on an
    /// already-paired simulator and keeps the app honest.
    private func launchUnpaired() throws -> XCUIApplication {
        let app = XCUIApplication()
        app.launch()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20),
                      "App never reached the foreground.")
        guard app.buttons["Add a desktop"].waitForExistence(timeout: 10) else {
            throw XCTSkip("This simulator already has a paired desktop — "
                          + "erase it to exercise first run.")
        }
        return app
    }

    /// The app launches and shows first-run rather than dying or hanging on a blank screen.
    func testLaunchesToOnboarding() throws {
        let app = try launchUnpaired()
        XCTAssertTrue(app.buttons["Add a desktop"].exists,
                      "First run did not offer a way to pair a Mac.")
    }

    /// Onboarding has to say what the app IS and what the user must do next.
    ///
    /// It previously said only "Pair with your Mac's Iron Rain daemon to get started." in the
    /// de-emphasised colour, and never mentioned the two prerequisites: the daemon has to already
    /// be running on the Mac, and the QR code lives in that Mac's ⋯ menu.
    func testOnboardingNamesThePrerequisite() throws {
        let app = try launchUnpaired()
        XCTAssertTrue(app.staticTexts["Pair your Mac"].waitForExistence(timeout: 15),
                      "Onboarding has no headline.")

        // Look for the instruction by substring rather than exact copy, so wording can be edited
        // without breaking the test — what must not regress is that the route to the QR code is
        // stated at all.
        let mentionsRoute = app.staticTexts.containing(
            NSPredicate(format: "label CONTAINS[c] %@", "Pair a phone")
        ).firstMatch
        XCTAssertTrue(mentionsRoute.waitForExistence(timeout: 5),
                      "Onboarding never tells the user where to find the pairing code on their Mac.")
    }

    /// Every control on first run must be reachable by VoiceOver.
    ///
    /// The app shipped with 53 `.help()` strings and 7 accessibility modifiers — tooltips where the
    /// labels should have been — so the primary controls announced as a bare "Button".
    func testFirstRunControlsAreLabelled() throws {
        let app = try launchUnpaired()
        XCTAssertTrue(app.buttons["Add a desktop"].waitForExistence(timeout: 15))

        for button in app.buttons.allElementsBoundByIndex where button.isHittable {
            XCTAssertFalse(button.label.trimmingCharacters(in: .whitespaces).isEmpty,
                           "An interactive control on first run has no accessibility label — "
                           + "VoiceOver would announce it as an unnamed button.")
        }
    }

    /// The pairing sheet must always offer a way out.
    ///
    /// The QR scanner used to set a black background and then `return` out of setup on any failure —
    /// denied camera, simulator, no hardware — leaving a full-screen black sheet with no Cancel, so
    /// a user who tapped "Don't Allow" once had bricked onboarding. The simulator has no camera,
    /// which makes it the exact failure path, and therefore the right place to assert an exit exists.
    func testScannerAlwaysOffersAnExit() throws {
        let app = try launchUnpaired()
        app.buttons["Add a desktop"].tap()

        let scan = app.buttons["Scan QR code"]
        guard scan.waitForExistence(timeout: 8) else {
            // The paste-a-link path is a valid alternative route; only fail if NEITHER exists.
            XCTAssertTrue(app.textFields.firstMatch.waitForExistence(timeout: 5),
                          "The add-desktop sheet offers neither a scanner nor a manual paste field.")
            return
        }
        scan.tap()

        let cancel = app.buttons["Cancel"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 10),
                      "The scanner has no visible Cancel — on a device where the camera is "
                      + "unavailable this is an unrecoverable dead end.")
        cancel.tap()
        XCTAssertTrue(app.buttons["Add a desktop"].waitForExistence(timeout: 10),
                      "Cancelling the scanner did not return to the add-desktop screen.")
    }
}
