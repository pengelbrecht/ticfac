/**
 * The Workflow-hosted reconciler (tick z23): the control flow — window,
 * dispatch, settle-from-durable-evidence, resume — proven against the SAME
 * pinned contracts a local run's records answer to.
 *
 * The acceptance criteria this file certifies:
 *
 *  - ONE Workflow per EpicRun drives the run (the Workflow-engine test below
 *    creates a real instance through the `EPIC_RECONCILER` binding).
 *  - the run's records are INDISTINGUISHABLE from a local run's: every
 *    checkpoint and attempt record the reconciler writes is validated against
 *    `contracts/ticfac-run-state.json`'s own schemas — the same schema set
 *    `internal/runstate` writes for a local run, pinned once for both.
 *  - a Workflow restarted mid-run RESUMES from `.ticfac/`: incarnation two
 *    is a fresh reconciler over the same durable state, and every in-flight
 *    attempt is adopted by its marker's identity — never dispatched over.
 */

import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import runStateContract from "../../contracts/ticfac-run-state.json";
import {
  type AttemptExecutor,
  type AttemptHandle,
  type AttemptReport,
  type AttemptSpec,
  type AttemptStatus,
  EpicReconciler,
  type IntegrationHost,
  nextAttemptNumber,
  planFrom,
  roleOf,
  skeletonRank,
  tryOf,
} from "../src/epic-reconciler";
import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import {
  attemptPath,
  type Checkpoint,
  checkpointPath,
  decisionPath,
  provenance,
  ROLES,
  RUN_STATE_SCHEMA_VERSION,
  RUN_STATES,
  RunStateStore,
  TICK_STATES,
  terminalState,
} from "../src/run-state-store";
import { encodeTick, type Tick, TrackerClient } from "../src/tracker-client";
import { type Defs, parseDefs, parseSchema, validate } from "./json-schema";

// ------------------------------------------------------------ the contract ---

const contract = runStateContract as {
  schemas: Record<string, unknown>;
  $defs: unknown;
};

const contractDefs: Defs = parseDefs(contract.$defs);

function expectRecordValid(record: string, name: "checkpoint" | "attempt" | "decision"): void {
  const schema = parseSchema(contract.schemas[name], "$");
  const errors = validate(schema, contractDefs, JSON.parse(record));
  expect(errors, `${name} must satisfy the pinned run-state schema: ${errors.join("; ")}`).toEqual(
    [],
  );
}

// --------------------------------------------------------- the memory store ---

/** One shared origin: two incarnations over it see each other's commits. */
class MemoryContents implements ContentsStore {
  readonly files = new Map<string, StoredFile>();
  #next = 0;

  constructor(seed: Record<string, string> = {}) {
    for (const [path, content] of Object.entries(seed)) {
      this.files.set(path, { content, sha: this.#mint() });
    }
  }

  #mint(): string {
    this.#next += 1;
    return `blob-${this.#next}`;
  }

  async list(prefix: string): Promise<string[]> {
    return [...this.files.keys()].filter((path) => path.startsWith(prefix)).sort();
  }

  async read(path: string): Promise<StoredFile | null> {
    const file = this.files.get(path);
    return file === undefined ? null : { content: file.content, sha: file.sha };
  }

  async create(path: string, input: { content: string; message: string }): Promise<StoreWrite> {
    if (this.files.has(path)) {
      return { state: "exists", detail: `${path} already exists` };
    }
    const sha = this.#mint();
    this.files.set(path, { content: input.content, sha });
    return { state: "written", commit_sha: `commit-${this.#next}`, content_sha: sha };
  }

  async update(
    path: string,
    sha: string,
    input: { content: string; message: string },
  ): Promise<StoreWrite> {
    const file = this.files.get(path);
    if (file === undefined) return { state: "missing", detail: `${path} is not on this ref` };
    if (file.sha !== sha) return { state: "conflict", detail: `${path} moved under the write` };
    const next = this.#mint();
    this.files.set(path, { content: input.content, sha: next });
    return { state: "written", commit_sha: `commit-${this.#next}`, content_sha: next };
  }
}

// ------------------------------------------------------------- the fixtures ---

const RUN_ID = "epic-ex1";
const EPIC_ID = "ex1";
const PROJECT = "example/owner";
const BRANCH = "epic/ex1";

const NOW = new Date("2026-09-19T10:30:00Z");

function tick(overrides: Partial<Tick> & Pick<Tick, "id" | "title">): Tick {
  return {
    status: "open",
    priority: 2,
    type: "task",
    owner: "worker@example.com",
    created_by: "planner@example.com",
    created_at: "2026-09-18T09:00:00Z",
    updated_at: "2026-09-18T09:00:00Z",
    ...overrides,
  };
}

/** An epic with a blocked second wave and a skeleton: the shape the plan proves. */
function seedTracker(): Record<string, string> {
  const records: Record<string, string> = {};
  const add = (t: Tick) => {
    records[`.tick/issues/${t.id}.json`] = encodeTick(t);
  };
  add(tick({ id: EPIC_ID, title: "Example epic", type: "epic", priority: 1 }));
  add(tick({ id: "t01", title: "First", priority: 1, parent: EPIC_ID }));
  add(tick({ id: "t02", title: "Second", parent: EPIC_ID }));
  add(tick({ id: "t03", title: "Third", parent: EPIC_ID }));
  add(tick({ id: "t04", title: "After first", parent: EPIC_ID, blocked_by: ["t01"] }));
  add(
    tick({
      id: "rev1",
      title: "Final review",
      parent: EPIC_ID,
      role: "review",
      blocked_by: ["t01", "t02", "t03", "t04"],
    }),
  );
  add(
    tick({
      id: "clo1",
      title: "Close out",
      parent: EPIC_ID,
      role: "closeout",
      blocked_by: ["rev1"],
    }),
  );
  records[".tick/runners.toml"] = "[orchestration]\nmax_parallel = 2\n";
  return records;
}

function sharedContents(seed = seedTracker()): MemoryContents {
  return new MemoryContents(seed);
}

function clientFor(contents: ContentsStore): TrackerClient {
  return new TrackerClient(contents, PROJECT, BRANCH, { now: () => NOW });
}

function storeFor(contents: ContentsStore): RunStateStore {
  return new RunStateStore(contents, {
    run_id: RUN_ID,
    epic_id: EPIC_ID,
    now: () => NOW.toISOString(),
    provenance: provenance({
      run_id: RUN_ID,
      source_ref: `refs/heads/${BRANCH}`,
      source_sha: "0".repeat(40),
      phase: "worker",
    }),
  });
}

/**
 * The fake executor: every started attempt is `running` until the test
 * settles it, exactly the way a real container is. Its handle IS the marker's
 * job_handle — the identity the reconciler adopts by.
 */
class FakeExecutor implements AttemptExecutor {
  readonly started: Array<{ tick_id: string; attempt: number; write_ref: string }> = [];
  readonly settled = new Map<string, AttemptReport>();
  readonly cancelled: string[] = [];
  /** Every collect this executor was asked for, in order — what a resumed run must NOT re-ask. */
  readonly collects: string[] = [];

  constructor(readonly contents: MemoryContents) {}

  #key(handle: AttemptHandle): string {
    return String(handle.job_id);
  }

  async start(spec: AttemptSpec): Promise<AttemptHandle> {
    this.started.push({ tick_id: spec.tick_id, attempt: spec.attempt, write_ref: spec.write_ref });
    return {
      executor: "cloudflare-sandbox",
      job_id: `run-${spec.run_id}/tick-${spec.tick_id}/attempt-${spec.attempt}`,
      attempt: spec.attempt,
      try: spec.attempt,
      tick_id: spec.tick_id,
      role: spec.role,
      remote: "origin",
      resumed_from: null,
      write_ref: spec.write_ref,
    };
  }

  async inspect(handle: AttemptHandle): Promise<AttemptStatus> {
    return this.settled.has(this.#key(handle))
      ? { state: "exited", exit_code: 0 }
      : { state: "running" };
  }

  async collect(handle: AttemptHandle): Promise<AttemptReport> {
    this.collects.push(this.#key(handle));
    return (
      this.settled.get(this.#key(handle)) ?? {
        outcome: "failed",
        commits: 0,
        detail: "never settled",
      }
    );
  }

  async cancel(handle: AttemptHandle): Promise<void> {
    this.cancelled.push(this.#key(handle));
  }

  /** The test settles an attempt — the container finishing its work. */
  finish(tick: string, attempt: number, report: AttemptReport): void {
    this.settled.set(`run-${RUN_ID}/tick-${tick}/attempt-${attempt}`, report);
  }
}

/** Every reported attempt integrates and closes: the shape the phase's later ticks wire for real. */
class FakeIntegration implements IntegrationHost {
  readonly integrated: string[] = [];

  async integrate(input: { tick_id: string; attempt: number; write_ref: string }) {
    this.integrated.push(`${input.tick_id}#${input.attempt}`);
    return { integrated: true as const };
  }
}

/**
 * An isolate that dies mid-settle, after the decision write and before the
 * integration, the row write or the close: the crash shape a restart must
 * recover the already-recorded exchange from.
 */
class DyingIntegration implements IntegrationHost {
  async integrate(): Promise<{ integrated: true } | { integrated: false; reason: string }> {
    throw new Error("died mid-settle: the isolate crashed after recording the decision");
  }
}

function reconcilerFor(
  contents: MemoryContents,
  executor?: AttemptExecutor,
  integration?: IntegrationHost,
  maxParallel?: number,
): EpicReconciler {
  const store = storeFor(contents);
  return new EpicReconciler({
    client: clientFor(contents),
    store,
    executor,
    integration,
    provenance: store.provenance,
    maxParallel,
  });
}

async function readCheckpoint(contents: MemoryContents): Promise<Checkpoint | null> {
  const file = await contents.read(checkpointPath(RUN_ID));
  return file === null ? null : (JSON.parse(file.content) as Checkpoint);
}

/** Runs passes until terminal — a test's driver, never the reconciler's own. */
async function drive(
  pass: () => Promise<import("../src/epic-reconciler").PassResult>,
  budget = 40,
): Promise<{
  outcome: import("../src/epic-reconciler").PassResult;
  dispatched: Array<{ tick_id: string; attempt: number }>;
}> {
  const seen: Array<{ tick_id: string; attempt: number }> = [];
  const trace: string[] = [];
  for (let i = 0; i < budget; i += 1) {
    const outcome = await pass();
    trace.push(
      `${i}: ${outcome.state} (${outcome.reason}) dispatched=${JSON.stringify(outcome.dispatched)}`,
    );
    seen.push(...outcome.dispatched);
    if (outcome.terminal) return { outcome, dispatched: seen };
  }
  throw new Error(`the run did not settle within the pass budget: ${trace.join(" | ")}`);
}

// --------------------------------------------------------------- the tests ---

describe("the plan, ported", () => {
  it("orders work before review before closeout, earlier waves first", async () => {
    const contents = sharedContents();
    const graph = await clientFor(contents).graph(EPIC_ID);
    const plan = planFrom(graph!);
    expect(plan.map((entry) => entry.tick_id)).toEqual([
      "t01",
      "t02",
      "t03",
      "t04",
      "rev1",
      "clo1",
    ]);
    expect(skeletonRank("implement-tick")).toBeLessThan(skeletonRank("review-epic"));
    expect(skeletonRank("review-epic")).toBeLessThan(skeletonRank("closeout-epic"));
    expect(
      roleOf({
        id: "x",
        title: "",
        priority: 0,
        status: "open",
        agent_ready: true,
        role: "review",
      }),
    ).toBe("review-epic");
    expect(roleOf({ id: "x", title: "", priority: 0, status: "open", agent_ready: true })).toBe(
      "implement-tick",
    );
  });

  it("attempt numbers are run-wide identity, and a tick's try is its own count", () => {
    expect(nextAttemptNumber([{ attempt: 1 }, { attempt: 2 }])).toBe(3);
    expect(nextAttemptNumber([{ attempt: 4 }, { attempt: 2 }])).toBe(5);
    expect(
      tryOf(
        [
          { attempt: 1, tick_id: "t01" },
          { attempt: 3, tick_id: "t02" },
        ],
        "t01",
        3,
      ),
    ).toBe(2);
  });
});

describe("the run state store, against the pinned contract", () => {
  it("writes the checkpoint create-if-absent, no_change on an observation, sha-guarded after", async () => {
    const contents = sharedContents();
    const store = storeFor(contents);
    const first = await store.writeCheckpoint({
      state: "admitted",
      reason: "the epic graph is read and the run is admitted",
      sequence: 1,
    });
    expect(first.state).toBe("created");
    expectRecordValid((await contents.read(checkpointPath(RUN_ID)))!.content, "checkpoint");

    // The same sequence is an observation, and an observation writes nothing.
    const again = await store.writeCheckpoint({
      state: "admitted",
      reason: "the epic graph is read and the run is admitted",
      sequence: 1,
    });
    expect(again.state).toBe("no_change");

    const moved = await store.writeCheckpoint({
      state: "running",
      reason: "an attempt is in flight",
      sequence: 2,
    });
    expect(moved.state).toBe("updated");

    // A second incarnation that read sequence 1 and now writes its OWN view of
    // sequence 2 cannot advance the run: the ref already moved, the store
    // re-reads it, and a sequence that did not move past the ref's is an
    // observation — it writes NOTHING. Two incarnations cannot each believe
    // they advanced the run, and neither ever writes on a stale read.
    const stale = storeFor(contents);
    const refused = await stale.writeCheckpoint({
      state: "collecting",
      reason: "a stale writer tries to move the run",
      sequence: 2,
    });
    expect(refused.state).toBe("no_change");
    expect((await readCheckpoint(contents))?.state).toBe("running");
    expect((await readCheckpoint(contents))?.reason).toBe("an attempt is in flight");

    // The mechanism underneath, pinned directly: an update holding a stale
    // blob sha after the ref moved is a REFUSAL — the compare-and-swap the
    // contract's `cas.mechanisms` name, never a lost update.
    const before = (await contents.read(checkpointPath(RUN_ID)))!;
    const direct = await contents.update(checkpointPath(RUN_ID), "blob-totally-stale", {
      content: before.content,
      message: "a stale-sha update",
    });
    expect(direct.state).toBe("conflict");
    expect((await contents.read(checkpointPath(RUN_ID)))!.content).toBe(before.content);
  });

  it("records attempts create-if-absent: existence is the idempotency marker", async () => {
    const contents = sharedContents();
    const store = storeFor(contents);
    const first = await store.recordAttempt({
      attempt: 1,
      tick_id: "t01",
      dispatched_at: NOW.toISOString(),
      job_handle: { executor: "cloudflare-sandbox", job_id: "run-x/tick-t01/attempt-1" },
    });
    expect(first.state).toBe("created");
    expectRecordValid((await contents.read(attemptPath(RUN_ID, 1)))!.content, "attempt");

    const second = await store.recordAttempt({
      attempt: 1,
      tick_id: "t01",
      dispatched_at: NOW.toISOString(),
      job_handle: { executor: "cloudflare-sandbox", job_id: "run-x/tick-t01/attempt-1" },
    });
    expect(second.state).toBe("conflict_exists");
  });

  it("records decisions create-if-absent: one validated exchange, never rewritten", async () => {
    const contents = sharedContents();
    const store = storeFor(contents);
    const exchange = {
      role: "review-epic" as const,
      request: {
        tick_id: "rev1",
        epic_id: EPIC_ID,
        attempt: 5,
        job_id: `run-${RUN_ID}/tick-rev1/attempt-5`,
        write_ref: `refs/heads/ticfac/run-${RUN_ID}/tick-rev1/attempt-5`,
        role: "review-epic",
      },
      response: { outcome: "done", commits: 0, detail: "the review found nothing to refuse" },
      validated: true,
      requested_at: NOW.toISOString(),
      answered_at: NOW.toISOString(),
    };

    const first = await store.recordDecision({ decision: 1, ...exchange });
    expect(first.state).toBe("created");
    expectRecordValid((await contents.read(decisionPath(RUN_ID, 1)))!.content, "decision");

    // A decision is created once and never rewritten: the same number is the
    // repository refusing a second recording of one exchange, the same
    // create-if-absent rule an attempt marker answers to.
    const again = await store.recordDecision({ decision: 1, ...exchange });
    expect(again.state).toBe("conflict_exists");
    expect((await store.decision(1))?.request).toMatchObject({ tick_id: "rev1", attempt: 5 });
    expect((await store.decisions()).map((d) => d.decision)).toEqual([1]);

    // The VALIDATED answer is what lands. An unvalidated response is refused
    // before it is written — an unvalidated model answer landing as a
    // decision is how a hallucinated wave gets dispatched, which is the
    // local store's own rule (Decision.Validate).
    await expect(
      store.recordDecision({ decision: 2, ...exchange, validated: false }),
    ).rejects.toThrow(/validated/);
    expect(await contents.read(decisionPath(RUN_ID, 2))).toBeNull();

    // The role vocabulary is the pinned bundle's, in its order — a bundle
    // bump that does not pass through ROLES fails here, the same guard
    // RUN_STATES and TICK_STATES carry.
    expect([...ROLES]).toEqual((contract.$defs as { role: { enum: string[] } }).role.enum);
    await expect(
      store.recordDecision({ decision: 2, ...exchange, role: "not-a-role" as never }),
    ).rejects.toThrow(/role/);
    expect(await contents.read(decisionPath(RUN_ID, 2))).toBeNull();
  });

  it("terminal states: completed and cancelled end a run, failed is resumable", () => {
    expect(terminalState("completed")).toBe(true);
    expect(terminalState("cancelled")).toBe(true);
    expect(terminalState("failed")).toBe(false);
    expect(RUN_STATE_SCHEMA_VERSION).toBe(runStateContract.schema_version);
    // The mirrored vocabularies ARE the pinned bundle's enums, in its order —
    // a bundle bump that does not pass through the constants fails here.
    expect([...RUN_STATES]).toEqual(
      (contract.schemas.checkpoint as { properties: { state: { enum: string[] } } }).properties
        .state.enum,
    );
    expect([...TICK_STATES]).toEqual(
      (contract.$defs as { tick_state: { properties: { state: { enum: string[] } } } }).tick_state
        .properties.state.enum,
    );
  });
});

describe("the reconciler's window and records", () => {
  it("dispatches at most the configured width, and records every dispatch as a local run would", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration());

    const first = await reconciler.reconcilePass();
    expect(first.terminal).toBe(false);
    // Width 2 from .tick/runners.toml: exactly two admissions, not three.
    expect(first.dispatched.map((d) => d.tick_id)).toEqual(["t01", "t02"]);
    expect(executor.started.length).toBe(2);
    // Attempt numbers are run-wide: 1 and 2, whatever the tick.
    expect(first.dispatched.map((d) => d.attempt)).toEqual([1, 2]);

    // The claims are in the tracker; the run state is in .ticfac/, shaped by
    // the same pinned schemas a local run's records answer to.
    const client = clientFor(contents);
    expect((await client.show("t01"))?.status).toBe("in_progress");
    expect((await client.show("t03"))?.status).toBe("open");

    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.state).toBe("running");
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")).toEqual({
      tick_id: "t01",
      state: "dispatched",
      attempt: 1,
    });
    expectRecordValid((await contents.read(checkpointPath(RUN_ID)))!.content, "checkpoint");
    expectRecordValid((await contents.read(attemptPath(RUN_ID, 1)))!.content, "attempt");
    expectRecordValid((await contents.read(attemptPath(RUN_ID, 2)))!.content, "attempt");

    // The containers finish; the next pass settles them. A pass that only
    // SETTLES still writes its rows — or a resume would read the closed
    // ticks as in flight and settle them again.
    executor.finish("t01", 1, { outcome: "done", commits: 1, detail: "pushed" });
    executor.finish("t02", 2, { outcome: "done", commits: 1, detail: "pushed" });
    const second = await reconciler.reconcilePass();
    expect(second.terminal).toBe(false);
    const settled = await readCheckpoint(contents);
    expect(settled?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("closed");
    expect(settled?.ticks?.find((t) => t.tick_id === "t02")?.state).toBe("closed");
    expect(settled?.sequence).toBeGreaterThan(checkpoint!.sequence);
    expectRecordValid((await contents.read(checkpointPath(RUN_ID)))!.content, "checkpoint");

    // The marker's job_handle names the branch identity a local run's does.
    const marker1 = JSON.parse((await contents.read(attemptPath(RUN_ID, 1)))!.content) as {
      job_handle: Record<string, unknown>;
    };
    expect(marker1.job_handle.write_ref).toBe(`refs/heads/ticfac/run-${RUN_ID}/tick-t01/attempt-1`);
    expect(marker1.job_handle.try).toBe(1);
  });

  it("completes the epic: every tick closed, the checkpoint terminal, markers for each dispatch", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();
    const reconciler = reconcilerFor(contents, executor, integration);

    const run = await drive(async () => {
      // Settle everything in flight before the next pass — the test plays the
      // containers finishing, one wave at a time.
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, {
            outcome: "done",
            commits: 2,
            detail: "pushed and reported",
          });
        }
      }
      return outcome;
    });

    expect(run.outcome.state).toBe("completed");
    expect(run.outcome.reason).toContain(EPIC_ID);

    const client = clientFor(contents);
    for (const id of ["t01", "t02", "t03", "t04", "rev1", "clo1"]) {
      expect((await client.show(id))?.status, `${id} must be closed`).toBe("closed");
    }
    expect((await client.show(EPIC_ID))?.status).toBe("open"); // the epic close is the closeout tick's business

    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.state).toBe("completed");
    expect(checkpoint?.ticks?.every((t) => t.state === "closed")).toBe(true);
    expectRecordValid((await contents.read(checkpointPath(RUN_ID)))!.content, "checkpoint");

    // Six dispatches, six markers, every one shaped by the contract.
    const markers = await contents.list(`.ticfac/runs/${RUN_ID}/attempts`);
    expect(markers.length).toBe(6);
    for (const path of markers) {
      expectRecordValid((await contents.read(path))!.content, "attempt");
    }
    // Each tick was integrated exactly once, before its close.
    expect(integration.integrated.length).toBe(6);

    // Six exchanges, six decisions — every collected answer landed on the
    // run branch as the contract's decision record, and the review and
    // closeout roles are named from the closed role vocabulary.
    const decisionFiles = await contents.list(`.ticfac/runs/${RUN_ID}/decisions`);
    expect(decisionFiles.length).toBe(6);
    const roles: string[] = [];
    for (const path of decisionFiles) {
      const file = (await contents.read(path))!;
      expectRecordValid(file.content, "decision");
      roles.push((JSON.parse(file.content) as { role: string }).role);
    }
    expect(roles).toContain("implement-tick");
    expect(roles).toContain("review-epic");
    expect(roles).toContain("closeout-epic");
  });
});

describe("a Workflow restarted mid-run resumes from .ticfac/", () => {
  it("adopts every in-flight attempt by identity and never dispatches over one", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();

    // Incarnation one: dispatches two attempts, then the isolate dies.
    const firstIncarnation = reconcilerFor(contents, executor, integration);
    const first = await firstIncarnation.reconcilePass();
    expect(first.dispatched.map((d) => d.tick_id)).toEqual(["t01", "t02"]);

    // The Workflow restarts: a FRESH reconciler over the SAME durable state —
    // no memory but the .tick/ records and the .ticfac/ run branch.
    const secondIncarnation = reconcilerFor(contents, executor, integration);
    const resume = await secondIncarnation.reconcilePass();
    expect(resume.terminal).toBe(false);
    // Nothing new dispatched: two attempts are in flight and the window is
    // full of THEM — the attempts the first incarnation dispatched.
    expect(resume.dispatched).toEqual([]);
    expect(executor.started.length).toBe(2);

    // The containers finish; the resumed run settles them, closes them, and
    // drives the rest of the epic to completion.
    executor.finish("t01", 1, { outcome: "done", commits: 1, detail: "pushed" });
    executor.finish("t02", 2, { outcome: "done", commits: 1, detail: "pushed" });

    const run = await drive(async () => {
      const outcome = await secondIncarnation.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, {
            outcome: "done",
            commits: 1,
            detail: "pushed",
          });
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("completed");

    // Every attempt dispatched exactly once: no marker, no job, was repeated.
    const perTick = new Map<string, number>();
    for (const start of executor.started) {
      perTick.set(start.tick_id, (perTick.get(start.tick_id) ?? 0) + 1);
    }
    for (const [tick, count] of perTick) {
      expect(count, `${tick} was started ${count} times`).toBe(1);
    }
    const markers = await contents.list(`.ticfac/runs/${RUN_ID}/attempts`);
    expect(markers.length).toBe(6);
  });

  it("adopts a dispatch whose checkpoint row was lost, by its marker", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();
    const reconciler = reconcilerFor(contents, executor, integration);

    const first = await reconciler.reconcilePass();
    expect(first.dispatched.length).toBe(2);

    // The incarnation died between writing the marker and checkpointing the
    // row: the checkpoint on the ref still says both ticks are ready.
    const stale = await readCheckpoint(contents);
    const demoted = stale!.ticks!.map((row) =>
      row.tick_id === "t01" ? { tick_id: "t01", state: "ready" as const } : row,
    );
    const file = (await contents.read(checkpointPath(RUN_ID)))!;
    const edited = JSON.stringify({ ...JSON.parse(file.content), ticks: demoted }, null, 2);
    await contents.update(checkpointPath(RUN_ID), file.sha, {
      content: edited,
      message: "simulate the lost row",
    });

    const resumed = reconcilerFor(contents, executor, integration);
    const resume = await resumed.reconcilePass();
    // t01 was re-adopted from its marker, not dispatched over — the marker
    // for attempt 1 is single, and no second start happened for it.
    expect(resume.dispatched.map((d) => d.tick_id)).toEqual([]);
    expect(executor.started.filter((s) => s.tick_id === "t01").length).toBe(1);
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("dispatched");
  });

  it("a FAILED run is resumable under the same run id: the recorded boundary, then past it", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);

    // No integration host: the run refuses to close a reported tick and ends
    // failed, naming the boundary — never `completed` over unproven work.
    const bare = reconcilerFor(contents, executor);
    const first = await bare.reconcilePass();
    executor.finish(first.dispatched[0].tick_id, first.dispatched[0].attempt, {
      outcome: "done",
      commits: 1,
      detail: "pushed",
    });
    const failed = await bare.reconcilePass();
    expect(failed.terminal).toBe(true);
    expect(failed.state).toBe("failed");
    expect(failed.reason).toContain("no integration host");
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("reported");
    expect((await clientFor(contents).show("t01"))?.status).toBe("in_progress");

    // A terminal-but-failed checkpoint does not end the resume: the same run
    // id, the same attempt numbering, the integration host now wired.
    const integration = new FakeIntegration();
    const resumed = reconcilerFor(contents, executor, integration);
    const pass = await resumed.reconcilePass();
    expect(pass.terminal).toBe(false); // settled t01 and moved on, not a replay of the failure
    // The attempt the FIRST incarnation dispatched is still in flight — the
    // resumed run adopts it and waits on it, and the container now finishes.
    executor.finish("t02", 2, { outcome: "done", commits: 1, detail: "pushed" });
    // ...and so are the ones the resumed pass itself dispatched.
    for (const dispatch of pass.dispatched) {
      executor.finish(dispatch.tick_id, dispatch.attempt, {
        outcome: "done",
        commits: 1,
        detail: "pushed",
      });
    }

    const run = await drive(async () => {
      const outcome = await resumed.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, {
            outcome: "done",
            commits: 1,
            detail: "pushed",
          });
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("completed");
    // The settled tick closed through the integration host; the attempt
    // numbering continued — no attempt was renumbered by the resume.
    expect(integration.integrated).toContain("t01#1");
  });

  it("re-reads a recorded decision rather than re-asking the executor", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();

    // Incarnation one: dispatch two attempts, the containers finish, and the
    // settling pass collects the first report and records it as a decision on
    // the run branch — then the isolate dies mid-pass, before the exchange's
    // row write, its integration or its close. The decision is on origin and
    // the checkpoint still says dispatched: the exact shape a container that
    // dies leaves its work in (SPEC §10.4's own words).
    const firstIncarnation = reconcilerFor(contents, executor, integration);
    const first = await firstIncarnation.reconcilePass();
    expect(first.dispatched.map((d) => d.tick_id)).toEqual(["t01", "t02"]);
    executor.finish("t01", 1, { outcome: "done", commits: 1, detail: "pushed one" });
    executor.finish("t02", 2, { outcome: "done", commits: 1, detail: "pushed two" });
    const dying = reconcilerFor(contents, executor, new DyingIntegration());
    await expect(dying.reconcilePass()).rejects.toThrow(/died mid-settle/);

    expect((await contents.read(decisionPath(RUN_ID, 1)))?.content).toBeTruthy();
    expectRecordValid((await contents.read(decisionPath(RUN_ID, 1)))!.content, "decision");
    const recorded = JSON.parse((await contents.read(decisionPath(RUN_ID, 1)))!.content) as {
      role: string;
      request: Record<string, unknown>;
      response: Record<string, unknown>;
      provenance: { tick_id: string | null; attempt: number | null; phase: string };
    };
    expect(recorded.role).toBe("implement-tick");
    expect(recorded.request).toMatchObject({ tick_id: "t01", epic_id: EPIC_ID, attempt: 1 });
    expect(recorded.response).toMatchObject({ outcome: "done", commits: 1, detail: "pushed one" });
    expect(recorded.provenance.tick_id).toBe("t01");
    expect(recorded.provenance.attempt).toBe(1);
    expect(recorded.provenance.phase).toBe("post-wave");
    // The second exchange was never reached — no decision for it.
    expect(await contents.read(decisionPath(RUN_ID, 2))).toBeNull();
    // And the row write the pass never reached: still dispatched on the ref.
    expect((await readCheckpoint(contents))?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe(
      "dispatched",
    );

    // Incarnation two: a FRESH reconciler over the same durable state — a
    // contents store and the tracker, no database anywhere. It re-reads the
    // recorded decision for t01 (the executor is never asked for that answer
    // again: a validated decision is a thing a model was paid for once) and
    // collects t02's, which no record holds, for the first time.
    const collectedBefore = executor.collects.length;
    const secondIncarnation = reconcilerFor(contents, executor, integration);
    const resume = await secondIncarnation.reconcilePass();
    expect(resume.terminal).toBe(false);
    expect(executor.collects.length).toBe(collectedBefore + 1); // t02 only — t01 re-read

    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("closed");
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t02")?.state).toBe("closed");
    expectRecordValid((await contents.read(decisionPath(RUN_ID, 2)))!.content, "decision");
  });

  it("a terminal non-failed checkpoint is not restarted by replay", async () => {
    const contents = sharedContents();
    const store = storeFor(contents);
    await store.writeCheckpoint({
      state: "completed",
      reason: "somebody finished this run",
      sequence: 9,
    });
    const executor = new FakeExecutor(contents);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration());
    const outcome = await reconciler.reconcilePass();
    expect(outcome.terminal).toBe(true);
    expect(outcome.state).toBe("completed");
    expect(executor.started.length).toBe(0);
  });
});

describe("the refusals", () => {
  it("a blocked attempt stops the run: a person must read the answer", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration());
    const first = await reconciler.reconcilePass();
    for (const dispatch of first.dispatched) {
      executor.finish(dispatch.tick_id, dispatch.attempt, {
        outcome: "blocked",
        commits: 0,
        detail: "the tick needs a decision only a human can make",
      });
    }
    const blocked = await reconciler.reconcilePass();
    expect(blocked.terminal).toBe(true);
    expect(blocked.state).toBe("failed");
    expect(blocked.reason).toContain("BLOCKED");
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("rejected");
    expectRecordValid((await contents.read(checkpointPath(RUN_ID)))!.content, "checkpoint");
  });

  it("an attempt that left nothing is redispatched as a new number", async () => {
    const contents = sharedContents();
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();
    const reconciler = reconcilerFor(contents, executor, integration);
    const first = await reconciler.reconcilePass();
    for (const dispatch of first.dispatched) {
      executor.finish(dispatch.tick_id, dispatch.attempt, {
        outcome: "failed",
        commits: 0,
        detail: "died before pushing",
      });
    }
    const second = await reconciler.reconcilePass();
    // t01 and t02 left nothing; t03 was never dispatched. The window opens
    // for the redispatch first — a NEW attempt number for each.
    expect(second.dispatched.map((d) => d.attempt)).toEqual([3, 4]);
    expect(second.dispatched.map((d) => d.tick_id)).toEqual(["t01", "t02"]);
  });

  it("a deployment with no executor refuses dispatches before recording any", async () => {
    const contents = sharedContents();
    const reconciler = reconcilerFor(contents, undefined, new FakeIntegration());
    const refused = await reconciler.reconcilePass();
    expect(refused.terminal).toBe(true);
    expect(refused.state).toBe("failed");
    expect(refused.reason).toContain("no attempt executor");
    // Nothing was recorded as if a dispatch had happened — the refusal lands
    // before any record says one did, the way the local reconciler refuses a
    // profile it cannot build an executor for.
    expect(await contents.list(`.ticfac/runs/${RUN_ID}/attempts`)).toEqual([]);
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.state).toBe("failed");
    expect((await clientFor(contents).show("t01"))?.status).toBe("open");
  });
});

describe("one Workflow per EpicRun, driven by the engine", () => {
  it("the EPIC_RECONCILER binding drives an epic run to completion", async () => {
    const contents = sharedContents();
    const executor = new AutoFinishExecutor();
    const integration = new FakeIntegration();

    Object.assign(env, {
      TICK_CONTENTS: { project: PROJECT, ref: BRANCH, store: contents },
      TICFAC_EXECUTOR: executor,
      TICFAC_INTEGRATION: integration,
      TICFAC_RECONCILE_POLL_MS: 5,
    });

    const workflow = env.EPIC_RECONCILER!;
    const runID = `${RUN_ID}-wf`;
    const instance = await workflow.create({
      id: runID,
      params: {
        run_id: runID,
        epic_id: EPIC_ID,
        project: PROJECT,
        branch: BRANCH,
        poll_interval_ms: 5,
      },
    });

    // Wait on the DURABLE EVIDENCE — the checkpoint the Workflow writes —
    // never on a guessed sleep. The observable is the run's own record.
    const deadline = Date.now() + 25_000;
    let checkpoint: Checkpoint | null = null;
    for (;;) {
      const file = await contents.read(checkpointPath(runID));
      if (file !== null) {
        checkpoint = JSON.parse(file.content) as Checkpoint;
        if (checkpoint.state === "completed" || checkpoint.state === "failed") break;
      }
      if (Date.now() > deadline) {
        throw new Error(
          `timed out waiting for the Workflow; checkpoint: ${JSON.stringify(checkpoint)}`,
        );
      }
      await scheduler.wait(20);
    }

    expect(checkpoint?.state).toBe("completed");
    expectRecordValid((await contents.read(checkpointPath(runID)))!.content, "checkpoint");
    const markers = await contents.list(`.ticfac/runs/${runID}/attempts`);
    expect(markers.length).toBe(6);
    for (const path of markers) {
      expectRecordValid((await contents.read(path))!.content, "attempt");
    }
    expect(instance.id).toBe(runID);
    const status = (await instance.status()) as { status?: string };
    expect(String(status.status)).toContain("complete");
  });
});

/** An executor whose containers finish on their own: what a healthy wave looks like. */
class AutoFinishExecutor implements AttemptExecutor {
  readonly started: Array<{ tick_id: string; attempt: number }> = [];

  async start(spec: AttemptSpec): Promise<AttemptHandle> {
    this.started.push({ tick_id: spec.tick_id, attempt: spec.attempt });
    return {
      executor: "cloudflare-sandbox",
      job_id: `run-${spec.run_id}/tick-${spec.tick_id}/attempt-${spec.attempt}`,
      attempt: spec.attempt,
      try: spec.attempt,
      tick_id: spec.tick_id,
      role: spec.role,
      remote: "origin",
      resumed_from: null,
      write_ref: spec.write_ref,
    };
  }

  async inspect(): Promise<AttemptStatus> {
    return { state: "exited", exit_code: 0 };
  }

  async collect(): Promise<AttemptReport> {
    return { outcome: "done", commits: 2, detail: "pushed and reported" };
  }

  async cancel(): Promise<void> {}
}
