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
