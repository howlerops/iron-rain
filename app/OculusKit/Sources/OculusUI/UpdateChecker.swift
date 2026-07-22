#if os(macOS)
import Foundation
import AppKit

enum UpdateError: Error {
    case msg(String)
    var message: String { if case let .msg(m) = self { return m }; return "Update failed." }
}

/// Self-update check for the curl-installed macOS app (which doesn't get TestFlight/App Store
/// updates). It compares this build's version against the latest GitHub release and flags when a
/// newer one exists, so the sidebar can offer a one-tap re-run of the install command. No-op in
/// DEBUG — dev builds from Xcode carry the placeholder version and shouldn't nag.
@MainActor
public final class UpdateChecker: ObservableObject {
    @Published public private(set) var latestVersion: String?
    @Published public private(set) var updateAvailable = false
    // Self-update progress (macOS in-place update, mirroring install.sh).
    @Published public private(set) var installing = false
    @Published public private(set) var installPhase = ""
    @Published public var installError: String?
    public let currentVersion: String

    /// Where a manual downloader lands; also the update command (reused from DaemonLauncher).
    public static let releasesURL = URL(string: "https://github.com/howlerops/iron-rain/releases/latest")!
    private static let apiURL = URL(string: "https://api.github.com/repos/howlerops/iron-rain/releases/latest")!
    /// Stable URL that always resolves to the newest release's macOS app zip.
    private static let appZipURL = URL(string: "https://github.com/howlerops/iron-rain/releases/latest/download/IronRain-macos.zip")!

    public init() {
        currentVersion = (Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String) ?? "0.0.0"
    }

    /// Fetches the latest published release tag and flags whether it's newer than this build.
    /// Silent on any failure (offline, rate-limited) — an update prompt is never worth an error.
    public func check() async {
        #if DEBUG
        return
        #else
        var req = URLRequest(url: Self.apiURL)
        req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        req.timeoutInterval = 8
        guard let (data, resp) = try? await URLSession.shared.data(for: req),
              (resp as? HTTPURLResponse)?.statusCode == 200,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let tag = obj["tag_name"] as? String else { return }
        let latest = Self.normalize(tag)
        latestVersion = latest
        updateAvailable = Self.isNewer(latest, than: Self.normalize(currentVersion))
        #endif
    }

    /// Downloads the latest macOS app zip and swaps this bundle in place, then relaunches — the
    /// in-app equivalent of re-running install.sh. The final swap runs in a small detached script
    /// that waits for THIS process to quit (you can't overwrite a running bundle), so the app
    /// terminates itself at the end. Only meaningful for the curl-installed app; harmless otherwise.
    public func installAndRelaunch() async {
        guard !installing else { return }
        installing = true; installError = nil
        defer { installing = false }
        do {
            installPhase = "Downloading update…"
            // Prefer the version-pinned asset URL (valid the instant the release is published with
            // its assets) over latest/download (whose pointer can lag briefly after a release).
            let assetURL: URL = latestVersion.flatMap {
                URL(string: "https://github.com/howlerops/iron-rain/releases/download/v\($0)/IronRain-macos.zip")
            } ?? Self.appZipURL
            var req = URLRequest(url: assetURL); req.timeoutInterval = 120
            let (zipURL, resp) = try await URLSession.shared.download(for: req)
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else { throw UpdateError.msg("Download failed.") }

            installPhase = "Preparing…"
            let work = FileManager.default.temporaryDirectory.appendingPathComponent("ironrain-update-\(UUID().uuidString)")
            try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
            let zipDst = work.appendingPathComponent("IronRain-macos.zip")
            try FileManager.default.moveItem(at: zipURL, to: zipDst)

            let extractDir = work.appendingPathComponent("extract")
            try run("/usr/bin/ditto", ["-x", "-k", zipDst.path, extractDir.path])
            guard let newApp = try FileManager.default.contentsOfDirectory(at: extractDir, includingPropertiesForKeys: nil)
                .first(where: { $0.pathExtension == "app" }) else { throw UpdateError.msg("Update package looked empty.") }

            let dest = Bundle.main.bundleURL.path            // where THIS app runs (e.g. /Applications/Iron Rain.app)
            // Pre-flight: the swap writes into the app's parent dir — bail early (before quitting) if
            // it isn't writable, so the user sees guidance instead of the old version relaunching.
            let parent = Bundle.main.bundleURL.deletingLastPathComponent().path
            guard FileManager.default.isWritableFile(atPath: parent) else {
                throw UpdateError.msg("Can't write to \(parent). Use the manual command below (it'll prompt for your password if needed).")
            }
            let pid = ProcessInfo.processInfo.processIdentifier

            // Detached swap: wait for us to quit, copy the new bundle alongside, atomically replace,
            // strip quarantine (like the installer), relaunch. Copy-then-swap so a failed copy never
            // destroys the installed app. Every step logs so a failure is diagnosable.
            let script = """
            #!/bin/sh
            PID="$1"; SRC="$2"; DEST="$3"
            echo "[$(date)] update: waiting for pid $PID to quit"
            n=0; while kill -0 "$PID" 2>/dev/null; do sleep 0.2; n=$((n+1)); [ $n -gt 300 ] && break; done
            sleep 0.5
            echo "[$(date)] update: copying $SRC -> $DEST.new"
            rm -rf "$DEST.new"
            if ! /usr/bin/ditto "$SRC" "$DEST.new"; then echo "update: ditto FAILED"; open "$DEST"; exit 1; fi
            echo "[$(date)] update: replacing $DEST"
            rm -rf "$DEST"
            if ! mv "$DEST.new" "$DEST"; then echo "update: mv FAILED"; open "$DEST.new"; exit 1; fi
            /usr/bin/xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
            echo "[$(date)] update: relaunching $DEST"
            open "$DEST" && echo "update: done" || echo "update: open FAILED"
            """
            let scriptURL = work.appendingPathComponent("apply-update.sh")
            try script.write(to: scriptURL, atomically: true, encoding: .utf8)

            installPhase = "Installing & relaunching…"
            // Detach via nohup (immune to SIGHUP when we exit) and pass the script + args as an ARRAY
            // — no shell string, so paths with spaces ("Iron Rain.app") can't break. Redirect the
            // helper's output to a persistent log the user can share if anything goes wrong.
            let logPath = NSHomeDirectory() + "/Library/Logs/IronRain-update.log"
            FileManager.default.createFile(atPath: logPath, contents: nil)
            let p = Process()
            p.executableURL = URL(fileURLWithPath: "/usr/bin/nohup")
            p.arguments = ["/bin/sh", scriptURL.path, String(pid), newApp.path, dest]
            if let logFH = try? FileHandle(forWritingTo: URL(fileURLWithPath: logPath)) {
                p.standardOutput = logFH
                p.standardError = logFH
            }
            try p.run() // survives our termination; waits on our PID then swaps + reopens

            try? await Task.sleep(nanoseconds: 300_000_000)
            NSApplication.shared.terminate(nil)
            // Backstop: if a stalled termination keeps us alive, exit hard so the helper can proceed.
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            exit(0)
        } catch {
            installError = (error as? UpdateError)?.message ?? error.localizedDescription
        }
    }

    private func run(_ tool: String, _ args: [String]) throws {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: tool)
        p.arguments = args
        try p.run(); p.waitUntilExit()
        if p.terminationStatus != 0 { throw UpdateError.msg("\(tool) failed (\(p.terminationStatus)).") }
    }

    /// Strips a leading "v" and any pre-release suffix ("0.2.0-rc1" → "0.2.0").
    nonisolated static func normalize(_ v: String) -> String {
        var s = v.hasPrefix("v") ? String(v.dropFirst()) : v
        if let dash = s.firstIndex(of: "-") { s = String(s[..<dash]) }
        return s
    }

    /// Numeric component compare: "0.2.10" is newer than "0.2.9".
    nonisolated static func isNewer(_ a: String, than b: String) -> Bool {
        let pa = a.split(separator: ".").map { Int($0) ?? 0 }
        let pb = b.split(separator: ".").map { Int($0) ?? 0 }
        for i in 0..<max(pa.count, pb.count) {
            let x = i < pa.count ? pa[i] : 0
            let y = i < pb.count ? pb[i] : 0
            if x != y { return x > y }
        }
        return false
    }
}
#endif
