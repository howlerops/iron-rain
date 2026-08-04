#if os(iOS)
import AVFoundation
import AudioToolbox
import SwiftUI

/// Live camera QR scanner for pairing, with a real screen for every outcome that isn't a scan.
///
/// The version this replaces painted the view black and `return`ed out of setup whenever the camera
/// wasn't usable — denied permission, the Simulator, a busy or broken device. It is presented
/// full-screen with `.ignoresSafeArea()` and no chrome, so that `return` left the user holding a
/// black rectangle: no title, no explanation, no cancel, and no route back to the paste field. The
/// only escape was a swipe-down nobody expects on an edge-to-edge camera view. This is first run,
/// and "Don't Allow" is one irreversible tap, so a dead end there is a bricked onboarding.
///
/// Every branch below therefore ends somewhere the user can act: a titled state, a way to fix the
/// cause, and a way to pair without the camera at all.
struct QRScannerView: View {
    /// Both default to dismissing this sheet, which is why the existing call sites — which pass
    /// only a trailing `onScan` closure — keep compiling untouched. A host that presents this
    /// somewhere other than a sheet, or that wants to stay open, overrides them.
    var onCancel: (() -> Void)? = nil
    var onManualEntry: (() -> Void)? = nil
    let onScan: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var phase: Phase = .checking

    private enum Phase: Equatable {
        case checking           // deciding, or waiting on the system permission prompt
        case scanning
        case denied             // .denied / .restricted — only recoverable in Settings
        case failed(String)     // no device, or the capture graph refused to build
    }

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()

            switch phase {
            case .checking:
                VStack(spacing: 12) {
                    ProgressView().tint(.white)
                    Text("Checking camera access…")
                        .font(.subheadline).foregroundStyle(.white.opacity(0.75))
                }
            case .scanning:
                CameraScanner(onScan: onScan, onFailure: { phase = .failed($0) })
                    .ignoresSafeArea()
                reticle
            case .denied:
                explanation(
                    icon: "video.slash",
                    title: "Iron Rain can't use the camera",
                    detail: "Scanning the pairing code needs camera access. Turn it on in Settings, or pair by pasting the link your Mac shows instead.",
                    action: ("Open Settings", openSystemSettings)
                )
            case .failed(let why):
                explanation(
                    icon: "exclamationmark.triangle",
                    title: "The camera isn't available",
                    detail: why,
                    action: nil
                )
            }

            cancelBar
            if phase != .checking { manualEntryBar }
        }
        .task { await decideAccess() }
    }

    // MARK: - Chrome

    /// Always on screen, in every phase. The sheet is edge-to-edge camera with no navigation bar,
    /// so without this the only dismissal is an undiscoverable swipe.
    private var cancelBar: some View {
        HStack {
            Button(action: cancel) {
                Label("Cancel", systemImage: "xmark")
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(.white)
                    .padding(.horizontal, 14).padding(.vertical, 9)
                    .background(.ultraThinMaterial, in: Capsule())
            }
            .accessibilityLabel("Cancel scanning")
            .accessibilityHint("Closes the scanner without pairing")
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 16)
        .padding(.top, Self.windowInsets.top + 12)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var manualEntryBar: some View {
        Button(action: manualEntry) {
            Text("Paste a link instead")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.white)
                .padding(.horizontal, 16).padding(.vertical, 10)
                .background(.ultraThinMaterial, in: Capsule())
        }
        .accessibilityLabel("Paste a pairing link instead")
        .accessibilityHint("Returns to the screen where you can paste the oculus pairing link")
        .padding(.bottom, Self.windowInsets.bottom + 20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
    }

    /// The frame tells you where to aim; the caption tells you what to aim at. Neither existed, so
    /// a full-bleed live camera was the entire instruction.
    private var reticle: some View {
        VStack(spacing: 18) {
            RoundedRectangle(cornerRadius: 22, style: .continuous)
                .strokeBorder(.white.opacity(0.9), lineWidth: 3)
                .frame(width: 236, height: 236)
                .accessibilityHidden(true)
            Text("Point at the QR code on your Mac")
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.white)
                .padding(.horizontal, 14).padding(.vertical, 8)
                .background(.ultraThinMaterial, in: Capsule())
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Camera is scanning. Point it at the QR code on your Mac.")
    }

    private func explanation(icon: String, title: String, detail: String,
                             action: (label: String, run: () -> Void)?) -> some View {
        VStack(spacing: 14) {
            Image(systemName: icon)
                .font(.largeTitle).foregroundStyle(.white.opacity(0.85))
                .accessibilityHidden(true)
            Text(title)
                .font(.title3.weight(.semibold)).foregroundStyle(.white)
                .multilineTextAlignment(.center)
            Text(detail)
                .font(.subheadline).foregroundStyle(.white.opacity(0.75))
                .multilineTextAlignment(.center)
            if let action {
                Button(action.label, action: action.run)
                    .buttonStyle(.borderedProminent)
                    .tint(.white)
                    .foregroundStyle(.black)
                    .controlSize(.large)
                    .padding(.top, 4)
                    .accessibilityLabel(action.label)
            }
        }
        .padding(.horizontal, 32)
        .frame(maxWidth: 420)
    }

    // MARK: - Actions

    private func cancel() {
        if let onCancel { onCancel() } else { dismiss() }
    }

    /// Defaults to dismissing, which lands back on the screen that presented the scanner — the one
    /// that already owns the "Paste oculus://pair link" field.
    private func manualEntry() {
        if let onManualEntry { onManualEntry() } else { dismiss() }
    }

    private func openSystemSettings() {
        guard let url = URL(string: UIApplication.openSettingsURLString) else { return }
        UIApplication.shared.open(url)
    }

    /// Decides the phase BEFORE any capture object is built. Building the session first is what made
    /// a denial indistinguishable from a hardware failure: both fell out of the same `guard`.
    private func decideAccess() async {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            phase = .scanning
        case .notDetermined:
            phase = .checking
            phase = await AVCaptureDevice.requestAccess(for: .video) ? .scanning : .denied
        case .denied, .restricted:
            phase = .denied
        @unknown default:
            phase = .denied
        }
    }

    /// Safe-area insets read from the window rather than from layout.
    ///
    /// The presenting sheet wraps this whole view in `.ignoresSafeArea()` — correct for the camera,
    /// which must be full-bleed — and that zeroes the safe-area insets for everything inside it. A
    /// Cancel button positioned with ordinary padding then lands under the status bar and the notch.
    /// The window still knows the real insets.
    private static var windowInsets: UIEdgeInsets {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let window = scenes.flatMap(\.windows).first { $0.isKeyWindow } ?? scenes.first?.windows.first
        return window?.safeAreaInsets ?? .zero
    }
}

/// The camera itself. Reports setup failures instead of leaving a black view behind.
private struct CameraScanner: UIViewControllerRepresentable {
    let onScan: (String) -> Void
    let onFailure: (String) -> Void

    func makeUIViewController(context: Context) -> ScannerVC {
        let vc = ScannerVC()
        vc.onScan = onScan
        vc.onFailure = onFailure
        return vc
    }
    func updateUIViewController(_ vc: ScannerVC, context: Context) {}

    final class ScannerVC: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
        var onScan: ((String) -> Void)?
        var onFailure: ((String) -> Void)?
        private let session = AVCaptureSession()
        private var preview: AVCaptureVideoPreviewLayer?
        private var didScan = false

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .black
            guard let device = AVCaptureDevice.default(for: .video) else {
                return fail("This device has no camera the app can use — the Simulator never does. Paste the pairing link instead.")
            }
            guard let input = try? AVCaptureDeviceInput(device: device), session.canAddInput(input) else {
                return fail("The camera couldn't be opened. Another app may be holding it — close it and try again.")
            }
            session.addInput(input)

            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else {
                return fail("The camera couldn't be set up to read QR codes on this device.")
            }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]

            let layer = AVCaptureVideoPreviewLayer(session: session)
            layer.videoGravity = .resizeAspectFill
            view.layer.addSublayer(layer)
            preview = layer

            DispatchQueue.global(qos: .userInitiated).async { [weak self] in self?.session.startRunning() }
        }

        /// The preview is a CALayer, so nothing lays it out for us, and `viewDidLoad` runs before the
        /// final bounds are known — sizing it only there left the image cropped and offset on any
        /// sheet whose height differed from the initial guess, and after every rotation.
        override func viewDidLayoutSubviews() {
            super.viewDidLayoutSubviews()
            preview?.frame = view.layer.bounds
        }

        override func viewDidDisappear(_ animated: Bool) {
            super.viewDidDisappear(animated)
            session.stopRunning()
        }

        /// Deferred to the next runloop turn: `viewDidLoad` can run inside SwiftUI's own update pass,
        /// and flipping the wrapper's state there is a "modifying state during view update" error.
        private func fail(_ why: String) {
            DispatchQueue.main.async { [weak self] in self?.onFailure?(why) }
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
