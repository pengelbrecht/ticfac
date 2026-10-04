/**
 * The worker contract as hooks on pi-durable (epic 43y step 5, tick pom —
 * docs/spikes/n0b-round2-pi-durable.md, "The worker contract on
 * pi-durable").
 *
 * On the pi-CLI path this contract lives in `image/worker.sh`, which
 * re-prompted a harness that exited 0 without its report (the early-exit
 * nudge, tick 060) and handed a report that fails `ticfac lint-report` back
 * to the same session (the linter pushback, #183) — as process RELAUNCHES.
 * A durable conversation needs neither: an `onYield` hook can continue the
 * run with another user message in the SAME conversation, and that is what
 * this module is — the port of worker.sh's nudge/pushback loop into
 * `GenerationHooks.onYield`, plus the wall deadline as `abort()`.
 *
 * The other half of the step lives in the container: `worker.sh --boot` and
 * `--finish` (image/worker.sh) split the shell's own halves around the
 * conversation this module runs, and the strings both halves share are pinned
 * in `contracts/worker-boot-contract.json`, read by both suites.
 *
 * The bounds are the shell's bounds, on purpose: `MaxNudges` /
 * `MaxLintPushbacks` in the Go supervisor (internal/exec/subprocess) and
 * worker.sh's own `NUDGE_MAX` / `REPORT_PUSHBACK_MAX` are 2 and 2, and a
 * worker told twice what is missing and still not writing its report is not
 * going to write it — the wall clock is the outer bound.
 */

import type { Context } from "@earendil-works/chord";
import type { AssistantMessage } from "@earendil-works/pi-ai";
import type {
  ConversationId,
  GenerationHooks,
  HookApi,
  UserInput,
} from "@earendil-works/pi-durable";
import type { ExecutionEnv } from "@earendil-works/pi-durable/env";

/** The same bound as the Go supervisor's `MaxNudges` and worker.sh's NUDGE_MAX. */
export const WORKER_MAX_NUDGES = 2;

/** The same bound as the Go supervisor's `MaxLintPushbacks` and worker.sh's REPORT_PUSHBACK_MAX. */
export const WORKER_MAX_PUSHBACKS = 2;

/**
 * The same line every local prompt carries (HeadlessLine), and worker.sh's
 * `HEADLESS_LINE`: the model cannot see that it ends a job by ending its
 * turn, and every interactive habit it has says a background task will call
 * it back.
 */
export const WORKER_HEADLESS_LINE =
  "You run headless: ending your turn ends the job. Run commands in the foreground and wait for them; never end your turn while waiting on a background task.";

/** The report checker the image ships beside ticfac (worker.sh's REPORT_CHECKER). */
export const WORKER_REPORT_CHECKER = "ticfac-exec-subprocess";

export type WorkerContractOptions = {
  /** The report's absolute path in the env — `RESULT-<tick>.md` inside the checkout. */
  readonly reportPath: string;
  /** The checkout's directory — the env's cwd — passed to the checker as `--repo`. */
  readonly repoDir: string;
  /** The branch the attempt lands its commits on; the nudge tells the agent to commit there. */
  readonly branch: string;
  /** The tick the attempt implements; the checker's `--tick`. */
  readonly tick: string;
  /** The role the report is checked as (worker.sh's report_role); role-neutral when unset. */
  readonly role?: string;
  /** The checker binary; default {@link WORKER_REPORT_CHECKER}. */
  readonly checkerBinary?: string;
  /** Overrides the nudge bound (default {@link WORKER_MAX_NUDGES}). */
  readonly maxNudges?: number;
  /** Overrides the pushback bound (default {@link WORKER_MAX_PUSHBACKS}). */
  readonly maxPushbacks?: number;
  /** Where unrunnable checks and spent bounds are said; default console.warn. */
  readonly log?: (line: string) => void;
};

/**
 * How many follow-ups one conversation has already been sent.
 *
 * PER CONVERSATION and in the harness process — a Map in the hook closure —
 * not a task memo: a `continue` hands the run to a SUCCESSOR generation
 * (pi-durable's `answer()` does exactly that), and a task's memos do not
 * cross the handOver, so a memo-based bound would reset at every follow-up
 * and nudge forever. The process scope is the shell's own scope too:
 * worker.sh counted its loop per boot and a crashed container lost it, and
 * the wall clock is the outer bound either way.
 */
type ConversationCount = { nudges: number; pushbacks: number };

function conversationCount(
  counts: Map<ConversationId, ConversationCount>,
  conversationId: ConversationId,
): ConversationCount {
  let count = counts.get(conversationId);
  if (count === undefined) {
    count = { nudges: 0, pushbacks: 0 };
    counts.set(conversationId, count);
  }
  return count;
}

/** A shell-word single-quoted string; the checker's argv reaches `exec` as one command line. */
export function shellQuote(text: string): string {
  return `'${text.replaceAll("'", `'\\''`)}'`;
}

/**
 * The nudge a yield with NO report is continued with — worker.sh's
 * `nudge_prompt_text`, spelled here so the durable path asks for exactly
 * what the shell path asked for.
 */
export function workerNudgeMessage(options: WorkerContractOptions): string {
  return (
    `You ended your turn without writing your report. ${WORKER_HEADLESS_LINE}\n\n` +
    "Finish the work you were doing: if you were waiting on a command, run it again in the foreground and wait for it. " +
    `Commit on ${options.branch}, then write your report to this exact absolute path, ending with its STATUS line:\n\n` +
    `    ${options.reportPath}`
  );
}

/** The checker's command line, as the env's shell receives it. */
export function workerReportCheckCommand(options: WorkerContractOptions): string {
  const checker = shellQuote(options.checkerBinary ?? WORKER_REPORT_CHECKER);
  const args = [
    checker,
    "lint-report",
    shellQuote(options.reportPath),
    "--pushback",
    "--tick",
    shellQuote(options.tick),
    "--repo",
    shellQuote(options.repoDir),
  ];
  if (options.role && options.role.trim() !== "") {
    args.push("--role", shellQuote(options.role));
  }
  return args.join(" ");
}

/**
 * The report check as the env runs it: exit 0 passes, exit 1 pushes back
 * with the checker's own output, anything else is a checker that could not
 * answer and pushes nothing back — the same three answers worker.sh's
 * `check_report` takes, never holding the work hostage to its report.
 */
export async function runWorkerReportCheck(
  env: ExecutionEnv,
  options: WorkerContractOptions,
  context: Context,
): Promise<
  | { readonly verdict: "pass" }
  | { readonly verdict: "pushback"; readonly message: string }
  | { readonly verdict: "broken"; readonly reason: string }
> {
  let output = "";
  const result = await env.exec(
    workerReportCheckCommand(options),
    {
      cwd: options.repoDir,
      onOutput: (text: string) => {
        output += text;
      },
    },
    context,
  );
  if (!result.ok) {
    return { verdict: "broken", reason: result.error.message ?? String(result.error) };
  }
  if (result.value.exitCode === 0) {
    return { verdict: "pass" };
  }
  if (result.value.exitCode === 1) {
    return { verdict: "pushback", message: output.trim() };
  }
  return { verdict: "broken", reason: `the checker exited ${result.value.exitCode}` };
}

/**
 * The worker's `onYield`: the early-exit nudge (060) and the report linter
 * pushback (#183) as follow-up messages in the SAME conversation.
 *
 * What it decides, in worker.sh's order:
 *
 * - No report at the yield → the agent ended its turn early; nudge it, at
 *   most `maxNudges` times.
 * - A report at the yield → run the report check; a failing report is
 *   pushed back with the checker's own message, at most `maxPushbacks`
 *   times. A checker that cannot answer pushes nothing back: the work is
 *   never held hostage to its report, and collect decides.
 * - Anything else → `undefined`: the yield stands and the run settles.
 *
 * Every bound is counted in the task's durable memos, first-writer-wins per
 * follow-up, so a crash-and-replay between a nudge and its yield does not
 * spend the bound twice.
 */
export function workerOnYield(
  env: ExecutionEnv,
  options: WorkerContractOptions,
): GenerationHooks["onYield"] {
  const maxNudges = options.maxNudges ?? WORKER_MAX_NUDGES;
  const maxPushbacks = options.maxPushbacks ?? WORKER_MAX_PUSHBACKS;
  const log = options.log ?? console.warn;
  // Per conversation, in this process — see ConversationCount above for why
  // this is a Map in the closure and not a task memo.
  const counts = new Map<ConversationId, ConversationCount>();
  return async (
    _answer: AssistantMessage,
    api: HookApi,
    context: Context,
  ): Promise<{ readonly continue: UserInput } | undefined> => {
    const report = await env.exists(options.reportPath, context);
    if (!report.ok) {
      log(`the report check could not look for ${options.reportPath}: ${report.error}`);
      return undefined;
    }
    const spent = conversationCount(counts, api.conversationId);
    if (!report.value) {
      if (spent.nudges >= maxNudges) {
        log(
          `the report at ${options.reportPath} is still missing after ${spent.nudges} nudge(s); the yield stands and collect decides`,
        );
        return undefined;
      }
      spent.nudges += 1;
      return { continue: workerNudgeMessage(options) };
    }
    const check = await runWorkerReportCheck(env, options, context);
    if (check.verdict === "broken") {
      log(`the report check could not run: ${check.reason}; the report goes as the agent wrote it`);
      return undefined;
    }
    if (check.verdict === "pass") {
      return undefined;
    }
    if (spent.pushbacks >= maxPushbacks) {
      log(
        `the report at ${options.reportPath} still fails the report check after ${spent.pushbacks} pushback(s); it is pushed as the agent wrote it, and collect decides`,
      );
      return undefined;
    }
    spent.pushbacks += 1;
    return { continue: check.message === "" ? workerNudgeMessage(options) : check.message };
  };
}

/** A thing whose live work can be stopped the way a conversation can. */
export type WallDeadlineTarget = {
  abort(context: Context): Promise<void>;
};

/** What an armed wall deadline leaves its armer: the only way to disarm it. */
export type WallDeadline = { readonly cancel: () => void };

/**
 * The wall deadline as `abort()` (worker.sh's `harness_timeout`, which had
 * to kill a process because a CLI has no other door). When it fires it
 * aborts the conversation — queued inputs withdrawn, live tasks marked —
 * and the host then runs the finish phase over whatever the conversation
 * settled with. `cancel()` disarms it, which is what a host that finishes
 * inside the wall owes the process: an armed timer left behind by a settled
 * conversation would abort the NEXT submission.
 */
export function armWallDeadline(
  target: WallDeadlineTarget,
  deadlineMs: number,
  context: Context,
): WallDeadline {
  const timer = setTimeout(() => {
    void target.abort(context).catch((error: unknown) => {
      console.warn(`the wall deadline fired and the abort failed: ${error}`);
    });
  }, deadlineMs);
  return { cancel: () => clearTimeout(timer) };
}
