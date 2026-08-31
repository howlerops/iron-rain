import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#elseif canImport(UIKit)
import UIKit
#endif

/// A native code editor: a platform text view (NSTextView / UITextView) with a monospaced font,
/// live syntax highlighting from `SyntaxHighlighter`, and language-server diagnostics drawn as
/// colored underlines. On macOS, hovering a symbol shows a type/doc popover (VSCode/Zed style);
/// the caret position feeds go-to-definition, and the editor can scroll to a target.
struct CodeEditor: View {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    let palette: OculusPalette
    var editable: Bool
    var diagnostics: [LSPDiagnostic] = []
    var scrollTarget: EditorTarget? = nil
    var onCaret: (Int, Int) -> Void = { _, _ in }
    var onConsumedScroll: () -> Void = {}
    var hoverProvider: (Int, Int) async -> String = { _, _ in "" }
    var completionProvider: (Int, Int) async -> [LSPCompletionItem] = { _, _ in [] }

    var body: some View {
        CodeTextView(text: $text, language: language, theme: theme, palette: palette, editable: editable,
                     diagnostics: diagnostics, scrollTarget: scrollTarget,
                     onCaret: onCaret, onConsumedScroll: onConsumedScroll,
                     hoverProvider: hoverProvider, completionProvider: completionProvider)
            .background(theme.background)
    }
}

/// Rainbow bracket ranges: each () [] {} character tagged with its nesting depth, skipping
/// brackets inside string/comment tokens (so `"("` in a string isn't colored). `tokens` is the
/// already-computed syntax token list; scanning is single-pass with a moving skip-interval index.
private func bracketRanges(_ ns: NSString, tokens: [(NSRange, CodeToken)]) -> [(NSRange, Int)] {
    let skip = tokens.filter { $0.1 == .string || $0.1 == .comment }
        .map { $0.0 }.sorted { $0.location < $1.location }
    var out: [(NSRange, Int)] = []
    var depth = 0
    var si = 0
    for i in 0..<ns.length {
        while si < skip.count && NSMaxRange(skip[si]) <= i { si += 1 }
        if si < skip.count && NSLocationInRange(i, skip[si]) { continue }
        switch ns.character(at: i) {
        case 40, 91, 123: // ( [ {
            out.append((NSRange(location: i, length: 1), depth)); depth += 1
        case 41, 93, 125: // ) ] }
            depth = max(depth - 1, 0); out.append((NSRange(location: i, length: 1), depth))
        default:
            break
        }
    }
    return out
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

// MARK: - Hover popover card

/// The floating card shown on hover: the language server's type/doc info. LSP hover is often
/// markdown with ```lang fenced code```; we strip the fences and render monospaced.
struct HoverCard: View {
    let text: String
    let theme: CodeTheme
    let palette: OculusPalette

    private var cleaned: String {
        var s = text.replacingOccurrences(of: "```swift", with: "")
        for fence in ["```go", "```typescript", "```javascript", "```python", "```rust", "```c", "```cpp", "```"] {
            s = s.replacingOccurrences(of: fence, with: "")
        }
        return s.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        ScrollView {
            Text(cleaned)
                .font(.system(size: 12, design: .monospaced))
                .foregroundStyle(palette.foreground)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(10)
        }
        .frame(width: Self.width)
        .frame(maxHeight: Self.maxHeight)
        .background(palette.card)
    }

    static let width: CGFloat = 460
    static let maxHeight: CGFloat = 300

    /// A definite, finite popover size for `text`.
    ///
    /// The card is a ScrollView, so it has no intrinsic height to hand NSPopover — which is how the
    /// hover came to ask for a window AppKit would not build. Estimating from the wrapped line count
    /// keeps a one-line type signature from getting a 300pt card while still bounding a long doc
    /// comment, and the clamp guarantees the result is always something NSWindow accepts.
    static func size(for text: String) -> NSSize {
        let charsPerLine = 62.0 // 460pt at 12pt monospaced, less the padding
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
            .reduce(0.0) { $0 + max(1, (Double($1.count) / charsPerLine).rounded(.up)) }
        let height = min(maxHeight, max(44, lines * 15 + 20))
        return NSSize(width: width, height: height)
    }
}

extension NSRect {
    /// Every component finite — a rect built from NaN or infinity is one AppKit refuses.
    var isFinite: Bool {
        origin.x.isFinite && origin.y.isFinite && size.width.isFinite && size.height.isFinite
    }
}

#if os(macOS)
/// NSTextView that forwards mouse-hover to the coordinator (for the type popover).
final class HoverTextView: NSTextView {
    var onHoverMove: ((NSPoint) -> Void)?
    var onHoverExit: (() -> Void)?
    var currentLineColor: NSColor = .clear
    private var hoverTracking: NSTrackingArea?

    /// Fills the caret's line with a subtle highlight (VSCode's current-line highlight).
    override func drawBackground(in rect: NSRect) {
        super.drawBackground(in: rect) // default background fill first
        guard currentLineColor != .clear, selectedRange().length == 0,
              let lm = layoutManager, let tc = textContainer else { return }
        let ns = string as NSString
        let caret = min(selectedRange().location, ns.length)
        let lineRange = ns.lineRange(for: NSRange(location: caret, length: 0))
        let glyphRange = lm.glyphRange(forCharacterRange: lineRange, actualCharacterRange: nil)
        var r = lm.boundingRect(forGlyphRange: glyphRange, in: tc)
        r.origin.x = 0
        r.size.width = bounds.width
        r = r.offsetBy(dx: 0, dy: textContainerInset.height)
        currentLineColor.setFill()
        r.fill()
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let t = hoverTracking { removeTrackingArea(t) }
        let t = NSTrackingArea(rect: bounds,
                               options: [.mouseMoved, .mouseEnteredAndExited, .activeInKeyWindow, .inVisibleRect],
                               owner: self, userInfo: nil)
        addTrackingArea(t)
        hoverTracking = t
    }

    override func mouseMoved(with event: NSEvent) {
        super.mouseMoved(with: event)
        onHoverMove?(convert(event.locationInWindow, from: nil))
    }

    override func mouseExited(with event: NSEvent) {
        super.mouseExited(with: event)
        onHoverExit?()
    }
}

/// A line-number gutter drawn alongside the editor, kept in sync on edit + scroll. Numbers
/// appear only on the first fragment of each source line (wrapped continuations are blank).
final class LineNumberRulerView: NSRulerView {
    private weak var tv: NSTextView?
    private let theme: CodeTheme
    private let numberFont = NSFont.monospacedSystemFont(ofSize: 10.5, weight: .regular)

    init(textView: NSTextView, theme: CodeTheme) {
        self.tv = textView
        self.theme = theme
        super.init(scrollView: textView.enclosingScrollView, orientation: .verticalRuler)
        clientView = textView
        ruleThickness = 46
        NotificationCenter.default.addObserver(self, selector: #selector(redraw),
                                               name: NSText.didChangeNotification, object: textView)
        if let cv = textView.enclosingScrollView?.contentView {
            cv.postsBoundsChangedNotifications = true
            NotificationCenter.default.addObserver(self, selector: #selector(redraw),
                                                   name: NSView.boundsDidChangeNotification, object: cv)
        }
    }
    required init(coder: NSCoder) { fatalError("init(coder:) unavailable") }
    deinit { NotificationCenter.default.removeObserver(self) }
    @objc private func redraw() { needsDisplay = true }

    override func drawHashMarksAndLabels(in rect: NSRect) {
        guard let tv, let lm = tv.layoutManager, let tc = tv.textContainer else { return }
        NSColor(theme.background).setFill(); bounds.fill()
        NSColor(theme.comment).withAlphaComponent(0.18).setFill()
        NSRect(x: bounds.maxX - 0.5, y: 0, width: 0.5, height: bounds.height).fill()

        let ns = tv.string as NSString
        let insetY = tv.textContainerInset.height
        let relativeY = convert(NSPoint.zero, from: tv).y
        let attrs: [NSAttributedString.Key: Any] = [.font: numberFont, .foregroundColor: NSColor(theme.comment)]

        func drawNumber(_ n: Int, at frag: NSRect) {
            let label = "\(n)" as NSString
            let size = label.size(withAttributes: attrs)
            let y = frag.minY + insetY + relativeY + (frag.height - size.height) / 2
            label.draw(at: NSPoint(x: ruleThickness - size.width - 6, y: y), withAttributes: attrs)
        }

        let visibleGlyphs = lm.glyphRange(forBoundingRect: tv.visibleRect, in: tc)
        let firstChar = ns.length == 0 ? 0 : lm.characterIndexForGlyph(at: visibleGlyphs.location)
        var line0 = newlineCount(ns, before: firstChar) // 0-based source line of the first visible char
        var first = true
        var glyphIndex = visibleGlyphs.location
        while glyphIndex < NSMaxRange(visibleGlyphs) {
            var eff = NSRange()
            let frag = lm.lineFragmentRect(forGlyphAt: glyphIndex, effectiveRange: &eff)
            let charIndex = lm.characterIndexForGlyph(at: eff.location)
            let atLineStart = charIndex == 0 || ns.character(at: charIndex - 1) == 10
            if atLineStart {
                if !first { line0 += 1 }
                drawNumber(line0 + 1, at: frag)
            }
            first = false
            glyphIndex = NSMaxRange(eff)
        }
        // Trailing empty line (doc empty or ends with a newline).
        let extra = lm.extraLineFragmentRect
        if extra.height > 0 {
            drawNumber(newlineCount(ns, before: ns.length) + 1, at: extra)
        }
    }

    private func newlineCount(_ ns: NSString, before idx: Int) -> Int {
        var n = 0, i = 0
        while i < idx { if ns.character(at: i) == 10 { n += 1 }; i += 1 }
        return n
    }
}

private struct CodeTextView: NSViewRepresentable {
    @Binding var text: String
    let language: CodeLanguage
    let theme: CodeTheme
    let palette: OculusPalette
    var editable: Bool
    var diagnostics: [LSPDiagnostic]
    var scrollTarget: EditorTarget?
    var onCaret: (Int, Int) -> Void
    var onConsumedScroll: () -> Void
    var hoverProvider: (Int, Int) async -> String
    var completionProvider: (Int, Int) async -> [LSPCompletionItem]

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        scroll.borderType = .noBorder

        let tv = HoverTextView(frame: NSRect(origin: .zero, size: scroll.contentSize))
        tv.minSize = NSSize(width: 0, height: 0)
        tv.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.textContainer?.containerSize = NSSize(width: scroll.contentSize.width, height: CGFloat.greatestFiniteMagnitude)
        tv.textContainer?.widthTracksTextView = true
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
        tv.usesFindBar = true                 // ⌘F opens the native find/replace bar
        tv.isIncrementalSearchingEnabled = true
        tv.currentLineColor = NSColor(theme.plain).withAlphaComponent(0.06)
        tv.string = text
        tv.onHoverMove = { [weak tv] p in if let tv { context.coordinator.hoverMoved(tv, to: p) } }
        tv.onHoverExit = { context.coordinator.hoverExited() }
        scroll.documentView = tv

        // Line-number gutter.
        let ruler = LineNumberRulerView(textView: tv, theme: theme)
        scroll.verticalRulerView = ruler
        scroll.hasVerticalRuler = true
        scroll.rulersVisible = true

        context.coordinator.highlight(tv)
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? HoverTextView else { return }
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
        private var hoverTask: Task<Void, Never>?
        private var popover: NSPopover?
        private var shownRange: NSRange?
        init(_ p: CodeTextView) { parent = p }

        func textDidChange(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            parent.text = tv.string
            hideHover()
            highlight(tv)
            scheduleCompletion(tv)
        }

        func textViewDidChangeSelection(_ notification: Notification) {
            guard let tv = notification.object as? NSTextView else { return }
            tv.needsDisplay = true // repaint the current-line highlight
            let (line, char) = lineChar(forOffset: tv.selectedRange().location, in: tv.string as NSString)
            parent.onCaret(line, char)
        }

        // MARK: autocomplete (native NSTextView completion list, fed by the language server)

        private var completionTask: Task<Void, Never>?
        private var pendingCompletions: [LSPCompletionItem] = []

        /// After a keystroke, if the char before the caret is an identifier char or ".", fetch
        /// completions and trigger the native completion list. Debounced.
        func scheduleCompletion(_ tv: NSTextView) {
            let ns = tv.string as NSString
            let caret = tv.selectedRange().location
            guard caret > 0, caret <= ns.length else { pendingCompletions = []; return }
            let prev = ns.character(at: caret - 1)
            let isIdent = (prev >= 48 && prev <= 57) || (prev >= 65 && prev <= 90) || (prev >= 97 && prev <= 122) || prev == 95 || prev == 46
            guard isIdent else { completionTask?.cancel(); return }
            let (line, col) = lineChar(forOffset: caret, in: ns)
            completionTask?.cancel()
            completionTask = Task { @MainActor [weak self, weak tv] in
                try? await Task.sleep(nanoseconds: 200_000_000)
                guard let self, let tv, !Task.isCancelled else { return }
                let items = await self.parent.completionProvider(line, col)
                guard !Task.isCancelled, !items.isEmpty, tv.selectedRange().location == caret else { return }
                self.pendingCompletions = items
                tv.complete(nil) // shows the native completion list; delegate supplies items
            }
        }

        // NSTextView asks its delegate for the completion strings; we return the LSP inserts and
        // let AppKit handle the list UI, keyboard navigation, and replacing the partial word.
        func textView(_ textView: NSTextView, completions words: [String],
                      forPartialWordRange charRange: NSRange, indexOfSelectedItem index: UnsafeMutablePointer<Int>?) -> [String] {
            guard !pendingCompletions.isEmpty else { return words }
            index?.pointee = 0
            var seen = Set<String>()
            return pendingCompletions.map { $0.insert }.filter { seen.insert($0).inserted }
        }

        // MARK: hover

        func hoverMoved(_ tv: HoverTextView, to point: NSPoint) {
            guard let lm = tv.layoutManager, let container = tv.textContainer else { return }
            let ns = tv.string as NSString
            guard ns.length > 0 else { hideHover(); return }
            let inset = tv.textContainerInset
            let p = NSPoint(x: point.x - inset.width, y: point.y - inset.height)
            var frac: CGFloat = 0
            let glyph = lm.glyphIndex(for: p, in: container, fractionOfDistanceThroughGlyph: &frac)
            let charIndex = lm.characterIndexForGlyph(at: glyph)
            guard charIndex < ns.length else { hoverTask?.cancel(); return }
            // Only trigger when the pointer is actually over the glyph (not trailing whitespace).
            let glyphRect = lm.boundingRect(forGlyphRange: NSRange(location: glyph, length: 1), in: container)
                .offsetBy(dx: inset.width, dy: inset.height)
            guard glyphRect.contains(point) else { hoverTask?.cancel(); return }
            let ch = ns.character(at: charIndex)
            if let scalar = Unicode.Scalar(ch), CharacterSet.whitespacesAndNewlines.contains(scalar) {
                hoverTask?.cancel(); return
            }
            if let sr = shownRange, NSLocationInRange(charIndex, sr) { return } // same token already shown

            let (line, col) = lineChar(forOffset: charIndex, in: ns)
            hoverTask?.cancel()
            hoverTask = Task { [weak self, weak tv] in
                try? await Task.sleep(nanoseconds: 300_000_000) // debounce like VSCode
                guard let self, let tv, !Task.isCancelled else { return }
                let info = await self.parent.hoverProvider(line, col)
                guard !Task.isCancelled, !info.isEmpty else { return }
                self.showHover(info, at: glyphRect, in: tv, charIndex: charIndex)
            }
        }

        func hoverExited() { hoverTask?.cancel(); hideHover() }

        private func showHover(_ text: String, at rect: NSRect, in tv: NSTextView, charIndex: Int) {
            // Hovering a symbol used to CRASH the app: SIGABRT out of
            // -[NSWindow _initContent:styleMask:backing:defer:contentView:], raised while NSPopover
            // built its window. Three separate ways to hand AppKit a window it refuses to make, all
            // closed here — the popover is the only thing standing between an LSP reply and a
            // process abort, so none of them is worth leaving to chance.
            //
            // 1. The SIZE. HoverCard is a ScrollView, and a ScrollView has no intrinsic content
            //    height, so `sizingOptions = [.preferredContentSize]` could resolve to a
            //    non-finite height. An infinite content rect is exactly what NSWindow rejects.
            //    A measured, clamped size is passed instead, so the popover is never asked to be
            //    a size that cannot exist.
            // 2. The VIEW. This runs after a 300ms debounce inside a Task. The text view can be
            //    out of the window by then — closing the file, or switching session, is enough —
            //    and showing a popover relative to a window-less view is its own exception.
            // 3. The RECT. It comes from the layout manager, which can hand back a non-finite
            //    rect for glyphs it could not lay out.
            guard tv.window != nil, rect.isFinite else { return }
            hideHover()
            let pop = NSPopover()
            pop.behavior = .transient
            pop.animates = false
            let host = NSHostingController(rootView: HoverCard(text: text, theme: parent.theme, palette: parent.palette))
            pop.contentViewController = host
            pop.contentSize = HoverCard.size(for: text)
            shownRange = NSRange(location: charIndex, length: 1)
            popover = pop
            pop.show(relativeTo: rect, of: tv, preferredEdge: .maxY)
        }

        private func hideHover() {
            popover?.performClose(nil)
            popover = nil
            shownRange = nil
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
            let tokens = SyntaxHighlighter.tokens(str, language: parent.language)
            for (r, kind) in tokens where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.color(kind)), range: r)
                }
            }
            for (r, depth) in bracketRanges(ns, tokens: tokens) where NSMaxRange(r) <= full.length {
                ts.addAttribute(.foregroundColor, value: NSColor(parent.theme.bracketColor(depth)), range: r)
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
    let palette: OculusPalette
    var editable: Bool
    var diagnostics: [LSPDiagnostic]
    var scrollTarget: EditorTarget?
    var onCaret: (Int, Int) -> Void
    var onConsumedScroll: () -> Void
    var hoverProvider: (Int, Int) async -> String            // unused on iOS (no mouse hover)
    var completionProvider: (Int, Int) async -> [LSPCompletionItem] // unused on iOS for now

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
            let tokens = SyntaxHighlighter.tokens(str, language: parent.language)
            for (r, kind) in tokens where kind != .plain {
                if NSMaxRange(r) <= full.length {
                    attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.color(kind)), range: r)
                }
            }
            for (r, depth) in bracketRanges(ns, tokens: tokens) where NSMaxRange(r) <= full.length {
                attr.addAttribute(.foregroundColor, value: UIColor(parent.theme.bracketColor(depth)), range: r)
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
