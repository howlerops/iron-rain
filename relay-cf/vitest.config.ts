import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// Runs the tests INSIDE workerd (via miniflare) against the real wrangler.toml, so the
// Durable Object, hibernation tags and WebSocket close semantics under test are the ones
// that ship — not a mock of them.
export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.toml" } })],
});
