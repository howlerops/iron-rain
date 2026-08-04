#if os(macOS)
import Foundation

/// Starts the Iron Rain daemon (oculusd) for you on macOS, so there's no terminal:
/// if no daemon is already listening and `oculusd` is installed (curl installer →
/// ~/.local/bin, or Homebrew/PATH), it spawns `oculusd serve`. The app then auto-connects
/// via ~/.oculus/pairing.json.
@MainActor
public final class DaemonLauncher: ObservableObject {
    /// Why the daemon isn't up — drives the actionable guidance on the first screen.
    public enum Trouble: Equatable {
        case notInstalled   // no oculusd binary found → show the install command
        case startFailed(String) // spawning it errored (e.g. a sandboxed app can't exec it)
        case notResponding  // spawned/looked for it but nothing is listening on 6000
    }

    @Published public private(set) var status = "checking…"
    @Published public private(set) var managed = false // true if WE started the daemon
    @Published public private(set) var running = false // a daemon is reachable on :6000
    @Published public private(set) var trouble: Trouble? // set when not running (nil while healthy)
    private var process: Process?
    private var lastSpawnError = "" // captured daemon stderr (e.g. "bind: address already in use")

    /// The one-liner that installs the daemon (shown + copyable when it isn't found).
    public static let installCommand = "curl -fsSL https://howlerops.github.io/iron-rain/install.sh | sh"
    /// How to start it by hand if the app can't (e.g. a sandboxed build can't spawn subprocesses).
    public static let manualCommand = "oculusd serve"

    /// The one launcher for the process.
    ///
    /// This has to be shared rather than per-view. `managed` — whether THIS app started the daemon
    /// child — is the state that decides whether handing off to a launchd agent must stop that child
    /// first. A second instance reports `managed == false` and skips the stop, so launchd loads the
    /// agent, the agent's daemon then can't bind its port, and the user is left with a switch that
    /// reads "on" having done nothing. That failure is silent, which is what makes it dangerous.
    public static let shared = DaemonLauncher()

    public init() {}

    /// Ensures a local daemon is running, waiting briefly for it to come up. Safe to call at
    /// launch AND to re-invoke as a "retry" — it re-checks the port first, so it's idempotent.
    /// Returns whether a daemon is reachable afterwards.
    @discardableResult
    public func ensureRunning() async -> Bool {
        // Probe /healthz, not a bare TCP connect — so a stale/foreign process squatting :6000 that
        // ISN'T an Iron Rain daemon doesn't read as "running" (which would leave the app unable to
        // connect with no explanation).
        if await daemonHealthy() {
            markRunning("daemon running")
            return true
        }
        let portHeldByOther = isPortOpen(6000) // something is on :6000 but isn't answering /healthz
        guard let bin = findOculusd() else {
            markTrouble(.notInstalled, "Daemon not found. Install it, then retry.")
            return false
        }
        lastSpawnError = ""
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        // --addr 0.0.0.0: bind all interfaces so the pairing QR carries the Mac's LAN IP (reachable
        // from the phone on the same network) instead of ws://127.0.0.1, which the phone can't dial.
        // No --secret: the daemon loads/persists a STABLE secret (~/.oculus/secret) so it survives
        // restarts/reinstalls and already-paired clients stay authorized (a fresh random secret each
        // launch rotated it and broke the phone pairing).
        p.arguments = ["serve", "--addr", "0.0.0.0:6000"]
        // CRITICAL: send the daemon's stdout/stderr to a LOG FILE, not a pipe held by the app. A pipe
        // dies with the app — when you quit Iron Rain, the pipe's read end closes and the daemon's next
        // log write gets SIGPIPE and the daemon is KILLED (the "closing the app stops the daemon" bug).
        // A file is the daemon's own fd, so it survives app quit; the daemon keeps running and the app
        // reconnects to it on next launch. We still read the file's tail to detect an instant-exit error.
        let logURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".oculus/oculusd.log")
        try? FileManager.default.createDirectory(at: logURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        if !FileManager.default.fileExists(atPath: logURL.path) {
            FileManager.default.createFile(atPath: logURL.path, contents: nil)
        }
        let logHandle = try? FileHandle(forWritingTo: logURL)
        try? logHandle?.seekToEnd()
        p.standardOutput = logHandle ?? FileHandle.nullDevice
        p.standardError = logHandle ?? FileHandle.nullDevice
        p.terminationHandler = { [weak self] _ in
            let err = Self.tail(of: logURL, maxBytes: 2000).trimmingCharacters(in: .whitespacesAndNewlines)
            Task { @MainActor in
                self?.managed = false
                if !err.isEmpty { self?.lastSpawnError = err }
                if self?.running == true { self?.markTrouble(.notResponding, "Daemon stopped.") }
            }
        }
        do {
            try p.run()
            process = p
            managed = true
        } catch {
            // A hardened/sandboxed app can't exec an external binary — surface the manual path.
            markTrouble(.startFailed(error.localizedDescription),
                        "Couldn't start the daemon automatically: \(error.localizedDescription)")
            return false
        }
        // Wait up to ~4s for it to answer /healthz (and write pairing.json).
        for _ in 0..<20 {
            if await daemonHealthy() { markRunning("daemon started"); return true }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        // Didn't come up. A port conflict is the common, confusing case — name it explicitly.
        let detail = lastSpawnError.isEmpty ? "" : "\n\n\(lastSpawnError.suffix(240))"
        if portHeldByOther || lastSpawnError.lowercased().contains("address already in use") {
            markTrouble(.startFailed("port 6000 in use"),
                        "Port 6000 is already in use by another process, so the daemon couldn't start. Quit whatever is using it (Activity Monitor), then retry.\(detail)")
        } else {
            markTrouble(.notResponding, "Daemon didn't come up. Retry, or start it in a terminal.\(detail)")
        }
        return false
    }

    /// True only if an Iron Rain daemon answers /healthz on :6000 (distinguishes our daemon from a
    /// foreign process that merely holds the port).
    private func daemonHealthy() async -> Bool {
        guard let url = URL(string: "http://127.0.0.1:6000/healthz") else { return false }
        var req = URLRequest(url: url)
        req.timeoutInterval = 1
        guard let (data, resp) = try? await URLSession.shared.data(for: req),
              (resp as? HTTPURLResponse)?.statusCode == 200 else { return false }
        return String(data: data, encoding: .utf8)?.contains("ok") == true
    }

    private func markRunning(_ msg: String) {
        running = true
        trouble = nil
        status = msg
    }

    private func markTrouble(_ t: Trouble, _ msg: String) {
        running = false
        trouble = t
        status = msg
    }

    public func stop() {
        process?.terminate()
        process = nil
        managed = false
        running = false
        status = "stopped"
    }

    /// Reads the last `maxBytes` of a file as UTF-8 (for surfacing a daemon's instant-exit error from
    /// its log without loading the whole file).
    private nonisolated static func tail(of url: URL, maxBytes: Int) -> String {
        guard let h = try? FileHandle(forReadingFrom: url) else { return "" }
        defer { try? h.close() }
        let end = (try? h.seekToEnd()) ?? 0
        let start = end > UInt64(maxBytes) ? end - UInt64(maxBytes) : 0
        try? h.seek(toOffset: start)
        let data = (try? h.readToEnd()) ?? Data()
        return String(data: data, encoding: .utf8) ?? ""
    }

    private func findOculusd() -> String? {
        let home = NSHomeDirectory()
        let candidates = [
            "\(home)/.local/bin/oculusd",
            "/opt/homebrew/bin/oculusd",
            "/usr/local/bin/oculusd",
            "\(home)/go/bin/oculusd",
        ]
        for c in candidates where FileManager.default.isExecutableFile(atPath: c) { return c }
        return which("oculusd")
    }

    private func which(_ name: String) -> String? {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        p.arguments = ["which", name]
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return nil }
        p.waitUntilExit()
        let out = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return (out?.isEmpty == false) ? out : nil
    }

    private func isPortOpen(_ port: UInt16) -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        if fd < 0 { return false }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let res = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        return res == 0
    }

}
#endif
