import Foundation

public enum OculusClientError: Error {
    case handshakeRejected(String)
    case notConnected
    case badMessage
}

/// OculusClient connects to a daemon over WebSocket, performs the encrypted
/// handshake, and sends/receives protocol envelopes. Mirrors `daemon/transport`
/// (client side) + `daemon/server` Dial.
///
/// An `actor` so send/recv and the sealer/opener state are serialized: concurrent
/// callers can't interleave a partially-initialized handshake or race the channel.
public actor OculusClient {
    private let task: URLSessionWebSocketTask
    private var sealer: Sealer?
    private var opener: Opener?

    public init(url: URL, session: URLSession = URLSession(configuration: .default)) {
        self.task = session.webSocketTask(with: url)
    }

    private struct ClientHello: Encodable { let clientPub: String }
    private struct ServerHello: Decodable { let ok: Bool; let error: String? }

    /// Performs the handshake, mirroring `daemon/transport` (client side):
    /// 1. announce the client public key in the clear (a public key — safe),
    /// 2. derive the channel from static-static ECDH,
    /// 3. prove the pairing secret by sending it *encrypted* (never in the clear),
    /// 4. read the server's encrypted verdict.
    /// Performs the encrypted handshake. When connecting via a relay the URL already carries the
    /// registration (?sid=&role=client), so the relay bridges us to the daemon before this runs and
    /// the identical E2E handshake then flows over the bridge — the relay only forwards ciphertext,
    /// so a relayed connection is exactly as secure as a direct LAN one.
    public func connect(clientPrivate: Data, daemonPublic: Data, secret: String) async throws {
        // Bound the handshake: task.receive() (step 4) has no timeout, so a daemon that accepts the
        // socket but stalls the verdict (e.g. still holding a pre-restart client's connection) would
        // hang this route forever — and if every route hangs, the whole connect never resolves and
        // sessions never load. Race the handshake against a deadline; on timeout, close the socket so
        // the stalled receive throws, and surface it as unreachable (→ the caller retries).
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { try await self.performHandshake(clientPrivate: clientPrivate, daemonPublic: daemonPublic, secret: secret) }
            group.addTask {
                try await Task.sleep(nanoseconds: 12_000_000_000)
                self.close()
                throw OculusClientError.notConnected
            }
            defer { group.cancelAll() }
            _ = try await group.next()
        }
    }

    private func performHandshake(clientPrivate: Data, daemonPublic: Data, secret: String) async throws {
        task.resume()

        let clientPub = try OculusCrypto.publicKey(fromPrivate: clientPrivate)
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let hello = try enc.encode(ClientHello(clientPub: clientPub.hexString))
        try await task.send(.data(hello))

        let keys = try OculusCrypto.deriveSessionKeys(localPrivate: clientPrivate, remotePublic: daemonPublic)
        let s = Sealer(key: keys.c2d)
        let o = Opener(key: keys.d2c)
        sealer = s
        opener = o

        // 3. send the pairing secret encrypted (first sealed frame).
        try await task.send(.data(s.seal(Data(secret.utf8))))

        // 4. read the encrypted verdict.
        let respData = try o.open(try await Self.bytes(of: task.receive()))
        let resp = try JSONDecoder().decode(ServerHello.self, from: respData)
        guard resp.ok else {
            sealer = nil; opener = nil
            throw OculusClientError.handshakeRejected(resp.error ?? "rejected")
        }
    }

    /// Encrypts and sends one protocol envelope.
    public func send(_ envelope: Data) async throws {
        guard let sealer else { throw OculusClientError.notConnected }
        try await task.send(.data(sealer.seal(envelope)))
    }

    /// Receives and decrypts one protocol envelope.
    public func recv() async throws -> Data {
        guard let opener else { throw OculusClientError.notConnected }
        return try opener.open(try await Self.bytes(of: task.receive()))
    }

    /// Sends a WebSocket PING and waits for the PONG. Throws if the pipe is dead.
    ///
    /// URLSession never reports a half-open connection on its own: with nothing to send, `receive()`
    /// waits forever on a socket whose peer vanished when the Mac slept or a NAT dropped the mapping.
    /// An idle coding session generates no traffic by definition, so a periodic ping is the only
    /// thing that turns that silence into an error the app can act on — otherwise "Connected" stays
    /// on screen indefinitely above a conversation that will never move again.
    ///
    /// `nonisolated` on purpose: `task` is an immutable `let` and `sendPing` is thread-safe, whereas
    /// hopping through the actor would queue the ping behind the in-flight `recv()` — which, on the
    /// exact dead pipe this exists to detect, never returns.
    public nonisolated func ping(timeout: TimeInterval = 6) async throws {
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { try await self.awaitPong() }
            group.addTask {
                try await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
                // Close before throwing: a pong that never arrives leaves `sendPing`'s handler
                // outstanding, and cancelling the task is what makes URLSession invoke it with an
                // error so the sibling child (and its continuation) can finish.
                self.close()
                throw OculusClientError.notConnected
            }
            defer { group.cancelAll() }
            _ = try await group.next()
        }
    }

    private nonisolated func awaitPong() async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            task.sendPing { err in
                if let err { cont.resume(throwing: err) } else { cont.resume() }
            }
        }
    }

    /// nonisolated so the UI can tear down the connection synchronously; `task` is an
    /// immutable let and URLSessionWebSocketTask.cancel is thread-safe.
    public nonisolated func close() {
        task.cancel(with: .normalClosure, reason: nil)
    }

    private static func bytes(of message: URLSessionWebSocketTask.Message) throws -> Data {
        switch message {
        case let .data(d): return d
        case let .string(s): return Data(s.utf8)
        @unknown default: throw OculusClientError.badMessage
        }
    }
}
