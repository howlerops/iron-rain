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
