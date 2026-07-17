import XCTest
@testable import OculusKit

/// Regression tests for the pure-data invariants the OculusUI view fixes rely on.
///
/// The OculusUI target (where the SwiftUI views live) is not a dependency of this
/// test target, so the views can't be imported directly. These tests instead lock
/// the semantics of the standard-library operations the refactors substituted in —
/// so a future change that breaks those semantics is caught here.
final class ViewModelInvariantsTests: XCTestCase {

    private func issue(_ id: String, category: String) -> Issue {
        Issue(id: id, key: "K-\(id)", title: "t\(id)", body: nil, status: category,
              category: category, assignee: nil, url: nil, provider: "linear",
              branchName: nil, teamID: nil, priority: nil, updatedAt: nil)
    }

    // IssuesView.board: grouping once must yield exactly what per-category filtering did.
    func testGroupingByCategoryMatchesFilter() {
        let issues = [
            issue("1", category: "todo"),
            issue("2", category: "in_progress"),
            issue("3", category: "todo"),
            issue("4", category: "done"),
            issue("5", category: "in_progress"),
            issue("6", category: "todo"),
        ]
        let grouped = Dictionary(grouping: issues, by: { $0.category })

        for category in ["todo", "in_progress", "done", "other"] {
            let fromGroup = grouped[category] ?? []
            let fromFilter = issues.filter { $0.category == category }
            XCTAssertEqual(fromGroup, fromFilter,
                           "grouping[\(category)] must equal filter(category == \(category))")
        }
        XCTAssertEqual((grouped["todo"] ?? []).count, 3)
        XCTAssertEqual((grouped["in_progress"] ?? []).count, 2)
        XCTAssertEqual((grouped["done"] ?? []).count, 1)
        XCTAssertNil(grouped["other"]) // absent category -> nil -> [] fallback
    }

    // Composer chips: removing an attachment by value must delete only the intended
    // item regardless of position, unlike remove(at: staleIndex).
    func testAttachmentRemovalByValue() {
        let a = ImageAttachment(mime: "image/jpeg", data: "AAAA")
        let b = ImageAttachment(mime: "image/jpeg", data: "BBBB")
        let c = ImageAttachment(mime: "image/jpeg", data: "CCCC")
        var pending = [a, b, c]

        pending.removeAll { $0 == b }
        XCTAssertEqual(pending, [a, c])

        pending.removeAll { $0 == a }
        XCTAssertEqual(pending, [c])
    }

    // SessionSidebar.sessionGroups: the [projectID: name] lookup must resolve the
    // same names the old per-group `projects.first { $0.id == pid }` scan produced.
    func testProjectNameLookupMatchesLinearScan() {
        let projects = [
            Project(id: "p1", name: "Alpha", path: "/a", isGitRepo: true, defaultBranch: nil, source: nil),
            Project(id: "p2", name: "Beta", path: "/b", isGitRepo: true, defaultBranch: nil, source: nil),
        ]
        let lookup = Dictionary(projects.map { ($0.id, $0.name) },
                                uniquingKeysWith: { first, _ in first })

        for pid in ["p1", "p2", "missing", ""] {
            let fromLookup = lookup[pid]
            let fromScan = projects.first { $0.id == pid }?.name
            XCTAssertEqual(fromLookup, fromScan, "name lookup must match linear scan for \(pid)")
        }
    }
}
