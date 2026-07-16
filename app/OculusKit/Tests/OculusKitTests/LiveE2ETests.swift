import XCTest
@testable import OculusKit

/// The definitive cross-language E2E: a Swift client spawns the real Go daemon
/// (cmd/oculus-e2e, with a stub opencode backend), connects over WebSocket, and
/// drives a full session — create -> streamed output -> approval -> respond -> idle.
final class LiveE2ETests: XCTestCase {
    func repoRoot() -> URL {
        // .../app/OculusKit/Tests/OculusKitTests/LiveE2ETests.swift -> repo root
        var url = URL(fileURLWithPath: #filePath)
        for _ in 0 ..< 5 { url.deleteLastPathComponent() }
        return url
    }

    /// Builds and launches the Go E2E helper; returns (process, wsURL, daemonPub, secret).
    func launchDaemon() throws -> (Process, String, Data, String) {
        let daemonDir = repoRoot().appendingPathComponent("daemon")
        let binURL = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("oculus-e2e-\(UUID().uuidString)")

        // go build -o <bin> ./cmd/oculus-e2e
        let build = Process()
        build.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        build.arguments = ["go", "build", "-o", binURL.path, "./cmd/oculus-e2e"]
        build.currentDirectoryURL = daemonDir
        let buildErr = Pipe()
        build.standardError = buildErr
        try build.run()
        build.waitUntilExit()
        guard build.terminationStatus == 0 else {
            let msg = String(data: buildErr.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            throw XCTSkip("go build failed (is Go installed?): \(msg)")
        }

        // launch and read the READY line
        let proc = Process()
        proc.executableURL = binURL
        let out = Pipe()
        proc.standardOutput = out
        try proc.run()

        let handle = out.fileHandleForReading
        var buf = Data()
        let deadline = Date().addingTimeInterval(15)
        var readyLine: String?
        while Date() < deadline {
            let chunk = handle.availableData
            if chunk.isEmpty { break }
            buf.append(chunk)
            if let s = String(data: buf, encoding: .utf8),
               let line = s.split(separator: "\n").first(where: { $0.hasPrefix("READY ") }) {
                readyLine = String(line)
                break
            }
        }
        guard let line = readyLine else {
            proc.terminate()
            throw XCTestError(.failureWhileWaiting)
        }
        let parts = line.split(separator: " ")
        guard parts.count == 4, let pub = Data(hexString: String(parts[2])) else {
            proc.terminate()
            throw XCTestError(.failureWhileWaiting)
        }
        return (proc, String(parts[1]), pub, String(parts[3]))
    }

    func testLiveSessionOverWebSocket() async throws {
        let (proc, wsURL, daemonPub, secret) = try launchDaemon()
        defer { proc.terminate() }

        let client = OculusClient(url: URL(string: wsURL)!)
        defer { client.close() }

        let clientPriv = OculusCrypto.generatePrivateKey()
        try await client.connect(clientPrivate: clientPriv, daemonPublic: daemonPub, secret: secret)

        // session.create
        let create = try Protocol.encode(id: "c1", type: MessageType.sessionCreate,
                                          payload: SessionCreate(provider: "opencode", prompt: "go"))
        try await client.send(create)

        var gotOK = false, gotOutput = false, gotIdle = false
        for _ in 0 ..< 30 {
            let data = try await client.recv()
            let header = try Protocol.header(data)
            switch header.type {
            case MessageType.ok where header.id == "c1":
                gotOK = true
            case MessageType.outputDelta:
                gotOutput = true
            case MessageType.approvalRequest:
                let ar = try Protocol.payload(data, as: ApprovalRequest.self)
                let respond = try Protocol.encode(id: "c2", type: MessageType.approvalRespond,
                                                  payload: ApprovalRespond(approvalID: ar.approvalID, decision: Decision.allow))
                try await client.send(respond)
            case MessageType.sessionStatus:
                let ss = try Protocol.payload(data, as: SessionStatus.self)
                if ss.status == SessionStatusValue.idle || ss.status == SessionStatusValue.done {
                    gotIdle = true
                }
            case MessageType.error:
                let e = try Protocol.payload(data, as: ProtocolError.self)
                XCTFail("daemon error: \(e.message)")
                return
            default:
                break
            }
            if gotOK && gotOutput && gotIdle { break }
        }

        XCTAssertTrue(gotOK, "expected ok for session.create")
        XCTAssertTrue(gotOutput, "expected streamed output")
        XCTAssertTrue(gotIdle, "expected session to reach idle after approval")
    }
}
