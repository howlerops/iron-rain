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
    // The ingest key is a FILTER, and enforcing it has to cost less than what it protects.
    //
    // Turning it on rejected every daemon built before the key existed — which is the whole fleet
    // until an update reaches it — and their telemetry was dropped with a 401 nobody sees, because
    // the daemon does not surface a failed report and nothing else was watching. The comment three
    // paragraphs up said this was the one step with a wrong order; setting it anyway is how that
    // prediction got tested.
    //
    // So an unkeyed batch is ACCEPTED and marked. What the key is worth is filtering drive-by junk
    // and billed writes, and it still does that once the fleet carries it — the `keyed` dimension
    // below is how you watch the rollover and decide when refusing is finally free. What the key is
    // NOT is authentication: it ships inside a binary anyone can download.
    let keyed = false;
    if (env.INGEST_KEY) {
      const auth = request.headers.get("Authorization") || "";
      keyed = timingSafeEqual(auth, `Bearer ${env.INGEST_KEY}`);
      if (!keyed && env.INGEST_STRICT === "1") {
        // Opt-in hard refusal, for once the fleet has rolled. Deliberately a separate switch from
        // the key itself: binding a key should not silently start discarding real data.
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
        blobs: [str(e.event), str(e.provider), str(e.error), version, os, arch, installID,
          keyed ? "keyed" : "unkeyed"],
        // double1 duration ms, double2 ok (1/0), double3 client timestamp, double4 keyed (1/0).
        doubles: [num(e.dur_ms), e.ok ? 1 : 0, num(e.ts), keyed ? 1 : 0],
        // Sampling index (<=96 bytes): group by event name.
        indexes: [str(e.event).slice(0, 32)],
      });
      accepted++;
    }
    return Response.json({ ok: true, accepted, keyed });
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


// ---- time range ---------------------------------------------------------------------------------
//
// "days" was the whole vocabulary, with a minimum of one. That is useless for the question this page
// is most often opened to answer — something just broke, what changed — where the useful window is
// the last hour or two and a day of data buries it.
//
// So: presets down to 15 minutes, plus an explicit from/to for the case a preset cannot express
// ("the deploy on Tuesday afternoon"). Both collapse to the same three values the queries need.

const PRESETS = [
  ["15m", "Last 15 minutes", 0.25],
  ["1h", "Last hour", 1],
  ["2h", "Last 2 hours", 2],
  ["6h", "Last 6 hours", 6],
  ["12h", "Last 12 hours", 12],
  ["24h", "Last 24 hours", 24],
  ["7d", "Last 7 days", 168],
  ["30d", "Last 30 days", 720],
  ["90d", "Last 90 days", 2160],
];

/** A calendar date, exactly, so a hand-typed value cannot reach the query as anything else. */
const SAFE_DATE = /^\d{4}-\d{2}-\d{2}$/;

/**
 * Resolves the requested window into the SQL predicate, a bucket size and a human label.
 *
 * Buckets are chosen so a chart has somewhere between a few dozen and a few hundred points: minutes
 * over an hour, hours over days, days beyond a fortnight. 90 days of hourly points is 2160 columns
 * of noise and one hour of daily points is a single bar, and both look like a broken chart rather
 * than a badly chosen axis.
 */
function readRange(url) {
  const from = (url.searchParams.get("from") || "").trim();
  const to = (url.searchParams.get("to") || "").trim();
  if (SAFE_DATE.test(from) && SAFE_DATE.test(to) && from <= to) {
    // Inclusive of the whole end day: someone asking for the 3rd to the 3rd means that day, not a
    // zero-width window.
    const where = `timestamp >= toDateTime('${from} 00:00:00') AND timestamp < toDateTime('${to} 00:00:00') + INTERVAL '1' DAY`;
    const spanDays = (Date.parse(to + "T00:00:00Z") - Date.parse(from + "T00:00:00Z")) / 86400000 + 1;
    return {
      kind: "custom",
      from,
      to,
      where,
      bucket: spanDays <= 2 ? "1' HOUR" : "1' DAY",
      label: from === to ? from : `${from} to ${to}`,
    };
  }

  const key = url.searchParams.get("range") || "";
  const preset = PRESETS.find((p) => p[0] === key) || PRESETS.find((p) => p[0] === "7d");
  const hours = preset[2];
  const bucket =
    hours <= 2 ? "5' MINUTE" : hours <= 12 ? "15' MINUTE" : hours <= 48 ? "1' HOUR" : "1' DAY";
  return {
    kind: "preset",
    key: preset[0],
    where: `timestamp > toDateTime(now()) - INTERVAL '${hours * 60}' MINUTE`,
    bucket,
    label: preset[1].replace(/^Last /, "last "),
  };
}

/** The filters the page offers, mapped to the blob columns they constrain. */
const FILTERS = [
  { key: "event", col: "blob1", label: "Event" },
  { key: "provider", col: "blob2", label: "Provider" },
  { key: "version", col: "blob4", label: "Version" },
  { key: "os", col: "blob5", label: "OS" },
  { key: "arch", col: "blob6", label: "Arch" },
];

// A filter value is concatenated into SQL, so it is restricted rather than escaped.
//
// Escaping quotes would be the usual answer and is the weaker one: it depends on getting every
// metacharacter right for a dialect whose grammar is not ours. These values are event names,
// provider names, semver strings and GOOS/GOARCH — all of which fit a short, boring alphabet — so
// anything outside it is dropped and the filter is simply not applied. A rejected filter shows the
// unfiltered page; it can never show someone else's data or run someone else's SQL.
const SAFE_VALUE = /^[A-Za-z0-9._:\/-]{1,64}$/;

/** Reads the filter state out of the query string, discarding anything that fails SAFE_VALUE.
 *
 * Each dimension holds a LIST. A single value per dimension could only ever narrow, so the obvious
 * question — "how do opencode and claude-code compare?" — had no expression in the UI at all; you
 * had to load the page twice and remember the first set of numbers. */
function readFilters(url) {
  const out = {};
  for (const f of FILTERS) {
    const vals = url.searchParams
      .getAll(f.key)
      .map((v) => v.trim())
      .filter((v) => v && SAFE_VALUE.test(v));
    if (vals.length) out[f.key] = [...new Set(vals)].sort();
  }
  const status = url.searchParams.get("status");
  if (status === "ok" || status === "failed") out.status = status;
  return out;
}

/** Builds the SQL WHERE fragment for the active filters. */
function whereClause(active, rangeWhere) {
  const parts = [rangeWhere];
  for (const f of FILTERS) {
    const vals = active[f.key];
    if (!vals || !vals.length) continue;
    // Every value has already passed SAFE_VALUE; values within a dimension are OR-ed (IN) and the
    // dimensions AND together, which is what a reader expects from "provider: opencode, pi".
    parts.push(`${f.col} IN (${vals.map((v) => `'${v}'`).join(", ")})`);
  }
  if (active.status === "ok") parts.push("double2 = 1");
  if (active.status === "failed") parts.push("double2 = 0");
  return parts.join(" AND ");
}

/** The current state as a query string, with one dimension replaced. */
function queryWith(active, range, overrides = {}) {
  const p = new URLSearchParams();
  const r = overrides.range || range;
  if (r.kind === "custom") {
    p.set("from", r.from);
    p.set("to", r.to);
  } else {
    p.set("range", r.key);
  }
  for (const f of FILTERS) {
    const vals = f.key in overrides ? overrides[f.key] : active[f.key];
    for (const v of vals || []) p.append(f.key, v);
  }
  const status = "status" in overrides ? overrides.status : active.status;
  if (status) p.set("status", status);
  return "?" + p.toString();
}

/** A link that REPLACES a dimension with a single value — what clicking a chart row means. */
function only(active, range, key, value) {
  return queryWith(active, range, { [key]: [value] });
}

/** A link that removes one value from a dimension, leaving the rest — the badge's x. */
function without(active, range, key, value) {
  const rest = (active[key] || []).filter((v) => v !== value);
  return queryWith(active, range, { [key]: rest });
}

async function stats(request, env) {
  if (!env.STATS_PASSWORD) {
    return await html(setupPage("STATS_PASSWORD is not set on this Worker."), 503);
  }
  const auth = request.headers.get("Authorization") || "";
  if (!auth.startsWith("Basic ") || !checkBasic(auth, env.STATS_PASSWORD)) {
    return new Response("unauthorized", {
      status: 401,
      headers: { "WWW-Authenticate": 'Basic realm="Iron Rain telemetry"' },
    });
  }
  if (!env.CF_ACCOUNT_ID || !env.CF_ANALYTICS_TOKEN) {
    return await html(
      setupPage("CF_ACCOUNT_ID and CF_ANALYTICS_TOKEN are needed to read the dataset."),
      503
    );
  }

  const url = new URL(request.url);
  const range = readRange(url);
  const active = readFilters(url);

  // Streamed, so the shell and a skeleton arrive immediately.
  //
  // Seven Analytics Engine queries take a few hundred milliseconds, and a browser given nothing
  // shows the PREVIOUS page the whole time — so changing a filter looked like it had not registered,
  // and the honest fix is to show that the request landed. The skeleton goes out first; the real
  // content follows in the same response and a rule at the end hides the placeholder. No client-side
  // JavaScript is involved, which is what keeps script-src closed.
  const { readable, writable } = new TransformStream();
  const writer = writable.getWriter();
  const enc = new TextEncoder();
  const send = (chunk) => writer.write(enc.encode(chunk));

  send(shellHead("Telemetry — Iron Rain") + headerBlock() + skeletonTemplate() + skeleton());

  (async () => {
    try {
      send(await body(env, range, active));
    } catch (e) {
      send(`<p class="warn">Rendering failed: ${escapeHtml(String(e).slice(0, 200))}</p>`);
    } finally {
      send(`<style>.sk{display:none}</style><script>${ENHANCE}</script></main></body></html>`);
      await writer.close();
    }
  })();

  return new Response(readable, { status: 200, headers: htmlHeaders(await scriptHash()) });
}

/** Runs every query and renders the result body. */
async function body(env, range, active) {
  const where = whereClause(active, range.where);
  const bucket = range.bucket;

  // SUM(_sample_interval), never count().
  //
  // Analytics Engine samples under load and hands back the weight of each surviving row in
  // _sample_interval. count() therefore reports how many rows SURVIVED sampling, which silently
  // under-reports exactly when traffic is high enough to care about. Summing the interval is the
  // documented way to recover the true figure.
  const q = (sql) => sqlQuery(env, sql);
  const [byEvent, failures, versions, platforms, installs, timing, series, rollover, facets] =
    await Promise.all([
      q(`SELECT blob1 AS event, SUM(_sample_interval) AS n,
                SUM(IF(double2 = 0, _sample_interval, 0)) AS failed
         FROM oculus_telemetry WHERE ${where} GROUP BY event ORDER BY n DESC LIMIT 40`),
      q(`SELECT blob1 AS event, blob3 AS error, SUM(_sample_interval) AS n
         FROM oculus_telemetry WHERE ${where} AND double2 = 0 AND blob3 != ''
         GROUP BY event, error ORDER BY n DESC LIMIT 30`),
      q(`SELECT blob4 AS version, COUNT(DISTINCT blob7) AS installs, SUM(_sample_interval) AS events
         FROM oculus_telemetry WHERE ${where} GROUP BY version ORDER BY installs DESC LIMIT 25`),
      q(`SELECT blob5 AS os, blob6 AS arch, COUNT(DISTINCT blob7) AS installs
         FROM oculus_telemetry WHERE ${where} GROUP BY os, arch ORDER BY installs DESC LIMIT 20`),
      q(`SELECT COUNT(DISTINCT blob7) AS installs, SUM(_sample_interval) AS events
         FROM oculus_telemetry WHERE ${where}`),
      q(`SELECT blob2 AS provider,
                quantileWeighted(0.5)(double1, _sample_interval) AS p50,
                quantileWeighted(0.95)(double1, _sample_interval) AS p95,
                SUM(_sample_interval) AS n
         FROM oculus_telemetry WHERE ${where} AND double1 > 0 AND blob2 != ''
         GROUP BY provider ORDER BY n DESC LIMIT 20`),
      q(`SELECT toStartOfInterval(timestamp, INTERVAL '${bucket}) AS t,
                SUM(_sample_interval) AS n,
                SUM(IF(double2 = 0, _sample_interval, 0)) AS failed
         FROM oculus_telemetry WHERE ${where} GROUP BY t ORDER BY t ASC`),
      // How much of the fleet carries the ingest key yet.
      //
      // This is the fact that decides when INGEST_STRICT can be switched on, and without it that
      // decision is a guess — which is how enabling the key came to reject every report for a day.
      // blob8 is written by the ingest path as "keyed"/"unkeyed".
      q(`SELECT blob8 AS keyed, COUNT(DISTINCT blob7) AS installs, SUM(_sample_interval) AS events
         FROM oculus_telemetry WHERE ${range.where} AND blob8 != '' GROUP BY keyed`),
      // Facets come from the WINDOW, not the current filter: a list that only offers what is
      // already selected is a dead end you cannot back out of.
      //
      // Counted, because a facet list without counts makes you guess which values are worth
      // opening — and the count is the cheapest possible answer to "is this even represented".
      q(`SELECT blob1 AS event, blob2 AS provider, blob4 AS version, blob5 AS os, blob6 AS arch,
                SUM(_sample_interval) AS n
         FROM oculus_telemetry WHERE ${range.where}
         GROUP BY event, provider, version, os, arch LIMIT 600`),
    ]);

  const failed = [byEvent, failures, versions, platforms, installs, timing, series, rollover, facets]
    .find((r) => r.error);
  if (failed) {
    return `<div id="chips"></div><div id="results"><p class="warn">The Analytics Engine query failed: ${escapeHtml(
      failed.error
    )}</p></div>`;
  }

  const total = installs.rows[0] || {};
  // Two regions, and only these are swapped when a filter changes: the chips (which describe the
  // query) and the results (which are the query's answer). The header and the form controls are
  // deliberately left alone — replacing the form would close whatever popover is open and discard
  // focus, which is precisely the "page forgot what I was doing" that the swap exists to avoid.
  return filterBar(facets.rows, range, active) +
    `<div id="results">` +
    content({ range, byEvent, failures, versions, platforms, total, timing, series, rollover, active }) +
    `</div>`;
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

function htmlHeaders(hash) {
  return {
    "Content-Type": "text/html; charset=utf-8",
    // A credentialled view of account data has no business in a shared cache.
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    // This page ships no JavaScript of its own — the charts are SVG built server-side and the
    // filters are a plain GET form, so nothing here needs a script to work.
    //
    // static.cloudflareinsights.com is allowed because Cloudflare INJECTS its Web Analytics beacon
    // into HTML responses on a proxied hostname, and with 'none' the browser logged a CSP violation
    // on every page load. The choice is to allow Cloudflare's own beacon on Cloudflare's own edge or
    // to turn Web Analytics off for this hostname in the dashboard; neither affects the page, and
    // the allowance is one exact origin rather than a blanket script-src.
    // script-src is a HASH plus one exact origin — never 'unsafe-inline' and never a wildcard.
    //
    // The page renders values that arrived over the wire (an error string originates in a provider's
    // output), so the property worth keeping is that nothing from the dataset can execute. A hash
    // keeps exactly that: only the one script whose bytes match may run, and any injected script —
    // inline or not — still cannot. static.cloudflareinsights.com is Cloudflare's own beacon, which
    // it injects into HTML on a proxied hostname whether or not we want it.
    "Content-Security-Policy":
      "default-src 'none'; style-src 'unsafe-inline'; img-src data:; " +
      `script-src '${hash}' https://static.cloudflareinsights.com; ` +
      "connect-src 'self' https://cloudflareinsights.com",
  };
}

async function html(bodyHtml, status = 200) {
  return new Response(bodyHtml, { status, headers: htmlHeaders(await scriptHash()) });
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
  if (!list.length) return `<tr><td colspan="9" class="empty">no data for this selection</td></tr>`;
  return list.map(cells).join("");
}

// ---- charts -------------------------------------------------------------------------------------
//
// Hand-rolled SVG, server-rendered. A chart library would be most of this Worker's bundle, would
// need client-side JS (and therefore a script-src hole for OUR code, not just Cloudflare's), and
// would render the same shapes. These are pure functions over numbers, which also makes them
// testable without a browser.
//
// Every attribute is QUOTED. An unquoted value followed by "/>" takes the slash into itself, the tag
// never self-closes, and the following <path> is parsed as its child — which SVG does not render.
// That is how the failures series came to be present in the markup and invisible on screen.

/** An area chart of totals with failures drawn over it. */
function areaChart(points, { w = 1040, h = 150 } = {}) {
  if (points.length < 2) {
    return `<p class="empty">not enough data in this window to plot</p>`;
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

/** A labelled horizontal bar. `href` makes each row a filter link. */
function bars(list, { label, value, sub, href }) {
  if (!list.length) return `<p class="empty">no data for this selection</p>`;
  const top = Math.max(1, ...list.map((r) => num(value(r))));
  return `<div class="bars">${list
    .map((r) => {
      const pct = ((num(value(r)) / top) * 100).toFixed(1);
      const name = escapeHtml(label(r));
      const cell = href
        ? `<a class="bar-label" href="${escapeHtml(href(r))}" title="filter by ${name}">${name}</a>`
        : `<span class="bar-label" title="${name}">${name}</span>`;
      return `<div class="bar">${cell}
<span class="bar-track"><span class="bar-fill" style="width:${pct}%"></span></span>
<span class="bar-val">${escapeHtml(sub(r))}</span></div>`;
    })
    .join("")}</div>`;
}


// ---- progressive enhancement -------------------------------------------------------------------
//
// The form works with no JavaScript at all: Apply submits, the server renders, done. That path is
// what every assertion about filtering still exercises, and it is why this file can be read without
// holding a client-side state model in your head.
//
// What this script adds is that changing a filter swaps only the parts whose DATA changed. Without
// it, every filter change reloaded the document — which threw away the scroll position, closed any
// open facet popover, and re-rendered a header and a filter bar that had not changed. The cost of a
// full reload is not the bytes; it is that the page visibly forgets what you were doing.
//
// It is pinned in the CSP by HASH, not by 'unsafe-inline'. That distinction is the whole reason this
// is acceptable on a page that renders values from the dataset: only this exact source can execute,
// so an error string that reaches the DOM still cannot become script. Change one byte here and the
// browser refuses to run it — which is why a test recomputes the hash and compares it to the header.
const ENHANCE = `
(() => {
  const form = document.getElementById('f');
  if (!form || !window.fetch || !window.DOMParser) return;
  const results = document.getElementById('results');
  const chips = document.getElementById('chips');
  const skel = document.getElementById('sk');
  if (!results || !chips || !skel) return;

  let inflight = 0;

  async function load(url, push) {
    const seq = ++inflight;
    results.innerHTML = skel.innerHTML;
    results.setAttribute('aria-busy', 'true');
    if (push) history.pushState({}, '', url);
    try {
      const res = await fetch(url, { headers: { 'X-Partial': '1' } });
      const html = await res.text();
      // A stale response must never paint: filters change faster than seven analytics queries
      // return, and the last request to START is the one whose answer the user is waiting for.
      if (seq !== inflight) return;
      const doc = new DOMParser().parseFromString(html, 'text/html');
      const r = doc.getElementById('results');
      const c = doc.getElementById('chips');
      if (!r) { location.assign(url); return; }
      results.innerHTML = r.innerHTML;
      chips.innerHTML = c ? c.innerHTML : '';
    } catch (e) {
      if (seq === inflight) location.assign(url);
    } finally {
      if (seq === inflight) results.removeAttribute('aria-busy');
    }
  }

  function urlFromForm() {
    const q = new URLSearchParams(new FormData(form));
    // FormData keeps empty selects; they would serialise as status= and read as a filter.
    for (const k of [...q.keys()]) if (!q.get(k)) q.delete(k);
    return location.pathname + '?' + q.toString();
  }

  form.addEventListener('submit', (e) => { e.preventDefault(); load(urlFromForm(), true); });
  form.addEventListener('change', () => load(urlFromForm(), true));

  // Links inside the swapped regions (a bar row, a version cell, a chip's x) are filter changes
  // too. Delegated from the containers, because the nodes they live on are replaced on every load.
  for (const root of [results, chips]) {
    root.addEventListener('click', (e) => {
      const a = e.target.closest('a[href^="?"]');
      if (!a || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
      e.preventDefault();
      load(location.pathname + a.getAttribute('href'), true);
    });
  }

  addEventListener('popstate', () => load(location.pathname + location.search, false));
})();
`;

/** The CSP hash for ENHANCE. Verified against the source by a test, so the two cannot drift. */
async function scriptHash() {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(ENHANCE));
  return "sha256-" + btoa(String.fromCharCode(...new Uint8Array(digest)));
}

// ---- page ---------------------------------------------------------------------------------------

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
  /* Accent used as TEXT, as opposed to --gold used as a fill.
     They cannot be the same token: gold is tuned to sit on near-black, and reusing it on a light
     card measured 1.27:1 — the filter chips were very nearly invisible. Each mode gets a value that
     clears 4.5:1 against the surface it actually lands on, checked by a test rather than by eye. */
  --accent-fg:#e8d48b; --accent-strong:#d4c066;
  --font-sans:'DM Sans',-apple-system,BlinkMacSystemFont,system-ui,sans-serif;
  --font-display:'Instrument Serif',Georgia,'Times New Roman',serif;
  --font-mono:ui-monospace,'SF Mono','Fira Code',Menlo,monospace;
  --radius-sm:12px; --radius-xs:8px;
}
@media (prefers-color-scheme:light){:root{
  --bg:#fafaf7; --fg:#0e0d0b; --muted:#706860; --muted-med:#908880;
  --card:#f1ede6; --card-hover:#e8e4dc; --border:#ddd9d0; --border-sub:#e8e4dc;
  --amber-dim:rgba(196,155,33,.10); --bad:#b8452c; --ok:#4f7a3a;
  --muted-med:#787070;            /* 3.34:1 before — under AA for body text */
  --accent-fg:#8b6200;            /* 4.68:1 on --card */
  --accent-strong:#946b01;        /* 4.60:1 on --bg, for the wordmark */
}}
@media (prefers-color-scheme:dark){:root{
  --muted:#807a6c;                /* 3.61:1 before — under AA for the small uppercase labels */
}}
*,*::before,*::after{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font-family:var(--font-sans);
 font-size:15px;line-height:1.6;-webkit-font-smoothing:antialiased}
main{max-width:1080px;margin:0 auto;padding:3rem 1.5rem 5rem}
a{color:inherit}
header{display:flex;flex-wrap:wrap;align-items:baseline;gap:.75rem;margin-bottom:.35rem}
.wordmark{font-family:var(--font-mono);font-weight:700;letter-spacing:.08em;text-transform:uppercase;
 font-size:.95rem;color:var(--accent-strong)}
h1{font-family:var(--font-display);font-weight:400;font-size:2rem;margin:0;letter-spacing:-.01em}
.sub{color:var(--muted-med);margin:0 0 1.5rem;font-size:.875rem;max-width:56ch}
/* One toolbar holding every lever, the way an analytics tool does it: time range first, then the
   dimensions, then the action. The range used to be a separate row of links above the form, which
   meant two controls that both reloaded the page and neither of which knew about the other — pick a
   range after changing a dropdown and the dropdown was discarded. Inside the form, everything
   applies together. */
/* Faceted filter bar.
   Named .filters, NOT .bar — ".bar" is already a row in the horizontal bar charts below, and being
   defined later it would win, silently rendering this with a three-column chart grid. (Quotes, not
   backticks: this stylesheet is a template literal and a backtick in a comment ends it.)

   The popovers are <details>/<summary>. That is a disclosure widget the browser opens and closes on
   its own, which is the whole reason this page can offer multi-select facets while shipping no
   JavaScript at all and keeping script-src closed. */
.filters{display:flex;flex-wrap:wrap;align-items:center;gap:.5rem;margin-bottom:.85rem}
.filters .range{font:inherit;font-size:.8rem;padding:.4rem 1.8rem .4rem .7rem;border-radius:999px;
 border:1px solid var(--border);background-color:var(--card);color:var(--fg);appearance:none;
 cursor:pointer;
 background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='10' height='6'%3E%3Cpath d='M1 1l4 4 4-4' fill='none' stroke='%23999' stroke-width='1.5'/%3E%3C/svg%3E");
 background-repeat:no-repeat;background-position:right .65rem center}
.facet{position:relative}
.facet>summary{display:flex;align-items:center;gap:.4rem;list-style:none;cursor:pointer;
 font-size:.8rem;padding:.4rem .8rem;border-radius:999px;border:1px dashed var(--border);
 color:var(--muted-med);white-space:nowrap;user-select:none}
.facet>summary::-webkit-details-marker{display:none}
.facet>summary:hover{border-color:var(--amber-border);color:var(--fg)}
.facet.active>summary{border-style:solid;border-color:var(--amber-border);color:var(--fg);
 background:var(--card)}
.facet .plus{font-weight:600;opacity:.75}
.facet.active .plus{display:none}
.facet .div{width:1px;height:.9rem;background:var(--border)}
.facet .badge{font-size:.72rem;padding:.1rem .45rem;border-radius:6px;background:var(--amber-dim);
 color:var(--accent-fg);max-width:8rem;overflow:hidden;text-overflow:ellipsis}
.facet[open]>summary{border-color:var(--amber-border)}
.pop{position:absolute;z-index:20;top:calc(100% + .35rem);left:0;min-width:15rem;max-width:22rem;
 background:var(--card);border:1px solid var(--border);border-radius:12px;padding:.35rem;
 box-shadow:0 12px 30px rgba(0,0,0,.28)}
.opts{max-height:17rem;overflow-y:auto}
.opt{display:flex;align-items:center;gap:.5rem;padding:.33rem .5rem;border-radius:8px;
 font-size:.82rem;cursor:pointer}
.opt:hover{background:var(--card-hover)}
.opt input{position:absolute;opacity:0;width:0;height:0}
.opt .tick{flex:none;width:.95rem;height:.95rem;border-radius:4px;border:1px solid var(--border);
 display:inline-block;position:relative}
.opt.on .tick,.opt input:checked+.tick{background:var(--gold);border-color:var(--gold)}
.opt.on .tick::after,.opt input:checked+.tick::after{content:"";position:absolute;left:.28rem;
 top:.08rem;width:.22rem;height:.5rem;border:solid #130e00;border-width:0 2px 2px 0;
 transform:rotate(45deg)}
.opt input:focus-visible+.tick{outline:2px solid var(--accent-fg);outline-offset:1px}
.opt .name{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.opt .cnt{font-size:.72rem;color:var(--muted);font-variant-numeric:tabular-nums}
.pop.dates{min-width:13rem;padding:.6rem}
.dt{display:flex;align-items:center;justify-content:space-between;gap:.6rem;font-size:.8rem;
 padding:.25rem 0}
.dt span{color:var(--muted)}
.dt input{font:inherit;font-size:.8rem;padding:.25rem .4rem;border-radius:7px;
 border:1px solid var(--border);background:var(--bg);color:var(--fg)}
.panel .note{font-size:.78rem;color:var(--muted);margin:.75rem 0 0;line-height:1.5}
.pop .note{font-size:.72rem;color:var(--muted);margin:.5rem 0 0;line-height:1.4}
.popclear{display:block;margin-top:.25rem;padding:.4rem .5rem;border-top:1px solid var(--border-sub);
 font-size:.78rem;color:var(--muted-med);text-decoration:none;text-align:center}
.popclear:hover{color:var(--accent-fg)}
.filters .go{font:inherit;font-size:.8rem;font-weight:600;padding:.42rem 1.1rem;border-radius:999px;
 border:1px solid transparent;background:var(--gold);color:#130e00;cursor:pointer}
.filters .go:hover{background:var(--gold-bright)}
.filters .reset{font-size:.79rem;color:var(--muted-med);text-decoration:none;padding:.42rem .3rem}
.filters .reset:hover{color:var(--accent-fg)}
.chips{display:flex;flex-wrap:wrap;gap:.4rem;margin:0 0 1.75rem}
.chip{font-size:.76rem;padding:.24rem .65rem;border-radius:999px;background:var(--amber-dim);
 border:1px solid var(--amber-border);color:var(--accent-fg);text-decoration:none}
.chip b{font-weight:600} .chip span{opacity:.7;margin-left:.35rem}
.chip:hover{background:var(--card-hover)}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(10rem,1fr));gap:.9rem;margin-bottom:2rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:var(--radius-sm);
 padding:1rem 1.15rem}
.card b{display:block;font-family:var(--font-display);font-weight:400;font-size:2.1rem;
 line-height:1.1;font-variant-numeric:tabular-nums}
.card span{display:block;color:var(--muted);font-size:.75rem;text-transform:uppercase;
 letter-spacing:.06em;margin-top:.25rem}
.card.accent b{color:var(--accent-strong)}
h2{font-size:.78rem;text-transform:uppercase;letter-spacing:.09em;color:var(--muted);
 font-weight:600;margin:2.5rem 0 .75rem}
.panel{background:var(--card);border:1px solid var(--border);border-radius:var(--radius-sm);
 padding:1.1rem 1.25rem;overflow:hidden}
.chart{display:block;width:100%;height:150px}
.legend{display:flex;gap:1.1rem;margin-top:.6rem;font-size:.75rem;color:var(--muted);flex-wrap:wrap}
.legend i{display:inline-block;width:.7rem;height:.15rem;vertical-align:middle;margin-right:.35rem;
 border-radius:2px}
.bars{display:flex;flex-direction:column;gap:.45rem}
.bar{display:grid;grid-template-columns:minmax(6rem,11rem) 1fr minmax(4rem,auto);
 align-items:center;gap:.75rem;font-size:.85rem}
.bar-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--fg);
 text-decoration:none}
a.bar-label:hover{color:var(--accent-fg)}
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
.bad{color:var(--bad)}
.empty{color:var(--muted);font-size:.85rem;margin:.4rem 0;text-align:center;padding:1.5rem 0}
footer{margin-top:3rem;padding-top:1.25rem;border-top:1px solid var(--border-sub);
 color:var(--muted);font-size:.75rem}
.warn{background:var(--card);border:1px solid var(--amber-border);border-radius:var(--radius-sm);
 padding:1rem 1.15rem;color:var(--accent-fg)}
code{font-family:var(--font-mono);font-size:.85em;color:var(--accent-fg)}
/* Skeleton. Shown while the queries run, hidden by a rule sent at the END of the same response —
   which is why this needs no JavaScript: CSS applies as it is parsed. */
.sk-block{background:var(--card);border:1px solid var(--border);border-radius:var(--radius-sm);
 margin-bottom:.9rem}
.sk-line{height:.7rem;border-radius:999px;margin:.55rem 0;
 background:linear-gradient(90deg,var(--border-sub),var(--card-hover),var(--border-sub));
 background-size:200% 100%;animation:sh 1.3s ease-in-out infinite}
@keyframes sh{0%{background-position:200% 0}100%{background-position:-200% 0}}
@media (prefers-reduced-motion:reduce){.sk-line{animation:none}}
`;

function shellHead(title) {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escapeHtml(title)}</title><style>${CSS}</style></head><body><main>`;
}

function headerBlock() {
  return `<header><span class="wordmark">Iron Rain</span><h1>Telemetry</h1></header>
<p class="sub">Anonymised: no paths, prompts, tokens or repo names, and install ids are random values
each daemon assigns itself. Nothing here identifies a person.</p>`;
}

/** The same skeleton, parked in a template so the client can re-show it during a filter swap
 * without rebuilding the markup in JavaScript — one definition, two consumers. */
function skeletonTemplate() {
  return `<template id="sk">${skeleton().replace(/^<div class="sk">/, "<div>").replace(/<\/div>$/, "</div>")}</template>`;
}

/** Placeholder shown while the queries run; removed by a style rule at the end of the stream. */
function skeleton() {
  const lines = (n, w = 100) =>
    Array.from({ length: n }, (_, i) => `<div class="sk-line" style="width:${w - i * 7}%"></div>`).join("");
  return `<div class="sk">
<div class="sk-block" style="padding:1rem 1.15rem">${lines(1, 60)}</div>
<div class="cards">${Array.from(
    { length: 4 },
    () => `<div class="card">${lines(2, 70)}</div>`
  ).join("")}</div>
<div class="sk-block" style="padding:1.1rem 1.25rem;height:11rem">${lines(1, 100)}</div>
<div class="sk-block" style="padding:1.1rem 1.25rem">${lines(6)}</div>
<div class="sk-block" style="padding:1.1rem 1.25rem">${lines(4)}</div>
</div>`;
}

/** The faceted filter bar: one popover per dimension, multi-select, with counts.
 *
 * Built on <details>/<summary>, which is a popover the browser already knows how to open and close.
 * The alternative is a script, and this page's entire security posture is that it has none —
 * script-src stays closed, so nothing that lands in the dataset can ever become executable here.
 *
 * What that costs: the list is not type-to-filter, and changes need Apply rather than applying live.
 * What it buys, beyond the CSP: a form that works before, during and after any script would have
 * loaded, and a URL that fully describes the view — which is what makes a filtered dashboard
 * something you can send to someone.
 */
function filterBar(facetRows, range, active) {
  // Count each value across the window. Facet rows are grouped tuples, so one row contributes its
  // weight to every dimension it names.
  const counts = {};
  for (const f of FILTERS) counts[f.key] = new Map();
  for (const r of facetRows) {
    const n = num(r.n) || 1;
    for (const f of FILTERS) {
      const v = String(r[f.key] ?? "");
      if (!v || !SAFE_VALUE.test(v)) continue;
      counts[f.key].set(v, (counts[f.key].get(v) || 0) + n);
    }
  }

  const facet = (f) => {
    const chosen = active[f.key] || [];
    const values = [...counts[f.key].entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
    if (!values.length && !chosen.length) return "";

    const options = values
      .slice(0, 60)
      .map(([v, n]) => {
        const on = chosen.includes(v);
        return `<label class="opt${on ? " on" : ""}">
<input type="checkbox" name="${f.key}" value="${escapeHtml(v)}"${on ? " checked" : ""}>
<span class="tick" aria-hidden="true"></span>
<span class="name" title="${escapeHtml(v)}">${escapeHtml(v)}</span>
<span class="cnt">${fmt(n)}</span></label>`;
      })
      .join("");

    // Selected values ride on the trigger, as they do in the pattern this follows: the point of a
    // faceted bar is that the current query is readable without opening anything.
    const badges = chosen
      .slice(0, 2)
      .map((v) => `<span class="badge">${escapeHtml(v)}</span>`)
      .join("");
    const more = chosen.length > 2 ? `<span class="badge">+${chosen.length - 2}</span>` : "";

    return `<details class="facet${chosen.length ? " active" : ""}">
<summary><span class="plus" aria-hidden="true">+</span><span class="dim">${f.label}</span>${
      chosen.length ? `<span class="div"></span>${badges}${more}` : ""
    }</summary>
<div class="pop">
<div class="opts">${options || '<p class="empty">nothing in this window</p>'}</div>
${
  chosen.length
    ? `<a class="popclear" href="${escapeHtml(queryWith(active, range, { [f.key]: [] }))}">Clear ${escapeHtml(
        f.label.toLowerCase()
      )}</a>`
    : ""
}
</div></details>`;
  };

  const rangeOpts = PRESETS.map(
    ([k, label]) =>
      `<option value="${k}"${range.kind === "preset" && range.key === k ? " selected" : ""}>${label}</option>`
  ).join("");

  // A custom window sits alongside the presets rather than replacing them: presets answer "what is
  // happening now", which is most visits, and a date pair answers "what happened on Tuesday", which
  // no preset can express. Native date inputs, so there is still no script involved.
  const custom = `<details class="facet${range.kind === "custom" ? " active" : ""}">
<summary><span class="plus" aria-hidden="true">+</span><span class="dim">Dates</span>${
    range.kind === "custom"
      ? `<span class="div"></span><span class="badge">${escapeHtml(range.label)}</span>`
      : ""
  }</summary>
<div class="pop dates">
<label class="dt"><span>From</span><input type="date" name="from" value="${
    range.kind === "custom" ? escapeHtml(range.from) : ""
  }"></label>
<label class="dt"><span>To</span><input type="date" name="to" value="${
    range.kind === "custom" ? escapeHtml(range.to) : ""
  }"></label>
<p class="note">A complete pair overrides the preset. Clearing either returns to it.</p>
</div></details>`;

  const statusOpts = [
    ["", "Any outcome"],
    ["ok", "Succeeded"],
    ["failed", "Failed"],
  ]
    .map(([v, l]) => `<option value="${v}"${(active.status || "") === v ? " selected" : ""}>${l}</option>`)
    .join("");

  const anyActive = FILTERS.some((f) => (active[f.key] || []).length) || !!active.status;

  return `<form class="filters" method="get" id="f">
<select class="range" name="range" aria-label="Time range"${
    range.kind === "custom" ? " disabled" : ""
  }>${rangeOpts}</select>
${custom}
${FILTERS.map(facet).join("")}
<select class="range" name="status" aria-label="Outcome">${statusOpts}</select>
<button class="go" type="submit">Apply</button>
${anyActive ? `<a class="reset" href="?range=${range.kind === "preset" ? range.key : "7d"}">Reset <span aria-hidden="true">&times;</span></a>` : ""}
</form><div id="chips">${activeChips(active, range)}</div>`;
}

/** Active values as removable chips, so the current query is legible without opening a popover. */
function activeChips(active, range) {
  const chips = [];
  for (const f of FILTERS) {
    for (const v of active[f.key] || []) {
      chips.push(
        `<a class="chip" href="${escapeHtml(without(active, range, f.key, v))}"><b>${f.label}</b> ${escapeHtml(
          v
        )}<span>&times;</span></a>`
      );
    }
  }
  if (active.status) {
    chips.push(
      `<a class="chip" href="${escapeHtml(queryWith(active, range, { status: "" }))}"><b>Outcome</b> ${escapeHtml(
        active.status
      )}<span>&times;</span></a>`
    );
  }
  return chips.length ? `<div class="chips">${chips.join("")}</div>` : "";
}

function setupPage(why) {
  return (
    shellHead("Telemetry — Iron Rain") +
    `<header><span class="wordmark">Iron Rain</span><h1>Telemetry</h1></header>
<p class="warn">${escapeHtml(why)}</p>
<h2>Configuration</h2>
<div class="panel tbl"><table>
<tr><th>variable</th><th>what it does</th></tr>
<tr><td class="mono">STATS_PASSWORD</td><td>the password this page prompts for; the username is ignored</td></tr>
<tr><td class="mono">CF_ACCOUNT_ID</td><td>Workers &amp; Pages &rarr; Account ID</td></tr>
<tr><td class="mono">CF_ANALYTICS_TOKEN</td><td>API token with Account &rarr; Account Analytics &rarr; Read</td></tr>
</table></div>
<footer>Set these as GitHub repository secrets and the deploy workflow syncs them to the Worker.
The ingest endpoint is unaffected by all of them.</footer></main></body></html>`
  );
}

function content({ range, byEvent, failures, versions, platforms, total, timing, series, rollover, active }) {
  const failedTotal = byEvent.rows.reduce((a, r) => a + num(r.failed), 0);
  const eventTotal = byEvent.rows.reduce((a, r) => a + num(r.n), 0);
  const rate = eventTotal ? (failedTotal / eventTotal) * 100 : 0;
  const points = series.rows.map((r) => ({ n: num(r.n), failed: num(r.failed) }));
  const link = (key) => (r) => only(active, range, key, r[key]);

  return `<div class="cards">
  <div class="card accent"><b>${fmt(total.installs)}</b><span>installs seen</span></div>
  <div class="card"><b>${fmt(total.events)}</b><span>events</span></div>
  <div class="card"><b class="${failedTotal ? "bad" : ""}">${rate.toFixed(1)}%</b><span>failure rate</span></div>
  <div class="card"><b>${fmt(versions.rows.length)}</b><span>versions live</span></div>
</div>

<h2>Activity — ${escapeHtml(range.label)}</h2>
<div class="panel">${areaChart(points)}
<div class="legend"><span><i style="background:var(--gold-bright)"></i>events · peak ${fmt(
    Math.max(0, ...points.map((p) => p.n))
  )}</span>
<span><i style="background:var(--bad)"></i>failures · peak ${fmt(
    Math.max(0, ...points.map((p) => p.failed))
  )} (own scale)</span></div></div>

<h2>Events</h2>
<div class="panel">${bars(byEvent.rows.slice(0, 12), {
    label: (r) => r.event,
    value: (r) => r.n,
    sub: (r) => (num(r.failed) ? `${fmt(r.n)} · ${fmt(r.failed)} failed` : fmt(r.n)),
    href: link("event"),
  })}</div>

<h2>Failures</h2>
<div class="panel tbl"><table><tr><th>event</th><th>error</th><th>count</th></tr>
${rows(failures.rows, (r) => `<tr><td>${escapeHtml(r.event)}</td>
<td class="err">${escapeHtml(r.error)}</td><td class="n bad">${fmt(r.n)}</td></tr>`)}</table></div>

<h2>Turn duration by provider</h2>
<div class="panel">${bars(timing.rows, {
    label: (r) => r.provider,
    value: (r) => r.p95,
    sub: (r) => `p50 ${dur(r.p50)} · p95 ${dur(r.p95)}`,
    href: link("provider"),
  })}</div>

<h2>Ingest key rollover</h2>
<div class="panel">${rolloverPanel(rollover.rows)}</div>

<h2>Versions</h2>
<div class="panel tbl"><table><tr><th>version</th><th>installs</th><th>events</th></tr>
${rows(versions.rows, (r) => `<tr><td class="mono">${
    r.version
      ? `<a href="${escapeHtml(only(active, range, "version", r.version))}">${escapeHtml(r.version)}</a>`
      : "unknown"
  }</td><td class="n">${fmt(r.installs)}</td><td class="n">${fmt(r.events)}</td></tr>`)}</table></div>

<h2>Platforms</h2>
<div class="panel tbl"><table><tr><th>os</th><th>arch</th><th>installs</th></tr>
${rows(platforms.rows, (r) => `<tr>
<td><a href="${escapeHtml(only(active, range, "os", r.os))}">${escapeHtml(r.os)}</a></td>
<td class="mono">${escapeHtml(r.arch)}</td><td class="n">${fmt(r.installs)}</td></tr>`)}</table></div>

<footer>Counts are SUM(_sample_interval), not count(): Analytics Engine samples under load and
count() would report only the rows that survived it — under-reporting exactly when traffic is high
enough to matter.</footer>`;
}

/** How much of the fleet sends the ingest key — the fact that decides when it can be enforced. */
function rolloverPanel(rows) {
  const by = Object.fromEntries(rows.map((r) => [String(r.keyed || ""), r]));
  const keyed = num(by.keyed?.installs);
  const unkeyed = num(by.unkeyed?.installs);
  const total = keyed + unkeyed;
  if (!total) return `<p class="empty">no data for this selection</p>`;

  const pct = ((keyed / total) * 100).toFixed(0);
  const done = unkeyed === 0;
  return `<div class="bars">
<div class="bar"><span class="bar-label">sending the key</span>
<span class="bar-track"><span class="bar-fill" style="width:${(keyed / total) * 100}%"></span></span>
<span class="bar-val">${fmt(keyed)} install${keyed === 1 ? "" : "s"}</span></div>
<div class="bar"><span class="bar-label">not yet</span>
<span class="bar-track"><span class="bar-fill" style="width:${(unkeyed / total) * 100}%;background:var(--bad)"></span></span>
<span class="bar-val">${fmt(unkeyed)} install${unkeyed === 1 ? "" : "s"}</span></div>
</div>
<p class="note">${
    done
      ? `${pct}% — every install seen in this window sends the key. INGEST_STRICT can be set; ` +
        `until it is, the key filters nothing.`
      : `${pct}% — ${fmt(unkeyed)} install${unkeyed === 1 ? "" : "s"} predate the key. Setting ` +
        `INGEST_STRICT now would drop their telemetry silently: the daemon does not surface a ` +
        `failed report, so the data would simply stop and read as a quiet fleet.`
  }</p>`;
}
