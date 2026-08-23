import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif
#if os(iOS)
import ActivityKit
#endif

/// Outcome of one raced connection route: a live client, an explicit daemon refusal, or couldn't
/// reach it. Sendable so it can be returned from the connection race's child tasks.
///
/// `connected` carries the URL that won because the race otherwise throws that away: every route
/// yields the same working client, so nothing downstream could tell whether the app is on the local
/// network or bouncing every keystroke through a relay — which is the first thing worth knowing when
/// a session feels slow from the couch.
private enum RouteOutcome: Sendable {
    case connected(OculusClient, URL)
    case rejected(String)
    case unreachable
}

/// Drives one daemon connection: connect, autodetect running sessions, hold a
/// streaming conversation, and approve/deny tool calls. Built entirely on OculusKit
/// (the proven, vector-locked client). Shared by the iOS and macOS app targets.
/// One step in the session-create checklist (see Model.createSteps). Identified by stage so
/// repeated events for the same phase (e.g. worktree 1/3 → 2/3) update one row instead of stacking.
public struct CreateStep: Identifiable, Equatable {
    public let stage: String
    public var detail: String
    public var step: Int
    public var total: Int
    public var done: Bool
    public var id: String { stage }
}

@MainActor
public final class Model: ObservableObject {
    @Published public var wsURL = "ws://127.0.0.1:6000/ws"
    /// Shared relay URL for remote access from anywhere (off-LAN). When set, the client dials the
    /// relay and bridges to this daemon by server_id (= the daemon pubkey); the direct `wsURL` is
    /// the LAN fallback. Empty = LAN-only.
    @Published public var relayURL = ""
    @Published public var daemonPubHex = ""
    @Published public var secret = ""
    /// Human name of this desktop (set when managed by a DesktopStore).
    @Published public var name = ""
    /// When true, a DesktopStore owns persistence — the Model won't write the legacy
    /// single-pairing UserDefaults keys.
    public var managed = false
    /// Stable identity for a desktop connection (the daemon's public key).
    public var id: String { daemonPubHex }

    @Published public var connected = false
    @Published public var connecting = false // a connect/handshake attempt is in flight (not an error)
    @Published public var status = "Not connected"
    @Published public var statusDetail: String? // human reason when not connected (unreachable, wrong secret, key mismatch)
    /// Which route the live connection won on — "this Mac", "LAN", or "relay". Empty when not
    /// connected. Every route ends in an identical working client, so this is the only thing that
    /// distinguishes "slow because it's going through Cloudflare" from "slow for some other reason".
    @Published public var connectionRoute = ""
    /// The winning route's host — names WHICH relay when several are configured. Kept apart from the
    /// short label so the label stays glanceable and the specific address is still recoverable.
    @Published public var connectionRouteHost = ""
    /// True when this connection is going through a relay rather than straight to the Mac.
    public var onRelay: Bool { connected && connectionRoute == Self.relayRouteLabel }
    /// A prominent, dismissable error for a user action that failed while connected (e.g. a session
    /// that couldn't start). Drives an alert on the main surface — status text alone is invisible
    /// once the triggering sheet has dismissed.
    @Published public var actionError: String?
    /// True while a session is being created (worktree setup + provider spin-up can take a few
    /// seconds). Drives a skeleton loading overlay that locks the surface until it's ready.
    @Published public var startingSession = false
    @Published public var startingProvider = ""
    /// Live, ordered steps streamed by the daemon while a session is being created (worktree per
    /// repo, provider spin-up), so the loading screen is a prescriptive checklist rather than a
    /// generic skeleton. Reset each create; the last step completes on "ready".
    @Published public var createSteps: [CreateStep] = []
    /// The active session's agent slash commands (built-in + custom from .claude/commands), for the
    /// composer's "/" palette. Loaded per session.
    @Published public var commands: [SlashCommand] = []
    @Published public var messages: [ChatMessage] = []
    @Published public var sessionID: String?
    @Published public var currentSession: Session? // metadata (project/worktree/branch) of the active session
    @Published public var pendingApproval: ApprovalRequest? { didSet { refreshLiveActivity() } }

    /// Every approval currently waiting on the user, keyed by session id.
    ///
    /// `pendingApproval` above is only ever the OPEN session's, because the request handler used to
    /// drop anything whose `sessionID` didn't match the one on screen. That made the Activity screen
    /// — the app's default tab, and the one whose entire job is answering "what needs me?" — able to
    /// show that a session was blocked but not to unblock it: every approval cost a round trip into
    /// the session and a full transcript replay, and an approval raised while you were elsewhere was
    /// discarded outright until the daemon happened to re-send it.
    ///
    /// Keeping the map means an approval survives wherever you are, and `respond(_:scope:for:)` can
    /// answer one without opening its session.
    @Published public var pendingApprovals: [String: ApprovalRequest] = [:]
    @Published public var discovered: [Discovered] = []
    @Published public var busy = false { didSet { refreshLiveActivity() } } // agent is producing output; drives the Live Activity
    @Published public var activity: String? // current step, e.g. "running bash"
    // Sub-agent (child session) inline transcripts. When a child card is expanded, we subscribe to
    // its session and stream its tool calls + outputs into its own buffer — without leaving the parent.
    @Published public var childMessages: [String: [ChatMessage]] = [:] // child session id → its messages
    @Published public var childActivity: [String: String] = [:]        // child id → current tool activity
    @Published public var expandedChildIDs: Set<String> = []           // which child cards are open
    @Published public var pairingPublicURL: String? // reachable URL for the phone-pairing QR
    /// Live LSP diagnostics keyed by file path (editor underlines + the problems list).
    @Published public var diagnostics: [String: [LSPDiagnostic]] = [:]
    /// The active session's live to-do list (from the agent).
    @Published public var todos: [Todo] = []

    // Heartbeat supervision: latest derived state per session, and whether the active session
    // is enrolled in autonomous nudging (mirrors the daemon; user-toggleable).
    @Published public var heartbeats: [String: SessionHeartbeat] = [:]
    @Published public var autonomous = false

    // Indexed agent-authored handoff files (progress externalized to disk). Keyed lookups by
    // session id let the chat surface a "handoff saved" affordance for the active session.
    @Published public var handoffs: [HandoffEntry] = []
    /// Test/build run output + outcome for the active session.
    @Published public var testOutput: [String] = []
    @Published public var testResult: RunResult?
    @Published public var testRunning = false
    @Published public var showTests = false
    /// The transcript row a `!command` run is streaming into, or nil when no bang run is in flight.
    ///
    /// This exists because run.output / run.result carry only a session id — there is no run id to
    /// correlate on — so exactly ONE consumer of that stream may be active at a time. While this is
    /// set, output lands in the transcript row; otherwise it lands in the test panel. The two entry
    /// points disable each other (see `runBusy`) rather than racing for the same lines.
    @Published public var shellRunID: UUID? = nil

    // Projects + worktrees.
    @Published public var projects: [Project] = []
    @Published public var sessions: [Session] = [] // hub-managed sessions (for sidebar grouping)
    @Published public var lastDiff: String? // populated by worktreeDiff()
    @Published public var workspaceDiffs: [WorkspaceMemberDiff] = [] // per-repo diffs (workspace sessions)
    @Published public var workspacePRResults: [WorkspaceMemberPR] = [] // per-repo PR outcomes
    @Published public var workspacePRRunning = false
    @Published public var conflicts: [FileConflict] = [] // files shared with other worktrees
    @Published public var pendingImages: [ImageAttachment] = [] // attached, sent with the next prompt
    @Published public var pendingFiles: [FileAttachment] = []    // document attachments (text extracted)

    // Trackers (Linear/Jira).
    @Published public var issues: [Issue] = []
    /// Saved issue views (named filter presets) + tickets the user has hidden. Persisted locally.
    @Published public var savedIssueFilters: [SavedIssueFilter] = []
    @Published public var hiddenIssueIDs: Set<String> = []
    @Published public var connectedTrackers: [String] = []
    // Trackers that have an OAuth app configured (client_id present) — drives whether the connect
    // screen shows the OAuth button or asks for the OAuth app credentials.
    @Published public var oauthApps: [String] = []
    /// Connected trackers whose fetch/refresh is failing — drives a "reconnect" pill.
    @Published public var trackerAuthErrors: [String] = []
    /// Provider -> the ACTUAL failure message (e.g. "jira: 401 Unauthorized"), so the UI can show
    /// WHY a connected tracker isn't loading, not just that it isn't.
    @Published public var trackerAuthDetails: [String: String] = [:]
    /// The daemon guessed which Jira site to use because the token reaches several.
    ///
    /// Drives a prompt rather than leaving you to notice the board is someone else's project and go
    /// hunting for the switcher. Cleared once a site is chosen explicitly.
    @Published public var jiraSiteAmbiguous = false
    /// Whether the daemon is shipping anonymized diagnostics (on by default; toggled in Settings).
    @Published public var telemetryEnabled: Bool = true
    /// Atlassian sites the Jira token can reach + the active one — for orgs with more than one Jira
    /// site (picking the wrong one is the classic "connected but no tickets").
    @Published public var jiraSites: [JiraSite] = []
    @Published public var jiraCurrentSite: String = ""
    /// Recurring autonomous workflows ("loops") + their run history.
    @Published public var loops: [Loop] = []
    @Published public var loopRuns: [LoopRun] = []
    // Last tracker-connect/OAuth error, surfaced on the connect screen.
    @Published public var trackerError: String?
    @Published public var oauthURL: URL? // set when an OAuth flow returns an authorize URL to open

    // Kanban board (real workflow-status columns + drag/drop).
    /// Selectable boards/projects (from `issue.projects`).
    @Published public var issueProjects: [IssueProject] = []
    /// The board currently shown; persisted globally so the app reopens on the same board.
    @Published public var selectedProjectID: String?
    /// The selected board's ordered workflow-status columns (from `issue.columns`).
    @Published public var boardColumns: [IssueState] = []
    /// Per-project column ordering (ids), overriding the daemon's default position order.
    @Published public var boardColumnOrder: [String: [String]] = [:]
    /// Per-project hidden column ids.
    @Published public var hiddenBoardColumns: [String: Set<String>] = [:]
    /// Options applied to the NEXT session created (by the first send). Set via newSession(...).
    @Published public var newSessionProvider = "opencode"
    // Agent providers this daemon actually has (opencode/claude-code/pi + any generic CLI agents),
    // fetched on connect so the picker reflects reality instead of a hardcoded list. Starts empty
    // with providersLoaded=false so pickers can show a loader rather than a fake default.
    @Published public var providers: [String] = []
    @Published public var providersLoaded = false
    // Full agent roster (native + detected + custom) for the "Manage agents" screen.
    @Published public var agents: [AgentInfo] = []
    // Models available to the CURRENT session's provider (for the chat-header model picker).
    @Published public var sessionModels: [ModelInfo] = []
    @Published public var modelEditable = false
    @Published public var currentModel: String?
    // Live daemon log (Developer bottom panel). Populated on subscribe (replay + streamed lines).
    @Published public var daemonLog: [String] = []
    @Published public var showLogPanel = false
    private var logSubscribed = false
    // Title for the actionError alert, so a send/no-response failure isn't mislabeled "Couldn't start the session".
    @Published public var actionErrorTitle = "Something went wrong"
    // Per-session error detail for BACKGROUND (non-active) sessions, so a session whose sends stopped
    // landing surfaces in the sidebar instead of failing invisibly while you're looking elsewhere.
    @Published public var sessionErrors: [String: String] = [:]
    // Cross-session activity feed (Activity destination + Needs-You inbox + ticker). Newest first.
    @Published public var activityFeed: [ActivityEvent] = []
    /// Count of unread "needs you" items — drives the nav badge, the iOS tab badge, and the app icon.
    public var needsYouCount: Int { activityFeed.filter { $0.needsYou && !$0.read }.count }
    // Last user message that failed to deliver, stashed so a per-message Retry can resend it verbatim.
    private var pendingRetry: (id: UUID, text: String, images: [ImageAttachment])?
    /// Text to inject into the composer (Design-Mode picked-element block). The Composer observes
    /// this, appends it to its draft, and clears it.
    @Published public var draftInsert: String = ""
    /// Unsent composer text, kept per session (keyed by session id; the empty string keys the
    /// not-yet-started "new session" composer). Switching sessions swaps the draft in and out so a
    /// half-typed message is never lost, and never leaks into another session.
    @Published public var drafts: [String: String] = [:]
    /// A binding into `drafts` for the currently open session — this is what the Composer edits.
    public var currentDraft: String {
        get { drafts[sessionID ?? ""] ?? "" }
        set { drafts[sessionID ?? ""] = newValue }
    }
    public var pendingProjectID: String?
    public var pendingProjectIDs: [String]?  // multi-root workspace (multi-repo)
    public var pendingWorktree = false
    public var pendingWorkspaceName: String?
    public var pendingPlan = false

    private var client: OculusClient?
    private let defaults = UserDefaults.standard
    /// This device's X25519 private key, cached for the life of the model once resolved.
    ///
    /// It used to be `let clientPrivate = OculusCrypto.generatePrivateKey()` — a brand-new keypair on
    /// every launch. That single line broke device revocation completely: the daemon identifies
    /// devices by this public key, so a revoked phone that was force-quit and reopened arrived as a
    /// key nobody had ever seen, took the first-sight branch, and was authorized. The registry was a
    /// list of app launches. Persisting the key (Keychain, ThisDeviceOnly) is what gives "revoke this
    /// device" something to bind to.
    private var cachedClientPrivate: Data?
    /// macOS only: the bootstrap code from ~/.oculus/pairing.json, kept as a recovery credential for
    /// when the daemon refuses our stored one (it was reinstalled and no longer knows this device).
    /// Never used ahead of the stored credential — see attemptConnect.
    var localBootstrapSecret = ""
    /// In-flight request/reply calls (fs.*), keyed by envelope id, resolved in receiveLoop.
    private var pendingRequests: [String: CheckedContinuation<Envelope, Error>] = [:]
    /// Decoded tracker images keyed by URL (fetched through the daemon; auth-gated).
    private var imageCache: [String: Data] = [:]
    /// Wall-clock of the last live event (any parent OR sub-agent delta/tool/status). Bumped at the
    /// single event choke point (`bumpWatchdog`). Drives the softer, earlier "stream may be stuck"
    /// hint — distinct from the hard no-response watchdog — so a silently half-open socket (the app
    /// thinks it's working, but no bytes are arriving) is visible WITHOUT restarting the app.
    private var lastEventAt = Date()
    /// True when we're busy but no event has arrived for a while — surfaced as a gentle, dismissable
    /// "No updates — Reconnect" hint in the working bar. Flips back to false the instant activity
    /// resumes (or on a manual resync).
    @Published public var streamMaybeStalled = false
    /// While true, skip replayed transcript messages that duplicate ones already shown
    /// (set briefly around a live re-attach so reviving a session doesn't double the chat).
    var dedupReplay = false
    /// Child sessions we've already sent a sessionSubscribe for — so expanding a card twice (or
    /// collapse+re-expand) doesn't re-subscribe. Kept across collapse; cleared on parent switch.
    private var subscribedChildIDs: Set<String> = []
    var reconnectWanted = false
    private(set) var reconnecting = false
    /// The running backoff loop, held so foregrounding can abandon a sleep that was scheduled against
    /// wall-clock time the suspended process never actually experienced.
    private var reconnectTask: Task<Void, Never>?
    /// The ~20s WebSocket ping loop. Only a ping asks the transport a question it must answer; an idle
    /// session can otherwise sit on a socket that died an hour ago with `receive()` still waiting.
    private var keepaliveTask: Task<Void, Never>?
    /// When this daemon last completed a handshake — the difference between "your Mac is asleep" and
    /// "we have no idea". Persisted, because the most common case is a cold launch hours later.
    private var lastConnectedAt: Date?
    #if os(iOS)
    private var liveActivity: Any?
    #endif

    public init() {
        // Restore the last pairing so the app auto-reconnects without re-pairing.
        wsURL = defaults.string(forKey: Keys.ws) ?? wsURL
        daemonPubHex = defaults.string(forKey: Keys.pub) ?? ""
        relayURL = defaults.string(forKey: Keys.relay) ?? ""
        // Keychain first, with a one-time migration of any plaintext copy an earlier build left in
        // UserDefaults — an upgrade must not present the user with an unpaired app.
        secret = loadCredential()
        loadIssuePrefs()
        selectedProjectID = defaults.string(forKey: BoardKeys.project)
        if let p = selectedProjectID { loadBoardPrefs(for: p) }
    }

    // MARK: saved issue views + hidden tickets

    private enum IssuePrefKeys { static let filters = "oculus.issueFilters", hidden = "oculus.hiddenIssues" }

    private func loadIssuePrefs() {
        if let d = defaults.data(forKey: IssuePrefKeys.filters),
           let f = try? JSONDecoder().decode([SavedIssueFilter].self, from: d) { savedIssueFilters = f }
        if let h = defaults.stringArray(forKey: IssuePrefKeys.hidden) { hiddenIssueIDs = Set(h) }
    }

    private func persistIssuePrefs() {
        if let d = try? JSONEncoder().encode(savedIssueFilters) { defaults.set(d, forKey: IssuePrefKeys.filters) }
        defaults.set(Array(hiddenIssueIDs), forKey: IssuePrefKeys.hidden)
    }

    public func hideIssue(_ id: String) { hiddenIssueIDs.insert(id); persistIssuePrefs() }
    public func unhideIssue(_ id: String) { hiddenIssueIDs.remove(id); persistIssuePrefs() }
    public func unhideAllIssues() { hiddenIssueIDs.removeAll(); persistIssuePrefs() }
    public func addSavedIssueFilter(_ f: SavedIssueFilter) { savedIssueFilters.append(f); persistIssuePrefs() }
    public func deleteSavedIssueFilter(_ id: String) { savedIssueFilters.removeAll { $0.id == id }; persistIssuePrefs() }

    /// A managed connection owned by a DesktopStore (persistence handled by the store).
    public convenience init(name: String, wsURL: String, daemonPubHex: String, secret: String, relay: String = "") {
        self.init()
        self.managed = true
        self.name = name
        self.wsURL = wsURL
        self.daemonPubHex = daemonPubHex
        self.secret = secret
        self.relayURL = relay
    }

    private enum Keys { static let ws = "oculus.ws", pub = "oculus.pub", secret = "oculus.secret", relay = "oculus.relay" }

    /// True once the daemon has been paired at least once (creds are saved).
    public var hasSavedPairing: Bool { !wsURL.isEmpty && !daemonPubHex.isEmpty && !secret.isEmpty }

    private func savePairing() {
        guard !managed else { return } // a DesktopStore persists managed connections
        defaults.set(wsURL, forKey: Keys.ws)
        defaults.set(daemonPubHex, forKey: Keys.pub)
        defaults.set(relayURL, forKey: Keys.relay)
        // The credential goes to the Keychain, never to UserDefaults. A plist is plaintext, it is in
        // unencrypted backups, and this string reaches a shell on the paired Mac.
        saveCredential()
    }

    /// Writes the current credential to the Keychain and scrubs any copy an older build left in
    /// UserDefaults. The scrub matters as much as the write: migrating forward while leaving the
    /// plaintext copy behind would fix nothing for every user who already has one.
    private func saveCredential() {
        guard !daemonPubHex.isEmpty else { return }
        if secret.isEmpty {
            Keychain.remove(Keychain.credentialAccount(daemonPub: daemonPubHex))
        } else {
            Keychain.set(secret, for: Keychain.credentialAccount(daemonPub: daemonPubHex))
        }
        if !managed { defaults.removeObject(forKey: Keys.secret) }
    }

    /// Loads the stored credential for this daemon, migrating a legacy UserDefaults copy on the way.
    private func loadCredential() -> String {
        guard !daemonPubHex.isEmpty else { return "" }
        switch Keychain.read(Keychain.credentialAccount(daemonPub: daemonPubHex)) {
        case .found(let c):
            return c
        case .timedOut:
            // Unreachable, not absent. Migrating now would write the legacy plaintext over whatever
            // the Keychain actually holds — including a credential that was rotated since. Report
            // "not paired" for this launch instead; the stored value survives for the next one.
            return ""
        case .missing:
            break
        }
        // Pre-Keychain build: adopt the plaintext value once, then delete it.
        if let legacy = defaults.string(forKey: Keys.secret), !legacy.isEmpty {
            Keychain.set(legacy, for: Keychain.credentialAccount(daemonPub: daemonPubHex))
            defaults.removeObject(forKey: Keys.secret)
            return legacy
        }
        return ""
    }

    /// This device's stable X25519 private key for the paired daemon, created on first use.
    ///
    /// Falls back to an ephemeral key only when there is no daemon to key it against (nothing is
    /// paired yet, so there is no identity to be stable about).
    private func clientKey() -> Data {
        if let k = cachedClientPrivate { return k }
        guard !daemonPubHex.isEmpty else { return OculusCrypto.generatePrivateKey() }
        let account = Keychain.deviceKeyAccount(daemonPub: daemonPubHex)
        switch Keychain.read(account) {
        case .found(let hex):
            if let data = Data(hexString: hex), !data.isEmpty {
                cachedClientPrivate = data
                return data
            }
        case .timedOut:
            // The stored key is unreachable, NOT absent — so do not mint over it. Minting here would
            // overwrite this device's stable identity with a key the daemon has never seen, turning a
            // stall that resolves on the next launch into a pairing destroyed for good. An ephemeral
            // key fails authentication and surfaces as "needs pairing", which is recoverable; the
            // real key is still there when the Keychain answers again.
            //
            // Deliberately NOT cached: caching would make the ephemeral key outlive the stall for the
            // whole process, so a later read that succeeds could never take effect.
            NSLog("Keychain: device key unreachable within budget — using an ephemeral key for this "
                  + "attempt rather than overwriting the stored identity.")
            return OculusCrypto.generatePrivateKey()
        case .missing:
            break
        }
        let fresh = OculusCrypto.generatePrivateKey()
        Keychain.set(fresh.map { String(format: "%02x", $0) }.joined(), for: account)
        cachedClientPrivate = fresh
        return fresh
    }

    /// Stores a credential the daemon just minted for this device and tells it the handover landed.
    ///
    /// The daemon waits for this acknowledgement before it starts the clock on the old permanent
    /// pairing secret. A credential that was minted but never stored — the app was killed mid-frame,
    /// or this is an older build that ignores the frame — must not cost the user the only credential
    /// they still have.
    func applyDeviceCredential(_ credential: String) {
        guard !credential.isEmpty, !daemonPubHex.isEmpty else { return }
        secret = credential
        saveCredential()
        onCredentialStored?(daemonPubHex, credential)
        Task { await ackDeviceCredential() }
    }

    /// Called after a new credential is stored, so a DesktopStore can persist it for this desktop.
    var onCredentialStored: (@MainActor (String, String) -> Void)?

    private func ackDeviceCredential() async {
        guard let client, let raw = try? Protocol.encode(id: "", type: MessageType.deviceCredentialAck) else { return }
        try? await client.send(raw)
    }

    // MARK: connection

    /// Connects to a locally-running (or paired) daemon and keeps the connection
    /// alive — auto-reconnecting with backoff if it drops. On macOS, if there's no
    /// saved pairing it reads the daemon's local pairing file (~/.oculus/pairing.json)
    /// so a same-machine app connects with zero config.
    public func autoConnectIfPaired() async {
        await loadLocalPairing() // macOS: refresh the reachable URL (and pair if unpaired)
        if hasSavedPairing && !connected { await connect() }
    }

    private func loadLocalPairing() async {
        #if os(macOS)
        let path = (NSHomeDirectory() as NSString).appendingPathComponent(".oculus/pairing.json")
        // Read the bytes off the main actor — synchronous disk I/O must not block the UI.
        let data = await Task.detached { FileManager.default.contents(atPath: path) }.value
        guard let data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: String],
              let ws = obj["ws"], let pub = obj["pub"], let sec = obj["secret"] else { return }
        // Always refresh the reachable URL for the pairing QR — even once we're paired,
        // since the daemon's public URL (relay/tunnel) can change between launches.
        pairingPublicURL = obj["public"]
        // Always refresh the relay URL (the daemon may have been upgraded to a build that connects
        // to the shared relay), even once paired — so an existing local pairing gains remote access.
        if let relay = obj["relay"], !relay.isEmpty { relayURL = relay }
        // Keep the file's bootstrap code as a recovery credential even once paired: it is what heals a
        // daemon reinstall that left this device unknown. It is never presented ahead of the
        // per-device credential — see attemptConnect.
        localBootstrapSecret = sec
        // Trusted path: this came off a 0600 file in our own home directory, not off the network, so a
        // changed key here is a daemon reinstall and must heal silently. See applyLocalPairing.
        if !hasSavedPairing || (!pub.isEmpty && pub != daemonPubHex) {
            applyLocalPairing(url: ws, pub: pub, secret: sec, relay: obj["relay"] ?? "")
        }
        #endif
    }

    /// A freshly minted, single-use pairing URL and when it expires. Nil until one is minted.
    ///
    /// This used to be a computed property that rendered `secret` — the permanent, owner-equivalent
    /// credential — into a URL and a QR. Anything that saw that QR held a key to the Mac forever:
    /// a screenshot, the photo library it syncs to, a screen recording, someone across the table.
    /// Now the QR carries a code the daemon mints on request, which is spent by the first device that
    /// scans it and expires within minutes either way.
    @Published public var pairingCode: PairCode?

    /// Whether a pairing code is being minted (the QR sheet shows a spinner rather than a stale code).
    @Published public var mintingPairCode = false

    /// True once we can offer to pair another device — i.e. we're connected to a daemon as its owner.
    public var canMintPairingCode: Bool { connected && !daemonPubHex.isEmpty }

    /// Asks the daemon for a fresh single-use pairing code. This is the owner's re-pair path: a new
    /// phone, a reinstall, a device that was revoked and is coming back.
    public func mintPairingCode() async {
        guard client != nil, !mintingPairCode else { return }
        mintingPairCode = true
        defer { mintingPairCode = false }
        do {
            let env = try await request(MessageType.pairCode, payload: Optional<Int>.none)
            var code = try env.payload(as: PairCode.self)
            // The daemon builds the URL from the address IT knows it is reachable at. Fall back to the
            // address this app is actually connected on, so a code is never un-scannable just because
            // the daemon couldn't name its own public URL.
            if (code.url ?? "").isEmpty { code.url = localPairURL(for: code.code) }
            pairingCode = code
        } catch {
            actionErrorTitle = "Couldn't create a pairing code"
            actionError = error.localizedDescription
        }
    }

    /// Discards the displayed code. Called when the pairing sheet closes: a code left on screen is a
    /// live credential, and the sheet is exactly where someone screenshots one.
    public func clearPairingCode() { pairingCode = nil }

    private func localPairURL(for code: String) -> String? {
        let base = pairingPublicURL ?? (wsURL.isEmpty ? nil : wsURL)
        guard let base, !daemonPubHex.isEmpty else { return nil }
        var c = URLComponents()
        c.scheme = "oculus"
        c.host = "pair"
        c.queryItems = [
            .init(name: "ws", value: base),
            .init(name: "pub", value: daemonPubHex),
            .init(name: "secret", value: code),
        ]
        if !relayURL.isEmpty {
            c.queryItems?.append(.init(name: "relay", value: relayURL))
        }
        return c.url?.absoluteString
    }

    public func connect() async {
        reconnectWanted = true
        loadLastReachable() // so a cold launch can still tell "asleep" from "no idea"
        await attemptConnect()
    }

    private func attemptConnect() async {
        guard let pub = Data(hexString: daemonPubHex) else {
            status = "Invalid daemon public key"
            return
        }
        connecting = true
        status = "Connecting…"
        defer { connecting = false }
        // Build candidate routes and RACE them: the direct LAN/localhost URL (instant + free when
        // you're on the same network) plus every shared relay (reachable from anywhere), each with
        // URL-param registration. First to finish the handshake wins; the rest are cancelled (their
        // in-flight sockets closed) — so you get LAN latency at home and relay reachability when
        // away, with no sequential-timeout penalty either way. Relays forward only ciphertext, so
        // every route is equally end-to-end-secure.
        var routes: [URL] = []
        if let u = URL(string: wsURL) { routes.append(u) }                 // direct (LAN / localhost)
        for r in Self.splitRelays(relayURL) {                              // shared relays (DO, Fly, …)
            if let u = Self.relayClientURL(r, serverID: daemonPubHex) { routes.append(u) }
        }
        guard !routes.isEmpty else { status = "No address to connect to"; return }

        let priv = clientKey(), sec = secret
        var winner: OculusClient?
        var winnerURL: URL?        // the route that won — displayed so "on relay" is never a guess
        var rejected: String?      // a route reached the daemon and it refused (wrong secret / key)
        await withTaskGroup(of: RouteOutcome.self) { group in
            for url in routes {
                group.addTask {
                    let c = OculusClient(url: url)
                    return await withTaskCancellationHandler {
                        do {
                            try await c.connect(clientPrivate: priv, daemonPublic: pub, secret: sec)
                            return .connected(c, url)
                        } catch OculusClientError.handshakeRejected(let m) {
                            c.close(); return .rejected(m)
                        } catch {
                            c.close(); return .unreachable
                        }
                    } onCancel: {
                        c.close() // a faster route won — stop this in-flight attempt immediately
                    }
                }
            }
            for await outcome in group {
                switch outcome {
                case .connected(let c, let u):
                    if winner == nil {
                        winner = c
                        winnerURL = u
                        group.cancelAll()  // a route won — abandon the slower ones
                    } else {
                        c.close()          // a straggler connected after we already had a winner
                    }
                case .rejected(let m):
                    if rejected == nil { rejected = m }
                case .unreachable:
                    break
                }
            }
        }

        if let c = winner {
            // Cancelling a backoff loop mid-dial (what foregrounding does) can leave an older attempt
            // still finishing its handshake. Whoever assigns `client` last wins, so close the loser's
            // socket here or it lingers open, receiving frames nobody reads.
            client?.close()
            client = c
            connected = true
            connectionRoute = winnerURL.map { Self.routeLabel($0, direct: wsURL) } ?? ""
            connectionRouteHost = winnerURL?.host ?? ""
            status = connectionRoute.isEmpty ? "Connected" : "Connected · \(connectionRoute)"
            statusDetail = nil
            consecutiveFailures = 0
            noteReachable()
            savePairing()
            startKeepalive()
            Task { await receiveLoop() }
            await finishConnect()
            return
        }

        // Every route failed.
        if rejected != nil, !localBootstrapSecret.isEmpty, localBootstrapSecret != sec {
            // The daemon reached us and refused our credential. On this Mac that has one likely cause:
            // the daemon was reinstalled (or its device registry was cleared) and no longer knows this
            // device. ~/.oculus/pairing.json is same-user-only and holds a bootstrap code for exactly
            // this, so re-enroll with it once rather than presenting the user with a dead pairing.
            secret = localBootstrapSecret
            saveCredential()
            await attemptConnect()
            return
        }
        consecutiveFailures += 1
        status = "Connect failed"
        if let rejected {
            statusDetail = rejected.isEmpty ? "Pairing rejected" : "Pairing rejected: \(rejected)"
        } else {
            statusDetail = Self.unreachableDetail(
                lastConnected: lastConnectedAt,
                sustained: consecutiveFailures >= Self.sustainedFailureThreshold
            )
        }
        scheduleReconnect()
    }

    /// How long after a successful handshake we're still willing to blame sleep rather than the
    /// network. A Mac that answered inside this window has not changed address or moved networks —
    /// the overwhelmingly likely explanation for total silence is that the lid closed.
    private static let recentlyReachableWindow: TimeInterval = 6 * 60 * 60

    /// The honest reason every route failed.
    ///
    /// "Can't reach this Mac" sends the user hunting for a network fault. When the daemon answered
    /// recently and now nothing responds — not the LAN address, not any relay — the machine is
    /// almost certainly asleep, and that is an actionable sentence instead of a shrug. Static and
    /// pure so the wording is testable without a socket.
    ///
    /// `sustained` adds one defensive sentence, and it is here for a specific attack rather than for
    /// completeness. A substituted daemon key fails closed but is INDISTINGUISHABLE from an
    /// unreachable Mac from up here: the handshake dies inside the client and every non-rejection
    /// error arrives as `.unreachable`. We deliberately do not guess which one it was — inventing a
    /// "your Mac's identity changed" alarm out of a generic socket error would fire on every flaky
    /// coffee-shop network and train people to ignore it.
    ///
    /// What we can do without guessing is refuse to leave the user with only one confident
    /// explanation, because the attack's next step is offering them a fresh QR to "fix" it. Naming
    /// where a pairing code must come from costs nothing when the Mac really is asleep, and removes
    /// the attacker's leverage when it isn't.
    static func unreachableDetail(lastConnected: Date?, now: Date = Date(), sustained: Bool = false) -> String {
        var detail: String
        if let lastConnected, now.timeIntervalSince(lastConnected) >= 0,
           now.timeIntervalSince(lastConnected) < recentlyReachableWindow {
            detail = "Your Mac may be asleep — wake it, or turn off sleep in System Settings › Battery."
        } else {
            detail = "Can’t reach this Mac" // daemon down / never paired / genuinely off the network
        }
        if sustained {
            detail += " If this keeps happening, get a new pairing code from the Mac itself — don’t scan one someone sent you."
        }
        return detail
    }

    /// How many connect attempts have failed back to back. Reset on every success.
    private var consecutiveFailures = 0

    /// Failures before the status text stops offering a single confident cause. Three is past a
    /// transient blip and short of the point where someone goes looking for a QR code to scan.
    private static let sustainedFailureThreshold = 3

    static let relayRouteLabel = "relay"

    /// Names the route that won the connect race. Loopback means the daemon is on this very machine;
    /// a host matching the paired address means the local network; anything else is one of the relays
    /// the pairing carries. Static and pure so the wording is testable without a socket, like
    /// `unreachableDetail` above.
    static func routeLabel(_ url: URL, direct: String) -> String {
        let host = url.host ?? ""
        if host == "127.0.0.1" || host == "localhost" || host == "::1" { return "this Mac" }
        if let dh = URL(string: direct)?.host, !dh.isEmpty, dh == host { return "LAN" }
        return relayRouteLabel
    }

    private var lastReachableKey: String { "oculus.lastReachable.\(daemonPubHex)" }

    /// Records that the daemon answered, surviving app relaunch — the phone is usually cold-launched
    /// hours later, which is exactly when the sleep diagnosis is most useful.
    private func noteReachable() {
        let now = Date()
        lastConnectedAt = now
        guard !daemonPubHex.isEmpty else { return }
        defaults.set(now.timeIntervalSince1970, forKey: lastReachableKey)
    }

    private func loadLastReachable() {
        guard lastConnectedAt == nil, !daemonPubHex.isEmpty else { return }
        let t = defaults.double(forKey: lastReachableKey)
        if t > 0 { lastConnectedAt = Date(timeIntervalSince1970: t) }
    }

    /// Post-connection hydration: load projects/sessions/integrations and replay any pending
    /// notification-driven action. Runs once, after a route wins the race.
    private func finishConnect() async {
        // Teach the technical vocabulary before the user can type into the composer, so autocorrect
        // isn't rewriting `mcp`/`jira` on the very first prompt.
        TechDictionary.seedIfNeeded()
        TechDictionary.applyCustom()
        // THE CRITICAL PATH FIRST. Everything below used to be one long `await` chain — thirteen
        // sequential request/reply round trips before the conversation was usable. Over a relay on
        // cellular that is seconds of blank pane on every single swap, which is the moment the
        // product is judged on. Only three of them are on the path to a usable screen:
        //   identify   — must be first, so a prompt sent immediately after connecting is attributed;
        //   sessions   — the sidebar, and the list `hydrateLikelySessions` reads to warm the cache;
        //   reopen     — the transcript the user was looking at.
        // Note: discovery of terminal-owned sessions is on-demand (the Add Session search),
        // not auto-loaded — the sidebar shows only sessions started/opened in the app.
        await identifySelf()
        await loadSessions()
        // Read the likely-next sessions off disk BEFORE reopening, so the reopen can paint from
        // cache. Previously this had no call site at all — the cache was written and never read.
        await hydrateLikelySessions()
        // If a session was open when the socket dropped (e.g. the daemon restarted and forgot its
        // in-memory sessions), re-attach it so its transcript + prompts resume.
        await reopenCurrentSession()
        // Fresh launch: nothing open in memory, but reopen the session we last had open on this
        // desktop so you land back where you left off. Best-effort — no-ops if it no longer exists.
        if currentSession == nil, let last = UserDefaults.standard.string(forKey: lastSessionKey), !last.isEmpty {
            await openSession(last)
        }
        // EVERYTHING ELSE CONCURRENTLY. These populate secondary surfaces (issues board, loops,
        // activity feed, pickers) that the user has to navigate to; none of them feeds another, so
        // they cost one round trip together instead of ten in a row. Failures stay independent —
        // each already swallows its own error, so a daemon too old for one message can't stall the
        // rest, which the sequential chain allowed.
        await withTaskGroup(of: Void.self) { group in
            group.addTask { await self.loadProjects() }
            group.addTask { await self.loadIntegrationStatus() }
            group.addTask { await self.loadIssues() }
            group.addTask {
                // Ordered pair: the board's columns are looked up for whatever project the projects
                // load selects, so these two cannot be split apart.
                await self.loadIssueProjects() // board picker options
                await self.loadBoardColumns()  // real workflow-status columns for the selected board
            }
            group.addTask { await self.listProviders() }       // the daemon's real agent set
            group.addTask { await self.listHandoffs() }        // handoff index (live updates via handoff.list)
            group.addTask { await self.loadLoops() }           // recurring autonomous workflows
            group.addTask { await self.loadActivity() }        // cross-session feed + Needs-You inbox
            group.addTask { await self.loadTelemetryStatus() } // diagnostics toggle
            group.addTask { await self.loadNotifyPrefs() }     // per-type push toggles
        }
        if let token = OculusStore.shared.deviceToken {
            await registerDevice(token: token)
        }
        if let queued = OculusStore.shared.pendingPrompt {
            OculusStore.shared.pendingPrompt = nil
            await send(queued)
        }
        if let decision = OculusStore.shared.pendingDecision, pendingApproval != nil {
            OculusStore.shared.pendingDecision = nil
            await respond(decision)
        }
        // Tapped a notification (agent finished / error / approval) → open that session.
        if let sid = OculusStore.shared.handoffSessionID {
            OculusStore.shared.handoffSessionID = nil
            await openSession(sid)
        }
    }

    /// Splits a comma-separated relay list into individual URLs (trimmed; empties dropped).
    static func splitRelays(_ list: String) -> [String] {
        list.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    /// Builds a relay client URL carrying the unified registration params (?sid=&role=client).
    static func relayClientURL(_ base: String, serverID: String) -> URL? {
        guard var comps = URLComponents(string: base) else { return nil }
        var items = comps.queryItems ?? []
        items.append(.init(name: "sid", value: serverID))
        items.append(.init(name: "role", value: "client"))
        comps.queryItems = items
        return comps.url
    }

    /// The retry schedule: attempt IMMEDIATELY, then 2s, 4s, 8s, held at 15s.
    ///
    /// Sleeping first meant every reconnect — including the common case where the drop was a
    /// two-second blip, or where the user just took the phone out of their pocket — cost a guaranteed
    /// two seconds of "Reconnecting…" before anything was even attempted. The 15s ceiling matters as
    /// much as the growth: unbounded doubling leaves a phone that was offline on a train retrying
    /// minutes after the network came back.
    static func nextReconnectDelay(_ current: UInt64) -> UInt64 {
        current == 0 ? 2 : min(current * 2, 15)
    }

    /// Retries the connection with exponential backoff until it succeeds or the user
    /// disconnects. One loop at a time.
    func scheduleReconnect() {
        guard reconnectWanted, hasSavedPairing, !reconnecting, !connected else { return }
        reconnecting = true
        reconnectTask = Task { // inherits @MainActor from Model
            var delay: UInt64 = 0
            // The cancellation check is FIRST and repeated after the sleep: `cancelReconnectBackoff`
            // fires when the app foregrounds, and a loop that woke from a cancelled sleep and dialed
            // anyway would race the immediate attempt the foreground path is about to make.
            while reconnectWanted && !connected && !Task.isCancelled {
                status = "Reconnecting…"
                if delay > 0 { try? await Task.sleep(nanoseconds: delay * 1_000_000_000) }
                if Task.isCancelled { break }
                if reconnectWanted && !connected { await attemptConnect() }
                delay = Self.nextReconnectDelay(delay)
            }
            reconnecting = false
        }
    }

    /// Abandons a pending backoff sleep so the caller can dial right now.
    ///
    /// iOS does not run timers for a suspended process: a loop that had just begun a 15s wait when
    /// the phone went into a pocket still has ~15s left when it comes out, measured against
    /// wall-clock time the app never experienced. Without this the user watches "Reconnecting…" for
    /// the remainder of a delay that stopped being meaningful an hour ago.
    func cancelReconnectBackoff() {
        reconnectTask?.cancel()
        reconnectTask = nil
        reconnecting = false
    }

    // MARK: - Liveness: keepalive + foreground probe

    /// How often the socket is pinged while connected. Four of these fit inside the ~90s a user is
    /// willing to stare at a frozen screen before force-quitting, which is the bar to beat.
    private static let keepaliveInterval: UInt64 = 20

    /// Pings the socket on a timer so a dead pipe surfaces in under a minute instead of never.
    ///
    /// `URLSession` never reports a half-open TCP connection on its own: with no traffic to send,
    /// `receive()` waits forever on a socket whose peer vanished when the Mac slept or the NAT
    /// dropped the mapping. An idle session generates no traffic by definition, so the ping is the
    /// only thing that turns that silence into an error we can act on.
    /// Whether the ping loop is live. Exposed for tests: the loop itself is a detached Task, so its
    /// existence is the only observable thing about it.
    var keepaliveRunningForTests: Bool {
        get { keepaliveTask != nil }
        set { if !newValue { keepaliveTask?.cancel(); keepaliveTask = nil } }
    }

    private func startKeepalive() {
        keepaliveTask?.cancel()
        keepaliveTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: Self.keepaliveInterval * 1_000_000_000)
                guard !Task.isCancelled, let self, self.connected, let c = self.client else { return }
                do {
                    try await c.ping()
                } catch {
                    // Re-check identity: an awaited ping can outlive the connection it was sent on,
                    // and tearing down a NEWER client would drop a connection that is perfectly fine.
                    guard self.connected, self.client === c else { return }
                    self.dropConnection("Reconnecting…")
                    return
                }
            }
        }
    }

    private func stopKeepalive() {
        keepaliveTask?.cancel()
        keepaliveTask = nil
    }

    /// Tears down a connection we no longer believe in and hands control to the backoff loop.
    /// `close()` also makes the in-flight `recv()` throw, so `receiveLoop` unwinds behind us.
    private func dropConnection(_ reason: String) {
        guard connected || client != nil else { return }
        client?.close()
        client = nil
        connected = false
        // The next attempt races from scratch and may well land somewhere else, so the old route is
        // not a fact any more — leaving it up would keep claiming "LAN" while we dial a relay.
        connectionRoute = ""
        connectionRouteHost = ""
        status = reason
        busy = false
        stopKeepalive()
        stopStallLoop()
        failPendingRequests(NSError(domain: "Oculus", code: -3,
                                    userInfo: [NSLocalizedDescriptionKey: "connection lost"]))
        refreshLiveActivity(ended: true)
        scheduleReconnect()
    }

    /// A hard-deadlined round trip that only the DAEMON can answer.
    ///
    /// Deliberately not a WebSocket ping: when we're on a relay, the relay terminates the socket and
    /// answers pings itself, so a ping would happily succeed against a relay whose daemon died. The
    /// probe therefore rides the application protocol. It also carries its own deadline, because
    /// `request` has none and an un-deadlined request on a half-open socket never returns — which is
    /// precisely the state being tested for.
    func probeConnection(timeout: TimeInterval = 4) async -> Bool {
        guard let client else { return false }
        let id = UUID().uuidString
        guard let env = try? Protocol.encode(id: id, type: MessageType.clientIdentify,
                                             payload: ClientIdentify(name: identity)) else { return false }
        let deadline = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
            guard !Task.isCancelled, let self else { return }
            // removeValue is the handoff: whoever takes the continuation out of the map resumes it,
            // so the deadline and the reply can never both resolve it.
            self.takePendingRequest(id)?.resume(throwing: OculusClientError.notConnected)
        }
        defer { deadline.cancel() }
        do {
            _ = try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Envelope, Error>) in
                pendingRequests[id] = cont
                Task { @MainActor in
                    do { try await client.send(env) }
                    catch { self.takePendingRequest(id)?.resume(throwing: error) }
                }
            }
            return true
        } catch {
            return false
        }
    }

    private func takePendingRequest(_ id: String) -> CheckedContinuation<Envelope, Error>? {
        pendingRequests.removeValue(forKey: id)
    }

    /// Re-establishes trust in the connection after the process was suspended. Returns true if the
    /// existing connection answered; false means it has been torn down and the backoff loop owns it.
    ///
    /// `connected` is a claim about a socket nobody has touched since before the suspension, and a
    /// wrong claim here is the worst thing this app can show: a "Connected" pill above a transcript
    /// that will never move again. The user waits instead of acting.
    @discardableResult
    func revalidateConnection() async -> Bool {
        guard connected else { return false }
        if await probeConnection() { return true }
        dropConnection("Reconnecting…")
        return false
    }

    /// The app came to the foreground. Called from the Scene's `scenePhase` observer — before this
    /// existed the app had NO lifecycle wiring at all, so a phone taken out of a pocket sat on a
    /// frozen backoff and a stale status until the user tapped something.
    public func appDidBecomeActive() async {
        guard hasSavedPairing else { return }
        loadLastReachable()
        // Order matters: revalidate BEFORE cancelling the backoff, so a connection that is actually
        // healthy is left completely alone (no backoff exists in that case anyway).
        if connected {
            if await revalidateConnection() {
                // Restart the ping. `appWillResignActive` stopped it, and this healthy path is the
                // one that returns early — so without this the keepalive died on the FIRST app
                // switch and never came back. `.inactive` fires on the iOS app switcher, Control
                // Centre, and every macOS app switch, so "a dead pipe is detected within a minute"
                // silently reverted to "never" within minutes of ordinary use.
                startKeepalive()
                return
            }
        }
        cancelReconnectBackoff()
        // The loop may have been mid-handshake when we cancelled it, and that handshake may have
        // landed. Dialing again would strand a perfectly good connection behind a second one.
        guard !connected else { return }
        await connect()
    }

    /// The app is going away. The keepalive is stopped rather than left to fire against a suspended
    /// runtime, where its failure would be an artifact of suspension rather than evidence about the
    /// socket — and would leave a spurious "Reconnecting…" waiting on screen for the user's return.
    public func appWillResignActive() {
        stopKeepalive()
    }

    public func disconnect() {
        reconnectWanted = false
        cancelReconnectBackoff()
        stopKeepalive()
        client?.close()
        client = nil
        connected = false
        status = "Not connected"
        refreshLiveActivity(ended: true)
    }

    /// Fills the connect fields from a scanned pairing payload (oculus://pair?...).
    ///
    /// Returns true when the pairing was applied, false when it would REPLACE the daemon identity key
    /// we already pinned — in which case it is staged on `pendingKeyChange` and the caller must get an
    /// explicit confirmation before calling `confirmKeyChange()`.
    ///
    /// Why this can't just assign, which is what it used to do:
    ///
    /// Pinning `daemonPubHex` is the ONLY thing standing between the user and a relay-side attacker.
    /// The channel is derived from the pinned key, so a substituted daemon key fails closed — an
    /// attacker cannot open the sealed proof or forge the verdict. That guarantee is worth exactly as
    /// much as the pin's stability, and the pin was being overwritten by any scanned QR with no
    /// comparison and no prompt.
    ///
    /// That turns a hard cryptographic barrier into a one-step social engineering problem, and the
    /// step is easy because a real attack LOOKS like the thing that prompts a re-pair: substitution
    /// presents as a connection that stopped working (see attemptConnect — the app cannot tell a
    /// changed key from an unreachable Mac). Break the connection, offer a fresh QR, and the user
    /// re-pairs to the attacker's key while the app says nothing at all. One accepted substitution is
    /// permanent, because from then on the attacker's key IS the pin.
    ///
    /// So: first pairing is frictionless (trust on first use — there is nothing to compare against),
    /// re-pairing to the same key is silent (the overwhelmingly common case), and a CHANGED key stops
    /// and asks. See `applyLocalPairing` for the one case where a changed key is trustworthy evidence.
    @discardableResult
    public func applyPairing(url: String, pub: String, secret: String, relay: String = "") -> Bool {
        // A payload with no key is malformed, not a key change. Refuse it outright: falling through
        // would blank the pin, which is a silent downgrade to "trust whatever answers next".
        guard !pub.isEmpty else { return false }
        if !daemonPubHex.isEmpty, pub != daemonPubHex {
            pendingKeyChange = PairingKeyChange(
                name: name.isEmpty ? "this Mac" : name,
                currentPub: daemonPubHex, newPub: pub,
                wsURL: url, secret: secret, relay: relay
            )
            return false
        }
        applyPairingUnchecked(url: url, pub: pub, secret: secret, relay: relay)
        return true
    }

    /// Applies a pairing whose evidence did NOT arrive over the network.
    ///
    /// The only caller is the macOS local-daemon path, which reads ~/.oculus/pairing.json — a 0600
    /// file in the user's own home directory. Writing it requires already being that user on that
    /// machine, at which point ~/.oculus/key is readable too and the whole model is void by
    /// construction. So a key change learned from that file is a daemon reinstall, not an attacker,
    /// and prompting for it would be teaching the user to click through a warning that is always
    /// benign — which is exactly how a warning stops working on the day it matters.
    func applyLocalPairing(url: String, pub: String, secret: String, relay: String = "") {
        applyPairingUnchecked(url: url, pub: pub, secret: secret, relay: relay)
    }

    private func applyPairingUnchecked(url: String, pub: String, secret: String, relay: String) {
        // A different Mac means a different device identity: drop the cached key so clientKey()
        // resolves the one filed under THIS daemon rather than presenting another Mac's key.
        if pub != self.daemonPubHex { cachedClientPrivate = nil }
        self.wsURL = url
        self.daemonPubHex = pub
        self.secret = secret
        self.relayURL = relay
        // The pairing code we just consumed is worth storing only until the daemon replaces it with a
        // real per-device credential (which it does on the first frame after the handshake). Persist
        // it anyway: a connection that drops before that frame arrives has to be able to retry.
        saveCredential()
    }

    /// A pairing that would replace the pinned daemon identity key, held until the user confirms.
    public struct PairingKeyChange: Identifiable, Equatable {
        public var id: String { newPub }
        public var name: String
        public var currentPub: String
        public var newPub: String
        public var wsURL: String
        public var secret: String
        public var relay: String

        /// The first 64 bits of each key, grouped — enough to compare against the `daemon pubkey:`
        /// line the daemon prints, without asking anyone to read 64 hex characters aloud. 64 bits is
        /// far past what an attacker could grind a matching prefix for.
        public var currentFingerprint: String { Model.keyFingerprint(currentPub) }
        public var newFingerprint: String { Model.keyFingerprint(newPub) }
    }

    /// Set when a scanned pairing would repin this connection to a different daemon key.
    @Published public var pendingKeyChange: PairingKeyChange?

    /// Accepts the staged identity change. Only call this from a control the user had to press after
    /// reading what changed.
    public func confirmKeyChange() {
        guard let c = pendingKeyChange else { return }
        pendingKeyChange = nil
        applyPairingUnchecked(url: c.wsURL, pub: c.newPub, secret: c.secret, relay: c.relay)
    }

    /// Discards the staged change and keeps the existing pin.
    public func cancelKeyChange() { pendingKeyChange = nil }

    /// Renders a public key as four groups of four hex characters.
    ///
    /// `nonisolated` because it reads nothing: it is a pure transform of the string it is handed.
    /// Model is @MainActor, so a static member inherits that isolation and the fingerprint could
    /// then only be computed from the main actor — which is wrong for the two callers that matter,
    /// both of which run in synchronous non-isolated context while DECIDING whether to prompt about
    /// a changed key. Hopping to the main actor to format a string there would mean the comparison
    /// and the decision could no longer be made in one step.
    nonisolated static func keyFingerprint(_ hex: String) -> String {
        let head = Array(hex.prefix(16))
        guard head.count == 16 else { return hex }
        return stride(from: 0, to: 16, by: 4)
            .map { String(head[$0 ..< $0 + 4]) }
            .joined(separator: " ")
    }

    // MARK: conversation

    /// Sends a user turn: creates the session on the first message, then follow-ups
    /// go to the same session (a real multi-turn conversation).
    public func send(_ text: String) async {
        var trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let imgs = pendingImages
        let files = pendingFiles
        guard (!trimmed.isEmpty || !imgs.isEmpty || !files.isEmpty) else { return }
        // Document attachments ride along as fenced text blocks so every provider sees their content.
        let shownBody = trimmed
        if !files.isEmpty {
            let blocks = files.map { "Attached file: \($0.name)\n```\n\($0.text)\n```" }.joined(separator: "\n\n")
            trimmed = trimmed.isEmpty ? blocks : blocks + "\n\n" + trimmed
        }
        var badges: [String] = []
        if !imgs.isEmpty { badges.append("🖼️ \(imgs.count) image\(imgs.count == 1 ? "" : "s")") }
        if !files.isEmpty { badges.append("📎 \(files.count) file\(files.count == 1 ? "" : "s")") }
        let shown = shownBody.isEmpty ? badges.joined(separator: "  ") : shownBody
        // Append the user's message FIRST — before any connectivity guard — so the text can NEVER be
        // silently dropped (the composer already cleared the draft). A disconnected send is marked
        // .failed and kept, retryable, instead of vanishing.
        let msgID = UUID()
        guard let client else {
            messages.append(ChatMessage(id: msgID, role: .user, text: shown, delivery: .failed))
            pendingRetry = (msgID, trimmed, imgs)
            setError("Not connected", "You’re not connected to the daemon, so your message wasn’t sent. It’s kept below — reconnect, then tap Retry.")
            return
        }
        messages.append(ChatMessage(id: msgID, role: .user, text: shown, delivery: .sending))
        pendingRetry = (msgID, trimmed, imgs)
        busy = true
        pendingImages = []
        pendingFiles = []
        if let sid = sessionID {
            await deliverPrompt(sessionID: sid, text: trimmed, images: imgs, allowReattach: true, messageID: msgID)
        } else {
            do {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                              payload: SessionCreate(provider: newSessionProvider,
                                                                     projectID: pendingProjectID,
                                                                     projectIDs: pendingProjectIDs,
                                                                     prompt: trimmed,
                                                                     images: imgs.isEmpty ? nil : imgs,
                                                                     worktree: pendingWorktree ? true : nil,
                                                                     workspaceName: pendingWorkspaceName,
                                                                     plan: pendingPlan ? true : nil))
                try await client.send(env)
                markDelivery(msgID, .ok)
                noteActivity()
            } catch {
                markDelivery(msgID, .failed)
                setError("Couldn’t start the session", error.localizedDescription)
                status = "Send failed: \(error.localizedDescription)"
                busy = false
            }
        }
    }

    /// Sends a prompt to an existing session and awaits the daemon's ack. If the daemon no
    /// longer knows the session (e.g. it restarted and forgot its in-memory sessions), the
    /// underlying opencode/claude session still lives server-side, so we transparently
    /// re-attach it and retry once — instead of the chat hanging on "working…" forever.
    private func deliverPrompt(sessionID sid: String, text: String, images: [ImageAttachment], allowReattach: Bool, messageID: UUID? = nil) async {
        do {
            _ = try await request(MessageType.sessionPrompt,
                                  payload: SessionPrompt(sessionID: sid, text: text, images: images.isEmpty ? nil : images))
            if let messageID { markDelivery(messageID, .ok) }
            noteActivity()
        } catch {
            let msg = error.localizedDescription.lowercased()
            if allowReattach, msg.contains("no such session") || msg.contains("no session"),
               let revived = await reattachCurrentSync() {
                await deliverPrompt(sessionID: revived, text: text, images: images, allowReattach: false, messageID: messageID)
                return
            }
            if let messageID { markDelivery(messageID, .failed) }
            setError("Couldn’t send your message", error.localizedDescription)
            status = "Send failed"
            busy = false
        }
    }

    /// Sets the alert title + message together so a delivery failure isn't mislabeled as a
    /// session-startup failure (the alert used to hardcode "Couldn't start the session").
    public func setError(_ title: String, _ message: String) {
        actionErrorTitle = title
        actionError = message
    }

    /// Updates a user message's delivery badge (sending → ok/failed).
    private func markDelivery(_ id: UUID, _ state: ChatMessage.Delivery) {
        if let idx = messages.firstIndex(where: { $0.id == id }) { messages[idx].delivery = state }
    }

    /// Resends the last failed user message verbatim (per-message Retry affordance).
    public func retryFailedMessage() async {
        guard let r = pendingRetry, let idx = messages.firstIndex(where: { $0.id == r.id }) else { return }
        messages[idx].delivery = .sending
        guard let sid = sessionID, client != nil else {
            messages[idx].delivery = .failed
            setError("Not connected", "Still not connected to the daemon. Reconnect and try again.")
            return
        }
        busy = true
        await deliverPrompt(sessionID: sid, text: r.text, images: r.images, allowReattach: true, messageID: r.id)
    }

    /// Re-establishes the daemon's managed session for the currently open session, keeping the
    /// on-screen transcript (used mid-send when the daemon forgot the session). Returns the
    /// revived session id, or nil. Replayed history is de-duplicated briefly so the chat
    /// doesn't double up.
    private func reattachCurrentSync() async -> String? {
        guard let s = currentSession else { return nil }
        dedupReplay = true
        Task { try? await Task.sleep(nanoseconds: 5_000_000_000); dedupReplay = false }
        do {
            let env = try await request(MessageType.sessionAttach,
                payload: SessionAttach(provider: s.provider, sessionID: s.id, url: nil, cwd: s.cwd))
            if let revived = try? env.payload(as: Session.self) {
                sessionID = revived.id; currentSession = revived; return revived.id
            }
            return s.id
        } catch {
            return nil
        }
    }

    /// Re-opens the currently active session after a reconnect (the daemon may have restarted and
    /// dropped its in-memory sessions). Repaints IN PLACE. No-op if nothing is open.
    ///
    /// This used to `messages.removeAll()` and reset the whole cache state, so every reconnect —
    /// including the two-second blips the keepalive now catches — blanked the conversation until a
    /// relay round trip refilled it. That is the swap moment, and a blank pane at the swap moment
    /// reads as data loss.
    ///
    /// The frames stay on screen and the attach replay is compared against them instead, by exactly
    /// the machinery a cache-painted open already uses: `transcriptPainted` is the frame-level record
    /// of what is rendered (the paint, plus every captured live frame), so arming
    /// `transcriptReconciling` makes `receiveLoop` buffer the replay and hand it to `finishReconcile`,
    /// which splices in only genuinely new frames and rebuilds wholesale if the two disagree.
    /// Clearing the painted set here is what forced the blank; keeping it WITHOUT arming the barrier
    /// is what used to append the whole replay a second time. Both halves are required.
    func reopenCurrentSession() async {
        guard let s = currentSession else { return }
        // Buffer the replay for reconciliation rather than applying it on top of what is up.
        transcriptReconciling = true
        transcriptReplayBuffer = []
        // A provider that re-streams its own history after an attach emits frames byte-identical to
        // ones already on screen; the guard window makes `captureFrame` drop those instead of
        // doubling the cache. Same window a cache-painted open uses.
        transcriptAnchorGuardUntil = Date().addingTimeInterval(20)
        armReopenReconcileCap()
        // Turn-scoped state does NOT survive the drop: the daemon re-asserts busy/turn/approval from
        // the replay, and a spinner left over from a turn that ended while we were away is a lie.
        busy = false
        pendingApproval = nil
        stopStallLoop()
        guard let client else { return } // nothing to attach to; the repaint state above still stands
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAttach,
                                          payload: SessionAttach(provider: s.provider, sessionID: s.id, url: nil, cwd: s.cwd))
            try await client.send(env)
        } catch { /* best-effort; the user can resend to trigger a mid-send re-attach */ }
    }

    /// Bounds the reopen reconcile. `bufferForReconcile` arms its own cap on the first replay frame,
    /// but a replay that never arrives at all (attach rejected, daemon wedged) would otherwise leave
    /// the barrier up forever, silently swallowing every subsequent live frame into the buffer —
    /// a session that looks frozen while the socket is fine.
    private func armReopenReconcileCap() {
        transcriptReconcileCap?.cancel()
        transcriptReconcileCap = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 8_000_000_000)
            guard !Task.isCancelled, let self, self.transcriptReconciling else { return }
            // finishReconcile deliberately stays armed on an empty buffer (the replay may still be in
            // flight), so this is the one place that has to give up outright.
            if self.transcriptReplayBuffer.isEmpty {
                self.transcriptReconciling = false
            } else {
                self.finishReconcile()
            }
        }
    }

    // MARK: liveness — Turn Engine cutover
    // The DAEMON now owns turn liveness (`turn.state` transitions + ~10s heartbeats, backed by
    // provider probes) — the client runs NO timer that can declare an agent dead. The one clock left
    // watches for missing FRAMES (heartbeats included): silence past ~4 heartbeats means the
    // CONNECTION is suspect → show the "stream may be stuck · Reconnect" hint. It never fabricates
    // "no response"; only a daemon-declared `abandoned` turn renders that.

    /// The daemon's authoritative state for the active session's turn (nil = none seen yet).
    @Published public var turn: TurnState?

    /// Every frame for the active session funnels through here: keeps the connection-health clock
    /// fed, clears the swap loader, and un-flags a suspected stall.
    private func noteActivity() {
        lastEventAt = Date()
        bumpSettle()
        if streamMaybeStalled { streamMaybeStalled = false }
        if sessionLoading { sessionLoading = false }
        startStallLoop()
    }

    private var stallTask: Task<Void, Never>?
    /// Heartbeats arrive every ~10s while a turn is open — 45s of NOTHING means the pipe is suspect.
    private let stallAfter: TimeInterval = 45

    private func startStallLoop() {
        guard stallTask == nil else { return }
        stallTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 2_000_000_000)
                guard let self else { return }
                let stale = self.busy && Date().timeIntervalSince(self.lastEventAt) > self.stallAfter
                if stale != self.streamMaybeStalled { self.streamMaybeStalled = stale }
            }
        }
    }

    private func stopStallLoop() {
        stallTask?.cancel(); stallTask = nil
        if streamMaybeStalled { streamMaybeStalled = false }
    }

    /// Applies the daemon's authoritative turn state — the replacement for the old client watchdog.
    /// Patience is unbounded while the daemon says `running`; "No response" exists ONLY as the
    /// daemon's `abandoned` verdict (provider probe failed repeatedly), never as a client guess.
    private func applyTurnState(_ ts: TurnState) {
        guard ts.sessionID == sessionID else { return }
        noteActivity()
        turn = ts
        switch ts.state {
        case SessionStatusValue.running:
            busy = true
        case SessionStatusValue.awaitingApproval:
            busy = false
        case SessionStatusValue.recovering:
            // The agent stopped answering and the daemon is rebuilding the connection. Stay busy —
            // the turn is still open and the work is very likely still running on the other side.
            // Nothing here is actionable, so it deliberately does NOT set an error: the old
            // behaviour turned every wifi handover into "No response from the agent".
            busy = true
            status = "Reconnecting…"
        case SessionStatusValue.stalled:
            // STILL WORKING, as far as anyone can prove — the daemon has decided nothing is
            // progressing and is nudging the agent to continue. Deliberately not an error and not a
            // dead end: keep the composer in its busy state so an accidental send can't race the
            // nudge, and say plainly what's happening instead of either lying ("working…") or
            // crying wolf ("no response").
            busy = true
            status = (ts.nudges ?? 0) > 0 ? "Stuck — nudged \(ts.nudges!)×" : "Stuck — nudging"
        case SessionStatusValue.needsYou:
            busy = false
            activity = nil
            finalizeStreaming()
            let reason = (ts.reason?.isEmpty == false) ? ts.reason! : "It stopped making progress."
            let note = "⏸ The agent is stuck and needs you — \(reason)"
            if messages.last?.text != note { messages.append(ChatMessage(role: .system, text: note)) }
            status = "Needs you"
            // NOT setError: nothing failed. This is a turn that stopped moving and didn't respond to
            // being nudged, and dressing it as an error is how the error banner became noise.
        case "abandoned":
            busy = false
            activity = nil
            finalizeStreaming()
            if let r = pendingRetry { markDelivery(r.id, .failed) }
            let reason = (ts.reason?.isEmpty == false) ? ts.reason! : "The agent stopped responding."
            let note = "⚠️ \(reason)"
            if messages.last?.text != note { messages.append(ChatMessage(role: .system, text: note)) }
            setError("No response from the agent", reason)
            status = "No response"
        default: // idle | error — session.status events already finalize the UI for these
            busy = false
        }
        // Terminal turn ⇒ no sub-agent can still be running. The daemon now seals children on every
        // close path too; this is the client-side backstop for an OLDER daemon (and for any seal
        // event lost in transit), because a card that spins forever is the failure users actually
        // saw — dozens of "Searching…" badges with no way to recover short of restarting the app.
        //
        // `stalled` is explicitly NOT terminal: the turn is still open and the children may still
        // come back, so sealing here would be a lie that the nudge is about to contradict.
        let stillOpen = ts.state == SessionStatusValue.running
            || ts.state == SessionStatusValue.awaitingApproval
            || ts.state == SessionStatusValue.stalled
            || ts.state == SessionStatusValue.recovering
        if !stillOpen {
            let failed = ts.state == "abandoned"
                || ts.state == SessionStatusValue.error
                || ts.state == SessionStatusValue.needsYou
            let sealed = failed ? "error" : "done"
            for (id, status) in subAgentStatus where status != "done" && status != "error" {
                subAgentStatus[id] = sealed
            }
        }
    }

    /// Deletes a daemon-managed session: halts its agent (which ends the session server-side)
    /// and removes it from the list immediately (optimistic — the next session.list confirms).
    /// No-op for discovered sessions (not owned here). Clears the conversation if it's on screen.
    public func stopSession(_ id: String) async {
        guard let client else { return }
        // Optimistic removal so the row disappears at once instead of lingering until the
        // server broadcasts the updated session list.
        sessions.removeAll { $0.id == id }
        if sessionID == id { newSession() }
        // Delete its cached transcript too. The cache holds this machine's source code and the
        // conversation about it; a session the user deleted should not leave that on the device.
        forgetCached(id)
        // Forget it as the auto-reopen target — otherwise the next reconnect re-opens (and
        // re-attaches) the just-deleted session, so it reappears.
        if defaults.string(forKey: lastSessionKey) == id { defaults.removeObject(forKey: lastSessionKey) }
        sessionErrors[id] = nil
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionStop,
                                          payload: SessionRef(sessionID: id))
            try await client.send(env)
        } catch {
            status = "Delete failed: \(error)"
        }
    }

    /// Renames a managed session (empty clears the label → back to the derived title). Updates
    /// locally at once; the daemon broadcasts the change to every client.
    public func renameSession(_ id: String, to name: String) async {
        guard let client else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if let i = sessions.firstIndex(where: { $0.id == id }) { sessions[i].name = trimmed.isEmpty ? nil : trimmed }
        if sessionID == id { currentSession?.name = trimmed.isEmpty ? nil : trimmed }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionRename,
                                          payload: SessionRename(sessionID: id, name: trimmed))
            try await client.send(env)
        } catch {
            status = "Rename failed: \(error.localizedDescription)"
        }
    }

    /// Adds an image to be sent with the next prompt (converted to base64).
    public func attachImage(mime: String, data: Data) {
        pendingImages.append(ImageAttachment(mime: mime, data: data.base64EncodedString()))
    }

    /// Attaches a document (its extracted text) to send with the next prompt.
    public func attachFile(name: String, text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        pendingFiles.append(FileAttachment(name: name, text: trimmed))
    }

    /// Attaches to an existing session discovered on the host: loads its history and
    /// continues it live. opencode sessions only (claude-code transcripts are view-only).
    /// Clears the current conversation to start a fresh session on the next message.
    public func newSession() {
        sessionID = nil
        currentSession = nil
        messages.removeAll()
        pendingApproval = nil
        busy = false
        lastDiff = nil
        todos = []
        testOutput = []; testResult = nil; testRunning = false; showTests = false
        // A bang run belongs to the session it was typed in. Leaving the marker set would route the
        // OLD session's remaining output into whatever row the new session appends first.
        shellRunID = nil; shellBuffer = ""; shellEcho = nil
        shellFlushTask?.cancel(); shellFlushTask = nil
    }

    /// Starts a fresh session with explicit options (provider, one or more project folders,
    /// and an opt-in git worktree). Options apply when the first message creates the session.
    /// Passing 2+ project IDs makes a multi-root workspace (the daemon runs in their common
    /// ancestor); a single folder uses `projectID` and can be worktree-isolated.
    public func newSession(provider: String, projectID: String? = nil, projectIDs: [String]? = nil, worktree: Bool = false, workspaceName: String? = nil, plan: Bool = false) {
        newSessionProvider = provider
        let multi = (projectIDs?.count ?? 0) > 1
        pendingProjectIDs = multi ? projectIDs : nil
        pendingProjectID = multi ? nil : (projectID ?? projectIDs?.first)
        pendingWorktree = multi ? false : worktree
        pendingWorkspaceName = workspaceName
        newSession()
        pendingPlan = plan
    }

    /// Eagerly creates a session NOW (no prompt) so it appears in the sidebar and opens in the
    /// detail immediately — rather than only stashing options until the first message (which
    /// looked like "nothing happened"). The provider makes an idle session; the first message
    /// then prompts it. 2+ folders → a multi-root workspace (common ancestor, no worktree).
    /// Fetches the models a provider offers (for the New Session picker), without a live session.
    public func providerModels(_ provider: String) async -> (models: [ModelInfo], editable: Bool) {
        guard client != nil else { return ([], false) }
        if let resp = try? await request(MessageType.modelList, payload: ModelListReq(provider: provider)),
           let ml = try? resp.payload(as: ModelList.self) {
            return (ml.models, ml.editable)
        }
        return ([], false)
    }

    /// Folds a streamed create step into `createSteps`: repeated events for the same stage update
    /// that row (e.g. worktree 1/3 → 2/3); a new stage marks the prior steps done and appends the
    /// current one. "ready" completes them all.
    private func applyCreateStep(_ p: SessionProgress) {
        if p.stage == "ready" {
            for i in createSteps.indices { createSteps[i].done = true }
            return
        }
        if let i = createSteps.firstIndex(where: { $0.stage == p.stage }) {
            createSteps[i].detail = p.detail
            createSteps[i].step = p.step ?? 0
            createSteps[i].total = p.total ?? 0
        } else {
            for i in createSteps.indices { createSteps[i].done = true } // prior phases finished
            createSteps.append(CreateStep(stage: p.stage, detail: p.detail, step: p.step ?? 0, total: p.total ?? 0, done: false))
        }
    }

    /// - Parameter prompt: the first instruction, sent WITH the create. The agent then works during
    ///   bootstrap instead of idling until the user re-engages — which on a phone, where you open the
    ///   app to start something and put it away again, is the difference between one interaction and
    ///   two. The daemon has always accepted this; only the primary sheet failed to send it.
    public func createSession(provider: String, projectIDs: [String]? = nil, worktree: Bool = false, workspaceName: String? = nil, mode: String = SessionMode.code, autonomous: Bool = false, model: String? = nil, modelProvider: String? = nil, prompt: String? = nil) async {
        guard client != nil else { return }
        startingProvider = provider
        createSteps = [] // fresh checklist; the daemon streams session.progress as it works
        startingSession = true // loading overlay + UI lock until the session is ready
        defer { startingSession = false; createSteps = [] }
        newSession() // clear the conversation; the created session replaces it on success
        self.autonomous = autonomous
        let multi = (projectIDs?.count ?? 0) > 1
        do {
            // Await the reply so a failure (e.g. the provider's backend is unreachable) surfaces to
            // the user instead of silently doing nothing. On success the daemon returns the Session.
            let resp = try await request(MessageType.sessionCreate,
                payload: SessionCreate(provider: provider,
                                       projectID: multi ? nil : projectIDs?.first,
                                       projectIDs: multi ? projectIDs : nil,
                                       prompt: prompt?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? prompt : nil,
                                       // Isolation applies to both single-repo (one worktree) and
                                       // multi-repo (a worktree per repo) — the daemon branches on
                                       // it. Don't drop it for the multi-repo case.
                                       worktree: worktree ? true : nil,
                                       workspaceName: workspaceName,
                                       // `plan` stays populated so an OLDER daemon (which knows only
                                       // the bool) still starts architect sessions in plan mode.
                                       plan: mode == SessionMode.architect ? true : nil,
                                       mode: mode == SessionMode.code ? nil : mode,
                                       autonomous: autonomous ? true : nil,
                                       model: model,
                                       modelProvider: modelProvider))
            let s = try resp.payload(as: Session.self)
            sessionID = s.id
            currentSession = s
            // Optimistically place it in the sidebar NOW so the empty-state → split transition
            // happens synchronously with currentSession set. Otherwise the fire-and-forget
            // session.list broadcast can land after `startingSession` clears, leaving the empty
            // CTA (or a split whose detail races currentSession) until the user reopens it.
            if !sessions.contains(where: { $0.id == s.id }) { sessions.insert(s, at: 0) }
            refreshLiveActivity()
            // Subscribe so the fresh session streams events immediately (not only after the first send).
            if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: s.id)) {
                try? await client?.send(env)
            }
            await loadSessions() // reconcile with the daemon's authoritative list
            await loadCommands(sessionID: s.id) // populate the "/" palette for this agent
            await loadModels(sessionID: s.id)   // populate the header model picker
        } catch {
            // Surface prominently: the New Session sheet has already dismissed, so status text alone
            // is invisible while connected — actionError drives an alert on the main surface.
            let reason = error.localizedDescription
            actionError = "Couldn’t start \(provider).\n\n\(reason)\n\nCheck the agent is installed and running, or pick another agent."
            status = "Couldn’t start \(provider)"
            statusDetail = reason
        }
    }

    /// Fetches the agent providers this daemon has registered, so the new-session picker shows the
    /// real set (including generic CLI agents like codex/gemini) instead of a hardcoded default.
    public func listProviders() async {
        guard client != nil else { return }
        defer { providersLoaded = true }
        if let resp = try? await request(MessageType.providerList, payload: ProviderList()),
           let pl = try? resp.payload(as: ProviderList.self) {
            applyProviders(pl.providers)
        }
    }

    /// The user's preferred default agent harness (persisted). Empty = "auto" (first detected).
    private var defaultAgentKey: String { "oculus.defaultAgent" }
    public var defaultAgent: String {
        get { defaults.string(forKey: defaultAgentKey) ?? "" }
    }

    /// Sets the preferred default agent harness for new sessions + chats (persisted). Pass "" to
    /// reset to auto (first detected). Applies immediately if it's available.
    public func setDefaultAgent(_ provider: String) {
        defaults.set(provider, forKey: defaultAgentKey)
        if provider.isEmpty {
            newSessionProvider = providers.first ?? newSessionProvider
        } else if providers.contains(provider) {
            newSessionProvider = provider
        }
        objectWillChange.send()
    }

    /// Adopt a provider set (from a request reply or an unsolicited provider.list broadcast) and keep
    /// the default selection valid. Resolution order: the user's persisted preferred harness (if it's
    /// actually detected) → the current selection (if still available) → the first detected harness.
    func applyProviders(_ list: [String]) {
        providers = list
        let pref = defaultAgent
        if !pref.isEmpty, list.contains(pref) {
            newSessionProvider = pref
        } else if !list.contains(newSessionProvider), let first = list.first {
            newSessionProvider = first
        }
    }

    /// Re-detects agent harnesses on the daemon's PATH (opencode/claude-code/pi + CLIs) without a
    /// restart, then reloads the roster. Use when you've just installed an agent.
    public func rescanAgents() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.providerRefresh, payload: ProviderList()),
           let pl = try? resp.payload(as: ProviderList.self) { applyProviders(pl.providers) }
        await loadAgents()
    }

    /// Full agent roster for the management UI.
    public func loadAgents() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.agentList, payload: Optional<Int>.none),
           let al = try? resp.payload(as: AgentList.self) {
            agents = al.agents
        }
    }

    /// Add or edit a custom CLI agent; the daemon persists it, registers it live, and returns the
    /// updated roster. Returns an error message on failure (nil on success).
    @discardableResult
    public func upsertAgent(_ a: AgentUpsert) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentUpsert, payload: a)
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Loads the models available to the current session's provider (for the header picker).
    /// Clears to non-editable if the provider is agent-managed (e.g. pi, generic CLI).
    public func loadModels(sessionID override: String? = nil) async {
        guard client != nil, let sid = override ?? sessionID else { sessionModels = []; modelEditable = false; return }
        if let resp = try? await request(MessageType.modelList, payload: ModelListReq(sessionID: sid)),
           let ml = try? resp.payload(as: ModelList.self) {
            sessionModels = ml.models
            modelEditable = ml.editable
            currentModel = ml.current?.isEmpty == false ? ml.current : currentSession?.model
        } else {
            sessionModels = []; modelEditable = false
        }
    }

    /// Switches the current session's model.
    @discardableResult
    public func setSessionModel(_ m: ModelInfo) async -> String? {
        guard client != nil, let sid = sessionID else { return "No session" }
        do {
            _ = try await request(MessageType.sessionSetModel, payload: SessionSetModel(sessionID: sid, model: m.id, provider: m.provider))
            currentModel = m.id
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Show or hide an agent in the session pickers (any kind). The daemon persists it and pushes
    /// the new visible set to every client.
    @discardableResult
    public func setAgentVisible(_ name: String, _ visible: Bool) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentVisible, payload: AgentVisible(name: name, visible: visible))
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Remove a custom CLI agent.
    @discardableResult
    public func deleteAgent(_ name: String) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentDelete, payload: AgentRef(name: name))
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Fetches the handoff index (optionally scoped to a working directory). Also arrives
    /// unsolicited via the handoff.list event whenever an agent saves progress.
    public func listHandoffs(cwd: String? = nil) async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.handoffList, payload: HandoffList(cwd: cwd)),
           let hl = try? resp.payload(as: HandoffList.self) {
            handoffs = hl.handoffs
        }
    }

    /// The indexed handoff for the active session, if the agent has saved one.
    public var activeHandoff: HandoffEntry? {
        guard let sid = sessionID else { return nil }
        return handoffs.first { $0.sessionID == sid }
    }

    /// Delegates a subtask to a scoped sub-agent seeded from the parent's handoff (not its
    /// transcript). On success the child becomes the active session (it arrives via the OK).
    public func delegateSubtask(subtask: String, files: [String]? = nil, autonomous: Bool = false) async {
        guard client != nil, let parent = sessionID else { return }
        let trimmed = subtask.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        do {
            let resp = try await request(MessageType.sessionChild,
                                         payload: SessionChild(parentSessionID: parent, subtask: trimmed,
                                                               files: files, autonomous: autonomous))
            let child = try resp.payload(as: Session.self)
            // Only now replace the view — a failed delegate must not lose the parent conversation.
            // The daemon already subscribed this connection to the child, so its transcript replays
            // over the subscription.
            newSession()
            self.autonomous = autonomous
            sessionID = child.id
            currentSession = child
        } catch {
            status = "Delegate failed: \(error.localizedDescription)"
        }
    }

    /// Toggles autonomous heartbeat supervision for the active session. Re-arming (autonomous =
    /// true) also resets the daemon's nudge counter so a previously-exhausted session runs again.
    public func setAutonomy(_ on: Bool) async {
        autonomous = on
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAutonomy,
                                          payload: SessionAutonomy(sessionID: sid, autonomous: on)) {
            try? await client.send(env)
        }
    }

    /// Runs the session's tests/build (daemon auto-detects the command, or pass one). Output
    /// streams into testOutput; the outcome lands in testResult.
    public func runTests(command: String? = nil) async {
        guard let client, let sid = sessionID else { return }
        testOutput = []
        testResult = nil
        testRunning = true
        showTests = true
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.runTest,
                                          payload: RunTest(sessionID: sid, command: command)) {
            try? await client.send(env)
        }
    }

    // MARK: !command — run a shell command yourself

    /// True while EITHER a test run or a `!command` owns the run.output stream. Both surfaces gate
    /// on this: the daemon tags run output with a session, not a run, so two concurrent runs would
    /// interleave their lines into whichever surface happened to be listening.
    public var runBusy: Bool { testRunning || shellRunID != nil }

    /// Runs `command` on the host, in this session's workspace, and streams it into its own
    /// transcript row.
    ///
    /// The AGENT IS NOT INVOLVED — it does not run the command, is not told the command ran, and
    /// never sees the output unless the user taps "Send to agent" on the row. That matches Claude
    /// Code's `!` behaviour, and it is the point of the feature: a quick `git status` should cost
    /// nothing and change nothing about the conversation.
    ///
    /// Uses the correlated request path rather than fire-and-forget, so a REFUSAL (shell is
    /// owner-only) resolves into the row that asked for it. Fire-and-forget would surface the
    /// daemon's error as a detached global alert with no visible link to the command you just typed.
    public func runShell(_ command: String) async {
        let cmd = command.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cmd.isEmpty else { return }
        guard let sid = sessionID else {
            actionError = "Open or start a session first — a command runs in that session's folder."
            return
        }
        guard !runBusy else {
            actionError = "Another run is still going. Wait for it to finish, then run this."
            return
        }
        finalizeStreaming() // seal any in-flight agent text so the command row doesn't split it
        let call = ToolCall(id: "shell-\(UUID().uuidString)", name: "!", title: cmd,
                            output: "", status: "running")
        let row = ChatMessage(role: .shell, text: cmd, tool: call)
        messages.append(row)
        shellRunID = row.id
        // The runner opens every run by echoing "$ <command>" (runner.go). We use that first line
        // twice: to drop it (the header already shows the command) and, when it DOESN'T match, to
        // notice that our request was dropped — see claimShellLine.
        shellEcho = "$ " + cmd
        do {
            _ = try await request(MessageType.runTest, payload: RunTest(sessionID: sid, command: cmd))
        } catch {
            // Refused (owner-only), unsupported by an older daemon, or the socket dropped. Land it
            // ON the row: the user asked this specific question and deserves this specific answer.
            finishShellRun(ok: false, exitCode: -1, note: error.localizedDescription)
        }
    }

    /// Buffered command output. Coalesced on the same 40ms timer as token streaming — a chatty
    /// command emits lines far faster than a screen refreshes, and mutating the @Published messages
    /// array per line re-diffs the whole transcript each time.
    private var shellBuffer = ""
    private var shellFlushTask: Task<Void, Never>?
    /// The "$ <command>" line we expect to open OUR run, cleared once the first line arrives.
    private var shellEcho: String?

    /// Routes one run.output line to the in-flight `!command` row. Returns false when the line
    /// demonstrably belongs to a DIFFERENT run, so the caller sends it to the test panel instead.
    ///
    /// The daemon runs one command per session and, when a second is requested, returns without
    /// emitting anything at all (runTestLimits' "someone else is already running" path). Another
    /// device's test run therefore looks exactly like ours never being answered. The first output
    /// line disambiguates it: our run always opens with our own command echoed back, so a first line
    /// that isn't ours means ours was dropped. Without this check the row would spin forever while
    /// displaying somebody else's output as its own result.
    private func claimShellLine(_ line: String) -> Bool {
        guard let echo = shellEcho else {
            appendShellOutput(line)
            return true
        }
        shellEcho = nil
        if line == echo { return true } // our run started; the header already shows the command
        finishShellRun(ok: false, exitCode: -1,
                       note: "Another run is already in progress on this session — this command didn't start. Try again once it finishes.")
        return false
    }
    /// Keep at most this much output on a row. A `yes` or a full build log would otherwise grow the
    /// transcript without bound; the tail is the part you ran it for. The ROW shows less again (see
    /// BangCommand.previewOutput) — this is what "Send to agent" and text selection can reach.
    private static let shellOutputCap = 64_000

    private func appendShellOutput(_ line: String) {
        shellBuffer += line + "\n"
        guard shellFlushTask == nil else { return }
        shellFlushTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: Model.flushInterval)
            guard let self else { return }
            self.shellFlushTask = nil
            self.flushShellOutput()
        }
    }

    private func flushShellOutput() {
        let pending = shellBuffer
        shellBuffer = ""
        guard !pending.isEmpty, let id = shellRunID,
              let idx = messages.firstIndex(where: { $0.id == id }), messages[idx].tool != nil else { return }
        var out = messages[idx].tool!.output + pending
        if out.count > Model.shellOutputCap {
            out = "…(earlier output trimmed)\n" + String(out.suffix(Model.shellOutputCap))
        }
        messages[idx].tool!.output = out
    }

    /// Seals the active bang-command row with its outcome. `note` is appended to the output for
    /// failures that produced no output of their own (a refusal, a dropped socket).
    private func finishShellRun(ok: Bool, exitCode: Int, note: String? = nil) {
        shellFlushTask?.cancel(); shellFlushTask = nil
        shellEcho = nil
        flushShellOutput()
        guard let id = shellRunID else { return }
        shellRunID = nil
        shellExit[id] = exitCode
        guard let idx = messages.firstIndex(where: { $0.id == id }), messages[idx].tool != nil else { return }
        if let note, !note.isEmpty {
            let existing = messages[idx].tool!.output
            messages[idx].tool!.output = existing.isEmpty ? note : existing + "\n" + note
        }
        messages[idx].tool!.status = ok ? "completed" : "error"
    }

    /// Exit code per finished bang-command row, so the card can show "exit 1" without inventing a
    /// field on ToolCall (which models the AGENT's tool calls and has no exit concept).
    @Published public var shellExit: [UUID: Int] = [:]

    /// Interrupts the current agent turn without ending the session — so you can redirect it
    /// with a new prompt (mid-run steering).
    public func interrupt() async {
        guard let client, let sid = sessionID else { return }
        // Seal whatever text was mid-stream so it doesn't merge into the next turn, and say we're
        // asking — but do NOT declare the turn over. That is the daemon's to report.
        //
        // This used to set busy = false and status = "Interrupted" immediately, which meant the UI
        // claimed the agent had stopped whether or not it actually had: if the send failed, or the
        // provider ignored the abort (the wedged turn people press Stop for in the first place), the
        // agent kept working behind a UI that insisted it hadn't. The daemon closes the turn on
        // interrupt now and broadcasts it, so the honest thing is to wait the one round-trip.
        finalizeStreaming()
        activity = nil
        status = "Interrupting…"
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionInterrupt,
                                          payload: SessionRef(sessionID: sid)) {
            try? await client.send(env)
        }
    }

    // MARK: projects + worktrees

    public func loadProjects() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.projectList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    public func loadSessions() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    // MARK: trackers (Linear/Jira)

    public func connectTracker(provider: String, token: String) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.integrationConnect,
                                          payload: IntegrationConnect(provider: provider, token: token)) {
            try? await client.send(env)
            await loadIssues()
        }
    }

    /// Removes a tracker's connection (clears its token on the daemon, keeping the OAuth app so
    /// reconnecting is one tap) and refreshes the board. Also used to clear a broken connection.
    public func disconnectTracker(_ provider: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationDisconnect,
                                         payload: IntegrationConnect(provider: provider, token: ""))
            if let st = try? resp.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
            await loadIssues()
        } catch {
            trackerError = "Couldn’t disconnect \(provider): \(error.localizedDescription)"
        }
    }

    /// Folds an IntegrationStatus into the published tracker state (one place so every path — the
    /// broadcast, connect, disconnect, oauth-app — stays consistent).
    func applyIntegrationStatus(_ st: IntegrationStatus) {
        connectedTrackers = st.connected
        oauthApps = st.oauthApps ?? []
        trackerAuthErrors = st.authErrors ?? []
        trackerAuthDetails = st.authErrorDetails ?? [:]
        jiraSiteAmbiguous = st.jiraSiteAmbiguous ?? false
    }

    /// Begins an OAuth flow for a tracker (linear/jira). The daemon replies with an authorize URL,
    /// which is opened in a browser. Surfaces a clear error (e.g. no OAuth app configured) via
    /// `trackerError` instead of silently doing nothing.
    public func startOAuth(provider: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationOAuth, payload: IntegrationOAuth(provider: provider))
            if let oa = try? resp.payload(as: IntegrationOAuth.self), let u = oa.url, let url = URL(string: u) {
                oauthURL = url
            } else {
                trackerError = "\(provider): the daemon didn't return an authorize URL."
            }
        } catch {
            trackerError = "Couldn't start \(provider) OAuth: \(error.localizedDescription)"
        }
    }

    /// Saves a tracker's OAuth app credentials (client_id/secret) on the daemon, then starts the
    /// OAuth flow — so the user never has to hand-edit integrations.json.
    public func setOAuthApp(provider: String, clientID: String, clientSecret: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationOAuthApp,
                                         payload: IntegrationOAuthApp(provider: provider, clientID: clientID, clientSecret: clientSecret))
            if let st = try? resp.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
            await startOAuth(provider: provider)
        } catch {
            trackerError = "Couldn't save the \(provider) OAuth app: \(error.localizedDescription)"
        }
    }

    public func startLinearOAuth() async { await startOAuth(provider: "linear") }

    /// Lists the Atlassian sites the Jira token can access (+ the active one). Empty on non-OAuth or
    /// single-site setups (the picker only matters with >1 site).
    public func loadJiraSites() async {
        guard client != nil, connectedTrackers.contains("jira") else { jiraSites = []; return }
        if let resp = try? await request(MessageType.jiraSites, payload: Optional<Int>.none),
           let js = try? resp.payload(as: JiraSites.self) {
            jiraSites = js.sites
            jiraCurrentSite = js.current ?? ""
        }
    }

    /// Switches the active Jira site (cloud id) — no re-auth; the token spans all the org's sites.
    public func setJiraSite(_ cloudID: String) async {
        guard client != nil else { return }
        trackerError = nil
        jiraCurrentSite = cloudID // optimistic
        if let resp = try? await request(MessageType.jiraSetSite, payload: JiraSetSite(cloudID: cloudID)),
           let st = try? resp.payload(as: IntegrationStatus.self) {
            applyIntegrationStatus(st)
        }
        await loadIssues()
    }

    /// Reads whether the daemon is sending anonymized diagnostics (drives the Settings toggle).
    public func loadTelemetryStatus() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.telemetryStatus, payload: Optional<Int>.none),
           let t = try? resp.payload(as: Telemetry.self) {
            telemetryEnabled = t.enabled
        }
    }

    /// Turns anonymized diagnostics on/off on the daemon (persisted there).
    public func setTelemetry(_ on: Bool) async {
        guard client != nil else { return }
        telemetryEnabled = on // optimistic
        if let resp = try? await request(MessageType.telemetrySet, payload: Telemetry(enabled: on)),
           let t = try? resp.payload(as: Telemetry.self) {
            telemetryEnabled = t.enabled
        }
    }

    public func loadIntegrationStatus() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.integrationStatus, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    public func loadIssues() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.issueList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    // MARK: Kanban board — projects / columns / move / create

    private enum BoardKeys {
        static let project = "oculus.board.project"
        static func order(_ p: String) -> String { "oculus.board.order.\(p)" }
        static func hidden(_ p: String) -> String { "oculus.board.hidden.\(p)" }
    }

    /// Reads a project's saved column order + hidden set from defaults into the published caches.
    private func loadBoardPrefs(for project: String) {
        if boardColumnOrder[project] == nil {
            let s = defaults.string(forKey: BoardKeys.order(project)) ?? ""
            boardColumnOrder[project] = s.isEmpty ? [] : s.components(separatedBy: ",")
        }
        if hiddenBoardColumns[project] == nil {
            let s = defaults.string(forKey: BoardKeys.hidden(project)) ?? ""
            hiddenBoardColumns[project] = Set(s.isEmpty ? [] : s.components(separatedBy: ","))
        }
    }

    /// The provider (linear/jira) that owns a given project id.
    private func providerForProject(_ id: String) -> String? {
        issueProjects.first(where: { $0.id == id })?.provider
    }

    /// Switches the active board, persisting the choice and hydrating its column prefs.
    public func selectProject(_ id: String?) {
        selectedProjectID = id
        if let id {
            defaults.set(id, forKey: BoardKeys.project)
            loadBoardPrefs(for: id)
        } else {
            defaults.removeObject(forKey: BoardKeys.project)
        }
    }

    /// All columns for the current board, applying the saved order (unordered ids fall back to position).
    public func orderedColumns() -> [IssueState] {
        guard let p = selectedProjectID, let order = boardColumnOrder[p], !order.isEmpty else {
            return boardColumns.sorted { $0.position < $1.position }
        }
        return boardColumns.sorted { a, b in
            let ia = order.firstIndex(of: a.id) ?? Int.max
            let ib = order.firstIndex(of: b.id) ?? Int.max
            if ia != ib { return ia < ib }
            return a.position < b.position
        }
    }

    /// Visible columns for the current board (ordered, minus hidden).
    public func visibleColumns() -> [IssueState] {
        guard let p = selectedProjectID else { return orderedColumns() }
        let hidden = hiddenBoardColumns[p] ?? []
        return orderedColumns().filter { !hidden.contains($0.id) }
    }

    /// Columns the user has hidden on the current board (for a "reveal" menu).
    public func hiddenColumns() -> [IssueState] {
        guard let p = selectedProjectID, let hidden = hiddenBoardColumns[p], !hidden.isEmpty else { return [] }
        return orderedColumns().filter { hidden.contains($0.id) }
    }

    public func hideBoardColumn(_ id: String) {
        guard let p = selectedProjectID else { return }
        var s = hiddenBoardColumns[p] ?? []
        s.insert(id)
        hiddenBoardColumns[p] = s
        defaults.set(s.sorted().joined(separator: ","), forKey: BoardKeys.hidden(p))
    }

    public func showBoardColumn(_ id: String) {
        guard let p = selectedProjectID else { return }
        var s = hiddenBoardColumns[p] ?? []
        s.remove(id)
        hiddenBoardColumns[p] = s
        defaults.set(s.sorted().joined(separator: ","), forKey: BoardKeys.hidden(p))
    }

    /// Moves a column one slot left/right within the visible order, persisting the new order.
    public func moveBoardColumn(_ id: String, left: Bool) {
        guard let p = selectedProjectID else { return }
        var ids = orderedColumns().map(\.id)
        guard let idx = ids.firstIndex(of: id) else { return }
        let target = left ? idx - 1 : idx + 1
        guard ids.indices.contains(target) else { return }
        ids.swapAt(idx, target)
        boardColumnOrder[p] = ids
        defaults.set(ids.joined(separator: ","), forKey: BoardKeys.order(p))
    }

    /// Loads the selectable boards; defaults the selection to the first if none is set.
    public func loadIssueProjects() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.issueProjects, payload: Optional<Int>.none),
           let list = try? resp.payload(as: IssueProjectsList.self) {
            issueProjects = list.projects
            if selectedProjectID == nil || !list.projects.contains(where: { $0.id == selectedProjectID }) {
                selectProject(list.projects.first?.id)
            } else if let p = selectedProjectID {
                loadBoardPrefs(for: p)
            }
        }
    }

    /// Loads the current board's workflow-status columns (needs a selected project + known provider).
    public func loadBoardColumns() async {
        guard client != nil, let p = selectedProjectID, let provider = providerForProject(p) else {
            boardColumns = []
            return
        }
        if let resp = try? await request(MessageType.issueColumns, payload: IssueColumnsReq(provider: provider, project: p)),
           let list = try? resp.payload(as: IssueStateList.self) {
            boardColumns = list.states.sorted { $0.position < $1.position }
        }
    }

    /// Moves a card to a workflow status. Optimistic: the card jumps to the target column immediately,
    /// is replaced with the daemon's returned issue on success, and reverts (with an error) on failure.
    public func moveIssue(_ id: String, toStatus statusID: String) async {
        guard let idx = issues.firstIndex(where: { $0.id == id }) else { return }
        let original = issues[idx]
        if let col = boardColumns.first(where: { $0.id == statusID }) {
            issues[idx].status = col.name
            issues[idx].category = col.category
        }
        do {
            let updated = try await request(MessageType.issueMove,
                payload: IssueMove(provider: original.provider, issueID: id, statusID: statusID))
                .payload(as: Issue.self)
            if let i = issues.firstIndex(where: { $0.id == updated.id }) { issues[i] = updated }
        } catch {
            if let i = issues.firstIndex(where: { $0.id == id }) { issues[i] = original }
            trackerError = "Couldn’t move ticket: \(error.localizedDescription)"
        }
    }

    /// Creates a ticket on a board and refreshes the list on success.
    public func createIssue(project: String, title: String, description: String? = nil,
                            priority: Int? = nil, type: String? = nil) async {
        guard client != nil, let provider = providerForProject(project) else { return }
        trackerError = nil
        do {
            _ = try await request(MessageType.issueCreate,
                payload: IssueCreate(provider: provider, project: project, title: title,
                                     description: description, priority: priority, type: type))
                .payload(as: Issue.self)
            await loadIssues()
        } catch {
            trackerError = "Couldn’t create ticket: \(error.localizedDescription)"
        }
    }

    /// Launches an agent on a ticket (worktree on its branch). Requires a project (repo).
    public func launchIssue(_ issue: Issue, projectID: String, agentProvider: String? = nil) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.issueLaunch,
                                          payload: IssueLaunch(issueID: issue.id, provider: issue.provider,
                                                               projectID: projectID, worktree: true,
                                                               agentProvider: agentProvider)) {
            try? await client.send(env)
        }
    }

    /// Observes an existing hub-managed session (replays its transcript, then live).
    public func openSession(_ id: String) async {
        guard let client else { return }
        // Already the open session → no-op. Re-running the full clear+resubscribe (e.g. when you
        // click the active row again) briefly wiped messages/currentSession and blanked the detail.
        //
        // This check comes FIRST on purpose: arming the de-duplicator before it meant a harmless
        // re-click on the active session put a live, streaming conversation into dedup mode for five
        // seconds, where any new message repeating earlier text would be silently dropped.
        if id == currentSession?.id, !messages.isEmpty { return }
        // A self-replaying provider may re-stream history right after we subscribe, on top of the
        // daemon's replay — and a trimmed ring now replays durable history in front of the live
        // window, which overlaps too. Arm the de-duplicator so those overlaps collapse instead of
        // doubling. Only for a real switch, where the transcript is about to be cleared anyway.
        // A stopped session (daemon restarted, provider couldn't re-attach) has nothing to subscribe
        // to — load it and let ChatView show a Restart affordance, instead of erroring on subscribe.
        if let s = sessions.first(where: { $0.id == id }), s.status == SessionStatusValue.stopped {
            sessionID = id
            currentSession = s
            messages.removeAll()
            todos = []; pendingApproval = nil; busy = false; lastDiff = nil
            turn = nil // stopped session has no live turn
            finishSettling()
            clearChildState()
            UserDefaults.standard.set(id, forKey: lastSessionKey)
            return
        }
        // Switch the active session id NOW, before clearing/subscribing — so streaming events from
        // the session we're leaving (still Live) are filtered out instead of bleeding into this one.
        // Hand what is on screen back to the in-memory cache for the session being LEFT. Without
        // this, A -> B -> A repaints from whatever was on disk at hydration time and loses everything
        // that streamed in between — the replay would recover it, but only after the round trip this
        // whole feature exists to hide.
        if let prev = sessionID, !transcriptPainted.isEmpty { transcriptHydrated[prev] = transcriptPainted }
        sessionID = id
        messages.removeAll()
        todos = []
        pendingApproval = nil
        busy = false
        lastDiff = nil
        resetTranscriptCacheState()
        // Paging state belongs to the session being LEFT. It's assigned only when a page.end arrives,
        // so without this the previous session's "there is more history" leaked into a session that
        // has none — and offered a Load-earlier button that could never load anything.
        hasEarlierHistory = false
        loadingEarlier = false
        pageAnchor = nil
        daemonEventsRendered = 0
        turn = nil // the new session's turn.state will repopulate
        clearChildState() // a new parent session starts with no expanded/subscribed children
        // If this session's frames are already in memory, paint them in THIS tick — no loader, no
        // settle, no de-duplicator. SwiftUI commits once and ChatView builds a scroll view that is
        // already full and already bottom-anchored, which is the entire perceived win. The daemon's
        // replay still arrives and is reconciled against what we painted.
        if paintFromCache(id) {
            sessionLoading = false
        } else {
            sessionLoading = true
            beginSettling() // hide the transcript until the replay burst lands, then reveal at the bottom
            dedupReplay = true
            Task { try? await Task.sleep(nanoseconds: 5_000_000_000); dedupReplay = false }
        }
        UserDefaults.standard.set(id, forKey: lastSessionKey) // remember for auto-reopen next launch
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            try? await client.send(env)
        }
        armLoadTimeout() // a session with NO history streams no replay events — stop the loader after a beat
        await loadCommands(sessionID: id)
        await loadModels(sessionID: id)
    }

    /// True while a just-opened session waits for its replayed transcript — drives the in-chat loader
    /// so a swap reads as "loading…" instead of a blank pane. Cleared on the first event for the
    /// session (bumpWatchdog) or by armLoadTimeout when the session has no history.
    @Published public var sessionLoading = false
    private var loadToken = 0

    /// True while a just-opened session's history is REPLAYING. The chat hides the scroll view behind
    /// the loader until the burst settles, then builds it once — fully formed, natively anchored at
    /// the bottom — instead of visibly scrolling through history as appends land one by one.
    @Published public var transcriptSettling = false
    private var settleTask: Task<Void, Never>?
    private var settleCapTask: Task<Void, Never>?

    /// Debounced by every incoming frame while settling: 160ms of quiet = the replay burst is done.
    /// A 1.5s cap guarantees the transcript always reveals (e.g. opening mid-stream, where deltas
    /// never pause).
    private func bumpSettle() {
        guard transcriptSettling else { return }
        settleTask?.cancel()
        settleTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 160_000_000)
            guard let self, !Task.isCancelled else { return }
            self.finishSettling()
        }
    }

    private func beginSettling() {
        transcriptSettling = true
        bumpSettle() // even zero replay events reveals after one quiet window
        settleCapTask?.cancel()
        settleCapTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            guard let self, !Task.isCancelled else { return }
            self.finishSettling()
        }
    }

    private func finishSettling() {
        settleTask?.cancel(); settleTask = nil
        settleCapTask?.cancel(); settleCapTask = nil
        if transcriptSettling { transcriptSettling = false }
    }
    private func armLoadTimeout() {
        loadToken &+= 1
        let tok = loadToken
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_800_000_000)
            guard let self, tok == self.loadToken else { return }
            self.sessionLoading = false
        }
    }

    /// Expands/collapses a sub-agent's inline transcript. On first expand, subscribes to the child
    /// session so its tool calls + outputs stream live into `childMessages[id]` — without leaving the
    /// parent. The subscription + buffer are kept on collapse (cheap; avoids re-replay churn); collapse
    /// just hides the body.
    /// The command/summary of the tool the agent is running RIGHT NOW (the last still-`running` tool
    /// card), so the working bar can say "Running a command · npm test" instead of a contentless
    /// "Running a command". Nil when nothing is mid-tool.
    public var activityDetail: String? {
        if let t = messages.last(where: { $0.role == .tool && $0.tool?.status == "running" })?.tool,
           !t.title.isEmpty { return t.title }
        return nil
    }

    /// Forces a fresh reconnect to the daemon — the manual escape from a half-open/stuck stream. Closing
    /// the socket unwinds the receive loop into `scheduleReconnect`, whose `finishConnect` clears the
    /// transcript and replays it from the daemon (which holds the true, current state) — the same
    /// "restart the app and it catches up" recovery, without the restart.
    public func forceResync() async {
        streamMaybeStalled = false
        status = "Reconnecting…"
        connected = false            // so scheduleReconnect's guard doesn't bail
        client?.close()              // receive loop throws → scheduleReconnect → replay rebuilds
        scheduleReconnect()
    }

    public func toggleChildExpanded(_ id: String) {
        if expandedChildIDs.contains(id) {
            expandedChildIDs.remove(id)
            return
        }
        expandedChildIDs.insert(id)
        if childMessages[id] == nil { childMessages[id] = [] }
        guard !subscribedChildIDs.contains(id) else { return }
        subscribedChildIDs.insert(id)
        if let client, let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            Task { try? await client.send(env) }
        }
    }

    /// Re-creates a stopped session as a FRESH conversation in the same folder/agent/model and opens
    /// it. The provider conversation itself can't be resumed (that's why it stopped — a CLI agent has
    /// no server-side session, or the old one is gone), so history isn't restored; the context is.
    public func restartSession(_ id: String) async {
        guard sessions.contains(where: { $0.id == id }) else { return }
        busy = true
        status = "Restarting…"
        do {
            let env = try await request(MessageType.sessionRestart, payload: SessionRef(sessionID: id))
            let revived = try env.payload(as: Session.self)
            busy = false
            if let idx = sessions.firstIndex(where: { $0.id == id }) { sessions[idx] = revived }
            else { sessions.append(revived) }
            await openSession(revived.id) // its id changed (new live session)
        } catch {
            busy = false
            status = "Restart failed"
            let msg = error.localizedDescription
            // If the daemon no longer has this session (its record/worktree is gone), it can never be
            // restarted — drop the dead card locally and re-sync the list so it stops nagging. The
            // user should start a fresh session instead; say so.
            if msg.lowercased().contains("no such session") || msg.lowercased().contains("agent")  {
                sessions.removeAll { $0.id == id }
                if sessionID == id { newSession() }
                await loadSessions() // converge with the daemon's real state
                actionError = "That session can’t be restarted — its workspace is gone. Start a fresh session instead."
            } else {
                actionError = "Couldn’t restart the session.\n\n\(msg)"
            }
        }
    }

    /// Starts an EPHEMERAL "just chat" session — no project, not persisted (vanishes on restart, no
    /// clutter). One tap → a ready conversational agent. Opens it immediately.
    public func startEphemeralChat() async {
        guard client != nil else { return }
        status = "Starting chat…"
        do {
            let env = try await request(MessageType.sessionCreate,
                                        payload: SessionCreate(provider: newSessionProvider, ephemeral: true))
            let s = try env.payload(as: Session.self)
            // Record it before anything can stream: an ephemeral chat is never persisted daemon-side,
            // so it must never reach the on-device cache either.
            ephemeralSessionIDs.insert(s.id)
            await loadSessions()
            await openSession(s.id)
        } catch { setError("Couldn’t start chat", error.localizedDescription) }
    }

    /// The result of asking for a fan-out. Named rather than an optional, because "nil" was exactly
    /// the shape that let the failure disappear: the caller could not tell "didn't start" from
    /// "didn't start, and here is why", so it showed nothing.
    ///
    /// Deliberately NOT `@discardableResult` any more either — the reason is the whole point now, and
    /// silently dropping it is the bug this replaced.
    public enum FanoutOutcome {
        case started(group: String)
        case failed(reason: String)
    }

    /// Fan-out: spawn `count` agents on the SAME prompt, each in its own worktree, as one group —
    /// race several approaches, then compare and merge the winner.
    ///
    /// - Parameter subtasks: when non-empty, each agent gets its OWN subtask (a division of labour)
    ///   instead of all of them racing the same prompt; `count` is then ignored.
    ///
    /// Returns the group id or the REASON it could not start.
    ///
    /// The reason is returned rather than raised as an alert because the only caller is a sheet, and
    /// a root-level alert cannot draw over a presented sheet — so the failure was invisible. What the
    /// user saw was a "Fan out 3 agents" button that did nothing at all, while the daemon had in fact
    /// answered immediately and precisely:
    ///
    ///   fan-out: no variants started: …/scratchpad/ocproj: not a git repository: fatal: cannot
    ///   change to '…/scratchpad/ocproj': No such file or directory
    ///
    /// i.e. the selected repo's folder no longer existed. Entirely actionable, and entirely swallowed.
    /// A dead control is worse than an error: an error tells you to pick a different repo, whereas
    /// nothing happening reads as the app being broken.
    public func fanout(prompt: String, provider: String, projectID: String?, count: Int, plan: Bool = false, judge: Bool = false, subtasks: [String] = []) async -> FanoutOutcome {
        guard client != nil else { return .failed(reason: "Not connected to the daemon.") }
        let n = subtasks.isEmpty ? count : subtasks.count
        busy = true; status = subtasks.isEmpty ? "Fanning out \(n) agents…" : "Splitting into \(n) subtasks…"
        defer { busy = false }
        do {
            let env = try await request(MessageType.fanoutCreate,
                                        payload: FanoutCreate(provider: provider, projectID: projectID,
                                                              prompt: prompt, plan: plan ? true : nil,
                                                              judge: judge ? true : nil,
                                                              prompts: subtasks.isEmpty ? nil : subtasks,
                                                              count: count))
            let res = try env.payload(as: FanoutResult.self)
            status = "Fan-out: \(res.sessionIDs.count) agents running"
            await loadSessions()
            return .started(group: res.group)
        } catch {
            return .failed(reason: fanoutFailureText(error))
        }
    }

    /// Turns a fan-out failure into something a person can act on.
    ///
    /// The daemon's message is kept — it names the actual obstacle (a missing folder, a directory
    /// that isn't a git repo, an agent that won't start) and replacing it with a generic string
    /// would throw away the only useful part. A leading hint is added for the case that is both the
    /// most common and the least self-evident: every variant needs its own git worktree, so a repo
    /// that isn't a git repository can never fan out however many times you press the button.
    private func fanoutFailureText(_ error: Error) -> String {
        let raw = error.localizedDescription
        if raw.contains("not a git repository") || raw.contains("No such file or directory") {
            return "Fan-out gives each agent its own git worktree, so the repo has to be a git repository that still exists on disk. \(raw)"
        }
        return raw
    }

    /// Ends a fan-out group: keep `winner` (nil = discard all), tear down the rest + their worktrees.
    /// Force removes even worktrees with uncommitted changes. Returns the count actually removed.
    @discardableResult
    /// Asks a fresh agent to combine the finished attempts into one. It joins the SAME comparison as
    /// an extra attempt, so nothing here clears `fanoutSummary` — the existing cards stay on screen
    /// and stay pickable while it works, and the daemon rebroadcasts the summary when it lands.
    public func synthesizeFanout(group: String) async -> Bool {
        guard client != nil else { return false }
        do {
            _ = try await request(MessageType.fanoutSynthesize, payload: FanoutResolve(group: group, keep: nil, force: nil))
            return true
        } catch {
            // Surfaced inline on the comparison rather than as a modal: the user is mid-review of
            // the other attempts, and this is an optional extra that failed, not a blocked workflow.
            setError("Couldn’t combine the attempts", "\(error)")
            return false
        }
    }

    public func resolveFanout(group: String, keep winner: String?, force: Bool = false) async -> Int {
        if fanoutSummary?.group == group { fanoutSummary = nil } // the comparison is answered
        guard client != nil else { return 0 }
        busy = true; status = "Resolving fan-out…"
        defer { busy = false }
        do {
            let env = try await request(MessageType.fanoutResolve,
                                        payload: FanoutResolve(group: group, keep: winner, force: force ? true : nil))
            let res = try env.payload(as: FanoutResolved.self)
            if let failed = res.failed, !failed.isEmpty {
                setError("Some variants weren’t removed",
                         "\(failed.count) worktree(s) have uncommitted changes — resolve again with Force to discard them.")
            } else {
                status = "Fan-out resolved — removed \(res.removed.count)"
            }
            await loadSessions()
            return res.removed.count
        } catch {
            setError("Couldn’t resolve fan-out", error.localizedDescription)
            return 0
        }
    }

    /// Per-type push-notification toggles (loaded from the daemon; edited in Settings → Notifications).
    @Published public var notifyPrefs: [NotifyPref] = []

    public func loadNotifyPrefs() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.notifyPrefsGet, payload: Optional<Int>.none),
           let np = try? env.payload(as: NotifyPrefs.self) {
            notifyPrefs = np.prefs
        }
    }

    /// Flips one notification type on/off; optimistically updates the local list, then persists.
    public func setNotifyPref(_ key: String, enabled: Bool) async {
        guard client != nil else { return }
        if let i = notifyPrefs.firstIndex(where: { $0.key == key }) { notifyPrefs[i].enabled = enabled }
        if let env = try? await request(MessageType.notifyPrefsSet, payload: NotifyPrefSet(key: key, enabled: enabled)),
           let np = try? env.payload(as: NotifyPrefs.self) {
            notifyPrefs = np.prefs
        }
    }

    /// This device's human name, announced to the daemon on connect so prompts can be attributed.
    /// Defaults to the device name; a message tagged with a DIFFERENT name came from someone else.
    public var identity: String = Model.defaultIdentity()

    static func defaultIdentity() -> String {
        #if os(iOS)
        return UIDevice.current.name
        #else
        return Host.current().localizedName ?? "Mac"
        #endif
    }

    /// Announces this device to the daemon. Best-effort: an older daemon just errors and we carry on
    /// unattributed rather than failing the connection.
    func identifySelf() async {
        guard client != nil, !identity.isEmpty else { return }
        _ = try? await request(MessageType.clientIdentify, payload: ClientIdentify(name: identity))
    }

    /// Devices enrolled to reach this daemon (Trust screen).
    @Published public var devices: [DeviceInfo] = []
    /// Whether the open worktree session's PR has landed — drives the "clean up?" affordance.
    @Published public var worktreeStatus: WorktreeStatusResult? = nil

    public func loadDevices() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.deviceList, payload: Optional<Int>.none),
           let list = try? env.payload(as: DeviceList.self) {
            devices = list.devices
        }
    }

    /// Locks out one device. The daemon keeps the entry marked revoked rather than deleting it —
    /// a deleted entry would simply re-enroll on the device's next connection.
    public func revokeDevice(_ pub: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.deviceRevoke, payload: DeviceRef(pub: pub)),
           let list = try? env.payload(as: DeviceList.self) {
            devices = list.devices
        }
    }

    public func labelDevice(_ pub: String, label: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.deviceLabel, payload: DeviceRef(pub: pub, label: label)),
           let list = try? env.payload(as: DeviceList.self) {
            devices = list.devices
        }
    }

    /// Asks whether this worktree's work has landed. Cheap enough to poll when the panel is open.
    public func refreshWorktreeStatus() async {
        guard client != nil, let sid = sessionID else { return }
        if let env = try? await request(MessageType.worktreeStatus, payload: WorktreeRef(sessionID: sid)),
           let st = try? env.payload(as: WorktreeStatusResult.self) {
            worktreeStatus = st
        }
    }

    /// Lands a worktree branch into the repo's default branch. For repos with no remote this is what
    /// "finish" means — without it the agent's work sat on a branch with no way to land it from a phone.
    @discardableResult
    public func mergeWorktree(message: String? = nil) async -> Bool {
        guard client != nil, let sid = sessionID else { return false }
        busy = true
        defer { busy = false }
        do {
            _ = try await request(MessageType.worktreeMerge, payload: WorktreeRef(sessionID: sid, message: message))
            await loadSessions()
            return true
        } catch {
            setError("Couldn't merge", error.localizedDescription)
            return false
        }
    }

    /// Spend + tokens over time (Usage screen).
    @Published public var usage: UsageReport? = nil
    @Published public var loadingUsage = false

    public func loadUsage() async {
        guard client != nil else { return }
        loadingUsage = true
        defer { loadingUsage = false }
        if let env = try? await request(MessageType.usageReport, payload: Optional<Int>.none),
           let r = try? env.payload(as: UsageReport.self) {
            usage = r
        }
    }

    /// Older history exists on the daemon beyond what's loaded. Drives "Show earlier messages".
    @Published public var hasEarlierHistory = false
    /// A page request is in flight.
    @Published public var loadingEarlier = false
    /// Where the incoming page's messages start, so they can be lifted above the existing ones once
    /// the page closes. Page frames arrive through the same channel as live events and append
    /// normally; reordering once at the end is simpler and safer than teaching every event handler
    /// about insertion points.
    private var pageAnchor: Int? = nil

    /// How many DAEMON FRAMES the loaded transcript represents.
    ///
    /// `transcript.page` asks for "the history before what I already have", and the daemon serves it
    /// by indexing its own event ring by absolute offset (`historyPage`, daemon/hub/session.go). The
    /// client used to send `messages.count` — RENDERED ROWS — which has never been the same unit:
    /// hundreds of `output.delta` frames fold into one row, tool events merge in place by id, UI
    /// components merge by id, and status frames render nothing at all. On any real conversation the
    /// two diverge wildly and the page is cut in the wrong place.
    private var daemonEventsRendered = 0

    /// Frames the daemon SYNTHESIZES onto a replay or page rather than storing in its ring. They must
    /// not advance the paging cursor, or every page would be short by the number of trailers.
    static let nonRingFrameTypes: Set<String> = [
        MessageType.turnState, MessageType.transcriptPageBegin, MessageType.transcriptPageEnd,
    ]

    // MARK: on-device transcript cache (see ModelTranscriptCache.swift)

    /// Cached frames already read off disk, keyed by session — the thing that makes an open instant.
    var transcriptHydrated: [String: [Data]] = [:]
    /// The frames currently represented on screen for the open session.
    var transcriptPainted: [Data] = []
    /// True while the daemon's replay is being collected for comparison against what we painted.
    var transcriptReconciling = false
    var transcriptReplayBuffer: [Data] = []
    var transcriptReconcileTask: Task<Void, Never>?
    var transcriptReconcileCap: Task<Void, Never>?
    /// Frames waiting to be written, so streaming doesn't put file I/O on the main actor.
    var transcriptWriteBuffer: [String: [Data]] = [:]
    var transcriptWriteTask: Task<Void, Never>?
    /// Until this moment, a frame byte-identical to one on screen is treated as a provider re-stream.
    var transcriptAnchorGuardUntil: Date = .distantPast
    /// Sessions the user started as "just chat". They are never persisted daemon-side, so writing
    /// them to the on-device cache would make "not saved" untrue on the one device that matters.
    var ephemeralSessionIDs: Set<String> = []

    func resetDaemonEventCount() { daemonEventsRendered = 0 }

    /// Seals streaming rows after a cache paint — a frame captured mid-stream would otherwise leave a
    /// caret blinking on text that finished long ago.
    func finalizeStreamingForCache() {
        if let last = messages.last, last.role == .assistant, last.streaming {
            messages[messages.count - 1].streaming = false
        }
    }

    /// Fetches the history immediately before what's loaded. The daemon streams it bracketed by
    /// page.begin / page.end.
    public func loadEarlierHistory() async {
        guard let client, let sid = sessionID, !loadingEarlier, hasEarlierHistory else { return }
        loadingEarlier = true
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.transcriptPage,
                                          payload: TranscriptPage(sessionID: sid, loaded: daemonEventsRendered)) {
            try? await client.send(env)
        } else {
            loadingEarlier = false
        }
    }

    /// Outstanding share invites (owner-only).
    @Published public var invites: [Invite] = []
    /// The most recently minted invite link. Shown once, then cleared — the daemon never returns a
    /// secret again, so this is the only moment it can be copied.
    @Published public var freshInviteURL: String? = nil

    public func loadInvites() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.inviteList, payload: Optional<Int>.none),
           let l = try? env.payload(as: InviteList.self) { invites = l.invites }
    }

    public func createInvite(label: String, role: String, ttlHours: Int) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.inviteCreate,
                                        payload: InviteCreate(label: label, role: role, ttlHours: ttlHours)),
           let created = try? env.payload(as: InviteCreated.self) {
            freshInviteURL = created.url
            await loadInvites()
        }
    }

    public func revokeInvite(id: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.inviteRevoke, payload: InviteRef(id: id)),
           let l = try? env.payload(as: InviteList.self) { invites = l.invites }
    }

    /// Who else is connected, and what they may do. Empty until the daemon reports it.
    @Published public var participants: [Participant] = []
    /// Whether multi-user enforcement is on. Off (the default) means everyone is the owner.
    @Published public var sharingEnabled = false

    /// This client's OWN role, or nil when we genuinely cannot tell.
    ///
    /// The daemon's participant list is a roster of everyone connected — it carries no "this one is
    /// you" marker (see ParticipantList / hub.participants), so the only way to self-identify is to
    /// match the name we announced with client.identify. That match is normalized the same way the
    /// daemon normalizes it (trim, then cap at 60 chars, hub.go's identify handler) or a long device
    /// name would never find itself.
    ///
    /// It returns nil rather than a guess in the two cases where the match is unsound: nobody
    /// matches (the roster hasn't arrived yet), or SEVERAL do (two Macs both called "Mac"). A
    /// guessed role would either lock the owner out of their own machine or promise an observer a
    /// control the daemon will refuse — both worse than admitting we don't know.
    public var selfRole: String? {
        // Enforcement off is not ambiguity: roleRegistry.role() returns owner for every connection
        // until someone shares a session, so a solo user is definitively the owner.
        guard sharingEnabled else { return ParticipantRole.owner }
        let me = String(identity.trimmingCharacters(in: .whitespacesAndNewlines).prefix(60))
        guard !me.isEmpty else { return nil }
        let mine = participants.filter { $0.name == me }
        guard mine.count == 1 else { return nil }
        return mine[0].role
    }

    /// True only when we KNOW this client is not the owner. Deliberately false while `selfRole` is
    /// nil: an unknown role must leave controls alive and let the daemon refuse (which surfaces as a
    /// real message), never hide a control on a hunch.
    public var knownNonOwner: Bool {
        guard let role = selfRole else { return false }
        return role != ParticipantRole.owner
    }

    /// Why the owner-only controls are unavailable, for the affordance that has to explain itself.
    public var ownerOnlyReason: String {
        "Running commands on the host is owner-only. You're \(ParticipantRole.label(selfRole ?? ParticipantRole.observer).lowercased()) on this session — ask the owner to grant it."
    }

    public func loadParticipants() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.participants, payload: Optional<Int>.none),
           let pl = try? env.payload(as: ParticipantList.self) {
            participants = pl.participants
            sharingEnabled = pl.enabled
        }
    }

    /// Turns sharing on or off. Whoever enables it becomes the owner.
    public func setSharingEnabled(_ on: Bool) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.rolesEnable, payload: RolesEnable(enabled: on)),
           let pl = try? env.payload(as: ParticipantList.self) {
            participants = pl.participants
            sharingEnabled = pl.enabled
        }
    }

    /// Grants or revokes another participant's ability to steer.
    public func grantRole(name: String, role: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.roleGrant, payload: RoleGrant(name: name, role: role)),
           let pl = try? env.payload(as: ParticipantList.self) {
            participants = pl.participants
            sharingEnabled = pl.enabled
        }
    }

    /// MCP servers registered with the daemon (Settings → MCP servers). Kept live by mcp.changed.
    @Published public var mcpServers: [MCPServerInfo] = []

    /// Servers your agents are already configured with, offered for import.
    @Published public var mcpFound: [MCPFound] = []
    /// Whether the daemon owns MCP exclusively (harnesses ignore their own config).
    @Published public var mcpExclusive = false

    /// Scans each harness's own config for servers the daemon doesn't have.
    public func discoverMCPServers() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.mcpDiscover, payload: Optional<Int>.none),
           let d = try? env.payload(as: MCPDiscovered.self) {
            mcpFound = d.found
            mcpExclusive = d.exclusive
        }
    }

    /// Adopts the named discovered servers into the daemon registry.
    public func importMCPServers(names: [String]) async {
        guard client != nil, !names.isEmpty else { return }
        if let env = try? await request(MessageType.mcpImport, payload: MCPImport(names: names)),
           let list = try? env.payload(as: MCPList.self) {
            mcpServers = list.servers
            mcpFound.removeAll { names.contains($0.name) }
        }
    }

    /// Turns exclusive mode on/off — whether harnesses ignore their own MCP config.
    public func setMCPExclusive(_ on: Bool) async {
        guard client != nil else { return }
        let previous = mcpExclusive
        mcpExclusive = on
        if (try? await request(MessageType.mcpExclusive, payload: MCPExclusiveSet(enabled: on))) == nil {
            mcpExclusive = previous
        }
    }

    /// Results from the public MCP registry.
    @Published public var mcpDirectory: [MCPDirectoryEntry] = []
    @Published public var mcpBrowsing = false
    @Published public var mcpBrowseError: String? = nil

    /// Searches the public registry so adding a server doesn't mean knowing its argv by heart.
    public func browseMCPDirectory(query: String) async {
        guard client != nil else { return }
        mcpBrowsing = true
        mcpBrowseError = nil
        defer { mcpBrowsing = false }
        do {
            let env = try await request(MessageType.mcpBrowse, payload: MCPBrowse(query: query))
            mcpDirectory = (try? env.payload(as: MCPDirectory.self))?.entries ?? []
            if mcpDirectory.isEmpty { mcpBrowseError = "Nothing matched that search." }
        } catch {
            mcpDirectory = []
            mcpBrowseError = error.localizedDescription
        }
    }

    public func loadMCPServers() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.mcpList, payload: Optional<Int>.none),
           let list = try? env.payload(as: MCPList.self) {
            mcpServers = list.servers
        }
    }

    public func upsertMCPServer(_ server: MCPUpsert) async -> String? {
        guard client != nil else { return "Not connected." }
        do {
            let env = try await request(MessageType.mcpUpsert, payload: server)
            if let list = try? env.payload(as: MCPList.self) { mcpServers = list.servers }
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    public func deleteMCPServer(name: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.mcpDelete, payload: MCPRef(name: name)),
           let list = try? env.payload(as: MCPList.self) { mcpServers = list.servers }
    }

    /// Enables/disables a server, and REVERTS if the daemon didn't take it.
    ///
    /// The optimistic write below lands before the request, so a failure used to leave the switch
    /// showing a state the daemon never agreed to — the toggle read "on" while the server stayed off.
    /// A control that silently disagrees with the thing it controls is worse than one that fails.
    public func setMCPServerEnabled(name: String, enabled: Bool) async {
        guard client != nil else { return }
        let previous = mcpServers.first(where: { $0.name == name })?.enabled
        if let i = mcpServers.firstIndex(where: { $0.name == name }) { mcpServers[i].enabled = enabled }
        do {
            let env = try await request(MessageType.mcpEnable, payload: MCPEnable(name: name, enabled: enabled))
            if let list = try? env.payload(as: MCPList.self) { mcpServers = list.servers }
        } catch {
            if let previous, let i = mcpServers.firstIndex(where: { $0.name == name }) {
                mcpServers[i].enabled = previous
            }
            setError("Couldn’t \(enabled ? "enable" : "disable") \(name)", error.localizedDescription)
        }
    }

    /// Connects to the server and lists its tools — the honest "does this actually work" check.
    public func checkMCPServer(name: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.mcpCheck, payload: MCPRef(name: name)),
           let list = try? env.payload(as: MCPList.self) { mcpServers = list.servers }
    }

    /// The finished fan-out comparison, if one arrived. Set by the daemon's fanout.summary broadcast;
    /// presenting it is the host view's job. Cleared when the group is resolved.
    @Published public var fanoutSummary: FanoutSummary? = nil

    /// The active session's mode (code | ask | architect). Mirrors the daemon, which is authoritative
    /// and enforces it regardless of what the client shows.
    @Published public var sessionMode: String = SessionMode.code

    /// Switches the live session's mode. Optimistic: the daemon confirms via session.list.
    public func setSessionMode(_ mode: String) async {
        guard client != nil, let sid = sessionID else { return }
        let previous = sessionMode
        sessionMode = mode
        appendTool("◆ Mode → \(SessionMode.label(mode))")
        if (try? await request(MessageType.sessionModeSet, payload: SessionModeSet(sessionID: sid, mode: mode))) == nil {
            sessionMode = previous
            actionError = "Couldn't switch mode. The agent is still in \(SessionMode.label(previous))."
        }
    }

    /// Persisted "always allow / never allow" rules (Settings → Approval rules). Kept live by the
    /// daemon's approval.rules.changed broadcast, so answering "Always…" on the phone updates this
    /// screen on the Mac.
    @Published public var approvalRules: [ApprovalRuleInfo] = []

    public func loadApprovalRules() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.approvalRulesList, payload: Optional<Int>.none),
           let list = try? env.payload(as: ApprovalRulesList.self) {
            approvalRules = list.rules
        }
    }

    /// Revokes one rule; the agent asks again next time it wants that tool.
    public func deleteApprovalRule(index: Int) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.approvalRuleDelete, payload: ApprovalRuleDelete(index: index)),
           let list = try? env.payload(as: ApprovalRulesList.self) {
            approvalRules = list.rules
        }
    }

    /// Checkpoints (restore points) for the active session's worktree — newest first.
    @Published public var checkpoints: [Checkpoint] = []
    /// Multi-account credentials + per-provider usage meter (Accounts view).
    @Published public var accounts: [Account] = []
    @Published public var providerUsage: [ProviderUsage] = []

    public func loadAccounts() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.accountList, payload: Optional<Int>.none),
           let al = try? env.payload(as: AccountList.self) { accounts = al.accounts; providerUsage = al.usage }
    }
    private func applyAccountList(_ env: Envelope) {
        if let al = try? env.payload(as: AccountList.self) { accounts = al.accounts; providerUsage = al.usage }
    }
    public func upsertAccount(_ a: Account) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.accountUpsert, payload: a) { applyAccountList(env) }
    }
    public func deleteAccount(_ id: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.accountDelete, payload: AccountRef(accountID: id)) { applyAccountList(env) }
    }
    public func activateAccount(provider: String, id: String) async {
        guard client != nil else { return }
        do {
            let env = try await request(MessageType.accountActivate, payload: AccountActivate(provider: provider, accountID: id))
            applyAccountList(env)
        } catch { setError("Couldn’t switch account", error.localizedDescription) }
    }
    /// Probes an account's remaining rate-limit/quota from the provider API.
    public func accountQuota(_ id: String) async -> AccountQuota? {
        guard client != nil else { return nil }
        return try? await request(MessageType.accountQuota, payload: AccountRef(accountID: id)).payload(as: AccountQuota.self)
    }

    /// SSH remote hosts (run/inspect a worktree on a remote box).
    @Published public var remotes: [RemoteHost] = []
    public func loadRemotes() async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.remoteList, payload: Optional<Int>.none),
           let rl = try? env.payload(as: RemoteList.self) { remotes = rl.hosts }
    }
    public func upsertRemote(_ h: RemoteHost) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.remoteUpsert, payload: h),
           let rl = try? env.payload(as: RemoteList.self) { remotes = rl.hosts }
    }
    public func deleteRemote(_ id: String) async {
        guard client != nil else { return }
        if let env = try? await request(MessageType.remoteDelete, payload: RemoteRef(id: id)),
           let rl = try? env.payload(as: RemoteList.self) { remotes = rl.hosts }
    }
    public func remoteStatus(_ id: String) async -> RemoteStatus? {
        guard client != nil else { return nil }
        return try? await request(MessageType.remoteStatus, payload: RemoteRef(id: id)).payload(as: RemoteStatus.self)
    }
    /// Starts an agent session ON a remote host over SSH and opens it.
    public func remoteRun(hostID: String, agentCommand: String, prompt: String) async {
        guard client != nil else { return }
        do {
            let env = try await request(MessageType.remoteRun, payload: RemoteRun(hostID: hostID, agentCommand: agentCommand, prompt: prompt))
            let s = try env.payload(as: Session.self)
            await loadSessions()
            await openSession(s.id)
        } catch { setError("Couldn’t start remote agent", error.localizedDescription) }
    }

    /// Saves a checkpoint of the current session's worktree (a rollback point on the timeline).
    public func saveCheckpoint(label: String = "") async {
        guard let sid = sessionID, client != nil else { return }
        do {
            let env = try await request(MessageType.checkpointCreate, payload: CheckpointCreate(sessionID: sid, label: label.isEmpty ? nil : label))
            checkpoints = (try? env.payload(as: CheckpointList.self))?.checkpoints ?? checkpoints
            status = "Checkpoint saved"
        } catch { setError("Couldn’t save checkpoint", error.localizedDescription) }
    }

    /// Loads the active session's checkpoints (call when opening the rollback menu).
    public func loadCheckpoints() async {
        guard let sid = sessionID, client != nil else { checkpoints = []; return }
        if let env = try? await request(MessageType.checkpointList, payload: SessionRef(sessionID: sid)) {
            checkpoints = (try? env.payload(as: CheckpointList.self))?.checkpoints ?? []
        }
    }

    /// Rolls the active session's worktree back to a checkpoint (tracked files restored to that point).
    public func restoreCheckpoint(_ sha: String) async {
        guard let sid = sessionID, client != nil else { return }
        do {
            _ = try await request(MessageType.checkpointRestore, payload: CheckpointRestore(sessionID: sid, sha: sha))
            status = "Rolled back to checkpoint"
        } catch { setError("Couldn’t roll back", error.localizedDescription) }
    }

    /// Recovers a "broken" session — one that opens but whose sends silently fail because the daemon
    /// bound it to the wrong directory partition. Re-attaches on the daemon, which re-resolves the
    /// session's real directory from the provider and heals the stored cwd, KEEPING all history (the
    /// id is unchanged). Use this instead of Restart when you don't want to lose the conversation.
    public func recoverSession(_ id: String) async {
        busy = true
        status = "Recovering…"
        do {
            let env = try await request(MessageType.sessionRecover, payload: SessionRef(sessionID: id))
            let healed = try env.payload(as: Session.self)
            busy = false
            status = "Recovered"
            if let idx = sessions.firstIndex(where: { $0.id == id }) { sessions[idx] = healed }
            else { sessions.append(healed) }
            await openSession(healed.id) // re-subscribe to the freshly re-attached session
        } catch {
            busy = false
            status = "Recover failed"
            let msg = error.localizedDescription
            if msg.lowercased().contains("use restart") {
                actionError = "This agent can’t re-attach an existing conversation. Use Restart to start fresh in the same folder."
            } else {
                actionError = "Couldn’t recover the session.\n\n\(msg)"
            }
        }
    }

    private var lastSessionKey: String { "oculus.lastSession.\(id)" }

    /// Loads the agent's slash commands (built-in + custom from .claude/commands) for the composer's
    /// "/" palette. Scoped to the session's provider + working directory.
    public func loadCommands(sessionID sid: String) async {
        guard client != nil else { commands = []; return }
        if let resp = try? await request(MessageType.commandList, payload: CommandListReq(sessionID: sid)),
           let cl = try? resp.payload(as: CommandList.self) {
            commands = cl.commands
        } else {
            commands = []
        }
    }

    /// Registers a project folder and returns it (the daemon replies with the created
    /// Project). The returned project is merged into `projects` so callers can select it
    /// immediately — fixing the old fire-and-forget flow where an added folder never appeared.
    @discardableResult
    public func addProject(path: String) async -> Project? {
        guard path.trimmingCharacters(in: .whitespaces).isEmpty == false else { return nil }
        do {
            let env = try await request(MessageType.projectAdd, payload: ProjectAdd(path: path))
            let p = try env.payload(as: Project.self)
            if let i = projects.firstIndex(where: { $0.id == p.id }) { projects[i] = p } else { projects.append(p) }
            return p
        } catch {
            status = "Add project failed: \(error.localizedDescription)"
            return nil
        }
    }

    /// Lists the subdirectories of `path` (nil = the user's home) for the new-session folder picker,
    /// so you can browse INTO a folder and select several sub-folders. Returns nil on failure.
    public func browseFolders(path: String?) async -> ProjectBrowse? {
        guard client != nil else { return nil }
        if let resp = try? await request(MessageType.projectBrowse, payload: ProjectBrowseReq(path: path)) {
            return try? resp.payload(as: ProjectBrowse.self)
        }
        return nil
    }

    // MARK: loops (recurring autonomous workflows)

    public func loadLoops() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.loopList, payload: Optional<Int>.none),
           let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
    }

    /// Loads the cross-session activity feed (newest first) — the Activity destination + Needs-You inbox.
    public func loadActivity() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.activityList, payload: Optional<Int>.none),
           let al = try? resp.payload(as: ActivityList.self) {
            activityFeed = al.events.sorted { $0.ts > $1.ts }
        }
    }

    /// Marks activity items read (empty = all), clearing the Needs-You badge. Optimistic + persisted daemon-side.
    public func markActivityRead(_ ids: [String] = []) async {
        let target = Set(ids)
        for i in activityFeed.indices where ids.isEmpty || target.contains(activityFeed[i].id) { activityFeed[i].read = true }
        guard client != nil else { return }
        _ = try? await client?.send(Protocol.encode(id: UUID().uuidString, type: MessageType.activityMarkRead,
                                                     payload: ActivityMarkRead(ids: ids.isEmpty ? nil : ids)))
    }
    public func upsertLoop(_ l: Loop) async {
        guard client != nil else { return }
        _ = try? await request(MessageType.loopUpsert, payload: l)
        await loadLoops()
    }
    public func deleteLoop(_ id: String) async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.loopDelete, payload: LoopRef(id: id)),
           let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
    }
    /// Arms or disarms a loop, and says so when it fails.
    ///
    /// This only updated on success, so a failure left the switch snapping back with no explanation —
    /// and a loop is an AUTONOMOUS agent run, so "did that arm or not" is not a question to leave the
    /// user guessing at.
    public func setLoopEnabled(_ id: String, _ on: Bool) async {
        guard client != nil else { return }
        do {
            let resp = try await request(MessageType.loopSetEnabled, payload: LoopSetEnabled(id: id, enabled: on))
            if let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
        } catch {
            let name = loops.first(where: { $0.id == id })?.name ?? "loop"
            setError("Couldn’t \(on ? "turn on" : "turn off") \(name)", error.localizedDescription)
        }
    }

    public func removeProject(id: String) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.projectRemove, payload: ProjectRef(projectID: id)) {
            try? await client.send(env)
        }
    }

    /// Requests the diff of the current worktree session (result lands in lastDiff).
    public func worktreeDiff() async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeDiff, payload: WorktreeDiff(sessionID: sid)) {
            try? await client.send(env)
        }
    }

    /// Fetches the per-member diff for a cross-repo workspace session. Populates workspaceDiffs
    /// and also concatenates them into lastDiff so the existing diff review UI renders every repo.
    public func workspaceDiff() async {
        guard client != nil, let sid = sessionID else { return }
        if let resp = try? await request(MessageType.workspaceDiff, payload: WorkspaceDiff(sessionID: sid)),
           let wd = try? resp.payload(as: WorkspaceDiff.self) {
            let members = wd.members ?? []
            workspaceDiffs = members
            lastDiff = members.map(\.diff).filter { !$0.isEmpty }.joined(separator: "\n")
        }
    }

    /// Commits, pushes, and opens a PR for every member repo of a workspace session — the
    /// coordinated multi-PR finish. Populates workspacePRResults (per-repo outcome).
    public func workspacePR(title: String, body: String? = nil) async {
        guard client != nil, let sid = sessionID else { return }
        workspacePRRunning = true
        defer { workspacePRRunning = false }
        do {
            let resp = try await request(MessageType.workspacePR, payload: WorkspacePR(sessionID: sid, title: title, body: body))
            if let pr = try? resp.payload(as: WorkspacePR.self) { workspacePRResults = pr.members ?? [] }
        } catch {
            actionError = "Couldn’t open the workspace PRs.\n\n\(error.localizedDescription)"
        }
    }

    public func removeWorktree(force: Bool = false) async {
        guard let client, let sid = sessionID else { return }
        forgetCached(sid)
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeRemove, payload: WorktreeRemove(sessionID: sid, force: force)) {
            try? await client.send(env)
        }
    }

    /// Removes a SPECIFIC session's worktree (git worktree remove + prune) and ends the session — the
    /// all-sessions manager uses this to clean up an old worktree session that isn't the active one.
    /// Mirrors stopSession's optimistic + auto-reopen cleanup so the row doesn't linger or reappear.
    public func removeWorktree(_ id: String, force: Bool = true) async {
        guard let client else { return }
        sessions.removeAll { $0.id == id }
        if sessionID == id { newSession() }
        forgetCached(id) // the worktree is gone; its cached transcript should go with it
        if defaults.string(forKey: lastSessionKey) == id { defaults.removeObject(forKey: lastSessionKey) }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeRemove, payload: WorktreeRemove(sessionID: id, force: force)) {
            try? await client.send(env)
        }
    }

    public func createPR(title: String, body: String? = nil) async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreePR, payload: WorktreePR(sessionID: sid, title: title, body: body)) {
            try? await client.send(env)
        }
    }

    /// Whether a catch-up is in flight, and its last outcome (message + any conflicted files) — for
    /// the WorktreePanel to show.
    @Published public var catchingUp = false
    @Published public var catchUpMessage: String?
    @Published public var catchUpConflicts: [String] = []

    /// Merges the repo's default branch INTO this session's branch ("catch up to main"). Correlated
    /// so the outcome (updated / already-current / conflicts to resolve) surfaces in the panel.
    public func catchUpToMain() async {
        guard client != nil, let sid = sessionID else { return }
        catchingUp = true
        catchUpMessage = nil
        catchUpConflicts = []
        defer { catchingUp = false }
        do {
            let resp = try await request(MessageType.worktreeCatchUp, payload: WorktreeCatchUp(sessionID: sid))
            let r = try resp.payload(as: WorktreeCatchUp.self)
            catchUpMessage = r.message ?? "Done."
            catchUpConflicts = r.conflicts ?? []
            if r.status == "updated" { await worktreeDiff() } // refresh the diff after a clean merge
        } catch {
            catchUpMessage = "Couldn’t catch up: \(error.localizedDescription)"
        }
    }

    /// Requests files this worktree shares with other active worktrees (result -> conflicts).
    public func loadConflicts() async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeConflicts, payload: WorktreeConflicts(sessionID: sid)) {
            try? await client.send(env)
        }
    }

    /// Opens a session discovered on the host: opencode continues it live; claude-code
    /// resumes it (the daemon's registered provider Attaches via `--resume`, loading history).
    /// The daemon replies "provider cannot attach" for providers without resume support.
    public func attach(_ d: Discovered) async {
        guard let client, let sid = d.sessionID else { return }
        messages.removeAll()
        sessionID = nil
        busy = false
        pendingApproval = nil
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAttach,
                                          payload: SessionAttach(provider: d.provider, sessionID: sid, url: d.url, cwd: d.cwd))
            try await client.send(env)
        } catch {
            status = "Attach failed: \(error)"
        }
    }

    // MARK: - Built-in editor file access (request/reply)

    /// Sends a request and awaits its correlated OK/error reply by envelope id. Unlike prompts
    /// (fire-and-forget events), fs.* calls need a response, so receiveLoop resolves the
    /// matching continuation.
    /// Feature messages this daemon didn't recognize. A daemon older than the app answers
    /// "unknown type: …", and because most feature calls use `try?`, that used to be INDISTINGUISHABLE
    /// from "there's nothing to show" — an out-of-date daemon looked exactly like an empty screen.
    /// Recording it lets the UI say so instead of silently doing nothing.
    @Published public var unsupportedByDaemon: Set<String> = []

    /// True once any feature call has been refused as unknown — the daemon predates this app.
    public var daemonOutdated: Bool { !unsupportedByDaemon.isEmpty }

    /// Notes an "unknown type" refusal. Returns true when that's what the error was.
    @discardableResult
    private func noteIfUnsupported(_ type: String, _ error: Error) -> Bool {
        guard error.localizedDescription.localizedCaseInsensitiveContains("unknown type") else { return false }
        if !unsupportedByDaemon.contains(type) {
            unsupportedByDaemon.insert(type)
            status = "Daemon is out of date"
        }
        return true
    }

    private func request(_ type: String, payload: some Encodable) async throws -> Envelope {
        guard let client else {
            throw NSError(domain: "Oculus", code: -1, userInfo: [NSLocalizedDescriptionKey: "not connected"])
        }
        let id = UUID().uuidString
        let env = try Protocol.encode(id: id, type: type, payload: payload)
        do {
            return try await withCheckedThrowingContinuation { cont in
                pendingRequests[id] = cont
                Task {
                    do { try await client.send(env) }
                    catch { if let c = pendingRequests.removeValue(forKey: id) { c.resume(throwing: error) } }
                }
            }
        } catch {
            noteIfUnsupported(type, error)
            throw error
        }
    }

    /// Opens the Developer log panel and starts streaming the daemon's log (replays recent lines,
    /// then tails new ones). Idempotent — safe to call each time the panel is shown.
    public func openLogPanel() {
        showLogPanel = true
        guard !logSubscribed else { return }
        logSubscribed = true
        Task {
            do {
                let hist = try await request(MessageType.logSubscribe, payload: [String: String]()).payload(as: LogHistory.self)
                daemonLog = hist.lines
            } catch {
                logSubscribed = false
                actionError = "Couldn't stream daemon logs: \(error.localizedDescription)"
            }
        }
    }

    /// Hides the panel and tells the daemon to stop streaming (frees the subscription).
    public func closeLogPanel() {
        showLogPanel = false
        guard logSubscribed else { return }
        logSubscribed = false
        Task { try? await client?.send(Protocol.encode(id: UUID().uuidString, type: MessageType.logUnsubscribe, payload: [String: String]())) }
    }

    public func clearDaemonLog() { daemonLog = [] }

    /// Lists a directory (nil/empty path → the available roots). With sessionID, the roots are
    /// scoped to that session's workspace folder(s) — a per-session file tree.
    public func fsTree(_ path: String?, sessionID: String? = nil) async throws -> FSTree {
        try await request(MessageType.fsTree, payload: FSTreeReq(path: path, sessionID: sessionID)).payload(as: FSTree.self)
    }

    /// Reads a text file (content + sha for conflict detection).
    public func fsRead(_ path: String) async throws -> FSFile {
        try await request(MessageType.fsRead, payload: FSReadReq(path: path)).payload(as: FSFile.self)
    }

    /// Saves a file if `baseSha` still matches on disk; result.conflict signals a stale write.
    public func fsWrite(_ path: String, content: String, baseSha: String) async throws -> FSWriteResult {
        try await request(MessageType.fsWrite, payload: FSWriteReq(path: path, content: content, baseSha: baseSha))
            .payload(as: FSWriteResult.self)
    }

    /// Unified diff for a session's changes or an in-root path (review).
    public func fsDiff(sessionID: String? = nil, path: String? = nil) async throws -> String {
        try await request(MessageType.fsDiff, payload: FSDiffReq(sessionID: sessionID, path: path))
            .payload(as: FSDiff.self).diff
    }

    /// Reads a file's raw bytes (an image to show inline in the editor).
    public func fsReadBytes(_ path: String) async throws -> FSBytes {
        try await request(MessageType.fsReadBytes, payload: FSReadBytesReq(path: path)).payload(as: FSBytes.self)
    }

    // MARK: - LSP (editor diagnostics/types/definition)

    /// Opens a document in its language server (diagnostics then arrive via lsp.diagnostics).
    public func lspOpen(_ path: String, content: String) async {
        _ = try? await request(MessageType.lspOpen, payload: LSPDocReq(path: path, content: content))
    }

    /// Notifies the language server the document changed (full-sync).
    public func lspChange(_ path: String, content: String) async {
        _ = try? await request(MessageType.lspChange, payload: LSPDocReq(path: path, content: content))
    }

    /// Closes the document in its language server + clears its diagnostics.
    public func lspClose(_ path: String) async {
        diagnostics[path] = nil
        _ = try? await request(MessageType.lspClose, payload: LSPDocReq(path: path))
    }

    /// Hover (type info / docs) at a 0-based position; empty string if none.
    public func lspHover(_ path: String, line: Int, character: Int) async -> String {
        (try? await request(MessageType.lspHover, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPHover.self).contents) ?? ""
    }

    /// Go-to-definition at a 0-based position; nil if none.
    public func lspDefinition(_ path: String, line: Int, character: Int) async -> LSPDefinition? {
        guard let d = try? await request(MessageType.lspDefinition, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPDefinition.self), d.found else { return nil }
        return d
    }

    /// Autocomplete suggestions at a 0-based position.
    public func lspComplete(_ path: String, line: Int, character: Int) async -> [LSPCompletionItem] {
        (try? await request(MessageType.lspComplete, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPCompletion.self).items) ?? []
    }

    /// Formats the whole document via the language server; returns the formatted text (or nil
    /// if unchanged / no formatter).
    public func lspFormat(_ path: String, content: String) async -> String? {
        guard let r = try? await request(MessageType.lspFormat, payload: LSPFormatReq(path: path, content: content))
            .payload(as: LSPFormatResult.self), r.changed else { return nil }
        return r.text
    }

    /// All references to the symbol at a 0-based position.
    public func lspReferences(_ path: String, line: Int, character: Int) async -> [LSPLocation] {
        (try? await request(MessageType.lspReferences, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPLocations.self).locations) ?? []
    }

    /// Document symbols (outline) for a file.
    public func lspSymbols(_ path: String) async -> [LSPSymbol] {
        (try? await request(MessageType.lspSymbols, payload: LSPDocReq(path: path))
            .payload(as: LSPSymbols.self).symbols) ?? []
    }

    /// Renames the symbol at a position across files; returns the paths that were rewritten.
    public func lspRename(_ path: String, line: Int, character: Int, newName: String) async -> [String] {
        (try? await request(MessageType.lspRename, payload: LSPRenameReq(path: path, line: line, character: character, newName: newName))
            .payload(as: LSPRenameResult.self).files) ?? []
    }

    /// Multi-file text search across a session's workspace (or all roots).
    public func fsSearch(_ query: String, sessionID: String? = nil, regex: Bool = false) async -> [FSSearchHit] {
        (try? await request(MessageType.fsSearch, payload: FSSearchReq(query: query, sessionID: sessionID, regex: regex))
            .payload(as: FSSearchResult.self).results) ?? []
    }

    /// Whether a language server is installed for a file (and whether we can install one).
    public func lspServerInfo(_ path: String) async -> LSPServerInfo? {
        try? await request(MessageType.lspServerInfo, payload: LSPDocReq(path: path)).payload(as: LSPServerInfo.self)
    }

    /// Installs the language server for a file (runs a package manager; may take minutes).
    public func lspInstall(_ path: String) async -> LSPInstallResult? {
        try? await request(MessageType.lspInstall, payload: LSPDocReq(path: path)).payload(as: LSPInstallResult.self)
    }

    // MARK: issue detail / edit / comments / images

    /// Fetches a ticket's full body + comments.
    public func issueDetail(_ issue: Issue) async throws -> IssueDetail {
        try await request(MessageType.issueDetail, payload: IssueDetailReq(provider: issue.provider, issueID: issue.id))
            .payload(as: IssueDetail.self)
    }

    /// Fetches a team's workflow states (for the status picker). Empty if no team id.
    public func issueStates(_ issue: Issue) async throws -> [IssueState] {
        guard let team = issue.teamID, !team.isEmpty else { return [] }
        return try await request(MessageType.issueStates, payload: IssueStatesReq(provider: issue.provider, teamID: team))
            .payload(as: IssueStateList.self).states
    }

    /// Applies a partial edit (only the non-nil fields) and returns the refreshed issue.
    /// Updates the board cache in place so the change is reflected without a full reload.
    @discardableResult
    public func updateIssue(_ issue: Issue, title: String? = nil, description: String? = nil,
                            stateID: String? = nil, priority: Int? = nil,
                            assigneeID: String? = nil, labelIDs: [String]? = nil,
                            cycleID: String? = nil, estimate: Double? = nil, dueDate: String? = nil) async throws -> Issue {
        let updated = try await request(MessageType.issueUpdate,
            payload: IssueUpdate(provider: issue.provider, issueID: issue.id,
                                 title: title, description: description, stateID: stateID, priority: priority,
                                 assigneeID: assigneeID, labelIDs: labelIDs, cycleID: cycleID,
                                 estimate: estimate, dueDate: dueDate))
            .payload(as: Issue.self)
        if let i = issues.firstIndex(where: { $0.id == updated.id }) { issues[i] = updated }
        return updated
    }

    // Ticket-editor pickers, cached per (provider, team) so reopening a ticket doesn't refetch. The
    // caches are simple dictionaries keyed by "provider|team"; the inspector calls these on open.
    @Published public var issueMembers: [String: [IssueUser]] = [:]
    @Published public var issueLabelsCache: [String: [IssueLabel]] = [:]
    @Published public var issueCyclesCache: [String: [IssueCycle]] = [:]
    private func pickerKey(_ provider: String, _ team: String) -> String { "\(provider)|\(team)" }

    /// Loads the assignable users for an issue's team (assignee picker), caching by team.
    public func loadMembers(for issue: Issue) async {
        guard let team = issue.teamID, !team.isEmpty else { return }
        let key = pickerKey(issue.provider, team)
        if issueMembers[key] != nil { return }
        if let list = try? await request(MessageType.issueMembers,
            payload: IssueMembersReq(provider: issue.provider, teamID: team, issueID: issue.id))
            .payload(as: IssueMemberList.self) {
            issueMembers[key] = list.members
        }
    }
    /// Loads a team's labels (label picker), caching by team.
    public func loadLabels(for issue: Issue) async {
        guard let team = issue.teamID, !team.isEmpty else { return }
        let key = pickerKey(issue.provider, team)
        if issueLabelsCache[key] != nil { return }
        if let list = try? await request(MessageType.issueLabels,
            payload: IssueLabelsReq(provider: issue.provider, teamID: team))
            .payload(as: IssueLabelList.self) {
            issueLabelsCache[key] = list.labels
        }
    }
    /// Loads a team's sprints/cycles (sprint picker), caching by team.
    public func loadCycles(for issue: Issue) async {
        guard let team = issue.teamID, !team.isEmpty else { return }
        let key = pickerKey(issue.provider, team)
        if issueCyclesCache[key] != nil { return }
        if let list = try? await request(MessageType.issueCycles,
            payload: IssueCyclesReq(provider: issue.provider, teamID: team))
            .payload(as: IssueCycleList.self) {
            issueCyclesCache[key] = list.cycles
        }
    }
    public func members(for issue: Issue) -> [IssueUser] { issue.teamID.map { issueMembers[pickerKey(issue.provider, $0)] ?? [] } ?? [] }
    public func labels(for issue: Issue) -> [IssueLabel] { issue.teamID.map { issueLabelsCache[pickerKey(issue.provider, $0)] ?? [] } ?? [] }
    public func cycles(for issue: Issue) -> [IssueCycle] { issue.teamID.map { issueCyclesCache[pickerKey(issue.provider, $0)] ?? [] } ?? [] }

    /// Posts a new comment and returns it.
    public func addComment(_ issue: Issue, body: String) async throws -> IssueComment {
        try await request(MessageType.issueComment, payload: IssueCommentAdd(provider: issue.provider, issueID: issue.id, body: body))
            .payload(as: IssueComment.self)
    }

    /// Edits an existing comment.
    public func editComment(provider: String, commentID: String, body: String) async throws {
        _ = try await request(MessageType.issueCommentEdit, payload: IssueCommentEdit(provider: provider, commentID: commentID, body: body))
    }

    /// Fetches an auth-gated tracker image through the daemon, cached by URL.
    public func issueImage(provider: String, url: String) async throws -> Data {
        if let cached = imageCache[url] { return cached }
        let img = try await request(MessageType.issueImage, payload: IssueImageReq(provider: provider, url: url))
            .payload(as: IssueImage.self)
        guard let data = Data(base64Encoded: img.data) else {
            throw NSError(domain: "Oculus", code: -4, userInfo: [NSLocalizedDescriptionKey: "bad image data"])
        }
        imageCache[url] = data
        return data
    }

    /// Fails every in-flight fs request (called when the socket drops).
    private func failPendingRequests(_ error: Error) {
        let inflight = pendingRequests
        pendingRequests.removeAll()
        for (_, cont) in inflight { cont.resume(throwing: error) }
    }

    /// Answer an approval for a session that isn't open, without navigating to it.
    ///
    /// This is what lets the Activity screen resolve "needs you" in place. It deliberately does NOT
    /// touch `pendingApproval` unless the target happens to be the open session — answering session
    /// B's request must not disturb what session A is showing.
    public func respond(_ decision: String, scope: ApprovalScope? = nil, for sessionID: String) async {
        guard let client, let ap = pendingApprovals[sessionID] else { return }
        pendingApprovals[sessionID] = nil
        if self.sessionID == sessionID { pendingApproval = nil }
        refreshLiveActivity()
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.approvalRespond,
                                          payload: ApprovalRespond(approvalID: ap.approvalID, decision: decision, scope: scope))
            try await client.send(env)
        } catch {
            // Put it back — the agent is still blocked, and a silently-dropped decision is the one
            // failure mode this surface cannot have.
            pendingApprovals[sessionID] = ap
            if self.sessionID == sessionID { pendingApproval = ap }
            refreshLiveActivity()
            actionError = "Couldn’t send your \(decision == Decision.deny ? "denial" : "approval").\n\n\(error.localizedDescription)"
            status = "Respond failed"
        }
    }

    /// - Parameter scope: only meaningful with `Decision.always`; nil means the broad
    ///   "this tool, everywhere" rule. The daemon supplies the available scopes on the request.
    public func respond(_ decision: String, scope: ApprovalScope? = nil) async {
        guard let client, let ap = pendingApproval else { return }
        let verb: String
        switch decision {
        case Decision.deny: verb = "✗ Denied"
        case Decision.always: verb = scope.map { "✓ \($0.label)" } ?? "✓ Always allow"
        default: verb = "✓ Allowed"
        }
        let cmd = (ap.detail?.isEmpty == false) ? " · \(ap.detail!)" : ""
        // A scoped Always already names what it covers, so don't repeat the tool after it.
        appendTool(decision == Decision.always && scope != nil ? verb : "\(verb) \(ap.tool)\(cmd)")
        pendingApproval = nil
        refreshLiveActivity()
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.approvalRespond,
                                          payload: ApprovalRespond(approvalID: ap.approvalID, decision: decision, scope: scope))
            try await client.send(env)
        } catch {
            // Restore the pending approval so the agent isn't left hanging on a decision the daemon
            // never received, and tell the user instead of silently dropping it.
            pendingApproval = ap
            refreshLiveActivity()
            actionError = "Couldn’t send your \(decision == Decision.deny ? "denial" : "approval").\n\n\(error.localizedDescription)"
            status = "Respond failed"
        }
    }

    public func discover() async {
        guard let client else { return }
        let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.discover)
        if let env { try? await client.send(env) }
    }

    public func registerDevice(token: String) async {
        guard let client else { return }
        let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.deviceRegister,
                                       payload: DeviceRegister(token: token))
        if let env { try? await client.send(env) }
    }

    // MARK: streaming helpers

    // Token deltas accumulate in `streamBuffer` and are folded into the last streaming
    // `messages` element on a short timer, so the @Published array republishes a few
    // times per second instead of once per token — avoiding O(n) list re-diffing per
    // token (worst-case O(n²) over a long response). The streaming message still lives
    // in `messages`, so the view renders it unchanged.
    private var streamBuffer = ""
    private var flushTask: Task<Void, Never>?
    private static let flushInterval: UInt64 = 40_000_000 // 40ms (~25fps) — streaming text renders
    // plain (cheap) while in-flight, so a faster flush stays smooth without stalling the main thread.

    /// Folds any buffered token text into the current streaming message (one array
    /// mutation). Safe to call at any time — a no-op when nothing is buffered.
    /// Cache-path entry to the same buffered-text fold the live path uses.
    func flushStreamForCache() { flushStream() }

    private func flushStream() {
        guard !streamBuffer.isEmpty else { return }
        if let last = messages.last, last.streaming {
            messages[messages.count - 1].text += streamBuffer
        }
        streamBuffer = ""
    }

    private func scheduleFlush() {
        guard flushTask == nil else { return } // a flush is already pending; it'll drain the buffer
        flushTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: Model.flushInterval)
            guard let self else { return }
            self.flushTask = nil
            self.flushStream()
        }
    }

    private func cancelFlush() {
        flushTask?.cancel()
        flushTask = nil
    }

    private func appendAssistantDelta(_ text: String) {
        // The answer starting means thinking is done — finalize any streaming thinking.
        finalizeThinking()
        if let last = messages.last, last.role == .assistant, last.streaming {
            // keep buffering into the existing streaming message
        } else {
            flushStream()
            messages.append(ChatMessage(role: .assistant, text: "", streaming: true))
        }
        streamBuffer += text
        scheduleFlush()
    }

    /// The tail of the current/most-recent reasoning, for the working bar — so "Thinking" carries the
    /// actual words when the model streams reasoning, not just a label. Empty when there's none.
    public var liveThinkingTail: String {
        // Prefer the live streaming buffer; else the last thinking message this turn.
        if let last = messages.last, last.role == .thinking, last.streaming {
            let t = (last.text + streamBuffer)
            return String(t.suffix(240))
        }
        // Bounded scan. This property is read from the working bar's body, which re-evaluates on every
        // stream flush (~25×/second), and an unbounded `last(where:)` walks to index 0 every time the
        // turn has no reasoning yet — i.e. the common case, on the longest transcripts, at the highest
        // frequency. Reasoning that is more than a screen of messages back is also not "what the agent
        // is thinking now", so the bound costs nothing real.
        let window = messages.suffix(40)
        if let t = window.last(where: { $0.role == .thinking })?.text, !t.isEmpty {
            return String(t.suffix(240))
        }
        return ""
    }

    private func appendThinkingDelta(_ text: String) {
        if let last = messages.last, last.role == .thinking, last.streaming {
            // keep buffering into the existing streaming message
        } else {
            flushStream()
            messages.append(ChatMessage(role: .thinking, text: "", streaming: true))
        }
        streamBuffer += text
        scheduleFlush()
    }

    private func finalizeThinking() {
        if let last = messages.last, last.role == .thinking, last.streaming {
            flushStream() // fold any buffered thinking tokens before sealing the message
            messages[messages.count - 1].streaming = false
        }
    }

    private func finalizeStreaming() {
        finalizeThinking()
        flushStream() // fold any buffered assistant tokens before sealing the message
        cancelFlush()
        if let last = messages.last, last.role == .assistant, last.streaming {
            messages[messages.count - 1].streaming = false
        }
    }

    private func appendTool(_ text: String) {
        finalizeStreaming()
        messages.append(ChatMessage(role: .tool, text: text))
    }

    // MARK: generative UI

    /// The sentinels the daemon wraps the injected iron:ui guide in (see genui.Preamble). Kept in sync
    /// with the Go side.
    private static let uiGuideOpen = "⟦iron:ui-guide⟧"
    private static let uiGuideClose = "⟦/iron:ui-guide⟧"

    /// Removes the injected generative-UI guide from a message so it never shows in the transcript.
    /// The guide is prepended to a session's first user turn; a provider that replays history echoes
    /// it back, so we strip it on display. Safe on text without a guide.
    static func stripUIGuide(_ s: String) -> String {
        guard let start = s.range(of: uiGuideOpen), let end = s.range(of: uiGuideClose) else { return s }
        guard start.lowerBound < end.upperBound else { return s }
        var rest = String(s[end.upperBound...])
        while let f = rest.first, f == "\n" || f == "\r" || f == " " || f == "\t" { rest.removeFirst() }
        return String(s[..<start.lowerBound]) + rest
    }

    /// Sub-agent lifecycle status by id ("started" | "done" | "error"), driving each inline card's
    /// live/finished chrome.
    @Published public var subAgentStatus: [String: String] = [:]
    /// When each sub-agent started — drives its live "elapsed" readout so a long or stuck lane is visible.
    @Published public var subAgentStartedAt: [String: Date] = [:]
    /// The session whose Code surface (file tree · editor · diff review) is open in the Sessions
    /// detail; nil = the chat transcript. Promoted to the model so the chat toolbar's "Code / Review"
    /// button and CodeSurface's own back button can both drive it (was a buried right-click only).
    @Published public var codeReviewTarget: String?
    /// Set to open Design Mode (the in-app browser element picker) — surfaced as a session toolbar
    /// button, not only the Cmd-K palette. The deck presents the sheet when this flips true.
    @Published public var designRequested = false

    /// Folds a sub-agent lifecycle event into the transcript. "started" inserts an inline collapsible
    /// card (once) at the point the parent delegated, and readies its child buffers so the sub-agent's
    /// forwarded output/tools stream into it; "done" seals it. Only for the active parent session.
    private func applySubAgent(_ sa: SubAgent) {
        guard sa.parentID == sessionID else { return }
        subAgentStatus[sa.id] = sa.status
        switch sa.status {
        case "started":
            if subAgentStartedAt[sa.id] == nil { subAgentStartedAt[sa.id] = Date() }
            if childMessages[sa.id] == nil { childMessages[sa.id] = [] }
            if !messages.contains(where: { $0.role == .subagent && $0.subAgentID == sa.id }) {
                finalizeStreaming()
                let title = (sa.title?.isEmpty == false) ? sa.title! : "Sub-agent"
                messages.append(ChatMessage(role: .subagent, text: title, subAgentID: sa.id))
            }
        case "done", "error":
            finalizeChildStreaming(sa.id)
            childActivity[sa.id] = nil
        default: break
        }
    }

    /// Folds a rich tool call into a transcript (the active parent's, or a sub-agent's child buffer),
    /// updating in place by the tool's stable id (running → completed+output) so a card fills in with
    /// its result instead of hiding behind a "running…" chip.
    private func applySessionTool(_ st: SessionTool) {
        // MERGE into an existing card by id (a "running" event carries name+command, the later
        // "completed" event may carry only the output), so neither overwrites the other's fields.
        func merged(into old: ToolCall?) -> ToolCall {
            ToolCall(id: st.id,
                     name: st.name.isEmpty ? (old?.name ?? "") : st.name,
                     title: (st.title?.isEmpty ?? true) ? (old?.title ?? "") : st.title!,
                     output: (st.output?.isEmpty ?? true) ? (old?.output ?? "") : st.output!,
                     status: st.status.isEmpty ? (old?.status ?? "running") : st.status,
                     startedAt: old?.startedAt ?? Date(), // keep the original clock across merges
                     // Only the terminal frame carries a diff (the running one has no result yet),
                     // so a later frame without counts must not wipe the ones we already have.
                     additions: (st.additions ?? 0) > 0 ? st.additions! : (old?.additions ?? 0),
                     deletions: (st.deletions ?? 0) > 0 ? st.deletions! : (old?.deletions ?? 0))
        }
        if st.sessionID == sessionID {
            noteActivity()
            if let idx = messages.firstIndex(where: { $0.role == .tool && $0.tool?.id == st.id }) {
                messages[idx].tool = merged(into: messages[idx].tool)
            } else {
                finalizeStreaming()
                messages.append(ChatMessage(role: .tool, text: st.name, tool: merged(into: nil)))
            }
        } else if childMessages[st.sessionID] != nil {
            noteActivity()
            var buf = childMessages[st.sessionID] ?? []
            if let idx = buf.firstIndex(where: { $0.role == .tool && $0.tool?.id == st.id }) {
                buf[idx].tool = merged(into: buf[idx].tool)
            } else {
                buf.append(ChatMessage(role: .tool, text: st.name, tool: merged(into: nil)))
            }
            childMessages[st.sessionID] = buf
        }
    }

    /// Folds an incoming generative-UI component into the transcript. A component with the same
    /// stable `id` UPDATES in place (running skeleton → ready table); a new id appends a `.ui` row.
    /// Any in-flight streaming assistant text is sealed first so the component lands after it.
    private func applyUIComponent(_ c: UIComponent) {
        if let idx = messages.firstIndex(where: { $0.role == .ui && $0.component?.id == c.id }) {
            messages[idx].component = c
        } else {
            finalizeStreaming()
            messages.append(ChatMessage(role: .ui, text: "", component: c))
        }
    }

    /// The user activated a generative-UI action (choice/confirm). Sends ui.action to the daemon,
    /// which maps it to the NEXT user turn (prompt/answer) or resolves an approval (permission) — the
    /// component can never execute a tool directly. Optimistically echoes a prompt as a user message.
    /// - Parameter values: a form's collected answers; nil for every other component. The daemon
    ///   renders them into the user turn, so the phrasing stays canonical across clients.
    public func invokeUIAction(_ c: UIComponent, _ a: UIComponentAction, values: [String: JSONValue]? = nil) async {
        guard let client else { return }
        let encoded: JSONValue? = values.map { .object($0) }
        let invoke = UIActionInvoke(sessionID: c.sessionID, messageID: c.messageID, componentID: c.id,
                                    actionID: a.id, kind: a.kind, prompt: a.prompt, values: encoded)
        // Echo optimistically, including the submitted values so the user sees what they sent.
        var echo = a.prompt ?? ""
        if let values, !values.isEmpty {
            let lines = values.keys.sorted().compactMap { k -> String? in
                guard let v = values[k], let s = v.prettyJSON, !s.isEmpty else { return nil }
                return "\(k): \(s)"
            }
            if !lines.isEmpty { echo = echo.isEmpty ? lines.joined(separator: "\n") : echo + "\n\n" + lines.joined(separator: "\n") }
        }
        if (a.kind == "prompt" || a.kind == "answer"), !echo.isEmpty {
            messages.append(ChatMessage(role: .user, text: echo, delivery: .sending))
            busy = true
        }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.uiAction, payload: invoke) {
            try? await client.send(env)
        }
    }

    // MARK: sub-agent (child) transcript buffers

    /// Resets all inline child-transcript state — called on parent-session switch so a new session
    /// starts clean (no stale expanded cards, buffers, or lingering subscriptions).
    func clearChildState() {
        childMessages.removeAll()
        childActivity.removeAll()
        expandedChildIDs.removeAll()
        subscribedChildIDs.removeAll()
        subAgentStatus.removeAll()
        subAgentStartedAt.removeAll()
        childStreamBuffers.removeAll()
        childFlushTask?.cancel(); childFlushTask = nil
    }

    // Child deltas are COALESCED (buffer + timed flush) exactly like the parent's streamBuffer. Without
    // this, every sub-agent token re-published `childMessages` and re-rendered the whole transcript —
    // the perf cliff that appeared when sub-agent streaming started working.
    private var childStreamBuffers: [String: String] = [:]
    private var childFlushTask: Task<Void, Never>?

    private func appendChildDelta(_ sid: String, _ text: String) {
        childStreamBuffers[sid, default: ""] += text
        scheduleChildFlush()
    }

    private func scheduleChildFlush() {
        guard childFlushTask == nil else { return }
        childFlushTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 50_000_000) // 50ms (~20fps) — one re-publish per cycle for all children
            guard let self else { return }
            self.childFlushTask = nil
            self.flushChildStreams()
        }
    }

    /// Folds buffered child text for `sid` (or all children) into childMessages in one mutation.
    private func flushChild(_ sid: String) {
        guard let text = childStreamBuffers[sid], !text.isEmpty else { return }
        var buf = childMessages[sid] ?? []
        if let last = buf.last, last.role == .assistant, last.streaming {
            buf[buf.count - 1].text += text
        } else {
            buf.append(ChatMessage(role: .assistant, text: text, streaming: true))
        }
        childMessages[sid] = buf
        childStreamBuffers[sid] = ""
    }

    private func flushChildStreams() {
        for sid in childStreamBuffers.keys { flushChild(sid) }
    }

    /// Seals any in-flight streaming assistant message in a child's buffer (folding pending deltas first).
    private func finalizeChildStreaming(_ sid: String) {
        flushChild(sid) // don't lose buffered text at the boundary
        guard var buf = childMessages[sid], let last = buf.last, last.role == .assistant, last.streaming else { return }
        buf[buf.count - 1].streaming = false
        childMessages[sid] = buf
    }

    // MARK: receive loop

    private func receiveLoop() async {
        guard let client else { return }
        while connected {
            do {
                let data = try await client.recv()
                // Parse the envelope once; env.payload(as:) then decodes only the payload
                // subtree instead of re-tokenizing the whole frame per candidate type.
                let env = try Protocol.envelope(data)
                // Correlated request/reply (fs.*): resolve the waiting continuation by id and
                // skip the broadcast switch. Events carry an empty id, so they fall through.
                if let id = env.id, !id.isEmpty, let cont = pendingRequests.removeValue(forKey: id) {
                    if env.type == MessageType.error {
                        let msg = (try? env.payload(as: [String: String].self))?["message"] ?? "request failed"
                        cont.resume(throwing: NSError(domain: "Oculus", code: -2, userInfo: [NSLocalizedDescriptionKey: msg]))
                    } else {
                        cont.resume(returning: env)
                    }
                    continue
                }
                // While reconciling, transcript frames are collected rather than applied — see
                // finishReconcile: comparing the whole replay against what we painted is exact,
                // whereas applying frame by frame would need a guess per frame.
                if bufferForReconcile(data, env: env) { continue }
                applyEvent(env, raw: data)
                captureFrame(data, env: env)
            } catch {
                connected = false
                status = "Disconnected"
                busy = false
                stopStallLoop()
                refreshLiveActivity(ended: true)
            }
        }
        // Connection dropped — fail any in-flight fs requests and auto-reconnect (unless the
        // user disconnected).
        failPendingRequests(NSError(domain: "Oculus", code: -3, userInfo: [NSLocalizedDescriptionKey: "disconnected"]))
        scheduleReconnect()
    }

    /// Applies ONE daemon frame to the model.
    ///
    /// Extracted from receiveLoop so a cached frame can replay through exactly the same code as a
    /// live one. A separate "restore from cache" rendering path would drift from this one the moment
    /// either changed, and the divergence would surface as a transcript that looks subtly different
    /// depending on whether you happened to have opened the session before.
    @MainActor func applyEvent(_ env: Envelope, raw: Data) {
        if !Self.nonRingFrameTypes.contains(env.type),
           let fs = try? env.payload(as: FrameSessionID.self), let fsid = fs.sessionID, fsid == sessionID {
            daemonEventsRendered += 1
        }
        switch env.type {
        case MessageType.ok:
            // Route the OK frame to exactly one typed decode by its unique payload
            // key — no extra parse (payloadKeys reads the already-parsed envelope).
            let keys = env.payloadKeys
            if keys.contains("items"), let dl = try? env.payload(as: DiscoverList.self), !dl.items.isEmpty {
                discovered = dl.items
            } else if keys.contains("projects"), let pl = try? env.payload(as: ProjectList.self) {
                projects = pl.projects
            } else if keys.contains("sessions"), let sl = try? env.payload(as: SessionList.self) {
                sessions = sl.sessions
            } else if keys.contains("connected"), let st = try? env.payload(as: IntegrationStatus.self) {
                applyIntegrationStatus(st)
            } else if keys.contains("issues"), let il = try? env.payload(as: IssueList.self) {
                issues = il.issues
            } else if keys.contains("url"), keys.contains("provider"), let oa = try? env.payload(as: IntegrationOAuth.self), let u = oa.url, let url = URL(string: u) {
                oauthURL = url
            } else if keys.contains("diff"), let wd = try? env.payload(as: WorktreeDiff.self), wd.diff != nil {
                lastDiff = wd.diff
            } else if keys.contains("files"), let wc = try? env.payload(as: WorktreeConflicts.self), wc.files != nil {
                conflicts = wc.files ?? []
            } else if keys.contains("pushed"), let pr = try? env.payload(as: WorktreePRResult.self) {
                status = pr.url.map { "PR: \($0)" } ?? "Pushed \(pr.branch)"
            } else if keys.contains("id"), let s = try? env.payload(as: Session.self) {
                sessionID = s.id
                currentSession = s
                refreshLiveActivity()
                Task { await loadSessions() } // reflect the new session in the sidebar
            }
        case MessageType.deviceCredential:
            // The daemon just enrolled this device and handed it the credential it keeps from now on.
            // It arrives as a frame rather than in the handshake because the handshake's pairing proof
            // is a bare string with no room for a reply — and widening that wire format would break
            // every client already in the wild. The channel is already encrypted and authenticated by
            // this point, so a normal frame is the right carrier.
            if let dc = try? env.payload(as: DeviceCredential.self), !dc.credential.isEmpty {
                applyDeviceCredential(dc.credential)
            }
        case MessageType.error:
            // An uncorrelated error (a fire-and-forget send the daemon rejected, e.g. a
            // session that couldn't start) — surface it instead of silently doing nothing.
            if let e = try? env.payload(as: [String: String].self), let m = e["message"] {
                let low = m.lowercased()
                // Benign restart artifact: a fire-and-forget subscribe/attach to a session the
                // daemon dropped after a restart. It's now surfaced as a "stopped" session with
                // a Restart action, so don't raise a scary alert for it.
                if low.contains("no such session") || low.contains("no session") || low.contains("cannot attach") {
                    break
                }
                status = "Error"
                statusDetail = m
                actionError = m
                busy = false
            }
        case MessageType.sessionMessage:
            if let m = try? env.payload(as: SessionMessage.self), m.sessionID == sessionID {
                noteActivity()
                let role: ChatMessage.Role = m.role == "user" ? .user : (m.role == "tool" ? .tool : .assistant)
                let shown = Self.stripUIGuide(m.text) // hide the injected iron:ui guide from turn 1
                let trimmed = shown.trimmingCharacters(in: .whitespacesAndNewlines)
                // Skip the daemon's echo of a user turn we already show. We append every SENT
                // prompt locally for instant feedback, and opencode then echoes the same user
                // message back (sometimes more than once, e.g. a slash-command expansion) —
                // arriving AFTER the assistant text + tool cards, so a `messages.last` check
                // missed it and the prompt duplicated. Match against ANY user message already
                // on screen (live path only; replayed history is handled by dedupReplay below).
                if role == .user, !trimmed.isEmpty, !dedupReplay,
                   messages.contains(where: { $0.role == .user && $0.text.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed }) {
                    break
                }
                // Just after a live re-attach, the provider replays history; skip messages
                // that duplicate ones already on screen so the transcript doesn't double.
                // Scope the scan: during a page load, only rows at or after the page anchor can be
                // duplicates of what the page carries. Scanning the whole transcript grows without
                // bound and can silently collapse a legitimately repeated line from earlier on.
                if dedupReplay {
                    let window = pageAnchor.map { messages[min($0, messages.count)...] } ?? messages[messages.startIndex...]
                    if window.contains(where: { $0.role == role && $0.text.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed }) {
                        break
                    }
                }
                // A full-text assistant message that arrives while an assistant message is still
                // streaming is the daemon's authoritative end-of-turn RESYNC (opencode's SSE
                // dropped mid-turn, so it re-sends the completed text) — REPLACE the partial
                // streamed message with it rather than appending a duplicate.
                if role == .assistant, let last = messages.last, last.role == .assistant, last.streaming {
                    streamBuffer = ""; cancelFlush() // drop the partial; the resync text is authoritative
                    messages[messages.count - 1].text = shown
                    messages[messages.count - 1].streaming = false
                    break
                }
                finalizeStreaming()
                // Attribute a user message that came from ANOTHER device. Our own echoes are
                // deduped above, so anything reaching here with an author is someone else's.
                let author = (role == .user && m.author != identity) ? m.author : nil
                messages.append(ChatMessage(role: role, text: shown, author: author))
            } else if let m = try? env.payload(as: SessionMessage.self), childMessages[m.sessionID] != nil {
                // A sub-agent's message — route into its own buffer, never the main transcript.
                // Tool calls arrive as role=="tool" with Text=the tool name; keep them.
                noteActivity() // the active session's sub-agent is alive → parent isn't dead
                let role: ChatMessage.Role = m.role == "user" ? .user : (m.role == "tool" ? .tool : .assistant)
                finalizeChildStreaming(m.sessionID)
                childMessages[m.sessionID, default: []].append(ChatMessage(role: role, text: Self.stripUIGuide(m.text)))
            }
        case MessageType.thinking:
            if let t = try? env.payload(as: Thinking.self), t.sessionID == sessionID {
                appendThinkingDelta(t.text)
                busy = true
                noteActivity() // reset AFTER busy=true so a mid-turn stall is still caught
            }
        case MessageType.outputDelta:
            if let d = try? env.payload(as: OutputDelta.self), d.sessionID == sessionID {
                noteActivity()
                appendAssistantDelta(d.text)
            } else if let d = try? env.payload(as: OutputDelta.self), childMessages[d.sessionID] != nil {
                noteActivity() // sub-agent output = the parent is alive
                appendChildDelta(d.sessionID, d.text)
            }
        case MessageType.sessionStatus:
            if let ss = try? env.payload(as: SessionStatus.self), ss.sessionID == sessionID {
                // Any status for the active session clears its background-error badge.
                sessionErrors[ss.sessionID] = nil
                noteActivity() // re-arms while running; no-ops once busy clears on idle/done below
                status = ss.status
                activity = ss.detail
                switch ss.status {
                case SessionStatusValue.idle, SessionStatusValue.done:
                    pendingApproval = nil; busy = false; activity = nil; finalizeStreaming()
                case SessionStatusValue.awaitingApproval:
                    busy = false
                case SessionStatusValue.needsYou:
                    // A stuck turn the daemon gave up nudging. The transcript note comes from
                    // turn.state (applyTurnState) — here we only make sure the spinner stops.
                    // Without this case it fell to `default`, which sets busy = true: the one status
                    // that means "this needs a human" would have rendered as "working".
                    busy = false; pendingApproval = nil; activity = nil; finalizeStreaming()
                case SessionStatusValue.error, "errored":
                    // An errored session isn't "working": stop the spinner, keep the reason
                    // (statusDetail) AND put it in the transcript so it's actually readable —
                    // it was previously only flashed as transient `activity` next to the dots.
                    busy = false; pendingApproval = nil; activity = nil; finalizeStreaming()
                    let reason = (ss.detail?.isEmpty == false) ? ss.detail! : "The agent reported an error."
                    statusDetail = reason
                    if messages.last?.text != "⚠️ \(reason)" {
                        messages.append(ChatMessage(role: .system, text: "⚠️ \(reason)"))
                    }
                default:
                    busy = true
                    // NOTE: do NOT clear pendingApproval here — with parallel tool
                    // calls a sibling tool can be "running" while another awaits
                    // approval. Cross-client clear happens on idle / when a new
                    // approval replaces it.
                }
                refreshLiveActivity()
            } else if let ss = try? env.payload(as: SessionStatus.self), childMessages[ss.sessionID] != nil {
                // A sub-agent's status — drives its inline card's activity chip. It ALSO keeps the
                // parent watchdog alive: a long sub-agent "Reading" emits status, not deltas, so
                // without this the parent could falsely time out while the sub-agent works.
                noteActivity()
                switch ss.status {
                case SessionStatusValue.idle, SessionStatusValue.done, SessionStatusValue.error, "errored":
                    childActivity[ss.sessionID] = nil
                    finalizeChildStreaming(ss.sessionID)
                default:
                    childActivity[ss.sessionID] = ss.detail
                }
            } else if let ss = try? env.payload(as: SessionStatus.self) {
                // A BACKGROUND session (not active, not a child): record/clear an error so a
                // session whose sends stopped landing surfaces in the sidebar instead of failing
                // invisibly while you're looking at another session. The daemon's no-response
                // watchdog now emits this for ANY session, so background stalls are catchable.
                switch ss.status {
                case SessionStatusValue.error, "errored":
                    sessionErrors[ss.sessionID] = (ss.detail?.isEmpty == false) ? ss.detail! : "The agent reported an error."
                case SessionStatusValue.running, SessionStatusValue.idle, SessionStatusValue.done:
                    sessionErrors[ss.sessionID] = nil
                default:
                    break
                }
            }
        case MessageType.approvalRequest:
            // Record EVERY approval, not just the open session's. The old `ar.sessionID == sessionID`
            // guard silently dropped the rest, so a request raised while you were on another screen
            // was gone until the daemon re-sent it.
            if let ar = try? env.payload(as: ApprovalRequest.self) {
                pendingApprovals[ar.sessionID] = ar
                if ar.sessionID == sessionID {
                    stopStallLoop()
                    pendingApproval = ar
                }
                refreshLiveActivity()
            }
        case MessageType.approvalResolved:
            // Another device answered this exact approval — clear our card and
            // mirror the decision so both transcripts match.
            if let r = try? env.payload(as: ApprovalResolved.self) {
                // Drop it from the map wherever it lived, so a stale "needs you" row can't survive
                // another device answering it.
                for (sid, ap) in pendingApprovals where ap.approvalID == r.approvalID {
                    pendingApprovals[sid] = nil
                }
            }
            if let r = try? env.payload(as: ApprovalResolved.self),
               let ap = pendingApproval, ap.approvalID == r.approvalID {
                let verb = r.decision == Decision.deny ? "✗ Denied"
                    : (r.decision == Decision.always ? "✓ Always allow" : "✓ Allowed")
                let cmd = (ap.detail?.isEmpty == false) ? " · \(ap.detail!)" : ""
                appendTool("\(verb) \(ap.tool)\(cmd)")
                pendingApproval = nil
                refreshLiveActivity()
            }
        case MessageType.transcriptPageBegin:
            if let b = try? env.payload(as: TranscriptPageBegin.self), b.sessionID == sessionID {
                pageAnchor = messages.count
                dedupReplay = true // the page can overlap what's on screen
            }
        case MessageType.transcriptPageEnd:
            if let e = try? env.payload(as: TranscriptPageEnd.self), e.sessionID == sessionID {
                // NOT `+= e.count`: every frame in the page already incremented the counter as it
                // arrived through applyEvent. Adding the page size again double-counted, so the next
                // "Show earlier" asked the daemon to skip twice as far and silently jumped history.
                // Lift the page above the messages that were already there. It arrived in
                // chronological order and appended, so one rotation puts it in place.
                if let anchor = pageAnchor, messages.count > anchor {
                    let page = Array(messages[anchor...])
                    messages.removeSubrange(anchor...)
                    messages.insert(contentsOf: page, at: 0)
                }
                pageAnchor = nil
                hasEarlierHistory = e.hasMore
                loadingEarlier = false
                dedupReplay = false
            }
        case MessageType.turnState:
            if let ts = try? env.payload(as: TurnState.self) { applyTurnState(ts) }
        case MessageType.sessionList:
            // PROACTIVE broadcast the daemon sends after any session create/delete/restore/
            // model change. Without handling it here the sidebar only refreshed on an explicit
            // reload — so a 2nd session (or a restored set) never appeared until a manual
            // refresh. Update the list live.
            if let sl = try? env.payload(as: SessionList.self) {
                sessions = sl.sessions
                // The daemon is authoritative on mode (another device may have switched it).
                if let cur = sl.sessions.first(where: { $0.id == sessionID }) {
                    sessionMode = cur.mode ?? SessionMode.code
                }
            }
        case MessageType.worktreeStatus:
            // PROACTIVE broadcast from the daemon's PR-check watcher, which re-sends the full
            // status whenever a poll actually changes the rollup. Without this case the watcher
            // notifies (a push, an Activity entry) about a build the open panel keeps showing as
            // green until you hit Refresh — the notification and the screen disagreeing about the
            // same PR. Same request type reused as an event, exactly as session.list does.
            if let st = try? env.payload(as: WorktreeStatusResult.self), st.sessionID == sessionID {
                worktreeStatus = st
            }
        case MessageType.issueList: // broadcast from the 60s tracker poll
            if let il = try? env.payload(as: IssueList.self) {
                issues = il.issues
            }
        case MessageType.integrationStatus: // broadcast after (re)connect/disconnect
            if let st = try? env.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
        case MessageType.telemetryStatus: // broadcast after a toggle on any device
            if let t = try? env.payload(as: Telemetry.self) { telemetryEnabled = t.enabled }
        case MessageType.sessionProgress: // live create step → prescriptive loading checklist
            if startingSession, let p = try? env.payload(as: SessionProgress.self) {
                applyCreateStep(p)
            }
        case MessageType.logLine: // streamed daemon log line → Developer log panel
            if let l = try? env.payload(as: LogLine.self) {
                daemonLog.append(l.line)
                if daemonLog.count > 2000 { daemonLog.removeFirst(daemonLog.count - 2000) }
            }
        case MessageType.activityEvent: // new cross-session activity item → prepend to the feed
            if let e = try? env.payload(as: ActivityEvent.self) {
                activityFeed.removeAll { $0.id == e.id }
                activityFeed.insert(e, at: 0)
                if activityFeed.count > 500 { activityFeed.removeLast(activityFeed.count - 500) }
            }
        case MessageType.lspDiagnostics: // language server published diagnostics for a file
            if let d = try? env.payload(as: LSPDiagnostics.self) {
                diagnostics[d.path] = d.diagnostics
            }
        case MessageType.sessionUsage: // per-turn token/cost usage; accumulate onto the session
            if let u = try? env.payload(as: SessionUsage.self), u.sessionID == sessionID {
                currentSession?.inputTokens = (currentSession?.inputTokens ?? 0) + u.inputTokens
                currentSession?.outputTokens = (currentSession?.outputTokens ?? 0) + u.outputTokens
                currentSession?.costUSD = (currentSession?.costUSD ?? 0) + u.costUSD
            }
        case MessageType.sessionTodos: // the agent's live to-do list (replaces the prior list)
            if let t = try? env.payload(as: SessionTodos.self), t.sessionID == sessionID {
                todos = t.todos
            }
        case MessageType.uiComponent: // a normalized generative-UI component (projected or fenced)
            if let c = try? env.payload(as: UIComponent.self), c.sessionID == sessionID {
                applyUIComponent(c)
            }
        case MessageType.sessionSubAgent: // a sub-agent started/finished under the active session
            if let sa = try? env.payload(as: SubAgent.self) {
                applySubAgent(sa)
                noteActivity() // a sub-agent spinning up means the parent is alive
            }
        case MessageType.sessionTool: // a rich tool call (command + output) for the session or a sub-agent
            if let st = try? env.payload(as: SessionTool.self) {
                applySessionTool(st)
            }
        case MessageType.sessionHeartbeat: // supervision "on-track" state for a session
            if let hb = try? env.payload(as: SessionHeartbeat.self) {
                heartbeats[hb.sessionID] = hb
                // The daemon disarms autonomy when a session exhausts its budget; mirror that
                // so the toggle reflects reality.
                if hb.sessionID == sessionID, hb.state == "exhausted" { autonomous = false }
            }
        case MessageType.handoffList: // the handoff index changed (agent saved progress)
            if let hl = try? env.payload(as: HandoffList.self) {
                handoffs = hl.handoffs
            }
        case MessageType.loopList: // a loop config changed or a run started
            if let ll = try? env.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
        case MessageType.providerList: // pushed after a custom agent is added/removed
            if let pl = try? env.payload(as: ProviderList.self) { applyProviders(pl.providers); providersLoaded = true }
        case MessageType.participants: // someone joined, left, or had their role changed
            if let pl = try? env.payload(as: ParticipantList.self) {
                participants = pl.participants
                sharingEnabled = pl.enabled
            }
        case MessageType.mcpChanged: // a server was added/removed/toggled, or a probe finished
            if let list = try? env.payload(as: MCPList.self) { mcpServers = list.servers }
        case MessageType.fanoutSummary: // every variant in a fan-out group finished
            if let sum = try? env.payload(as: FanoutSummary.self) { fanoutSummary = sum }
        case MessageType.approvalRulesChanged: // an Always answer or a revoke, on ANY device
            if let list = try? env.payload(as: ApprovalRulesList.self) { approvalRules = list.rules }
        case MessageType.runOutput: // streamed line from a test/build/!command run
            if let o = try? env.payload(as: RunOutput.self), o.sessionID == sessionID {
                // A bang run OWNS the stream while it's in flight (the two entry points disable each
                // other, so this is never an ambiguous handoff) — its lines belong in its transcript
                // row, not in the test panel the user never opened.
                if shellRunID != nil, claimShellLine(o.line) {
                    // consumed by the `!command` row
                } else {
                    testOutput.append(o.line)
                    if testOutput.count > 2000 { testOutput.removeFirst(testOutput.count - 2000) }
                }
            }
        case MessageType.runResult: // test/build/!command run finished
            if let r = try? env.payload(as: RunResult.self), r.sessionID == sessionID {
                if shellRunID != nil {
                    finishShellRun(ok: r.ok, exitCode: r.exitCode)
                } else {
                    testResult = r
                    testRunning = false
                }
            }
        default:
            break
        }
    }

    // MARK: live activity

    /// Starts/updates/ends the session Live Activity to match current state.
    /// Ends any Live Activities left over from a previous launch (e.g. the app was killed while an
    /// activity was showing), so none linger in the Dynamic Island. A new one is created on demand
    /// when an agent is actually working.
    func clearStaleLiveActivities() {
        #if os(iOS)
        if #available(iOS 16.1, *) {
            for act in Activity<OculusActivityAttributes>.activities {
                Task { await act.end(dismissalPolicy: .immediate) }
            }
            liveActivity = nil
        }
        #endif
    }

    func refreshLiveActivity(ended: Bool = false) {
        #if os(iOS)
        if #available(iOS 16.1, *) {
            if ended {
                if let act = liveActivity as? Activity<OculusActivityAttributes> {
                    Task { await act.end(dismissalPolicy: .immediate) }
                }
                liveActivity = nil
                return
            }
            // The activity is meaningful only while the agent is WORKING or an approval is pending.
            // When idle/done (or no session) end it so it doesn't linger in the Dynamic Island.
            guard ActivityAuthorizationInfo().areActivitiesEnabled, let sid = sessionID,
                  busy || pendingApproval != nil else {
                if let act = liveActivity as? Activity<OculusActivityAttributes> {
                    Task { await act.end(dismissalPolicy: .immediate) }
                }
                liveActivity = nil
                return
            }
            let state = OculusActivityAttributes.ContentState(
                status: status, tool: pendingApproval?.tool, awaitingApproval: pendingApproval != nil
            )
            if let act = liveActivity as? Activity<OculusActivityAttributes> {
                Task { await act.update(using: state) }
            } else {
                liveActivity = try? Activity.request(
                    attributes: OculusActivityAttributes(sessionID: sid), contentState: state
                )
            }
        }
        #endif
    }
}

extension Model {
    /// SF Symbol reflecting live state — used by the menu-bar item.
    public var menuBarSymbol: String {
        if pendingApproval != nil { return "bell.badge.fill" }
        if connected { return "bolt.horizontal.circle.fill" }
        return "bolt.horizontal.circle"
    }
}

#if os(macOS)
/// Compact menu-bar surface: live status + one-tap approve/deny for the active desktop.
public struct MenuBarView: View {
    @ObservedObject var store: DesktopStore
    public init(store: DesktopStore) { self.store = store }

    public var body: some View {
        if let model = store.active {
            MenuBarBody(model: model, store: store)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                Text("Iron Rain").font(.headline)
                Text("No desktop paired. Open the window to add one.")
                    .font(.caption).foregroundStyle(.secondary)
                Divider()
                Button("Quit Iron Rain") { NSApplication.shared.terminate(nil) }
            }.padding(12).frame(width: 240)
        }
    }
}

struct MenuBarBody: View {
    @ObservedObject var model: Model
    @ObservedObject var store: DesktopStore

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Iron Rain").font(.headline)
                Spacer()
                if store.models.count > 1 {
                    Menu(model.name.isEmpty ? "Desktop" : model.name) {
                        ForEach(store.models, id: \.id) { m in
                            Button(m.name.isEmpty ? "Desktop" : m.name) { store.selectedID = m.id }
                        }
                    }.fixedSize()
                }
            }
            Text(model.status).font(.caption).foregroundStyle(.secondary)

            if let ap = model.pendingApproval {
                Divider()
                Text("Approve \(ap.tool)?").bold()
                HStack {
                    Button("Deny") { Task { await model.respond(Decision.deny) } }
                    Button("Allow") { Task { await model.respond(Decision.allow) } }
                }
            } else if model.connected {
                Text("\(model.discovered.count) detected · \(model.messages.count) messages")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Text("Open the window to connect.").font(.caption).foregroundStyle(.secondary)
            }

            Divider()
            Button("Quit Iron Rain") { NSApplication.shared.terminate(nil) }
        }
        .padding(12)
        .frame(width: 240)
    }
}
#endif
