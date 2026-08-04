import SwiftUI

/// Iron Rain / HowlerOps brand tokens. The signature look is gold-on-black; the gold gradient is
/// the wordmark + accent treatment reused across the app, the landing site, and the terminal UI.
public enum IronRainBrand {
    public static let gold       = Color(red: 196/255.0, green: 155/255.0, blue: 33/255.0)  // #c49b21
    public static let goldBright = Color(red: 212/255.0, green: 184/255.0, blue: 32/255.0)  // #d4b820
    public static let goldLight  = Color(red: 212/255.0, green: 192/255.0, blue: 102/255.0) // #d4c066
    public static let goldDark   = Color(red: 154/255.0, green: 122/255.0, blue: 24/255.0)  // #9a7a18

    /// The signature 135° gold gradient — used to fill the wordmark and brand accents.
    ///
    /// This is the DARK-MODE gradient. Its lightest stop, `goldLight`, measures 1.82:1 on white, so
    /// on a light background the top half of the wordmark all but disappears. Prefer
    /// `goldGradient(for:)` so the lockup stays legible in both schemes.
    public static let goldGradient = LinearGradient(
        colors: [goldLight, gold, goldDark],
        startPoint: .topLeading, endPoint: .bottomTrailing
    )

    /// Light-mode stops: the same hue walk, shifted down until every stop clears AA on white.
    public static let goldGradientLight = LinearGradient(
        colors: [Color(hex: 0xA8801A), Color(hex: 0x8A6510), Color(hex: 0x6B4C0A)],
        startPoint: .topLeading, endPoint: .bottomTrailing
    )

    /// The gradient to use for `scheme`.
    public static func goldGradient(for scheme: ColorScheme) -> LinearGradient {
        scheme == .dark ? goldGradient : goldGradientLight
    }
}

/// The stylized "IRON RAIN" wordmark: uppercase, tracked, monospaced, filled with the gold
/// gradient. `size` scales the type. Matches the landing-site + terminal treatments.
public struct IronRainWordmark: View {
    @Environment(\.colorScheme) private var scheme
    private let size: CGFloat
    public init(size: CGFloat = 30) { self.size = size }
    public var body: some View {
        Text("IRON RAIN")
            .font(.system(size: size, weight: .heavy, design: .monospaced))
            .tracking(size * 0.12)
            .foregroundStyle(IronRainBrand.goldGradient(for: scheme))
            .accessibilityLabel("Iron Rain")
    }
}

/// Compact HORIZONTAL brand lockup (wolf mark + wordmark) for the top of the app's sidebar — the
/// signature that identifies the window at a glance.
public struct IronRainHeader: View {
    private let markSize: CGFloat
    private let wordSize: CGFloat
    public init(markSize: CGFloat = 22, wordSize: CGFloat = 15) {
        self.markSize = markSize; self.wordSize = wordSize
    }
    public var body: some View {
        HStack(spacing: 8) {
            Image("WolfMark").resizable().scaledToFit()
                .frame(width: markSize, height: markSize)
            IronRainWordmark(size: wordSize)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Iron Rain")
    }
}

/// Logo + wordmark lockup for landing / empty / loading surfaces.
public struct IronRainLockup: View {
    private let markSize: CGFloat
    private let wordSize: CGFloat
    public init(markSize: CGFloat = 72, wordSize: CGFloat = 30) {
        self.markSize = markSize
        self.wordSize = wordSize
    }
    public var body: some View {
        VStack(spacing: 14) {
            Image("WolfMark").resizable().scaledToFit()
                .frame(width: markSize, height: markSize)
            IronRainWordmark(size: wordSize)
        }
    }
}
