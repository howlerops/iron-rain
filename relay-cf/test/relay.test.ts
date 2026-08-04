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

  static async connect(sid: string, role: "host" | "client", pop = false): Promise<Peer> {
    const res = await SELF.fetch(`https://relay.test/ws?sid=${sid}&role=${role}${pop ? "&pop=1" : ""}`, {
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

// --- the daemon half of the proof of possession (mirrors daemon/relay/pop.go) --------------------

const POP_LABEL = "iron-rain/relay-pop/v1";

const hex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
const unhex = (s: string) => new Uint8Array(s.match(/../g)!.map((h) => parseInt(h, 16)));

async function hmac(key: Uint8Array, msg: Uint8Array): Promise<Uint8Array> {
  const k = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, [
    "sign",
  ]);
  return new Uint8Array(await crypto.subtle.sign("HMAC", k, msg));
}

type DaemonKey = { sid: string; priv: CryptoKey };

/** A daemon identity: an X25519 key pair whose PUBLIC half is the server_id. */
async function daemonKey(): Promise<DaemonKey> {
  const kp = (await crypto.subtle.generateKey({ name: "X25519" }, true, [
    "deriveBits",
  ])) as CryptoKeyPair;
  const pub = new Uint8Array((await crypto.subtle.exportKey("raw", kp.publicKey)) as ArrayBuffer);
  return { sid: hex(pub), priv: kp.privateKey };
}

/** Registers as a host and answers the challenge — what an updated daemon does on every dial. */
async function provenHost(key: DaemonKey): Promise<Peer> {
  const host = await Peer.connect(key.sid, "host", true);
  const challenge = JSON.parse((await host.waitMessage()) as string);
  expect(challenge.ir).toBe("pop-challenge");

  const eph = await crypto.subtle.importKey("raw", unhex(challenge.eph), { name: "X25519" }, false, []);
  // workers-types spells the peer key `$public`; the runtime property is `public`.
  const agree = { name: "X25519", public: eph } as unknown as SubtleCryptoDeriveKeyAlgorithm;
  const shared = new Uint8Array(await crypto.subtle.deriveBits(agree, key.priv, 256));

  const nonce = unhex(challenge.nonce);
  const sid = unhex(key.sid);
  const signed = new Uint8Array(nonce.length + sid.length);
  signed.set(nonce);
  signed.set(sid, nonce.length);

  const prk = await hmac(new TextEncoder().encode(POP_LABEL), shared);
  host.send(JSON.stringify({ ir: "pop-proof", v: 1, mac: hex(await hmac(prk, signed)) }));
  await settle();
  return host;
}

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

  it("refuses an unproven host while a proven host holds the slot", async () => {
    // The host-slot hijack (docs/security-interception-review.md §4.2). The sid is the daemon's
    // PUBLIC key — printed on daemon start, in every pairing QR, in this Worker's request logs — so
    // before the proof, presenting it was enough to evict the real daemon, kill remote access for
    // as long as you kept re-registering, and take the bridge position.
    const key = await daemonKey();
    const daemon = await provenHost(key);

    const attacker = await Peer.connect(key.sid, "host"); // knows the sid and nothing else
    const refused = await attacker.waitClose();
    expect(refused.code).toBe(1008);
    expect(refused.reason).toBe("host slot held by a proven host");

    // The daemon is still the bridge, not merely still connected.
    const client = await Peer.connect(key.sid, "client");
    client.send("c2h");
    expect(await daemon.waitMessage()).toBe("c2h");
    expect(daemon.closeInfo).toBeNull();
  });

  it("lets a proven host reclaim its own slot", async () => {
    // The protection must not lock the daemon out of its own registration: its re-dial loop opens a
    // fresh socket for every client, and each of those proves itself.
    const key = await daemonKey();
    const stale = await provenHost(key);
    const fresh = await provenHost(key);

    expect((await stale.waitClose()).code).toBe(1012);

    const client = await Peer.connect(key.sid, "client");
    client.send("to-fresh-host");
    expect(await fresh.waitMessage()).toBe("to-fresh-host");
  });

  it("lets a proven host evict an unproven squatter", async () => {
    // Why the rule is "proven beats unproven" and not "the incumbent always wins": the latter would
    // have turned an eviction loop into a permanent squat by whoever registered first, which is
    // worse. The real daemon must always be able to take its slot back.
    const key = await daemonKey();
    const squatter = await Peer.connect(key.sid, "host");
    await settle();

    const daemon = await provenHost(key);
    expect((await squatter.waitClose()).code).toBe(1012);

    const client = await Peer.connect(key.sid, "client");
    client.send("c2h");
    expect(await daemon.waitMessage()).toBe("c2h");
  });

  it("closes a host whose proof does not verify and leaves the incumbent alone", async () => {
    // Opting in with a wrong answer must not evict the incumbent as a side effect of being refused.
    const key = await daemonKey();
    const daemon = await provenHost(key);

    const attacker = await Peer.connect(key.sid, "host", true);
    await attacker.waitMessage(); // the challenge
    attacker.send(JSON.stringify({ ir: "pop-proof", v: 1, mac: "aa".repeat(32) }));
    const refused = await attacker.waitClose();
    expect(refused.code).toBe(1008);
    expect(refused.reason).toBe("host proof failed");

    const client = await Peer.connect(key.sid, "client");
    client.send("c2h");
    expect(await daemon.waitMessage()).toBe("c2h");
    expect(daemon.closeInfo).toBeNull();
  });

  it("does not bridge a client to a host that has not answered its challenge yet", async () => {
    // A pending host holds no slot: it is deliberately invisible until it proves itself, so the
    // window between registering and answering reads as "no host" rather than as a bridge to
    // someone unverified. It is one round trip wide, and the app already retries an unreachable
    // relay.
    const key = await daemonKey();
    const pending = await Peer.connect(key.sid, "host", true);
    await pending.waitMessage(); // challenge received, deliberately unanswered

    const client = await Peer.connect(key.sid, "client");
    expect((await client.waitClose()).code).toBe(1008);
    expect(pending.closeInfo).toBeNull();
  });

  it("rejects ?pop=1 on a server_id that is not a public key", async () => {
    // A challenge against a non-key can never be answered, so it is refused at the edge rather than
    // becoming a socket that hangs until the daemon gives up.
    const res = await SELF.fetch("https://relay.test/ws?sid=srv-1&role=host&pop=1", {
      headers: { Upgrade: "websocket" },
    });
    expect(res.status).toBe(400);
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
