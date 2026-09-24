import { describe, expect, it } from "vitest";

import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import type { WorkerTask } from "../src/worker-collect";
import {
  type Canceller,
  checkLiveness,
  confirmDispatch,
  DEFAULT_SALVAGE_GRACE_MS,
  evaluateProbeOutput,
  type SalvageSpec,
  type Sleeper,
  salvageWorker,
  spawnWorker,
  teardownWorker,
  type WaveCancellation,
  type WorkerLogSink,
  type WorkSpec,
} from "../src/worker-dispatch";

/**
 * Per-tick worker sandboxes (tick 0ds): the three spawn-time defences —
 * green-start trap, confirmed dispatch, expiring liveness — plus concurrent
 * fan-out and a collect seam that structurally cannot read a sandbox.
 *
 * These are the functions the per-tick sandbox dispatch door's executor is
 * built from; the wave fan-out that also lived here is deleted (tick l6t), and
 * so are its tests. They drive the pure dispatch functions directly against a
 * fake `SandboxBinding`, the same seam `run-workflow.test.ts` uses for the
 * orchestrator sandbox — nothing here starts a real container.
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

  say(text: string): void {
    this.output += text;
  }

  finish(code: number): void {
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
  killed: string[] = [];
  /** The container died and came back empty. */
  vanished = false;
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

  /**
   * Runs just before `getProcess` answers. A real container can print on its
   * way out — after the supervisor's last read and before the state it reads
   * says the process is over — and this is the only way a fake can reproduce
   * that ordering (tick 0fg).
   */
  beforeGetProcess: (() => void) | null = null;

  async getProcess(id: string): Promise<SandboxProcessView | null> {
    this.beforeGetProcess?.();
    if (this.vanished) return null;
    const process = this.processes.find((p) => p.id === id);
    return process === undefined ? null : process.view;
  }

  /** The live sandbox list — the reconcile protocol's third source (tick s7f). */
  async listProcesses(): Promise<SandboxProcessView[]> {
    if (this.vanished) return [];
    return this.processes.map((p) => ({ ...p.view, command: p.command }));
  }

  async readOutput(id: string, offset: number): Promise<SandboxOutput> {
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined || this.vanished) return { text: "", offset };
    return { text: process.output.slice(offset), offset: process.output.length };
  }

  async killProcess(id: string): Promise<void> {
    this.killed.push(id);
    const process = this.processes.find((p) => p.id === id);
    if (process === undefined) return;
    process.killed = true;
    process.state = "failed";
    process.exit_code = 143;
  }

  async destroy(): Promise<void> {
    this.destroyed = true;
  }

  /** The most recently started process. */
  get current(): FakeProcess {
    const process = this.processes.at(-1);
    if (process === undefined) throw new Error(`sandbox ${this.name} started nothing`);
    return process;
  }
}

class FakeSandboxes implements SandboxBinding {
  readonly booted: FakeSandbox[] = [];
  readonly bootOrder: string[] = [];
  /**
   * Every address, in order (tick k24). `binding.get` is called once by spawn,
   * once per wait poll and once by teardown, so a test can tell which stage of
   * the cycle a worker is in without guessing at timing.
   */
  readonly gets: string[] = [];
  readonly #byName = new Map<string, FakeSandbox>();

  async get(name: string): Promise<OrchestratorSandbox> {
    this.gets.push(name);
    let sandbox = this.#byName.get(name);
    if (sandbox === undefined) {
      sandbox = new FakeSandbox(name);
      this.#byName.set(name, sandbox);
      this.booted.push(sandbox);
      this.bootOrder.push(name);
    }
    return sandbox;
  }

  named(name: string): FakeSandbox {
    const sandbox = this.#byName.get(name);
    if (sandbox === undefined) throw new Error(`no sandbox named ${name} was booted`);
    return sandbox;
  }

  /** Like `named`, but for a caller that wants to know a sandbox was never addressed. */
  find(name: string): FakeSandbox | undefined {
    return this.#byName.get(name);
  }
}

/** The sandbox name a test addresses one worker by — any distinct name. */
function workerSandboxName(runID: string, tickID: string): string {
  return `${runID}-${tickID}`;
}

const PROBE_SPEC = { command: "tk --version", expect: "READY" };
const WORK_SPEC: WorkSpec = {
  probe: PROBE_SPEC,
  command: "ticks-worker",
  env: { TICKS_TICK_ID: "0ds" },
};

function task(tickID: string): WorkerTask {
  return { tick_id: tickID, branch: `tick/1vn/${tickID}`, base_sha: "a".repeat(40) };
}

/** A sleeper that never waits — the polling loops still poll, instantly. */
const noWait: Sleeper = async () => {};

/** The door process a container was asked through, if it was asked at all. */
function doorIn(sandbox: FakeSandbox): FakeProcess | undefined {
  return sandbox.processes.find((p) => p.command.startsWith(SALVAGE_SPEC.command));
}

const SALVAGE_SPEC: SalvageSpec = {
  command: "/usr/local/bin/ticks-worker --cancel",
  env: { TICKS_TICK_ID: "0ds" },
  marker: "ticks-worker-cancel-requested",
};

// ------------------------------------------------------------ probe evaluation ---

describe("evaluateProbeOutput", () => {
  it("passes only when the exact expected content is present", () => {
    expect(evaluateProbeOutput("boot ok\nREADY\n", "READY", 0).ok).toBe(true);
  });

  // The exit code is deliberately not the criterion: an `npx` probe for a
  // missing tool prints npm's own version and exits 0 (.tick/learnings.md,
  // "Cross-language parity, parsers and formats").
  it("fails on wrong content even when the exit code is 0", () => {
    const outcome = evaluateProbeOutput("8.19.2\n", "READY", 0);
    expect(outcome.ok).toBe(false);
    expect(outcome.ok === false && outcome.reason).toBe("wrong-output");
  });

  it("distinguishes silent (no output) from wrong-content", () => {
    const outcome = evaluateProbeOutput("", "READY", 0);
    expect(outcome.ok).toBe(false);
    expect(outcome.ok === false && outcome.reason).toBe("no-output");
  });
});

// ------------------------------------------------------------- the green-start trap ---

describe("spawnWorker: the green-start trap", () => {
  // Every test here drives the fake sandbox from INSIDE the injected `sleep`
  // callback rather than mutating it right after calling `spawnWorker` —
  // `spawnWorker` is async and suspends at its first `await` before the
  // probe process even exists, so mutating "after the call" races it. The
  // sleep callback is the one point guaranteed to run only once the probe
  // (or the real command) is genuinely up and being polled.

  it("catches a container that starts cleanly and prints nothing — never counted as launched", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    // The probe finishes immediately with empty output — a container that
    // came up clean and did nothing.
    const sleep: Sleeper = async () => binding.named(name).current.finish(0);

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep,
    });

    expect(result.launched).toBe(false);
    expect(result.probe.ok).toBe(false);
    expect(result.process_id).toBeNull();
    // The real work command was never started: only the probe ran.
    expect(binding.named(name).processes).toHaveLength(1);
    expect(binding.named(name).processes[0]!.command).toBe(PROBE_SPEC.command);
  });

  it("catches the npx-returns-npm-version class: wrong content, exit 0", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    const sleep: Sleeper = async () => {
      const probe = binding.named(name).current;
      probe.say("8.19.2\n");
      probe.finish(0);
    };

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep,
    });

    expect(result.launched).toBe(false);
    expect(result.probe.ok === false && result.probe.reason).toBe("wrong-output");
    expect(binding.named(name).processes).toHaveLength(1);
  });

  it("persists a failed probe's output through the recorder — it is the only account of what the container printed (tick ys3)", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    const sleep: Sleeper = async () => {
      const probe = binding.named(name).current;
      probe.say("8.19.2\n");
      probe.finish(0);
    };
    const recorded: unknown[] = [];

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep,
      record: {
        async dispatched() {},
        async started() {},
        async probeFailed(t, sandboxName, probe) {
          recorded.push({ tick_id: t.tick_id, sandboxName, probe });
        },
      },
    });

    expect(result.launched).toBe(false);
    expect(recorded).toEqual([
      {
        tick_id: "0ds",
        sandboxName: name,
        probe: {
          ok: false,
          reason: "wrong-output",
          detail: expect.stringContaining("8.19.2"),
          output: "8.19.2\n",
        },
      },
    ]);
  });

  it("an SDK error during the probe is captured as a ProbeOutcome, not lost to an uncaught rejection (tick ys3)", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    // The real Sandbox SDK's process calls are network round trips and can
    // reject outright, not only answer with the wrong content — a Durable
    // Object hiccup, a container refusing a request mid-boot. Patching the
    // booted sandbox (rather than the `sleep` callback) throws on the very
    // first poll, before timing can matter.
    const sandbox = await binding.get(name);
    sandbox.getProcess = async () => {
      throw new Error("container did not respond: 503");
    };
    const recorded: unknown[] = [];

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep: noWait,
      record: {
        async dispatched() {},
        async started() {},
        async probeFailed(t, sandboxName, probe) {
          recorded.push({ tick_id: t.tick_id, sandboxName, probe });
        },
      },
    });

    expect(result.launched).toBe(false);
    expect(result.process_id).toBeNull();
    expect(result.probe.ok).toBe(false);
    expect(result.probe.ok === false && result.probe.reason).toBe("probe-error");
    expect(result.probe.ok === false && result.probe.detail).toContain(
      "container did not respond: 503",
    );
    // Nothing was lost: the recorder still gets to persist this attempt's
    // account of itself, exactly as it would for a wrong-output probe.
    expect(recorded).toEqual([
      {
        tick_id: "0ds",
        sandboxName: name,
        probe: {
          ok: false,
          reason: "probe-error",
          detail: expect.stringContaining("container did not respond: 503"),
          output: "",
        },
      },
    ]);
  });

  it("never calls probeFailed when the probe passes", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    const sleep: Sleeper = async () => {
      for (const sandbox of binding.booted) {
        const process = sandbox.processes.at(-1);
        if (process === undefined || process.state !== "running") continue;
        process.say(process.command === PROBE_SPEC.command ? "READY\n" : "working\n");
        process.finish(0);
      }
    };
    let called = false;

    await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 5_000,
      confirm_poll_ms: 1,
      sleep,
      record: {
        async dispatched() {},
        async started() {},
        async probeFailed() {
          called = true;
        },
      },
    });

    expect(called).toBe(false);
  });

  it("a probe that produced output but never finished is a timeout", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    // Something came back, so the container is up — it is the probe that stalled.
    const sleep: Sleeper = async () => {
      binding.named(name).processes[0]?.say("working");
    };

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 20,
      probe_poll_ms: 5,
      sleep,
    });

    expect(result.launched).toBe(false);
    expect(result.probe.ok === false && result.probe.reason).toBe("timeout");
    expect(binding.named(name).processes).toHaveLength(1);
  });

  it("a container that produced nothing at all is a boot timeout, not a green start (tick 7go)", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    // No sleeper override: this genuinely waits ~20ms of wall clock, bounded
    // and short enough to keep the suite fast.
    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 20,
      probe_poll_ms: 5,
    });

    expect(result.launched).toBe(false);
    // The distinction the first real wave paid for: healthy containers
    // were reported as having started cleanly and done nothing, when they were
    // still pulling a 1.1 GB image. Silence is "never got there", not "failed".
    expect(result.probe.ok === false && result.probe.reason).toBe("boot-timeout");
    expect(result.probe.ok === false && result.probe.detail).toMatch(/never reached its probe/);
    expect(binding.named(name).processes).toHaveLength(1);
  });

  it("a container that vanishes mid-probe is caught, not treated as still booting", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    const sleep: Sleeper = async () => {
      binding.named(name).vanished = true;
    };

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep,
    });

    expect(result.launched).toBe(false);
    expect(result.probe.ok === false && result.probe.reason).toBe("process-gone");
  });

  it("a passing probe lets the real command start", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    let sleeps = 0;
    const sleep: Sleeper = async () => {
      sleeps += 1;
      const sandbox = binding.named(name);
      if (sleeps === 1) {
        // First poll: the probe is still running. Make it pass.
        sandbox.processes[0]!.say("boot ok\nREADY\n");
        sandbox.processes[0]!.finish(0);
        return;
      }
      // Later polls belong to confirmDispatch, watching the real command.
      sandbox.processes[1]!.say("starting…\n");
    };

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 5_000,
      confirm_poll_ms: 1,
      sleep,
    });

    expect(result.launched).toBe(true);
    expect(result.process_id).toBe(binding.named(name).processes[1]!.id);
    expect(binding.named(name).processes).toHaveLength(2);
    expect(binding.named(name).processes[1]!.command).toBe(WORK_SPEC.command);
    expect(binding.named(name).processes[1]!.env.TICKS_TICK_ID).toBe("0ds");
    expect(result.confirm?.confirmed).toBe(true);
  });
});

// ----------------------------------------------------------- confirmed dispatch ---

describe("confirmDispatch", () => {
  it("confirms once the process is running AND has produced output", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });
    // First poll: running, no output yet. The injected sleep is what
    // simulates time passing — output appears as its side effect, exactly
    // as a real worker would only print something after doing some work.
    let polls = 0;
    const sleep: Sleeper = async () => {
      polls += 1;
      sandbox.current.say("cloning…\n");
    };

    const outcome = await confirmDispatch(sandbox, started.id, {
      timeoutMs: 5_000,
      pollMs: 1,
      sleep,
    });

    expect(outcome.confirmed).toBe(true);
    expect(polls).toBe(1);
  });

  it("running with no output is NOT confirmed — that is what a green-start trap on the real command looks like", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });
    // No sleeper override: genuinely waits out a short window with the
    // process stuck running and silent.
    const outcome = await confirmDispatch(sandbox, started.id, { timeoutMs: 20, pollMs: 5 });

    expect(outcome.confirmed).toBe(false);
    expect(outcome.detail).toContain("no evidence");
  });

  it("a worker that finishes before ever being observed running still counts as confirmed", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });
    sandbox.current.finish(0); // completed before the first look
    const outcome = await confirmDispatch(sandbox, started.id, {
      timeoutMs: 5_000,
      pollMs: 1,
      sleep: noWait,
    });

    expect(outcome.confirmed).toBe(true);
    expect(outcome.detail).toContain("terminal state");
  });

  it("a worker that vanishes before any evidence is observed is not confirmed", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });
    sandbox.vanished = true;
    const outcome = await confirmDispatch(sandbox, started.id, {
      timeoutMs: 5_000,
      pollMs: 1,
      sleep: noWait,
    });

    expect(outcome.confirmed).toBe(false);
    expect(outcome.detail).toContain("vanished");
  });

  it("an unconfirmed dispatch still reports launched: true — the durable layer decides, not this wait", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0ds");
    let sleeps = 0;
    const sleep: Sleeper = async (ms) => {
      sleeps += 1;
      if (sleeps === 1) {
        const probe = binding.named(name).current;
        probe.say("READY\n");
        probe.finish(0);
        return;
      }
      // From then on: the real work process just sits there running,
      // silent, for the whole (tiny) confirm window — a genuine delay so
      // Date.now() actually advances toward the confirm deadline, rather
      // than a resolved-instantly no-op that spins forever with a clock
      // that never moves.
      await scheduler.wait(ms);
    };

    const result = await spawnWorker(binding, name, task("0ds"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 20,
      confirm_poll_ms: 1,
      sleep,
    });

    expect(result.launched).toBe(true);
    expect(result.confirm?.confirmed).toBe(false);
    expect(result.detail).toContain("unconfirmed");
  });
});

// -------------------------------------------------------------- expiring liveness ---

describe("checkLiveness / teardownWorker: expiring liveness", () => {
  it("teardown reads liveness fresh, not from a stale observation — a container that died is detected before the kill", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });

    // At confirm time it was alive. Between then and teardown (a GitHub
    // round trip in the real flow) it dies on its own.
    const liveness = await checkLiveness(binding, "s1", started.id);
    expect(liveness.alive).toBe(true);
    sandbox.current.finish(1);

    const outcome = await teardownWorker(binding, "s1", started.id);
    expect(outcome.liveness?.alive).toBe(false);
    // Never killed: it was already dead by the time the fresh check ran.
    expect(sandbox.killed).toEqual([]);
    expect(sandbox.destroyed).toBe(true);
  });

  it("kills a genuinely still-alive process before destroying the container", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });

    const outcome = await teardownWorker(binding, "s1", started.id);
    expect(outcome.killed).toBe(true);
    expect(sandbox.killed).toEqual([started.id]);
    expect(sandbox.destroyed).toBe(true);
  });

  it("still destroys a sandbox that never launched real work (green-start trap path)", async () => {
    const binding = new FakeSandboxes();
    await binding.get("s1");
    const outcome = await teardownWorker(binding, "s1", null);

    expect(outcome.killed).toBe(false);
    expect(outcome.liveness).toBeNull();
    expect(outcome.destroyed).toBe(true);
  });

  it("a sandbox already gone reports not-alive rather than throwing", async () => {
    const binding = new FakeSandboxes();
    const sandbox = (await binding.get("s1")) as FakeSandbox;
    const started = await sandbox.startProcess("ticks-worker", { env: {} });
    sandbox.vanished = true;

    const liveness = await checkLiveness(binding, "s1", started.id);
    expect(liveness).toEqual({ alive: false, state: "gone" });
  });
});

// ------------------------------------------------------- the cancellation seam ---

/**
 * tick k24, kept for the seam that still carries it: `spawnWorker` accepts a
 * Canceller, and every polling loop in the spawn cycle looks for a stop on
 * the canceller's own cadence. Nothing in the surviving dispatch path
 * constructs one (the wave fan-out did, tick l6t deletes it) — the seam stays
 * because `spawnWorker`'s signature is the door's, and a caller may wire a
 * stop into it again without touching this module.
 *
 * Every case below drives the seam directly against the fake sandbox — no wall
 * clock, no timing assumptions, and no "wait and hope".
 */

/** A test-local latched canceller, standing in for whatever constructs one. */
function latched(cancellation: WaveCancellation): Canceller {
  return {
    pollMs: 0,
    reads: 0,
    cancelled: cancellation,
    check: async () => cancellation,
  };
}

const STOPPED: WaveCancellation = { reason: "stopped:hard", detail: "a hard stop stands" };

/** The cancellation run run_f7bd5a36 actually died of, verbatim (tick 7zk). */
const _BUDGET: WaveCancellation = {
  reason: "budget:cost",
  detail: "the cost budget is exhausted: $8.00 of $8.00",
};

describe("cancelling a worker mid-cycle", () => {
  it("stops a container during its probe, and never starts its real command", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    const cancel = latched(STOPPED);
    // The probe never finishes: without the seam this would poll until the
    // probe timeout, and a real operator would wait it out.
    const result = await spawnWorker(binding, name, task("aaa"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      sleep: noWait,
      cancel,
    });

    expect(result.launched).toBe(false);
    expect(result.cancelled).toEqual(STOPPED);
    expect(result.probe.ok === false && result.probe.reason).toBe("cancelled");
    // Only the probe ever ran: a cancelled dispatch does not start real work.
    expect(binding.named(name).processes).toHaveLength(1);
    expect(binding.named(name).processes[0]!.command).toBe(PROBE_SPEC.command);
  });

  it("stops a container whose dispatch is still unconfirmed, and reports it launched", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    // Latched after one read: the confirm loop's first poll sees a live run,
    // the next sees the stop.
    let looked = 0;
    const cancel: Canceller = {
      pollMs: 0,
      reads: 0,
      cancelled: null,
      check: async () => (++looked > 1 ? STOPPED : null),
    };
    let sleeps = 0;
    const sleep: Sleeper = async () => {
      sleeps += 1;
      if (sleeps === 1) {
        const p = binding.named(name).processes[0]!;
        p.say("READY\n");
        p.finish(0);
      }
      // The real command then produces nothing, so confirm keeps polling —
      // which is where the cancellation lands.
    };

    const result = await spawnWorker(binding, name, task("aaa"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 5_000,
      confirm_poll_ms: 1,
      sleep,
      cancel,
    });

    // The real command IS running in that container — which is exactly why the
    // caller has to be told, so it tears the thing down.
    expect(result.launched).toBe(true);
    expect(result.process_id).not.toBeNull();
    expect(result.cancelled).toEqual(STOPPED);
    expect(result.confirm?.confirmed).toBe(false);
  });
});

// ------------------------------------------ the container's own output ---

/**
 * Every worker container streams its stdout/stderr somewhere durable, the way
 * the orchestrator sandbox does — continuously, never at exit (tick 0fg).
 *
 * A container that dies must still leave its diagnostics: the exit-7 wave cost
 * seven paid runs because `worker.sh`'s `die` message, which names the exact
 * gateway route and HTTP body, went nowhere. Export-at-exit is the one design
 * that cannot answer that, because the thing being diagnosed is the exit.
 */
class FakeLogSink implements WorkerLogSink {
  readonly flushes: { tick_id: string; text: string }[] = [];

  forTick(tickID: string) {
    return async (text: string): Promise<void> => {
      this.flushes.push({ tick_id: tickID, text });
    };
  }

  text(tickID: string): string {
    return this.flushes
      .filter((flush) => flush.tick_id === tickID)
      .map((flush) => flush.text)
      .join("");
  }
}

describe("a worker container's own output is streamed while it runs", () => {
  it("flushes the probe's output as it appears, not once the probe is over", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0fg");
    const logs = new FakeLogSink();
    let look = 0;
    const sleep: Sleeper = async () => {
      const process = binding.named(name).current;
      look += 1;
      if (look === 1) {
        process.say("ticks-worker: resolving the model route\n");
        return;
      }
      if (look === 2) {
        // By the second look the first line must ALREADY be durable — a stream
        // that only lands at exit is no use to a container about to die.
        expect(logs.text("0fg")).toBe("ticks-worker: resolving the model route\n");
        process.say("READY\n");
        process.finish(0);
        return;
      }
      // The confirm loop, now watching the real work command.
      process.say("implementing 0fg\n");
      process.finish(0);
    };

    const result = await spawnWorker(binding, name, task("0fg"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 5_000,
      confirm_poll_ms: 1,
      sleep,
      logs,
    });

    expect(result.probe.ok).toBe(true);
    expect(logs.text("0fg")).toContain("READY");
  });

  it("keeps a sink failure out of the dispatch — telemetry cannot fail a dispatch", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "0fg");
    const sleep: Sleeper = async () => {
      const probe = binding.named(name).current;
      probe.say("READY\n");
      probe.finish(0);
    };

    const result = await spawnWorker(binding, name, task("0fg"), WORK_SPEC, {
      probe_timeout_ms: 5_000,
      probe_poll_ms: 1,
      confirm_timeout_ms: 5_000,
      confirm_poll_ms: 1,
      sleep,
      logs: {
        forTick() {
          return async () => {
            throw new Error("R2 is having a day");
          };
        },
      },
    });

    expect(result.probe.ok).toBe(true);
    expect(result.launched).toBe(true);
  });
});

// ------------------------------------------------------- the salvage window ---

/**
 * tick 7zk: a cancelled container is ASKED to stop and push before it is
 * destroyed.
 *
 * Run run_f7bd5a36 is the case. Three containers, all booted green, all
 * working, and the cost budget tripped at $8.00 of $8.00. The supervisor did
 * everything right — revoke first, then tear down, exactly the ordering tick
 * gyl argued for — and the run kept nothing: `no-commits`, `branch_exists:
 * false`, three times. The salvage that would have rescued them already
 * existed in `image/worker.sh` and already had live proof behind it
 * (tick 5fg, run 3, commit adfedff5); what it did not have was a door the
 * SUPERVISOR could knock on. These cases are that door.
 *
 * The tension is resolved by ORDER, not by trading one property for the other:
 * the credential is revoked before the window opens, so a container inside it
 * cannot make a model call at all — the only thing it can still do is finish a
 * git push.
 */
describe("salvageWorker: the grace window between revoke and destroy", () => {
  it("asks the container to stop and push, and waits for it to finish", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    const sandbox = (await binding.get(name)) as FakeSandbox;
    const work = await sandbox.startProcess("ticks-worker", { env: {} });

    // The container answers the door and then does what worker.sh does: it
    // commits what it has, writes its report and pushes.
    const sleep: Sleeper = async () => {
      const door = doorIn(sandbox);
      if (door === undefined) return;
      door.say("ticks-worker: ticks-worker-cancel-requested reason=budget:cost\n");
      const running = sandbox.processes.find((p) => p.id === work.id)!;
      if (running.state !== "running") return;
      running.say("ticks-worker: pushed tick/1vn/aaa to origin\n");
      running.finish(11);
    };

    const outcome = await salvageWorker(binding, name, work.id, SALVAGE_SPEC, {
      reason: "budget:cost",
      pollMs: 1,
      sleep,
    });

    expect(outcome.requested).toBe(true);
    expect(outcome.settled).toBe(true);
    expect(outcome.state).toBe("failed");
    // The reason travels INTO the container, so the branch it pushes says why
    // it stopped rather than looking like an agent that gave up.
    expect(doorIn(sandbox)?.command).toBe("/usr/local/bin/ticks-worker --cancel budget:cost");
  });

  // The window is BOUNDED, which is the other half of why it is safe: a
  // container that will not stop does not get to hold a cancelled run open.
  it("destroys nothing but stops waiting when the window closes", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    const sandbox = (await binding.get(name)) as FakeSandbox;
    const work = await sandbox.startProcess("ticks-worker", { env: {} });

    const outcome = await salvageWorker(binding, name, work.id, SALVAGE_SPEC, {
      reason: "budget:cost",
      // Zero is the bound under test: the window closes on its own terms and
      // the caller goes on to tear the container down.
      graceMs: 0,
      pollMs: 1,
      sleep: noWait,
    });

    expect(outcome.requested).toBe(true);
    expect(outcome.settled).toBe(false);
    expect(outcome.state).toBe("running");
    expect(outcome.detail).toContain("lost");
    // The window does not destroy anything: that is `teardownWorker`'s job and
    // it still runs after this.
    expect(sandbox.destroyed).toBe(false);
  });

  // A container that already finished lost nothing, and asking it to stop
  // would start a process in a container about to be destroyed for no reason.
  it("asks nothing of a container that was already over", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    const sandbox = (await binding.get(name)) as FakeSandbox;
    const work = await sandbox.startProcess("ticks-worker", { env: {} });
    sandbox.current.finish(0);

    const outcome = await salvageWorker(binding, name, work.id, SALVAGE_SPEC, {
      reason: "budget:cost",
      sleep: noWait,
    });

    expect(outcome.requested).toBe(false);
    expect(outcome.settled).toBe(true);
    expect(doorIn(sandbox)).toBeUndefined();
  });

  // A container booted without a door behaves exactly as it did before this
  // tick: nothing is started in it, and the caller destroys it.
  it("asks nothing of a container with no salvage door", async () => {
    const binding = new FakeSandboxes();
    const name = workerSandboxName("run1", "aaa");
    const sandbox = (await binding.get(name)) as FakeSandbox;
    const work = await sandbox.startProcess("ticks-worker", { env: {} });

    const outcome = await salvageWorker(binding, name, work.id, undefined, { sleep: noWait });

    expect(outcome.requested).toBe(false);
    expect(sandbox.processes).toHaveLength(1);
  });

  // The window is sized for a commit and a push, not for a tick.
  it("is bounded by a window sized for a push, not for work", () => {
    expect(DEFAULT_SALVAGE_GRACE_MS).toBeGreaterThan(0);
    expect(DEFAULT_SALVAGE_GRACE_MS).toBeLessThanOrEqual(60_000);
  });
});
