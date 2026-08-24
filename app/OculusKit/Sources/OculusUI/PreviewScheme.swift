import Foundation
import OculusKit
#if canImport(WebKit)
import WebKit
#endif

/// Rendering a session's dev server in a web view that cannot reach it.
///
/// Design Mode's WKWebView runs in the APP. The dev server runs on the daemon host. On a Mac running
/// both those are the same machine, which is why this went unnoticed — but on a phone `localhost` is
/// the phone, and the phone has no dev server. Every request has to be carried to the daemon and the
/// response carried back.
///
/// It rides the connection that already exists rather than opening its own. That is forced, not
/// preferred: the relay bridges exactly one host and one client, and registering a second client
/// EVICTS the first — so a side-channel socket would work on a LAN and break the instant you were
/// remote, which is the only case this feature exists for.

/// The scheme WKWebView routes through the daemon. Non-standard on purpose: WebKit will not hand a
/// scheme handler any scheme it already knows how to fetch.
public let previewTunnelScheme = "ironrain-preview"

/// The single authority every tunnelled page is served under.
///
/// Constant rather than derived from the session, so RELATIVE urls inside the page resolve back into
/// the tunnel for free — "/assets/app.js" stays in the tunnel because it inherits scheme and host
/// from the document.
public let previewTunnelHost = "session"

/// The document URL for a tunnelled preview.
public func previewTunnelURL(path: String = "/") -> URL? {
    var p = path
    if !p.hasPrefix("/") { p = "/" + p }
    return URL(string: "\(previewTunnelScheme)://\(previewTunnelHost)\(p)")
}

/// Extracts the daemon-side request path from a tunnel URL, query string included.
///
/// Pure and separately tested because it is the one place a malformed URL could produce a path the
/// daemon then has to defend against. The daemon anchors the path to a leading slash regardless —
/// two independent guards, because this one runs on the client and a client is not trusted.
public func previewTunnelPath(from url: URL) -> String {
    var path = url.path.isEmpty ? "/" : url.path
    if !path.hasPrefix("/") { path = "/" + path }
    if let q = url.query, !q.isEmpty { path += "?" + q }
    return path
}

#if canImport(WebKit)

/// Serves a WKWebView's requests by asking the daemon to fetch them.
public final class PreviewSchemeHandler: NSObject, WKURLSchemeHandler {
    private let fetch: (String, String, [String: String], Data?) async throws -> PreviewFetchResp

    /// Tasks WebKit has not cancelled.
    ///
    /// Load-bearing, not bookkeeping: calling didReceive/didFinish on a task WebKit has already
    /// stopped raises an Objective-C exception that takes the app down, and a stop is entirely
    /// routine — every navigation away from a page in flight produces one. The lock is here because
    /// `stop` arrives on the main thread while a fetch completes on another.
    private let lock = NSLock()
    private var live = Set<ObjectIdentifier>()

    public init(fetch: @escaping (String, String, [String: String], Data?) async throws -> PreviewFetchResp) {
        self.fetch = fetch
    }

    private func isLive(_ task: WKURLSchemeTask) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return live.contains(ObjectIdentifier(task))
    }

    private func retire(_ task: WKURLSchemeTask) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return live.remove(ObjectIdentifier(task)) != nil
    }

    public func webView(_ webView: WKWebView, start urlSchemeTask: WKURLSchemeTask) {
        lock.lock()
        live.insert(ObjectIdentifier(urlSchemeTask))
        lock.unlock()

        guard let url = urlSchemeTask.request.url else {
            _ = retire(urlSchemeTask)
            urlSchemeTask.didFailWithError(PreviewTunnelError.badRequest)
            return
        }
        let path = previewTunnelPath(from: url)
        let method = urlSchemeTask.request.httpMethod ?? "GET"
        var headers = urlSchemeTask.request.allHTTPHeaderFields ?? [:]
        headers.removeValue(forKey: "Host") // the daemon sets the one the preview router routes on
        let body = urlSchemeTask.request.httpBody

        Task { [weak self] in
            guard let self else { return }
            do {
                let resp = try await self.fetch(path, method, headers, body)
                // Re-check AFTER the await: the page may have navigated away while this was in
                // flight, and answering a stopped task is fatal.
                guard self.isLive(urlSchemeTask) else { return }
                let data = Data(base64Encoded: resp.body) ?? Data()
                let http = HTTPURLResponse(
                    url: url,
                    statusCode: resp.status,
                    httpVersion: "HTTP/1.1",
                    headerFields: resp.headers ?? [:]
                )
                guard self.retire(urlSchemeTask) else { return }
                if let http {
                    urlSchemeTask.didReceive(http)
                    urlSchemeTask.didReceive(data)
                    urlSchemeTask.didFinish()
                } else {
                    urlSchemeTask.didFailWithError(PreviewTunnelError.badResponse)
                }
            } catch {
                guard self.retire(urlSchemeTask) else { return }
                urlSchemeTask.didFailWithError(error)
            }
        }
    }

    public func webView(_ webView: WKWebView, stop urlSchemeTask: WKURLSchemeTask) {
        _ = retire(urlSchemeTask)
    }
}

/// Failures that belong to the tunnel rather than to the dev server.
public enum PreviewTunnelError: LocalizedError {
    case badRequest
    case badResponse

    public var errorDescription: String? {
        switch self {
        case .badRequest: return "The preview request had no address."
        case .badResponse: return "The dev server's response could not be reconstructed."
        }
    }
}

#endif
