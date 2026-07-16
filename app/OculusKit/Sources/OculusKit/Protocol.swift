import Foundation

/// The Oculus wire protocol in Swift, mirroring `daemon/protocol`. JSON uses
/// snake_case keys (matched via the encoder/decoder strategies in `ProtocolCoding`).
public enum MessageType {
    public static let sessionList = "session.list"
    public static let sessionGet = "session.get"
    public static let sessionCreate = "session.create"
    public static let sessionPrompt = "session.prompt"
    public static let sessionStop = "session.stop"
    public static let approvalRespond = "approval.respond"
    public static let discover = "discover.list"

    public static let sessionStatus = "session.status"
    public static let outputDelta = "output.delta"
    public static let approvalRequest = "approval.request"

    public static let ok = "ok"
    public static let error = "error"
}

public enum SessionStatusValue {
    public static let running = "running"
    public static let idle = "idle"
    public static let awaitingApproval = "awaiting_approval"
    public static let done = "done"
    public static let error = "error"
}

public enum Decision {
    public static let allow = "allow"
    public static let deny = "deny"
}

// Payload types (Decodable ignores unknown keys, so optional/extra fields are safe).

public struct SessionCreate: Codable {
    public var provider: String; public var cwd: String?; public var prompt: String?
    public init(provider: String, cwd: String? = nil, prompt: String? = nil) { self.provider = provider; self.cwd = cwd; self.prompt = prompt }
}
public struct SessionRef: Codable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}
public struct SessionPrompt: Codable {
    public var sessionID: String; public var text: String
    public init(sessionID: String, text: String) { self.sessionID = sessionID; self.text = text }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case text }
}
public struct OutputDelta: Codable {
    public var sessionID: String; public var text: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case text }
}
public struct SessionStatus: Codable {
    public var sessionID: String; public var status: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case status }
}
public struct ApprovalRequest: Codable {
    public var approvalID: String; public var sessionID: String; public var tool: String
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case sessionID = "session_id"; case tool }
}
public struct ApprovalRespond: Codable {
    public var approvalID: String; public var decision: String
    public init(approvalID: String, decision: String) { self.approvalID = approvalID; self.decision = decision }
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case decision }
}
public struct Session: Codable { public var id: String; public var provider: String; public var status: String; public var title: String? }
public struct ProtocolError: Codable { public var message: String }

public enum DiscoveredKind {
    public static let server = "server"
    public static let session = "session"
}

/// One autodetected agent artifact on the host (opencode server, opencode live
/// session, or claude-code transcript). Mirrors `protocol.Discovered`.
public struct Discovered: Codable {
    public var provider: String
    public var kind: String
    public var url: String?
    public var sessionID: String?
    public var title: String?
    public var cwd: String?
    public var path: String?
    public var pid: Int?
    enum CodingKeys: String, CodingKey {
        case provider, kind, url
        case sessionID = "session_id"
        case title, cwd, path, pid
    }
}
public struct DiscoverList: Codable { public var items: [Discovered] }

/// Shared encoder/decoder for the wire format. Keys are set explicitly via each
/// type's CodingKeys (matching the Go JSON), so no key-strategy is used.
public enum ProtocolCoding {
    public static func encoder() -> JSONEncoder { JSONEncoder() }
    public static func decoder() -> JSONDecoder { JSONDecoder() }
}

/// Envelope header (id + type) with the payload handled generically.
public struct EnvelopeHeader: Decodable {
    public let id: String?
    public let type: String
}

private struct WireOut<T: Encodable>: Encodable {
    let id: String?
    let type: String
    let payload: T?
    enum CodingKeys: String, CodingKey { case id, type, payload }
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        if let id { try c.encode(id, forKey: .id) }
        try c.encode(type, forKey: .type)
        if let payload { try c.encode(payload, forKey: .payload) }
    }
}

private struct WireIn<T: Decodable>: Decodable {
    let id: String?
    let type: String
    let payload: T?
}

public enum Protocol {
    /// Encodes an envelope `{id?, type, payload}`. Pass `Optional<T>.none` payload as
    /// `nil` typed appropriately, or use `encode(id:type:)` for no payload.
    public static func encode<T: Encodable>(id: String?, type: String, payload: T?) throws -> Data {
        try ProtocolCoding.encoder().encode(WireOut(id: id, type: type, payload: payload))
    }

    /// Encodes an envelope with no payload.
    public static func encode(id: String?, type: String) throws -> Data {
        try ProtocolCoding.encoder().encode(WireOut<Int>(id: id, type: type, payload: nil))
    }

    /// Reads just the envelope header (id + type).
    public static func header(_ data: Data) throws -> EnvelopeHeader {
        try ProtocolCoding.decoder().decode(EnvelopeHeader.self, from: data)
    }

    /// Decodes the envelope payload into T.
    public static func payload<T: Decodable>(_ data: Data, as _: T.Type) throws -> T {
        guard let p = try ProtocolCoding.decoder().decode(WireIn<T>.self, from: data).payload else {
            throw NSError(domain: "OculusKit", code: 1, userInfo: [NSLocalizedDescriptionKey: "missing payload"])
        }
        return p
    }
}
