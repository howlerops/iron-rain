import SwiftUI
import OculusKit
import UniformTypeIdentifiers
#if canImport(AppKit)
import AppKit
#endif
#if canImport(UIKit)
import UIKit
#endif

/// Provider-aware label for a ticket's external "open" action.
func openInLabel(for provider: String) -> String {
    switch provider {
    case "jira": return "Open in Jira"
    case "linear": return "Open in Linear"
    default: return "Open in browser"
    }
}

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
    /// Dismissal for the which-site prompt, so it can be waved away for this viewing without
    /// choosing. Choosing clears the underlying flag daemon-side and it stops appearing at all.
    @State private var siteBannerDismissed = false
    @State private var launching: Issue?
    @State private var selectedIssue: Issue?
    @State private var searchText = ""
    @State private var priorityFilter: Int?     // nil = any
    @State private var assigneeFilter: String?  // nil = any
    @State private var cycleFilter: String?     // nil = any; "__none__" = not in a cycle; else cycle id
    @State private var showHidden = false       // reveal hidden tickets (to unhide)
    @State private var savingView = false       // "save current filters as a view" dialog
    @State private var newViewName = ""
    @State private var creatingTicket = false    // "+ New ticket" sheet
    @State private var dropTargetColumn: String? // column id currently under a dragged card
    /// The tracker a Disconnect button is waiting on. Disconnecting drops the daemon's stored
    /// credentials for it — every board on that tracker disappears until you re-authorise — and
    /// three separate buttons did it with no confirmation at all.
    @State private var pendingDisconnect: String?
    @Environment(\.openURL) private var openURL
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor

    public init(model: Model, palette: OculusPalette, embedded: Bool = false, onLaunched: @escaping () -> Void = {}) {
        self.model = model; self.palette = palette; self.embedded = embedded; self.onLaunched = onLaunched
    }

    private let columns: [(name: String, category: String)] = [
        ("To Do", "todo"), ("In Progress", "in_progress"), ("Done", "done"),
    ]

    public var body: some View {
        content
            .confirmationDialog(
                "Disconnect \(trackerDisplayName(pendingDisconnect ?? ""))?",
                isPresented: Binding(get: { pendingDisconnect != nil }, set: { if !$0 { pendingDisconnect = nil } }),
                titleVisibility: .visible,
                presenting: pendingDisconnect
            ) { p in
                Button("Disconnect", role: .destructive) {
                    pendingDisconnect = nil
                    Task { await model.disconnectTracker(p) }
                }
                Button("Cancel", role: .cancel) { pendingDisconnect = nil }
            } message: { p in
                Text("Removes the stored credentials for \(trackerDisplayName(p)). Its issues disappear from every board here until you connect it again. Your OAuth app setup is kept, so reconnecting is one tap.")
            }
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
            .task {
                await model.loadIntegrationStatus(); await model.loadIssues(); await model.loadJiraSites()
                await model.loadIssueProjects(); await model.loadBoardColumns()
            }
            // The initial .task can run before the desktop finishes connecting (client not ready),
            // leaving trackers showing "not connected" even though the daemon has them. Re-fetch the
            // moment the connection lands (e.g. right after scanning the pairing QR).
            .onChange(of: model.connected) { isConnected in
                if isConnected {
                    Task {
                        await model.loadIntegrationStatus(); await model.loadIssues(); await model.loadJiraSites()
                        await model.loadIssueProjects(); await model.loadBoardColumns()
                    }
                }
            }
            // Switching boards reloads that board's status columns.
            .onChange(of: model.selectedProjectID) { _ in
                Task { await model.loadBoardColumns() }
            }
            .sheet(isPresented: $creatingTicket) {
                NewTicketSheet(model: model, palette: palette, projectID: model.selectedProjectID)
            }
    }

    /// The right-slide inspector: a dimmed tap-to-dismiss backdrop + the panel sliding in
    /// from the trailing edge, so the board stays visible behind it.
    @ViewBuilder private func inspectorOverlay(_ issue: Issue) -> some View {
        ZStack(alignment: .trailing) {
            // A tap-to-dismiss scrim is invisible to VoiceOver as a gesture; as a button it is
            // reachable, and the Escape shortcut gives the same exit from a keyboard.
            Button { selectedIssue = nil } label: {
                Color.black.opacity(0.28).ignoresSafeArea()
            }
            .buttonStyle(.plain)
            .keyboardShortcut(.cancelAction)
            .accessibilityLabel("Close inspector")
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
                                    .accessibilityLabel("Refresh issues")
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
                if model.issues.isEmpty {
                    trackerEmptyState // connected but nothing to show — explain + let them disconnect
                } else if kanban {
                    VStack(spacing: 0) { jiraSitePrompt; board }
                } else {
                    VStack(spacing: 0) { jiraSitePrompt; table }
                }
            }
        }
    }

    /// Offers the site choice when the daemon says it GUESSED, even if the board looks fine.
    ///
    /// The switcher used to live only inside the empty state, so it appeared only when the wrong
    /// site had no issues. Pick a site that does have issues — someone else's project — and the
    /// board fills with plausible, wrong data and never offers the choice. That is why the fix was
    /// "re-choose it in the app": there was nothing to tell you, and nowhere obvious to look.
    ///
    /// Shown once, dismissible, and gone for good once a site is chosen explicitly.
    @ViewBuilder private var jiraSitePrompt: some View {
        if model.jiraSiteAmbiguous, model.jiraSites.count > 1, !siteBannerDismissed {
            HStack(spacing: 10) {
                Image(systemName: "questionmark.circle.fill")
                    .foregroundStyle(palette.warning)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Which Jira site?")
                        .font(.footnote.weight(.semibold)).foregroundStyle(palette.foreground)
                    Text("Your Atlassian login reaches \(model.jiraSites.count) sites. These issues are from the one picked for you.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 8)
                Picker("Jira site", selection: Binding(get: { model.jiraCurrentSite },
                                                      set: { id in Task { await model.setJiraSite(id) } })) {
                    ForEach(model.jiraSites) { s in
                        Text(s.name.isEmpty ? s.url : s.name).tag(s.cloudID)
                    }
                }
                .pickerStyle(.menu).labelsHidden()
                .accessibilityLabel("Choose the Jira site")
                Button { siteBannerDismissed = true } label: {
                    Image(systemName: "xmark").font(.caption)
                }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                .frame(width: 44, height: 44).contentShape(Rectangle())
                .accessibilityLabel("Dismiss")
            }
            .padding(.horizontal, OculusSpace.md).padding(.vertical, OculusSpace.sm)
            .background(palette.warning.opacity(0.10))
            .overlay(alignment: .bottom) { Divider().overlay(palette.border) }
        }
    }

    /// Site switcher for multi-site Atlassian orgs — picking the wrong site is the classic
    /// "connected but no tickets" (the daemon was routing to the unused site). No re-auth needed.
    private var jiraSitePicker: some View {
        VStack(spacing: 8) {
            Text("Your Atlassian login has \(model.jiraSites.count) Jira sites. Pick the one your tickets are in:")
                .font(.caption).foregroundStyle(palette.foreground).multilineTextAlignment(.center)
            Picker("Jira site", selection: Binding(get: { model.jiraCurrentSite }, set: { id in Task { await model.setJiraSite(id) } })) {
                ForEach(model.jiraSites) { s in
                    Text(s.name.isEmpty ? s.url : s.name).tag(s.cloudID)
                }
            }
            .pickerStyle(.menu)
            .labelsHidden()
        }
        .padding(12)
        .frame(maxWidth: 460)
        .background(palette.primary.opacity(0.08), in: OculusShape.rounded(OculusRadius.md))
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.primary.opacity(0.4)))
    }

    /// Why the board is empty, in the user's terms.
    private var emptyStateMessage: String {
        if !model.trackerAuthErrors.isEmpty {
            let names = model.trackerAuthErrors.map { $0 == "jira" ? "Jira" : $0.capitalized }
                .sorted().joined(separator: " and ")
            return "\(names) couldn't be reached, so this board is incomplete. The reason is below."
        }
        if model.jiraSites.count > 1 {
            return "Your trackers answered, but nothing came back. If you have more than one Jira site, the wrong one may be selected."
        }
        return "Your trackers answered, but no issues are assigned to you. Reconnect if you expected some, or disconnect a tracker you no longer use."
    }

    /// Shown when trackers are connected but the board is empty — distinguishes "working, but no
    /// issues assigned to you" from a real failure, and always exposes Disconnect so a broken or
    /// unwanted connection can be removed (previously unreachable once connected).
    private var trackerEmptyState: some View {
        VStack(spacing: 16) {
            Spacer()
            // The shared empty state, so "you have nothing" says WHY and offers a way forward
            // instead of a bare line of grey text.
            // The wording has to follow the actual state. "Your trackers answered" was printed
            // unconditionally, so a board that was empty BECAUSE every tracker was failing said the
            // trackers had answered — directly contradicting the error banner sitting above it and
            // the per-tracker failure below it. An empty board and a broken board are different
            // things and only one of them is the user's fault.
            SheetEmptyState(icon: model.trackerAuthErrors.isEmpty ? "tray" : "exclamationmark.triangle",
                            title: model.trackerAuthErrors.isEmpty ? "No issues to show" : "Couldn't load your issues",
                            message: emptyStateMessage,
                            palette: palette) {
                Button { Task { await model.loadIssues() } } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
            }
            if model.jiraSites.count > 1 { jiraSitePicker }
            VStack(spacing: 12) {
                ForEach(model.connectedTrackers, id: \.self) { p in
                    let dn = p == "jira" ? "Jira" : p.capitalized
                    VStack(spacing: 6) {
                        if let d = model.trackerAuthDetails[p], !d.isEmpty {
                            Text("\(dn) failed: \(d)")
                                .font(.caption.monospaced()).foregroundStyle(palette.warning)
                                .textSelection(.enabled).multilineTextAlignment(.center)
                        } else {
                            Text("\(dn) is connected and responded, but no issues are assigned to you.")
                                .font(.caption).foregroundStyle(palette.mutedForeground)
                                .multilineTextAlignment(.center)
                        }
                        HStack(spacing: 8) {
                            Button { Task { await model.startOAuth(provider: p) } } label: { Text("Reconnect") }
                                .buttonStyle(.bordered)
                            Button(role: .destructive) { pendingDisconnect = p } label: { Text("Disconnect") }
                                .buttonStyle(.bordered)
                        }
                    }
                    .padding(12)
                    .frame(maxWidth: 460)
                    .background(palette.card, in: OculusShape.rounded(OculusRadius.md))
                    .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border))
                }
            }
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }

    /// Shown when a tracker's fetch/refresh is failing, so you can see WHY and reconnect or drop it.
    private var authErrorBanner: some View {
        ForEach(model.trackerAuthErrors, id: \.self) { provider in
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(palette.warning)
                        .accessibilityHidden(true)
                    Text("\(provider == "jira" ? "Jira" : provider.capitalized) isn’t loading issues.")
                        .font(.callout.weight(.medium)).foregroundStyle(palette.foreground)
                    Spacer()
                    Button { Task { await model.startOAuth(provider: provider) } } label: {
                        Text("Reconnect").font(.callout.weight(.semibold))
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    Button(role: .destructive) { pendingDisconnect = provider } label: {
                        Text("Disconnect").font(.callout)
                    }
                    .buttonStyle(.bordered)
                }
                // The ACTUAL failure the daemon recorded (expired token, bad cloud id, 401, etc.) —
                // so you can diagnose it instead of guessing.
                if let detail = model.trackerAuthDetails[provider], !detail.isEmpty {
                    Text(detail)
                        .font(.caption.monospaced()).foregroundStyle(palette.mutedForeground)
                        .textSelection(.enabled).lineLimit(4).fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            .background(palette.warning.opacity(0.12))
            .overlay(Rectangle().frame(height: 1).foregroundStyle(palette.warning.opacity(0.3)), alignment: .bottom)
        }
    }

    // MARK: search + filter

    /// Issues after the active search text + priority/assignee/cycle filters.
    private var filteredIssues: [Issue] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return model.issues.filter { i in
            // Hidden tickets are excluded everywhere unless you toggle "show hidden" to unhide them.
            if model.hiddenIssueIDs.contains(i.id) && !showHidden { return false }
            // When boards are known, scope to the selected board (degrades to no-op if unsupported).
            if !model.issueProjects.isEmpty, let proj = model.selectedProjectID, i.teamID != proj { return false }
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
                    .plainInput()
                    .submitLabel(.search)
                    .accessibilityLabel("Search issues")
                if !searchText.isEmpty {
                    Button { searchText = "" } label: {
                        Image(systemName: "xmark.circle.fill").font(.caption)
                            .frame(width: 30, height: 30).contentShape(Rectangle())
                    }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                        .accessibilityLabel("Clear search")
                }
            }
            // Vertical padding and a floor on the height. There was only leading padding, so the
            // field collapsed to the intrinsic height of its text — a sliver you have to aim at, and
            // one that jumped taller the moment the clear button appeared and shorter when it went.
            // 36pt is the platform's search-field height; the iOS floor is the 44pt touch minimum.
            .padding(.leading, 10)
            .padding(.vertical, OculusSpace.sm)
            #if os(iOS)
            .frame(minHeight: 44)
            #else
            .frame(minHeight: 28)
            #endif
            .background(palette.card).clipShape(Capsule())
            .overlay(Capsule().strokeBorder(palette.border))

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

    private func trackerDisplayName(_ p: String) -> String {
        p == "jira" ? "Jira" : p.capitalized
    }

    private func priorityLabel(_ p: Int) -> String {
        switch p { case 1: return "Urgent"; case 2: return "High"; case 3: return "Medium"; case 4: return "Low"; default: return "No priority" }
    }

    private var inlineHeader: some View {
        HStack(spacing: 10) {
            // No "Issues" label. This header exists ONLY in the embedded (macOS detail) case, which
            // is precisely the case where the enclosing split view already puts "Issues" in the
            // window chrome directly above it — so the word appeared twice, two lines apart, and the
            // second one bought nothing. The row stays for the controls it carries.
            Spacer()
            if !model.connectedTrackers.isEmpty {
                Picker("", selection: $kanban) { Text("Board").tag(true); Text("List").tag(false) }
                    .pickerStyle(.segmented).labelsHidden().fixedSize()
                Button { Task { await model.loadIssues() } } label: {
                    Image(systemName: "arrow.clockwise")
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                    .help("Refresh issues")
                    .accessibilityLabel("Refresh issues")
            }
        }
        .padding(.leading, 14).padding(.trailing, 4)
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
                // Deliberately NOT Dynamic Type: this is one composed illustration, not text — the
                // three glyphs are positioned by fixed offsets inside fixed-diameter circles, and
                // scaling only the glyphs would slide them out of their halo.
                Image(systemName: "checklist")
                    .font(.system(size: 38, weight: .semibold))
                    .foregroundStyle(palette.primaryText)
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
                .fill(palette.warning.opacity(0.85))
                .frame(width: 3)
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.callout)
                    .foregroundStyle(palette.warning)
                    .accessibilityHidden(true)
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
        .background(palette.warning.opacity(0.08))
        .clipShape(OculusShape.rounded(OculusRadius.md))
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.warning.opacity(0.22)))
        .padding(.horizontal, 20)
    }

    // MARK: kanban

    /// A rendered board column, resolved from real workflow states (falling back to the fixed
    /// category buckets when the daemon hasn't supplied per-project columns yet).
    private struct BoardColumn: Identifiable, Hashable {
        var id: String       // status id (real) or category (fallback) — the moveIssue target
        var name: String
        var category: String
        var real: Bool       // true when built from model.boardColumns
    }

    private var boardColumnDefs: [BoardColumn] {
        let cols = model.visibleColumns()
        if cols.isEmpty {
            return columns.map { BoardColumn(id: $0.category, name: $0.name, category: $0.category, real: false) }
        }
        return cols.map { BoardColumn(id: $0.id, name: $0.name, category: $0.category, real: true) }
    }

    private var board: some View {
        let defs = boardColumnDefs
        let realColumns = defs.first?.real ?? false
        // Group once per render instead of re-filtering per column; indexing the grouping is O(1).
        let grouped: [String: [Issue]] = realColumns
            ? Dictionary(grouping: filteredIssues, by: { $0.status.lowercased() })
            : Dictionary(grouping: filteredIssues, by: { $0.category })
        return VStack(spacing: 0) {
            boardToolbar
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(alignment: .top, spacing: 14) {
                    ForEach(defs) { col in
                        let colIssues = realColumns ? (grouped[col.name.lowercased()] ?? []) : (grouped[col.category] ?? [])
                        boardColumn(col, issues: colIssues)
                    }
                }
                .padding(14)
                .frame(maxHeight: .infinity, alignment: .top)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    /// Board header: the project/board picker, a Columns menu (hide/reorder/reveal), and New ticket.
    private var boardToolbar: some View {
        HStack(spacing: 10) {
            if !model.issueProjects.isEmpty {
                Menu {
                    ForEach(model.issueProjects) { p in
                        Button { model.selectProject(p.id) } label: {
                            let hint = p.provider == "jira" ? "Jira" : p.provider.capitalized
                            if p.id == model.selectedProjectID {
                                Label("\(p.name) · \(hint)", systemImage: "checkmark")
                            } else {
                                Text("\(p.name) · \(hint)")
                            }
                        }
                    }
                } label: {
                    Label(selectedProjectName, systemImage: "square.stack.3d.up.fill").font(.callout)
                }
                .menuStyle(.borderlessButton).fixedSize()
                .foregroundStyle(palette.foreground)
            }

            if !model.boardColumns.isEmpty { columnsMenu }

            Spacer()

            if model.selectedProjectID != nil {
                Button { creatingTicket = true } label: {
                    Label("New ticket", systemImage: "plus").font(.callout.weight(.medium))
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
            }
        }
        .padding(.horizontal, 14).padding(.top, 4).padding(.bottom, 8)
    }

    private var selectedProjectName: String {
        model.issueProjects.first(where: { $0.id == model.selectedProjectID })?.name ?? "Board"
    }

    /// Per-board column management: reorder, hide, and reveal hidden columns.
    private var columnsMenu: some View {
        Menu {
            ForEach(model.visibleColumns()) { c in
                Menu(c.name) {
                    Button { model.moveBoardColumn(c.id, left: true) } label: { Label("Move left", systemImage: "arrow.left") }
                    Button { model.moveBoardColumn(c.id, left: false) } label: { Label("Move right", systemImage: "arrow.right") }
                    Divider()
                    Button { model.hideBoardColumn(c.id) } label: { Label("Hide column", systemImage: "eye.slash") }
                }
            }
            let hidden = model.hiddenColumns()
            if !hidden.isEmpty {
                Divider()
                Menu("Hidden columns") {
                    ForEach(hidden) { c in
                        Button { model.showBoardColumn(c.id) } label: { Label(c.name, systemImage: "eye") }
                    }
                }
            }
        } label: {
            Label("Columns", systemImage: "rectangle.split.3x1").font(.callout)
        }
        .menuStyle(.borderlessButton).fixedSize()
        .foregroundStyle(palette.mutedForeground)
    }

    @ViewBuilder private func boardColumn(_ col: BoardColumn, issues colIssues: [Issue]) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(col.name).font(.subheadline.bold())
                Text("\(colIssues.count)").font(.caption)
                    .foregroundStyle(palette.mutedForeground)
                Spacer()
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
        .background(dropTargetColumn == col.id ? palette.primary.opacity(0.14) : palette.card.opacity(0.5))
        .clipShape(OculusShape.rounded(OculusRadius.lg))
        .overlay(
            OculusShape.rounded(OculusRadius.lg)
                .strokeBorder(dropTargetColumn == col.id ? palette.primary : Color.clear, lineWidth: 1.5)
        )
        .modifier(ColumnDropModifier(columnID: col.id, dropTarget: $dropTargetColumn,
                                     onDropID: { id in Task { await model.moveIssue(id, toStatus: col.id) } }))
    }

    private func card(_ issue: Issue) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Text(issue.key).font(.caption2.bold()).foregroundStyle(palette.primaryText)
                Spacer()
                if let p = issue.priority, p > 0 { priorityDot(p) }
            }
            Text(issue.title).font(.callout).lineLimit(3)
            if let cycle = issue.cycleLabel {
                cycleChip(cycle, systemImage: "arrow.triangle.2.circlepath")
            }
            if let sprint = issue.sprintName, !sprint.isEmpty {
                cycleChip(sprint, systemImage: "flag.checkered")
            }
            HStack(spacing: 6) {
                Text(issue.status).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1)
                if let a = issue.assignee, !a.isEmpty { assigneeChip(a) }
                Spacer(minLength: 4)
                Button { launching = issue } label: {
                    Label("Start agent", systemImage: "play.circle.fill").font(.caption2)
                        .frame(minHeight: 32).contentShape(Rectangle())
                }
                .buttonStyle(.plain).foregroundStyle(palette.primaryText)
                .accessibilityLabel("Start an agent on \(issue.key)")
            }
        }
        .padding(12)
        .background(palette.card)
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(selectedIssue?.id == issue.id ? palette.primary : palette.border))
        .clipShape(OculusShape.rounded(OculusRadius.md))
        .contentShape(Rectangle())
        .opacity(model.hiddenIssueIDs.contains(issue.id) ? 0.5 : 1) // dim while revealed via "show hidden"
        // Opening a ticket was a bare tap gesture, so the whole card came through VoiceOver as a
        // pile of static text with no way to act on it. `.accessibilityAction` rather than wrapping
        // the card in a Button: the card already contains its own Start-agent button, and nesting
        // buttons breaks the drag-to-column gesture.
        .onTapGesture { selectedIssue = issue }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
        .accessibilityHint("Open ticket details")
        .accessibilityAction { selectedIssue = issue }
        .contextMenu { issueRowMenu(issue) }
        .modifier(CardDragModifier(id: issue.id))
    }

    /// The compact cycle/sprint pill used on cards.
    private func cycleChip(_ text: String, systemImage: String) -> some View {
        Label(text, systemImage: systemImage).font(.caption2)
            .foregroundStyle(palette.mutedForeground)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background(Capsule().fill(palette.muted.opacity(0.4)))
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
            Button { openURL(u) } label: { Label(openInLabel(for: issue.provider), systemImage: "arrow.up.right.square") }
        }
    }

    /// Priority was red/orange/grey and NOTHING else, so P1 and P3 differed only by hue — invisible
    /// to a red-green colour blindness and to Differentiate Without Color alike. The glyph differs per
    /// level, and the level is always spoken.
    private func priorityDot(_ p: Int) -> some View {
        let color: Color = p == 1 ? palette.destructive : (p == 2 ? palette.warning : palette.mutedForeground)
        return Group {
            if differentiateWithoutColor {
                Text(p <= 4 ? "P\(p)" : "—")
                    .font(.caption2.weight(.bold).monospacedDigit())
                    .foregroundStyle(color)
            } else {
                Image(systemName: prioritySymbol(p))
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(color)
            }
        }
        .accessibilityLabel("Priority: \(priorityLabel(p))")
    }

    private func prioritySymbol(_ p: Int) -> String {
        switch p {
        case 1: return "exclamationmark.2"
        case 2: return "exclamationmark"
        case 3: return "equal"
        default: return "minus"
        }
    }

    /// The assignee's initials in a small tinted circle (with the full name on hover) — compact
    /// enough for a card, clear enough to scan who owns what.
    private func assigneeChip(_ name: String) -> some View {
        let initials = name.split(separator: " ").prefix(2).compactMap { $0.first }.map(String.init).joined().uppercased()
        return Text(initials.isEmpty ? "?" : initials)
            .font(.caption2.weight(.bold))
            .foregroundStyle(palette.primaryText)
            .frame(minWidth: 18, minHeight: 18)
            .padding(.horizontal, 2)
            .background(Capsule().fill(palette.primary.opacity(0.16)))
            .help(name)
            .accessibilityLabel("Assigned to \(name)")
    }

    // MARK: table

    private var table: some View {
        List(filteredIssues) { issue in
            HStack(spacing: 10) {
                Text(issue.key).font(.caption.bold()).foregroundStyle(palette.primaryText).frame(width: 72, alignment: .leading)
                VStack(alignment: .leading, spacing: 1) {
                    // The title is how you pick the ticket; it takes a second line before truncating.
                    Text(issue.title).lineLimit(2)
                    HStack(spacing: 6) {
                        Text(issue.status).font(.caption2).foregroundStyle(palette.mutedForeground)
                        if let cycle = issue.cycleLabel {
                            Text("· \(cycle)").font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                    }
                }
                Spacer()
                Button { launching = issue } label: {
                    Image(systemName: "play.circle.fill")
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                    .buttonStyle(.plain).foregroundStyle(palette.primaryText)
                    .help("Start an agent on this ticket")
                    .accessibilityLabel("Start an agent on \(issue.key)")
            }
            .opacity(model.hiddenIssueIDs.contains(issue.id) ? 0.5 : 1)
            .frame(minHeight: 44)
            .contentShape(Rectangle())
            .onTapGesture { selectedIssue = issue }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isButton)
            .accessibilityHint("Open ticket details")
            .accessibilityAction { selectedIssue = issue }
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
                    AgentPicker(model: model, selection: $agent, palette: palette)
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
            .task {
                await model.loadProjects()
                if !model.providers.isEmpty, !model.providers.contains(agent) { agent = model.providers.first ?? agent }
            }
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
    /// Disconnect drops the daemon's stored credentials for this tracker; it had no confirmation.
    @State private var confirmDisconnect = false

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
                    OculusShape.rounded(OculusRadius.md)
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
                        // Sized to match the 22pt bundled provider logo it stands in for; a scaling
                        // symbol next to a fixed-size image would make the two branches disagree.
                        Image(systemName: systemImage)
                            .font(.system(size: 20, weight: .semibold))
                            .foregroundStyle(palette.primaryText)
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
                        .foregroundStyle(palette.success)
                        .padding(.horizontal, 8).padding(.vertical, 4)
                        .background(palette.success.opacity(0.12), in: Capsule())
                    Button(role: .destructive) { confirmDisconnect = true } label: {
                        Text("Disconnect").font(.caption.weight(.medium))
                    }
                    .buttonStyle(.bordered)
                    .help("Remove this connection (keeps your OAuth app so you can reconnect in one tap).")
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
                        .background(palette.secondary.opacity(0.5), in: OculusShape.rounded(OculusRadius.sm))

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
        .clipShape(OculusShape.rounded(OculusRadius.lg))
        .overlay(OculusShape.rounded(OculusRadius.lg).strokeBorder(palette.border))
        .confirmationDialog("Disconnect \(displayName)?", isPresented: $confirmDisconnect,
                            titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) {
                Task { await model.disconnectTracker(provider) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Removes the stored credentials for \(displayName). Its issues disappear from every board here until you connect it again. Your OAuth app setup is kept, so reconnecting is one tap.")
        }
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
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help("Cancel OAuth setup")
                .accessibilityLabel("Cancel OAuth setup")
            }

            let steps = oauthSteps(for: provider)
            if steps.isEmpty {
                HStack(alignment: .top, spacing: 6) {
                    Image(systemName: "info.circle").font(.caption2).foregroundStyle(palette.primaryText)
                    Text(setupHelp).font(.caption2).foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(10)
                .background(palette.secondary.opacity(0.5), in: OculusShape.rounded(OculusRadius.sm))
            } else {
                VStack(alignment: .leading, spacing: 9) {
                    ForEach(Array(steps.enumerated()), id: \.offset) { i, step in
                        stepRow(number: i + 1, step: step)
                    }
                }
                .padding(11)
                .background(palette.secondary.opacity(0.5), in: OculusShape.rounded(OculusRadius.sm))
            }

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

    // MARK: - OAuth setup steps (numbered, with copyable values + a tappable console link)

    struct OAuthStep {
        let text: String
        var link: String? = nil    // opens in a browser
        var copyable: String? = nil // shown monospaced with a copy button
    }

    /// Numbered setup steps per tracker. The Callback URL and scopes are copyable so they paste
    /// cleanly into the provider's console; the console itself is a tappable link.
    private func oauthSteps(for provider: String) -> [OAuthStep] {
        switch provider {
        case "jira":
            return [
                OAuthStep(text: "Open the Atlassian developer console.", link: "https://developer.atlassian.com/console/myapps/"),
                OAuthStep(text: "Create an app → “OAuth 2.0 integration”."),
                OAuthStep(text: "Under Authorization, add this Callback URL:", copyable: "http://127.0.0.1:6900/oauth/jira/callback"),
                OAuthStep(text: "Under Permissions → Jira API, add these scopes:", copyable: "read:jira-work write:jira-work read:jira-user offline_access"),
                OAuthStep(text: "Copy the app’s Client ID and Secret into the fields below."),
            ]
        case "linear":
            return [
                OAuthStep(text: "Open Linear’s API settings.", link: "https://linear.app/settings/api"),
                OAuthStep(text: "Create a new OAuth application."),
                OAuthStep(text: "Add this Callback / Redirect URL:", copyable: "http://127.0.0.1:6900/oauth/linear/callback"),
                OAuthStep(text: "Copy the Client ID and Secret into the fields below."),
            ]
        default:
            return []
        }
    }

    @ViewBuilder private func stepRow(number: Int, step: OAuthStep) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text("\(number)")
                .font(.caption2.bold().monospacedDigit())
                .foregroundStyle(palette.primaryForeground)
                .frame(width: 16, height: 16)
                .background(Circle().fill(palette.primary))
            VStack(alignment: .leading, spacing: 4) {
                if let link = step.link, let url = URL(string: link) {
                    HStack(spacing: 4) {
                        Text(step.text).font(.caption2).foregroundStyle(palette.mutedForeground)
                        Link(destination: url) {
                            HStack(spacing: 2) { Text("Open"); Image(systemName: "arrow.up.right.square") }
                                .font(.caption2.weight(.semibold)).foregroundStyle(palette.primaryText)
                        }
                    }
                } else {
                    Text(step.text).font(.caption2).foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let value = step.copyable {
                    HStack(spacing: 6) {
                        Text(value)
                            .font(.system(.caption, design: .monospaced))
                            .textSelection(.enabled)
                            .lineLimit(2).truncationMode(.middle)
                            .padding(.horizontal, 7).padding(.vertical, 4)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(palette.input, in: OculusShape.rounded(OculusRadius.sm))
                        Button { copyToClipboard(value) } label: {
                            Image(systemName: "doc.on.doc").font(.caption2)
                                .frame(width: 44, height: 44).contentShape(Rectangle())
                        }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                        .help("Copy")
                        .accessibilityLabel("Copy \(value)")
                    }
                }
            }
        }
    }

    private func copyToClipboard(_ s: String) {
        #if canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(s, forType: .string)
        #elseif canImport(UIKit)
        UIPasteboard.general.string = s
        #endif
    }
}

// MARK: - Drag & drop (card → column)

/// Makes a card draggable with the issue id as the payload. Uses the modern `.draggable` API when
/// available, falling back to an `NSItemProvider` `onDrag` on older OSes.
struct CardDragModifier: ViewModifier {
    let id: String
    func body(content: Content) -> some View {
        if #available(macOS 14.0, iOS 17.0, *) {
            content.draggable(id)
        } else {
            content.onDrag { NSItemProvider(object: id as NSString) }
        }
    }
}

/// Turns a column into a drop target for a dragged card id, highlighting while hovered and calling
/// `onDropID` with the dropped issue id. Modern `.dropDestination` with an `onDrop` fallback.
struct ColumnDropModifier: ViewModifier {
    let columnID: String
    @Binding var dropTarget: String?
    let onDropID: (String) -> Void

    func body(content: Content) -> some View {
        if #available(macOS 14.0, iOS 17.0, *) {
            content.dropDestination(for: String.self) { items, _ in
                guard let id = items.first else { return false }
                onDropID(id)
                return true
            } isTargeted: { hovering in
                if hovering { dropTarget = columnID }
                else if dropTarget == columnID { dropTarget = nil }
            }
        } else {
            content.onDrop(of: [UTType.text], isTargeted: Binding(
                get: { dropTarget == columnID },
                set: { hovering in
                    if hovering { dropTarget = columnID }
                    else if dropTarget == columnID { dropTarget = nil }
                }
            )) { providers in
                guard let provider = providers.first else { return false }
                _ = provider.loadObject(ofClass: NSString.self) { obj, _ in
                    guard let s = obj as? String else { return }
                    DispatchQueue.main.async { onDropID(s) }
                }
                return true
            }
        }
    }
}

// MARK: - New ticket

/// Sheet to create a ticket on the current board: title, description, a Jira-only Type field
/// (default "Task"), and a priority picker. Closes on success; errors surface via trackerError.
struct NewTicketSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let projectID: String?

    @Environment(\.dismiss) private var dismiss
    @State private var title = ""
    @State private var description = ""
    @State private var type = "Task"
    @State private var priority = 0   // 0 = no priority
    @State private var submitting = false

    private var isJira: Bool {
        model.issueProjects.first(where: { $0.id == projectID })?.provider == "jira"
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Ticket") {
                    TextField("Title", text: $title)
                        #if os(iOS)
                        .textInputAutocapitalization(.sentences)
                        #endif
                    TextField("Description", text: $description, axis: .vertical)
                        .lineLimit(3...8)
                }
                if isJira {
                    Section("Type") {
                        TextField("Type", text: $type)
                            #if os(iOS)
                            .textInputAutocapitalization(.words).autocorrectionDisabled()
                            #endif
                    }
                }
                Section("Priority") {
                    Picker("Priority", selection: $priority) {
                        Text("No priority").tag(0)
                        Text("Urgent").tag(1)
                        Text("High").tag(2)
                        Text("Medium").tag(3)
                        Text("Low").tag(4)
                    }
                }
            }
            .navigationTitle("New ticket")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        submit()
                    } label: {
                        if submitting { ProgressView().controlSize(.small) } else { Text("Create") }
                    }
                    .disabled(projectID == nil || submitting || title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
        }
    }

    private func submit() {
        guard let pid = projectID else { return }
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        submitting = true
        Task {
            await model.createIssue(project: pid, title: t,
                                    description: description.isEmpty ? nil : description,
                                    priority: priority == 0 ? nil : priority,
                                    type: isJira ? type : nil)
            submitting = false
            if model.trackerError == nil { dismiss() }
        }
    }
}
