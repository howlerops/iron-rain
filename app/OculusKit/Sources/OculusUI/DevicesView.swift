import SwiftUI
import OculusKit

/// Which devices can reach this Mac's agents, and how to cut one off.
///
/// Enrollment and revocation were built end to end — the daemon mints a per-device credential at
/// pairing, lists devices, revokes them, and renames them; the client models all four — and the
/// whole thing was reachable from no screen in the app. So a phone you no longer have still holds a
/// live credential to a machine that runs shell commands on your behalf, and the only way to take it
/// away was to open a terminal on the Mac. That is the wrong shape for the one feature people reach
/// for when something has actually gone wrong.
///
/// Revoking marks the entry revoked rather than deleting it, deliberately: a deleted row simply
/// re-enrolls on the device's next connection, which would make Revoke look like it worked and
/// quietly do nothing.
public struct DevicesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    @State private var confirmRevoke: DeviceInfo? = nil
    @State private var confirmRetire = false
    @State private var renaming: String? = nil
    @State private var draftLabel = ""

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    public var body: some View {
        OculusSheet(
            title: "Devices",
            subtitle: "What can reach this Mac's agents.",
            palette: palette,
            onClose: onClose
        ) {
            if model.devices.isEmpty {
                SheetEmptyState(icon: "iphone.and.arrow.forward",
                                title: "No devices enrolled",
                                message: "Devices appear here once they pair with this Mac. Scan the pairing code on the daemon, or open an invite link.",
                                palette: palette)
            } else {
                VStack(spacing: OculusSpace.sm) {
                    ForEach(model.devices) { row($0) }
                }
            }
            legacySection
        }
        .task {
            await model.loadDevices()
            await model.loadPairStatus()
        }
        .confirmationDialog("Revoke this device?",
                            isPresented: Binding(get: { confirmRevoke != nil },
                                                 set: { if !$0 { confirmRevoke = nil } }),
                            titleVisibility: .visible) {
            Button("Revoke", role: .destructive) {
                if let d = confirmRevoke { revoke(d) }
                confirmRevoke = nil
            }
            Button("Cancel", role: .cancel) { confirmRevoke = nil }
        } message: {
            Text("\(name(confirmRevoke)) will be disconnected and will not be able to reach this Mac's agents again without pairing from scratch.")
        }
        .confirmationDialog("Retire the old pairing secret?", isPresented: $confirmRetire, titleVisibility: .visible) {
            Button("Retire it now", role: .destructive) { retireLegacy() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Devices enrolled since the upgrade keep working. Anything still relying on the old shared secret — including a device you revoked above — will have to pair again.")
        }
    }

    private func row(_ d: DeviceInfo) -> some View {
        SheetCard(palette: palette) {
            HStack(alignment: .top, spacing: OculusSpace.sm) {
                Image(systemName: d.guest == true ? "person.crop.circle.badge.clock" : "iphone")
                    .foregroundStyle(d.this == true ? palette.primary : palette.mutedForeground)
                    // Decorative: everything it encodes is written out beside it.
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        if renaming == d.pub {
                            TextField("Name", text: $draftLabel, onCommit: { commitRename(d) })
                                .textFieldStyle(.roundedBorder)
                                .frame(maxWidth: 220)
                        } else {
                            Text(name(d)).font(.subheadline).foregroundStyle(palette.foreground)
                        }
                        if d.this == true {
                            Badge(text: "This device", tint: palette.primary, palette: palette)
                        }
                        if d.guest == true {
                            Badge(text: "Guest", tint: palette.mutedForeground, palette: palette)
                        }
                    }
                    Text(seenLine(d)).font(.caption).foregroundStyle(palette.mutedForeground)
                    // The key is the identity. Shown truncated and selectable so a device can be
                    // told apart from another with the same name before you cut one of them off.
                    Text(shortKey(d.pub))
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(palette.mutedForeground)
                        .textSelection(.enabled)
                }
                Spacer()
                VStack(alignment: .trailing, spacing: 4) {
                    if renaming == d.pub {
                        Button("Save") { commitRename(d) }
                            .buttonStyle(.bordered)
                            #if os(macOS)
                            .controlSize(.small)
                            #endif
                    } else {
                        Button("Rename") {
                            draftLabel = d.label ?? ""
                            renaming = d.pub
                        }
                        .buttonStyle(.bordered)
                        #if os(macOS)
                        .controlSize(.small)
                        #endif
                        .accessibilityLabel("Rename \(name(d))")
                    }
                    // Never offered for the connection we are speaking over: revoking it would drop
                    // this session mid-tap and leave the user with no way back in.
                    if d.this != true {
                        Button("Revoke", role: .destructive) { confirmRevoke = d }
                            .buttonStyle(.bordered)
                            #if os(macOS)
                            .controlSize(.small)
                            #endif
                            .accessibilityLabel("Revoke \(name(d))")
                    }
                }
            }
        }
    }

    /// The old shared secret is not a device, so revoking devices does not retire it. Shown only
    /// when it is still live — there is no reason to explain a door that is already shut.
    @ViewBuilder private var legacySection: some View {
        if model.pairStatus?.legacyLive == true {
            SheetCard(palette: palette, tint: palette.warning) {
                HStack(spacing: 6) {
                    Image(systemName: "key.slash").foregroundStyle(palette.warning)
                        .accessibilityHidden(true)
                    Text("The old pairing secret still works").font(.subheadline.weight(.medium))
                        .foregroundStyle(palette.foreground)
                }
                Text(legacyDetail)
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Retire it now", role: .destructive) { confirmRetire = true }
                    .buttonStyle(.bordered)
                    #if os(macOS)
                    .controlSize(.small)
                    #endif
            }
        }
    }

    private var legacyDetail: String {
        let base = "Before per-device credentials, every device shared one permanent secret. It belongs to no device, so revoking devices above does not retire it — anything that has ever seen it can still connect."
        guard let at = model.pairStatus?.legacyRetireAt, at > 0 else {
            return base + " It has no expiry."
        }
        let when = Date(timeIntervalSince1970: TimeInterval(at))
        return base + " It expires on its own \(when.formatted(date: .abbreviated, time: .shortened))."
    }

    // MARK: - Actions
    //
    // Every one of these checks that the daemon's answer actually MOVED. revokeDevice and
    // labelDevice both return Void and swallow their errors, which on this screen fails in the
    // dangerous direction: the list would redraw as though a device were cut off while its
    // credential still opens the daemon.

    private func revoke(_ d: DeviceInfo) {
        Task {
            await model.revokeDevice(d.pub)
            await model.loadDevices()
            if model.devices.contains(where: { $0.pub == d.pub && $0.guest != true }) {
                model.setError("Couldn't revoke \(name(d))",
                               "It is still enrolled and can still reach this Mac's agents. Check the daemon is connected and try again.")
            }
        }
    }

    private func commitRename(_ d: DeviceInfo) {
        let wanted = draftLabel.trimmingCharacters(in: .whitespacesAndNewlines)
        renaming = nil
        guard !wanted.isEmpty, wanted != (d.label ?? "") else { return }
        Task {
            await model.labelDevice(d.pub, label: wanted)
            if model.devices.first(where: { $0.pub == d.pub })?.label != wanted {
                model.setError("Couldn't rename that device", "Its name is unchanged.")
            }
        }
    }

    private func retireLegacy() {
        Task {
            if await model.retireLegacySecret() == false {
                model.setError("The old pairing secret is still live",
                               "Nothing changed — anything holding that secret can still connect. Check the daemon is connected and try again.")
            }
        }
    }

    // MARK: - Formatting

    private func name(_ d: DeviceInfo?) -> String {
        guard let d else { return "That device" }
        if let l = d.label, !l.isEmpty { return l }
        return "Unnamed device"
    }

    private func seenLine(_ d: DeviceInfo) -> String {
        let first = Date(timeIntervalSince1970: TimeInterval(d.firstSeen))
        let last = Date(timeIntervalSince1970: TimeInterval(d.lastSeen))
        let lastText = d.lastSeen > 0 ? last.formatted(.relative(presentation: .named)) : "never"
        let firstText = d.firstSeen > 0 ? first.formatted(date: .abbreviated, time: .omitted) : "unknown"
        return "Last seen \(lastText) · paired \(firstText)"
    }

    /// Enough of the key to tell two devices apart, not so much that it wraps on a phone.
    private func shortKey(_ pub: String) -> String {
        pub.count <= 16 ? pub : String(pub.prefix(8)) + "…" + String(pub.suffix(8))
    }
}

/// A small inline pill. Local to this screen — the roster badges elsewhere carry a role, which is a
/// different thing with different colour rules.
private struct Badge: View {
    let text: String
    let tint: Color
    let palette: OculusPalette

    var body: some View {
        Text(text)
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(tint.opacity(0.15), in: Capsule())
            .foregroundStyle(tint)
    }
}
