import XCTest
import CoreGraphics
@testable import OculusUI

/// The "am I at the bottom?" decision that gates auto-follow.
///
/// The test-output pane scrolled to the bottom on EVERY appended line, so scrolling up to read the
/// first failure was undone by the next line — on the one pane whose entire purpose is reading a
/// failure that has already scrolled past. The transcript had carried this gate for a long time;
/// the pane two views away did not.
///
/// What this covers and what it does not: the arithmetic is here and tested, including the boundary
/// and the degenerate inputs that reach it during layout. Whether SwiftUI delivers the preference
/// that feeds it is not testable in a unit test and is unchanged from the transcript's long-shipped
/// path — that half wants a human with the app open.
final class ScrollFollowTests: XCTestCase {

    func testAPaneScrolledToTheEndCountsAsAtTheBottom() {
        XCTAssertTrue(scrollBottomIsVisible(bottomY: 400, viewportHeight: 400))
        XCTAssertTrue(scrollBottomIsVisible(bottomY: 380, viewportHeight: 400))
    }

    /// The slack exists so sub-pixel layout and the last row's padding do not read as "scrolled up",
    /// which would stop a pane following its own tail while sitting at the end of it.
    func testTheSlackAbsorbsSubPixelLayout() {
        XCTAssertTrue(scrollBottomIsVisible(bottomY: 417.6, viewportHeight: 400),
                      "a pane a few points past the edge stopped following its own tail")
        XCTAssertFalse(scrollBottomIsVisible(bottomY: 419, viewportHeight: 400),
                       "the slack swallowed a real scroll-up; the user is dragged back down")
    }

    /// Scrolled genuinely up — following must stop, which is the whole point.
    func testScrolledUpDoesNotFollow() {
        XCTAssertFalse(scrollBottomIsVisible(bottomY: 2_000, viewportHeight: 400),
                       "a user reading earlier output is yanked to the bottom by the next line")
    }

    /// Layout hands these in before a pane has been measured. Answering "true" would make an
    /// unmeasured pane follow — which is how this reads as working right up until it matters.
    func testDegenerateInputsDoNotFollow() {
        XCTAssertFalse(scrollBottomIsVisible(bottomY: .infinity, viewportHeight: 400))
        XCTAssertFalse(scrollBottomIsVisible(bottomY: .nan, viewportHeight: 400))
        XCTAssertFalse(scrollBottomIsVisible(bottomY: 10, viewportHeight: 0))
    }

    /// Both panes must ask the same question, or the fix drifts back apart.
    func testBothPanesUseTheSharedPredicate() throws {
        let here = URL(fileURLWithPath: #filePath)
        let root = here.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let src = try String(contentsOf: root.appendingPathComponent("Sources/OculusUI/ChatView.swift"), encoding: .utf8)
        // "= scrollBottomIsVisible", not the bare name — the bare name also matches the declaration,
        // which sits in this same file and would make the count read 3 for two call sites.
        let uses = src.components(separatedBy: "= scrollBottomIsVisible(bottomY:").count - 1
        XCTAssertEqual(uses, 2, "the transcript and the test-output pane must share one predicate; "
                       + "two hand-written copies is how they came to disagree in the first place")
        XCTAssertTrue(src.contains("guard isTestOutputBottomVisible else { return }"),
                      "the test-output pane no longer gates its auto-follow at all")
    }
}
