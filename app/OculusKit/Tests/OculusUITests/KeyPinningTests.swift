import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for the pin on the daemon's identity key.
///
/// The channel is derived from `daemonPubHex`, so a substituted daemon key fails closed — an attacker
/// cannot open the sealed proof or forge the verdict. That guarantee is worth exactly as much as the
/// pin's stability, and the pin used to be replaced by any scanned QR with no comparison and no
/// prompt. Every assertion below defends the same property: a key that CHANGES cannot take effect
/// without the user deliberately accepting it.
///
/// The three cases the fix has to keep straight, all covered here: first pairing is frictionless
/// (nothing to compare against), same-key re-pair is silent (the common case), changed key stops.
@MainActor
final class KeyPinningTests: XCTestCase {

    private let keyA = String(repeating: "a1b2", count: 16) // 64 hex chars
    private let keyB = String(repeating: "c3d4", count: 16)

    /// A model with no pin, isolated from whatever the shared UserDefaults happens to hold.
    private func unpairedModel() -> Model {
        let m = Model()
        m.daemonPubHex = ""
        m.wsURL = ""
        m.secret = ""
        return m
    }

    // MARK: - Model (single pairing / iOS path)

    /// Trust on first use: there is no pin to compare against, so pairing must not ask anything.
    func testFirstPairingIsFrictionless() {
        let m = unpairedModel()
        XCTAssertTrue(m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1"))
        XCTAssertNil(m.pendingKeyChange, "a first pairing has nothing to confirm against")
        XCTAssertEqual(m.daemonPubHex, keyA)
    }

    /// The common case — a fresh pairing code for the same Mac — must stay silent. A prompt here
    /// would fire constantly and teach the user to dismiss it, which is how the prompt that matters
    /// stops working.
    func testSameKeyRepairIsSilent() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1")

        XCTAssertTrue(m.applyPairing(url: "ws://10.0.0.5:6000/ws", pub: keyA, secret: "pc_2"))
        XCTAssertNil(m.pendingKeyChange)
        XCTAssertEqual(m.secret, "pc_2", "a same-key re-pair still refreshes the credential")
        XCTAssertEqual(m.wsURL, "ws://10.0.0.5:6000/ws", "and the address")
    }

    /// The attack: break the connection, offer a fresh QR, become the Mac. The pin must survive a
    /// scan the user has not accepted.
    func testChangedKeyIsStagedAndDoesNotOverwriteThePin() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1")

        XCTAssertFalse(m.applyPairing(url: "ws://evil.example/ws", pub: keyB, secret: "pc_evil"))
        XCTAssertEqual(m.daemonPubHex, keyA, "the pin must NOT move until the user accepts it")
        XCTAssertEqual(m.secret, "pc_1", "and neither must the credential")
        XCTAssertEqual(m.wsURL, "ws://mac.local:6000/ws", "nor the address we dial")

        XCTAssertEqual(m.pendingKeyChange?.currentPub, keyA)
        XCTAssertEqual(m.pendingKeyChange?.newPub, keyB)
    }

    /// Declining leaves everything as it was — including the staged change, which must not linger and
    /// re-fire later.
    func testCancellingAKeyChangeKeepsTheSavedKey() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1")
        m.applyPairing(url: "ws://evil.example/ws", pub: keyB, secret: "pc_evil")

        m.cancelKeyChange()
        XCTAssertNil(m.pendingKeyChange)
        XCTAssertEqual(m.daemonPubHex, keyA)
        XCTAssertEqual(m.secret, "pc_1")
    }

    /// Confirming is the ONLY way the pin moves.
    func testConfirmingAKeyChangeRepins() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1")
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyB, secret: "pc_2")

        m.confirmKeyChange()
        XCTAssertNil(m.pendingKeyChange)
        XCTAssertEqual(m.daemonPubHex, keyB)
        XCTAssertEqual(m.secret, "pc_2")
    }

    /// A key change learned from ~/.oculus/pairing.json is a daemon reinstall, not an attacker: the
    /// file is 0600 in the user's own home, and writing it already requires holding ~/.oculus/key.
    /// Prompting for an always-benign event is how a warning gets trained away.
    func testLocalPairingHealsAReinstallWithoutPrompting() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://127.0.0.1:6000/ws", pub: keyA, secret: "lc_1")

        m.applyLocalPairing(url: "ws://127.0.0.1:6000/ws", pub: keyB, secret: "lc_2")
        XCTAssertNil(m.pendingKeyChange, "a local-file key change must not prompt")
        XCTAssertEqual(m.daemonPubHex, keyB, "and must heal in place")
    }

    /// An empty incoming key is not a key change — it is a malformed payload. It must neither raise a
    /// dialog nor blank the pin, because an empty pin is a silent downgrade to trusting whatever
    /// answers next.
    func testEmptyIncomingKeyIsRefusedOutright() {
        let m = unpairedModel()
        m.applyPairing(url: "ws://mac.local:6000/ws", pub: keyA, secret: "pc_1")

        XCTAssertFalse(m.applyPairing(url: "ws://mac.local:6000/ws", pub: "", secret: "pc_2"))
        XCTAssertNil(m.pendingKeyChange)
        XCTAssertEqual(m.daemonPubHex, keyA, "a keyless payload must never blank the pin")
        XCTAssertEqual(m.secret, "pc_1")
    }

    // MARK: - Fingerprints

    /// The dialog asks the user to compare against the key the daemon prints, so the rendering has to
    /// be comparable at a glance and stable.
    func testFingerprintIsFourGroupsOfFour() {
        XCTAssertEqual(Model.keyFingerprint(keyA), "a1b2 a1b2 a1b2 a1b2")
        XCTAssertNotEqual(Model.keyFingerprint(keyA), Model.keyFingerprint(keyB))
    }

    /// A short or malformed key must render as itself rather than crash or silently truncate to
    /// something that could compare equal to a real one.
    func testShortKeyFingerprintIsPassedThrough() {
        XCTAssertEqual(Model.keyFingerprint("abc"), "abc")
    }

    // MARK: - DesktopStore (multi-Mac / macOS path)

    private func desktop(_ id: String, name: String, ws: String) -> Desktop {
        Desktop(id: id, name: name, wsURL: ws, secret: "", relay: nil)
    }

    /// Desktops are keyed by public key, so a changed key slips in as a NEW entry rather than
    /// overwriting. That is not safety: the user gets two identically-named Macs, only the new one
    /// works, and they pick it. The collision has to be found by an anchor that isn't the key.
    func testSameAddressDifferentKeyIsACollision() {
        let known = [desktop(keyA, name: "Studio", ws: "ws://mac.local:6000/ws")]
        let p = PairingPayload(wsURL: "ws://mac.local:6000/ws", pub: keyB, secret: "s", name: "Studio")
        let clash = DesktopStore.collision(for: p, among: known)
        XCTAssertEqual(clash?.desktop.id, keyA)
        XCTAssertTrue(clash?.reason.contains("mac.local") == true)
    }

    /// An attacker pointing at their own address still has to call themselves something, and calling
    /// themselves the user's Mac is the point of the exercise.
    func testSameNameDifferentAddressIsACollision() {
        let known = [desktop(keyA, name: "Studio", ws: "ws://mac.local:6000/ws")]
        let p = PairingPayload(wsURL: "ws://evil.example/ws", pub: keyB, secret: "s", name: "studio")
        XCTAssertEqual(DesktopStore.collision(for: p, among: known)?.desktop.id, keyA)
    }

    /// Same key is an ordinary re-pair and must not raise anything.
    func testSameKeyIsNotACollision() {
        let known = [desktop(keyA, name: "Studio", ws: "ws://mac.local:6000/ws")]
        let p = PairingPayload(wsURL: "ws://10.0.0.9:6000/ws", pub: keyA, secret: "s", name: "Studio")
        XCTAssertNil(DesktopStore.collision(for: p, among: known))
    }

    /// Genuinely adding a second, different Mac must stay frictionless.
    func testUnrelatedDesktopIsNotACollision() {
        let known = [desktop(keyA, name: "Studio", ws: "ws://mac.local:6000/ws")]
        let p = PairingPayload(wsURL: "ws://laptop.local:6000/ws", pub: keyB, secret: "s", name: "Laptop")
        XCTAssertNil(DesktopStore.collision(for: p, among: known))
    }

    // MARK: - Status honesty

    /// We do NOT claim to detect key substitution from a socket error — every non-rejection failure
    /// arrives identically. What the copy must not do is leave one confident explanation standing,
    /// because the attack's next move is offering a QR code to "fix" it.
    func testSustainedFailureNamesWhereAPairingCodeMustComeFrom() {
        let now = Date()
        let asleep = Model.unreachableDetail(lastConnected: now.addingTimeInterval(-120), now: now)
        XCTAssertFalse(asleep.contains("don’t scan"), "a single blip must not raise the subject")

        let sustained = Model.unreachableDetail(lastConnected: now.addingTimeInterval(-120), now: now, sustained: true)
        XCTAssertTrue(sustained.contains("from the Mac itself"))
        XCTAssertTrue(sustained.hasPrefix(asleep), "the defensive line is added, not substituted")
    }
}
