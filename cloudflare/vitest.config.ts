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
      // A vitest that is SIGKILLed never runs miniflare's exit hook, and the
      // workerd it started outlives it forever (two dozen had piled up on the
      // shared host). The watchdog reaps them by exact pid once this vitest
      // is gone (scripts/workerd-watchdog.mjs).
      globalSetup: ["./scripts/workerd-watchdog-setup.mjs"],
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
      //
      // Re-measured again 2026-10-07 (tick 3xe, the second re-measurement),
      // because the runtime had moved under the flag: #203 (2026-10-04) took
      // this suite from pool-workers 0.21.3 to 0.22.0 (vitest still 4.1.11,
      // vite 8.2.1, miniflare 5.20261001.0-alpha) and the suite grew from 53
      // to 80 test files — everything above measured a suite that no longer
      // exists. The committed flag stayed put and parallelism came from the
      // CLI (`vitest run --file-parallelism` overrides this config; the
      // proof it took is in vitest's own Duration line — the first parallel
      // run imported cumulatively for 1164 s inside a 171 s wall, against
      // 154 s of import in the 236 s serial baseline).
      //
      // Result: 20 parallel runs, ALL green — 80 files, 1864 tests, zero
      // failures every time — beside 2 green serial baselines (236.04 s at
      // host load ~5, 366.24 s at ~30). "no such table" appeared in NONE of
      // the 22 runs, and the point 1 rule held again: 6 "evicted mid-commit"
      // lines every run, 17-22 hung-context cancellations in green parallel
      // runs (a green serial one emits ~60) — noise from abandoned work,
      // never a verdict. Ten of the twenty ran under recorded 1-minute host
      // load 13-87, one of them through the 87 spike; the first ten ran as
      // the host climbed ~5 to ~14. That is the load-spike condition that
      // killed the 09-18 fifth run, hit ten times over, and none failed: at
      // that measurement's 1-in-5 rate, twenty greens would be luck at
      // p ≈ 1% (0.8^20).
      //
      // Wall clock on the 10-core laptop: parallel 73-171 s, median 109 s,
      // against serial 236-366 s — parallel is worth 2-3x here. So the 0.21.3
      // breakage did NOT survive the 0.22.0 runtime on this class of host.
      //
      // STAYS OFF ANYWAY. This flag exists for the 2-vCPU ubuntu runner
      // where 5qj's corruption was observed, and twenty green laptop runs
      // are still zero CI runs. The corruption was last confirmed on CI in
      // epic 692 (pre-5qj, on that runner) and on a laptop 2026-09-18 on
      // pool-workers 0.21.3; nothing has since confirmed it in 0.22.0, and
      // nothing here can clear CI. Turning the flag on now would change the
      // CI suite on no CI evidence at all — the same bet the paragraph above
      // already refused, and the gain it would buy there is unmeasured too
      // (CI runs this suite beside a Go job several times longer, so the
      // typescript job is not CI's critical path; the 345 s serial cost the
      // tick names is the suite's own duration, not a PR's). The path to
      // flipping this is still the one above: N parallel runs on the real
      // runner, via the temporary workflow_dispatch job. With 0.22.0 green
      // twenty times on a loaded laptop, that CI measurement is now the
      // only thing between this suite and parallel files.
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
