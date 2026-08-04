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
                    Text("Iron Rain session").font(.headline)
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

// MARK: - Home-screen glance

/// One reading of the fleet: how many agents are working, and how many are blocked on you.
///
/// `isLive` is not decoration. It separates "the real answer is zero" from "there is no answer",
/// which the view must render differently — see `FleetProvider`.
struct FleetEntry: TimelineEntry {
    let date: Date
    let running: Int
    let needsYou: Int
    let isLive: Bool
}

/// ⚠️ THIS PROVIDER HAS NO DATA SOURCE, AND CANNOT HAVE ONE YET. READ BEFORE WIRING ANYTHING IN.
///
/// A widget runs in its own process, so it cannot see the app's in-memory `Model`; the only
/// supported channel between them is a shared container, and this project has none. `Oculus`'s
/// entitlements declare `aps-environment` and nothing else, and the `OculusWidgets` target in
/// `project.yml` has no entitlements file at all. Adding an App Group changes entitlements and the
/// provisioning profile for BOTH targets, so it is not invented here.
///
/// Until that exists this widget reports that it has nothing to show, rather than rendering
/// "0 running · 0 need you". A confident zero would answer the user's question wrongly — and
/// wrongly in the dangerous direction, since "nothing needs me" is exactly what someone glances at
/// the widget to confirm — with no way for them to tell it apart from the truth.
///
/// To finish once an App Group is provisioned: add it to both targets, have `Model` write a small
/// {running, needsYou, updatedAt} snapshot into `UserDefaults(suiteName:)` whenever those counts
/// change, read it in `currentEntry()` with `isLive: true`, and have the app call
/// `WidgetCenter.shared.reloadAllTimelines()` after each write.
struct FleetProvider: TimelineProvider {
    func placeholder(in context: Context) -> FleetEntry {
        FleetEntry(date: Date(), running: 2, needsYou: 1, isLive: false)
    }

    func getSnapshot(in context: Context, completion: @escaping (FleetEntry) -> Void) {
        // The widget GALLERY is the one place representative numbers are honest — it is showing
        // what the widget is for, not what your fleet is doing. Everywhere else, tell the truth.
        completion(context.isPreview ? placeholder(in: context) : currentEntry())
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<FleetEntry>) -> Void) {
        // `.never`: with nothing to read, a refresh schedule would spend the widget's daily reload
        // budget re-rendering an identical view. When real data lands the app should push updates
        // through WidgetCenter on change, which is both fresher and cheaper than polling here.
        completion(Timeline(entries: [currentEntry()], policy: .never))
    }

    private func currentEntry() -> FleetEntry {
        FleetEntry(date: Date(), running: 0, needsYou: 0, isLive: false)
    }
}

struct FleetWidgetView: View {
    @Environment(\.widgetFamily) private var family
    var entry: FleetEntry

    private let gold = Color(hex: 0xD9A520)

    var body: some View {
        Group {
            if entry.isLive {
                family == .systemMedium ? AnyView(liveMedium) : AnyView(liveSmall)
            } else {
                unavailable
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
        .widgetContainerBackground(Color.black)
    }

    private var header: some View {
        HStack(spacing: 5) {
            Image(systemName: "bolt.horizontal.circle.fill").font(.caption2).foregroundStyle(gold)
            Text("IRON RAIN").font(.system(size: 10, weight: .semibold)).tracking(0.8)
                .foregroundStyle(.secondary)
        }
    }

    private var liveSmall: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Spacer(minLength: 6)
            Text("\(entry.running)").font(.system(size: 44, weight: .semibold, design: .rounded))
                .foregroundStyle(.white).minimumScaleFactor(0.6).lineLimit(1)
            Text(entry.running == 1 ? "agent running" : "agents running")
                .font(.caption).foregroundStyle(.secondary)
            Spacer(minLength: 6)
            needsYouLine
        }
    }

    private var liveMedium: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Spacer(minLength: 8)
            HStack(alignment: .firstTextBaseline, spacing: 28) {
                stat(entry.running, entry.running == 1 ? "agent running" : "agents running", tint: .white)
                // The blocked count is the one that costs the user something, so it gets the accent
                // even at zero — a glance should land on it without reading the labels.
                stat(entry.needsYou, entry.needsYou == 1 ? "needs you" : "need you",
                     tint: entry.needsYou > 0 ? gold : .white)
            }
            Spacer(minLength: 8)
            Text(entry.needsYou > 0 ? "Open Iron Rain to answer." : "Nothing is waiting on you.")
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    private func stat(_ value: Int, _ label: String, tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("\(value)").font(.system(size: 40, weight: .semibold, design: .rounded))
                .foregroundStyle(tint).minimumScaleFactor(0.6).lineLimit(1)
            Text(label).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    @ViewBuilder private var needsYouLine: some View {
        if entry.needsYou > 0 {
            HStack(spacing: 4) {
                Image(systemName: "bell.badge.fill").font(.caption2)
                Text(entry.needsYou == 1 ? "1 needs you" : "\(entry.needsYou) need you")
                    .font(.caption.weight(.medium)).lineLimit(1)
            }
            .foregroundStyle(gold)
        } else {
            Text("Nothing waiting").font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    /// What the widget shows today. It names the reason and the fix instead of drawing an empty
    /// dashboard, so it reads as a state the app is in rather than a widget that is broken.
    private var unavailable: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Spacer(minLength: 8)
            Image(systemName: "bolt.horizontal.circle").font(.title).foregroundStyle(gold)
                .padding(.bottom, 6)
            Text("Open Iron Rain").font(.headline).foregroundStyle(.white).lineLimit(1)
                .minimumScaleFactor(0.8)
            Text("Your agents' status lives in the app.")
                .font(.caption).foregroundStyle(.secondary)
                .lineLimit(2).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
    }
}

private extension View {
    /// iOS 17 moved widget backgrounds behind `containerBackground`, and a widget that keeps
    /// painting its own is letterboxed in the removable-background environments (StandBy, the iPad
    /// Lock Screen, macOS). The API does not exist at the iOS 16 floor, so both paths are kept —
    /// the older one has to add the padding the container would otherwise supply.
    @ViewBuilder func widgetContainerBackground<B: View>(_ background: B) -> some View {
        if #available(iOS 17.0, *) {
            containerBackground(for: .widget) { background }
        } else {
            padding(16).background(background)
        }
    }
}

/// `StaticConfiguration`, not `AppIntentConfiguration`: the latter is iOS 17+, and there is nothing
/// to configure on this widget anyway. Same for interactive buttons — approving from the widget
/// itself needs iOS 17, and it would need the App Group above before it had anything to act on.
struct OculusFleetWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: "com.howlerops.oculus.fleet", provider: FleetProvider()) { entry in
            FleetWidgetView(entry: entry)
        }
        .configurationDisplayName("Agents")
        .description("How many agents are running, and how many need you.")
        .supportedFamilies([.systemSmall, .systemMedium])
    }
}

@main
struct OculusWidgetsBundle: WidgetBundle {
    var body: some Widget {
        OculusFleetWidget()
        if #available(iOS 16.1, *) {
            OculusLiveActivity()
        }
    }
}
