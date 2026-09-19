/**
 * The sandbox compatibility executor (SPEC §12 Phase 4 item 4, tick k4s):
 * the four operations the EpicReconciler dispatches through, built over the
 * SandboxBinding seam, exercised here against fakes the same way
 * worker-dispatch's own suite exercises spawn/wait/teardown — a lifecycle
 * only provable by starting a real container is a lifecycle nobody tests.
 */
import { describe, expect, it } from "vitest";
import type { AttemptSpec } from "../src/epic-reconciler";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import {
  attemptSandboxName,
  reportFromWorker,
  type SandboxAttemptHandle,
  type SandboxExecutorDeps,
  sandboxExecutor,
} from "../src/sandbox-executor";
import type { WorkerBootInput } from "../src/worker-boot";
import {
  WORKER_CANCEL_COMMAND,
  WORKER_CANCEL_MARKER,
  WORKER_COMMAND,
  WORKER_PROBE_MARKER,
} from "../src/worker-boot";
import type { WorkerCollector, WorkerReport, WorkerTask } from "../src/worker-collect";

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
const SPEC: AttemptSpec = {
  run_id: RUN_ID,
  epic_id: "ncv",
  tick_id: "k4s",
  attempt: 3,
  role: "implement-tick",
  project: "pengelbrecht/ticfac",
  write_ref: "refs/heads/tick-run-x/k4s",
  base_ref: "refs/heads/main",
  title: "the sandbox executor",
};

/** Boot inputs with no secrets worth leaking, like every test fixture. */
function bootInput(spec: AttemptSpec): WorkerBootInput {
  return {
    repo_url: "https://example.com/pengelbrecht/ticfac.git",
    base_sha: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
    epic: spec.epic_id,
    tick: spec.tick_id,
    run_id: spec.run_id,
    gateway_base_url: "https://factory.example.com/api/gateway",
    gateway_token: "tkr_testtoken",
  };
}

/**
 * A collector the tests fill per case; collect's mapping is what the
 * executor's own tests pin, so the fake answers with whatever a case wants.
 */
class FakeCollector implements WorkerCollector {
  report: WorkerReport = {
    tick_id: SPEC.tick_id,
    branch: "tick/ncv/k4s",
    base_sha: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
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

/** The executor under test, wired over the fakes. */
function makeExecutor() {
  const binding = new FakeSandboxes();
  const collector = new FakeCollector();
  const deps: SandboxExecutorDeps = {
    binding,
    collector,
    boot: async (spec) => bootInput(spec),
    // No wall clock: the fake containers answer the probes instantly, and
    // nothing here should ever depend on real waiting.
    spawn: { sleep: async () => {} },
  };
  return { binding, collector, executor: sandboxExecutor(deps) };
}

/** Unwraps a handle into this executor's own shape. */
function asHandle(handle: unknown): SandboxAttemptHandle {
  return handle as SandboxAttemptHandle;
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

    expect(handle.executor).toBe("cloudflare-sandbox");
    expect(handle.job_id).toBe(`run-${RUN_ID}/tick-k4s/attempt-3`);
    expect(handle.sandbox).toBe("run-x-k4s-3");
    expect(handle.write_ref).toBe(SPEC.write_ref);
    expect(handle.branch).toBe("tick/ncv/k4s");
    expect(handle.base_sha).toBe("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2");
    expect(handle.launched).toBe(true);
    expect(handle.process_id).not.toBeNull();

    const sandbox = binding.named("run-x-k4s-3");
    // The probe ran FIRST: the green-start trap is the executor's too, not a
    // wave-only decoration.
    expect(sandbox.processes[0]?.command).toContain("--probe");
    const work = sandbox.workProcess();
    expect(work).toBeDefined();
    expect(handle.process_id).toBe(work?.id);
  });

  it("starts the work with the boot inputs the seam composed", async () => {
    const { binding, executor } = makeExecutor();
    await executor.start(SPEC);
    const work = binding.named("run-x-k4s-3").workProcess();
    expect(work?.env.TICKS_REPO_URL).toBe("https://example.com/pengelbrecht/ticfac.git");
    expect(work?.env.TICKS_BASE_SHA).toBe("a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2");
    expect(work?.env.TICKS_TICK).toBe("k4s");
    expect(work?.env.AI_GATEWAY_TOKEN).toBe("tkr_testtoken");
  });

  it("ADOPTS a live work process instead of dispatching a second one", async () => {
    const { binding, executor } = makeExecutor();
    const first = asHandle(await executor.start(SPEC));
    const before = binding.named("run-x-k4s-3").processes.length;

    // A Workflow step replay: the same spec, asked again.
    const second = asHandle(await executor.start(SPEC));

    expect(second.process_id).toBe(first.process_id);
    expect(second.launched).toBe(true);
    expect(second.detail).toContain("adopted");
    expect(binding.named("run-x-k4s-3").processes.length).toBe(before);
  });

  it("reports a failed green-start probe as not launched, never as absent", async () => {
    const { binding, collector } = makeExecutor();
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
      boot: async (spec) => bootInput(spec),
      spawn: { probe_timeout_ms: 10, probe_poll_ms: 1, sleep: async () => {} },
    });

    const handle = asHandle(await failing.start(SPEC));
    expect(handle.launched).toBe(false);
    expect(handle.detail).toContain("green-start trap");
  });
});

// ---------------------------------------------------------------- inspect ---

describe("inspect", () => {
  it("reports a running work process as running", async () => {
    const { executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    expect(await executor.inspect(handle)).toEqual({ state: "running" });
  });

  it("reports a terminal process as exited, with its exit code", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    binding.named("run-x-k4s-3").workProcess()?.finish(0);
    expect(await executor.inspect(handle)).toEqual({ state: "exited", exit_code: 0 });
  });

  it("falls back to the process list when the id answers nothing", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    const _sandbox = binding.named("run-x-k4s-3");
    // The recorded id goes stale — the container forgot it. The list still
    // answers, and "no id" must not read as "nothing is running".
    const stale = handle.process_id;
    handle.process_id = "gone-pid";
    expect(await executor.inspect(handle)).toEqual({ state: "running" });
    handle.process_id = stale;
  });

  it("says gone when neither the id nor the list can answer", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    binding.named("run-x-k4s-3").workProcess()?.finish(11);
    expect(await executor.inspect(handle)).toEqual({ state: "exited", exit_code: 11 });
    handle.process_id = "gone-pid";
    expect(await executor.inspect(handle)).toEqual({ state: "gone" });
  });
});

// ---------------------------------------------------------------- collect ---

describe("collect", () => {
  it("reads only the durable layer, through the collector the handle names", async () => {
    const { collector, executor } = makeExecutor();
    const handle = await executor.start(SPEC);
    const report = await executor.collect(handle);
    expect(collector.asked).toEqual([
      {
        tick_id: "k4s",
        branch: "tick/ncv/k4s",
        base_sha: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
      },
    ]);
    expect(report.outcome).toBe("done");
    expect(report.commits).toBe(2);
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
    branch: "tick/ncv/k4s",
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
    handle.process_id = null;
    await executor.cancel(handle);
    expect(binding.named("run-x-k4s-3").destroyed).toBe(true);
  });
});

// ------------------------------------------------------- the whole executor ---

describe("the four operations, end to end", () => {
  it("a dispatch a reconciler would settle: start, watch it finish, collect", async () => {
    const { binding, executor } = makeExecutor();
    const handle = asHandle(await executor.start(SPEC));
    binding.named("run-x-k4s-3").workProcess()?.finish(0);
    const status = await executor.inspect(handle);
    expect(status).toEqual({ state: "exited", exit_code: 0 });
    const report = await executor.collect(handle);
    expect(report.outcome).toBe("done");
    expect(report.detail).toContain("ready-to-merge");
    const record = handle as unknown as Record<string, unknown>;
    // The handle is a local attempt's shape, not a cloud-only one.
    expect(record.executor).toBe("cloudflare-sandbox");
    expect(record.resumed_from).toBeNull();
    expect(record.remote).toBe("origin");
    expect(JSON.stringify(handle)).not.toContain("tkr_testtoken");
  });
});
