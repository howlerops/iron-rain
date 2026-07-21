import SwiftUI
import OculusKit

/// The Linear-like ticket surface: connect a tracker, see assigned issues in a kanban or
/// table, and start an agent on a ticket. A first-class top-level screen (its own stack on
/// iPhone). onLaunched switches to the Sessions tab after launching.
public struct IssuesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onLaunched: () -> Void = {}

    var embedded = false // macOS: rendered inside a NavigationSplitView detail (no own nav/toolbar)
    @State private var kanban = true
    @State private var token = ""
    @State private var jiraToken = ""
    @State private var launching: Issue?
    @State private var selectedIssue: Issue?
    @State private var searchText = ""
    @State private var priorityFilter: Int?     // nil = any
    @State private var assigneeFilter: String?  // nil = any
    @State private var cycleFilter: String?     // nil = any; "__none__" = not in a cycle; else cycle id
    @Environment(\.openURL) private var openURL

    public init(model: Model, palette: OculusPalette, embedded: Bool = false, onLaunched: @escaping () -> Void = {}) {
        self.model = model; self.palette = palette; self.embedded = embedded; self.onLaunched = onLaunched
    }

    private let columns: [(name: String, category: String)] = [
        ("To Do", "todo"), ("In Progress", "in_progress"), ("Done", "done"),
    ]

    public var body: some View {
        content
            .overlay(alignment: .trailing) {
                if let issue = selectedIssue { inspectorOverlay(issue) }
            }
            .animation(.spring(response: 0.34, dampingFraction: 0.92), value: selectedIssue?.id)
            .sheet(item: $launching) { issue in
                LaunchIssueSheet(model: model, issue: issue, palette: palette) { launched in
                    launching = nil
                    if launched { onLaunched() }
                }
            }
            .task { await model.loadIntegrationStatus(); await model.loadIssues() }
    }

    /// The right-slide inspector: a dimmed tap-to-dismiss backdrop + the panel sliding in
    /// from the trailing edge, so the board stays visible behind it.
    @ViewBuilder private func inspectorOverlay(_ issue: Issue) -> some View {
        ZStack(alignment: .trailing) {
            Color.black.opacity(0.28)
                .ignoresSafeArea()
                .onTapGesture { selectedIssue = nil }
                .transition(.opacity)
            IssueInspectorPanel(model: model, issue: issue, palette: palette,
                                onStart: { let i = issue; selectedIssue = nil; launching = i },
                                onClose: { selectedIssue = nil })
                .frame(width: inspectorWidth)
                .frame(maxHeight: .infinity)
                .transition(.move(edge: .trailing))
                #if os(macOS)
                .shadow(color: .black.opacity(0.28), radius: 22, x: -8, y: 0)
                #endif
        }
        .zIndex(20)
    }

    private var inspectorWidth: CGFloat {
        #if os(macOS)
        return 470
        #else
        return min(UIScreen.main.bounds.width * 0.94, 470)
        #endif
    }

    /// Embedded (macOS detail) uses an inline header + no NavigationStack/toolbar so it
    /// doesn't fight the enclosing NavigationSplitView's toolbar. Standalone (iOS tab)
    /// keeps its own NavigationStack + toolbar.
    @ViewBuilder private var content: some View {
        if embedded {
            VStack(spacing: 0) {
                inlineHeader
                Divider().overlay(palette.border)
                surface
            }
            .background(palette.background)
            .onChange(of: model.oauthURL) { url in
                if let url { openURL(url); model.oauthURL = nil }
            }
        } else {
            NavigationStack {
                surface
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
        }
    }

    @ViewBuilder private var surface: some View {
        if model.connectedTrackers.isEmpty && model.issues.isEmpty {
            connectScreen
        } else {
            VStack(spacing: 0) {
                filterBar
                if kanban { board } else { table }
            }
        }
    }

    // MARK: search + filter

    /// Issues after the active search text + priority/assignee/cycle filters.
    private var filteredIssues: [Issue] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return model.issues.filter { i in
            if !q.isEmpty {
                let hay = "\(i.key) \(i.title) \(i.body ?? "")".lowercased()
                if !hay.contains(q) { return false }
            }
            if let p = priorityFilter, i.priority != p { return false }
            if let a = assigneeFilter, (i.assignee ?? "") != a { return false }
            if let c = cycleFilter {
                if c == "__none__" { if (i.cycleID ?? "").isEmpty == false { return false } }
                else if i.cycleID != c { return false }
            }
            return true
        }
    }

    private var availableCycles: [(id: String, label: String)] {
        var seen = Set<String>()
        var out: [(id: String, label: String)] = []
        for i in model.issues {
            guard let id = i.cycleID, !id.isEmpty, let label = i.cycleLabel, !seen.contains(id) else { continue }
            seen.insert(id); out.append((id: id, label: label))
        }
        return out.sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
    }

    private var availableAssignees: [String] {
        Array(Set(model.issues.compactMap { $0.assignee }.filter { !$0.isEmpty })).sorted()
    }

    private var activeFilterCount: Int {
        [priorityFilter != nil, assigneeFilter != nil, cycleFilter != nil].filter { $0 }.count
    }

    private func clearFilters() { priorityFilter = nil; assigneeFilter = nil; cycleFilter = nil }

    private var filterBar: some View {
        HStack(spacing: 8) {
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").font(.caption).foregroundStyle(palette.mutedForeground)
                TextField("Search issues…", text: $searchText).textFieldStyle(.plain).font(.callout)
                    #if os(iOS)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    #endif
                if !searchText.isEmpty {
                    Button { searchText = "" } label: { Image(systemName: "xmark.circle.fill").font(.caption) }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                }
            }
            .padding(.horizontal, 10).padding(.vertical, 6)
            .background(palette.card).clipShape(Capsule())
            .overlay(Capsule().stroke(palette.border))

            Menu {
                Menu("Priority") {
                    Button { priorityFilter = nil } label: { filterRow("Any priority", priorityFilter == nil) }
                    ForEach([1, 2, 3, 4], id: \.self) { p in
                        Button { priorityFilter = p } label: { filterRow(priorityLabel(p), priorityFilter == p) }
                    }
                }
                if !availableCycles.isEmpty {
                    Menu("Cycle") {
                        Button { cycleFilter = nil } label: { filterRow("Any cycle", cycleFilter == nil) }
                        Button { cycleFilter = "__none__" } label: { filterRow("No cycle", cycleFilter == "__none__") }
                        ForEach(availableCycles, id: \.id) { c in
                            Button { cycleFilter = c.id } label: { filterRow(c.label, cycleFilter == c.id) }
                        }
                    }
                }
                if !availableAssignees.isEmpty {
                    Menu("Assignee") {
                        Button { assigneeFilter = nil } label: { filterRow("Anyone", assigneeFilter == nil) }
                        ForEach(availableAssignees, id: \.self) { a in
                            Button { assigneeFilter = a } label: { filterRow(a, assigneeFilter == a) }
                        }
                    }
                }
                if activeFilterCount > 0 { Divider(); Button("Clear filters", role: .destructive) { clearFilters() } }
            } label: {
                Label(activeFilterCount == 0 ? "Filter" : "Filter (\(activeFilterCount))",
                      systemImage: "line.3.horizontal.decrease.circle\(activeFilterCount == 0 ? "" : ".fill")")
                    .font(.callout)
            }
            .menuStyle(.borderlessButton).fixedSize()
            .foregroundStyle(activeFilterCount == 0 ? palette.mutedForeground : palette.primary)
        }
        .padding(.horizontal, 14).padding(.top, 10).padding(.bottom, 6)
    }

    private func filterRow(_ text: String, _ on: Bool) -> some View {
        Label(text, systemImage: on ? "checkmark" : "")
    }

    private func priorityLabel(_ p: Int) -> String {
        switch p { case 1: return "Urgent"; case 2: return "High"; case 3: return "Medium"; case 4: return "Low"; default: return "No priority" }
    }

    private var inlineHeader: some View {
        HStack(spacing: 10) {
            Text("Issues").font(.headline)
            Spacer()
            if !model.connectedTrackers.isEmpty {
                Picker("", selection: $kanban) { Text("Board").tag(true); Text("List").tag(false) }
                    .pickerStyle(.segmented).labelsHidden().fixedSize()
                Button { Task { await model.loadIssues() } } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
    }

    // MARK: connect

    private var connectScreen: some View {
        ScrollView {
            VStack(spacing: 22) {
                Image(systemName: "checklist").font(.system(size: 44)).foregroundStyle(palette.primary)
                Text("Connect your issue tracker").font(.title2.bold())
                Text("See your assigned issues and launch agents on them — a worktree per ticket, its PR linked back automatically.")
                    .font(.subheadline).foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center).padding(.horizontal, 28)

                // Linear
                trackerCard(name: "Linear", systemImage: "link") {
                    Button { Task { await model.startOAuth(provider: "linear") } } label: {
                        Label("Connect with Linear", systemImage: "link").frame(maxWidth: 340)
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    Text("or paste an API key (Settings → Security & access → Personal API keys)")
                        .font(.caption2).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center)
                    SecureField("Linear API key", text: $token)
                        .textFieldStyle(.roundedBorder).frame(maxWidth: 340)
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                    Button("Connect with key") {
                        let t = token; token = ""
                        Task { await model.connectTracker(provider: "linear", token: t) }
                    }
                    .buttonStyle(.bordered).tint(palette.primary).disabled(token.isEmpty)
                }

                // Jira (Atlassian OAuth 2.0 3LO)
                trackerCard(name: "Jira", systemImage: "square.stack.3d.up") {
                    Button { Task { await model.startOAuth(provider: "jira") } } label: {
                        Label("Connect with Jira", systemImage: "link").frame(maxWidth: 340)
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    Text("Opens Atlassian to authorize. (Needs the OAuth app's client_id/secret in the daemon's ~/.oculus/integrations.json.)")
                        .font(.caption2).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center)
                    Text("or paste an API token as  site|email|token")
                        .font(.caption2).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center)
                    SecureField("https://you.atlassian.net|you@co.com|token", text: $jiraToken)
                        .textFieldStyle(.roundedBorder).frame(maxWidth: 340)
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                    Button("Connect with token") {
                        let t = jiraToken; jiraToken = ""
                        Task { await model.connectTracker(provider: "jira", token: t) }
                    }
                    .buttonStyle(.bordered).tint(palette.primary).disabled(jiraToken.isEmpty)
                }
            }
            .padding(.vertical, 28)
            .frame(maxWidth: .infinity)
        }
    }

    @ViewBuilder private func trackerCard<Content: View>(name: String, systemImage: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(spacing: 10) {
            Label(name, systemImage: systemImage).font(.headline)
            content()
        }
        .padding(18)
        .frame(maxWidth: 400)
        .background(palette.secondary.opacity(0.35), in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(palette.border))
    }

    // MARK: kanban

    private var board: some View {
        // Group once per render instead of re-filtering the issues twice per
        // column (count + ForEach); indexing the grouping is O(1) per column.
        let grouped = Dictionary(grouping: filteredIssues, by: { $0.category })
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
                        .padding(.horizontal, 4)
                        // Each column scrolls its own cards, lazily — a "Done" column with
                        // hundreds of tickets must not overflow the board's height.
                        ScrollView(.vertical, showsIndicators: false) {
                            LazyVStack(alignment: .leading, spacing: 10) {
                                ForEach(colIssues) { issue in card(issue) }
                            }
                            .padding(.bottom, 8)
                        }
                    }
                    .frame(width: 280)
                    .frame(maxHeight: .infinity, alignment: .top)
                    .padding(10)
                    .background(palette.card.opacity(0.5))
                    .clipShape(RoundedRectangle(cornerRadius: 14))
                }
            }
            .padding(14)
            .frame(maxHeight: .infinity, alignment: .top)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func card(_ issue: Issue) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Text(issue.key).font(.caption2.bold()).foregroundStyle(palette.primary)
                Spacer()
                if let p = issue.priority, p > 0 { priorityDot(p) }
            }
            Text(issue.title).font(.callout).lineLimit(3)
            if let cycle = issue.cycleLabel {
                Label(cycle, systemImage: "arrow.triangle.2.circlepath").font(.caption2)
                    .foregroundStyle(palette.mutedForeground)
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(Capsule().fill(palette.muted.opacity(0.4)))
            }
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
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(selectedIssue?.id == issue.id ? palette.primary : palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .contentShape(Rectangle())
        .onTapGesture { selectedIssue = issue }
    }

    private func priorityDot(_ p: Int) -> some View {
        let color: Color = p == 1 ? .red : (p == 2 ? .orange : palette.mutedForeground)
        return Circle().fill(color).frame(width: 7, height: 7)
    }

    // MARK: table

    private var table: some View {
        List(filteredIssues) { issue in
            HStack(spacing: 10) {
                Text(issue.key).font(.caption.bold()).foregroundStyle(palette.primary).frame(width: 72, alignment: .leading)
                VStack(alignment: .leading, spacing: 1) {
                    Text(issue.title).lineLimit(1)
                    HStack(spacing: 6) {
                        Text(issue.status).font(.caption2).foregroundStyle(palette.mutedForeground)
                        if let cycle = issue.cycleLabel {
                            Text("· \(cycle)").font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                    }
                }
                Spacer()
                Button { launching = issue } label: { Image(systemName: "play.circle.fill") }
                    .buttonStyle(.plain).foregroundStyle(palette.primary)
            }
            .contentShape(Rectangle())
            .onTapGesture { selectedIssue = issue }
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
