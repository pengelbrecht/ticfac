import { defineConfig } from "vitest/config";

// The NODE half of this package's suites (epic 43y, tick kgk).
//
// The workerd config (vitest.config.ts) runs everything that the cloud host
// runs — storage conformance, transcript replay, the RPC shapes of the
// execution environments — inside real workerd, where nothing can exec.
// These tests are the ones that must NOT run there: the boundary guard and
// the short-command FileSystem are shell behaviour, and a shell behaviour
// test that never runs a shell certifies nothing. They run against real
// bash, real files and real processes, the same way the Go suite proves
// image/worker.sh's own guard (internal/sandboximage).
//
// A separate config, not a second project inside the workerd one: the
// pool-workers plugin is a Vite plugin that owns every test file it is
// handed, so the two runtimes are split at the config level and `pnpm test`
// runs both.
export default defineConfig({
  test: {
    include: ["test/node/**/*.test.ts"],
    environment: "node",
    testTimeout: 30_000,
    hookTimeout: 30_000,
  },
});
