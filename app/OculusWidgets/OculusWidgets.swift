import ActivityKit
import OculusUI
import SwiftUI
import WidgetKit

/// Live Activity for an Oculus session: shows live status on the lock screen and in
/// the Dynamic Island, and flags a pending tool approval. Real Dynamic Island
/// rendering needs a device; this builds and runs in the iOS 16.1+ simulator.
@available(iOS 16.1, *)
struct OculusLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: OculusActivityAttributes.self) { context in
            // Lock screen / banner presentation.
            HStack(spacing: 12) {
                Image(systemName: context.state.awaitingApproval ? "bell.badge.fill" : "bolt.horizontal.circle.fill")
                    .foregroundStyle(Color(hex: 0xD9A520))
                    .font(.title2)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Oculus session").font(.headline)
                    Text(subtitle(context.state)).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
            }
            .padding()
            .activityBackgroundTint(Color.black)
            .activitySystemActionForegroundColor(Color(hex: 0xD9A520))
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    Image(systemName: "bolt.horizontal.circle.fill").foregroundStyle(Color(hex: 0xD9A520))
                }
                DynamicIslandExpandedRegion(.trailing) {
                    if context.state.awaitingApproval { Image(systemName: "bell.badge.fill").foregroundStyle(Color(hex: 0xD9A520)) }
                }
                DynamicIslandExpandedRegion(.center) {
                    Text(subtitle(context.state)).font(.caption)
                }
            } compactLeading: {
                Image(systemName: context.state.awaitingApproval ? "bell.badge.fill" : "bolt.horizontal.circle")
                    .foregroundStyle(Color(hex: 0xD9A520))
            } compactTrailing: {
                if context.state.awaitingApproval { Text("!").bold().foregroundStyle(Color(hex: 0xD9A520)) }
            } minimal: {
                Image(systemName: "bolt.horizontal.circle.fill").foregroundStyle(Color(hex: 0xD9A520))
            }
        }
    }

    private func subtitle(_ s: OculusActivityAttributes.ContentState) -> String {
        s.awaitingApproval ? "Approve \(s.tool ?? "tool")?" : s.status
    }
}

@main
struct OculusWidgetsBundle: WidgetBundle {
    var body: some Widget {
        if #available(iOS 16.1, *) {
            OculusLiveActivity()
        }
    }
}
