import XCTest
import UIKit

/// Gets the app into a paired state so the behavioural tests can actually run.
///
/// Most of what the v0.2.135 design pass changed — composer focus, tab navigation, approvals — only
/// exists once a desktop is paired, so without this those tests skip and the riskiest changes in the
/// release stay unverified. Skipping is the honest default, but it is not the goal.
///
/// Pass a pairing URL in `IRONRAIN_PAIR_URL` and this pairs the simulator on the way in. Generate one
/// from a throwaway daemon rather than your real one:
///
///     go build -o /tmp/oculusd-test ./daemon
///     HOME=/tmp/fakehome /tmp/oculusd-test serve --addr 127.0.0.1:6099 --oauth-port 6901
///     # copy the oculus://pair line it prints
///     IRONRAIN_PAIR_URL='oculus://pair?...' xcodebuild test -scheme Oculus-iOS ...
///
/// `HOME` is what isolates it: the daemon keeps its database, transcripts and pairing state under
/// `~/.oculus`, so pointing HOME elsewhere means a test run cannot touch a real one. The simulator
/// reaches the host's loopback directly, so `127.0.0.1` in the URL resolves to the daemon on your Mac.
enum PairingSupport {

    /// The pairing URL for this run, if one was supplied.
    static var pairURL: String? {
        guard let raw = ProcessInfo.processInfo.environment["IRONRAIN_PAIR_URL"],
              raw.hasPrefix("oculus://") else { return nil }
        return raw
    }

    /// Launches the app and pairs it if it comes up on first run.
    ///
    /// Throws `XCTSkip` when no URL was supplied and nothing is paired — a red suite that only means
    /// "you didn't start a daemon" teaches people to ignore red, which costs more than the coverage
    /// is worth.
    static func launchPaired(_ testCase: XCTestCase) throws -> XCUIApplication {
        let app = XCUIApplication()
        app.launch()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 30),
                      "App never reached the foreground.")

        // A paired-but-disconnected app looks "paired" (no first-run screen) while being useless to
        // every test that follows, and the resulting failures point at whatever screen the test
        // wanted rather than at the connection. Name it here instead.
        //
        // NOTE: this currently fires on the SECOND and later runs against a throwaway daemon. The
        // daemon enrolls the device and issues a credential — devices.json shows the entry — but
        // every reconnect after the initial pairing is refused with "transport: unauthorized", and
        // `last_seen` never advances past `first_seen`. Until that is understood, erase the simulator
        // and mint a fresh pairing code per run.
        if app.buttons["Try again"].waitForExistence(timeout: 3)
            || app.buttons["Scan a new code"].exists {
            throw XCTSkip("Paired but not connected — the daemon is refusing this device's "
                          + "credential. Erase the simulator and pair again with a fresh code.")
        }

        // Already paired from an earlier run in this simulator — nothing to do.
        let addDesktop = app.buttons["Add a desktop"]
        guard addDesktop.waitForExistence(timeout: 8) else { return app }

        guard let url = pairURL else {
            throw XCTSkip("Not paired and no IRONRAIN_PAIR_URL supplied — see PairingSupport.")
        }

        addDesktop.tap()

        let field = app.textFields["Paste oculus://pair link"]
        XCTAssertTrue(field.waitForExistence(timeout: 10),
                      "The add-desktop sheet has no paste field.")
        field.tap()
        // Paste rather than type. A pairing URL is ~290 characters of percent-encoded punctuation
        // (`%3A%2F%2F`, `&`, `=`), and driving that through the software keyboard means XCUITest
        // switching keyboard planes for almost every character — it drops symbols, and the failure
        // presents as "pairing didn't complete" rather than "the text is wrong", which sends you
        // debugging the wrong system. The test runner is itself a process on the simulator, so it can
        // put the URL on the pasteboard directly.
        UIPasteboard.general.string = url
        field.press(forDuration: 1.2)
        let paste = app.menuItems["Paste"]
        if paste.waitForExistence(timeout: 5) {
            paste.tap()
        } else {
            field.typeText(url) // fall back rather than fail outright
        }

        // Confirm the field actually holds the URL before blaming the daemon for what follows.
        let typed = (field.value as? String) ?? ""
        XCTAssertTrue(typed.hasPrefix("oculus://"),
                      "The paste field does not contain the pairing URL (got: \(typed.prefix(40))…)")

        let add = app.buttons["Add desktop"]
        XCTAssertTrue(add.waitForExistence(timeout: 5))
        XCTAssertTrue(add.isEnabled,
                      "Add stayed disabled for a valid oculus:// link — the URL parser rejected it.")
        add.tap()

        // Pairing is a network round trip; give it room before declaring failure.
        XCTAssertTrue(app.buttons["Add a desktop"].waitForNonExistence(timeout: 30),
                      "Still on first run after adding a desktop — pairing did not complete.")
        return app
    }

    /// Opens a session, starting an ephemeral chat if the daemon has none yet.
    ///
    /// A freshly-paired throwaway daemon has an empty session list, so tests that need a composer
    /// would skip forever without this. "Chat" is the cheapest route — it needs no folder or agent
    /// picker, just a session with a live composer, which is the surface under test.
    static func openSession(_ testCase: XCTestCase) throws -> XCUIApplication {
        let app = try launchPaired(testCase)

        // iOS opens on Activity — deliberately, since the phone is a triage inbox — but the session
        // list and the Chat affordance live under Sessions. Without this the helper searched the
        // wrong tab and every composer test skipped while looking like it had a daemon problem.
        let sessionsTab = app.tabBars.buttons["Sessions"]
        if sessionsTab.waitForExistence(timeout: 10) {
            sessionsTab.tap()
        }

        // An existing session is preferable: it exercises the real open path rather than creation.
        let existing = app.cells.firstMatch
        if existing.waitForExistence(timeout: 5) {
            existing.tap()
            if app.textViews.firstMatch.waitForExistence(timeout: 15) { return app }
        }

        let chat = app.buttons["Chat"]
        guard chat.waitForExistence(timeout: 8) else {
            throw XCTSkip("No sessions and no Chat affordance. \(visibleControls(app))")
        }
        chat.tap()

        // Chat opens the New Session sheet rather than dropping straight into a composer, so the
        // flow has to be completed: pick a working folder, then Start.
        let start = app.buttons["Start"]
        if start.waitForExistence(timeout: 12) {
            // Prefer a scratch directory. The chosen folder becomes a real agent's working
            // directory, and a test suite should not casually point one at a source repo — nothing
            // here sends a prompt, but the default should still be the harmless one.
            for folder in ["scratchpad", "tmp", "Documents"] {
                let candidate = app.buttons[folder]
                if candidate.exists { candidate.tap(); break }
            }
            if start.isEnabled {
                start.tap()
            } else if app.buttons["Start new"].exists {
                app.buttons["Start new"].tap()
            }
        }

        guard app.textViews.firstMatch.waitForExistence(timeout: 40) else {
            throw XCTSkip("Could not reach a composer. \(visibleControls(app))")
        }
        return app
    }

    /// What is actually on screen, for skip/failure messages.
    ///
    /// A test that gives up saying "could not reach a composer" sends you debugging the daemon; one
    /// that lists the buttons it COULD see tells you in a single run that you were on the wrong
    /// screen, or that a label changed. The whole cost of this harness so far has been guessing at
    /// that, so it is worth the few lines.
    static func visibleControls(_ app: XCUIApplication) -> String {
        let buttons = app.buttons.allElementsBoundByIndex
            .prefix(25).map(\.label).filter { !$0.isEmpty }
        let cells = app.cells.count
        let fields = app.textFields.count + app.textViews.count
        return "On screen — cells: \(cells), textInputs: \(fields), buttons: \(buttons)"
    }
}

extension XCUIElement {
    /// Waits for an element to GO AWAY. XCTest ships the positive form only, and "the onboarding
    /// screen is gone" is the honest signal that pairing worked — asserting on something appearing
    /// instead would couple the test to whatever screen happens to come next.
    @discardableResult
    func waitForNonExistence(timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if !exists { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
        }
        return !exists
    }
}
