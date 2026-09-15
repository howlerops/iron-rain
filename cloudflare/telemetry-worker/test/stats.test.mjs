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

// ---- rendering bugs a passing test suite did not catch -------------------------------------------
//
// Both of these shipped green and were found by rendering the page and looking at it. They are
// pinned here because "the tests pass" was true while the chart was missing a series and a duration
// read "18.3kms".

const renderPage = async (fixtures) => {
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (_u, init) => {
    const sql = String(init?.body || "");
    const pick = sql.includes("toStartOfInterval")
      ? fixtures.series
      : sql.includes("quantileWeighted")
        ? fixtures.timing
        : [];
    return new Response(JSON.stringify({ data: pick }), { status: 200 });
  };
  try {
    const res = await worker.fetch(
      new Request("https://telemetry.test/stats?days=30", {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    return await res.text();
  } finally {
    globalThis.fetch = realFetch;
  }
};

test("the failures series is drawn as a sibling, not swallowed by the previous path", async () => {
  // The bug: `vector-effect=non-scaling-stroke/>` — an UNQUOTED attribute value takes the `/` into
  // itself, so the tag never self-closes and the next <path> is parsed as its CHILD. SVG does not
  // render path children, so the failures line vanished while remaining present in the markup.
  const body = await renderPage({
    series: Array.from({ length: 10 }, (_, i) => ({ t: i, n: 100 + i, failed: 3 + i })),
    timing: [],
  });

  assert.match(body, /stroke="var\(--bad\)"/, "no failures path was emitted at all");

  const opens = (body.match(/<path\b/g) || []).length;
  const closes = (body.match(/<\/path>/g) || []).length;
  assert.equal(
    opens,
    closes,
    `${opens} <path> opened but ${closes} closed. An unclosed path adopts everything after it as ` +
      `children, which SVG does not render — the element is in the DOM and paints nothing.`
  );

  assert.deepEqual(
    body.match(/=[^"'\s>]+\/>/g),
    null,
    "an unquoted attribute value is immediately followed by '/>', which puts the slash INSIDE the " +
      "value and stops the tag self-closing. Quote the value."
  );
});

test("failures are plotted on their own scale, or they are a flat line on the axis", async () => {
  // A few dozen failures against a few thousand events share an axis and the failure line welds
  // itself to the bottom edge — the legend then advertises a series that is not visibly there.
  const body = await renderPage({
    series: Array.from({ length: 12 }, (_, i) => ({ t: i, n: 5000 + i * 100, failed: 2 + (i % 4) })),
    timing: [],
  });

  const failPath = body.match(/<path d="([^"]+)" fill="none" stroke="var\(--bad\)"/);
  assert.ok(failPath, "no failures path");
  const ys = [...failPath[1].matchAll(/[ML][\d.]+,([\d.]+)/g)].map((m) => Number(m[1]));
  const spread = Math.max(...ys) - Math.min(...ys);
  assert.ok(
    spread > 40,
    `the failures line spans only ${spread.toFixed(1)} of 150 vertical units, so its shape is ` +
      `invisible next to a much larger events series. It needs its own scale.`
  );
  assert.match(body, /own scale/, "the legend must say the failure axis is independent");
});

test("durations are formatted as durations", async () => {
  // fmt() abbreviates magnitudes, so 18300 became "18.3k" and the page read "p95 18.3kms".
  const body = await renderPage({
    series: [],
    timing: [{ provider: "opencode", p50: 840, p95: 18300, n: 10 }],
  });
  assert.doesNotMatch(body, /kms|Mms/, "a magnitude abbreviation was concatenated with 'ms'");
  assert.match(body, /p50 840ms/);
  assert.match(body, /p95 18s/, "18300ms should read as seconds, not as an abbreviated count");
});
