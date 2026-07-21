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

    /// The one-liner that installs the daemon (shown + copyable when it isn't found).
    public static let installCommand = "curl -fsSL https://howlerops.github.io/iron-rain/install.sh | sh"
    /// How to start it by hand if the app can't (e.g. a sandboxed build can't spawn subprocesses).
    public static let manualCommand = "oculusd serve"

    public init() {}

    /// Ensures a local daemon is running, waiting briefly for it to come up. Safe to call at
    /// launch AND to re-invoke as a "retry" — it re-checks the port first, so it's idempotent.
    /// Returns whether a daemon is reachable afterwards.
    @discardableResult
    public func ensureRunning() async -> Bool {
        if isPortOpen(6000) {
            markRunning("daemon running")
            return true
        }
        guard let bin = findOculusd() else {
            markTrouble(.notInstalled, "Daemon not found. Install it, then retry.")
            return false
        }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        // --addr 0.0.0.0: bind all interfaces so the pairing QR carries the Mac's LAN IP (reachable
        // from the phone on the same network) instead of ws://127.0.0.1, which the phone can't dial.
        // No --secret: the daemon loads/persists a STABLE secret (~/.oculus/secret) so it survives
        // restarts/reinstalls and already-paired clients stay authorized (a fresh random secret each
        // launch rotated it and broke the phone pairing).
        p.arguments = ["serve", "--addr", "0.0.0.0:6000"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        p.terminationHandler = { [weak self] _ in
            Task { @MainActor in
                self?.managed = false
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
        // Wait up to ~4s for it to listen (and write pairing.json).
        for _ in 0..<20 {
            if isPortOpen(6000) { markRunning("daemon started"); return true }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        markTrouble(.notResponding, "Daemon didn't come up. Retry, or start it in a terminal.")
        return isPortOpen(6000)
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
