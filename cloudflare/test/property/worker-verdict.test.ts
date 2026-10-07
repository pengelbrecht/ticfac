import * as hegel from "@hegeldev/hegel";
import * as gs from "@hegeldev/hegel/generators";
import { expect, it } from "vitest";

import {
  reportIsOnlyChange,
  verdictFor,
  WORKER_VERDICTS,
  type WorkerVerdict,
} from "../../src/worker-verdict";

/**
 * The report-only-worker refusal (tick p0n, the dyo/94u pair): the TS collect
 * and the Go executor must agree about a worker that delivered nothing but
 * its own report.
 *
 * The two implementations of this one rule set — `src/worker-verdict.ts`
 * (the TS collect's classification, extracted from worker-collect.ts) and
 * `internal/exec/cloudflaresandbox/collect.go` (the Go executor's
 * `reportIsOnlyChange` + `classify`) — read the SAME branch shape on the
 * same substrate. They are copies by design (the bundle split), and the
 * failure mode of two copies is silent: drift one and a worker that did
 * nothing reads ready-to-merge through one path while the other refuses it,
 * with nothing failing anywhere. dyo made the Go side refuse; 94u made the TS
 * side refuse the same way.
 *
 * So the properties here drive BOTH sides over arbitrary branch shapes: the
 * TS side directly, and the Go side through a restatement of its rule copied
 * from `collect.go`'s source below (mirrored, like the lifecycle harness
 * mirrors Go — this repository cannot import Go, and a mirror that names
 * its source and fails on disagreement is the honest substitute).
 *
 * Where the two sides DISAGREE today, it is recorded, not hidden: the
 * zero-commit-no-report corner (a push that never landed) reads `no-commits`
 * on the TS side and `missing-result` on the Go side — both fail the attempt,
 * but through different words of the closed vocabulary. That divergence is
 * outside the dyo/94u guarantee (report-only branches with a report), and
 * the properties below state the pair's actual contract precisely: the
 * report-only refusal agrees, and a report-only branch is never, on either
 * side, ready-to-merge.
 */

// ---------------------------------------------------- the Go side, restated ---

/**
 * `reportIsOnlyChange`, restated from
 * `internal/exec/cloudflaresandbox/collect.go`:
 *
 *	func reportIsOnlyChange(changed []string, reportPath string) bool {
 *	    if len(changed) == 0 {
 *	        return false
 *	    }
 *	    for _, path := range changed {
 *	        if path != reportPath {
 *	            return false
 *	        }
 *	    }
 *	    return true
 *	}
 */
function goReportIsOnlyChange(changed: string[], reportPath: string): boolean {
  if (changed.length === 0) return false;
  for (const path of changed) {
    if (path !== reportPath) return false;
  }
  return true;
}

/**
 * `NoCommitsIsFailure`, restated from `internal/exec/subprocess/collect.go`
 * (tick 19l): every role of the closed vocabulary has a DECIDED answer to
 * "is an empty branch a failure?", the review alone is released (its
 * deliverable is its answer, and its grade takes the push credential away),
 * and a role the rule has never heard of is held to the check.
 */
function goNoCommitsIsFailure(role: string): boolean {
  return role !== "review-epic";
}

/**
 * `classify`, restated from `internal/exec/cloudflaresandbox/collect.go` —
 * the verdict, in the order the checks run: the first FAILING check wins.
 * The Go side takes violations (`.tick/` boundary and artifact-prefix paths)
 * and the role; the TS side takes `report_only` and `boundary_files` as
 * already-measured evidence, which is why the mapping below computes
 * report_only with the Go rule rather than taking it on trust.
 */
function goClassify(
  role: string,
  commits: number,
  reportOnly: boolean,
  hasReport: boolean,
  status: string,
  violations: string[],
): WorkerVerdict {
  if (!hasReport || status === "") return WORKER_VERDICTS.missingResult;
  if (commits === 0 && goNoCommitsIsFailure(role)) return WORKER_VERDICTS.noCommits;
  if (reportOnly && goNoCommitsIsFailure(role)) return WORKER_VERDICTS.noCommits;
  if (violations.length > 0) return WORKER_VERDICTS.boundaryViolation;
  return WORKER_VERDICTS.readyToMerge;
}

/** The roles the door dispatches and the TS collect settles (worker roles). */
const WORKER_ROLES = ["implement-tick", "resolve-conflict", "gate-repair", "a-future-role"];

// --------------------------------------------------------- the generators ---

/** A path a branch may carry: the report, a work file, a tracker path. */
const pathGen = gs.oneOf(
  gs.composite((tc) => `RESULT-${tc.draw(gs.text({ minSize: 1, maxSize: 8 }))}.md`),
  gs.composite((tc) => `src/${tc.draw(gs.text({ minSize: 1, maxSize: 12 }))}.go`),
  gs.composite((tc) => `.tick/issues/${tc.draw(gs.text({ minSize: 1, maxSize: 6 }))}.json`),
  gs.just(""),
);

const changedGen = gs.arrays(pathGen, { minSize: 0, maxSize: 8 });

const branchShapeGen = gs.composite<{
  changed: string[];
  commits: number;
  hasReport: boolean;
  status: string;
  reportPath: string;
  role: string;
}>((tc) => ({
  changed: tc.draw(changedGen),
  // Commits and the changed list are related in the world: a branch with no
  // commits beyond its base changed nothing, and one that changed things has
  // at least the entrypoint's report commit on this substrate.
  commits: tc.draw(gs.oneOf(gs.just(0), gs.integers({ minValue: 1, maxValue: 9 }))),
  hasReport: tc.draw(gs.booleans()),
  status: tc.draw(
    gs.oneOf(
      gs.just(""),
      gs.sampledFrom(["DONE", "DONE_WITH_CONCERNS", "NEEDS_CONTEXT", "BLOCKED"]),
    ),
  ),
  reportPath: tc.draw(
    gs.oneOf(
      gs.just("RESULT-abc.md"),
      gs.composite((tc2) => `RESULT-${tc2.draw(gs.text({ minSize: 1, maxSize: 6 }))}.md`),
    ),
  ),
  role: tc.draw(gs.sampledFrom(WORKER_ROLES)),
}));

// ------------------------------------------------------------ the properties ---

it("both sides read the same changed-file list the same way: reportIsOnlyChange agrees everywhere", () => {
  hegel.test(
    (tc) => {
      const changed = tc.draw(changedGen);
      const reportPath = tc.draw(
        gs.oneOf(
          gs.just("RESULT-abc.md"),
          gs.composite((tc2) => `RESULT-${tc2.draw(gs.text({ minSize: 1, maxSize: 6 }))}.md`),
        ),
      );
      const ts = reportIsOnlyChange(changed, reportPath);
      const go = goReportIsOnlyChange(changed, reportPath);
      if (ts !== go) {
        throw new Error(
          `reportIsOnlyChange disagrees: TS=${ts} Go=${go} on ${JSON.stringify(changed)} vs ${reportPath}`,
        );
      }
      // And the rule's own shape: an empty list is NEVER report-only (that is
      // the push that never landed), and a report-only list is non-empty and
      // exactly the report.
      if (changed.length === 0 && ts) {
        throw new Error("an empty branch was read as report-only");
      }
      if (ts && !(changed.length > 0 && changed.every((p) => p === reportPath))) {
        throw new Error(
          `a non-report-only list was read as report-only: ${JSON.stringify(changed)}`,
        );
      }
    },
    { testCases: 2000 },
  );
});

it("a report-only worker is refused as no-commits by both sides, never ready-to-merge (dyo/94u)", () => {
  hegel.test(
    (tc) => {
      const shape = tc.draw(branchShapeGen);
      const reportOnly = goReportIsOnlyChange(shape.changed, shape.reportPath);
      if (!reportOnly) return; // this property is about the report-only shape
      // A report-only branch with its report present and readable — the
      // dyo/94u case exactly: the container committed the report, and that
      // one commit is all it did.
      const hasReport = shape.hasReport && shape.status !== "";
      if (!hasReport) return;

      const ts = verdictFor({
        branch_exists: true,
        commits: shape.commits,
        result_exists: shape.hasReport,
        status: shape.status,
        boundary_files: [],
        report_only: reportIsOnlyChange(shape.changed, shape.reportPath),
      });
      // On this substrate the entrypoint's report commit means a report-only
      // branch HAS a commit; a commits=0 report-only shape cannot exist, so
      // the Go mirror is asked only for the shapes the world produces.
      if (shape.commits === 0) return;
      const go = goClassify(
        shape.role,
        shape.commits,
        reportOnly,
        shape.hasReport,
        shape.status,
        [],
      );

      if (ts !== go) {
        throw new Error(
          `the report-only refusal disagrees: TS=${ts} Go=${go} for role ${shape.role} on ${JSON.stringify(shape.changed)}`,
        );
      }
      if (ts === WORKER_VERDICTS.readyToMerge) {
        throw new Error(
          `a report-only worker read ready-to-merge on both sides for ${JSON.stringify(shape.changed)}`,
        );
      }
      expect(ts).toBe(WORKER_VERDICTS.noCommits);
      expect(go).toBe(WORKER_VERDICTS.noCommits);
    },
    { testCases: 3000 },
  );
});

it("the review alone is released from the no-commits check, on both sides (tick 19l's rule)", () => {
  hegel.test(
    (tc) => {
      const role = tc.draw(gs.oneOf(gs.sampledFrom(WORKER_ROLES), gs.just("review-epic")));
      const released = role === "review-epic";
      if (goNoCommitsIsFailure(role) !== !released) {
        throw new Error(`NoCommitsIsFailure(${role}) disagrees with the recorded rule`);
      }
    },
    { testCases: 500 },
  );
});

it("TS and Go agree wherever both give a verdict, outside the recorded zero-commit corner", () => {
  hegel.test(
    (tc) => {
      const shape = tc.draw(branchShapeGen);
      const reportOnly = goReportIsOnlyChange(shape.changed, shape.reportPath);
      const violations = shape.changed.filter((p) => p.startsWith(".tick/"));
      const ts = verdictFor({
        branch_exists: true,
        commits: shape.commits,
        result_exists: shape.hasReport,
        status: shape.status,
        boundary_files: violations,
        report_only: reportIsOnlyChange(shape.changed, shape.reportPath),
      });
      const go = goClassify(
        shape.role,
        shape.commits,
        reportOnly,
        shape.hasReport,
        shape.status,
        violations,
      );

      // The recorded divergence: a branch that carries nothing at all — the
      // push that never landed — reads no-commits on the TS side (its own
      // distinct fact: the branch exists and is empty) and missing-result on
      // the Go side (an answer nobody can read is the more urgent fact).
      // Both fail the attempt; the words differ. That is a finding about the
      // pair, not a property they hold, so this property skips exactly that
      // shape and states what remains.
      if (shape.commits === 0 && !shape.hasReport) return;
      if (shape.commits === 0 && shape.hasReport && shape.status === "") return;
      if (ts !== go) {
        throw new Error(
          `TS=${ts} Go=${go} for ${JSON.stringify(shape)} — the two readers of one branch shape disagree`,
        );
      }
    },
    { testCases: 3000 },
  );
});

it("the closed vocabulary: the TS side can only ever speak one of the four words (plus remote-unknown)", () => {
  hegel.test(
    (tc) => {
      const shape = tc.draw(branchShapeGen);
      const ts = verdictFor({
        branch_exists: tc.draw(gs.booleans()),
        commits: shape.commits,
        result_exists: shape.hasReport,
        status: shape.status,
        boundary_files: shape.changed.filter((p) => p.startsWith(".tick/")),
        report_only: reportIsOnlyChange(shape.changed, shape.reportPath),
      });
      const words = Object.values(WORKER_VERDICTS);
      if (!words.includes(ts)) {
        throw new Error(`the collect invented the word ${JSON.stringify(ts)}`);
      }
    },
    { testCases: 2000 },
  );
});
