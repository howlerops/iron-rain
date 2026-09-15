import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

// Both Workers must keep answering on workers.dev after gaining a custom domain, and the settings
// that ensure it must be where Cloudflare will actually read them.
//
// The history this guards, because it is the reason the config is JSONC now. Defining routes makes
// wrangler DISABLE the workers.dev subdomain unless workers_dev is set — that alone took both old
// hostnames to 404 in one deploy. The fix then set workers_dev = true *after* a TOML table header,
// where the key belongs to that table: it parsed, it deployed, and it was inert, so the next deploy
// was green while the endpoints stayed dead. A grep for the string would have passed both times.
//
// Hence two rules below: assert on the PARSED value at root scope, and refuse TOML outright for
// these files. The second is the permanent fix — JSON has no implicit scoping, so the class of
// mistake cannot recur, rather than being policed after the fact.

const WORKERS = [
  {
    dir: "relay-cf",
    oldHost: "oculus-relay.jacobbeck-dev.workers.dev",
    domain: "relay.ironrain.app",
  },
  {
    dir: "cloudflare/telemetry-worker",
    oldHost: "oculus-telemetry.jacobbeck-dev.workers.dev",
    domain: "telemetry.ironrain.app",
  },
];

/** Parses JSONC the way wrangler does: strip // line comments outside strings, then JSON.parse. */
function readJsonc(path) {
  const raw = readFileSync(path, "utf8");
  const stripped = raw
    .split("\n")
    .map((line) => {
      const i = line.indexOf("//");
      if (i < 0) return line;
      const before = line.slice(0, i);
      // A // inside a string literal is not a comment; an odd quote count means we're inside one.
      return (before.match(/"/g) || []).length % 2 === 0 ? before : line;
    })
    .join("\n");
  return JSON.parse(stripped);
}

for (const w of WORKERS) {
  test(`${w.dir}: config is JSONC, not TOML`, () => {
    assert.ok(
      existsSync(join(repo, w.dir, "wrangler.jsonc")),
      `${w.dir}/wrangler.jsonc is missing`
    );
    assert.ok(
      !existsSync(join(repo, w.dir, "wrangler.toml")),
      `${w.dir}/wrangler.toml is back. TOML is what made this class of bug possible: a bare key ` +
        `belongs to the table header above it, so a root-level setting written in the wrong place ` +
        `parses cleanly and does nothing. If both files exist wrangler picks one and the other is ` +
        `a decoy. Keep the JSONC.`
    );
  });

  test(`${w.dir}: workers.dev stays enabled alongside the custom domain`, () => {
    const cfg = readJsonc(join(repo, w.dir, "wrangler.jsonc"));

    assert.ok(
      Array.isArray(cfg.routes) && cfg.routes.some((r) => r.pattern === w.domain),
      `${w.dir} no longer routes ${w.domain}; this test guards the interaction between a custom ` +
        `domain and workers.dev, and without the route there is nothing to guard`
    );
    assert.equal(
      cfg.workers_dev,
      true,
      `workers_dev is not true at the top level of ${w.dir}/wrangler.jsonc. With routes defined, ` +
        `wrangler disables the workers.dev subdomain — so ${w.oldHost} stops serving and everything ` +
        `already paired against it breaks.`
    );
  });

  // The generalised form of the same mistake: a setting that only means something at the top level,
  // nested inside some other object where it will be ignored without complaint.
  test(`${w.dir}: no top-level-only setting is buried inside another object`, () => {
    const cfg = readJsonc(join(repo, w.dir, "wrangler.jsonc"));
    const rootOnly = [
      "workers_dev",
      "routes",
      "route",
      "main",
      "compatibility_date",
      "compatibility_flags",
      "account_id",
      "minify",
      "preview_urls",
    ];
    const found = [];
    const walk = (node, path) => {
      if (!node || typeof node !== "object") return;
      for (const [k, v] of Object.entries(node)) {
        const here = path ? `${path}.${k}` : k;
        if (path && rootOnly.includes(k)) found.push(here);
        walk(v, here);
      }
    };
    for (const [k, v] of Object.entries(cfg)) walk(v, k);

    assert.deepEqual(
      found,
      [],
      `these settings only take effect at the top level, and are nested: ${found.join(", ")}. ` +
        `Nested, they parse fine and are silently ignored — which is exactly how workers_dev came ` +
        `to be set, deployed, and inert while both old hostnames served 404.`
    );
  });
}
