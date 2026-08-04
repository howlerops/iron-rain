import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif

/// A paired desktop (Mac) the app can connect to. Persisted as JSON in UserDefaults — everything
/// EXCEPT the credential, which lives in the Keychain (see `secret` below).
public struct Desktop: Codable, Identifiable, Hashable {
    public var id: String // daemon public key (stable identity)
    public var name: String
    public var wsURL: String
    /// The credential for this desktop. It is deliberately NOT written to the persisted JSON: this
    /// store's blob lives in UserDefaults, which is a plaintext plist in unencrypted backups, and this
    /// string reaches a shell on that Mac. `save()` blanks it and files the real value under the
    /// desktop's id in the Keychain; `loadDesktops()` reads it back (migrating any plaintext copy an
    /// older build left behind).
    public var secret: String
    /// Shared relay URL for remote access from anywhere. Optional so JSON saved by older builds
    /// (no relay key) still decodes.
    public var relay: String?
}

/// Parsed `oculus://pair?ws=…&pub=…&secret=…&name=…[&relay=…]` payload.
public struct PairingPayload {
    public var wsURL: String
    public var pub: String
    public var secret: String
    public var name: String?
    public var relay: String?

    public init?(_ payload: String) {
        guard let comps = URLComponents(string: payload), comps.scheme == "oculus" else { return nil }
        let items = comps.queryItems ?? []
        func q(_ n: String) -> String? { items.first { $0.name == n }?.value }
        guard let ws = q("ws"), let pub = q("pub"), let secret = q("secret") else { return nil }
        self.wsURL = ws; self.pub = pub; self.secret = secret; self.name = q("name"); self.relay = q("relay")
    }

    public init(wsURL: String, pub: String, secret: String, name: String? = nil, relay: String? = nil) {
        self.wsURL = wsURL; self.pub = pub; self.secret = secret; self.name = name; self.relay = relay
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
            // Keychain first — a build that already migrated has no plaintext copy left to read.
            let sec = Keychain.get(Keychain.credentialAccount(daemonPub: pub))
                ?? defaults.string(forKey: "oculus.secret") ?? ""
            if !pub.isEmpty, !ws.isEmpty {
                let relay = defaults.string(forKey: "oculus.relay") ?? ""
                desks.append(Desktop(id: pub, name: "My Mac", wsURL: ws, secret: sec, relay: relay))
            }
        }
        // Read the local daemon pairing once and reuse it (avoids re-reading pairing.json).
        let local = await localPairing()
        if let local, !desks.contains(where: { $0.id == local.desktop.id }) {
            // A local daemon whose key we don't recognise, at an address we DO: the daemon on this Mac
            // was reinstalled (or ~/.oculus/key was wiped) and generated a fresh identity. Retire the
            // stale entry in place rather than appending a second one.
            //
            // This is the recovery path for a legitimate key change, and it is silent on purpose. The
            // evidence came from a 0600 file in the user's own home — writing it already requires being
            // that user on this machine, which also means holding ~/.oculus/key. Prompting here would
            // put a security warning in front of an event that is always benign, and a warning people
            // are trained to dismiss is worse than no warning at all. The prompt belongs on the QR
            // path (see `add`), where the evidence is something a stranger could have handed you.
            if let stale = desks.first(where: { Self.host($0.wsURL) != nil && Self.host($0.wsURL) == Self.host(local.desktop.wsURL) }) {
                desks.removeAll { $0.id == stale.id }
                Keychain.forgetDaemon(stale.id)
                let dead = stale.id
                Task { await TranscriptCache.shared.forgetDaemon(dead) }
            }
            desks.append(local.desktop)
        }
        for d in desks { ensureModel(d) }
        // End any Live Activities orphaned by a previous launch so none linger in the Dynamic
        // Island (they're re-created on demand when an agent is actually working).
        models.first?.clearStaleLiveActivities()
        // macOS: pairing.json is the source of truth for the LOCAL daemon — refresh the reachable URL
        // from it, and keep its bootstrap code as a FALLBACK credential.
        //
        // It used to overwrite `secret` outright on every launch, which was right when that field held
        // one permanent shared secret. It is wrong now: this app holds a per-device credential, and
        // replacing it with the file's bootstrap code every launch would re-enroll (and re-mint) on
        // every single start. So the stored credential leads, and the bootstrap code is what we fall
        // back to when it is refused — which is the case the old overwrite existed to heal: the daemon
        // was reinstalled and no longer knows this device.
        if let local, let m = models.first(where: { $0.id == local.desktop.id }) {
            m.pairingPublicURL = local.publicURL
            m.localBootstrapSecret = local.desktop.secret
            if m.secret.isEmpty { m.secret = local.desktop.secret }
            m.wsURL = local.desktop.wsURL
            if let relay = local.desktop.relay, !relay.isEmpty { m.relayURL = relay }
        }
        save()
        if selectedID == nil { selectedID = models.first?.id }
        // Reveal the surface immediately and connect in the BACKGROUND. Previously bootstrap awaited
        // connectAll before flipping didBootstrap, so a single hung handshake (e.g. the daemon still
        // holding the pre-update client's socket right after a self-update relaunch) left the app on
        // the loading spinner forever. The real surface shows per-desktop connection status instead.
        didBootstrap = true
        Task { await connectAll() }
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

    /// A scanned pairing that claims to be a Mac we already know, under a DIFFERENT identity key.
    public struct KeyChange: Identifiable, Equatable {
        public var id: String { payloadPub }
        /// What we call the desktop this collides with.
        public var existingName: String
        public var existingPub: String
        public var payloadPub: String
        public var payloadName: String
        public var wsURL: String
        public var secret: String
        public var relay: String
        /// Why we think these are the same machine — shown so the user can judge the claim.
        public var matchedOn: String

        public var existingFingerprint: String { Model.keyFingerprint(existingPub) }
        public var newFingerprint: String { Model.keyFingerprint(payloadPub) }
    }

    /// Set when `add` was handed a pairing that would repin a Mac we already have.
    @Published public var pendingKeyChange: KeyChange?

    /// Adds (or re-pairs) a desktop from a scanned/entered pairing payload and connects.
    ///
    /// Returns nil when the payload would replace an existing desktop's identity key — it is staged on
    /// `pendingKeyChange` instead, and the caller must confirm before it takes effect.
    ///
    /// Desktops are keyed by daemon public key, so a changed key used to slip in as a brand-new entry
    /// rather than overwriting anything. That is not the safety it looks like: the user ends up with
    /// two identically-named Macs, one of which no longer connects, and the one that works is the new
    /// key — so they select the attacker's and the outcome is the same as an overwrite, just with an
    /// extra row in the sidebar. Detecting the collision has to use an anchor that ISN'T the key,
    /// which is why this matches on the address and the name: those are what the user thinks
    /// identifies their Mac.
    @discardableResult
    public func add(_ p: PairingPayload) -> Model? {
        let name = (p.name?.isEmpty == false) ? p.name! : "Desktop"
        if let clash = collision(for: p) {
            pendingKeyChange = KeyChange(
                existingName: clash.desktop.name, existingPub: clash.desktop.id,
                payloadPub: p.pub, payloadName: name,
                wsURL: p.wsURL, secret: p.secret, relay: p.relay ?? "",
                matchedOn: clash.reason
            )
            return nil
        }
        return apply(p, name: name)
    }

    /// Accepts a staged identity change: the old desktop is retired and the new key takes its place.
    ///
    /// The old entry is removed rather than left alongside, because after a genuine reinstall it can
    /// never connect again — and leaving a permanently-broken duplicate named after the user's Mac is
    /// how they end up guessing which row is real. Removing it also purges that identity's credential
    /// and its cached transcripts, which is correct either way: on a reinstall the cache is keyed by a
    /// daemon public key that no longer exists, and after an attack it is evidence we should not keep
    /// serving from.
    public func confirmKeyChange() {
        guard let c = pendingKeyChange else { return }
        pendingKeyChange = nil
        remove(c.existingPub)
        apply(PairingPayload(wsURL: c.wsURL, pub: c.payloadPub, secret: c.secret,
                             name: c.payloadName, relay: c.relay.isEmpty ? nil : c.relay),
              name: c.payloadName)
    }

    /// Discards the staged change; the existing pin stands.
    public func cancelKeyChange() { pendingKeyChange = nil }

    @discardableResult
    private func apply(_ p: PairingPayload, name: String) -> Model {
        let m = ensureModel(Desktop(id: p.pub, name: name, wsURL: p.wsURL, secret: p.secret, relay: p.relay))
        m.name = name; m.wsURL = p.wsURL; m.secret = p.secret; m.daemonPubHex = p.pub; m.relayURL = p.relay ?? ""
        selectedID = m.id
        save()
        Task { if !m.connected { await m.connect() } }
        return m
    }

    private func collision(for p: PairingPayload) -> (desktop: Desktop, reason: String)? {
        let known = models.map { Desktop(id: $0.id, name: $0.name, wsURL: $0.wsURL, secret: "", relay: $0.relayURL) }
        return Self.collision(for: p, among: known)
    }

    /// Finds an existing desktop this payload claims to be, under a different key.
    ///
    /// Same key is not a collision — that is an ordinary re-pair (a rotated credential, a moved
    /// address) and must stay silent. Static and pure so the matching rule is testable without a
    /// store, a socket, or a keychain.
    ///
    /// The anchors are the address and the display name because those are what a person means by
    /// "my Mac". The key cannot be the anchor: the key is the thing in question.
    static func collision(for p: PairingPayload, among known: [Desktop]) -> (desktop: Desktop, reason: String)? {
        guard !p.pub.isEmpty else { return nil }
        let payloadName = (p.name?.isEmpty == false) ? p.name! : ""
        for d in known where !d.id.isEmpty && d.id != p.pub {
            if let a = host(d.wsURL), let b = host(p.wsURL), a == b {
                return (d, "the same address (\(a))")
            }
            if !payloadName.isEmpty, d.name.caseInsensitiveCompare(payloadName) == .orderedSame {
                return (d, "the same name (\(d.name))")
            }
        }
        return nil
    }

    static func host(_ url: String) -> String? {
        guard let h = URL(string: url)?.host, !h.isEmpty else { return nil }
        return h.lowercased()
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
        // Unpairing must leave nothing of that Mac behind. The cached transcripts hold its source
        // code and its conversations, and the Keychain holds a credential that still opens a shell on
        // it; removing the desktop without purging both would keep them on the device indefinitely.
        Keychain.forgetDaemon(id)
        Task { await TranscriptCache.shared.forgetDaemon(id) }
    }

    @discardableResult
    private func ensureModel(_ d: Desktop) -> Model {
        if let existing = models.first(where: { $0.id == d.id }) { return existing }
        let m = Model(name: d.name, wsURL: d.wsURL, daemonPubHex: d.id, secret: d.secret, relay: d.relay ?? "")
        // When the daemon replaces this desktop's pairing code with a real per-device credential, the
        // store has to persist the new one — otherwise the next launch would reconnect with a code
        // that was spent on the first connection.
        m.onCredentialStored = { [weak self] _, _ in self?.save() }
        models.append(m)
        return m
    }

    private func loadDesktops() -> [Desktop] {
        guard let data = defaults.data(forKey: key),
              let list = try? decoder.decode([Desktop].self, from: data) else { return [] }
        return list.map { d in
            var d = d
            if let stored = Keychain.get(Keychain.credentialAccount(daemonPub: d.id)) {
                d.secret = stored
            } else if !d.secret.isEmpty {
                // An older build persisted the credential in the plist. Adopt it into the Keychain
                // once; the next save() writes the blob back without it.
                Keychain.set(d.secret, for: Keychain.credentialAccount(daemonPub: d.id))
            }
            return d
        }
    }

    private func save() {
        // The credential is blanked in the persisted blob and written to the Keychain instead. Both
        // halves matter: writing to the Keychain while still persisting the plaintext copy would
        // leave the exposure exactly where it was.
        let list = models.filter { !$0.id.isEmpty }.map { m -> Desktop in
            if m.secret.isEmpty {
                Keychain.remove(Keychain.credentialAccount(daemonPub: m.id))
            } else {
                Keychain.set(m.secret, for: Keychain.credentialAccount(daemonPub: m.id))
            }
            return Desktop(id: m.id, name: m.name, wsURL: m.wsURL, secret: "", relay: m.relayURL)
        }
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
        return (Desktop(id: pub, name: obj["name"] ?? "This Mac", wsURL: ws, secret: sec, relay: obj["relay"]), obj["public"])
        #else
        return nil
        #endif
    }
}
