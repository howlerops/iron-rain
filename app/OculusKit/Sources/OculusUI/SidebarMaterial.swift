import SwiftUI
#if os(macOS)
import AppKit
#endif

/// The sidebar's translucent backing.
///
/// The "floating glass" sidebar is the SYSTEM's, not ours: on macOS 26 a NavigationSplitView sidebar
/// gets its own inset, rounded, translucent material and all we have to do is not paint over it
/// (hence `.scrollContentBackground(.hidden)`). On earlier systems there is no such treatment, so the
/// same code produced a flat grey panel — which next to the rest of the app reads as unfinished, and
/// is why the app looked materially different on two Macs running the same build.
///
/// So on pre-26 systems we supply the material ourselves. `NSVisualEffectView` with the `.sidebar`
/// material is exactly what Finder and Mail use, has existed since 10.14, and vibrates with the
/// desktop behind it the same way — a deliberate approximation rather than an attempt to fake Liquid
/// Glass, which cannot be reproduced and would look worse for trying.
struct SidebarMaterial: ViewModifier {
    func body(content: Content) -> some View {
        #if os(macOS)
        // Liquid Glass is opt-in BY SDK, not by OS version. An app linked against the macOS 15 SDK
        // gets the compatibility appearance on Tahoe 26 — the system does not float its sidebar.
        //
        // So `#available(macOS 26.0, *)` alone is the wrong question, and asking it alone shipped a
        // real bug: release builds come off CI with Xcode 16.4 / MacOSX15.5.sdk, so on a Tahoe 26 Mac
        // the runtime check said "26, the system handles it", we withheld our own material, and the
        // system never supplied one either — leaving a flat grey panel where every native sidebar is
        // translucent. The compile-time arm is what distinguishes "running on 26" from "built for 26".
        #if compiler(>=6.2)
        if #available(macOS 26.0, *) {
            content // built for 26 AND running on 26: the system floats this; anything we add muddies it
        } else {
            content.background(VisualEffectBackground(material: .sidebar).ignoresSafeArea())
        }
        #else
        // Built against a pre-26 SDK. No system glass is coming at any runtime version, so always
        // supply the material ourselves.
        content.background(VisualEffectBackground(material: .sidebar).ignoresSafeArea())
        #endif
        #else
        content
        #endif
    }
}

/// A card/panel material for surfaces that should read as raised on older systems, where the app
/// otherwise looks like flat rectangles on a flat background.
struct PanelMaterial: ViewModifier {
    func body(content: Content) -> some View {
        #if os(macOS)
        // Same compile-vs-runtime distinction as SidebarMaterial above — see the note there.
        #if compiler(>=6.2)
        if #available(macOS 26.0, *) {
            content
        } else {
            content.background(VisualEffectBackground(material: .headerView).ignoresSafeArea())
        }
        #else
        content.background(VisualEffectBackground(material: .headerView).ignoresSafeArea())
        #endif
        #else
        content
        #endif
    }
}

/// True for anything rendered inside the sidebar column.
///
/// Several views appear in BOTH the sidebar and the detail pane — `DestinationHint` is in both, and
/// `ActivityView` is the Activity column as well as content. Each of them painted
/// `.background(palette.background)`, which is right in the detail (it is the window's surface) and
/// wrong in the sidebar, where it covers the material with an opaque white slab. That is why the
/// lower half of the column went white on Loops, Issues and Activity while the rail above stayed
/// translucent — two different backgrounds meeting mid-column.
///
/// A flag threaded through initialisers would work but would have to be plumbed through every call
/// site; the environment carries it to whatever the column happens to contain, including views added
/// later that nobody remembers to update.
private struct InSidebarColumnKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    var inSidebarColumn: Bool {
        get { self[InSidebarColumnKey.self] }
        set { self[InSidebarColumnKey.self] = newValue }
    }
}

extension View {
    /// Applies the sidebar material on systems that don't provide one, and marks everything inside
    /// as sidebar content so it knows not to paint its own opaque background.
    func sidebarMaterial() -> some View {
        modifier(SidebarMaterial()).environment(\.inSidebarColumn, true)
    }
    func panelMaterial() -> some View { modifier(PanelMaterial()) }

    /// An opaque surface fill that steps aside inside the sidebar column.
    ///
    /// Use in place of `.background(palette.background)` on any view that can be hosted in either
    /// place. In the detail pane it behaves exactly as before.
    @ViewBuilder
    func surfaceBackground(_ color: Color, inSidebar: Bool) -> some View {
        if inSidebar { self } else { self.background(color) }
    }
}

#if os(macOS)
/// Bridges NSVisualEffectView. `behindWindow` blending is what makes it vibrate with the desktop
/// rather than with whatever the app happens to have drawn underneath.
private struct VisualEffectBackground: NSViewRepresentable {
    let material: NSVisualEffectView.Material

    func makeNSView(context: Context) -> NSVisualEffectView {
        let v = NSVisualEffectView()
        v.material = material
        v.blendingMode = .behindWindow
        v.state = .followsWindowActiveState // dims with the window, like every native sidebar
        return v
    }

    func updateNSView(_ v: NSVisualEffectView, context: Context) {
        v.material = material
    }
}
#endif
