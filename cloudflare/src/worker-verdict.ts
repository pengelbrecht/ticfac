/**
 * The worker collect's VERDICT CLASSIFICATION — the pure core of the TS
 * collect's report-only refusal, extracted as its own module (tick p0n).
 *
 * `worker-collect.ts` reads the durable layer through GitHub's API and builds
 * a `WorkerReport`; what that report MEANS is decided here, by three pure
 * functions over evidence fields. The decision layer is where the dyo/94u
 * seam lives — the guarantee that the TS collect and the Go executor
 * (`internal/exec/cloudflaresandbox/collect.go`, `classify`) refuse a
 * report-only worker with the SAME verdict — and a seam that two
 * implementations of, with nothing driving it, drifts until a cloud run reads
 * one way locally and the other way in the reconciler. The property tests
 * (`test/property/worker-verdict.test.ts`) drive these functions over
 * arbitrary branch shapes against a restatement of the Go rule, so the two
 * sides cannot silently disagree.
 *
 * No imports, like `run-watch.ts` and `sandbox-dispatch-validation.ts`: the
 * plain-Node vitest leg that runs the Hegel tests loads this module, and a
 * decision layer that cannot be imported without workerd is a decision layer
 * entangled with the platform it decides about.
 */

/**
 * The closed verdict vocabulary, shared with the local and herd collectors —
 * `internal/herd/collect` and `internal/cloud/collect` also implement it, and
 * the failure mode of three copies is silent: re-spell one and a cloud run
 * and a herd run disagree about what happened to the same tick with nothing
 * failing anywhere.
 *
 * The four verdicts `internal/herd/collect` defines, plus `unknown` — a fact
 * only a REMOTE read can state: a GitHub read can fail (rate limit, outage, a
 * bad token), and an unreadable remote must not be reported as a failing
 * verdict any more than `progress.ts` may report a run that could not be
 * checked as one that did nothing. (The collect that reads through GitHub —
 * `worker-collect.ts` — is where that fact is earned; this module only names
 * the word for it.)
 *
 * Cross-language drift is what the vocabulary's own checks are for:
 * the failure mode of three copies is silent: re-spell one and a cloud run
 * and a herd run disagree about what happened to the same tick with nothing
 * failing anywhere. Two things stop that in this bundle.
 * `satisfies Record<string, WorkerVerdict>` makes a re-spelling a TYPE error —
 * in either direction, since editing the union below orphans the value beside
 * it and editing a value leaves the union unsatisfied. And
 * `test/collect-vocabulary.test.ts` checks these against
 * `contracts/collect-vocabulary.json`, the file the two Go implementations
 * read.
 *
 * `unknown` is the one entry with no twin in `internal/herd/collect`: only an
 * implementation reading a REMOTE can fail to read the evidence at all. The
 * contract records it under `remote_only_verdicts` for that reason.
 */
export type WorkerVerdict =
  | "ready-to-merge"
  | "no-commits"
  | "missing-result"
  | "boundary-violation"
  | "unknown";

/**
 * The verdict strings as VALUES, so the vocabulary has one spelling per name
 * in this module rather than a literal at each `return` (tick hn1).
 */
export const WORKER_VERDICTS = {
  readyToMerge: "ready-to-merge",
  noCommits: "no-commits",
  missingResult: "missing-result",
  boundaryViolation: "boundary-violation",
  unknown: "unknown",
} as const satisfies Record<string, WorkerVerdict>;

/**
 * The evidence a branch read produced — the verdict's INPUT, every field of
 * it a fact the durable layer stated (or the shape the TS collect's
 * `WorkerReport` carries for the same question).
 */
export type WorkerVerdictInput = {
  /** Whether the branch exists on origin at all. */
  branch_exists: boolean;
  /** Commits on the branch beyond its base. */
  commits: number;
  /** Whether the report file exists on the branch. */
  result_exists: boolean;
  /** The report's parsed STATUS line, "" when unreadable or unparseable. */
  status: string;
  /** `.tick/` paths the branch touches relative to the merge base. */
  boundary_files: string[];
  /**
   * Whether every path the branch changed beyond its base is the report
   * itself — the shape a worker that did no work leaves on this substrate
   * ({@link reportIsOnlyChange}).
   */
  report_only: boolean;
};

/**
 * Whether every path the branch changed beyond its base is the report the
 * container's own entrypoint commits — the shape a worker that did no work
 * leaves on this substrate, where the subprocess executor's empty branch is
 * impossible by construction. Ported from the Go executor's
 * `reportIsOnlyChange` (tick dyo) so the two reads of the same branch cannot
 * disagree about it. Nothing changed is NOT this shape: that is the push that
 * never landed or the honest empty branch, and it keeps its own verdict and
 * message.
 *
 * Go (internal/exec/cloudflaresandbox/collect.go):
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
export function reportIsOnlyChange(changed: string[], reportPath: string): boolean {
  if (changed.length === 0) return false;
  return changed.every((path) => path === reportPath);
}

/**
 * The verdict the evidence supports, in the order the checks run — the order
 * IS the contract, because it decides which failure a branch carrying
 * several shapes is read as.
 *
 * The report-only branch (tick 94u, mirroring the Go executor's classify):
 * the closed vocabulary's own word for a worker that delivered nothing is
 * `no-commits`, never a fifth word. It is checked after missing-result for
 * the same reason the Go side checks it there — an answer nobody can read is
 * the more urgent fact.
 */
export function verdictFor(r: WorkerVerdictInput): WorkerVerdict {
  if (!r.branch_exists || r.commits === 0) return WORKER_VERDICTS.noCommits;
  if (!r.result_exists || r.status === "") return WORKER_VERDICTS.missingResult;
  if (r.report_only) return WORKER_VERDICTS.noCommits;
  if (r.boundary_files.length > 0) return WORKER_VERDICTS.boundaryViolation;
  return WORKER_VERDICTS.readyToMerge;
}
