import SwiftUI

/// The Oculus/HowlerOps palette, migrated from linear-orchestrator's design system
/// (`apps/mobile/src/theme/colors.ts`). Amber (`#d9a520`) on black — a sparse, dark,
/// session-first command surface. Use `OculusPalette.current(scheme)` in views.
public struct OculusPalette {
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

    public static let dark = OculusPalette(
        background: Color(hex: 0x000000),
        foreground: Color(hex: 0xE5E5E5),
        card: Color(hex: 0x1F1F1F),
        cardForeground: Color(hex: 0xF2F2F2),
        primary: Color(hex: 0xE3B65B), // brand gold (dark) — reserved for state: selection, running, actions
        primaryForeground: Color(hex: 0x000000),
        secondary: Color(hex: 0x252525),
        muted: Color(hex: 0x333333),
        mutedForeground: Color(hex: 0xB3B3B3),
        accent: Color(hex: 0x3A2D10),
        accentForeground: Color(hex: 0xF0C441),
        destructive: Color(hex: 0xDC2626),
        border: Color(hex: 0x2D2D2D),
        input: Color(hex: 0x1C1C1C)
    )

    public static let light = OculusPalette(
        background: Color(hex: 0xFFFFFF),
        foreground: Color(hex: 0x000000),
        card: Color(hex: 0xF7F7F7),
        cardForeground: Color(hex: 0x242424),
        primary: Color(hex: 0xB8842A), // brand gold (light) — reserved for state: selection, running, actions
        primaryForeground: Color(hex: 0xFFFFFF),
        secondary: Color(hex: 0xF0F0F0),
        muted: Color(hex: 0xE6E6E6),
        mutedForeground: Color(hex: 0x666666),
        accent: Color(hex: 0xFFF7DB),
        accentForeground: Color(hex: 0x9A6B00),
        destructive: Color(hex: 0xDC2626),
        border: Color(hex: 0xE5E5E5),
        input: Color(hex: 0xF4F4F4)
    )

    public static func current(_ scheme: ColorScheme) -> OculusPalette {
        scheme == .dark ? .dark : .light
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
