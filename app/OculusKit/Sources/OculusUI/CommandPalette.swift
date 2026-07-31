import SwiftUI
import OculusKit

/// The Cmd-K command palette — one fuzzy entry point spanning destinations, sessions, loops, agents,
/// and quick actions. This is the "feels clean" spine of the Command Deck: velocity for power users
/// without adding a second nav model (the rail/tabs remain the discoverable path). macOS opens it
/// with ⌘K as a centered overlay; iOS presents it as a pull-down search sheet.
struct PaletteItem: Identifiable {
    enum Kind { case destination, session, loop, agent, action }
    let id: String
    let kind: Kind
    let title: String
    let subtitle: String
    let symbol: String
    let run: () -> Void
}

struct CommandPalette: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let items: [PaletteItem]
    let onClose: () -> Void

    @State private var query = ""
    @FocusState private var focused: Bool

    private var filtered: [PaletteItem] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return items }
        return items.filter { fuzzy(q, $0.title.lowercased()) || $0.subtitle.lowercased().contains(q) }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 9) {
                Image(systemName: "magnifyingglass").foregroundStyle(palette.mutedForeground)
                TextField("Jump to a session, loop, agent, or action…", text: $query)
                    .textFieldStyle(.plain)
                    .font(.system(size: 15))
                    .focused($focused)
                    .onSubmit { filtered.first?.run(); onClose() }
                Text("esc").font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .background(RoundedRectangle(cornerRadius: 4).fill(palette.secondary))
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
            Divider().overlay(palette.border)
            ScrollView {
                LazyVStack(spacing: 1) {
                    ForEach(filtered.prefix(40)) { item in
                        Button { item.run(); onClose() } label: { row(item) }
                            .buttonStyle(.plain)
                    }
                    if filtered.isEmpty {
                        Text("No matches").font(.callout).foregroundStyle(palette.mutedForeground)
                            .frame(maxWidth: .infinity).padding(.vertical, 30)
                    }
                }
                .padding(6)
            }
            .frame(maxHeight: 380)
        }
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .frame(maxWidth: 560)
        .shadow(color: .black.opacity(0.35), radius: 30, y: 12)
        .onAppear { focused = true }
    }

    private func row(_ item: PaletteItem) -> some View {
        HStack(spacing: 11) {
            Image(systemName: item.symbol).font(.system(size: 13)).frame(width: 20)
                .foregroundStyle(item.kind == .action ? palette.primary : palette.mutedForeground)
            VStack(alignment: .leading, spacing: 1) {
                Text(item.title).font(.system(size: 13.5)).foregroundStyle(palette.foreground).lineLimit(1)
                if !item.subtitle.isEmpty {
                    Text(item.subtitle).font(.system(size: 10.5, design: .monospaced))
                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
            }
            Spacer(minLength: 4)
            Text(kindLabel(item.kind)).font(.system(size: 9, weight: .semibold)).tracking(0.5)
                .foregroundStyle(palette.mutedForeground)
                .padding(.horizontal, 5).padding(.vertical, 2)
                .background(RoundedRectangle(cornerRadius: 4).fill(palette.secondary))
        }
        .padding(.horizontal, 10).padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private func kindLabel(_ k: PaletteItem.Kind) -> String {
        switch k {
        case .destination: return "GO"
        case .session: return "SESSION"
        case .loop: return "LOOP"
        case .agent: return "AGENT"
        case .action: return "ACTION"
        }
    }

    /// Subsequence fuzzy match ("rlyfx" matches "relay-fix").
    private func fuzzy(_ needle: String, _ hay: String) -> Bool {
        if hay.contains(needle) { return true }
        var it = hay.makeIterator()
        for ch in needle {
            var found = false
            while let h = it.next() { if h == ch { found = true; break } }
            if !found { return false }
        }
        return true
    }
}
