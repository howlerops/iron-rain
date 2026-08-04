import SwiftUI
import OculusKit

/// Shared chrome for every modal in the app.
///
/// Before this, each sheet invented its own: `padding(16)` here, `padding()` there, min sizes of
/// 520×440 / 540×440 / whatever, headers with slightly different type and spacing, and no consistent
/// place for search or a footer. Nothing was individually wrong, but opening two sheets in a row made
/// the app feel assembled rather than designed — and the numbers had drifted away from the
/// OculusSpace/OculusRadius scales that the rest of the UI uses.
///
/// One scaffold fixes the class of problem: a sheet declares its title, optional subtitle, optional
/// search and filter, and its content. Everything else — padding, sizing, the scrolling body, the
/// header/footer rules — is decided once, here.
struct OculusSheet<Content: View>: View {
    let title: String
    var subtitle: String? = nil
    let palette: OculusPalette

    /// Trailing controls in the header (Add, Browse…). Kept to the header so the content area is
    /// purely content and doesn't have to reserve space for actions.
    var actions: AnyView? = nil
    /// Provide to show a search field under the header.
    var search: Binding<String>? = nil
    var searchPrompt: String = "Search"
    /// Optional filter row rendered beside the search field.
    var filters: AnyView? = nil
    /// A sheet's own dismissal. Always rendered last in the header, always labelled the same.
    var onClose: (() -> Void)? = nil

    /// Set false when the content brings its own scrolling container.
    ///
    /// The body is normally a `ScrollView`, which is right for a stack of cards but makes a `List`
    /// impossible: a List inside a ScrollView has no height to size itself against, so it either
    /// collapses to nothing or scrolls within a scroll. That is the whole reason the iOS sheets in
    /// this app hand-rolled Mac-style cards instead of using the platform's list — there was no way
    /// to put a List in one.
    var scrolls: Bool = true

    /// Set false when this sheet is PUSHED onto a navigation stack rather than presented as a modal.
    ///
    /// The stack already draws the title and the way back. Drawing the scaffold header too would
    /// show the title twice and offer two different ways out of the same screen — and only one of
    /// them would run the sheet's unsaved-work guard.
    var showsHeader: Bool = true

    @ViewBuilder let content: () -> Content

    /// One size for every sheet. Modals that each pick their own dimensions make the window appear
    /// to jump as you move between them.
    private let minW: CGFloat = 560
    private let minH: CGFloat = 460

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if showsHeader { header } else if subtitle != nil { pushedSubtitle }
            if search != nil || filters != nil { controlBar }
            Divider().overlay(palette.border)
            if scrolls {
                ScrollView {
                    VStack(alignment: .leading, spacing: OculusSpace.md) {
                        content()
                    }
                    .padding(OculusSpace.lg)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else {
                content()
            }
        }
        // A minimum size is a macOS idea: sheets there are free-floating windows that would otherwise
        // open comically small. On a phone the sheet is already the width of the screen, and asking
        // for 560pt forced the content WIDER than the device and let the page scroll sideways.
        #if os(macOS)
        .frame(minWidth: minW, minHeight: minH)
        #else
        .frame(maxWidth: .infinity)
        #endif
        .background(palette.background)
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: OculusSpace.md) {
            VStack(alignment: .leading, spacing: OculusSpace.xxs) {
                Text(title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(palette.foreground)
                if let subtitle {
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: OculusSpace.md)
            if let actions { actions }
            if let onClose {
                Button("Done", action: onClose)
                    .keyboardShortcut(.defaultAction)
                    .accessibilityLabel("Close \(title)")
            }
        }
        .padding(.horizontal, OculusSpace.lg)
        .padding(.top, OculusSpace.lg)
        .padding(.bottom, OculusSpace.md)
    }

    /// A pushed screen loses the header, and with it the subtitle — which in these sheets is not
    /// decoration but the sentence saying what the thing does and where its credentials end up.
    /// It moves above the content rather than being dropped.
    private var pushedSubtitle: some View {
        Text(subtitle ?? "")
            .font(.caption)
            .foregroundStyle(palette.mutedForeground)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, OculusSpace.lg)
            .padding(.top, OculusSpace.md)
            .padding(.bottom, OculusSpace.md)
    }

    private var controlBar: some View {
        HStack(spacing: OculusSpace.sm) {
            if let search {
                SearchField(text: search, prompt: searchPrompt, palette: palette)
            }
            if let filters { filters }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, OculusSpace.lg)
        .padding(.bottom, OculusSpace.md)
    }
}

/// A search input that looks the same everywhere, with a clear button that appears only when there's
/// something to clear.
struct SearchField: View {
    @Binding var text: String
    var prompt: String = "Search"
    let palette: OculusPalette
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: OculusSpace.xs) {
            Image(systemName: "magnifyingglass")
                .font(.caption)
                .foregroundStyle(palette.mutedForeground)
                .accessibilityHidden(true)
            TextField(prompt, text: $text)
                .textFieldStyle(.plain)
                .font(.footnote)
                .focused($focused)
                .foregroundStyle(palette.foreground)
            if !text.isEmpty {
                Button {
                    text = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain)
                // An icon-only button with no label announces as a bare "Button". This one is in
                // every sheet in the app, so the omission was the app's most-repeated VoiceOver bug.
                .accessibilityLabel("Clear search")
                .sheetTapTarget()
                .transition(.opacity)
            }
        }
        .padding(.horizontal, OculusSpace.sm)
        .padding(.vertical, 5)
        .background(palette.input)
        .clipShape(OculusShape.rounded(OculusRadius.sm))
        .overlay(
            OculusShape.rounded(OculusRadius.sm)
                .strokeBorder(focused ? palette.primary.opacity(0.5) : palette.border)
        )
        .frame(maxWidth: 260)
        .animation(.easeOut(duration: 0.12), value: text.isEmpty)
        .animation(.easeOut(duration: 0.12), value: focused)
    }
}

/// A row of mutually-exclusive filter chips. Used instead of a Picker when the options benefit from
/// carrying counts — "Needs attention 2" tells you whether it's worth tapping before you tap it.
struct FilterChips<T: Hashable>: View {
    struct Option: Identifiable {
        let value: T
        let label: String
        var count: Int? = nil
        var id: String { label }
    }

    @Binding var selection: T
    let options: [Option]
    let palette: OculusPalette

    var body: some View {
        HStack(spacing: OculusSpace.xs) {
            ForEach(options) { opt in
                let active = opt.value == selection
                Button {
                    selection = opt.value
                } label: {
                    HStack(spacing: OculusSpace.xs) {
                        Text(opt.label)
                        if let c = opt.count, c > 0 {
                            Text("\(c)")
                                .font(.caption2.weight(.semibold).monospaced())
                                .opacity(0.75)
                        }
                    }
                    .font(.caption.weight(active ? .semibold : .regular))
                    .foregroundStyle(active ? palette.primaryForeground : palette.mutedForeground)
                    .padding(.horizontal, OculusSpace.sm)
                    .padding(.vertical, 4)
                    .background(active ? palette.primary : palette.input)
                    .clipShape(OculusShape.rounded(OculusRadius.pill))
                }
                .buttonStyle(.plain)
            }
        }
        .animation(.easeOut(duration: 0.14), value: selection)
    }
}

/// The empty / no-results state, so "you have nothing" and "your filter matched nothing" don't get
/// rendered three different ways in three different sheets.
struct SheetEmptyState<Actions: View>: View {
    let icon: String
    let title: String
    let message: String
    let palette: OculusPalette
    @ViewBuilder var actions: () -> Actions

    var body: some View {
        VStack(spacing: OculusSpace.md) {
            Image(systemName: icon)
                .font(.largeTitle)
                .foregroundStyle(palette.mutedForeground.opacity(0.5))
                .accessibilityHidden(true)
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(palette.foreground)
            Text(message)
                .font(.footnote)
                .foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: 380)
            actions()
                .padding(.top, OculusSpace.xs)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, OculusSpace.xxl)
    }
}

extension SheetEmptyState where Actions == EmptyView {
    init(icon: String, title: String, message: String, palette: OculusPalette) {
        self.init(icon: icon, title: title, message: message, palette: palette, actions: { EmptyView() })
    }
}

/// A bordered card — the standard container for a banner or a grouped block inside a sheet.
struct SheetCard<Content: View>: View {
    let palette: OculusPalette
    var tint: Color? = nil
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: OculusSpace.sm) {
            content()
        }
        .padding(OculusSpace.md)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.card)
        .overlay(
            OculusShape.rounded(OculusRadius.md)
                .strokeBorder(tint?.opacity(0.45) ?? palette.border)
        )
        .clipShape(OculusShape.rounded(OculusRadius.md))
    }
}

extension View {
    /// Grows a control's HIT AREA to the 44pt HIG minimum without changing how big it looks.
    ///
    /// These sheets were drawn against the Mac's control idiom — 11pt trash glyphs, `.mini` toggles —
    /// and then shipped unchanged to iPhone, where an 11pt target is roughly a quarter of the area a
    /// finger can reliably hit. The glyph stays the size the layout was designed around; only the
    /// tappable rectangle around it grows, and only where there is a finger.
    func sheetTapTarget() -> some View {
        #if os(iOS)
        return self.frame(minWidth: 44, minHeight: 44).contentShape(Rectangle())
        #else
        return self
        #endif
    }

    /// The list chrome these sheets share: the platform's grouped list, but drawn on the sheet's own
    /// background. The system grey a grouped List brings with it reads as a foreign panel dropped
    /// into a palette-themed sheet.
    func sheetListChrome(_ palette: OculusPalette) -> some View {
        #if os(iOS)
        return self
            .listStyle(.insetGrouped)
            .scrollContentBackground(.hidden)
            .background(palette.background)
        #else
        return self
        #endif
    }

    /// The platform's swipe-to-delete on a list row — routed through the SAME confirmation the row's
    /// trash button uses.
    ///
    /// The gesture was missing from every one of these sheets, because none of them used a List. The
    /// obvious way to add it back — a destructive button that deletes — would have made the swipe the
    /// one path in the app to an UNCONFIRMED delete, and what these rows remove (MCP credentials, API
    /// keys, an SSH host's forwards) cannot be re-derived from anywhere. So `stage` sets the pending
    /// item and the existing `confirmationDialog` still has the last word. Full swipe is off for the
    /// same reason: the fastest gesture shouldn't be the one that skips a step.
    func sheetSwipeDelete(_ label: String, stage: @escaping () -> Void) -> some View {
        #if os(iOS)
        return self.swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button(role: .destructive, action: stage) { Label(label, systemImage: "trash") }
        }
        #else
        return self
        #endif
    }

    /// Guards a sheet that holds unsaved typed input against an accidental swipe-to-dismiss.
    ///
    /// Every sheet in this app was a freely drag-dismissible takeover, so a stray downward swipe on
    /// an editor threw away whatever had been typed — including hand-entered API keys, which are the
    /// one thing in here that cannot be recovered by looking somewhere else. A sheet with a dirty
    /// draft now refuses the gesture; Cancel still works, and confirms.
    func sheetDraftGuard(_ dirty: Bool) -> some View {
        interactiveDismissDisabled(dirty)
    }
}
