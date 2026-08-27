import Foundation
#if canImport(WebKit)
import WebKit
#endif

/// Performing an agent's DOM operations in the preview the user already has open.
///
/// The daemon has no browser. A dev server's HTML is inert, and for any client-rendered app the
/// markup it serves is an empty shell — everything real appears only after the page's own JavaScript
/// runs. The two ways to give the daemon a DOM both mean shipping Chromium to every user.
///
/// The app already has a browser engine, so the work happens here: the daemon broadcasts an ask,
/// whichever client has that session's preview open performs it, and the answer goes back.
///
/// The limitation is inherent and is stated to the agent rather than hidden: nothing works while
/// nobody is looking. A refusal an agent can act on beats a confident answer about a page that was
/// never rendered.

/// Tracks which web view is showing which session's preview.
///
/// Weak references throughout: a registry that keeps web views alive would leak one per Design sheet
/// opened, and — worse — a retained but detached view would answer an agent about a page nobody is
/// looking at any more.
public final class PreviewDOMRegistry {
    public static let shared = PreviewDOMRegistry()

    #if canImport(WebKit)
    private final class WeakView {
        weak var view: WKWebView?
        init(_ v: WKWebView) { view = v }
    }
    private var views: [String: WeakView] = [:]
    #endif
    private let lock = NSLock()

    #if canImport(WebKit)
    public func register(sessionID: String, view: WKWebView) {
        guard !sessionID.isEmpty else { return }
        lock.lock(); views[sessionID] = WeakView(view); lock.unlock()
    }

    public func unregister(sessionID: String) {
        guard !sessionID.isEmpty else { return }
        lock.lock(); views.removeValue(forKey: sessionID); lock.unlock()
    }

    /// The live web view for a session, if one is still on screen.
    public func view(for sessionID: String) -> WKWebView? {
        lock.lock(); defer { lock.unlock() }
        guard let box = views[sessionID] else { return nil }
        if box.view == nil {
            views.removeValue(forKey: sessionID) // the sheet closed; drop the empty box
            return nil
        }
        return box.view
    }
    #endif

    #if canImport(WebKit)
    /// Web views this app created for an agent, rather than for a person to look at.
    ///
    /// Held STRONGLY, unlike the ones above: nothing else is keeping them alive. Evicted after an
    /// idle period so a day of agent work does not accumulate one renderer per session.
    private var headless: [String: (view: WKWebView, lastUsed: Date)] = [:]

    /// How long an agent-created view survives without use.
    private static let headlessIdle: TimeInterval = 10 * 60

    /// A view for this session, preferring one a PERSON already has open.
    ///
    /// The visible one is preferred because it is the page the user is actually looking at — the
    /// agent and the human should be seeing the same thing where possible.
    public func anyView(for sessionID: String) -> WKWebView? {
        if let visible = view(for: sessionID) { return visible }
        lock.lock(); defer { lock.unlock() }
        evictIdleLocked()
        guard let entry = headless[sessionID] else { return nil }
        headless[sessionID] = (entry.view, Date())
        return entry.view
    }

    /// Registers a view this app created for agent use.
    public func adoptHeadless(sessionID: String, view: WKWebView) {
        guard !sessionID.isEmpty else { return }
        lock.lock(); defer { lock.unlock() }
        evictIdleLocked()
        headless[sessionID] = (view, Date())
    }

    /// Drops an agent-created view — when its session ends, or its page failed to load.
    public func dropHeadless(sessionID: String) {
        lock.lock(); defer { lock.unlock() }
        headless.removeValue(forKey: sessionID)
    }

    private func evictIdleLocked() {
        let cutoff = Date().addingTimeInterval(-Self.headlessIdle)
        for (id, entry) in headless where entry.lastUsed < cutoff {
            headless.removeValue(forKey: id)
        }
    }
    #endif

    /// Whether any web view is currently showing this session's preview.
    public func isShowing(sessionID: String) -> Bool {
        #if canImport(WebKit)
        return view(for: sessionID) != nil
        #else
        return false
        #endif
    }
}

#if canImport(WebKit)
/// Builds an offscreen web view for a session's preview, so an agent can look at the page without a
/// person having the Design sheet open.
///
/// The alternative was to have the daemon ask the app to OPEN that sheet, which works and is wrong:
/// an agent would be yanking a window in front of whatever its owner was doing, and the moment that
/// is possible it is also a thing a misbehaving agent can do repeatedly. This renders the same page
/// with the same scripts and interrupts nobody.
///
/// The frame is explicit and non-zero on purpose. A WKWebView with a zero frame still runs
/// JavaScript, so the temptation is to leave it unsized — but it does not LAY OUT, and the snapshot
/// walks the page asking each element for its client rects. Unsized, every element reports no boxes,
/// the visibility check drops all of them, and the agent is told a fully working page is empty. That
/// is the worst possible answer: confidently wrong rather than unavailable.
@MainActor
public enum HeadlessPreview {
    /// A conventional desktop viewport. Responsive layouts branch on width, so a page rendered at
    /// 320pt would answer questions about the mobile layout while the person asking is looking at
    /// the desktop one.
    static let viewport = CGSize(width: 1280, height: 900)

    /// How long to wait for the page before giving up and saying so.
    static let loadTimeout: TimeInterval = 20

    /// Creates a view, loads `url`, and returns it once the page has finished loading.
    public static func make(url: URL, tunnel: PreviewSchemeHandler?) async -> WKWebView? {
        let cfg = WKWebViewConfiguration()
        if let tunnel {
            cfg.setURLSchemeHandler(tunnel, forURLScheme: previewTunnelScheme)
        }
        let wv = WKWebView(frame: CGRect(origin: .zero, size: viewport), configuration: cfg)
        let waiter = LoadWaiter()
        wv.navigationDelegate = waiter
        wv.load(URLRequest(url: url))
        let loaded = await waiter.wait(timeout: loadTimeout)
        // The delegate is cleared either way: it exists only to observe the first load, and leaving
        // it attached would keep a waiter alive for the life of the view.
        wv.navigationDelegate = nil
        return loaded ? wv : nil
    }
}

/// Resumes once a web view finishes (or fails) its first navigation.
private final class LoadWaiter: NSObject, WKNavigationDelegate {
    private var continuation: CheckedContinuation<Bool, Never>?
    private var settled = false

    func wait(timeout: TimeInterval) async -> Bool {
        await withCheckedContinuation { (c: CheckedContinuation<Bool, Never>) in
            continuation = c
            // A dev server that accepts the connection and then never answers would otherwise park
            // this forever, and the agent with it.
            Task { @MainActor in
                try? await Task.sleep(nanoseconds: UInt64(timeout * 1_000_000_000))
                self.settle(false)
            }
        }
    }

    private func settle(_ ok: Bool) {
        guard !settled else { return } // a failure after a success, or the timeout after either
        settled = true
        continuation?.resume(returning: ok)
        continuation = nil
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { settle(true) }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { settle(false) }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { settle(false) }
}
#endif

/// Reads the live DOM into an outline an agent can reason about, stamping each listed element with a
/// ref.
///
/// Refs rather than CSS selectors, in both directions. A selector the agent composes could reach any
/// element on the page, including ones it was never shown; a ref can only name something a snapshot
/// actually returned. It also survives a class name changing underneath, which is exactly what
/// happens while an agent is editing the styles.
public let previewSnapshotJS = """
(function() {
  var MAX = 200;
  // Every snapshot opens a new EPOCH, and the epoch is part of each ref.
  //
  // Without it, refs restart at e1 on every snapshot, so a ref held from an earlier snapshot can
  // resolve to a completely different element after the page re-renders — the agent clicks "Save"
  // and hits "Delete", with nothing anywhere reporting a problem. A silent mis-click is the most
  // expensive failure this feature can have, and it is only detectable if the ref itself carries
  // which snapshot it came from.
  var epoch = (window.__irEpoch || 0) + 1;
  window.__irEpoch = epoch;
  var sel = 'a,button,input,select,textarea,summary,label,[role],[onclick],h1,h2,h3,h4';
  var seen = 0, out = [];
  function visible(el) {
    if (!el.getClientRects().length) return false;
    var s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0';
  }
  // Fields whose CONTENTS must never be reported, whatever else is true of them.
  //
  // Checked in one place because there are two ways a value escapes and only one is obvious. The
  // explicit `value` branch below is easy to remember; the accessible NAME is not — it falls back
  // through label, placeholder and title to el.value, so an unlabelled password field reported its
  // own contents as its name. A real browser found that; reading the source did not.
  function sensitive(el) {
    var type = (el.getAttribute('type') || '').toLowerCase();
    if (type === 'password') return true;
    var hint = ((el.getAttribute('name') || '') + ' ' + (el.id || '') + ' ' +
                (el.getAttribute('autocomplete') || '')).toLowerCase();
    return /pass|secret|token|otp|cvv|cvc|card|cc-|ssn/.test(hint);
  }
  function name(el) {
    var t = (el.getAttribute('aria-label') || el.getAttribute('placeholder') ||
             el.getAttribute('title') || el.innerText ||
             (sensitive(el) ? '' : el.value) || '').trim();
    return t.length > 120 ? t.slice(0, 120) + '…' : t;
  }
  document.querySelectorAll(sel).forEach(function(el) {
    if (seen >= MAX || !visible(el)) return;
    seen++;
    var ref = 's' + epoch + 'e' + seen;
    el.setAttribute('data-ir-ref', ref);
    var e = { ref: ref, tag: el.tagName.toLowerCase() };
    var role = el.getAttribute('role'); if (role) e.role = role;
    var n = name(el); if (n) e.name = n;
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT') {
      e.type = el.getAttribute('type') || el.tagName.toLowerCase();
      // A snapshot goes into the agent's context AND into the durable transcript, where a secret
      // captured once is retained long after the session ends.
      if (sensitive(el)) {
        if (el.value) e.value = '[withheld]'; // say a value EXISTS without saying what it is
      } else if (el.value) {
        e.value = String(el.value).slice(0, 120);
      }
    }
    if (el.disabled) e.disabled = true;
    out.push(e);
  });
  return JSON.stringify({
    url: location.href,
    title: document.title,
    truncated: seen >= MAX,
    elements: out
  });
})()
"""

/// Finds a snapshotted element by ref, without ever putting the ref into a selector.
///
/// Building `'[data-ir-ref="' + ref + '"]'` would hand agent-supplied text to the CSS parser, and a
/// ref carrying a quote either breaks the selector or reaches further than intended. Comparing the
/// attribute in a loop removes the parser from the path entirely — bounded work, since a snapshot
/// stamps at most a couple of hundred elements.
private let previewFindByRefJS = """
      var m = /^s(\\d+)e\\d+$/.exec(ref);
      if (!m) return JSON.stringify({ error: 'Malformed ref ' + ref + '. Use a ref from preview_snapshot.' });
      if (Number(m[1]) !== (window.__irEpoch || 0)) {
        return JSON.stringify({ error: 'Stale ref ' + ref + ' — the page has been snapshotted again since. Take a fresh preview_snapshot and use its refs.' });
      }
      var el = null, all = document.querySelectorAll('[data-ir-ref]');
      for (var i = 0; i < all.length; i++) {
        if (all[i].getAttribute('data-ir-ref') === ref) { el = all[i]; break; }
      }
      if (el && !el.isConnected) el = null; // detached by a re-render since the snapshot
"""

/// Clicks a previously-snapshotted element.
///
/// The ref is bound to a JS variable ONCE, encoded, and every later use concatenates that variable.
/// Interpolating it a second time — even into an error message — is how it ends up outside a string
/// literal: `error: 'No element \\(ref).'` looks like a message and is an injection point.
public func previewClickJS(ref: String) -> String {
    """
    (function() {
      var ref = \(jsString(ref));
    \(previewFindByRefJS)
      if (!el) return JSON.stringify({ error: 'No element ' + ref + '. The page may have re-rendered — take a fresh preview_snapshot.' });
      el.scrollIntoView({ block: 'center' });
      el.click();
      return JSON.stringify({ clicked: ref, url: location.href });
    })()
    """
}

/// Types into a previously-snapshotted input.
public func previewFillJS(ref: String, value: String) -> String {
    """
    (function() {
      var ref = \(jsString(ref)), value = \(jsString(value));
    \(previewFindByRefJS)
      if (!el) return JSON.stringify({ error: 'No element ' + ref + '. The page may have re-rendered — take a fresh preview_snapshot.' });
      if (!('value' in el)) return JSON.stringify({ error: 'Element ' + ref + ' is a ' + el.tagName.toLowerCase() + ', which cannot be typed into.' });
      el.focus();
      // React (and anything else tracking the value internally) ignores a plain assignment: it
      // installs its own value setter, so the change has to go through the NATIVE one and then be
      // announced with the events a framework listens for. Without this the field looks filled on
      // screen and the app's state never changes.
      var proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      var setter = Object.getOwnPropertyDescriptor(proto, 'value');
      if (setter && setter.set) { setter.set.call(el, value); } else { el.value = value; }
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return JSON.stringify({ filled: ref });
    })()
    """
}

/// Encodes a Swift string as a JavaScript literal.
///
/// Every value an agent supplies is interpolated into a script, so this is the boundary that keeps a
/// ref or a form value from becoming code. JSONSerialization rather than hand-rolled escaping,
/// because JSON string syntax is a subset of JavaScript's and it already handles quotes, backslashes,
/// newlines and the unicode line separators that break naive escapers.
public func jsString(_ s: String) -> String {
    if let data = try? JSONSerialization.data(withJSONObject: [s], options: []),
       let arr = String(data: data, encoding: .utf8) {
        return String(arr.dropFirst().dropLast()) // ["x"] -> "x"
    }
    return "\"\"" // unencodable input becomes an empty string, never raw text
}
