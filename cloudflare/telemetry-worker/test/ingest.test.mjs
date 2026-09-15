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

test("with INGEST_KEY configured it refuses a batch with no key", async () => {
  const { env, written } = fakeEnv({ INGEST_KEY: "s3cret" });
  const res = await worker.fetch(batch(), env);
  assert.equal(res.status, 401);
  assert.equal(written.length, 0, "an unauthenticated batch still reached Analytics Engine");
});

test("with INGEST_KEY configured it refuses a wrong key and accepts the right one", async () => {
  const { env: bad, written: badWrites } = fakeEnv({ INGEST_KEY: "s3cret" });
  const wrong = batch();
  wrong.headers.set("Authorization", "Bearer nope");
  assert.equal((await worker.fetch(wrong, bad)).status, 401);
  assert.equal(badWrites.length, 0);

  const { env: good, written: goodWrites } = fakeEnv({ INGEST_KEY: "s3cret" });
  const right = batch();
  right.headers.set("Authorization", "Bearer s3cret");
  assert.equal((await worker.fetch(right, good)).status, 200);
  assert.equal(goodWrites.length, 1);
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
