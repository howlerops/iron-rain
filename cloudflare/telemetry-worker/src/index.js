// Oculus / Iron Rain anonymized telemetry ingest.
//
// Accepts POST /ingest with a JSON batch from the daemon and writes each event to Cloudflare
// Analytics Engine. The daemon already scrubs paths/home-dir out of error strings and never sends
// prompts/tokens/repo names — this Worker does not attempt to re-derive identity and stores only
// what it receives (capped/coerced).
//
// Query later via the Analytics Engine SQL API, e.g.:
//   SELECT blob1 AS event, blob3 AS error, count() AS n
//   FROM oculus_telemetry
//   WHERE double2 = 0            -- failures (ok=0)
//   GROUP BY event, error ORDER BY n DESC

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (request.method === "GET" && url.pathname === "/stats") {
      return stats(request, env);
    }
    if (request.method !== "POST" || url.pathname !== "/ingest") {
      return new Response("not found", { status: 404 });
    }
    if (!env.TELEMETRY) return new Response("telemetry binding missing", { status: 500 });

    // The ingest key, when one is configured.
    //
    // ROLLOUT ORDER DOES NOT MATTER, deliberately. Unset, this accepts everything exactly as before,
    // so deploying this Worker ahead of the daemons does not drop a single one in the field. The
    // daemon always sends the header, so a daemon that updates first is equally fine against an
    // older Worker that ignores it. Set INGEST_KEY once the fleet has rolled and the old behaviour
    // is gone — that flip is the only step with a wrong order, and it is a one-line `wrangler
    // secret put`.
    //
    // What this is NOT: real authentication. The key ships inside a binary anyone can download, so
    // it is extractable by anyone who cares to. It raises the bar from "curl a URL that is printed
    // in the docs" to "pull a constant out of a Go binary", and combined with the CORS removal below
    // it closes the drive-by and browser-origin paths entirely. The threat here is poisoned
    // analytics and billed writes, not user data — the daemon scrubs paths and never sends prompts,
    // tokens or repo names.
    if (env.INGEST_KEY) {
      const auth = request.headers.get("Authorization") || "";
      if (!timingSafeEqual(auth, `Bearer ${env.INGEST_KEY}`)) {
        return new Response("unauthorized", { status: 401 });
      }
    }

    let body;
    try {
      body = await request.json();
    } catch {
      return new Response("bad json", { status: 400 });
    }
    const events = Array.isArray(body?.events) ? body.events : null;
    if (!events) return Response.json({ ok: false, error: "events must be an array" }, { status: 400 });

    const version = str(body.version);
    const os = str(body.os);
    const arch = str(body.arch);
    const installID = str(body.install_id);

    let accepted = 0;
    for (const e of events.slice(0, 100)) {
      if (!e || typeof e.event !== "string" || !e.event) continue;
      env.TELEMETRY.writeDataPoint({
        // blob1..blob7 — string dimensions.
        blobs: [str(e.event), str(e.provider), str(e.error), version, os, arch, installID],
        // double1 duration ms, double2 ok (1/0), double3 client timestamp.
        doubles: [num(e.dur_ms), e.ok ? 1 : 0, num(e.ts)],
        // Sampling index (<=96 bytes): group by event name.
        indexes: [str(e.event).slice(0, 32)],
      });
      accepted++;
    }
    return Response.json({ ok: true, accepted });
  },
};

function str(v) {
  return (v == null ? "" : String(v)).slice(0, 256);
}
function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}
// CORS is GONE, and its absence is the point.
//
// The only client of this endpoint is the daemon, which is not a browser and has never needed a
// preflight. `Access-Control-Allow-Origin: *` did nothing for it and everything for a drive-by: any
// page in any tab could POST batches into this account's Analytics Engine, unauthenticated, from a
// visitor's own network. Removing it means a browser cannot reach the endpoint at all, whatever the
// ingest key is doing.

/** Compares two strings without leaking where they diverge. */
function timingSafeEqual(a, b) {
  if (typeof a !== "string" || typeof b !== "string" || a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// ---- /stats ------------------------------------------------------------------------------------
//
// Telemetry nobody looks at is just a bill. This renders the dataset as a page, because the
// alternative — the Analytics Engine SQL API — needs a shell, a token and remembered SQL, which in
// practice means it gets read once and never again.
//
// HTTP Basic rather than a ?key= in the URL. The credential then travels in a header instead of
// somewhere that lands in browser history, referrers and every proxy log in between, and the browser
// still prompts for it, which a header-only scheme cannot do.
//
// The numbers are aggregates over anonymised rows: the daemon scrubs paths and home directories and
// never sends prompts, tokens or repo names, and the only per-install value here is a random id it
// generated for itself. Nothing below can identify a person, and nothing below should ever start to.
async function stats(request, env) {
  if (!env.STATS_PASSWORD) {
    return html(setupPage("STATS_PASSWORD is not set on this Worker."), 503);
  }
  const auth = request.headers.get("Authorization") || "";
  if (!auth.startsWith("Basic ") || !checkBasic(auth, env.STATS_PASSWORD)) {
    return new Response("unauthorized", {
      status: 401,
      headers: { "WWW-Authenticate": 'Basic realm="Iron Rain telemetry"' },
    });
  }
  if (!env.CF_ACCOUNT_ID || !env.CF_ANALYTICS_TOKEN) {
    return html(
      setupPage("CF_ACCOUNT_ID and CF_ANALYTICS_TOKEN are needed to read the dataset."),
      503
    );
  }

  const days = Math.min(90, Math.max(1, Number(new URL(request.url).searchParams.get("days")) || 7));
  const since = `toDateTime(now()) - INTERVAL '${days}' DAY`;

  // SUM(_sample_interval), never count().
  //
  // Analytics Engine samples under load and hands back the weight of each surviving row in
  // _sample_interval. count() therefore reports how many rows SURVIVED sampling, which silently
  // under-reports exactly when traffic is high enough to care about. Summing the interval is the
  // documented way to recover the true figure.
  const q = (sql) => sqlQuery(env, sql);
  const [byEvent, failures, versions, platforms, installs, timing] = await Promise.all([
    q(`SELECT blob1 AS event, SUM(_sample_interval) AS n,
              SUM(IF(double2 = 0, _sample_interval, 0)) AS failed
       FROM oculus_telemetry WHERE timestamp > ${since}
       GROUP BY event ORDER BY n DESC LIMIT 40`),
    q(`SELECT blob1 AS event, blob3 AS error, SUM(_sample_interval) AS n
       FROM oculus_telemetry WHERE timestamp > ${since} AND double2 = 0 AND blob3 != ''
       GROUP BY event, error ORDER BY n DESC LIMIT 30`),
    q(`SELECT blob4 AS version, COUNT(DISTINCT blob7) AS installs, SUM(_sample_interval) AS events
       FROM oculus_telemetry WHERE timestamp > ${since}
       GROUP BY version ORDER BY installs DESC LIMIT 25`),
    q(`SELECT blob5 AS os, blob6 AS arch, COUNT(DISTINCT blob7) AS installs
       FROM oculus_telemetry WHERE timestamp > ${since}
       GROUP BY os, arch ORDER BY installs DESC LIMIT 20`),
    q(`SELECT COUNT(DISTINCT blob7) AS installs, SUM(_sample_interval) AS events
       FROM oculus_telemetry WHERE timestamp > ${since}`),
    q(`SELECT blob2 AS provider,
              quantileWeighted(0.5)(double1, _sample_interval) AS p50,
              quantileWeighted(0.95)(double1, _sample_interval) AS p95,
              SUM(_sample_interval) AS n
       FROM oculus_telemetry WHERE timestamp > ${since} AND double1 > 0 AND blob2 != ''
       GROUP BY provider ORDER BY n DESC LIMIT 20`),
  ]);

  const failed = [byEvent, failures, versions, platforms, installs, timing].find((r) => r.error);
  if (failed) {
    return html(
      setupPage(`The Analytics Engine query failed: ${escapeHtml(failed.error)}`),
      502
    );
  }

  const total = installs.rows[0] || {};
  return html(page({ days, byEvent, failures, versions, platforms, total, timing }));
}

/** Runs one SQL statement against the Analytics Engine SQL API. */
async function sqlQuery(env, sql) {
  try {
    const res = await fetch(
      `https://api.cloudflare.com/client/v4/accounts/${env.CF_ACCOUNT_ID}/analytics_engine/sql`,
      {
        method: "POST",
        headers: { Authorization: `Bearer ${env.CF_ANALYTICS_TOKEN}` },
        body: sql,
      }
    );
    const text = await res.text();
    if (!res.ok) return { rows: [], error: `HTTP ${res.status}: ${text.slice(0, 300)}` };
    const body = JSON.parse(text);
    return { rows: body.data || [], error: null };
  } catch (e) {
    return { rows: [], error: String(e).slice(0, 300) };
  }
}

/** Constant-time-ish Basic auth check against the configured password (any username). */
function checkBasic(header, password) {
  let decoded = "";
  try {
    decoded = atob(header.slice("Basic ".length).trim());
  } catch {
    return false;
  }
  const idx = decoded.indexOf(":");
  const given = idx < 0 ? decoded : decoded.slice(idx + 1);
  return timingSafeEqual(given, password);
}

function html(body, status = 200) {
  return new Response(body, {
    status,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      // This page is a credentialled view of account data; it has no business in a shared cache.
      "Cache-Control": "no-store",
      "X-Content-Type-Options": "nosniff",
      // It renders values that arrived over the wire. They are escaped, and this is the backstop.
      "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
    },
  });
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]
  );
}

function setupPage(why) {
  return `<!doctype html><meta charset=utf-8><title>Iron Rain telemetry</title>
<style>${CSS}</style><main><h1>Telemetry</h1>
<p class=warn>${escapeHtml(why)}</p>
<p>Set these on the Worker (Cloudflare dashboard &rarr; Workers &amp; Pages &rarr;
oculus-telemetry &rarr; Settings &rarr; Variables and Secrets, or <code>wrangler secret put</code>):</p>
<table>
<tr><th>STATS_PASSWORD</th><td>any password; the browser prompts for it. Username is ignored.</td></tr>
<tr><th>CF_ACCOUNT_ID</th><td>Workers &amp; Pages &rarr; Account ID</td></tr>
<tr><th>CF_ANALYTICS_TOKEN</th><td>an API token with <b>Account &rarr; Account Analytics &rarr; Read</b></td></tr>
</table>
<p class=note>The ingest endpoint keeps working regardless — none of these affect it.</p></main>`;
}

const CSS = `
:root{color-scheme:light dark}
body{margin:0;font:15px/1.5 ui-sans-serif,system-ui,-apple-system,sans-serif}
main{max-width:70rem;margin:0 auto;padding:2rem 1.25rem}
h1{font-size:1.4rem;margin:0 0 .25rem} h2{font-size:1rem;margin:2rem 0 .5rem;font-weight:600}
.sub{opacity:.65;margin:0 0 1.5rem;font-size:.9rem}
.cards{display:flex;flex-wrap:wrap;gap:1rem;margin-bottom:1rem}
.card{flex:1 1 9rem;border:1px solid color-mix(in srgb,currentColor 18%,transparent);
border-radius:.6rem;padding:.85rem 1rem}
.card b{display:block;font-size:1.7rem;font-variant-numeric:tabular-nums;line-height:1.2}
.card span{opacity:.65;font-size:.8rem}
table{border-collapse:collapse;width:100%;font-size:.9rem;overflow-x:auto;display:block}
th,td{text-align:left;padding:.4rem .6rem;border-bottom:1px solid
color-mix(in srgb,currentColor 12%,transparent);white-space:nowrap}
th{font-weight:600;opacity:.75} td.n{text-align:right;font-variant-numeric:tabular-nums}
td.err{white-space:normal;opacity:.85;max-width:38rem}
.bad{color:#c0392b} @media (prefers-color-scheme:dark){.bad{color:#ff8a7a}}
.warn{padding:.75rem 1rem;border-radius:.5rem;
background:color-mix(in srgb,currentColor 8%,transparent)}
.note{opacity:.6;font-size:.85rem} code{font-family:ui-monospace,monospace;font-size:.85em}
nav{margin-bottom:1.25rem;font-size:.9rem} nav a{margin-right:.75rem}
`;

// num() is the ingest path's coercion, reused here: same job, same answer for a bad value.

function fmt(v) {
  return num(v).toLocaleString("en-US");
}

function rows(list, cells) {
  if (!list.length) return `<tr><td colspan=9 class=note>no data in this window</td></tr>`;
  return list.map(cells).join("");
}

function page({ days, byEvent, failures, versions, platforms, total, timing }) {
  const failedTotal = byEvent.rows.reduce((a, r) => a + num(r.failed), 0);
  const eventTotal = byEvent.rows.reduce((a, r) => a + num(r.n), 0);
  const rate = eventTotal ? ((failedTotal / eventTotal) * 100).toFixed(1) : "0.0";

  return `<!doctype html><meta charset=utf-8><title>Iron Rain telemetry</title>
<meta name=viewport content="width=device-width,initial-scale=1"><style>${CSS}</style><main>
<h1>Iron Rain telemetry</h1>
<p class=sub>Anonymised. No paths, prompts, tokens or repo names — install ids are random and
self-assigned. Last ${days} day${days === 1 ? "" : "s"}.</p>
<nav>${[1, 7, 30, 90].map((d) => `<a href="?days=${d}">${d}d</a>`).join("")}</nav>

<div class=cards>
  <div class=card><b>${fmt(total.installs)}</b><span>installs seen</span></div>
  <div class=card><b>${fmt(total.events)}</b><span>events</span></div>
  <div class=card><b class="${failedTotal ? "bad" : ""}">${rate}%</b><span>failure rate</span></div>
  <div class=card><b>${fmt(versions.rows.length)}</b><span>versions in the wild</span></div>
</div>

<h2>Events</h2>
<table><tr><th>event</th><th>count</th><th>failed</th></tr>
${rows(byEvent.rows, (r) => `<tr><td>${escapeHtml(r.event)}</td><td class=n>${fmt(r.n)}</td>
<td class="n ${num(r.failed) ? "bad" : ""}">${fmt(r.failed)}</td></tr>`)}</table>

<h2>Failures</h2>
<table><tr><th>event</th><th>error</th><th>count</th></tr>
${rows(failures.rows, (r) => `<tr><td>${escapeHtml(r.event)}</td>
<td class=err>${escapeHtml(r.error)}</td><td class=n>${fmt(r.n)}</td></tr>`)}</table>

<h2>Versions</h2>
<table><tr><th>version</th><th>installs</th><th>events</th></tr>
${rows(versions.rows, (r) => `<tr><td>${escapeHtml(r.version) || "<i>unknown</i>"}</td>
<td class=n>${fmt(r.installs)}</td><td class=n>${fmt(r.events)}</td></tr>`)}</table>

<h2>Platforms</h2>
<table><tr><th>os</th><th>arch</th><th>installs</th></tr>
${rows(platforms.rows, (r) => `<tr><td>${escapeHtml(r.os)}</td><td>${escapeHtml(r.arch)}</td>
<td class=n>${fmt(r.installs)}</td></tr>`)}</table>

<h2>Duration by provider</h2>
<table><tr><th>provider</th><th>p50 ms</th><th>p95 ms</th><th>samples</th></tr>
${rows(timing.rows, (r) => `<tr><td>${escapeHtml(r.provider)}</td>
<td class=n>${fmt(Math.round(num(r.p50)))}</td><td class=n>${fmt(Math.round(num(r.p95)))}</td>
<td class=n>${fmt(r.n)}</td></tr>`)}</table>
</main>`;
}
