/**
 * The reconciler's control flow, hosted by a Cloudflare Workflow: one
 * Workflow per EpicRun (SPEC §12 Phase 4 item 2).
 *
 * What moved, and what did not. Before this, RunWorkflow booted a container
 * that ran an orchestrating agent and supervised it; the reconciler's logic
 * lived in the agent's prompt-driven loop. After this, the Workflow IS the
 * reconciler: the plan, the dispatch window, the settle-from-durable-evidence
 * and the checkpoint all run as Workflow steps, and a run outlives its
 * isolate by construction — a restarted Workflow resumes from `.ticfac/`,
 * because every step re-derives its decision from the durable records.
 *
 * The semantics are the Go reconciler's (`internal/reconcile`), not a fresh
 * design: same dispatch window (the configured width, admitted one at a
 * time, refusals stopping the run rather than draining it), same durable
 * evidence (an attempt's marker on the run branch and the executor's own
 * four operations — never a claim's word for its own progress), same
 * refusals (a wave-full claim is a retry; a human gate is a hold, not a
 * failure of the work). A role job's answer is durable evidence too, the
 * same way it is locally: the review and closeout exchanges land on the run
 * branch as the contract's decision records, so a restart re-reads them
 * instead of re-asking a model it already paid. `planFrom`, the window and
 * the settle loop below are
 * ports of those pieces, re-expressed for a host that cannot run tk
 * (tracker access goes through `tracker-client.ts`, the contract-held
 * implementation of §3.1).
 *
 * One recorded decision, not an implementation detail (the tick's own rule):
 * on the local host the reconciler MERGES an attempt's branch and runs the
 * integrated gate before closing its tick. On this host, integration is not
 * yet reachable: the serialized publisher is tick ef7's (Phase 4 item 3) and
 * the container executor is tick k4s's (item 4), and this module refuses to
 * close a tick behind an integration it cannot perform. A run whose ticks
 * are all reported therefore ends `failed` naming the integration boundary
 * UNLESS an `IntegrationHost` is wired — which is what the phase's later
 * ticks and this module's tests do. A Workflow that closed ticks without
 * integrating them would write `completed` over work nobody proved, which is
 * exactly the false success the Go reconciler exists to refuse.
 *
 * And where this host's semantics deliberately DIVERGE from the Go
 * reconciler's — the admission boundaries it does not carry, the per-repo
 * publish slot, the lapsed-slot re-acquire, the named PR base — each
 * divergence is a numbered decision in `decisions/reconciler-parity.json`
 * (D26-D33, tick sz0), pinned to the code regions that implement it on both
 * sides and held by `internal/reconcile`'s parity check: two implementations
 * of one reconciler may differ only where a decision says so.
 */

import { WorkflowEntrypoint, type WorkflowEvent, type WorkflowStep } from "cloudflare:workers";

import {
  type CloseoutRule,
  ciForTree,
  ciSubject,
  composePRBody,
  finalReviewOf,
  readCloseoutRule,
  readFindings,
} from "./closeout";
import { type PullRequest, type PullRequests, pullRequestsFromEnv } from "./forge";
import { contentsStore } from "./git-contents";
import type { Env } from "./index";
import { DEFAULT_LEASE_TTL_MS, type HolderCredentials, MAX_LEASE_TTL_MS } from "./lease";
import {
  type Checkpoint,
  provenance,
  RunStateStore,
  type TickState,
  terminalState,
} from "./run-state-store";
import { leaseLostTrip, renewalTtl, renewRunLease } from "./run-workflow";
import { roomFor } from "./runs";
import { sandboxExecutorFromEnv } from "./sandbox-executor";
import { type Graph, type GraphTask, TrackerClient } from "./tracker-client";

// ----------------------------------------------------------- the executor ---

/** What the reconciler asks one implementer job to be. */
export type AttemptSpec = {
  run_id: string;
  epic_id: string;
  tick_id: string;
  /** 1-based, per tick — the number the attempt marker and branch carry. */
  attempt: number;
  role: string;
  project: string;
  /** The branch the attempt commits to; created empty by the executor. */
  write_ref: string;
  base_ref: string;
  title: string;
};

/** The JobHandle the executor's start returned (SPEC 4.3); opaque here. */
export type AttemptHandle = Record<string, unknown>;

export type AttemptStatus = {
  state: "running" | "exited" | "gone";
  exit_code?: number | null;
};

export type AttemptReport = {
  outcome: "done" | "blocked" | "failed";
  commits: number;
  detail: string;
};

/**
 * contracts/job-protocol.json's four operations, the seam every executor is:
 * start, inspect, collect, cancel. The cloud host's concrete executor is the
 * sandbox compatibility executor (tick k4s); until it lands, a deployment
 * WITHOUT one gets a refusal before any record says a dispatch happened —
 * `EpicReconciler` treats a missing executor exactly the way the local
 * reconciler treats a profile it cannot build an executor for.
 */
export interface AttemptExecutor {
  start(spec: AttemptSpec): Promise<AttemptHandle>;
  inspect(handle: AttemptHandle): Promise<AttemptStatus>;
  collect(handle: AttemptHandle): Promise<AttemptReport>;
  cancel(handle: AttemptHandle): Promise<void>;
}

// --------------------------------------------------------- the integration ---

/**
 * The merge-and-gate half of a tick's settle: what turns a reported attempt
 * into a closeable one. Not this tick's to implement — it is the serialized
 * publisher (ef7) and the integrated gate — but the control flow has to name
 * where it sits, because a reconciler that closed ticks without it would
 * write `completed` over work nobody proved.
 */
export interface IntegrationHost {
  integrate(input: {
    tick_id: string;
    attempt: number;
    write_ref: string;
  }): Promise<{ integrated: true } | { integrated: false; reason: string }>;
}

// --------------------------------------------------------------- the plan ---

export type PlanEntry = {
  tick_id: string;
  title: string;
  wave: number;
  role: string;
  priority: number;
  order: number;
};

/**
 * The ONE place the EPIC-SKELETON's ordering lives: work first, then review,
 * then closeout — the port of `internal/reconcile`'s skeletonRank.
 */
export function skeletonRank(role: string): number {
  switch (role) {
    case "review-epic":
      return 1;
    case "closeout-epic":
      return 2;
    default:
      return 0;
  }
}

/**
 * Maps a tracker task onto contracts/job-protocol.json's closed role
 * vocabulary — a task nobody classified is implement-tick.
 */
export function roleOf(task: GraphTask): string {
  let role = task.role ?? "";
  if (role === "") role = task.type ?? "";
  role = role.toLowerCase().trim();
  switch (role) {
    case "review":
    case "review-epic":
      return "review-epic";
    case "closeout":
    case "close-out":
    case "closeout-epic":
      return "closeout-epic";
    case "plan":
    case "plan-epic":
      return "plan-epic";
    default:
      return "implement-tick";
  }
}

/**
 * The plan: the graph's waves flattened into a dispatch order — work before
 * review before closeout, earlier waves first, closed ticks skipped. The
 * port of `internal/reconcile`'s planFrom.
 */
export function planFrom(graph: Graph): PlanEntry[] {
  const out: PlanEntry[] = [];
  const seen = new Set<string>();
  for (const wave of graph.waves ?? []) {
    for (const task of wave.tasks ?? []) {
      if (seen.has(task.id) || task.status === "closed") continue;
      seen.add(task.id);
      out.push({
        tick_id: task.id,
        title: task.title,
        wave: wave.wave,
        role: roleOf(task),
        priority: task.priority,
        order: 0,
      });
    }
  }
  out.sort((a, b) => {
    const ra = skeletonRank(a.role);
    const rb = skeletonRank(b.role);
    if (ra !== rb) return ra - rb;
    return a.wave - b.wave;
  });
  out.forEach((entry, i) => {
    entry.order = i + 1;
  });
  return out;
}

/** The branch one attempt commits to — the identity the local run uses too. */
export function writeRefFor(runID: string, tickID: string, attempt: number): string {
  return `refs/heads/ticfac/run-${runID}/tick-${tickID}/attempt-${attempt}`;
}

/**
 * The number a new dispatch takes. Attempt numbers are RUN-WIDE identity,
 * not per-tick ordinals — the port of `internal/reconcile`'s rule: a number
 * names its branch, its durable marker and every record of the dispatch.
 */
export function nextAttemptNumber(attempts: Array<{ attempt: number }>): number {
  let number = attempts.length + 1;
  for (const existing of attempts) {
    if (existing.attempt >= number) number = existing.attempt + 1;
  }
  return number;
}

/** How many dispatches this tick has had — the tick's own TRY, not identity. */
export function tryOf(
  attempts: Array<{ attempt: number; tick_id: string }>,
  tick: string,
  number: number,
): number {
  let try_ = 1;
  for (const existing of attempts) {
    if (existing.tick_id === tick && existing.attempt < number) try_ += 1;
  }
  return try_;
}

/**
 * Whether a tick's deliverable is an ANSWER rather than a change — the port
 * of `internal/reconcile`'s `isRoleJob` (profiles.go): review and closeout
 * run through the same executor as any implementation tick, but what the
 * reconciler acts on is the answer they return, and that answer is what the
 * run-state contract's decision record exists to hold.
 */
export function isRoleJob(role: string): boolean {
  return role === "review-epic" || role === "closeout-epic";
}

/**
 * The number a new recorded exchange takes. Decision numbers are run-wide
 * identity, the same rule attempt numbers answer to: a number names its
 * record and every citation of the exchange.
 */
export function nextDecisionNumber(decisions: Array<{ decision: number }>): number {
  let number = decisions.length + 1;
  for (const existing of decisions) {
    if (existing.decision >= number) number = existing.decision + 1;
  }
  return number;
}

/** The minimal shape a recorded exchange is adopted by. */
type DecisionKey = {
  decision: number;
  role: string;
  request: Record<string, unknown>;
  response: Record<string, unknown>;
};

/**
 * The recorded exchange for one attempt, if the run branch holds one — keyed
 * by tick, attempt AND role, so a redispatch under a new number can never be
 * mistaken for the previous attempt's answer.
 */
function decisionFor(
  decisions: DecisionKey[],
  role: string,
  tickID: string,
  attempt: number,
): DecisionKey | null {
  for (const existing of decisions) {
    if (existing.role !== role) continue;
    if (existing.request.tick_id !== tickID) continue;
    if (existing.request.attempt !== attempt) continue;
    return existing;
  }
  return null;
}

/**
 * Whether an executor report carries the shape the settle acts on. The
 * collect of a role job IS the answer this host records — an answer outside
 * the closed vocabulary is not recorded as a decision, and settles as an
 * attempt that left nothing, the same closed-envelope rule the local
 * `collectRole` holds a role result to.
 */
function reportIsShaped(report: {
  outcome?: unknown;
  commits?: unknown;
  detail?: unknown;
}): report is AttemptReport {
  return (
    (report.outcome === "done" || report.outcome === "blocked" || report.outcome === "failed") &&
    typeof report.commits === "number" &&
    report.commits >= 0 &&
    typeof report.detail === "string" &&
    report.detail !== ""
  );
}

/**
 * The phase a recorded exchange names — the same mapping the local
 * reconciler's `phaseFor` makes, and the same closed vocabulary
 * `$defs.phase` pins.
 */
function decisionPhase(role: string): "review" | "closeout" {
  return role === "review-epic" ? "review" : "closeout";
}

// ----------------------------------------------------------- the reconciler ---

export type ReconcilerDeps = {
  client: TrackerClient;
  store: RunStateStore;
  executor?: AttemptExecutor;
  /** Wires the merge-and-gate half; absent means the recorded boundary above. */
  integration?: IntegrationHost;
  /**
   * The code-hosting surface the PR + CI close-out rule demands (tick cxk):
   * find/open the epic PR, read CI on a commit, carry the review's verdict
   * and the run's findings onto the PR. Absent with a rule declared is a
   * typed refusal, never a silent ungated close — the same fail-closed
   * answer `internal/reconcile` gives a build with no forge configured.
   */
  pullRequests?: PullRequests;
  /** The ref the epic PR asks to merge into; GitHub's `main` convention by default. */
  baseRef?: string;
  /** The clock the CI holds' bound measures; real time by default. */
  now?: () => Date;
  /** How long a held close-out waits on CI before refusing; the Go gate's own default. */
  gateTimeoutMs?: number;
  provenance: import("./run-state-store").Provenance;
  /** The dispatch window; 0 or absent means the graph's configured width. */
  maxParallel?: number;
};

/**
 * How long a held close-out waits on CI before refusing — the port of the
 * Go reconciler's `DefaultGateTimeout`, and for the same reason: a pending
 * CI is a wait the run BOUNDS, because a CI run on the PR is a gate this
 * run waits on rather than one it runs.
 */
export const DEFAULT_GATE_TIMEOUT_MS = 60 * 60 * 1000;

/** What one reconcile pass learned: the state to checkpoint, and the evidence. */
export type PassResult = {
  terminal: boolean;
  state: Checkpoint["state"];
  reason: string;
  /** The ticks dispatched this pass, with their attempt numbers. */
  dispatched: Array<{ tick_id: string; attempt: number }>;
};

/**
 * The reconciler, as a loop of passes over durable state.
 *
 * A pass is RESUMABLE BY CONSTRUCTION: it takes nothing from memory that the
 * run branch does not hold — the checkpoint's tick states, the attempt
 * markers' handles, the tracker's own answers. That is what makes a Workflow
 * restart mid-run a resume: the engine replays the completed steps, the next
 * pass re-derives the world, and an in-flight attempt is ADOPTED by identity
 * (its marker, its handle) rather than dispatched over — the property the
 * local reconciler earned the hard way (the 7zs attempt-22 incident in
 * `.tick/learnings.md`).
 */
export class EpicReconciler {
  readonly #deps: ReconcilerDeps;
  #sequence = 0;

  constructor(deps: ReconcilerDeps) {
    this.#deps = deps;
  }

  /** The checkpoint's rows, seeded for plan entries the run has not seen. */
  #seed(checkpoint: Checkpoint | null, plan: PlanEntry[]): Map<string, TickState> {
    const rows = new Map<string, TickState>();
    for (const ts of checkpoint?.ticks ?? []) {
      rows.set(ts.tick_id, { ...ts });
    }
    for (const entry of plan) {
      if (!rows.has(entry.tick_id)) {
        rows.set(entry.tick_id, { tick_id: entry.tick_id, state: "ready" });
      }
    }
    return rows;
  }

  /**
   * Settles the rows the plan no longer carries because the tracker already
   * closed them — the close-to-checkpoint window the Go reconciler closes by
   * asking the tracker, never by trusting a dead incarnation to have
   * written the row.
   */
  async #settleClosedRows(rows: Map<string, TickState>, plan: PlanEntry[]): Promise<boolean> {
    const planned = new Set(plan.map((entry) => entry.tick_id));
    let changed = false;
    for (const [tickID, row] of [...rows]) {
      if (planned.has(tickID) || row.state === "closed") continue;
      const current = await this.#deps.client.show(tickID);
      if (current !== null && current.status === "closed") {
        rows.set(tickID, { ...row, state: "closed" });
        changed = true;
      }
    }
    return changed;
  }

  async #checkpoint(
    state: Checkpoint["state"],
    reason: string,
    rows: Map<string, TickState>,
  ): Promise<void> {
    this.#sequence += 1;
    const ticks = [...rows.values()].sort((a, b) => (a.tick_id < b.tick_id ? -1 : 1));
    const outcome = await this.#deps.store.writeCheckpoint({
      state,
      reason,
      sequence: this.#sequence,
      ticks,
      provenance: this.#deps.provenance,
    });
    if (outcome.state === "conflict_stale_sha" || outcome.state === "conflict_exists") {
      // Another writer moved the run's state. The ref is the authority; the
      // next pass re-derives from it. A conflict is not retried blindly in
      // the same pass because every decision after it would rest on a stale
      // read — the same rule the contract's CAS sequences pin.
      throw new Error(`checkpoint conflict: ${outcome.detail}`);
    }
  }

  /**
   * Settles a REPORTED tick: gate (the close-out's own CI gate, when the tick
   * is the close-out and the repository declares the rule), integrate, then
   * close — or the recorded boundary/refusal that stops the run. Returns the
   * terminal result when the run stops, null when the tick settled.
   *
   * The close-out's close gate runs BEFORE the integration call, one step
   * left of where the local reconciler puts it (there: integrated gate, then
   * CI gate): a held gate leaves this row `reported` and is re-derived every
   * pass, and an integration that ran on every held pass would re-perform
   * the merge the row says is still pending. CI's verdict is about the tree
   * either way, and green is the only path that reaches the integrate — so
   * the gate is never satisfied by work the gate refused.
   */
  async #settleReported(
    tickID: string,
    attempt: number,
    rows: Map<string, TickState>,
    dispatchedThisPass: Array<{ tick_id: string; attempt: number }>,
    plan: PlanEntry[],
    checkpoint: Checkpoint | null,
  ): Promise<PassResult | null> {
    const { client, store } = this.#deps;
    const role = plan.find((entry) => entry.tick_id === tickID)?.role ?? "implement-tick";
    // reconciler-decision:D31:begin:gate-order — the close-out's close gate
    // runs BEFORE the integration call, one step left of the local order
    // (there: integrated gate, then CI gate), so a held gate re-derives
    // without re-performing the merge (decisions/reconciler-parity.json,
    // D31).
    if (role === "closeout-epic" && (await this.#closeoutRule()).declared) {
      // The close-out's OTHER CI gate (the sqx position, ported): the
      // admission's green is not evidence about the head the close-out's own
      // commits moved, so the CLOSE re-derives CI from the PR.
      const gate = await this.#gateCloseoutClose(tickID, rows, checkpoint, dispatchedThisPass);
      if (gate !== null) return gate;
    }
    // reconciler-decision:D31:end:gate-order
    // reconciler-decision:D31:begin:integration-boundary — no integration
    // host, no close: the run stops naming the boundary rather than writing
    // `completed` over unproven work (decisions/reconciler-parity.json, D31).
    if (this.#deps.integration === undefined) {
      // The recorded decision (module header): no integration host, no close,
      // no false completion. The run stops naming the boundary rather than
      // writing `completed` over unproven work.
      rows.set(tickID, { tick_id: tickID, state: "reported", attempt });
      const reason =
        `${tickID} reported commits on ${writeRefFor(store.runID, tickID, attempt)}; this host ` +
        `cannot integrate and gate them yet (the serialized publisher and the sandbox executor ` +
        `are this phase's later items), so the run stops rather than closing the tick unproven`;
      await this.#checkpoint("failed", reason, rows);
      return {
        terminal: true,
        state: "failed",
        reason: `${tickID} reported; no integration host on this Workflow`,
        dispatched: dispatchedThisPass,
      };
    }
    // reconciler-decision:D31:end:integration-boundary
    const outcome = await this.#deps.integration.integrate({
      tick_id: tickID,
      attempt,
      write_ref: writeRefFor(store.runID, tickID, attempt),
    });
    if (!outcome.integrated) {
      rows.set(tickID, { tick_id: tickID, state: "rejected" });
      const reason = `integration of ${tickID} (attempt ${attempt}) refused: ${outcome.reason}`;
      await this.#checkpoint("failed", reason, rows);
      return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
    }
    const closed = await client.close(tickID, { reason: "implemented and integrated" });
    if (closed.state === "refused") {
      // A human gate holds the tick: routed to `awaiting` by the client, the
      // row stays reported, and the run says so — a hold a person must see,
      // never a quiet failure of the work.
      rows.set(tickID, { tick_id: tickID, state: "reported", attempt });
      const reason = `closing ${tickID} was refused: ${closed.detail}; the gate is a person's to clear`;
      await this.#checkpoint("failed", reason, rows);
      return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
    }
    rows.set(tickID, { tick_id: tickID, state: "closed", attempt });
    return null;
  }

  #liveCount(rows: Map<string, TickState>): number {
    let live = 0;
    for (const row of rows.values()) {
      if (row.state === "dispatched") live += 1;
    }
    return live;
  }

  #window(graph: Graph): number {
    if (this.#deps.maxParallel !== undefined && this.#deps.maxParallel > 0) {
      return this.#deps.maxParallel;
    }
    return graph.dispatch.max_parallel;
  }

  // ---------------------------------------- the CI-gated close-out (cxk) ---

  /** The rule, read once per incarnation the way the Go `New` reads it. */
  #rule: CloseoutRule | null = null;
  #ruleRead = false;

  async #closeoutRule(): Promise<CloseoutRule> {
    if (!this.#ruleRead) {
      this.#rule = await readCloseoutRule(this.#deps.store.store);
      this.#ruleRead = true;
    }
    return this.#rule ?? { declared: false, ciWorkflow: "", stated: "" };
  }

  /** The branch the run integrates on — the ref every durable record lives at. */
  #branch(): string {
    return this.#deps.client.ref;
  }

  // reconciler-decision:D32:begin:baseref — this host's PR base is named by
  // the submitter (default `main`); the local close-out resolves the remote's
  // HEAD instead (decisions/reconciler-parity.json, D32).
  #baseRef(): string {
    return this.#deps.baseRef ?? "main";
  }
  // reconciler-decision:D32:end:baseref

  #now(): Date {
    return this.#deps.now?.() ?? new Date();
  }

  #gateTimeoutMs(): number {
    return this.#deps.gateTimeoutMs ?? DEFAULT_GATE_TIMEOUT_MS;
  }

  /** The bounds a refusal names the wait in — a person reads a number, not a constant. */
  #humanBound(): string {
    return `${Math.round(this.#gateTimeoutMs() / 60_000)} minutes`;
  }

  /**
   * The body the epic PR carries, recomposed from the durable record — the
   * `closeoutPRBody` port. The record, not the caller's memory, is the
   * input: a resumed close-out composes the same body from the same
   * decisions and findings, which is the idempotence of the write.
   */
  async #composeBody(): Promise<{ body: string; findings: number }> {
    const { store } = this.#deps;
    const rule = await this.#closeoutRule();
    const decisions = await store.decisions();
    const findings = await readFindings(store.store, store.runID);
    const body = composePRBody({
      runID: store.runID,
      branch: this.#branch(),
      rule,
      review: finalReviewOf(decisions),
      findings,
    });
    return { body, findings: findings.length };
  }

  /**
   * One CI hold, across passes. The first held pass checkpoints `gating`
   * with the hold's own reason; every later pass that derives the SAME hold
   * is an OBSERVATION and writes nothing, so the checkpoint's `updated_at`
   * stays the durable start the bound measures — a wait that rewrites its
   * own clock is a wait that never expires. Past the bound the run refuses,
   * saying what a person must do: re-run the epic once CI concludes.
   */
  async #holdOnCI(
    reason: string,
    rows: Map<string, TickState>,
    checkpoint: Checkpoint | null,
    dispatched: Array<{ tick_id: string; attempt: number }>,
    boundReason: string,
    boundEffect?: () => void,
  ): Promise<PassResult> {
    if (checkpoint !== null && checkpoint.state === "gating" && checkpoint.reason === reason) {
      if (this.#now().getTime() - Date.parse(checkpoint.updated_at) > this.#gateTimeoutMs()) {
        boundEffect?.();
        await this.#checkpoint("failed", boundReason, rows);
        return { terminal: true, state: "failed", reason: boundReason, dispatched };
      }
      // The same hold, re-derived: an observation, and observations write
      // nothing — the ref keeps the hold's start.
    } else {
      await this.#checkpoint("gating", reason, rows);
    }
    return { terminal: false, state: "gating", reason, dispatched };
  }

  /** Whether the plan's review entry has not settled — the close-out follows it. */
  #reviewUnsettled(plan: PlanEntry[], rows: Map<string, TickState>): boolean {
    for (const entry of plan) {
      if (entry.role !== "review-epic") continue;
      if (rows.get(entry.tick_id)?.state !== "closed") return true;
    }
    return false;
  }

  /**
   * The close-out phase's ADMISSION precondition — the `admitCloseout` port,
   * one shape over: read the rule → PR exists? → open one if not, carrying
   * the record → CI green? → ADMIT the close-out, or refuse typed, naming
   * which half is unmet (the halves send the next repair somewhere
   * different: a missing surface at the host, an absent CI at the workflow's
   * triggers, a red CI at the failing job the message names, a pending CI at
   * the clock).
   *
   * Returns null when the close-out is admitted; a PassResult for a hold or
   * a typed refusal — the close-out job is claimed and dispatched only
   * behind an admission, never like an implement tick.
   */
  async #admitCloseout(
    rows: Map<string, TickState>,
    checkpoint: Checkpoint | null,
    dispatched: Array<{ tick_id: string; attempt: number }>,
  ): Promise<PassResult | null> {
    const { store } = this.#deps;
    const rule = await this.#closeoutRule();
    const refuse = async (reason: string): Promise<PassResult> => {
      await this.#checkpoint("failed", reason, rows);
      return { terminal: true, state: "failed", reason, dispatched };
    };

    const forge = this.#deps.pullRequests;
    if (forge === undefined) {
      // The fail-closed answer a build with no code-hosting surface gives a
      // rule-declaring repository: the refusal is where an operator learns
      // to provision one, not a silent ungated close-out.
      return refuse(
        `the repository declares the PR + CI close-out rule, and this Workflow has no ` +
          `code-hosting surface configured to open or read the epic PR: ${rule.stated}`,
      );
    }

    // The record half: the body the PR carries, composed from the durable
    // state before the branch is asked about, so the open hands it to the PR
    // the moment it exists and the found path rewrites it — one idempotence
    // argument for both (the 4sb rule).
    let composed: { body: string; findings: number };
    try {
      composed = await this.#composeBody();
    } catch (error) {
      return refuse(
        `the epic PR cannot carry the final review's verdict and the run's findings: the ` +
          `run's own record could not be read to compose them: ${String(error)}. ` +
          `The rule the repository declares is: ${rule.stated}`,
      );
    }

    // The PR half. Find before open, so a resumed run cut between the two
    // finds the PR the previous incarnation opened rather than opening a
    // second one.
    const head = this.#branch();
    const base = this.#baseRef();
    let pr: PullRequest;
    try {
      const found = await forge.find(head, base);
      if (found === null) {
        pr = await forge.open({
          headRef: head,
          baseRef: base,
          title: `epic ${store.epicID}: integrate ${head}`,
          body: composed.body,
        });
      } else {
        pr = found;
        await forge.updateBody(pr, composed.body);
      }
    } catch (error) {
      return refuse(
        `the close-out cannot be admitted: the code-hosting surface could not open or write ` +
          `the epic PR for ${head} (→ ${base}): ${String(error)}. ` +
          `The rule the repository declares is: ${rule.stated}`,
      );
    }
    // The PR fact lands in the checkpoint the moment it is known, so a
    // resumed run — and a person reading the run's record — sees where the
    // PR lives rather than rediscovering it — and only then: a pass-based
    // admission re-runs on every held pass, and rewriting the fact each
    // time would rewrite the hold's own durable start (its `updated_at`).
    // This is a state change the first time, an observation after.
    if (checkpoint === null || !checkpoint.reason.includes(`the epic PR #${pr.number}`)) {
      await this.#checkpoint(
        "running",
        `the epic PR #${pr.number} (${head} → ${base}) is open and carries the final review's ` +
          `verdict and the ${composed.findings} finding(s) this run drafted; the close-out of ` +
          `${store.epicID} waits on CI`,
        rows,
      );
    }

    // The CI half, re-derived from the PR on every admission — CI's answer
    // changes with every push, and the head this run remembers is stale the
    // moment it checkpoints (the 9da shape `ciForTree` exists for).
    for (;;) {
      let verdict: Awaited<ReturnType<typeof ciForTree>>;
      try {
        verdict = await ciForTree(forge, pr);
      } catch (error) {
        return refuse(
          `the close-out cannot be admitted: CI on the epic PR #${pr.number} could not be ` +
            `read: ${String(error)}. The rule the repository declares is: ${rule.stated}`,
        );
      }
      switch (verdict.report.state) {
        case "green":
          await this.#checkpoint(
            "running",
            `CI is green on ${ciSubject(verdict.sha, verdict.isHead, pr)}; the close-out of ` +
              `${store.epicID} is admitted`,
            rows,
          );
          return null; // admitted: the dispatch loop claims the close-out
        case "red": {
          const failing = verdict.report.failing.join(", ");
          return refuse(
            `CI is red on the epic PR #${pr.number}: the failing job is ${failing}. The ` +
              `close-out of ${store.epicID} is not admitted until CI is green on the PR, and ` +
              `the rule the repository declares is: ${rule.stated}`,
          );
        }
        case "none":
          return refuse(
            `no CI has run on the epic PR #${pr.number}: ${rule.ciWorkflow} has produced no ` +
              `check runs on its head, so the close-out's precondition is unsatisfiable by ` +
              `waiting — the workflow may not trigger on pull_request at all. The rule the ` +
              `repository declares is: ${rule.stated}`,
          );
        case "pending":
          return await this.#holdOnCI(
            `CI on the epic PR #${pr.number} is pending; the close-out of ${store.epicID} is held`,
            rows,
            checkpoint,
            dispatched,
            `CI on the epic PR #${pr.number} was still pending ${this.#humanBound()} after the ` +
              `close-out began waiting on it, so this run does not admit the close-out of ` +
              `${store.epicID}: re-run the epic once CI concludes, and this admission is ` +
              `re-derived from the PR, not rediscovered. The rule the repository declares ` +
              `is: ${rule.stated}`,
          );
      }
    }
  }

  /**
   * The close-out's OTHER CI gate — the `gateCloseoutClose` port. The
   * admission covers the head as it stood when the close-out STARTS; the
   * close-out's own commits — the retro, the learnings it compacts — then
   * move that head, so the CLOSE re-derives CI from the PR rather than
   * trusting the admission's green. One rule, two gates, one gap.
   *
   * Called with the close-out tick reported; returns null when the close is
   * gated green (the close proceeds), a PassResult for a hold or a typed
   * refusal (the tick does not close behind either).
   */
  async #gateCloseoutClose(
    tickID: string,
    rows: Map<string, TickState>,
    checkpoint: Checkpoint | null,
    dispatched: Array<{ tick_id: string; attempt: number }>,
  ): Promise<PassResult | null> {
    const { store } = this.#deps;
    const rule = await this.#closeoutRule();
    const refuse = async (reason: string): Promise<PassResult> => {
      await this.#checkpoint("failed", reason, rows);
      return { terminal: true, state: "failed", reason, dispatched };
    };

    const forge = this.#deps.pullRequests;
    if (forge === undefined) {
      // Defensive, and the same fail-closed answer the admission keeps.
      return refuse(
        `the repository declares the PR + CI close-out rule, and this Workflow has no ` +
          `code-hosting surface to gate the close-out's close on: ${rule.stated}`,
      );
    }

    // The PR is looked up rather than remembered from the admission, for the
    // same reason the admission looks before it opens: the forge is the PR's
    // authority the way origin is the run's.
    const head = this.#branch();
    const base = this.#baseRef();
    let pr: PullRequest | null;
    try {
      pr = await forge.find(head, base);
    } catch (error) {
      return refuse(
        `the close-out of ${store.epicID} cannot be gated on CI: the code-hosting surface ` +
          `could not say whether the epic PR still exists for ${head} (→ ${base}): ` +
          `${String(error)}. The rule the repository declares is: ${rule.stated}`,
      );
    }
    if (pr === null) {
      return refuse(
        `the epic PR for ${head} (→ ${base}) is gone: the close-out's commits are on ${head}, ` +
          `but a close-out whose rule declares a PR does not close behind a PR that no longer ` +
          `exists. Re-run the epic: the admission re-opens the PR and the close is gated again`,
      );
    }

    for (;;) {
      let verdict: Awaited<ReturnType<typeof ciForTree>>;
      try {
        verdict = await ciForTree(forge, pr);
      } catch (error) {
        return refuse(
          `the close-out of ${store.epicID} cannot be gated on CI: CI on the epic PR ` +
            `#${pr.number} could not be read: ${String(error)}. The rule the repository ` +
            `declares is: ${rule.stated}`,
        );
      }
      switch (verdict.report.state) {
        case "green": {
          // The last write the run owns (the 4sb rule, at the close gate): the
          // close-out's OWN attempt can have drafted a finding the
          // admission's body predated, and a person must not merge behind a
          // body the run's final state contradicts. The write is an
          // overwrite, so a resumed close-out that reaches this gate again
          // rewrites the same view and adds nothing.
          try {
            const composed = await this.#composeBody();
            await forge.updateBody(pr, composed.body);
          } catch (error) {
            return refuse(
              `the epic PR #${pr.number} cannot carry the final review's verdict and the ` +
                `run's findings at the close-out's close: ${String(error)}. The rule the ` +
                `repository declares is: ${rule.stated}`,
            );
          }
          return null; // gated green: the close proceeds
        }
        case "red": {
          const failing = verdict.report.failing.join(", ");
          rows.set(tickID, { tick_id: tickID, state: "rejected" });
          return refuse(
            `CI is red on the epic PR #${pr.number} on the head that includes the close-out's ` +
              `own commits: the failing job is ${failing}. The close-out of ${store.epicID} is ` +
              `NOT closed behind it, and the rule the repository declares is: ${rule.stated}. ` +
              `The repair is what the failing job names — the retro, the learnings or the ` +
              `records the close-out itself wrote: fix them, push to ${head}, and run the epic ` +
              `again under this run id — the close gate re-derives CI from the PR, it does not ` +
              `trust the admission's green`,
          );
        }
        case "none":
        case "pending": {
          // A pending CI — and a silent one, in the window after this run's
          // own push — is a hold here, not a failure: the run itself just
          // moved the head CI is asked about. Only a silence that survives
          // the bound is the workflow's failure again.
          const silent = verdict.report.state === "none";
          const reason = silent
            ? `CI on the epic PR #${pr.number} has produced no check runs on the head that ` +
              `includes the close-out's own commits; the close-out of ${store.epicID} is held`
            : `CI on the epic PR #${pr.number} is pending on the head that includes the ` +
              `close-out's own commits; the close-out of ${store.epicID} is held`;
          const what = silent ? "had still produced no check runs" : "was still pending";
          return await this.#holdOnCI(
            reason,
            rows,
            checkpoint,
            dispatched,
            `CI on the epic PR #${pr.number} ${what} ${this.#humanBound()} after the close-out ` +
              `began waiting on the head that includes its own commits, so this run does not ` +
              `close the close-out of ${store.epicID}: re-run the epic once CI concludes, and ` +
              `this gate is re-derived from the PR, not rediscovered. The rule the repository ` +
              `declares is: ${rule.stated}`,
            // The refused tick reads as rejected in the run's own record, the
            // same answer the local gate's `setTick(tick, "rejected")` gives.
            () => rows.set(tickID, { tick_id: tickID, state: "rejected" }),
          );
        }
      }
    }
  }

  /**
   * One pass. Everything it reads, it reads from the durable authorities —
   * the tracker through the contract client, the run branch through the
   * run-state store — so the pass after a Workflow restart is this pass.
   */
  async reconcilePass(): Promise<PassResult> {
    const { store } = this.#deps;

    const checkpoint = await store.checkpoint();
    if (checkpoint !== null) {
      this.#sequence = checkpoint.sequence;
      // Recovery is a fetch and then a read. A checkpoint that is terminal
      // and not failed is a run somebody completed or cancelled; replaying
      // it must not restart it. A FAILED one is resumable: the same run id,
      // the same attempt numbering, and nothing here trusts the checkpoint's
      // own account of a tick — the tracker is asked whether it is closed,
      // and the attempt marker decides whether a dispatch is adopted.
      if (terminalState(checkpoint.state)) {
        return {
          terminal: true,
          state: checkpoint.state,
          reason: checkpoint.reason,
          dispatched: [],
        };
      }
    }

    // reconciler-decision:D33:begin:plan-cadence — every pass re-derives
    // the world from the durable authorities, plan included; the local host
    // replans only when an attempt closes (decisions/reconciler-parity.json,
    // D33).
    const graph = await this.#deps.client.graph(store.epicID);
    if (graph === null) {
      return {
        terminal: true,
        state: "failed",
        reason: `epic ${store.epicID} is not readable`,
        dispatched: [],
      };
    }
    const plan = planFrom(graph);
    // reconciler-decision:D33:end:plan-cadence
    if (plan.length === 0) {
      return {
        terminal: true,
        state: "failed",
        reason: `epic ${store.epicID} has no dispatchable tick`,
        dispatched: [],
      };
    }

    const rows = this.#seed(checkpoint, plan);
    await this.#settleClosedRows(rows, plan);

    const result = await this.#work(rows, plan, graph, checkpoint);
    if (result.terminal) {
      await this.#checkpoint(result.state, result.reason, rows);
    }
    return result;
  }

  /** The settle/dispatch half of a pass, over assembled rows. */
  async #work(
    rows: Map<string, TickState>,
    plan: PlanEntry[],
    graph: Graph,
    checkpoint: Checkpoint | null,
  ): Promise<PassResult> {
    const { client, store, executor } = this.#deps;
    const dispatchedThisPass: Array<{ tick_id: string; attempt: number }> = [];

    // A host with no executor refuses dispatches BEFORE anything is claimed
    // or recorded — the same place `internal/reconcile` refuses a profile it
    // cannot build an executor for: "a build failure here is a refusal before
    // the tick is claimed and before any record says a dispatch happened".
    if (executor === undefined) {
      const reason =
        "no attempt executor is configured on this Workflow (the sandbox compatibility executor exists — a missing container binding, epic base or factory URL is what is missing, and the deploy log names which); " +
        "refusing to dispatch rather than recording a dispatch nobody would run";
      await this.#checkpoint("failed", reason, rows);
      return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
    }

    // ---- settle: every in-flight attempt, from durable evidence.
    let stateChanged = false; // any row mutation this pass — settle or dispatch
    const decisions = (await store.decisions()) as DecisionKey[]; // recorded exchanges, read once and shadowed below
    for (const [tickID, row] of [...rows]) {
      if (row.state === "reported") {
        // Reported by an earlier pass (or an earlier incarnation): the work
        // exists, and the close it never reached runs here.
        const settle = await this.#settleReported(
          tickID,
          row.attempt ?? 0,
          rows,
          dispatchedThisPass,
          plan,
          checkpoint,
        );
        if (settle !== null) return settle;
        stateChanged = true;
        continue;
      }
      if (row.state !== "dispatched") continue;
      const attempt = row.attempt ?? 0;
      const marker = await store.attempt(attempt);
      if (marker === null) {
        // The checkpoint says dispatched and no marker says a dispatch
        // happened. The marker is the idempotency truth (create-if-absent,
        // on the branch): settle the row back to ready and let the dispatch
        // half try again — never dispatch over a number whose marker is
        // absent, because a slow create would then race a second worker.
        rows.set(tickID, { tick_id: tickID, state: "ready" });
        stateChanged = true;
        continue;
      }
      if (marker.tick_id !== tickID) {
        await this.#checkpoint(
          "failed",
          `attempt ${attempt} of this run belongs to ${marker.tick_id}, not ${tickID}; the state and the markers disagree`,
          rows,
        );
        return {
          terminal: true,
          state: "failed",
          reason: `attempt ${attempt} belongs to ${marker.tick_id}, not ${tickID}`,
          dispatched: dispatchedThisPass,
        };
      }
      // The exchange's role is the plan's: a review or closeout attempt's
      // collect is the role-job EXCHANGE — the answer the local reconciler
      // records as a decision before it decides anything on it. Here that
      // answer lands on the run branch too, so a restart re-reads it instead
      // of re-asking a model it already paid.
      const role = plan.find((entry) => entry.tick_id === tickID)?.role ?? "implement-tick";
      const recorded = isRoleJob(role) ? decisionFor(decisions, role, tickID, attempt) : null;
      let report: AttemptReport;
      if (recorded !== null && reportIsShaped(recorded.response)) {
        // A decision already names this exact exchange: re-READ it, never
        // re-ask it. This is what makes a Workflow restart recoverable with
        // a wiped executor — the answer is on the branch (SPEC §10.4).
        report = recorded.response;
      } else {
        const status = await executor.inspect(marker.job_handle); // adopted by identity
        if (status.state === "running") continue;
        report = await executor.collect(marker.job_handle);
        if (isRoleJob(role) && reportIsShaped(report)) {
          // The request and the validated response land together, with
          // provenance — the contract's decision record, create-if-absent:
          // a refused create is another incarnation's record standing, a
          // decision is never rewritten, and this pass proceeds on the
          // answer it already collected either way. A shadow keeps later
          // numbers in this pass honest against what the ref may hold.
          const number = nextDecisionNumber(decisions);
          const outcome = await store.recordDecision({
            decision: number,
            role: role as "review-epic" | "closeout-epic",
            request: {
              tick_id: tickID,
              epic_id: store.epicID,
              attempt,
              job_id:
                typeof marker.job_handle.job_id === "string" ? marker.job_handle.job_id : null,
              write_ref: writeRefFor(store.runID, tickID, attempt),
              role,
            },
            response: { outcome: report.outcome, commits: report.commits, detail: report.detail },
            validated: true,
            requested_at: marker.dispatched_at,
            answered_at: new Date().toISOString(),
            provenance: { ...marker.provenance, phase: decisionPhase(role) },
          });
          if (outcome.state === "created" || outcome.state === "conflict_exists") {
            decisions.push({
              decision: number,
              role: role as "review-epic" | "closeout-epic",
              request: { tick_id: tickID, attempt },
              response: { outcome: report.outcome, commits: report.commits, detail: report.detail },
            });
          }
        }
      }
      if (report.outcome === "done" && report.commits > 0) {
        rows.set(tickID, { tick_id: tickID, state: "reported", attempt });
        const settle = await this.#settleReported(
          tickID,
          attempt,
          rows,
          dispatchedThisPass,
          plan,
          checkpoint,
        );
        if (settle !== null) return settle;
        stateChanged = true;
      } else if (report.outcome === "blocked") {
        // A blocked attempt is an answer a person must read: rejected here,
        // and the refusal stops the run rather than draining a window whose
        // tree nothing stands behind.
        rows.set(tickID, { tick_id: tickID, state: "rejected" });
        const reason = `${tickID} (attempt ${attempt}) reported BLOCKED: ${report.detail}`;
        await this.#checkpoint("failed", reason, rows);
        return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
      } else {
        // An attempt that left nothing is redispatched: a new attempt
        // number, the same identity — the local run's resume rule.
        rows.set(tickID, { tick_id: tickID, state: "ready", attempt });
        stateChanged = true;
      }
    }

    // ---- dispatch: the window, one admission at a time.
    const width = this.#window(graph);
    let live = this.#liveCount(rows);
    const markers = await store.attempts();
    for (const entry of plan) {
      // reconciler-decision:D26:begin:admission — this host's admission is
      // the width and the row's readiness only: no wave boundary, no blocker
      // read, no wave-composition refusal (decisions/reconciler-parity.json,
      // D26).
      if (width > 0 && live >= width) break;
      const row = rows.get(entry.tick_id);
      if (row === undefined || row.state !== "ready") continue;
      // reconciler-decision:D26:end:admission

      // A marker this tick holds that no row admits: a dispatch by an
      // incarnation that died between writing the marker and checkpointing
      // the row. The marker is the durable evidence — adopt the attempt by
      // identity rather than dispatching over it.
      const orphaned = markers.filter(
        (m) => m.tick_id === entry.tick_id && m.attempt > (row.attempt ?? 0),
      );
      if (orphaned.length > 0) {
        const latest = orphaned.reduce((a, b) => (a.attempt > b.attempt ? a : b));
        rows.set(entry.tick_id, {
          tick_id: entry.tick_id,
          state: "dispatched",
          attempt: latest.attempt,
        });
        stateChanged = true;
        live += 1;
        continue; // the next pass settles it through the evidence path
      }

      // The close-out's admission precondition (tick cxk, the admitCloseout
      // port): BEFORE the close-out job is claimed or dispatched, the rule
      // the repository declares is enforced — PR found or opened, the
      // review's verdict and the run's findings carried onto it, CI green.
      // An orphaned closeout marker skips this by continuing above: a
      // marker is durable evidence a PREVIOUS incarnation was admitted and
      // dispatched, and adoption is the resume of that dispatch, never a
      // second admission over it.
      // reconciler-decision:D27:begin:closeout-admission — the one hold-back
      // this host carries: the close-out's admission waits for the review to
      // settle; nothing runs alone here (decisions/reconciler-parity.json,
      // D27).
      if (entry.role === "closeout-epic" && (await this.#closeoutRule()).declared) {
        // The close-out follows the review: the body the PR must carry
        // names the FINAL review's verdict, and a review still in flight
        // has recorded none. The tracker declares this order (the
        // close-out's blocked_by); this host's window can reach the
        // close-out entry beside the review instead of after it, so the
        // admission waits for the review to settle rather than composing a
        // body that states an absence the run has not settled yet.
        if (this.#reviewUnsettled(plan, rows)) continue;
        const admission = await this.#admitCloseout(rows, checkpoint, dispatchedThisPass);
        if (admission !== null) return admission; // a hold, or a typed refusal
      }
      // reconciler-decision:D27:end:closeout-admission

      // reconciler-decision:D26:begin:claim-wait — the tracker's own
      // refusal is the barrier here: the claim is asked, and a full window is
      // a wait, where the local window never asks for what tk would refuse.
      const claim = await client.claim(entry.tick_id, `run-${store.runID}`);
      if (claim.state === "refused") {
        if (claim.reason === "wave_full") break; // a full window is a wait, not a failure
        const reason = `claiming ${entry.tick_id} was refused: ${claim.detail}`;
        await this.#checkpoint("failed", reason, rows);
        return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
      }
      // reconciler-decision:D26:end:claim-wait

      // Attempt numbers are RUN-WIDE identity, not per-tick ordinals: they
      // name the marker, the branch and every record of this dispatch. The
      // tick's own TRY is how many dispatches this tick has had — what a
      // worker receives as TICFAC_TRY.
      const number = nextAttemptNumber(markers);
      const try_ = tryOf(markers, entry.tick_id, number);
      const write_ref = writeRefFor(store.runID, entry.tick_id, number);
      const handle: AttemptHandle = {
        executor: "cloudflare-sandbox",
        job_id: `run-${store.runID}/tick-${entry.tick_id}/attempt-${number}`,
        attempt: number,
        try: try_,
        tick_id: entry.tick_id,
        role: entry.role,
        remote: "origin",
        resumed_from: null,
        write_ref,
      };

      // The marker is written BEFORE the job starts: create-if-absent, and a
      // refusal means the repository — not a lock — says this dispatch is
      // somebody else's, and the loser must not start a job.
      const markerOutcome = await store.recordAttempt({
        attempt: number,
        tick_id: entry.tick_id,
        dispatched_at: new Date().toISOString(),
        job_handle: handle,
        provenance: provenance({
          run_id: store.runID,
          tick_id: entry.tick_id,
          attempt: number,
          source_ref: "refs/heads/main",
          source_sha: "0".repeat(40),
          phase: "worker",
          // The contract's closed executor vocabulary: the sandbox
          // compatibility executor is this phase's item 4, and the dispatch
          // rides its name. The Workflow HOSTS the reconciler; it is not the
          // executor a job runs on.
          executor: "cloudflare-sandbox",
          role: entry.role,
        }),
      });
      if (markerOutcome.state !== "created") {
        // Another writer holds this number. Adopt it if it is THIS tick's;
        // recompute if it is not — attempt numbers are run-wide, so the
        // record that refused the create is not necessarily about this tick.
        const existing = await store.attempt(number);
        if (existing !== null && existing.tick_id === entry.tick_id) {
          rows.set(entry.tick_id, {
            tick_id: entry.tick_id,
            state: "dispatched",
            attempt: number,
          });
          stateChanged = true;
          live += 1;
          continue;
        }
        markers.push({
          schema_version: 0,
          attempt: number,
          tick_id: existing?.tick_id ?? entry.tick_id,
          dispatched_at: "",
          job_handle: {},
          provenance: store.provenance,
        });
        continue; // recompute the number against the ref as it stands now
      }
      markers.push({
        schema_version: 0,
        attempt: number,
        tick_id: entry.tick_id,
        dispatched_at: "",
        job_handle: handle,
        provenance: store.provenance,
      });

      const started = await executor.start({
        run_id: store.runID,
        epic_id: store.epicID,
        tick_id: entry.tick_id,
        attempt: number,
        role: entry.role,
        project: client.project,
        write_ref,
        base_ref: "refs/heads/main",
        title: entry.title,
      });
      if (started !== undefined) {
        Object.assign(handle, started);
      }

      // The handle start returned is RECORDED on the marker, not remembered
      // in the isolate (tick t5p). Job-protocol's start rule pins the order —
      // "Persist the JobSpec before addressing the executor, then record the
      // returned handle. A handle that was never persisted is a job nobody
      // can find after a restart." — and this host's executor is the reason
      // the rule exists: what start adds (the container's name, the work
      // process's id, the branch it pushes, the base the collect compares
      // against) is exactly what a later pass — and a restarted incarnation —
      // re-inspects the attempt by. Without this write every one of them
      // addressed a container nobody recorded, and the adoption-by-identity
      // the local reconciler earned the hard way was this host's words only.
      //
      // SHA-guarded and idempotent, so a replayed pass records it once, and
      // a marker another writer moved is re-read and retried on the fresh
      // read — never lost, never spun on: a conflict that survives a fresh
      // read is an operational problem to fail on, not a race to win.
      for (let tries = 0; ; tries += 1) {
        const recorded = await store.updateAttemptHandle(number, handle);
        if (recorded.state === "updated" || recorded.state === "no_change") break;
        if (recorded.state !== "conflict_stale_sha" || tries >= 3) {
          const detail =
            "detail" in recorded ? recorded.detail : `the write was refused (${recorded.state})`;
          throw new Error(
            `recording the handle of attempt ${number} (${entry.tick_id}) was refused: ` +
              `${detail}; the job is running but no later pass could re-inspect it`,
          );
        }
        // `conflict_stale_sha`: the marker moved under the write — re-read and
        // retry, the same fresh-read-and-retry the contract's CAS rules name.
      }
      rows.set(entry.tick_id, { tick_id: entry.tick_id, state: "dispatched", attempt: number });
      dispatchedThisPass.push({ tick_id: entry.tick_id, attempt: number });
      stateChanged = true;
      live += 1;
    }

    // ---- the pass's verdict.
    const allClosed = plan.every((entry) => rows.get(entry.tick_id)?.state === "closed");
    if (allClosed) {
      const reason = `every tick of ${store.epicID} is closed behind the integrated gate`;
      await this.#checkpoint("completed", reason, rows);
      return { terminal: true, state: "completed", reason, dispatched: dispatchedThisPass };
    }
    const rejected = plan.filter((entry) => rows.get(entry.tick_id)?.state === "rejected");
    if (rejected.length > 0) {
      const reason = `${rejected.map((e) => e.tick_id).join(", ")} did not pass; the run stopped`;
      return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
    }
    if (stateChanged || this.#sequence === 0) {
      const reason =
        live > 0 ? `${live} attempt(s) in flight; the window is open` : "the plan is admitted";
      await this.#checkpoint(live > 0 ? "running" : "dispatching", reason, rows);
    }
    return {
      terminal: false,
      state: live > 0 ? "running" : "dispatching",
      reason: `${live} in flight`,
      dispatched: dispatchedThisPass,
    };
  }
}

// -------------------------------------------------------- the Workflow host ---

export type EpicReconcilerParams = {
  /** The EpicRun's id — also the Workflow instance id: ONE Workflow per run. */
  run_id: string;
  epic_id: string;
  /** owner/name the run branch lives in. */
  project: string;
  /** The run branch: where `.tick/` records and `.ticfac/` state are pushed. */
  branch: string;
  /**
   * The epic base: the commit every worker container clones at and every
   * collect compares its branch against (tick k4s). The submitter names it
   * the way RunWorkflow's does — a reconciler without it cannot dispatch,
   * and the executor wiring refuses naming this field rather than booting
   * workers on nothing.
   */
  base_sha?: string;
  /** The dispatch window; 0 or absent for the repository's own declaration. */
  max_parallel?: number;
  /**
   * The dispatch lease's release credential, handed over by the submit route
   * (tick nu9). The Workflow renews it inside every pass step and releases it
   * on the way out, exactly as the Run Workflow did — the lease is the
   * project's single-arbiter answer (D4), and a driver that never renewed it
   * would let the room's alarm ignite a queued submission beside a live run.
   *
   * Absent means no lease to own: the engine tests' instances, or a Workflow
   * created by hand — nothing renews or releases what nobody holds.
   */
  lease_token?: string;
  /**
   * Who submitted the run, carried for the one call that needs it after
   * ignition — a reclaim of a lapsed lease records the run's requester on
   * the lease the room re-issues (tick oen). Absent is accepted; a reclaim
   * without it fails and the run stops naming that, rather than inventing a
   * requester.
   */
  requested_by?: string;
  /** The Workflow's poll cadence in ms; defaults to a keepalive beat. */
  poll_interval_ms?: number;
  /**
   * The base ref the epic PR asks to merge into (tick cxk). Absent means
   * GitHub's `main` convention — this host cannot resolve the remote's own
   * HEAD the way the local `prBase` does, and the submitter naming the base
   * is the honest replacement for guessing.
   */
  base_ref?: string;
};

/** The default poll cadence: a beat, not a number a run's correctness leans on. */
export const DEFAULT_RECONCILE_POLL_MS = 60_000;

/**
 * The reconciler's Workflow: one instance per EpicRun, keyed by run id
 * (`env.EPIC_RECONCILER.create({ id: run_id, params })`).
 *
 * The steps are the loop: each `reconcile` pass re-derives the world from the
 * durable authorities and returns whether the run is terminal, and `settle`
 * beats between passes. A Workflow restarted mid-run replays its completed
 * steps and the next pass resumes from `.ticfac/` — the checkpoint names
 * where the run stopped, the attempt markers name what is in flight, and no
 * step trusts anything else it holds.
 *
 * Every pass writes as THE repository's one writer: the run takes the
 * repository Durable Object's publish slot first (tick ef7, SPEC §12 Phase 4
 * item 3) and its `contentsStore` publishes through the room's one
 * serialized publisher, so two concurrent Workflows of one repository
 * cannot both write — the second stops naming the holder. The slot is a
 * heartbeat, like the RunRoom lease: renewed every pass, re-acquired (under
 * the room's own compare-and-swap) when a restart outlived its ttl, and
 * released on the way out so a finished run never wedges the repository.
 */
export class EpicReconcilerWorkflow extends WorkflowEntrypoint<Env, EpicReconcilerParams> {
  async run(event: WorkflowEvent<EpicReconcilerParams>, step: WorkflowStep) {
    const params = event.payload;
    const env = this.env;
    const pollMs =
      params.poll_interval_ms ?? env.TICFAC_RECONCILE_POLL_MS ?? DEFAULT_RECONCILE_POLL_MS;

    const room = () => env.REPO_ROOMS.get(env.REPO_ROOMS.idFromName(params.project));
    // reconciler-decision:D29:begin:slot-key — the slot is keyed by the
    // REPOSITORY (one writer per project, across epics); the local host has
    // no slot — its guard is the run branch's own compare-and-swap
    // (decisions/reconciler-parity.json, D29).
    // The slot outlives a poll beat threefold so one slow pass cannot lose it,
    // and stays inside the lease's own pinned bounds.
    const slotTtlMs = Math.min(MAX_LEASE_TTL_MS, Math.max(DEFAULT_LEASE_TTL_MS, pollMs * 3));

    // The slot is taken as its own step so its token is DURABLE: a restarted
    // isolate replays this step and re-reads the slot it already holds,
    // instead of re-acquiring blind.
    const acquired = await step.do("publish-slot", async () =>
      room().acquireSlot({
        run_id: params.run_id,
        epic: params.epic_id,
        origin: "cloud",
        ttl_ms: slotTtlMs,
      }),
    );
    // reconciler-decision:D29:end:slot-key
    if (!acquired.ok) {
      // The tick's acceptance, answered: two concurrent runs cannot both
      // write, and the loser stops naming the holder. It cannot even record
      // its own failure — a checkpoint write would be a publish too.
      const reason =
        acquired.error === "lease_held"
          ? `the publish slot for ${params.project} is held by run ${acquired.holder.run_id} ` +
            `(epic ${acquired.holder.epic}); this run stops rather than write around it`
          : `the publish slot for ${params.project} refused this run: ${acquired.detail}`;
      // The dispatch lease the route handed over must not wedge the project
      // behind a run that stopped before it started: a refused run releases
      // it on the way out (D4's own remedy — the room's alarm would
      // otherwise hold it for the boot ttl).
      await step.do("release-dispatch-lease", async () => {
        if (params.lease_token === undefined) return;
        try {
          await roomFor(env, params.project).releaseDispatchLease({
            run_id: params.run_id,
            token: params.lease_token,
          });
        } catch (error) {
          console.error(
            `run ${params.run_id} could not release the dispatch lease for ${params.project}: ${String(error)}`,
          );
        }
      });
      return { terminal: true, state: "failed", reason, dispatched: [] };
    }
    let holder: HolderCredentials = { run_id: params.run_id, token: acquired.lease.token };

    // The run's one repository view: reads direct, publishes through the room.
    const repository = contentsStore(env, params.project, params.branch, () => holder);
    const store = new RunStateStore(repository, {
      run_id: params.run_id,
      epic_id: params.epic_id,
      // The checkpoint is a reconciler-side record: executor stays null —
      // the contract's own golden checkpoint does the same.
      provenance: provenance({
        run_id: params.run_id,
        source_ref: `refs/heads/${params.branch}`,
        source_sha: "0".repeat(40),
        phase: "worker",
      }),
    });
    const client = new TrackerClient(repository, params.project, params.branch, {
      maxParallel: params.max_parallel,
    });

    // The code-hosting surface the PR + CI close-out rule demands (tick cxk):
    // a test's injection for one project, the deployment's GitHub surface
    // otherwise — and no token is the supported, fail-closed state a
    // rule-declaring repository refuses against, never a silent ungated
    // close.
    const pullRequests: PullRequests | undefined =
      env.TICFAC_PULL_REQUESTS !== undefined &&
      env.TICFAC_PULL_REQUESTS !== null &&
      env.TICFAC_PULL_REQUESTS.project === params.project
        ? env.TICFAC_PULL_REQUESTS.forge
        : (pullRequestsFromEnv(env, params.project) ?? undefined);

    // The executor the run dispatches through: the test seam first, then the
    // deployment's own wiring (tick k4s). A missing wiring — no container
    // binding, no epic base, no factory URL — still refuses dispatches, by
    // design; `sandboxExecutorFromEnv` names what is missing on its way out
    // so the refusal a pass records is a stated gap, not a shrug.
    const executor =
      env.TICFAC_EXECUTOR ??
      sandboxExecutorFromEnv(env, {
        project: params.project,
        ...(params.base_sha === undefined ? {} : { base_sha: params.base_sha }),
      });

    const reconciler = new EpicReconciler({
      client,
      store,
      executor,
      integration: env.TICFAC_INTEGRATION,
      pullRequests,
      baseRef: params.base_ref,
      provenance: store.provenance,
      maxParallel: params.max_parallel,
    });

    let pass = 0;
    for (;;) {
      const result = await step.do(pass === 0 ? "plan" : `reconcile-pass-${pass}`, async () => {
        // The slot's heartbeat, renewed INSIDE the step so a pass never writes
        // under a slot it has not just confirmed — and so a token the room
        // rotated (a re-acquire after a lapse) is carried in the step's durable
        // result, where a restarted isolate re-reads it.
        const renewal = await room().renewSlot({
          run_id: holder.run_id,
          token: holder.token,
          ttl_ms: slotTtlMs,
        });
        let token = holder.token;
        let slotFailure: string | null = null;
        if (renewal.ok) {
          token = renewal.lease.token;
        } else if (renewal.error === "lease_lost" && renewal.lost === "expired") {
          // reconciler-decision:D30:begin:lapse — a lapsed (not taken) slot
          // is re-acquired under the room's CAS and the run continues, where
          // the local host rebuilds a lost push lease rather than ending
          // (decisions/reconciler-parity.json, D30).
          // Lapsed, not taken: re-derive, under the room's own CAS — another
          // run may have taken the slot in the gap, and then this acquire
          // refuses naming it, which is the run stopping, below.
          const again = await room().acquireSlot({
            run_id: params.run_id,
            epic: params.epic_id,
            origin: "cloud",
            ttl_ms: slotTtlMs,
          });
          if (again.ok) token = again.lease.token;
          else
            slotFailure =
              again.error === "lease_held"
                ? `the publish slot for ${params.project} was taken over by run ${again.holder.run_id} while this run lapsed`
                : `the publish slot for ${params.project} refused this run: ${again.detail}`;
          // reconciler-decision:D30:end:lapse
        } else if (renewal.error === "lease_lost") {
          // Taken: another run is this repository's writer now.
          slotFailure =
            `the publish slot for ${params.project} was lost to run ${renewal.holder?.run_id ?? "unknown"}: ` +
            renewal.detail;
        } else {
          slotFailure = `the publish slot for ${params.project} refused the heartbeat: ${renewal.detail}`;
        }
        if (slotFailure !== null) {
          return {
            terminal: true,
            state: "failed",
            reason: slotFailure,
            dispatched: [],
            slot_token: token,
          };
        }

        // The confirmed token is THIS pass's write credential, not only its
        // durable result (tick e9n). The repository view reads `holder` at
        // every write, and a re-acquire after a lapse mints a NEW token that
        // the publisher compares from the first write on — assigning it only
        // AFTER the step returned meant the pass whose heartbeat re-acquired
        // still wrote under the stale token, every publish of that pass was
        // refused `not_holder`, and a lease lapse — the normal case for a
        // run that outlives its slot, not an edge — ended the run. The
        // assignment below the step stays: a replayed step never re-runs
        // this callback, and the token is recovered from its durable result.
        holder = { run_id: params.run_id, token };

        // The DISPATCH lease, renewed in the same step for the same reason
        // the slot's heartbeat is: a run that never renewed it would let the
        // room's alarm read BOOT_LEASE_TTL as a release and ignite a queued
        // submission beside a live run. The renewal is the Run Workflow's
        // own (reused, not re-answered: the same reclaim-on-lapse rule, the
        // same take-is-a-stop rule), inside the step so a replayed step never
        // re-runs it.
        if (params.lease_token !== undefined) {
          const renewal = await renewRunLease(
            env,
            {
              run_id: params.run_id,
              project: params.project,
              epic: params.epic_id,
              requested_by: params.requested_by ?? "",
              base_sha: params.base_sha ?? "",
              lease_token: params.lease_token,
            },
            renewalTtl(pollMs),
          );
          if (renewal !== null && !renewal.ok) {
            // D4 is one arbiter per project, and a run that is not the
            // arbiter must not keep writing. Like the slot refusal above, it
            // cannot even record its own failure — a checkpoint write would
            // be a publish.
            const trip = leaseLostTrip(renewal);
            return {
              terminal: true,
              state: "failed",
              reason: `run ${params.run_id} stopped: ${trip.detail}`,
              dispatched: [],
              slot_token: token,
            };
          }
        }

        const outcome = await reconciler.reconcilePass();
        return {
          terminal: outcome.terminal,
          state: outcome.state,
          reason: outcome.reason,
          dispatched: outcome.dispatched,
          slot_token: token,
        };
      });
      holder = { run_id: params.run_id, token: result.slot_token };
      if (result.terminal) {
        // Best effort and its own steps: a release that fails (the slot or the
        // lease already lapsed, or was taken over) must not turn a terminal
        // verdict into a wedged Workflow — the rooms' alarms sweep what it
        // leaves behind.
        await step.do("release-publish-slot", async () => {
          try {
            await room().releaseSlot(holder);
          } catch (error) {
            console.error(
              `run ${params.run_id} could not release the publish slot for ${params.project}: ${String(error)}`,
            );
          }
        });
        await step.do("release-dispatch-lease", async () => {
          if (params.lease_token === undefined) return;
          try {
            // The release is what ignites a queued submission (D22): a
            // finished run hands the project to whatever waited behind it.
            await roomFor(env, params.project).releaseDispatchLease({
              run_id: params.run_id,
              token: params.lease_token,
            });
          } catch (error) {
            console.error(
              `run ${params.run_id} could not release the dispatch lease for ${params.project}: ${String(error)}`,
            );
          }
        });
        return {
          terminal: result.terminal,
          state: result.state,
          reason: result.reason,
          dispatched: result.dispatched,
        };
      }
      await step.sleep(`settle-${pass}`, pollMs);
      pass += 1;
      if (pass > 10_000) {
        throw new Error("the reconcile loop exceeded 10,000 passes; refusing to spin");
      }
    }
  }
}
