import XCTest
import ImageIO
import CoreGraphics
import UniformTypeIdentifiers
@testable import OculusUI

/// An inline transcript image is drawn in a 420×280 point box and was decoded at the source file's
/// own resolution, then retained in the row's @State for as long as the conversation stayed open.
///
/// A screenshot is the overwhelmingly common case here — the "[Image: source: …]" form exists because
/// that is what agents paste — and an ordinary macOS screenshot is 3024×1964, which decodes to ~23 MB
/// of resident memory. Six per row is the declared bound, the rows are never evicted, and the phone
/// is the device that has to hold all of it.
final class InlineImageDecodeTests: XCTestCase {

    /// A big image must come back bounded, not at its source resolution.
    func testALargeImageIsDecodedDown() throws {
        let data = try pngData(width: 3024, height: 1964)
        let cg = try XCTUnwrap(downsampledCGImage(data, maxPixel: 1280), "the image failed to decode at all")

        XCTAssertLessThanOrEqual(max(cg.width, cg.height), 1280,
                                 "decoded at \(cg.width)×\(cg.height) — the row is 420 points wide and is "
                                 + "holding \(cg.width * cg.height * 4 / 1_048_576) MB to fill it")
        // Still a usable thumbnail, and still the right shape.
        XCTAssertGreaterThan(cg.width, 640, "downsampled past the point of being legible on a Retina panel")
        XCTAssertEqual(Double(cg.width) / Double(cg.height), 3024.0 / 1964.0, accuracy: 0.02,
                       "the aspect ratio changed — the thumbnail is distorted")
    }

    /// And a small one must not be blown up: the budget is a ceiling, not a target.
    func testASmallImageIsLeftAlone() throws {
        let data = try pngData(width: 320, height: 200)
        let cg = try XCTUnwrap(downsampledCGImage(data, maxPixel: 1280))
        XCTAssertEqual(cg.width, 320)
        XCTAssertEqual(cg.height, 200)
    }

    /// Bytes that are not an image at all must return nil rather than trapping — `load` reads whatever
    /// path the transcript names, and the transcript is written by a third-party agent.
    func testGarbageDecodesToNothing() {
        XCTAssertNil(downsampledCGImage(Data("not an image".utf8), maxPixel: 1280))
        XCTAssertNil(downsampledCGImage(Data(), maxPixel: 1280))
    }

    private func pngData(width: Int, height: Int) throws -> Data {
        let cs = CGColorSpaceCreateDeviceRGB()
        let ctx = try XCTUnwrap(CGContext(data: nil, width: width, height: height, bitsPerComponent: 8,
                                          bytesPerRow: 0, space: cs,
                                          bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        // Not a flat fill: a uniform image can compress to almost nothing and would make the test
        // pass for reasons that have nothing to do with pixel dimensions.
        for i in 0..<24 {
            ctx.setFillColor(red: Double(i) / 24, green: 0.3, blue: 0.7, alpha: 1)
            ctx.fill(CGRect(x: 0, y: i * height / 24, width: width, height: height / 24))
        }
        let image = try XCTUnwrap(ctx.makeImage())
        let out = NSMutableData()
        let dest = try XCTUnwrap(CGImageDestinationCreateWithData(out, UTType.png.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(dest, image, nil)
        XCTAssertTrue(CGImageDestinationFinalize(dest))
        return out as Data
    }
}
