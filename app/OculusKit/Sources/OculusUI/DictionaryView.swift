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
    /// "Forget the built-in words" unteaches ~100 entries from the SYSTEM dictionary in one tap,
    /// affecting Mail and Notes too, and there is no button that puts them back individually.
    @State private var confirmForget = false

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
                sectionHeader("Your words", count: filteredCustom.count)
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
                    .font(.footnote).foregroundStyle(palette.foreground)
            }

            SheetCard(palette: palette) {
                Text("These are taught to your Mac's dictionary, so they also stop being flagged in Mail and Notes — not just here.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
                Button(role: .destructive) { confirmForget = true } label: {
                    Text("Forget the built-in words")
                }
                .buttonStyle(.bordered)
                #if os(macOS)
                .controlSize(.small)
                #endif
            }
        }
        .onAppear { custom = TechDictionary.custom }
        .confirmationDialog("Forget the built-in words?",
                            isPresented: $confirmForget, titleVisibility: .visible) {
            Button("Forget \(TechDictionary.seeded.count) words", role: .destructive) {
                TechDictionary.forgetSeeded()
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Removes all \(TechDictionary.seeded.count) built-in technical words from your Mac's dictionary — so autocorrect starts rewriting them again here, and they're flagged again in Mail and Notes. Your own words are kept.")
        }
    }

    private var addRow: some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            HStack(spacing: OculusSpace.sm) {
                TextField("Add a word (e.g. kubectl)", text: $draft)
                    .textFieldStyle(.roundedBorder)
                    .submitLabel(.done)
                    .onSubmit(add)
                    .plainInput()
                Button("Add", action: add)
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .disabled(draft.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            if let w = justAdded {
                Label("“\(w)” added — autocorrect will leave it alone.", systemImage: "checkmark.circle.fill")
                    .font(.caption).foregroundStyle(palette.success)
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
            Text(t).font(.caption.weight(.semibold))
            Text("\(count)").font(.caption.weight(.semibold).monospaced())
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
            Text(w).font(.system(.caption, design: .monospaced))
            if removable {
                Button { onRemove(w) } label: {
                    Image(systemName: "xmark").font(.caption2.weight(.bold))
                }
                .buttonStyle(.plain)
                // A tiny glyph announcing as a bare "Button" in a wall of ~100 identical ones.
                .accessibilityLabel("Forget the word \(w)")
            }
        }
        .foregroundStyle(palette.mutedForeground)
        .padding(.horizontal, OculusSpace.sm)
        .padding(.vertical, 3)
        .background(palette.input)
        .clipShape(OculusShape.rounded(OculusRadius.pill))
    }
}
