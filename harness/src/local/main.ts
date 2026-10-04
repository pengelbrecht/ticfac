/**
 * The local worker host's entry point (epic 43y step 7, tick hpk): the
 * process the Go executor's `pi` runner is — see ./worker-host.ts for what
 * the host assembles, and /runtime/register.mjs for how TypeScript sources
 * run here without a build step.
 *
 * THE ARGV, as internal/exec/subprocess's runner table spells it:
 *
 *     node --experimental-strip-types \
 *          --import <harness>/runtime/register.mjs \
 *          <harness>/src/local/main.ts \
 *          --config <stateDir>/worker.json \
 *          [--model <provider/id>] \
 *          --message <the job prompt, or a relaunch's follow-up>
 *
 * `--config` names the whole per-attempt interface (storage, worktree,
 * branch, report, steer socket, wall deadline); `--model` overrides the
 * config's model so the attempt record's argv names the model that ran, the
 * way every other runner's does; `--message` is the input — the job prompt
 * for the attempt's first process, the supervisor's follow-up text for a
 * relaunch.
 *
 * EXIT CODES — the supervisor's contract with this runner:
 *
 *   0  the conversation settled, and every steer settled with it. Whether
 *      the work is DONE is collect's to read from the report and the branch,
 *      never this code's.
 *   1  the conversation settled UNANSWERED — the model, the provider or the
 *      abort (the wall) ended it without an answer. A failed runner.
 *   2  this host could not run at all: an unusable config, a storage that
 *      cannot open, a routed model this host refuses, a worktree with no
 *      identity for the checkpoints. The message on stderr says which.
 *
 * The process installs nothing, writes nothing outside the worktree and the
 * state directory, and holds no credential the host did not hand it: Workers
 * AI credentials resolve from the environment, the way they do for the pi
 * CLI this host replaces.
 *
 * A SIGKILL is a supported ending, not an error: everything durable — every
 * turn, tool call and queued steer — was committed before anything ran, and
 * a relaunched process resumes from the storage. Nothing below catches a
 * signal on purpose.
 */

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

/** The exit codes, spelled once (the header comment is the contract). */
const EXIT_SETTLED = 0;
const EXIT_UNANSWERED = 1;
const EXIT_CANNOT_RUN = 2;

/** The harness package's root, from this module's own place in it. */
const harnessRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

/**
 * The pinned `@earendil-works/*` packages must be installed in the harness
 * directory before any import below resolves them. The orchestrator's own
 * checkout usually has them (it is the tree the run works on), and the
 * executor's argv points `--import` and the entry at that checkout's copy —
 * so this is a one-second warm-store no-op there. A tree without them (a
 * CI job that did not install the workspace, a fresh clone) installs them
 * here, once, before the imports: a local worker that cannot boot over a
 * missing node_modules is a worker no relaunch ever fixes.
 */
function ensureDependencies(): void {
  const pinned = join(harnessRoot, "node_modules", "@earendil-works", "pi-durable");
  if (existsSync(pinned)) return;
  const label = "the pi-durable dependencies";
  const install = spawnSync("pnpm", ["install", "--frozen-lockfile", "--prefer-offline"], {
    cwd: harnessRoot,
    stdio: "inherit",
  });
  if (install.error !== undefined || install.status !== 0) {
    throw new Error(
      `${label} are not installed in ${harnessRoot} and pnpm could not install them ` +
        "(is pnpm on PATH?): the local worker host runs on the pinned @earendil-works packages",
    );
  }
}

/** Parses the argv into `{ config, model?, message }`, or throws. */
function parseArgs(argv: string[]): { config: string; model?: string; message: string } {
  const out: { config?: string; model?: string; message?: string } = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i] as string;
    const take = (): string => {
      const value = argv[++i];
      if (value === undefined) throw new Error(`${arg} needs a value`);
      return value;
    };
    if (arg === "--config") out.config = take();
    else if (arg === "--model") out.model = take();
    else if (arg === "--message") out.message = take();
    else throw new Error(`unknown argument ${arg} (want --config, --model, --message)`);
  }
  if (out.config === undefined)
    throw new Error("no --config: the worker.json the executor wrote is the whole interface");
  if (out.message === undefined)
    throw new Error("no --message: the input is the job prompt, or a relaunch's follow-up");
  return { config: out.config, model: out.model, message: out.message };
}

/**
 * The entry. Dynamic imports throughout: the config is read and the
 * dependencies ensured BEFORE any of the package's own modules load, so the
 * EXIT_CANNOT_RUN path needs none of them.
 */
async function main(): Promise<number> {
  const parsed = parseArgs(process.argv.slice(2));
  ensureDependencies();
  const config = JSON.parse(
    await readFile(parsed.config, "utf8"),
  ) as import("./worker-host.js").LocalWorkerConfig;
  const { runLocalWorker } = await import("./worker-host.js");
  const settled = await runLocalWorker({
    config: parsed.model === undefined ? config : { ...config, model: parsed.model },
    message: parsed.message,
  });
  if (settled.status === "done") {
    // The answer's own words, bounded, are the last thing in the runner log:
    // the log is what collect reads beside the report.
    const bounded =
      settled.answer.length > 4000 ? `${settled.answer.slice(0, 4000)}…` : settled.answer;
    if (bounded.trim() !== "") console.log(bounded);
    return EXIT_SETTLED;
  }
  console.error(`the worker's run did not answer: ${settled.reason}`);
  return EXIT_UNANSWERED;
}

main()
  .then((code) => {
    process.exit(code);
  })
  .catch((error: unknown) => {
    console.error(`the local worker host could not run: ${String(error)}`);
    process.exit(EXIT_CANNOT_RUN);
  });
