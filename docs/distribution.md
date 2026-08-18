# Distribution: GitHub Pages + TestFlight

## GitHub Pages (site)
The marketing/support site lives in `site/` and deploys to
**https://howlerops.github.io/iron-rain/** via `.github/workflows/pages.yml` on every push to
`main` that touches `site/`. Pages source is set to "GitHub Actions".

- `site/index.html` — landing page
- `site/privacy.html` — Privacy Policy (App Store required URL)
- `site/support.html` — Support page (App Store required URL) + TestFlight instructions

Use these URLs in App Store Connect:
- Privacy Policy URL: `https://howlerops.github.io/iron-rain/privacy.html`
- Support URL: `https://howlerops.github.io/iron-rain/support.html`

## TestFlight (fastlane)
Auth is via the **App Store Connect API key** — no Apple ID / 2FA. Signing is Xcode-managed
(automatic) using the same key + `-allowProvisioningUpdates`, so no certs/profiles are
checked in.

### One-time setup
1. Install fastlane: `brew install fastlane`.
2. The API key `.p8` lives at `~/.appstoreconnect/private_keys/AuthKey_<KEY_ID>.p8` (0600).
3. `cp fastlane/.env.example fastlane/.env` and fill in `ASC_KEY_ID` / `ASC_ISSUER_ID`
   (already done locally; `fastlane/.env` is gitignored).
4. Verify auth: `fastlane ios check`.

### Create the app record (once)
The app is branded **Iron Rain** (the bundle id stays `com.howlerops.oculus`). The App
Store Connect **API cannot create app records** (Apple only allows it in the web UI), so:

1. `fastlane ios bootstrap_app` — registers the bundle id in the Developer portal (this
   the API *can* do) and prints the web-UI steps.
2. In [App Store Connect](https://appstoreconnect.apple.com) → **Apps → + → New App**:
   Platform **iOS**, Name **Iron Rain**, Bundle ID **com.howlerops.oculus**, SKU
   **howlerops-oculus**.
3. `fastlane ios bootstrap_app` again — now it creates the **External Testers** group
   (public link enabled).

### External testers — automated
`fastlane ios submit_external` sets the Test Information (review contact, description,
feedback email) via the raw App Store Connect REST API (spaceship masks the real errors —
e.g. it reported "missing contactFirstName" when Apple actually rejected an INVALID phone
format), adds the latest build to the **External Testers** group, and submits it for
external beta review. Apple's phone validation requires a *possible* number (the fictional
`555-01xx` range is rejected) — set `BETA_CONTACT_PHONE` in `.env` to your real number
(a format-valid placeholder is used otherwise). Apple reviews the first external build
(~24-48h), then External Testers get it.

Internal testers (added in App Store Connect → TestFlight → Internal Testing) get every
build immediately with no review.

### Ship a build to TestFlight
```sh
fastlane ios beta                   # xcodegen → archive → upload → external review
# internal-only (no external review):
fastlane ios beta_internal
```
`beta` bumps the build number from the latest on TestFlight, builds `Oculus-iOS`
(app-store export, managed signing), uploads, and submits for external beta review with
the External Testers group notified.

### TestFlight from CI
`.github/workflows/testflight.yml` runs the same lane on a macOS runner on manual dispatch.
It needs **two** kinds of secret, and both are required:

**1. The App Store Connect API key** — authenticates you to Apple.
- `ASC_KEY_ID`, `ASC_ISSUER_ID`
- `ASC_KEY_P8_BASE64` — `base64 -i ~/.appstoreconnect/private_keys/AuthKey_<KEY_ID>.p8 | pbcopy`

**2. The Apple Distribution certificate** — actually signs the binary.
- `IOS_CERT_P12_BASE64`, `IOS_CERT_PASSWORD`

The API key is *not* a signing identity, and this distinction is the whole reason this
workflow failed every run for its first month. A distribution certificate's private key
exists only on the machine that created it — Apple will never re-issue it — so it has to be
carried into CI as a `.p12`. Without it fastlane finds no key on the runner, tries to create
a *third* distribution certificate, and hits Apple's cap of two before anything compiles.

Export it once (Keychain Access can do this too — this is the scriptable version):

```sh
# 1. Confirm the identity exists locally.
security find-identity -v -p codesigning | grep "Apple Distribution"

# 2. Export it WITH its private key. -P sets the .p12 password; pick a strong one and keep
#    it — it becomes IOS_CERT_PASSWORD. macOS will prompt to allow keychain access.
security export -k login.keychain-db -t identities -f pkcs12 \
  -P '<choose-a-password>' -o ~/Desktop/ios-distribution.p12

# 3. Base64 it for the secret, then destroy the plaintext .p12.
base64 -i ~/Desktop/ios-distribution.p12 | pbcopy   # → paste as IOS_CERT_P12_BASE64
rm ~/Desktop/ios-distribution.p12
```

`security export` emits every identity in the keychain, so if you hold several, export the
one you want from Keychain Access instead (right-click the *Apple Distribution* row →
Export) rather than shipping the others to CI.

The workflow imports the `.p12` into a throwaway keychain scoped to the job, sets the key
partition list (or `codesign` blocks on a prompt nobody can answer), and exports
`SIGNING_KEYCHAIN` so `provision()` points `cert` at it. This is the same approach the macOS
release job has always used for its Developer ID certificate — see `release.yml`.

Certificates expire (~1 year). When yours does, re-export and update the two secrets; the
symptom is a signing failure in CI while local builds keep working, because the local
keychain has the renewed identity and CI still holds the old one.

### Checking signing without building
```sh
fastlane ios check      # auth, app record, Push capability, active/invalid profile counts
fastlane ios signing    # resolves profiles READONLY — proves a build will reuse, not re-cut
```
The profile count from `check` should stay flat. If it climbs by one per build, something is
changing the App ID's capabilities each run and invalidating every profile — see the note in
`provision()`, which is where a 99-profile pile came from.

## Secrets hygiene
- `*.p8`, `.env`, `.env.*` are gitignored (only `fastlane/.env.example` is tracked).
- Never commit the `.p8` or the issuer/key IDs; keep them in `fastlane/.env` or CI secrets.
