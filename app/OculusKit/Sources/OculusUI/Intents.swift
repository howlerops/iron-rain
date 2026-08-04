import AppIntents
import Foundation
import OculusKit

/// App-wide store so App Intents (which run outside the SwiftUI view tree) and
/// Handoff can hand work to the running app. `Model` observes it: a queued prompt
/// is started on the next connect; a handed-off session id is surfaced.
@MainActor
public final class OculusStore: ObservableObject {
    public static let shared = OculusStore()
    @Published public var pendingPrompt: String?
    @Published public var handoffSessionID: String?
    /// APNs device token (hex) captured by the app delegate once the OS grants one.
    @Published public var deviceToken: String?
    /// A decision (allow/deny) chosen from a notification action, to apply on connect.
    @Published public var pendingDecision: String?
    private var decisionExpiry: Task<Void, Never>?
    private init() {}

    /// Queues an approval answer for the Model to apply on its next connect, and drops it if
    /// nothing consumes it.
    ///
    /// The expiry is the point of this method. `pendingDecision` is drained only when a connect
    /// finds an approval still waiting; if that approval was already answered somewhere else, the
    /// value just stays set — and the NEXT approval to come along, for a different tool and
    /// possibly a different session, silently inherits it. That is an auto-approve of something
    /// the user never saw. An answer only ever means anything for the request that prompted it, so
    /// an unconsumed one is discarded rather than left armed.
    public func queueDecision(_ decision: String, expiresIn seconds: UInt64 = 90) {
        pendingDecision = decision
        decisionExpiry?.cancel()
        decisionExpiry = Task { [weak self] in // inherits @MainActor from the enclosing class
            try? await Task.sleep(nanoseconds: seconds * 1_000_000_000)
            guard !Task.isCancelled else { return }
            self?.pendingDecision = nil
        }
    }
}

/// Siri / Shortcuts: "Start a session in Oculus". Opens the app and queues the
/// prompt; the app starts it once connected.
@available(iOS 16.0, macOS 13.0, *)
public struct StartSessionIntent: AppIntent {
    public static var title: LocalizedStringResource = "Start Iron Rain Session"
    public static var openAppWhenRun: Bool = true
    public init() {}

    @Parameter(title: "Prompt")
    public var prompt: String

    @MainActor
    public func perform() async throws -> some IntentResult {
        OculusStore.shared.pendingPrompt = prompt
        return .result()
    }
}

/// Answering a pending tool approval from the Lock Screen, Siri or a Shortcut — without first
/// finding the session and scrolling to the card.
///
/// Two intents rather than one with an allow/deny parameter, because the two carry DIFFERENT
/// authentication requirements and `authenticationPolicy` is a per-intent static. Merging them
/// would force one of the two to be wrong: either denying needs a face scan, or — far worse —
/// approving stops needing one.
///
/// Both hand the answer to `OculusStore` rather than calling the daemon: an intent has no
/// connection of its own, and the Model applies a queued decision on its next connect (the same
/// path the approval push notification's actions use). The consequence, which is real: the answer
/// lands on the approval that is open when the app next connects, so it is only meaningful
/// immediately after the request — hence the expiry in `queueDecision`.
@available(iOS 16.0, macOS 13.0, *)
public struct ApproveToolIntent: AppIntent {
    public static var title: LocalizedStringResource = "Approve Pending Tool"
    public static var description = IntentDescription("Approves the tool call your agent is waiting on.")
    /// Approving lets an agent run a command on your Mac, so this is the one intent here that must
    /// never fire from a locked pocket or an unattended automation. `.requiresLocalDeviceAuthentication`
    /// makes the OS demand Face ID / Touch ID / passcode before `perform` is ever called. This
    /// matches what the approval PUSH NOTIFICATION already does (`.authenticationRequired` on its
    /// Allow action) — the two are the same remote capability and must not disagree about its cost.
    public static var authenticationPolicy: IntentAuthenticationPolicy = .requiresLocalDeviceAuthentication
    /// The queued decision is drained by the Model on connect, which only happens in-app.
    public static var openAppWhenRun: Bool = true
    public init() {}

    @MainActor
    public func perform() async throws -> some IntentResult {
        OculusStore.shared.queueDecision(Decision.allow)
        return .result()
    }
}

/// The safe half of the pair — see `ApproveToolIntent` for why they are separate types.
@available(iOS 16.0, macOS 13.0, *)
public struct DenyToolIntent: AppIntent {
    public static var title: LocalizedStringResource = "Deny Pending Tool"
    public static var description = IntentDescription("Denies the tool call your agent is waiting on.")
    /// Deliberately ungated. Denying only ever WITHHOLDS a capability, so the failure mode of
    /// running it unintentionally is a blocked agent, not a command on someone's Mac. Gating it
    /// would make refusing slower than allowing, which is exactly backwards.
    public static var authenticationPolicy: IntentAuthenticationPolicy = .alwaysAllowed
    public static var openAppWhenRun: Bool = true
    public init() {}

    @MainActor
    public func perform() async throws -> some IntentResult {
        OculusStore.shared.queueDecision(Decision.deny)
        return .result()
    }
}

/// The Handoff activity type advertised by the app (also listed in Info.plist under
/// NSUserActivityTypes).
public let oculusSessionActivityType = "com.howlerops.oculus.session"

/// Keys inside a session activity's `userInfo`. Shared so the publisher (ChatView) and the
/// receiver (the app's scene) cannot drift apart — a typo on one side is a Handoff that silently
/// does nothing, which is indistinguishable from Handoff not being implemented at all.
///
/// IDENTIFIERS ONLY. A user activity is replicated between devices through iCloud, so nothing that
/// grants access may travel in it: the pairing secret stays in the Keychain, and a device that is
/// not already paired to a Mac must not become able to reach it just because an activity arrived.
public enum OculusHandoffKey {
    /// The session to reopen on the receiving device.
    public static let sessionID = "session_id"
    /// Which Mac the session belongs to — the daemon's PUBLIC key. It names a desktop and confers
    /// nothing; without the paired secret it cannot be connected to. Carrying it is what lets the
    /// receiver tell "you aren't paired with that Mac" apart from "that session is gone".
    public static let desktopID = "desktop_id"
}
