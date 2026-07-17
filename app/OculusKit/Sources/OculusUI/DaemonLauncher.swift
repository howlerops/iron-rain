#if os(macOS)
import Foundation

/// Starts the Iron Rain daemon (oculusd) for you on macOS, so there's no terminal:
/// if no daemon is already listening and `oculusd` is installed (curl installer →
/// ~/.local/bin, or Homebrew/PATH), it spawns `oculusd serve`. The app then auto-connects
/// via ~/.oculus/pairing.json.
@MainActor
public final class DaemonLauncher: ObservableObject {
    @Published public private(set) var status = "checking…"
    @Published public private(set) var managed = false // true if WE started the daemon
    private var process: Process?

    public init() {}

    /// Ensures a local daemon is running, waiting briefly for it to come up. Safe to call
    /// once at launch. Returns whether a daemon is reachable afterwards.
    @discardableResult
    public func ensureRunning() async -> Bool {
        if isPortOpen(6000) {
            status = "daemon running"
            return true
        }
        guard let bin = findOculusd() else {
            status = "install the daemon: curl -fsSL https://howlerops.github.io/oculus/install.sh | sh"
            return false
        }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        p.arguments = ["serve", "--secret", randomSecret()]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        p.terminationHandler = { [weak self] _ in
            Task { @MainActor in self?.managed = false; self?.status = "daemon stopped" }
        }
        do {
            try p.run()
            process = p
            managed = true
        } catch {
            status = "couldn't start daemon: \(error.localizedDescription)"
            return false
        }
        // Wait up to ~4s for it to listen (and write pairing.json).
        for _ in 0..<20 {
            if isPortOpen(6000) { status = "daemon started"; return true }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        status = "daemon starting…"
        return isPortOpen(6000)
    }

    public func stop() {
        process?.terminate()
        process = nil
        managed = false
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

    private func randomSecret() -> String {
        (0..<16).map { _ in String(format: "%02x", UInt8.random(in: 0...255)) }.joined()
    }
}
#endif
