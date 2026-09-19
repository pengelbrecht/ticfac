/**
 * The run's durable state, in the repository it runs against: the
 * `contracts/ticfac-run-state.json` layout, on the RUN BRANCH, through the
 * contents API.
 *
 * SPEC §10.4 and the contract's own `authority` section: the committed
 * records under `.ticfac/runs/<run-id>/` ARE the run's state — durable means
 * pushed on origin, D1 is never the authority, and a Workflow that dies and
 * comes back resumes by FETCHING them. This module is the half of that a
 * Workflow host needs: the checkpoint (SHA-guarded update, first write
 * create-if-absent) and the attempt markers (create-if-absent, existence IS
 * the idempotency marker).
 *
 * The compare-and-swap rules are the contract's, not this module's opinions:
 *
 *  - a checkpoint write that changes nothing is `no_change` — a checkpoint is
 *    written on a STATE CHANGE, never on an observation;
 *  - an update whose guarded blob moved is a refusal (`conflict_stale_sha`),
 *    retried by the caller on a fresh read — never a lost update;
 *  - an attempt record that already exists is `conflict_exists` — the
 *    repository refusing a second dispatch of the same attempt, which is the
 *    idempotency rule itself, not an error.
 *
 * A local host reaches the same rules through `git push --force-with-lease`;
 * this module reaches them through the contents API's compare-and-swap. Two
 * mechanisms, one rule — the exact shape that drifts silently, which is why
 * `test/ticfac-run-state.test.ts` runs the contract's CAS table against the
 * in-memory fake in both languages, and why the records this module writes
 * are validated against the contract's own schemas here.
 */

import { type ContentsStore, contentsStore } from "./git-contents";

import type { Env } from "./index";

/**
 * The contract's `schema_version` — a record of another version is not read.
 *
 * Mirrored here rather than imported, because src/ cannot import outside
 * cloudflare (test/isolation.test.ts) — and kept honest the same way
 * `internal/runstate/records.go` keeps its constants honest: the tests assert
 * this equals the pinned bundle's `schema_version` and that the state
 * vocabularies below equal its enums, so a bundle bump that does not pass
 * through this file fails the build.
 */
export const RUN_STATE_SCHEMA_VERSION = 3;

/** `.ticfac` — the contract's `layout.root`. */
export const RUN_STATE_ROOT = ".ticfac";

/** The closed state vocabulary, the contract's order (`schemas.checkpoint.properties.state`). */
export const RUN_STATES = [
  "admitted",
  "dispatching",
  "running",
  "collecting",
  "gating",
  "integrating",
  "publishing",
  "completed",
  "failed",
  "cancelled",
] as const;

/** The closed tick-state vocabulary (`$defs.tick_state.properties.state`). */
export const TICK_STATES = [
  "ready",
  "dispatched",
  "reported",
  "integrated",
  "rejected",
  "closed",
] as const;

/** A terminal checkpoint is one whose run is over; `failed` is still resumable. */
export function terminalState(state: string): boolean {
  return state === "completed" || state === "cancelled";
}

// ------------------------------------------------------------- the records ---

/**
 * Provenance: `$defs.provenance` — every field REQUIRED, nullable where it
 * can be genuinely absent. "Required-and-null rather than omitted, because
 * 'this ran before integration' and 'nobody recorded where it ran' are
 * different claims."
 */
export type Provenance = {
  run_id: string;
  tick_id: string | null;
  attempt: number | null;
  source_ref: string;
  source_sha: string;
  integration_ref: string | null;
  phase: "worker" | "post-wave" | "integrated" | "review" | "closeout";
  executor: string | null;
  workspace_id: string | null;
  backend: string | null;
  substrate_protocol: number | null;
  substrate_server_version: string | null;
  role: string | null;
  tier: string | null;
  profile_digest: string | null;
  model: string | null;
  context_manifest_digest: string | null;
};

/** One tick's position within the run. */
export type TickState = {
  tick_id: string;
  state: (typeof TICK_STATES)[number];
  /** Omitted entirely when this tick has no dispatch yet. */
  attempt?: number;
};

/** The run's one mutable record, at `checkpoint.json`. */
export type Checkpoint = {
  schema_version: number;
  run_id: string;
  epic_id: string;
  /** Monotonic within the run; an observation (same sequence) writes nothing. */
  sequence: number;
  state: (typeof RUN_STATES)[number];
  reason: string;
  updated_at: string;
  ticks?: TickState[];
  provenance: Provenance;
};

/** One dispatch's marker, at `attempts/<n>.json`. Its existence proves the dispatch. */
export type AttemptRecord = {
  schema_version: number;
  /** 1-based, per tick, the number the dispatch was admitted as. */
  attempt: number;
  tick_id: string;
  dispatched_at: string;
  /** The JobHandle the executor's start returned (SPEC 4.3); opaque here. */
  job_handle: Record<string, unknown>;
  provenance: Provenance;
};

// -------------------------------------------------------------- the paths ---

export function checkpointPath(runID: string): string {
  return `${RUN_STATE_ROOT}/runs/${runID}/checkpoint.json`;
}

export function attemptPath(runID: string, attempt: number): string {
  return `${RUN_STATE_ROOT}/runs/${runID}/attempts/${attempt}.json`;
}

/** Two-space indent, no trailing newline — the same shape `internal/runstate` writes. */
function encodeRecord(record: unknown): string {
  return JSON.stringify(record, null, 2);
}

/**
 * The parts of `schemas.checkpoint` a type cannot carry: the envelope's
 * version, the closed vocabularies, a sequence that moved, a reason present.
 * The schema itself is asserted against this module's records in
 * `run-state-store.test.ts` and in the reconciler's tests.
 */
export function validateCheckpoint(checkpoint: Checkpoint): string | null {
  if (checkpoint.schema_version !== RUN_STATE_SCHEMA_VERSION) {
    return `checkpoint schema_version is ${checkpoint.schema_version}, want ${RUN_STATE_SCHEMA_VERSION}`;
  }
  if (checkpoint.run_id === "" || checkpoint.epic_id === "") {
    return "checkpoint names no run or epic";
  }
  if (checkpoint.sequence < 1) {
    return `checkpoint sequence ${checkpoint.sequence} is not monotonic within the run`;
  }
  if (!(RUN_STATES as readonly string[]).includes(checkpoint.state)) {
    return `checkpoint state ${JSON.stringify(checkpoint.state)} is not one of the permitted values`;
  }
  if (checkpoint.reason === "") {
    return "checkpoint names no reason: a checkpoint is written on a state change, so there is always something to name";
  }
  if (checkpoint.updated_at === "") return "checkpoint has no updated_at";
  for (let i = 0; i < (checkpoint.ticks?.length ?? 0); i += 1) {
    const ts = checkpoint.ticks![i];
    if (ts.tick_id === "" || !(TICK_STATES as readonly string[]).includes(ts.state)) {
      return `checkpoint ticks[${i}] is ${JSON.stringify(ts)}, which is not a tick state`;
    }
  }
  return null;
}

// --------------------------------------------------------------- the CAS ---

/**
 * What one state write did — the contract's own vocabulary
 * (`cas.mechanisms`), not an exception: every case a resumed run must tell
 * apart is its own answer.
 */
export type RunWriteOutcome =
  | { state: "created" }
  | { state: "updated" }
  /** The record is already what the ref holds; a state change it was not. */
  | { state: "no_change" }
  /** The guarded blob moved: a fresh read and a retry, never a lost update. */
  | { state: "conflict_stale_sha"; detail: string }
  /** A create whose path already holds a file: the repository's own refusal. */
  | { state: "conflict_exists"; detail: string }
  /** An update naming a path the ref does not hold. */
  | { state: "conflict_missing_base"; detail: string };

/**
 * The run-state half of a Workflow: fetch the durable records, write them
 * back under their CAS rules.
 *
 * Every method re-reads through the store rather than caching a view: the
 * whole point of this module is that a Workflow isolate that died between
 * two calls sees, on its next call, whatever the ref holds — its own earlier
 * writes included — and nothing it holds in memory can be stale relative to
 * the decision it is about to write.
 */
export class RunStateStore {
  readonly store: ContentsStore;
  readonly runID: string;
  readonly epicID: string;
  readonly provenance: Provenance;
  #now: () => string;

  constructor(
    store: ContentsStore,
    input: {
      run_id: string;
      epic_id: string;
      /** updated_at is taken from here, injected for deterministic tests. */
      now?: () => string;
      provenance: Provenance;
    },
  ) {
    this.store = store;
    this.runID = input.run_id;
    this.epicID = input.epic_id;
    this.provenance = input.provenance;
    this.#now = input.now ?? (() => new Date().toISOString());
  }

  /** The checkpoint the run branch holds, or null when the run never started. */
  async checkpoint(): Promise<Checkpoint | null> {
    const file = await this.store.read(checkpointPath(this.runID));
    if (file === null) return null;
    const parsed = JSON.parse(file.content) as Checkpoint;
    const problem = validateCheckpoint(parsed);
    if (problem !== null) {
      throw new Error(
        `the run branch holds an unreadable checkpoint for ${this.runID}: ${problem}`,
      );
    }
    return parsed;
  }

  /**
   * Writes the next state of the run, SHA-guarded, on a STATE CHANGE.
   *
   * `expect` is the sequence the caller last saw (0 for a run that never
   * checkpointed): a ref that moved past it is a `conflict_stale_sha`, so
   * two incarnations cannot each believe they advanced the run — the same
   * race a `git push --force-with-lease` refuses on a local host.
   */
  async writeCheckpoint(
    next: Omit<
      Checkpoint,
      "schema_version" | "run_id" | "epic_id" | "sequence" | "updated_at" | "provenance"
    > & {
      sequence: number;
      provenance?: Provenance;
    },
  ): Promise<RunWriteOutcome> {
    const path = checkpointPath(this.runID);
    const record: Checkpoint = {
      schema_version: RUN_STATE_SCHEMA_VERSION,
      run_id: this.runID,
      epic_id: this.epicID,
      updated_at: this.#now(),
      provenance: next.provenance ?? this.provenance,
      ...next,
    };
    const problem = validateCheckpoint(record);
    if (problem !== null) throw new Error(problem);

    const content = encodeRecord(record);
    const existing = await this.store.read(path);
    if (existing === null) {
      // First write: create-if-absent.
      const result = await this.store.create(path, {
        content,
        message: `ticfac run ${this.runID}: open the run state`,
      });
      if (result.state === "written") return { state: "created" };
      if (result.state === "exists") {
        return { state: "conflict_exists", detail: result.detail };
      }
      if (result.state === "conflict") {
        return { state: "conflict_stale_sha", detail: result.detail };
      }
      return { state: "conflict_missing_base", detail: result.detail };
    }
    if (existing.content === content) return { state: "no_change" };
    const previous = JSON.parse(existing.content) as Checkpoint;
    if (record.sequence <= previous.sequence) {
      // A sequence that did not move is an observation, and an observation
      // writes nothing — asserted here rather than left to the caller, the
      // same rule `Checkpoint.Validate` enforces in Go.
      return {
        state: "no_change",
      };
    }
    const result = await this.store.update(path, existing.sha, {
      content,
      message: `ticfac run ${this.runID}: ${record.state} — ${record.reason}`,
    });
    if (result.state === "written") return { state: "updated" };
    if (result.state === "conflict") {
      return { state: "conflict_stale_sha", detail: result.detail };
    }
    return { state: "conflict_missing_base", detail: result.detail };
  }

  /** One attempt marker, by number — null when the ref holds none. */
  async attempt(attempt: number): Promise<AttemptRecord | null> {
    const file = await this.store.read(attemptPath(this.runID, attempt));
    if (file === null) return null;
    const parsed = JSON.parse(file.content) as AttemptRecord;
    if (parsed.schema_version !== RUN_STATE_SCHEMA_VERSION) {
      throw new Error(
        `attempt ${attempt} of ${this.runID} is schema_version ${parsed.schema_version}, not ${RUN_STATE_SCHEMA_VERSION}`,
      );
    }
    return parsed;
  }

  /** Every attempt marker the ref holds, in attempt order. */
  async attempts(): Promise<AttemptRecord[]> {
    const paths = await this.store.list(`${RUN_STATE_ROOT}/runs/${this.runID}/attempts`);
    const records: Array<{ n: number; record: AttemptRecord }> = [];
    for (const path of paths) {
      const match = /^.*\/attempts\/(\d+)\.json$/.exec(path);
      if (match === null) continue;
      const record = await this.attempt(Number(match[1]));
      if (record !== null) records.push({ n: Number(match[1]), record });
    }
    records.sort((a, b) => a.n - b.n);
    return records.map((entry) => entry.record);
  }

  /**
   * Records one dispatch's marker: create-if-absent.
   *
   * `conflict_exists` is the repository refusing a second dispatch of the
   * same attempt number — the idempotency rule a resumed run relies on to
   * adopt an in-flight attempt rather than pay for it twice.
   */
  async recordAttempt(
    record: Omit<AttemptRecord, "schema_version" | "provenance"> & {
      provenance?: Provenance;
    },
  ): Promise<RunWriteOutcome> {
    const full: AttemptRecord = {
      schema_version: RUN_STATE_SCHEMA_VERSION,
      provenance: record.provenance ?? this.provenance,
      ...record,
    };
    const path = attemptPath(this.runID, full.attempt);
    const result = await this.store.create(path, {
      content: encodeRecord(full),
      message: `ticfac run ${this.runID}: record attempt ${full.attempt} of ${full.tick_id}`,
    });
    if (result.state === "written") return { state: "created" };
    if (result.state === "exists") return { state: "conflict_exists", detail: result.detail };
    if (result.state === "conflict") {
      return { state: "conflict_stale_sha", detail: result.detail };
    }
    return { state: "conflict_missing_base", detail: result.detail };
  }
}

/**
 * A provenance record with the nulls filled in — every field is required, so
 * a caller states the fields it knows and this supplies the honest nulls for
 * the rest.
 */
export function provenance(input: {
  run_id: string;
  source_ref: string;
  source_sha: string;
  phase: Provenance["phase"];
  integration_ref?: string | null;
  tick_id?: string | null;
  attempt?: number | null;
  executor?: string | null;
  role?: string | null;
  workspace_id?: string | null;
  backend?: string | null;
  substrate_protocol?: number | null;
  substrate_server_version?: string | null;
  tier?: string | null;
  profile_digest?: string | null;
  model?: string | null;
  context_manifest_digest?: string | null;
}): Provenance {
  return {
    run_id: input.run_id,
    tick_id: input.tick_id ?? null,
    attempt: input.attempt ?? null,
    source_ref: input.source_ref,
    source_sha: input.source_sha,
    integration_ref: input.integration_ref ?? null,
    phase: input.phase,
    executor: input.executor ?? null,
    workspace_id: input.workspace_id ?? null,
    backend: input.backend ?? null,
    substrate_protocol: input.substrate_protocol ?? null,
    substrate_server_version: input.substrate_server_version ?? null,
    role: input.role ?? null,
    tier: input.tier ?? null,
    profile_digest: input.profile_digest ?? null,
    model: input.model ?? null,
    context_manifest_digest: input.context_manifest_digest ?? null,
  };
}

/** The store this deployment uses: a test's fake, or the GitHub contents API. */
export function runStateStore(
  env: Env,
  project: string,
  ref: string,
  input: { run_id: string; epic_id: string; now?: () => string; provenance: Provenance },
): RunStateStore {
  return new RunStateStore(contentsStore(env, project, ref), input);
}
