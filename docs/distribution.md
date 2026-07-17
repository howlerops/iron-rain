# Distribution: GitHub Pages + TestFlight

## GitHub Pages (site)
The marketing/support site lives in `site/` and deploys to
**https://howlerops.github.io/oculus/** via `.github/workflows/pages.yml` on every push to
`main` that touches `site/`. Pages source is set to "GitHub Actions".

- `site/index.html` — landing page
- `site/privacy.html` — Privacy Policy (App Store required URL)
- `site/support.html` — Support page (App Store required URL) + TestFlight instructions

Use these URLs in App Store Connect:
- Privacy Policy URL: `https://howlerops.github.io/oculus/privacy.html`
- Support URL: `https://howlerops.github.io/oculus/support.html`

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
```sh
fastlane ios bootstrap_app          # creates the App Store Connect app + App ID
```
Note: the App Store **display name must be globally unique** — set `app_name` in the
`bootstrap_app` lane (or create the record in the App Store Connect UI) if "Oculus" is
taken. Then, in App Store Connect → your app → TestFlight, create an **External Testers**
group (and, optionally, enable a public link).

### Ship a build to TestFlight
```sh
fastlane ios beta                   # xcodegen → archive → upload → external review
# internal-only (no external review):
fastlane ios beta_internal
```
`beta` bumps the build number from the latest on TestFlight, builds `Oculus-iOS`
(app-store export, managed signing), uploads, and submits for external beta review with
the External Testers group notified.

### CI (optional)
`.github/workflows/testflight.yml` runs the same lane on a macOS runner on manual dispatch.
Add these repository secrets (Settings → Secrets → Actions):
- `ASC_KEY_ID`, `ASC_ISSUER_ID`
- `ASC_KEY_P8_BASE64` — `base64 -i ~/.appstoreconnect/private_keys/AuthKey_<KEY_ID>.p8 | pbcopy`

The workflow decodes the key, writes `fastlane/.env`, and runs `fastlane ios beta`.

## Secrets hygiene
- `*.p8`, `.env`, `.env.*` are gitignored (only `fastlane/.env.example` is tracked).
- Never commit the `.p8` or the issuer/key IDs; keep them in `fastlane/.env` or CI secrets.
