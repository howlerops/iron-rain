import { test } from "node:test";
import assert from "node:assert/strict";
import worker from "../src/index.js";

// The /stats page is a credentialled view of account data served from the same Worker as the ingest
// endpoint. Two things therefore have to hold: it must not be readable without the password, and it
// must not be able to break ingest, which is the thing every daemon in the field depends on.

const GET = (path, headers = {}) =>
  worker.fetch(new Request(`https://telemetry.test${path}`, { headers }), envWith());

const envWith = (over = {}) => ({
  TELEMETRY: { writeDataPoint() {} },
  STATS_PASSWORD: "hunter2",
  CF_ACCOUNT_ID: "acc",
  CF_ANALYTICS_TOKEN: "tok",
  ...over,
});

test("no credentials gets a 401 that makes the browser ask", async () => {
  const res = await GET("/stats");
  assert.equal(res.status, 401);
  assert.match(
    res.headers.get("WWW-Authenticate") || "",
    /^Basic /,
    "without this header the browser shows a bare 401 instead of prompting, so the page is " +
      "unreachable to the person it was built for"
  );
});

test("a wrong password is refused", async () => {
  const res = await GET("/stats", { Authorization: "Basic " + btoa("admin:wrong") });
  assert.equal(res.status, 401);
});

test("a malformed Authorization header does not throw", async () => {
  for (const header of ["Basic", "Basic !!!!not-base64", "Bearer hunter2", ""]) {
    const res = await GET("/stats", { Authorization: header });
    assert.equal(res.status, 401, `header ${JSON.stringify(header)} should be refused, not crash`);
  }
});

test("with no STATS_PASSWORD configured the page refuses rather than opening up", async () => {
  const res = await worker.fetch(
    new Request("https://telemetry.test/stats"),
    envWith({ STATS_PASSWORD: undefined })
  );
  assert.equal(
    res.status,
    503,
    "an unset password must not mean an unauthenticated dashboard; failing closed is the only " +
      "safe reading of a missing credential"
  );
  assert.doesNotMatch(await res.text(), /oculus_telemetry/, "the setup page leaks query internals");
});

test("authenticated, but unconfigured for reading, explains what is missing", async () => {
  const res = await worker.fetch(
    new Request("https://telemetry.test/stats", {
      headers: { Authorization: "Basic " + btoa("x:hunter2") },
    }),
    envWith({ CF_ANALYTICS_TOKEN: undefined })
  );
  assert.equal(res.status, 503);
  const body = await res.text();
  assert.match(body, /CF_ANALYTICS_TOKEN/);
  assert.doesNotMatch(body, /hunter2/, "the setup page must never echo the password back");
});

test("values from the dataset are escaped into the page", async () => {
  // An error string is attacker-influenced in principle: it originates in a provider's output,
  // travels through a daemon, and lands in a table cell here. Rendering it raw would make this
  // dashboard a stored-XSS sink for anything that can post a batch.
  const evil = `</td><script>alert(1)</script>`;
  const realFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ data: [{ event: evil, error: evil, n: 1, failed: 0 }] }), {
      status: 200,
    });
  try {
    const res = await worker.fetch(
      new Request("https://telemetry.test/stats", {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    const body = await res.text();
    assert.equal(res.status, 200);
    assert.doesNotMatch(body, /<script>alert\(1\)<\/script>/, "a dataset value reached the page unescaped");
    assert.match(body, /&lt;script&gt;/, "the value should be present, escaped");
    assert.equal(res.headers.get("Cache-Control"), "no-store", "a credentialled page must not be cached");
  } finally {
    globalThis.fetch = realFetch;
  }
});

test("a failing Analytics query reports instead of rendering an empty dashboard", async () => {
  // Silently showing zeroes is worse than an error: it reads as "nothing is happening" when the
  // truth is "nothing was asked".
  const realFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response("bad token", { status: 403 });
  try {
    const res = await worker.fetch(
      new Request("https://telemetry.test/stats", {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    assert.equal(res.status, 502);
    assert.match(await res.text(), /query failed/i);
  } finally {
    globalThis.fetch = realFetch;
  }
});

test("adding /stats did not disturb ingest", async () => {
  let written = 0;
  const res = await worker.fetch(
    new Request("https://telemetry.test/ingest", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ version: "1", events: [{ event: "session.create", ok: true }] }),
    }),
    envWith({ TELEMETRY: { writeDataPoint: () => void written++ } })
  );
  assert.equal(res.status, 200);
  assert.equal(written, 1);
  assert.equal((await res.json()).accepted, 1);
});

test("/stats is GET-only and unknown paths still 404", async () => {
  const post = await worker.fetch(
    new Request("https://telemetry.test/stats", { method: "POST" }),
    envWith()
  );
  assert.equal(post.status, 404, "POST /stats should fall through to the 404, not the dashboard");
  const other = await GET("/nope");
  assert.equal(other.status, 404);
});
