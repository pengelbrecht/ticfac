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
    // FLAT, on purpose: the node-own half of this package's suites lives in
    // test/node/ (vitest.node.config.ts) and runs real bash, which workerd
    // cannot — a glob that reached it here would run those tests inside
    // workerd, where child_process is not implemented and every one of them
    // fails for a reason that has nothing to do with the code.
    include: ["test/*.test.ts"],
    // Each conformance case is a fresh Durable Object running migrations and
    // a scripted scenario on a 2-vCPU CI runner; the Vitest default (5 s) is
    // a laptop number, as the factory suite already learned.
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});
