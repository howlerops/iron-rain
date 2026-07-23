import Foundation

/// One item in a session conversation. Assistant text streams in-place (append to the
/// last streaming assistant message); tool/approval/system rows are discrete.
public struct ChatMessage: Identifiable, Equatable {
    public enum Role: Equatable {
        case user
        case assistant
        case thinking // the agent's reasoning ("it's working")
        case tool     // a tool invocation / result note
        case system   // session lifecycle, status, handoff
    }

    /// Delivery state for a USER message, so a send that never reached the agent is visibly marked
    /// (and retryable) instead of sitting in the transcript looking exactly like a delivered one.
    public enum Delivery: Equatable {
        case ok       // reached the daemon (or not a user message)
        case sending  // in flight, awaiting the daemon's ack
        case failed   // not delivered (disconnected / daemon rejected) — offer a retry
    }

    public let id: UUID
    public var role: Role
    public var text: String
    public var streaming: Bool
    public var delivery: Delivery

    public init(id: UUID = UUID(), role: Role, text: String, streaming: Bool = false, delivery: Delivery = .ok) {
        self.id = id
        self.role = role
        self.text = text
        self.streaming = streaming
        self.delivery = delivery
    }
}
