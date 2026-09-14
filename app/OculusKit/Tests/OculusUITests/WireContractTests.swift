import XCTest
@testable import OculusKit

/// The daemon omits a key; the client decodes it as required; the WHOLE message is discarded.
///
/// This is the most expensive shape of bug in this protocol and the least visible. A Codable
/// property declared non-optional throws `keyNotFound` when its key is absent, and that throw
/// unwinds the entire decode — not the one field. The screen then renders its empty state, which
/// looks exactly like "there is nothing here" rather than "the reply was thrown away". Nothing is
/// logged, because nothing failed as far as any layer can tell.
///
/// The mirror image is just as quiet: an explicit `CodingKeys` enum that omits an optional property
/// compiles without complaint and simply never reads that key. The value arrives on the wire and is
/// silently dropped.
///
/// Both of these shipped. The fixtures below are the daemon's real output — Go `omitempty` means an
/// empty string, a false bool and a zero int are all ABSENT, not null.
final class WireContractTests: XCTestCase {

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try ProtocolCoding.decoder().decode(type, from: Data(json.utf8))
    }

    /// Linear attachments never carry a MIME type, and a non-image Jira attachment has `is_image`
    /// false — so Go drops both keys. Decoding them as required threw, and the ticket panel rendered
    /// permanently empty for any ticket with an attachment. Linear's GitHub and Slack apps add those
    /// automatically, so that is most active tickets.
    func testAnIssueWithAttachmentsStillDecodes() throws {
        let detail = try decode(IssueDetail.self, """
        {"issue":{"id":"ENG-7","key":"ENG-7","title":"Fix login","provider":"linear","status":"In Progress","category":"in_progress"},
         "comments":[],
         "attachments":[
           {"id":"a1","filename":"build.log","url":"https://x/1"},
           {"id":"a2","filename":"shot.png","url":"https://x/2","mime":"image/png","is_image":true,"size":4096}
         ]}
        """)

        let atts = try XCTUnwrap(detail.attachments)
        XCTAssertEqual(atts.count, 2, "the attachment list was lost")
        XCTAssertNil(atts[0].mime, "a Linear attachment has no MIME type and must not require one")
        XCTAssertFalse(atts[0].showsInline, "a log file rendered as an inline image")
        XCTAssertTrue(atts[1].showsInline)
        XCTAssertEqual(atts[1].size, 4096)
    }

    /// And the whole reply, not just the attachment array: the throw propagates, so the failure is
    /// an empty ticket panel rather than a ticket missing its attachments.
    func testTheThrowWouldTakeTheWholeTicketWithIt() throws {
        let detail = try decode(IssueDetail.self, """
        {"issue":{"id":"ENG-7","key":"ENG-7","title":"Fix login","provider":"linear","status":"In Progress","category":"in_progress"},
         "comments":[{"id":"c1","author":"Sam","body":"looks right to me","createdAt":"2026-09-01"}],
         "attachments":[{"id":"a1","filename":"build.log","url":"https://x/1"}]}
        """)
        XCTAssertEqual(detail.issue.title, "Fix login",
                       "the ticket itself is gone — an attachment with no MIME type discarded the reply")
        XCTAssertEqual(detail.comments.count, 1, "the comments went with it")
    }

    /// A session's real spend arrives and is dropped, because Session's explicit CodingKeys enum
    /// never listed these two. The toolbar then reports "cost not reported by this provider" for a
    /// session that has spent money — on every reload, for every provider.
    func testASessionsSpendSurvivesAReload() throws {
        let s = try decode(Session.self, """
        {"id":"ses_1","provider":"opencode","status":"idle","cwd":"/tmp",
         "input_tokens":41000,"output_tokens":3800,"cost_usd":4.2,
         "cost_known":true,"context_tokens":79300}
        """)
        XCTAssertEqual(s.costUSD, 4.2)
        XCTAssertEqual(s.costKnown, true,
                       "cost_known was on the wire and never read — the toolbar shows \"—\" and the "
                       + "tooltip blames the provider for a number the daemon sent")
        XCTAssertEqual(s.contextTokens, 79300,
                       "context_tokens was on the wire and never read")
    }

    /// The absent case must stay distinguishable from the false case: a provider that genuinely
    /// reports no cost is not the same as a daemon that never mentioned it.
    func testAProviderThatReportsNoCostIsStillDistinguishable() throws {
        let s = try decode(Session.self, #"{"id":"ses_2","provider":"pi","status":"idle","cwd":"/tmp"}"#)
        XCTAssertNil(s.costKnown, "an omitted cost_known must read as unknown, not as a known zero")
        XCTAssertNil(s.contextTokens)
    }
}
