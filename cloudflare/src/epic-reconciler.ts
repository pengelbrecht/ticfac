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
 */

import { WorkflowEntrypoint, type WorkflowEvent, type WorkflowStep } from "cloudflare:workers";

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
  provenance: import("./run-state-store").Provenance;
  /** The dispatch window; 0 or absent means the graph's configured width. */
  maxParallel?: number;
};

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
   * Settles a REPORTED tick: integrate, then close — or the recorded
   * boundary/refusal that stops the run. Returns the terminal result when the
   * run stops, null when the tick settled.
   */
  async #settleReported(
    tickID: string,
    attempt: number,
    rows: Map<string, TickState>,
    dispatchedThisPass: Array<{ tick_id: string; attempt: number }>,
  ): Promise<PassResult | null> {
    const { client, store } = this.#deps;
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

    const result = await this.#work(rows, plan, graph);
    if (result.terminal) {
      await this.#checkpoint(result.state, result.reason, rows);
    }
    return result;
  }

  /** The settle/dispatch half of a pass, over assembled rows. */
  async #work(rows: Map<string, TickState>, plan: PlanEntry[], graph: Graph): Promise<PassResult> {
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
        const settle = await this.#settleReported(tickID, attempt, rows, dispatchedThisPass);
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
      if (width > 0 && live >= width) break;
      const row = rows.get(entry.tick_id);
      if (row === undefined || row.state !== "ready") continue;

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

      const claim = await client.claim(entry.tick_id, `run-${store.runID}`);
      if (claim.state === "refused") {
        if (claim.reason === "wave_full") break; // a full window is a wait, not a failure
        const reason = `claiming ${entry.tick_id} was refused: ${claim.detail}`;
        await this.#checkpoint("failed", reason, rows);
        return { terminal: true, state: "failed", reason, dispatched: dispatchedThisPass };
      }

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
  /** The Workflow's poll cadence in ms; defaults to a keepalive beat. */
  poll_interval_ms?: number;
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
    if (!acquired.ok) {
      // The tick's acceptance, answered: two concurrent runs cannot both
      // write, and the loser stops naming the holder. It cannot even record
      // its own failure — a checkpoint write would be a publish too.
      const reason =
        acquired.error === "lease_held"
          ? `the publish slot for ${params.project} is held by run ${acquired.holder.run_id} ` +
            `(epic ${acquired.holder.epic}); this run stops rather than write around it`
          : `the publish slot for ${params.project} refused this run: ${acquired.detail}`;
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
        // Best effort and its own step: a release that fails (the slot already
        // lapsed, or was taken over) must not turn a terminal verdict into a
        // wedged Workflow — the room's alarm sweeps what it leaves behind.
        await step.do("release-publish-slot", async () => {
          try {
            await room().releaseSlot(holder);
          } catch (error) {
            console.error(
              `run ${params.run_id} could not release the publish slot for ${params.project}: ${String(error)}`,
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
