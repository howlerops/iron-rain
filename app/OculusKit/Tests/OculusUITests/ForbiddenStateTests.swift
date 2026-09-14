import XCTest
@testable import OculusUI
@testable import OculusKit

/// An empty state is a claim about the world, and for a guest it was the wrong one.
///
/// Most loaders in this app swallow their error — a bootstrap request that fails must not block the
/// connection — so a refusal and a failure both came out as "there is nothing here". Once the
/// owner's devices and notification settings became owner-only, that meant the Devices screen told a
/// guest "No devices enrolled" about a Mac with devices enrolled, and the Notifications section
/// waited on a spinner for a list that was never coming.
final class ForbiddenStateTests: XCTestCase {

    private func error(code: String?) -> Error {
        var info: [String: Any] = [NSLocalizedDescriptionKey: "Only the session owner can list enrolled devices."]
        if let code { info[OculusError.codeKey] = code }
        return NSError(domain: "Oculus", code: -2, userInfo: info)
    }

    func testARefusalIsRecognised() {
        XCTAssertTrue(OculusError.isForbidden(error(code: OculusError.forbidden)))
    }

    /// A disk error, a dropped socket, an older daemon that sends no code at all — none of these
    /// mean "you are not allowed", and treating them that way would tell the user they have a
    /// permissions problem when they have a network problem.
    func testAnOrdinaryFailureIsNotARefusal() {
        XCTAssertFalse(OculusError.isForbidden(error(code: nil)))
        XCTAssertFalse(OculusError.isForbidden(error(code: "something_else")))
        XCTAssertFalse(OculusError.isForbidden(
            NSError(domain: "Oculus", code: -1, userInfo: [NSLocalizedDescriptionKey: "not connected"])))
    }

    /// The receive loop's own path, from the daemon's bytes to the thrown error. This is the wiring
    /// the classifier is useless without.
    func testTheRefusalSurvivesTheTripFromWireToThrownError() throws {
        let raw = Data("""
        {"id":"req-1","type":"error","payload":{"message":"Only the session owner can list enrolled devices.","code":"forbidden"}}
        """.utf8)
        let err = requestError(from: try Protocol.envelope(raw))
        XCTAssertTrue(OculusError.isForbidden(err),
                      "the daemon marked this forbidden and the client lost it between the wire and "
                      + "the throw — every screen will render its empty state instead")
        XCTAssertTrue(err.localizedDescription.contains("list enrolled devices"))

        // And an ordinary failure from the same path stays unclassified.
        let plain = Data(#"{"id":"req-2","type":"error","payload":{"message":"no such session"}}"#.utf8)
        XCTAssertFalse(OculusError.isForbidden(requestError(from: try Protocol.envelope(plain))))
    }

    /// The daemon's exact payload shape, decoded the way the receive loop decodes it.
    func testTheDaemonsRefusalPayloadDecodes() throws {
        let err = try ProtocolCoding.decoder().decode(ProtocolError.self, from: Data("""
        {"message":"Only the session owner can list enrolled devices.","code":"forbidden"}
        """.utf8))
        XCTAssertEqual(err.code, OculusError.forbidden)
        XCTAssertTrue(err.message.contains("list enrolled devices"))

        // And an older daemon, which sends no code.
        let old = try ProtocolCoding.decoder().decode(ProtocolError.self, from: Data(#"{"message":"nope"}"#.utf8))
        XCTAssertNil(old.code, "an absent code must read as unclassified, never as a refusal")
    }

    /// The flags the two screens branch on start false, so nothing claims a permissions problem
    /// before anything has been refused.
    @MainActor
    func testNothingClaimsARefusalBeforeOneHappens() {
        let m = Model()
        XCTAssertFalse(m.devicesForbidden)
        XCTAssertFalse(m.notifyPrefsForbidden)
    }

    /// A failure that is NOT a refusal must leave the flag clear — the screen should show its
    /// ordinary empty state, not accuse the user of being a guest.
    ///
    /// Asked of the CLASSIFIER the loader actually calls. The previous version called loadDevices()
    /// on a never-connected Model, which returns at its `guard client != nil` before reaching any
    /// classification — so it set the flag itself, watched a function do nothing, and asserted the
    /// flag was unchanged. Inverting the real line left it green.
    @MainActor
    func testAnOrdinaryFailureDoesNotSetTheForbiddenFlag() {
        let m = Model()

        m.noteDevicesLoadFailure(error(code: OculusError.forbidden))
        XCTAssertTrue(m.devicesForbidden, "a real refusal was not recognised")

        // A dropped socket, a disk error, an older daemon that sends no code at all.
        m.noteDevicesLoadFailure(NSError(domain: "Oculus", code: -1,
                                         userInfo: [NSLocalizedDescriptionKey: "not connected"]))
        XCTAssertFalse(m.devicesForbidden,
                       "an ordinary failure was classified as a refusal. The Devices screen then "
                       + "tells the OWNER they are a guest during a routine reconnect.")
    }

    /// And the disconnected path still has to leave things alone, which is the half the old test
    /// was actually exercising.
    @MainActor
    func testADisconnectedLoadChangesNothing() async {
        let m = Model() // never connected: loadDevices returns at its guard
        m.devicesForbidden = true // stale from a previous connection as a guest
        await m.loadDevices()
        XCTAssertTrue(m.devicesForbidden,
                      "loadDevices returns early with no client, so nothing should have re-evaluated it")
    }
}
