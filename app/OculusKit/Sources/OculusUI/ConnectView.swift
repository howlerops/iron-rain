import SwiftUI
#if os(iOS)
import AVFoundation
import AudioToolbox
#endif

/// Pairing screen: scan the daemon's QR (fastest) or enter details manually.
public struct ConnectView: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    @State private var showScanner = false
    @State private var showManual = false

    public init(model: Model) { self.model = model }

    public var body: some View {
        VStack(spacing: 20) {
            Spacer()
            Image("WolfMark").resizable().scaledToFit().frame(width: 72, height: 72)
            VStack(spacing: 4) {
                Text("Iron Rain").font(.largeTitle.bold())
                Text("Pair with your Mac's Iron Rain daemon")
                    .font(.subheadline).foregroundStyle(palette.mutedForeground)
            }

            if !model.status.isEmpty && model.status != "Not connected" {
                Text(model.status).font(.caption).foregroundStyle(palette.destructive)
                    .multilineTextAlignment(.center).padding(.horizontal, 24)
            }

            #if os(iOS)
            Button { showScanner = true } label: {
                Label("Scan QR code", systemImage: "qrcode.viewfinder")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary)
            .padding(.horizontal, 40)
            #endif

            Button { withAnimation { showManual.toggle() } } label: {
                Text(showManual ? "Hide manual entry" : "Enter details manually")
                    .font(.subheadline)
            }
            .tint(palette.primary)

            if showManual { manualForm }
            Spacer()
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background.ignoresSafeArea())
        #if os(iOS)
        .sheet(isPresented: $showScanner) {
            QRScannerView { payload in
                showScanner = false
                if applyPairing(payload) { Task { await model.connect() } }
            }
            .ignoresSafeArea()
        }
        #endif
    }

    private var manualForm: some View {
        VStack(spacing: 10) {
            field("Daemon WebSocket URL", text: $model.wsURL)
            field("Daemon public key (hex)", text: $model.daemonPubHex)
            SecureField("Pairing secret", text: $model.secret)
                .padding(12).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 12))
            Button { Task { await model.connect() } } label: {
                Text("Connect").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary)
        }
        .padding(.horizontal, 24)
        .transition(.opacity.combined(with: .move(edge: .bottom)))
    }

    private func field(_ title: String, text: Binding<String>) -> some View {
        let tf = TextField(title, text: text)
            .padding(12).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 12))
        #if os(iOS)
        return tf.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return tf
        #endif
    }

    /// Parses `oculus://pair?ws=…&pub=…&secret=…` into the model. Returns true if valid.
    @discardableResult
    private func applyPairing(_ payload: String) -> Bool {
        guard let comps = URLComponents(string: payload), comps.scheme == "oculus" else { return false }
        let items = comps.queryItems ?? []
        func q(_ name: String) -> String? { items.first { $0.name == name }?.value }
        guard let ws = q("ws"), let pub = q("pub"), let secret = q("secret") else { return false }
        model.applyPairing(url: ws, pub: pub, secret: secret)
        return true
    }
}

#if os(iOS)
/// A live camera QR scanner. Calls `onScan` with the decoded string once.
struct QRScannerView: UIViewControllerRepresentable {
    let onScan: (String) -> Void

    func makeUIViewController(context: Context) -> ScannerVC {
        let vc = ScannerVC()
        vc.onScan = onScan
        return vc
    }
    func updateUIViewController(_ vc: ScannerVC, context: Context) {}

    final class ScannerVC: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
        var onScan: ((String) -> Void)?
        private let session = AVCaptureSession()
        private var didScan = false

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .black
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device),
                  session.canAddInput(input) else { return }
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else { return }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]

            let preview = AVCaptureVideoPreviewLayer(session: session)
            preview.frame = view.layer.bounds
            preview.videoGravity = .resizeAspectFill
            view.layer.addSublayer(preview)

            DispatchQueue.global(qos: .userInitiated).async { [weak self] in self?.session.startRunning() }
        }

        override func viewDidDisappear(_ animated: Bool) {
            super.viewDidDisappear(animated)
            session.stopRunning()
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput,
                            didOutput objects: [AVMetadataObject],
                            from connection: AVCaptureConnection) {
            guard !didScan,
                  let obj = objects.first as? AVMetadataMachineReadableCodeObject,
                  let value = obj.stringValue else { return }
            didScan = true
            AudioServicesPlaySystemSound(kSystemSoundID_Vibrate)
            onScan?(value)
        }
    }
}
#endif
