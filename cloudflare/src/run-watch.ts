/**
 * The supervision loop's WATCH as a state machine — the decisions
 * `supervisePass` makes between boot and finalize, extracted so they can be
 * property-tested (tick p0n).
 *
 * ## Why a module of its own
 *
 * The watch's decisions are the ones the live runs paid for, and every one of
 * them is a pure function of what the last look learned — but they lived
 * inside `supervisePass`, interleaved with Workflow steps, so nothing could
 * test them without booting the whole Workflow inside workerd. Two things
 * follow from that, and both are the reason this file exists:
 *
 * 1. **It is standalone on purpose** — no imports at all. `run-workflow.ts`
 *    cannot be loaded outside workerd (it imports `cloudflare:workers`), so
 *    the plain-Node vitest leg that runs the Hegel property tests
 *    (`vitest.config.hegel.ts`) imports THIS file instead. Every type below is
 *    structural, so `run-workflow.ts`'s `Trip`, `Observation` and done
 *    signals assign to them without adapters: the machine is not a copy of
 *    the loop's logic, it IS the loop's logic, called by it.
 * 2. **Its order is the loop's order.** Every transition below mirrors the
 *    order `supervisePass` checks things in — trip before the unknown hold,
 *    the unknown hold before the linger, the linger before the exit
 *    classification, the halt question before the terminal-exit one — because
 *    that order is where the paid-for behaviour lives (a trip wins over an
 *    unanswered question; a failed question never reaches the exit
 *    classification; a halt is asked about before a terminal code is trusted).
 *
 * The three properties this machine exists to hold, each named for the tick
 * that paid for it:
 *
 * - **Every path reaches a terminal decision** (tick 0ye): every
 *   `watchLook` answers one of the verdicts that end in `finalize` —
 *   `completed`, `failed` (three ways), `tripped`, or the reboot that ends the
 *   boot — or `hold`s, and the look budget turns any unbroken hold into a
 *   clean stop. There is no input that leaves the watch undecided, and none
 *   that ends the boot without the caller's destroy-then-reboot protocol.
 * - **A container that cannot be asked is never read as dead** (tick 3ed): a
 *   look whose question failed (`process: "unknown"`) can only `hold` or, at
 *   `MAX_UNANSWERED_LOOKS` in a row, fail as `unanswerable` — never
 *   `completed`, never `ended`, never a reboot.
 * - **Two orchestrators never run at once** (the `dl8`/`cr4` ordering): the
 *   only decision that leads to another boot is `ended` classified as
 *   `reboot` — which requires the platform to have ANSWERED that the process
 *   is over. Every other ending of a live container — a trip, an
 *   unanswerable stretch, the exhausted budget — is terminal for the run, and
 *   the reboot is issued only after the previous container's destroy (the
 *   caller's `finally`, which the machine sequences by never producing two
 *   `ended` decisions for one boot).
 *
 * Everything the machine does NOT do is deliberate: it does not sleep, ask,
 * read, write, revoke or destroy — those are the caller's steps, and the
 * machine's decisions say which to take. The narrative strings a run's records
 * carry stay in the caller too (`orchestratorContainerGone`,
 * `outOfLooksDetail`): the existing workerd suite pins them through the real
 * Workflow, and this machine's tests pin the decisions they describe.
 */

// ------------------------------------------------------------ the shapes ---

/**
 * What a look may conclude about the orchestrator process.
 *
 * The platform's four states (`sandbox.ts`'s `SandboxProcessState`) are its
 * ANSWERS; `unknown` is the supervisor's own class for a question that could
 * not be asked — deliberately not one of the four, because an unanswered
 * question is not a verdict about the process (A2/A6). Structural: run-workflow's
 * `ObservedProcessState` assigns to this.
 */
export type WatchProcessState = "running" | "completed" | "failed" | "gone" | "unknown";

/**
 * Why a pass ended early, and how fast the credential has to die with it.
 *
 * Structural: run-workflow's `Trip` assigns to this.
 */
export type WatchTrip = { hard: boolean } & (
  | { kind: "stop"; detail: string }
  | { kind: "budget"; budget: "wall_clock" | "cost" | "observations"; detail: string }
);

/**
 * What one look learned, as the machine consumes it: the shape of
 * `observe()`'s `Observation` minus the log cursor and spend the cadence owns.
 *
 * `lingered` is never on a real observation: the CALLER sets it on the
 * synthesized look it re-submits after stopping a lingered process, so the
 * machine does not count one look twice.
 */
export type WatchLook = {
  process: WatchProcessState;
  exit_code: number | null;
  /** The trip the look's budget and stop checks found, or null. */
  trip: WatchTrip | null;
  /** When the look happened, on the same clock as `watchStart`'s. */
  at_ms: number;
  /** Why the question failed, present only on a look that could not ask. */
  unanswered?: string;
  /** Marks the synthesized look after a linger stop: same look, not a new one. */
  lingered?: boolean;
};

/**
 * How many consecutive looks may fail to ask before the pass gives up on the
 * container (tick 3ed). Moved here from run-workflow.ts when the machine was
 * extracted; run-workflow re-exports it, so every importer sees one spelling.
 *
 * Bounded on purpose, like every allowance in the watch: a container nobody
 * can reach is usually a broken platform, and holding forever would burn the
 * run's whole watch on a question that is never answered. The bound is in
 * LOOKS, not wall clock, so it costs a fixed number of steps inside the
 * instance's budget — and reaching it fails the pass as its own class rather
 * than rebooting, because an unanswered question is not a death (A2/A6).
 */
export const MAX_UNANSWERED_LOOKS = 3;

/** The look cadence while a signalled orchestrator settles (tick 1y4). */
export const DONE_SETTLE_LOOK_MS = 10_000;

/**
 * The exit codes a container may end with that mean "do not boot another
 * sandbox for this run" — the configuration verdicts the entrypoint and
 * `ticfac run-epic` speak in their exit status (moved here from sandbox.ts so
 * the machine's exit classification is the same code the door and the
 * workflow read; sandbox.ts re-exports them).
 */
export const TERMINAL_EXIT_CODES: readonly number[] = [
  2, // a required input is missing or malformed (including no gateway)
  3, // clone or checkout of the submitted SHA failed — or the reconciler's
  // held: the run stopped holding something only a person can move
  4, // tk is absent or is not the version the image pins — or the reconciler's
  // not-found: the epic does not exist on the submitted tree
  5, // an Environment pre-flight check failed
  6, // the repository's own [sandbox] setup failed
];

/** Whether a nonzero exit means "do not boot another sandbox for this run". */
export function isTerminalExit(code: number | null): boolean {
  return code !== null && TERMINAL_EXIT_CODES.includes(code);
}

/**
 * The class of configuration verdict a terminal exit code names, so the run's
 * recorded reason says WHAT failed and not only THAT it did. An exit status
 * is the only channel a container's boot has to the supervisor, so this class
 * is how `run.json`'s detail tells an operator "the epic was missing from the
 * submitted tree" from "the pre-flight refused" — before they go read the
 * flushed log for the sentence the container itself printed.
 */
export function terminalExitReason(code: number): string {
  switch (code) {
    case 2:
      return "a required input is missing or malformed";
    case 3:
      return (
        "the clone or checkout of the submitted SHA failed, or the reconciler held — " +
        "the run stopped holding something only a person can move"
      );
    case 4:
      return (
        "tk is absent or is not the version the image pins, or the reconciler answered not-found — " +
        "the epic does not exist on the submitted tree"
      );
    case 5:
      return "an Environment pre-flight check failed";
    case 6:
      return "the repository's own [sandbox] setup failed";
    default:
      return "an unknown configuration verdict";
  }
}

/**
 * The exit codes that mean "a service outside the container did not answer
 * through the boot's own retry window" — the image's EXIT_GATEWAY_UNAVAILABLE
 * (14: the model gateway, or the provider behind it, gave no usable answer to
 * the pre-flight probe) and EXIT_ORIGIN_UNAVAILABLE (15: origin did not answer
 * the fetch). Neither is a verdict on the run or its configuration, and
 * neither is a crash: nothing of the run was tried, and the same container
 * booted a few minutes later usually starts. Epic ymf's cloud run
 * (run_91f2952a, 2026-10-09) ended ten hours in because a twenty-minute
 * Workers AI outage spent the boots a crash would. The supervisor reboots on
 * these after a backoff, outside the crash budget, for a bounded window
 * (run-workflow.ts, TRANSIENT_REBOOT_BACKOFF_MS).
 */
export const TRANSIENT_EXIT_CODES: readonly number[] = [14, 15];

/** Whether an exit is a service outside the container not answering (14, 15). */
export function isTransientExit(code: number | null): boolean {
  return code !== null && TRANSIENT_EXIT_CODES.includes(code);
}

/** What a transient exit code says did not answer, in operator words. */
export function transientExitReason(code: number): string {
  return code === 15
    ? "origin did not answer the fetch through the boot's retry window"
    : "the model provider gave no usable answer to the boot's pre-flight probe through its retry window";
}

// --------------------------------------------------------- the watch state ---

/**
 * What the machine carries across the looks of one boot.
 *
 * Everything here is recomputable from checkpointed step results, never a live
 * clock, so a replayed Workflow folds the same looks into the same decisions.
 * The log cursor and the spend sample are NOT here: they belong to the
 * cadence and the flush, which the caller owns.
 */
export type WatchState = {
  /** How many looks this boot has taken, lingered re-looks excepted. */
  looksTaken: number;
  /** Consecutive looks that could not ASK the container (tick 3ed). */
  unasked: number;
  /**
   * The orchestrator's own report of its end, once a done signal carried one,
   * and the look time it was first seen still running after it (tick 1y4).
   * From then on the watch looks on the settle cadence, and a process still
   * alive past the settle window is stopped and concluded as the exit it
   * reported.
   */
  reported: { exit_code: number | undefined; since_ms: number | null } | null;
  /** The last look's `at_ms` — the checkpointed reading the cadence paces from. */
  lastAt: number;
};

/** The machine's configuration, all of it already resolved by the caller. */
export type WatchConfig = {
  /** How many looks one boot may take (run-workflow's `max_observations`). */
  max_observations: number;
  /** How long a signalled orchestrator has to exit (the run config's settle). */
  settle_ms: number;
  /** The settle cadence ({@link DONE_SETTLE_LOOK_MS}). */
  settle_look_ms: number;
};

/** The state the watch starts from: nothing asked, nothing reported. */
export function watchStart(at_ms: number): WatchState {
  return { looksTaken: 0, unasked: 0, reported: null, lastAt: at_ms };
}

/**
 * Folds a done signal into the state, at the moment it woke a look.
 *
 * Only a signal that carries the run's own outcome starts the settle window:
 * a bare wake-up (an older client) says nothing about how the process will
 * exit, so it is never a reason to stop one — and once a report stands, later
 * signals do not replace it, because the window is keyed on the FIRST report
 * the watch saw, exactly as `supervisePass` keeps `reported` once set.
 */
export function watchHear(
  state: WatchState,
  signal: { outcome?: { exit_code?: number } },
): WatchState {
  if (state.reported === null && signal.outcome !== undefined) {
    return { ...state, reported: { exit_code: signal.outcome.exit_code, since_ms: null } };
  }
  return state;
}

/**
 * The delay the next look waits: the paced backoff, or — once the orchestrator
 * has REPORTED its end — the settle cadence, whichever is sooner (tick 1y4).
 */
export function watchDelay(pacedMs: number, state: WatchState, config: WatchConfig): number {
  if (state.reported === null) return pacedMs;
  return Math.min(pacedMs, config.settle_look_ms, config.settle_ms);
}

/**
 * Whether the process a look saw is over: exited, or gone (never unknown).
 *
 * A predicate, so `watchLook`'s narrowing below is the compiler's, not a
 * cast's: what reaches the `ended` verdict is provably one of the three
 * terminal states the platform answers with.
 */
export function processEnded(
  process: WatchProcessState,
): process is "completed" | "failed" | "gone" {
  return process === "completed" || process === "failed" || process === "gone";
}

// ------------------------------------------------------------ the verdicts ---

/**
 * What one look decided. Every kind but `hold` ends the CALLER's loop:
 * `trip`, `unanswerable`, `completed` and `exhausted` end the pass (each
 * mapped onto the `PassOutcome` that reaches `finalize`); `ended` hands to
 * {@link watchEnded}; `linger` is the one intra-look exception — the caller
 * stops the process and immediately re-asks with the synthesized look.
 */
export type WatchDecision =
  /** Ask again on the cadence: the container is live and nothing tripped. */
  | { kind: "hold"; state: WatchState }
  /**
   * The orchestrator reported its end and is still running past the settle
   * window (tick 1y4): stop the process, then re-ask `watchLook` with the
   * synthesized look (`process: "completed"`, the reported exit code,
   * `lingered: true`) so it is classified exactly like an exit the platform
   * answered with.
   */
  | { kind: "linger"; state: WatchState; at_ms: number; reported_exit_code: number | undefined }
  /**
   * The look tripped a budget or a stop. `ended` says whether the process is
   * over — an orchestrator that has already exited has nothing in flight, so
   * it gets no grace window (tick 1y4).
   */
  | { kind: "trip"; state: WatchState; trip: WatchTrip; ended: boolean }
  /**
   * Nobody can ask this container (tick 3ed): fail the pass as its own
   * class. `unanswered` is why the question failed, for the caller's detail.
   */
  | { kind: "unanswerable"; state: WatchState; unanswered: string | null }
  /** The orchestrator exited 0: the pass is completed. */
  | { kind: "completed"; state: WatchState; lingered: boolean }
  /**
   * The platform answered that the process is over. Hand to
   * {@link watchEnded} with the halt answer (asked first unless `process` is
   * `gone`); `lingered` says the exit was concluded from a reported outcome.
   */
  | {
      kind: "ended";
      state: WatchState;
      process: "completed" | "failed" | "gone";
      exit_code: number | null;
      lingered: boolean;
    }
  /**
   * The look budget is out with the orchestrator still live: stop it cleanly
   * — never boot a replacement beside a live one (hn6).
   */
  | { kind: "exhausted"; state: WatchState };

/**
 * One look's transition — the whole decision core of the watch, in the
 * caller's order.
 *
 * Trip first (the operator's stop and the budgets win over everything: a run
 * that must end does not spend a look classifying first), the unknown hold
 * second (a failed question never reaches the exit classification — reading
 * it as a death is the bug tick 3ed paid for), then the linger, then the exit
 * classification. A `hold` is the only answer that consumes the look budget
 * without ending anything: the next look asks again, and
 * `max_observations` bounds the asking.
 */
export function watchLook(state: WatchState, look: WatchLook, config: WatchConfig): WatchDecision {
  // The same look, re-classified after a linger stop: no new look, no double
  // count — the machine's budget counts looks, not transitions.
  const counted = look.lingered === true ? state : { ...state, looksTaken: state.looksTaken + 1 };
  const next: WatchState = { ...counted, lastAt: look.at_ms };

  // The trip (the operator's stop, the budgets, a lost lease): the run ends
  // here, and whether the PROCESS has ended decides the grace window, never
  // the trip itself.
  if (look.trip !== null) {
    return { kind: "trip", state: next, trip: look.trip, ended: processEnded(look.process) };
  }

  // The question failed (tick 3ed): a container the supervisor cannot ASK is
  // UNKNOWN, not dead, and everything below this point reads the platform's
  // answer about the process — so a failed question may not reach it. The
  // watch HOLDS, and only a STRETCH of failed questions ends the pass, as its
  // own failure class.
  if (look.process === "unknown") {
    const unasked = state.unasked + 1;
    if (unasked < MAX_UNANSWERED_LOOKS) {
      return { kind: "hold", state: { ...next, unasked } };
    }
    return {
      kind: "unanswerable",
      state: { ...next, unasked },
      unanswered: look.unanswered ?? null,
    };
  }
  const held: WatchState = { ...next, unasked: 0 };

  // The linger (tick 1y4): the orchestrator said it was done, with its
  // outcome, and is still running. The window is measured from the FIRST look
  // that saw the reported process running — `since_ms ??=` pins it — so a
  // settle window cannot be restarted by another look.
  if (held.reported !== null && look.process === "running") {
    const since = held.reported.since_ms ?? look.at_ms;
    const reported = { ...held.reported, since_ms: since };
    if (look.at_ms - since >= config.settle_ms) {
      return {
        kind: "linger",
        state: { ...held, reported },
        at_ms: look.at_ms,
        reported_exit_code: reported.exit_code,
      };
    }
    return { kind: "hold", state: { ...held, reported } };
  }

  // The exits. 0 is the pass's completion; any other ending of the process
  // hands to the halt-then-terminal classification.
  if (look.process === "completed" && (look.exit_code ?? 0) === 0) {
    return { kind: "completed", state: held, lingered: look.lingered === true };
  }
  if (processEnded(look.process)) {
    return {
      kind: "ended",
      state: held,
      process: look.process,
      exit_code: look.exit_code,
      lingered: look.lingered === true,
    };
  }

  // Still running, nothing tripped: watch on. The look budget below is the
  // bound that turns an unbroken hold into a clean stop.
  return { kind: "hold", state: held };
}

/** Whether the watch has spent its look budget — the caller stops the boot. */
export function watchOut(state: WatchState, config: WatchConfig): boolean {
  return state.looksTaken >= config.max_observations;
}

/**
 * The classification of an `ended` look, asked before the pass decides what
 * the boot's ending means (the halt question first, for the reason
 * `supervisePass` states: a halt exits 3 when the reconciler held, and 3 is
 * also a configuration verdict's code — the halt line on the feed is what
 * says which it was).
 *
 * `halted` is the supervisor-halt read the caller makes unless the platform
 * answered `gone` (a replaced container cannot have halted deliberately), so
 * `null` here means "asked and none stood" on every path but that one — and
 * the `gone` rule is enforced HERE rather than only in the caller, so no
 * caller can hand a halt answer for a replaced container and be believed:
 * a container the platform took cannot have stopped deliberately, whatever
 * the halt read says (the property tests pin exactly this).
 */
export type WatchEndedDecision =
  /** The orchestrator stopped deliberately: a decision, not a death. */
  | { kind: "halted" }
  /** A configuration verdict: another container reaches the identical answer. */
  | { kind: "terminal"; reason: string }
  /**
   * The container died on this run's watch: a replacement may be booted.
   * `transient` marks a boot that stopped on a service outside it not
   * answering (TRANSIENT_EXIT_CODES): the replacement waits out a backoff and
   * does not spend the crash budget. Never set for a `gone` container — the
   * platform took it, so its exit code says nothing about a service.
   */
  | { kind: "reboot"; transient: boolean };

export function watchEnded(
  ended: Extract<WatchDecision, { kind: "ended" }>,
  halted: string | null,
): WatchEndedDecision {
  if (halted !== null && ended.process !== "gone") return { kind: "halted" };
  if (isTerminalExit(ended.exit_code)) {
    return { kind: "terminal", reason: terminalExitReason(ended.exit_code ?? -1) };
  }
  return {
    kind: "reboot",
    transient: ended.process !== "gone" && isTransientExit(ended.exit_code),
  };
}
