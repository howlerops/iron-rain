import SwiftUI
import OculusKit

/// SSH remote worktrees: register a beefy remote box (an ssh target + a repo path) and inspect its
/// worktree — git status/diff — over SSH from the app. Reachability is probed on load. Running a full
/// agent session ON the remote builds on this transport and is the larger next step.
struct RemotesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void

    @State private var showAdd = false
    @State private var status: [String: RemoteStatus] = [:]
    @State private var loading: Set<String> = []
    @State private var runHost: RemoteHost?   // host we're composing a remote agent run for

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Label("Remote hosts", systemImage: "server.rack").font(.headline)
                Spacer()
                Button { showAdd = true } label: { Label("Add", systemImage: "plus") }
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(16)
            Divider().overlay(palette.border)

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text("Run and inspect a worktree on a remote box over SSH. Uses your ~/.ssh keys/config (BatchMode — set up key auth first).")
                        .font(.callout).foregroundStyle(palette.mutedForeground)
                    if model.remotes.isEmpty {
                        Text("No remote hosts yet.").font(.subheadline).foregroundStyle(palette.mutedForeground).padding(.top, 8)
                    } else {
                        ForEach(model.remotes) { host in hostCard(host) }
                    }
                }
                .padding(16)
            }
        }
        .frame(minWidth: 540, minHeight: 440)
        .background(palette.background)
        .task { await model.loadRemotes() }
        .sheet(isPresented: $showAdd) {
            AddRemoteSheet(model: model, palette: palette) { showAdd = false }
        }
        .sheet(item: $runHost) { host in
            RemoteRunSheet(model: model, palette: palette, host: host) { onClose(); runHost = nil }
        }
    }

    private func hostCard(_ host: RemoteHost) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Circle().fill(host.reachable == true ? palette.primary : palette.destructive).frame(width: 7, height: 7)
                Text(host.name).font(.system(size: 14, weight: .semibold)).foregroundStyle(palette.foreground)
                Text(host.sshTarget).font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                Spacer()
                Text(host.reachable == true ? "reachable" : "unreachable")
                    .font(.system(size: 10, weight: .medium)).foregroundStyle(host.reachable == true ? palette.primary : palette.destructive)
                Button { Task { await model.deleteRemote(host.id) } } label: { Image(systemName: "trash").font(.system(size: 11)) }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
            Text(host.remotePath).font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
            if let fwds = host.forwards, !fwds.isEmpty {
                Label(fwds.map { "localhost:\($0.localPort) → :\($0.remotePort)" }.joined(separator: ", "),
                      systemImage: "arrow.left.arrow.right")
                    .font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.primary)
            }
            HStack {
                Button {
                    loading.insert(host.id)
                    Task { status[host.id] = await model.remoteStatus(host.id); loading.remove(host.id) }
                } label: {
                    if loading.contains(host.id) { ProgressView().controlSize(.small) }
                    else { Label("Check worktree", systemImage: "arrow.clockwise") }
                }
                .buttonStyle(.bordered).controlSize(.small)
                Button { runHost = host } label: { Label("Run agent here", systemImage: "play.circle") }
                    .buttonStyle(.bordered).controlSize(.small)
                    .disabled(host.reachable != true)
            }
            if let st = status[host.id] {
                if let err = st.error, !err.isEmpty {
                    Text(err).font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.destructive).lineLimit(3)
                } else if st.status.isEmpty {
                    Text("Clean — no uncommitted changes.").font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                } else {
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(st.status).font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.foreground)
                    }
                    .frame(maxHeight: 80)
                }
            }
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: 10).fill(palette.secondary.opacity(0.4))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(palette.border)))
    }
}

/// Compose a remote agent run: the agent command to run on the box + the first prompt.
struct RemoteRunSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let host: RemoteHost
    var onDone: () -> Void

    @State private var command = "opencode run"
    @State private var prompt = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Run agent on \(host.name)").font(.headline)
                Spacer()
                Button("Cancel") { onDone() }.keyboardShortcut(.cancelAction)
            }
            Text("Runs `\(command)` in \(host.remotePath) over SSH and streams it back. The agent must be installed on the remote.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
            VStack(alignment: .leading, spacing: 4) {
                Text("AGENT COMMAND").font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
                TextField("opencode run / claude -p / codex exec", text: $command).textFieldStyle(.roundedBorder)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("TASK").font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $prompt).frame(height: 80)
                    .padding(6).background(RoundedRectangle(cornerRadius: 8).fill(palette.secondary.opacity(0.5)))
                    .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(palette.border))
            }
            HStack {
                Spacer()
                Button {
                    Task { await model.remoteRun(hostID: host.id, agentCommand: command, prompt: prompt); onDone() }
                } label: { Label("Run remotely", systemImage: "play.circle") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(command.trimmingCharacters(in: .whitespaces).isEmpty || prompt.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20).frame(width: 480).background(palette.background)
    }
}

struct AddRemoteSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void

    @State private var name = ""
    @State private var target = ""
    @State private var path = ""
    @State private var devPort = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Add remote host").font(.headline)
                Spacer()
                Button("Cancel", action: onClose).keyboardShortcut(.cancelAction)
            }
            field("NAME", "Build box", $name)
            field("SSH TARGET", "user@host or an ~/.ssh/config alias", $target)
            field("REMOTE PATH", "/home/you/project", $path)
            field("DEV SERVER PORT (optional)", "3000 — tunneled to localhost for Design Mode", $devPort)
            HStack {
                Spacer()
                Button {
                    var fwds: [PortForward]? = nil
                    if let p = Int(devPort.trimmingCharacters(in: .whitespaces)), p > 0 {
                        fwds = [PortForward(localPort: p, remotePort: p)]
                    }
                    Task {
                        await model.upsertRemote(RemoteHost(name: name, sshTarget: target, remotePath: path, forwards: fwds))
                        onClose()
                    }
                } label: { Text("Add host") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(name.isEmpty || target.isEmpty || path.isEmpty)
            }
        }
        .padding(20).frame(width: 460).background(palette.background)
    }

    private func field(_ label: String, _ placeholder: String, _ text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.system(size: 10, weight: .semibold)).foregroundStyle(palette.mutedForeground)
            TextField(placeholder, text: text).textFieldStyle(.roundedBorder)
        }
    }
}
