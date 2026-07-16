---
name: oculus-app
description: The universal SwiftUI app (iOS + macOS) and how it's structured/built. Use when changing app UI, the shared OculusUI target, the xcodegen project, or adding an app target/feature.
---

# Oculus app (SwiftUI, iOS + macOS)

The app is deliberately thin — all logic lives in the vector-locked `OculusKit` client, all UI in a
**single shared target** so iOS and macOS are identical by construction.

## Layout (`app/`)
- **`OculusKit/`** — SwiftPM package with two library products:
  - `OculusKit` — crypto (CryptoKit, reproduces Go golden vectors), `Protocol`, `OculusClient`.
  - `OculusUI` — the SwiftUI surface: `Model` (`@MainActor ObservableObject`) + `public ContentView`.
    Depends on `OculusKit`. Platform-neutral (iOS 16 / macOS 13); iOS-only tweaks behind `#if os(iOS)`.
- **`Oculus/Sources/OculusMain.swift`** — the universal `@main App` (imports `OculusUI`, shows
  `ContentView()`); macOS adds a min-size frame via `#if os(macOS)`.
- **`project.yml`** — xcodegen spec → `Oculus.xcodeproj` with targets `Oculus-iOS` + `Oculus-macOS`,
  both using `Oculus/Sources` + the local `OculusKit` package's `OculusUI` product. The generated
  `.xcodeproj` is **gitignored** — commit `project.yml`, regenerate on checkout.
- **`OculusApp/`** — a no-Xcode macOS dev harness (`swift run`) that also renders `OculusUI.ContentView`.

## Build
```sh
cd app && xcodegen generate
# iOS simulator (no signing needed):
xcodebuild -project Oculus.xcodeproj -scheme Oculus-iOS \
  -destination 'generic/platform=iOS Simulator' build
# run it: xcrun simctl boot <UDID>; xcrun simctl install <UDID> <path>/Oculus-iOS.app; \
#         xcrun simctl launch <UDID> com.howlerops.oculus
cd OculusApp && swift run   # quick macOS harness
```
Verified: the iOS target builds for the simulator SDK and launches (renders the Connect UI). The full
protocol path is proven by `OculusKit` LiveE2ETests (Swift client ↔ real Go daemon) — the app reuses
that same code, so a live daemon connection works transitively.

## v0 surface (`ContentView`)
Connect form (ws URL · daemon pubkey hex · pairing secret) → prompt an agent → **approve/deny** banner
→ streamed output. On connect it fires `discover.list` and shows **autodetected** host sessions
(see [[oculus-discovery]]). Keep new protocol wiring in `Model`, not the views.

## Adding UI / platform features
Shared behavior → `OculusUI` (guard platform-specifics with `#if os(...)`). A new app target (widget,
notification service, menu-bar) → add it to `project.yml` and regenerate. Keep parity with the Go
protocol via `OculusKit` — see [[oculus-protocol]], [[oculus-swiftkit]].
