import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-pool-workers";
import { defineConfig } from "vitest/config";

// The factory harness mirrors cloud/worker/test in intent — real workerd,
// bindings read straight from wrangler.toml, so a failure means the deployable
// config is wrong rather than that a mock drifted. It does NOT mirror its
// versions: cloud/worker is on vitest 3 + pool-workers 0.9 (where the pool was
// configured through `test.poolOptions.workers`); this bundle is on vitest 4 +
// pool-workers 0.21, where the pool is a Vite plugin.
export default defineConfig(async () => {
  // `import.meta.url` is standard and typed by vite/client, so the config
  // needs no Node type definitions (this bundle does not ship @types/node).
  const migrationsPath = new URL("./migrations", import.meta.url).pathname;
  const migrations = await readD1Migrations(migrationsPath);

  return {
    plugins: [
      cloudflareTest({
        wrangler: { configPath: "./wrangler.toml" },
        miniflare: {
          // The Workers test runtime starts D1 empty. Keep the migration
          // objects in the test runtime so setup can apply the same SQL that
          // `tk factory deploy` applies from this directory.
          bindings: { TEST_MIGRATIONS: migrations },
        },
      }),
    ],
    test: {
      include: ["test/**/*.test.ts"],
      setupFiles: ["./test/apply-migrations.ts"],
      // One test file at a time. The suite runs inside real workerd, and
      // run-workflow.test.ts drives Workflows, Durable Objects and D1 with
      // wall-clock budgets; when other files run beside it on a 2-vCPU CI
      // runner it fails with hung-context cancellations, "evicted mid-commit"
      // and "no such table" — state torn out from under a live test, not code
      // under test. Observed on every CI run of epic 692 (which added ~150
      // tests in new files) while main stayed green; the file alone passes.
      // Serial files cost minutes, not correctness. (Legacy tick 5qj.)
      //
      // Re-measured 2026-09-18 on @cloudflare/vitest-pool-workers 0.21.3 /
      // vitest 4.1.11 (tick 3xe). STAYS OFF, and two things the 5qj wording
      // above gets wrong are worth writing down so the next re-measurement
      // starts from the truth:
      //
      // 1. Two of the three signatures 5qj names are NOT evidence of this bug.
      //    A fully serial, fully green run (53 files, 1421 passed) emits 71
      //    "had hung and would never generate a response" cancellations and 6
      //    "evicted mid-commit" lines. They are background noise from tests
      //    that deliberately abandon Workflow and Durable Object work, and
      //    counting them tells you nothing. "no such table" appeared zero
      //    times in any run, serial or parallel. Judge this by whether tests
      //    FAIL, not by grepping the log. This is the same finding as tick
      //    heu, from the other end: a green run of this suite and a broken
      //    one produce indistinguishable stderr, so the wall of workerd noise
      //    heu is about is exactly what makes the 5qj signatures useless as
      //    evidence. Fixing heu would also make this flag re-measurable.
      //
      // 2. Parallel does not reliably fail — which is exactly why it must not
      //    be turned on. Five full parallel runs on a 10-core laptop: four
      //    green, and the fifth failed five tests, all in run-workflow.test.ts
      //    (the file 5qj names), with "timed out waiting for the orchestrator
      //    to start", "timed out waiting for run run_wf_53 to finish" and
      //    "Engine was never started". The failing run was the one where the
      //    machine's load average happened to spike. Flipping this flag on a
      //    green run is sampling, not measuring.
      //
      // And it buys almost nothing here: parallel green runs took 54.19 s,
      // 70.77 s, 71.12 s and 84.47 s against 78.98 s serial — call it ten
      // seconds of an eighty-second suite, on hardware with five times CI's
      // cores. On the 2-vCPU runner where 5qj was observed there are no spare
      // cores for the gain and every reason for the contention. Ten seconds is
      // not worth a test suite that fails one run in five for reasons that
      // have nothing to do with the code under test.
      //
      // What this re-measurement could NOT test, stated plainly so nobody
      // reads more into it than it holds: all six runs were on a 10-core
      // macOS laptop under varying load from other work on the same machine.
      // NOTHING here was run on the 2-vCPU ubuntu-latest runner where 5qj's
      // corruption was actually observed, so this confirms that parallelism
      // still breaks, and does NOT establish how it breaks on CI or how often.
      // The only place that claim can be settled is CI itself. If a future
      // tick wants to settle it, the shape is a temporary workflow_dispatch
      // job that runs this suite with the flag on, N times, on the real
      // runner — not another laptop sample.
      fileParallelism: false,
      // Serial files removed the state-corruption failures and left plain
      // "Test timed out in 5000ms" on the same file: run-workflow's Workflow
      // legs are budgeted in wall-clock and a 2-vCPU runner runs them at a
      // fraction of a laptop's speed. Vitest's 5 s default is a laptop number.
      testTimeout: 30_000,
      hookTimeout: 30_000,
    },
  };
});
