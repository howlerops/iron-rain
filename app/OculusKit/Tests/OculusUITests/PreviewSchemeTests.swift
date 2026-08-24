import XCTest
@testable import OculusUI
import Foundation

/// The pure parts of the preview tunnel: turning a web view's URL into the path the daemon fetches,
/// and back again.
final class PreviewSchemeTests: XCTestCase {

    func testTunnelURLIsBuiltUnderOneConstantHost() throws {
        let u = try XCTUnwrap(previewTunnelURL())
        XCTAssertEqual(u.scheme, previewTunnelScheme)
        // The host must be constant: relative URLs inside the page inherit scheme and host from the
        // document, which is what keeps "/assets/app.js" inside the tunnel for free.
        XCTAssertEqual(u.host, previewTunnelHost)
        XCTAssertEqual(u.path, "/")
    }

    func testTunnelURLAnchorsAPathWithoutASlash() throws {
        let u = try XCTUnwrap(previewTunnelURL(path: "assets/app.js"))
        XCTAssertEqual(u.path, "/assets/app.js")
    }

    func testPathIncludesTheQueryString() throws {
        // Dev servers lean on query strings for cache busting and HMR ids; dropping them yields a
        // stale or missing asset rather than an obvious failure.
        let u = try XCTUnwrap(URL(string: "\(previewTunnelScheme)://\(previewTunnelHost)/assets/app.js?v=abc123&t=1"))
        XCTAssertEqual(previewTunnelPath(from: u), "/assets/app.js?v=abc123&t=1")
    }

    func testEmptyPathBecomesRoot() throws {
        let u = try XCTUnwrap(URL(string: "\(previewTunnelScheme)://\(previewTunnelHost)"))
        XCTAssertEqual(previewTunnelPath(from: u), "/")
    }

    func testRoundTripsThroughTheURL() throws {
        for path in ["/", "/index.html", "/a/b/c.css", "/deep/nested/thing.js"] {
            let u = try XCTUnwrap(previewTunnelURL(path: path))
            XCTAssertEqual(previewTunnelPath(from: u), path, "round trip failed for \(path)")
        }
    }

    /// The client anchors the path and so does the daemon. Two independent guards on purpose: this
    /// one runs on the client, and a client is not a trusted source.
    func testExtractedPathAlwaysStartsWithASlash() throws {
        let urls = [
            "\(previewTunnelScheme)://\(previewTunnelHost)/ok",
            "\(previewTunnelScheme)://\(previewTunnelHost)",
            "\(previewTunnelScheme)://\(previewTunnelHost)/?q=1",
        ]
        for s in urls {
            let u = try XCTUnwrap(URL(string: s))
            XCTAssertTrue(previewTunnelPath(from: u).hasPrefix("/"), "\(s) produced an unanchored path")
        }
    }

    /// The tunnel scheme is not one WebKit can fetch itself — that is what makes it hand requests to
    /// our handler rather than trying to resolve them.
    func testSchemeIsNotAStandardOne() {
        for standard in ["http", "https", "file", "data", "about", "ws", "wss", "ftp"] {
            XCTAssertNotEqual(previewTunnelScheme, standard)
        }
    }
}
