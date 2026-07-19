import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

/// A native code editor: a platform text view (NSTextView / UITextView) with a monospaced font,
/// live syntax highlighting from `SyntaxHighlighter`, and language-server diagnostics drawn as
/// colored underlines. Reports the caret position (for hover/type info) and can scroll to a
/// target (go-to-definition). Dependency-free — the highlighting backend can be swapped for
/// tree-sitter behind `SyntaxHighlighter`.
struct CodeEditor: View {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool
    var diagnostics: [LSPDiagnostic] = []
    var scrollTarget: EditorTarget? = nil
    var onCaret: (Int, Int) -> Void = { _, _ in }
    var onConsumedScroll: () -> Void = {}

    var body: some View {
        CodeTextView(text: $text, language: language, theme: theme, editable: editable,
                     diagnostics: diagnostics, scrollTarget: scrollTarget,
                     onCaret: onCaret, onConsumedScroll: onConsumedScroll)
            .background(theme.background)
    }
}

// MARK: - LSP position <-> NSString offset (0-based line/char, UTF-16, matching LSP + NSString)

private func lineStarts(_ ns: NSString) -> [Int] {
    var starts = [0]
    ns.enumerateSubstrings(in: NSRange(location: 0, length: ns.length),
                           options: [.byLines, .substringNotRequired]) { _, _, enclosing, _ in
        starts.append(enclosing.location + enclosing.length)
    }
    return starts
}

private func offsetFor(line: Int, char: Int, in ns: NSString, starts: [Int]) -> Int {
    guard line >= 0, line < starts.count else { return ns.length }
    return min(starts[line] + max(char, 0), ns.length)
}

private func lineChar(forOffset off: Int, in ns: NSString) -> (Int, Int) {
    let starts = lineStarts(ns)
    var lo = 0, hi = starts.count - 1, line = 0
    while lo <= hi { // binary search for the greatest start <= off
        let mid = (lo + hi) / 2
        if starts[mid] <= off { line = mid; lo = mid + 1 } else { hi = mid - 1 }
    }
    return (line, off - starts[line])
}

/// Maps LSP diagnostics to NSRanges + severities.
private func diagnosticRanges(_ diags: [LSPDiagnostic], in ns: NSString) -> [(NSRange, Int)] {
    guard !diags.isEmpty else { return [] }
    let starts = lineStarts(ns)
    var out: [(NSRange, Int)] = []
    for d in diags {
        let s = offsetFor(line: d.startLine, char: d.startChar, in: ns, starts: starts)
        var e = offsetFor(line: d.endLine, char: d.endChar, in: ns, starts: starts)
        if e <= s { e = min(s + 1, ns.length) }
        if s < ns.length { out.append((NSRange(location: s, length: e - s), d.severity)) }
    }
    return out
}

#if os(macOS)
private struct CodeTextView: NSViewRepresentable {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool
    var diagnostics: [LSPDiagnostic]
    var scrollTarget: EditorTarget?
    var onCaret: (Int, Int) -> Void
    var onConsumedScroll: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        guard let tv = scroll.documentView as? NSTextView else { return scroll }
        tv.delegate = context.coordinator
        tv.isRichText = false
        tv.isAutomaticQuoteSubstitutionEnabled = false
        tv.isAutomaticDashSubstitutionEnabled = false
        tv.isAutomaticTextReplacementEnabled = false
        tv.isAutomaticSpellingCorrectionEnabled = false
        tv.allowsUndo = true
        tv.font = Self.font
        tv.backgroundColor = NSColor(theme.background)
        tv.textContainerInset = NSSize(width: 8, height: 8)
        tv.string = text
        context.coordinator.highlight(tv)
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? NSTextView else { return }
        tv.isEditable = editable
        tv.isSelectable = true
        tv.backgroundColor = NSColor(theme.background)
        if tv.string != text {
            let sel = tv.selectedRange()
            tv.string = text
            tv.setSelectedRange(NSRange(location: min(sel.location, (text as NSString).length), length: 0))
        }
        context.coordinator.parent = self
        context.coordinator.highlight(tv)
        context.coordinator.applyScrollTarget(tv)
    }

    static let font = NSFont.monospacedSystemFont(ofSize: 12.5, weight: .regular)

    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: CodeTextView
        private var lastTarget: EditorTarget?
        init(_ p: CodeTextView) { parent = p }

        func textDidChange(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            parent.text = tv.string
            highlight(tv)
        }

        func textViewDidChangeSelection(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            let (line, char) = lineChar(forOffset: tv.selectedRange().location, in: tv.string as NSString)
            parent.onCaret(line, char)
        }

        func applyScrollTarget(_ tv: NSTextView) {
            guard let t = parent.scrollTarget, t != lastTarget else { return }
            lastTarget = t
            let ns = tv.string as NSString
            let off = offsetFor(line: t.line, char: t.char, in: ns, starts: lineStarts(ns))
            let r = NSRange(location: min(off, ns.length), length: 0)
            tv.setSelectedRange(r)
            tv.scrollRangeToVisible(r)
            tv.window?.makeFirstResponder(tv)
            DispatchQueue.main.async { [weak self] in self?.parent.onConsumedScroll() }
        }

        func highlight(_ tv: NSTextView) {
            guard let ts = tv.textStorage else { return }
            let str = tv.string
            let ns = str as NSString
            let full = NSRange(location: 0, length: ns.length)
            ts.beginEditing()
            ts.addAttribute(.font, value: CodeTextView.font, range: full)
            ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.plain), range: full)
            ts.removeAttribute(.underlineStyle, range: full)
            ts.removeAttribute(.underlineColor, range: full)
            for (r, kind) in SyntaxHighlighter.tokens(str, language: parent.language) where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.color(kind)), range: r)
                }
            }
            for (r, sev) in diagnosticRanges(parent.diagnostics, in: ns) where NSMaxRange(r) <= full.length {
                ts.addAttribute(.underlineStyle,
                                value: NSUnderlineStyle.thick.rawValue | NSUnderlineStyle.patternDot.rawValue, range: r)
                ts.addAttribute(.underlineColor, value: Self.severityColor(sev), range: r)
            }
            ts.endEditing()
        }

        static func severityColor(_ sev: Int) -> NSColor {
            switch sev { case 1: return .systemRed; case 2: return .systemYellow; default: return .systemGray }
        }
    }
}
#else
private struct CodeTextView: UIViewRepresentable {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool
    var diagnostics: [LSPDiagnostic]
    var scrollTarget: EditorTarget?
    var onCaret: (Int, Int) -> Void
    var onConsumedScroll: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> UITextView {
        let tv = UITextView()
        tv.delegate = context.coordinator
        tv.font = Self.font
        tv.autocorrectionType = .no
        tv.autocapitalizationType = .none
        tv.smartQuotesType = .no
        tv.backgroundColor = UIColor(theme.background)
        tv.textContainerInset = UIEdgeInsets(top: 8, left: 8, bottom: 8, right: 8)
        tv.text = text
        context.coordinator.highlight(tv)
        return tv
    }

    func updateUIView(_ tv: UITextView, context: Context) {
        tv.isEditable = editable
        tv.backgroundColor = UIColor(theme.background)
        if tv.text != text {
            tv.text = text
        }
        context.coordinator.parent = self
        context.coordinator.highlight(tv)
        context.coordinator.applyScrollTarget(tv)
    }

    static let font = UIFont.monospacedSystemFont(ofSize: 13, weight: .regular)

    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: CodeTextView
        private var lastTarget: EditorTarget?
        init(_ p: CodeTextView) { parent = p }

        func textViewDidChange(_ tv: UITextView) {
            parent.text = tv.text
            highlight(tv)
        }

        func textViewDidChangeSelection(_ tv: UITextView) {
            let (line, char) = lineChar(forOffset: tv.selectedRange.location, in: (tv.text ?? "") as NSString)
            parent.onCaret(line, char)
        }

        func applyScrollTarget(_ tv: UITextView) {
            guard let t = parent.scrollTarget, t != lastTarget else { return }
            lastTarget = t
            let ns = (tv.text ?? "") as NSString
            let off = offsetFor(line: t.line, char: t.char, in: ns, starts: lineStarts(ns))
            let r = NSRange(location: min(off, ns.length), length: 0)
            tv.selectedRange = r
            tv.scrollRangeToVisible(r)
            DispatchQueue.main.async { [weak self] in self?.parent.onConsumedScroll() }
        }

        func highlight(_ tv: UITextView) {
            let str = tv.text ?? ""
            let ns = str as NSString
            let full = NSRange(location: 0, length: ns.length)
            let attr = NSMutableAttributedString(string: str)
            attr.addAttribute(.font, value: CodeTextView.font, range: full)
            attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.plain), range: full)
            for (r, kind) in SyntaxHighlighter.tokens(str, language: parent.language) where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.color(kind)), range: r)
                }
            }
            for (r, sev) in diagnosticRanges(parent.diagnostics, in: ns) where NSMaxRange(r) <= full.length {
                attr.addAttribute(.underlineStyle,
                                  value: NSUnderlineStyle.thick.rawValue | NSUnderlineStyle.patternDot.rawValue, range: r)
                attr.addAttribute(.underlineColor, value: Self.severityColor(sev), range: r)
            }
            let sel = tv.selectedRange
            tv.attributedText = attr
            tv.selectedRange = sel
        }

        static func severityColor(_ sev: Int) -> UIColor {
            switch sev { case 1: return .systemRed; case 2: return .systemYellow; default: return .systemGray }
        }
    }
}
#endif
