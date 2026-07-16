#if os(iOS)
import ActivityKit
import Foundation

/// Live Activity attributes for an Oculus session — shared by the app (which starts
/// and updates the activity) and the widget extension (which renders it). The
/// dynamic `ContentState` carries the live status and any pending approval.
@available(iOS 16.1, *)
public struct OculusActivityAttributes: ActivityAttributes {
    public struct ContentState: Codable, Hashable {
        public var status: String
        public var tool: String?
        public var awaitingApproval: Bool
        public init(status: String, tool: String? = nil, awaitingApproval: Bool = false) {
            self.status = status
            self.tool = tool
            self.awaitingApproval = awaitingApproval
        }
    }

    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
}
#endif
