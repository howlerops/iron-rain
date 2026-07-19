import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif

/// A paired desktop (Mac) the app can connect to. Persisted as JSON in UserDefaults.
public struct Desktop: Codable, Identifiable, Hashable {
    public var id: String // daemon public key (stable identity)
    public var name: String
    public var wsURL: String
    public var secret: String
}

/// Parsed `oculus://pair?ws=…&pub=…&secret=…&name=…` payload.
public struct PairingPayload {
    public var wsURL: String
    public var pub: String
    public var secret: String
    public var name: String?

    public init?(_ payload: String) {
        guard let comps = URLComponents(string: payload), comps.scheme == "oculus" else { return nil }
        let items = comps.queryItems ?? []
        func q(_ n: String) -> String? { items.first { $0.name == n }?.value }
        guard let ws = q("ws"), let pub = q("pub"), let secret = q("secret") else { return nil }
        self.wsURL = ws; self.pub = pub; self.secret = secret; self.name = q("name")
    }
}

/// Owns every paired desktop connection at once — the app connects to all of them
/// simultaneously and shows a unified, per-desktop-grouped view. Each desktop is a
/// managed `Model`; this store handles persistence, selection, add/rename/remove.
@MainActor
public final class DesktopStore: ObservableObject {
    @Published public private(set) var models: [Model] = []
    @Published public var selectedID: String?
    /// False until `bootstrap()` has loaded saved desktops and made its first connection
    /// attempt. The UI shows a loading screen until this flips, so the connected surface
    /// isn't preceded by a flash of the onboarding/disconnected default.
    @Published public private(set) var didBootstrap = false

    private let defaults = UserDefaults.standard
    private let key = "oculus.desktops"
    // Reused across every save/load — allocating a fresh coder per call is needless setup.
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    public init() {}

    /// The desktop whose conversation is currently shown.
    public var active: Model? {
        models.first { $0.id == selectedID } ?? models.first
    }

    public var isEmpty: Bool { models.isEmpty }

    /// Loads saved desktops (migrating a legacy single pairing and, on macOS, the local
    /// daemon's pairing.json), then connects to all of them at once.
    public func bootstrap() async {
        var desks = loadDesktops()
        if desks.isEmpty {
            let ws = defaults.string(forKey: "oculus.ws") ?? ""
            let pub = defaults.string(forKey: "oculus.pub") ?? ""
            let sec = defaults.string(forKey: "oculus.secret") ?? ""
            if !pub.isEmpty, !ws.isEmpty {
                desks.append(Desktop(id: pub, name: "My Mac", wsURL: ws, secret: sec))
            }
        }
        // Read the local daemon pairing once and reuse it (avoids re-reading pairing.json).
        let local = await localPairing()
        if let local, !desks.contains(where: { $0.id == local.desktop.id }) {
            desks.append(local.desktop)
        }
        for d in desks { ensureModel(d) }
        // macOS: refresh the reachable pairing URL on the local model (tunnels change).
        if let local, let m = models.first(where: { $0.id == local.desktop.id }) {
            m.pairingPublicURL = local.publicURL
        }
        save()
        if selectedID == nil { selectedID = models.first?.id }
        await connectAll()
        didBootstrap = true
    }

    public func connectAll() async {
        // Fan out concurrently — a single slow/unreachable desktop's handshake must not
        // block connecting to every other paired desktop. Each Model is @MainActor, so the
        // group children hop to the main actor individually.
        await withTaskGroup(of: Void.self) { group in
            for m in models where !m.connected {
                group.addTask { await m.connect() }
            }
        }
    }

    /// Adds (or re-pairs) a desktop from a scanned/entered pairing payload and connects.
    @discardableResult
    public func add(_ p: PairingPayload) -> Model {
        let name = (p.name?.isEmpty == false) ? p.name! : "Desktop"
        let m = ensureModel(Desktop(id: p.pub, name: name, wsURL: p.wsURL, secret: p.secret))
        m.name = name; m.wsURL = p.wsURL; m.secret = p.secret; m.daemonPubHex = p.pub
        selectedID = m.id
        save()
        Task { if !m.connected { await m.connect() } }
        return m
    }

    public func rename(_ id: String, to name: String) {
        models.first { $0.id == id }?.name = name
        save()
        objectWillChange.send()
    }

    public func remove(_ id: String) {
        models.first { $0.id == id }?.disconnect()
        models.removeAll { $0.id == id }
        if selectedID == id { selectedID = models.first?.id }
        save()
    }

    @discardableResult
    private func ensureModel(_ d: Desktop) -> Model {
        if let existing = models.first(where: { $0.id == d.id }) { return existing }
        let m = Model(name: d.name, wsURL: d.wsURL, daemonPubHex: d.id, secret: d.secret)
        models.append(m)
        return m
    }

    private func loadDesktops() -> [Desktop] {
        guard let data = defaults.data(forKey: key),
              let list = try? decoder.decode([Desktop].self, from: data) else { return [] }
        return list
    }

    private func save() {
        let list = models.filter { !$0.id.isEmpty }
            .map { Desktop(id: $0.id, name: $0.name, wsURL: $0.wsURL, secret: $0.secret) }
        if let data = try? encoder.encode(list) { defaults.set(data, forKey: key) }
    }

    private func localPairing() async -> (desktop: Desktop, publicURL: String?)? {
        #if os(macOS)
        let path = (NSHomeDirectory() as NSString).appendingPathComponent(".oculus/pairing.json")
        // Read the bytes off the main actor — synchronous disk I/O must not block the UI.
        let data = await Task.detached { FileManager.default.contents(atPath: path) }.value
        guard let data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: String],
              let ws = obj["ws"], let pub = obj["pub"], let sec = obj["secret"] else { return nil }
        return (Desktop(id: pub, name: obj["name"] ?? "This Mac", wsURL: ws, secret: sec), obj["public"])
        #else
        return nil
        #endif
    }
}
