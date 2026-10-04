import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import {
  BOOT_ENTRYPOINT,
  DEFAULT_INSTANCE,
  type DoContainer,
  type DoExecProcess,
  FACTORY_IMAGE_NAME,
  FactorySandboxCore,
  type FactorySandboxNamespace,
  type FactorySandboxStub,
  factorySandboxBinding,
  factorySandboxBindingFromEnv,
  HEARTBEAT_MS,
  IDLE_TIMEOUT_MS,
  IMAGE_ENV_FILE,
  KEEPALIVE_TIMEOUT_MS,
  PROCESS_RUNNER,
  parseRunnerState,
  READ_CHUNK_BYTES,
  READY_TIMEOUT_MS,
  RUN_MAX_BYTES,
  runnerView,
  type SandboxState,
  START_WRAPPER,
  utf8Boundary,
} from "../src/factory-sandbox";

const encoder = new TextEncoder();
const IMAGE = "registry.cloudflare.com/acct/ticks-factory-sandbox-factory@sha256:aaaa";

type FakeProcess = { state: string; output: Uint8Array; killed: number };

/** ticks-proc's `status` answers. */
const statusRunning = (pid: number) => `state=running\npid=${pid}\n`;
const statusExited = (code: number) => `state=exited\npid=42\nexit_code=${code}\n`;

/**
 * A stand-in for `ctx.container` that runs the process runner's verbs the way
 * the image's `ticfac-proc` answers them: `inspect` prints one state line,
 * `read` prints raw bytes from an offset, `list` prints one id per line.
 *
 * `bash` answers the `run` door's short commands: the fake cannot run bash,
 * so `options.bash` is the test's stand-in for what the container's bash
 * would print and exit with, keyed on the command line it was handed.
 */
function fakeContainer(
  options: {
    running?: boolean;
    images?: Record<string, string>;
    notReadyFor?: number;
    bash?: (line: string, env?: Record<string, string>) => { stdout: string; exitCode?: number };
  } = {},
) {
  let notReadyFor = options.notReadyFor ?? 0;
  const processes = new Map<string, FakeProcess>();
  const starts: Parameters<DoContainer["start"]>[0][] = [];
  const timeouts: number[] = [];
  const execs: { argv: string[]; env?: Record<string, string>; cwd?: string }[] = [];
  let destroyed = 0;
  let monitored = 0;
  let running = options.running ?? false;
  let startedImage = "";

  const answer = (stdout: Uint8Array | string, exitCode = 0): DoExecProcess => {
    const bytes = typeof stdout === "string" ? encoder.encode(stdout) : stdout;
    return {
      pid: 1,
      exitCode: Promise.resolve(exitCode),
      output: async () => ({
        stdout: bytes.slice().buffer as ArrayBuffer,
        stderr: new ArrayBuffer(0),
        exitCode,
      }),
    };
  };

  const container: DoContainer = {
    get running() {
      return running;
    },
    images: options.images ?? { [FACTORY_IMAGE_NAME]: IMAGE },
    start(opts) {
      if (running) throw new Error("already running");
      starts.push(opts);
      startedImage = opts.image;
      running = true;
    },
    async exec(argv, opts) {
      if (!running) throw new Error("container is not running");
      execs.push({
        argv,
        ...(opts?.env ? { env: opts.env } : {}),
        ...(opts?.cwd ? { cwd: opts.cwd } : {}),
      });
      if (argv[0] === "test") {
        // The readiness check: refused while the container is "starting".
        if (notReadyFor > 0) {
          notReadyFor -= 1;
          throw new Error("Command `test` was not found in the container.");
        }
        return answer("");
      }
      // A bounded read is the runner's read piped through head -c.
      if (argv[0] === "sh" && argv[2]?.startsWith(`${PROCESS_RUNNER} read `)) {
        const [id, offset, max] = argv.slice(4) as [string, string, string];
        const p = processes.get(id);
        if (p === undefined) return answer("", 3);
        return answer(p.output.subarray(Number(offset), Number(offset) + Number(max)));
      }
      // A start goes through the environment wrapper; the runner line follows it.
      const line = argv[0] === "sh" && argv[2] === START_WRAPPER ? argv.slice(4) : argv;
      if (line[0] === "bash") {
        // The run door: one short command, wrapped for bounding. The test's
        // stand-in answers with what the container's bash would have.
        const out = options.bash?.(line[2] ?? "", opts?.env);
        return out === undefined ? answer("", 127) : answer(out.stdout, out.exitCode ?? 0);
      }
      if (line[0] !== PROCESS_RUNNER) return answer("", 127);
      const [, verb, id] = line;
      switch (verb) {
        case "start": {
          if (processes.has(id!)) return answer("", 4);
          processes.set(id!, { state: statusRunning(42), output: new Uint8Array(), killed: 0 });
          return answer("");
        }
        case "status": {
          const p = processes.get(id!);
          return p === undefined ? answer("", 3) : answer(p.state);
        }
        case "kill": {
          const p = processes.get(id!);
          if (p !== undefined) {
            p.killed += 1;
            p.state = statusExited(143);
          }
          return answer("");
        }
        case "list":
          return answer([...processes.keys()].map((k) => `${k}\n`).join(""));
        default:
          return answer("", 2);
      }
    },
    monitor: () => {
      monitored += 1;
      return new Promise<void>(() => {});
    },
    async destroy() {
      destroyed += 1;
      running = false;
      processes.clear();
    },
    async setInactivityTimeout(ms) {
      timeouts.push(ms);
    },
    async inspect() {
      return running ? { image: startedImage, labels: {} } : null;
    },
  };

  return {
    container,
    processes,
    starts,
    timeouts,
    execs,
    get destroyed() {
      return destroyed;
    },
    get monitored() {
      return monitored;
    },
    stop() {
      running = false;
      processes.clear();
    },
    write(id: string, text: string | Uint8Array) {
      const p = processes.get(id)!;
      const add = typeof text === "string" ? encoder.encode(text) : text;
      const next = new Uint8Array(p.output.length + add.length);
      next.set(p.output);
      next.set(add, p.output.length);
      p.output = next;
    },
    exit(id: string, code: number) {
      processes.get(id)!.state = statusExited(code);
    },
  };
}

/** A Durable Object state with an in-memory storage and alarm. */
function fakeState(container: DoContainer | undefined) {
  const store = new Map<string, unknown>();
  let alarm: number | null = null;
  const blocked: Promise<unknown>[] = [];
  const storage = {
    async get<T>(key: string) {
      return store.get(key) as T | undefined;
    },
    async put(key: string, value: unknown) {
      store.set(key, value);
    },
    async delete(key: string) {
      return store.delete(key);
    },
    async setAlarm(at: number | Date) {
      alarm = typeof at === "number" ? at : at.getTime();
    },
    async deleteAlarm() {
      alarm = null;
    },
    async getAlarm() {
      return alarm;
    },
    async list<T>(options: { prefix?: string; limit?: number } = {}) {
      const found = new Map<string, T>();
      for (const [k, v] of store) {
        if (options.prefix !== undefined && !k.startsWith(options.prefix)) continue;
        if (options.limit !== undefined && found.size >= options.limit) break;
        found.set(k, v as T);
      }
      return found;
    },
  };
  const state = {
    container,
    storage,
    blockConcurrencyWhile<T>(fn: () => Promise<T>) {
      const p = fn();
      blocked.push(p);
      return p;
    },
    waitUntil(_p: Promise<unknown>) {},
  };
  return {
    state: state as unknown as SandboxState,
    store,
    get alarm() {
      return alarm;
    },
    settled: () => Promise.all(blocked),
  };
}

function sandbox(c: ReturnType<typeof fakeContainer>) {
  const s = fakeState(c.container);
  return { object: new FactorySandboxCore(s.state), state: s };
}

describe("FactorySandbox: starting a process", () => {
  it("boots the container on the deployment's named image, sized and online, idling under the entrypoint", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);

    const view = await object.startProcess("ticks-worker", { TICKS_RUN_ID: "run_1" });

    expect(c.starts).toEqual([
      {
        image: IMAGE,
        instance: DEFAULT_INSTANCE,
        enableInternet: true,
        entrypoint: BOOT_ENTRYPOINT,
      },
    ]);
    expect(view).toMatchObject({ state: "running", exit_code: null, command: "ticks-worker" });
    // The process's own variables reach it; the image's are sourced from the
    // file the entrypoint wrote, under them.
    const start = c.execs.find((e) => e.argv.includes("start"))!;
    expect(start.env).toEqual({ TICKS_RUN_ID: "run_1" });
    expect(start.argv.slice(0, 4)).toEqual(["sh", "-c", START_WRAPPER, IMAGE_ENV_FILE]);
    expect(start.argv.slice(-3)).toEqual(["bash", "-c", "exec 2>&1; ticks-worker"]);
    expect(start.cwd).toBe("/workspace");
  });

  it("starts on a run's pinned image and instance size when the boot names them", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);

    await object.startProcess(
      "x",
      {},
      {
        pinnedImage: "registry.cloudflare.com/acct/old@sha256:bbbb",
        instance: { vcpu: 4, memoryMib: 12288, diskMb: 40000 },
      },
    );

    expect(c.starts[0]).toMatchObject({
      image: "registry.cloudflare.com/acct/old@sha256:bbbb",
      instance: { vcpu: 4, memoryMib: 12288, diskMb: 40000 },
    });
  });

  it("starts a second process in the running container instead of booting again", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);

    const a = await object.startProcess("probe", {});
    const b = await object.startProcess("work", {});

    expect(c.starts).toHaveLength(1);
    expect(a.id).not.toBe(b.id);
  });

  it("never waits for a cold container: the process is pending until the alarm starts it", async () => {
    // umq proof run run_6b9f…: a Workflow boot step (5 minutes) waited inline
    // for a cold image pull and timed out. The start must return at once.
    const c = fakeContainer({ notReadyFor: 3 });
    const s = fakeState(c.container);
    const object = new FactorySandboxCore(s.state);

    const view = await object.startProcess("ticks-orchestrator", { TOKEN: "t" });

    expect(view).toMatchObject({
      state: "running",
      exit_code: null,
      command: "ticks-orchestrator",
    });
    expect(c.processes.size).toBe(0);
    expect(s.alarm).not.toBeNull();
    expect(await object.getProcess(view.id)).toMatchObject({ state: "running" });
    expect(await object.readOutput(view.id, 0)).toEqual({ text: "", offset: 0 });
    expect((await object.listProcesses()).map((p) => p.id)).toEqual([view.id]);

    await object.alarm(); // still not answering
    await object.alarm(); // still not answering
    expect(c.processes.size).toBe(0);
    await object.alarm(); // answers: started with its own env, and the env is dropped
    expect(c.processes.has(view.id)).toBe(true);
    expect(c.execs.find((e) => e.argv.includes("start"))?.env).toEqual({ TOKEN: "t" });
    expect([...s.store.keys()].some((k) => k.startsWith("pending:"))).toBe(false);
    expect(await object.getProcess(view.id)).toMatchObject({ state: "running" });

    // Ready once per instance: the next start goes straight to the runner.
    const before = c.execs.length;
    await object.startProcess("again", {});
    expect(c.execs.slice(before).some((e) => e.argv[0] === "test")).toBe(false);
  });

  it("fails a pending process with the reason, and destroys a container that never answers", async () => {
    const c = fakeContainer({ notReadyFor: 1_000_000 });
    const s = fakeState(c.container);
    let clock = 1_000;
    const object = new FactorySandboxCore(s.state, () => clock);
    const { id } = await object.startProcess("ticks-orchestrator", {});

    await object.alarm();
    expect(await object.getProcess(id)).toMatchObject({ state: "running" });
    clock += READY_TIMEOUT_MS;
    await object.alarm();

    expect(c.destroyed).toBe(1);
    expect(await object.getProcess(id)).toEqual({
      id,
      command: "ticks-orchestrator",
      state: "failed",
      exit_code: null,
    });
    const why = await object.readOutput(id, 0);
    expect(why.text).toMatch(/did not answer within 20 minutes of its start/);
    expect(await object.readOutput(id, why.offset)).toEqual({ text: "", offset: why.offset });
  });

  it("fails a pending process at once when its container stops while starting", async () => {
    const c = fakeContainer({ notReadyFor: 1_000_000 });
    const { object, state } = sandbox(c);
    const { id } = await object.startProcess("x", {});
    state.store.set("last_stop", { at: "t", how: "stopped: image pull failed" });

    c.stop();
    await object.alarm();

    expect(await object.getProcess(id)).toMatchObject({ state: "failed", exit_code: null });
    expect((await object.readOutput(id, 0)).text).toMatch(
      /stopped before it answered.*image pull failed/,
    );
  });

  it("re-arms a pending process's alarm after a restart", async () => {
    const c = fakeContainer({ notReadyFor: 1_000_000 });
    const first = sandbox(c);
    await first.object.startProcess("x", {});
    const restarted = fakeState(c.container);
    for (const [k, v] of first.state.store) restarted.store.set(k, v);
    new FactorySandboxCore(restarted.state);
    await restarted.settled();
    expect(restarted.alarm).not.toBeNull();
  });

  it("refuses a boot with no image instead of starting an empty one", async () => {
    const c = fakeContainer({ images: {} });
    const { object } = sandbox(c);

    await expect(object.startProcess("x", {})).rejects.toThrow(/no image to start/);
    expect(c.starts).toEqual([]);
  });

  it("says the runner refused when the runner refuses", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const view = await object.startProcess("x", {});
    // Same id twice: the runner refuses a taken id (exit 4).
    const origRandom = crypto.randomUUID;
    crypto.randomUUID = () => view.id as `${string}-${string}-${string}-${string}-${string}`;
    try {
      await expect(object.startProcess("y", {})).rejects.toThrow(/refused to start/);
    } finally {
      crypto.randomUUID = origRandom;
    }
  });
});

describe("FactorySandbox: lifetime", () => {
  it("bounds an unwatched boot by the idle ceiling and sets no heartbeat", async () => {
    const c = fakeContainer();
    const { object, state } = sandbox(c);

    await object.startProcess("probe", {});

    expect(c.timeouts).toEqual([IDLE_TIMEOUT_MS]);
    expect(state.alarm).toBeNull();
    // Watched from its start, so a failed start leaves its reason.
    expect(c.monitored).toBe(1);
  });

  it("keeps a keepAlive boot alive with an alarm heartbeat, and watches it", async () => {
    const c = fakeContainer();
    const { object, state } = sandbox(c);

    await object.startProcess("ticks-orchestrator", {}, { keepAlive: true });

    expect(c.timeouts).toEqual([KEEPALIVE_TIMEOUT_MS]);
    expect(state.alarm).not.toBeNull();
    expect(c.monitored).toBe(1);

    await object.alarm();
    expect(c.timeouts).toEqual([KEEPALIVE_TIMEOUT_MS, KEEPALIVE_TIMEOUT_MS]);
    expect(state.alarm! - Date.now()).toBeLessThanOrEqual(HEARTBEAT_MS);
  });

  it("moves a probed container to keepAlive when its work starts (the worker's two boots)", async () => {
    const c = fakeContainer();
    const { object, state } = sandbox(c);

    await object.startProcess("ticks-worker --probe", {});
    await object.startProcess("ticks-worker", {}, { keepAlive: true });

    expect(c.timeouts).toEqual([IDLE_TIMEOUT_MS, KEEPALIVE_TIMEOUT_MS]);
    expect(state.alarm).not.toBeNull();
  });

  it("stops its heartbeat once the container is gone", async () => {
    const c = fakeContainer();
    const { object, state } = sandbox(c);
    await object.startProcess("x", {}, { keepAlive: true });

    c.stop();
    state.store.set("keep_alive", true);
    await object.alarm();

    expect(state.store.has("keep_alive")).toBe(false);
  });

  it("re-arms the inactivity timeout in its constructor, because a deploy restarts every object", async () => {
    const c = fakeContainer();
    const first = sandbox(c);
    await first.object.startProcess("ticks-orchestrator", {}, { keepAlive: true });
    c.timeouts.length = 0;

    // A deploy: the object is constructed again over the same storage and the
    // same still-running container.
    const restarted = fakeState(c.container);
    for (const [k, v] of first.state.store) restarted.store.set(k, v);
    new FactorySandboxCore(restarted.state);
    await restarted.settled();

    expect(c.timeouts).toEqual([KEEPALIVE_TIMEOUT_MS]);
    expect(restarted.alarm).not.toBeNull();
    expect(c.monitored).toBe(2);
  });

  it("re-arms an unwatched boot with the idle ceiling, and does not start one", async () => {
    const c = fakeContainer({ running: true });
    const s = fakeState(c.container);
    new FactorySandboxCore(s.state);
    await s.settled();

    expect(c.timeouts).toEqual([IDLE_TIMEOUT_MS]);
    expect(c.starts).toEqual([]);
    expect(s.alarm).toBeNull();
  });

  it("destroys the container and its heartbeat", async () => {
    const c = fakeContainer();
    const { object, state } = sandbox(c);
    await object.startProcess("x", {}, { keepAlive: true });

    await object.destroy();

    expect(c.destroyed).toBe(1);
    expect(state.alarm).toBeNull();
    expect(await object.isRunning()).toBe(false);
  });
});

describe("FactorySandbox: reading a process", () => {
  it("maps the runner's states onto the seam's", () => {
    expect(runnerView("p", parseRunnerState(statusRunning(7)), "c")).toEqual({
      id: "p",
      command: "c",
      state: "running",
      exit_code: null,
    });
    expect(runnerView("p", parseRunnerState("state=starting\npid=\n"), undefined)?.state).toBe(
      "running",
    );
    expect(runnerView("p", parseRunnerState(statusExited(0)), undefined)).toMatchObject({
      state: "completed",
      exit_code: 0,
    });
    expect(runnerView("p", parseRunnerState(statusExited(3)), undefined)).toMatchObject({
      state: "failed",
      exit_code: 3,
    });
    expect(runnerView("p", parseRunnerState("state=lost\npid=9\n"), undefined)).toMatchObject({
      state: "failed",
      exit_code: null,
    });
    expect(runnerView("p", parseRunnerState(""), undefined)).toBeNull();
    expect(runnerView("p", parseRunnerState("garbage"), undefined)).toBeNull();
  });

  it("reports the exit code once the process ends, and keeps the record", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const { id } = await object.startProcess("ticks-orchestrator", {});

    c.exit(id, 3);

    expect(await object.getProcess(id)).toEqual({
      id,
      command: "ticks-orchestrator",
      state: "failed",
      exit_code: 3,
    });
  });

  it("lists the live processes with the commands they were started with", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const probe = await object.startProcess("ticks-worker --probe", {});
    const work = await object.startProcess("ticks-worker", {});
    c.exit(probe.id, 0);

    const listed = await object.listProcesses();

    expect(listed).toEqual(
      expect.arrayContaining([
        { id: probe.id, command: "ticks-worker --probe", state: "completed", exit_code: 0 },
        { id: work.id, command: "ticks-worker", state: "running", exit_code: null },
      ]),
    );
  });

  it("never boots a container to answer a question", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);

    expect(await object.getProcess("p")).toBeNull();
    expect(await object.listProcesses()).toEqual([]);
    expect(await object.readOutput("p", 5)).toEqual({ text: "", offset: 5 });
    await object.killProcess("p");
    expect(await object.isRunning()).toBe(false);
    expect(await object.runningImage()).toBeNull();
    expect(c.starts).toEqual([]);
  });

  it("forgets every process when the container came back empty", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const { id } = await object.startProcess("x", {});

    c.stop();

    expect(await object.getProcess(id)).toBeNull();
  });

  it("reads output by byte cursor and resumes where it left off", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const { id } = await object.startProcess("x", {});

    c.write(id, "hello ");
    const first = await object.readOutput(id, 0);
    c.write(id, "wörld\n");
    const second = await object.readOutput(id, first.offset);
    const third = await object.readOutput(id, second.offset);

    expect(first).toEqual({ text: "hello ", offset: 6 });
    expect(second).toEqual({ text: "wörld\n", offset: 6 + encoder.encode("wörld\n").length });
    expect(third).toEqual({ text: "", offset: second.offset });
  });

  it("reads in bounded chunks and never splits a character across them", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const { id } = await object.startProcess("x", {});
    // A multi-byte character straddling the chunk boundary.
    const head = "a".repeat(READ_CHUNK_BYTES - 1);
    c.write(id, `${head}é tail`);

    const first = await object.readOutput(id, 0);
    const second = await object.readOutput(id, first.offset);

    expect(first.text).toBe(head);
    expect(first.offset).toBe(READ_CHUNK_BYTES - 1);
    expect(second.text).toBe("é tail");
  });

  it("finds UTF-8 boundaries", () => {
    const e = encoder.encode("aé€😀");
    expect(utf8Boundary(e)).toBe(e.length);
    expect(utf8Boundary(e.subarray(0, e.length - 1))).toBe(e.length - 4);
    expect(utf8Boundary(e.subarray(0, 2))).toBe(1);
    expect(utf8Boundary(new Uint8Array())).toBe(0);
  });

  it("kills through the runner", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);
    const { id } = await object.startProcess("x", {});

    await object.killProcess(id);

    expect(c.processes.get(id)?.killed).toBe(1);
    expect((await object.getProcess(id))?.exit_code).toBe(143);
  });
});

describe("FactorySandbox: images", () => {
  it("names the deployment's image for a run to pin, and the image a running container started on", async () => {
    const c = fakeContainer();
    const { object } = sandbox(c);

    expect(await object.imageRef()).toBe(IMAGE);
    await object.startProcess("x", {}, { pinnedImage: "registry.cloudflare.com/acct/r@sha256:cc" });
    expect(await object.runningImage()).toBe("registry.cloudflare.com/acct/r@sha256:cc");
  });

  it("has no image ref on a deployment without the named image", async () => {
    const { object } = sandbox(fakeContainer({ images: {} }));
    expect(await object.imageRef()).toBeNull();
  });
});

describe("factorySandboxBinding", () => {
  function recordingNamespace() {
    const calls: { name: string; method: string; args: unknown[] }[] = [];
    const namespace: FactorySandboxNamespace = {
      idFromName: (name) => ({ name }) as unknown as DurableObjectId,
      get(id) {
        const name = (id as unknown as { name: string }).name;
        const record =
          (method: string) =>
          async (...args: unknown[]) => {
            calls.push({ name, method, args });
            return method === "startProcess"
              ? { id: "p1", state: "running", exit_code: null }
              : method === "listProcesses"
                ? []
                : method === "readOutput"
                  ? { text: "", offset: 0 }
                  : null;
          };
        return new Proxy({}, { get: (_t, m) => record(String(m)) }) as FactorySandboxStub;
      },
    };
    return { namespace, calls };
  }

  it("addresses the object by name and carries the boot's lifetime, size and pin to the start", async () => {
    const { namespace, calls } = recordingNamespace();
    const binding = factorySandboxBinding(namespace);

    const s = await binding.get("run_1-1", {
      keepAlive: true,
      image: "docker.io/somebody/else:1",
      instance: "standard-4",
      pinnedImage: IMAGE,
    } as Parameters<typeof binding.get>[1]);
    await s.startProcess("ticks-orchestrator", { env: { A: "1" } });

    expect(calls).toEqual([
      {
        name: "run_1-1",
        method: "startProcess",
        args: [
          "ticks-orchestrator",
          { A: "1" },
          { keepAlive: true, instance: "standard-4", pinnedImage: IMAGE },
        ],
      },
    ]);
  });

  it("does nothing at get: no question is asked until a method is called", async () => {
    const { namespace, calls } = recordingNamespace();
    await factorySandboxBinding(namespace).get("n");
    expect(calls).toEqual([]);
  });

  it("is null when the deployment has no SANDBOXES_V1, and the seam as given otherwise", () => {
    expect(factorySandboxBindingFromEnv({ ...env, SANDBOXES_V1: undefined })).toBeNull();
    const fake = { get: async () => ({}) as never };
    expect(factorySandboxBindingFromEnv({ ...env, SANDBOXES_V1: fake })).toBe(fake);
  });

  it("is bound on this deployment's config", () => {
    expect(env.SANDBOXES_V1).toBeDefined();
  });
});

describe("FactorySandbox: the run door", () => {
  // One short command, started, waited for and read in one RPC (epic 43y
  // step 3): the harness package's FactorySandboxEnv runs every file
  // operation through this door, so its contract is asserted on the exact
  // command line the door hands the container — the bounding, the image
  // environment and the command's own exit code are all part of what the
  // env depends on.
  it("runs a short command through the image environment and returns its output and exit code", async () => {
    const seen: { line: string; env?: Record<string, string> }[] = [];
    const c = fakeContainer({
      bash: (line, env) => {
        seen.push({ line, env });
        return { stdout: "hello\n" };
      },
    });
    const { object } = sandbox(c);

    const out = await object.run('cat "$FILE"', { FILE: "result.md" });
    expect(out).toEqual({ ready: true, exitCode: 0, output: "hello\n", truncated: false });

    // The command reached the container through the environment wrapper, in
    // the working directory, with the caller's variables laid over the image
    // environment, and bounded: head -c keeps what this Durable Object
    // buffers finite, and PIPESTATUS keeps the command's own exit code.
    expect(seen).toEqual([
      {
        line: `exec 2>&1; { cat "$FILE"; } | head -c ${RUN_MAX_BYTES + 1}; exit "\${PIPESTATUS[0]}"`,
        env: { FILE: "result.md" },
      },
    ]);
    expect(c.execs.at(-1)?.cwd).toBe("/workspace");
  });

  it("returns the command's own exit code under the bounding pipeline", async () => {
    const c = fakeContainer({ bash: () => ({ stdout: "", exitCode: 3 }) });
    const { object } = sandbox(c);
    const out = await object.run("exit 3", {});
    expect(out).toEqual({ ready: true, exitCode: 3, output: "", truncated: false });
  });

  it("marks output beyond maxBytes as truncated and returns only the bound", async () => {
    const c = fakeContainer({
      bash: (line) => {
        const max = Number(line.match(/head -c (\d+)/)?.[1]);
        return { stdout: "x".repeat(max) };
      },
    });
    const { object } = sandbox(c);

    // The fake answers with exactly what head -c would have passed: maxBytes
    // + 1 bytes. Anything less proves the door sliced; anything more proves
    // the container's whole answer reached the Durable Object unbounded.
    const out = await object.run("yes", {}, { maxBytes: 4096 });
    expect(out.ready).toBe(true);
    if (!out.ready) return;
    expect(out.output).toBe("x".repeat(4096));
    expect(out.truncated).toBe(true);
  });

  it("boots the container on the run's boot options and keeps it alive", async () => {
    const c = fakeContainer({ bash: () => ({ stdout: "" }) });
    const { object, state } = sandbox(c);

    await object.run("true", {}, { boot: { keepAlive: true } });
    expect(c.starts[0]?.image).toBe(IMAGE);
    expect(await state.store.get("keep_alive")).toBe(true);
  });

  it("refuses to answer, rather than guess, while the container has not answered", async () => {
    const c = fakeContainer({ notReadyFor: 1, bash: () => ({ stdout: "late" }) });
    const { object } = sandbox(c);

    const first = await object.run("cat a", {});
    expect(first).toEqual({ ready: false });
  });

  it("waits for a starting container up to readyWaitMs and then answers", async () => {
    const c = fakeContainer({ notReadyFor: 1, bash: () => ({ stdout: "late" }) });
    const { object } = sandbox(c);

    const out = await object.run("cat a", {}, { readyWaitMs: 15_000 });
    expect(out).toEqual({ ready: true, exitCode: 0, output: "late", truncated: false });
  });
});
