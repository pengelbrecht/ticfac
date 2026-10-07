import * as hegel from "@hegeldev/hegel";
import * as gs from "@hegeldev/hegel/generators";
import { expect, it } from "vitest";

import {
  DONE_SETTLE_LOOK_MS,
  isTerminalExit,
  MAX_UNANSWERED_LOOKS,
  processEnded,
  TERMINAL_EXIT_CODES,
  type WatchConfig,
  type WatchDecision,
  type WatchLook,
  type WatchProcessState,
  type WatchState,
  type WatchTrip,
  watchDelay,
  watchEnded,
  watchHear,
  watchLook,
  watchOut,
  watchStart,
} from "../../src/run-watch";

/**
 * The supervision loop as a state machine (tick p0n): property tests over
 * `src/run-watch.ts`, the pure transition core `supervisePass` runs — the
 * decisions the live runs paid for, in the tick's three named properties:
 *
 * 1. **Every path reaches finalize** (tick 0ye's bug class): whatever the
 *    looks observe, the pass ends in one of the three `PassOutcome` kinds
 *    that `superviseRun` funnels to `finalize` — `completed`, `failed` or
 *    `tripped` — and it ends within the machine's bounds (looks per boot,
 *    boots per run), so no sequence can leave the watch undecided.
 * 2. **A container that cannot be asked is never read as dead** (tick 3ed's
 *    class): a look whose question failed (`unknown`) can only hold or, past
 *    `MAX_UNANSWERED_LOOKS` in a row, fail the pass as its own class — never
 *    `completed`, never the `ended` classification that reboots.
 * 3. **Two orchestrators never run at once**: the only decision that leads to
 *    another boot is an ANSWERED process-over look classified `reboot`; every
 *    other ending of a live container — trip, unanswerable, exhausted — is
 *    terminal for the run.
 *
 * The drivers below restate `supervisePass`'s loop (the pass-level one
 * includes the attempt loop and the boot bound) as a fold over the same
 * decisions the production loop consumes, in the same order. What the model
 * does NOT restate is what the machine already owns — the look budget
 * (`watchOut`), the unanswered streak, the settle cadence and the exit
 * classification are driven through the machine's own exports, so a change
 * to the machine shows up here without the model being edited.
 */

// --------------------------------------------------------- the generators ---

const PROCESS_STATES: readonly WatchProcessState[] = [
  "running",
  "completed",
  "failed",
  "gone",
  "unknown",
];

const processGen = gs.sampledFrom(PROCESS_STATES);

/** An exit code the platform might answer with: 0, a real failure, a config code. */
const exitCodeGen = gs.oneOf(
  gs.just(0),
  gs.just(null),
  gs.integers({ minValue: 1, maxValue: 255 }),
  gs.sampledFrom(TERMINAL_EXIT_CODES),
);

const stopTripGen = gs.composite<Extract<WatchTrip, { kind: "stop" }>>((tc) => ({
  kind: "stop" as const,
  hard: tc.draw(gs.booleans()),
  detail: `a stop drawn by the property (${tc.draw(gs.integers({ minValue: 0, maxValue: 99 }))})`,
}));

const budgetTripGen = gs.composite<Extract<WatchTrip, { kind: "budget" }>>((tc) => {
  const budget = tc.draw(gs.sampledFrom(["wall_clock", "cost", "observations"] as const));
  // Budget trips are hard in production (tick gyl): the credential dies first.
  return { kind: "budget" as const, budget, hard: true, detail: `the ${budget} budget drew` };
});

const tripGen = gs.oneOf<WatchTrip | null>(gs.just(null), stopTripGen, budgetTripGen);

/** One look's observation, as observe() would report it. */
type LookEvent = {
  process: WatchProcessState;
  exit_code: number | null;
  trip: WatchTrip | null;
  at_delta: number;
  unanswered?: string;
};

const lookGen = gs.composite<LookEvent>((tc) => {
  const process = tc.draw(processGen);
  // An exit code is only a fact the platform can answer about an ended
  // process; for a running or unknown one the model may still carry one (the
  // seam's exit_code is nullable), and the machine must not read it.
  const answerable = process !== "unknown" && process !== "running";
  return {
    process,
    exit_code: answerable ? tc.draw(exitCodeGen) : tc.draw(gs.oneOf(gs.just(null), exitCodeGen)),
    trip: tc.draw(tripGen),
    at_delta: tc.draw(gs.integers({ minValue: 0, maxValue: 30 })),
    ...(process === "unknown"
      ? {
          unanswered: `the container could not be asked (draw ${tc.draw(gs.text({ maxSize: 8 }))})`,
        }
      : {}),
  };
});

/**
 * A done signal, when one lands before the look it wakes: the outcome shape
 * `readDoneSignal` answers with, as `watchHear` consumes it.
 */
type SignalEvent = { outcome?: { exit_code?: number } } | null;

const signalGen = gs.composite<SignalEvent>((tc) =>
  tc.draw(gs.booleans())
    ? {
        outcome: {
          exit_code: tc.draw(gs.oneOf(gs.just(0), gs.integers({ minValue: 1, maxValue: 9 }))),
        },
      }
    : null,
);

type BootEvent = { signal: SignalEvent; look: LookEvent };

/** One boot: the looks the world offers it, plus the halt answer its ended looks would read. */
const bootGen = gs.composite<{ halt: string | null; events: BootEvent[] }>((tc) => ({
  halt: tc.draw(gs.oneOf(gs.just(null), gs.just("the reconciler held a triage (drawn halt)"))),
  events: tc.draw(
    gs.arrays(
      gs.composite<BootEvent>((tc2) => ({
        signal: tc2.draw(signalGen),
        look: tc2.draw(lookGen),
      })),
      { minSize: 0, maxSize: 10 },
    ),
  ),
}));

const configGen = gs.composite<WatchConfig>((tc) => ({
  max_observations: tc.draw(gs.integers({ minValue: 1, maxValue: 6 })),
  settle_ms: tc.draw(gs.integers({ minValue: 1, maxValue: 50 })),
  settle_look_ms: DONE_SETTLE_LOOK_MS,
}));

/** The boot bound production pins: MAX_SANDBOX_BOOTS. */
const MAX_BOOTS = 3;

// -------------------------------------------------------------- the model ---

type PassOutcome =
  | { kind: "completed"; detail: string; boots: number }
  | { kind: "failed"; detail: string; boots: number }
  | { kind: "tripped"; trip: WatchTrip; boots: number };

/**
 * What one step of the fold learned: the look the world offered (the
 * SYNTHESIZED look, for the step after a linger stop) and the decision the
 * machine made about it. `look: null` is the budget running out — no look
 * offered, the exhausted ending.
 */
type Step = { look: LookEvent | null; decision: string };

/** What driving one boot produced. */
type BootResult = {
  outcome: PassOutcome;
  steps: Step[];
  reboot: boolean;
  looks: number;
};

/** The exhausted ending supervisePass builds: on_exhausted "stop" → a budget trip. */
function exhaustedOutcome(config: WatchConfig): PassOutcome {
  return {
    kind: "tripped",
    trip: {
      kind: "budget",
      budget: "observations",
      hard: false,
      detail: `the supervisor ran out of looks (${config.max_observations})`,
    },
    boots: 1,
  };
}

/** One boot's watch, folded exactly as supervisePass's loop does. */
function runWatch(
  config: WatchConfig,
  boot: { halt: string | null; events: BootEvent[] },
): BootResult {
  const steps: Step[] = [];
  let state: WatchState = watchStart(0);
  let looks = 0;

  while (!watchOut(state, config)) {
    const event = boot.events[looks];
    if (event === undefined) {
      // The world stops offering looks: production's loop would wait on its
      // cadence until the budget ran out, and the model reaches that ending
      // now — the caller's exhausted stop.
      steps.push({ look: null, decision: "exhausted" });
      return { outcome: exhaustedOutcome(config), steps, reboot: false, looks };
    }
    looks += 1;
    if (event.signal !== null) state = watchHear(state, event.signal);
    const at = state.lastAt + event.look.at_delta + 1;
    const seen: WatchLook = { ...event.look, at_ms: at };
    let decision: WatchDecision = watchLook(state, seen, config);
    if (decision.kind === "linger") {
      steps.push({ look: event.look, decision: "linger" });
      const synthesized: LookEvent = {
        ...event.look,
        process: "completed",
        exit_code: decision.reported_exit_code ?? null,
      };
      decision = watchLook(
        decision.state,
        { ...seen, process: "completed", exit_code: synthesized.exit_code, lingered: true },
        config,
      );
      steps.push({ look: synthesized, decision: decision.kind });
    } else {
      steps.push({ look: event.look, decision: decision.kind });
    }
    switch (decision.kind) {
      case "hold":
        state = decision.state;
        continue;
      case "trip":
        return {
          outcome: { kind: "tripped", trip: decision.trip, boots: 1 },
          steps,
          reboot: false,
          looks,
        };
      case "unanswerable":
        return {
          outcome: { kind: "failed", detail: "unanswerable", boots: 1 },
          steps,
          reboot: false,
          looks,
        };
      case "completed":
        return {
          outcome: { kind: "completed", detail: "exited 0", boots: 1 },
          steps,
          reboot: false,
          looks,
        };
      case "ended": {
        const classified = watchEnded(decision, decision.process === "gone" ? null : boot.halt);
        if (classified.kind === "halted") {
          return {
            outcome: { kind: "failed", detail: "stopped deliberately", boots: 1 },
            steps,
            reboot: false,
            looks,
          };
        }
        if (classified.kind === "terminal") {
          return {
            outcome: { kind: "failed", detail: classified.reason, boots: 1 },
            steps,
            reboot: false,
            looks,
          };
        }
        return {
          outcome: { kind: "completed", detail: "reboot", boots: 1 },
          steps,
          reboot: true,
          looks,
        };
      }
    }
  }
  // The budget ran out without the process ever observed over.
  steps.push({ look: null, decision: "exhausted" });
  return { outcome: exhaustedOutcome(config), steps, reboot: false, looks };
}

/** The whole pass, attempts and all — supervisePass's attempt loop as a fold. */
function runPass(
  config: WatchConfig,
  boots: { halt: string | null; events: BootEvent[] }[],
): {
  outcome: PassOutcome;
  steps: Step[];
  bootsUsed: number;
  reboots: number;
  looksUsed: number;
} {
  const steps: Step[] = [];
  let reboots = 0;
  let looksUsed = 0;
  for (let attempt = 1; attempt <= MAX_BOOTS; attempt++) {
    const boot = boots[attempt - 1];
    if (boot === undefined) {
      // No boot to run: production never reaches here (the boot step always
      // answers), so the model treats a missing boot as the pass ending on
      // what it already has.
      return {
        outcome: { kind: "failed", detail: "no boot", boots: attempt - 1 },
        steps,
        bootsUsed: attempt - 1,
        reboots,
        looksUsed,
      };
    }
    const watched = runWatch(config, boot);
    steps.push(...watched.steps);
    looksUsed += watched.looks;
    if (watched.reboot) {
      reboots += 1;
      if (attempt < MAX_BOOTS) continue; // reconcile, destroy, next attempt
      // The last allowed boot died: the pass falls out of its attempt loop.
      return {
        outcome: { kind: "failed", detail: "boots were not enough", boots: attempt },
        steps,
        bootsUsed: attempt,
        reboots,
        looksUsed,
      };
    }
    return {
      outcome: { ...watched.outcome, boots: attempt },
      steps,
      bootsUsed: attempt,
      reboots,
      looksUsed,
    };
  }
  return {
    outcome: { kind: "failed", detail: "boots were not enough", boots: MAX_BOOTS },
    steps,
    bootsUsed: MAX_BOOTS,
    reboots,
    looksUsed,
  };
}

/**
 * superviseRun's funnel, restated (tick 0ye): every `PassOutcome` kind the
 * pass can produce reaches `finalize` — `completed` through the progress
 * step, `failed` directly, `tripped` through the stop record — and a
 * verdict that mapped to none of these would be a run that never finalized.
 */
function reachesFinalize(pass: PassOutcome): boolean {
  return pass.kind === "completed" || pass.kind === "failed" || pass.kind === "tripped";
}

// ------------------------------------------------------------ the properties ---

it("every path the looks offer ends in an outcome that reaches finalize (tick 0ye)", () => {
  hegel.test(
    (tc) => {
      const config = tc.draw(configGen);
      const boots = tc.draw(gs.arrays(bootGen, { minSize: 1, maxSize: MAX_BOOTS }));
      const run = runPass(config, boots);
      if (!reachesFinalize(run.outcome)) {
        throw new Error(
          `the pass ended in ${JSON.stringify(run.outcome)} for config ${JSON.stringify(config)}`,
        );
      }
      // The bounds the machine owns: no sequence can spend more looks or
      // boots than the allowances hand it — a run that needed more would be
      // one that never ends.
      if (run.looksUsed > config.max_observations * run.bootsUsed) {
        throw new Error(
          `used ${run.looksUsed} looks across ${run.bootsUsed} boots with a budget of ${config.max_observations} each`,
        );
      }
      if (run.bootsUsed > MAX_BOOTS) {
        throw new Error(`used ${run.bootsUsed} boots past the bound of ${MAX_BOOTS}`);
      }
    },
    { testCases: 2000 },
  );
});

it("a look that could not ask its container never classifies the process (tick 3ed)", () => {
  hegel.test(
    (tc) => {
      const config = tc.draw(configGen);
      const boot = tc.draw(bootGen);
      const { steps } = runWatch(config, boot);

      // The core of 3ed: a failed question is a fact about the observer, so
      // nothing it produces may read as a fact about the process — not a
      // completion, not the ended classification that reboots. A trip may
      // still arrive on an unanswered look (the budgets and the operator's
      // stop win over everything, checked first in production), and the hold
      // is the whole point.
      for (const step of steps) {
        if (step.look === null) continue;
        if (step.look.process !== "unknown") continue;
        if (
          step.decision !== "hold" &&
          step.decision !== "unanswerable" &&
          step.decision !== "trip"
        ) {
          throw new Error(
            `a look that could not ask (${JSON.stringify(step.look)}) was decided as ${step.decision}`,
          );
        }
      }

      // The streak: unanswerable is only ever the CONSECUTIVE bound —
      // MAX_UNANSWERED_LOOKS unanswered looks in a row, reset by any
      // answered one, computed over the looks the watch actually took.
      let streak = 0;
      let boundReached = false;
      for (const step of steps) {
        if (step.look === null) continue;
        if (step.look.process === "unknown") {
          streak += 1;
          if (streak >= MAX_UNANSWERED_LOOKS) boundReached = true;
        } else {
          streak = 0;
        }
      }
      const unanswerable = steps.some((s) => s.decision === "unanswerable");
      const tripped = steps.some((s) => s.decision === "trip");
      if (boundReached && !unanswerable && !tripped) {
        throw new Error(
          `${MAX_UNANSWERED_LOOKS} consecutive unanswered looks ended the boot as neither unanswerable nor a trip`,
        );
      }
      if (!boundReached && unanswerable) {
        throw new Error(
          `the boot failed as unanswerable without ${MAX_UNANSWERED_LOOKS} consecutive unanswered looks (steps: ${steps.map((s) => s.decision).join(",")})`,
        );
      }
    },
    { testCases: 2000 },
  );
});

it("a replacement is only ever booted for a container the platform answered was over", () => {
  hegel.test(
    (tc) => {
      const config = tc.draw(configGen);
      const boots = tc.draw(gs.arrays(bootGen, { minSize: 1, maxSize: MAX_BOOTS }));
      const run = runPass(config, boots);

      // The boot count: every boot past the first is preceded by exactly one
      // ended→reboot decision, so boots and reboots agree — no boot ever
      // happens beside a live container. (The one shape that breaks the
      // equality is the LAST boot's reboot, which finds no attempt left and
      // ends the pass as "boots were not enough" — still no second
      // container, the pass simply ends.)
      if (run.reboots !== run.bootsUsed) {
        if (
          !(run.reboots === run.bootsUsed && run.outcome.kind === "failed") &&
          run.bootsUsed !== run.reboots + 1
        ) {
          throw new Error(
            `${run.bootsUsed} boots against ${run.reboots} reboots: a boot happened without an ended container`,
          );
        }
      }

      // And the ended classification itself: whatever look produced it was an
      // ANSWER that the process was over — never a live one, never a failed
      // question (the linger's synthesized look is `completed`, which is the
      // whole point of the linger: the reported exit, concluded).
      for (const step of run.steps) {
        if (step.decision !== "ended") continue;
        if (step.look === null) continue;
        if (!processEnded(step.look.process)) {
          throw new Error(
            `the machine classified a look whose process was ${step.look.process} as ended`,
          );
        }
      }
    },
    { testCases: 2000 },
  );
});

it("a reported orchestrator that lingers past the settle window is stopped exactly once", () => {
  hegel.test(
    (tc) => {
      const config = tc.draw(configGen);
      const boot = tc.draw(bootGen);
      const { steps } = runWatch(config, boot);
      const lingers = steps.filter((s) => s.decision === "linger").length;
      if (lingers > 1) {
        throw new Error(`the machine stopped the same lingering orchestrator ${lingers} times`);
      }
      if (lingers === 1) {
        // The stop concludes the boot as the exit the orchestrator reported:
        // the next decision is a classification, never another settle wait.
        const index = steps.findIndex((s) => s.decision === "linger");
        const after = steps[index + 1]?.decision;
        if (after !== "completed" && after !== "ended") {
          throw new Error(`a lingered orchestrator was next decided ${String(after)}`);
        }
      }
    },
    { testCases: 2000 },
  );
});

it("the settle cadence: a reported orchestrator is looked at on the short cadence, never slower", () => {
  hegel.test(
    (tc) => {
      const paced = tc.draw(gs.integers({ minValue: 1, maxValue: 600_000 }));
      const config = tc.draw(configGen);
      const reported = tc.draw(
        gs.oneOf(
          gs.just(null),
          gs.composite((tc2) => ({
            exit_code: tc2.draw(gs.integers({ minValue: 0, maxValue: 9 })),
            since_ms: null as number | null,
          })),
        ),
      );
      const state: WatchState = { looksTaken: 0, unasked: 0, reported, lastAt: 0 };
      const delay = watchDelay(paced, state, config);
      if (reported === null) {
        expect(delay).toBe(paced);
        return;
      }
      expect(delay).toBe(Math.min(paced, config.settle_look_ms, config.settle_ms));
    },
    { testCases: 1000 },
  );
});

it("the exit classification: a halted or terminal exit never reboots, and `gone` is never a halt", () => {
  hegel.test(
    (tc) => {
      const process = tc.draw(gs.sampledFrom(["completed", "failed", "gone"] as const));
      const exitCode = tc.draw(exitCodeGen);
      const halted = tc.draw(gs.oneOf(gs.just(null), gs.just("a halt the feed carried")));
      const classified = watchEnded(
        { kind: "ended", state: watchStart(0), process, exit_code: exitCode, lingered: false },
        halted,
      );
      // `gone` (a replaced container) is never a halt, whatever the halt read
      // says — the machine enforces it, not just the caller's protocol — and
      // the same exit classification decides terminal-or-reboot for it as for
      // an exited process.
      if (process === "gone") {
        expect(classified.kind === "terminal" || classified.kind === "reboot").toBe(true);
        expect(classified.kind).toBe(isTerminalExit(exitCode) ? "terminal" : "reboot");
        return;
      }
      // Halt wins, terminal next, reboot only for a death on this run's watch.
      if (halted !== null) {
        expect(classified.kind).toBe("halted");
        return;
      }
      if (isTerminalExit(exitCode)) {
        expect(classified.kind).toBe("terminal");
        return;
      }
      expect(classified.kind).toBe("reboot");
    },
    { testCases: 1500 },
  );
});

it("the look budget: a hold streak is bounded by the budget, and the exhausted ending is a stop", () => {
  hegel.test(
    (tc) => {
      const config = tc.draw(configGen);
      // A container that never answers anything but `running`: the watch
      // holds on every look until the budget is out — and the exhausted
      // ending is a clean STOP (a budget trip, never a reboot), which is the
      // whole of "never boot a replacement beside a live one".
      const events: BootEvent[] = Array.from({ length: config.max_observations + 2 }, () => ({
        signal: null,
        look: { process: "running" as const, exit_code: null, trip: null, at_delta: 0 },
      }));
      const { steps, outcome } = runWatch(config, { halt: null, events });
      expect(steps.at(-1)?.decision).toBe("exhausted");
      expect(outcome.kind).toBe("tripped");
      if (outcome.kind !== "tripped") return;
      expect(outcome.trip.kind).toBe("budget");
      if (outcome.trip.kind === "budget") expect(outcome.trip.budget).toBe("observations");
    },
    { testCases: 500 },
  );
});
