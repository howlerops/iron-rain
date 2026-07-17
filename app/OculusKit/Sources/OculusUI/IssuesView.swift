import SwiftUI
import OculusKit

/// The Linear-like ticket surface: connect a tracker, see assigned issues in a kanban or
/// table, and start an agent on a ticket. A first-class top-level screen (its own stack on
/// iPhone). onLaunched switches to the Sessions tab after launching.
public struct IssuesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onLaunched: () -> Void = {}

    @State private var kanban = true
    @State private var token = ""
    @State private var launching: Issue?
    @Environment(\.openURL) private var openURL

    public init(model: Model, palette: OculusPalette, onLaunched: @escaping () -> Void = {}) {
        self.model = model; self.palette = palette; self.onLaunched = onLaunched
    }

    private let columns: [(name: String, category: String)] = [
        ("To Do", "todo"), ("In Progress", "in_progress"), ("Done", "done"),
    ]

    public var body: some View {
        NavigationStack {
            Group {
                if model.connectedTrackers.isEmpty && model.issues.isEmpty {
                    connectScreen
                } else if kanban {
                    board
                } else {
                    table
                }
            }
            .onChange(of: model.oauthURL) { url in
                if let url { openURL(url); model.oauthURL = nil }
            }
            .background(palette.background)
            .navigationTitle("Issues")
            .toolbar {
                if !model.connectedTrackers.isEmpty {
                    ToolbarItem(placement: .primaryAction) {
                        Picker("", selection: $kanban) { Text("Board").tag(true); Text("List").tag(false) }
                            .pickerStyle(.segmented).fixedSize()
                    }
                    ToolbarItem(placement: .cancellationAction) {
                        Button { Task { await model.loadIssues() } } label: { Image(systemName: "arrow.clockwise") }
                    }
                }
            }
        }
        .sheet(item: $launching) { issue in
            LaunchIssueSheet(model: model, issue: issue, palette: palette) { launched in
                launching = nil
                if launched { onLaunched() }
            }
        }
        .task { await model.loadIntegrationStatus(); await model.loadIssues() }
    }

    // MARK: connect

    private var connectScreen: some View {
        VStack(spacing: 16) {
            Spacer()
            Image(systemName: "checklist").font(.system(size: 48)).foregroundStyle(palette.primary)
            Text("Connect Linear").font(.title2.bold())
            Text("Paste a Linear API key (Settings → Security & access → Personal API keys) to see your assigned issues and launch agents on them.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).padding(.horizontal, 28)
            Button {
                Task { await model.startLinearOAuth() }
            } label: {
                Label("Connect with Linear", systemImage: "link").frame(maxWidth: 360)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary)

            Text("or paste a key").font(.caption2).foregroundStyle(palette.mutedForeground)
            SecureField("Linear API key", text: $token)
                .textFieldStyle(.roundedBorder).frame(maxWidth: 360)
                #if os(iOS)
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                #endif
            Button("Connect with key") {
                let t = token; token = ""
                Task { await model.connectTracker(provider: "linear", token: t) }
            }
            .buttonStyle(.bordered).tint(palette.primary)
            .disabled(token.isEmpty)
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: kanban

    private var board: some View {
        // Group once per render instead of re-filtering `model.issues` twice per
        // column (count + ForEach); indexing the grouping is O(1) per column.
        let grouped = Dictionary(grouping: model.issues, by: { $0.category })
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(alignment: .top, spacing: 14) {
                ForEach(columns, id: \.category) { col in
                    let colIssues = grouped[col.category] ?? []
                    VStack(alignment: .leading, spacing: 10) {
                        HStack {
                            Text(col.name).font(.subheadline.bold())
                            Text("\(colIssues.count)").font(.caption)
                                .foregroundStyle(palette.mutedForeground)
                        }
                        ForEach(colIssues) { issue in
                            card(issue)
                        }
                        Spacer(minLength: 0)
                    }
                    .frame(width: 260)
                    .padding(10)
                    .background(palette.card.opacity(0.5))
                    .clipShape(RoundedRectangle(cornerRadius: 14))
                }
            }
            .padding(14)
        }
    }

    private func card(_ issue: Issue) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Text(issue.key).font(.caption2.bold()).foregroundStyle(palette.primary)
                Spacer()
                if let p = issue.priority, p > 0 { priorityDot(p) }
            }
            Text(issue.title).font(.callout).lineLimit(3)
            HStack {
                Text(issue.status).font(.caption2).foregroundStyle(palette.mutedForeground)
                Spacer()
                Button { launching = issue } label: {
                    Label("Start agent", systemImage: "play.circle.fill").font(.caption2)
                }.buttonStyle(.plain).foregroundStyle(palette.primary)
            }
        }
        .padding(12)
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private func priorityDot(_ p: Int) -> some View {
        let color: Color = p == 1 ? .red : (p == 2 ? .orange : palette.mutedForeground)
        return Circle().fill(color).frame(width: 7, height: 7)
    }

    // MARK: table

    private var table: some View {
        List(model.issues) { issue in
            HStack(spacing: 10) {
                Text(issue.key).font(.caption.bold()).foregroundStyle(palette.primary).frame(width: 72, alignment: .leading)
                VStack(alignment: .leading, spacing: 1) {
                    Text(issue.title).lineLimit(1)
                    Text(issue.status).font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Button { launching = issue } label: { Image(systemName: "play.circle.fill") }
                    .buttonStyle(.plain).foregroundStyle(palette.primary)
            }
        }
    }
}

/// Sheet to pick the repo (and agent) before launching an agent on a ticket.
struct LaunchIssueSheet: View {
    @ObservedObject var model: Model
    let issue: Issue
    let palette: OculusPalette
    var onDone: (_ launched: Bool) -> Void

    @State private var projectID: String?
    @State private var agent = "opencode"
    private static let agents = ["opencode", "claude-code", "pi"]

    var body: some View {
        NavigationStack {
            Form {
                Section("Ticket") {
                    LabeledContent(issue.key, value: issue.title)
                    if let b = issue.branchName { LabeledContent("Branch", value: b).font(.caption) }
                }
                Section("Run in") {
                    Picker("Project", selection: $projectID) {
                        Text("Choose a project…").tag(String?.none)
                        ForEach(model.projects) { p in Text(p.name).tag(String?.some(p.id)) }
                    }
                    Picker("Agent", selection: $agent) {
                        ForEach(Self.agents, id: \.self) { Text($0).tag($0) }
                    }
                }
            }
            .navigationTitle("Start agent")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Start") {
                        guard let pid = projectID else { return }
                        Task { await model.launchIssue(issue, projectID: pid, agentProvider: agent) }
                        onDone(true)
                    }.disabled(projectID == nil)
                }
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { onDone(false) } }
            }
            .task { await model.loadProjects() }
        }
    }
}
