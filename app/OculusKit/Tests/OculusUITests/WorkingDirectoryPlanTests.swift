import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for the New Session sheet's folder check — the derivation that decides, at SELECTION time,
/// whether the picked folders can become a working directory at all.
///
/// The point of these is parity: `WorkingDirectoryPlan` re-implements the multi-repo rule that
/// daemon/hub/hub.go applies in `session.create`, so that an impossible combination is refused
/// beside the folders instead of by an alert after the prompt is written. If the two ever disagree
/// the sheet goes back to promising something the daemon then refuses, which is the exact failure
/// this replaced — so the ancestor arithmetic is pinned here case for case.
final class WorkingDirectoryPlanTests: XCTestCase {

    private func runsIn(_ plan: WorkingDirectoryPlan) -> String?? {
        if case .ok(let p) = plan { return .some(p) }
        return .none
    }

    private func isBlocked(_ plan: WorkingDirectoryPlan) -> Bool {
        if case .blocked = plan { return true }
        return false
    }

    // MARK: - commonAncestor parity with daemon/hub/hub.go

    func testAncestorOfSiblingsIsTheirParent() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor(["/Users/x/code/a", "/Users/x/code/b"]),
                       "/Users/x/code")
    }

    func testAncestorOfNestedPathIsTheShallowerOne() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor(["/Users/x/code", "/Users/x/code/a"]),
                       "/Users/x/code")
    }

    /// Different roots share only "/" — the case the daemon refuses. Go's split keeps the leading
    /// empty component, so the answer is "/" and not "": the Swift port has to agree or the sheet
    /// will let a doomed selection through.
    func testAncestorAcrossVolumesIsRoot() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor(["/Users/x/a", "/Volumes/disk/b"]), "/")
    }

    func testAncestorIgnoresTrailingSlashesAndDotSegments() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor(["/Users/x/code/a/", "/Users/x/code/./b"]),
                       "/Users/x/code")
    }

    func testAncestorOfNothingIsEmpty() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor([]), "")
    }

    /// Sibling name that merely starts with another's is not a match — a component-wise walk gets
    /// this right where a string-prefix walk would answer "/Users/x/code/a".
    func testAncestorComparesWholeComponents() {
        XCTAssertEqual(WorkingDirectoryPlan.commonAncestor(["/Users/x/code/api", "/Users/x/code/apiary"]),
                       "/Users/x/code")
    }

    // MARK: - the verdict

    func testNoSelectionRunsWhereverTheDaemonDefaults() {
        XCTAssertEqual(runsIn(.evaluate(paths: [], isolate: false, canIsolate: false)), .some(nil))
    }

    func testSingleSelectionRunsInThatFolder() {
        XCTAssertEqual(runsIn(.evaluate(paths: ["/Users/x/code/a"], isolate: false, canIsolate: true)),
                       .some("/Users/x/code/a"))
    }

    func testSharedMultiRepoRunsInTheCommonParent() {
        let plan = WorkingDirectoryPlan.evaluate(paths: ["/Users/x/code/a", "/Users/x/code/b"],
                                                 isolate: false, canIsolate: true)
        XCTAssertEqual(runsIn(plan), .some("/Users/x/code"))
    }

    /// The trap this whole check exists for: accepted at selection time before, refused by the
    /// daemon only after the user had written a prompt and pressed Start.
    func testUnrelatedFoldersAreBlockedAtSelectionTime() {
        let plan = WorkingDirectoryPlan.evaluate(paths: ["/Users/x/a", "/Volumes/disk/b"],
                                                 isolate: false, canIsolate: false)
        XCTAssertTrue(isBlocked(plan))
    }

    /// Isolation gives each repo its own worktree under a folder the daemon creates, so unrelated
    /// repos are fine — which is why it can be offered as the one-tap fix.
    func testIsolationRescuesUnrelatedRepos() {
        let plan = WorkingDirectoryPlan.evaluate(paths: ["/Users/x/a", "/Volumes/disk/b"],
                                                 isolate: true, canIsolate: true)
        XCTAssertEqual(runsIn(plan), .some(nil))
    }

    /// Whatever we say, we must not say it about the agent: the old alert read "Check the agent is
    /// installed and running" for a problem that was entirely about folders, and sent people off to
    /// debug a working opencode install.
    func testBlockedCopyBlamesTheFoldersNotTheAgent() {
        guard case .blocked(let summary, let detail, let fix) =
                WorkingDirectoryPlan.evaluate(paths: ["/Users/x/a", "/Volumes/disk/b"],
                                              isolate: false, canIsolate: false) else {
            return XCTFail("expected a blocked plan")
        }
        for line in [summary, detail, fix] {
            XCTAssertFalse(line.lowercased().contains("agent"), "copy blames the agent: \(line)")
        }
        XCTAssertTrue(detail.lowercased().contains("folder"))
    }

    /// The fix has to be reachable: only offer "isolate them" when isolation is actually available
    /// for this selection, otherwise it names a control the user cannot use.
    func testFixOffersIsolationOnlyWhenItIsAvailable() {
        guard case .blocked(_, _, let withIsolation) =
                WorkingDirectoryPlan.evaluate(paths: ["/Users/x/a", "/Volumes/disk/b"],
                                              isolate: false, canIsolate: true),
              case .blocked(_, _, let withoutIsolation) =
                WorkingDirectoryPlan.evaluate(paths: ["/Users/x/a", "/Volumes/disk/b"],
                                              isolate: false, canIsolate: false) else {
            return XCTFail("expected blocked plans")
        }
        XCTAssertTrue(withIsolation.lowercased().contains("worktree"))
        XCTAssertFalse(withoutIsolation.lowercased().contains("worktree"))
    }
}
