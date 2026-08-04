# Session interception review

Read-only assessment of the Iron Rain / Oculus transport, pairing, and authorization model.
Scope: `daemon/crypto`, `daemon/transport`, `daemon/server`, `daemon/relay`, `relay-cf`,
`daemon/hub` (roles/invites/devices), `app/OculusKit`.

Every claim below names the threat model it holds under. Nothing here says "sessions cannot be
intercepted."

---

## 0. Correction to a premise

The handshake is **not Noise**, and not any authenticated key-exchange pattern. It is a bare
**static-static X25519 ECDH → HKDF-SHA256 → ChaCha20-Poly1305** channel with no ephemeral keys, no
transcript hash, no server-contributed randomness, and no confirmation message tied to the session.

- `daemon/crypto/crypto.go:1-14` states the scheme and says so explicitly: *"static-static ECDH gives
  a stable paired key but no forward secrecy."*
- `daemon/crypto/crypto.go:84-102` — `DeriveSessionKeys` is a single ECDH plus two HKDF expansions
  over fixed labels. Nothing else feeds the KDF.
- `daemon/transport/transport.go:45-113` — the whole handshake: client sends a plaintext JSON hello
  with its public key, both sides derive keys, client sends the pairing secret as the first sealed
  frame, server replies `{"ok":true}` sealed.
- `app/OculusKit/Sources/OculusKit/Crypto.swift:31-43` — the Swift half, identical.

Three places in the tree assert a Noise handshake or a MITM guarantee that the code does not
implement. These are worth fixing because they are load-bearing for how the system gets reasoned
about:

- `daemon/hub/devices.go:23` — *"The Noise handshake already proves each client's static public key
  before any secret is checked."* There is no Noise handshake, and the client's key is **asserted,
  not proved** (see §1.2).
- `daemon/relay/relay.go:8-11` — *"cannot MITM (the client already has the real daemon pubkey from
  the pairing QR)"*. True for confidentiality; false for availability and for replay (see §3, §4).
- `relay-cf/src/index.ts:69-70` — refers to "one Noise session".

The scheme it actually is (static-static ECDH) is not broken. But it is a materially different
security object from Noise `XX`/`IK`, and the gaps in §4 and §5 follow directly from that
difference.

---

## 1. Threat model

Claims are stated against these five attackers. Each finding says which ones it applies to.

| # | Attacker | Can reach |
|---|---|---|
| **T1** | **Passive network observer** on the LAN / shared Wi-Fi | Full packet capture of the direct route. The direct route is plain `ws://` on `0.0.0.0:6000` (`app/OculusKit/Sources/OculusUI/DaemonLauncher.swift:56`, `site/install.sh:91`), so there is no TLS beneath the app-layer AEAD. |
| **T2** | **Malicious or compelled relay operator** (Cloudflare Workers, or the Fly host) | Full ciphertext stream, both endpoints' IPs, timing, sizes, and the `?sid=` in every request URL. Workers observability is on (`relay-cf/wrangler.toml:17-18`), so the sid lands in logs. |
| **T3** | **Someone who sees the user's screen, photo roll, clipboard, terminal scrollback, or CI logs** | The pairing URL, which carries the full credential (`daemon/main.go:556-567`). |
| **T4** | **Stolen/unlocked phone or Mac, or a filesystem/backup reader** | `UserDefaults` plist (client secret in plaintext) and `~/.oculus/` (daemon key, secret, pairing.json). |
| **T5** | **A compromised or malicious invited guest** — someone legitimately handed an invite link | An authenticated connection with a non-owner role, plus everything the invite protocol exposes. |

Out of scope, stated so it is not mistaken for a clean bill: malware running as the user on the Mac
(it can read `~/.oculus/key` and is game over by construction), and a compromised Apple/APNs.

Note on stakes: `run.test` already reaches arbitrary shell, and is being raised to `capOwner` in a
parallel change. That change narrows *which role* can do it; it does not change the fact that
**anything that yields an owner-authenticated connection yields arbitrary code execution on the
user's Mac**. Every "credential leaked" finding below should be read at that severity.

---

## 2. Key trust: is a changed daemon key rejected?

**Guaranteed (T1, T2): yes, key substitution fails closed** — but cryptographically, not by an
explicit check.

The client never learns the daemon's key from the wire. It derives the channel from the *pinned*
`daemonPubHex` (`app/OculusKit/Sources/OculusUI/OculusUI.swift:357`, passed to
`Client.swift:63`). An attacker substituting a different static key cannot derive `c2d`, so it
cannot open the sealed pairing secret; and cannot derive `d2c`, so it cannot forge the `{"ok":true}`
verdict the client requires (`Client.swift:73-78`). The connection fails.

**Not guaranteed:**

1. **There is no key-change detection or warning.** A substitution is indistinguishable from a
   network failure — `attemptConnect` maps every non-`handshakeRejected` error to `.unreachable`
   (`OculusUI.swift:391-393`). A user under active attack sees "Reconnecting…", not "this Mac's
   identity changed." The security property holds; the *observability* of an attack is zero.
2. **Re-pairing silently overwrites the pin.** `applyPairing` (`OculusUI.swift:784-789`) assigns
   `daemonPubHex` unconditionally — no comparison against the stored value, no confirmation. A user
   who scans a QR because "it stopped connecting" will replace their pin with the attacker's key and
   the app will say nothing. Combined with (1) — where a real attack presents *as* a connection
   failure — this is a workable social-engineering path: break the connection, offer a fresh QR.
   On macOS, `DesktopStore` keys desktops by pubkey (`DesktopStore.swift:9`), so a changed key adds a
   *second* desktop entry rather than overwriting — slightly better, and inconsistent with the
   single-pairing path.
3. **The daemon does not pin clients at all.** `ServerHandshake` derives keys from whatever
   `client_pub` the peer sends (`transport.go:81-92`) and enrolls it on first sight
   (`devices.go:96-100`). Client identity is a self-assigned label, not a pinned credential. This is
   what makes §5 fail.

**Fix:** compare against the stored pin in `applyPairing`; if it differs, require an explicit,
worded confirmation ("This Mac's identity key changed. This is expected only if you reinstalled the
daemon."). Distinguish key-mismatch from unreachable in `attemptConnect` and surface it.

---

## 3. Pairing secret distribution

**This is the most plausible real-world interception path, well ahead of anything cryptographic.**
Applies to T3 and T4.

What the credential is:

- 128 bits of entropy, hex (`daemon/main.go:849-864`, `randomHex(16)`) — not guessable.
- **Reusable forever.** No TTL, no use counter, no rotation path in the code. It is deliberately
  stable across restarts so paired devices keep working (`main.go:849-853`).
- **Shared, not per-device.** One string authorizes every owner device (`main.go:326`,
  `invites.go:153-160`).
- **Owner-equivalent.** Presenting it yields `RoleOwner` (`invites.go:180-185`), which is every
  capability including approvals and settings (`roles.go:105-117`).

Where it travels and rests:

| Location | Evidence |
|---|---|
| A URL query parameter, `oculus://pair?ws=…&pub=…&secret=…&relay=…` | `daemon/main.go:556-567`, `OculusUI.swift:331-348` |
| Printed to the terminal on every daemon start | `daemon/main.go:377` — `pairing secret: %s` |
| `~/.oculus/pairing.json`, plaintext (0600) | `daemon/main.go:495-511` |
| `~/.oculus/secret`, plaintext (0600) | `daemon/main.go:863` |
| Client `UserDefaults`, plaintext — acknowledged in a TODO | `OculusUI.swift:298` — `// TODO: move the secret to the Keychain`; also `devices.go:26-28` |
| Every macOS "desktop" entry, JSON in `UserDefaults` | `DesktopStore.swift:8-16` |
| A QR code rendered on screen | `OculusUI.swift:333` |
| The system clipboard, for invites | `SharingView.swift:119`, `171-177` |

A URL is the worst possible container for a permanent credential. It reaches: screenshots and the
photo library (auto-synced to iCloud), QR-scanner app history, clipboard managers and iOS Universal
Clipboard (which syncs across devices), shell history and terminal scrollback, CI/build logs if
`oculusd` is ever run in one, and screen-sharing recordings. `UserDefaults` is a plaintext plist
included in unencrypted iTunes/Finder backups and readable on a jailbroken or unlocked device.

Anyone who obtains it has permanent owner access to the user's Mac, and — because of §5 — the user
has no effective way to revoke them short of deleting `~/.oculus/secret` and re-pairing every device.

**Fixes, in order of value:**
1. Make the pairing secret **single-use**: it authorizes exactly one enrollment, after which the
   device holds a per-device credential and the pairing secret is dead. This is the change that
   makes every leak path above expire.
2. Give it a **short TTL** (minutes) and mint it on demand from the app, rather than persisting one
   forever at `~/.oculus/secret`.
3. Move the client-side credential to the **Keychain** with
   `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` — that removes the backup and Universal-Clipboard
   exposure in one step.
4. Stop printing the secret to stdout by default; print the QR only.

---

## 4. Relay exposure

### 4.1 What a relay operator learns (T2)

Content is safe: the relay forwards opaque frames (`relay.go:246-258`,
`relay-cf/src/index.ts:115-121`) and cannot derive the channel key. But it observes:

- **The daemon's static public key, in the URL, on every connection.** `?sid=` *is* the daemon
  pubkey (`daemon/main.go:333`, `OculusUI.swift:570-577`, `relay-cf/src/index.ts:24-33`). This is a
  permanent, globally-unique identifier for the user's machine. Workers observability is enabled
  (`relay-cf/wrangler.toml:17-18`), so it is in request logs, not merely in transit.
- Source IP of both the daemon and every client, and therefore the user's home/office and travel
  pattern, correlated over time by that stable sid.
- When the Mac is on (the host socket is held open 24/7 — `main.go:332-334`), and precisely when
  someone is working, for how long.
- Per-frame sizes and timing. On a streaming LLM transcript this is a strong side channel: token
  cadence, response lengths, prompt sizes, and the burst signature of a tool call are all visible.
- Which client devices connect to which daemon, and how many.

Nothing in the `?sid=`/`&role=` protocol leaks *beyond* metadata — no session ids, no titles, no
plaintext. The `ir-ping`/`ir-pong` keepalive is answered inside the runtime and never forwarded
(`relay-cf/src/index.ts:55-62`), which is correctly done.

**Fix:** make `sid` a **blinded, rotating** value rather than the raw pubkey — e.g.
`HMAC(pairing-derived key, epoch)` — so the relay cannot correlate a machine across time or against
a pubkey it obtained elsewhere. Length-pad frames to bucket boundaries if the transcript side
channel matters.

### 4.2 Relay host-slot hijack — anyone who knows the daemon's public key (T2, T3, and anyone who has seen a QR or a log line)

**Relay registration is entirely unauthenticated, and the newest registration wins.**

- Go relay: `serveHost` installs the new entry and evicts the incumbent —
  `old.ws.Close(…, "replaced by newer host registration")` (`relay.go:114-133`). The only input is
  `?sid=`, taken straight from the URL (`relay.go:75-77`).
- Cloudflare relay: identical — `supersede(role, …)` before `acceptWebSocket`
  (`relay-cf/src/index.ts:98-108`, `141-149`). The Worker validates only that `sid` is non-empty and
  `role` is one of two strings (`index.ts:24-28`).

The `sid` is the daemon's **public** key. It is printed to stdout (`main.go:376`), stored in
`pairing.json`, embedded in every pairing QR, and logged by the relay operator. Nothing about it is
secret.

Consequences for an attacker holding only that public value:

1. **Persistent remote denial of service.** Register as `host` in a loop. The real daemon is evicted
   each time; its re-dial loop re-registers and is evicted again. Every remote client gets bridged to
   the attacker and fails the handshake, or is told `no host for server_id`. Remote access is dead
   and the user's diagnosis is "the relay is flaky."
2. **A capture position.** Clients connect to the attacker believing it is the relay path to their
   daemon, and send their plaintext `client_hello` plus the sealed pairing-secret frame and any
   further frames before timing out. The attacker cannot read them — but see §4.3, which is what
   makes the capture worth having.
3. **Client-slot eviction** works the same way (`index.ts:98-106`) — a second `role=client` kicks the
   legitimate device off mid-session.

**Fix:** require proof of key possession at relay registration. A challenge–response is unnecessary
overhead here; the minimum viable version is: the relay issues a random nonce on connect, and the
host must return a signature (or an HMAC under a pairing-derived key) over it before it is allowed
to take the host slot. Simpler stopgap: refuse to supersede a *live, recently-active* host — make the
incumbent win rather than the newcomer, so hijack requires waiting for a genuine disconnect.

### 4.3 No replay protection anywhere in the transport (T1, T2, and §4.2's position)

**This is the finding I would fix first after the pairing secret.** A captured client→daemon stream
can be replayed to the real daemon verbatim, and it will authenticate and execute.

Why it works:

- **The server contributes no randomness to the handshake.** `ServerHandshake` reads, derives,
  decrypts, and answers (`transport.go:80-113`). There is no nonce, no challenge, no timestamp, no
  transcript binding. The entire client→daemon direction is a pure function of bytes the attacker
  already has.
- **The keys are static.** `DeriveSessionKeys` is ECDH over two long-term keys
  (`crypto.go:84-102`) — replaying the same `client_hello` reproduces exactly the same `c2d` key on
  the daemon, so previously captured frames still open.
- **Nonces are random per message, and never checked for reuse.** `Open` decrypts whatever nonce
  arrives (`crypto.go:163-170`). A replayed frame carries its original nonce and verifies fine. (The
  random-nonce choice itself is correct and well-reasoned given static keys — `crypto.go:129-136`.
  It just does nothing against replay.)
- **The hub does not deduplicate.** `Hub.Serve` decodes and dispatches every frame with no seen-id
  set, no sequence number, no monotonic counter (`hub.go:1995` onward — `conn.Recv()` →
  `protocol.Decode` → `dispatch`, unconditionally). Envelope `ID` is echoed on responses only
  (`protocol/protocol.go:210-214`).

The attack: record `client_hello` + the sealed secret frame + the session's command frames. Later,
open a fresh connection to the daemon (LAN, or via the relay), replay the first two frames — the
daemon authorizes, `enroll`s the same pubkey, and grants `RoleOwner` — then replay any captured
command frame, any number of times, in any order. The attacker cannot read the responses (no `d2c`
key) but the *effects happen*: prompts run, worktree PRs open, and anything that reached shell
reaches it again.

This is reachable by **T1, a purely passive LAN observer**, because the direct route has no TLS:
capture at a coffee shop, replay later. It does not require breaking any cryptography.

**Fix:** bind each session to server-contributed randomness. The minimal change that closes it: the
daemon sends a random 32-byte challenge as its *first* frame; the client's sealed proof covers
`HKDF(shared, challenge)` rather than the bare secret, and both sides mix the challenge into the
channel keys. That single change makes every recorded stream undecryptable-and-unreplayable against
a new session. Add a per-connection monotonic sequence number inside the sealed frame and reject
non-increasing values, to kill in-session reordering/duplication too. (The proper fix is an
ephemeral handshake — see §4.4 — which delivers both properties as a side effect.)

### 4.4 Forward secrecy

**Not guaranteed, against any attacker who records traffic and later obtains `~/.oculus/key` (T2 +
T4).**

- The daemon's private key is a long-lived file, hex, mode 0600 (`main.go:883-901`).
- The client's public key is sent **in the clear** in the hello (`transport.go:47`).
- Therefore `ECDH(daemonPriv, clientPub)` reconstructs the channel key for **every session ever
  recorded**, retroactively and permanently. `crypto.go:13-14` and `relay.go:9-11` both acknowledge
  this.

One accidental mitigation, worth naming precisely because it is accidental and undocumented: the app
generates a **fresh client key on every launch** — `private let clientPrivate =
OculusCrypto.generatePrivateKey()` (`OculusUI.swift:210`), never persisted. So the client half is
effectively ephemeral-per-process, and compromise of the *client* does not expose past sessions.
It does nothing for daemon-key compromise, and it breaks device revocation (§5.1).

**Fix:** ephemeral-static (Noise `IK`) or ephemeral-ephemeral with static authentication (`XX` with a
pinned responder key). This is the tracked follow-up already named in `crypto.go:13-14`; it also
closes §4.3 for free.

---

## 5. Authorization as a session property

### 5.1 Device revocation does not revoke (T4, T5)

Three independent reasons it fails:

1. **The revoked identity never comes back.** Revocation is keyed on the client's static public key
   (`devices.go:126-136`), but the app mints a **new keypair every launch**
   (`OculusUI.swift:210`). A stolen phone that is force-quit and reopened presents a key the registry
   has never seen, `enroll` takes the first-sight branch and returns `true`
   (`devices.go:96-100`), and it is authorized. The registry is not a device list; it is a list of
   app launches.
2. **It does not cut live connections.** `TypeDeviceRevoke` calls `RevokeDevice` and rebroadcasts the
   list (`hub.go:3205-3219`); `RevokeDevice` sets a bool and writes a file
   (`devices.go:126-136`). Nothing iterates `h.clients` and closes anything. The revoked device stays
   connected, keeps its role, and keeps driving the agent until it disconnects on its own.
3. **Invite-authenticated clients bypass `enroll` entirely.** `AcceptSecret` calls `h.enroll` only on
   the owner-secret branch; the invite branch returns `true` without touching the device registry
   (`invites.go:153-169`). A guest device therefore cannot be device-revoked at all.

**Fix:** persist the client keypair in the Keychain so device identity is stable (this is a
prerequisite for revocation meaning anything); close every live `*transport.Conn` whose
`PeerPublicKey()` matches on revoke; route invite redemptions through `enroll` too.

### 5.2 Invite revocation does not terminate live sessions (T5)

- Invites: 24h default TTL (`invites.go:28`), independent 128-bit secret (`invites.go:67`), can never
  mint owner (`invites.go:59-63`) — all good.
- **Multi-use, not single-use.** `Redeemed` is a *set of pubkeys*, and the UI displays "used N×"
  (`invites.go:38-39`, `SharingView.swift:155-158`). One leaked link admits unlimited devices for 24
  hours.
- **Revoke does not disconnect.** `inviteRegistry.revoke` deletes map entries
  (`invites.go:117-130`); the handler rebroadcasts participants (`hub.go:4105-4119`). No socket is
  closed. A revoked guest keeps its live connection and its stored role
  (`roles.byConn` is untouched — `roles.go:81-92`) until they leave voluntarily.
- Same for expiry: `roleFor` is consulted once at connect (`hub.go:2002`), so an invite that lapses
  mid-session changes nothing about the live connection.

**Fix:** on revoke/expiry, close every connection whose `PeerPublicKey()` is in `inv.Redeemed`. Make
invites single-use by default, with an explicit "allow multiple devices" opt-in.

### 5.3 Role grants are addressed by a self-asserted display name (T5)

`grantRole` finds its target by case-insensitive match on the client's *declared* name
(`roles.go:149-171`), where the name comes from `client.identify` — a string the client chooses.
`transport.Conn` already carries the authenticated `peerPub` (`transport.go:129-135`), and it is used
for the initial role resolution (`hub.go:2002`), but not here.

A second connection that declares the same display name as a trusted one can receive a `steer` grant
the owner intended for someone else — `grantRole` takes the first match in a map iteration, which is
non-deterministic in Go. A guest who learns a co-worker's device label can position for this.

**Fix:** address role grants by public key; treat the display name as presentation only.

### 5.4 Two failure-open defaults worth knowing about

Neither is wrong for a solo user, but both are sharp edges once sharing is on:

- **Role enforcement defaults off, and off means everyone is owner.** `role()` returns `RoleOwner`
  whenever `!enabled` (`roles.go:68-78`). It is switched on automatically when the first invite is
  redeemed (`invites.go:161-166`) — good. But `roles.enable` is gated at `capApprove`, not
  `capOwner` (`hub.go:4123-4140`), and **whoever calls it becomes owner**
  (`hub.go:4136-4137`). Turning enforcement *off* therefore instantly promotes every connected guest
  to owner. The capability required to do that is `capApprove`, which today only owners hold — so it
  is not currently exploitable, but it is a single role-table change away from being an escalation.
- **`roleForConn` defaults to `RoleOwner`** for any pubkey not found in the invite registry
  (`invites.go:180-185`). The invite registry is in-memory only (`invites.go:42-44`). Any future code
  path that authenticates a client without registering it in `byPub` grants owner by default.

**Fix:** invert the default — resolve to `RoleObserver` unless the connection presented the owner
secret, and record that fact explicitly at handshake time rather than inferring it from an absence.

---

## 6. Other findings

- **Session content leaves the E2E envelope through push notifications (T4, plus Apple).** APNs
  alerts carry `title`/`body` in cleartext to Apple and onto the lock screen
  (`daemon/push/push.go:133`). Content includes the session label, branch, or working-directory
  basename (`hub/session.go:215-227`), the failing test command (`hub.go:1916-1926`), and — the
  broadest one — arbitrary agent error text (`hub.go:1883-1896`, `body = detail`). Project names and
  branch names are frequently the most sensitive metadata a repo has. *Fix:* send content-free pushes
  ("Agent finished — open Iron Rain") by default, with the detail fetched over the encrypted channel;
  make rich bodies opt-in.
- **Slack mirroring is a plaintext egress of agent events** to an arbitrary webhook URL read from
  disk (`main.go:305-314`, `daemon/slack/slack.go:29-45`). Expected behaviour for a feature that says
  "mirror to Slack," but it means the E2E property is off by default for anyone who enables it. Worth
  stating in the UI.
- **`InsecureSkipVerify: true` on the WebSocket accept** (`server/server.go:41-46`) disables
  origin checking. The comment is correct that the E2EE handshake is the real boundary. But the
  daemon binds `0.0.0.0:6000` in the installed configuration (`site/install.sh:91`,
  `DaemonLauncher.swift:56`), so any web page loaded on any device on the LAN can open a WebSocket to
  it. It cannot authenticate — but it can reach the handshake, and combined with §4.3 a browser is a
  usable replay vehicle. *Fix:* restrict origins, or keep the TODO at `server.go:43-44` on the list.
- **No rate limiting on handshake attempts.** Failed handshakes just close (`server.go:60-69`). At
  128 bits of entropy this is not a brute-force concern, but there is no lockout, no backoff, and no
  log-based alerting on repeated failures — so an ongoing attack is invisible.
- **The daemon's static key and secret sit in `~/.oculus` at 0600** (`main.go:883-901`,
  `main.go:863`). Correct for the same-user model; note that it means Time Machine backups and any
  process running as the user hold the keys to every recorded session, forever (§4.4).

---

## 7. Ranked gaps

Ordered by how likely I judge them to be exploited in practice, not by cryptographic elegance.

| # | Gap | Attacker | Fix |
|---|---|---|---|
| **1** | **Permanent owner-equivalent secret distributed in a URL and stored in plaintext** (§3). One screenshot, clipboard sync, or plist read = permanent shell on the user's Mac. Not revocable in practice (§5.1). | T3, T4 | Single-use pairing secret + short TTL; per-device credential after enrollment; Keychain (`ThisDeviceOnly`) on the client; stop printing it to stdout. |
| **2** | **No replay protection in the transport** (§4.3). A recorded client→daemon stream replays verbatim, authenticates, and executes — reachable by a *passive* LAN observer because the direct route has no TLS. | T1, T2 | Server-contributed challenge mixed into the channel keys, plus a per-connection sequence number rejected on non-increase. |
| **3** | **Unauthenticated relay host-slot hijack keyed on a public value** (§4.2). Knowing the daemon's public key — printed, QR'd, and logged by the relay — is enough to evict the real daemon and become the bridge. | T2, T3 | Prove key possession before granting the host slot; or make the live incumbent win over a newcomer. |
| **4** | **Revocation is inert** (§5.1, §5.2). Device revocation cannot bind (client keys are per-launch), never cuts a live socket, and does not apply to invited guests; invite revocation likewise leaves live sessions running. | T4, T5 | Persist the client keypair; close matching live conns on revoke/expiry; route invites through `enroll`. |
| **5** | **No forward secrecy** (§4.4). One future read of `~/.oculus/key` decrypts every session ever recorded. | T2 + T4 | Ephemeral handshake (Noise `IK`/`XX` with pinned responder key) — also closes #2. |
| **6** | **Key-change is silent and re-pairing overwrites the pin without confirmation** (§2). Turns a detectable attack into a support ticket. | T2, T3 | Compare-then-confirm in `applyPairing`; distinguish key-mismatch from unreachable. |
| **7** | **Role grants addressed by self-asserted display name** (§5.3). | T5 | Address by public key. |
| **8** | **Session metadata in APNs pushes and Slack mirroring** (§6). Project/branch names and raw agent error text leave the encrypted envelope. | Apple, T4 | Content-free pushes by default. |
| **9** | **Relay sees a permanent machine identifier** (§4.1). Long-term correlation of a user across IPs and time. | T2 | Blinded, rotating `sid`. |
| **10** | **Failure-open role defaults** (§5.4). Not currently exploitable; one role-table edit from being an escalation. | T5 | Default to observer; record owner-ness explicitly at handshake. |

---

## 8. What I could not determine from the code

Stated explicitly rather than assumed benign.

- **Whether relay logs are retained, and for how long.** `[observability] enabled = true`
  (`relay-cf/wrangler.toml:17-18`) means request URLs — including every `?sid=` and its source IP —
  are captured, but retention, Logpush destinations, and account access are deployment
  configuration not present in this repo. The Fly relay's logging config is not in the repo at all
  (`relay/` contains only a README).
- **The iOS Data Protection class actually applied to the `UserDefaults` plist.** That depends on the
  target's entitlements and build settings, which I did not read. The default
  (`CompleteUntilFirstUserAuthentication`) would mean the secret is readable from a device that has
  booted and been unlocked once — but I am inferring, not reporting.
- **Whether the pairing QR is ever written to the photo library or a share sheet.** I found the
  clipboard path for invites (`SharingView.swift:171-177`) but did not audit every QR-rendering and
  share surface.
- **Whether any deployment terminates the direct route behind TLS** (a tunnel, `--public-url` with
  `wss://`). The default installed configuration is plain `ws://` on `0.0.0.0:6000`, which is what §1
  T1 and §4.3 assume; a user running behind a tunnel is in a better position.
- **Whether `m.meta.label` can contain user prompt text**, which would change §6's push-notification
  severity from "project metadata" to "conversation content." I traced `activityTitle`
  (`hub/session.go:215-227`) but not every writer of `label`.
- **Whether an invite secret could collide with, or be confused for, the owner secret** in
  `AcceptSecret` (`invites.go:153-169`). The owner check runs first, so a collision would silently
  grant owner — at 128 bits this is not a real risk, but I did not find an explicit guard and did not
  run the tests to confirm one exists elsewhere.
- **Anything requiring execution.** Per instruction, I ran no builds or tests; every claim above is
  from reading source.
