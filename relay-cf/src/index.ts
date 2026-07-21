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

// RelayDO bridges one host and one client. It keeps no storage; per-connection role is stashed in
// the socket attachment (survives hibernation) so we can evict a stale host on reconnect.
export class RelayDO {
  ctx: DurableObjectState;

  constructor(ctx: DurableObjectState, _env: Env) {
    this.ctx = ctx;
  }

  async fetch(req: Request): Promise<Response> {
    const role = new URL(req.url).searchParams.get("role")!; // validated by the Worker

    // A fresh host registration supersedes a stale one (reconnect after a half-open connection, or
    // an id collision) — mirrors the Go relay so there is always exactly one live host per sid.
    if (role === "host") {
      for (const ws of this.ctx.getWebSockets()) {
        if (ws.deserializeAttachment()?.role === "host") {
          try { ws.close(1012, "replaced by newer host registration"); } catch { /* already gone */ }
        }
      }
    }

    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    this.ctx.acceptWebSocket(server, [role]); // hibernatable
    server.serializeAttachment({ role });
    return new Response(null, { status: 101, webSocket: client });
  }

  // Forward every frame to the other side of the bridge. With one host + one client this is a plain
  // 1:1 pipe; frame type (binary/text) is preserved so the encrypted transport is untouched.
  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    for (const peer of this.ctx.getWebSockets()) {
      if (peer !== ws) {
        try { peer.send(message); } catch { /* peer closing */ }
      }
    }
  }

  async webSocketClose(ws: WebSocket): Promise<void> {
    this.closePeers(ws, 1000, "peer closed");
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    this.closePeers(ws, 1011, "peer error");
  }

  private closePeers(ws: WebSocket, code: number, reason: string): void {
    for (const peer of this.ctx.getWebSockets()) {
      if (peer !== ws) {
        try { peer.close(code, reason); } catch { /* already gone */ }
      }
    }
  }
}
