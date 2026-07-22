import SwiftUI
#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

/// A scrollable, auto-growing multiline chat input backed by the platform text view (NSTextView /
/// UITextView) so it does what SwiftUI's TextField can't: it SCROLLS once it hits the max height
/// (overflow text stays editable) and it handles Enter-to-send / Shift+Enter-newline reliably via
/// the responder chain instead of the flaky onKeyPress path. Grows from one line to `maxHeight`,
/// then scrolls. Placeholder is drawn by the caller (a SwiftUI overlay) since it stays empty text.
struct ComposerTextView: View {
    @Binding var text: String
    var maxHeight: CGFloat = 160
    var onSubmit: () -> Void
    @State private var measured: CGFloat = 34

    var body: some View {
        Representable(text: $text, maxHeight: maxHeight, height: $measured, onSubmit: onSubmit)
            .frame(height: min(max(34, measured), maxHeight))
    }
}

#if canImport(AppKit)
private struct Representable: NSViewRepresentable {
    @Binding var text: String
    var maxHeight: CGFloat
    @Binding var height: CGFloat
    var onSubmit: () -> Void

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
        // Return sends; Shift+Return inserts a newline.
        func textView(_ textView: NSTextView, doCommandBy sel: Selector) -> Bool {
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

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> UITextView {
        let tv = KeyTextView()
        tv.delegate = context.coordinator
        tv.font = .preferredFont(forTextStyle: .body)
        tv.backgroundColor = .clear
        tv.isScrollEnabled = true
        tv.textContainerInset = UIEdgeInsets(top: 7, left: 0, bottom: 7, right: 0)
        tv.textContainer.lineFragmentPadding = 0
        tv.onSubmit = onSubmit
        return tv
    }

    func updateUIView(_ tv: UITextView, context: Context) {
        (tv as? KeyTextView)?.onSubmit = onSubmit
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

/// UITextView that sends on a hardware Return (Shift+Return still inserts a newline via the default).
private final class KeyTextView: UITextView {
    var onSubmit: (() -> Void)?
    override var keyCommands: [UIKeyCommand]? {
        [UIKeyCommand(input: "\r", modifierFlags: [], action: #selector(sendCommand))]
    }
    @objc private func sendCommand() { onSubmit?() }
}
#endif
