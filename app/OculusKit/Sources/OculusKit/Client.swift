import Foundation

public enum OculusClientError: Error {
    case handshakeRejected(String)
    case notConnected
    case badMessage
    /// A sealed frame arrived whose sequence number did not advance — a duplicate or a
    /// reordering, i.e. something is replaying frames into a live session.
    case replay
    /// The daemon's first frame was neither a v1 challenge nor anything else we can act on.
    case badHandshake(String)
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
    /// Which handshake this connection ended up on: `handshakeV1` has replay protection,
    /// `handshakeV0` does not (a daemon too old to offer a challenge).
    private var version = OculusClient.handshakeV0
    private var sendSeq: UInt64 = 0
    private var lastRecvSeq: UInt64?

    /// Handshake versions — see `daemon/transport`.
    public static let handshakeV0 = 0
    public static let handshakeV1 = 1

    /// How long to wait for the daemon's challenge before concluding it is a pre-v1 daemon.
    ///
    /// A pre-v1 daemon sends *nothing* at that point (it has read the hello and is waiting
    /// for the sealed proof), so silence is the only signal there is. Getting it wrong is
    /// survivable in both directions: too short and we fall back against a v1 daemon, which
    /// recovers by trial-opening the v0 proof — the connection works, without replay
    /// protection; too long and connecting to a pre-v1 daemon stalls for this duration.
    /// 3s clears any plausible relay round trip and leaves headroom inside the 12s budget
    /// below. It disappears when v0 support does.
    private static let legacyFallbackNanos: UInt64 = 3_000_000_000

    public init(url: URL, session: URLSession = URLSession(configuration: .default)) {
        self.task = session.webSocketTask(with: url)
    }

    private struct ClientHello: Encodable { let clientPub: String; let v: Int }
    private struct ServerChallenge: Decodable { let v: Int; let challenge: String }
    private struct ClientProof: Encodable { let secret: String; let challenge: String }
    private struct ServerHello: Decodable { let ok: Bool; let error: String? }

    /// The handshake version this connection negotiated. `handshakeV0` means the daemon is
    /// old enough that the session has no replay protection.
    public var handshakeVersion: Int { version }

    /// Performs the handshake, mirroring `daemon/transport` (client side):
    /// 1. announce the client public key in the clear (a public key — safe) plus the version,
    /// 2. take the daemon's per-connection challenge (or fall back to v0 if none arrives),
    /// 3. derive the channel from static-static ECDH bound to that challenge,
    /// 4. prove the pairing secret by sending it *encrypted* and challenge-bound,
    /// 5. read the server's encrypted verdict.
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

        // 1. announce our public key (safe in the clear) and the handshake version we speak.
        let clientPub = try OculusCrypto.publicKey(fromPrivate: clientPrivate)
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let hello = try enc.encode(ClientHello(clientPub: clientPub.hexString, v: Self.handshakeV1))
        try await task.send(.data(hello))

        // 2. take the daemon's per-connection challenge.
        //
        // The receive is started ONCE and never abandoned. A second `receive()` would queue
        // behind this one, so if we walked away from it the pre-v1 daemon's verdict would be
        // delivered to a read nobody is waiting on and the fallback would hang forever.
        let pending = Task { try await Self.bytes(of: self.task.receive()) }
        guard let first = await Self.frameOrTimeout(pending, nanos: Self.legacyFallbackNanos) else {
            // Silence: either a daemon too old to send a challenge, or a link slower than the
            // fallback delay. Both are handled by the v0 path.
            try await legacyHandshake(clientPrivate: clientPrivate, daemonPublic: daemonPublic, secret: secret, pending: pending)
            return
        }
        guard let challenge = Self.parseChallenge(first) else {
            throw OculusClientError.badHandshake("daemon's first frame was not a v1 challenge")
        }

        // 3. derive the channel bound to that challenge — these keys exist for this
        //    connection only, which is what makes a recorded stream useless against it.
        let keys = try OculusCrypto.deriveSessionKeysV1(localPrivate: clientPrivate, daemonPublic: daemonPublic, challenge: challenge)
        sealer = Sealer(key: keys.c2d)
        opener = Opener(key: keys.d2c)
        version = Self.handshakeV1
        sendSeq = 0
        lastRecvSeq = nil

        // 4. prove the pairing secret, encrypted and bound to the challenge. Sent through
        //    `send`/`recv` so the handshake frames carry sequence numbers 0 like every other
        //    frame — the daemon counts them the same way.
        let proof = try JSONEncoder().encode(ClientProof(secret: secret, challenge: challenge.hexString))
        try await send(proof)

        // 5. read the encrypted verdict.
        try acceptVerdict(try await recv())
    }

    /// The pre-v1 handshake (no challenge, no sequencing), for a daemon that never sent one.
    /// `pending` is the still-outstanding receive from `performHandshake`; it carries the
    /// verdict and must not be re-issued.
    private func legacyHandshake(clientPrivate: Data, daemonPublic: Data, secret: String, pending: Task<Data, Error>) async throws {
        let keys = try OculusCrypto.deriveSessionKeys(localPrivate: clientPrivate, remotePublic: daemonPublic)
        let s = Sealer(key: keys.c2d)
        let o = Opener(key: keys.d2c)
        sealer = s
        opener = o
        version = Self.handshakeV0
        try await task.send(.data(s.seal(Data(secret.utf8))))

        // Normally the next frame is the verdict. But if we merely lost a race with a slow v1
        // daemon, this read delivers that daemon's challenge and the verdict is the frame
        // after it — the daemon recovers by trial-opening our v0 proof, so the connection
        // still completes. Exactly one stray challenge can be in flight.
        var raw = try await pending.value
        var plain = try? o.open(raw)
        if plain == nil, Self.parseChallenge(raw) != nil {
            raw = try await Self.bytes(of: task.receive())
            plain = try? o.open(raw)
        }
        guard let verdict = plain else {
            sealer = nil; opener = nil
            throw OculusClientError.badHandshake("could not decrypt the daemon's verdict")
        }
        try acceptVerdict(verdict)
    }

    private func acceptVerdict(_ data: Data) throws {
        let resp = try JSONDecoder().decode(ServerHello.self, from: data)
        guard resp.ok else {
            sealer = nil; opener = nil
            throw OculusClientError.handshakeRejected(resp.error ?? "rejected")
        }
    }

    /// Parses a cleartext v1 challenge frame, or nil if the frame is not one. A sealed frame
    /// is indistinguishable from random, so it decodes as this JSON only with negligible
    /// probability; nothing is trusted on the strength of this — it only decides which of two
    /// well-defined paths to take.
    private static func parseChallenge(_ raw: Data) -> Data? {
        guard let sc = try? JSONDecoder().decode(ServerChallenge.self, from: raw), sc.v >= handshakeV1,
              let challenge = Data(hexString: sc.challenge), challenge.count == OculusCrypto.challengeSize
        else { return nil }
        return challenge
    }

    /// Returns `pending`'s frame, or nil if `nanos` elapses first (or the read failed — the
    /// caller re-awaits `pending` and gets the real error).
    ///
    /// Written with two detached tasks and a one-shot gate rather than a task group on
    /// purpose: `await someTask.value` is not cancellation-aware, so a task-group child
    /// blocked on it keeps the group alive after `cancelAll()` and the group would sit there
    /// until the read finally returned — i.e. the timeout would silently not work. Nothing
    /// here waits on the reader: it stays outstanding, which is exactly what the fallback
    /// path needs.
    private static func frameOrTimeout(_ pending: Task<Data, Error>, nanos: UInt64) async -> Data? {
        let gate = OneShotGate()
        return await withCheckedContinuation { (cont: CheckedContinuation<Data?, Never>) in
            Task {
                let frame = try? await pending.value
                await gate.resume(cont, with: frame)
            }
            Task {
                try? await Task.sleep(nanoseconds: nanos)
                await gate.resume(cont, with: nil)
            }
        }
    }

    /// Encrypts and sends one protocol envelope.
    public func send(_ envelope: Data) async throws {
        guard let sealer else { throw OculusClientError.notConnected }
        var payload = envelope
        if version >= Self.handshakeV1 {
            // Taking the number, framing and sealing all happen in one synchronous stretch
            // with the `task.send` call below, so the actor cannot slip another `send` in
            // between: the order numbers are handed out in is the order frames are queued in.
            payload = SequenceFraming.frame(envelope, seq: sendSeq)
            // Burn the number even if the send throws — reusing it after a failed write would
            // look exactly like a replay to the daemon and drop the connection.
            sendSeq += 1
        }
        try await task.send(.data(sealer.seal(payload)))
    }

    /// Receives and decrypts one protocol envelope, rejecting any frame whose sequence number
    /// does not advance (a duplicate or a reordering injected by something on the wire).
    ///
    /// One consumer only. Two concurrent `recv()` calls would resume in an order the actor
    /// does not control and could reject a perfectly good frame as a replay — which is really
    /// just the pre-existing rule made enforceable, since consuming an ordered event stream
    /// out of order was never safe. `OculusUI`'s receive loop is that single consumer.
    public func recv() async throws -> Data {
        guard let opener else { throw OculusClientError.notConnected }
        let plain = try opener.open(try await Self.bytes(of: task.receive()))
        guard version >= Self.handshakeV1 else { return plain }
        let (seq, payload) = try SequenceFraming.unframe(plain)
        if let last = lastRecvSeq, seq <= last { throw OculusClientError.replay }
        lastRecvSeq = seq
        return payload
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

/// Lets two racing tasks resume the same continuation, with the loser dropped. A
/// `CheckedContinuation` resumed twice is a crash, so the "first one wins" rule needs a
/// place to live; an actor is the smallest one that is correct under concurrency.
private actor OneShotGate {
    private var resumed = false

    func resume(_ cont: CheckedContinuation<Data?, Never>, with value: Data?) {
        guard !resumed else { return }
        resumed = true
        cont.resume(returning: value)
    }
}
