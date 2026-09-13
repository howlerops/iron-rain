import XCTest
@testable import OculusUI
@testable import OculusKit

/// Attachment chips must be diffed by identity, never by hashing their payload.
///
/// Both strips used `ForEach(..., id: \.self)`. SwiftUI re-diffs a list on every body evaluation, and
/// the composer's body runs on every keystroke — so hashing the value meant hashing an image's full
/// base64, and a document's entire extracted text, once per character typed.
///
/// Both types already carry (or now carry) a UUID minted once per attachment, which is exactly as
/// stable as the value was and costs nothing to hash.
final class AttachmentIdentityTests: XCTestCase {

    func testImageIdentityDoesNotDependOnThePayload() {
        let big = String(repeating: "A", count: 2_000_000) // ~2 MB of base64
        let a = ImageAttachment(mime: "image/png", data: big)
        let b = ImageAttachment(mime: "image/png", data: big)

        // Two attachments with identical bytes are still DISTINCT chips — that is what id buys, and
        // what `\.self` could never express: by value they were the same element.
        XCTAssertNotEqual(a.id, b.id, "each attachment needs its own identity, even with identical bytes")

        // Hashing identity is O(1) regardless of payload size.
        let started = Date()
        var h = Hasher()
        for _ in 0..<10_000 { h.combine(a.id) }
        _ = h.finalize()
        XCTAssertLessThan(Date().timeIntervalSince(started), 0.5,
                          "hashing identity should not scale with the attachment payload")
    }

    func testFileAttachmentHasStableIdentity() {
        let doc = String(repeating: "lorem ipsum ", count: 100_000)
        let a = FileAttachment(name: "spec.md", text: doc)
        let b = FileAttachment(name: "spec.md", text: doc)
        XCTAssertNotEqual(a.id, b.id, "two attachments of the same document are still two chips")

        // Identity is stable across copies, so a chip doesn't lose its place on a re-render.
        let copy = a
        XCTAssertEqual(copy.id, a.id)
    }
}
