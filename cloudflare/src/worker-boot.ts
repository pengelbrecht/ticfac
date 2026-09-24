/**
 * What a per-tick worker sandbox is actually told to run (tick tap).
 *
 * `worker-dispatch.ts` (tick 0ds) is the mechanism — probe, confirmed
 * dispatch, expiring liveness, concurrent fan-out — and it says in as many
 * words that what a worker sandbox's entrypoint runs is not its job:
 * `command`/`env` are supplied by the caller. This module is that caller's
 * half, and the reason it is a module rather than an object literal at the
 * call site is the probe.
 *
 * THE PROBE MARKER IS NOT A GUESS. The green-start trap only works if the
 * dispatcher checks the probe's CONTENT, and content it can check for is
 * content the container has to promise. `image/worker.sh` makes that
 * promise — `ticks-worker --probe` proves tk, git and the harness binary
 * answer and then prints {@link WORKER_PROBE_MARKER} — and this module is
 * where the control plane reads it rather than inventing a second spelling.
 * The two languages and the shell are pinned together by
 * `contracts/worker-boot-contract.json`, checked from both suites.
 *
 * ONE IMAGE, TWO ROLES. On Cloudflare an image belongs to the containers
 * application rather than to a boot (tick x3v), so a worker container is the
 * same image as the orchestrator container. What tells it which role it is
 * playing is which entrypoint is started inside it — {@link WORKER_COMMAND}
 * rather than {@link ORCHESTRATOR_COMMAND}. There is deliberately no role flag
 * beside that: a container whose role is the command it was given cannot be
 * started in the wrong one.
 */

import type { ProbeSpec, WorkSpec } from "./worker-dispatch";

// ------------------------------------------------------------ the commands ---

export const ORCHESTRATOR_COMMAND = "/usr/local/bin/ticks-orchestrator";
export const WORKER_COMMAND = "/usr/local/bin/ticks-worker";
export const WORKER_PROBE_ARG = "--probe";
export const WORKER_PROBE_COMMAND = `${WORKER_COMMAND} ${WORKER_PROBE_ARG}`;

/**
 * The string the probe's stdout must CONTAIN for a sandbox to count as
 * launched — never its exit code, which is the whole point of
 * `evaluateProbeOutput`. Fixed, with no version or id in it, because both
 * halves of the check are a substring match and anything varying would drift.
 */
export const WORKER_PROBE_MARKER = "ticks-worker-probe-ok";

// ------------------------------------------------- the cancellation door ---

/**
 * The argument that turns {@link WORKER_COMMAND} into the cancellation door
 * (tick 7zk).
 *
 * Until this existed the supervisor could say exactly two things to a worker
 * container — kill the process, destroy the container — and both of them mean
 * "this container's work is gone". Run `run_f7bd5a36` paid $8.00 for three
 * containers that were all still working when the cost budget tripped and kept
 * nothing: no branch, no report, no salvage, a silent `no-commits` on every
 * tick. The salvage that would have rescued them already existed and was
 * already proven live (tick 5fg, run 3, commit adfedff5) — it just had no door
 * the supervisor could knock on.
 *
 * This is the third thing to say: STOP AND PUSH NOW. Started as its own
 * process inside the container, it lodges the request and asks the HARNESS to
 * stop, so the entrypoint returns from its harness call exactly as it does at
 * its own bound and everything after it — sweep, salvage, report, push — runs.
 */
export const WORKER_CANCEL_ARG = "--cancel";

/** The door, without a reason. `workerCancelCommand` adds one. */
export const WORKER_CANCEL_COMMAND = `${WORKER_COMMAND} ${WORKER_CANCEL_ARG}`;

/**
 * What the container prints once the request is lodged.
 *
 * Content, never an exit code — the same rule {@link WORKER_PROBE_MARKER}
 * exists for, applied to the other end of a container's life.
 */
export const WORKER_CANCEL_MARKER = "ticks-worker-cancel-requested";

/**
 * What the pushed report carries when the SUPERVISOR stopped the container.
 *
 * A branch cut short by the run and a branch abandoned by its agent carry the
 * same partial work and call for opposite next actions, so the container says
 * which it was.
 */
export const WORKER_CANCEL_REPORT_MARKER = "CANCELLED BY THE SUPERVISOR";

/** Where the container keeps the harness pid the door needs to find. */
export const WORKER_STATE_DIR_ENV = "TICKS_WORKER_STATE_DIR";

/**
 * The trace id a worker container boots with (D20, tick hyi).
 *
 * The container prints it in its own boot banner so a human reading the log
 * sees the chain without a second lookup. It is corroboration, not the record:
 * the control plane writes the same id as a header at the head of the
 * container's R2 stream (`artifacts.ts`), because a container that dies before
 * it prints anything is exactly the one being read.
 *
 * Pinned across the three readers by `contracts/worker-boot-contract.json`
 * — `internal/sandbox.EnvTraceID` and `image/common.sh` are the other
 * two.
 */
export const WORKER_TRACE_ID_ENV = "TICKS_TRACE_ID";

/**
 * A cancellation reason, reduced to something safe to hand a shell.
 *
 * The reason travels from the stop that asked for the salvage — `budget:cost`,
 * `stopped:hard` — which is a machine-readable token by construction. It is
 * still filtered rather than trusted: this string becomes an argument in a
 * command line the control plane composes, and a cancellation is carried
 * from a stop record a caller supplied. Anything outside the token alphabet is
 * dropped, not escaped, because a reason is a label and a label that needed
 * escaping is not one.
 */
export function workerCancelReason(reason?: string | null): string {
  return (reason ?? "").replace(/[^A-Za-z0-9:._-]/g, "").slice(0, 64);
}

/** The command that asks one container to stop and push, with its reason. */
export function workerCancelCommand(reason?: string | null): string {
  const token = workerCancelReason(reason);
  return token === "" ? WORKER_CANCEL_COMMAND : `${WORKER_CANCEL_COMMAND} ${token}`;
}

/** What the worker entrypoint exports as TK_ACTOR. */
export const WORKER_ACTOR = "cloud:worker";

// -------------------------------------------------------------- the names ---

/** The namespace a per-tick worker's branch lives in (D9's other half is `tick-run/`). */
export const WORKER_BRANCH_PREFIX = "tick/";

/**
 * The branch one tick's worker pushes — the branch `worker-collect.ts`
 * compares against the epic base and reads the report out of. Derived here and
 * in the entrypoint from the same rule, so the container and the collector
 * cannot disagree about where the work is.
 */
export function workerBranch(epic: string, tick: string): string {
  return `${WORKER_BRANCH_PREFIX}${epic}/${tick}`;
}

/**
 * The EPIC SLOT value that makes a container derive a per-ATTEMPT landing
 * branch (tick us2).
 *
 * A worker container derives its branch as `tick/${TICKS_EPIC}/${TICKS_TICK}`
 * — inside the vendored image, from the two env slots and nothing else — so
 * the control plane's only lever on where an attempt's container lands is
 * the epic slot. Riding the attempt in it (`<epic>/attempt-<n>`) gives every
 * attempt a landing branch of its own, which is the rule the local run
 * moved to for exactly this reason (internal/reconcile's attemptWriteRef): a
 * shared per-tick branch meant a redispatch's container adopted the previous
 * attempt's pushed work, and its collect counted the previous attempt's
 * commits. The tick id keeps the real slot — the prompt and the report file
 * (`RESULT-<tick>.md`) are derived from it.
 */
export function workerAttemptEpicSlot(epic: string, attempt: number): string {
  return `${epic}/attempt-${attempt}`;
}

/**
 * The branch one ATTEMPT's worker container pushes (tick us2): the landing
 * zone the executor mirrors onto the attempt's write_ref at collect. The
 * spelling must be what the container derives from the boot this module's
 * `workerBootEnv` composes — `tick/${TICKS_EPIC}/${TICKS_TICK}` — which the
 * tests pin, because the image's rule and this helper have no compiler
 * between them.
 */
export function attemptLandingBranch(epic: string, attempt: number, tick: string): string {
  return `${WORKER_BRANCH_PREFIX}${workerAttemptEpicSlot(epic, attempt)}/${tick}`;
}

/** The report a worker's branch must carry. Mirrors `resultFile` in worker-collect.ts. */
export function workerResultFile(tick: string): string {
  return `RESULT-${tick}.md`;
}

// ---------------------------------------------------------- the exit codes ---

/**
 * The classes a worker container can end in that an orchestrator cannot, so a
 * caller reading `WaitOutcome.exit_code` can tell them apart. Kept here beside
 * the command that produces them; the entrypoint's `EXIT_*` constants and
 * `internal/sandbox`'s `ExitWorker*` are the same three numbers.
 */
export const WORKER_EXIT = {
  /** Commits exist and origin would not take them: the work dies with the container. */
  push: 9,
  /** Branch and report reached origin with no work commits — the green-start trap's exit counterpart. */
  no_work: 10,
  /** The harness failed or ran out of time; whatever it committed was pushed first. */
  agent: 11,
} as const;

// --------------------------------------------------------------- the boot ---

/** Whether a worker runs the repository's own `[sandbox]` setup. */
export type WorkerSetupMode = "always" | "skip";

export type WorkerBootInput = {
  repo_url: string;
  base_sha: string;
  epic: string;
  tick: string;
  /**
   * The dispatch's attempt number, when this boot belongs to one (tick us2).
   * It rides the EPIC slot of the container's branch derivation
   * ({@link workerAttemptEpicSlot}), never the tick's — the tick slot drives
   * the prompt and the report file, and stays the tick's own id.
   */
  attempt?: number;
  run_id: string;
  gateway_base_url: string;
  gateway_token: string;
  harness?: string;
  model?: string;
  github_token?: string;
  sandbox_image?: string;
  workdir?: string;
  cache_dir?: string;
  factory_url?: string;
  factory_token?: string;
  factory_project?: string;
  /** The chain this container's work belongs to; see {@link WORKER_TRACE_ID_ENV}. */
  trace_id?: string;
  /**
   * Whether this worker runs the repository's `[sandbox]` setup.
   *
   * This is the performance lever for the whole per-tick design and it is
   * exposed rather than decided: fan-out per-sandbox time degrades 3.74x at
   * N=5 and tick kuf found ALL of that in dependency install, not the image
   * pull. `always` is the default and the correct one — a worker that cannot
   * run the repository's tests cannot implement a tick — and a wave whose
   * ticks touch no dependencies can decline to pay it N times.
   */
  setup?: WorkerSetupMode;
  /**
   * How long this worker's harness may WORK, if the caller bounds it.
   *
   * Zero (absent) means unbounded — the entrypoint's own default. When a
   * caller does bound it, the bound is the agent's working time, never a
   * supervisor's observation window wearing it as a disguise (tick 5fg).
   */
  harness_budget_ms?: number;
};

/**
 * A worker container's own default harness.
 *
 * `pi`, per the operator's rule (tick uqi): the cloud's harness is pi and only
 * pi, and nothing in the cloud runs claude — `image/common.sh` refuses
 * `claude` outright against a non-Anthropic provider, by design, and the
 * routes the operator pays for are Workers AI ones the image wires pi to.
 * The default and the `wrangler.toml` pins agree; this constant is the floor
 * under a deployment that sets no `RUN_WORKER_HARNESS`, not the rule itself.
 */
export const WORKER_DEFAULT_HARNESS = "pi";

/**
 * A worker container's own default model when nothing else names one.
 *
 * Deliberately NOT `.tick/runners.toml`'s `[roles.implement]` (`kind =
 * "claude"`, `model = "sonnet"`): that table is shared with `tk herd spawn`'s
 * LOCAL worker CLIs on an operator's machine, which authenticate straight to
 * Anthropic and have no factory gateway credential at all. A cloud worker
 * left to fall through to it asked the checkout's `tk sandbox model
 * --role implement` for that same `sonnet`/`anthropic` route, which the
 * factory gateway does not serve (Phase 2 routes Workers AI only) —
 * `probe_model` died `EXIT_MODEL` deterministically, on every worker, in
 * every wave, before the harness ever started (tick ys3). Repointing
 * `[roles.implement]` itself was the trap: it would have fixed the cloud
 * container and broken every local epic run in the same commit. A cloud
 * worker's harness and model come from the FACTORY, never from the
 * repository's implement role.
 *
 * It is a DEFAULT, not the route: {@link workerModel} resolves the run's own
 * choice first, then the deployment's `RUN_WORKER_MODEL`, then this. Tick 1cd
 * added the middle rung, because until then changing which model every cloud
 * worker runs meant editing this line and redeploying the factory; tick uqi
 * pinned that rung in `wrangler.toml` to the same value this constant now
 * holds, so the deployable config — not a source constant — is what a
 * deployment reads.
 *
 * **Why GLM 5.3 (tick uqi).** The operator's rule is Workers AI models only
 * — nothing in the cloud runs claude — and pi on GLM 5.3 (GLM 5.3 Flash for
 * cheap work) is today's choice within it; the model may change, the rule
 * does not. The route is proven rather than hoped for: the
 * 2026-09-22/23 smoke runs (the throwaway `smoke/xte-omp-glm` branch, which
 * this tick retires) booted pi on GLM 5.3 through the gateway end to end, and
 * the pinned image
 * carries pi's GLM 5.3 catalog correction (vendored from ticks `d5dbfbc4`:
 * pi's catalog overstates the model's output limit, and the override pins
 * maxTokens and the thinking format) — without which the first harness probe
 * dies on a bodyless HTTP 400 that looks like nothing else in the log.
 *
 * The defaults this replaces were Workers AI too — flash, then
 * `deepseek-v4-pro-0813` on run_215b7cbff9's evidence that flash converged on
 * only one of three real ticks inside a 90-minute budget — so the operator's
 * Phase 2 constraint (Workers AI only, the Cloudflare credit) is unchanged.
 * The end state is still per-tick tier selection, which `[roles.implement]`
 * `.tiers` already expresses for local workers and the cloud path has no
 * plumbing for — see the note on {@link workerModel} for exactly what it
 * needs. Until then a deployment that knows its wave is cheap sets
 * `RUN_WORKER_MODEL` to `workers-ai/@cf/zai-org/glm-5.3-flash`, which is the
 * rung tick 1cd added.
 */
export const WORKER_DEFAULT_MODEL = "workers-ai/@cf/zai-org/glm-5.3";

/**
 * Whether a configured route string was actually supplied.
 *
 * Blank is unset, matching `run-workflow.ts`'s `textVar`: an exported empty
 * `TICKS_MODEL` is not "let the container decide", it is a defeated default.
 */
function configured(value: string | null | undefined): string | null {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : null;
}

/**
 * Which harness one worker container is actually told to run.
 *
 * @see workerModel for the precedence, which is the same for both.
 */
export function workerHarness(run?: string | null, deployment?: string | null): string {
  return configured(run) ?? configured(deployment) ?? WORKER_DEFAULT_HARNESS;
}

/**
 * Which model one worker container is actually told to run.
 *
 * **Precedence, highest first, and the order is deliberate:**
 *
 * 1. **the run submission** — what THIS run asked for (`context.config.model`,
 *    from `RUN_MODEL` or an operator's `--model`). It already outranked the
 *    constant before tick 1cd and it still does: a choice made about one run
 *    outranks a standing one.
 * 2. **the deployment variable** — `RUN_WORKER_MODEL`, this factory's standing
 *    choice of worker model. The rung tick 1cd added, so the answer to "which
 *    model do cloud workers run" is `wrangler.toml` plus a deploy rather than
 *    a source edit, exactly as `RUN_WORKER_BUDGET_MS` and the other budgets
 *    already work.
 * 3. **the built-in default** — {@link WORKER_DEFAULT_MODEL}, which is the
 *    fallback and stays the fallback.
 *
 * **What per-tick tier selection would need, and why it is not this tick.**
 * All three rungs above are per-RUN: every container in a wave gets the same
 * model, so a one-line tick pays pro's rate and a hard one cannot ask for it.
 * `[roles.implement].tiers` already expresses the answer for local workers.
 * Reaching it from here needs four things the cloud path does not have: (a) a
 * per-tick tier on the wave plan — the plan carries `tick_id`/`base_sha` and
 * nothing about difficulty, so something has to decide the tier and the only
 * evidence available at dispatch is the tracker record; (b) a tier -> model
 * table the FACTORY owns, not the checkout's, for ys3's reason — the
 * repository's table routes local CLIs straight to Anthropic; (c) `WorkSpec`
 * construction per task rather than per wave, which `workerWorkSpec` already
 * is (it is called with `(task) => ...`), so this part is free; and (d) a
 * per-tick budget, since a flash worker needs MORE wall clock than pro.
 * Good Phase 3 candidate.
 */
export function workerModel(run?: string | null, deployment?: string | null): string {
  return configured(run) ?? configured(deployment) ?? WORKER_DEFAULT_MODEL;
}

/**
 * The container's own harness bound, in whole seconds — `TICKS_WORKER_TIMEOUT`.
 *
 * Zero (unbounded) when the caller bounds nothing, which is what an unset
 * variable means to the entrypoint.
 */
export function workerHarnessTimeoutSeconds(harnessBudgetMs?: number): number {
  if (harnessBudgetMs === undefined || !Number.isFinite(harnessBudgetMs) || harnessBudgetMs <= 0) {
    return 0;
  }
  return Math.floor(harnessBudgetMs / 1000);
}

/**
 * The environment one worker container boots with.
 *
 * Only what is set is passed, and the entrypoint defaults everything else —
 * except `TICKS_HARNESS`/`TICKS_MODEL`, which this function itself defaults
 * (see {@link WORKER_DEFAULT_MODEL}), because the entrypoint's own fallback
 * for an unset model is the repository's `[roles.implement]`, and that route
 * is for local worker CLIs, not a factory-dispatched container. An empty
 * string is not the same as absent to a shell reading `${VAR:-default}`.
 *
 * The control plane resolves `harness`/`model` through {@link workerModel}
 * before it gets here, so the fallback below is the last rung of the same
 * ladder rather than a second, competing default.
 */
export function workerBootEnv(input: WorkerBootInput): Record<string, string> {
  const env: Record<string, string> = {
    TICKS_REPO_URL: input.repo_url,
    TICKS_BASE_SHA: input.base_sha,
    TICKS_EPIC:
      input.attempt === undefined ? input.epic : workerAttemptEpicSlot(input.epic, input.attempt),
    TICKS_TICK: input.tick,
    TICKS_RUN_ID: input.run_id,
    AI_GATEWAY_BASE_URL: input.gateway_base_url,
    AI_GATEWAY_TOKEN: input.gateway_token,
    TICKS_WORKER_SETUP: input.setup ?? "always",
  };
  const optional: [string, string | undefined][] = [
    // Defaulted rather than left absent, unlike everything else below: an
    // absent TICKS_MODEL falls through to the container's own
    // `resolve_model`, which asks the checkout's `[roles.implement]` — the
    // LOCAL worker route, unreachable from the factory gateway (tick ys3).
    ["TICKS_HARNESS", input.harness ?? WORKER_DEFAULT_HARNESS],
    ["TICKS_MODEL", input.model ?? WORKER_DEFAULT_MODEL],
    ["GITHUB_TOKEN", input.github_token],
    ["TICKS_SANDBOX_IMAGE", input.sandbox_image],
    ["TICKS_WORKDIR", input.workdir],
    ["TICKS_CACHE_DIR", input.cache_dir],
    ["TICKS_FACTORY_URL", input.factory_url],
    ["TICKS_FACTORY_TOKEN", input.factory_token],
    ["TICKS_FACTORY_PROJECT", input.factory_project],
    [WORKER_TRACE_ID_ENV, input.trace_id],
  ];
  for (const [name, value] of optional) {
    if (value !== undefined && value !== "") env[name] = value;
  }
  const timeout = workerHarnessTimeoutSeconds(input.harness_budget_ms);
  if (timeout > 0) env.TICKS_WORKER_TIMEOUT = String(timeout);
  return env;
}

/**
 * The green-start probe for one worker sandbox.
 *
 * It carries the same environment as the real command so the probe answers as
 * the container that is about to do the work — a probe run in a different
 * environment proves something about a container nobody is going to use.
 */
export function workerProbeSpec(input: WorkerBootInput): ProbeSpec {
  return {
    command: WORKER_PROBE_COMMAND,
    env: workerBootEnv(input),
    expect: WORKER_PROBE_MARKER,
  };
}

/**
 * Everything `spawnWorker` needs for one tick.
 *
 * PER TICK, not per run: `TICKS_TICK` differs for every worker container, so
 * a caller dispatching several builds one of these per task rather than
 * sharing a single `WorkSpec` across them.
 */
export function workerWorkSpec(input: WorkerBootInput): WorkSpec {
  const env = workerBootEnv(input);
  return {
    probe: { command: WORKER_PROBE_COMMAND, env, expect: WORKER_PROBE_MARKER },
    command: WORKER_COMMAND,
    env,
    // How this container is asked to stop and push before it is destroyed
    // (tick 7zk). Carried in the spec rather than assembled at the teardown
    // site for the same reason `command` is: the dispatcher is TOLD what to
    // run in a container, it does not invent it. The reason is appended by
    // the caller, which is the only party that knows why the wave stopped.
    salvage: { command: WORKER_CANCEL_COMMAND, env, marker: WORKER_CANCEL_MARKER },
  };
}
