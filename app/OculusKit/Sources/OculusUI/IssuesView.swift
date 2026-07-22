import SwiftUI
import OculusKit

/// A saved issue view — a named preset of the search + priority/assignee/cycle filters, so you can
/// jump to "My high-priority bugs" etc. Persisted on the Model.
public struct SavedIssueFilter: Codable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var search: String
    public var priority: Int?
    public var assignee: String?
    public var cycle: String?
    public init(id: String = UUID().uuidString, name: String, search: String = "", priority: Int? = nil, assignee: String? = nil, cycle: String? = nil) {
        self.id = id; self.name = name; self.search = search; self.priority = priority; self.assignee = assignee; self.cycle = cycle
    }
}

/// The Linear-like ticket surface: connect a tracker, see assigned issues in a kanban or
/// table, and start an agent on a ticket. A first-class top-level screen (its own stack on
/// iPhone). onLaunched switches to the Sessions tab after launching.
public struct IssuesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onLaunched: () -> Void = {}

    var embedded = false // macOS: rendered inside a NavigationSplitView detail (no own nav/toolbar)
    @State private var kanban = true
    @State private var launching: Issue?
    @State private var selectedIssue: Issue?
    @State private var searchText = ""
    @State private var priorityFilter: Int?     // nil = any
    @State private var assigneeFilter: String?  // nil = any
    @State private var cycleFilter: String?     // nil = any; "__none__" = not in a cycle; else cycle id
    @State private var showHidden = false       // reveal hidden tickets (to unhide)
    @State private var savingView = false       // "save current filters as a view" dialog
    @State private var newViewName = ""
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
            // The initial .task can run before the desktop finishes connecting (client not ready),
            // leaving trackers showing "not connected" even though the daemon has them. Re-fetch the
            // moment the connection lands (e.g. right after scanning the pairing QR).
            .onChange(of: model.connected) { isConnected in
                if isConnected {
                    Task { await model.loadIntegrationStatus(); await model.loadIssues() }
                }
            }
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
                if !model.trackerAuthErrors.isEmpty { authErrorBanner }
                filterBar
                if kanban { board } else { table }
            }
        }
    }

    /// A pill shown when a tracker's OAuth has expired/failed, so you can reconnect in one tap.
    private var authErrorBanner: some View {
        ForEach(model.trackerAuthErrors, id: \.self) { provider in
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                Text("\(provider == "jira" ? "Jira" : provider.capitalized) needs reconnecting — its access expired.")
                    .font(.callout).foregroundStyle(palette.foreground)
                Spacer()
                Button { Task { await model.startOAuth(provider: provider) } } label: {
                    Text("Reconnect").font(.callout.weight(.semibold))
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .background(Color.orange.opacity(0.12))
            .overlay(Rectangle().frame(height: 1).foregroundStyle(Color.orange.opacity(0.3)), alignment: .bottom)
        }
    }

    // MARK: search + filter

    /// Issues after the active search text + priority/assignee/cycle filters.
    private var filteredIssues: [Issue] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return model.issues.filter { i in
            // Hidden tickets are excluded everywhere unless you toggle "show hidden" to unhide them.
            if model.hiddenIssueIDs.contains(i.id) && !showHidden { return false }
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

    private var hiddenCount: Int { model.issues.filter { model.hiddenIssueIDs.contains($0.id) }.count }

    private func applySavedFilter(_ f: SavedIssueFilter) {
        searchText = f.search; priorityFilter = f.priority; assigneeFilter = f.assignee; cycleFilter = f.cycle
    }

    /// True when the current filters exactly match a saved view (so we can highlight it).
    private func matchesCurrent(_ f: SavedIssueFilter) -> Bool {
        f.search == searchText && f.priority == priorityFilter && f.assignee == assigneeFilter && f.cycle == cycleFilter
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
                if hiddenCount > 0 {
                    Divider()
                    Button { showHidden.toggle() } label: {
                        Label(showHidden ? "Hide hidden tickets" : "Show hidden (\(hiddenCount))",
                              systemImage: showHidden ? "eye" : "eye.slash")
                    }
                    Button("Unhide all", role: .destructive) { model.unhideAllIssues(); showHidden = false }
                }
                if activeFilterCount > 0 { Divider(); Button("Clear filters", role: .destructive) { clearFilters() } }
            } label: {
                Label(activeFilterCount == 0 ? "Filter" : "Filter (\(activeFilterCount))",
                      systemImage: "line.3.horizontal.decrease.circle\(activeFilterCount == 0 ? "" : ".fill")")
                    .font(.callout)
            }
            .menuStyle(.borderlessButton).fixedSize()
            .foregroundStyle(activeFilterCount == 0 ? palette.mutedForeground : palette.primary)

            // Saved views: named filter presets.
            Menu {
                if model.savedIssueFilters.isEmpty {
                    Text("No saved views yet")
                } else {
                    ForEach(model.savedIssueFilters) { f in
                        Button { applySavedFilter(f) } label: { filterRow(f.name, matchesCurrent(f)) }
                    }
                    Menu("Delete view") {
                        ForEach(model.savedIssueFilters) { f in
                            Button(f.name, role: .destructive) { model.deleteSavedIssueFilter(f.id) }
                        }
                    }
                }
                Divider()
                Button { newViewName = ""; savingView = true } label: { Label("Save current as view…", systemImage: "plus") }
            } label: {
                Label("Views", systemImage: "square.stack.3d.up").font(.callout)
            }
            .menuStyle(.borderlessButton).fixedSize()
            .foregroundStyle(palette.mutedForeground)
        }
        .padding(.horizontal, 14).padding(.top, 10).padding(.bottom, 6)
        .alert("Save view", isPresented: $savingView) {
            TextField("View name", text: $newViewName)
            Button("Save") {
                let name = newViewName.trimmingCharacters(in: .whitespaces)
                guard !name.isEmpty else { return }
                model.addSavedIssueFilter(SavedIssueFilter(name: name, search: searchText,
                                                           priority: priorityFilter, assignee: assigneeFilter, cycle: cycleFilter))
            }
            Button("Cancel", role: .cancel) {}
        } message: { Text("Save the current search + filters as a named view.") }
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
            VStack(spacing: 0) {
                connectHero
                    .padding(.vertical, 40)

                if let err = model.trackerError {
                    trackerErrorBanner(err)
                        .padding(.bottom, 16)
                }

                LazyVGrid(
                    columns: [GridItem(.adaptive(minimum: 300, maximum: 460), spacing: 16)],
                    spacing: 16
                ) {
                    TrackerConnectCard(
                        model: model,
                        provider: "linear",
                        displayName: "Linear",
                        systemImage: "checklist",
                        palette: palette,
                        tokenLabel: "API key",
                        tokenHelp: "Linear → Settings → Security & access → Personal API keys.",
                        setupHelp: "Create an OAuth app at linear.app/settings/api, then paste its Client ID + Secret here."
                    )
                    TrackerConnectCard(
                        model: model,
                        provider: "jira",
                        displayName: "Jira",
                        systemImage: "square.stack.3d.up",
                        palette: palette,
                        tokenLabel: "site|email|token",
                        tokenHelp: "Paste as https://you.atlassian.net|you@co.com|apitoken (id.atlassian.com → API tokens).",
                        setupHelp: "Create an OAuth 2.0 (3LO) app at developer.atlassian.com/console. Callback: http://127.0.0.1:6900/oauth/jira/callback. Scopes: read:jira-work, write:jira-work, read:jira-user, offline_access. Then paste its Client ID + Secret here."
                    )
                }
                .frame(maxWidth: 960)
                .padding(.horizontal, 20)
                .padding(.bottom, 40)
            }
            .frame(maxWidth: .infinity)
        }
        .task { await model.loadIntegrationStatus() }
    }

    /// Composed SF Symbol illustration + headline/subtitle for the empty state.
    private var connectHero: some View {
        VStack(spacing: 20) {
            ZStack {
                Circle()
                    .fill(palette.primary.opacity(0.06))
                    .frame(width: 108, height: 108)
                Circle()
                    .fill(palette.primary.opacity(0.10))
                    .frame(width: 78, height: 78)
                Image(systemName: "checklist")
                    .font(.system(size: 38, weight: .semibold))
                    .foregroundStyle(palette.primary)
                Image(systemName: "arrow.triangle.branch")
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(palette.primary.opacity(0.65))
                    .offset(x: 28, y: -22)
                Image(systemName: "play.circle.fill")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundStyle(palette.primary.opacity(0.55))
                    .offset(x: -26, y: 24)
            }
            .frame(width: 108, height: 108)

            VStack(spacing: 8) {
                Text("Connect your issue tracker")
                    .font(.title2.bold())
                    .foregroundStyle(palette.foreground)
                Text("See your assigned issues and launch agents on them —\na worktree per ticket, its PR linked back automatically.")
                    .font(.subheadline)
                    .foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center)
            }
        }
        .padding(.horizontal, 32)
    }

    /// Intentionally styled error banner with a left accent bar.
    private func trackerErrorBanner(_ err: String) -> some View {
        HStack(spacing: 0) {
            Rectangle()
                .fill(Color.orange.opacity(0.85))
                .frame(width: 3)
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.callout)
                    .foregroundStyle(.orange)
                Text(err)
                    .font(.callout)
                    .foregroundStyle(palette.foreground)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 12).padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .frame(maxWidth: 560, alignment: .leading)
        .background(Color.orange.opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(Color.orange.opacity(0.22)))
        .padding(.horizontal, 20)
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
        .opacity(model.hiddenIssueIDs.contains(issue.id) ? 0.5 : 1) // dim while revealed via "show hidden"
        .onTapGesture { selectedIssue = issue }
        .contextMenu { issueRowMenu(issue) }
    }

    /// Right-click / long-press actions on a ticket — start an agent, hide/unhide, open externally.
    @ViewBuilder private func issueRowMenu(_ issue: Issue) -> some View {
        Button { launching = issue } label: { Label("Start agent", systemImage: "play.circle") }
        if model.hiddenIssueIDs.contains(issue.id) {
            Button { model.unhideIssue(issue.id) } label: { Label("Unhide", systemImage: "eye") }
        } else {
            Button { model.hideIssue(issue.id) } label: { Label("Hide ticket", systemImage: "eye.slash") }
        }
        if let url = issue.url, let u = URL(string: url) {
            Button { openURL(u) } label: { Label("Open in \(issue.provider == "jira" ? "Jira" : "Linear")", systemImage: "arrow.up.right.square") }
        }
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
            .opacity(model.hiddenIssueIDs.contains(issue.id) ? 0.5 : 1)
            .contentShape(Rectangle())
            .onTapGesture { selectedIssue = issue }
            .contextMenu { issueRowMenu(issue) }
            #if os(iOS)
            .swipeActions(edge: .trailing) {
                if model.hiddenIssueIDs.contains(issue.id) {
                    Button { model.unhideIssue(issue.id) } label: { Label("Unhide", systemImage: "eye") }.tint(.gray)
                } else {
                    Button { model.hideIssue(issue.id) } label: { Label("Hide", systemImage: "eye.slash") }.tint(.gray)
                }
            }
            #endif
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

/// One tracker's connect card: OAuth when its OAuth app is configured, an inline "set up the OAuth
/// app" form (client_id + secret) when it isn't — so users never hand-edit integrations.json — plus
/// a token/key fallback. Fixes the old "OAuth button does nothing" (no app configured, no feedback).
struct TrackerConnectCard: View {
    @ObservedObject var model: Model
    let provider: String
    let displayName: String
    let systemImage: String
    let palette: OculusPalette
    let tokenLabel: String
    let tokenHelp: String
    let setupHelp: String

    @State private var clientID = ""
    @State private var clientSecret = ""
    @State private var tokenField = ""
    @State private var addingApp = false
    @State private var showToken = false

    private var configured: Bool { model.oauthApps.contains(provider) }
    private var connected: Bool { model.connectedTrackers.contains(provider) }

    /// The provider's official brand color, when we ship its real logo (Brand.xcassets). nil → use
    /// the generic SF Symbol badge.
    private var brandColor: Color? {
        switch provider {
        case "linear": return Color(red: 94/255.0, green: 106/255.0, blue: 210/255.0) // #5E6AD2
        case "jira":   return Color(red: 0, green: 82/255.0, blue: 204/255.0)          // #0052CC
        default:       return nil
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Card header: icon badge, tracker name, connected pill
            HStack(spacing: 12) {
                ZStack {
                    RoundedRectangle(cornerRadius: 10)
                        .fill(brandColor?.opacity(0.14) ?? palette.accent.opacity(0.7))
                        .frame(width: 44, height: 44)
                    if let brandColor {
                        // The real Linear/Jira brand mark (template SVG in Brand.xcassets), tinted
                        // in the provider's brand color.
                        Image(provider, bundle: .module)
                            .renderingMode(.template)
                            .resizable().scaledToFit()
                            .frame(width: 22, height: 22)
                            .foregroundStyle(brandColor)
                    } else {
                        Image(systemName: systemImage)
                            .font(.system(size: 20, weight: .semibold))
                            .foregroundStyle(palette.primary)
                    }
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(displayName)
                        .font(.headline)
                        .foregroundStyle(palette.foreground)
                    Text("Issue tracker")
                        .font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                if connected {
                    Label("Connected", systemImage: "checkmark.circle.fill")
                        .font(.caption.bold())
                        .foregroundStyle(.green)
                        .padding(.horizontal, 8).padding(.vertical, 4)
                        .background(Color.green.opacity(0.1), in: Capsule())
                }
            }
            .padding(16)

            Divider().overlay(palette.border)

            // Primary connect section — switches between OAuth button, setup form, or setup CTA
            VStack(alignment: .leading, spacing: 12) {
                if configured {
                    oauthButton("Connect with \(displayName)")
                } else if addingApp {
                    oauthSetupForm
                        .transition(.opacity.combined(with: .move(edge: .top)))
                } else {
                    VStack(alignment: .leading, spacing: 8) {
                        Button {
                            withAnimation(.spring(response: 0.3, dampingFraction: 0.82)) { addingApp = true }
                        } label: {
                            Label("Set up \(displayName) OAuth", systemImage: "lock.open")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(palette.primary)

                        Text("Recommended. Connect via OAuth after registering an OAuth app on \(displayName)'s developer portal.")
                            .font(.caption)
                            .foregroundStyle(palette.mutedForeground)
                    }
                    .transition(.opacity)
                }
            }
            .padding(16)

            Divider().overlay(palette.border.opacity(0.5))

            // Token fallback — visually subordinate, progressive disclosure
            VStack(alignment: .leading, spacing: 0) {
                Button {
                    withAnimation(.spring(response: 0.3, dampingFraction: 0.82)) { showToken.toggle() }
                } label: {
                    HStack(spacing: 6) {
                        Image(systemName: showToken ? "chevron.down" : "chevron.right")
                            .font(.caption2.bold())
                            .frame(width: 12)
                        Text(showToken ? "Hide token option" : "or connect with a \(tokenLabel)")
                            .font(.caption)
                    }
                    .foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain)
                .padding(16)

                if showToken {
                    VStack(alignment: .leading, spacing: 10) {
                        HStack(alignment: .top, spacing: 6) {
                            Image(systemName: "key.fill")
                                .font(.caption2)
                                .foregroundStyle(palette.mutedForeground)
                            Text(tokenHelp)
                                .font(.caption2)
                                .foregroundStyle(palette.mutedForeground)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .padding(10)
                        .background(palette.secondary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))

                        field(SecureField(tokenLabel, text: $tokenField))

                        Button {
                            let t = tokenField; tokenField = ""
                            Task { await model.connectTracker(provider: provider, token: t) }
                        } label: {
                            Text("Connect with token").frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.bordered)
                        .tint(palette.primary)
                        .disabled(tokenField.isEmpty)
                    }
                    .padding(.horizontal, 16).padding(.bottom, 16)
                    .transition(.opacity.combined(with: .move(edge: .top)))
                }
            }
        }
        .frame(maxWidth: 460, alignment: .leading)
        .background(palette.card)
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(palette.border))
    }

    /// Inline OAuth app credential form (client_id + secret) with setup instructions and a cancel affordance.
    private var oauthSetupForm: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("OAuth app credentials")
                    .font(.subheadline.bold())
                    .foregroundStyle(palette.foreground)
                Spacer()
                Button {
                    withAnimation(.spring(response: 0.3, dampingFraction: 0.82)) {
                        addingApp = false; clientID = ""; clientSecret = ""
                    }
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.callout)
                        .foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain)
            }

            HStack(alignment: .top, spacing: 6) {
                Image(systemName: "info.circle")
                    .font(.caption2)
                    .foregroundStyle(palette.primary)
                Text(setupHelp)
                    .font(.caption2)
                    .foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(10)
            .background(palette.secondary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))

            field(TextField("Client ID", text: $clientID))
            field(SecureField("Client secret", text: $clientSecret))

            Button {
                let cid = clientID, sec = clientSecret
                Task { await model.setOAuthApp(provider: provider, clientID: cid, clientSecret: sec) }
            } label: {
                Text("Save & connect").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .tint(palette.primary)
            .disabled(clientID.isEmpty || clientSecret.isEmpty)
        }
    }

    private func oauthButton(_ title: String) -> some View {
        Button { Task { await model.startOAuth(provider: provider) } } label: {
            Label(title, systemImage: "link").frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent).tint(palette.primary)
    }

    private func field<F: View>(_ f: F) -> some View {
        let styled = f.textFieldStyle(.roundedBorder)
        #if os(iOS)
        return styled.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return styled
        #endif
    }
}
