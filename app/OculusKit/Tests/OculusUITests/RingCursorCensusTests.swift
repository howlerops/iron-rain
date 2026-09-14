import XCTest
@testable import OculusUI
@testable import OculusKit

/// Every hub-wide broadcast must make a DELIBERATE decision about the paging cursor.
///
/// `daemonEventsRendered` is the cursor "Show earlier messages" sends as `loaded`, and the daemon
/// computes `end := len(ring) - loaded` against a session's replayable ring. Counting a frame the
/// ring never held inflates that cursor, so the page comes back starting further back than where the
/// client's transcript actually ends — a HOLE — and once the inflation passes the ring's length,
/// `end` hits zero and the live window is skipped entirely.
///
/// The client decides by frame TYPE, and the list of types to skip has now been incomplete twice:
/// the third sweep added session.status and session.facts, the fourth found session.heartbeat,
/// activity.event and worktree.status still missing. session.heartbeat fires about every ten seconds
/// for the whole life of a session, so that one inflates without bound on any session left open.
///
/// Finding them one at a time does not converge. This reads the DAEMON's own source — the same
/// technique daemon/hub/capability_census_test.go uses for capabilities — and requires every
/// hub-wide broadcast type either to be listed as non-ring or to be declared here as carrying no
/// session id. A new one is then a compile-time-visible decision rather than a silent hole.
final class RingCursorCensusTests: XCTestCase {

    /// Hub-wide broadcast types whose payload carries NO session_id, so they can never match the
    /// counting predicate however they are delivered. Each is a decision, not an oversight.
    private let carriesNoSessionID: Set<String> = [
        "TypeSessionList", "TypeProviderList", "TypeProjectList", "TypeDeviceList",
        "TypeIssueList", "TypeIssueProjects", "TypeIntegrationStatus", "TypeLoopList",
        "TypeMCPList", "TypeAccountList", "TypeRemoteList", "TypeInviteList",
        "TypeFanoutSummary", "TypeNotifyPrefs", "TypeRoleState", "TypeApprovalRules",
        "TypeUsageReport", "TypeTelemetryStatus", "TypeHandoffList", "TypeLogLine",
        "TypeWorkspaceList", "TypeDiscoverList", "TypeAgentList", "TypeSessionDefaults",
        "TypePairStatus", "TypeCommandList", "TypeModelList", "TypeCheckpointList",
        // Checked against their payload structs in daemon/protocol/protocol.go, because the census
        // flagged all four the first time it ran and a decision made from the name alone is a guess:
        //   ApprovalResolved {approval_id, decision}      — keyed by approval, not session
        //   FSChange         {path, sha}                  — keyed by path
        //   LSPDiagnostics   {path, diagnostics}          — keyed by path
        //   ApprovalRulesChanged                          — a rules list, not session-scoped
        // None decode a session_id, so none can match the counting predicate however delivered.
        "TypeApprovalResolved", "TypeFSChange", "TypeLSPDiagnostics", "TypeApprovalRulesChanged",
    ]

    private func daemonSource(_ relative: String) throws -> String {
        // …/app/OculusKit/Tests/OculusUITests/<this file>
        let here = URL(fileURLWithPath: #filePath)
        let repoRoot = here.deletingLastPathComponent() // OculusUITests
            .deletingLastPathComponent()               // Tests
            .deletingLastPathComponent()               // OculusKit
            .deletingLastPathComponent()               // app
            .deletingLastPathComponent()               // repo root
        return try String(contentsOf: repoRoot.appendingPathComponent(relative), encoding: .utf8)
    }

    /// Maps a Go constant name (TypeSessionHeartbeat) to its wire string ("session.heartbeat").
    private func wireNames() throws -> [String: String] {
        let src = try daemonSource("daemon/protocol/protocol.go")
        var out: [String: String] = [:]
        for line in src.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.hasPrefix("Type"), let eq = t.range(of: " = \"") else { continue }
            let name = String(t[..<t.range(of: " ")!.lowerBound])
            let rest = t[eq.upperBound...]
            guard let end = rest.firstIndex(of: "\"") else { continue }
            out[name] = String(rest[..<end])
        }
        return out
    }

    func testEveryHubWideBroadcastTypeIsAccountedFor() throws {
        let names = try wireNames()
        XCTAssertFalse(names.isEmpty, "could not read the daemon's message types — the census is blind")

        var broadcast: Set<String> = []
        for file in ["hub.go", "heartbeat.go", "turn.go", "prchecks.go", "session.go", "fanout_summary.go",
                     "loops.go", "issues.go", "activity.go", "worktree.go", "devices.go", "invites.go"] {
            guard let src = try? daemonSource("daemon/hub/\(file)") else { continue }
            for marker in ["h.broadcast(protocol.", "h.broadcastWithCapability(protocol.",
                           "hub.broadcast(protocol.", "m.hub.broadcast(protocol."] {
                var search = src.startIndex..<src.endIndex
                while let hit = src.range(of: marker, range: search) {
                    let rest = src[hit.upperBound...]
                    let ident = rest.prefix { $0.isLetter || $0.isNumber }
                    if ident.hasPrefix("Type") { broadcast.insert(String(ident)) }
                    search = hit.upperBound..<src.endIndex
                }
            }
        }
        XCTAssertFalse(broadcast.isEmpty, "found no hub-wide broadcasts — the scan is not working")

        var undeclared: [String] = []
        for goName in broadcast.sorted() {
            if carriesNoSessionID.contains(goName) { continue }
            guard let wire = names[goName] else {
                // A type the client has no name for cannot be counted by the client either.
                continue
            }
            if !Model.nonRingFrameTypes.contains(wire) { undeclared.append("\(goName) (\(wire))") }
        }

        XCTAssertTrue(undeclared.isEmpty,
                      "these types are broadcast hub-wide — so they are in NO session's ring — but the "
                      + "client still counts them toward the paging cursor: \(undeclared.joined(separator: ", "))"
                      + ".\n\nEach one inflates `loaded`, so \"Show earlier messages\" returns a page "
                      + "starting before where the transcript actually ends (a hole), and once the "
                      + "inflation exceeds the ring length the live window is skipped entirely. Add it "
                      + "to nonRingFrameTypes, or to carriesNoSessionID here with the reason.")
    }

    /// The three the fourth sweep found, named explicitly: a census that silently stopped scanning
    /// would otherwise pass while the regression came back.
    func testTheKnownOffendersAreListed() {
        for t in [MessageType.sessionHeartbeat, MessageType.activityEvent, MessageType.worktreeStatus,
                  MessageType.sessionStatus, MessageType.sessionFacts] {
            XCTAssertTrue(Model.nonRingFrameTypes.contains(t),
                          "\(t) is broadcast outside every session ring and must not advance the cursor")
        }
    }
}
