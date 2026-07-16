import Foundation

public enum OculusClientError: Error {
    case handshakeRejected(String)
    case notConnected
    case badMessage
}

/// OculusClient connects to a daemon over WebSocket, performs the encrypted
/// handshake, and sends/receives protocol envelopes. Mirrors `daemon/transport`
/// (client side) + `daemon/server` Dial.
public final class OculusClient {
    private let task: URLSessionWebSocketTask
    private var sealer: Sealer?
    private var opener: Opener?

    public init(url: URL, session: URLSession = URLSession(configuration: .default)) {
        self.task = session.webSocketTask(with: url)
    }

    private struct ClientHello: Encodable { let clientPub: String; let secret: String }
    private struct ServerHello: Decodable { let ok: Bool; let daemonPub: String?; let error: String? }

    /// Performs the handshake: sends the client public key + pairing secret, reads the
    /// server's response, and derives the directional session keys.
    public func connect(clientPrivate: Data, daemonPublic: Data, secret: String) async throws {
        task.resume()

        let clientPub = try OculusCrypto.publicKey(fromPrivate: clientPrivate)
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let hello = try enc.encode(ClientHello(clientPub: clientPub.hexString, secret: secret))
        try await task.send(.data(hello))

        let respData = try await Self.bytes(of: task.receive())
        let dec = JSONDecoder()
        dec.keyDecodingStrategy = .convertFromSnakeCase
        let resp = try dec.decode(ServerHello.self, from: respData)
        guard resp.ok else { throw OculusClientError.handshakeRejected(resp.error ?? "rejected") }

        let keys = try OculusCrypto.deriveSessionKeys(localPrivate: clientPrivate, remotePublic: daemonPublic)
        sealer = Sealer(key: keys.c2d)
        opener = Opener(key: keys.d2c)
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

    public func close() {
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
