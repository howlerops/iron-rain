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
  // Hourly buckets for a short window, daily beyond it: 90 days of hourly points is 2160 columns of
  // noise, and one day of daily points is a single bar.
  const bucket = days <= 2 ? "1' HOUR" : "1' DAY";

  // SUM(_sample_interval), never count().
  //
  // Analytics Engine samples under load and hands back the weight of each surviving row in
  // _sample_interval. count() therefore reports how many rows SURVIVED sampling, which silently
  // under-reports exactly when traffic is high enough to care about. Summing the interval is the
  // documented way to recover the true figure.
  const q = (sql) => sqlQuery(env, sql);
  const [byEvent, failures, versions, platforms, installs, timing, series] = await Promise.all([
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
    q(`SELECT toStartOfInterval(timestamp, INTERVAL '${bucket}) AS t,
              SUM(_sample_interval) AS n,
              SUM(IF(double2 = 0, _sample_interval, 0)) AS failed
       FROM oculus_telemetry WHERE timestamp > ${since}
       GROUP BY t ORDER BY t ASC`),
  ]);

  const failed = [byEvent, failures, versions, platforms, installs, timing, series]
    .find((r) => r.error);
  if (failed) {
    return html(setupPage(`The Analytics Engine query failed: ${escapeHtml(failed.error)}`), 502);
  }

  const total = installs.rows[0] || {};
  return html(page({ days, byEvent, failures, versions, platforms, total, timing, series }));
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
      // A credentialled view of account data has no business in a shared cache.
      "Cache-Control": "no-store",
      "X-Content-Type-Options": "nosniff",
      // Everything is inline and self-contained: no scripts at all, no external fonts, no images.
      // The charts are SVG built server-side, which is why 'none' can stay the default and why this
      // page cannot be turned into an exfiltration path by anything that lands in the dataset.
      "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
    },
  });
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]
  );
}

// num() is the ingest path's coercion, reused here: same job, same answer for a bad value.

function fmt(v) {
  const n = num(v);
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0) + "M";
  if (n >= 10_000) return (n / 1000).toFixed(n < 100_000 ? 1 : 0) + "k";
  return n.toLocaleString("en-US");
}

/** Formats a millisecond duration. fmt() is for magnitudes — it turned 18300ms into "18.3kms". */
function dur(ms) {
  const n = num(ms);
  if (n < 1000) return `${Math.round(n)}ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}s`;
  const m = Math.floor(n / 60_000);
  const rest = Math.round((n % 60_000) / 1000);
  return rest ? `${m}m ${rest}s` : `${m}m`;
}

function rows(list, cells) {
  if (!list.length) return `<tr><td colspan=9 class=empty>no data in this window</td></tr>`;
  return list.map(cells).join("");
}

// ---- charts -------------------------------------------------------------------------------------
//
// Hand-rolled SVG, server-rendered. A chart library would be most of this Worker's bundle, would
// need client-side JS (and therefore a script-src hole in the CSP), and would render the same five
// shapes. These are pure functions over numbers, which also makes them testable without a browser.

/** An area chart of totals with failures drawn over it. */
function areaChart(points, { w = 1040, h = 150 } = {}) {
  if (points.length < 2) {
    return `<p class=empty>not enough data in this window to plot</p>`;
  }
  const max = Math.max(1, ...points.map((p) => p.n));
  // Failures get their OWN scale, and the legend says so.
  //
  // Sharing the events axis looked correct and showed nothing: a few dozen failures against a few
  // thousand events is a flat line welded to the bottom edge, so the chart promised a series in its
  // legend that was not visibly there. Failures matter at their own magnitude — the question is
  // "when did they spike", not "how do they compare in volume to successes", which the failure-rate
  // card already answers.
  const failMax = Math.max(1, ...points.map((p) => p.failed));
  const x = (i) => (i / (points.length - 1)) * w;
  const y = (v, scale) => h - (v / scale) * (h - 8) - 2;

  const line = (key, scale) =>
    points.map((p, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(p[key], scale).toFixed(1)}`).join("");
  const area = `${line("n", max)}L${w},${h}L0,${h}Z`;
  const anyFailures = points.some((p) => p.failed > 0);

  return `<svg class="chart" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" role="img"
 aria-label="events over time, peak ${fmt(max)} events and ${fmt(failMax)} failures">
<defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">
<stop offset="0%" stop-color="var(--gold)" stop-opacity=".38"></stop>
<stop offset="100%" stop-color="var(--gold)" stop-opacity="0"></stop>
</linearGradient></defs>
<path d="${area}" fill="url(#g)"></path>
<path d="${line("n", max)}" fill="none" stroke="var(--gold-bright)" stroke-width="1.75"
 vector-effect="non-scaling-stroke"></path>
${anyFailures ? `<path d="${line("failed", failMax)}" fill="none" stroke="var(--bad)"
 stroke-width="1.5" vector-effect="non-scaling-stroke" stroke-dasharray="4 3"></path>` : ""}
</svg>`;
}

/** A labelled horizontal bar, used for the event and provider breakdowns. */
function bars(list, { label, value, sub, max }) {
  if (!list.length) return `<p class=empty>no data in this window</p>`;
  const top = Math.max(1, ...list.map((r) => num(value(r))), max || 0);
  return `<div class=bars>${list
    .map((r) => {
      const v = num(value(r));
      const pct = ((v / top) * 100).toFixed(1);
      return `<div class=bar>
<span class=bar-label title="${escapeHtml(label(r))}">${escapeHtml(label(r))}</span>
<span class=bar-track><span class=bar-fill style="width:${pct}%"></span></span>
<span class=bar-val>${escapeHtml(sub(r))}</span>
</div>`;
    })
    .join("")}</div>`;
}

const CSS = `
/* Tokens lifted from site/style.css so this page is the same product as the marketing site —
   same gold, same ink, same dark-first-with-light-override. Copied rather than imported because a
   Worker cannot read a file from another directory at runtime, and one <link> to the site would make
   this page depend on that deploy staying up. */
:root{
  --bg:#09090a; --fg:#f0ece2; --muted:#6e6860; --muted-med:#9a9088;
  --gold:#c49b21; --gold-bright:#d4b820; --gold-light:#d4c066; --gold-soft:#e8d48b;
  --gold-gradient:linear-gradient(135deg,#d4c066,#c49b21,#9a7a18);
  --amber-dim:rgba(196,155,33,.12); --amber-border:rgba(196,155,33,.28);
  --card:#111013; --card-hover:#181620; --border:#1e1c22; --border-sub:#141318;
  --bad:#e0664f; --ok:#6f9a5a;
  --font-sans:'DM Sans',-apple-system,BlinkMacSystemFont,system-ui,sans-serif;
  --font-display:'Instrument Serif',Georgia,'Times New Roman',serif;
  --font-mono:ui-monospace,'SF Mono','Fira Code',Menlo,monospace;
  --radius:18px; --radius-sm:12px; --radius-xs:8px;
}
@media (prefers-color-scheme:light){:root{
  --bg:#fafaf7; --fg:#0e0d0b; --muted:#706860; --muted-med:#908880;
  --card:#f1ede6; --card-hover:#e8e4dc; --border:#ddd9d0; --border-sub:#e8e4dc;
  --amber-dim:rgba(196,155,33,.10); --bad:#b8452c; --ok:#4f7a3a;
}}
*,*::before,*::after{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font-family:var(--font-sans);
 font-size:15px;line-height:1.6;-webkit-font-smoothing:antialiased}
main{max-width:1080px;margin:0 auto;padding:3rem 1.5rem 5rem}
header{display:flex;flex-wrap:wrap;align-items:baseline;gap:.75rem;margin-bottom:.35rem}
.wordmark{font-family:var(--font-mono);font-weight:700;letter-spacing:.08em;text-transform:uppercase;
 font-size:.95rem;background:var(--gold-gradient);-webkit-background-clip:text;background-clip:text;
 -webkit-text-fill-color:transparent;color:transparent}
h1{font-family:var(--font-display);font-weight:400;font-size:2rem;margin:0;letter-spacing:-.01em}
.sub{color:var(--muted-med);margin:0 0 1.75rem;font-size:.875rem;max-width:56ch}
nav{display:flex;gap:.4rem;margin-bottom:2rem}
nav a{font-size:.8rem;padding:.3rem .8rem;border-radius:999px;text-decoration:none;
 color:var(--muted-med);border:1px solid var(--border);font-variant-numeric:tabular-nums}
nav a.on{color:var(--gold-soft);border-color:var(--amber-border);background:var(--amber-dim)}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(10rem,1fr));gap:.9rem;margin-bottom:2rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:var(--radius-sm);
 padding:1rem 1.15rem}
.card b{display:block;font-family:var(--font-display);font-weight:400;font-size:2.1rem;
 line-height:1.1;font-variant-numeric:tabular-nums}
.card span{display:block;color:var(--muted);font-size:.75rem;text-transform:uppercase;
 letter-spacing:.06em;margin-top:.25rem}
.card.accent b{background:var(--gold-gradient);-webkit-background-clip:text;background-clip:text;
 -webkit-text-fill-color:transparent;color:transparent}
h2{font-size:.78rem;text-transform:uppercase;letter-spacing:.09em;color:var(--muted);
 font-weight:600;margin:2.5rem 0 .75rem}
.panel{background:var(--card);border:1px solid var(--border);border-radius:var(--radius-sm);
 padding:1.1rem 1.25rem;overflow:hidden}
.chart{display:block;width:100%;height:150px}
.legend{display:flex;gap:1.1rem;margin-top:.6rem;font-size:.75rem;color:var(--muted)}
.legend i{display:inline-block;width:.7rem;height:.15rem;vertical-align:middle;margin-right:.35rem;
 border-radius:2px}
.bars{display:flex;flex-direction:column;gap:.45rem}
.bar{display:grid;grid-template-columns:minmax(6rem,11rem) 1fr minmax(4rem,auto);
 align-items:center;gap:.75rem;font-size:.85rem}
.bar-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--fg)}
.bar-track{background:var(--border-sub);border-radius:999px;height:.5rem;overflow:hidden}
.bar-fill{display:block;height:100%;border-radius:999px;background:var(--gold-gradient)}
.bar-val{text-align:right;font-variant-numeric:tabular-nums;color:var(--muted-med);font-size:.8rem}
.tbl{overflow-x:auto}
table{border-collapse:collapse;width:100%;font-size:.85rem;min-width:24rem}
th{text-align:left;padding:.5rem .7rem;font-size:.7rem;text-transform:uppercase;
 letter-spacing:.06em;color:var(--muted);font-weight:600;border-bottom:1px solid var(--border)}
td{padding:.5rem .7rem;border-bottom:1px solid var(--border-sub);white-space:nowrap}
tr:last-child td{border-bottom:0}
tr:hover td{background:var(--card-hover)}
td.n{text-align:right;font-variant-numeric:tabular-nums}
td.err{white-space:normal;color:var(--muted-med);font-family:var(--font-mono);font-size:.78rem;
 max-width:40rem;line-height:1.45}
td.mono{font-family:var(--font-mono);font-size:.8rem}
.bad{color:var(--bad)} .zero{color:var(--muted)}
.empty{color:var(--muted);font-size:.85rem;margin:.4rem 0;text-align:center;padding:1.5rem 0}
footer{margin-top:3rem;padding-top:1.25rem;border-top:1px solid var(--border-sub);
 color:var(--muted);font-size:.75rem}
.warn{background:var(--card);border:1px solid var(--amber-border);border-radius:var(--radius-sm);
 padding:1rem 1.15rem;color:var(--gold-soft)}
code{font-family:var(--font-mono);font-size:.85em;color:var(--gold-soft)}
`;

function shell(title, body) {
  return `<!doctype html><html lang=en><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1">
<title>${title}</title><style>${CSS}</style><body><main>${body}</main></body></html>`;
}

function setupPage(why) {
  return shell(
    "Telemetry — Iron Rain",
    `<header><span class=wordmark>Iron Rain</span><h1>Telemetry</h1></header>
<p class=warn>${escapeHtml(why)}</p>
<h2>Configuration</h2>
<div class=panel><div class=tbl><table>
<tr><th>variable</th><th>what it does</th></tr>
<tr><td class=mono>STATS_PASSWORD</td><td>the password this page prompts for. The username is ignored.</td></tr>
<tr><td class=mono>CF_ACCOUNT_ID</td><td>Workers &amp; Pages &rarr; Account ID</td></tr>
<tr><td class=mono>CF_ANALYTICS_TOKEN</td><td>API token with Account &rarr; Account Analytics &rarr; Read</td></tr>
</table></div></div>
<footer>Set these as GitHub repository secrets and the deploy workflow syncs them to the Worker.
The ingest endpoint is unaffected by all of them.</footer>`
  );
}

function page({ days, byEvent, failures, versions, platforms, total, timing, series }) {
  const failedTotal = byEvent.rows.reduce((a, r) => a + num(r.failed), 0);
  const eventTotal = byEvent.rows.reduce((a, r) => a + num(r.n), 0);
  const rate = eventTotal ? (failedTotal / eventTotal) * 100 : 0;

  const points = series.rows.map((r) => ({ n: num(r.n), failed: num(r.failed) }));
  const ranges = [1, 7, 30, 90]
    .map((d) => `<a href="?days=${d}"${d === days ? " class=on" : ""}>${d}d</a>`)
    .join("");

  return shell(
    "Telemetry — Iron Rain",
    `<header><span class=wordmark>Iron Rain</span><h1>Telemetry</h1></header>
<p class=sub>Anonymised: no paths, prompts, tokens or repo names, and install ids are random values
each daemon assigns itself. Nothing here identifies a person.</p>
<nav>${ranges}</nav>

<div class=cards>
  <div class="card accent"><b>${fmt(total.installs)}</b><span>installs seen</span></div>
  <div class=card><b>${fmt(total.events)}</b><span>events</span></div>
  <div class=card><b class="${failedTotal ? "bad" : ""}">${rate.toFixed(1)}%</b><span>failure rate</span></div>
  <div class=card><b>${fmt(versions.rows.length)}</b><span>versions live</span></div>
</div>

<h2>Activity — last ${days} day${days === 1 ? "" : "s"}</h2>
<div class=panel>${areaChart(points)}
<div class=legend><span><i style="background:var(--gold-bright)"></i>events · peak ${fmt(
      Math.max(1, ...points.map((p) => p.n))
    )}</span>
<span><i style="background:var(--bad)"></i>failures · peak ${fmt(
      Math.max(0, ...points.map((p) => p.failed))
    )} (own scale)</span></div></div>

<h2>Events</h2>
<div class=panel>${bars(byEvent.rows.slice(0, 12), {
      label: (r) => r.event,
      value: (r) => r.n,
      sub: (r) => (num(r.failed) ? `${fmt(r.n)} · ${fmt(r.failed)} failed` : fmt(r.n)),
    })}</div>

<h2>Failures</h2>
<div class="panel tbl"><table><tr><th>event</th><th>error</th><th>count</th></tr>
${rows(failures.rows, (r) => `<tr><td>${escapeHtml(r.event)}</td>
<td class=err>${escapeHtml(r.error)}</td><td class="n bad">${fmt(r.n)}</td></tr>`)}</table></div>

<h2>Turn duration by provider</h2>
<div class=panel>${bars(timing.rows, {
      label: (r) => r.provider,
      value: (r) => r.p95,
      sub: (r) => `p50 ${dur(r.p50)} · p95 ${dur(r.p95)}`,
    })}</div>

<h2>Versions</h2>
<div class="panel tbl"><table><tr><th>version</th><th>installs</th><th>events</th></tr>
${rows(versions.rows, (r) => `<tr><td class=mono>${escapeHtml(r.version) || "unknown"}</td>
<td class=n>${fmt(r.installs)}</td><td class=n>${fmt(r.events)}</td></tr>`)}</table></div>

<h2>Platforms</h2>
<div class="panel tbl"><table><tr><th>os</th><th>arch</th><th>installs</th></tr>
${rows(platforms.rows, (r) => `<tr><td>${escapeHtml(r.os)}</td>
<td class=mono>${escapeHtml(r.arch)}</td><td class=n>${fmt(r.installs)}</td></tr>`)}</table></div>

<footer>Counts are SUM(_sample_interval), not count(): Analytics Engine samples under load and
count() would report only the rows that survived it — under-reporting exactly when traffic is high
enough to matter.</footer>`
  );
}
