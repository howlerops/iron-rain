// Run: node --test cloudflare/telemetry-worker/test/
//
// No Cloudflare harness and no dependencies: the Worker's fetch is an ordinary function over the
// standard Request/Response globals, so it can be called directly. A test that needs a toolchain
// installed is a test nobody runs, and this endpoint had none at all.
import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "../src/index.js";

/** A stub Analytics Engine binding that records what it was asked to write. */
function fakeEnv(extra = {}) {
  const written = [];
  return { env: { TELEMETRY: { writeDataPoint: (p) => written.push(p) }, ...extra }, written };
}

const batch = (events = [{ event: "session.create", ok: true }]) =>
  new Request("https://t.example/ingest", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ version: "0.2.200", os: "darwin", events }),
  });

test("accepts a batch and writes one data point per event", async () => {
  const { env, written } = fakeEnv();
  const res = await worker.fetch(batch([{ event: "a", ok: true }, { event: "b", ok: false }]), env);
  assert.equal(res.status, 200);
  assert.equal(written.length, 2);
});

test("a browser cannot reach it: no CORS headers on any response", async () => {
  // The only client is the daemon, which is not a browser and never needed a preflight.
  // `Access-Control-Allow-Origin: *` did nothing for it and everything for a drive-by: any page in
  // any tab could POST batches into this account's Analytics Engine from a visitor's network.
  const { env } = fakeEnv();
  const res = await worker.fetch(batch(), env);
  assert.equal(res.headers.get("Access-Control-Allow-Origin"), null,
    "the endpoint still advertises itself to browsers, which is the whole drive-by path");
});

test("with no INGEST_KEY configured it accepts everything, so the deploy order cannot break a fleet", async () => {
  const { env, written } = fakeEnv();
  const res = await worker.fetch(batch(), env);
  assert.equal(res.status, 200);
  assert.equal(written.length, 1, "deploying the Worker ahead of the daemons must drop nobody");
});

/** A batch request, optionally carrying an Authorization header. */
const keyedBatch = (auth, events = [{ event: "session.create", ok: true }]) =>
  new Request("https://t.example/ingest", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(auth ? { Authorization: auth } : {}),
    },
    body: JSON.stringify({ version: "0.2.200", os: "darwin", events }),
  });

test("with INGEST_KEY configured, an unkeyed batch is ACCEPTED and marked", async () => {
  // Refusing it was tried and was wrong. Every daemon built before the key existed sends no header,
  // and 401-ing them dropped real telemetry behind an error nobody sees: the daemon does not surface
  // a failed report and nothing else was watching. The data simply stopped, which is
  // indistinguishable from a quiet fleet.
  //
  // The key filters drive-by junk and billed writes; it is not authentication, since it ships inside
  // a binary anyone can download. Marking the batch is what makes the rollover observable, so
  // refusing can be switched on later once it costs nothing.
  const { env, written } = fakeEnv({ INGEST_KEY: "k" });
  const res = await worker.fetch(keyedBatch(null), env);
  assert.equal(res.status, 200, "an unkeyed batch was rejected; every pre-key daemon goes silent");
  const body = await res.json();
  assert.equal(body.accepted, 1);
  assert.equal(body.keyed, false, "the batch must be marked unkeyed, or the rollover is invisible");
  assert.equal(
    written[0].blobs[7],
    "unkeyed",
    "the keyed/unkeyed dimension is not recorded, so there is no way to see how much of the fleet " +
      "still predates the key — which is the fact that decides when refusing is safe"
  );
});

test("a keyed batch is marked keyed", async () => {
  const { env, written } = fakeEnv({ INGEST_KEY: "k" });
  const res = await worker.fetch(keyedBatch("Bearer k"), env);
  assert.equal(res.status, 200);
  assert.equal((await res.json()).keyed, true);
  assert.equal(written[0].blobs[7], "keyed");
  assert.equal(written[0].doubles[3], 1);
});

test("a WRONG key is accepted but counted as unkeyed, not silently trusted", async () => {
  const { env, written } = fakeEnv({ INGEST_KEY: "k" });
  const res = await worker.fetch(keyedBatch("Bearer wrong"), env);
  assert.equal(res.status, 200);
  assert.equal((await res.json()).keyed, false);
  assert.equal(written[0].blobs[7], "unkeyed");
});

test("INGEST_STRICT is what refuses, and only when explicitly set", async () => {
  // A separate switch from the key on purpose: binding a key should not silently begin discarding
  // real data. Strict mode is for once the fleet has rolled and the dashboard shows it.
  const lenient = await worker.fetch(keyedBatch(null), fakeEnv({ INGEST_KEY: "k" }).env);
  assert.equal(lenient.status, 200);

  const strict = await worker.fetch(
    keyedBatch(null),
    fakeEnv({ INGEST_KEY: "k", INGEST_STRICT: "1" }).env
  );
  assert.equal(strict.status, 401, "strict mode did not refuse an unkeyed batch");

  const strictOK = await worker.fetch(
    keyedBatch("Bearer k"),
    fakeEnv({ INGEST_KEY: "k", INGEST_STRICT: "1" }).env
  );
  assert.equal(strictOK.status, 200, "strict mode refused a correctly keyed batch");
});

test("caps the events it will accept from one request", async () => {
  const { env, written } = fakeEnv();
  await worker.fetch(batch(Array.from({ length: 500 }, (_, i) => ({ event: `e${i}`, ok: true }))), env);
  assert.equal(written.length, 100, "the per-request cap is what bounds a single hostile batch");
});

test("rejects anything that is not a POST to /ingest", async () => {
  const { env } = fakeEnv();
  assert.equal((await worker.fetch(new Request("https://t.example/"), env)).status, 404);
  assert.equal((await worker.fetch(new Request("https://t.example/ingest"), env)).status, 404);
});
