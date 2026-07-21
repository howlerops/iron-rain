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

### CI (optional)
`.github/workflows/testflight.yml` runs the same lane on a macOS runner on manual dispatch.
Add these repository secrets (Settings → Secrets → Actions):
- `ASC_KEY_ID`, `ASC_ISSUER_ID`
- `ASC_KEY_P8_BASE64` — `base64 -i ~/.appstoreconnect/private_keys/AuthKey_<KEY_ID>.p8 | pbcopy`

The workflow decodes the key, writes `fastlane/.env`, and runs `fastlane ios beta`.

## Secrets hygiene
- `*.p8`, `.env`, `.env.*` are gitignored (only `fastlane/.env.example` is tracked).
- Never commit the `.p8` or the issuer/key IDs; keep them in `fastlane/.env` or CI secrets.
