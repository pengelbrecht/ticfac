import { describe, expect, it } from "vitest";

import {
  COLD_START_BENCHMARK_MS,
  DEFAULT_CONFIRM_TIMEOUT_MS,
  DEFAULT_PROBE_TIMEOUT_MS,
  DEFAULT_SALVAGE_GRACE_MS,
  FANOUT_DEGRADATION_FACTOR,
  probeTimeoutMs,
} from "../src/worker-dispatch";
import {
  fitsInStep,
  STEP_WORK_BUDGET_MS,
  shareStepBudget,
  stepBudget,
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
 */
describe("the 10-minute Workflow step limit is a named, guarded constant", () => {
  it("is the number Cloudflare killed two live runs with", () => {
    // Both fan-out runs of epic 1vn errored with the literal string
    // "Execution timed out after 600000ms", in step cloud:dispatch:0-1.
    expect(WORKFLOW_STEP_TIMEOUT_MS).toBe(600_000);
  });

  it("never lets a step be sized for its whole allowance", () => {
    expect(STEP_WORK_BUDGET_MS).toBeLessThan(WORKFLOW_STEP_TIMEOUT_MS);
    // The margin is what pays for the things a step cannot plan: an R2 write,
    // a GitHub round trip, a container that answers late.
    expect(WORKFLOW_STEP_TIMEOUT_MS - STEP_WORK_BUDGET_MS).toBeGreaterThanOrEqual(60_000);
    expect(fitsInStep(STEP_WORK_BUDGET_MS)).toBe(true);
    expect(fitsInStep(STEP_WORK_BUDGET_MS + 1)).toBe(false);
    expect(stepBudget(WORKFLOW_STEP_TIMEOUT_MS * 10)).toBe(STEP_WORK_BUDGET_MS);
  });

  /**
   * tick 7zk's arithmetic, still: a stop path holds the container's salvage
   * window open INSIDE the same step that found the stop, and that step must
   * not be the one that kills the supervisor. The window is bounded, and it
   * is not the whole allowance.
   */
  it("bounds the salvage window a stop holds open inside one step", () => {
    expect(fitsInStep(DEFAULT_SALVAGE_GRACE_MS)).toBe(true);
    expect(DEFAULT_SALVAGE_GRACE_MS).toBeLessThan(STEP_WORK_BUDGET_MS);
  });

  /**
   * A dispatch's waits are budgeted TOGETHER, and this is the tightest pin in
   * the package: `spawnWorker` waits on the probe (up to the widest measured
   * cold-start allowance) and then on the confirm (three minutes), in ONE
   * call, from inside a Workflow step (the reconciler's start) and from the
   * dispatch door. The sum is two seconds under the hard cap — the wave
   * path's `waveSpawnBudget` used to scale the two shares apart to buy room
   * for everything else in the step; that scaling died with the wave
   * (tick l6t), so what is pinned now is that the UNSCALED sum still fits
   * the cap at all.
   */
  it("keeps a dispatch's probe and confirm inside one hard step cap, together", () => {
    expect(DEFAULT_PROBE_TIMEOUT_MS + DEFAULT_CONFIRM_TIMEOUT_MS).toBeLessThanOrEqual(
      WORKFLOW_STEP_TIMEOUT_MS,
    );
    // The widest measured probe is the default, never an extrapolation.
    expect(DEFAULT_PROBE_TIMEOUT_MS).toBe(probeTimeoutMs(5));
  });

  it("scales every share when a caller asks for more than a step can spend", () => {
    const shared = shareStepBudget({ a: 400_000, b: 400_000 }, "a test");
    expect(shared.a! + shared.b!).toBeLessThanOrEqual(STEP_WORK_BUDGET_MS);
    // In proportion — never starving the last share to pay the first.
    expect(shared.a).toBe(shared.b);
  });
});

/**
 * The probe budget. The width parameter carries the fan-out degradation curve
 * tick 2xm measured (and the wave path's deletion leaves nothing running it
 * above width 1); the derivation itself still sizes every probe `spawnWorker`
 * waits on, so it stays pinned — including the measured data.
 */
describe("probeTimeoutMs", () => {
  it("keeps tick 7go's derivation at the widest measured width", () => {
    expect(probeTimeoutMs(5)).toBe(DEFAULT_PROBE_TIMEOUT_MS);
    expect(probeTimeoutMs(5)).toBe(
      Math.ceil(COLD_START_BENCHMARK_MS * FANOUT_DEGRADATION_FACTOR * 1.2),
    );
  });

  it("charges a narrower fan-out only the degradation it measured", () => {
    expect(probeTimeoutMs(1)).toBe(Math.ceil(COLD_START_BENCHMARK_MS * 1.2));
    expect(probeTimeoutMs(3)).toBe(Math.ceil(COLD_START_BENCHMARK_MS * 2.22 * 1.2));
    expect(probeTimeoutMs(2)).toBeGreaterThan(probeTimeoutMs(1));
    expect(probeTimeoutMs(2)).toBeLessThan(probeTimeoutMs(3));
  });

  it("never extrapolates past the benchmark, in either direction", () => {
    // Wider than anything measured gets the widest measured factor, not a
    // number invented by extending a line off the end of the data.
    expect(probeTimeoutMs(50)).toBe(probeTimeoutMs(5));
    expect(probeTimeoutMs(0)).toBe(probeTimeoutMs(1));
    expect(probeTimeoutMs(Number.NaN)).toBe(probeTimeoutMs(1));
  });

  it("still leaves every width's probe inside one step on its own", () => {
    for (const width of [1, 3, 5, 50]) {
      expect(fitsInStep(probeTimeoutMs(width))).toBe(true);
    }
  });
});
