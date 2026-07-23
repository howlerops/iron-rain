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
    if (request.method === "OPTIONS") return cors(new Response(null, { status: 204 }));
    const url = new URL(request.url);
    if (request.method !== "POST" || url.pathname !== "/ingest") {
      return cors(new Response("not found", { status: 404 }));
    }
    if (!env.TELEMETRY) return cors(new Response("telemetry binding missing", { status: 500 }));

    let body;
    try {
      body = await request.json();
    } catch {
      return cors(new Response("bad json", { status: 400 }));
    }
    const events = Array.isArray(body?.events) ? body.events : null;
    if (!events) return cors(Response.json({ ok: false, error: "events must be an array" }, { status: 400 }));

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
    return cors(Response.json({ ok: true, accepted }));
  },
};

function str(v) {
  return (v == null ? "" : String(v)).slice(0, 256);
}
function num(v) {
  const n = Number(v);
  return Number.isFinite(n) ? n : 0;
}
function cors(r) {
  r.headers.set("Access-Control-Allow-Origin", "*");
  r.headers.set("Access-Control-Allow-Methods", "POST, OPTIONS");
  r.headers.set("Access-Control-Allow-Headers", "Content-Type");
  return r;
}
