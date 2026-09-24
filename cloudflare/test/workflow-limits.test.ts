import { describe, expect, it } from "vitest";

import {
  COLD_START_BENCHMARK_MS,
  DEFAULT_CONFIRM_TIMEOUT_MS,
  DEFAULT_PROBE_TIMEOUT_MS,
  DEFAULT_SALVAGE_GRACE_MS,
  FANOUT_DEGRADATION_FACTOR,
} from "../src/worker-dispatch";
import {
  STEP_BUDGET_FRACTION,
  STEP_WORK_BUDGET_MS,
  WORKFLOW_STEP_TIMEOUT_MS,
} from "../src/workflow-limits";

/**
 * Cloudflare's per-step execution cap, pinned (tick 2xm).
 *
 * `.tick/learnings.md` records the shape of this class of bug twice already:
 * PBKDF2 at 210k iterations passed sixty local tests and 503'd on every
 * deployed request, because local workerd does not enforce what the edge
 * enforces. The rule it produced — "pin each limit as a named constant with a
 * guard test" — is what this file is. A green vitest run cannot prove the edge
 * accepts a value; it can prove nothing in this package is SIZED past one.
 *
 * The clamps this file used to test (`fitsInStep`, `stepBudget`,
 * `shareStepBudget`) and the width-keyed probe derivation (`probeTimeoutMs`)
 * sized the wave legs the deleted Workflow reconciler ran inside one step;
 * they went with the reconciler (tick mn7). What stays pinned is the cap
 * itself — which `contracts/lifecycle-invariants.json` names as
 * `harness.thresholds.substrate.step_cap_ms`, so it cannot move without a
 * bundle bump — and the dispatch waits the per-tick door's spawn still runs.
 */
describe("the 10-minute Workflow step limit is a named, guarded constant", () => {
  it("is the number Cloudflare killed two live runs with", () => {
    // Both fan-out runs of epic 1vn errored with the literal string
    // "Execution timed out after 600000ms", in step cloud:dispatch:0-1.
    expect(WORKFLOW_STEP_TIMEOUT_MS).toBe(600_000);
  });

  it("never lets a step be sized for its whole allowance", () => {
    expect(STEP_BUDGET_FRACTION).toBe(0.8);
    expect(STEP_WORK_BUDGET_MS).toBe(Math.floor(WORKFLOW_STEP_TIMEOUT_MS * STEP_BUDGET_FRACTION));
    expect(STEP_WORK_BUDGET_MS).toBeLessThan(WORKFLOW_STEP_TIMEOUT_MS);
    // The margin is what pays for the things a step cannot plan: an R2 write,
    // a GitHub round trip, a container that answers late.
    expect(WORKFLOW_STEP_TIMEOUT_MS - STEP_WORK_BUDGET_MS).toBeGreaterThanOrEqual(60_000);
  });

  /**
   * tick 7zk's arithmetic, still: a stop path holds the container's salvage
   * window open INSIDE the same step that found the stop, and that step must
   * not be the one that kills the supervisor. The window is bounded, and it
   * is not the whole allowance.
   */
  it("bounds the salvage window a stop holds open inside one step", () => {
    expect(DEFAULT_SALVAGE_GRACE_MS).toBeLessThan(STEP_WORK_BUDGET_MS);
  });

  /**
   * A dispatch's waits run TOGETHER, and this is the tightest pin in the
   * package: `spawnWorker` waits on the probe (up to the widest measured
   * cold-start allowance) and then on the confirm (three minutes), in ONE
   * call, from the dispatch door. The sum must still fit the hard cap now
   * that nothing scales the shares apart — the wave path's `waveSpawnBudget`
   * died with the wave (tick l6t).
   */
  it("keeps a dispatch's probe and confirm inside one hard step cap, together", () => {
    expect(DEFAULT_PROBE_TIMEOUT_MS + DEFAULT_CONFIRM_TIMEOUT_MS).toBeLessThanOrEqual(
      WORKFLOW_STEP_TIMEOUT_MS,
    );
  });
});

/**
 * The probe budget, still sized by tick 7go's derivation at the widest measured
 * fan-out: the door's dispatches are one container each (tick mn7 deleted the
 * width-keyed curve along with the reconciler that needed it), so the widest
 * measured factor is the one every probe is sized by.
 */
describe("the probe budget covers the measured cold start, degraded for fan-out", () => {
  it("keeps tick 7go's derivation at the widest measured width", () => {
    expect(DEFAULT_PROBE_TIMEOUT_MS).toBe(
      Math.ceil(COLD_START_BENCHMARK_MS * FANOUT_DEGRADATION_FACTOR * 1.2),
    );
  });

  it("never sizes a probe below the bare measured cold start", () => {
    expect(DEFAULT_PROBE_TIMEOUT_MS).toBeGreaterThan(COLD_START_BENCHMARK_MS);
  });
});
