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
        if #available(macOS 26.0, *) {
            content // the system already floats this sidebar; anything we add muddies it
        } else {
            content.background(VisualEffectBackground(material: .sidebar).ignoresSafeArea())
        }
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
        if #available(macOS 26.0, *) {
            content
        } else {
            content.background(VisualEffectBackground(material: .headerView).ignoresSafeArea())
        }
        #else
        content
        #endif
    }
}

extension View {
    /// Applies the sidebar material on systems that don't provide one.
    func sidebarMaterial() -> some View { modifier(SidebarMaterial()) }
    func panelMaterial() -> some View { modifier(PanelMaterial()) }
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
