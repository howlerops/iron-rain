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
    /// Content is placed inside a scroll view unless the sheet manages its own (a List, say).
    var scrolls: Bool = true

    @ViewBuilder let content: () -> Content

    /// One size for every sheet. Modals that each pick their own dimensions make the window appear
    /// to jump as you move between them.
    private let minW: CGFloat = 560
    private let minH: CGFloat = 460

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
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
        .frame(minWidth: minW, minHeight: minH)
        .background(palette.background)
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: OculusSpace.md) {
            VStack(alignment: .leading, spacing: OculusSpace.xxs) {
                Text(title)
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(palette.foreground)
                if let subtitle {
                    Text(subtitle)
                        .font(.system(size: 11.5))
                        .foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: OculusSpace.md)
            if let actions { actions }
            if let onClose {
                Button("Done", action: onClose)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(.horizontal, OculusSpace.lg)
        .padding(.top, OculusSpace.lg)
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
                .font(.system(size: 11))
                .foregroundStyle(palette.mutedForeground)
            TextField(prompt, text: $text)
                .textFieldStyle(.plain)
                .font(.system(size: 12))
                .focused($focused)
                .foregroundStyle(palette.foreground)
            if !text.isEmpty {
                Button {
                    text = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain)
                .transition(.opacity)
            }
        }
        .padding(.horizontal, OculusSpace.sm)
        .padding(.vertical, 5)
        .background(palette.input)
        .clipShape(RoundedRectangle(cornerRadius: OculusRadius.sm))
        .overlay(
            RoundedRectangle(cornerRadius: OculusRadius.sm)
                .stroke(focused ? palette.primary.opacity(0.5) : palette.border)
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
                                .font(.system(size: 10, weight: .semibold, design: .monospaced))
                                .opacity(0.75)
                        }
                    }
                    .font(.system(size: 11, weight: active ? .semibold : .regular))
                    .foregroundStyle(active ? palette.primaryForeground : palette.mutedForeground)
                    .padding(.horizontal, OculusSpace.sm)
                    .padding(.vertical, 4)
                    .background(active ? palette.primary : palette.input)
                    .clipShape(RoundedRectangle(cornerRadius: OculusRadius.pill))
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
                .font(.system(size: 28))
                .foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text(title)
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(palette.foreground)
            Text(message)
                .font(.system(size: 12))
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
            RoundedRectangle(cornerRadius: OculusRadius.md)
                .stroke(tint?.opacity(0.45) ?? palette.border)
        )
        .clipShape(RoundedRectangle(cornerRadius: OculusRadius.md))
    }
}
