import SwiftUI
#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

/// A native code editor: a platform text view (NSTextView / UITextView) with a monospaced font
/// and live syntax highlighting from `SyntaxHighlighter`. Editable or read-only. Dependency-free
/// — the highlighting backend can be swapped for tree-sitter behind `SyntaxHighlighter`.
struct CodeEditor: View {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool

    var body: some View {
        CodeTextView(text: $text, language: language, theme: theme, editable: editable)
            .background(theme.background)
    }
}

#if os(macOS)
private struct CodeTextView: NSViewRepresentable {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool

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
    }

    static let font = NSFont.monospacedSystemFont(ofSize: 12.5, weight: .regular)

    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: CodeTextView
        init(_ p: CodeTextView) { parent = p }

        func textDidChange(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            parent.text = tv.string
            highlight(tv)
        }

        func highlight(_ tv: NSTextView) {
            guard let ts = tv.textStorage else { return }
            let str = tv.string
            let full = NSRange(location: 0, length: (str as NSString).length)
            ts.beginEditing()
            ts.addAttribute(.font, value: CodeTextView.font, range: full)
            ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.plain), range: full)
            for (r, kind) in SyntaxHighlighter.tokens(str, language: parent.language) where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.color(kind)), range: r)
                }
            }
            ts.endEditing()
        }
    }
}
#else
private struct CodeTextView: UIViewRepresentable {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    var editable: Bool

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
    }

    static let font = UIFont.monospacedSystemFont(ofSize: 13, weight: .regular)

    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: CodeTextView
        init(_ p: CodeTextView) { parent = p }

        func textViewDidChange(_ tv: UITextView) {
            parent.text = tv.text
            highlight(tv)
        }

        func highlight(_ tv: UITextView) {
            let str = tv.text ?? ""
            let full = NSRange(location: 0, length: (str as NSString).length)
            let attr = NSMutableAttributedString(string: str)
            attr.addAttribute(.font, value: CodeTextView.font, range: full)
            attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.plain), range: full)
            for (r, kind) in SyntaxHighlighter.tokens(str, language: parent.language) where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.color(kind)), range: r)
                }
            }
            let sel = tv.selectedRange
            tv.attributedText = attr
            tv.selectedRange = sel
        }
    }
}
#endif
