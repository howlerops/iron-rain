import SwiftUI

/// The confirmation shown when a pairing would replace a Mac's identity key.
///
/// The wording is doing security work, so it is worth being deliberate about. Three rules it follows:
///
///  1. **Say what happened, not what to click.** "This Mac's identity key changed" is a fact the user
///     can act on. "Are you sure?" is not — it just asks them to guess.
///  2. **Name the only benign cause, and the check that separates it from the other one.** A key
///     changes because the daemon was reinstalled. There is exactly one way to tell that apart from an
///     attacker offering a QR code, and it is comparing the key against the Mac itself — so the dialog
///     shows both fingerprints and says where to look, rather than leaving the user to invent a test.
///  3. **Make the safe option the easy one.** Keep is the default; replacing is the destructive
///     button. Someone who does not understand the question should land on the outcome that loses
///     nothing but a connection.
///
/// What it deliberately does NOT say is that this protects against interception in general. Two
/// things it does nothing about: the channel has no forward secrecy, so anyone recording traffic who
/// later obtains ~/.oculus/key decrypts all of it retroactively; and the v0 handshake is still
/// accepted while clients migrate, so an active attacker can force a downgrade by staying silent
/// through the v1 challenge window and get replayability back. This dialog defends one specific
/// thing — the pin's stability — and that one thing happens to be what BOTH of those weaknesses
/// still route through, because every one of them assumes the attacker could not simply become your
/// Mac in the first place.
struct KeyChangeAlertContent {
    var machine: String
    var currentFingerprint: String
    var newFingerprint: String
    /// Optional explanation of why we believe the new pairing refers to this machine.
    var matchedOn: String?

    var title: String { "\(machine)’s identity key changed" }

    var message: String {
        var lines = [
            "This pairing uses a different key than the one saved for \(machine).",
        ]
        if let matchedOn, !matchedOn.isEmpty {
            lines.append("It claims to be the same Mac — it has \(matchedOn).")
        }
        lines.append("")
        lines.append("Saved:  \(currentFingerprint)")
        lines.append("New:    \(newFingerprint)")
        lines.append("")
        lines.append("This is expected only if you reinstalled the daemon on that Mac. "
            + "Check the key on the Mac itself — oculusd prints it as “daemon pubkey” when it starts — "
            + "and replace only if the new one matches. If you didn’t reinstall anything, "
            + "someone may be trying to take the place of your Mac.")
        return lines.joined(separator: "\n")
    }
}

extension View {
    /// Attaches the identity-change confirmation. `content` is nil while there is nothing staged.
    func keyChangeAlert(
        _ content: KeyChangeAlertContent?,
        onReplace: @escaping () -> Void,
        onKeep: @escaping () -> Void
    ) -> some View {
        alert(
            content?.title ?? "Identity key changed",
            isPresented: Binding(get: { content != nil }, set: { if !$0 { onKeep() } })
        ) {
            // Keep first and .cancel so Return/Escape both land on it: the destructive option must
            // never be what a reflexive keypress selects.
            Button("Keep the saved key", role: .cancel, action: onKeep)
            Button("Replace", role: .destructive, action: onReplace)
        } message: {
            if let content { Text(content.message) }
        }
    }
}
