import XCTest
@testable import OculusUI

/// The app once hung forever on its launch spinner because `bootstrap()` read the keychain
/// synchronously on the main actor as its first act. `SecItemCopyMatching` does not merely take a
/// while — it blocks INDEFINITELY waiting for user authorisation when the calling binary's code
/// identity has changed (a re-signed build, a restored or migrated Mac, a locked keychain). Until
/// somebody answers that prompt the call never returns, so `didBootstrap` never flipped, no error
/// was shown, and there was no way forward.
///
/// Sampling a hung build put the entire main thread in:
///   DesktopStore.bootstrap() → loadDesktops() → Keychain.get → SecItemCopyMatching
///
/// WHAT THESE TESTS CAN AND CANNOT REACH. The hang itself is not reproducible in-process: it needs
/// the OS to raise an authorisation prompt that nobody answers, and no API asks it to. So there is
/// no honest test here for "a blocked read returns within the budget" — an earlier version of this
/// file faked one with two sleeping tasks, which only proved that `Task.sleep` works. That is
/// deleted rather than kept as decoration.
///
/// What IS reachable is the classification that the dangerous paths branch on, and that is the part
/// that actually prevents data loss: `missing` and `timedOut` must never collapse together, because
/// `clientKey()` mints a new X25519 identity on `missing` and would otherwise overwrite this
/// device's real key the first time the keychain stalled.
final class KeychainBudgetTests: XCTestCase {

    /// An account that was never stored must report `missing` — the case that licenses minting a
    /// fresh device key — and must not be confused with a stall.
    func testAbsentAccountReportsMissingNotTimedOut() {
        let account = "test.absent.\(UUID().uuidString)"
        switch Keychain.read(account) {
        case .missing:
            break // expected
        case .found(let s):
            XCTFail("a never-stored account returned a value: \(s)")
        case .timedOut:
            XCTFail("an absent account was reported as a stall — this is the conflation that makes "
                    + "clientKey() overwrite a live device identity")
        }
    }

    /// A healthy keychain answers immediately. If this ever starts costing the full budget, the read
    /// is stalling on every launch and the app is three seconds slower to appear.
    func testAbsentAccountAnswersWellInsideTheBudget() {
        let start = ContinuousClock.now
        _ = Keychain.read("test.absent.\(UUID().uuidString)")
        let elapsed = ContinuousClock.now - start
        XCTAssertLessThan(elapsed, .seconds(1),
                          "a routine keychain miss took \(elapsed) — reads are stalling")
    }

    /// `get` is the lossy convenience wrapper, and the loss must be limited to callers that cannot
    /// act on the difference. Anything that writes in response to a nil has to use `read`.
    func testGetCollapsesMissingToNil() {
        XCTAssertNil(Keychain.get("test.absent.\(UUID().uuidString)"))
    }
}
