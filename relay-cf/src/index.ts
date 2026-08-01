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
type Attachment = { role: Role; superseded?: boolean };

// Close codes. 1008 (policy violation) mirrors the Go relay's StatusPolicyViolation for "no host",
// so the app maps a relay refusal to `.unreachable` identically no matter which relay answered.
// 1012 (service restart) is "you were replaced, reconnect" — a different situation and a different
// client response, so it deliberately does NOT share a code with the refusal.
const CLOSE_NO_HOST = 1008;
const CLOSE_SUPERSEDED = 1012;

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
// first device's encrypted stream and fed the daemon two interleaved senders on one Noise session —
// corrupting a live bridge rather than being rejected.
export class RelayDO {
  ctx: DurableObjectState;

  constructor(ctx: DurableObjectState, _env: Env) {
    this.ctx = ctx;
    this.ctx.setWebSocketAutoResponse(
      new WebSocketRequestResponsePair(KEEPALIVE_PING, KEEPALIVE_PONG),
    );
  }

  async fetch(req: Request): Promise<Response> {
    const role = new URL(req.url).searchParams.get("role") as Role; // validated by the Worker

    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];

    // No host registered means the daemon is not reachable through this relay, full stop. Say so
    // NOW: an accepted-then-silent socket is indistinguishable from a slow one, which is why the app
    // pads every offline determination with a timeout instead of trusting the relay. Not accepted as
    // hibernatable — this socket is dead on arrival and must never appear in getWebSockets().
    if (role === "client" && this.live("host").length === 0) {
      server.accept();
      server.close(CLOSE_NO_HOST, "no host for server_id");
      return new Response(null, { status: 101, webSocket: client });
    }

    // Newest registration of a role wins, for hosts (a re-dial after a half-open connection would
    // otherwise leave two hosts) and for clients alike (a second device must replace the first, not
    // join it — one host socket carries exactly one Noise session).
    this.supersede(
      role,
      role === "host"
        ? "replaced by newer host registration"
        : "replaced by newer client registration",
    );

    this.ctx.acceptWebSocket(server, [role]); // tag = role, so getWebSockets(role) is the lookup
    server.serializeAttachment({ role } satisfies Attachment);
    return new Response(null, { status: 101, webSocket: client });
  }

  // Forward each frame to the socket holding the OTHER role. Frame type (binary/text) is preserved
  // so the encrypted transport is untouched.
  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const att = this.attachment(ws);
    if (!att || att.superseded) return; // an evicted socket's last frames are not part of the bridge
    for (const peer of this.live(other(att.role))) {
      try { peer.send(message); } catch { /* peer closing */ }
    }
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

  /** Sockets holding `role` that have not been evicted. */
  private live(role: Role): WebSocket[] {
    return this.ctx.getWebSockets(role).filter((ws) => this.attachment(ws)?.superseded !== true);
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
