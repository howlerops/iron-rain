import SwiftUI
import OculusKit

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
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().overlay(palette.border)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    toggleRow
                    if model.participants.isEmpty {
                        Text("No other devices are connected.")
                            .font(.callout).foregroundStyle(palette.mutedForeground)
                            .padding(.top, 4)
                    } else {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("CONNECTED").font(.system(size: 10, weight: .semibold)).tracking(0.8)
                                .foregroundStyle(palette.mutedForeground)
                            ForEach(model.participants) { row($0) }
                        }
                    }
                    if model.sharingEnabled { rulesNote }
                }
                .padding(16)
            }
        }
        .frame(minWidth: 460, minHeight: 320)
        .background(palette.background)
        .task { await model.loadParticipants() }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("Sharing").font(.headline).foregroundStyle(palette.foreground)
                Text("Who can steer your agents.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            if let onClose { Button("Done", action: onClose).keyboardShortcut(.defaultAction) }
        }
        .padding(14)
    }

    private var toggleRow: some View {
        VStack(alignment: .leading, spacing: 5) {
            Toggle(isOn: Binding(
                get: { model.sharingEnabled },
                set: { on in Task { await model.setSharingEnabled(on) } }
            )) {
                Text("Require permission to steer").font(.system(size: 13))
            }
            .toggleStyle(.switch).tint(palette.primary)
            Text(model.sharingEnabled
                 ? "New devices can watch, but can't prompt or interrupt until you let them."
                 : "Off — every device connected to this Mac has full control. Fine when they're all yours.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
    }

    private func row(_ p: Participant) -> some View {
        HStack(spacing: 10) {
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
                .buttonStyle(.bordered).font(.system(size: 11))
            }
        }
        .padding(.vertical, 3)
    }

    private var rulesNote: some View {
        VStack(alignment: .leading, spacing: 4) {
            Label("Approvals stay yours", systemImage: "lock.shield")
                .font(.system(size: 12, weight: .medium)).foregroundStyle(palette.foreground)
            Text("Anyone you let steer can prompt and interrupt. Only you can approve a tool — those run with your credentials on this Mac.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
        .padding(10)
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
