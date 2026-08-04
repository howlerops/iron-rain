import SwiftUI
import OculusKit

/// Fan-out composer: race one prompt across N agents in isolated worktrees, then compare and merge
/// the winner. The daemon spawns each variant on its own branch; when they finish you review each
/// (per-variant diff) and finish the one you like — the others are just discarded worktrees.
struct FanoutSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void

    @State private var prompt = ""
    @State private var provider = ""
    @State private var projectID = ""
    @State private var count = 3
    @State private var plan = false
    @State private var judge = false
    /// Race (every agent attempts the same task) vs. divide (each agent gets its own subtask).
    @State private var divide = false
    @State private var problem: String? = nil
    @State private var confirmDiscard = false
    @FocusState private var promptFocused: Bool

    /// In divide mode each non-empty line of the task box is one agent's subtask.
    private var subtasks: [String] {
        guard divide else { return [] }
        return prompt.split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    /// The typed task is the unsaved work — everything else has a default.
    private var dirty: Bool { !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Label("Fan out", systemImage: "square.grid.2x2")
                    .font(.headline).foregroundStyle(palette.foreground)
                Spacer()
                Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
            }

            Text("Race the same task across several agents, each on its own branch. Compare their approaches, then merge the one you like.")
                .font(.callout).foregroundStyle(palette.mutedForeground)

            VStack(alignment: .leading, spacing: 5) {
                Text("Task").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $prompt)
                    .font(.body).frame(minHeight: 90).focused($promptFocused)
                    // Concentric with the 6pt-padded container it sits in, so the inner corner
                    // doesn't flare against the outer one.
                    .padding(6)
                    .background(OculusShape.rounded(OculusRadius.sm).fill(palette.secondary.opacity(0.5)))
                    .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
            }

            HStack(spacing: 16) {
                VStack(alignment: .leading, spacing: 5) {
                    Text("Agent").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                    Picker("", selection: $provider) {
                        ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                    }
                    .labelsHidden().frame(minWidth: 130)
                    .accessibilityLabel("Agent to fan out")
                }
                VStack(alignment: .leading, spacing: 5) {
                    Text("Repo").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                    Picker("", selection: $projectID) {
                        Text("—").tag("")
                        ForEach(model.projects) { Text($0.name).tag($0.id) }
                    }
                    .labelsHidden().frame(minWidth: 150)
                    .accessibilityLabel("Repo to work in")
                }
                if !divide {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Agents").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                        Stepper("\(count)", value: $count, in: 2...6).frame(minWidth: 90)
                    }
                }
            }

            Picker("", selection: $divide) {
                Text("Race the same task").tag(false)
                Text("Split into subtasks").tag(true)
            }
            .pickerStyle(.segmented).labelsHidden()
            if divide {
                Text("One line per subtask. Each gets its own agent and its own branch, so you can review them independently.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            Toggle("Plan first (each agent proposes a plan before editing)", isOn: $plan)
            if !divide {
                Toggle("Recommend a winner when they finish", isOn: $judge)
                    .help("A fresh agent reads each attempt's summary and diffstat and suggests one to keep. Advisory — you still choose.")
                    .font(.callout)
            }

            if let problem {
                Text(problem).font(.footnote).foregroundStyle(palette.destructive)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                Spacer()
                // Enabled and validated on tap. The disabled button spanned prompt AND agent AND
                // repo, so it could say "not yet" but not which of the three was missing.
                Button { run() } label: {
                    Label(divide ? "Run \(max(subtasks.count, 1)) subtasks" : "Fan out \(count) agents",
                          systemImage: "square.grid.2x2")
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(22)
        // A fixed width is a macOS idea — this sheet is a free-floating window there. On a 393pt
        // iPhone, where the command palette also reaches it, 560pt forced the content wider than the
        // device and let the page scroll sideways.
        #if os(macOS)
        .frame(width: 560)
        #else
        .frame(maxWidth: .infinity)
        #endif
        .background(palette.background)
        .onAppear {
            if provider.isEmpty { provider = model.providers.first ?? "opencode" }
            if projectID.isEmpty { projectID = model.projects.first?.id ?? "" }
            promptFocused = true
        }
        .sheetDraftGuard(dirty)
        .confirmationDialog("Discard this task?", isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button("Discard", role: .destructive) { onClose() }
            Button("Keep editing", role: .cancel) {}
        } message: {
            Text("What you've typed won't be kept.")
        }
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onClose() }
    }

    private func run() {
        if prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            problem = divide ? "Write one subtask per line — each line becomes one agent's job."
                             : "Describe the task the agents should race."
            return
        }
        if provider.isEmpty { problem = "Pick which agent to fan out."; return }
        if projectID.isEmpty { problem = "Pick the repo the agents should work in."; return }
        problem = nil
        Task {
            let group = await model.fanout(prompt: prompt, provider: provider,
                                           projectID: projectID.isEmpty ? nil : projectID,
                                           count: count, plan: plan, judge: judge, subtasks: subtasks)
            // `fanout` returns nil on failure (and raises its own alert). Closing regardless would
            // throw away the prompt the user would have to retype to try again.
            if group != nil { onClose() }
        }
    }
}
