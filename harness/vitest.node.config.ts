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
    // Load-sized defaults (tick fim): this suite drives real bash, real git
    // and real child processes on a shared host whose observed steady load
    // is 23-47, and under synthetic 12x oversubscription 24 of its tests
    // failed at the quiet-host 30s testTimeout while beforeEach hooks —
    // real git fixtures — timed out at the 30s hookTimeout. 120s is 4x that
    // default: room for a loaded host, still a bound a genuinely hung test
    // fails inside. The tests whose runtime is process-bound state their
    // own 300s bounds on top (harness-timeout-discipline.test.ts), so this
    // default is the floor, never the discipline.
    testTimeout: 120_000,
    hookTimeout: 120_000,
  },
});
