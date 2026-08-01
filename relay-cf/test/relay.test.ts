import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

// A connected relay peer with everything it received already recorded. Listeners are
// attached synchronously with accept() so a close the DO sends immediately (the "no host"
// case) can't be missed.
class Peer {
  readonly messages: (string | ArrayBuffer)[] = [];
  closeInfo: { code: number; reason: string } | null = null;
  private readonly ws: WebSocket;

  private constructor(ws: WebSocket) {
    this.ws = ws;
    ws.accept();
    ws.addEventListener("message", (e: MessageEvent) => {
      this.messages.push(e.data as string | ArrayBuffer);
    });
    ws.addEventListener("close", (e: CloseEvent) => {
      this.closeInfo = { code: e.code, reason: e.reason };
    });
  }

  static async connect(sid: string, role: "host" | "client"): Promise<Peer> {
    const res = await SELF.fetch(`https://relay.test/ws?sid=${sid}&role=${role}`, {
      headers: { Upgrade: "websocket" },
    });
    if (!res.webSocket) throw new Error(`no webSocket on response (status ${res.status})`);
    return new Peer(res.webSocket);
  }

  send(data: string) {
    this.ws.send(data);
  }

  /** Resolves once the socket has been closed by the DO, or throws after `ms`. */
  async waitClose(ms = 2000): Promise<{ code: number; reason: string }> {
    const deadline = Date.now() + ms;
    while (!this.closeInfo) {
      if (Date.now() > deadline) throw new Error("timed out waiting for close");
      await scheduler.wait(5);
    }
    return this.closeInfo;
  }

  /** Resolves with the next message, or throws after `ms`. */
  async waitMessage(ms = 2000): Promise<string | ArrayBuffer> {
    const start = this.messages.length;
    const deadline = Date.now() + ms;
    while (this.messages.length === start) {
      if (Date.now() > deadline) throw new Error("timed out waiting for message");
      await scheduler.wait(5);
    }
    return this.messages[start];
  }
}

/** Lets any in-flight forwarding settle, so "did NOT receive" assertions are meaningful. */
const settle = () => scheduler.wait(100);

let n = 0;
const freshSid = () => `sid-${Date.now()}-${n++}`; // one Durable Object per test, no state leak

describe("relay DO", () => {
  it("closes a client with a policy code when no host is registered", async () => {
    // The app pads every offline determination by ~12s today precisely because this case
    // hangs. A 1008 close is an immediate, unambiguous "unreachable" — same code and
    // reason the Go relay sends (relay.go: StatusPolicyViolation, "no host for server_id").
    const client = await Peer.connect(freshSid(), "client");
    const info = await client.waitClose();
    expect(info.code).toBe(1008);
    expect(info.reason).toBe("no host for server_id");
  });

  it("routes frames by role rather than broadcasting to every socket", async () => {
    const sid = freshSid();
    const host = await Peer.connect(sid, "host");
    const client = await Peer.connect(sid, "client");

    client.send("c2h");
    expect(await host.waitMessage()).toBe("c2h");
    host.send("h2c");
    expect(await client.waitMessage()).toBe("h2c");

    // Neither side may ever be handed its own frame back — a same-role broadcast would
    // feed the daemon its own ciphertext.
    await settle();
    expect(host.messages).toEqual(["c2h"]);
    expect(client.messages).toEqual(["h2c"]);
  });

  it("supersedes an older client so a second device cannot corrupt a live bridge", async () => {
    // This is the bug: webSocketMessage forwarded every frame to EVERY other socket, so a
    // second device racing in got a copy of the first device's encrypted stream (and the
    // daemon got two interleaved senders on one Noise session). One host + one client per
    // sid, newest wins — mirroring the Go relay's host rule.
    const sid = freshSid();
    const host = await Peer.connect(sid, "host");
    const first = await Peer.connect(sid, "client");
    const second = await Peer.connect(sid, "client");

    const closed = await first.waitClose();
    expect(closed.code).toBe(1012);

    host.send("only-for-second");
    expect(await second.waitMessage()).toBe("only-for-second");
    await settle();
    expect(first.messages).toEqual([]);
    expect(host.closeInfo).toBeNull(); // the host survives a client swap
  });

  it("supersedes an older host registration", async () => {
    const sid = freshSid();
    const stale = await Peer.connect(sid, "host");
    const fresh = await Peer.connect(sid, "host");

    expect((await stale.waitClose()).code).toBe(1012);

    const client = await Peer.connect(sid, "client");
    client.send("to-fresh-host");
    expect(await fresh.waitMessage()).toBe("to-fresh-host");
    await settle();
    expect(stale.messages).toEqual([]);
  });

  it("answers the app-level keepalive itself without forwarding it to the peer", async () => {
    // setWebSocketAutoResponse keeps this off the DO's wall clock (hibernation preserved),
    // and the peer must never see the keepalive token as if it were session ciphertext.
    const sid = freshSid();
    const host = await Peer.connect(sid, "host");
    const client = await Peer.connect(sid, "client");

    client.send("ir-ping");
    expect(await client.waitMessage()).toBe("ir-pong");
    await settle();
    expect(host.messages).toEqual([]);
  });
});
