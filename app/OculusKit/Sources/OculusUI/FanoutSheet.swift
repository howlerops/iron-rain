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
    @FocusState private var promptFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Label("Fan out", systemImage: "square.grid.2x2")
                    .font(.headline).foregroundStyle(palette.foreground)
                Spacer()
                Button("Cancel") { onClose() }.keyboardShortcut(.cancelAction)
            }

            Text("Race the same task across several agents, each on its own branch. Compare their approaches, then merge the one you like.")
                .font(.callout).foregroundStyle(palette.mutedForeground)

            VStack(alignment: .leading, spacing: 5) {
                Text("TASK").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $prompt)
                    .font(.body).frame(height: 90).focused($promptFocused)
                    .padding(6).background(RoundedRectangle(cornerRadius: 8).fill(palette.secondary.opacity(0.5)))
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border))
            }

            HStack(spacing: 16) {
                VStack(alignment: .leading, spacing: 5) {
                    Text("AGENT").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                    Picker("", selection: $provider) {
                        ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                    }.labelsHidden().frame(minWidth: 130)
                }
                VStack(alignment: .leading, spacing: 5) {
                    Text("REPO").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                    Picker("", selection: $projectID) {
                        Text("—").tag("")
                        ForEach(model.projects) { Text($0.name).tag($0.id) }
                    }.labelsHidden().frame(minWidth: 150)
                }
                VStack(alignment: .leading, spacing: 5) {
                    Text("AGENTS").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                    Stepper("\(count)", value: $count, in: 2...6).frame(width: 90)
                }
            }

            Toggle("Plan first (each agent proposes a plan before editing)", isOn: $plan)
            Toggle("Recommend a winner when they finish", isOn: $judge)
                .help("A fresh agent reads each attempt's summary and diffstat and suggests one to keep. Advisory — you still choose.")
                .font(.callout)

            HStack {
                Spacer()
                Button {
                    Task {
                        await model.fanout(prompt: prompt, provider: provider,
                                           projectID: projectID.isEmpty ? nil : projectID, count: count, plan: plan, judge: judge)
                        onClose()
                    }
                } label: {
                    Label("Fan out \(count) agents", systemImage: "square.grid.2x2")
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || provider.isEmpty || projectID.isEmpty)
            }
        }
        .padding(22)
        .frame(width: 560)
        .background(palette.background)
        .onAppear {
            if provider.isEmpty { provider = model.providers.first ?? "opencode" }
            if projectID.isEmpty { projectID = model.projects.first?.id ?? "" }
            promptFocused = true
        }
    }
}
