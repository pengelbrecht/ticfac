import { describe, expect, it } from "vitest";

import { jobKindOfSandboxName, runIDOfSandboxName } from "../src/run-substrate";
import { proofAttempt, proofTick } from "../src/staging-agent";

// The staging agent Worker names each proof attempt's run and container (tick
// jhp). Since tick hxd the WorkerAgent finds its container door by reading
// the run's substrate row, keyed on the run id PARSED FROM THE CONTAINER NAME
// — so a proof whose names do not round-trip is routed to the sdk0 door the
// staging Worker does not bind, and every start throws "binds no container
// namespace".
describe("the staging agent's proof attempt names", () => {
  for (const name of ["kill-host-mid-tool", "workspace-lost-mid-turn", "deploy-mid-run", "proof"]) {
    it(`${name}: the container name parses back to the run whose substrate row was written`, () => {
      const attempt = proofAttempt(name);
      expect(runIDOfSandboxName(attempt.sandbox)).toBe(attempt.runId);
      expect(jobKindOfSandboxName(attempt.sandbox)).toBe("implement");
    });
  }

  it("a proof's own tick names its container and keeps the round trip (tick cni)", () => {
    const attempt = proofAttempt("proof-cni-x", proofTick("cni"));
    expect(attempt.sandbox).toBe("run_proof_proof_cni_x-cni-1");
    expect(runIDOfSandboxName(attempt.sandbox)).toBe(attempt.runId);
    expect(jobKindOfSandboxName(attempt.sandbox)).toBe("implement");
  });

  it("a tick the request may not name falls back to xd3", () => {
    for (const given of [undefined, null, "", "Has-Dash", "a-b", 7, "x".repeat(17)]) {
      expect(proofTick(given)).toBe("xd3");
    }
    expect(proofAttempt("proof").sandbox).toBe(proofAttempt("proof", proofTick(undefined)).sandbox);
  });

  it("two proof names never share a run or a container", () => {
    const a = proofAttempt("deploy-mid-run");
    const b = proofAttempt("deploy-mid-run-2");
    expect(a.runId).not.toBe(b.runId);
    expect(a.sandbox).not.toBe(b.sandbox);
  });
});
