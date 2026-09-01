import XCTest
@testable import OculusUI
@testable import OculusKit

/// A sub-agent lane's report must not render twice when the daemon replays the parent's history.
///
/// The daemon rings BOTH halves of a lane's output: the deltas it broadcast live, and (at turn end)
/// one synthetic finalized message per lane, recordOnly()'d so live watchers don't see it but a later
/// attach can still read the report. A replay therefore delivers the lane's text twice — once as
/// deltas, once whole — and the child branch of the sessionMessage handler appends unconditionally.
@MainActor
final class SubAgentLaneReplayTests: XCTestCase {

    private func apply(_ m: Model, _ json: String) {
        let raw = Data(json.utf8)
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame: \(json)") }
        m.applyEvent(env, raw: raw)
    }

    func testLaneReportIsNotDuplicatedByTheSyntheticFinalizedMessage() {
        let m = Model()
        m.sessionID = "parent"
        // The replay, in ring order.
        apply(m, #"{"type":"session.subagent","payload":{"parent_id":"parent","id":"kid","title":"Review","status":"started"}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"kid","text":"Found a data race "}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"kid","text":"on the cache map."}}"#)
        // finalizeTurnTranscript's per-lane synthetic message (daemon/hub/session.go:1085).
        apply(m, #"{"type":"session.message","payload":{"session_id":"kid","role":"assistant","text":"Found a data race on the cache map."}}"#)

        let lane = m.childMessages["kid"] ?? []
        let texts = lane.filter { $0.role == .assistant }.map(\.text)
        XCTAssertEqual(texts, ["Found a data race on the cache map."],
                       "the lane rendered its report twice: \(texts)")
    }
}
