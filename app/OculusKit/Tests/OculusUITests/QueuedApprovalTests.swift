import XCTest
@testable import OculusUI
@testable import OculusKit

/// A queued approval answer must land ONLY on the request it was given for.
///
/// Answering an approval is a privilege grant — it lets an agent run a command on the user's Mac —
/// so a stale one is not a cosmetic bug. The push-notification action (the most common way an
/// approval is actually answered on a phone) assigned `pendingDecision` directly, which meant two
/// things at once: the answer was untargeted, and unlike the Siri/Shortcuts path it never expired.
///
/// The sequence that bites: tap Allow on the Lock Screen while the app is not connected; that same
/// approval gets resolved some other way (at the Mac, by a standing rule, or the turn errors out);
/// hours later a different session raises a different tool call; you open the app for an unrelated
/// reason, and the connect drains the stale "allow" onto a request you never saw.
@MainActor
final class QueuedApprovalTests: XCTestCase {

    override func setUp() {
        super.setUp()
        OculusStore.shared.pendingDecision = nil
    }

    func testPushDecisionCarriesTheApprovalItWasGivenFor() {
        OculusStore.shared.queueDecision(Decision.allow, approvalID: "appr_1")
        XCTAssertEqual(OculusStore.shared.pendingDecision?.approvalID, "appr_1",
                       "the daemon sends approval_id on every approval push; dropping it makes the answer untargeted")
        XCTAssertEqual(OculusStore.shared.pendingDecision?.decision, Decision.allow)
    }

    /// Siri and Shortcuts genuinely cannot know which request is open, so they queue without an id
    /// and rely on the expiry. That path must keep working.
    func testIntentDecisionWithoutAnIDIsStillAllowed() {
        OculusStore.shared.queueDecision(Decision.deny)
        XCTAssertNil(OculusStore.shared.pendingDecision?.approvalID)
        XCTAssertEqual(OculusStore.shared.pendingDecision?.decision, Decision.deny)
    }

    /// The guard itself: an answer given for one approval must not apply to another.
    func testADecisionForAnotherApprovalDoesNotMatchTheOpenOne() {
        let open = ApprovalRequest(approvalID: "appr_2", sessionID: "s1", tool: "bash")
        OculusStore.shared.queueDecision(Decision.allow, approvalID: "appr_1")

        guard let queued = OculusStore.shared.pendingDecision else {
            return XCTFail("nothing queued")
        }
        let applies = queued.approvalID == nil || queued.approvalID == open.approvalID
        XCTAssertFalse(applies,
                       "an allow given for appr_1 was applied to appr_2 — a command the user never saw")
    }

    func testADecisionForTheOpenApprovalDoesApply() {
        let open = ApprovalRequest(approvalID: "appr_1", sessionID: "s1", tool: "bash")
        OculusStore.shared.queueDecision(Decision.allow, approvalID: "appr_1")
        guard let queued = OculusStore.shared.pendingDecision else {
            return XCTFail("nothing queued")
        }
        let applies = queued.approvalID == nil || queued.approvalID == open.approvalID
        XCTAssertTrue(applies, "the answer was given for exactly this request and must still work")
    }

    /// Every producer must go through queueDecision, which is what arms the expiry. A path that
    /// assigns the property directly gets no expiry and stays armed indefinitely.
    func testQueuedDecisionExpires() async throws {
        OculusStore.shared.queueDecision(Decision.allow, approvalID: "appr_1", expiresIn: 1)
        XCTAssertNotNil(OculusStore.shared.pendingDecision)
        try await Task.sleep(nanoseconds: 1_400_000_000)
        XCTAssertNil(OculusStore.shared.pendingDecision,
                     "an unconsumed answer must be discarded, not left armed for the next request")
    }
}
