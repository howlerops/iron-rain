---
name: oculus-push
description: Daemon-side APNs push for actionable lock-screen approvals (ES256 JWT auth, BYO .p8 key). Use when changing push delivery, device-token registration, or the approval->notification mapping.
---

# Oculus push (APNs)

`daemon/push` delivers notifications to Apple devices so a tool-approval can be actioned from the lock
screen. Token-based auth (BYO `.p8`), HTTP/2 to APNs.

## Pieces
- `ParseP8(pem)` → `*ecdsa.PrivateKey` from an Apple `.p8` (PKCS#8 EC).
- `NewAPNs(APNsConfig{KeyID,TeamID,BundleID,Key,BaseURL?,Now?,Client?})` → `Notifier`.
- `Notifier.Notify(ctx, deviceToken, Notification{Title,Body,Category,ThreadID,Custom})` builds the
  `aps` payload (+ custom top-level keys like `approval_id`), signs a provider **JWT (ES256, R‖S 64B)**,
  and POSTs `…/3/device/<token>` with `authorization: bearer <jwt>`, `apns-topic: <bundle>`,
  `apns-push-type: alert`. Non-200 → error (e.g. 410 = dead token).

## Hub integration
- `Hub.SetNotifier(n)` + `Hub.RegisterDevice(token)`. Clients register over the protocol:
  `device.register {token}` → `RegisterDevice`.
- When the hub forwards an `ApprovalRequest`, `pushApproval` fires a push to every registered token
  (async, best-effort; no-op if unconfigured): Title `Approve <tool>`, Category `APPROVAL`,
  Custom `{approval_id, session_id}`.

## Enable it
`oculusd serve … --apns-key path/to/AuthKey.p8 --apns-key-id KID --apns-team-id TID [--apns-bundle
com.howlerops.oculus] [--apns-sandbox]`. The `.p8` is a secret — never commit it (gitignored).

## Tested vs device-gated
`daemon/push/push_test.go` runs the sender against a **mock APNs**: asserts request path/headers/body
and cryptographically **verifies the ES256 JWT** against a test key. `daemon/hub/push_test.go` proves
`device.register` → approval → push through the real encrypted transport with a fake Notifier.
Genuinely device-gated (can't automate here): a real Apple APNs key, a real device token from iOS
`didRegisterForRemoteNotificationsWithDeviceToken` (call `Model.registerDevice(token)`), the
notification-service/action-button entitlements, and end-to-end delivery to a physical device.

See [[oculus-daemon]] (forward path), [[oculus-protocol]] (device.register), [[oculus-app]]
(`registerDevice`).
