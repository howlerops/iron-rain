import Foundation

/// The Oculus wire protocol in Swift, mirroring `daemon/protocol`. JSON uses
/// snake_case keys (matched via the encoder/decoder strategies in `ProtocolCoding`).
public enum MessageType {
    public static let sessionList = "session.list"
    public static let sessionGet = "session.get"
    public static let sessionCreate = "session.create"
    public static let sessionPrompt = "session.prompt"
    public static let sessionStop = "session.stop"
    public static let sessionAttach = "session.attach"
    public static let sessionSubscribe = "session.subscribe"
    public static let approvalRespond = "approval.respond"
    public static let discover = "discover.list"
    public static let deviceRegister = "device.register"
    public static let projectList = "project.list"
    public static let projectAdd = "project.add"
    public static let projectRemove = "project.remove"
    public static let worktreeDiff = "worktree.diff"
    public static let worktreeRemove = "worktree.remove"
    public static let worktreePR = "worktree.pr"
    public static let worktreeConflicts = "worktree.conflicts"
    public static let integrationConnect = "integration.connect"
    public static let integrationStatus = "integration.status"
    public static let integrationOAuth = "integration.oauth"
    public static let issueList = "issue.list"
    public static let issueStates = "issue.states"
    public static let issueLaunch = "issue.launch"

    public static let sessionStatus = "session.status"
    public static let sessionMessage = "session.message"
    public static let thinking = "thinking.delta"
    public static let outputDelta = "output.delta"
    public static let approvalRequest = "approval.request"
    public static let approvalResolved = "approval.resolved"

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
    public static let always = "always"
}

// Payload types (Decodable ignores unknown keys, so optional/extra fields are safe).

public struct SessionCreate: Codable {
    public var provider: String
    public var cwd: String?
    public var projectID: String?
    public var prompt: String?
    public var images: [ImageAttachment]?
    public var worktree: Bool?
    public var workspaceName: String?
    public init(provider: String, cwd: String? = nil, projectID: String? = nil, prompt: String? = nil, images: [ImageAttachment]? = nil, worktree: Bool? = nil, workspaceName: String? = nil) {
        self.provider = provider; self.cwd = cwd; self.projectID = projectID; self.prompt = prompt; self.images = images; self.worktree = worktree; self.workspaceName = workspaceName
    }
    enum CodingKeys: String, CodingKey {
        case provider, cwd, prompt, images, worktree
        case projectID = "project_id"
        case workspaceName = "workspace_name"
    }
}
public struct SessionRef: Codable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}
public struct ImageAttachment: Codable, Hashable, Identifiable {
    public let id: UUID // client-only stable identity for SwiftUI; never on the wire
    public var mime: String
    public var data: String // base64, no data: prefix
    public init(mime: String, data: String) { self.id = UUID(); self.mime = mime; self.data = data }
    // The daemon's ImageAttachment is just {mime, data}; id is excluded from CodingKeys so
    // it isn't encoded, and a decoded attachment gets a fresh id. Distinct instances stay
    // distinct (so two byte-identical images are still independently removable).
    enum CodingKeys: String, CodingKey { case mime, data }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = UUID()
        self.mime = try c.decode(String.self, forKey: .mime)
        self.data = try c.decode(String.self, forKey: .data)
    }
}
public struct SessionPrompt: Codable {
    public var sessionID: String; public var text: String; public var images: [ImageAttachment]?
    public init(sessionID: String, text: String, images: [ImageAttachment]? = nil) {
        self.sessionID = sessionID; self.text = text; self.images = images
    }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case text; case images }
}
public struct OutputDelta: Codable {
    public var sessionID: String; public var text: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case text }
}
public struct SessionStatus: Codable {
    public var sessionID: String; public var status: String; public var detail: String?
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case status; case detail }
}
public struct Thinking: Codable {
    public var sessionID: String; public var text: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case text }
}
public struct ApprovalRequest: Codable, Equatable {
    public var approvalID: String; public var sessionID: String; public var tool: String; public var detail: String?
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case sessionID = "session_id"; case tool; case detail }
}
public struct ApprovalRespond: Codable {
    public var approvalID: String; public var decision: String
    public init(approvalID: String, decision: String) { self.approvalID = approvalID; self.decision = decision }
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case decision }
}
public struct ApprovalResolved: Codable {
    public var approvalID: String; public var decision: String
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case decision }
}
public struct Session: Codable, Identifiable {
    public var id: String
    public var provider: String
    public var status: String
    public var title: String?
    public var projectID: String?
    public var cwd: String?
    public var workspaceName: String?
    public var branch: String?
    public var port: Int?
    public var issueKey: String?
    public var issueID: String?
    enum CodingKeys: String, CodingKey {
        case id, provider, status, title, cwd, branch, port
        case projectID = "project_id"
        case workspaceName = "workspace_name"
        case issueKey = "issue_key"
        case issueID = "issue_id"
    }
}
public struct ProtocolError: Codable { public var message: String }
public struct SessionList: Codable { public var sessions: [Session] }

// Projects — registered folders sessions can be spawned in.
public struct Project: Codable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var path: String
    public var isGitRepo: Bool
    public var defaultBranch: String?
    public var source: String? // "manual" or "auto" (discovered from an active agent's cwd)
    public var isAuto: Bool { source == "auto" }
    enum CodingKeys: String, CodingKey {
        case id, name, path, source
        case isGitRepo = "is_git_repo"
        case defaultBranch = "default_branch"
    }
}
public struct ProjectAdd: Codable {
    public var path: String
    public init(path: String) { self.path = path }
}
public struct ProjectRef: Codable {
    public var projectID: String
    public init(projectID: String) { self.projectID = projectID }
    enum CodingKeys: String, CodingKey { case projectID = "project_id" }
}
public struct ProjectList: Codable { public var projects: [Project] }

// Worktree finish flow.
public struct WorktreeRemove: Codable {
    public var sessionID: String; public var force: Bool?
    public init(sessionID: String, force: Bool? = nil) { self.sessionID = sessionID; self.force = force }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case force }
}
public struct WorktreeDiff: Codable {
    public var sessionID: String; public var diff: String?
    public init(sessionID: String, diff: String? = nil) { self.sessionID = sessionID; self.diff = diff }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case diff }
}
public struct WorktreePR: Codable {
    public var sessionID: String; public var title: String; public var body: String?
    public init(sessionID: String, title: String, body: String? = nil) { self.sessionID = sessionID; self.title = title; self.body = body }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case title; case body }
}
public struct WorktreePRResult: Codable {
    public var sessionID: String; public var branch: String; public var pushed: Bool; public var url: String?
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case branch; case pushed; case url }
}
public struct FileConflict: Codable, Identifiable, Hashable {
    public var path: String; public var branches: [String]
    public var id: String { path }
}
public struct WorktreeConflicts: Codable {
    public var sessionID: String; public var files: [FileConflict]?
    public init(sessionID: String, files: [FileConflict]? = nil) { self.sessionID = sessionID; self.files = files }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case files }
}

// Integrations / issues.
public struct IntegrationConnect: Codable {
    public var provider: String; public var token: String
    public init(provider: String, token: String) { self.provider = provider; self.token = token }
}
public struct IntegrationStatus: Codable { public var connected: [String] }
public struct IntegrationOAuth: Codable {
    public var provider: String; public var url: String?
    public init(provider: String, url: String? = nil) { self.provider = provider; self.url = url }
}
public struct Issue: Codable, Identifiable, Hashable {
    public var id: String
    public var key: String
    public var title: String
    public var body: String?
    public var status: String
    public var category: String // todo | in_progress | done | other
    public var assignee: String?
    public var url: String?
    public var provider: String
    public var branchName: String?
    public var teamID: String?
    public var priority: Int?
    public var updatedAt: String?
    enum CodingKeys: String, CodingKey {
        case id, key, title, body, status, category, assignee, url, provider, priority
        case branchName = "branch_name"
        case teamID = "team_id"
        case updatedAt = "updated_at"
    }
}
public struct IssueList: Codable { public var issues: [Issue] }
public struct IssueState: Codable, Identifiable, Hashable {
    public var id: String; public var name: String; public var category: String; public var position: Double
}
public struct IssueStatesReq: Codable {
    public var provider: String; public var teamID: String
    public init(provider: String, teamID: String) { self.provider = provider; self.teamID = teamID }
    enum CodingKeys: String, CodingKey { case provider; case teamID = "team_id" }
}
public struct IssueStateList: Codable { public var states: [IssueState] }
public struct IssueLaunch: Codable {
    public var issueID: String; public var provider: String; public var projectID: String
    public var worktree: Bool?; public var agentProvider: String?
    public init(issueID: String, provider: String, projectID: String, worktree: Bool? = true, agentProvider: String? = nil) {
        self.issueID = issueID; self.provider = provider; self.projectID = projectID; self.worktree = worktree; self.agentProvider = agentProvider
    }
    enum CodingKeys: String, CodingKey {
        case provider, worktree
        case issueID = "issue_id"
        case projectID = "project_id"
        case agentProvider = "agent_provider"
    }
}

public struct SessionAttach: Codable {
    public var provider: String; public var sessionID: String; public var url: String?
    public init(provider: String, sessionID: String, url: String?) { self.provider = provider; self.sessionID = sessionID; self.url = url }
    enum CodingKeys: String, CodingKey { case provider; case sessionID = "session_id"; case url }
}
public struct SessionMessage: Codable {
    public var sessionID: String; public var role: String; public var text: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case role; case text }
}

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

/// Registers this device's APNs token to receive approval pushes. Mirrors
/// `protocol.DeviceRegister`.
public struct DeviceRegister: Codable {
    public var token: String
    public init(token: String) { self.token = token }
}

/// Shared encoder/decoder for the wire format. Keys are set explicitly via each
/// type's CodingKeys (matching the Go JSON), so no key-strategy is used.
///
/// The instances are cached and reused: `JSONEncoder`/`JSONDecoder` are safe for
/// concurrent encode/decode as long as their configuration isn't mutated after
/// setup, and reusing them avoids per-message allocation on the hot streaming path.
public enum ProtocolCoding {
    public static let sharedEncoder = JSONEncoder()
    public static let sharedDecoder = JSONDecoder()
    public static func encoder() -> JSONEncoder { sharedEncoder }
    public static func decoder() -> JSONDecoder { sharedDecoder }
}

/// Envelope header (id + type) with the payload handled generically.
public struct EnvelopeHeader: Decodable {
    public let id: String?
    public let type: String
}

/// A wire envelope parsed in a single pass. Dispatch on `type`/`id`, then decode
/// the payload via `payload(as:)` without re-tokenizing the whole message. This
/// avoids the double JSON parse of reading the header and then re-decoding the
/// payload from the same raw bytes.
public struct Envelope {
    public let id: String?
    public let type: String
    private let payloadJSON: Any?

    init(id: String?, type: String, payloadJSON: Any?) {
        self.id = id
        self.type = type
        self.payloadJSON = payloadJSON
    }

    /// True when the envelope carried a `payload` field.
    public var hasPayload: Bool { payloadJSON != nil }

    /// Top-level keys of the payload object (empty if the payload isn't an object).
    /// Lets a caller pick the right payload type without another JSON parse.
    public var payloadKeys: Set<String> {
        guard let dict = payloadJSON as? [String: Any] else { return [] }
        return Set(dict.keys)
    }

    /// Decodes the already-parsed payload into `T`. Only the payload subtree is
    /// touched; the envelope itself is not re-parsed.
    public func payload<T: Decodable>(as _: T.Type) throws -> T {
        guard let obj = payloadJSON else {
            throw NSError(domain: "OculusKit", code: 1, userInfo: [NSLocalizedDescriptionKey: "missing payload"])
        }
        let data = try JSONSerialization.data(withJSONObject: obj)
        return try ProtocolCoding.decoder().decode(T.self, from: data)
    }
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

    /// Parses the wire envelope in a single pass. Use `env.type`/`env.id` to
    /// dispatch and `env.payload(as:)` to decode the payload without re-parsing
    /// the bytes — the receive loop should prefer this over `header` + `payload`.
    public static func envelope(_ data: Data) throws -> Envelope {
        let obj = try JSONSerialization.jsonObject(with: data)
        guard let dict = obj as? [String: Any], let type = dict["type"] as? String else {
            throw NSError(domain: "OculusKit", code: 2, userInfo: [NSLocalizedDescriptionKey: "invalid envelope"])
        }
        return Envelope(id: dict["id"] as? String, type: type, payloadJSON: dict["payload"])
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
