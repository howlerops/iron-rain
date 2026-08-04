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

/// A sheet that shows a scannable pairing QR, so a phone can pair from the Mac without typing.
///
/// The code is minted when the sheet opens and is single-use with a short expiry. It is deliberately
/// NOT the pairing secret this Mac stores: that one was permanent and owner-equivalent, so anyone who
/// saw this sheet — over your shoulder, in a screen recording, in the screenshot you took to send to
/// someone — held a shell on the machine indefinitely. Showing the expiry is part of the fix: a
/// credential whose lifetime is invisible is one people assume is permanent.
public struct PairingQRView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onDone: () -> Void

    public init(model: Model, palette: OculusPalette, onDone: @escaping () -> Void) {
        self.model = model
        self.palette = palette
        self.onDone = onDone
    }

    private var url: String { model.pairingCode?.url ?? "" }

    public var body: some View {
        VStack(spacing: 16) {
            Text("Pair your phone").font(.title2.bold())
            Text("Open Iron Rain on your phone → Scan QR code")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)

            if let cg = qrCGImage(from: url), !url.isEmpty {
                Image(decorative: cg, scale: 1)
                    .interpolation(.none).resizable()
                    .frame(width: 240, height: 240)
                    .padding(14)
                    .background(Color.white)
                    .clipShape(RoundedRectangle(cornerRadius: 14))
            } else if model.mintingPairCode {
                ProgressView().frame(width: 240, height: 240)
            } else {
                Text("Couldn't create a pairing code").foregroundStyle(palette.destructive)
                    .frame(width: 240, height: 240)
            }

            if let code = model.pairingCode {
                Text("Pairs one device · expires \(code.expiry.formatted(date: .omitted, time: .shortened))")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                Text(code.url ?? "")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(palette.mutedForeground)
                    .lineLimit(2).truncationMode(.middle)
                    .textSelection(.enabled)
                    .padding(.horizontal, 24)
            }

            HStack(spacing: 12) {
                Button("New code") { Task { await model.mintPairingCode() } }
                    .disabled(model.mintingPairCode)
                Button("Done", action: onDone)
                    .buttonStyle(.borderedProminent).tint(palette.primary)
            }
        }
        .padding(28)
        .frame(minWidth: 320)
        .background(palette.background)
        .task {
            // Mint on open rather than reusing whatever is in memory: the sheet being open is the
            // signal that someone is about to pair, and a code minted earlier has been burning its
            // (short) lifetime since.
            await model.mintPairingCode()
        }
    }
}
