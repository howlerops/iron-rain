import SwiftUI

/// Measuring how much width a scrolling container actually has.
///
/// Needed wherever content sits inside a `ScrollView(.horizontal)` and should fill the viewport when
/// it is narrow but overflow (and scroll) when it is wide. That combination is not expressible with
/// frames alone:
///
///   - `.frame(maxWidth: .infinity)` resolves to the VIEWPORT width, so anything longer is clipped
///     and the scroll view has nothing to scroll — the content can never exceed its own container.
///   - `.fixedSize(horizontal: true)` gives the content its natural width, so long content scrolls
///     correctly, but short content collapses to hug its text and leaves the container half empty.
///
/// Feeding the measured width back in as a `minWidth`, with `fixedSize` for the natural width, gets
/// both: fill when short, scroll when long.
struct ViewportWidthKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

extension View {
    /// Publishes this view's width into `width`. Put it on the scrolling container, not the content —
    /// the content's width is the thing being decided.
    func measuringViewportWidth(into width: Binding<CGFloat>) -> some View {
        background(GeometryReader { geo in
            Color.clear.preference(key: ViewportWidthKey.self, value: geo.size.width)
        })
        .onPreferenceChange(ViewportWidthKey.self) { width.wrappedValue = $0 }
    }
}
