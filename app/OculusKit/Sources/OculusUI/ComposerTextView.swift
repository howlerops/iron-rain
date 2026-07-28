import SwiftUI
import PDFKit
#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

/// A document attached to the next prompt — its extracted plain text, sent as a fenced block so
/// every provider sees the content (no per-adapter file handling needed).
public struct FileAttachment: Hashable {
    public let name: String
    public let text: String
    public init(name: String, text: String) { self.name = name; self.text = text }
}

/// Extracts plain text from a document URL: PDFKit for PDFs, NSAttributedString for docx/rtf/doc/
/// odt/html, and UTF-8 (lossy fallback) for text/markdown/json/csv/source. Returns nil if nothing
/// readable came out.
func extractDocumentText(from url: URL) -> String? {
    let ext = url.pathExtension.lowercased()
    if ext == "pdf" {
        if let doc = PDFDocument(url: url), let s = doc.string, !s.isEmpty { return s }
        return nil
    }
    let rich: Set<String> = ["docx", "doc", "rtf", "rtfd", "odt", "html", "htm", "webarchive"]
    if rich.contains(ext) {
        if let a = try? NSAttributedString(url: url, options: [:], documentAttributes: nil), !a.string.isEmpty {
            return a.string
        }
        return nil
    }
    if let s = try? String(contentsOf: url, encoding: .utf8), !s.isEmpty { return s }
    if let d = try? Data(contentsOf: url) { return String(decoding: d, as: UTF8.self) }
    return nil
}

/// A scrollable, auto-growing multiline chat input backed by the platform text view (NSTextView /
/// UITextView) so it does what SwiftUI's TextField can't: it SCROLLS once it hits the max height
/// (overflow text stays editable) and it handles Enter-to-send / Shift+Enter-newline reliably via
/// the responder chain instead of the flaky onKeyPress path. Grows from one line to `maxHeight`,
/// then scrolls. Placeholder is drawn by the caller (a SwiftUI overlay) since it stays empty text.
struct ComposerTextView: View {
    @Binding var text: String
    var maxHeight: CGFloat = 160
    var onSubmit: () -> Void
    // Return true to CONSUME the key (used to drive the slash-command popup: Tab completes,
    // arrows move the highlight). Return false to let the text view do the default.
    var onTab: () -> Bool = { false }
    var onMoveUp: () -> Bool = { false }
    var onMoveDown: () -> Bool = { false }
    @State private var measured: CGFloat = 34

    var body: some View {
        Representable(text: $text, maxHeight: maxHeight, height: $measured,
                      onSubmit: onSubmit, onTab: onTab, onMoveUp: onMoveUp, onMoveDown: onMoveDown)
            .frame(height: min(max(34, measured), maxHeight))
    }
}

#if canImport(AppKit)
private struct Representable: NSViewRepresentable {
    @Binding var text: String
    var maxHeight: CGFloat
    @Binding var height: CGFloat
    var onSubmit: () -> Void
    var onTab: () -> Bool = { false }
    var onMoveUp: () -> Bool = { false }
    var onMoveDown: () -> Bool = { false }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let tv = KeyTextView()
        tv.delegate = context.coordinator
        tv.isRichText = false
        tv.font = .systemFont(ofSize: NSFont.systemFontSize)
        tv.drawsBackground = false
        tv.textContainerInset = NSSize(width: 2, height: 6)
        tv.isVerticallyResizable = true
        tv.textContainer?.widthTracksTextView = true
        tv.autoresizingMask = [.width]
        // Native writing aids for the prompt box: live spell-check, autocorrect, and inline text
        // completion (⌥+Esc / F5 shows the word list; suggestions surface as you type).
        tv.isContinuousSpellCheckingEnabled = true
        tv.isAutomaticSpellingCorrectionEnabled = true
        tv.isAutomaticTextCompletionEnabled = true
        tv.isGrammarCheckingEnabled = true
        tv.isAutomaticQuoteSubstitutionEnabled = false // don't smart-quote code snippets in a prompt
        tv.isAutomaticDashSubstitutionEnabled = false
        tv.onSubmit = onSubmit

        let scroll = NSScrollView()
        scroll.documentView = tv
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.verticalScrollElasticity = .allowed
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? KeyTextView else { return }
        tv.onSubmit = onSubmit
        if tv.string != text { tv.string = text }
        DispatchQueue.main.async { recomputeHeight(tv) }
    }

    func recomputeHeight(_ tv: NSTextView) {
        guard let lm = tv.layoutManager, let tc = tv.textContainer else { return }
        lm.ensureLayout(for: tc)
        let used = lm.usedRect(for: tc).height + tv.textContainerInset.height * 2
        let h = min(max(34, used), maxHeight)
        if abs(h - height) > 0.5 { height = h }
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        let parent: Representable
        init(_ p: Representable) { parent = p }
        func textDidChange(_ note: Notification) {
            guard let tv = note.object as? NSTextView else { return }
            parent.text = tv.string
            parent.recomputeHeight(tv)
        }
        // Return sends; Shift+Return inserts a newline. Tab / ↑ / ↓ drive the slash-command popup
        // when it's open (the closures return true to consume), else fall through to defaults.
        func textView(_ textView: NSTextView, doCommandBy sel: Selector) -> Bool {
            if sel == #selector(NSResponder.insertTab(_:)) { return parent.onTab() }
            if sel == #selector(NSResponder.moveUp(_:)) { return parent.onMoveUp() }
            if sel == #selector(NSResponder.moveDown(_:)) { return parent.onMoveDown() }
            if sel == #selector(NSResponder.insertNewline(_:)) {
                let shift = NSApp.currentEvent?.modifierFlags.contains(.shift) ?? false
                if shift { return false } // let it insert a newline
                parent.onSubmit()
                return true               // consume — don't insert a newline
            }
            return false
        }
    }
}

/// NSTextView subclass carrying the submit closure (used by the delegate, kept here for clarity).
private final class KeyTextView: NSTextView {
    var onSubmit: (() -> Void)?
}
#elseif canImport(UIKit)
private struct Representable: UIViewRepresentable {
    @Binding var text: String
    var maxHeight: CGFloat
    @Binding var height: CGFloat
    var onSubmit: () -> Void
    var onTab: () -> Bool = { false }
    var onMoveUp: () -> Bool = { false }
    var onMoveDown: () -> Bool = { false }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> UITextView {
        let tv = KeyTextView()
        tv.delegate = context.coordinator
        tv.font = .preferredFont(forTextStyle: .body)
        tv.backgroundColor = .clear
        tv.isScrollEnabled = true
        tv.textContainerInset = UIEdgeInsets(top: 7, left: 0, bottom: 7, right: 0)
        tv.textContainer.lineFragmentPadding = 0
        // Native writing aids: autocorrect, spell-check, and the predictive/QuickType bar for the prompt.
        tv.autocorrectionType = .yes
        tv.spellCheckingType = .yes
        tv.smartQuotesType = .no // keep code snippets in a prompt literal
        tv.smartDashesType = .no
        tv.onSubmit = onSubmit
        tv.onTab = onTab; tv.onMoveUp = onMoveUp; tv.onMoveDown = onMoveDown
        return tv
    }

    func updateUIView(_ tv: UITextView, context: Context) {
        (tv as? KeyTextView)?.onSubmit = onSubmit
        (tv as? KeyTextView)?.onTab = onTab
        (tv as? KeyTextView)?.onMoveUp = onMoveUp
        (tv as? KeyTextView)?.onMoveDown = onMoveDown
        if tv.text != text { tv.text = text }
        recompute(tv)
    }

    func recompute(_ tv: UITextView) {
        let size = tv.sizeThatFits(CGSize(width: tv.bounds.width, height: .greatestFiniteMagnitude))
        let h = min(max(34, size.height), maxHeight)
        tv.isScrollEnabled = size.height > maxHeight
        if abs(h - height) > 0.5 { DispatchQueue.main.async { height = h } }
    }

    final class Coordinator: NSObject, UITextViewDelegate {
        let parent: Representable
        init(_ p: Representable) { parent = p }
        func textViewDidChange(_ tv: UITextView) { parent.text = tv.text; parent.recompute(tv) }
    }
}

/// UITextView that sends on a hardware Return, and drives the slash-command popup with Tab / ↑ / ↓
/// (hardware keyboard on iPad). Shift+Return still inserts a newline via the default.
private final class KeyTextView: UITextView {
    var onSubmit: (() -> Void)?
    var onTab: (() -> Bool)?
    var onMoveUp: (() -> Bool)?
    var onMoveDown: (() -> Bool)?
    override var keyCommands: [UIKeyCommand]? {
        [
            UIKeyCommand(input: "\r", modifierFlags: [], action: #selector(sendCommand)),
            UIKeyCommand(input: "\t", modifierFlags: [], action: #selector(tabCommand)),
            UIKeyCommand(input: UIKeyCommand.inputUpArrow, modifierFlags: [], action: #selector(upCommand)),
            UIKeyCommand(input: UIKeyCommand.inputDownArrow, modifierFlags: [], action: #selector(downCommand)),
        ]
    }
    @objc private func sendCommand() { onSubmit?() }
    @objc private func tabCommand() { _ = onTab?() }
    @objc private func upCommand() { _ = onMoveUp?() }
    @objc private func downCommand() { _ = onMoveDown?() }
}
#endif
