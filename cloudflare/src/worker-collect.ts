/**
 * Per-tick worker collect — the durable-layer-only verdict (tick 0ds).
 *
 * A worker sandbox clones at the epic base, branches `tick/<epic-id>/<tick-id>`,
 * implements, pushes the branch plus `RESULT-<tick-id>.md`, and exits. This
 * module is what a caller checks afterward, and it reads *only* what survived
 * in git — commits, the result file, the `.tick/` boundary diff — never a
 * sandbox's terminal output (the collect rule herdr-runner.md states, applied
 * here verbatim per the tick's own description).
 *
 * It is a port of `internal/herd/collect/collect.go` onto the remote's own
 * history through GitHub's API, because a worker sandbox pushes and exits —
 * there is no local worktree left to read the way `tk herd collect` reads
 * one. The three checks and their ordering are identical on purpose: a
 * container-per-tick substrate and a herdr-pane substrate should never
 * disagree about what "ready to merge" means for the same tick.
 *
 * The fourth check is this substrate's own (tick 94u, ported from the Go
 * executor's collect, tick dyo): the container's entrypoint commits the
 * report itself, in its own commit (image/worker.sh), so a worker that did
 * NOTHING still leaves one commit beyond the base, and counting commits
 * alone reads it as ready-to-merge. When the diff minus the report is empty
 * the verdict is `no-commits` — the same word the Go side refuses with —
 * with its own sentence, because "the branch is empty" is a lie about this
 * one. A herdr worker commits its own report and its collect needs no such
 * check; on this substrate the two reads of the same branch must agree, or
 * dyo's guarantee is only half the cloud.
 */

import { githubAuthorization } from "./github-app";
import type { Env } from "./index";
import { workerBootStoppedBranch, workerBootStoppedFile, workerExitClass } from "./worker-boot";

// ------------------------------------------------------------- the verdict ---

/**
 * The four verdicts `internal/herd/collect` defines, plus `unknown` — a fact
 * this module has that a local git read does not: a GitHub read can fail
 * (rate limit, outage, a bad token), and an unreadable remote must not be
 * reported as a failing verdict any more than `progress.ts` may report a run
 * that could not be checked as one that did nothing.
 */
export type WorkerVerdict =
  | "ready-to-merge"
  | "no-commits"
  | "missing-result"
  | "boundary-violation"
  | "unknown";

/**
 * The verdict strings as VALUES, so the vocabulary has one spelling per name in
 * this module rather than a literal at each `return` (tick hn1).
 *
 * This is the third implementation of a rule set `internal/herd/collect` and
 * `internal/cloud/collect` also implement, and the failure mode of three copies
 * is silent: re-spell one and a cloud run and a herd run disagree about what
 * happened to the same tick with nothing failing anywhere. Two things stop that
 * here. `satisfies Record<string, WorkerVerdict>` makes a re-spelling a TYPE
 * error — in either direction, since editing the union above orphans the value
 * below and editing a value below leaves the union unsatisfied. And
 * `test/collect-vocabulary.test.ts` checks these against
 * `contracts/collect-vocabulary.json`, the file the two Go implementations read.
 *
 * `unknown` is the one entry with no twin in `internal/herd/collect`: only an
 * implementation reading a REMOTE can fail to read the evidence at all. The
 * contract records it under `remote_only_verdicts` for that reason.
 */
export const WORKER_VERDICTS = {
  readyToMerge: "ready-to-merge",
  noCommits: "no-commits",
  missingResult: "missing-result",
  boundaryViolation: "boundary-violation",
  unknown: "unknown",
} as const satisfies Record<string, WorkerVerdict>;

/**
 * The line `image/worker.sh` prepends to `RESULT-<tick>.md` when its
 * boundary guard caught the agent trying to write tracker state (tick dxk).
 *
 * The guard exists because a real container ignored the prompt's Boundaries
 * section — run_215b7cbff9dd405c80d738be45cccde5, tick 5jo, which ran
 * `tk close` and committed the result. The container now refuses rather than
 * asks, which means `boundary_files` below is EMPTY for exactly the runs a
 * human most needs to hear about: the violation was prevented, so the branch
 * is clean and the verdict is ready-to-merge. This marker is what carries it
 * anyway. It is pinned in the shared worker-boot contract beside the probe
 * marker, for the same reason that one is: two halves matching on a substring
 * drift the moment only one of them is edited.
 */
export const BOUNDARY_REPORT_MARKER = "BOUNDARY VIOLATION ATTEMPTED";

export const STATUS_DONE = "DONE";
export const STATUS_DONE_WITH_CONCERNS = "DONE_WITH_CONCERNS";
export const STATUS_NEEDS_CONTEXT = "NEEDS_CONTEXT";
export const STATUS_BLOCKED = "BLOCKED";

/** What one tick's worker sandbox is being checked against. */
export type WorkerTask = {
  tick_id: string;
  /** `tick/<epic-id>/<tick-id>`, per the design this tick's description states. */
  branch: string;
  /** The epic base the branch is compared against — never a moving `main`. */
  base_sha: string;
  /**
   * The branch the container itself pushed (its landing branch), when the
   * branch read above is a ref the work was mirrored onto (the attempt's
   * write_ref). A container that stopped in its boot leaves its reason
   * beside THIS one (workerBootStoppedBranch). Defaults to `branch`.
   */
  landing_branch?: string;
  /**
   * The run the attempt belongs to: the container's fallback landing name is
   * `<landing branch>-<run>` when origin already had the recorded name from
   * another base (git-refs.ts), and its boot marker sits beside that one.
   */
  run_id?: string;
};

/** One worker's collect outcome. Every field is evidence, mirroring `collect.Report`. */
export type WorkerReport = {
  tick_id: string;
  branch: string;
  base_sha: string;
  verdict: WorkerVerdict;
  branch_exists: boolean;
  /** Commits on the branch beyond `base_sha`. */
  commits: number;
  result_path: string;
  result_exists: boolean;
  status: string;
  status_detail: string;
  status_line: string;
  /** `.tick/` paths the branch touches relative to the merge base. Non-empty is a violation. */
  boundary_files: string[];
  /**
   * Whether every path the branch changed beyond its base is the report
   * itself — the shape a worker that did no work leaves on this substrate,
   * where the container's entrypoint commits the report (image/worker.sh)
   * and so one commit always exists (tick 94u, the TS half of the Go
   * executor's own refusal, tick dyo). Like `boundary_attempted` this is
   * evidence the verdict is computed FROM; the verdict stays `no-commits`,
   * never a fifth word, because the closed vocabulary is the contract.
   */
  report_only: boolean;
  /**
   * Whether the worker's own report says its container CAUGHT the agent
   * crossing the boundary. Independent of `boundary_files`, and usually the
   * opposite of it: a caught attempt is a prevented one, so the branch is
   * clean. Undefined when the report could not be read.
   */
  boundary_attempted?: boolean;
  /**
   * What a container that stopped in its boot, before its harness, left
   * beside its landing branch (#176): its exit code and the boot's own stop
   * message. Read only when the branch carries nothing — no report, no
   * commit — and evidence like the fields above: it changes the detail,
   * never the verdict.
   */
  boot_stopped?: { exit_code: number; reason: string };
  detail: string;
};

export function resultFile(tickID: string): string {
  return `RESULT-${tickID}.md`;
}

/** Whether a worker's own status is a human escalation, independent of the verdict. */
export function needsHuman(report: WorkerReport): boolean {
  return report.status === STATUS_BLOCKED || report.status === STATUS_NEEDS_CONTEXT;
}

// ------------------------------------------------------------ status parse ---

/** The markdown a report line may be wrapped in — ported byte-for-byte from collect.go. */
const DECORATION = /^[ \t>\-*#`]+|[ \t>\-*#`]+$/g;

/**
 * Matches a report's status line. `DONE_WITH_CONCERNS` is first in the
 * alternation so it is never truncated to `DONE`.
 *
 * Exported so `test/collect-vocabulary.test.ts` can compare `.source` against
 * `contracts/collect-vocabulary.json`, which the two Go implementations compare
 * `regexp.String()` against. The pattern text — the alternation ORDER included
 * — is the contract, not just the four status words: get the order wrong and a
 * weakened regexp reads `DONE_WITH_CONCERNS` as `DONE`, which inverts the
 * verdict on the exact status a human most needs to see.
 */
export const STATUS_LINE =
  /^STATUS:[ \t]*(DONE_WITH_CONCERNS|DONE|NEEDS_CONTEXT|BLOCKED)\b[ \t]*(?:[-–—:][ \t]*)?(.*)$/;

/**
 * Finds the FINAL status line of a report and splits it into the status
 * word, the detail after it, and the raw line. Everything is empty when the
 * report carries no recognisable status. Ported from `collect.ParseStatus`.
 */
export function parseStatus(body: string): { status: string; detail: string; line: string } {
  let status = "";
  let detail = "";
  let line = "";
  for (const raw of body.split("\n")) {
    const trimmed = raw.replace(/\r$/, "").replace(DECORATION, "");
    const match = STATUS_LINE.exec(trimmed);
    if (match === null) continue;
    // Keep scanning: the contract is the *final* status line, and a report
    // may quote the template's four options above it.
    status = match[1];
    detail = (match[2] ?? "").replace(DECORATION, "").trim();
    line = trimmed;
  }
  return { status, detail, line };
}

// -------------------------------------------------------------- the seam ---

/**
 * The reader a caller collects a worker with — a test's fake, or GitHub.
 *
 * A seam for the same reason `RepoRefs` is one in `progress.ts`: the verdict
 * logic is what needs testing, and a rule exercisable only against a real
 * pushed branch is a rule nobody tests. It is also the structural proof this
 * module needs for its own headline claim — "collect reads only what
 * survived in git" — because a `WorkerCollector` never receives a sandbox
 * reference at all; it cannot read terminal output because it has nothing to
 * read it from.
 */
export interface WorkerCollector {
  collect(task: WorkerTask): Promise<WorkerReport>;
}

/**
 * The collector this deployment uses: a test's fake, or GitHub.
 *
 * Mirrors `repoConfig(env)` in repo-config.ts — the same reason applies: the
 * durable-layer-only verdict is what needs testing, and a rule exercisable
 * only against a real pushed branch is a rule nobody tests.
 */
export function workerCollector(env: Env, project: string): WorkerCollector {
  const injected = env.WORKER_COLLECTOR;
  return injected === undefined || injected === null
    ? githubWorkerCollector(env, project)
    : injected;
}

export const GITHUB_API_BASE_URL = "https://api.github.com";

async function githubHeaders(
  env: Env,
  project: string,
): Promise<{ ok: true; headers: Record<string, string> } | { ok: false; detail: string }> {
  try {
    return {
      ok: true,
      headers: {
        accept: "application/vnd.github+json",
        "user-agent": "ticks-factory",
        ...(await githubAuthorization(env, project)),
      },
    };
  } catch (error) {
    return { ok: false, detail: String((error as Error).message ?? error) };
  }
}

function apiBase(env: Env): string {
  return (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
}

type CompareResult =
  | { ok: true; ahead_by: number; changed: string[]; boundary_files: string[] }
  | { ok: false; missing: true }
  | { ok: false; missing: false; detail: string };

/**
 * GitHub's three-dot compare, `base...branch` — the same comparison
 * `collect.go`'s `git diff base...ref` makes, which is why `ahead_by` and the
 * changed-file list agree with the local-git verdict for the same tick.
 *
 * GitHub's `files` array is capped at 300 entries for a very large diff; a
 * single tick's change realistically never approaches that, and this is
 * noted here rather than silently assumed — see the module's test file for
 * the same caveat spelled out for whoever revisits this. The report-only
 * check below reads the same list, so a diff big enough to cap it is a diff
 * that is not report-only — the safe side to err on.
 */
async function compareBranch(
  env: Env,
  project: string,
  base: string,
  branch: string,
): Promise<CompareResult> {
  const url =
    `${apiBase(env)}/repos/${project}/compare/` +
    `${encodeURIComponent(base)}...${encodeURIComponent(branch)}`;
  const auth = await githubHeaders(env, project);
  if (!auth.ok) return { ok: false, missing: false, detail: auth.detail };
  const response = await fetch(url, { headers: auth.headers });
  if (response.status === 404) return { ok: false, missing: true };
  if (!response.ok) {
    return {
      ok: false,
      missing: false,
      detail: `GitHub answered HTTP ${response.status} comparing ${base}...${branch}`,
    };
  }
  const body = (await response.json()) as {
    ahead_by?: number;
    files?: { filename?: string }[];
  };
  const changed = (body.files ?? [])
    .map((f) => f.filename)
    .filter((f): f is string => typeof f === "string")
    .sort();
  const boundary_files = changed.filter((f) => f === ".tick" || f.startsWith(".tick/"));
  return {
    ok: true,
    ahead_by: typeof body.ahead_by === "number" ? body.ahead_by : 0,
    changed,
    boundary_files,
  };
}

type ContentsResult =
  | { ok: true; text: string }
  | { ok: false; missing: true }
  | { ok: false; missing: false; detail: string };

/** GitHub's contents API, decoded. */
async function readFileAt(
  env: Env,
  project: string,
  ref: string,
  path: string,
): Promise<ContentsResult> {
  const url = `${apiBase(env)}/repos/${project}/contents/${path}?ref=${encodeURIComponent(ref)}`;
  const auth = await githubHeaders(env, project);
  if (!auth.ok) return { ok: false, missing: false, detail: auth.detail };
  const response = await fetch(url, { headers: auth.headers });
  if (response.status === 404) return { ok: false, missing: true };
  if (!response.ok) {
    return {
      ok: false,
      missing: false,
      detail: `GitHub answered HTTP ${response.status} reading ${path}@${ref}`,
    };
  }
  const body = (await response.json()) as { content?: string; encoding?: string };
  if (typeof body.content !== "string" || body.encoding !== "base64") {
    return {
      ok: false,
      missing: false,
      detail: `GitHub returned an unreadable contents response for ${path}@${ref}`,
    };
  }
  const binary = atob(body.content.replace(/\n/g, ""));
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
  return { ok: true, text: new TextDecoder().decode(bytes) };
}

/** The deployment's collector: GitHub's compare and contents APIs. */
export function githubWorkerCollector(env: Env, project: string): WorkerCollector {
  return { collect: (task) => collectFromGithub(env, project, task) };
}

/**
 * One tick's verdict, read entirely from GitHub — never from the sandbox
 * that did the work.
 *
 * Order matches `collect.go`'s `verdictFor`: no-commits, then missing-result,
 * then boundary-violation, then ready-to-merge. The result file is read even
 * when there are no commits — a worker that reported BLOCKED without
 * committing says so here, which is the most useful thing an operator can be
 * told (the same reasoning `Collect` in Go gives).
 */
export async function collectFromGithub(
  env: Env,
  project: string,
  task: WorkerTask,
): Promise<WorkerReport> {
  const report: WorkerReport = {
    tick_id: task.tick_id,
    branch: task.branch,
    base_sha: task.base_sha,
    verdict: WORKER_VERDICTS.unknown,
    branch_exists: false,
    commits: 0,
    result_path: resultFile(task.tick_id),
    result_exists: false,
    status: "",
    status_detail: "",
    status_line: "",
    boundary_files: [],
    // Not yet computed: the report-only fact comes from the compare's
    // changed-file list, and every path that returns before that read says
    // false rather than undefined — the same always-an-answer rule the
    // other evidence fields hold.
    report_only: false,
    detail: "",
  };

  const compare = await compareBranch(env, project, task.base_sha, task.branch);
  if (!compare.ok) {
    if (compare.missing) {
      report.verdict = WORKER_VERDICTS.noCommits;
      report.detail = `${task.branch} does not exist on origin (or ${task.base_sha} is unresolvable)`;
      return withBootStopped(env, project, task, report);
    }
    report.detail = compare.detail;
    return report; // unknown: the remote could not be read
  }
  report.branch_exists = true;
  report.commits = compare.ahead_by;
  report.boundary_files = compare.boundary_files;
  // The did-nothing shape in this substrate's own terms (tick 94u): the
  // entrypoint's report commit is the only change the branch carries. An
  // empty `changed` list is NOT it — that is the push that never landed or
  // the honest empty branch, which keep their own verdict and sentence.
  report.report_only = reportIsOnlyChange(compare.changed, report.result_path);

  const contents = await readFileAt(env, project, task.branch, report.result_path);
  if (contents.ok) {
    report.result_exists = true;
    report.boundary_attempted = contents.text.includes(BOUNDARY_REPORT_MARKER);
    const parsed = parseStatus(contents.text);
    report.status = parsed.status;
    report.status_detail = parsed.detail;
    report.status_line = parsed.line;
  } else if (!contents.missing) {
    report.verdict = WORKER_VERDICTS.unknown;
    report.detail = contents.detail;
    return report;
  }

  report.verdict = verdictFor(report);
  report.detail = detailFor(report);
  if (
    report.verdict === WORKER_VERDICTS.noCommits &&
    !report.report_only &&
    !report.result_exists
  ) {
    return withBootStopped(env, project, task, report);
  }
  return report;
}

/**
 * The reason a container that never reached its harness left beside its
 * landing branch (#176), read the way the Go collect reads it
 * (internal/exec/cloudflaresandbox/boot_stopped.go `bootStopped`): the
 * landing branch's marker, then the per-run fallback's. The sentence is the Go
 * collect's own, so a run reads the same line from either side. The verdict
 * stays what the branch made it; a marker that cannot be read or parsed
 * leaves the report as it was.
 */
async function withBootStopped(
  env: Env,
  project: string,
  task: WorkerTask,
  report: WorkerReport,
): Promise<WorkerReport> {
  const landing = task.landing_branch ?? task.branch;
  const branches = [landing];
  if (task.run_id !== undefined && task.run_id !== "") branches.push(`${landing}-${task.run_id}`);
  for (const branch of branches) {
    const marker = await readFileAt(
      env,
      project,
      workerBootStoppedBranch(branch),
      workerBootStoppedFile(task.tick_id),
    );
    if (!marker.ok) continue;
    const stopped = parseBootStopped(marker.text);
    if (stopped === null) continue;
    report.boot_stopped = stopped;
    const cls = workerExitClass(stopped.exit_code);
    report.detail =
      `the container's boot stopped before its harness started (exit ${stopped.exit_code}` +
      `${cls === "" ? "" : `: ${cls}`}): ${stopped.reason}. ` +
      `Nothing reached ${landing}; the reason is on ${workerBootStoppedBranch(landing)}`;
    return report;
  }
  return report;
}

/**
 * The boot-stopped marker's `exit:` and `reason:` lines, or null when either
 * is missing or the code is not a number. Ported from the Go collect's
 * `parseBootStopped`: the first of each line wins.
 */
export function parseBootStopped(body: string): { exit_code: number; reason: string } | null {
  let code = -1;
  let reason = "";
  for (const line of body.split("\n")) {
    if (line.startsWith("exit: ") && code < 0) {
      const value = line.slice("exit: ".length).trim();
      if (!/^-?\d+$/.test(value)) return null;
      code = Number(value);
    }
    if (line.startsWith("reason: ") && reason === "") {
      reason = line.slice("reason: ".length).trim();
    }
  }
  if (code < 0 || reason === "") return null;
  return { exit_code: code, reason };
}

/**
 * Whether every path the branch changed beyond its base is the report the
 * container's own entrypoint commits — the shape a worker that did no work
 * leaves on this substrate, where the subprocess executor's empty branch is
 * impossible by construction. Ported from the Go executor's
 * `reportIsOnlyChange` (tick dyo) so the two reads of the same branch cannot
 * disagree about it. Nothing changed is NOT this shape: that is the push
 * that never landed or the honest empty branch, and it keeps its own verdict
 * and message.
 */
function reportIsOnlyChange(changed: string[], reportPath: string): boolean {
  if (changed.length === 0) return false;
  return changed.every((path) => path === reportPath);
}

function verdictFor(r: WorkerReport): WorkerVerdict {
  if (!r.branch_exists || r.commits === 0) return WORKER_VERDICTS.noCommits;
  if (!r.result_exists || r.status === "") return WORKER_VERDICTS.missingResult;
  // The report-only branch (tick 94u, mirroring the Go executor's classify):
  // the closed vocabulary's own word for a worker that delivered nothing is
  // `no-commits`, never a fifth word. It is checked after missing-result for
  // the same reason the Go side checks it there — an answer nobody can read
  // is the more urgent fact.
  if (r.report_only) return WORKER_VERDICTS.noCommits;
  if (r.boundary_files.length > 0) return WORKER_VERDICTS.boundaryViolation;
  return WORKER_VERDICTS.readyToMerge;
}

function detailFor(r: WorkerReport): string {
  switch (r.verdict) {
    case WORKER_VERDICTS.noCommits:
      if (r.report_only) {
        return (
          `the only commit ${r.branch} carries beyond its base (${r.base_sha.slice(0, 8)}) is ` +
          `the report at ${r.result_path}: the worker committed no work, and a report is not a deliverable`
        );
      }
      return r.branch_exists
        ? `${r.branch} has no commits beyond ${r.base_sha.slice(0, 8)}`
        : `${r.branch} does not exist on origin`;
    case WORKER_VERDICTS.missingResult:
      return r.result_exists
        ? `${r.result_path} on ${r.branch} has no recognisable STATUS: line`
        : `${r.result_path} is missing from ${r.branch}`;
    case WORKER_VERDICTS.boundaryViolation:
      return `${r.branch} touches ${r.boundary_files.join(", ")}, which the orchestrator owns`;
    default:
      return `${r.branch} is ready to merge (${r.commits} commit${r.commits === 1 ? "" : "s"})`;
  }
}
