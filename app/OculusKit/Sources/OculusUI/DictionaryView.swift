import SwiftUI
import OculusKit

/// Manage the technical vocabulary autocorrect is allowed to leave alone.
///
/// The prompt box keeps autocorrect on — most of what you type is prose. The failure was narrower:
/// the system had never heard of `mcp` or `jira`, so it rewrote them and you found out after
/// sending. Here you can see what's been taught, add your own, and take it back.
struct DictionaryView: View {
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    @State private var draft = ""
    @State private var custom: [String] = TechDictionary.custom
    @State private var query = ""
    @State private var showSeeded = false
    @State private var justAdded: String? = nil

    private var filteredSeeded: [String] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        let all = TechDictionary.seeded.sorted()
        return q.isEmpty ? all : all.filter { $0.contains(q) }
    }
    private var filteredCustom: [String] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        return q.isEmpty ? custom : custom.filter { $0.lowercased().contains(q) }
    }

    var body: some View {
        OculusSheet(
            title: "Dictionary",
            subtitle: "Words autocorrect should leave alone.",
            palette: palette,
            search: TechDictionary.seeded.count + custom.count >= 6 ? $query : nil,
            searchPrompt: "Search words",
            onClose: onClose
        ) {
            addRow

            if !filteredCustom.isEmpty {
                sectionHeader("YOUR WORDS", count: filteredCustom.count)
                SheetCard(palette: palette) {
                    FlowChips(words: filteredCustom, palette: palette, removable: true) { word in
                        TechDictionary.remove(word)
                        custom = TechDictionary.custom
                    }
                }
            } else if !query.isEmpty && filteredSeeded.isEmpty {
                SheetEmptyState(icon: "character.book.closed",
                                title: "Nothing matches",
                                message: "No word matching “\(query)”.",
                                palette: palette) {
                    Button("Clear search") { query = "" }.buttonStyle(.bordered)
                }
            }

            DisclosureGroup(isExpanded: $showSeeded) {
                SheetCard(palette: palette) {
                    FlowChips(words: filteredSeeded, palette: palette, removable: false) { _ in }
                }
                .padding(.top, OculusSpace.xs)
            } label: {
                Text("Built-in technical words (\(TechDictionary.seeded.count))")
                    .font(.system(size: 12)).foregroundStyle(palette.foreground)
            }

            SheetCard(palette: palette) {
                Text("These are taught to your Mac's dictionary, so they also stop being flagged in Mail and Notes — not just here.")
                    .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
                Button("Forget the built-in words") {
                    TechDictionary.forgetSeeded()
                }
                .buttonStyle(.bordered).controlSize(.small)
            }
        }
        .onAppear { custom = TechDictionary.custom }
    }

    private var addRow: some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            HStack(spacing: OculusSpace.sm) {
                TextField("Add a word (e.g. kubectl)", text: $draft)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit(add)
                    #if os(iOS)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    #endif
                Button("Add", action: add)
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .disabled(draft.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            if let w = justAdded {
                Label("“\(w)” added — autocorrect will leave it alone.", systemImage: "checkmark.circle.fill")
                    .font(.system(size: 11)).foregroundStyle(Color(hex: 0x3FB950))
                    .transition(.opacity)
            }
        }
        .animation(.easeOut(duration: 0.15), value: justAdded)
    }

    private func add() {
        let word = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard TechDictionary.add(word) else { draft = ""; return }
        custom = TechDictionary.custom
        draft = ""
        justAdded = word
        Task {
            try? await Task.sleep(nanoseconds: 2_500_000_000)
            if justAdded == word { justAdded = nil }
        }
    }

    private func sectionHeader(_ t: String, count: Int) -> some View {
        HStack(spacing: OculusSpace.xs) {
            Text(t).font(.system(size: 10, weight: .semibold)).tracking(0.8)
            Text("\(count)").font(.system(size: 10, weight: .semibold, design: .monospaced))
            Spacer()
        }
        .foregroundStyle(palette.mutedForeground)
        .padding(.top, OculusSpace.xs)
    }
}

/// Word chips that wrap onto multiple lines. A plain HStack would push a long vocabulary off the
/// edge of the sheet; a List would waste a whole row on each short word.
struct FlowChips: View {
    let words: [String]
    let palette: OculusPalette
    let removable: Bool
    let onRemove: (String) -> Void

    var body: some View {
        // Chunked rows rather than a Layout: it reads clearly, and these lists are ~100 items, not
        // thousands, so exact packing isn't worth the complexity.
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                HStack(spacing: OculusSpace.xs) {
                    ForEach(row, id: \.self) { chip($0) }
                    Spacer(minLength: 0)
                }
            }
        }
    }

    /// Packs words into rows by approximate width, so a row of short words isn't half empty.
    private var rows: [[String]] {
        var out: [[String]] = []
        var current: [String] = []
        var width = 0
        for w in words {
            let cost = w.count + 4
            if width + cost > 58, !current.isEmpty {
                out.append(current); current = []; width = 0
            }
            current.append(w); width += cost
        }
        if !current.isEmpty { out.append(current) }
        return out
    }

    private func chip(_ w: String) -> some View {
        HStack(spacing: 3) {
            Text(w).font(.system(size: 11, design: .monospaced))
            if removable {
                Button { onRemove(w) } label: {
                    Image(systemName: "xmark").font(.system(size: 8, weight: .bold))
                }
                .buttonStyle(.plain)
            }
        }
        .foregroundStyle(palette.mutedForeground)
        .padding(.horizontal, OculusSpace.sm)
        .padding(.vertical, 3)
        .background(palette.input)
        .clipShape(RoundedRectangle(cornerRadius: OculusRadius.pill))
    }
}
