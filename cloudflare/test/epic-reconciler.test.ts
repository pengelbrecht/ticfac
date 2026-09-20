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
 *    checkpoint, attempt and decision record the reconciler writes is validated
 *    against `contracts/ticfac-run-state.json`'s own schemas — the same schema set
 *    `internal/runstate` writes for a local run, pinned once for both.
 *  - a Workflow restarted mid-run RESUMES from `.ticfac/`: incarnation two
 *    is a fresh reconciler over the same durable state, every in-flight
 *    attempt is adopted by its marker's identity — never dispatched over —
 *    and a role job whose answer already landed as a decision is RE-READ,
 *    never re-asked (tick 9fc: run, attempt and decision records all live on
 *    the run branch, so the resumed run needs no D1 access at all).
 */

import { env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
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
  isRoleJob,
  nextAttemptNumber,
  nextDecisionNumber,
  planFrom,
  roleOf,
  skeletonRank,
  tryOf,
} from "../src/epic-reconciler";
import type { CIReport, PullRequest, PullRequests } from "../src/forge";
import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import type { RepoRoom } from "../src/repo-room";
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
import { attemptSandboxName } from "../src/sandbox-executor";
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
 * settles it, exactly the way a real container is. Its handle is the one the
 * reconciler PERSISTS on the attempt marker — the identity a later pass,
 * and a restarted incarnation, re-addresses the attempt by (tick t5p).
 *
 * It demands the identity the real executor demands. The concrete executor
 * of this host is the sandbox one, whose `inspect` re-addresses the attempt
 * by the container's NAME — a `job_id` the reconciler composed before start
 * is not a container and cannot be inspected, and keying on it here is how
 * the suite stayed green over a marker that never recorded what start
 * returned. So `#key` REFUSES a handle with no `sandbox`: a marker written
 * before start and never completed would fail loudly here, the way it fails
 * in production with `namedSandbox(binding, undefined)`.
 */
class FakeExecutor implements AttemptExecutor {
  readonly started: Array<{ tick_id: string; attempt: number; write_ref: string }> = [];
  readonly settled = new Map<string, AttemptReport>();
  readonly cancelled: string[] = [];
  /** Every collect this executor was asked for, in order — what a resumed run must NOT re-ask. */
  readonly collects: string[] = [];

  constructor(readonly contents: MemoryContents) {}

  #key(handle: AttemptHandle): string {
    const sandbox = handle.sandbox;
    if (typeof sandbox !== "string" || sandbox === "") {
      throw new Error(
        `this handle carries no container: the marker it came from never recorded what ` +
          `start returned, and a job_id (${String(handle.job_id)}) is not an identity an attempt ` +
          `can be re-inspected by`,
      );
    }
    return sandbox;
  }

  async start(spec: AttemptSpec): Promise<AttemptHandle> {
    this.started.push({ tick_id: spec.tick_id, attempt: spec.attempt, write_ref: spec.write_ref });
    // What the REAL executor returns and the reconciler must persist: the
    // identity composed before start, beside the container's own addressing
    // — its name, its work process, the branch it pushes and the base the
    // collect compares against.
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
      sandbox: attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt),
      process_id: `proc-${spec.attempt}`,
      branch: `ticfac/${spec.epic_id}/${spec.tick_id}`,
      base_sha: "f".repeat(40),
      launched: true,
      detail: "booted by the fake",
      run_id: spec.run_id,
      epic_id: spec.epic_id,
      project: spec.project,
      base_ref: spec.base_ref,
      title: spec.title,
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
    this.settled.set(attemptSandboxName(RUN_ID, tick, attempt), report);
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
 * integration: the crash shape a restart must recover the already-recorded
 * exchange from.
 */
class DyingIntegration implements IntegrationHost {
  async integrate(): Promise<{ integrated: true } | { integrated: false; reason: string }> {
    throw new Error("died mid-settle: the isolate crashed after recording the decision");
  }
}

// ----------------------------------------------- the CI-gated close-out (cxk) ---

/**
 * The rule the close-out gates on (cxk): the repository's own declaration,
 * the anchor `internal/reconcile` recognises, with the workflow named.
 */
const CLOSEOUT_RULE =
  "## Rules\n\n" +
  "- Epic integration goes through a PR + CI gate: the epic close-out may not " +
  "complete until CI (.github/workflows/ci.yml) is green on the epic PR. No direct " +
  "merges of epic branches to the default branch.\n";

/**
 * The tracker fixture plus the two records the gated close-out reads: the rule
 * in `.tick/config.md` and one findings draft the run's earlier incarnation
 * left on the branch — the text the epic PR must carry beside CI.
 */
function seededWithRule(runID: string = RUN_ID): Record<string, string> {
  const records = seedTracker();
  records[".tick/config.md"] = CLOSEOUT_RULE;
  records[`.ticfac/runs/${runID}/findings/key-draft.json`] = JSON.stringify(
    {
      schema_version: 1,
      key: "key-draft",
      source: "ticfac-worker",
      discovered_from: `run-${runID}/tick-t01/attempt-1`,
      kind: "defect",
      title: "A defect outside the discovering tick",
      body: "The finding's own text, which the person merging must read beside CI.",
      severity: "medium",
      target: "",
      tick_id: "t01",
      attempt: 1,
      status: "proposed",
      proposed_at: "2026-09-19T09:00:00Z",
    },
    null,
    2,
  );
  return records;
}

/**
 * The fake forge: the `PullRequests` seam with the CI answers a test states
 * per sha, the ancestors a branch has, and the paths a range of commits
 * changed — the three inputs the close-out's gates read. CI is asked by SHA
 * and every ask is recorded, because WHICH commit a verdict was borrowed from
 * is the thing 9da's walk exists to keep honest.
 */
class FakeForge implements PullRequests {
  readonly opened: Array<{ headRef: string; baseRef: string; title: string; body: string }> = [];
  /** every updateBody, in order — the overwrite the found path and the close gate write. */
  readonly bodies: Array<{ number: number; body: string }> = [];
  readonly prs = new Map<string, PullRequest>();
  readonly ciBySHA = new Map<string, CIReport>();
  readonly ciCalls: string[] = [];
  readonly ancestorsByHead = new Map<string, string[]>();
  readonly changedByPair = new Map<string, string[] | null>();
  /** What a sha with no entry answers with: `none` until a test says otherwise. */
  default: CIReport = { state: "none", failing: [] };
  failOpen: Error | null = null;
  failUpdate: Error | null = null;
  failCI: Error | null = null;
  #next = 0;

  async find(headRef: string, _baseRef: string): Promise<PullRequest | null> {
    return this.prs.get(headRef) ?? null;
  }

  async open(input: {
    headRef: string;
    baseRef: string;
    title: string;
    body: string;
  }): Promise<PullRequest> {
    if (this.failOpen !== null) throw this.failOpen;
    this.#next += 1;
    const pr: PullRequest = {
      number: this.#next,
      url: `https://example.com/${PROJECT}/pull/${this.#next}`,
      head_ref: input.headRef,
      head_sha: `head-${this.#next}`,
      base_ref: input.baseRef,
    };
    this.opened.push(input);
    this.prs.set(input.headRef, pr);
    return pr;
  }

  async updateBody(pr: PullRequest, body: string): Promise<void> {
    if (this.failUpdate !== null) throw this.failUpdate;
    this.bodies.push({ number: pr.number, body });
  }

  async ci(sha: string): Promise<CIReport> {
    if (this.failCI !== null) throw this.failCI;
    this.ciCalls.push(sha);
    return this.ciBySHA.get(sha) ?? this.default;
  }

  async ancestors(headRef: string, limit: number): Promise<string[]> {
    return (this.ancestorsByHead.get(headRef) ?? []).slice(0, limit);
  }

  async changedPaths(from: string, to: string): Promise<string[] | null> {
    return this.changedByPair.get(`${from}...${to}`) ?? null;
  }

  /** The branch moved under the PR — 9da's shape: the run's own writes moved the head. */
  moveHead(headRef: string, sha: string): void {
    const pr = this.prs.get(headRef);
    if (pr === undefined) throw new Error(`no PR for ${headRef} to move`);
    this.prs.set(headRef, { ...pr, head_sha: sha });
  }
}

function reconcilerFor(
  contents: MemoryContents,
  executor?: AttemptExecutor,
  integration?: IntegrationHost,
  maxParallel?: number,
  extra?: {
    pullRequests?: PullRequests;
    baseRef?: string;
    now?: () => Date;
    gateTimeoutMs?: number;
  },
): EpicReconciler {
  const store = storeFor(contents);
  return new EpicReconciler({
    client: clientFor(contents),
    store,
    executor,
    integration,
    provenance: store.provenance,
    maxParallel,
    ...(extra ?? {}),
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
    expect(nextDecisionNumber([{ decision: 1 }, { decision: 2 }])).toBe(3);
    expect(nextDecisionNumber([{ decision: 4 }, { decision: 2 }])).toBe(5);
    // Only review and closeout are role jobs — the port of the Go
    // reconciler's isRoleJob: their deliverable is an ANSWER, and it is
    // those exchanges a decision record exists to hold.
    expect(isRoleJob("review-epic")).toBe(true);
    expect(isRoleJob("closeout-epic")).toBe(true);
    expect(isRoleJob("implement-tick")).toBe(false);
    expect(isRoleJob("plan-epic")).toBe(false);
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

  it("completes a marker with the handle start returned, SHA-guarded — the dispatch guard untouched (t5p)", async () => {
    const contents = sharedContents();
    const store = storeFor(contents);
    // The marker is written BEFORE start, create-if-absent — a marker
    // written afterwards guards nothing.
    await store.recordAttempt({
      attempt: 1,
      tick_id: "t01",
      dispatched_at: NOW.toISOString(),
      job_handle: {
        executor: "cloudflare-sandbox",
        job_id: `run-${RUN_ID}/tick-t01/attempt-1`,
      },
    });

    // Then the executor's start answers, and the handle it returned is
    // recorded on the SAME marker: job-protocol's start rule — "Persist the
    // JobSpec before addressing the executor, then record the returned
    // handle. A handle that was never persisted is a job nobody can find
    // after a restart." The record's other fields are untouched, and the
    // create-if-absent guard above is still the one that answers a second
    // reconciler racing the same dispatch.
    const handle = {
      executor: "cloudflare-sandbox",
      job_id: `run-${RUN_ID}/tick-t01/attempt-1`,
      attempt: 1,
      tick_id: "t01",
      sandbox: attemptSandboxName(RUN_ID, "t01", 1),
      process_id: "proc-1",
      branch: `ticfac/${EPIC_ID}/t01`,
      base_sha: "f".repeat(40),
    };
    const completed = await store.updateAttemptHandle(1, handle);
    expect(completed.state).toBe("updated");
    const file = (await contents.read(attemptPath(RUN_ID, 1)))!;
    expectRecordValid(file.content, "attempt");
    const marker = JSON.parse(file.content) as {
      attempt: number;
      tick_id: string;
      dispatched_at: string;
      job_handle: Record<string, unknown>;
    };
    expect(marker.job_handle).toEqual(handle);
    expect(marker.tick_id).toBe("t01");
    expect(marker.dispatched_at).toBe(NOW.toISOString());

    // The completion is idempotent: the same handle again is an observation,
    // and an observation writes nothing.
    const again = await store.updateAttemptHandle(1, handle);
    expect(again.state).toBe("no_change");

    // A later handle (the truth moved) is still an update — the completion
    // records what the executor last answered for a live attempt, never
    // what an incarnation remembers.
    const moved = await store.updateAttemptHandle(1, { ...handle, process_id: "proc-2" });
    expect(moved.state).toBe("updated");
    const reread = JSON.parse((await contents.read(attemptPath(RUN_ID, 1)))!.content) as {
      job_handle: { process_id: string };
    };
    expect(reread.job_handle.process_id).toBe("proc-2");

    // A marker that is not on the ref is a missing base, not a create: the
    // completion records beside a dispatch that exists, never in place of
    // one.
    const missing = await store.updateAttemptHandle(9, handle);
    expect(missing.state).toBe("conflict_missing_base");
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

    // The role vocabulary is the pinned bundle's — a role outside it is
    // refused before anything is written.
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
    // The role vocabulary is the pinned bundle's too — the decisions this
    // run records name their role from the same closed enum a local run's do.
    expect([...ROLES]).toEqual((contract.$defs as { role: { enum: string[] } }).role.enum);
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

    // The role-job exchanges — and ONLY those, the same rule the local
    // reconciler records by (isRoleJob: review and closeout, whose
    // deliverable is an answer) — landed on the run branch as the
    // contract's decision records, so a resumed run re-reads them with no
    // D1 access at all. An implement-tick attempt's verdict is its branch
    // and the gate, never a recorded answer.
    const decisionFiles = await contents.list(`.ticfac/runs/${RUN_ID}/decisions`);
    expect(decisionFiles).toEqual([
      `.ticfac/runs/${RUN_ID}/decisions/1.json`,
      `.ticfac/runs/${RUN_ID}/decisions/2.json`,
    ]);
    const byTick = new Map<
      string,
      { role: string; phase: string; response: Record<string, unknown> }
    >();
    for (const path of decisionFiles) {
      const file = (await contents.read(path))!;
      expectRecordValid(file.content, "decision");
      const record = JSON.parse(file.content) as {
        role: string;
        request: Record<string, unknown>;
        response: Record<string, unknown>;
        provenance: { phase: string; tick_id: string | null };
      };
      byTick.set(String(record.request.tick_id), {
        role: record.role,
        phase: record.provenance.phase,
        response: record.response,
      });
    }
    expect([...byTick.keys()].sort()).toEqual(["clo1", "rev1"]);
    expect(byTick.get("rev1")?.role).toBe("review-epic");
    expect(byTick.get("rev1")?.phase).toBe("review");
    expect(byTick.get("rev1")?.response).toMatchObject({
      outcome: "done",
      commits: 2,
      detail: "pushed and reported",
    });
    expect(byTick.get("clo1")?.role).toBe("closeout-epic");
    expect(byTick.get("clo1")?.phase).toBe("closeout");
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

  it("re-inspects an attempt by its PERSISTED handle after the restart — a job_id is not identity (t5p)", async () => {
    const contents = sharedContents();
    const integration = new FakeIntegration();

    // Incarnation one dispatches, the containers run on, and the isolate
    // dies — the shape every resume in this file answers to.
    const firstExecutor = new FakeExecutor(contents);
    const first = reconcilerFor(contents, firstExecutor, integration);
    const outcome = await first.reconcilePass();
    expect(outcome.dispatched.map((d) => d.tick_id)).toEqual(["t01", "t02"]);

    // The marker on the run branch holds the handle start RETURNED — the
    // container's own addressing beside the identity the reconciler
    // composed before start. That completion is the adoption-by-identity
    // the local reconciler always had: it is the only thing that survives
    // the isolate, and without it no later pass can re-address the attempt
    // at all.
    const markerFile = (await contents.read(attemptPath(RUN_ID, 1)))!;
    expectRecordValid(markerFile.content, "attempt");
    const marker = JSON.parse(markerFile.content) as {
      job_handle: Record<string, unknown>;
      tick_id: string;
    };
    expect(marker.tick_id).toBe("t01");
    expect(marker.job_handle.sandbox).toBe(attemptSandboxName(RUN_ID, "t01", 1));
    expect(marker.job_handle.process_id).toBe("proc-1");
    expect(marker.job_handle.branch).toBe(`ticfac/${EPIC_ID}/t01`);

    // The Workflow restarts: a FRESH reconciler and a FRESH executor over
    // the same durable state. The first executor's memory died with its
    // isolate — a fresh fake answers "running" for everything — so the
    // only thing the resume can re-address the attempt by is the handle on
    // the marker. The fake DEMANDS the persisted container name (its #key
    // refuses a bare job_id), so this pass is the proof: without the
    // persisted handle, the resume could not inspect the attempt at all.
    const secondExecutor = new FakeExecutor(contents);
    const second = reconcilerFor(contents, secondExecutor, integration);
    const resume = await second.reconcilePass();
    expect(resume.terminal).toBe(false);
    expect(resume.dispatched).toEqual([]); // adopted, never dispatched over
    expect(secondExecutor.started.length).toBe(0);

    // The restart settles the attempt THROUGH the persisted handle: the
    // second executor is asked to collect the container the FIRST one
    // booted, addressed by the name the marker carries.
    secondExecutor.finish("t01", 1, { outcome: "done", commits: 1, detail: "pushed" });
    const settled = await second.reconcilePass();
    expect(settled.terminal).toBe(false);
    expect(secondExecutor.collects).toEqual([attemptSandboxName(RUN_ID, "t01", 1)]);
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "t01")?.state).toBe("closed");
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

    // Incarnation one drives the work ticks to closed and dispatches the
    // skeleton — the review, and the closeout beside it in the window.
    const firstIncarnation = reconcilerFor(contents, executor, integration);
    let reviewAttempt = 0;
    let closeoutAttempt = 0;
    for (;;) {
      const outcome = await firstIncarnation.reconcilePass();
      const review = outcome.dispatched.find((d) => d.tick_id === "rev1");
      if (review !== undefined) {
        reviewAttempt = review.attempt;
        closeoutAttempt = outcome.dispatched.find((d) => d.tick_id === "clo1")?.attempt ?? 0;
        break;
      }
      if (outcome.terminal) {
        throw new Error(`the run ended before the review was dispatched: ${outcome.reason}`);
      }
      for (const dispatch of outcome.dispatched) {
        executor.finish(dispatch.tick_id, dispatch.attempt, {
          outcome: "done",
          commits: 1,
          detail: "pushed",
        });
      }
    }

    // The review container finishes; the closeout's is still booting. The
    // settling pass collects the review's answer, records it as a decision
    // on the run branch — then the isolate dies mid-settle, after the decision
    // write and before the integration, the row write or the close. The
    // decision is on the ref and the checkpoint still says dispatched: the
    // crash shape a restart must recover the recorded exchange from.
    executor.finish("rev1", reviewAttempt, {
      outcome: "done",
      commits: 2,
      detail: "the review found nothing to refuse",
    });
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
    expect(recorded.role).toBe("review-epic");
    expect(recorded.request).toMatchObject({
      tick_id: "rev1",
      epic_id: EPIC_ID,
      attempt: reviewAttempt,
    });
    expect(recorded.response).toMatchObject({
      outcome: "done",
      commits: 2,
      detail: "the review found nothing to refuse",
    });
    expect(recorded.provenance.tick_id).toBe("rev1");
    expect(recorded.provenance.attempt).toBe(reviewAttempt);
    expect(recorded.provenance.phase).toBe("review");
    // The row write the dying pass never reached: still dispatched on the ref.
    expect((await readCheckpoint(contents))?.ticks?.find((t) => t.tick_id === "rev1")?.state).toBe(
      "dispatched",
    );

    // Incarnation two: a FRESH reconciler over the same durable state — a
    // contents store and the tracker, no database anywhere. It re-reads the
    // recorded decision for the review (the executor is never asked for that
    // answer again: a validated decision is a thing a model was paid for
    // once) and closes it, while the closeout's container is still running.
    const collectedBefore = executor.collects.length;
    const secondIncarnation = reconcilerFor(contents, executor, integration);
    const resume = await secondIncarnation.reconcilePass();
    expect(resume.terminal).toBe(false);
    expect(executor.collects.length).toBe(collectedBefore); // nothing re-asked
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "rev1")?.state).toBe("closed");
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "clo1")?.state).toBe("dispatched");

    // The closeout container finishes; the resumed run collects it for the
    // first time, records ITS decision, and completes the epic.
    executor.finish("clo1", closeoutAttempt, {
      outcome: "done",
      commits: 1,
      detail: "the closeout wrote the retro",
    });
    const run = await drive(async () => secondIncarnation.reconcilePass());
    expect(run.outcome.state).toBe("completed");
    // The review was collected exactly once — by the incarnation that died —
    // and its answer was re-read from the run branch ever after. The
    // closeout is the only exchange collected after the crash.
    expect(
      executor.collects.filter((key) => key === attemptSandboxName(RUN_ID, "rev1", reviewAttempt))
        .length,
    ).toBe(1);
    expect(executor.collects.length).toBe(collectedBefore + 1);

    const decisionFiles = await contents.list(`.ticfac/runs/${RUN_ID}/decisions`);
    expect(decisionFiles).toEqual([
      `.ticfac/runs/${RUN_ID}/decisions/1.json`,
      `.ticfac/runs/${RUN_ID}/decisions/2.json`,
    ]);
    for (const path of decisionFiles) {
      expectRecordValid((await contents.read(path))!.content, "decision");
    }
    const review = JSON.parse((await contents.read(decisionPath(RUN_ID, 1)))!.content) as {
      role: string;
      request: Record<string, unknown>;
      provenance: { phase: string };
    };
    expect(review.role).toBe("review-epic");
    expect(review.request).toMatchObject({ tick_id: "rev1", attempt: reviewAttempt });
    expect(review.provenance.phase).toBe("review");
    const closeout = JSON.parse((await contents.read(decisionPath(RUN_ID, 2)))!.content) as {
      role: string;
      request: Record<string, unknown>;
      provenance: { phase: string };
    };
    expect(closeout.role).toBe("closeout-epic");
    expect(closeout.request).toMatchObject({ tick_id: "clo1", attempt: closeoutAttempt });
    expect(closeout.provenance.phase).toBe("closeout");
    const final = await readCheckpoint(contents);
    expect(final?.state).toBe("completed");
    expect(final?.ticks?.every((t) => t.state === "closed")).toBe(true);
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

describe("the CI-gated close-out, ported", () => {
  /** The detail each role reports with — the review's answer is what the PR carries. */
  const finishFor = (tickID: string) =>
    tickID === "rev1"
      ? { outcome: "done" as const, commits: 2, detail: "the review found nothing to refuse" }
      : { outcome: "done" as const, commits: 1, detail: "pushed and reported" };

  /** Drives every dispatch to done, then stops at the first gated hold. */
  async function driveToHold(
    pass: () => Promise<import("../src/epic-reconciler").PassResult>,
    onDispatched: (dispatched: Array<{ tick_id: string; attempt: number }>) => void = () => {},
    budget = 60,
  ): Promise<import("../src/epic-reconciler").PassResult> {
    for (let i = 0; i < budget; i += 1) {
      const outcome = await pass();
      if (outcome.terminal || outcome.state === "gating") return outcome;
      onDispatched(outcome.dispatched);
    }
    throw new Error("the run did not settle within the pass budget");
  }

  it("admits the close-out behind green CI and opens a PR that carries the review's verdict and the run's findings", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const integration = new FakeIntegration();
    const forge = new FakeForge();
    forge.default = { state: "green", failing: [] };
    const reconciler = reconcilerFor(contents, executor, integration, undefined, {
      pullRequests: forge,
    });

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("completed");

    // The PR: opened for the run branch into the default base, under the rule.
    expect(forge.opened.length).toBe(1);
    expect(forge.opened[0].headRef).toBe(BRANCH);
    expect(forge.opened[0].baseRef).toBe("main");
    expect(forge.opened[0].title).toContain(EPIC_ID);

    // The body the PR was OPENED with, and the one the close gate re-carried,
    // are recompositions of the same record: an overwrite, never an append.
    expect(forge.bodies.length).toBe(1);
    for (const body of [forge.opened[0].body, forge.bodies[0].body]) {
      expect(body).toContain("the review found nothing to refuse");
      expect(body).toContain("A defect outside the discovering tick");
      expect(body).toContain(
        "The finding's own text, which the person merging must read beside CI.",
      );
    }
    expect(forge.bodies[0].body).toBe(forge.opened[0].body);

    // The close-out closed behind the gate, and the run completed behind it.
    expect((await clientFor(contents).show("clo1"))?.status).toBe("closed");
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.state).toBe("completed");
    expect(checkpoint?.ticks?.every((t) => t.state === "closed")).toBe(true);
  });

  it("refuses the close-out on red CI, naming the failing job, and never dispatches it", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.default = { state: "red", failing: ["build-and-test"] };
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("failed");
    expect(run.outcome.reason).toContain("build-and-test");
    expect(run.outcome.reason).toContain("PR + CI gate");
    // The close-out job was never claimed or started, and the tick stayed open.
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(0);
    expect((await clientFor(contents).show("clo1"))?.status).toBe("open");
    // The PR was opened and carried the record even though CI refused it.
    expect(forge.opened.length).toBe(1);
  });

  it("refuses the close-out when no CI has run at all, naming the workflow", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge(); // default: none, no ancestors
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("failed");
    expect(run.outcome.reason).toContain(".github/workflows/ci.yml");
    expect(run.outcome.reason).toContain("unsatisfiable by waiting");
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(0);
  });

  it("holds the close-out while CI is pending, and admits it once CI concludes", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.default = { state: "pending", failing: [] };
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    let held = false;
    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (outcome.state === "gating") {
        // A held pass is a wait the run re-derives, not a failure of the work.
        held = true;
        forge.default = { state: "green", failing: [] };
      }
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(held).toBe(true);
    expect(run.outcome.state).toBe("completed");
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(1);
    expect((await clientFor(contents).show("clo1"))?.status).toBe("closed");
  });

  it("refuses the close-out when the rule is declared and no code-hosting surface is wired", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration());

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("failed");
    expect(run.outcome.reason).toContain("no code-hosting surface");
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(0);
  });

  it("gates on the newest ancestor CI ran on when the head has none — 9da's walk, ported", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    // The PR exists with a head the run's own checkpoint writes moved, no
    // check on it yet, and a green ancestor behind run-state-only commits.
    forge.prs.set(BRANCH, {
      number: 7,
      url: "https://example.com/pr/7",
      head_ref: BRANCH,
      head_sha: "head-moved-by-checkpoints",
      base_ref: "main",
    });
    forge.ciBySHA.set("head-moved-by-checkpoints", { state: "none", failing: [] });
    forge.ancestorsByHead.set(BRANCH, ["head-moved-by-checkpoints", "ancestor-green"]);
    forge.ciBySHA.set("ancestor-green", { state: "green", failing: [] });
    forge.changedByPair.set("ancestor-green...head-moved-by-checkpoints", [
      ".ticfac/runs/epic-ex1/checkpoint.json",
    ]);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("completed");
    // The head was asked first, and the ancestor's verdict was used only
    // after the walk proved every change between them was run state.
    expect(forge.ciCalls[0]).toBe("head-moved-by-checkpoints");
    expect(forge.ciCalls).toContain("ancestor-green");
    expect((await clientFor(contents).show("clo1"))?.status).toBe("closed");
  });

  it("does not borrow an ancestor's green when anything outside .ticfac/ changed since it", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.prs.set(BRANCH, {
      number: 7,
      url: "https://example.com/pr/7",
      head_ref: BRANCH,
      head_sha: "head-moved-by-checkpoints",
      base_ref: "main",
    });
    forge.ciBySHA.set("head-moved-by-checkpoints", { state: "none", failing: [] });
    forge.ancestorsByHead.set(BRANCH, ["head-moved-by-checkpoints", "ancestor-green"]);
    forge.ciBySHA.set("ancestor-green", { state: "green", failing: [] });
    // Code moved between the ancestor and the head: its verdict does not
    // describe this tree, so the honest answer is the head's own — none.
    forge.changedByPair.set("ancestor-green...head-moved-by-checkpoints", [
      "src/thing.ts",
      ".ticfac/runs/epic-ex1/checkpoint.json",
    ]);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("failed");
    expect(run.outcome.reason).toContain(".github/workflows/ci.yml");
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(0);
  });

  it("refuses to close the close-out on red CI at the close, naming the job, and leaves the tick open", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.default = { state: "green", failing: [] };
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
    });

    let moved = false;
    const run = await drive(async () => {
      const outcome = await reconciler.reconcilePass();
      if (!outcome.terminal) {
        for (const dispatch of outcome.dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
        if (!moved && outcome.dispatched.some((d) => d.tick_id === "clo1")) {
          // The close-out's own commits moved the PR head; its CI is red.
          moved = true;
          forge.moveHead(BRANCH, "head-after-closeout");
          forge.ciBySHA.set("head-after-closeout", { state: "red", failing: ["retro-lint"] });
        }
      }
      return outcome;
    });
    expect(run.outcome.state).toBe("failed");
    expect(run.outcome.reason).toContain("retro-lint");
    expect(run.outcome.reason).toContain("the close-out's own commits");
    // The close-out is NOT closed behind red CI: the row is rejected, the
    // tick stays claimed-but-open, and the run's record names the job.
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "clo1")?.state).toBe("rejected");
    expect((await clientFor(contents).show("clo1"))?.status).toBe("in_progress");
  });

  it("holds the close at absent CI without rewriting the wait's start, then refuses past its bound", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.default = { state: "green", failing: [] };
    let clock = new Date(NOW);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
      now: () => clock,
      gateTimeoutMs: 60 * 60 * 1000,
    });

    let moved = false;
    const outcome = await driveToHold(
      async () => reconciler.reconcilePass(),
      (dispatched) => {
        for (const dispatch of dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
        if (!moved && dispatched.some((d) => d.tick_id === "clo1")) {
          moved = true;
          forge.moveHead(BRANCH, "head-after-closeout");
          forge.ciBySHA.set("head-after-closeout", { state: "none", failing: [] });
        }
      },
    );
    expect(outcome.terminal).toBe(false);
    expect(outcome.state).toBe("gating");
    const held = await readCheckpoint(contents);
    expect(held?.state).toBe("gating");
    expect(held?.reason).toContain("produced no check runs");

    // The same hold on the next pass is an OBSERVATION: the checkpoint keeps
    // its own updated_at, which is the durable start the bound measures.
    const before = (await contents.read(checkpointPath(RUN_ID)))!.content;
    const again = await reconciler.reconcilePass();
    expect(again.terminal).toBe(false);
    expect(again.state).toBe("gating");
    expect((await contents.read(checkpointPath(RUN_ID)))!.content).toBe(before);

    // Past the bound the run refuses rather than holding forever, and says
    // what a person must do: re-run the epic once CI concludes.
    clock = new Date(NOW.getTime() + 61 * 60 * 1000);
    const refused = await reconciler.reconcilePass();
    expect(refused.terminal).toBe(true);
    expect(refused.state).toBe("failed");
    expect(refused.reason).toContain("produced no check runs");
    expect(refused.reason).toContain("does not close the close-out");
    const checkpoint = await readCheckpoint(contents);
    expect(checkpoint?.state).toBe("failed");
    expect(checkpoint?.ticks?.find((t) => t.tick_id === "clo1")?.state).toBe("rejected");
  });

  it("bounds the admission's pending hold too, and refuses to admit past it", async () => {
    const contents = new MemoryContents(seededWithRule());
    const executor = new FakeExecutor(contents);
    const forge = new FakeForge();
    forge.default = { state: "pending", failing: [] };
    let clock = new Date(NOW);
    const reconciler = reconcilerFor(contents, executor, new FakeIntegration(), undefined, {
      pullRequests: forge,
      now: () => clock,
      gateTimeoutMs: 60 * 60 * 1000,
    });

    const outcome = await driveToHold(
      async () => reconciler.reconcilePass(),
      (dispatched) => {
        for (const dispatch of dispatched) {
          executor.finish(dispatch.tick_id, dispatch.attempt, finishFor(dispatch.tick_id));
        }
      },
    );
    expect(outcome.state).toBe("gating");
    // The hold re-derives from the PR on the next pass, still inside the bound.
    const again = await reconciler.reconcilePass();
    expect(again.state).toBe("gating");
    clock = new Date(NOW.getTime() + 61 * 60 * 1000);
    const refused = await reconciler.reconcilePass();
    expect(refused.terminal).toBe(true);
    expect(refused.state).toBe("failed");
    expect(refused.reason).toContain("was still pending");
    expect(refused.reason).toContain("does not admit the close-out");
    expect(executor.started.filter((s) => s.tick_id === "clo1").length).toBe(0);
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
    // The role-job exchanges landed on the run branch as decision records —
    // and D1 was never asked to hold any of the run's state. The runs index
    // is the submission control plane's, not the reconciler's; this run's
    // records are all in the repository it runs against, which is what
    // makes a resumed Workflow recoverable with no database at all (SPEC
    // §10.4, tick 9fc).
    const decisions = await contents.list(`.ticfac/runs/${runID}/decisions`);
    expect(decisions.length).toBe(2);
    for (const path of decisions) {
      expectRecordValid((await contents.read(path))!.content, "decision");
    }
    const indexed = await env.DB.prepare("SELECT run_id FROM runs WHERE run_id = ?")
      .bind(runID)
      .first<{ run_id: string }>();
    expect(indexed).toBeNull();
    expect(instance.id).toBe(runID);
    // The ENGINE's instance status is a second clock, and it lags the run's
    // own record: the checkpoint above already says completed, and on a fast
    // host the engine has caught up by the next line. On a 2-vCPU CI runner it
    // has not, and this assertion read it ONCE and failed — twice, on identical
    // source, while the durable evidence said the run had finished (tick lan).
    //
    // So wait for it, the way the sibling test below already does. What is
    // being asserted is that the engine eventually agrees with the record, not
    // that it agrees within one tick of the scheduler.
    const statusDeadline = Date.now() + 20_000;
    let status: { status?: string } = {};
    for (;;) {
      status = (await instance.status()) as { status?: string };
      const state = String(status.status);
      if (state !== "running" && state !== "queued") break;
      if (Date.now() > statusDeadline) {
        throw new Error(`timed out waiting for the Workflow engine; status: ${state}`);
      }
      await scheduler.wait(20);
    }
    expect(String(status.status)).toContain("complete");
    // The run released the publish slot on its way out (tick ef7): a finished
    // run must not wedge the repository behind its own slot.
    await expect(
      env.REPO_ROOMS.get(env.REPO_ROOMS.idFromName(PROJECT)).slotStatus(),
    ).resolves.toBeNull();
  });

  it("drives a gated close-out: the Workflow hands over a PR that carries the verdict and the findings", async () => {
    const runID = `${RUN_ID}-closeout-wf`;
    const contents = new MemoryContents(seededWithRule(runID));
    const forge = new FakeForge();
    forge.default = { state: "green", failing: [] };
    Object.assign(env, {
      TICK_CONTENTS: { project: PROJECT, ref: BRANCH, store: contents },
      TICFAC_EXECUTOR: new AutoFinishExecutor(),
      TICFAC_INTEGRATION: new FakeIntegration(),
      TICFAC_PULL_REQUESTS: { project: PROJECT, forge },
      TICFAC_RECONCILE_POLL_MS: 5,
    });

    await env.EPIC_RECONCILER!.create({
      id: runID,
      params: {
        run_id: runID,
        epic_id: EPIC_ID,
        project: PROJECT,
        branch: BRANCH,
        poll_interval_ms: 5,
      },
    });

    // Wait on the DURABLE EVIDENCE — the checkpoint the Workflow writes.
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

    // The run completed behind the gate, and the epic PR it opened carries
    // the final review's verdict and the run's findings — the record the
    // person merging reads beside CI, not CI status alone.
    expect(checkpoint?.state).toBe("completed");
    expect(forge.opened.length).toBe(1);
    expect(forge.opened[0].headRef).toBe(BRANCH);
    expect(forge.opened[0].body).toContain("pushed and reported");
    expect(forge.opened[0].body).toContain("A defect outside the discovering tick");
    // The close gate's last write is the same view, overwritten once — never
    // appended to, however many passes the Workflow took.
    expect(forge.bodies.length).toBe(1);
    expect(forge.bodies[0].body).toBe(forge.opened[0].body);
    const decisions = await contents.list(`.ticfac/runs/${runID}/decisions`);
    expect(decisions.length).toBe(2);
  });

  it("refuses to run a second Workflow that cannot take the publish slot, and writes nothing", async () => {
    const contents = sharedContents();
    Object.assign(env, {
      TICK_CONTENTS: { project: PROJECT, ref: BRANCH, store: contents },
      TICFAC_RECONCILE_POLL_MS: 5,
    });

    // Pre-hold the repository's one publish slot with a run that is not this
    // one — the shape two concurrent runs of one epic actually take.
    const room = env.REPO_ROOMS.get(env.REPO_ROOMS.idFromName(PROJECT));
    const held = await room.acquireSlot({ run_id: "run_other", epic: EPIC_ID });
    if (!held.ok) throw new Error("expected to pre-hold the publish slot");

    const runID = `${RUN_ID}-slot-refused`;
    const instance = await env.EPIC_RECONCILER!.create({
      id: runID,
      params: {
        run_id: runID,
        epic_id: EPIC_ID,
        project: PROJECT,
        branch: BRANCH,
        poll_interval_ms: 5,
      },
    });

    // Wait on the DURABLE EVIDENCE — the Workflow's own terminal status —
    // never on a guessed sleep.
    const deadline = Date.now() + 20_000;
    let status: { status?: string; output?: unknown } = {};
    for (;;) {
      status = (await instance.status()) as { status?: string; output?: unknown };
      const state = String(status.status);
      if (state !== "running" && state !== "queued") break;
      if (Date.now() > deadline) {
        throw new Error(`timed out waiting for the Workflow; status: ${state}`);
      }
      await scheduler.wait(20);
    }

    expect(String(status.status)).toContain("complete");
    const output = status.output as { state?: string; reason?: string };
    expect(output.state).toBe("failed");
    expect(output.reason).toContain("run_other");
    // It stopped BEFORE anything was published: the repository holds no run
    // state for a run that never held its slot — writing the failure would
    // itself have been the publish it was refused.
    expect(await contents.read(checkpointPath(runID))).toBeNull();
    // And the pre-held slot is untouched: the refused run released nothing.
    await expect(room.slotStatus()).resolves.toMatchObject({ run_id: "run_other" });
    await room.releaseSlot({ run_id: "run_other", token: held.lease.token });
  });

  // The slot-inspection the lapse test needs: read past the room's public
  // API the way repo-room's own tests do (the token is withheld from every
  // view by design, but the row is what production compares tokens against).
  async function slotToken(stub: DurableObjectStub<RepoRoom>): Promise<string | null> {
    let token: string | null = null;
    await runInDurableObject(stub, (_instance, state) => {
      const rows = [...state.storage.sql.exec<{ token: string }>("SELECT token FROM publish_slot")];
      token = rows[0]?.token ?? null;
    });
    return token;
  }

  /**
   * Expires the slot DELIBERATELY: the row's deadline moves into the past —
   * which is all "the lease lapsed" is; the room's own clock is its only
   * reader — and then the room's own alarm sweeps the row, the way a long
   * pass or a restart that outlives the ttl really delivers a lapse.
   */
  async function expireSlotNow(stub: DurableObjectStub<RepoRoom>): Promise<void> {
    await runInDurableObject(stub, (_instance, state) => {
      state.storage.sql.exec(
        "UPDATE publish_slot SET expires_at = ? WHERE id = 'slot'",
        Date.now() - 1,
      );
    });
    await runDurableObjectAlarm(stub);
  }

  it("completes a pass's writes under the re-acquired token after the lease lapses (tick e9n)", async () => {
    const contents = sharedContents();
    const executor = new AutoFinishExecutor();
    const integration = new FakeIntegration();
    // The poll beat is a full second so the test gets a named window — the
    // sleep between passes — in which the slot is held, the pass is over,
    // and no write is in flight. The slot's own ttl stays the 60s floor
    // (three poll beats, the Workflow's rule); the lapse below is DELIVERED,
    // not waited for.
    const POLL_MS = 1_000;

    Object.assign(env, {
      TICK_CONTENTS: { project: PROJECT, ref: BRANCH, store: contents },
      TICFAC_EXECUTOR: executor,
      TICFAC_INTEGRATION: integration,
      TICFAC_RECONCILE_POLL_MS: POLL_MS,
    });

    const room = env.REPO_ROOMS.get(env.REPO_ROOMS.idFromName(PROJECT));
    const runID = `${RUN_ID}-lapsed-slot`;
    await env.EPIC_RECONCILER!.create({
      id: runID,
      params: {
        run_id: runID,
        epic_id: EPIC_ID,
        project: PROJECT,
        branch: BRANCH,
        poll_interval_ms: POLL_MS,
      },
    });

    // Pass 0's verdict checkpoint is its last write. Waiting on it — the
    // run's own record, never a guessed sleep — parks the test inside the
    // sleep that follows the pass, with the slot held and live and nothing
    // in flight.
    const opened = Date.now() + 25_000;
    for (;;) {
      if ((await contents.read(checkpointPath(runID))) !== null) break;
      if (Date.now() > opened) throw new Error("timed out waiting for pass 0 to open the run");
      await scheduler.wait(5);
    }
    const before = await slotToken(room);
    if (before === null) throw new Error("expected the run to hold the publish slot");

    // THE DELIBERATE LAPSE: the deadline passes, the room's alarm sweeps the
    // row, and the repository observably has no writer at all — what a
    // long pass or a restart outliving the ttl really leaves behind.
    await expireSlotNow(room);
    await expect(room.slotStatus()).resolves.toBeNull();

    // The next pass's heartbeat finds no slot and re-acquires it — waited on
    // as the durable evidence. A re-acquire after a lapse mints a NEW token:
    // the old one died with the swept row, and the publisher compares
    // tokens from here on.
    const reacquired = Date.now() + 25_000;
    for (;;) {
      const held = await room.slotStatus();
      if (held !== null) break;
      if (Date.now() > reacquired) {
        throw new Error("timed out waiting for the slot to be re-acquired");
      }
      await scheduler.wait(5);
    }
    const after = await slotToken(room);
    expect(after).not.toBe(before);

    // THE ACCEPTANCE: the pass after the lapse COMPLETES ITS WRITES under the
    // re-acquired token — the tick closes and attempt markers it publishes
    // pass a publisher that compares `after`, and the run goes on to finish.
    // With the stale token the first of those writes is refused `not_holder`,
    // the pass dies, and the checkpoint never leaves the lapsed state.
    const end = Date.now() + 25_000;
    let checkpoint: Checkpoint | null = null;
    for (;;) {
      const file = await contents.read(checkpointPath(runID));
      if (file !== null) {
        checkpoint = JSON.parse(file.content) as Checkpoint;
        if (checkpoint.state === "completed" || checkpoint.state === "failed") break;
      }
      if (Date.now() > end) {
        throw new Error(`timed out waiting for the run; checkpoint: ${JSON.stringify(checkpoint)}`);
      }
      await scheduler.wait(20);
    }
    expect(checkpoint?.state).toBe("completed");
    expectRecordValid((await contents.read(checkpointPath(runID)))!.content, "checkpoint");
    expect(integration.integrated.length).toBeGreaterThan(0);
    // And the finished run wedged nobody behind its slot.
    await expect(room.slotStatus()).resolves.toBeNull();
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
      // The container's own addressing, the same shape the unit-suite fake
      // returns and the reconciler must persist (tick t5p): the markers the
      // engine-driven runs write are the real record, not a leaner stand-in.
      sandbox: attemptSandboxName(spec.run_id, spec.tick_id, spec.attempt),
      process_id: `proc-${spec.attempt}`,
      branch: `ticfac/${spec.epic_id}/${spec.tick_id}`,
      base_sha: "f".repeat(40),
      launched: true,
      detail: "auto-finished",
      run_id: spec.run_id,
      epic_id: spec.epic_id,
      project: spec.project,
      base_ref: spec.base_ref,
      title: spec.title,
    };
  }

  async inspect(handle: AttemptHandle): Promise<AttemptStatus> {
    // The same demand the unit-suite fake makes (tick t5p): an executor that
    // answers any handle at all is the forgiving fake that certified the
    // defect, so the engine-driven runs fail over a marker that never
    // recorded what start returned, too.
    if (typeof handle.sandbox !== "string" || handle.sandbox === "") {
      throw new Error(
        "this handle carries no container: the marker it came from never recorded what start returned",
      );
    }
    return { state: "exited", exit_code: 0 };
  }

  async collect(): Promise<AttemptReport> {
    return { outcome: "done", commits: 2, detail: "pushed and reported" };
  }

  async cancel(): Promise<void> {}
}
