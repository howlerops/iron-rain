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

    static func set(_ value: String, for account: String) {
        guard let data = value.data(using: .utf8) else { return }
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

    static func get(_ account: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess,
              let data = out as? Data, let s = String(data: data, encoding: .utf8), !s.isEmpty
        else { return nil }
        return s
    }

    static func remove(_ account: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
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
