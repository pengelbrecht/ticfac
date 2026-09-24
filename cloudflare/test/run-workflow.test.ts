import { env, runInDurableObject, SELF } from "cloudflare:test";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { type RunRecord, readHarnessOutput, readRunRecord, reconcileKey } from "../src/artifacts";
import {
  enrolProject,
  getRun,
  getRunProgress,
  listDispatchLogs,
  listRunGatewayTokens,
} from "../src/db";
import { GATEWAY_PATH_PREFIX, proxyModelRequest } from "../src/gateway";
import type { RepoRefs } from "../src/progress";
import type { RepoConfigReader } from "../src/repo-config";
import { DONE_EVENT_TYPE } from "../src/run-done";
import type { RunEventMessage, RunEventSink } from "../src/run-events";
import type { DispatchLease } from "../src/run-room";
import {
  applyProgress,
  leaseLostTrip,
  MAX_SANDBOX_BOOTS,
  type RunOutcome,
} from "../src/run-workflow";
import { roomFor, runStatus, runWorkflowBinding, startRun, stopRun, submitRun } from "../src/runs";
import {
  DEFAULT_SANDBOX_IMAGE,
  ORCHESTRATOR_COMMAND,
  type OrchestratorSandbox,
  type SandboxBinding,
  type SandboxOutput,
  type SandboxProcessState,
  type SandboxProcessView,
  sandboxName,
} from "../src/sandbox";
import { EPIC_TYPE, type TrackerReader } from "../src/tick-membership";

/**
 * The Run Workflow: boot one orchestrator sandbox, watch it, enforce the
 * budgets, finalize.
 *
 * These drive the REAL Workflow inside workerd — the binding from
 * wrangler.toml, the real RunRoom lease, the real D1 index, the real R2
 * bucket. The only substitution is the container itself, through the
 * `SANDBOXES` seam: a fake sandbox is what lets a test kill an orchestrator
 * mid-run and read the log stream while the run is still going, which is
 * exactly what this tick has to prove.
 */

// --------------------------------------------------------- the fake sandbox ---

class FakeProcess {
  state: SandboxProcessState = "running";
  exit_code: number | null = null;
  output = "";
  killed = false;

  constructor(
    readonly id: string,
    readonly command: string,
    readonly env: Record<string, string>,
  ) {}

  /** Print something, as a harness would while it works. */
  say(text: string): void {
    this.output += text;
  }

  exit(code: number): void {
    this.state = code === 0 ? "completed" : "failed";
    this.exit_code = code;
  }

  get view(): SandboxProcessView {
    return { id: this.id, state: this.state, exit_code: this.exit_code };
  }
}

class FakeSandbox implements OrchestratorSandbox {
  readonly processes: FakeProcess[] = [];
  destroyed = false;
  /** The container died and came back empty: it no longer knows its process. */
  vanished = false;
  /** cr4: how many times the watch loop asked this container for its process. */
  looked = 0;
  /** tick s7f: how many times the reconcile has read this container's process list. */
  listed = 0;
  #next = 0;

  constructor(readonly name: string) {}

  async startProcess(
    command: string,
    options: { env: Record<string, string> },
  ): Promise<SandboxProcessView> {
    const process = new FakeProcess(`${this.name}-p${++this.#next}`, command, options.env);
    this.processes.push(process);
    return process.view;
  }

  async getProcess(id: string): Promise<SandboxProcessView | null> {
    this.looked += 1;
    if (this.vanished) return null;
    const process = this.processes.find((p) => p.id === id);
    return process === undefined ? null : process.view;
  }

  /**
   * The live sandbox list (tick s7f). A vanished container answers with an
   * empty list, which is what a container that died and came back looks like —
   * and is deliberately NOT the same as the listing throwing.
   */
  async listProcesses(): Promise<SandboxProcessView[]> {
    this.listed += 1;
    if (this.vanished) return [];
    return this.processes.map((p) => ({ ...p.view, command: p.command }));
  }

  async readOutput(id: string, offset: number): Promise<SandboxOutput> {
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined || this.vanished) return { text: "", offset };
    return { text: process.output.slice(offset), offset: process.output.length };
  }

  async killProcess(id: string): Promise<void> {
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined) return;
    process.killed = true;
    process.state = "failed";
    process.exit_code = 143;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
  }

  get current(): FakeProcess {
    const process = this.processes.at(-1);
    if (process === undefined) throw new Error(`sandbox ${this.name} started nothing`);
    return process;
  }
}

class FakeSandboxes implements SandboxBinding {
  readonly booted: FakeSandbox[] = [];
  /** The image each `get` asked for — the boot's own parameter, per tick 3q2. */
  readonly requestedImages: (string | undefined)[] = [];
  /** cr4: the keepAlive each `get` asked for, in boot order. */
  readonly requestedKeepAlive: (boolean | undefined)[] = [];
  /**
   * tick s7f: throw on the Nth `get` of this name, once.
   *
   * This is how a test kills the supervisor MID-WAVE. The dispatch step is
   * the one that boots containers, and a supervisor that dies inside it is
   * replaced by one that runs the same step again — so a throw placed after
   * the work processes exist reproduces the replacement exactly, with no
   * waiting on a clock.
   */
  failGetAt: { name: string; nth: number; evict?: boolean } | null = null;
  /** How many times each name has been addressed, for `failGetAt`. */
  readonly #gets = new Map<string, number>();
  readonly #byName = new Map<string, FakeSandbox>();

  /**
   * tick s7f: the container is evicted and no snapshot restores it. Addressing
   * the same name again provisions an EMPTY container, which is what a cold
   * recovery actually looks like.
   */
  discard(name: string): void {
    this.#byName.delete(name);
  }

  async get(
    name: string,
    options?: { image?: string; keepAlive?: boolean },
  ): Promise<OrchestratorSandbox> {
    const nth = (this.#gets.get(name) ?? 0) + 1;
    this.#gets.set(name, nth);
    if (this.failGetAt !== null && this.failGetAt.name === name && this.failGetAt.nth === nth) {
      const { evict } = this.failGetAt;
      this.failGetAt = null;
      // `evict` is the no-snapshot case: the supervisor and the container die
      // together, so addressing the name again provisions an empty one.
      if (evict === true) this.discard(name);
      throw new Error(`the supervisor lost ${name} mid-wave`);
    }
    this.requestedImages.push(options?.image);
    this.requestedKeepAlive.push(options?.keepAlive);
    let sandbox = this.#byName.get(name);
    if (sandbox === undefined) {
      sandbox = new FakeSandbox(name);
      this.#byName.set(name, sandbox);
      this.booted.push(sandbox);
    }
    return sandbox;
  }

  /** The sandbox the run is currently working in. */
  get latest(): FakeSandbox {
    const sandbox = this.booted.at(-1);
    if (sandbox === undefined) throw new Error("no sandbox has been booted");
    return sandbox;
  }

  /** tick s7f: how many times a name has been addressed. */
  addressed(name: string): number {
    return this.#gets.get(name) ?? 0;
  }

  /** tick b6e: the sandbox already booted under this exact name. */
  named(name: string): FakeSandbox {
    const sandbox = this.#byName.get(name);
    if (sandbox === undefined) throw new Error(`no sandbox named ${name} was booted`);
    return sandbox;
  }

  /** The process of the sandbox booted with this phase, or undefined. */
  phase(phase: string): FakeProcess | undefined {
    for (const sandbox of this.booted) {
      for (const process of sandbox.processes) {
        if (process.env.TICKS_PHASE === phase) return process;
      }
    }
    return undefined;
  }
}

// -------------------------------------------------------------- the remote ---

/**
 * The durable layer, faked: the branch heads a run's work would land on.
 *
 * Nothing else in this file can stand in for it. A fake sandbox exiting 0 is
 * exactly the run tick ehy is about — the boot chain worked, the harness had
 * nothing to say, and NOTHING HAPPENED — so a test that wants a `completed`
 * run has to push something here, the same way a real orchestrator would.
 */
class FakeRepo implements RepoRefs {
  refs: Record<string, string> = { main: BASE_SHA };
  /** Set to make the remote unreadable, as a GitHub outage would. */
  unreadable: string | null = null;
  reads = 0;

  async list(): Promise<Record<string, string>> {
    this.reads += 1;
    if (this.unreadable !== null) throw new Error(this.unreadable);
    return { ...this.refs };
  }

  /** What an orchestrator that did work leaves behind. */
  push(branch: string, sha: string): void {
    this.refs[branch] = sha;
  }

  /** Merging the epic branch and cleaning it up. */
  deleteBranch(branch: string): void {
    delete this.refs[branch];
  }
}

/**
 * The repository's tracked config, faked: what `[sandbox]` a run's checkout
 * declares at the submitted SHA.
 *
 * The control plane reads this BEFORE it boots anything, because the image a
 * repository declares is the image the run has to get (tick x3v) — and a
 * declaration cannot be read out of a container that is already running the
 * wrong one.
 */
class FakeRepoConfig implements RepoConfigReader {
  /** The file's text, or null for a repository that tracks none. */
  source: string | null = null;
  /** Set to make the file unreadable, as a GitHub outage would. */
  unreadable: string | null = null;
  readonly asked: { project: string; ref: string }[] = [];

  /**
   * tick k24: something to do INSIDE the read, once.
   *
   * The control plane resolves this file before it credentials anything, so a
   * hook here is a synchronization seam with no timing in it: whatever it does
   * has provably happened before the first kill-check runs.
   */
  onRead: (() => Promise<void>) | null = null;

  async read(project: string, ref: string): Promise<string | null> {
    this.asked.push({ project, ref });
    const hook = this.onRead;
    if (hook !== null) {
      this.onRead = null;
      await hook();
    }
    if (this.unreadable !== null) throw new Error(this.unreadable);
    return this.source;
  }

  /** What a repository pinning its own orchestrator image tracks. */
  declare(image: string): void {
    this.source = `version = 2\n\n[sandbox]\nimage = "${image}"\n`;
  }
}

/**
 * The tracker as GitHub would serve it at one commit (tick kya): one JSON
 * record per tick, which is how `POST /api/wave` proves the ticks a container
 * asks for belong to the run's epic.
 *
 * Empty by default, and that is the 99% path here rather than an omission: an
 * unreadable tracker is a NON-answer, so a wave the module cannot check is
 * dispatched rather than refused. Only the tests that are about the check
 * populate one.
 */
class FakeTracker implements TrackerReader {
  private readonly records = new Map<string, Record<string, unknown>>();

  tick(id: string, parent: string, type = "task"): this {
    this.records.set(id, { id, title: id, status: "open", type, parent });
    return this;
  }

  epic(id: string): this {
    this.records.set(id, { id, title: id, status: "open", type: EPIC_TYPE });
    return this;
  }

  async read(_project: string, _ref: string, tickID: string): Promise<string | null> {
    const record = this.records.get(tickID);
    return record === undefined ? null : JSON.stringify(record);
  }
}

/**
 * A cloud wave's per-tick verdicts, faked (tick b6e): the durable git layer
 * `collectFromGithub` would read, without a real repository.
 *
 * Defaults every tick to `ready-to-merge` — the common case — so a test only
 * has to `set` the ticks it cares about making say something else.
 */
// ------------------------------------------------------------------ harness ---

const GATEWAY = "https://gateway.ai.cloudflare.com/v1/account/ticks";
const FACTORY = "https://factory.example.com";
const PROJECT = "example-org/example-repo";
const BASE_SHA = "a".repeat(40);

let sandboxes: FakeSandboxes;
let repo: FakeRepo;
let repoConfig: FakeRepoConfig;
let tracker: FakeTracker;
const saved: Record<string, unknown> = {};

/** Overrides a binding for one test, remembering what to put back. */
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

/**
 * Makes EVERY `INSERT INTO dispatch_log` whose `decision` matches throw, on
 * the real D1 binding — every other statement (including dispatch-log
 * inserts with a non-matching decision) still hits the real database. A
 * persistent failure, not a one-shot: a Workflow step retry re-runs the same
 * code, so a test that only fails the first attempt cannot tell a bug that
 * self-heals on retry from one that is actually fixed — both would let the
 * run settle. Only a write that never succeeds tells them apart: the old,
 * unguarded `await logDispatch(...)` at the tail of `finalize` retried the
 * WHOLE step (and so re-ran everything above it) up to `FINALIZE_RETRIES`
 * times; the fixed, caught write lets `finalize` succeed on its first and
 * only attempt regardless. Returns the attempt counter so a test can assert
 * on it directly rather than on a state a retry could still reach eventually.
 */
function _failEveryDispatchLogInsert(matches: (decision: string) => boolean): {
  attempts(): number;
} {
  const db = env.DB as D1Database;
  let attempts = 0;
  set(
    "DB",
    new Proxy(db, {
      get(target, property, receiver) {
        if (property !== "prepare") {
          const value = Reflect.get(target, property, receiver);
          return typeof value === "function" ? value.bind(target) : value;
        }
        return (sql: string) => {
          const stmt = target.prepare(sql);
          if (!sql.includes("INSERT INTO dispatch_log")) return stmt;
          return new Proxy(stmt, {
            get(stmtTarget, stmtProperty, stmtReceiver) {
              if (stmtProperty !== "bind") {
                const value = Reflect.get(stmtTarget, stmtProperty, stmtReceiver);
                return typeof value === "function" ? value.bind(stmtTarget) : value;
              }
              return (...args: unknown[]) => {
                const bound = (stmtTarget.bind as (...a: unknown[]) => D1PreparedStatement)(
                  ...args,
                );
                const decision = args[2];
                if (typeof decision !== "string" || !matches(decision)) return bound;
                return new Proxy(bound, {
                  get(boundTarget, boundProperty, boundReceiver) {
                    if (boundProperty !== "run") {
                      const value = Reflect.get(boundTarget, boundProperty, boundReceiver);
                      return typeof value === "function" ? value.bind(boundTarget) : value;
                    }
                    return async () => {
                      attempts++;
                      throw new Error("simulated dispatch_log write failure");
                    };
                  },
                });
              };
            },
          });
        };
      },
    }),
  );
  return { attempts: () => attempts };
}

// A run a test started must not outlive the test (ticfac tick 3cq).
//
// The Workflow engine is shared by every test in this file, and each test
// installs a fresh FakeSandboxes. When one test times out with its run still
// going - on CI under load: 'run run_wf_59 to finish - saw: row=stopping
// workflow=running' - that Workflow keeps booting containers, and they land in
// the NEXT test's fake: 'the orchestrator to start - saw: run_wf_59-3(0 proc),
// run_wf_61-1(1 proc), ...'. The next test watched booted[0], a stranger with
// no process, and timed out too, and so on down the file - one slow test
// became four to six failures, a different block each time. The fix is
// containment, not a longer wait: whatever a test started and did not finish
// is terminated when it ends, and firstProcess only ever looks at this test's
// own runs.
let runsBeforeThisTest = new Set<string>();
const WORKFLOW_OVER = new Set(["complete", "errored", "terminated"]);

async function allRunIDs(): Promise<string[]> {
  const rows = await env.DB.prepare("SELECT run_id FROM runs").all<{ run_id: string }>();
  return (rows.results ?? []).map((row) => row.run_id);
}

/** A sandbox that belongs to a run started before this test - a stranger here. */
function fromAnEarlierTest(sandbox: FakeSandbox): boolean {
  for (const id of runsBeforeThisTest) {
    if (sandbox.name === id || sandbox.name.startsWith(`${id}-`)) return true;
  }
  return false;
}

beforeEach(async () => {
  runsBeforeThisTest = new Set(await allRunIDs());
});

afterEach(async () => {
  const workflow = runWorkflowBinding(env);
  if (workflow === null) return;
  for (const id of (await allRunIDs()).filter((run) => !runsBeforeThisTest.has(run))) {
    let instance: Awaited<ReturnType<typeof workflow.get>>;
    try {
      instance = await workflow.get(id);
    } catch {
      continue; // no Workflow was ever created for this row
    }
    let status: string;
    try {
      status = (await instance.status()).status;
    } catch {
      continue; // an engine that never started has nothing to leak
    }
    if (!WORKFLOW_OVER.has(status)) {
      await (instance as unknown as { terminate(): Promise<void> })
        .terminate()
        .catch(() => undefined);
    }
  }
});

beforeEach(() => {
  sandboxes = new FakeSandboxes();
  set("SANDBOXES", sandboxes);
  // The remote a run's progress is proved against. It starts where the
  // submission did, so a run that pushes nothing has moved nothing.
  repo = new FakeRepo();
  set("REPO_REFS", repo);
  // The checkout's own `[sandbox]` declaration. It declares nothing by
  // default, which is the 99% path: the deployment's image stands.
  repoConfig = new FakeRepoConfig();
  set("REPO_CONFIG", repoConfig);
  // The tracker `POST /api/wave` checks a requested wave against. Knows
  // nothing by default — see FakeTracker: a tracker that cannot be read does
  // not refuse a wave, so every test that is not about the check is unaffected
  // by it, and none of them reaches for GitHub.
  tracker = new FakeTracker();
  set("TICK_TRACKER", tracker);
  set("SANDBOX_IMAGE", undefined);
  set("AI_GATEWAY_BASE_URL", GATEWAY);
  set("CLOUDFLARE_API_TOKEN", undefined);
  // A run's model traffic goes through this deployment's own gateway proxy
  // (D17), so the Workflow has to know the factory's public URL to hand a
  // gateway to a sandbox. `tk factory deploy` writes it.
  set("FACTORY_BASE_URL", FACTORY);
  set("ANTHROPIC_API_KEY", "sk-operator-key");
  // The kill-switch cases below drive the anthropic route, which a default
  // factory refuses on billing grounds (tick fw6) — this one opted in.
  set("GATEWAY_ALLOWED_PROVIDERS", "anthropic");
  // A tight, fixed cadence: the supervision loop's own backoff is not what
  // these tests are about, and an explicit interval is a supported override.
  set("RUN_POLL_INTERVAL_MS", "25");
  set("RUN_STOP_GRACE_MS", "50");
  set("RUN_MAX_WALL_CLOCK_MS", "600000");
  // No explicit cost budget by default: the harness intentionally has no
  // Cloudflare API token, so this path must remain runnable and record unknown
  // spend rather than refusing every test run.
  set("RUN_MAX_COST_USD", undefined);
});

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

let counter = 0;

/** Takes the lease and ignites a run, exactly as the submit route does. */
async function ignite(
  overrides: {
    epic?: string;
    project?: string;
    leaseTtlMs?: number;
    /**
     * Tick l6t: a tick_ids field from before the wave deletion, passed into
     * the Workflow's params as a replayed instance would carry it — inert.
     */
    staleTickIDs?: string[];
    /**
     * tick k24: runs with the run id in hand, before the Workflow exists, so a
     * test can arm a seam that has to be in place before the very first step.
     */
    beforeStart?: (runID: string) => void;
    /**
     * tick oen: the lease has already lapsed when the Workflow starts — what
     * a boot longer than the acquire ttl leaves behind, made deterministic
     * instead of raced against a real clock.
     */
    lapsed?: boolean;
  } = {},
) {
  const project = overrides.project ?? `${PROJECT}-${++counter}`;
  const epic = overrides.epic ?? "ko8";
  const runID = `run_wf_${++counter}`;
  const room = roomFor(env, project);
  const lease = await room.acquireDispatchLease({
    run_id: runID,
    epic,
    origin: "cloud",
    ...(overrides.leaseTtlMs === undefined ? {} : { ttl_ms: overrides.leaseTtlMs }),
  });
  if (!lease.ok) throw new Error(`the lease was refused: ${JSON.stringify(lease)}`);
  if (overrides.lapsed === true) await expireLease(project);
  overrides.beforeStart?.(runID);
  const started = await startRun(env, {
    run_id: runID,
    project,
    epic,
    base_sha: BASE_SHA,
    requested_by: "operator",
    lease_token: lease.lease.token,
  });
  if (overrides.staleTickIDs !== undefined) {
    // A params blob from before tick l6t, replayed after it: the field is
    // passed straight into the Workflow's create call, as an old serialised
    // instance would hand it back, and must be inert.
    await runWorkflowBinding(env)!.create({
      id: runID,
      params: {
        run_id: runID,
        project,
        epic,
        base_sha: BASE_SHA,
        requested_by: "operator",
        lease_token: lease.lease.token,
        tick_ids: overrides.staleTickIDs,
      } as never,
    });
  }
  return {
    runID,
    project,
    epic,
    started,
    room,
    lease: lease.lease,
    leaseToken: lease.lease.token,
  };
}

/**
 * `runInDurableObject` finds the worker's own Durable Object namespaces by
 * reading `env` ONCE, on its first call, and asserts every one of them is a
 * namespace. Every test here replaces `env.SANDBOXES` (a Durable Object
 * binding) with a fake before it runs, so a first call from inside a test
 * fails that assertion. This makes the first call before any test has
 * replaced anything.
 */
beforeAll(async () => {
  await runInDurableObject(roomFor(env, `${PROJECT}-warm`), () => {});
});

/**
 * Expires a project's dispatch lease NOW, past the public API (tick oen). The
 * row stays exactly as its holder left it and only its deadline has passed —
 * a lapse with nobody else holding the lease, without racing a real clock.
 */
async function expireLease(project: string): Promise<void> {
  await runInDurableObject(roomFor(env, project), (_instance, state) => {
    state.storage.sql.exec("UPDATE dispatch_lease SET expires_at = ?", Date.now() - 1);
  });
}

/**
 * Lets a project's lease lapse and hands it to another run in ONE turn of the
 * room, so a working run's renewal cannot land in between and reclaim it
 * (tick oen). Two separate calls would race the run's own watch loop.
 */
async function handLeaseTo(project: string, runID: string, epic = "ko8"): Promise<DispatchLease> {
  return runInDurableObject(roomFor(env, project), async (instance, state) => {
    state.storage.sql.exec("UPDATE dispatch_lease SET expires_at = ?", Date.now() - 1);
    const taken = await instance.acquireDispatchLease({ run_id: runID, epic });
    if (!taken.ok) throw new Error(`the lease was refused: ${JSON.stringify(taken)}`);
    return taken.lease;
  });
}

async function waitFor<T>(
  what: string,
  probe: () => Promise<T | null | undefined | false>,
  timeoutMs = 15_000,
  describe?: () => Promise<string>,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await probe();
    if (value !== null && value !== undefined && value !== false) return value;
    if (Date.now() > deadline) {
      // What the wait SAW, not only that it ran out (ticfac tick 3cq): these
      // timeouts appear only under CI load, a different test each time, and
      // "timed out" alone has never been enough to say why.
      const seen =
        describe === undefined
          ? ""
          : await describe().catch((error: unknown) => `(could not describe: ${String(error)})`);
      throw new Error(`timed out waiting for ${what}${seen === "" ? "" : ` - saw: ${seen}`}`);
    }
    await scheduler.wait(10);
  }
}

/** The fake's sandboxes as a waiting test sees them: name and process count, in boot order. */
function describeSandboxes(): Promise<string> {
  const list = sandboxes.booted.map(
    (sandbox) => `${sandbox.name}(${sandbox.processes.length} proc)`,
  );
  return Promise.resolve(list.length === 0 ? "no sandbox booted" : list.join(", "));
}

/** A run's row state and its Workflow instance's own status. */
async function describeRun(runID: string): Promise<string> {
  const row = (await getRun(env.DB, runID))?.state ?? "no row";
  let workflow = "no instance";
  const binding = runWorkflowBinding(env);
  if (binding !== null) {
    try {
      workflow = (await (await binding.get(runID)).status()).status;
    } catch (error) {
      workflow = `status unreadable: ${String(error)}`;
    }
  }
  return `run ${runID} row=${row} workflow=${workflow}; sandboxes: ${await describeSandboxes()}`;
}

const runState = (runID: string) => getRun(env.DB, runID).then((run) => run?.state ?? null);

/** Waits until the run reaches one of the terminal index states. */
/** Where the stubbed Cloudflare API answers; nothing else may reach the network. */
const LOGS_API = "https://api.cloudflare.example/client/v4";

/**
 * Stands in for the Cloudflare logs API on the global fetch, recording the
 * filters each read sent and refusing a dotted metadata key exactly as the
 * real API does (error 7001 over an HTTP 400).
 */
/**
 * The AI Gateway logs API, refusing what it really refuses.
 *
 * It rejects a filter key outside its enum AND a `per_page` above 50 — both
 * are 400s that grounded a budgeted live run, and a stub that answered 200 to
 * either is what let them ship. `refuse` forces a status for the tests that
 * care what the operator is told, rather than whether the query was accepted.
 */
function stubLogsAPI(
  cost: number,
  refuse?: { status: number; message: string },
  calls = 1,
): {
  filters: { key: string; operator: string; value: unknown[] }[][];
  restore: () => void;
  /** tick k24: what each logged call costs from here on. */
  setCost: (next: number) => void;
} {
  const filters: { key: string; operator: string; value: unknown[] }[][] = [];
  let perCall = cost;
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    if (!url.startsWith(LOGS_API)) return original(input as RequestInfo, init);
    const sent = JSON.parse(new URL(url).searchParams.get("filters") ?? "[]") as {
      key: string;
      operator: string;
      value: unknown[];
    }[];
    filters.push(sent);
    for (const filter of sent) {
      if (!["metadata.key", "metadata.value"].includes(filter.key)) {
        return Response.json(
          {
            success: false,
            errors: [{ code: 7001, message: `Invalid enum value. received "${filter.key}"` }],
            result: null,
          },
          { status: 400 },
        );
      }
    }
    const perPage = Number(new URL(url).searchParams.get("per_page") ?? "0");
    if (perPage > 50) {
      return Response.json(
        {
          success: false,
          errors: [{ code: 7003, message: "Number must be less than or equal to 50" }],
          result: null,
        },
        { status: 400 },
      );
    }
    if (refuse !== undefined) {
      return Response.json(
        { success: false, errors: [{ message: refuse.message }], result: null },
        { status: refuse.status },
      );
    }
    const runID = String((sent[1]?.value ?? [])[0] ?? "");
    const page = Number(new URL(url).searchParams.get("page") ?? "1");
    const rows = Math.max(0, Math.min(perPage, calls - (page - 1) * perPage));
    return Response.json({
      success: true,
      result: Array.from({ length: rows }, () => ({
        cost: perCall,
        metadata: { run_id: runID, tick_id: "ko8" },
      })),
      // The API's real result_info: count/page/per_page/total_count, and no
      // `total_pages`. A stub that invented that field let a read stop after
      // one page and report 6% of a run's spend as its total.
      result_info: { count: rows, page, per_page: perPage, total_count: calls },
    });
  }) as typeof fetch;
  return {
    filters,
    restore: () => void (globalThis.fetch = original),
    setCost: (next: number) => void (perCall = next),
  };
}

async function settled(runID: string) {
  return waitFor(
    `run ${runID} to finish`,
    async () => {
      const run = await getRun(env.DB, runID);
      if (run === null) return null;
      return ["completed", "stopped", "failed"].includes(run.state) ? run : null;
    },
    // A stop or a closeout under CI load legitimately takes longer than a
    // boot does; the containment above is what keeps a slow one from
    // becoming a cascade, not this number.
    45_000,
    () => describeRun(runID),
  );
}

/** The SHA an orchestrator's pushed work lands at. */
const PUSHED_SHA = "b".repeat(40);

/**
 * What a run that actually did something leaves on the remote.
 *
 * Called before the harness exits in every test that expects `completed`: since
 * tick ehy the exit status alone cannot say the epic moved, so a test asserting
 * completion has to supply the evidence that says it.
 */
function orchestratorPushedWork(epic = "ko8"): void {
  repo.push(`epic/${epic}`, PUSHED_SHA);
}

/**
 * The integrate-and-plan orchestrator a cloud wave hands off to (tick wiy).
 *
 * A successful wave is followed by a `wave` pass, not a `closeout` one: the
 * run is continuing, not being wound up. That pass integrates what the
 * containers pushed and then either requests the next wave (through
 * `POST /api/wave`, which these tests drive via `requestNextWave`) or exits
 * having finished the epic. `closeout` still exists and still boots — for a
 * stop, a budget trip, and a run that hit its wave ceiling.
 */
async function _wavePass(pass = 1): Promise<FakeProcess> {
  return waitFor(`the integrate-and-plan orchestrator for pass ${pass}`, async () => {
    for (const sandbox of sandboxes.booted) {
      for (const process of sandbox.processes) {
        if (process.env.TICKS_PHASE === "wave" && process.env.TICKS_PASS === String(pass)) {
          return process;
        }
      }
    }
    return null;
  });
}

/**
 * What the orchestrator does at the end of a `wave` pass: compute the next
 * wave with Go's own `wave.Compute` (`tk graph`) and ask the factory to
 * dispatch it.
 *
 * Driven through `SELF.fetch` rather than by calling `requestWave` directly,
 * because half of what this proves is that a SANDBOX can reach the door at
 * all: the route is exempt from the operator's factory token and authorized
 * by the run's own gateway credential, and a test that bypassed the worker's
 * auth gate would prove nothing about either.
 */
async function _requestNextWave(
  process: FakeProcess,
  body: { epic: string; pass: number; base_sha: string; tick_ids: string[] },
): Promise<Response> {
  return SELF.fetch("https://factory.example.com/api/wave", {
    method: "POST",
    headers: {
      // Exactly what the container holds: TICKS_FACTORY_TOKEN is the run's
      // gateway token, never the operator's factory token.
      authorization: `Bearer ${process.env.TICKS_FACTORY_TOKEN}`,
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

/** Waits until a sandbox has been booted and started the orchestrator. */
async function firstProcess(): Promise<FakeProcess> {
  return waitFor(
    "the orchestrator to start",
    async () => {
      const first = sandboxes.booted.find((sandbox) => !fromAnEarlierTest(sandbox));
      return first !== undefined && first.processes.length > 0 ? first.current : null;
    },
    undefined,
    describeSandboxes,
  );
}

// -------------------------------------------------------------------- tests ---

describe("boot and finalize", () => {
  it("boots one sandbox on the skill loop and finalizes with a runs row", async () => {
    const { runID, project, epic } = await ignite();

    const process = await firstProcess();
    expect(process.command).toBe(ORCHESTRATOR_COMMAND);
    expect(process.env).toMatchObject({
      TICKS_REPO_URL: `https://github.com/${project}.git`,
      TICKS_BASE_SHA: BASE_SHA,
      TICKS_EPIC: epic,
      TICKS_RUN_ID: runID,
      TICKS_PHASE: "run",
      // The gateway a sandbox is pointed at is this factory's proxy, which
      // exchanges the run's token for the operator's provider key.
      AI_GATEWAY_BASE_URL: `${FACTORY}${GATEWAY_PATH_PREFIX}`,
    });
    // …and the only model credential inside the container is run-scoped.
    expect(process.env.AI_GATEWAY_TOKEN).toMatch(/^tkr_[0-9a-f]{64}$/);
    expect(JSON.stringify(process.env)).not.toContain("sk-operator-key");
    // The row says the run is live before it is over — `starting` is the
    // submit route's state, and the Workflow owns everything after it.
    await waitFor("the run to be running", async () => (await runState(runID)) === "running");

    process.say("orchestrator: wave 1\n");
    orchestratorPushedWork(epic);
    process.exit(0);

    const run = await settled(runID);
    expect(run.state).toBe("completed");
    expect(run.ended_at).not.toBeNull();
    // ONE sandbox: Phase 1 is a single-sandbox run.
    expect(sandboxes.booted).toHaveLength(1);
    expect(sandboxes.booted[0]!.destroyed).toBe(true);

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record).toMatchObject({ run_id: runID, project, epic, state: "completed" });
  });

  // The image is a parameter of the boot, not a constant of the call site: the
  // seam tick 3q2 left, now carrying a value. The container is also told which
  // image it got, which is what lets it report a repository whose tracked
  // `[sandbox].image` asks for a different one instead of ignoring it.
  it("boots the sandbox with an image reference and tells the container which one", async () => {
    const { runID } = await ignite();
    const process = await firstProcess();

    expect(sandboxes.requestedImages[0]).toBe(DEFAULT_SANDBOX_IMAGE);
    expect(process.env.TICKS_SANDBOX_IMAGE).toBe(DEFAULT_SANDBOX_IMAGE);
    // And nothing in that environment can carry a setup command: what a
    // sandbox runs to warm itself comes from the repository's tracked config
    // at the submitted SHA, read inside the container.
    for (const name of Object.keys(process.env)) expect(name).not.toMatch(/SETUP/i);

    // Let the run finish: an ignited run left alive keeps supervising, and a
    // shared workerd runtime is what the next test file has to run in.
    process.exit(0);
    await settled(runID);
  });

  // The escape hatch, working (tick x3v). A repository that pins its own image
  // in its tracked config gets that image — read from the checkout at the
  // submitted SHA, never from the submission, because an image is arbitrary
  // code and this container holds the run's credentials.
  it("boots the image the repository declares at the submitted SHA", async () => {
    const declared = "registry.example.com/acme/ticks-orchestrator:0.32.0";
    // The deployment that serves it: on this substrate an image belongs to the
    // container application, so honouring a declaration means the operator
    // deployed a factory running it.
    set("SANDBOX_IMAGE", declared);
    repoConfig.declare(declared);

    const { runID, project } = await ignite();
    const process = await firstProcess();

    expect(sandboxes.requestedImages[0]).toBe(declared);
    expect(process.env.TICKS_SANDBOX_IMAGE).toBe(declared);
    // Read from the tracked file at the run's own base commit, not at a branch
    // head that may have moved since the submission.
    expect(repoConfig.asked[0]).toEqual({ project, ref: BASE_SHA });

    process.exit(0);
    await settled(runID);
  });

  // The third clause of the tick, and the one that costs money to get wrong: a
  // declared image this deployment cannot serve must END the run, before a
  // container exists and before a credential is minted, rather than booting
  // the base image and letting a wave fail somewhere far from the cause.
  it("fails the run with an actionable error when the declared image cannot be booted", async () => {
    repoConfig.declare("registry.example.com/acme/ticks-orchestrator:0.32.0");

    const { runID, project, room } = await ignite();
    const run = await settled(runID);

    expect(run.state).toBe("failed");
    // Nothing was booted and nothing was credentialled: the refusal is the
    // whole run.
    expect(sandboxes.booted).toHaveLength(0);
    expect(await listRunGatewayTokens(env.DB, runID)).toHaveLength(0);
    // And the project is not left wedged behind a lease it never used.
    expect(await room.leaseStatus()).toBeNull();

    // What an operator reads back: both references, where the declaration
    // lives, and both ways out.
    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.state).toBe("failed");
    expect(record.detail).toContain("registry.example.com/acme/ticks-orchestrator:0.32.0");
    expect(record.detail).toContain(DEFAULT_SANDBOX_IMAGE);
    expect(record.detail).toContain(".tick/runners.toml");
    expect(record.detail).toContain("remove [sandbox].image");
  });

  // A GitHub hiccup must not fail every run: the control plane's reader is a
  // second reader of a Go-owned format, so an answer it could not get leaves
  // the deployment's image standing — and the container, which reads the
  // tracked config with the authoritative reader, refuses the boot if that was
  // the wrong call.
  it("boots this deployment's image when the tracked config cannot be read", async () => {
    repoConfig.unreadable = "GitHub answered HTTP 503";

    const { runID } = await ignite();
    const process = await firstProcess();

    expect(sandboxes.requestedImages[0]).toBe(DEFAULT_SANDBOX_IMAGE);
    expect(process.env.TICKS_SANDBOX_IMAGE).toBe(DEFAULT_SANDBOX_IMAGE);

    process.exit(0);
    await settled(runID);
  });

  it("releases the dispatch lease so the project can run again", async () => {
    const { runID, room } = await ignite();
    expect(await room.leaseStatus()).not.toBeNull();

    (await firstProcess()).exit(0);
    await settled(runID);

    expect(await room.leaseStatus()).toBeNull();
  });

  it("renews the lease as soon as the container is up, not a poll later (tick 4ef)", async () => {
    // The lease acquired at submit has to survive until the FIRST renewal, and
    // until tick 4ef that renewal was the first observation — behind boot plus
    // a poll delay. In production a boot (image pull, clone, toolchain, two
    // probes, pre-flight) took about a minute against a 60s lease, so the run
    // read its own expiry as a lost lease, treated it as a HARD trip, and
    // revoked its own gateway token: measured on run_d941c5ee as a
    // 403 run_token_revoked on the harness's first real call.
    //
    // The default test cadence (25ms) cannot express that, because the first
    // observation lands long before any lease expires. So invert the two: a
    // lease shorter than one poll delay makes "renewed only on observation"
    // fail exactly the way the real boot did.
    set("RUN_POLL_INTERVAL_MS", "3000");
    const { runID, room } = await ignite({ leaseTtlMs: 1_000 });
    const process = await firstProcess();

    // Past the point where a lease renewed only on observation is already dead.
    await new Promise((resolve) => setTimeout(resolve, 1_500));

    const lease = await room.leaseStatus();
    expect(lease).not.toBeNull();
    expect(lease!.run_id).toBe(runID);

    // The production symptom, asserted directly: nothing revoked this run's
    // credential, so the harness can still spend.
    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens.filter((token) => token.revoked_at !== null)).toEqual([]);

    const status = await runStatus(env, runID);
    expect(status?.run.state).toBe("running");

    process.exit(0);
    await settled(runID);
  });

  it("takes a lease at submit that outlives a cold boot (tick 4ef)", async () => {
    const project = `${PROJECT}-${++counter}`;
    await enrolProject(env.DB, {
      project,
      enrolled_by: "operator",
      enrolled_at: new Date().toISOString(),
    });
    const submitted = await submitRun(env, {
      project,
      epic: "ko8",
      base_sha: BASE_SHA,
      requested_by: "operator",
      trace_id: "tr_0123456789abcdef0123456789abcdef",
      queue: false,
    });
    if (submitted.outcome !== "started") {
      throw new Error(`the submission was refused: ${JSON.stringify(submitted)}`);
    }

    // The window the acquire ttl has to cover is a whole boot, so assert it in
    // those terms rather than against the constant: a minute is what the old
    // default gave, and a real boot takes about that.
    const lease = await roomFor(env, project).leaseStatus();
    expect(lease).not.toBeNull();
    expect(Date.parse(lease!.expires_at) - Date.now()).toBeGreaterThan(120_000);

    (await firstProcess()).exit(0);
    await settled(submitted.started.run.run_id);
  });

  it("renews the lease while the run is alive", async () => {
    const { runID, room } = await ignite();
    const process = await firstProcess();
    const first = (await room.leaseStatus())!.expires_at;

    await waitFor("a lease renewal", async () => {
      const lease = await room.leaseStatus();
      return lease !== null && lease.expires_at > first;
    });

    process.exit(0);
    await settled(runID);
  });
});

describe("exit 0 is not completion (tick ehy)", () => {
  // The run this comes from: everything booted, the model probe was green, the
  // HARNESS probe was green, the skill loop started — and the orchestrator then
  // produced 271 bytes stating the substrate it had resolved and the note it
  // intended to record, and exited. The Workflow marked it COMPLETED and
  // charged $0.22. No wave, no branch, no note, and the epic still had every
  // one of its open ticks.
  it("records a harness that exited 0 having pushed nothing as stopped, not completed", async () => {
    const { runID, project } = await ignite();
    const process = await firstProcess();

    process.say(
      "The dispatch substrate for this run is subagents.\n" +
        "I will record: run-state: substrate=subagents\n",
    );
    process.exit(0);

    const run = await settled(runID);
    expect(run.state).toBe("stopped");

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.state).toBe("stopped");
    expect(record.progress).toBe("none");
    // The record has to say WHY it is not a completion, or the next operator
    // reads "stopped" and assumes a budget trip.
    expect(record.detail ?? "").toMatch(/exit status is not completion/i);
    expect(record.progress_detail ?? "").toMatch(/no branch on origin changed/i);
  });

  it("still reports completed for a run that actually advanced the epic", async () => {
    const { runID, project, epic } = await ignite();
    const process = await firstProcess();

    // The durable layer, not the terminal: a branch on origin that was not
    // there when the run started.
    repo.push(`epic/${epic}`, PUSHED_SHA);
    process.exit(0);

    const run = await settled(runID);
    expect(run.state).toBe("completed");

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.progress).toBe("advanced");
    expect(record.progress_detail ?? "").toContain(`epic/${epic}`);
  });

  it("counts an epic branch that was merged and cleaned up as progress", async () => {
    // A run whose last act is merging its epic branch and deleting it leaves a
    // remote with FEWER branches than it found. That is the successful ending.
    const { runID, epic } = await ignite();
    repo.push(`epic/${epic}`, PUSHED_SHA);
    const before = await runState(runID);
    expect(before).not.toBeNull();

    const process = await firstProcess();
    repo.deleteBranch(`epic/${epic}`);
    process.exit(0);

    expect((await settled(runID)).state).toBe("completed");
  });

  // A GitHub outage must not invent a failure any more than an exit code may
  // invent a success. The run stays completed and the record says the evidence
  // could not be read — the same distinction `cost_source` draws.
  it("does not downgrade a run whose evidence could not be read, and says so", async () => {
    const { runID, project } = await ignite();
    const process = await firstProcess();

    repo.unreadable = "GitHub answered HTTP 503 for the branch listing";
    process.exit(0);

    const run = await settled(runID);
    expect(run.state).toBe("completed");

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.progress).toBe("unknown");
    expect(record.detail ?? "").toMatch(/could not be verified/i);
    expect(record.progress_detail ?? "").toContain("503");
  });

  // `tk cloud status <run>` reads this row. Two `stopped` runs — one that
  // pushed a wave before its budget tripped and one that did nothing at all —
  // are the same word without it.
  it("stamps the verdict in D1 so tk cloud status can show the distinction", async () => {
    const noop = await ignite();
    (await firstProcess()).exit(0);
    await settled(noop.runID);
    expect(await getRunProgress(env.DB, noop.runID)).toMatchObject({ progress: "none" });

    sandboxes = new FakeSandboxes();
    set("SANDBOXES", sandboxes);
    repo = new FakeRepo();
    set("REPO_REFS", repo);

    const real = await ignite();
    const process = await firstProcess();
    repo.push(`epic/${real.epic}`, PUSHED_SHA);
    process.exit(0);
    await settled(real.runID);
    expect(await getRunProgress(env.DB, real.runID)).toMatchObject({ progress: "advanced" });
  });

  // A stop is already a truthful state; the verdict rides along so an operator
  // can tell a stop that preserved work from a stop that had none to preserve.
  it("records the verdict for a clean stop without changing its state", async () => {
    const { runID, epic } = await ignite();
    await firstProcess();
    await stopRun(env, runID, "operator");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    repo.push(`epic/${epic}`, PUSHED_SHA);
    closeout.exit(0);

    expect((await settled(runID)).state).toBe("stopped");
    expect(await getRunProgress(env.DB, runID)).toMatchObject({ progress: "advanced" });
  });
});

describe("harness output streams to R2 during the run", () => {
  it("is readable while the run is still going", async () => {
    const { runID, project } = await ignite();
    const process = await firstProcess();

    process.say("orchestrator: graph resolved\n");
    const early = await waitFor("the first harness output in R2", async () => {
      const text = await readHarnessOutput(env.ARTIFACTS, project, runID);
      return text === "" ? null : text;
    });
    expect(early).toContain("graph resolved");
    // Still going: this is not an export-at-exit log.
    expect(process.state).toBe("running");
    expect(await runState(runID)).toBe("running");

    process.say("orchestrator: wave 1 merged\n");
    await waitFor("the second flush", async () =>
      (await readHarnessOutput(env.ARTIFACTS, project, runID)).includes("wave 1 merged"),
    );

    process.exit(0);
    await settled(runID);

    const full = await readHarnessOutput(env.ARTIFACTS, project, runID);
    expect(full).toBe("orchestrator: graph resolved\norchestrator: wave 1 merged\n");
  });

  it("leaves the log behind when the sandbox dies mid-run", async () => {
    const { runID, project } = await ignite();
    const process = await firstProcess();
    process.say("orchestrator: about to be killed\n");
    await waitFor("the output to reach R2", async () =>
      (await readHarnessOutput(env.ARTIFACTS, project, runID)).includes("about to be killed"),
    );

    sandboxes.booted[0]!.vanished = true;

    // The dead sandbox's output survives it — that is the point of streaming.
    const second = await waitFor("a replacement sandbox", async () =>
      sandboxes.booted.length > 1 ? sandboxes.booted[1]! : null,
    );
    expect(await readHarnessOutput(env.ARTIFACTS, project, runID)).toContain("about to be killed");

    second.current.exit(0);
    await settled(runID);
  });
});

describe("a dead orchestrator is replaced, not the end of the run", () => {
  it("boots a fresh sandbox whose first instruction is the reconcile protocol", async () => {
    const { runID } = await ignite();
    const first = await firstProcess();
    expect(first.env.TICKS_PHASE).toBe("run");

    sandboxes.booted[0]!.vanished = true;

    const replacement = await waitFor("a replacement sandbox", async () =>
      sandboxes.booted.length > 1 ? sandboxes.booted[1]! : null,
    );
    // A FRESH container, not the broken one reused.
    expect(replacement.name).not.toBe(sandboxes.booted[0]!.name);
    const process = await waitFor("the replacement orchestrator", async () =>
      replacement.processes.length > 0 ? replacement.current : null,
    );
    expect(process.env.TICKS_PHASE).toBe("reconcile");
    expect(process.env.TICKS_RUN_ID).toBe(runID);
    expect(process.env.TICKS_BASE_SHA).toBe(BASE_SHA);

    orchestratorPushedWork();
    process.exit(0);
    const run = await settled(runID);
    expect(run.state).toBe("completed");
  });

  it("reboots after a crashed harness too, and records why", async () => {
    const { runID, project } = await ignite();
    (await firstProcess()).exit(1);

    const replacement = await waitFor("a replacement sandbox", async () =>
      sandboxes.booted.length > 1 && sandboxes.booted[1]!.processes.length > 0
        ? sandboxes.booted[1]!.current
        : null,
    );
    expect(replacement.env.TICKS_PHASE).toBe("reconcile");

    // One reconcile.json per reboot: what the dead orchestrator looked like
    // when it was written off (D20's artifact tree).
    const written = await waitFor("the reconcile record", async () =>
      env.ARTIFACTS.get(reconcileKey(project, runID, 1)),
    );
    const record = JSON.parse(await written.text()) as {
      previous: { state: string; exit_code: number | null };
      detail: string;
    };
    expect(record.previous).toEqual({ state: "failed", exit_code: 1 });
    expect(record.detail).toContain("exited 1");

    orchestratorPushedWork();
    replacement.exit(0);
    expect((await settled(runID)).state).toBe("completed");
  });

  it("does not reboot on a configuration verdict from the entrypoint", async () => {
    const { runID } = await ignite();
    // Exit 5: an Environment pre-flight check failed. A fresh container reaches
    // the identical answer, so rebooting only spends money.
    (await firstProcess()).exit(5);

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    expect(sandboxes.booted).toHaveLength(1);
  });

  it("does not reboot when the reconciler answers not-found: the epic is absent from the submitted tree", async () => {
    const { runID, project } = await ignite();
    // Exit 4: the orchestrator entrypoint execs `ticfac run-epic`, which exits
    // with tk's not-found code when the epic does not exist on the submitted
    // tree — a verdict the cut of the next container's checkout reproduces
    // byte for byte (ticfac tick rf3). The first per-tick Cloudflare smoke run
    // re-booted into this identical failure until a person stopped it by hand.
    (await firstProcess()).exit(4);

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    expect(sandboxes.booted).toHaveLength(1);

    // And the run's record says WHY it stopped, as a class an operator can act
    // on — not just "exited 4": the durable reason is the refusal the
    // reconciler recorded, and this is the supervisor's own account of it.
    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.detail).toContain("the epic does not exist on the submitted tree");
    expect(record.detail).toContain("no sandbox was rebooted");
  });

  it("gives up after a bounded number of boots rather than looping forever", async () => {
    const { runID } = await ignite();
    // Every orchestrator this run is allowed crashes. A container that cannot
    // stay alive is telling you something a fourth boot will not fix.
    for (let boot = 0; boot < MAX_SANDBOX_BOOTS; boot++) {
      const sandbox = await waitFor(`sandbox ${boot + 1}`, async () =>
        sandboxes.booted.length > boot && sandboxes.booted[boot]!.processes.length > 0
          ? sandboxes.booted[boot]!
          : null,
      );
      sandbox.current.exit(1);
    }

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    expect(sandboxes.booted).toHaveLength(MAX_SANDBOX_BOOTS);
  });
});

describe("a run that outlives what one instance can watch", () => {
  it("stops it cleanly instead of booting a second orchestrator beside it", async () => {
    // Running out of looks is not a dead orchestrator. Rebooting here would put
    // two live orchestrators on the same project's `.tick/` (D4).
    set("RUN_MAX_OBSERVATIONS", "2");
    const { runID } = await ignite();
    const process = await firstProcess();
    process.say("orchestrator: still working\n");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    expect(process.killed).toBe(true);
    // Exactly two sandboxes: the one that was watched out, and the closeout.
    expect(sandboxes.booted).toHaveLength(2);
    expect(sandboxes.phase("reconcile")).toBeUndefined();

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
  });
});

// A stop is honoured by the run's own supervisor, and a supervisor that has
// already ended never reads it. On 2026-09-23 four runs sat in `stopping`
// indefinitely — counted as active — because their Workflow had errored on a
// boot that could not get a container. The row is frozen here the way theirs
// were: the supervisor is finished, the record still says the run is live.
// ------------------------------------- the supervisor watches, it does not orchestrate (cr4) ---

/**
 * The Run Workflow's job is boot, budget, watch, retry, finalize — and the
 * watching half is `step.waitForEvent` on the orchestrator's completion
 * (tick cr4): the event is an OPTIMISATION that avoids polling, the branch
 * and the process remain the truth, and a wait that expires is the CADENCE,
 * never a verdict. These hold the platform facts the tick is built on:
 *
 *  - a container booted `keepAlive` never idles away mid-run, and must be
 *    explicitly destroyed — every ending of a boot destroys its container;
 *  - waiting instances cost no concurrency, and a `waitForEvent` that expires
 *    THROWS, which is the signal to look again (and re-boot into resume when
 *    the container is gone) — never to conclude the run failed;
 *  - the completion event (sent by the route tick 7eq wires) wakes the wait
 *    immediately, so a finished orchestrator is not held up by a poll delay.
 */
describe("the supervisor watches, it does not orchestrate (cr4)", () => {
  it("boots the orchestrator under keepAlive so no idle shutdown can kill a live run, and destroys it when the run ends", async () => {
    const { runID, epic } = await ignite();
    const process = await firstProcess();

    // keepAlive is the boot's own parameter, exactly as the image is: a
    // container that heartbeats every 30s does not die of idleness while the
    // orchestrator works for hours — and the price is that ONLY destroy()
    // ever ends it, which the finalize sweep still pays as the backstop.
    expect(sandboxes.requestedKeepAlive[0]).toBe(true);
    // The worker containers a wave boots are NOT the orchestrator: they keep
    // the sleep-after ceiling a leaked one needs. (Their dispatch path is
    // tick l6t's to delete, not this one's to re-policy.)

    orchestratorPushedWork(epic);
    process.exit(0);
    const run = await settled(runID);
    expect(run.state).toBe("completed");
    expect(sandboxes.booted[0]!.destroyed).toBe(true);
  });

  it("destroys the container when its pass ends, before the next one boots — a keepAlive container bills until someone destroys it", async () => {
    const { runID } = await ignite();
    const work = await firstProcess();
    work.say("orchestrator: wave 1 in flight\n");
    const stopped = await stopRun(env, runID, "operator");
    expect(stopped.outcome).toBe("stopping");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    // The WORK container was destroyed — not killed, destroyed — before the
    // closeout boot started: with keepAlive, a container the supervisor has
    // stopped watching would otherwise bill until an operator noticed.
    expect(sandboxes.booted[0]!.destroyed).toBe(true);
    expect(work.killed).toBe(true);

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
    for (const sandbox of sandboxes.booted) expect(sandbox.destroyed).toBe(true);
  });

  it("learns the orchestrator finished from the completion event instead of waiting out the poll", async () => {
    // A poll interval the run could never wait out inside the test budget:
    // if the wait completes in time, the EVENT is what woke it — the branch
    // and process were already terminal, and the wait did not expire first.
    set("RUN_POLL_INTERVAL_MS", "60000");
    const { runID, epic } = await ignite();
    const process = await firstProcess();
    orchestratorPushedWork(epic);
    process.exit(0);

    const binding = runWorkflowBinding(env);
    expect(binding).not.toBeNull();
    const instance = await binding!.get(runID);
    // The event is buffered by the platform when it lands before the wait
    // starts, so this races nothing: sent now, it is delivered to the first
    // `waitForEvent` the Workflow reaches. The type and payload are the done
    // door's own (tick 7eq) — the one completion event this wait listens for.
    await instance.sendEvent!({ type: DONE_EVENT_TYPE, payload: { branch: `epic/${epic}` } });

    const run = await settled(runID);
    expect(run.state).toBe("completed");
  });

  it("treats a wait that times out as the cadence, never a verdict: a live orchestrator is watched on", async () => {
    // A cadence at the platform's own waitForEvent floor, so this exercises
    // the REAL wait-and-throw path, not the sub-second sleep the tight test
    // cadence falls back to.
    set("RUN_POLL_INTERVAL_MS", "1000");
    const { runID, epic } = await ignite();
    const process = await firstProcess();

    // Two looks happened while the orchestrator stayed alive: two waits
    // expired and each one concluded NOTHING — the run is still running, the
    // container is still alive, and no failure was inferred from a timeout.
    await waitFor("two looks at the live orchestrator", async () =>
      sandboxes.booted[0]!.looked >= 2 ? true : null,
    );
    expect(await runState(runID)).toBe("running");
    expect(process.state).toBe("running");

    // And the cadence is what notices the completion, exactly as before.
    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
  });

  it("retries a transient boot failure from a deliberate policy, not the platform default of five", async () => {
    const { runID, epic } = await ignite({
      beforeStart: (id) => {
        // The FIRST address of the orchestrator's sandbox fails — the shape
        // of a cold-start blip — and the boot step's own retry policy decides
        // what happens next: one re-run, from BOOT_RETRIES, not five from the
        // platform default a config-less step inherits.
        sandboxes.failGetAt = { name: sandboxName(id, 1), nth: 1 };
      },
    });

    const process = await firstProcess();
    // The boot step ran at least twice: the first address failed and its
    // deliberate policy re-ran it once rather than ending the run.
    expect(sandboxes.addressed(sandboxName(runID, 1))).toBeGreaterThanOrEqual(2);
    expect(sandboxes.booted).toHaveLength(1);

    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
  });
});

describe("a stop whose supervisor has already ended", () => {
  it("finishes the stop itself instead of leaving the run in stopping", async () => {
    const { runID, epic } = await ignite();
    const process = await firstProcess();
    await waitFor("the run to be running", async () => (await runState(runID)) === "running");
    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");

    // The frozen record: supervisor complete, row still active.
    await env.DB.prepare("UPDATE runs SET state = 'stopping', ended_at = NULL WHERE run_id = ?")
      .bind(runID)
      .run();

    const stop = await stopRun(env, runID, "operator");
    expect(stop.outcome).toBe("stopping");
    if (stop.outcome === "stopping") {
      expect(stop.supervisor_ended).toBe("complete");
      expect(stop.run.state).toBe("stopped");
      expect(stop.run.ended_at).not.toBeNull();
    }
    expect(await runState(runID)).toBe("stopped");
  });

  it("leaves a run whose supervisor is alive to its supervisor", async () => {
    const { runID } = await ignite();
    await firstProcess();
    await waitFor("the run to be running", async () => (await runState(runID)) === "running");

    const stop = await stopRun(env, runID, "operator");
    expect(stop.outcome).toBe("stopping");
    if (stop.outcome === "stopping") {
      expect(stop.supervisor_ended).toBeUndefined();
      expect(stop.run.state).toBe("stopping");
    }

    // And the live supervisor does finish it, through its own closeout —
    // driven to the end so no run of this test outlives it.
    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
  });
});

describe("a clean stop runs review and closeout", () => {
  it("stops on the operator's request and still closes the run out", async () => {
    const { runID, project, epic } = await ignite();
    const process = await firstProcess();
    process.say("orchestrator: wave 1 in flight\n");

    const stopped = await stopRun(env, runID, "operator");
    expect(stopped.outcome).toBe("stopping");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    // The work orchestrator was given its grace window and then killed — the
    // in-flight tick's evidence is on the run branch either way.
    expect(process.killed).toBe(true);
    expect(closeout.env.TICKS_EPIC).toBe(epic);
    expect(closeout.env.TICKS_STOP_REASON ?? "").toContain("operator");

    closeout.exit(0);
    const run = await settled(runID);
    expect(run.state).toBe("stopped");

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.state).toBe("stopped");
    expect(record.detail ?? "").toContain("operator");
  });

  it("treats a cost budget exactly like the operator stop path", async () => {
    const { runID, epic } = await ignite();
    const process = await firstProcess();

    // A last known ground-truth value remains enforceable when a later
    // telemetry read fails. The default budget is $25 because this test does
    // not opt into an explicit budget without a readable gateway.
    await env.DB.prepare("UPDATE runs SET cost_usd = ? WHERE run_id = ?").bind(25.5, runID).run();

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    expect(process.killed).toBe(true);
    expect(closeout.env.TICKS_STOP_REASON ?? "").toMatch(/budget|cost/i);

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");

    // "Why did this stop" is answerable from D1, not from a log line.
    const log = await listDispatchLogs(env.DB, runID, epic);
    expect(log.some((entry) => entry.reason === "budget_exhausted")).toBe(true);
  });

  it("treats a wall-clock budget the same way", async () => {
    set("RUN_MAX_WALL_CLOCK_MS", "1");
    const { runID } = await ignite();

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    expect(closeout.env.TICKS_STOP_REASON ?? "").toMatch(/wall|time/i);

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
  });

  it("still finalizes when the closeout orchestrator itself fails", async () => {
    const { runID } = await ignite();
    await firstProcess();
    await stopRun(env, runID, "operator");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    closeout.exit(1);

    // A failed closeout is still a stopped run with a released lease: an
    // abandoned run is the one outcome a stop must never produce.
    const run = await settled(runID);
    expect(run.state).toBe("stopped");
    expect(await roomFor(env, run.project).leaseStatus()).toBeNull();
  });
});

// ---------------------------------------------------- the gateway (D17) ---

/** Stands in for the operator's AI Gateway, recording what reached it. */
function fakeGateway() {
  const calls: string[] = [];
  const fetcher = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return Response.json({ ok: true });
  }) as unknown as typeof fetch;
  return { calls, fetcher };
}

/** One model request made with a sandbox's own credential. */
async function modelCall(token: string, fetcher: typeof fetch): Promise<Response> {
  return proxyModelRequest(
    env,
    new Request(`${FACTORY}${GATEWAY_PATH_PREFIX}/anthropic/v1/messages`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: "{}",
    }),
    ["anthropic", "v1", "messages"],
    { fetcher },
  );
}

describe("the run's gateway credential is the kill switch", () => {
  it("stops a running orchestrator's model traffic the moment the run trips", async () => {
    const { runID } = await ignite();
    const process = await firstProcess();
    const token = process.env.AI_GATEWAY_TOKEN!;
    const gateway = fakeGateway();

    // Mid-run: the sandbox's credential spends.
    expect((await modelCall(token, gateway.fetcher)).status).toBe(200);
    expect(gateway.calls).toHaveLength(1);

    await stopRun(env, runID, "operator");
    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );

    // The orchestrator that was stopped cannot spend another cent, whether or
    // not it noticed it was stopped — enforcement at the credential layer.
    const refused = await modelCall(token, gateway.fetcher);
    expect(refused.status).toBe(403);
    await expect(refused.json()).resolves.toMatchObject({ error: "run_token_revoked" });
    expect(gateway.calls).toHaveLength(1);

    // Rotation, not a shutdown: closeout still has to reach review and
    // closeout (D15), so it boots with a credential of its own.
    const closeoutToken = closeout.env.AI_GATEWAY_TOKEN!;
    expect(closeoutToken).not.toBe(token);
    expect((await modelCall(closeoutToken, gateway.fetcher)).status).toBe(200);

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
  });

  it("kills the credential before the grace window when a budget trips, not after it", async () => {
    // Tick gyl's second defect. Finishing in-flight work is right for an
    // ordinary stop and wrong for a run that is already over its allowance:
    // the grace window is time the runaway spends. So a budget trip revokes
    // FIRST, and the drain happens on a container that can no longer spend.
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    set("RUN_MAX_COST_USD", "1");
    // Wide enough that "before" and "after" the grace window are not the same
    // instant — the whole point of the assertion below.
    set("RUN_STOP_GRACE_MS", "3000");
    const logs = stubLogsAPI(9.99, undefined, 50);

    try {
      const { runID } = await ignite();
      const process = await firstProcess();
      const token = process.env.AI_GATEWAY_TOKEN!;
      const gateway = fakeGateway();
      expect((await modelCall(token, gateway.fetcher)).status).toBe(200);

      // The budget trips. The credential dies while the orchestrator is still
      // running, inside the window it would otherwise have kept spending in.
      await waitFor("the run's credential to be revoked", async () => {
        const tokens = await listRunGatewayTokens(env.DB, runID);
        return tokens.length > 0 && tokens.every((entry) => entry.revoked_at !== null);
      });
      expect(process.killed).toBe(false);
      const refused = await modelCall(token, gateway.fetcher);
      expect(refused.status).toBe(403);
      await expect(refused.json()).resolves.toMatchObject({ error: "run_token_revoked" });
      expect(gateway.calls).toHaveLength(1);

      // The unwind still happens: a stop must reach review and closeout (D15),
      // and a budget trip leaves no stop record, so closeout is credentialled.
      const closeout = await waitFor("the closeout orchestrator", async () =>
        sandboxes.phase("closeout"),
      );
      closeout.exit(0);
      expect((await settled(runID)).state).toBe("stopped");
    } finally {
      logs.restore();
    }
  });

  it("does not re-credential a run under a hard stop, in any pass", async () => {
    // The defect that made the kill switch decorative on a live run: revoking
    // a token stopped nothing, because the supervisor minted a replacement at
    // the next boot — and the closeout pass, which enforces no budgets, did
    // not read stop records at all. Only deleting the container application
    // halted the spend. A hard stop is a durable refusal to mint.
    const { runID } = await ignite();
    const work = await firstProcess();
    const gateway = fakeGateway();

    // A clean stop first, exactly as the incident went: the run unwinds into
    // closeout and is credentialled again, and the spend continues.
    await stopRun(env, runID, "operator");
    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    const closeoutToken = closeout.env.AI_GATEWAY_TOKEN!;
    expect(closeoutToken).not.toBe(work.env.AI_GATEWAY_TOKEN);
    expect((await modelCall(closeoutToken, gateway.fetcher)).status).toBe(200);

    // Now the operator pulls the switch.
    const stop = await stopRun(env, runID, "operator", "hard");
    expect(stop.outcome).toBe("stopping");
    if (stop.outcome === "stopping") {
      expect(stop.mode).toBe("hard");
      expect(stop.tokens_revoked).toBe(1);
    }
    expect((await modelCall(closeoutToken, gateway.fetcher)).status).toBe(403);

    // And the container dies of it, the way a harness handed 403s does. The
    // supervisor must NOT answer that with a fresh container and a fresh
    // credential.
    sandboxes.booted.at(-1)!.vanished = true;

    const run = await settled(runID);
    expect(run.state).toBe("stopped");
    expect(sandboxes.booted).toHaveLength(2);
    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens).toHaveLength(2);
    expect(tokens.every((entry) => entry.revoked_at !== null)).toBe(true);
    // Two model calls in the whole run, both before the switch was pulled.
    expect(gateway.calls).toHaveLength(1);
  });

  it("rotates the credential on every boot, so a dead container cannot spend", async () => {
    const { runID } = await ignite();
    const first = await firstProcess();
    const firstToken = first.env.AI_GATEWAY_TOKEN!;
    const gateway = fakeGateway();

    sandboxes.booted[0]!.vanished = true;
    const replacement = await waitFor("the replacement orchestrator", async () => {
      const sandbox = sandboxes.booted[1];
      return sandbox !== undefined && sandbox.processes.length > 0 ? sandbox.current : null;
    });

    // The container that was written off may still be alive somewhere. Its
    // credential is not.
    expect((await modelCall(firstToken, gateway.fetcher)).status).toBe(403);
    expect((await modelCall(replacement.env.AI_GATEWAY_TOKEN!, gateway.fetcher)).status).toBe(200);

    replacement.exit(0);
    await settled(runID);
  });

  it("leaves no live credential behind when the run is over", async () => {
    const { runID } = await ignite();
    const process = await firstProcess();
    const token = process.env.AI_GATEWAY_TOKEN!;
    process.exit(0);
    await settled(runID);

    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens.length).toBeGreaterThan(0);
    expect(tokens.every((entry) => entry.revoked_at !== null)).toBe(true);

    const gateway = fakeGateway();
    expect((await modelCall(token, gateway.fetcher)).status).toBe(403);
    expect(gateway.calls).toHaveLength(0);
  });

  it("refuses before boot when an explicit cost budget has no gateway telemetry", async () => {
    set("RUN_MAX_COST_USD", "1");
    const { runID, project } = await ignite();

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    expect(sandboxes.booted).toHaveLength(0);

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.detail ?? "").toMatch(/AI Gateway.*cost telemetry/i);
    expect(record.detail ?? "").toContain("CLOUDFLARE_API_TOKEN");
    expect(record.detail ?? "").toContain("tk factory setup");
  });

  it("boots the sandbox when a configured cost budget can read its gateway telemetry", async () => {
    // The other half of the refusal above: the guard only grounds a run whose
    // telemetry is unreadable. With a logs API that answers, a budgeted run
    // proceeds — and the number it acts on is the gateway's, not a zero.
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    set("RUN_MAX_COST_USD", "5");
    const logs = stubLogsAPI(0.02);

    try {
      const { runID, project } = await ignite();
      const process = await firstProcess();
      expect(sandboxes.booted).toHaveLength(1);
      orchestratorPushedWork();
      process.exit(0);
      const run = await settled(runID);

      expect(run.state).toBe("completed");
      // Every query the run made asked for metadata the way the API models it.
      expect(logs.filters.length).toBeGreaterThan(0);
      for (const filters of logs.filters) {
        expect(filters.map((filter) => filter.key)).toEqual(["metadata.key", "metadata.value"]);
        expect(filters[0]!.value).toEqual(["run_id"]);
        expect(filters[1]!.value).toEqual([runID]);
      }
      const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
      expect(record.cost_source).toBe("gateway");
      expect(run.cost_usd).toBeCloseTo(0.02, 10);
    } finally {
      logs.restore();
    }
  });

  it("trips a cost budget on a run whose spend spans many log pages", async () => {
    // The run that found this made 887 model calls for $49.31 against a $25
    // budget, and kept going: only the first page of 50 was ever counted, so
    // the recorded cost was $2.98 and no budget could trip. The stub answers
    // as the API does — 50 rows a page, no `total_pages` — and the budget has
    // to act on the whole of it.
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    set("RUN_MAX_COST_USD", "25");
    const logs = stubLogsAPI(0.0556, undefined, 887);

    try {
      const { runID, epic } = await ignite();
      const process = await firstProcess();

      const closeout = await waitFor("the closeout orchestrator", async () =>
        sandboxes.phase("closeout"),
      );
      expect(process.killed).toBe(true);
      // The COST budget specifically: a wall-clock trip would satisfy a looser
      // pattern while the undercount sailed on underneath it.
      expect(closeout.env.TICKS_STOP_REASON ?? "").toMatch(/cost budget/i);

      closeout.exit(0);
      const run = await settled(runID);
      expect(run.state).toBe("stopped");
      // The whole invoice, not one page of it.
      expect(run.cost_usd).toBeCloseTo(887 * 0.0556, 6);
      expect(run.cost_usd).toBeGreaterThan(25);

      const log = await listDispatchLogs(env.DB, runID, epic);
      expect(log.some((entry) => entry.reason === "budget_exhausted")).toBe(true);
    } finally {
      logs.restore();
    }
  });

  it("tells the operator to report a rejected query, not to configure what is configured", async () => {
    // The live run this came from was told "run `tk factory setup
    // --cloudflare-api-token`" for a 400 caused by our own query shape — the
    // credential was already there, and no amount of setup would have helped.
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    set("RUN_MAX_COST_USD", "1");
    const logs = stubLogsAPI(0, {
      status: 400,
      message: "Number must be less than or equal to 50",
    });

    try {
      const { runID, project } = await ignite();
      const run = await settled(runID);
      expect(run.state).toBe("failed");
      expect(sandboxes.booted).toHaveLength(0);

      const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
      const detail = record.detail ?? "";
      expect(detail).toContain("400");
      expect(detail).toContain("Number must be less than or equal to 50");
      expect(detail).toMatch(/report/i);
      // The remedy for a missing credential is the wrong advice here.
      expect(detail).not.toContain("tk factory setup --cloudflare-api-token");
    } finally {
      logs.restore();
    }
  });

  it("tells the operator to retry when the logs API is down, not to report a bug", async () => {
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    set("RUN_MAX_COST_USD", "1");
    const logs = stubLogsAPI(0, { status: 503, message: "service unavailable" });

    try {
      const { runID, project } = await ignite();
      const run = await settled(runID);
      expect(run.state).toBe("failed");

      const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
      const detail = record.detail ?? "";
      expect(detail).toContain("503");
      expect(detail).toMatch(/retry/i);
    } finally {
      logs.restore();
    }
  });

  it("says in the record when gateway cost telemetry could not be read", async () => {
    // No explicit cost budget or CLOUDFLARE_API_TOKEN in this harness: the run
    // still runs, bounded by wall clock, and its record says the cost is
    // unknown rather than $0.
    set("RUN_MAX_COST_USD", undefined);
    const { runID, project } = await ignite();
    (await firstProcess()).exit(0);
    await settled(runID);

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.cost_source ?? "").toContain("CLOUDFLARE_API_TOKEN");
  });
});

describe("an unprovisioned deployment fails closed", () => {
  it("fails the run naming the missing sandbox binding", async () => {
    set("SANDBOXES", undefined);
    delete (env as unknown as Record<string, unknown>).SANDBOXES;
    const { runID, project } = await ignite();

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord | null;
    expect(record?.detail ?? "").toContain("SANDBOXES");
  });

  it("fails the run naming the missing gateway", async () => {
    set("AI_GATEWAY_BASE_URL", undefined);
    delete (env as unknown as Record<string, unknown>).AI_GATEWAY_BASE_URL;
    const { runID, project } = await ignite();

    const run = await settled(runID);
    expect(run.state).toBe("failed");
    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord | null;
    expect(record?.detail ?? "").toContain("tk factory setup");
    expect(sandboxes.booted).toHaveLength(0);
  });
});

describe("applyProgress", () => {
  const ran: RunOutcome = {
    state: "completed",
    detail: "the orchestrator ran 3 tick(s)",
    boots: 1,
  };

  it("keeps the outcome's own detail when it downgrades a run that did not move", () => {
    // Tick 074: the downgrade used to REPLACE the detail, which threw away the
    // run's only account of what it did. "Nothing moved" and "here is what
    // ran" are both needed.
    const downgraded = applyProgress(ran, {
      state: "none",
      detail: "no branch on origin changed",
    });
    expect(downgraded.state).toBe("stopped");
    expect(downgraded.detail).toContain("the orchestrator ran 3 tick(s)");
    expect(downgraded.detail).toContain("no branch on origin changed");
    expect(downgraded.detail).toMatch(/exit status is not completion/i);
  });

  it("leaves a stop alone", () => {
    const stopped: RunOutcome = { state: "stopped", detail: "the operator stopped it", boots: 1 };
    expect(applyProgress(stopped, { state: "none", detail: "nothing moved" })).toEqual(stopped);
  });
});

/**
 * The deleted wave path (tick l6t): the Run Workflow no longer fans ticks out
 * to per-tick worker containers itself — a submission cannot carry a wave, and
 * the run boots ONE orchestrator container whose `ticfac run-epic` dispatches
 * every tick through the cloudflare-sandbox executor and the per-tick sandbox
 * door. What stays pinned here is the shape of the one path that remains.
 */
describe("the one dispatch path: a single orchestrator container", () => {
  it("boots exactly one orchestrator and no per-tick sandboxes, whatever the epic holds", async () => {
    const { runID } = await ignite();
    const process = await firstProcess();
    expect(sandboxes.booted).toHaveLength(1);
    expect(sandboxes.booted[0]!.name).not.toContain("-tick-");
    process.exit(0);
    await settled(runID);
  });

  it("treats a stale tick_ids param as inert: a run replayed across the deletion boots one orchestrator", async () => {
    // The field is gone from the submission the route accepts (pinned in
    // run-routes.test.ts). What this pins is the OTHER edge: a Workflow
    // instance created before the deletion, resumed after it, carries a
    // serialised params blob with tick_ids in it — and the resumed run must
    // boot the one orchestrator, not fan out. The Workflow does not even read
    // the field any more; the test says so by passing one.
    const { runID, epic } = await ignite({ staleTickIDs: ["aaa", "bbb"] });
    const process = await firstProcess();
    expect(sandboxes.booted).toHaveLength(1);
    expect(sandboxes.booted[0]!.name).not.toContain("-tick-");
    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
  });
});

// ------------------------------------------------- stops nothing waits out ---

/**
 * Runs `hook` once, inside the R2 write that records why a boot died.
 *
 * The synchronization seam this suite did not have (tick k24). Asserting "a
 * stop that arrives between two boots is refused" needs the stop to land in a
 * window the test cannot otherwise address: after the supervisor has decided
 * boot N is dead and before it credentials boot N+1. Waiting a while and
 * hoping is a flaky test, which is why tick b6e declined to write one. The
 * reconcile record IS that window — it is the last thing the supervisor writes
 * before looping to the next attempt — so a hook awaited inside the write is
 * ordered by the code under test rather than by the clock.
 */
function onReconcileWrite(hook: () => Promise<void>): void {
  const bucket = env.ARTIFACTS as R2Bucket;
  let fired = false;
  set(
    "ARTIFACTS",
    new Proxy(bucket, {
      get(target, property, receiver) {
        if (property === "put") {
          return async (key: string, value: unknown, options?: unknown) => {
            const put = await (target.put as (...args: unknown[]) => Promise<unknown>)(
              key,
              value,
              options,
            );
            if (!fired && typeof key === "string" && key.includes("/reconcile/")) {
              fired = true;
              await hook();
            }
            return put;
          };
        }
        const value = Reflect.get(target, property, receiver);
        return typeof value === "function" ? value.bind(target) : value;
      },
    }),
  );
}

describe("a hard stop is refused at every boundary, not only the ones a run happens to reach", () => {
  // Neither of these had an assertion before tick k24: the kill-switch suite
  // proved the credential layer refuses a REVOKED token, and that a closeout
  // reboot is refused, but nothing proved the two boundaries the supervisor
  // itself owns — before the first boot, and between two of them.
  it("credentials no orchestrator at all when the stop lands before the first boot", async () => {
    // The stop lands while the control plane is still resolving the repo's
    // tracked config — provably before the first kill-check, with no waiting.
    const { runID, project } = await ignite({
      beforeStart: (id) => {
        repoConfig.onRead = async () => {
          await stopRun(env, id, "operator", "hard");
        };
      },
    });

    const run = await settled(runID);
    expect(run.state).toBe("stopped");
    // Not one container was addressed — addressing one is what provisions it.
    expect(sandboxes.booted).toHaveLength(0);
    // And not one credential was minted: a hard stop is a durable refusal to
    // ISSUE, which is the half of tick gyl that a revocation alone never was.
    expect(await listRunGatewayTokens(env.DB, runID)).toHaveLength(0);

    const record = (await readRunRecord(env.ARTIFACTS, project, runID)) as RunRecord;
    expect(record.detail).toContain("no orchestrator was credentialled");
  });

  it("credentials no replacement when the stop lands between two boots", async () => {
    const { runID } = await ignite();
    const first = await firstProcess();
    const firstToken = first.env.AI_GATEWAY_TOKEN!;

    // Armed BEFORE the container dies, fired inside the write that records the
    // death: the stop is in place before the next attempt's kill-check runs.
    onReconcileWrite(async () => {
      await stopRun(env, runID, "operator", "hard");
    });
    sandboxes.booted[0]!.vanished = true;

    const run = await settled(runID);
    expect(run.state).toBe("stopped");
    // The replacement boot the supervisor was on its way to making never
    // happened — and neither did a closeout boot, which is refused by the same
    // check.
    expect(sandboxes.booted).toHaveLength(1);
    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens).toHaveLength(1);
    expect(tokens[0]!.revoked_at).not.toBeNull();
    // The dead boot's credential cannot be spent by anything that survived it.
    const gateway = fakeGateway();
    expect((await modelCall(firstToken, gateway.fetcher)).status).toBe(403);
  });
});

/**
 * The lease-loss message, on its own: the tests above it were the wave
 * machinery's; the message fix outlived them because lease loss is a
 * supervisePass concern, not a wave one.
 */
describe("a lease loss says which of the two it was", () => {
  /**
   * The other half of the message fix. An expired lease and a stolen one are
   * opposite problems, and an operator must not read "another run took it"
   * when nothing did — `.tick/learnings.md`'s never-collapse-failure-classes
   * rule, which this epic has now hit four times.
   */
  it("says a lease was TAKEN only when another run really holds it", async () => {
    const taken = leaseLostTrip({ lost: "taken", holder: "run_other" });
    expect(taken.hard).toBe(true);
    expect(taken.detail).toContain("taken by another run");
    expect(taken.detail).toContain("run_other");

    const expired = leaseLostTrip({ lost: "expired", holder: null });
    expect(expired.hard).toBe(true);
    expect(expired.detail).toContain("expired before it was renewed");
    expect(expired.detail).not.toContain("another run");
    // The words run_659b7cf2's operator was given, and which were false there.
    expect(expired.detail).not.toContain("lost to another run");
  });
});

/**
 * Tick oen: a lapse is not a loss.
 *
 * A long-pass workflow test failed twice on a 2-vCPU CI runner. Both times its
 * 200ms lease lapsed before the run's first renewal and the run's log read
 * "could not renew its lease after boot 1: ... has expired or been released —
 * no dispatch lease is held for this project, and no other run has taken it".
 * In the evening run that was the failure: the run hard-stopped itself before
 * its container ever worked. In the afternoon run the test's own read of
 * the lease lost the same race first. Nobody else held the project either
 * time. A production boot, stall or step longer than the ten-minute acquire is
 * the same arithmetic.
 *
 * These make the lapse deterministic — the lease is expired explicitly, never
 * raced against a real clock — and pin both halves: a lapse nobody took is
 * reclaimed and the run works on; a lease another run TOOK is still a stop.
 */
describe("a run whose lease lapsed with nobody holding it (tick oen)", () => {
  let warned: string[];
  let restoreWarn: () => void;

  beforeEach(() => {
    warned = [];
    const original = console.warn;
    console.warn = (...args: unknown[]) => void warned.push(args.map(String).join(" "));
    restoreWarn = () => void (console.warn = original);
  });
  afterEach(() => restoreWarn());

  it("reclaims a lease its boot outlived, and works on", async () => {
    const { runID, project, epic, room, lease } = await ignite({ lapsed: true });
    expect(await room.leaseStatus()).toBeNull();
    const process = await firstProcess();

    // The `:lease:` step right after boot found the lapse and took it back —
    // the SAME lease: nobody touched the row, so its tenure is unbroken.
    const held = await waitFor("the run to reclaim its lease", async () => room.leaseStatus());
    expect(held.run_id).toBe(runID);
    expect(held.acquired_at).toBe(lease.acquired_at);
    expect(warned.some((line) => line.includes(`${runID} reclaimed its lapsed lease`))).toBe(true);

    // And it did NOT stop: the harness can still spend, the run is running.
    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens.filter((token) => token.revoked_at !== null)).toEqual([]);
    expect((await runStatus(env, runID))?.run.state).toBe("running");
    expect(process.killed).toBe(false);

    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
    // Released with the credentials it was ignited with: the reclaim never
    // rotated the run's token out from under its own release.
    expect(await roomFor(env, project).leaseStatus()).toBeNull();
  });

  it("reclaims a lease that lapses mid-run, and works on", async () => {
    const { runID, project, epic, room, lease } = await ignite();
    const process = await firstProcess();
    // Past the run's first renewal, so the lapse below lands on a run that
    // already held its lease through boot — the watch loop's renewal is the
    // one that finds it.
    await waitFor("the run's renewals", async () => {
      const now = await room.leaseStatus();
      return now !== null && now.expires_at > lease.expires_at;
    });

    await expireLease(project);
    expect(await room.leaseStatus()).toBeNull();

    const held = await waitFor("the run to reclaim its lease", async () => room.leaseStatus());
    expect(held.run_id).toBe(runID);
    expect(warned.some((line) => line.includes(`${runID} reclaimed its lapsed lease`))).toBe(true);

    const tokens = await listRunGatewayTokens(env.DB, runID);
    expect(tokens.filter((token) => token.revoked_at !== null)).toEqual([]);
    expect((await runStatus(env, runID))?.run.state).toBe("running");
    expect(process.killed).toBe(false);

    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
    expect(await roomFor(env, project).leaseStatus()).toBeNull();
  });

  /**
   * The other half, which a reclaim must never blur: a lease another run holds
   * is a real loss. D4 is one arbiter per project, and the run that is not it
   * stops — and says who is.
   */
  it("still stops when another run took the lease while it lapsed", async () => {
    const { runID, project, room, lease } = await ignite();
    const process = await firstProcess();
    await waitFor("the run's renewals", async () => {
      const now = await room.leaseStatus();
      return now !== null && now.expires_at > lease.expires_at;
    });

    const theirs = await handLeaseTo(project, "run_theirs");

    const closeout = await waitFor("the closeout orchestrator", async () =>
      sandboxes.phase("closeout"),
    );
    expect(process.killed).toBe(true);
    expect(closeout.env.TICKS_STOP_REASON ?? "").toContain("taken by another run (run_theirs)");
    expect(warned.some((line) => line.includes("reclaimed"))).toBe(false);

    closeout.exit(0);
    expect((await settled(runID)).state).toBe("stopped");
    // The other run's lease is untouched — neither reclaimed nor released by
    // the run that lost it.
    await expect(room.leaseStatus()).resolves.toMatchObject({
      run_id: "run_theirs",
      expires_at: theirs.expires_at,
    });
  });
});

/**
 * The board finally sees a live run (tick bne).
 *
 * These drive the REAL Workflow with a recording sink in the `RUN_EVENTS`
 * seam, so what is asserted is what a deployed factory would actually put on
 * the wire — not what a builder returns in isolation (that is
 * test/run-events.test.ts).
 */
describe("a run streams run_event to the board", () => {
  let published: RunEventMessage[];

  /** A sink that remembers what reached it, or refuses everything. */
  class WorkflowSink implements RunEventSink {
    fail: string | null = null;
    async publish(_project: string, event: RunEventMessage) {
      if (this.fail !== null) throw new Error(this.fail);
      published.push(event);
      return { delivered: true, detail: "ok" };
    }
  }

  let sink: WorkflowSink;

  beforeEach(() => {
    published = [];
    sink = new WorkflowSink();
    set("RUN_EVENTS", sink);
  });

  const seen = (type: string) => published.filter((e) => e.event.type === type);

  it("shows a run live, announced by the one orchestrator container it boots", async () => {
    const { runID, epic } = await ignite();
    const process = await firstProcess();
    orchestratorPushedWork(epic);
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");

    // The run announced itself before any container existed — as ONE
    // orchestrator container, the only shape a run has since tick l6t.
    const started = seen("epic-started");
    expect(started).toHaveLength(1);
    expect(started[0]!).toMatchObject({
      type: "run_event",
      epicId: epic,
      source: "cloud:orchestrator",
    });
    expect(started[0]!.taskId).toBeUndefined();
    expect(started[0]!.event.message).toContain(runID);
    // The versioned feed line says the same thing.
    expect(started[0]!.event.status).toBe("one orchestrator container");

    // And the run's last word. No per-tick events: the workers a run's
    // `ticfac run-epic` dispatches through the per-tick sandbox door are not
    // the Workflow's to announce — see the finding the wave deletion filed
    // about per-tick board visibility.
    expect(seen("epic-completed")).toHaveLength(1);
    expect(seen("epic-completed")[0]!.source).toBe("cloud:orchestrator");
    expect(seen("task-started")).toHaveLength(0);
  });

  it("carries the gateway's cost on the closing event and never an agent's", async () => {
    set("CLOUDFLARE_API_TOKEN", "cf-api-token");
    set("CLOUDFLARE_API_BASE_URL", LOGS_API);
    const logs = stubLogsAPI(0.5, undefined, 4);

    try {
      const { runID, epic } = await ignite();
      const process = await firstProcess();
      orchestratorPushedWork(epic);
      process.exit(0);
      const run = await settled(runID);

      const completed = seen("epic-completed");
      expect(completed).toHaveLength(1);
      // The same number the gateway logs produced and the index row recorded.
      expect(completed[0]!.event.metrics?.costUsd).toBeCloseTo(4 * 0.5, 6);
      expect(completed[0]!.event.metrics?.costUsd).toBeCloseTo(run.cost_usd, 6);
    } finally {
      logs.restore();
    }
  });

  it("publishes no cost at all when the gateway telemetry could not be read", async () => {
    // Unknown and free are different facts. The default harness has no
    // Cloudflare API token, so this is the unreadable case.
    const { runID, epic } = await ignite();
    const process = await firstProcess();
    orchestratorPushedWork(epic);
    process.exit(0);
    await settled(runID);

    const completed = seen("epic-completed");
    expect(completed).toHaveLength(1);
    expect(completed[0]!.event.metrics?.costUsd).toBeUndefined();
  });

  it("streams the Phase 1 single-orchestrator run too, without per-tick events", async () => {
    const { runID, epic } = await ignite();
    const process = await firstProcess();
    orchestratorPushedWork(epic);
    process.exit(0);

    expect((await settled(runID)).state).toBe("completed");
    expect(seen("epic-started")).toHaveLength(1);
    expect(seen("epic-completed")).toHaveLength(1);
    expect(seen("epic-completed")[0]!.event.success).toBe(true);
    expect(seen("task-started")).toHaveLength(0);
  });
});

// ------------------------------------------------- the completion signal ---
//
// Tick 7eq: a finished orchestrator POSTs /api/done, the Worker turns that
// into instance.sendEvent(), and the Run Workflow's wait returns immediately —
// the run is not concluded by polling for a finish that already happened. The
// signal is buffered by the platform, so a container that finished before its
// supervisor resumed loses nothing; and it decides NOTHING, because the
// container may still die after finishing and before its callback lands. The
// second half of the acceptance is exactly that case: no callback, and the run
// is still concluded correctly — from the branch, the way every verdict here
// is concluded (tick ehy's rule, applied to the wake-up rather than the
// verdict).
describe("the completion signal (tick 7eq)", () => {
  /** The POST a finished orchestrator makes: the done door, on its run token. */
  function postDoneSignal(
    token: string,
    body: { branch: string; head?: string },
  ): Promise<Response> {
    return SELF.fetch("https://factory.example.com/api/done", {
      method: "POST",
      headers: {
        // Exactly what the container holds: TICKS_FACTORY_TOKEN is the run's
        // gateway token, never the operator's factory token — the same
        // credential the sandbox dispatch door takes.
        authorization: `Bearer ${token}`,
        "content-type": "application/json",
      },
      body: JSON.stringify(body),
    });
  }

  it("tells every orchestrator boot where the factory is", async () => {
    const { runID } = await ignite();

    const process = await firstProcess();
    // Every pass reports its own finish, so every boot knows the done door —
    // and the same URL is what the container's run-epic hands the per-tick
    // sandbox door's client.
    expect(process.env.TICKS_FACTORY_URL).toBe(FACTORY);
    expect(process.env.TICKS_FACTORY_TOKEN).toBe(process.env.AI_GATEWAY_TOKEN);

    // The run still concludes; nothing about the boot changed otherwise.
    orchestratorPushedWork();
    process.exit(0);
    expect((await settled(runID)).state).toBe("completed");
  });

  it("wakes a finished run without polling — the signal, not the cadence", async () => {
    // A cadence the test could never wait out: if this settles at all, it was
    // the event that woke the look, because the first cadence look is ten
    // minutes away.
    set("RUN_POLL_INTERVAL_MS", "600000");
    const { runID, epic } = await ignite();

    const process = await firstProcess();
    const token = process.env.TICKS_FACTORY_TOKEN!;
    orchestratorPushedWork(epic);
    process.exit(0);

    // The container is already finished when its callback lands — the harshest
    // ordering, and the one buffering exists for. The event is buffered, the
    // next wait returns with it, and the look observes an exit that already
    // happened instead of sleeping to it.
    const answered = await postDoneSignal(token, { branch: `epic/${epic}`, head: PUSHED_SHA });
    expect(answered.status).toBe(202);
    expect(((await answered.json()) as { delivered: boolean }).delivered).toBe(true);

    // Not "eventually" — within the test's own budget, an order of magnitude
    // below the cadence.
    const run = await settled(runID);
    expect(run.state).toBe("completed");

    // The one durable trace that the callback landed and was consumed: the
    // run's own dispatch log says the signal was heard, whatever the payload
    // claimed.
    const logged = await listDispatchLogs(env.DB, runID, epic);
    expect(logged.some((entry) => entry.decision === "signal:done")).toBe(true);
    // The verdict still came from the durable layer: the branch moved, and the
    // progress record says so.
    const progress = await getRunProgress(env.DB, runID);
    expect(progress?.progress).toBe("advanced");
  });

  it("concludes correctly from the branch when the callback never lands", async () => {
    // Above MIN_EVENT_WAIT_MS, so the wait is the EVENT wait, exercising the
    // timeout path this is about: no callback arrives, the wait throws its
    // timeout, the supervisor catches it, and the look happens anyway.
    set("RUN_POLL_INTERVAL_MS", "2000");
    const { runID, epic } = await ignite();

    const process = await firstProcess();
    // The container finishes and pushes — and then dies before its callback
    // can land, which is the exact gap event buffering does NOT close.
    orchestratorPushedWork(epic);
    process.exit(0);

    const run = await settled(runID);
    // Concluded correctly, from the branch: the exit status only ended the
    // wait, and `completed` is the durable layer agreeing the epic moved
    // (tick ehy — an exit status alone would have read `stopped`).
    expect(run.state).toBe("completed");
    const progress = await getRunProgress(env.DB, runID);
    expect(progress?.progress).toBe("advanced");

    // And the signal row is absent, proving this run concluded with no
    // callback at all — the branch, never the event, is the source of truth.
    const logged = await listDispatchLogs(env.DB, runID, epic);
    expect(logged.some((entry) => entry.decision === "signal:done")).toBe(false);
  });

  it("never trusts the signal: an event claiming a finish does not conclude the run", async () => {
    set("RUN_POLL_INTERVAL_MS", "2000");
    const { runID, epic } = await ignite();

    const process = await firstProcess();
    // A lying or early signal: the orchestrator is STILL RUNNING, nothing has
    // been pushed, and the container has not exited. The wake-up must fall
    // through to a look that reads exactly that.
    const answered = await postDoneSignal(process.env.TICKS_FACTORY_TOKEN!, {
      branch: `epic/${epic}`,
      head: PUSHED_SHA,
    });
    expect(answered.status).toBe(202);

    // The run is NOT concluded by the claim: it keeps being supervised until
    // the orchestrator actually finishes — and then the verdict comes from the
    // branch, which moved, so `completed` stands.
    orchestratorPushedWork(epic);
    process.exit(0);
    const run = await settled(runID);
    expect(run.state).toBe("completed");

    const logged = await listDispatchLogs(env.DB, runID, epic);
    expect(logged.some((entry) => entry.decision === "signal:done")).toBe(true);
  });
});
