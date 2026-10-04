import { describe, expect, it } from "vitest";

import contract from "../../contracts/status-model.json";
import { classifyStatusDoc, type StatusDoc } from "../src/status";

/**
 * The listing's classifier (tick c65): a run that is NOT alive must never
 * read "done" off its lifecycle phase alone. The phase is a checkpoint a
 * run UPDATES as it goes, not a terminal record — a run that dies
 * mid-waves still says phase "waves" — so the terminal read is the one the
 * bare `ticfac` overview performs (the classifier is its port, tick 2qz):
 * the phase first, then the liveness answer's own state, which for an ended
 * run IS its own durable terminal word — the cloud record's
 * completed/stopped/failed vocabulary (the same three words the Go model's
 * `livenessNamesAnEnd` names), never the local probe's process talk
 * (alive/dead/not_running), which names no end at all.
 *
 * The contract's own `dashboard_stopped` golden is the fixture that found
 * the defect: a run that died mid-waves, whose card chip read "done" while
 * its own headline verdict said "stopped". The Go half of the
 * cross-renderer pin is TestTheOverviewClassesAStoppedRunByItsOwnDurableWord
 * in internal/cli/overview_test.go — the same golden, the same answer.
 */

const goldens = (contract as { golden: Record<string, Record<string, unknown>> }).golden;

/** One minimal `ticfac.status.v1` doc, overriden by the fields a case pins. */
function doc(over: Record<string, unknown> = {}): StatusDoc {
  return {
    schema_version: 1,
    run_id: "epic-hn6",
    epic_id: "hn6",
    host: "local",
    generated_at: "2026-10-04T20:00:00Z",
    liveness: { alive: false, state: "not_running", reason: "" },
    lifecycle: { phase: "waves" },
    attention: [],
    ...over,
  } as unknown as StatusDoc;
}

describe("the listing's classifier never reads a dead mid-run run as done (tick c65)", () => {
  it("classes the contract's stopped golden as cancelled — the chip agrees with its own headline", () => {
    // The golden: alive false, phase still "waves" (it died mid-run), and
    // the liveness answer's own state "stopped" — the durable word. The
    // overview reads cancelled for this model; so must the listing.
    const classified = classifyStatusDoc(goldens.dashboard_stopped as unknown as StatusDoc);
    expect(classified.state).toBe("cancelled");
    expect(classified.state).not.toBe("done");
  });

  it("reads a mid-run death by the liveness state's own durable word, never off the phase alone", () => {
    // Three dead-mid-waves runs, one per word of the durable ending
    // vocabulary, all with the phase still naming no end.
    const stopped = classifyStatusDoc(
      doc({ liveness: { alive: false, state: "stopped", reason: "stopped by the operator" } }),
    );
    expect(stopped.state).toBe("cancelled");
    expect(stopped.reason).toBe("stopped by the operator");

    const failed = classifyStatusDoc(
      doc({ liveness: { alive: false, state: "failed", reason: "the container was evicted" } }),
    );
    expect(failed.state).toBe("failed");
    expect(failed.reason).toBe("the container was evicted");
    // The resume is named by the host the run lives on (tick tt6).
    expect(failed.clear_with).toBe("ticfac run-epic hn6");

    const completed = classifyStatusDoc(
      doc({ liveness: { alive: false, state: "completed", reason: "the close-out finished" } }),
    );
    expect(completed.state).toBe("done");

    // A CLOUD run's failure carries the cloud resume: a new submission to
    // its factory, never the local foreground restart.
    const cloudFailed = classifyStatusDoc(
      doc({ host: "cloud", liveness: { alive: false, state: "failed", reason: "" } }),
    );
    expect(cloudFailed.state).toBe("failed");
    expect(cloudFailed.clear_with).toBe("ticfac run hn6 --cloud");
  });

  it("keeps a run's own phase where the liveness state names no end — the local probe's words", () => {
    // The local probe's states (dead, not_running, unknown) are process
    // talk, not endings: a local run that ended says so in its phase
    // (which its own records wrote) or in the model's attention (the
    // dead-run wait the builder adds), never in the probe's word.
    const done = classifyStatusDoc(
      doc({
        lifecycle: { phase: "done" },
        liveness: { alive: false, state: "not_running", reason: "released" },
      }),
    );
    expect(done.state).toBe("done");

    const cancelled = classifyStatusDoc(
      doc({
        lifecycle: { phase: "cancelled" },
        liveness: { alive: false, state: "dead", reason: "" },
      }),
    );
    expect(cancelled.state).toBe("cancelled");

    const failed = classifyStatusDoc(
      doc({
        lifecycle: { phase: "failed" },
        liveness: { alive: false, state: "not_running", reason: "the gate refused it" },
      }),
    );
    expect(failed.state).toBe("failed");
    expect(failed.clear_with).toBe("ticfac run-epic hn6");
  });
});
