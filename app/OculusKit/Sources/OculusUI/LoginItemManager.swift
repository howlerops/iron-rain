#if os(macOS)
import Foundation

/// Installs/removes a launchd LaunchAgent so the Iron Rain daemon (`oculusd serve`) starts at
/// login and auto-restarts if it dies — independent of opening the app. Without this the daemon
/// only runs while the app is open (it's spawned as an app child), so a reboot leaves it down
/// until you next launch the app, and any session whose provider can't be re-attached is lost.
///
/// "Enabled" is defined by the presence of the plist at ~/Library/LaunchAgents. Enabling hands
/// the running daemon off to launchd (the app defers to whatever already answers :6000), which
/// means a brief daemon restart — persisted sessions are restored on the new daemon's startup.
@MainActor
public final class LoginItemManager: ObservableObject {
    public static let label = "com.howlerops.oculusd"

    @Published public private(set) var enabled = false
    @Published public private(set) var lastError: String?

    /// Set when the launch agent is installed but launchd cannot keep it alive.
    ///
    /// This exists because that failure was, until now, completely silent. A daemon whose binary the
    /// kernel refuses to exec (an unsigned Mach-O on Apple Silicon) dies before it can log anything,
    /// launchd relaunches it forever, and the APP papers over the whole thing by spawning its own
    /// child daemon when :6000 doesn't answer. One machine reached `runs = 90631` — ten days of
    /// continuous failure — while the app looked perfectly healthy. The background daemon is the
    /// thing that is supposed to survive reboots, so it failing is worth saying out loud.
    @Published public private(set) var agentFailure: String?

    /// Shared, for the same reason as `DaemonLauncher.shared`: the login-item toggle now appears in
    /// both the sidebar menu and the Settings window, and two instances would report `enabled`
    /// independently — so one could show "on" while the other showed "off" for the same machine.
    public static let shared = LoginItemManager()

    public init() { refresh() }

    private var plistURL: URL {
        URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/LaunchAgents/\(Self.label).plist")
    }

    /// Reflects on-disk state (the plist exists) into `enabled`.
    public func refresh() {
        enabled = FileManager.default.fileExists(atPath: plistURL.path)
        if enabled { Task { await checkAgentHealth() } } else { agentFailure = nil }
    }

    /// Asks launchd whether it is actually able to run the agent, and says so if not.
    ///
    /// launchd keeps the evidence: `runs` counts how many times it has started the job, and
    /// `last exit reason` records why the last one died. A healthy agent has a low run count and no
    /// exit reason. A crash-loop has a large one and a reason that repeats — and nothing else in the
    /// system was reading either.
    public func checkAgentHealth() async {
        guard enabled else { agentFailure = nil; return }
        let out = await launchctlOutput(["print", "gui/\(getuid())/\(Self.label)"])
        guard !out.isEmpty else {
            // Installed on disk but unknown to launchd: the plist is there and the job is not
            // loaded, which is its own (quieter) kind of broken — the agent will not start at login.
            agentFailure = "Start at login is set up, but launchd isn't running the daemon. Toggle it off and on to re-register it."
            return
        }
        let reason = value(of: "last exit reason", in: out)
        let runs = Int(value(of: "runs", in: out) ?? "") ?? 0

        // A couple of restarts is ordinary — a reboot, an update, a manual stop. A repeating exit
        // REASON with a high run count is a loop, and only that is worth interrupting someone over.
        guard let reason, !reason.isEmpty, runs >= 5 else { agentFailure = nil; return }

        if reason.contains("CODESIGNING") {
            // Name the actual remedy. This one is unfixable from inside the app — macOS refuses to
            // exec the binary at all — so a generic "something went wrong" would strand the user.
            agentFailure = "macOS is refusing to run the background daemon: its code signature is invalid "
                + "(launchd has retried \(runs) times). Reinstall the daemon, or repair it with:\n"
                + "codesign --force --sign - \"$(which oculusd)\""
        } else {
            agentFailure = "The background daemon keeps exiting (\(reason), \(runs) restarts). "
                + "The app is running its own copy meanwhile, so it won't survive a reboot."
        }
    }

    /// Pulls `key = value` out of `launchctl print` output.
    private func value(of key: String, in text: String) -> String? {
        for line in text.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.hasPrefix(key), let eq = t.firstIndex(of: "=") else { continue }
            return t[t.index(after: eq)...].trimmingCharacters(in: .whitespaces)
        }
        return nil
    }

    /// launchctl, capturing stdout. The sibling `launchctl(_:)` discards output because it only
    /// needs an exit code; this one is reading launchd's bookkeeping.
    private func launchctlOutput(_ args: [String]) async -> String {
        await withCheckedContinuation { cont in
            DispatchQueue.global().async {
                let p = Process()
                p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
                p.arguments = args
                let pipe = Pipe()
                p.standardOutput = pipe
                p.standardError = FileHandle.nullDevice
                guard (try? p.run()) != nil else { cont.resume(returning: ""); return }
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                cont.resume(returning: p.terminationStatus == 0 ? (String(data: data, encoding: .utf8) ?? "") : "")
            }
        }
    }

    /// Enable or disable auto-start. Coordinates with `launcher` so launchd and the app don't
    /// both try to bind :6000: enabling stops the app-managed child first, then loads the agent;
    /// disabling boots the agent out, then re-ensures a daemon is running so the app isn't left
    /// pointing at a dead port.
    public func setEnabled(_ on: Bool, launcher: DaemonLauncher) async {
        lastError = nil
        if on {
            guard let bin = Self.resolveOculusd() else {
                lastError = "Daemon binary not found. Install it first, then enable this."
                return
            }
            do {
                try writePlist(binary: bin)
            } catch {
                lastError = "Couldn't write the launch agent: \(error.localizedDescription)"
                return
            }
            // Free :6000 if WE own the running daemon, so launchd's instance can bind it. The
            // sessions were persisted to ~/.oculus/oculus.db and are restored on the new start.
            if launcher.managed { launcher.stop() }
            _ = await launchctl(["bootstrap", "gui/\(getuid())", plistURL.path])
            enabled = true
            // Give launchd's daemon a moment to bind, then reconnect the app to it.
            _ = await launcher.ensureRunning()
        } else {
            _ = await launchctl(["bootout", "gui/\(getuid())/\(Self.label)"])
            try? FileManager.default.removeItem(at: plistURL)
            enabled = false
            // launchd's daemon just stopped — bring one back so the app stays connected.
            _ = await launcher.ensureRunning()
        }
    }

    private func writePlist(binary: String) throws {
        let dir = plistURL.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let log = NSHomeDirectory() + "/.oculus/oculusd.log"
        // Run through a LOGIN shell (`-lc`) so the daemon inherits the user's full PATH — the app
        // gets it from the GUI session, but a bare launchd agent has only /usr/bin:/bin, and the
        // daemon resolves providers (node for the claude sidecar, opencode, codex/gemini/aider) via
        // PATH lookup. `exec` replaces the shell with oculusd, so launchd still tracks the real
        // process (KeepAlive works). --addr 0.0.0.0 so the pairing QR carries the LAN IP.
        let shell = ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh"
        let cmd = "exec \"\(binary)\" serve --addr 0.0.0.0:6000"
        let xml = """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>Label</key>
            <string>\(Self.label)</string>
            <key>ProgramArguments</key>
            <array>
                <string>\(shell)</string>
                <string>-lc</string>
                <string>\(cmd)</string>
            </array>
            <key>RunAtLoad</key>
            <true/>
            <key>KeepAlive</key>
            <true/>
            <key>ProcessType</key>
            <string>Background</string>
            <key>StandardOutPath</key>
            <string>\(log)</string>
            <key>StandardErrorPath</key>
            <string>\(log)</string>
        </dict>
        </plist>
        """
        try xml.data(using: .utf8)?.write(to: plistURL, options: .atomic)
    }

    @discardableResult
    private func launchctl(_ args: [String]) async -> Bool {
        await withCheckedContinuation { cont in
            DispatchQueue.global().async {
                let p = Process()
                p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
                p.arguments = args
                p.standardOutput = FileHandle.nullDevice
                p.standardError = FileHandle.nullDevice
                let ok = (try? p.run()) != nil
                if ok { p.waitUntilExit() }
                cont.resume(returning: ok && p.terminationStatus == 0)
            }
        }
    }

    /// Same resolution order as DaemonLauncher.findOculusd — the curl installer drops it in
    /// ~/.local/bin; Homebrew/go paths are the other common spots.
    static func resolveOculusd() -> String? {
        let home = NSHomeDirectory()
        let candidates = [
            "\(home)/.local/bin/oculusd",
            "/opt/homebrew/bin/oculusd",
            "/usr/local/bin/oculusd",
            "\(home)/go/bin/oculusd",
        ]
        for c in candidates where FileManager.default.isExecutableFile(atPath: c) { return c }
        return nil
    }
}
#endif
