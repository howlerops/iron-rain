import XCTest
@testable import OculusUI
@testable import OculusKit

/// The client's component recognizer must accept exactly what the daemon's does.
///
/// Two independent recognizers with different rules is not redundancy, it is a disagreement. The
/// daemon's (daemon/genui) is deliberately strict — closed catalog, size and shape caps — and it
/// forwards everything it refuses as ordinary text, on the stated principle that an over-cap payload
/// is "meant to be dropped SILENTLY and left as plain text". The client's accepted ANY object with
/// component/id/props: no catalog, no caps, no size limit. So a payload the daemon had judged unsafe
/// and deliberately passed through as prose was picked up here and rendered as a live card, and the
/// closed-catalog safety model — the thing that guarantees a model can only emit UI the client has
/// vetted — did not hold.
///
/// Every case below is asserted with the SAME payload and the SAME expected verdict in
/// daemon/genui/catalog_agreement_test.go. If you change one, change both.
@MainActor
final class CatalogAgreementTests: XCTestCase {

    private func rendersAsComponent(_ payload: String) -> Bool {
        // A bare one-line component JSON is the daemon's lenient catch, and the client's inline path.
        let segments = AssistantContentParser.parse(payload, sessionID: "s", messageID: "m")
        return segments.contains { if case .component = $0.kind { return true } else { return false } }
    }

    func testClientAcceptsExactlyWhatTheDaemonAccepts() {
        for c in catalogAgreementCases {
            let got = rendersAsComponent(c.payload)
            XCTAssertEqual(got, c.accepted,
                           "\(c.name): client \(got ? "rendered a card" : "left it as prose"), "
                           + "daemon \(c.accepted ? "builds a component" : "forwards it as text")")
        }
    }
}

struct CatalogCase {
    let name: String
    let accepted: Bool
    let payload: String
}

/// Shared with daemon/genui/catalog_agreement_test.go — same payloads, same verdicts.
let catalogAgreementCases: [CatalogCase] = [
    .init(name: "a valid table", accepted: true,
          payload: #"{"component":"table","id":"t1","props":{"columns":["a"],"rows":[["1"]]}}"#),
    .init(name: "a valid callout (no validator)", accepted: true,
          payload: #"{"component":"callout","id":"c1","props":{"body":"hi"}}"#),
    .init(name: "a component outside the catalog", accepted: false,
          payload: #"{"component":"iframe","id":"x1","props":{"src":"http://evil"}}"#),
    .init(name: "an empty id", accepted: false,
          payload: #"{"component":"table","id":"","props":{"columns":["a"],"rows":[["1"]]}}"#),
    .init(name: "a table over the column cap", accepted: false,
          payload: overCapTable()),
    .init(name: "a choice over the option cap", accepted: false,
          payload: overCapChoice()),
    .init(name: "a form with an invented field type", accepted: false,
          payload: #"{"component":"form","id":"f1","props":{"fields":[{"id":"a","type":"password"}]}}"#),
    .init(name: "a form with no fields", accepted: false,
          payload: #"{"component":"form","id":"f2","props":{"fields":[]}}"#),
    .init(name: "a valid form", accepted: true,
          payload: #"{"component":"form","id":"f3","props":{"fields":[{"id":"a","type":"text"}]}}"#),
]

private func overCapTable() -> String {
    let cols = (0..<21).map { "\"c\($0)\"" }.joined(separator: ",")
    return #"{"component":"table","id":"t2","props":{"columns":[\#(cols)],"rows":[]}}"#
}

private func overCapChoice() -> String {
    let opts = (0..<51).map { "\"o\($0)\"" }.joined(separator: ",")
    return #"{"component":"choice","id":"ch1","props":{"options":[\#(opts)]}}"#
}
