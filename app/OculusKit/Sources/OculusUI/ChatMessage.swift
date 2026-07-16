import Foundation

/// One item in a session conversation. Assistant text streams in-place (append to the
/// last streaming assistant message); tool/approval/system rows are discrete.
public struct ChatMessage: Identifiable, Equatable {
    public enum Role: Equatable {
        case user
        case assistant
        case tool     // a tool invocation / result note
        case system   // session lifecycle, status, handoff
    }

    public let id: UUID
    public var role: Role
    public var text: String
    public var streaming: Bool

    public init(id: UUID = UUID(), role: Role, text: String, streaming: Bool = false) {
        self.id = id
        self.role = role
        self.text = text
        self.streaming = streaming
    }
}
