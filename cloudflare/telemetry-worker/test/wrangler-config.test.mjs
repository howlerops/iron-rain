import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");

// Both Workers must keep answering on workers.dev after gaining a custom domain.
//
// wrangler DISABLES the workers.dev subdomain the moment [[routes]] appears, unless workers_dev is
// set. That took both old hostnames to 404 in one deploy — and the old addresses are what every
// already-paired device and every shipped build still talk to. For telemetry it is worse than a
// broken link: DefaultEndpoint is a single URL with no fallback, so field builds go silent with no
// error on either side.
//
// The assertion is on the PARSED value at root scope, not on the text. The first fix wrote
// `workers_dev = true` after a table header, where TOML scoped it to that table and it did nothing
// — a grep for the string would have passed while the setting was inert.
const CONFIGS = [
  ["relay-cf/wrangler.toml", "oculus-relay.jacobbeck-dev.workers.dev"],
  ["cloudflare/telemetry-worker/wrangler.toml", "oculus-telemetry.jacobbeck-dev.workers.dev"],
];

/** Minimal TOML reader: root-scope bare keys and table headers are all this needs to know. */
function rootKeys(text) {
  const out = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    if (line.startsWith("[")) break; // everything after the first header belongs to a table
    const eq = line.indexOf("=");
    if (eq > 0) out[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  return out;
}

for (const [path, oldHost] of CONFIGS) {
  test(`${path} keeps workers.dev enabled at root scope`, () => {
    const text = readFileSync(join(repo, path), "utf8");
    assert.ok(text.includes("[[routes]]"), `${path} has no custom domain; this test guards the interaction between the two`);

    const root = rootKeys(text);
    assert.equal(
      root.workers_dev,
      "true",
      `workers_dev is not set at ROOT scope in ${path}. With [[routes]] present, wrangler disables ` +
        `the workers.dev subdomain — so ${oldHost} stops serving, and everything already paired ` +
        `against it breaks. Note a key placed after a [table] header belongs to that table and has ` +
        `no effect here.`
    );
  });
}
