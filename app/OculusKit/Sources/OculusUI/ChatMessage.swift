import Foundation
import OculusKit

/// One item in a session conversation. Assistant text streams in-place (append to the
/// last streaming assistant message); tool/approval/system/ui rows are discrete.
public struct ChatMessage: Identifiable, Equatable {
    public enum Role: Equatable {
        case user
        case assistant
        case thinking // the agent's reasoning ("it's working")
        case tool     // a tool invocation / result note
        case system   // session lifecycle, status, handoff
        case ui       // a normalized generative-UI component (see UIComponent)
        case subagent // an inline, collapsible sub-agent card (its work streams into childMessages[id])
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
    /// Set only for `.ui` rows: the normalized generative-UI component to render. Its own string `id`
    /// (component.id) is stable within a message, so a `running` component updates in place to `ready`.
    public var component: UIComponent?
    /// Set only for `.subagent` rows: the sub-agent's session id, which keys its live streamed
    /// transcript in Model.childMessages / childActivity / subAgentStatus.
    public var subAgentID: String?
    /// Set only for `.tool` rows sourced from a rich tool event — the invocation + its output,
    /// updated in place by `tool.id` as the tool runs → completes.
    public var tool: ToolCall?

    public init(id: UUID = UUID(), role: Role, text: String, streaming: Bool = false, delivery: Delivery = .ok, component: UIComponent? = nil, subAgentID: String? = nil, tool: ToolCall? = nil) {
        self.id = id
        self.role = role
        self.text = text
        self.streaming = streaming
        self.delivery = delivery
        self.component = component
        self.subAgentID = subAgentID
        self.tool = tool
    }
}

/// A tool invocation shown as a rich, collapsible card (name · title command · output).
public struct ToolCall: Equatable {
    public var id: String        // stable tool-part id (update in place)
    public var name: String      // bash, read, edit, …
    public var title: String     // human command summary
    public var output: String    // result / error text
    public var status: String    // running | completed | error
    public init(id: String, name: String, title: String, output: String, status: String) {
        self.id = id; self.name = name; self.title = title; self.output = output; self.status = status
    }
}
