import SwiftUI
import OculusKit
#if os(macOS)
import AppKit
#else
import UIKit
#endif

/// Who's connected to this Mac's agents, and what each of them may do.
///
/// Sharing is OFF by default and this screen says so plainly: on a solo setup every device you own
/// is the owner, and adding permission friction nobody asked for would be a downgrade. Turning it on
/// makes new devices watch-only until you grant them steering.
///
/// Two deliberate rules, both enforced by the daemon rather than here:
///  - The owner is whose machine and credentials the agent actually acts with, and it is always
///    visible. A session that acts as you should never be steerable by someone you can't see.
///  - Only the owner answers approvals. Someone you've let steer can ask the agent to do something;
///    authorizing a tool that runs with YOUR credentials stays yours.
public struct SharingView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    public var body: some View {
        OculusSheet(
            title: "Sharing",
            subtitle: "Who can steer your agents.",
            palette: palette,
            onClose: onClose
        ) {
            toggleRow
            if model.participants.isEmpty {
                // A bare sentence left the reader with the question it raised — how do you connect
                // one — answered only by a section further down that sharing has to be ON to show.
                SheetEmptyState(icon: "iphone.and.arrow.forward",
                                title: "No other devices",
                                message: model.sharingEnabled
                                    ? "Create an invite link below and open it on the other device. Links admit one device and expire after 24 hours."
                                    : "Devices connect by scanning this Mac's pairing code, or through an invite link. Turn on the switch above to mint links and choose what each device may do.",
                                palette: palette)
            } else {
                VStack(alignment: .leading, spacing: OculusSpace.xs) {
                    Text("Connected").font(.caption.weight(.semibold))
                        .foregroundStyle(palette.mutedForeground)
                    VStack(spacing: OculusSpace.sm) {
                        ForEach(model.participants) { row($0) }
                    }
                }
            }
            if model.sharingEnabled {
                inviteSection
                rulesNote
            }
        }
        .task { await model.loadParticipants() }
        .animation(.easeOut(duration: 0.18), value: model.sharingEnabled)
    }

    private var toggleRow: some View {
        SheetCard(palette: palette) {
            Toggle(isOn: Binding(
                get: { model.sharingEnabled },
                set: { on in setSharing(on) }
            )) {
                Text("Require permission to steer").font(.footnote)
            }
            .toggleStyle(.switch).tint(palette.primary)
            Text(model.sharingEnabled
                 ? "New devices can watch, but can't prompt or interrupt until you let them."
                 : "Off — every device connected to this Mac has full control. Fine when they're all yours.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// `setSharingEnabled` returns Void and swallows its error, so a switch that never reached the
    /// daemon snaps to the state the user asked for while enforcement stays where it was. On this
    /// screen that fails in the dangerous direction — the UI would claim steering is gated when
    /// every connected device still has full control. The daemon returns the authoritative flag,
    /// so a flag that didn't move is the signal.
    private func setSharing(_ on: Bool) {
        Task {
            await model.setSharingEnabled(on)
            if model.sharingEnabled != on {
                model.setError(on ? "Couldn’t require permission" : "Couldn’t turn sharing off",
                               "Nothing changed — connected devices still have the access they had. Check the daemon is connected and try again.")
            }
        }
    }

    private func row(_ p: Participant) -> some View {
        SheetCard(palette: palette) {
        HStack(spacing: OculusSpace.sm) {
            Image(systemName: p.role == ParticipantRole.owner ? "person.crop.circle.fill.badge.checkmark" : "person.crop.circle")
                .foregroundStyle(p.role == ParticipantRole.owner ? palette.primary : palette.mutedForeground)
                // Decorative: the role it encodes is spelled out in the line directly beneath it.
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 1) {
                Text(p.name).font(.subheadline).foregroundStyle(palette.foreground)
                Text(ParticipantRole.label(p.role))
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            if model.sharingEnabled && p.role != ParticipantRole.owner {
                // Revocable by design: a grant you can't take back isn't a grant, it's a handover.
                Button(p.role == ParticipantRole.steerer ? "Revoke" : "Let steer") {
                    let next = p.role == ParticipantRole.steerer ? ParticipantRole.observer : ParticipantRole.steerer
                    grant(p, next)
                }
                .buttonStyle(.bordered)
                #if os(macOS)
                .controlSize(.small)
                #endif
                .accessibilityLabel(p.role == ParticipantRole.steerer
                                    ? "Revoke steering from \(p.name)" : "Let \(p.name) steer")
            }
        }
        }
    }

    @State private var inviteLabel = ""
    @State private var inviteRole = ParticipantRole.observer
    @State private var creating = false

    /// `grantRole` returns Void and swallows its error. A Revoke that silently did nothing is the
    /// worst outcome on this screen: the roster would show "watch only" while that device can still
    /// prompt and interrupt your agents. The daemon returns the full roster, so a role that didn't
    /// move is the signal.
    private func grant(_ p: Participant, _ next: String) {
        Task {
            await model.grantRole(name: p.name, role: next)
            if model.participants.first(where: { $0.name == p.name })?.role != next {
                model.setError(next == ParticipantRole.steerer
                               ? "Couldn’t let \(p.name) steer" : "Couldn’t revoke \(p.name)",
                               "Their access is unchanged. Check the daemon is connected and try again.")
            }
        }
    }

    /// Same failure mode for invites: a revoked link that wasn't actually revoked still admits a
    /// device. The daemon returns the remaining invites, so one that's still listed didn't go.
    private func revoke(_ inv: Invite) {
        Task {
            await model.revokeInvite(id: inv.id)
            if model.invites.contains(where: { $0.id == inv.id }) {
                model.setError("Couldn’t revoke that link",
                               "The link is still valid and can still admit a device. Check the daemon is connected and try again.")
            }
        }
    }

    /// Minting a share link. The secret is shown exactly once — the daemon never returns it again,
    /// which is deliberate: a credential you can re-read is one that leaks off a screen later.
    private var inviteSection: some View {
        SheetCard(palette: palette) {
            Text("Invite someone").font(.caption.weight(.semibold))
                .foregroundStyle(palette.mutedForeground)

            if let url = model.freshInviteURL {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Share this link — it won't be shown again.")
                        .font(.caption).foregroundStyle(palette.foreground)
                    Text(url)
                        .font(.system(.caption2, design: .monospaced))
                        .textSelection(.enabled).lineLimit(3)
                        .padding(8).background(palette.input)
                        .clipShape(OculusShape.rounded(6))
                    HStack {
                        Button("Copy link") { copyToPasteboard(url) }
                            .buttonStyle(.borderedProminent).tint(palette.primary)
                        Button("Done") { model.freshInviteURL = nil }
                            .buttonStyle(.bordered)
                    }
                }
                .padding(OculusSpace.md)
                .background(palette.input)
                .clipShape(OculusShape.rounded(OculusRadius.sm))
            } else {
                HStack(spacing: 8) {
                    TextField("Who's this for?", text: $inviteLabel).textFieldStyle(.roundedBorder)
                    Picker("", selection: $inviteRole) {
                        Text("Watch").tag(ParticipantRole.observer)
                        Text("Steer").tag(ParticipantRole.steerer)
                    }
                    .labelsHidden().pickerStyle(.menu).fixedSize()
                    Button(creating ? "…" : "Create") {
                        creating = true
                        Task {
                            await model.createInvite(label: inviteLabel, role: inviteRole, ttlHours: 24)
                            inviteLabel = ""
                            creating = false
                        }
                    }
                    .buttonStyle(.bordered).disabled(creating)
                }
                Text("Links admit one device, expire after 24 hours, and can never grant ownership.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            ForEach(model.invites) { inv in
                HStack(spacing: 8) {
                    Image(systemName: "link").font(.caption2).foregroundStyle(palette.mutedForeground)
                    Text(inv.label?.isEmpty == false ? inv.label! : "Untitled invite")
                        .font(.footnote).foregroundStyle(palette.foreground)
                    Text(ParticipantRole.label(inv.role))
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                    if inv.redeemed > 0 {
                        Text("· used \(inv.redeemed)×").font(.caption2)
                            .foregroundStyle(palette.mutedForeground)
                    }
                    Spacer()
                    Button("Revoke") { revoke(inv) }
                        .buttonStyle(.plain).font(.caption)
                        .foregroundStyle(palette.destructive)
                        .accessibilityLabel("Revoke invite \(inv.label?.isEmpty == false ? inv.label! : "Untitled")")
                        .sheetTapTarget()
                }
            }
        }
        .task(id: model.sharingEnabled) { await model.loadInvites() }
    }

    private func copyToPasteboard(_ s: String) {
        #if os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(s, forType: .string)
        #else
        UIPasteboard.general.string = s
        #endif
    }

    private var rulesNote: some View {
        SheetCard(palette: palette) {
            Label("Approvals stay yours", systemImage: "lock.shield")
                .font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
            Text("Anyone you let steer can prompt and interrupt. Only you can approve a tool — those run with your credentials on this Mac.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
