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
                    .clipShape(OculusShape.rounded(14))
            } else if model.mintingPairCode {
                ProgressView().frame(width: 240, height: 240)
            } else {
                Text("Couldn't create a pairing code").foregroundStyle(palette.destructive)
                    .frame(width: 240, height: 240)
            }

            if let code = model.pairingCode {
                // A live countdown, not a wall-clock time. "Expires 3:42 PM" makes the user do
                // subtraction to answer the only question they have — do I still have time to walk
                // to my phone — and "I walked to my desk, then scanned" is the single most common
                // first-run failure this screen produces.
                PairingExpiryCountdown(expiry: code.expiry, palette: palette)
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

/// Counts a pairing code down to zero, then says plainly that it is dead and what to do about it.
///
/// Codes are deliberately short-lived and single-use, which is the right security posture — but it
/// makes expiry the NORMAL outcome rather than an edge case, so it needs to be legible rather than
/// discovered when a scan mysteriously fails.
struct PairingExpiryCountdown: View {
    let expiry: Date
    let palette: OculusPalette
    @State private var now = Date()

    // 1Hz. The countdown is the only thing on screen that moves, and a second is the resolution the
    // user is actually reasoning at.
    private let tick = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var remaining: TimeInterval { expiry.timeIntervalSince(now) }

    var body: some View {
        Group {
            if remaining <= 0 {
                Label("This code has expired — tap New code", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(palette.destructive)
            } else {
                let m = Int(remaining) / 60, s = Int(remaining) % 60
                Text("Pairs one device · expires in \(m):\(String(format: "%02d", s))")
                    .font(.caption)
                    .monospacedDigit()
                    // Warn before it bites rather than after.
                    .foregroundStyle(remaining < 30 ? palette.warning : palette.mutedForeground)
            }
        }
        .onReceive(tick) { now = $0 }
        .accessibilityLabel(remaining <= 0
            ? "Pairing code expired. Tap New code for another."
            : "Pairing code expires in \(Int(remaining) / 60) minutes \(Int(remaining) % 60) seconds.")
    }
}
