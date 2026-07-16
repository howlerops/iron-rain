import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI

/// Generates a QR CGImage from a string (Core Image; no dependency).
func qrCGImage(from string: String) -> CGImage? {
    let filter = CIFilter.qrCodeGenerator()
    filter.message = Data(string.utf8)
    filter.correctionLevel = "M"
    guard let output = filter.outputImage else { return nil }
    let scaled = output.transformed(by: CGAffineTransform(scaleX: 12, y: 12))
    return CIContext().createCGImage(scaled, from: scaled.extent)
}

/// A sheet that shows a scannable pairing QR + the reachable URL, so a phone can
/// pair from the Mac without typing.
public struct PairingQRView: View {
    let url: String
    let palette: OculusPalette
    var onDone: () -> Void

    public init(url: String, palette: OculusPalette, onDone: @escaping () -> Void) {
        self.url = url
        self.palette = palette
        self.onDone = onDone
    }

    public var body: some View {
        VStack(spacing: 16) {
            Text("Pair your phone").font(.title2.bold())
            Text("Open Oculus on your phone → Scan QR code")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)

            if let cg = qrCGImage(from: url) {
                Image(decorative: cg, scale: 1)
                    .interpolation(.none).resizable()
                    .frame(width: 240, height: 240)
                    .padding(14)
                    .background(Color.white)
                    .clipShape(RoundedRectangle(cornerRadius: 14))
            } else {
                Text("Couldn't generate QR").foregroundStyle(palette.destructive)
            }

            Text(url)
                .font(.system(.caption2, design: .monospaced))
                .foregroundStyle(palette.mutedForeground)
                .lineLimit(2).truncationMode(.middle)
                .textSelection(.enabled)
                .padding(.horizontal, 24)

            Button("Done", action: onDone)
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
        .padding(28)
        .frame(minWidth: 320)
        .background(palette.background)
    }
}
