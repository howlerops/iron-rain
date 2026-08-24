import Foundation

/// The Oculus wire protocol in Swift, mirroring `daemon/protocol`. JSON uses
/// snake_case keys (matched via the encoder/decoder strategies in `ProtocolCoding`).
public enum MessageType {
    public static let sessionList = "session.list"
    public static let sessionGet = "session.get"
    public static let sessionCreate = "session.create"
    public static let sessionPrompt = "session.prompt"
    public static let sessionStop = "session.stop"
    public static let sessionInterrupt = "session.interrupt"
    public static let sessionRename = "session.rename"
    public static let sessionAttach = "session.attach"
    public static let sessionRestart = "session.restart"
    public static let sessionRecover = "session.recover"
    public static let sessionSubscribe = "session.subscribe"
    public static let approvalRespond = "approval.respond"
    public static let discover = "discover.list"
    public static let deviceRegister = "device.register"
    public static let providerList = "provider.list"
    public static let providerRefresh = "provider.refresh"
    public static let agentList = "agent.list"
    public static let agentUpsert = "agent.upsert"
    public static let agentDelete = "agent.delete"
    public static let agentVisible = "agent.visible"
    public static let modelList = "model.list"
    public static let sessionSetModel = "session.set_model"
    public static let projectList = "project.list"
    public static let projectAdd = "project.add"
    public static let projectBrowse = "project.browse"
    public static let commandList = "command.list"
    public static let loopList = "loop.list"
    public static let loopUpsert = "loop.upsert"
    public static let loopDelete = "loop.delete"
    public static let loopSetEnabled = "loop.enabled"
    public static let projectRemove = "project.remove"
    public static let worktreeDiff = "worktree.diff"
    public static let workspaceDiff = "workspace.diff"
    public static let workspacePR = "workspace.pr"
    public static let worktreeRemove = "worktree.remove"
    public static let worktreePR = "worktree.pr"
    public static let worktreeConflicts = "worktree.conflicts"
    public static let integrationConnect = "integration.connect"
    public static let integrationDisconnect = "integration.disconnect"
    public static let integrationStatus = "integration.status"
    public static let telemetrySet = "telemetry.set"
    public static let telemetryStatus = "telemetry.status"
    public static let sessionProgress = "session.progress"
    public static let logSubscribe = "log.subscribe"
    public static let logUnsubscribe = "log.unsubscribe"
    public static let logLine = "log.line"
    public static let activityList = "activity.list"
    public static let activityEvent = "activity.event"
    public static let activityMarkRead = "activity.markread"
    public static let fanoutCreate = "fanout.create"
    public static let fanoutResolve = "fanout.resolve"
    /// Spawn an agent that reads every variant's diff and writes one combined implementation — as an
    /// ADDITIONAL attempt in the same group, never replacing the others.
    public static let fanoutSynthesize = "fanout.synthesize"
    public static let notifyPrefsGet = "notify.prefs.get"
    public static let notifyPrefsSet = "notify.prefs.set"
    public static let approvalRulesList = "approval.rules.list"
    public static let approvalRuleDelete = "approval.rules.delete"
    public static let approvalRulesChanged = "approval.rules.changed"
    public static let sessionModeSet = "session.mode.set"
    public static let usageReport = "usage.report"
    public static let worktreeMerge = "worktree.merge"
    public static let worktreeStatus = "worktree.status"
    public static let deviceList = "device.list"
    public static let deviceRevoke = "device.revoke"
    public static let deviceLabel = "device.label"
    public static let deviceCredential = "device.credential"
    public static let deviceCredentialAck = "device.credential.ack"
    public static let pairCode = "pair.code"
    public static let pairStatus = "pair.status"
    public static let pairRetireLegacy = "pair.retire_legacy"
    public static let transcriptPage = "transcript.page"
    public static let transcriptPageBegin = "transcript.page.begin"
    public static let transcriptPageEnd = "transcript.page.end"
    public static let clientIdentify = "client.identify"
    public static let participants = "participants"
    public static let roleGrant = "role.grant"
    public static let rolesEnable = "roles.enable"
    public static let inviteCreate = "invite.create"
    public static let inviteList = "invite.list"
    public static let inviteRevoke = "invite.revoke"
    public static let fanoutSummary = "fanout.summary"
    public static let mcpList = "mcp.list"
    public static let mcpUpsert = "mcp.upsert"
    public static let mcpDelete = "mcp.delete"
    public static let mcpEnable = "mcp.enable"
    public static let mcpCheck = "mcp.check"
    public static let mcpChanged = "mcp.changed"
    public static let mcpBrowse = "mcp.browse"
    public static let mcpDiscover = "mcp.discover"
    public static let mcpImport = "mcp.import"
    public static let mcpExclusive = "mcp.exclusive"
    public static let turnState = "turn.state"
    public static let checkpointCreate = "checkpoint.create"
    public static let checkpointList = "checkpoint.list"
    public static let checkpointRestore = "checkpoint.restore"
    public static let accountList = "account.list"
    public static let accountUpsert = "account.upsert"
    public static let accountDelete = "account.delete"
    public static let accountActivate = "account.activate"
    public static let accountQuota = "account.quota"
    public static let remoteList = "remote.list"
    public static let remoteUpsert = "remote.upsert"
    public static let remoteDelete = "remote.delete"
    public static let remoteStatus = "remote.status"
    public static let remoteRun = "remote.run"
    public static let jiraSites = "jira.sites"
    public static let jiraSetSite = "jira.set_site"
    public static let worktreeCatchUp = "worktree.catch_up"
    public static let integrationOAuth = "integration.oauth"
    public static let integrationOAuthApp = "integration.oauthapp"
    public static let issueList = "issue.list"
    public static let issueStates = "issue.states"
    public static let issueLaunch = "issue.launch"
    public static let issueDetail = "issue.detail"
    public static let issueUpdate = "issue.update"
    public static let issueComment = "issue.comment"
    public static let issueCommentEdit = "issue.comment.edit"
    public static let issueMembers = "issue.members"
    public static let issueLabels = "issue.labels"
    public static let issueCycles = "issue.cycles"
    public static let issueImage = "issue.image"
    public static let issueColumns = "issue.columns"
    public static let issueMove = "issue.move"
    public static let issueCreate = "issue.create"
    public static let issueProjects = "issue.projects"

    // Built-in editor file access.
    public static let fsTree = "fs.tree"
    public static let fsRead = "fs.read"
    public static let fsReadBytes = "fs.readbytes"
    public static let previewFetch = "preview.fetch"
    public static let fsWrite = "fs.write"
    public static let fsDiff = "fs.diff"
    public static let fsWatch = "fs.watch"
    public static let fsChange = "fs.change"
    public static let fsSearch = "fs.search"

    // Built-in editor LSP (diagnostics/linting/types/definition).
    public static let lspOpen = "lsp.open"
    public static let lspChange = "lsp.change"
    public static let lspClose = "lsp.close"
    public static let lspHover = "lsp.hover"
    public static let lspDefinition = "lsp.definition"
    public static let lspComplete = "lsp.complete"
    public static let lspReferences = "lsp.references"
    public static let lspRename = "lsp.rename"
    public static let lspSymbols = "lsp.symbols"
    public static let lspFormat = "lsp.format"
    public static let lspDiagnostics = "lsp.diagnostics"
    public static let lspServerInfo = "lsp.serverinfo"
    public static let lspInstall = "lsp.install"

    public static let sessionUsage = "session.usage"
    public static let sessionTodos = "session.todos"
    public static let uiComponent = "ui.component"   // event: a normalized generative-UI component
    public static let uiAction = "ui.action"         // client → daemon: user activated a UI action
    public static let sessionSubAgent = "session.subagent" // a sub-agent started/finished under a parent
    public static let sessionTool = "session.tool"         // a tool call with its command + output
    public static let sessionHeartbeat = "session.heartbeat"
    public static let sessionAutonomy = "session.autonomy"
    public static let handoffList = "handoff.list"
    public static let sessionChild = "session.child"
    public static let runTest = "run.test"
    public static let runOutput = "run.output"
    public static let runResult = "run.result"
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
    public static let stopped = "stopped" // persisted but not live after a daemon restart; restartable
    /// An OPEN turn the daemon believes is wedged — the provider claims to be busy but nothing has
    /// progressed. Still recoverable: the daemon is nudging it. Not an error, and not terminal.
    public static let stalled = "stalled"
    /// A turn that stayed stuck after the daemon spent its nudges. Terminal, and it wants a human —
    /// but it is deliberately NOT an error: the agent didn't fail, it got stuck.
    public static let needsYou = "needs_you"
    /// An open turn whose agent stopped answering and which the daemon is actively repairing.
    /// Not an error and not actionable — it exists so a blip is something you watch heal rather
    /// than something you get paged about.
    public static let recovering = "recovering"
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
    public var projectIDs: [String]?
    public var prompt: String?
    public var images: [ImageAttachment]?
    public var worktree: Bool?
    public var workspaceName: String?
    public var plan: Bool?
    /// code | ask | architect. Enforced daemon-side for every harness (see SessionMode).
    public var mode: String?
    public var autonomous: Bool?
    public var maxNudges: Int?
    public var budgetUSD: Double?
    public var model: String?
    public var modelProvider: String?
    public var ephemeral: Bool?
    public init(provider: String, cwd: String? = nil, projectID: String? = nil, projectIDs: [String]? = nil, prompt: String? = nil, images: [ImageAttachment]? = nil, worktree: Bool? = nil, workspaceName: String? = nil, plan: Bool? = nil, mode: String? = nil, autonomous: Bool? = nil, maxNudges: Int? = nil, budgetUSD: Double? = nil, model: String? = nil, modelProvider: String? = nil, ephemeral: Bool? = nil) {
        self.provider = provider; self.cwd = cwd; self.projectID = projectID; self.projectIDs = projectIDs; self.prompt = prompt; self.images = images; self.worktree = worktree; self.workspaceName = workspaceName; self.plan = plan; self.mode = mode; self.autonomous = autonomous; self.maxNudges = maxNudges; self.budgetUSD = budgetUSD; self.model = model; self.modelProvider = modelProvider; self.ephemeral = ephemeral
    }
    enum CodingKeys: String, CodingKey {
        case provider, cwd, prompt, images, worktree, plan, mode, autonomous, model, ephemeral
        case projectID = "project_id"
        case projectIDs = "project_ids"
        case workspaceName = "workspace_name"
        case maxNudges = "max_nudges"
        case budgetUSD = "budget_usd"
        case modelProvider = "model_provider"
    }
}
public struct SessionRef: Codable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}

public struct SessionRename: Codable {
    public var sessionID: String; public var name: String
    public init(sessionID: String, name: String) { self.sessionID = sessionID; self.name = name }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case name }
}

// MARK: - Built-in editor file access (fs.*)
// Requests are encode-only (snake_case comes from the encoder's convertToSnakeCase);
// responses use single-word JSON keys so they decode by property name with no CodingKeys.

public struct FSTreeReq: Codable {
    public var path: String?
    public var sessionID: String?
    public init(path: String?, sessionID: String? = nil) { self.path = path; self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case path; case sessionID = "session_id" }
}
public struct FSNode: Codable, Identifiable, Hashable {
    public var name: String
    public var path: String
    public var dir: Bool
    public var size: Int?
    public var id: String { path }
}
public struct FSTree: Codable {
    public var path: String?
    public var entries: [FSNode]?
    public var roots: [FSNode]?
}
public struct FSReadReq: Codable {
    public var path: String
    public init(path: String) { self.path = path }
}
public struct FSFile: Codable {
    public var path: String
    public var content: String?
    public var sha: String
    public var mtime: Int?
    public var size: Int?
    public var binary: Bool?
    public var truncated: Bool?
}
public struct FSReadBytesReq: Codable {
    public var path: String
    public init(path: String) { self.path = path }
}
public struct FSBytes: Codable {
    public var path: String
    public var mime: String
    public var data: String // base64
}
public struct FSWriteReq: Codable {
    public var path: String
    public var content: String
    public var baseSha: String
    public init(path: String, content: String, baseSha: String) {
        self.path = path; self.content = content; self.baseSha = baseSha
    }
    // The envelope encoder does NOT convert to snake_case, so multi-word keys need explicit
    // CodingKeys — without this `baseSha` shipped verbatim, the daemon read "" and treated every
    // save as a conflict, so the built-in editor could never write an existing file.
    enum CodingKeys: String, CodingKey { case path, content; case baseSha = "base_sha" }
}

// MARK: - LSP (editor diagnostics/linting/types/definition)

public struct LSPDocReq: Codable {
    public var path: String
    public var content: String?
    public var language: String?
    public init(path: String, content: String? = nil, language: String? = nil) {
        self.path = path; self.content = content; self.language = language
    }
}
public struct LSPPosReq: Codable {
    public var path: String
    public var line: Int
    public var character: Int
    public init(path: String, line: Int, character: Int) {
        self.path = path; self.line = line; self.character = character
    }
}
public struct LSPHover: Codable {
    public var contents: String
}
public struct LSPDefinition: Codable {
    public var path: String
    public var line: Int
    public var character: Int
    public var found: Bool
}
public struct LSPDiagnostic: Codable, Identifiable, Hashable {
    public var id: String { "\(startLine):\(startChar)-\(endLine):\(endChar):\(message)" }
    public var startLine: Int
    public var startChar: Int
    public var endLine: Int
    public var endChar: Int
    public var severity: Int // 1=error 2=warning 3=info 4=hint
    public var message: String
    public var source: String?
    enum CodingKeys: String, CodingKey {
        case severity, message, source
        case startLine = "start_line"; case startChar = "start_char"
        case endLine = "end_line"; case endChar = "end_char"
    }
}
public struct LSPDiagnostics: Codable {
    public var path: String
    public var diagnostics: [LSPDiagnostic]
}
public struct LSPServerInfo: Codable {
    public var language: String
    public var installed: Bool
    public var installable: Bool
    public var installLabel: String
    enum CodingKeys: String, CodingKey {
        case language, installed, installable
        case installLabel = "install_label"
    }
}
public struct LSPInstallResult: Codable {
    public var ok: Bool
    public var installed: Bool
    public var message: String?
}
public struct LSPCompletionItem: Codable, Identifiable, Hashable {
    public var id: String { "\(label)\u{1}\(insert)\u{1}\(detail ?? "")" }
    public var label: String
    public var insert: String
    public var detail: String?
    public var kind: Int?
}
public struct LSPCompletion: Codable {
    public var items: [LSPCompletionItem]
}
public struct LSPFormatReq: Codable {
    public var path: String
    public var content: String
    public init(path: String, content: String) { self.path = path; self.content = content }
}
public struct LSPFormatResult: Codable {
    public var text: String
    public var changed: Bool
}
public struct LSPLocation: Codable, Identifiable, Hashable {
    public var id: String { "\(path):\(line):\(character)" }
    public var path: String
    public var line: Int
    public var character: Int
}
public struct LSPLocations: Codable {
    public var locations: [LSPLocation]
}
public struct LSPRenameReq: Codable {
    public var path: String
    public var line: Int
    public var character: Int
    public var newName: String
    public init(path: String, line: Int, character: Int, newName: String) {
        self.path = path; self.line = line; self.character = character; self.newName = newName
    }
    enum CodingKeys: String, CodingKey { case path, line, character; case newName = "new_name" }
}
public struct LSPRenameResult: Codable {
    public var files: [String]
    public var count: Int
}
public struct LSPSymbol: Codable, Identifiable, Hashable {
    public var id: String { "\(name):\(kind):\(line):\(character)" }
    public var name: String
    public var kind: Int
    public var detail: String?
    public var line: Int
    public var character: Int
    public var children: [LSPSymbol]?
}
public struct LSPSymbols: Codable {
    public var symbols: [LSPSymbol]
}

// MARK: - Multi-file search

public struct FSSearchReq: Codable {
    public var query: String
    public var sessionID: String?
    public var regex: Bool?
    public init(query: String, sessionID: String? = nil, regex: Bool? = nil) {
        self.query = query; self.sessionID = sessionID; self.regex = regex
    }
    enum CodingKeys: String, CodingKey { case query, regex; case sessionID = "session_id" }
}
public struct FSSearchHit: Codable, Identifiable, Hashable {
    public var id: String { "\(path):\(line):\(col)" }
    public var path: String
    public var line: Int
    public var col: Int
    public var text: String
}
public struct FSSearchResult: Codable {
    public var results: [FSSearchHit]
}
public struct FSWriteResult: Codable {
    public var path: String
    public var sha: String?
    public var mtime: Int?
    public var conflict: Bool?
}
public struct FSDiffReq: Codable {
    public var sessionID: String?
    public var path: String?
    public init(sessionID: String? = nil, path: String? = nil) { self.sessionID = sessionID; self.path = path }
    // Explicit CodingKeys (the envelope encoder isn't snake_case-converting) — otherwise `sessionID`
    // shipped verbatim, the daemon read "", and a session diff request had neither a session_id nor
    // a path, so the session-diff view always errored.
    enum CodingKeys: String, CodingKey { case path; case sessionID = "session_id" }
}
public struct FSDiff: Codable {
    public var path: String?
    public var diff: String
}
public struct FSChange: Codable {
    public var path: String
    public var sha: String?
}
/// Asks the daemon to fetch one resource from a session's dev server on our behalf.
///
/// There is no `url` field by design: the daemon derives the target from `sessionID` alone. A
/// client-supplied URL would make the daemon an open proxy inside the user's network.
public struct PreviewFetchReq: Codable {
    public var sessionID: String
    public var path: String
    public var method: String?
    public var headers: [String: String]?
    public var body: String? // base64
    public init(sessionID: String, path: String, method: String? = nil,
                headers: [String: String]? = nil, body: String? = nil) {
        self.sessionID = sessionID; self.path = path
        self.method = method; self.headers = headers; self.body = body
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case path, method, headers, body
    }
}

/// One whole HTTP response relayed back from the daemon host.
public struct PreviewFetchResp: Codable {
    public var status: Int
    public var headers: [String: String]?
    public var body: String // base64
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
/// One way an "Always" answer can be narrowed. The DAEMON computes these — the app never parses a
/// command itself, so scope semantics live in exactly one place and can't drift between clients.
public struct ApprovalScope: Codable, Equatable, Identifiable {
    public var kind: String     // tool | pattern | path | project
    public var value: String?
    public var label: String    // ready-to-render menu text
    public var id: String { kind + "|" + (value ?? "") }
    public init(kind: String, value: String? = nil, label: String) {
        self.kind = kind; self.value = value; self.label = label
    }
}
public struct ApprovalRequest: Codable, Equatable {
    public var approvalID: String; public var sessionID: String; public var tool: String; public var detail: String?
    /// The tool's raw arguments, when the harness provides them — shown in the card's "details"
    /// disclosure so the user can see exactly what they're approving, not just a truncated summary.
    public var input: JSONValue?
    public var suggestedScopes: [ApprovalScope]
    enum CodingKeys: String, CodingKey {
        case approvalID = "approval_id"; case sessionID = "session_id"; case tool; case detail
        case input; case suggestedScopes = "suggested_scopes"
    }
    public init(approvalID: String, sessionID: String, tool: String, detail: String? = nil,
                input: JSONValue? = nil, suggestedScopes: [ApprovalScope] = []) {
        self.approvalID = approvalID; self.sessionID = sessionID; self.tool = tool
        self.detail = detail; self.input = input; self.suggestedScopes = suggestedScopes
    }
    // Decode defensively: an older daemon sends neither new field.
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        approvalID = try c.decode(String.self, forKey: .approvalID)
        sessionID = try c.decode(String.self, forKey: .sessionID)
        tool = try c.decode(String.self, forKey: .tool)
        detail = try c.decodeIfPresent(String.self, forKey: .detail)
        input = try c.decodeIfPresent(JSONValue.self, forKey: .input)
        suggestedScopes = try c.decodeIfPresent([ApprovalScope].self, forKey: .suggestedScopes) ?? []
    }
}
public struct ApprovalRespond: Codable {
    public var approvalID: String; public var decision: String
    /// Only meaningful with decision == "always"; nil keeps the historical provider+tool rule.
    public var scope: ApprovalScope?
    public init(approvalID: String, decision: String, scope: ApprovalScope? = nil) {
        self.approvalID = approvalID; self.decision = decision; self.scope = scope
    }
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case decision; case scope }
}
public struct ApprovalResolved: Codable {
    public var approvalID: String; public var decision: String
    enum CodingKeys: String, CodingKey { case approvalID = "approval_id"; case decision }
}

/// An opaque JSON value — used to carry a generative-UI component's `props` at the transport layer
/// without the protocol knowing each component's shape. A component view decodes its typed props via
/// `props.decoded(TableProps.self)`, so adding a component never touches this file.
public indirect enum JSONValue: Codable, Equatable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let b = try? c.decode(Bool.self) { self = .bool(b) }
        else if let n = try? c.decode(Double.self) { self = .number(n) }
        else if let s = try? c.decode(String.self) { self = .string(s) }
        else if let a = try? c.decode([JSONValue].self) { self = .array(a) }
        else if let o = try? c.decode([String: JSONValue].self) { self = .object(o) }
        else { self = .null }
    }
    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }
    /// Re-encode this value and decode it into a typed struct (per-component props decoding).
    public func decoded<T: Decodable>(_ type: T.Type) -> T? {
        guard let data = try? JSONEncoder().encode(self) else { return nil }
        return try? JSONDecoder().decode(type, from: data)
    }

    /// Human-readable, key-sorted JSON for display (approval argument disclosure). A lone string
    /// renders as itself rather than a quoted one-element document. Returns nil for `.null`, so a
    /// caller can skip the whole affordance.
    public var prettyJSON: String? {
        if case .null = self { return nil }
        if case .string(let s) = self { return s }
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        guard let data = try? enc.encode(self), let s = String(data: data, encoding: .utf8) else { return nil }
        return s
    }
}

/// One tool call as a rich inline card: the invocation (name + title) is separated from its output,
/// updated in place by `id` as the tool goes running → completed.
public struct SessionTool: Codable, Equatable {
    public var sessionID: String
    public var id: String
    public var name: String
    public var title: String?
    public var output: String?
    public var status: String  // running | completed | error
    /// Lines added/removed by an edit, counted daemon-side. Both zero = unknown, render no badge.
    public var additions: Int?
    public var deletions: Int?
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", id, name, title, output, status, additions, deletions
    }
}

/// Announces a sub-agent's lifecycle under a parent session (e.g. opencode's `task` tool). The app
/// renders an inline collapsible card keyed by `id`; the child's output/tools then stream in tagged
/// with sessionID == id (routed into childMessages[id]).
public struct SubAgent: Codable {
    public var parentID: String
    public var id: String
    public var title: String?
    public var status: String  // started | done | error
    enum CodingKeys: String, CodingKey { case parentID = "parent_id", id, title, status }
}

/// A normalized generative-UI element (see the generative-UI plan). The daemon projects it from a
/// harness (tool events or a fenced ```iron:ui``` block); the client owns the native view. `props` is
/// inert, decoded per (component, schemaV); `fallbackText` renders when the component/schema is
/// unknown. `id` is stable within a message so a `running` skeleton updates in place to `ready`.
public struct UIComponent: Codable, Identifiable, Equatable {
    public var sessionID: String
    public var messageID: String?
    public var id: String
    public var component: String
    public var schemaV: Int
    public var status: String            // running | ready | error
    public var props: JSONValue?
    public var actions: [UIComponentAction]?
    public var fallbackText: String
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", messageID = "message_id", id, component
        case schemaV = "schema_v", status, props, actions, fallbackText = "fallback_text"
    }
}

/// An allow-listed interaction a component may offer. `kind` is a whitelisted verb the CLIENT runs —
/// never a command/RPC/URL: `prompt` (send a templated next user turn), `answer` (typed reply), or
/// `permission` (resolve an approval via the native ApprovalSheet).
public struct UIComponentAction: Codable, Identifiable, Equatable {
    public var id: String
    public var kind: String              // prompt | answer | permission
    public var label: String?
    public var style: String?            // default | destructive | cancel
    public var prompt: String?
    enum CodingKeys: String, CodingKey { case id, kind, label, style, prompt }
}

/// Sent when the user activates a component action. The daemon maps it to the next user turn
/// (prompt/answer) or resolves an approval (permission) — never a direct tool/destructive op.
public struct UIActionInvoke: Codable {
    public var sessionID: String
    public var messageID: String?
    public var componentID: String
    public var actionID: String
    public var kind: String
    public var prompt: String?
    public var values: JSONValue?
    public init(sessionID: String, messageID: String? = nil, componentID: String, actionID: String, kind: String, prompt: String? = nil, values: JSONValue? = nil) {
        self.sessionID = sessionID; self.messageID = messageID; self.componentID = componentID
        self.actionID = actionID; self.kind = kind; self.prompt = prompt; self.values = values
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", messageID = "message_id", componentID = "component_id"
        case actionID = "action_id", kind, prompt, values
    }
}
public struct Session: Codable, Identifiable {
    public var id: String
    public var provider: String
    public var status: String
    public var title: String?
    public var name: String?   // user-set label (takes precedence over title)
    public var projectID: String?
    public var cwd: String?
    public var workspaceName: String?
    public var branch: String?
    public var isWorkspace: Bool?
    public var parentID: String?
    public var subtask: String?
    public var port: Int?
    public var issueKey: String?
    public var issueID: String?
    public var model: String?          // active model id ("" = provider default)
    public var modelProvider: String?  // sub-provider/backend for the model
    public var mode: String?           // code | ask | architect ("" / nil = code)
    public var restartable: Bool?      // a "stopped" session that can be re-created (session.restart)
    public var updatedAt: Int? // unix seconds of last activity
    public var inputTokens: Int?
    public var outputTokens: Int?
    public var costUSD: Double?
    public var conflicted: Bool? // worktree branch would conflict with the default branch
    public var fanoutGroup: String?  // shared id when this is one of N agents racing the same prompt
    public var fanoutVariant: Int?   // 0-based variant index within the fan-out group
    public var ephemeral: Bool?      // scratch "just chat" session (no project, not persisted)
    /// Where the agent process runs: nil/"" is this Mac, "ssh" a remote host named by `execHost`.
    /// Absent means local by construction — a daemon predating the field sends nothing, and every
    /// session such a daemon has is local, so there is no "unknown" state to render a hedge for.
    /// Carried separately from `name` because a rename must not be able to erase where work runs.
    public var execKind: String?
    public var execHost: String?
    /// The session's dev server as a stable NAME — "http://fix-login.localhost:7777" — rather than a
    /// port that changes between runs.
    ///
    /// Distinct from `port`, which is only set when a setup hook allocated one. The daemon detects
    /// whatever port the dev server actually chose and registers a name for it, so this is populated
    /// for an ordinary `npm run dev` where `port` is nil. Prefer it over `port` wherever both could
    /// serve: dropping it is what made Design Mode fall back to a hardcoded localhost:3000.
    public var previewURL: String?
    enum CodingKeys: String, CodingKey {
        case id, provider, status, title, name, cwd, branch, port, model, mode, restartable, conflicted, ephemeral
        case previewURL = "preview_url"
        case execKind = "exec_kind"
        case execHost = "exec_host"
        case fanoutGroup = "fanout_group"
        case fanoutVariant = "fanout_variant"
        case projectID = "project_id"
        case workspaceName = "workspace_name"
        case isWorkspace = "is_workspace"
        case parentID = "parent_id"
        case subtask
        case issueKey = "issue_key"
        case issueID = "issue_id"
        case modelProvider = "model_provider"
        case updatedAt = "updated_at"
        case inputTokens = "input_tokens"
        case outputTokens = "output_tokens"
        case costUSD = "cost_usd"
    }
}
public struct ProtocolError: Codable { public var message: String }
public struct SessionList: Codable { public var sessions: [Session] }

// Usage + to-dos (agent observability events).
public struct SessionUsage: Codable {
    public var sessionID: String
    public var inputTokens: Int
    public var outputTokens: Int
    public var costUSD: Double
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case inputTokens = "input_tokens"
        case outputTokens = "output_tokens"
        case costUSD = "cost_usd"
    }
}
public struct Todo: Codable, Identifiable, Hashable {
    public var id: String { "\(status):\(content)" }
    public var content: String
    public var status: String // pending | in_progress | completed
}
public struct SessionTodos: Codable {
    public var sessionID: String
    public var todos: [Todo]
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case todos }
}

// Heartbeat supervision: derived on-track state for a session (event), and the client→daemon
// toggle to opt a session into (or out of) autonomous nudging.
public struct SessionHeartbeat: Codable {
    public var sessionID: String
    public var state: String // working|awaiting_input|idle_incomplete|stalled|done|errored|exhausted
    public var nudgeCount: Int
    public var todosDone: Int
    public var todosTotal: Int
    public var costUSD: Double
    public var budgetUSD: Double
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case state
        case nudgeCount = "nudge_count"
        case todosDone = "todos_done"
        case todosTotal = "todos_total"
        case costUSD = "cost_usd"
        case budgetUSD = "budget_usd"
    }
}
public struct SessionChild: Codable {
    public var parentSessionID: String
    public var subtask: String
    public var files: [String]?
    /// The harness to run this subtask on. Nil inherits the parent's — the point of setting it is
    /// to route a subtask to a DIFFERENT harness than the one you're talking to.
    public var provider: String?
    /// The model within that harness. Distinct from `provider` because "delegate this to Claude"
    /// and "delegate this to Claude on a small model" are different asks.
    public var model: String?
    public var modelProvider: String?
    public var autonomous: Bool?
    /// Give the child its own git worktree instead of sharing the parent's directory. Required for
    /// concurrent children: two agents in one tree edit the same files underneath each other, and
    /// neither is told.
    public var worktree: Bool?
    public init(parentSessionID: String, subtask: String, files: [String]? = nil, provider: String? = nil,
                model: String? = nil, modelProvider: String? = nil, autonomous: Bool? = nil,
                worktree: Bool? = nil) {
        self.parentSessionID = parentSessionID; self.subtask = subtask; self.files = files
        self.provider = provider; self.model = model; self.modelProvider = modelProvider
        self.autonomous = autonomous; self.worktree = worktree
    }
    enum CodingKeys: String, CodingKey {
        case parentSessionID = "parent_session_id"
        case modelProvider = "model_provider"
        case subtask, files, provider, model, autonomous, worktree
    }
}
public struct HandoffEntry: Codable, Identifiable, Hashable {
    public var id: String { sessionID }
    public var sessionID: String
    public var cwd: String
    public var path: String
    public var title: String
    public var summary: String
    public var updatedAt: Int
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case cwd, path, title, summary
        case updatedAt = "updated_at"
    }
}
public struct HandoffList: Codable {
    public var cwd: String?
    public var handoffs: [HandoffEntry]
    public init(cwd: String? = nil, handoffs: [HandoffEntry] = []) { self.cwd = cwd; self.handoffs = handoffs }
}
public struct SessionAutonomy: Codable {
    public var sessionID: String
    public var autonomous: Bool
    public var maxNudges: Int?
    public var budgetUSD: Double?
    public init(sessionID: String, autonomous: Bool, maxNudges: Int? = nil, budgetUSD: Double? = nil) {
        self.sessionID = sessionID; self.autonomous = autonomous; self.maxNudges = maxNudges; self.budgetUSD = budgetUSD
    }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"
        case autonomous
        case maxNudges = "max_nudges"
        case budgetUSD = "budget_usd"
    }
}
public struct RunTest: Codable {
    public var sessionID: String
    public var command: String?
    public init(sessionID: String, command: String? = nil) { self.sessionID = sessionID; self.command = command }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case command }
}
public struct RunOutput: Codable {
    public var sessionID: String
    public var line: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case line }
}
public struct RunResult: Codable {
    public var sessionID: String
    public var command: String
    public var ok: Bool
    public var exitCode: Int
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case command; case ok; case exitCode = "exit_code" }
}

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
public struct ProjectBrowseReq: Codable {
    public var path: String?
    public init(path: String?) { self.path = path }
}
public struct CommandListReq: Codable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" } // envelope encoder isn't snake_case
}
public struct SlashCommand: Codable, Identifiable, Hashable {
    public var name: String
    public var description: String?
    public var source: String?
    public var prefix: String?          // "/" (default) or "$" (codex skills)
    public var id: String { (prefix ?? "/") + name }
    public var isCustom: Bool { source == "custom" }
    public var glyph: String { prefix ?? "/" }
}
public struct CommandList: Codable { public var commands: [SlashCommand] }

/// A recurring autonomous workflow: watch a tracker for new tickets in a category and start an
/// agent on each.
public struct Loop: Codable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var enabled: Bool
    public var provider: String
    public var kind: String            // "ticket" (default) | "task"
    public var projectID: String       // legacy single repo
    public var projectIDs: [String]    // one or more repos (multi-root)
    public var triggerCategory: String
    public var tracker: String?
    public var prompt: String          // task kind: the recurring job
    public var intervalMinutes: Int    // task kind: schedule between runs
    public var lastRun: Int            // task kind: unix seconds of last run (read-only)
    public var worktree: Bool
    public var plan: Bool
    public var budgetUSD: Double
    public var maxConcurrent: Int
    public init(id: String = "", name: String = "", enabled: Bool = true, provider: String = "opencode",
                kind: String = "ticket", projectID: String = "", projectIDs: [String] = [],
                triggerCategory: String = "todo", tracker: String? = nil,
                prompt: String = "", intervalMinutes: Int = 360, lastRun: Int = 0,
                worktree: Bool = true, plan: Bool = true, budgetUSD: Double = 5, maxConcurrent: Int = 1) {
        self.id = id; self.name = name; self.enabled = enabled; self.provider = provider; self.kind = kind
        self.projectID = projectID; self.projectIDs = projectIDs
        self.triggerCategory = triggerCategory; self.tracker = tracker
        self.prompt = prompt; self.intervalMinutes = intervalMinutes; self.lastRun = lastRun
        self.worktree = worktree; self.plan = plan
        self.budgetUSD = budgetUSD; self.maxConcurrent = maxConcurrent
    }
    // Effective repo list (migrates the legacy single field).
    public var repos: [String] { projectIDs.isEmpty ? (projectID.isEmpty ? [] : [projectID]) : projectIDs }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
        provider = try c.decodeIfPresent(String.self, forKey: .provider) ?? "opencode"
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "ticket"
        projectID = try c.decodeIfPresent(String.self, forKey: .projectID) ?? ""
        projectIDs = try c.decodeIfPresent([String].self, forKey: .projectIDs) ?? []
        triggerCategory = try c.decodeIfPresent(String.self, forKey: .triggerCategory) ?? "todo"
        tracker = try c.decodeIfPresent(String.self, forKey: .tracker)
        prompt = try c.decodeIfPresent(String.self, forKey: .prompt) ?? ""
        intervalMinutes = try c.decodeIfPresent(Int.self, forKey: .intervalMinutes) ?? 360
        lastRun = try c.decodeIfPresent(Int.self, forKey: .lastRun) ?? 0
        worktree = try c.decodeIfPresent(Bool.self, forKey: .worktree) ?? true
        plan = try c.decodeIfPresent(Bool.self, forKey: .plan) ?? true
        budgetUSD = try c.decodeIfPresent(Double.self, forKey: .budgetUSD) ?? 5
        maxConcurrent = try c.decodeIfPresent(Int.self, forKey: .maxConcurrent) ?? 1
    }
    enum CodingKeys: String, CodingKey {
        case id, name, enabled, provider, kind, tracker, prompt, worktree, plan
        case projectID = "project_id", projectIDs = "project_ids"
        case triggerCategory = "trigger_category", intervalMinutes = "interval_minutes", lastRun = "last_run"
        case budgetUSD = "budget_usd", maxConcurrent = "max_concurrent"
    }
}
public struct LoopRun: Codable, Identifiable, Hashable {
    public var loopID: String
    public var issueKey: String
    public var issueTitle: String
    public var sessionID: String
    public var status: String
    public var startedAt: Int
    public var id: String { loopID + issueKey + sessionID }
    enum CodingKeys: String, CodingKey {
        case status
        case loopID = "loop_id", issueKey = "issue_key", issueTitle = "issue_title", sessionID = "session_id", startedAt = "started_at"
    }
}
public struct LoopList: Codable { public var loops: [Loop]; public var runs: [LoopRun] }
public struct LoopRef: Codable { public var id: String; public init(id: String) { self.id = id } }
public struct LoopSetEnabled: Codable {
    public var id: String; public var enabled: Bool
    public init(id: String, enabled: Bool) { self.id = id; self.enabled = enabled }
}
public struct ProjectDirEntry: Codable, Identifiable, Hashable {
    public var name: String
    public var path: String
    public var isGitRepo: Bool
    public var id: String { path }
    enum CodingKeys: String, CodingKey { case name, path; case isGitRepo = "is_git_repo" }
}
public struct ProjectBrowse: Codable {
    public var path: String
    public var parent: String?
    public var entries: [ProjectDirEntry]
}
public struct ProjectRef: Codable {
    public var projectID: String
    public init(projectID: String) { self.projectID = projectID }
    enum CodingKeys: String, CodingKey { case projectID = "project_id" }
}
public struct ProjectList: Codable { public var projects: [Project] }
public struct ProviderList: Codable {
    public var providers: [String]
    public init(providers: [String] = []) { self.providers = providers }
}

/// One agent in the roster. `kind` is "native" (opencode/claude-code/pi — not editable), "detected"
/// (a well-known CLI auto-found on PATH), or "custom" (user-defined — editable/removable).
public struct AgentInfo: Codable, Identifiable, Hashable {
    public var name: String
    public var kind: String
    public var available: Bool
    public var editable: Bool
    public var hidden: Bool
    public var command: String
    public var args: [String]
    public var resumeArgs: [String]
    public var models: [String]
    public var env: [String: String]
    public var id: String { name }
    public init(name: String = "", kind: String = "custom", available: Bool = false, editable: Bool = true,
                hidden: Bool = false, command: String = "", args: [String] = [], resumeArgs: [String] = [], models: [String] = [], env: [String: String] = [:]) {
        self.name = name; self.kind = kind; self.available = available; self.editable = editable
        self.hidden = hidden; self.command = command; self.args = args; self.resumeArgs = resumeArgs; self.models = models; self.env = env
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "custom"
        available = try c.decodeIfPresent(Bool.self, forKey: .available) ?? false
        editable = try c.decodeIfPresent(Bool.self, forKey: .editable) ?? false
        hidden = try c.decodeIfPresent(Bool.self, forKey: .hidden) ?? false
        command = try c.decodeIfPresent(String.self, forKey: .command) ?? ""
        args = try c.decodeIfPresent([String].self, forKey: .args) ?? []
        resumeArgs = try c.decodeIfPresent([String].self, forKey: .resumeArgs) ?? []
        models = try c.decodeIfPresent([String].self, forKey: .models) ?? []
        env = try c.decodeIfPresent([String: String].self, forKey: .env) ?? [:]
    }
    enum CodingKeys: String, CodingKey {
        case name, kind, available, editable, hidden, command, args, models, env
        case resumeArgs = "resume_args"
    }
}
public struct AgentList: Codable { public var agents: [AgentInfo] }

/// Add/edit a custom CLI agent. Args templates may contain {prompt} and {cwd}.
public struct AgentUpsert: Codable {
    public var name: String
    public var command: String
    public var args: [String]
    public var resumeArgs: [String]?
    public var models: [String]?
    public var env: [String: String]?   // e.g. point to a config file: OPENCODE_CONFIG=/path
    public init(name: String, command: String, args: [String] = [], resumeArgs: [String]? = nil, models: [String]? = nil, env: [String: String]? = nil) {
        self.name = name; self.command = command; self.args = args; self.resumeArgs = resumeArgs; self.models = models; self.env = env
    }
    enum CodingKeys: String, CodingKey { case name, command, args, models, env; case resumeArgs = "resume_args" }
}
public struct AgentRef: Codable {
    public var name: String
    public init(name: String) { self.name = name }
}
public struct AgentVisible: Codable {
    public var name: String
    public var visible: Bool
    public init(name: String, visible: Bool) { self.name = name; self.visible = visible }
}

/// One selectable model for a provider. `provider` is the sub-provider/backend opencode pairs with
/// the model id; empty for providers that take a bare model string.
public struct ModelInfo: Codable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var provider: String
    public init(id: String = "", name: String = "", provider: String = "") {
        self.id = id; self.name = name; self.provider = provider
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        provider = try c.decodeIfPresent(String.self, forKey: .provider) ?? ""
    }
    enum CodingKeys: String, CodingKey { case id, name, provider }
}
public struct ModelListReq: Codable {
    public var provider: String?
    public var sessionID: String?
    public init(provider: String? = nil, sessionID: String? = nil) { self.provider = provider; self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case provider; case sessionID = "session_id" }
}
public struct ModelList: Codable {
    public var models: [ModelInfo]
    public var current: String?
    public var editable: Bool
    public init(models: [ModelInfo] = [], current: String? = nil, editable: Bool = false) {
        self.models = models; self.current = current; self.editable = editable
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        models = try c.decodeIfPresent([ModelInfo].self, forKey: .models) ?? []
        current = try c.decodeIfPresent(String.self, forKey: .current)
        editable = try c.decodeIfPresent(Bool.self, forKey: .editable) ?? false
    }
    enum CodingKeys: String, CodingKey { case models, current, editable }
}
public struct SessionSetModel: Codable {
    public var sessionID: String
    public var model: String
    public var provider: String?
    public init(sessionID: String, model: String, provider: String? = nil) {
        self.sessionID = sessionID; self.model = model; self.provider = provider
    }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case model, provider }
}

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
public struct WorkspaceMemberDiff: Codable, Identifiable, Hashable {
    public var id: String { name }
    public var name: String
    public var branch: String
    public var diff: String
}
public struct WorkspaceDiff: Codable {
    public var sessionID: String
    public var members: [WorkspaceMemberDiff]?
    public init(sessionID: String, members: [WorkspaceMemberDiff]? = nil) { self.sessionID = sessionID; self.members = members }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case members }
}
public struct WorkspaceMemberPR: Codable, Identifiable, Hashable {
    public var id: String { name }
    public var name: String
    public var branch: String
    public var pushed: Bool
    public var url: String?
    public var skipped: String?
    public var error: String?
}
public struct WorkspacePR: Codable {
    public var sessionID: String
    public var title: String
    public var body: String?
    public var members: [WorkspaceMemberPR]?
    public init(sessionID: String, title: String, body: String? = nil, members: [WorkspaceMemberPR]? = nil) {
        self.sessionID = sessionID; self.title = title; self.body = body; self.members = members
    }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case title, body, members }
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
public struct WorktreeCatchUp: Codable {
    public var sessionID: String
    public var status: String?   // "updated" | "up_to_date" | "conflicts"
    public var base: String?
    public var message: String?
    public var conflicts: [String]?
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case status; case base; case message; case conflicts }
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
public struct Telemetry: Codable {
    public var enabled: Bool
    public init(enabled: Bool) { self.enabled = enabled }
}

/// One Atlassian site (cloud) the Jira OAuth token can reach.
public struct JiraSite: Codable, Identifiable, Equatable {
    public var id: String { cloudID }
    public var cloudID: String
    public var name: String
    public var url: String
    enum CodingKeys: String, CodingKey { case cloudID = "id"; case name; case url }
}
public struct JiraSites: Codable { public var sites: [JiraSite]; public var current: String? }
public struct JiraSetSite: Codable {
    public var cloudID: String
    public init(cloudID: String) { self.cloudID = cloudID }
    enum CodingKeys: String, CodingKey { case cloudID = "cloud_id" }
}

/// A live step during session creation (drives the prescriptive loading checklist).
public struct SessionProgress: Codable {
    public var stage: String
    public var detail: String
    public var step: Int?
    public var total: Int?
}

/// Reply to log.subscribe: the daemon's recently-buffered log lines (replayed on connect).
public struct LogHistory: Codable {
    public var lines: [String]
}

/// One streamed daemon log line (log.line event).
public struct LogLine: Codable {
    public var line: String
}

/// One cross-session activity item — the Activity feed, Needs-You inbox, and ticker all read these.
public struct ActivityEvent: Codable, Identifiable, Equatable {
    public var id: String
    public var ts: Int
    public var kind: String        // finished | needs_input | error | loop_run | loop_pr | started
    public var sessionID: String?
    public var provider: String?
    public var project: String?
    public var title: String
    public var detail: String?
    public var needsYou: Bool
    public var read: Bool
    enum CodingKeys: String, CodingKey {
        case id; case ts; case kind; case sessionID = "session_id"; case provider
        case project; case title; case detail; case needsYou = "needs_you"; case read
    }
}

/// Reply to activity.list: the recent feed (oldest first).
public struct ActivityList: Codable {
    public var events: [ActivityEvent]
}

/// activity.markread payload; empty IDs = mark all read.
public struct ActivityMarkRead: Codable {
    public var ids: [String]?
    public init(ids: [String]? = nil) { self.ids = ids }
}

/// fanout.create — spawn N agents on the SAME prompt in isolated worktrees.
public struct FanoutCreate: Codable {
    public var provider: String
    public var projectID: String?
    public var projectIDs: [String]?
    public var prompt: String
    public var plan: Bool?
    /// Ask a fresh agent to recommend a winner once every variant finishes. Advisory only.
    public var judge: Bool?
    /// Turns a RACE into a DIVISION of labour: each agent gets its own subtask. `count` is ignored
    /// when this is set — the list defines the fan.
    public var prompts: [String]?
    public var count: Int
    public var models: [String]?
    enum CodingKeys: String, CodingKey {
        case provider; case projectID = "project_id"; case projectIDs = "project_ids"
        case prompt; case plan; case judge; case prompts; case count; case models
    }
    public init(provider: String, projectID: String? = nil, projectIDs: [String]? = nil, prompt: String, plan: Bool? = nil, judge: Bool? = nil, prompts: [String]? = nil, count: Int, models: [String]? = nil) {
        self.provider = provider; self.projectID = projectID; self.projectIDs = projectIDs
        self.prompt = prompt; self.plan = plan; self.judge = judge; self.prompts = prompts
        self.count = count; self.models = models
    }
}

public struct FanoutResult: Codable {
    public var group: String
    public var sessionIDs: [String]
    enum CodingKeys: String, CodingKey { case group; case sessionIDs = "session_ids" }
}

/// fanout.resolve — end a fan-out group: tear down every variant except `keep` (the winner) + its
/// worktree, so a decided race doesn't leave orphaned worktrees/sessions accumulating.
public struct FanoutResolve: Codable {
    public var group: String
    public var keep: String?
    public var force: Bool?
    enum CodingKeys: String, CodingKey { case group; case keep; case force }
    public init(group: String, keep: String? = nil, force: Bool? = nil) {
        self.group = group; self.keep = keep; self.force = force
    }
}

public struct FanoutResolved: Codable {
    public var group: String
    public var kept: String?
    public var removed: [String]
    public var failed: [String]?
    enum CodingKeys: String, CodingKey { case group; case kept; case removed; case failed }
}

/// One sub-agent's state within a parent turn (turn.state children). `lastEventAt` is the child's
/// OWN clock: the parent turn's is bumped by any child, so it can't say whether a particular one is
/// still alive — which is how a fan-out with one chatty worker hid nine stalled ones.
public struct TurnChild: Codable, Identifiable, Equatable {
    public var id: String
    public var state: String // running | stalled | done | error
    public var title: String?
    public var startedAt: Int?
    public var lastEventAt: Int?
    enum CodingKeys: String, CodingKey {
        case id; case state; case title
        case startedAt = "started_at"; case lastEventAt = "last_event_at"
    }
}

/// A tool call the turn has started and not yet finished (turn.state tools). The daemon tracks these
/// so it can seal them when the turn ends — and so "busy but nothing has progressed in four minutes"
/// is answerable at all.
public struct TurnTool: Codable, Identifiable, Equatable {
    public var id: String
    public var name: String
    public var title: String?
    public var startedAt: Int?
    enum CodingKeys: String, CodingKey { case id; case name; case title; case startedAt = "started_at" }
}

/// The daemon's authoritative truth about a session's current turn — pushed on every transition and
/// as a ~10s heartbeat while a turn is open. The client renders this verbatim and runs no liveness
/// timers of its own.
public struct TurnState: Codable, Equatable {
    public var sessionID: String
    public var turnID: String
    public var state: String // running | awaiting_approval | stalled | idle | error | needs_you | abandoned
    public var startedAt: Int?
    public var lastEventAt: Int?
    public var detail: String?
    public var reason: String?
    public var children: [TurnChild]?
    public var tools: [TurnTool]?
    /// Nudges the daemon has spent trying to get THIS turn moving again (stalled turns only).
    public var nudges: Int?
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id"; case turnID = "turn_id"; case state
        case startedAt = "started_at"; case lastEventAt = "last_event_at"
        case detail; case reason; case children; case tools; case nudges
    }
}

/// One toggleable push-notification type. NotifyPrefs is the labeled catalog with each type's state.
public struct NotifyPref: Codable, Identifiable {
    public var key: String
    public var label: String
    public var detail: String?
    public var enabled: Bool
    public var id: String { key }
    enum CodingKeys: String, CodingKey { case key; case label; case detail; case enabled }
}
public struct NotifyPrefs: Codable {
    public var prefs: [NotifyPref]
    enum CodingKeys: String, CodingKey { case prefs }
}
public struct NotifyPrefSet: Codable {
    public var key: String
    public var enabled: Bool
    enum CodingKeys: String, CodingKey { case key; case enabled }
    public init(key: String, enabled: Bool) { self.key = key; self.enabled = enabled }
}

/// A restore point: a snapshot of a session's worktree at a point in time.
public struct Checkpoint: Codable, Identifiable, Equatable {
    public var sha: String
    public var label: String
    public var ts: Int
    public var id: String { sha }
}
public struct CheckpointList: Codable { public var checkpoints: [Checkpoint] }
public struct CheckpointCreate: Codable {
    public var sessionID: String; public var label: String?
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case label }
    public init(sessionID: String, label: String? = nil) { self.sessionID = sessionID; self.label = label }
}
public struct CheckpointRestore: Codable {
    public var sessionID: String; public var sha: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case sha }
    public init(sessionID: String, sha: String) { self.sessionID = sessionID; self.sha = sha }
}

/// A named credential set for a provider (env overrides = API keys / config dirs).
public struct Account: Codable, Identifiable, Equatable {
    public var id: String
    public var provider: String
    public var name: String
    public var env: [String: String]?
    public var active: Bool?
    public init(id: String = "", provider: String, name: String, env: [String: String]? = nil, active: Bool? = nil) {
        self.id = id; self.provider = provider; self.name = name; self.env = env; self.active = active
    }
}

/// Rolled-up token/cost usage for one provider (the usage meter).
public struct ProviderUsage: Codable, Identifiable {
    public var provider: String
    public var sessions: Int
    public var inputTokens: Int
    public var outputTokens: Int
    public var costUSD: Double
    public var id: String { provider }
    enum CodingKeys: String, CodingKey {
        case provider; case sessions; case inputTokens = "input_tokens"
        case outputTokens = "output_tokens"; case costUSD = "cost_usd"
    }
}

public struct AccountList: Codable {
    public var accounts: [Account]
    public var usage: [ProviderUsage]
}
public struct AccountActivate: Codable {
    public var provider: String; public var accountID: String
    enum CodingKeys: String, CodingKey { case provider; case accountID = "account_id" }
    public init(provider: String, accountID: String) { self.provider = provider; self.accountID = accountID }
}

/// An account's remaining rate-limit/quota, probed from the provider API.
public struct AccountQuota: Codable, Equatable {
    public var accountID: String
    public var available: Bool
    public var requestsRemaining: Int
    public var tokensRemaining: Int
    public var resetInSeconds: Int?
    public var note: String?
    enum CodingKeys: String, CodingKey {
        case accountID = "account_id"; case available; case requestsRemaining = "requests_remaining"
        case tokensRemaining = "tokens_remaining"; case resetInSeconds = "reset_in_seconds"; case note
    }
}
public struct AccountRef: Codable {
    public var accountID: String
    enum CodingKeys: String, CodingKey { case accountID = "account_id" }
    public init(accountID: String) { self.accountID = accountID }
}

/// A local↔remote port tunnel (ssh -L) so a remote dev server is reachable at localhost.
public struct PortForward: Codable, Equatable {
    public var localPort: Int
    public var remotePort: Int
    enum CodingKeys: String, CodingKey { case localPort = "local_port"; case remotePort = "remote_port" }
    public init(localPort: Int, remotePort: Int) { self.localPort = localPort; self.remotePort = remotePort }
}

/// A registered SSH remote where a worktree/agent can run.
public struct RemoteHost: Codable, Identifiable, Equatable {
    public var id: String
    public var name: String
    public var sshTarget: String
    public var remotePath: String
    public var reachable: Bool?
    public var forwards: [PortForward]?
    enum CodingKeys: String, CodingKey {
        case id; case name; case sshTarget = "ssh_target"; case remotePath = "remote_path"; case reachable; case forwards
    }
    public init(id: String = "", name: String, sshTarget: String, remotePath: String, reachable: Bool? = nil, forwards: [PortForward]? = nil) {
        self.id = id; self.name = name; self.sshTarget = sshTarget; self.remotePath = remotePath; self.reachable = reachable; self.forwards = forwards
    }
}
public struct RemoteList: Codable { public var hosts: [RemoteHost] }
public struct RemoteRef: Codable { public var id: String; public init(id: String) { self.id = id } }
public struct RemoteStatus: Codable {
    public var id: String
    public var status: String
    public var diff: String
    public var error: String?
}
public struct RemoteRun: Codable {
    public var hostID: String
    public var agentCommand: String
    public var prompt: String?
    enum CodingKeys: String, CodingKey { case hostID = "host_id"; case agentCommand = "agent_command"; case prompt }
    public init(hostID: String, agentCommand: String, prompt: String? = nil) {
        self.hostID = hostID; self.agentCommand = agentCommand; self.prompt = prompt
    }
}

public struct IntegrationStatus: Codable {
    public var connected: [String]
    public var oauthApps: [String]?   // providers with an OAuth app configured
    public var authErrors: [String]?  // connected providers whose fetch/refresh is failing (need reconnect)
    public var authErrorDetails: [String: String]? // provider -> the actual failure message (why it isn't loading)
    /// The Jira token reaches several Atlassian sites, so the active one was GUESSED.
    ///
    /// Atlassian never reports which site you chose at consent, so the daemon picks the first it can
    /// see. Get it wrong and the board shows a real, populated project — just not yours — which is
    /// the worst kind of wrong, because nothing looks broken.
    public var jiraSiteAmbiguous: Bool?
    enum CodingKeys: String, CodingKey {
        case connected; case oauthApps = "oauth_apps"; case authErrors = "auth_errors"
        case authErrorDetails = "auth_error_details"
        case jiraSiteAmbiguous = "jira_site_ambiguous"
    }
}
public struct IntegrationOAuth: Codable {
    public var provider: String; public var url: String?
    public init(provider: String, url: String? = nil) { self.provider = provider; self.url = url }
}
public struct IntegrationOAuthApp: Codable {
    public var provider: String
    public var clientID: String
    public var clientSecret: String
    public init(provider: String, clientID: String, clientSecret: String) {
        self.provider = provider; self.clientID = clientID; self.clientSecret = clientSecret
    }
    enum CodingKeys: String, CodingKey {
        case provider
        case clientID = "client_id"
        case clientSecret = "client_secret"
    }
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
    public var cycleID: String?
    public var cycleName: String?
    public var cycleNumber: Int?
    public var teamName: String?
    public var sprintName: String?
    public var sprintState: String?
    // Editable-field detail (populated by issue.detail; drives full two-way editing).
    public var assigneeID: String?
    public var labels: [IssueLabel]?
    public var estimate: Double?
    public var dueDate: String?
    enum CodingKeys: String, CodingKey {
        case id, key, title, body, status, category, assignee, url, provider, priority, labels, estimate
        case branchName = "branch_name"
        case teamID = "team_id"
        case updatedAt = "updated_at"
        case cycleID = "cycle_id"
        case cycleName = "cycle_name"
        case cycleNumber = "cycle_number"
        case teamName = "team_name"
        case sprintName = "sprint_name"
        case sprintState = "sprint_state"
        case assigneeID = "assignee_id"
        case dueDate = "due_date"
    }
    /// Display label for the issue's cycle/sprint, e.g. "Cycle 12" or a named cycle.
    public var cycleLabel: String? {
        if let n = cycleName, !n.isEmpty { return n }
        if let num = cycleNumber, num > 0 { return "Cycle \(num)" }
        return nil
    }
}

extension Session {
    /// A name derived from the working tree — the cwd's folder (or workspace name), with the worktree
    /// branch appended when present. Used to auto-name a session that has no user/provider title,
    /// instead of a meaningless "ses a1b2c3". Returns nil if no folder is known.
    public var folderName: String? {
        var folder: String?
        if let w = workspaceName, !w.isEmpty {
            folder = w
        } else if let c = cwd, !c.isEmpty {
            let last = (c as NSString).lastPathComponent
            folder = (last.isEmpty || last == "/") ? nil : last
        }
        guard let f = folder else { return nil }
        if let b = branch, !b.isEmpty { return "\(f) · \(b)" }
        return f
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

// MARK: - Ticket editor pickers: members / labels / cycles

public struct IssueUser: Codable, Identifiable, Hashable {
    public var id: String; public var name: String; public var email: String?; public var avatar: String?
}
public struct IssueLabel: Codable, Identifiable, Hashable {
    public var id: String; public var name: String; public var color: String?
}
public struct IssueCycle: Codable, Identifiable, Hashable {
    public var id: String; public var name: String; public var number: Int?; public var state: String?
}
public struct IssueMembersReq: Codable {
    public var provider: String; public var teamID: String; public var issueID: String?
    public init(provider: String, teamID: String, issueID: String? = nil) { self.provider = provider; self.teamID = teamID; self.issueID = issueID }
    enum CodingKeys: String, CodingKey { case provider; case teamID = "team_id"; case issueID = "issue_id" }
}
public struct IssueMemberList: Codable { public var members: [IssueUser] }
public struct IssueLabelsReq: Codable {
    public var provider: String; public var teamID: String
    public init(provider: String, teamID: String) { self.provider = provider; self.teamID = teamID }
    enum CodingKeys: String, CodingKey { case provider; case teamID = "team_id" }
}
public struct IssueLabelList: Codable { public var labels: [IssueLabel] }
public struct IssueCyclesReq: Codable {
    public var provider: String; public var teamID: String
    public init(provider: String, teamID: String) { self.provider = provider; self.teamID = teamID }
    enum CodingKeys: String, CodingKey { case provider; case teamID = "team_id" }
}
public struct IssueCycleList: Codable { public var cycles: [IssueCycle] }

// MARK: - Kanban board: columns / move / create / projects

/// Request the ordered workflow-status columns for a project's board.
public struct IssueColumnsReq: Codable {
    public var provider: String; public var project: String
    public init(provider: String, project: String) { self.provider = provider; self.project = project }
    enum CodingKeys: String, CodingKey { case provider, project }
}

/// Move a card to a workflow status.
public struct IssueMove: Codable {
    public var provider: String; public var issueID: String; public var statusID: String
    public init(provider: String, issueID: String, statusID: String) {
        self.provider = provider; self.issueID = issueID; self.statusID = statusID
    }
    enum CodingKeys: String, CodingKey { case provider; case issueID = "issue_id"; case statusID = "status_id" }
}

/// Create a new ticket on a project's board.
public struct IssueCreate: Codable {
    public var provider: String; public var project: String; public var title: String
    public var description: String?; public var priority: Int?; public var type: String?
    public init(provider: String, project: String, title: String, description: String? = nil, priority: Int? = nil, type: String? = nil) {
        self.provider = provider; self.project = project; self.title = title
        self.description = description; self.priority = priority; self.type = type
    }
    enum CodingKeys: String, CodingKey { case provider, project, title, description, priority, type }
}

/// A selectable board/project.
public struct IssueProject: Codable, Identifiable, Hashable {
    public var id: String; public var name: String; public var provider: String
    public init(id: String, name: String, provider: String) { self.id = id; self.name = name; self.provider = provider }
    enum CodingKeys: String, CodingKey { case id, name, provider }
}
public struct IssueProjectsList: Codable { public var projects: [IssueProject] }
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

// MARK: - Issue detail / edit / comments / images

public struct IssueComment: Codable, Identifiable, Hashable {
    public var id: String
    public var author: String?
    public var body: String
    public var createdAt: String?
    public init(id: String, author: String? = nil, body: String, createdAt: String? = nil) {
        self.id = id; self.author = author; self.body = body; self.createdAt = createdAt
    }
    enum CodingKeys: String, CodingKey { case id, author, body; case createdAt = "created_at" }
}

public struct IssueDetailReq: Codable {
    public var provider: String; public var issueID: String
    public init(provider: String, issueID: String) { self.provider = provider; self.issueID = issueID }
    enum CodingKeys: String, CodingKey { case provider; case issueID = "issue_id" }
}

public struct IssueDetail: Codable {
    public var issue: Issue
    public var comments: [IssueComment]
    public var attachments: [IssueAttachment]?
}

/// One file/image attached to an issue.
public struct IssueAttachment: Codable, Identifiable, Hashable {
    public var id: String
    public var filename: String
    public var url: String
    public var mime: String
    public var size: Int?
    public var isImage: Bool
    public init(id: String, filename: String, url: String, mime: String, size: Int? = nil, isImage: Bool = false) {
        self.id = id; self.filename = filename; self.url = url; self.mime = mime; self.size = size; self.isImage = isImage
    }
    enum CodingKeys: String, CodingKey {
        case id, filename, url, mime, size
        case isImage = "is_image"
    }
}

/// Partial edit — only non-nil fields are applied server-side.
public struct IssueUpdate: Codable {
    public var provider: String; public var issueID: String
    public var title: String?; public var description: String?
    public var stateID: String?; public var priority: Int?
    // A PRESENT value is applied; nil means "leave unchanged". To CLEAR a field, send its empty value
    // explicitly ("" to unassign / clear due date, 0 to clear estimate, [] to clear labels).
    public var assigneeID: String?; public var labelIDs: [String]?
    public var cycleID: String?; public var estimate: Double?; public var dueDate: String?
    public init(provider: String, issueID: String, title: String? = nil, description: String? = nil, stateID: String? = nil, priority: Int? = nil, assigneeID: String? = nil, labelIDs: [String]? = nil, cycleID: String? = nil, estimate: Double? = nil, dueDate: String? = nil) {
        self.provider = provider; self.issueID = issueID
        self.title = title; self.description = description; self.stateID = stateID; self.priority = priority
        self.assigneeID = assigneeID; self.labelIDs = labelIDs; self.cycleID = cycleID; self.estimate = estimate; self.dueDate = dueDate
    }
    enum CodingKeys: String, CodingKey {
        case provider, title, description, priority, estimate
        case issueID = "issue_id"; case stateID = "state_id"
        case assigneeID = "assignee_id"; case labelIDs = "label_ids"; case cycleID = "cycle_id"; case dueDate = "due_date"
    }
}

public struct IssueCommentAdd: Codable {
    public var provider: String; public var issueID: String; public var body: String
    public init(provider: String, issueID: String, body: String) { self.provider = provider; self.issueID = issueID; self.body = body }
    enum CodingKeys: String, CodingKey { case provider, body; case issueID = "issue_id" }
}

public struct IssueCommentEdit: Codable {
    public var provider: String; public var commentID: String; public var body: String
    public init(provider: String, commentID: String, body: String) { self.provider = provider; self.commentID = commentID; self.body = body }
    enum CodingKeys: String, CodingKey { case provider, body; case commentID = "comment_id" }
}

public struct IssueImageReq: Codable {
    public var provider: String; public var url: String
    public init(provider: String, url: String) { self.provider = provider; self.url = url }
    enum CodingKeys: String, CodingKey { case provider, url }
}

public struct IssueImage: Codable {
    public var mime: String
    public var data: String // base64
}

public struct SessionAttach: Codable {
    public var provider: String; public var sessionID: String; public var url: String?; public var cwd: String?
    public init(provider: String, sessionID: String, url: String?, cwd: String? = nil) {
        self.provider = provider; self.sessionID = sessionID; self.url = url; self.cwd = cwd
    }
    enum CodingKeys: String, CodingKey { case provider; case sessionID = "session_id"; case url; case cwd }
}
public struct SessionMessage: Codable {
    public var sessionID: String; public var role: String; public var text: String
    /// Who sent a USER message (the device/person name from client.identify). Empty for assistant
    /// and tool messages, and for anything sent before a client identified itself.
    public var author: String?
    enum CodingKeys: String, CodingKey { case sessionID = "session_id"; case role; case text; case author }
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
    public var updatedAt: Int? // unix seconds of last activity
    public var live: Bool?     // currently running in a terminal (not just a transcript)
    enum CodingKeys: String, CodingKey {
        case provider, kind, url
        case sessionID = "session_id"
        case title, cwd, path, pid
        case updatedAt = "updated_at"
        case live
    }
}
public extension Discovered {
    /// A stable composite identity for a discovered artifact, used to key ForEach so live host
    /// re-discovery (insert/remove/reorder) associates rows to the right data instead of to a
    /// positional array offset.
    var discoveryID: String {
        [provider, kind, sessionID, cwd, path].compactMap { $0 }.joined(separator: "|")
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

/// One persisted approval rule, as shown in the rules screen. `description` is rendered daemon-side
/// so every client words a rule identically.
public struct ApprovalRuleInfo: Codable, Equatable, Identifiable {
    public var index: Int
    public var action: String
    public var provider: String?
    public var tool: String?
    public var pattern: String?
    public var pathPrefix: String?
    public var projectID: String?
    public var projectName: String?
    public var description: String
    public var id: Int { index }
    enum CodingKeys: String, CodingKey {
        case index, action, provider, tool, pattern
        case pathPrefix = "path_prefix", projectID = "project_id", projectName = "project_name"
        case description
    }
}
public struct ApprovalRulesList: Codable, Equatable {
    public var rules: [ApprovalRuleInfo]
    public init(rules: [ApprovalRuleInfo] = []) { self.rules = rules }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        rules = try c.decodeIfPresent([ApprovalRuleInfo].self, forKey: .rules) ?? []
    }
    enum CodingKeys: String, CodingKey { case rules }
}
public struct ApprovalRuleDelete: Codable {
    public var index: Int
    public init(index: Int) { self.index = index }
}

/// A session's working mode. The daemon enforces these at the approval layer, so they behave the
/// same on every harness — including ones with no native permission mode of their own.
public enum SessionMode {
    /// Normal: the persisted approval rules decide, anything else asks.
    public static let code = "code"
    /// Read-only: reads and searches run, every mutating tool is refused.
    public static let ask = "ask"
    /// Plan-first: same refusals as ask, plus the harness's native plan mode where it has one.
    public static let architect = "architect"

    public static func label(_ mode: String?) -> String {
        switch mode {
        case ask: return "Ask"
        case architect: return "Architect"
        default: return "Code"
        }
    }
    /// Read-only modes get a visible chip; normal operation doesn't need one.
    public static func isRestricted(_ mode: String?) -> Bool { mode == ask || mode == architect }
}

public struct SessionModeSet: Codable {
    public var sessionID: String
    public var mode: String
    public init(sessionID: String, mode: String) { self.sessionID = sessionID; self.mode = mode }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id", mode }
}

/// One agent's outcome in a fan-out group. Title/summary are the agent's OWN handoff record — what
/// it says it did — so the comparison costs no extra model call.
public struct FanoutVariantResult: Codable, Equatable, Identifiable {
    public var sessionID: String
    public var variant: Int
    public var model: String?
    public var status: String
    public var title: String?
    public var summary: String?
    public var filesChanged: Int
    public var insertions: Int
    public var deletions: Int
    public var durationSec: Int?
    public var branch: String?
    public var failed: Bool?
    /// True for the variant that READ the others and wrote a combined implementation. A diff
    /// written with knowledge of its competitors isn't comparable to one written blind, so the
    /// comparison has to say so rather than showing it as just another attempt.
    public var isSynthesis: Bool?
    /// The 1-based variant numbers whose diffs it actually read. A combination of 2 of 6 attempts
    /// is a different object from one of all 6, and nothing else on the card reveals which it is.
    public var sourceVariants: [Int]?
    public var id: String { sessionID }
    enum CodingKeys: String, CodingKey {
        case sessionID = "session_id", variant, model, status, title, summary
        case filesChanged = "files_changed", insertions, deletions
        case durationSec = "duration_sec", branch, failed
        case isSynthesis = "is_synthesis", sourceVariants = "source_variants"
    }
}

/// The comparison broadcast when every variant in a fan-out group has finished.
public struct FanoutSummary: Codable, Equatable, Identifiable {
    public var id: String { group }
    public var group: String
    public var prompt: String?
    public var results: [FanoutVariantResult]
    public init(group: String, prompt: String? = nil, results: [FanoutVariantResult] = []) {
        self.group = group; self.prompt = prompt; self.results = results
    }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        group = try c.decode(String.self, forKey: .group)
        prompt = try c.decodeIfPresent(String.self, forKey: .prompt)
        results = try c.decodeIfPresent([FanoutVariantResult].self, forKey: .results) ?? []
    }
    enum CodingKeys: String, CodingKey { case group, prompt, results }
}

/// One tool an MCP server advertises.
public struct MCPTool: Codable, Equatable, Identifiable {
    public var name: String
    public var description: String?
    public var id: String { name }
}

/// A registered MCP server plus what the daemon last learned by talking to it.
public struct MCPServerInfo: Codable, Equatable, Identifiable {
    public var name: String
    public var transport: String
    public var command: String?
    public var args: [String]?
    /// Credential VALUES are redacted by the daemon; keys are shown so you can see what's set.
    public var env: [String: String]?
    public var url: String?
    public var enabled: Bool
    public var projectID: String?
    public var ok: Bool
    public var error: String?
    public var protocolVersion: String?
    public var serverVersion: String?
    public var tools: [MCPTool]?
    public var checkedAt: Int?
    public var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name, transport, command, args, env, url, enabled, ok, error, tools
        case projectID = "project_id"
        case protocolVersion = "protocol_version"
        case serverVersion = "server_version"
        case checkedAt = "checked_at"
    }
}

public struct MCPList: Codable, Equatable {
    public var servers: [MCPServerInfo]
    public init(servers: [MCPServerInfo] = []) { self.servers = servers }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        servers = try c.decodeIfPresent([MCPServerInfo].self, forKey: .servers) ?? []
    }
    enum CodingKeys: String, CodingKey { case servers }
}

public struct MCPUpsert: Codable {
    public var name: String
    public var transport: String?
    public var command: String?
    public var args: [String]?
    public var env: [String: String]?
    public var url: String?
    public var projectID: String?
    public init(name: String, transport: String? = nil, command: String? = nil, args: [String]? = nil,
                env: [String: String]? = nil, url: String? = nil, projectID: String? = nil) {
        self.name = name; self.transport = transport; self.command = command
        self.args = args; self.env = env; self.url = url; self.projectID = projectID
    }
    enum CodingKeys: String, CodingKey {
        case name, transport, command, args, env, url
        case projectID = "project_id"
    }
}

public struct MCPRef: Codable {
    public var name: String
    public init(name: String) { self.name = name }
}

public struct MCPEnable: Codable {
    public var name: String
    public var enabled: Bool
    public init(name: String, enabled: Bool) { self.name = name; self.enabled = enabled }
}

/// Announces this device's human name so the daemon can attribute prompts to it.
public struct ClientIdentify: Codable {
    public var name: String
    public init(name: String) { self.name = name }
}

/// What a participant may do. Enforced by the DAEMON — the client renders these but never decides
/// them, because a client cannot be trusted to enforce a permission it also displays.
public enum ParticipantRole {
    /// Everything, including answering approvals (which act with this machine's credentials).
    public static let owner = "owner"
    /// May prompt and interrupt, but NOT answer approvals.
    public static let steerer = "steerer"
    /// Watch only.
    public static let observer = "observer"

    public static func label(_ r: String) -> String {
        switch r {
        case owner: return "Owner"
        case steerer: return "Can steer"
        default: return "Watching"
        }
    }
}

public struct Participant: Codable, Equatable, Identifiable {
    public var name: String
    public var role: String
    public var id: String { name }
}

public struct ParticipantList: Codable, Equatable {
    public var enabled: Bool
    public var participants: [Participant]
    public init(enabled: Bool = false, participants: [Participant] = []) {
        self.enabled = enabled; self.participants = participants
    }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
        participants = try c.decodeIfPresent([Participant].self, forKey: .participants) ?? []
    }
    enum CodingKeys: String, CodingKey { case enabled, participants }
}

public struct RoleGrant: Codable {
    public var name: String
    public var role: String
    public init(name: String, role: String) { self.name = name; self.role = role }
}

public struct RolesEnable: Codable {
    public var enabled: Bool
    public init(enabled: Bool) { self.enabled = enabled }
}

/// An outstanding share credential. The secret is returned ONCE at creation and never listed again —
/// a credential you can re-read is one that leaks from a screen.
public struct Invite: Codable, Equatable, Identifiable {
    public var id: String
    public var label: String?
    public var role: String
    public var expiresAt: Int
    public var redeemed: Int
    enum CodingKeys: String, CodingKey {
        case id, label, role, redeemed
        case expiresAt = "expires_at"
    }
}

public struct InviteList: Codable, Equatable {
    public var invites: [Invite]
    public init(invites: [Invite] = []) { self.invites = invites }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        invites = try c.decodeIfPresent([Invite].self, forKey: .invites) ?? []
    }
    enum CodingKeys: String, CodingKey { case invites }
}

public struct InviteCreate: Codable {
    public var label: String?
    public var role: String?
    public var ttlHours: Int?
    public init(label: String? = nil, role: String? = nil, ttlHours: Int? = nil) {
        self.label = label; self.role = role; self.ttlHours = ttlHours
    }
    enum CodingKeys: String, CodingKey { case label, role; case ttlHours = "ttl_hours" }
}

public struct InviteCreated: Codable {
    public var invite: Invite
    public var url: String?
}

public struct InviteRef: Codable {
    public var id: String
    public init(id: String) { self.id = id }
}

/// One server published in the public MCP registry. Command/Args are a SUGGESTION the user confirms
/// before anything is saved — nothing installs itself.
public struct MCPDirectoryEntry: Codable, Equatable, Identifiable {
    public var name: String
    public var description: String?
    public var version: String?
    public var command: String?
    public var args: [String]?
    public var url: String?
    public var transport: String
    public var envKeys: [String]?
    public var unsupported: String?
    public var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name, description, version, command, args, url, transport, unsupported
        case envKeys = "env_keys"
    }
}

public struct MCPBrowse: Codable {
    public var query: String?
    public init(query: String? = nil) { self.query = query }
}

public struct MCPDirectory: Codable, Equatable {
    public var entries: [MCPDirectoryEntry]
    public init(entries: [MCPDirectoryEntry] = []) { self.entries = entries }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        entries = try c.decodeIfPresent([MCPDirectoryEntry].self, forKey: .entries) ?? []
    }
    enum CodingKeys: String, CodingKey { case entries }
}

/// A server found in a harness's OWN config, offered for import. Never adopted automatically — a
/// server definition carries a command that will run with your credentials.
public struct MCPFound: Codable, Equatable, Identifiable {
    public var name: String
    public var transport: String
    public var command: String?
    public var args: [String]?
    public var url: String?
    public var envKeys: [String]?
    public var source: String
    public var path: String?
    public var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name, transport, command, args, url, source, path
        case envKeys = "env_keys"
    }
}

public struct MCPDiscovered: Codable, Equatable {
    public var found: [MCPFound]
    public var exclusive: Bool
    public init(found: [MCPFound] = [], exclusive: Bool = false) {
        self.found = found; self.exclusive = exclusive
    }
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        found = try c.decodeIfPresent([MCPFound].self, forKey: .found) ?? []
        exclusive = try c.decodeIfPresent(Bool.self, forKey: .exclusive) ?? false
    }
    enum CodingKeys: String, CodingKey { case found, exclusive }
}

public struct MCPImport: Codable {
    public var names: [String]
    public init(names: [String]) { self.names = names }
}

public struct MCPExclusiveSet: Codable {
    public var enabled: Bool
    public init(enabled: Bool) { self.enabled = enabled }
}

/// Asks for the history immediately before what's already loaded. `loaded` is the count the client
/// holds, so the daemon needs no per-client cursor.
public struct TranscriptPage: Codable {
    public var sessionID: String
    public var loaded: Int
    public var limit: Int?
    public init(sessionID: String, loaded: Int, limit: Int? = nil) {
        self.sessionID = sessionID; self.loaded = loaded; self.limit = limit
    }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id", loaded, limit }
}

public struct TranscriptPageBegin: Codable {
    public var sessionID: String
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}

public struct TranscriptPageEnd: Codable {
    public var sessionID: String
    public var count: Int
    public var hasMore: Bool
    enum CodingKeys: String, CodingKey { case sessionID = "session_id", count; case hasMore = "has_more" }
}

public struct UsageSlice: Codable, Equatable, Identifiable {
    public var key: String
    public var label: String?
    public var inputTokens: Int
    public var outputTokens: Int
    public var costUSD: Double
    public var id: String { key }
    public var tokens: Int { inputTokens + outputTokens }
    enum CodingKeys: String, CodingKey {
        case key, label
        case inputTokens = "input_tokens", outputTokens = "output_tokens", costUSD = "cost_usd"
    }
}

/// The rolling window a subscription meters on. It starts with your FIRST activity rather than on
/// the clock, so the reset time comes from when usage began.
public struct UsageWindow: Codable, Equatable {
    public var startedAt: Int
    public var resetsAt: Int
    public var hours: Int
    public var costUSD: Double
    public var tokens: Int
    public var active: Bool
    enum CodingKeys: String, CodingKey {
        case hours, tokens, active
        case startedAt = "started_at", resetsAt = "resets_at", costUSD = "cost_usd"
    }
}

public struct UsageReport: Codable, Equatable {
    public var today: UsageSlice
    public var week: UsageSlice
    public var month: UsageSlice
    public var window: UsageWindow
    public var providers: [UsageSlice]
    public var models: [UsageSlice]
    public var sessions: [UsageSlice]
    /// Dollar figures are NOTIONAL when a subscription-backed agent is in play — nothing is billed
    /// per token. The UI must say so rather than implying a bill.
    public var subscription: Bool
}


/// A pull request's CI rollup, flattened to what a phone-sized review screen can act on: how many
/// checks passed, failed or are still running, plus the names of the failing ones (capped by the
/// daemon). Absent when the PR has no checks — a repo without CI is not a failure. The counts are
/// optional because the daemon omits zeroes.
public struct PRChecks: Codable, Equatable {
    public var state: String?      // SUCCESS | FAILURE | PENDING
    public var passed: Int?
    public var failed: Int?
    public var pending: Int?
    public var failing: [String]?
    public var passedCount: Int { passed ?? 0 }
    public var failedCount: Int { failed ?? 0 }
    public var pendingCount: Int { pending ?? 0 }
    public var total: Int { passedCount + failedCount + pendingCount }
}

/// A worktree's pull-request state, so the app can tell the user their work landed and offer to clean
/// up — rather than leaving finished worktrees on disk forever. Checks rides along because the
/// decision made off this screen is whether to merge, and "PR open" alone doesn't say whether it's
/// safe to.
public struct WorktreeStatusResult: Codable, Equatable {
    public var sessionID: String
    public var branch: String
    public var state: String?      // OPEN | MERGED | CLOSED; nil when there is no PR
    public var url: String?
    public var hasRemote: Bool
    public var checks: PRChecks?
    public var merged: Bool { state == "MERGED" }
    enum CodingKeys: String, CodingKey {
        case branch, state, url, checks
        case sessionID = "session_id", hasRemote = "has_remote"
    }
}

/// One device enrolled to reach this daemon, identified by the static public key it presents in the
/// handshake.
public struct DeviceInfo: Codable, Equatable, Identifiable {
    public var pub: String
    public var label: String?
    public var firstSeen: Int
    public var lastSeen: Int
    /// True for the device asking — so the UI never offers to revoke the connection it is speaking over.
    public var this: Bool?
    /// True for a device that came in through an invite. It holds no credential of its own: its
    /// access ends with the invite.
    public var guest: Bool?
    public var id: String { pub }
    enum CodingKeys: String, CodingKey {
        case pub, label, this, guest
        case firstSeen = "first_seen", lastSeen = "last_seen"
    }
}

/// The per-device credential the daemon mints at enrollment. The app stores it in the Keychain
/// (ThisDeviceOnly) and presents it instead of the pairing code from the next connection on.
public struct DeviceCredential: Codable, Equatable {
    public var pub: String
    public var credential: String
    public var issuedAt: Int?
    enum CodingKeys: String, CodingKey {
        case pub, credential
        case issuedAt = "issued_at"
    }
}

/// A freshly minted single-use pairing code and the URL that carries it.
public struct PairCode: Codable, Equatable {
    public var code: String
    public var url: String?
    /// Unix seconds. Shown to the owner, because a credential with an invisible lifetime is one
    /// people assume is permanent — and then screenshot.
    public var expiresAt: Int
    public var expiry: Date { Date(timeIntervalSince1970: TimeInterval(expiresAt)) }
    enum CodingKeys: String, CodingKey {
        case code, url
        case expiresAt = "expires_at"
    }
}

/// Whether the pre-upgrade permanent pairing secret is still accepted, and until when.
public struct PairStatus: Codable, Equatable {
    public var legacyLive: Bool
    public var legacyRetireAt: Int?
    enum CodingKeys: String, CodingKey {
        case legacyLive = "legacy_live"
        case legacyRetireAt = "legacy_retire_at"
    }
}

public struct DeviceList: Codable, Equatable {
    public var devices: [DeviceInfo]
}

public struct DeviceRef: Codable {
    public var pub: String
    public var label: String?
    public init(pub: String, label: String? = nil) { self.pub = pub; self.label = label }
}

public struct WorktreeRef: Codable {
    public var sessionID: String
    public var message: String?
    public init(sessionID: String, message: String? = nil) { self.sessionID = sessionID; self.message = message }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id", message }
}
