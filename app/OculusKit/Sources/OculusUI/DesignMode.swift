import SwiftUI
import OculusKit
#if canImport(WebKit)
import WebKit
#endif

/// Design Mode: a native WebKit view of your worktree's running app, with a click-to-pick element
/// tool that pulls the element's HTML + computed CSS (and a screenshot) straight into the prompt —
/// so you can say "make THIS button match the header" without describing it. Uses WKWebView (no
/// external browser dependency), so it's fully native on macOS and iOS.

/// One picked DOM element's context (decoded from the injected picker's message).
public struct PickedElement: Codable, Equatable {
    public var selector: String
    public var html: String
    public var css: String
    public var text: String?
}

/// Formats a picked element as a fenced prompt block the agent can act on. Pure + unit-tested.
public func designPromptBlock(_ el: PickedElement) -> String {
    var out = "I'm pointing at this element in the running app:\n\n"
    out += "Selector: `\(el.selector)`\n\n"
    out += "HTML:\n```html\n\(truncate(el.html, 2000))\n```\n\n"
    out += "Computed CSS:\n```css\n\(truncate(el.css, 1500))\n```"
    if let t = el.text, !t.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        out += "\n\nVisible text: \"\(truncate(t, 200))\""
    }
    return out
}

private func truncate(_ s: String, _ n: Int) -> String {
    s.count <= n ? s : String(s.prefix(n)) + "\n… (truncated)"
}

/// The JavaScript injected into the page: hover-highlights elements and, on click, posts the picked
/// element's selector/html/computed-css/text back to the app. Kept as a constant so it's reviewable.
let designPickerJS = """
(function() {
  if (window.__oculusPicker) return;
  window.__oculusPicker = true;
  var hl = document.createElement('div');
  hl.style.cssText = 'position:fixed;z-index:2147483647;pointer-events:none;border:2px solid #D9A520;background:rgba(217,165,32,0.12);border-radius:3px;transition:all .05s;';
  document.body.appendChild(hl);
  function cssPath(el) {
    if (el.id) return '#' + el.id;
    var path = [], e = el;
    while (e && e.nodeType === 1 && path.length < 5) {
      var sel = e.nodeName.toLowerCase();
      if (e.className && typeof e.className === 'string') sel += '.' + e.className.trim().split(/\\s+/).slice(0,2).join('.');
      path.unshift(sel); e = e.parentElement;
    }
    return path.join(' > ');
  }
  function move(ev) {
    var el = ev.target; if (!el || el === hl) return;
    var r = el.getBoundingClientRect();
    hl.style.left = r.left+'px'; hl.style.top = r.top+'px'; hl.style.width = r.width+'px'; hl.style.height = r.height+'px';
  }
  function pick(ev) {
    ev.preventDefault(); ev.stopPropagation();
    var el = ev.target; if (!el || el === hl) return;
    var cs = getComputedStyle(el), css = '';
    ['display','position','width','height','margin','padding','color','background','background-color','font','font-size','font-weight','border','border-radius','box-shadow','flex','grid','gap','align-items','justify-content'].forEach(function(p){ var v = cs.getPropertyValue(p); if (v) css += p+': '+v+';\\n'; });
    window.webkit.messageHandlers.oculusPick.postMessage({
      selector: cssPath(el), html: el.outerHTML.slice(0, 4000), css: css, text: (el.innerText||'').slice(0,300)
    });
  }
  document.addEventListener('mousemove', move, true);
  document.addEventListener('click', pick, true);
})();
"""

#if canImport(WebKit)
/// A WKWebView wrapper that loads a URL and, when picking is on, injects the element picker and
/// reports the picked element back.
struct DesignWebView {
    let url: URL
    @Binding var picking: Bool
    var onPick: (PickedElement) -> Void

    final class Coordinator: NSObject, WKScriptMessageHandler {
        let onPick: (PickedElement) -> Void
        init(onPick: @escaping (PickedElement) -> Void) { self.onPick = onPick }
        func userContentController(_ c: WKUserContentController, didReceive message: WKScriptMessage) {
            guard message.name == "oculusPick",
                  let data = try? JSONSerialization.data(withJSONObject: message.body),
                  let el = try? JSONDecoder().decode(PickedElement.self, from: data) else { return }
            onPick(el)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(onPick: onPick) }

    private func makeWebView(_ coordinator: Coordinator) -> WKWebView {
        let cfg = WKWebViewConfiguration()
        cfg.userContentController.add(coordinator, name: "oculusPick")
        let wv = WKWebView(frame: .zero, configuration: cfg)
        wv.load(URLRequest(url: url))
        return wv
    }

    private func inject(_ wv: WKWebView) {
        if picking { wv.evaluateJavaScript(designPickerJS, completionHandler: nil) }
    }
}

#if os(macOS)
extension DesignWebView: NSViewRepresentable {
    func makeNSView(context: Context) -> WKWebView { makeWebView(context.coordinator) }
    func updateNSView(_ wv: WKWebView, context: Context) { inject(wv) }
}
#else
extension DesignWebView: UIViewRepresentable {
    func makeUIView(context: Context) -> WKWebView { makeWebView(context.coordinator) }
    func updateUIView(_ wv: WKWebView, context: Context) { inject(wv) }
}
#endif

/// The Design Mode sheet: a URL bar, a "Pick element" toggle, and the web view. A picked element is
/// formatted into a prompt block and handed to the composer.
struct DesignModeView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var initialURL: String
    var onClose: () -> Void

    @State private var urlString: String
    @State private var loadedURL: URL?
    @State private var picking = false
    @State private var lastPick: PickedElement?

    init(model: Model, palette: OculusPalette, initialURL: String, onClose: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.initialURL = initialURL; self.onClose = onClose
        _urlString = State(initialValue: initialURL)
        _loadedURL = State(initialValue: URL(string: initialURL))
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Label("Design", systemImage: "cursorarrow.rays").font(.headline)
                TextField("http://localhost:3000", text: $urlString, onCommit: { loadedURL = normalizedURL(urlString) })
                    .textFieldStyle(.roundedBorder).frame(maxWidth: 320)
                Button { loadedURL = normalizedURL(urlString) } label: { Image(systemName: "arrow.clockwise") }
                Toggle(isOn: $picking) { Label("Pick element", systemImage: "cursorarrow.rays") }
                    .toggleStyle(.button).tint(palette.primary)
                Spacer()
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(12)
            Divider().overlay(palette.border)

            if let url = loadedURL {
                DesignWebView(url: url, picking: $picking) { el in
                    lastPick = el
                    picking = false
                }
                .frame(minHeight: 380)
            } else {
                Text("Enter your dev-server URL to inspect the running app.")
                    .foregroundStyle(palette.mutedForeground).frame(maxWidth: .infinity, maxHeight: .infinity)
            }

            if let el = lastPick {
                Divider().overlay(palette.border)
                HStack(spacing: 10) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Picked: \(el.selector)").font(.system(size: 12, weight: .medium)).lineLimit(1)
                        if let t = el.text, !t.isEmpty { Text(t).font(.system(size: 10)).foregroundStyle(palette.mutedForeground).lineLimit(1) }
                    }
                    Spacer()
                    Button {
                        model.draftInsert = designPromptBlock(el)
                        onClose()
                    } label: { Label("Add to prompt", systemImage: "text.badge.plus") }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                }
                .padding(12).background(palette.secondary.opacity(0.4))
            }
        }
        .frame(minWidth: 640, minHeight: 480)
        .background(palette.background)
    }

    private func normalizedURL(_ s: String) -> URL? {
        var t = s.trimmingCharacters(in: .whitespaces)
        if !t.contains("://") { t = "http://" + t }
        return URL(string: t)
    }
}
#endif
