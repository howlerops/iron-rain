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

test("a failing Analytics query is reported in the page, not rendered as zeroes", async () => {
  // Silently showing zeroes is worse than an error: it reads as "nothing is happening" when the
  // truth is "nothing was asked".
  //
  // This asserts the BODY, not the status, and that is a deliberate consequence of streaming. The
  // response head goes out before the queries run — that is what lets the skeleton appear
  // immediately — so by the time a query fails the status is long since committed to 200. The
  // status code is therefore no longer available to carry this, and the thing that actually reaches
  // a human is the message on the page.
  const realFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response("bad token", { status: 403 });
  try {
    const res = await worker.fetch(
      new Request("https://telemetry.test/stats", {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    const body = await res.text();
    assert.match(body, /query failed/i, "the failure is not reported anywhere in the page");
    assert.match(body, /403/, "the underlying cause is not shown, so it cannot be acted on");
    assert.doesNotMatch(
      body,
      /failure rate/,
      "a dashboard was rendered alongside the error — zeroes next to a warning read as real data"
    );
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

// ---- filtering ----------------------------------------------------------------------------------

/** Captures the SQL the page sends, so the filter plumbing can be asserted on directly. */
const capture = async (query) => {
  const sent = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (_u, init) => {
    sent.push(String(init?.body || ""));
    return new Response(JSON.stringify({ data: [] }), { status: 200 });
  };
  try {
    const res = await worker.fetch(
      new Request(`https://telemetry.test/stats${query}`, {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    return { sql: sent, body: await res.text() };
  } finally {
    globalThis.fetch = realFetch;
  }
};

test("a filter constrains every query", async () => {
  const { sql } = await capture("?days=7&provider=opencode&status=failed");
  // IN rather than =, because a dimension holds a list now: "opencode and pi" is a question the
  // single-value form could not express at all, so comparing two providers meant loading the page
  // twice and remembering the first set of numbers.
  const constrained = sql.filter((s) => s.includes("blob2 IN ('opencode')"));
  assert.ok(constrained.length >= 6, `only ${constrained.length} queries were filtered by provider`);
  assert.ok(
    sql.some((s) => s.includes("double2 = 0")),
    "status=failed did not reach the SQL"
  );
});

test("a hostile filter value never reaches the SQL", async () => {
  // These values are concatenated into a query, so they are RESTRICTED rather than escaped: event
  // names, providers, semver and GOOS/GOARCH all fit a short alphabet, and anything outside it is
  // dropped. A rejected filter renders the unfiltered page — it can never run someone else's SQL.
  for (const evil of [
    "opencode' OR '1'='1",
    "x'; DROP TABLE oculus_telemetry; --",
    "a' UNION SELECT blob7 FROM oculus_telemetry WHERE '1'='1",
    "opencode\\", 
    "a b",
  ]) {
    const { sql } = await capture(`?provider=${encodeURIComponent(evil)}`);
    const joined = sql.join("\n");
    assert.doesNotMatch(
      joined,
      /DROP|UNION|OR '1'='1/i,
      `the value ${JSON.stringify(evil)} reached the query`
    );
    assert.ok(
      !joined.includes(`'${evil}'`),
      `the value ${JSON.stringify(evil)} was interpolated verbatim`
    );
  }
});

test("a valid filter survives and an invalid one is simply ignored", async () => {
  const ok = await capture("?provider=claude-code");
  assert.ok(ok.sql.some((s) => s.includes("blob2 IN ('claude-code')")), "a legitimate value was dropped");

  const bad = await capture("?provider=' OR 1=1 --");
  assert.ok(
    !bad.sql.some((s) => s.includes("blob2 IN")),
    "an invalid value must drop the filter, not apply a mangled one"
  );
});

test("the facet query is NOT filtered, so a selection can be backed out of", async () => {
  const { sql } = await capture("?provider=opencode");
  const facet = sql.find((s) => s.includes("GROUP BY event, provider, version"));
  assert.ok(facet, "no facet query was issued, so the dropdowns have nothing to offer");
  assert.ok(
    !facet.includes("blob2 = 'opencode'"),
    "the facet query is constrained by the current filter, so each dropdown only offers what is " +
      "already selected — a dead end the user cannot navigate out of"
  );
});

test("the skeleton is sent before the data and hidden after it", async () => {
  const { body } = await capture("?days=7");
  const sk = body.indexOf('class="sk"');
  const hide = body.indexOf(".sk{display:none}");
  assert.ok(sk > -1, "no skeleton was emitted, so a slow query shows the previous page");
  assert.ok(hide > sk, "the rule hiding the skeleton must come AFTER it, or it never appears");
  assert.ok(
    body.indexOf("<style>") < sk,
    "the stylesheet must precede the skeleton or it renders unstyled"
  );
});

test("script-src is a hash and one origin — never unsafe-inline, never a wildcard", async () => {
  // The page renders values that arrived over the wire: an error string originates in a provider's
  // output and lands in a table cell. So the property worth keeping, now that there IS a script, is
  // that nothing FROM THE DATASET can execute. A hash keeps exactly that — only the one script whose
  // bytes match may run — where 'unsafe-inline' would have given it away entirely.
  const res = await worker.fetch(
    new Request("https://telemetry.test/stats", {
      headers: { Authorization: "Basic " + btoa("x:hunter2") },
    }),
    envWith({ CF_ANALYTICS_TOKEN: undefined })
  );
  const csp = res.headers.get("Content-Security-Policy") || "";
  assert.match(csp, /default-src 'none'/);
  assert.match(csp, /script-src 'sha256-[A-Za-z0-9+/=]+'/, "script-src carries no hash");
  assert.match(csp, /https:\/\/static\.cloudflareinsights\.com/);
  assert.doesNotMatch(csp, /script-src[^;]*'unsafe-inline'/, "inline script must stay forbidden");
  assert.doesNotMatch(csp, /script-src[^;]*\*/, "a wildcard script source defeats the point");
});

test("the CSP hash matches the script actually served", async () => {
  // What this can and cannot catch, stated because the obvious reading is wrong. The hash is
  // COMPUTED from the same constant that is served, so "someone edited the script and forgot to
  // update the hash" is impossible by construction — that control passes, and a test for it would
  // be tautological.
  //
  // What is real is the EMBEDDING diverging from the hashed bytes: a stray space in the <script>
  // wrapper, a future minifier, anything that transforms the string between hashing and serving.
  // The browser then refuses to run it and every filter change silently falls back to a full page
  // reload — working, slower, with no error anywhere. So this rehashes what was actually SERVED.
  const res = await worker.fetch(
    new Request("https://telemetry.test/stats?days=7", {
      headers: { Authorization: "Basic " + btoa("x:hunter2") },
    }),
    envWith()
  );
  const csp = res.headers.get("Content-Security-Policy") || "";
  const body = await res.text();

  const m = body.match(/<script>([\s\S]*?)<\/script>/);
  assert.ok(m, "no inline script was served, but the CSP allows one");
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(m[1]));
  const hash = "sha256-" + Buffer.from(new Uint8Array(digest)).toString("base64");

  assert.ok(
    csp.includes(`'${hash}'`),
    `the served script hashes to ${hash}, which the CSP does not list. The browser will refuse to ` +
      `run it and every filter change will fall back to a full page reload, silently.`
  );
});

test("the swap targets exist and the script can find them", async () => {
  // The queries have to succeed for the filter bar to render at all: the failure path deliberately
  // replaces the whole body with a warning, which is why this mocks them rather than letting the
  // real endpoint 401 and then asserting against an error page.
  const { body } = await capture("?days=7");
  for (const id of ["f", "results", "chips", "sk"]) {
    assert.ok(
      body.includes(`id="${id}"`),
      `#${id} is missing. The script bails out when any of its anchors is absent — which fails ` +
        `safe (full reloads) but silently, so this is the only thing that would notice.`
    );
  }
});

// ---- faceted, multi-select filtering -------------------------------------------------------------

test("several values in one dimension are OR-ed, and dimensions AND together", async () => {
  const { sql } = await capture("?provider=opencode&provider=pi&os=darwin");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(
    q,
    /blob2 IN \('opencode', 'pi'\)/,
    "two providers must widen within the dimension, not collide or drop one"
  );
  assert.match(q, /blob5 IN \('darwin'\)/);
  assert.ok(
    q.indexOf("blob2 IN") < q.indexOf("AND blob5 IN") || q.includes("AND"),
    "dimensions must AND together — a reader of 'provider: opencode, pi / os: darwin' expects " +
      "both constraints, not either"
  );
});

test("one bad value does not poison the rest of its dimension", async () => {
  const { sql } = await capture("?provider=opencode&provider=' OR 1=1 --&provider=pi");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(q, /blob2 IN \('opencode', 'pi'\)/, "the two legitimate values should survive");
  assert.doesNotMatch(q, /OR 1=1/);
});

test("duplicate values collapse", async () => {
  const { sql } = await capture("?provider=pi&provider=pi&provider=pi");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(q, /blob2 IN \('pi'\)/, "a repeated value must not be repeated in the query");
});

test("the facet list is multi-select, counted, and reachable without script", async () => {
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (_u, init) => {
    const sql = String(init?.body || "");
    const rows = sql.includes("GROUP BY event, provider, version, os, arch")
      ? [
          { event: "session.create", provider: "opencode", version: "1", os: "darwin", arch: "arm64", n: 90 },
          { event: "turn.complete", provider: "pi", version: "1", os: "linux", arch: "amd64", n: 10 },
        ]
      : [];
    return new Response(JSON.stringify({ data: rows }), { status: 200 });
  };
  try {
    const res = await worker.fetch(
      new Request("https://telemetry.test/stats?provider=opencode", {
        headers: { Authorization: "Basic " + btoa("x:hunter2") },
      }),
      envWith()
    );
    const body = await res.text();

    assert.match(body, /<details class="facet/, "the facets are not disclosure widgets");
    assert.match(
      body,
      /<input type="checkbox" name="provider" value="pi">/,
      "an unselected facet value must be an unchecked checkbox, so several can be chosen at once"
    );
    assert.match(
      body,
      /<input type="checkbox" name="provider" value="opencode" checked>/,
      "the active value is not reflected back into the list"
    );
    assert.match(body, /class="cnt">90</, "facet values carry no count, so which to open is a guess");
    // The no-JS path must stay intact: this is a real GET form with real checkboxes, and the
    // script only upgrades it. Asserting the markup rather than the absence of script, because
    // there IS a script now and the thing worth protecting is that it is optional.
    assert.match(body, /<form class="filters" method="get"/, "the form no longer submits on its own");
    assert.match(body, /<button class="go" type="submit">/, "there is no way to apply without script");
  } finally {
    globalThis.fetch = realFetch;
  }
});

// ---- time range ---------------------------------------------------------------------------------

test("presets reach hour and minute granularity, not just days", async () => {
  // "days", minimum one, could not express the window this page is most often opened for: something
  // just broke, what changed. A day of data buries the last twenty minutes.
  const { sql } = await capture("?range=2h");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(q, /INTERVAL '120' MINUTE/, "a 2-hour window did not reach the query");

  const short = await capture("?range=15m");
  assert.match(
    short.sql.find((s) => s.includes("blob1 AS event")),
    /INTERVAL '15' MINUTE/,
    "the shortest preset must be minutes, or the page cannot answer a question about right now"
  );
});

test("the chart bucket scales with the window", async () => {
  // Fixed buckets produce a chart that is either 2160 columns of noise or a single bar, and both
  // read as broken rather than as a badly chosen axis.
  const hour = await capture("?range=1h");
  assert.match(
    hour.sql.find((s) => s.includes("toStartOfInterval")),
    /INTERVAL '5' MINUTE/,
    "an hour bucketed coarser than minutes is a handful of bars"
  );
  const quarter = await capture("?range=90d");
  assert.match(
    quarter.sql.find((s) => s.includes("toStartOfInterval")),
    /INTERVAL '1' DAY/,
    "90 days bucketed hourly is 2160 points"
  );
});

test("an explicit date range overrides the preset and includes the whole end day", async () => {
  const { sql } = await capture("?from=2026-09-01&to=2026-09-03&range=1h");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(q, /timestamp >= toDateTime\('2026-09-01 00:00:00'\)/);
  assert.match(
    q,
    /timestamp < toDateTime\('2026-09-03 00:00:00'\) \+ INTERVAL '1' DAY/,
    "the end day must be included: asking for the 1st to the 3rd means through the 3rd, and an " +
      "exclusive bound silently drops a day of data"
  );
  assert.doesNotMatch(q, /INTERVAL '60' MINUTE/, "the preset should not also apply");
});

test("a single-day range is that day, not a zero-width window", async () => {
  const { sql } = await capture("?from=2026-09-03&to=2026-09-03");
  const q = sql.find((s) => s.includes("blob1 AS event"));
  assert.match(q, /toDateTime\('2026-09-03 00:00:00'\) \+ INTERVAL '1' DAY/);
});

test("a hostile or malformed date never reaches the SQL", async () => {
  // from/to are concatenated into the query like every other filter, so they are restricted to an
  // exact calendar-date shape rather than escaped.
  for (const bad of [
    "2026-09-01'; DROP TABLE oculus_telemetry; --",
    "' OR '1'='1",
    "2026-9-1",
    "yesterday",
    "2026-09-01T00:00:00Z",
  ]) {
    const { sql } = await capture(`?from=${encodeURIComponent(bad)}&to=2026-09-03`);
    const joined = sql.join("\n");
    assert.doesNotMatch(joined, /DROP|OR '1'='1/i, `${JSON.stringify(bad)} reached the query`);
    assert.ok(
      !joined.includes(bad),
      `${JSON.stringify(bad)} was interpolated verbatim instead of falling back to the preset`
    );
    assert.match(joined, /INTERVAL '10080' MINUTE/, "a rejected range must fall back to the default");
  }
});

test("a backwards range falls back rather than querying nothing", async () => {
  const { sql } = await capture("?from=2026-09-30&to=2026-09-01");
  assert.match(
    sql.join("\n"),
    /INTERVAL '10080' MINUTE/,
    "from after to yields an empty window and an empty dashboard, which reads as 'no activity' " +
      "rather than 'that range is backwards'"
  );
});

test("the range control offers presets and a date pair, and reflects the active one", async () => {
  const preset = await capture("?range=6h");
  assert.match(preset.body, /<option value="6h" selected>/, "the active preset is not reflected back");
  assert.match(preset.body, /<option value="15m"/, "the minute presets are missing");
  assert.match(preset.body, /name="from"/, "there is no way to enter an explicit range");

  const custom = await capture("?from=2026-09-01&to=2026-09-03");
  assert.match(custom.body, /value="2026-09-01"/, "the chosen dates are not shown back");
  assert.match(
    custom.body,
    /<select class="range" name="range" aria-label="Time range" disabled>/,
    "with an explicit range active the preset select must be disabled, or the page shows two " +
      "controls disagreeing about which window is displayed"
  );
});
