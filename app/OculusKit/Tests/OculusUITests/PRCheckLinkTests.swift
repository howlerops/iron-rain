import XCTest
@testable import OculusKit

/// CI went red and the phone could show you the failing check NAMES and nothing else.
///
/// Every failing check already arrives from GitHub carrying a link to its own log — detailsUrl on a
/// CheckRun, targetUrl on the older StatusContext — and the daemon dropped both while parsing. So
/// the one screen someone reviews a worktree from could tell them a build was broken and then offer
/// nothing whatsoever to do about it until they got back to a desk.
///
/// The wire shape is additive on purpose: `failing` keeps its old name-only form so an app built
/// before this decodes the message at all, and `failing_checks` carries the links beside it.
final class PRCheckLinkTests: XCTestCase {

    private func decode(_ json: String) throws -> PRChecks {
        try ProtocolCoding.decoder().decode(PRChecks.self, from: Data(json.utf8))
    }

    func testLinkedFailuresDecodeAndAreUsed() throws {
        let c = try decode("""
        {"state":"FAILURE","passed":2,"failed":2,
         "failing":["test (macos)","license/cla"],
         "failing_checks":[{"name":"test (macos)","url":"https://ci/1"},
                           {"name":"license/cla","url":"https://ci/2"}]}
        """)
        XCTAssertEqual(c.failures.map(\.name), ["test (macos)", "license/cla"])
        XCTAssertEqual(c.failures.first?.link?.absoluteString, "https://ci/1",
                       "the failure renders without a link and the phone still cannot say why CI broke")
    }

    /// An OLDER daemon sends names only. The screen must still list them — without links, but
    /// listed — rather than rendering an empty failure list under a red badge.
    func testAnOlderDaemonsNameOnlyReplyStillLists() throws {
        let c = try decode(#"{"state":"FAILURE","failed":1,"failing":["build"]}"#)
        XCTAssertEqual(c.failures.map(\.name), ["build"],
                       "a red build would show no failing checks at all against an older daemon")
        XCTAssertNil(c.failures.first?.link, "there is no link to offer; the row must not fake one")
    }

    /// A check whose provider gave no URL must not render a dead link.
    func testAFailureWithNoURLHasNoLink() throws {
        let c = try decode("""
        {"state":"FAILURE","failed":1,"failing":["flaky"],
         "failing_checks":[{"name":"flaky"}]}
        """)
        XCTAssertEqual(c.failures.count, 1)
        XCTAssertNil(c.failures.first?.link)
    }

    /// Two CI apps can report a check with the same name. They must stay two rows, or one of two
    /// real failures silently disappears from the list.
    func testSameNamedFailuresFromDifferentProvidersStayDistinct() throws {
        let c = try decode("""
        {"state":"FAILURE","failed":2,
         "failing_checks":[{"name":"test","url":"https://a/1"},{"name":"test","url":"https://b/1"}]}
        """)
        XCTAssertEqual(Set(c.failures.map(\.id)).count, 2, "the two failures collapsed into one row")
    }

    /// The count line reads "+N more failing" off this, so it has to count what is actually shown.
    func testTheCappedRemainderCountsWhatIsRendered() throws {
        let c = try decode("""
        {"state":"FAILURE","failed":7,
         "failing_checks":[{"name":"f1"},{"name":"f2"},{"name":"f3"},{"name":"f4"},{"name":"f5"}]}
        """)
        XCTAssertEqual(c.failedCount - c.failures.count, 2)
    }
}
