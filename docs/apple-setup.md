# Apple setup — signing, push (APNs), and a real device

Everything code-side is done and builds. Signing is already wired to **Team `Q6JSHJ4DQN`**
(Jacob Beck) with **automatic** signing. Bundle IDs: app `com.howlerops.oculus`, widget
`com.howlerops.oculus.OculusWidgets`.

## 1. App IDs — automatic (no manual portal step)
Because signing is **automatic**, Xcode **registers both App IDs and enables the Push capability for
you** the first time you build to a device (it reads `aps-environment` from the entitlements). You do
**not** need to create the App IDs by hand. (If you prefer to pre-create them: portal → Identifiers →
+ → App ID → `com.howlerops.oculus` with Push Notifications, and `com.howlerops.oculus.OculusWidgets`.)

## 2. Create an APNs Auth Key (.p8) — the one manual step (powers the daemon's push)
1. **Keys → +**, name it (e.g. "Oculus APNs"), enable **Apple Push Notifications service (APNs)**.
2. **Download** the `AuthKey_XXXXXXXXXX.p8` — you can only download it **once**. Keep it secret.
3. Note the **Key ID** (the `XXXXXXXXXX`) and your **Team ID** (top-right of the portal, 10 chars).
4. Save the file somewhere OUTSIDE the repo, e.g. `~/.oculus/AuthKey.p8`. It's gitignored (`*.p8`) —
   never commit it.

## 3. Signing — already wired
`app/project.yml` sets `DEVELOPMENT_TEAM: Q6JSHJ4DQN`, `CODE_SIGN_STYLE: Automatic`. Make sure
Xcode → Settings → Accounts is signed into your Apple ID (jacob.beck.018@gmail.com).

> ⚠️ **Team ID vs Apple-ID gotcha:** the value in a signing cert's CN — `Apple Development: name
> (XQ5D47Z462)` — is your **Apple ID identifier, NOT your Team ID**. The Team ID is the cert's **OU**
> field / the provisioning profile's `TeamIdentifier` (here `Q6JSHJ4DQN`). Using the wrong one gives
> Xcode "No Account for Team …" and APNs **403 InvalidProviderToken**. Find it with:
> `security find-certificate -a -c "Apple Development" -p | openssl x509 -noout -subject -nameopt sep_multiline | grep OU`

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
