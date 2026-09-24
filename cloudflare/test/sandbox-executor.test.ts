/**
 * The sandbox compatibility executor (SPEC §12 Phase 4 item 4, tick k4s):
 * the four operations of the job-protocol attempt seam, built over the
 * SandboxBinding seam, exercised here against fakes the same way
 * worker-dispatch's own suite exercises spawn/wait/teardown — a lifecycle
 * only provable by starting a real container is a lifecycle nobody tests.
 */
import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import jobProtocol from "../../contracts/job-protocol.json";
import type { AttemptSpec } from "../src/attempt-protocol";
import { insertRun, type Run } from "../src/db";
import { authorizeRunCredential, revokeRunTokens } from "../src/gateway";
import type { GitRefWriter, RefPut } from "../src/git-refs";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import {
  AdoptionModelUnknownError,
  attemptSandboxName,
  reportFromWorker,
  type SandboxBootRecord,
  type SandboxExecutorDeps,
  type SandboxJobHandle,
  sandboxExecutor,
  sandboxExecutorFromEnv,
} from "../src/sandbox-executor";
import {
  attemptLandingBranch,
  WORKER_CANCEL_COMMAND,
  WORKER_CANCEL_MARKER,
  WORKER_COMMAND,
  WORKER_PROBE_MARKER,
  type WorkerBootInput,
} from "../src/worker-boot";
import type { WorkerCollector, WorkerReport, WorkerTask } from "../src/worker-collect";
import { type Defs, parseDefs, parseSchema, validate } from "./json-schema";

// --------------------------------------------------------------- the fakes ---

/** One process inside a fake container. */
class FakeProcess {
  state: SandboxProcessState = "running";
  exit_code: number | null = null;
  output = "";

  constructor(
    readonly id: string,
    readonly command: string,
    readonly env: Record<string, string>,
  ) {}

  finish(code: number): void {
    this.state = code === 0 ? "completed" : "failed";
    this.exit_code = code;
  }

  get view(): SandboxProcessView {
    return { id: this.id, state: this.state, exit_code: this.exit_code, command: this.command };
  }
}

/**
 * One fake container. A process started with the probe command answers its
 * marker and a process started with the work command produces output — the
 * two contents the dispatch machinery waits for — so the executor's tests
 * run the REAL probe and confirm loops without wall-clock waiting.
 */
class FakeSandbox implements OrchestratorSandbox {
  readonly processes: FakeProcess[] = [];
  destroyed = false;
  #next = 0;

  constructor(readonly name: string) {}

  async startProcess(
    command: string,
    options: { env: Record<string, string> },
  ): Promise<SandboxProcessView> {
    const process = new FakeProcess(`${this.name}-p${++this.#next}`, command, options.env);
    if (command.includes("--probe")) {
      // The real probe prints its marker and EXITS; watchProbe only evaluates
      // a probe once it reaches a terminal state.
      process.output = WORKER_PROBE_MARKER;
      process.finish(0);
    }
    if (command === WORKER_COMMAND) process.output = "boot banner\n";
    if (command.startsWith(WORKER_CANCEL_COMMAND)) {
      process.output = WORKER_CANCEL_MARKER;
      // The door lodges the request and stops the harness: the work process
      // ends inside the grace window, the way a real one does.
      for (const other of this.processes) {
        if (other.command === WORKER_COMMAND && other.state === "running") other.finish(0);
      }
    }
    this.processes.push(process);
    return process.view;
  }

  async getProcess(id: string): Promise<SandboxProcessView | null> {
    const process = this.processes.find((p) => p.id === id);
    return process === undefined ? null : process.view;
  }

  async listProcesses(): Promise<SandboxProcessView[]> {
    return this.processes.map((p) => ({ ...p.view }));
  }

  async readOutput(id: string, offset: number): Promise<SandboxOutput> {
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined) return { text: "", offset };
    return { text: process.output.slice(offset), offset: process.output.length };
  }

  async killProcess(id: string): Promise<void> {
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined) return;
    process.finish(143);
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
  }

  /** The work process, if this container has one. */
  workProcess(): FakeProcess | undefined {
    return this.processes.find((p) => p.command === WORKER_COMMAND);
  }
}

/** The binding: containers by name, provisioned on first address. */
class FakeSandboxes implements SandboxBinding {
  readonly #byName = new Map<string, FakeSandbox>();

  async get(name: string): Promise<OrchestratorSandbox> {
    let sandbox = this.#byName.get(name);
    if (sandbox === undefined) {
      sandbox = new FakeSandbox(name);
      this.#byName.set(name, sandbox);
    }
    return sandbox;
  }

  named(name: string): FakeSandbox {
    const sandbox = this.#byName.get(name);
    if (sandbox === undefined) throw new Error(`no sandbox named ${name} was addressed`);
    return sandbox;
  }
}

const RUN_ID = "run-x";
const BASE_SHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2";
const SPEC: AttemptSpec = {
  run_id: RUN_ID,
  epic_id: "ncv",
  tick_id: "k4s",
  attempt: 3,
  role: "implement-tick",
  project: "pengelbrecht/ticfac",
  // The attempt's own ref, in the same vocabulary the local dispatch mints
  // (job-protocol's golden write_refs, internal/reconcile's attemptWriteRef).
  write_ref: `refs/heads/ticfac/run-${RUN_ID}/tick-k4s/attempt-3`,
  base_ref: `refs/heads/epic/ncv`,
  title: "the sandbox executor",
};

// The pinned job-protocol definitions, so a cloud-produced handle and status
// are validated against the same contract a local executor's answer to
// (tick us2): the compatibility claim is checked, not asserted.
const protocolDefs: Defs = parseDefs((jobProtocol as { $defs: unknown }).$defs);
const jobHandleSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_handle,
  "$",
);
const jobStatusSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_status,
  "$",
);

/** Boot inputs with no secrets worth leaking, like every test fixture. */
function bootInput(spec: AttemptSpec): WorkerBootInput {
  return {
    repo_url: "https://example.com/pengelbrecht/ticfac.git",
    base_sha: BASE_SHA,
    epic: spec.epic_id,
    tick: spec.tick_id,
    // The attempt rides the boot, so the container's TICKS_EPIC derives the
    // per-attempt landing branch (worker-boot.ts, tick us2).
    attempt: spec.attempt,
    run_id: spec.run_id,
    gateway_base_url: "https://factory.example.com/api/gateway",
    gateway_token: "tkr_testtoken",
    // The model the dispatch resolved, as the deployment's own boot composes
    // it: the request's choice outranks the standing one, so a spec that
    // names a different model is a container booted on a different one —
    // exactly the adoption case the boot record exists for (tick dyo).
    ...(spec.model === undefined ? {} : { model: spec.model }),
  };
}

/**
 * A collector the tests fill per case; collect's mapping is what the
 * executor's own tests pin, so the fake answers with whatever a case wants.
 */
class FakeCollector implements WorkerCollector {
  report: WorkerReport = {
    tick_id: SPEC.tick_id,
    branch: attemptLandingBranch(SPEC.epic_id, SPEC.attempt, SPEC.tick_id),
    base_sha: BASE_SHA,
    verdict: "ready-to-merge",
    branch_exists: true,
    commits: 2,
    result_path: "RESULT-k4s.md",
    result_exists: true,
    status: "DONE",
    status_detail: "the work is in",
    status_line: "STATUS: DONE",
    boundary_files: [],
    detail: "ready",
  };
  readonly asked: WorkerTask[] = [];

  async collect(task: WorkerTask): Promise<WorkerReport> {
    this.asked.push(task);
    return { ...this.report, tick_id: task.tick_id, branch: task.branch };
  }
}

/**
 * The ref writer the tests fill per case: collect's push of the attempt's
 * write_ref is recorded, and a case can make it fail the way GitHub can.
 */
class FakeRefWriter implements GitRefWriter {
  readonly puts: Array<{ branch: string; ref: string }> = [];
  answer: RefPut = { state: "created", sha: "pushed-head-1" };

  async put(input: { branch: string; ref: string }): Promise<RefPut> {
    this.puts.push(input);
    return this.answer;
  }
}

/**
 * The boot record the tests fill per case (tick dyo): what each attempt's
 * container was booted on, keyed by identity, so the adoption's read is
 * exercised against the same contract the deployed D1 wiring answers to.
 */
class FakeBootRecord implements SandboxBootRecord {
  readonly recorded: Array<{ run_id: string; tick_id: string; attempt: number; model: string }> =
    [];
  readonly #byIdentity = new Map<string, string>();

  /** Every boot the record holds, forgotten — a deployment that predates it. */
  forget(): void {
    this.#byIdentity.clear();
  }

  async record(boot: {
    run_id: string;
    tick_id: string;
    attempt: number;
    model: string;
  }): Promise<void> {
    this.#byIdentity.set(`${boot.run_id}/${boot.tick_id}/${boot.attempt}`, boot.model);
    this.recorded.push({ ...boot });
  }

  async modelOf(identity: {
    run_id: string;
    tick_id: string;
    attempt: number;
  }): Promise<string | null> {
    return (
      this.#byIdentity.get(`${identity.run_id}/${identity.tick_id}/${identity.attempt}`) ?? null
    );
  }
}

/** The executor under test, wired over the fakes. */
function makeExecutor() {
  const binding = new FakeSandboxes();
  const collector = new FakeCollector();
  const refs = new FakeRefWriter();
  const boots = new FakeBootRecord();
  const deps: SandboxExecutorDeps = {
    binding,
    collector,
    refs,
    boot: async (spec) => bootInput(spec),
    boots,
    // No wall clock: the fake containers answer the probes instantly, and
    // nothing here should ever depend on real waiting.
    spawn: { sleep: async () => {} },
  };
  return { binding, collector, refs, boots, executor: sandboxExecutor(deps) };
}

/** Unwraps a handle into this executor's own shape. */
function asHandle(handle: unknown): SandboxJobHandle {
  return handle as SandboxJobHandle;
}

// ---------------------------------------------------------------- the name ---

describe("the container's name", () => {
  it("carries the attempt, so a redispatch lands in a fresh container", () => {
    expect(attemptSandboxName("run-x", "k4s", 1)).toBe("run-x-k4s-1");
    expect(attemptSandboxName("run-x", "k4s", 2)).not.toBe(attemptSandboxName("run-x", "k4s", 1));
  });
});

// ------------------------------------------------------------------ start ---

describe("start", () => {
  it("boots a fresh container per attempt and probes before any work", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));

    // The job_handle contract's closed top level (job-protocol $defs.job_handle):
    // identity, executor name, the one open `handle` object, the issue time.
    expect(handle.schema_version).toBe(1);
    expect(handle.executor).toBe("cloudflare-sandbox");
    expect(handle.job_id).toBe(`run-${RUN_ID}/tick-k4s/attempt-3`);
    expect(handle.attempt).toBe(3);
    expect(handle.issued_at).not.toBe("");
    // The cloud's own addressing rides INSIDE the handle — never as extra
    // top-level fields beside the identity the contract closes.
    expect(handle.handle.sandbox).toBe("run-x-k4s-3");
    expect(handle.handle.write_ref).toBe(SPEC.write_ref);
    expect(handle.handle.base_sha).toBe(BASE_SHA);
    expect(handle.handle.launched).toBe(true);
    expect(handle.handle.process_id).not.toBeNull();

    const sandbox = binding.named("run-x-k4s-3");
    // The probe ran FIRST: the green-start trap is the executor's too, not a
    // wave-only decoration.
    expect(sandbox.processes[0]?.command).toContain("--probe");
    const work = sandbox.workProcess();
    expect(work).toBeDefined();
    expect(handle.handle.process_id).toBe(work?.id);
  });

  it("boots the container on a per-attempt landing branch, so a redispatch cannot land on the previous attempt's work", async () => {
    const { binding, executor } = makeExecutor();
    await executor.start(SPEC);

    const work = binding.named("run-x-k4s-3").workProcess();
    expect(work?.env.TICKS_TICK).toBe("k4s");
    // The attempt rides the epic slot, and the container derives
    // tick/<epic>/attempt-<n>/<tick> from it (image/worker.sh).
    expect(work?.env.TICKS_EPIC).toBe("ncv/attempt-3");
    expect(attemptLandingBranch("ncv", 3, "k4s")).toBe("tick/ncv/attempt-3/k4s");
  });

  it("starts the work with the boot inputs the seam composed", async () => {
    const { binding, executor } = makeExecutor();
    await executor.start(SPEC);
    const work = binding.named("run-x-k4s-3").workProcess();
    expect(work?.env.TICKS_REPO_URL).toBe("https://example.com/pengelbrecht/ticfac.git");
    expect(work?.env.TICKS_BASE_SHA).toBe(BASE_SHA);
    expect(work?.env.TICKS_TICK).toBe("k4s");
    expect(work?.env.AI_GATEWAY_TOKEN).toBe("tkr_testtoken");
  });

  it("ADOPTS a live work process instead of dispatching a second one", async () => {
    const { binding, executor } = makeExecutor();
    const first = asHandle(await executor.start(SPEC));
    const before = binding.named("run-x-k4s-3").processes.length;

    // A Workflow step replay: the same spec, asked again.
    const second = asHandle(await executor.start(SPEC));

    expect(second.handle.process_id).toBe(first.handle.process_id);
    expect(second.handle.launched).toBe(true);
    expect(second.handle.detail).toContain("adopted");
    expect(binding.named("run-x-k4s-3").processes.length).toBe(before);
  });

  it("an adoption names the RUNNING container's model, never the request's (tick dyo)", async () => {
    const { binding, boots, executor } = makeExecutor();
    const firstSpec: AttemptSpec = { ...SPEC, model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3" };
    await executor.start(firstSpec);

    // The boot the first start made is RECORDED before the container was
    // addressed, so the model is durable the moment there is a container to
    // ask about at all.
    expect(boots.recorded).toEqual([
      expect.objectContaining({
        run_id: SPEC.run_id,
        tick_id: SPEC.tick_id,
        attempt: SPEC.attempt,
        model: firstSpec.model,
      }),
    ]);
    expect(binding.named("run-x-k4s-3").workProcess()?.env.TICKS_MODEL).toBe(firstSpec.model);

    // A restarted incarnation whose profile now resolves a DIFFERENT model:
    // the container still running the tick was booted on the first one, and
    // the handle must state what the CONTAINER is on — never echo the new
    // request back as the answer, which is how a record came to name a model
    // nobody observed and the caller's model checks could never fire here.
    const second = asHandle(
      await executor.start({ ...SPEC, model: "workers-ai/@cf/example/some-other-model" }),
    );
    expect(second.handle.detail).toContain("adopted");
    expect(second.handle.model).toBe(firstSpec.model);
    expect(second.handle.model).not.toBe("workers-ai/@cf/example/some-other-model");
    // And nothing re-recorded: an adoption boots nothing, so the boot that
    // started the running container stays the one on record.
    expect(boots.recorded).toHaveLength(1);
  });

  it("refuses an adoption whose running container has no recorded boot, rather than guessing", async () => {
    const { boots, executor } = makeExecutor();
    await executor.start(SPEC);

    // A container booted by a deployment that did not record boots: the work
    // process is live, and nobody can state which model it is on. Adopting it
    // would put a guess in the one field every trace reads, so the start is
    // refused for a person to settle — never a lie in a handle.
    boots.forget();
    await expect(executor.start(SPEC)).rejects.toBeInstanceOf(AdoptionModelUnknownError);
    await expect(executor.start(SPEC)).rejects.toThrow(/cannot be stated/);
  });

  it("reports a failed green-start probe as not launched, never as absent", async () => {
    const { binding, collector, refs } = makeExecutor();
    // A container whose probe never answers: nothing prints the marker and
    // nothing exits, so the green-start trap is what the executor reports.
    const poisoned: SandboxBinding = {
      get: async (name) => {
        const sandbox = (await binding.get(name)) as unknown as FakeSandbox;
        const start = sandbox.startProcess.bind(sandbox);
        return {
          startProcess: async (command: string, options: { env: Record<string, string> }) => {
            const view = await start(command, options);
            if (command.includes("--probe")) sandbox.processes.at(-1)!.output = "";
            return view;
          },
          getProcess: (id: string) => sandbox.getProcess(id),
          listProcesses: () => sandbox.listProcesses(),
          readOutput: (id: string, offset: number) => sandbox.readOutput(id, offset),
          killProcess: (id: string) => sandbox.killProcess(id),
          destroy: () => sandbox.destroy(),
        } satisfies OrchestratorSandbox;
      },
    };
    const failing = sandboxExecutor({
      binding: poisoned,
      collector,
      refs,
      boot: async (spec) => bootInput(spec),
      boots: new FakeBootRecord(),
      spawn: { probe_timeout_ms: 10, probe_poll_ms: 1, sleep: async () => {} },
    });

    const handle = asHandle(await failing.start(SPEC));
    expect(handle.handle.launched).toBe(false);
    expect(handle.handle.detail).toContain("green-start trap");
  });

  it("returns a handle the pinned job-protocol contract takes, and refuses to be lied to", async () => {
    const { executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    // The contract suite, run against a cloud-produced record (tick us2): the
    // same closed job_handle a local executor's start answers to.
    const errors = validate(
      jobHandleSchema,
      protocolDefs,
      handle as unknown as Record<string, unknown>,
    );
    expect(errors, `job_handle must satisfy the pinned contract: ${errors.join("; ")}`).toEqual([]);
    // And the validator is a validator: drop a required field and it refuses.
    const mangled = { ...(handle as unknown as Record<string, unknown>) };
    delete mangled.issued_at;
    const refused = validate(jobHandleSchema, protocolDefs, mangled);
    expect(refused.join(";")).toContain("issued_at");
  });
});

// ---------------------------------------------------------------- inspect ---

describe("inspect", () => {
  it("reports a running work process in the contract's own vocabulary", async () => {
    const { executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    const status = await executor.inspect(handle);
    expect(status).toMatchObject({
      schema_version: 1,
      job_id: `run-${RUN_ID}/tick-k4s/attempt-3`,
      state: "running",
      terminal: false,
    });
    // The pinned job_status contract takes a cloud-produced status (tick us2).
    const errors = validate(
      jobStatusSchema,
      protocolDefs,
      status as unknown as Record<string, unknown>,
    );
    expect(errors, `job_status must satisfy the pinned contract: ${errors.join("; ")}`).toEqual([]);
  });

  it("reports a terminal process in the contract's own vocabulary, with its exit code riding an observation", async () => {
    const { binding, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    binding.named("run-x-k4s-3").workProcess()?.finish(0);
    expect(await executor.inspect(handle)).toMatchObject({ state: "succeeded", terminal: true });
    binding.named("run-x-k4s-3").workProcess()?.finish(11);
    expect(await executor.inspect(handle)).toMatchObject({ state: "failed", terminal: true });
  });

  it("falls back to the process list when the id answers nothing", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    const _sandbox = binding.named("run-x-k4s-3");
    // The recorded id goes stale — the container forgot it. The list still
    // answers, and "no id" must not read as "nothing is running".
    const stale = handle.handle.process_id;
    handle.handle.process_id = "gone-pid";
    expect(await executor.inspect(handle)).toMatchObject({ state: "running" });
    handle.handle.process_id = stale;
  });

  it("says lost — the observer's gap, never a verdict on the job — when neither the id nor the list can answer", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    binding.named("run-x-k4s-3").workProcess()?.finish(0);
    expect(await executor.inspect(handle)).toMatchObject({ state: "succeeded", terminal: true });
    handle.handle.process_id = "gone-pid";
    const status = await executor.inspect(handle);
    // `lost` is deliberately not terminal in the contract: it says nobody can
    // address the handle, which is a statement about the observer.
    expect(status).toMatchObject({ state: "lost", terminal: false });
    const errors = validate(
      jobStatusSchema,
      protocolDefs,
      status as unknown as Record<string, unknown>,
    );
    expect(errors).toEqual([]);
  });

  it("refuses a record that carries no executor handle, rather than addressing nothing", async () => {
    const { executor } = makeExecutor();
    // A dispatch marker from before the handle was persisted: the identity
    // half alone names the dispatch, and nothing in it names the container.
    await expect(executor.inspect({ tick_id: "k4s", attempt: 3 })).rejects.toThrow(
      /no executor handle|cannot be re-addressed/,
    );
    await expect(executor.collect({ tick_id: "k4s", attempt: 3 })).rejects.toThrow(
      /no executor handle|cannot be re-addressed/,
    );
  });

  it("refuses another executor's handle by name", async () => {
    const { executor } = makeExecutor();
    const foreign = {
      schema_version: 1,
      job_id: "run-x/tick-k4s/attempt-3",
      attempt: 3,
      executor: "herdr",
      handle: { workspace_id: "w2f" },
      issued_at: new Date().toISOString(),
    };
    await expect(executor.inspect(foreign)).rejects.toThrow(/herdr/);
  });
});

// ---------------------------------------------------------------- collect ---

describe("collect", () => {
  it("puts the container's pushed head on the attempt's write_ref, then reads the write_ref", async () => {
    const { collector, refs, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    const report = await executor.collect(handle);

    // The executor's push, the cloud's port of the local executor's own
    // pushBranch: the container lands on a per-attempt branch (the image
    // derives it), and collect is the moment the attempt's own ref — the one
    // the marker, the settle and a person all name — comes to carry the work.
    expect(refs.puts).toEqual([{ branch: "tick/ncv/attempt-3/k4s", ref: SPEC.write_ref }]);
    // And the collect READS the attempt's write_ref, never the landing
    // branch: the reconciler's settle and the collect look at one ref.
    expect(collector.asked).toEqual([
      {
        tick_id: "k4s",
        branch: "ticfac/run-run-x/tick-k4s/attempt-3",
        base_sha: BASE_SHA,
      },
    ]);
    expect(report.outcome).toBe("done");
    expect(report.commits).toBe(2);
  });

  it("never reports a clean verdict when the write_ref could not be advanced", async () => {
    const { collector, executor, refs } = makeExecutor();
    refs.answer = { state: "refused", detail: "GitHub answered HTTP 403 creating the ref" };
    const handle = await executor.start(SPEC);
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("failed");
    expect(report.commits).toBe(0);
    expect(report.detail).toContain(SPEC.write_ref);
    expect(report.detail).toContain("could not be advanced");
    expect(report.detail).toContain("403");
    // The collect never ran: there is no read of a ref that was not written.
    expect(collector.asked).toEqual([]);
  });

  it("collects the write_ref as it stands when the container never pushed its landing branch", async () => {
    const { collector, executor, refs } = makeExecutor();
    // The landing branch is absent — a container that died before its push.
    // The write_ref may still hold an earlier collect of this same attempt.
    refs.answer = { state: "missing" };
    const handle = await executor.start(SPEC);
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("done"); // the fake's verdict, read off the write_ref
    expect(collector.asked).toEqual([
      {
        tick_id: "k4s",
        branch: "ticfac/run-run-x/tick-k4s/attempt-3",
        base_sha: BASE_SHA,
      },
    ]);
  });

  it("maps a DONE_WITH_CONCERNS report to done, concerns riding the detail", async () => {
    const { collector, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    collector.report = {
      ...collector.report,
      status: "DONE_WITH_CONCERNS",
      status_detail: "one flake",
      status_line: "STATUS: DONE_WITH_CONCERNS — one flake",
    };
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("done");
    expect(report.detail).toContain("with concerns");
    expect(report.detail).toContain("one flake");
  });

  it("maps a BLOCKED report to blocked, for the run to stop on", async () => {
    const { collector, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    collector.report = {
      ...collector.report,
      status: "BLOCKED",
      status_detail: "the contract file is missing",
      status_line: "STATUS: BLOCKED — the contract file is missing",
    };
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("blocked");
    expect(report.detail).toContain("the contract file is missing");
  });
});

/** The pure mapping, pinned per verdict — the vocabulary is the compatibility claim. */
describe("reportFromWorker", () => {
  const base: WorkerReport = {
    tick_id: "k4s",
    branch: "ticfac/run-run-x/tick-k4s/attempt-3",
    base_sha: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
    verdict: "ready-to-merge",
    branch_exists: true,
    commits: 2,
    result_path: "RESULT-k4s.md",
    result_exists: true,
    status: "DONE",
    status_detail: "in",
    status_line: "STATUS: DONE",
    boundary_files: [],
    detail: "ready",
  };

  it("done: ready-to-merge with a DONE report", () => {
    expect(reportFromWorker(base)).toMatchObject({ outcome: "done", commits: 2 });
  });

  it("blocked: NEEDS_CONTEXT is a human escalation too", () => {
    const report = reportFromWorker({
      ...base,
      status: "NEEDS_CONTEXT",
      status_detail: "which registry?",
      status_line: "STATUS: NEEDS_CONTEXT — which registry?",
    });
    expect(report.outcome).toBe("blocked");
  });

  it("failed: no-commits, missing-result and boundary violations", () => {
    for (const verdict of ["no-commits", "missing-result", "boundary-violation"] as const) {
      const report = reportFromWorker({ ...base, verdict });
      expect(report.outcome, verdict).toBe("failed");
      expect(report.detail).toContain(verdict);
    }
  });

  it("failed, and never a clean verdict: an unreadable remote", () => {
    const report = reportFromWorker({ ...base, verdict: "unknown", commits: 0 });
    expect(report.outcome).toBe("failed");
    expect(report.detail).toContain("could not be read");
  });
});

// ----------------------------------------------------------------- cancel ---

describe("cancel", () => {
  it("asks the container to stop and push before destroying it", async () => {
    const { binding, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    await executor.cancel(handle);

    const sandbox = binding.named("run-x-k4s-3");
    const door = sandbox.processes.find((p) => p.command.startsWith(WORKER_CANCEL_COMMAND));
    expect(door).toBeDefined();
    // The work process ended inside the window — the door did its job — and
    // the container was reclaimed either way.
    expect(sandbox.workProcess()?.state).not.toBe("running");
    expect(sandbox.destroyed).toBe(true);
  });

  it("still reclaims a container whose work process never started", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    handle.handle.process_id = null;
    await executor.cancel(handle);
    expect(binding.named("run-x-k4s-3").destroyed).toBe(true);
  });
});

// ------------------------------------------------------- the whole executor ---

describe("the four operations, end to end", () => {
  it("a dispatch a reconciler would settle: start, watch it finish, collect", async () => {
    const { binding, executor, refs } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    binding.named("run-x-k4s-3").workProcess()?.finish(0);
    const status = await executor.inspect(handle);
    expect(status).toMatchObject({ state: "succeeded", terminal: true });
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("done");
    expect(report.detail).toContain("ready-to-merge");
    // The work reached the attempt's own ref on the way to the verdict.
    expect(refs.puts).toEqual([{ branch: "tick/ncv/attempt-3/k4s", ref: SPEC.write_ref }]);
    const record = handle as unknown as Record<string, unknown>;
    // The handle is a local attempt's shape, not a cloud-only one.
    expect(record.executor).toBe("cloudflare-sandbox");
    expect(JSON.stringify(handle)).not.toContain("tkr_testtoken");
  });
});

// ------------------------------------------ the deployment wiring (tick 53s) ---

/**
 * The wiring the deployable actually runs: `sandboxExecutorFromEnv`, with the
 * REAL D1 token tables behind it and the `SANDBOXES` seam faked, so the
 * credential a boot mints is the credential the gateway will judge.
 *
 * The fakes above stub `boot`, so they are green no matter what minting it
 * does; these are the tests that do not stub it. A run with `max_parallel > 1`
 * holds SEVERAL workers at once, and every one of them spends and pushes
 * through the run token its boot handed it — so a boot that revokes the run's
 * live tokens (what D17's rotation does for the ONE orchestrator a run
 * holds) cuts its own siblings off with 403 run_token_revoked, and parallel
 * dispatch becomes impossible (tick 53s).
 */
describe("the deployment wiring's boot credential", () => {
  const saved: Record<string, unknown> = {};

  afterEach(() => {
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
      else (env as unknown as Record<string, unknown>)[name] = value;
      delete saved[name];
    }
  });

  /** Sets a deployment variable for this describe, restored after each test. */
  function set(name: string, value: unknown): void {
    if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
  }

  /** A run in the index, live enough that its tokens can spend. */
  async function liveRun(): Promise<Run> {
    const run: Run = {
      run_id: `run_53s_${crypto.randomUUID()}`,
      project: "example-org/example-repo",
      epic: "ncv",
      base_sha: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
      requested_by: "operator",
      state: "running",
      started_at: new Date().toISOString(),
      ended_at: null,
      cost_usd: 0,
      trace_id: null,
      credential_grade: "write",
    };
    await insertRun(env.DB, run);
    return run;
  }

  /** The executor the deployment would dispatch through, over fake containers. */
  function deployedExecutor(run: Run, binding: FakeSandboxes) {
    set("SANDBOXES", binding);
    set("FACTORY_BASE_URL", "https://factory.example.com");
    set("GITHUB_TOKEN", "gh-operator-test-token");
    return sandboxExecutorFromEnv(env, {
      project: run.project,
      base_sha: run.base_sha,
    });
  }

  /** The gateway token one worker's boot handed its work process. */
  function workerToken(binding: FakeSandboxes, run: Run, tick: string, attempt: number): string {
    const work = binding.named(attemptSandboxName(run.run_id, tick, attempt)).workProcess();
    const token = work?.env.AI_GATEWAY_TOKEN ?? "";
    expect(token, `${tick}'s work process must hold a gateway token`).not.toBe("");
    return token;
  }

  it("a second worker's boot leaves the first's token usable (parallel dispatch)", async () => {
    const run = await liveRun();
    const binding = new FakeSandboxes();
    const executor = deployedExecutor(run, binding);
    expect(executor).toBeDefined();

    // max_parallel in miniature: two ticks of one run. Booted one after
    // the other — the deterministic ordering of what production interleaves —
    // because the defect is order-INVARIANT: a boot that revokes kills its
    // siblings whether it lands before or after them.
    const spec = (tick: string): AttemptSpec => ({
      ...SPEC,
      run_id: run.run_id,
      tick_id: tick,
      attempt: 1,
      write_ref: `refs/heads/tick-${run.run_id}/${tick}`,
    });
    await executor!.start(spec("k4s"));
    await executor!.start(spec("m9x"));

    const first = workerToken(binding, run, "k4s", 1);
    const second = workerToken(binding, run, "m9x", 1);
    // Per worker, not shared: a credential two attempts share is one a
    // revocation cannot take back from just one of them.
    expect(second).not.toBe(first);

    // THE acceptance: the FIRST worker's token still authorizes after the
    // second worker booted. This is the exact 403 run_token_revoked the old
    // per-boot revocation produced on the second worker's first boot.
    await expect(authorizeRunCredential(env, first)).resolves.toMatchObject({
      ok: true,
      token: { run_id: run.run_id, tick_id: "k4s", revoked_at: null },
    });
    await expect(authorizeRunCredential(env, second)).resolves.toMatchObject({
      ok: true,
      token: { run_id: run.run_id, tick_id: "m9x" },
    });

    // Their lifetimes end at the run's kill switch, not at each other's
    // boots: one revoke is what ends BOTH, and it is the run that holds it.
    expect(await revokeRunTokens(env, run.run_id, "stopped:hard")).toBe(2);
    await expect(authorizeRunCredential(env, first)).resolves.toMatchObject({
      ok: false,
      denial: { error: "run_token_revoked" },
    });
  });

  it("an adoption boot leaves the adopted worker's own token live", async () => {
    const run = await liveRun();
    const binding = new FakeSandboxes();
    const executor = deployedExecutor(run, binding);
    expect(executor).toBeDefined();
    const spec: AttemptSpec = {
      ...SPEC,
      run_id: run.run_id,
      tick_id: "k4s",
      attempt: 1,
      write_ref: `refs/heads/tick-${run.run_id}/k4s`,
    };

    const first = asHandle(await executor!.start(spec));
    const token = workerToken(binding, run, "k4s", 1);

    // A Workflow step replay: the same spec asked again ADOPTS the running
    // work process — and must not kill the credential that process is
    // spending with, which is what the boot the old adoption path took did.
    const replay = asHandle(await executor!.start(spec));
    expect(replay.handle.detail).toContain("adopted");
    expect(replay.handle.process_id).toBe(first.handle.process_id);
    await expect(authorizeRunCredential(env, token)).resolves.toMatchObject({ ok: true });
  });

  it("cancelling one worker leaves its sibling's token live", async () => {
    const run = await liveRun();
    const binding = new FakeSandboxes();
    const executor = deployedExecutor(run, binding);
    expect(executor).toBeDefined();
    const spec = (tick: string): AttemptSpec => ({
      ...SPEC,
      run_id: run.run_id,
      tick_id: tick,
      attempt: 1,
      write_ref: `refs/heads/tick-${run.run_id}/${tick}`,
    });
    await executor!.start(spec("k4s"));
    const cancelled = await executor!.start(spec("m9x"));
    const survivor = workerToken(binding, run, "k4s", 1);

    // Cancel composes a boot to re-derive the salvage door — and that boot
    // must not spend the sibling's credential to do it.
    await executor!.cancel(cancelled);
    expect(binding.named(attemptSandboxName(run.run_id, "m9x", 1)).destroyed).toBe(true);
    await expect(authorizeRunCredential(env, survivor)).resolves.toMatchObject({ ok: true });
  });
});
