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

    /// Reserved brand gold — HowlerOps `--primary` oklch(0.7516 0.1469 84) = #D9A520
    /// (goldenrod). The same gold is used in both schemes; only backgrounds/text invert.
    public static let brandGold = Color(hex: 0xD9A520)

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
        input: Color(hex: 0x171717)
    )

    public static let light = OculusPalette(
        background: Color(hex: 0xFFFFFF),
        foreground: Color(hex: 0x000000),
        card: Color(hex: 0xF7F7F7),
        cardForeground: Color(hex: 0x1F1F1F),
        primary: brandGold, // reserved for state: selection, running, actions
        primaryForeground: Color(hex: 0xFFFFFF),
        secondary: Color(hex: 0xF0F0F0),
        muted: Color(hex: 0xE2E2E2),
        mutedForeground: Color(hex: 0x6B6B6B),
        accent: Color(hex: 0xFDF5E3),
        accentForeground: Color(hex: 0xB8860B),
        destructive: Color(hex: 0xDC2626),
        border: Color(hex: 0xE5E5E5),
        input: Color(hex: 0xF4F4F4)
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

/// User chat-typeface preference, persisted via `@AppStorage("oculus.chatFontDesign")`. Drives the
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
}

/// A single corner-radius scale (was 6/8/10/12/16/18 scattered ad-hoc).
public enum OculusRadius {
    public static let sm: CGFloat = 8   // chips, small controls, tool cards
    public static let md: CGFloat = 12  // message bubbles, panels
    public static let lg: CGFloat = 16  // large cards / sheets
    public static let pill: CGFloat = 999
}

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
        case .system: return "Sans (default)"
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
