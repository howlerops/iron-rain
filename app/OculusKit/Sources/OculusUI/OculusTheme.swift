import SwiftUI

/// The Oculus/HowlerOps palette, migrated from linear-orchestrator's design system
/// (`apps/mobile/src/theme/colors.ts`). Amber (`#d9a520`) on black — a sparse, dark,
/// session-first command surface. Use `OculusPalette.current(scheme)` in views.
public struct OculusPalette: Equatable {
    public let background: Color
    public let foreground: Color
    public let card: Color
    public let cardForeground: Color
    public let primary: Color
    public let primaryForeground: Color
    public let secondary: Color
    public let muted: Color
    public let mutedForeground: Color
    public let accent: Color
    public let accentForeground: Color
    public let destructive: Color
    public let border: Color
    public let input: Color

    /// Gold for TYPE and thin strokes, as opposed to `primary` which is gold for FILLS.
    ///
    /// Identical to `primary` in dark mode (gold on black reads at 9.35:1). In light mode it darkens,
    /// because gold type on white is 2.25:1 — genuinely unreadable — while a gold *fill* on white is
    /// fine so long as what sits on it is dark. Keeping the two apart is what lets the brand survive
    /// the contrast fix instead of being sacrificed to it.
    public let primaryText: Color

    // Semantic status colors. These were previously hardcoded at ~37 call sites across 11 files —
    // including TWO different greens for "ok" — none of which were contrast-checked in light mode.
    // They belong here beside `primary`/`destructive` because they carry meaning, not decoration,
    // and therefore have to respond to appearance like everything else.
    public let success: Color
    public let warning: Color
    public let info: Color
    public let conflict: Color
    public let diffAdded: Color
    public let diffRemoved: Color

    /// Reserved brand gold — HowlerOps `--primary` oklch(0.7516 0.1469 84) = #D9A520 (goldenrod).
    ///
    /// Used unchanged in BOTH schemes, because it is a fill: near-black on this gold is 9.35:1 in
    /// either appearance. Only gold *type* on a light ground needs a different value — that is
    /// `brandGoldText`. Do not darken this to satisfy a contrast checker; darken the pairing instead.
    public static let brandGold = Color(hex: 0xD9A520)

    /// Gold that is legible AS TEXT on a light background.
    ///
    /// Use this ONLY for gold-coloured type or hairline strokes on a light ground — never as a fill.
    /// The brand gold is a FILL colour: `brandGold` behind near-black text measures 9.35:1 and is
    /// perfectly accessible, which is exactly how dark mode has always used it. The earlier fix
    /// darkened `primary` itself to chase a text-contrast number, which passed the audit and turned
    /// every gold button, pill and selected row in light mode into olive-brown — solving a real
    /// problem by destroying the brand. The colour was never wrong; the FOREGROUND paired with it was.
    public static let brandGoldText = Color(hex: 0x8A6510)

    public static let dark = OculusPalette(
        background: Color(hex: 0x000000),
        foreground: Color(hex: 0xE2E2E2),
        card: Color(hex: 0x1C1C1C),
        cardForeground: Color(hex: 0xF0F0F0),
        primary: brandGold, // reserved for state: selection, running, actions
        primaryForeground: Color(hex: 0x000000),
        secondary: Color(hex: 0x242424),
        muted: Color(hex: 0x333333),
        mutedForeground: Color(hex: 0xADADAD),
        accent: Color(hex: 0x3A2E17),
        accentForeground: Color(hex: 0xE9C34A),
        destructive: Color(hex: 0xE5484D),
        border: Color(hex: 0x2A2A2A),
        input: Color(hex: 0x171717),
        primaryText: brandGold, // gold on black is 9.35:1 — the brand gold works as type here
        success: Color(hex: 0x3FB950),
        warning: Color(hex: 0xE0912A),
        info: Color(hex: 0x58A6FF),
        conflict: Color(hex: 0xA071D6),
        diffAdded: Color(hex: 0x2EA043),
        diffRemoved: Color(hex: 0xF85149)
    )

    public static let light = OculusPalette(
        background: Color(hex: 0xFFFFFF),
        foreground: Color(hex: 0x000000),
        card: Color(hex: 0xF7F7F7),
        cardForeground: Color(hex: 0x1F1F1F),
        primary: brandGold, // THE brand gold, both schemes — it is a fill, and it is not the problem
        // Near-black on gold, exactly as dark mode does it: 9.35:1. White on gold was 2.25:1, and
        // white-on-gold was the actual accessibility failure here, not the gold.
        primaryForeground: Color(hex: 0x1A1A1A),
        secondary: Color(hex: 0xF0F0F0),
        muted: Color(hex: 0xE2E2E2),
        mutedForeground: Color(hex: 0x5F5F5F), // was 0x6B6B6B (4.28:1) — now 5.31:1 on white
        accent: Color(hex: 0xFDF5E3),
        accentForeground: Color(hex: 0x7A5A08), // was 0xB8860B (3.00:1 on accent) — now ~6.1:1
        destructive: Color(hex: 0xC0201C), // was 0xDC2626 (4.06:1) — now 5.24:1 on white
        border: Color(hex: 0xD2D6DC), // was 0xE5E5E5 (1.26:1) — separators need 3:1 to read
        input: Color(hex: 0xF4F4F4),
        primaryText: brandGoldText, // gold type on white is 2.25:1; this is the same hue at 5.4:1
        success: Color(hex: 0x1A7F43),
        warning: Color(hex: 0x8A5A0B),
        info: Color(hex: 0x0A5BC4),
        conflict: Color(hex: 0x6D3FA8),
        diffAdded: Color(hex: 0x1A7F43),
        diffRemoved: Color(hex: 0xC0201C)
    )

    public static func current(_ scheme: ColorScheme) -> OculusPalette {
        scheme == .dark ? .dark : .light
    }
}

/// User appearance preference, persisted via `@AppStorage("oculus.appearance")`. `.system`
/// follows the OS; light/dark force a scheme via `.preferredColorScheme`.
public enum Appearance: Int, CaseIterable, Identifiable {
    case system, light, dark
    public var id: Int { rawValue }
    public var label: String {
        switch self {
        case .system: return "System"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }
    public var symbol: String {
        switch self {
        case .system: return "circle.lefthalf.filled"
        case .light: return "sun.max"
        case .dark: return "moon"
        }
    }
    /// nil = follow the system; otherwise force the scheme.
    public var colorScheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}

/// A single spacing scale for the whole app, so padding/gaps stop being hand-picked magic numbers
/// (which made left edges jitter as you scanned). Use these everywhere instead of literals.
public enum OculusSpace {
    public static let xxs: CGFloat = 2
    public static let xs: CGFloat = 4
    public static let sm: CGFloat = 8
    public static let md: CGFloat = 12
    public static let lg: CGFloat = 16 // the standard content gutter — every full-width container uses this
    public static let xl: CGFloat = 24
    public static let xxl: CGFloat = 32

    /// The widest a column of reading text may get, however wide the window is.
    ///
    /// Prose set across a 2000pt monitor runs to ~200 characters a line, and the eye loses its place
    /// on the return sweep — you re-read the line you just finished. Typography's answer is roughly
    /// 45–90 characters; this sits near the top of that range, wide enough that a tool card's
    /// monospaced output and a diff still have room (they scroll horizontally INSIDE the column
    /// rather than stretching it).
    public static let readableMeasure: CGFloat = 820

    /// The widest a dense TABLE may get before its columns stop reading as one row.
    ///
    /// A table's metadata columns (provider, status, cost, age) are fixed-width, so every extra point
    /// of window goes to the name column and nowhere else. On a maximised 27" display that puts a
    /// session's status a hand's width from its name, and you lose the row on the way across. This is
    /// `rowMeasure` of name plus the ~370pt of fixed columns and their gutters.
    public static let tableMeasure: CGFloat = 1040

    /// The measure a CHAT transcript should take in a pane of `available` width.
    ///
    /// A fixed 820pt cap answers only half the question. It keeps lines readable, but on a maximised
    /// 27" display it also leaves most of the pane empty, and a transcript is not only prose: code
    /// blocks, diffs, tables and generative-UI cards all have a natural width well past a paragraph's
    /// and are the things that suffer first when the column is narrow. So the measure grows with the
    /// pane instead of being frozen at the width of a phone-sized argument.
    ///
    /// Growth is proportional and clamped at both ends: never below `readableMeasure` (a pane that
    /// small should just use what it has), never above `maxChatMeasure` (past which prose really does
    /// run too long). The fraction leaves a visible margin on both sides at every size, so the column
    /// still reads as a column rather than as text jammed against the window edges.
    public static func chatMeasure(in available: CGFloat) -> CGFloat {
        guard available > 0 else { return readableMeasure }
        return min(max(readableMeasure, available * chatWidthFraction), maxChatMeasure)
    }

    /// How much of a wide pane the transcript takes. The remaining ~26% becomes the two margins.
    private static let chatWidthFraction: CGFloat = 0.74

    /// Where the transcript stops widening. Beyond this a paragraph exceeds ~110 characters a line
    /// and the return sweep starts costing re-reads, which is the problem the measure exists to stop.
    public static let maxChatMeasure: CGFloat = 1240

    /// The widest a LIST ROW's content may get when the row ends in an action button.
    ///
    /// Distinct from `readableMeasure`, and much narrower, because the failure here is not line
    /// length — it's association. A two-line row whose trailing "Continue" sits 1200pt from the name
    /// it belongs to has no visible link between the two, and the eye has to travel the full width to
    /// re-pair them. Capping the row's CONTENT (never its tap target, which stays full-bleed) keeps
    /// the label and its button in one glance. Wider than any phone, so narrow layouts are untouched.
    public static let rowMeasure: CGFloat = 560
}

public extension View {
    /// Caps a view at a measure and places it in the available width, so a maximised window gains
    /// margins instead of longer lines. Inert below the cap — every phone and narrow split view lays
    /// out exactly as it did before.
    ///
    /// Applied to the transcript CONTENT rather than the scroll view itself: the scroll view stays
    /// full-bleed so its background is continuous and its scrollbar sits at the window edge where
    /// people reach for it. Every sibling pinned under the transcript (the typing bar, the composer)
    /// takes the same modifier AND the same measure, or the column visibly steps out at the bottom
    /// of the window.
    ///
    /// CENTRED, always. An earlier revision anchored this to the leading edge, reasoning that the
    /// sidebar already owns the left of the window so a centred column sits right of the window's
    /// true centre. That is arithmetically true and completely wrong in practice: the pane is the
    /// frame you read within, every chat surface people know centres inside it, and anchoring left
    /// simply moved all the empty space into one conspicuous slab on the right — which reads as a
    /// layout that failed rather than as a margin. The `alignment` parameter is kept for the few
    /// surfaces that genuinely are leading-aligned lists, but content columns take the default.
    func readableColumn(_ measure: CGFloat = OculusSpace.readableMeasure,
                        alignment: Alignment = .center) -> some View {
        frame(maxWidth: measure, alignment: alignment)
            .frame(maxWidth: .infinity, alignment: .center)
    }
}

/// A single corner-radius scale (was 6/8/10/12/16/18 scattered ad-hoc).
public enum OculusRadius {
    public static let sm: CGFloat = 8   // chips, small controls, tool cards
    public static let md: CGFloat = 12  // message bubbles, panels
    public static let lg: CGFloat = 16  // large cards / sheets
    public static let pill: CGFloat = 999
}

/// Rounded shapes for the whole app.
///
/// Every `RoundedRectangle` in the codebase used the default `.circular` corner style, which is a
/// mathematically different curve from the one every native control, sheet and window uses. At 143
/// call sites that reads as a consistent, subtle wrongness next to system UI rather than as any one
/// visible bug. `.continuous` is the squircle Apple actually draws, and it is available well below
/// this app's deployment floor — there is no reason not to use it everywhere.
///
/// On OS 26 the system's own containers additionally became *concentric*: a nested shape's radius is
/// derived from its parent's minus the padding between them, so corners stay parallel instead of
/// flaring. `ConcentricRectangle` does that automatically but is 26.0-only, so `concentricInner`
/// below computes the same relationship for the platforms this app still supports.
///
/// Note for anyone following the WWDC25 session: it demonstrates `containerConcentric`, which is NOT
/// in the shipping SDK — it was renamed to `.concentric` before release, so transcript code will not
/// compile.
public enum OculusShape {
    /// The app's standard rounded rectangle. Always continuous.
    public static func rounded(_ radius: CGFloat) -> RoundedRectangle {
        RoundedRectangle(cornerRadius: radius, style: .continuous)
    }

    /// The radius an inner shape must use to stay concentric inside `outer` across `padding`.
    ///
    /// Clamped at 0 — once the padding exceeds the parent radius the correct corner is square, not
    /// a negative radius. Use this instead of picking an inner radius by eye: a fixed inner radius
    /// inside a differently-rounded parent is what produces visibly "flared" corners.
    public static func concentricInner(outer: CGFloat, padding: CGFloat) -> CGFloat {
        max(outer - padding, 0)
    }

    /// The concentric inner shape for a container of `outer` radius inset by `padding`.
    public static func concentric(outer: CGFloat, padding: CGFloat) -> RoundedRectangle {
        rounded(concentricInner(outer: outer, padding: padding))
    }
}

extension View {
    /// No autocapitalization/autocorrect for technical fields — paths, keys, commands, URLs, branch
    /// names. No-op on macOS.
    ///
    /// This existed already but was `private` to AgentsView, so every other technical field in the
    /// app autocorrected: an `ANTHROPIC_API_KEY` typed into the account editor came out as
    /// `Anthropic_api_key`, and the workspace-name field — which becomes a git branch — capitalized.
    public func plainInput() -> some View {
        #if os(iOS)
        return self.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return self
        #endif
    }
}

/// User chat-typeface preference, persisted via `@AppStorage("oculus.chatFontDesign")`. Drives the
/// transcript's reading font (assistant/user/thinking text) so the chat can feel like a document
/// (serif), a terminal (mono), softer (rounded), or the platform default.
public enum ChatFontDesign: Int, CaseIterable, Identifiable {
    case system, rounded, serif, mono
    public var id: Int { rawValue }
    /// Display order for the picker — SF sans first (the right default for a dev tool), mono next
    /// (common for reading code), then the niche rounded/serif. Kept separate from the raw case order
    /// so persisted preferences (stored by rawValue) don't shift when we reorder the menu.
    public static var displayOrder: [ChatFontDesign] { [.system, .mono, .rounded, .serif] }
    public var label: String {
        switch self {
        case .system: return "Reading (default)"
        case .rounded: return "Rounded"
        case .serif: return "Serif"
        case .mono: return "Monospaced"
        }
    }
    public var symbol: String {
        switch self {
        case .system: return "textformat"
        case .rounded: return "textformat.alt"
        case .serif: return "textformat.size"
        case .mono: return "chevron.left.forwardslash.chevron.right"
        }
    }
    public var design: Font.Design {
        switch self {
        case .system: return .default
        case .rounded: return .rounded
        case .serif: return .serif
        case .mono: return .monospaced
        }
    }

    /// The face for AGENT RESPONSE text, as opposed to UI chrome and your own messages.
    ///
    /// Claude's web app sets `--font-claude-response` to its SERIF family while UI and user messages
    /// stay sans — a deliberate split that does most of the work of making a long answer read like
    /// prose instead of like console output. The default option mirrors it; an explicit pick
    /// (Monospaced, Rounded, Serif) is a choice about the whole transcript and is applied uniformly.
    public var responseDesign: Font.Design {
        switch self {
        case .system: return .serif
        default: return design
        }
    }

    /// Point size for response text. New York's x-height is ~10% below SF Pro's at the same nominal
    /// size (measured from the outlines, not the OS/2 table, which is unreliable on variable fonts),
    /// so serif needs a point or two to read at the same apparent size.
    public func responseSize(_ base: CGFloat) -> CGFloat {
        responseDesign == .serif ? base + 2 : base
    }
}

/// User chat text-size preference, persisted via `@AppStorage("oculus.chatFontScale")` as a raw
/// multiplier. A small closed set keeps the type ramp coherent (we scale every role by one factor).
public enum ChatFontScale: Int, CaseIterable, Identifiable {
    case small, standard, large, xlarge
    public var id: Int { rawValue }
    public var label: String {
        switch self {
        case .small: return "Small"
        case .standard: return "Standard"
        case .large: return "Large"
        case .xlarge: return "Extra Large"
        }
    }
    public var factor: CGFloat {
        switch self {
        case .small: return 0.9
        case .standard: return 1.0
        case .large: return 1.15
        case .xlarge: return 1.3
        }
    }
}

extension Color {
    /// 0xRRGGBB integer initializer.
    public init(hex: UInt32, alpha: Double = 1.0) {
        self.init(
            .sRGB,
            red: Double((hex >> 16) & 0xFF) / 255.0,
            green: Double((hex >> 8) & 0xFF) / 255.0,
            blue: Double(hex & 0xFF) / 255.0,
            opacity: alpha
        )
    }
}
