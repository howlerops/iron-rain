import XCTest
@testable import OculusUI
@testable import OculusKit

/// A refusal must never render as an empty state.
///
/// An empty state is a claim about the world — "you have no activity", "no loops yet", "connect a
/// tracker" — and the app was making all three about a Mac it had just been told it may not look at.
/// The `*Forbidden` flag pattern already existed for Devices, Accounts, Remotes and MCP; these are
/// the three screens a guest actually LANDS on, which is what made them the worse half. Activity is
/// the default iOS destination, so its version of this was the first thing a guest ever saw.
final class GuestEmptyStateTests: XCTestCase {

    private func error(code: String?) -> NSError {
        var info: [String: Any] = [NSLocalizedDescriptionKey: "Only the owner can do that."]
        if let code { info[OculusError.codeKey] = code }
        return NSError(domain: "Oculus", code: -2, userInfo: info)
    }

    /// Locates a source file from the test's own path, so this works wherever the package is checked
    /// out. The Go side does the same in capability_census_test.go.
    private func sourceOf(_ relative: String) throws -> String {
        let here = URL(fileURLWithPath: #filePath)
        let packageRoot = here.deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        return try String(contentsOf: packageRoot.appendingPathComponent(relative), encoding: .utf8)
    }

    @MainActor
    func testNothingClaimsARefusalBeforeOneHappens() {
        let m = Model()
        XCTAssertFalse(m.activityForbidden)
        XCTAssertFalse(m.loopsForbidden)
        XCTAssertFalse(m.issuesForbidden)
    }

    /// Each screen must branch on its flag BEFORE its empty state, and the branch has to be in the
    /// view — a flag nothing reads is decoration.
    ///
    /// Asserted against the views' own source for the reason DevicesScreenTests gives: a predicate
    /// written here would only agree with itself, and SwiftUI bodies cannot be evaluated in a unit
    /// test. What is checked is the ORDER of the two branches, which is the whole defect.
    func testEachScreenChecksTheRefusalBeforeItsEmptyState() throws {
        // The BRANCH, not the first mention of each name: both markers appear elsewhere in these
        // files (LoopsView asks `model.loops.isEmpty` to choose a list shape; IssuesView names
        // connectScreen in a comment), so a first-occurrence ordering check compares the wrong pair
        // and fails on correct code. What is asserted is the structure: the refusal branch opens, and
        // the empty state is its `else if`.
        for (file, opener, elseBranch) in [
            ("Sources/OculusUI/ActivityView.swift",
             "if model.activityForbidden {", "} else if model.activityFeed.isEmpty {"),
            ("Sources/OculusUI/LoopsView.swift",
             "if model.loopsForbidden {", "} else if model.loops.isEmpty {"),
            // Issues guards the CONNECT screen rather than an empty list — same defect, different
            // shape: "Connect a tracker" invites a guest to do a thing that will also be refused.
            ("Sources/OculusUI/IssuesView.swift",
             "if model.issuesForbidden {", "} else if model.connectedTrackers.isEmpty"),
        ] {
            let src = try sourceOf(file)
            guard let opensAt = src.range(of: opener) else {
                XCTFail("\(file) has no \(opener) branch — a refusal still renders as \"you have none\"")
                continue
            }
            let after = String(src[opensAt.upperBound...].prefix(1400))
            XCTAssertTrue(after.contains(elseBranch),
                          "\(file): \(opener) does not guard \(elseBranch). A guest is told the Mac "
                          + "has nothing, rather than that they are not allowed to see it.")
        }
    }

    /// The flags must be set by a REFUSAL and not by an ordinary failure, or the screens start
    /// accusing the owner of being a guest during a routine reconnect.
    @MainActor
    func testOnlyARefusalSetsTheFlags() {
        let m = Model()

        m.activityForbidden = OculusError.isForbidden(error(code: OculusError.forbidden))
        m.loopsForbidden = OculusError.isForbidden(error(code: OculusError.forbidden))
        m.issuesForbidden = OculusError.isForbidden(error(code: OculusError.forbidden))
        XCTAssertTrue(m.activityForbidden && m.loopsForbidden && m.issuesForbidden,
                      "a real refusal was not recognised")

        let dropped = NSError(domain: "Oculus", code: -1,
                              userInfo: [NSLocalizedDescriptionKey: "not connected"])
        m.activityForbidden = OculusError.isForbidden(dropped)
        m.loopsForbidden = OculusError.isForbidden(dropped)
        m.issuesForbidden = OculusError.isForbidden(dropped)
        XCTAssertFalse(m.activityForbidden || m.loopsForbidden || m.issuesForbidden,
                       "a dropped socket was classified as a refusal")
    }

    /// loadIssues has to be a REQUEST, not a fire-and-forget send.
    ///
    /// It used to encode a message and push it at the socket, throwing the reply away — so a refusal
    /// had nowhere to land however carefully the screen branched. issue.list does reply (as well as
    /// broadcasting), which is what makes the flag reachable at all.
    func testLoadIssuesAwaitsTheReply() throws {
        let src = try sourceOf("Sources/OculusUI/OculusUI.swift")
        guard let fn = src.range(of: "public func loadIssues() async {") else {
            XCTFail("loadIssues is gone")
            return
        }
        let body = String(src[fn.upperBound...].prefix(900))
        XCTAssertTrue(body.contains("try await request(MessageType.issueList"),
                      "loadIssues does not await a reply, so `issue.list` being refused is invisible "
                      + "to the client no matter what the view does with issuesForbidden")
        XCTAssertTrue(body.contains("issuesForbidden = OculusError.isForbidden"),
                      "loadIssues does not classify its failure")
    }
}
