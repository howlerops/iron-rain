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
    public static let sessionSubscribe = "session.subscribe"
    public static let approvalRespond = "approval.respond"
    public static let discover = "discover.list"
    public static let deviceRegister = "device.register"
    public static let providerList = "provider.list"
    public static let projectList = "project.list"
    public static let projectAdd = "project.add"
    public static let projectBrowse = "project.browse"
    public static let projectRemove = "project.remove"
    public static let worktreeDiff = "worktree.diff"
    public static let workspaceDiff = "workspace.diff"
    public static let workspacePR = "workspace.pr"
    public static let worktreeRemove = "worktree.remove"
    public static let worktreePR = "worktree.pr"
    public static let worktreeConflicts = "worktree.conflicts"
    public static let integrationConnect = "integration.connect"
    public static let integrationStatus = "integration.status"
    public static let integrationOAuth = "integration.oauth"
    public static let integrationOAuthApp = "integration.oauthapp"
    public static let issueList = "issue.list"
    public static let issueStates = "issue.states"
    public static let issueLaunch = "issue.launch"
    public static let issueDetail = "issue.detail"
    public static let issueUpdate = "issue.update"
    public static let issueComment = "issue.comment"
    public static let issueCommentEdit = "issue.comment.edit"
    public static let issueImage = "issue.image"

    // Built-in editor file access.
    public static let fsTree = "fs.tree"
    public static let fsRead = "fs.read"
    public static let fsReadBytes = "fs.readbytes"
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
    public var autonomous: Bool?
    public var maxNudges: Int?
    public var budgetUSD: Double?
    public init(provider: String, cwd: String? = nil, projectID: String? = nil, projectIDs: [String]? = nil, prompt: String? = nil, images: [ImageAttachment]? = nil, worktree: Bool? = nil, workspaceName: String? = nil, plan: Bool? = nil, autonomous: Bool? = nil, maxNudges: Int? = nil, budgetUSD: Double? = nil) {
        self.provider = provider; self.cwd = cwd; self.projectID = projectID; self.projectIDs = projectIDs; self.prompt = prompt; self.images = images; self.worktree = worktree; self.workspaceName = workspaceName; self.plan = plan; self.autonomous = autonomous; self.maxNudges = maxNudges; self.budgetUSD = budgetUSD
    }
    enum CodingKeys: String, CodingKey {
        case provider, cwd, prompt, images, worktree, plan, autonomous
        case projectID = "project_id"
        case projectIDs = "project_ids"
        case workspaceName = "workspace_name"
        case maxNudges = "max_nudges"
        case budgetUSD = "budget_usd"
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
    public var updatedAt: Int? // unix seconds of last activity
    public var inputTokens: Int?
    public var outputTokens: Int?
    public var costUSD: Double?
    enum CodingKeys: String, CodingKey {
        case id, provider, status, title, name, cwd, branch, port
        case projectID = "project_id"
        case workspaceName = "workspace_name"
        case isWorkspace = "is_workspace"
        case parentID = "parent_id"
        case subtask
        case issueKey = "issue_key"
        case issueID = "issue_id"
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
    public var provider: String?
    public var autonomous: Bool?
    public init(parentSessionID: String, subtask: String, files: [String]? = nil, provider: String? = nil, autonomous: Bool? = nil) {
        self.parentSessionID = parentSessionID; self.subtask = subtask; self.files = files; self.provider = provider; self.autonomous = autonomous
    }
    enum CodingKeys: String, CodingKey {
        case parentSessionID = "parent_session_id"
        case subtask, files, provider, autonomous
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
public struct IntegrationStatus: Codable {
    public var connected: [String]
    public var oauthApps: [String]?   // providers with an OAuth app configured
    enum CodingKeys: String, CodingKey { case connected; case oauthApps = "oauth_apps" }
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
    enum CodingKeys: String, CodingKey {
        case id, key, title, body, status, category, assignee, url, provider, priority
        case branchName = "branch_name"
        case teamID = "team_id"
        case updatedAt = "updated_at"
        case cycleID = "cycle_id"
        case cycleName = "cycle_name"
        case cycleNumber = "cycle_number"
    }
    /// Display label for the issue's cycle/sprint, e.g. "Cycle 12" or a named cycle.
    public var cycleLabel: String? {
        if let n = cycleName, !n.isEmpty { return n }
        if let num = cycleNumber, num > 0 { return "Cycle \(num)" }
        return nil
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
}

/// Partial edit — only non-nil fields are applied server-side.
public struct IssueUpdate: Codable {
    public var provider: String; public var issueID: String
    public var title: String?; public var description: String?
    public var stateID: String?; public var priority: Int?
    public init(provider: String, issueID: String, title: String? = nil, description: String? = nil, stateID: String? = nil, priority: Int? = nil) {
        self.provider = provider; self.issueID = issueID
        self.title = title; self.description = description; self.stateID = stateID; self.priority = priority
    }
    enum CodingKeys: String, CodingKey {
        case provider, title, description, priority
        case issueID = "issue_id"; case stateID = "state_id"
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
