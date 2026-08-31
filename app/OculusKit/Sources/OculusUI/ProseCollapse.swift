import SwiftUI
import OculusKit

// Long prose answers, folded down to a readable height with the rest one tap away.
//
// The generative-UI components already collapse when they get big, but the most common wall in a
// transcript is not a table — it is a 900-word answer, and on a phone that is several screens of
// scrolling before the next message. The component treatment (a one-line card) is wrong here: a
// table is an artifact you consult, whereas prose IS the answer, and hiding it entirely behind a
// chevron would mean tapping to read every substantial reply.
//
// So this keeps the opening inline, fades it out, and offers the whole thing in a sheet. You can
// always read the start without touching anything.

/// Renders assistant prose, folding it when it is long enough to be a wall.
struct CollapsibleProse: View {
    let text: String
    let palette: OculusPalette
    /// Extra spacing applied by the caller to match the rest of the transcript.
    var lineSpacing: CGFloat = 0

    @State private var showingSheet = false

    /// Past this much text the answer stops being something you skim and starts being something you
    /// scroll past. Deliberately generous: the cost of folding a reply the reader wanted whole is
    /// higher than the cost of leaving one long reply unfolded, so a normal thorough answer — a few
    /// hundred words — is untouched, and only genuine walls fold.
    private static let charThreshold = 2800
    private static let lineThreshold = 40
    /// How much stays on screen. Enough to tell what the answer says and decide whether to open it.
    private static let previewLines = 14

    private var isWall: Bool {
        text.count > Self.charThreshold || text.count(where: { $0 == "\n" }) > Self.lineThreshold
    }

    var body: some View {
        if !isWall {
            markdown(text)
        } else {
            VStack(alignment: .leading, spacing: 6) {
                markdown(Self.preview(of: text))
                    // The fade says "there is more here" without a label, and stops the preview from
                    // ending on a hard cut mid-sentence that reads like the response was truncated.
                    .mask(
                        LinearGradient(
                            stops: [.init(color: .black, location: 0),
                                    .init(color: .black, location: 0.82),
                                    .init(color: .black.opacity(0), location: 1)],
                            startPoint: .top, endPoint: .bottom
                        )
                    )
                    .allowsHitTesting(false) // the fade lies about where the text ends; don't offer selection there
                expandButton
            }
            .sheet(isPresented: $showingSheet) {
                ProseSheet(text: text, title: Self.title(of: text), palette: palette) { showingSheet = false }
            }
        }
    }

    private func markdown(_ s: String) -> some View {
        ChatMarkdownView(text: s, palette: palette).lineSpacing(lineSpacing)
    }

    private var expandButton: some View {
        Button { showingSheet = true } label: {
            HStack(spacing: 6) {
                Image(systemName: "text.alignleft").font(.caption)
                Text("Read full response").font(.caption.weight(.semibold))
                Text("·").font(.caption).foregroundStyle(palette.mutedForeground)
                Text(Self.size(of: text)).font(.caption).foregroundStyle(palette.mutedForeground)
                Image(systemName: "chevron.right").font(.caption2.weight(.semibold))
            }
            .foregroundStyle(palette.primary)
            .padding(.horizontal, 10).padding(.vertical, 6)
            .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.45)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Read the full response, \(Self.size(of: text))")
    }

    // MARK: - Text measures

    /// The first `previewLines` lines, kept safe to render on its own.
    ///
    /// Cutting at an arbitrary line can land INSIDE a fenced code block, and a prefix with an
    /// unbalanced fence renders as an unterminated block that swallows everything after it — the
    /// preview would look like a code dump of the rest of the answer. Counting the fences and
    /// closing an odd one costs a line and removes that entire failure.
    static func preview(of text: String) -> String {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        guard lines.count > previewLines else { return text }
        var head = Array(lines.prefix(previewLines))
        let fences = head.count { $0.trimmingCharacters(in: .whitespaces).hasPrefix("```") }
        if fences % 2 == 1 { head.append("```") }
        return head.joined(separator: "\n")
    }

    /// The sheet's title: the answer's own first heading when it has one, since a response that
    /// opens with "## Migration plan" is better named by that than by a generic word.
    static func title(of text: String) -> String {
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.hasPrefix("#") else { continue }
            let heading = t.drop(while: { $0 == "#" }).trimmingCharacters(in: .whitespaces)
            if !heading.isEmpty { return String(heading.prefix(60)) }
        }
        return "Response"
    }

    static func size(of text: String) -> String {
        let words = text.split(whereSeparator: { $0.isWhitespace }).count
        return words >= 1000 ? "\(words / 1000)k words" : "\(words) words"
    }
}

/// The whole response, scrollable and selectable, over the transcript.
private struct ProseSheet: View {
    let text: String
    let title: String
    let palette: OculusPalette
    let onClose: () -> Void

    var body: some View {
        OculusSheet(title: title, subtitle: CollapsibleProse.size(of: text), palette: palette, onClose: onClose) {
            ChatMarkdownView(text: text, palette: palette)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
