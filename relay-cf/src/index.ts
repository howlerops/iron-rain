// Iron Rain relay on Cloudflare Workers + Durable Objects.
//
// A daemon ("host") and an app ("client") that share a server_id are bridged by a single Durable
// Object — chosen by idFromName(server_id) — so both always land on the SAME logical instance no
// matter where on Earth they connect from (this is what makes multi-region "just work" without a
// backplane). The relay forwards ONLY opaque bytes: session content stays end-to-end encrypted and
// the relay cannot read it, exactly like the Go/Fly relay.
//
// Registration is by URL query (?sid=<hex pubkey>&role=host|client) rather than a first frame, so
// the Worker can route to the right DO WITHOUT reading the socket — which is what lets the DO use
// the WebSocket Hibernation API. Hibernation is the whole point: the daemon's host connection is
// open 24/7 but idle almost always, and hibernating sockets are not billed for wall-clock time.
//
// A host may add &pop=1 to prove it holds the private key behind the sid before it is allowed to
// take the host slot — see popChallenge below for what that closes and why it is opt-in.

export interface Env {
  RELAY: DurableObjectNamespace;
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    if (url.pathname === "/healthz") return new Response("ok");
    if (url.pathname !== "/ws") return new Response("iron rain relay (cloudflare)");

    const sid = url.searchParams.get("sid");
    const role = url.searchParams.get("role");
    if (!sid || (role !== "host" && role !== "client")) {
      return new Response("bad registration: need ?sid=&role=host|client", { status: 400 });
    }
    // A proof can only be checked against a real public key. Reject an impossible request at the
    // edge rather than accepting a socket that can never satisfy the challenge.
    if (url.searchParams.get("pop") === "1" && decodeHex(sid)?.length !== 32) {
      return new Response("bad registration: ?pop=1 needs a 32-byte hex public-key sid", { status: 400 });
    }
    if (req.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return new Response("expected a WebSocket upgrade", { status: 426 });
    }
    // Route host + client with the same sid to the same object.
    return env.RELAY.get(env.RELAY.idFromName(sid)).fetch(req);
  },
};

type Role = "host" | "client";

// What we stash on each socket. It survives hibernation, so it — not in-memory state — is the only
// place the DO may remember anything about a connection.
//
// `superseded` marks a socket we have evicted but whose close handshake has not completed yet. It
// matters because eviction is asynchronous: the evicted peer's `webSocketClose` can arrive AFTER its
// replacement has already paired, and without this flag that late event tore down the NEW bridge
// (reproduced: registering a second host left the fresh host connected but unable to reach a client).
//
// `want` is a host's outstanding proof-of-possession answer (hex MAC). A socket that has one is
// PENDING: it is accepted and tagged, but it is not part of the bridge, cannot be reached by a
// client, and has evicted nobody. `proven` is set once the answer matched, and is what makes the
// socket un-evictable by a host that has not proven anything.
type Attachment = { role: Role; superseded?: boolean; want?: string; proven?: boolean };

// Close codes. 1008 (policy violation) mirrors the Go relay's StatusPolicyViolation for "no host",
// so the app maps a relay refusal to `.unreachable` identically no matter which relay answered.
// 1012 (service restart) is "you were replaced, reconnect" — a different situation and a different
// client response, so it deliberately does NOT share a code with the refusal.
const CLOSE_NO_HOST = 1008;
const CLOSE_SUPERSEDED = 1012;
// 1008 again, and deliberately: a refused host registration is a policy refusal, and it matches the
// Go relay, which answers both refusals with StatusPolicyViolation. Only hosts ever see these two.
const CLOSE_SLOT_TAKEN = 1008;
const CLOSE_BAD_PROOF = 1008;

// Application-level keepalive. Protocol-level ping frames are already answered by the runtime
// without waking a hibernating DO, but a client that can only send data frames (or that wants an
// end-to-end, relay-observable liveness check) sends this token instead. setWebSocketAutoResponse
// answers it inside the runtime: no wall-clock billing, no hibernation break, and — importantly —
// the token is never delivered to webSocketMessage, so it can never be forwarded to the peer as if
// it were session ciphertext.
const KEEPALIVE_PING = "ir-ping";
const KEEPALIVE_PONG = "ir-pong";

// RelayDO bridges exactly ONE host and ONE client, routed by role — the same semantics as the Go
// relay (daemon/relay/relay.go). It keeps no storage.
//
// Role routing is not an optimisation. The previous implementation forwarded every frame to every
// other socket, which meant a second device racing onto the same server_id received a copy of the
// first device's encrypted stream and fed the daemon two interleaved senders on one encrypted
// channel — corrupting a live bridge rather than being rejected. (That channel is static-static
// X25519 ECDH -> HKDF-SHA256 -> ChaCha20-Poly1305, described in daemon/crypto/crypto.go. An earlier
// version of this comment called it a Noise session. It is not Noise, there is no ephemeral
// handshake, and the difference is exactly why the daemon's key alone cannot authenticate a
// registration — see popChallenge.)
export class RelayDO {
  ctx: DurableObjectState;

  constructor(ctx: DurableObjectState, _env: Env) {
    this.ctx = ctx;
    this.ctx.setWebSocketAutoResponse(
      new WebSocketRequestResponsePair(KEEPALIVE_PING, KEEPALIVE_PONG),
    );
  }

  async fetch(req: Request): Promise<Response> {
    const params = new URL(req.url).searchParams;
    const role = params.get("role") as Role; // validated by the Worker
    const pop = params.get("pop") === "1"; // sid validated as a 32-byte hex key by the Worker

    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    const upgraded = () => new Response(null, { status: 101, webSocket: client });
    // Refused sockets are accepted plainly and closed at once, NOT accepted as hibernatable: they
    // are dead on arrival and must never appear in getWebSockets().
    const refuse = (code: number, reason: string) => {
      server.accept();
      server.close(code, reason);
      return upgraded();
    };

    // No host registered means the daemon is not reachable through this relay, full stop. Say so
    // NOW: an accepted-then-silent socket is indistinguishable from a slow one, which is why the app
    // pads every offline determination with a timeout instead of trusting the relay.
    if (role === "client" && this.live("host").length === 0) {
      return refuse(CLOSE_NO_HOST, "no host for server_id");
    }

    if (role === "host" && pop) {
      // A host that will prove itself is accepted PENDING: it is tagged and readable, but `want`
      // keeps it out of live(), so until it answers it bridges nothing and — crucially — has
      // evicted nobody. Eviction is deferred to the proof (settleProof) because the answer is what
      // decides whether the claim is allowed at all, and it cannot arrive before this response.
      //
      // Any earlier pending socket is dropped first: a pending socket costs a connection slot and
      // nothing may accumulate them.
      this.closePending();
      const { challenge, want } = await popChallenge(decodeHex(params.get("sid")!)!);
      this.ctx.acceptWebSocket(server, [role]);
      server.serializeAttachment({ role, want } satisfies Attachment);
      server.send(challenge);
      return upgraded();
    }

    // THE HIJACK FIX. The server_id is the daemon's PUBLIC key — printed on daemon start, in every
    // pairing QR, and in this relay's own request logs — so "newest registration wins" meant anyone
    // who had seen that value could evict the real daemon, kill remote access for as long as they
    // cared to keep re-registering, and sit in the bridge position recording ciphertext. A host
    // that has proven possession of the private key is not evictable by one that has not.
    if (role === "host" && this.live("host").some((ws) => this.attachment(ws)?.proven)) {
      return refuse(CLOSE_SLOT_TAKEN, "host slot held by a proven host");
    }

    // Newest registration of a role otherwise wins, for hosts (a re-dial after a half-open
    // connection would otherwise leave two hosts) and for clients alike (a second device must
    // replace the first, not join it — one host socket carries exactly one encrypted session).
    //
    // Between two UNPROVEN hosts this is still a race anyone can enter, and that is deliberate:
    // daemons in the field do not implement the proof yet, and refusing a newcomer on their behalf
    // would pin a user's remote access to a stale registration they could never displace. The
    // protection arrives with the daemon update, not by making the relay stricter about a value
    // that was never secret.
    this.supersede(
      role,
      role === "host"
        ? "replaced by newer host registration"
        : "replaced by newer client registration",
    );

    this.ctx.acceptWebSocket(server, [role]); // tag = role, so getWebSockets(role) is the lookup
    server.serializeAttachment({ role } satisfies Attachment);
    return upgraded();
  }

  // Forward each frame to the socket holding the OTHER role. Frame type (binary/text) is preserved
  // so the encrypted transport is untouched.
  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const att = this.attachment(ws);
    if (!att || att.superseded) return; // an evicted socket's last frames are not part of the bridge
    if (att.want !== undefined) return this.settleProof(ws, att, message); // pending: not bridged
    for (const peer of this.live(other(att.role))) {
      try { peer.send(message); } catch { /* peer closing */ }
    }
  }

  // The other half of the pending-host handshake: a correct answer takes the host slot (evicting
  // whoever held it, proven or not — only the real daemon can get here), a wrong or missing one is
  // closed. A pending socket that simply never answers stays pending and inert until it goes away;
  // it holds no slot, so there is nothing to time out.
  private async settleProof(ws: WebSocket, att: Attachment, message: string | ArrayBuffer): Promise<void> {
    const got = typeof message === "string" ? proofMac(message) : null;
    if (got === null || !equalHex(got, att.want!)) {
      ws.close(CLOSE_BAD_PROOF, "host proof failed");
      return;
    }
    this.supersede(att.role, "replaced by newer host registration");
    ws.serializeAttachment({ role: att.role, proven: true } satisfies Attachment);
  }

  async webSocketClose(ws: WebSocket): Promise<void> {
    this.closePeer(ws, 1000, "peer closed");
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    this.closePeer(ws, 1011, "peer error");
  }

  // When one half of the bridge goes, the other half is useless — closing it is what makes the
  // daemon's re-dial loop run and re-register a fresh host socket.
  private closePeer(ws: WebSocket, code: number, reason: string): void {
    const att = this.attachment(ws);
    if (!att || att.superseded) return; // see Attachment: a late eviction must not kill its successor
    if (att.want !== undefined) return; // a pending host was never bridged; its exit changes nothing
    for (const peer of this.live(other(att.role))) {
      try { peer.close(code, reason); } catch { /* already gone */ }
    }
  }

  private supersede(role: Role, reason: string): void {
    for (const ws of this.live(role)) {
      try {
        // Flag BEFORE closing: the close event races back to us and must find the flag set.
        ws.serializeAttachment({ role, superseded: true } satisfies Attachment);
        ws.close(CLOSE_SUPERSEDED, reason);
      } catch { /* already gone */ }
    }
  }

  /** Drops any host still waiting to answer a challenge, so pending sockets cannot accumulate. */
  private closePending(): void {
    for (const ws of this.ctx.getWebSockets("host")) {
      const att = this.attachment(ws);
      if (att?.want === undefined || att.superseded) continue;
      try {
        ws.serializeAttachment({ role: att.role, superseded: true } satisfies Attachment);
        ws.close(CLOSE_SUPERSEDED, "replaced by newer host registration");
      } catch { /* already gone */ }
    }
  }

  /**
   * Sockets holding `role` that are part of the bridge: not evicted, and not still owing a proof.
   * A pending host is excluded deliberately — a client must not be bridged to a socket that has not
   * yet shown it is the daemon.
   */
  private live(role: Role): WebSocket[] {
    return this.ctx.getWebSockets(role).filter((ws) => {
      const att = this.attachment(ws);
      return att?.superseded !== true && att?.want === undefined;
    });
  }

  private attachment(ws: WebSocket): Attachment | null {
    try {
      return (ws.deserializeAttachment() as Attachment | null) ?? null;
    } catch {
      return null;
    }
  }
}

const other = (role: Role): Role => (role === "host" ? "client" : "host");

// --- proof of possession -----------------------------------------------------------------------
//
// WHY: this relay handed the host slot to whoever could NAME a server_id, and the server_id IS the
// daemon's public key. It is printed to stdout on every daemon start, embedded in every pairing QR,
// stored in ~/.oculus/pairing.json, and written into this Worker's own request logs (observability
// is on). Registration was therefore unauthenticated in practice: anyone who had seen that value
// could register as the host, evict the real daemon — whose re-dial loop re-registers and is
// evicted again, forever — and take the bridge position.
//
// That does not give them plaintext: the channel is sealed to the daemon's key and they hold no
// private key, so clients that reach them just fail the handshake. It gives them the user's remote
// access (a denial of service that presents as "the relay is flaky") and a recording position,
// which matters because the transport has no replay protection — a recorded client->daemon stream
// re-authenticates and re-executes against the real daemon later
// (docs/security-interception-review.md §4.2, §4.3).
//
// So the claim must cost the PRIVATE key. The daemon's identity key is X25519 — an agreement key
// with no signature scheme attached — so the proof is an ECDH one:
//
//   relay -> host   {"ir":"pop-challenge","v":1,"eph":<fresh X25519 public key>,"nonce":<32 bytes>}
//   host  -> relay  {"ir":"pop-proof","v":1,"mac":HMAC(prk, nonce || sid)}
//                   shared = X25519(daemonPriv, eph), prk = HMAC("iron-rain/relay-pop/v1", shared)
//
// The relay computes the same shared secret as X25519(ephPriv, sid), so only the holder of the key
// behind sid can produce the MAC. eph and nonce are fresh per connection, so a proof captured off
// the wire is worthless against the next challenge — this does not inherit the transport's replay
// gap. This must stay byte-for-byte identical to daemon/relay/pop.go; the two are one wire contract.
//
// Note what is NOT kept: the ephemeral private key. The expected MAC is computed at challenge time
// and stashed in the socket attachment, which is the only DO state that survives hibernation. That
// keeps the always-idle host socket hibernatable, which is the entire economics of this relay.

const POP_LABEL = "iron-rain/relay-pop/v1";
const POP_CHALLENGE = "pop-challenge";
const POP_PROOF = "pop-proof";

async function popChallenge(sid: Uint8Array): Promise<{ challenge: string; want: string }> {
  const eph = (await crypto.subtle.generateKey({ name: "X25519" }, true, [
    "deriveBits",
  ])) as CryptoKeyPair;
  // exportKey is typed as ArrayBuffer | JsonWebKey across all formats; "raw" is always the former.
  const ephPub = new Uint8Array((await crypto.subtle.exportKey("raw", eph.publicKey)) as ArrayBuffer);
  const sidKey = await crypto.subtle.importKey("raw", sid, { name: "X25519" }, false, []);
  // workers-types spells the peer key `$public` in its generated declarations; the runtime property
  // is `public`, per the Secure Curves API.
  const agree = { name: "X25519", public: sidKey } as unknown as SubtleCryptoDeriveKeyAlgorithm;
  const shared = new Uint8Array(await crypto.subtle.deriveBits(agree, eph.privateKey, 256));

  const nonce = crypto.getRandomValues(new Uint8Array(32));
  const prk = await hmac(new TextEncoder().encode(POP_LABEL), shared);
  const want = encodeHex(await hmac(prk, concat(nonce, sid)));
  return {
    challenge: JSON.stringify({
      ir: POP_CHALLENGE,
      v: 1,
      eph: encodeHex(ephPub),
      nonce: encodeHex(nonce),
    }),
    want,
  };
}

async function hmac(key: Uint8Array, msg: Uint8Array): Promise<Uint8Array> {
  const k = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, [
    "sign",
  ]);
  return new Uint8Array(await crypto.subtle.sign("HMAC", k, msg));
}

/** The MAC out of a pop-proof frame, or null if this is not one. */
function proofMac(message: string): string | null {
  try {
    const msg = JSON.parse(message) as { ir?: string; mac?: string };
    return msg.ir === POP_PROOF && typeof msg.mac === "string" ? msg.mac : null;
  } catch {
    return null;
  }
}

/**
 * Compares two hex MACs without leaking where they diverge. A naive `===` on a value the caller
 * supplies is a byte-at-a-time oracle for a value they otherwise cannot compute.
 */
function equalHex(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

function decodeHex(s: string): Uint8Array | null {
  if (s.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(s)) return null;
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.substr(i * 2, 2), 16);
  return out;
}

function encodeHex(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}
