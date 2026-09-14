import XCTest
@testable import OculusUI
@testable import OculusKit

/// The model/wire half of the fourth sweep: fields the daemon sends that the client had no property
/// for, an address sent half-complete, and three surfaces that acted before they had an answer.
final class WireAndSurfaceTests: XCTestCase {

    private func sourceOf(_ relative: String) throws -> String {
        let here = URL(fileURLWithPath: #filePath)
        let packageRoot = here.deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        return try String(contentsOf: packageRoot.appendingPathComponent(relative), encoding: .utf8)
    }

    /// A pushed branch whose `gh pr create` failed is reported ONLY by this field. Without a property
    /// for it the reply reduced to "pushed, no url" — which is also what a successful push with an
    /// uninteresting URL looks like.
    func testAFailedPullRequestDecodes() throws {
        let r = try ProtocolCoding.decoder().decode(WorktreePRResult.self, from: Data("""
        {"session_id":"s1","branch":"oculus/feat","pushed":true,
         "error":"gh: could not create pull request (no upstream configured)"}
        """.utf8))
        XCTAssertTrue(r.pushed)
        XCTAssertNil(r.url)
        XCTAssertEqual(r.error, "gh: could not create pull request (no upstream configured)",
                       "the failure reason did not map, so a failed PR is indistinguishable from a "
                       + "successful push — and `status`, the only other channel, is discarded by "
                       + "deriveHeaderStatus while connected")
        // A success must not look like a failure.
        let ok = try ProtocolCoding.decoder().decode(WorktreePRResult.self, from: Data("""
        {"session_id":"s1","branch":"oculus/feat","pushed":true,"url":"https://example.test/pr/1"}
        """.utf8))
        XCTAssertNil(ok.error)
    }

    /// A loop run that failed to start records why. Without the property the row was a red dot and
    /// the word "error", with an Open button that does nothing because there is no session.
    func testAFailedLoopRunCarriesItsReason() throws {
        let run = try ProtocolCoding.decoder().decode(LoopRun.self, from: Data("""
        {"loop_id":"L1","issue_key":"task","issue_title":"nightly sweep","session_id":"",
         "status":"error","started_at":1750000000,"error":"provider binary not found on PATH"}
        """.utf8))
        XCTAssertEqual(run.status, "error")
        XCTAssertEqual(run.error, "provider binary not found on PATH")
        XCTAssertTrue(run.sessionID.isEmpty)

        // And the row must not offer to open a session that was never created.
        let src = try sourceOf("Sources/OculusUI/LoopsView.swift")
        XCTAssertTrue(src.contains(".disabled(run.sessionID.isEmpty)"),
                      "the run row still offers Open for a run with no session — the one action on "
                      + "the row does nothing, which is how the missing reason presented")
    }

    /// opencode addresses a model as {providerID, modelID}, so sending the id alone is half an
    /// address — the child then silently runs on the provider default instead of the chosen model.
    func testDelegationCarriesTheModelsProvider() throws {
        let src = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertTrue(src.contains("modelProvider: String? = nil,\n                                worktree: Bool = false) async {"),
                      "delegateSubtask still has no modelProvider parameter, so SessionChild's field "
                      + "can never be populated however the sheet is wired")
        XCTAssertTrue(src.contains("model: model, modelProvider: modelProvider,"),
                      "delegateSubtask does not forward modelProvider onto the wire")

        let sheet = try sourceOf("Sources/OculusUI/ChatView.swift")
        XCTAssertTrue(sheet.contains("modelProvider: childModels.first { $0.id == selectedModel }?.provider"),
                      "the delegate sheet does not send the picked model's own provider")

        // The field has to survive a round trip, or the plumbing above is decoration.
        let encoded = try ProtocolCoding.encoder().encode(
            SessionChild(parentSessionID: "p", subtask: "do it", provider: "opencode",
                         model: "claude-sonnet", modelProvider: "anthropic"))
        XCTAssertTrue(String(data: encoded, encoding: .utf8)!.contains("\"model_provider\":\"anthropic\""),
                      "model_provider is not on the wire")
    }

    /// Erasing local state before a fire-and-forget send is the bug stopSession was already fixed
    /// for. removeWorktree had it twice: the cached transcript (and, in the by-id variant, the row
    /// and auto-reopen key) went first, so a failed send deleted this device's history for a worktree
    /// that is still there — and the daemon's next session.list put the row back with nothing behind
    /// it. Offline, `guard let client` made the whole thing a silent no-op.
    func testRemoveWorktreeSendsBeforeItErases() throws {
        let src = try sourceOf("Sources/OculusUI/OculusUI.swift")
        for fn in ["public func removeWorktree(force: Bool = false) async {",
                   "public func removeWorktree(_ id: String, force: Bool = true) async {"] {
            guard let at = src.range(of: fn) else {
                XCTFail("\(fn) is gone")
                continue
            }
            let body = String(src[at.upperBound...].prefix(1400))
            guard let sendAt = body.range(of: "try await client.send(env)"),
                  let forgetAt = body.range(of: "forgetCached(") else {
                XCTFail("\(fn) no longer both sends and forgets — this test cannot order them")
                continue
            }
            XCTAssertLessThan(sendAt.lowerBound, forgetAt.lowerBound,
                              "\(fn) erases the on-device transcript before the send lands")
            XCTAssertTrue(body.contains("actionError ="),
                          "\(fn) still fails silently — the menu item is a no-op when disconnected")
        }
    }

    /// A generative-UI card latches to "Sent — the agent will continue." the instant it is tapped.
    /// invokeUIAction's `guard let client else { return }` then made it a silent no-op, so the card
    /// stated that the agent had been told when nothing had left the device.
    func testAUIActionThatNeverSendsUnlatchesTheCard() throws {
        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        guard let fn = model.range(of: "public func invokeUIAction(") else {
            XCTFail("invokeUIAction is gone")
            return
        }
        let body = String(model[fn.upperBound...].prefix(600))
        XCTAssertFalse(body.contains("guard let client else { return }"),
                       "invokeUIAction still returns silently when disconnected, after the card has "
                       + "already said the action was sent")
        XCTAssertTrue(body.contains("actionError ="), "the disconnected path reports nothing")

        let ui = try sourceOf("Sources/OculusUI/GenerativeUI.swift")
        XCTAssertTrue(ui.contains("if let onAction, await onAction(a) == false { chosen = nil }"),
                      "the choice card never un-says \"Sent\" when the action did not go")
        XCTAssertTrue(ui.contains("if let onAction, await onAction(a, out) == false { submitted = false }"),
                      "the form stays permanently disabled after a submit that never left the device")
    }

    /// Deleting a session is the only irreversible action the sidebar offers — it ends the agent AND
    /// erases this device's transcript — and it fired straight from a context menu. YOLO mode and
    /// "Always allow", both far less final, each sit behind a dialog.
    func testDeletingASessionIsConfirmed() throws {
        let src = try sourceOf("Sources/OculusUI/SessionSidebar.swift")
        guard let at = src.range(of: "Label(\"Delete session\", systemImage: \"trash\")") else {
            XCTFail("the Delete session item is gone")
            return
        }
        let before = String(src[..<at.lowerBound].suffix(300))
        XCTAssertFalse(before.contains("await model.stopSession("),
                       "Delete still calls stopSession straight from the menu, with nothing in between")
        XCTAssertTrue(before.contains("pendingDeleteSession = item"), "Delete does not stage a confirmation")
        XCTAssertTrue(src.contains("Button(\"Delete session\", role: .destructive)"),
                      "there is no confirmation dialog for the staged delete")
    }

    /// `Button("Edit") { onDone() }` set editingLoop = false — the state it was already in. The only
    /// way into the editor from the Loops detail pane was guaranteed to do nothing.
    func testTheLoopEditButtonEntersTheEditor() throws {
        let deck = try sourceOf("Sources/OculusUI/CommandDeck.swift")
        XCTAssertFalse(deck.contains("Button(\"Edit\") { onDone(); }"),
                       "the Edit button still calls onDone, which leaves the editor it never entered")
        XCTAssertTrue(deck.contains("Button(\"Edit\") { onEdit() }"), "Edit does not call onEdit")
        let desktop = try sourceOf("Sources/OculusUI/DesktopViews.swift")
        XCTAssertTrue(desktop.contains("onEdit: { editingLoop = true }"),
                      "onEdit is not wired to anything, so the button is still a no-op")
    }

    /// The sidebar's grouping builds two dictionaries, runs a regex-backed clean() per session,
    /// partitions and sorts — and was evaluated three times per body pass, on a body that
    /// invalidates on every @Published mutation (~25 Hz during a turn).
    func testTheSidebarGroupsOncePerBodyPass() throws {
        let src = try sourceOf("Sources/OculusUI/SessionSidebar.swift")
        guard let body = src.range(of: "var body: some View {") else {
            XCTFail("the body is gone")
            return
        }
        let head = String(src[body.upperBound...].prefix(900))
        XCTAssertTrue(head.contains("let all = groups"), "the grouping is not computed once up front")
        XCTAssertTrue(head.contains("let shown = filtered(all)"), "the filtered set is not derived from it")
        // And the chips must still count over EVERY session, not the filtered set — otherwise every
        // inactive filter reports 0, which is the number that makes the chips worth having.
        XCTAssertTrue(src.contains("filterOptions(all)"),
                      "the filter chips count off the filtered rows, so \"Running 3\" can only ever "
                      + "read 0 for a filter that is not selected")
    }

    /// The diff's per-file add/delete counts walked every line of every hunk on each read, while the
    /// parse around them was cached. totals then walked the whole diff twice more, per body pass.
    func testDiffCountsAreTakenAtParseTime() throws {
        let src = try sourceOf("Sources/OculusUI/DiffReviewView.swift")
        XCTAssertFalse(src.contains("var additions: Int { hunks.reduce"),
                       "additions is still recomputed on every read, several times per body pass")
        XCTAssertTrue(src.contains("let additions: Int"), "the counts are not stored")
        XCTAssertTrue(src.contains("if l.kind == .add { add += 1 } else if l.kind == .del { del += 1 }"),
                      "the parser does not count as it goes, so the work was moved rather than removed")
    }

    /// The test-output pane scrolled to the bottom on EVERY appended line, so scrolling up to read
    /// the first failure was undone by the next line — on the pane whose purpose is reading a failure
    /// that has scrolled past. The transcript has had this gate all along.
    func testTestOutputOnlyFollowsWhenYouAreAtTheBottom() throws {
        let src = try sourceOf("Sources/OculusUI/ChatView.swift")
        guard let at = src.range(of: "onChange(of: model.testOutput.count)") else {
            XCTFail("the test-output follow is gone")
            return
        }
        let body = String(src[at.upperBound...].prefix(200))
        XCTAssertTrue(body.contains("guard isTestOutputBottomVisible else { return }"),
                      "the pane still scrolls to the bottom on every line, whatever the user is reading")
    }
}
