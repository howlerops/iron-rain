import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// Runs the tests INSIDE workerd (via miniflare) against the real wrangler config, so the
// Durable Object, hibernation tags and WebSocket close semantics under test are the ones
// that ship — not a mock of them.
//
// That also means this path is load-bearing: when the config moved from TOML to JSONC, this line
// was the thing that noticed, by failing to start the pool at all.
export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.jsonc" } })],
});
