#if os(macOS)
import Foundation

/// Self-update check for the curl-installed macOS app (which doesn't get TestFlight/App Store
/// updates). It compares this build's version against the latest GitHub release and flags when a
/// newer one exists, so the sidebar can offer a one-tap re-run of the install command. No-op in
/// DEBUG — dev builds from Xcode carry the placeholder version and shouldn't nag.
@MainActor
public final class UpdateChecker: ObservableObject {
    @Published public private(set) var latestVersion: String?
    @Published public private(set) var updateAvailable = false
    public let currentVersion: String

    /// Where a manual downloader lands; also the update command (reused from DaemonLauncher).
    public static let releasesURL = URL(string: "https://github.com/howlerops/oculus/releases/latest")!
    private static let apiURL = URL(string: "https://api.github.com/repos/howlerops/oculus/releases/latest")!

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
