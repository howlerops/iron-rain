import XCTest
@testable import OculusUI
@testable import OculusKit

/// An action that reports success must have checked.
///
/// Five screens dismissed, cleared their field, or closed an editor on a predicate that could not
/// fail: a membership test that is always true when editing, a callback fired outside the Task that
/// does the work, a flag the function returns before ever clearing. Each one discards the user's
/// input and reports that it worked.
///
/// These are asserted against the source of the call sites, for the reason DevicesScreenTests gives
/// for doing the same: SwiftUI bodies cannot be evaluated in a unit test, and a predicate written
/// here would only agree with itself.
final class DidItMoveTests: XCTestCase {

    private func sourceOf(_ relative: String) throws -> String {
        let here = URL(fileURLWithPath: #filePath)
        let packageRoot = here.deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        return try String(contentsOf: packageRoot.appendingPathComponent(relative), encoding: .utf8)
    }

    /// The loop editor closed on "is a loop with this id or name in the reloaded list", which is
    /// ALWAYS TRUE when editing — the old version is still in the list whether or not the save
    /// landed. So a failed save closed the editor and discarded the changes, in exactly the case the
    /// check was written to catch.
    func testTheLoopEditorClosesOnTheDaemonsAnswer() throws {
        let src = try sourceOf("Sources/OculusUI/LoopsView.swift")
        XCTAssertFalse(src.contains("model.loops.contains(where: { $0.id == draft.id || $0.name == draft.name })"),
                       "the editor still closes on a membership test that is always true when editing — "
                       + "a failed save silently discards the user's changes")
        XCTAssertTrue(src.contains("if await model.upsertLoop(draft) {"),
                      "the editor does not gate on whether the daemon accepted the save")
    }

    /// upsertLoop has to REPORT. It returned Void and swallowed its error, which is why the editor
    /// had to invent a check in the first place.
    func testUpsertLoopReportsWhetherItSaved() throws {
        let src = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertTrue(src.contains("public func upsertLoop(_ l: Loop) async -> Bool"),
                      "upsertLoop returns nothing, so no caller can tell a save from a refusal")
    }

    /// "Start agent" called onDone(true) OUTSIDE the Task — reporting success before the launch was
    /// attempted — and launchIssue swallowed every failure behind two `try?`s.
    func testStartAgentWaitsForTheLaunch() throws {
        let view = try sourceOf("Sources/OculusUI/IssuesView.swift")
        guard let start = view.range(of: "Button(\"Start\") {") else {
            XCTFail("the Start button is gone")
            return
        }
        let body = String(view[start.upperBound...].prefix(900))
        XCTAssertTrue(body.contains("if await model.launchIssue("),
                      "Start does not await the launch before reporting it — the sheet dismisses "
                      + "cheerfully whether or not an agent was started")

        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertTrue(model.contains("public func launchIssue(_ issue: Issue, projectID: String, agentProvider: String? = nil) async -> Bool"),
                      "launchIssue still reports nothing, so its caller has nothing to wait for")
    }

    /// "Create" inferred success from `trackerError == nil`, and createIssue returned at its own
    /// guard BEFORE clearing that field — so a bail-out dismissed the sheet as though the ticket had
    /// been filed. The guard is the likely path: it fails when the repo has no tracker mapped.
    func testCreateTicketGatesOnTheResultNotAStaleFlag() throws {
        let view = try sourceOf("Sources/OculusUI/IssuesView.swift")
        XCTAssertFalse(view.contains("if model.trackerError == nil { dismiss() }"),
                       "the create sheet still dismisses on a flag the call may never have touched")
        XCTAssertTrue(view.contains("if created { dismiss() }"),
                      "the create sheet does not gate on whether a ticket was created")

        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        guard let fn = model.range(of: "public func createIssue(project: String") else {
            XCTFail("createIssue is gone")
            return
        }
        let body = String(model[fn.upperBound...].prefix(1200))
        XCTAssertTrue(body.contains("trackerError = nil") &&
                      body.range(of: "trackerError = nil")!.lowerBound < (body.range(of: "guard client != nil")?.lowerBound ?? body.endIndex),
                      "createIssue can still return before clearing trackerError, which is what let a "
                      + "bail-out read as a success")
    }

    /// Minting an invite was the only action on the Sharing screen with no did-it-move check: both
    /// failures were swallowed behind `try?` and the label was cleared either way.
    func testCreateInviteKeepsTheLabelWhenItFails() throws {
        let view = try sourceOf("Sources/OculusUI/SharingView.swift")
        guard let btn = view.range(of: "Button(creating ? \"…\" : \"Create\") {") else {
            XCTFail("the Create button is gone")
            return
        }
        let body = String(view[btn.upperBound...].prefix(700))
        XCTAssertTrue(body.contains("if await model.createInvite("),
                      "the label is cleared without asking whether a link was minted")

        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertTrue(model.contains("public func createInvite(label: String, role: String, ttlHours: Int) async -> Bool"),
                      "createInvite reports nothing to gate on")
    }

    /// Assigning a nil result into a Swift Dictionary REMOVES the key, so a failed check rendered
    /// exactly like a button that was never pressed — on two screens whose only purpose is checking.
    func testAFailedCheckIsNotIndistinguishableFromNeverChecking() throws {
        let accounts = try sourceOf("Sources/OculusUI/AccountsView.swift")
        XCTAssertFalse(accounts.contains("quota[a.id] = await model.accountQuota(a.id)"),
                       "a nil quota is still assigned straight into the dictionary, which deletes the "
                       + "key and renders as \"never checked\"")
        XCTAssertTrue(accounts.contains("quotaError[a.id]"),
                      "AccountsView has nowhere to record a failed quota check")

        let remotes = try sourceOf("Sources/OculusUI/RemotesView.swift")
        XCTAssertFalse(remotes.contains("status[host.id] = await model.remoteStatus(host.id)"),
                       "a nil status is still assigned straight into the dictionary")
        XCTAssertTrue(remotes.contains("statusError[host.id]"),
                      "RemotesView has nowhere to record a check that never came back")
    }

    /// "Test" on an MCP server was a silent no-op when the request itself failed — indistinguishable
    /// from a test that ran and found nothing, on the one button whose job is saying whether the
    /// server works.
    func testTheMCPTestButtonReportsAFailedRequest() throws {
        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertTrue(model.contains("public func checkMCPServer(name: String) async -> String?"),
                      "checkMCPServer still swallows both failures behind try?")
        let view = try sourceOf("Sources/OculusUI/MCPServersView.swift")
        XCTAssertTrue(view.contains("checkError[s.name]"),
                      "MCPServersView has nowhere to show a check that could not be run")
    }

    /// The iOS "add by path" field was cleared BEFORE the add was attempted, and addProject reports
    /// its failure into `model.status`, which nothing on that screen renders.
    func testAddByPathKeepsTheTextWhenTheAddFails() throws {
        let src = try sourceOf("Sources/OculusUI/NewSessionView.swift")
        guard let fn = src.range(of: "private func addTypedPath() {") else {
            XCTFail("addTypedPath is gone")
            return
        }
        let body = String(src[fn.upperBound...].prefix(800))
        guard let clearAt = body.range(of: "addPath = \"\""),
              let addAt = body.range(of: "model.addProject(path: p)") else {
            XCTFail("addTypedPath no longer both clears the field and calls addProject")
            return
        }
        XCTAssertGreaterThan(clearAt.lowerBound, addAt.lowerBound,
                             "the field is cleared before the add is attempted, so a rejected path "
                             + "vanishes with nothing to show for it")
        XCTAssertTrue(body.contains("addPathError"),
                      "the failure still goes to model.status, which no view on this screen renders")
    }

    /// The daemon normalizes FOUR issue categories, not three. Canceled and duplicate Linear issues,
    /// and any unrecognised Jira status, land in "other" — which the fallback board had no column
    /// for, so those tickets vanished from the board while remaining in List view.
    func testTheFallbackBoardHasAColumnForEveryCategory() throws {
        let src = try sourceOf("Sources/OculusUI/IssuesView.swift")
        guard let defs = src.range(of: "private let columns: [(name: String, category: String)] = [") else {
            XCTFail("the fallback column list is gone")
            return
        }
        let body = String(src[defs.upperBound...].prefix(300))
        for category in ["todo", "in_progress", "done", "other"] {
            XCTAssertTrue(body.contains("\"\(category)\""),
                          "the fallback board has no column for \"\(category)\", so those tickets are "
                          + "absent from the board and present in List view — with no count that adds up")
        }
    }
}
