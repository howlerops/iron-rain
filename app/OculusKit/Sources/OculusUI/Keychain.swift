import Foundation
import Security

/// Keychain storage for the two secrets that authorize this device against a paired Mac: its own
/// X25519 private key, and the credential the daemon minted for it.
///
/// Both used to live in `UserDefaults`. That is a plaintext plist. It is included in unencrypted
/// iTunes/Finder backups, it is readable on an unlocked or jailbroken device, and on the Mac it is a
/// file any process running as the user can read. Since presenting that credential yields owner
/// access — which reaches arbitrary shell on the paired Mac — a plist read was game over, and no
/// amount of work on the daemon side could fix it.
///
/// `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` is doing two specific jobs:
///   - *WhenUnlocked*: a stolen, locked phone does not give the item up.
///   - *ThisDeviceOnly*: the item is excluded from backups and never syncs to another device. That is
///     what stops "restore this backup onto a new phone" from also restoring access to the Mac — and
///     it is what makes the daemon's device identity meaningful, since an identity that can be copied
///     to a second device is not an identity.
enum Keychain {
    /// The service every Iron Rain item is filed under, so `purge` can't touch anything else.
    private static let service = "com.howlerops.oculus"

    /// Every mutation runs here, and this queue is SERIAL on purpose.
    ///
    /// Writes have to leave the calling thread (see `set`), but they must not lose their order while
    /// doing it. Callers write in pairs that only make sense in sequence — `saveCredential` removes
    /// then a later pairing sets; `DesktopStore.save` removes a desktop's credential then writes the
    /// replacement. Dispatched to a *concurrent* queue those can land in either order, and the losing
    /// order deletes the credential that was just stored: a device that appears paired and is then
    /// refused forever. A serial queue costs nothing here (these are rare, launch- and pairing-time
    /// operations) and makes the reordering impossible.
    private static let writes = DispatchQueue(label: "com.howlerops.oculus.keychain-writes")

    /// Writes a credential. The keychain work runs OFF the calling thread and this returns at once.
    ///
    /// Writes block exactly like reads — SecItemUpdate performs a lookup first, so it sits on the
    /// same authorisation prompt. That is not hypothetical: after bounding `get`, the launch hang
    /// simply moved here. `Model.loadCredential()` falls through to a legacy migration on a failed
    /// read, so a timed-out read led straight into a blocking WRITE and the app froze again, one
    /// stack frame further along.
    ///
    /// Fire-and-forget is safe for this call: no caller consumes a result, and every use is a
    /// migration or a pairing — never a read-back-immediately sequence.
    static func set(_ value: String, for account: String) {
        guard let data = value.data(using: .utf8) else { return }
        writes.async { performSet(data, account: account) }
    }

    private static func performSet(_ data: Data, account: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        let attrs: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
        ]
        // Update first, add if absent. SecItemAdd on an existing account fails with errSecDuplicateItem
        // rather than replacing, which would silently leave the OLD credential in place — exactly the
        // failure that makes a rotated credential look like a broken pairing.
        var status = SecItemUpdate(query as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            status = SecItemAdd(query.merging(attrs) { $1 } as CFDictionary, nil)
        }

        // Say something when the write fails.
        //
        // Both statuses used to be discarded, which made the worst case indistinguishable from
        // success: the credential that gates ALL access to a paired Mac silently not persisting.
        // What it looks like from the outside is a device that pairs once and is then refused
        // forever with "unauthorized" — the daemon is holding a credential the client never kept.
        //
        // `errSecMissingEntitlement` (-34018) is the one to expect on an unsigned build: without an
        // `application-identifier` entitlement there is no keychain access group to write into. That
        // is why an ad-hoc simulator build cannot stay paired, and it is worth naming rather than
        // rediscovering.
        if status != errSecSuccess {
            let hint = status == errSecMissingEntitlement
                ? " (missing entitlement — is this an unsigned build?)" : ""
            NSLog("Keychain: failed to store '\(account)' — OSStatus \(status)\(hint). "
                  + "This device will not stay paired.")
        }
    }

    /// Longest any single read may stall the caller.
    ///
    /// SecItemCopyMatching is not just slow-on-a-bad-day — it blocks INDEFINITELY waiting for user
    /// authorisation whenever the calling binary's code identity no longer matches the one that
    /// stored the item: a re-signed build, a restored or migrated Mac, a keychain the user has
    /// locked. Until somebody answers that prompt, it never returns.
    private static let readBudget: DispatchTimeInterval = .seconds(3)

    /// Reads a credential, giving up after `readBudget`.
    ///
    /// The bound lives HERE, on the primitive, rather than at each call site, because the blocking
    /// calls were scattered through synchronous initialisers — `Model.init()` reads a credential as
    /// part of constructing itself, so there is no await to hang a timeout on. Fixing one caller
    /// simply moved the hang to the next one: bounding DesktopStore's read surfaced an identical
    /// stall inside Model.init a moment later.
    ///
    /// The symptom this removes: the app froze on its launch spinner forever, with no error and no
    /// way forward, because `bootstrap()` blocked before it could reveal the surface. Sampling put
    /// the entire main thread in SecItemCopyMatching.
    ///
    /// On timeout the query thread is ABANDONED, not cancelled — SecItemCopyMatching cannot be
    /// interrupted. That parks one background thread until the prompt is answered or dismissed. It
    /// is a real cost, accepted because the alternative is an unusable app; and it is bounded,
    /// because these reads happen at launch and at pairing, not in a loop.
    /// What a bounded read actually learned.
    ///
    /// `missing` and `timedOut` must not be conflated, because they license opposite actions. A
    /// genuine `missing` means "nothing is stored, create it" — that is how a device key gets minted
    /// on first use. A `timedOut` means "something may well be stored, we just couldn't see it", and
    /// treating that as `missing` is DESTRUCTIVE: `clientKey()` would mint a fresh X25519 key and
    /// write it straight over the real one, permanently breaking this device's identity with the
    /// paired Mac. Collapsing both into `nil` turned a recoverable stall into data loss.
    enum ReadResult {
        case found(String)
        case missing
        case timedOut
    }

    /// Reads a credential, reporting whether it was absent or merely unreachable within the budget.
    static func read(_ account: String) -> ReadResult {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        let box = ResultBox()
        let sem = DispatchSemaphore(value: 0)
        DispatchQueue.global(qos: .userInitiated).async {
            var out: CFTypeRef?
            let status = SecItemCopyMatching(query as CFDictionary, &out)
            if status == errSecSuccess, let data = out as? Data,
               let s = String(data: data, encoding: .utf8), !s.isEmpty {
                box.set(s)
            }
            sem.signal()
        }
        guard sem.wait(timeout: .now() + readBudget) == .success else { return .timedOut }
        if let s = box.value { return .found(s) }
        return .missing
    }

    /// Convenience for the callers that genuinely cannot act on the difference — both "absent" and
    /// "unreachable" mean they have no value to work with. Anything that would WRITE in response to a
    /// nil must use `read` instead.
    static func get(_ account: String) -> String? {
        if case .found(let s) = read(account) { return s }
        return nil
    }

    /// Minimal box so the worker thread can hand a value back across the semaphore without tripping
    /// Swift 6's concurrency checking on a captured `var`.
    private final class ResultBox: @unchecked Sendable {
        private let lock = NSLock()
        private var stored: String?
        func set(_ s: String) { lock.lock(); stored = s; lock.unlock() }
        var value: String? { lock.lock(); defer { lock.unlock() }; return stored }
    }

    /// Deletes a credential, off the calling thread for the same reason `set` is: SecItemDelete
    /// takes the same locks and can stall on the same prompt.
    static func remove(_ account: String) {
        writes.async {
            let query: [String: Any] = [
                kSecClass as String: kSecClassGenericPassword,
                kSecAttrService as String: service,
                kSecAttrAccount as String: account,
            ]
            SecItemDelete(query as CFDictionary)
        }
    }

    // MARK: accounts

    /// This device's persistent X25519 private key for one paired Mac, hex encoded.
    ///
    /// Keyed per daemon so two paired Macs see two different device identities: a Mac you share with
    /// a colleague should not learn the key you present to your own machine.
    static func deviceKeyAccount(daemonPub: String) -> String { "clientkey.\(daemonPub)" }

    /// The per-device credential the daemon minted for this device.
    static func credentialAccount(daemonPub: String) -> String { "credential.\(daemonPub)" }

    /// Everything belonging to one paired Mac, so unpairing leaves nothing behind.
    static func forgetDaemon(_ daemonPub: String) {
        remove(deviceKeyAccount(daemonPub: daemonPub))
        remove(credentialAccount(daemonPub: daemonPub))
    }
}
