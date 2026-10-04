import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// The same shape as cloudflare/vitest.config.ts — real workerd, bindings read
// straight from wrangler.toml — minus the D1 migrations and the watchdog: this
// worker has no D1, and a runaway workerd from THIS suite is reaped the same
// way (the watchdog's rule was about the factory suite's 80 s runs; these two
// files are seconds).
export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.toml" } })],
  test: {
    include: ["test/**/*.test.ts"],
    // Each conformance case is a fresh Durable Object running migrations and
    // a scripted scenario on a 2-vCPU CI runner; the Vitest default (5 s) is
    // a laptop number, as the factory suite already learned.
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});
