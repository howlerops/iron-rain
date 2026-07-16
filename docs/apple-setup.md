# Apple setup — signing, push (APNs), and a real device

This is the device/credential-gated tail: everything code-side is done and builds. These steps need
your Apple Developer account (paid), the Apple Developer Portal (interactive), and a physical iPhone.
Bundle IDs are already set: app `com.howlerops.oculus`, widget `com.howlerops.oculus.OculusWidgets`.

## 1. Register the App IDs (portal → Identifiers)
1. https://developer.apple.com/account → **Certificates, IDs & Profiles → Identifiers → +**.
2. New **App ID** (App), Bundle ID **`com.howlerops.oculus`** (explicit). Enable **Push Notifications**.
3. New **App ID** for the widget: **`com.howlerops.oculus.OculusWidgets`** (no push needed).

## 2. Create an APNs Auth Key (.p8) — powers push
1. **Keys → +**, name it (e.g. "Oculus APNs"), enable **Apple Push Notifications service (APNs)**.
2. **Download** the `AuthKey_XXXXXXXXXX.p8` — you can only download it **once**. Keep it secret.
3. Note the **Key ID** (the `XXXXXXXXXX`) and your **Team ID** (top-right of the portal, 10 chars).
4. Save the file somewhere OUTSIDE the repo, e.g. `~/.oculus/AuthKey.p8`. It's gitignored (`*.p8`) —
   never commit it.

## 3. Turn on signing for a device build
Automatic signing needs your Team ID. In `app/project.yml`, under **both** app targets' `settings.base`
(and the widget target), add:
```yaml
        DEVELOPMENT_TEAM: YOURTEAMID
        CODE_SIGN_STYLE: Automatic
        CODE_SIGNING_ALLOWED: YES
```
Then `cd app && xcodegen generate`. (Tell me your Team ID and I'll wire this for you.)

## 4. Build & run on your iPhone
Plug in the phone, trust the Mac, then either:
- **Xcode**: open `app/Oculus.xcodeproj`, pick the `Oculus-iOS` scheme + your device, Run. First run
  prompts to register the device / create a provisioning profile — accept.
- **CLI**: `xcodebuild -project app/Oculus.xcodeproj -scheme Oculus-iOS -destination 'platform=iOS,name=YOUR IPHONE' build`
  (signing must be configured as in step 3).

On first launch the app asks for notification permission → grant it. The app captures the APNs token
and, on connect, sends `device.register` to the daemon.

## 5. Run the daemon with push enabled
```sh
cd daemon && go run . serve \
  --opencode http://127.0.0.1:4096 \
  --secret <pairing-secret> \
  --apns-key ~/.oculus/AuthKey.p8 \
  --apns-key-id XXXXXXXXXX \
  --apns-team-id YOURTEAMID \
  --apns-bundle com.howlerops.oculus \
  --apns-sandbox        # development builds use the APNs sandbox
```
`--apns-sandbox` matches the `aps-environment: development` entitlement. For a TestFlight/App Store
build, switch the entitlement to `production` and drop `--apns-sandbox`.

## 6. End-to-end test
1. Connect the app to the daemon (ws URL + daemon pubkey + pairing secret from the daemon's startup log).
2. Start a session whose agent calls a tool that needs approval (e.g. opencode with `permission.bash=ask`).
3. Lock the phone. The approval should arrive as a push with **Allow / Deny** actions.
4. Tap **Allow** → the tool proceeds; the session continues to idle.

## What each value is (and isn't a secret)
- **Team ID**, **Key ID**, **Bundle ID** — identifiers, safe to commit (Team ID lives in project.yml).
- **`.p8` auth key** — SECRET. Keep it out of the repo; pass by path via `--apns-key`.

## TestFlight (later)
Archive the `Oculus-iOS` scheme (Release), upload via Xcode Organizer or `xcrun altool`/`notarytool`,
set the entitlement to `aps-environment: production`, and run the daemon without `--apns-sandbox`.
