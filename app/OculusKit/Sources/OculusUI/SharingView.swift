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
                Text("No other devices are connected.")
                    .font(.system(size: 12)).foregroundStyle(palette.mutedForeground)
            } else {
                VStack(alignment: .leading, spacing: OculusSpace.xs) {
                    Text("CONNECTED").font(.system(size: 10, weight: .semibold)).tracking(0.8)
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
                set: { on in Task { await model.setSharingEnabled(on) } }
            )) {
                Text("Require permission to steer").font(.system(size: 12.5))
            }
            .toggleStyle(.switch).tint(palette.primary)
            Text(model.sharingEnabled
                 ? "New devices can watch, but can't prompt or interrupt until you let them."
                 : "Off — every device connected to this Mac has full control. Fine when they're all yours.")
                .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func row(_ p: Participant) -> some View {
        SheetCard(palette: palette) {
        HStack(spacing: OculusSpace.sm) {
            Image(systemName: p.role == ParticipantRole.owner ? "person.crop.circle.fill.badge.checkmark" : "person.crop.circle")
                .foregroundStyle(p.role == ParticipantRole.owner ? palette.primary : palette.mutedForeground)
            VStack(alignment: .leading, spacing: 1) {
                Text(p.name).font(.system(size: 13)).foregroundStyle(palette.foreground)
                Text(ParticipantRole.label(p.role))
                    .font(.system(size: 10.5)).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            if model.sharingEnabled && p.role != ParticipantRole.owner {
                // Revocable by design: a grant you can't take back isn't a grant, it's a handover.
                Button(p.role == ParticipantRole.steerer ? "Revoke" : "Let steer") {
                    let next = p.role == ParticipantRole.steerer ? ParticipantRole.observer : ParticipantRole.steerer
                    Task { await model.grantRole(name: p.name, role: next) }
                }
                .buttonStyle(.bordered).controlSize(.small)
            }
        }
        }
    }

    @State private var inviteLabel = ""
    @State private var inviteRole = ParticipantRole.observer
    @State private var creating = false

    /// Minting a share link. The secret is shown exactly once — the daemon never returns it again,
    /// which is deliberate: a credential you can re-read is one that leaks off a screen later.
    private var inviteSection: some View {
        SheetCard(palette: palette) {
            Text("INVITE SOMEONE").font(.system(size: 10, weight: .semibold)).tracking(0.8)
                .foregroundStyle(palette.mutedForeground)

            if let url = model.freshInviteURL {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Share this link — it won't be shown again.")
                        .font(.caption).foregroundStyle(palette.foreground)
                    Text(url)
                        .font(.system(size: 10, design: .monospaced))
                        .textSelection(.enabled).lineLimit(3)
                        .padding(8).background(palette.input)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                    HStack {
                        Button("Copy link") { copyToPasteboard(url) }
                            .buttonStyle(.borderedProminent).tint(palette.primary)
                        Button("Done") { model.freshInviteURL = nil }
                            .buttonStyle(.bordered)
                    }
                }
                .padding(OculusSpace.md)
                .background(palette.input)
                .clipShape(RoundedRectangle(cornerRadius: OculusRadius.sm))
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
                Text("Links expire after 24 hours. An invite can never grant ownership.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            ForEach(model.invites) { inv in
                HStack(spacing: 8) {
                    Image(systemName: "link").font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                    Text(inv.label?.isEmpty == false ? inv.label! : "Untitled invite")
                        .font(.system(size: 12)).foregroundStyle(palette.foreground)
                    Text(ParticipantRole.label(inv.role))
                        .font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                    if inv.redeemed > 0 {
                        Text("· used \(inv.redeemed)×").font(.system(size: 10))
                            .foregroundStyle(palette.mutedForeground)
                    }
                    Spacer()
                    Button("Revoke") { Task { await model.revokeInvite(id: inv.id) } }
                        .buttonStyle(.plain).font(.system(size: 11))
                        .foregroundStyle(palette.destructive)
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
                .font(.system(size: 12, weight: .medium)).foregroundStyle(palette.foreground)
            Text("Anyone you let steer can prompt and interrupt. Only you can approve a tool — those run with your credentials on this Mac.")
                .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}
