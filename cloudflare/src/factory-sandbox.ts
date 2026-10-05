/**
 * FactorySandbox — the factory's own container Durable Object on the
 * `durable_object` scheduling policy (epic umq, tick nmd).
 *
 * ## Why this exists
 *
 * Sandbox SDK 0.x (the `Sandbox` class src/sandbox.ts adapts) runs on the
 * `default` scheduling policy: ONE image per container application, rolled out
 * application-wide on every deploy. That rollout is what took epic hn6's
 * orchestrator mid-run, and what `rollout_active_grace_period` and
 * internal/factory/rollout_held.go exist to fence. SDK 1.0 requires the
 * `durable_object` policy, and 0.x gets fixes only through 2026-12-31.
 *
 * Under `durable_object` the Durable Object starts its own container through
 * `this.ctx.container.start({ image, instance, ... })`: a running container
 * keeps the image it started on whatever is deployed afterwards (no rollout
 * touches it), each start may pick its own image and its own instance size,
 * and there is no platform `max_instances` — container-capacity.ts's count is
 * the only cap.
 *
 * ## What this class is
 *
 * The seven methods of the seam in src/sandbox.ts (start, get, list, read,
 * kill, destroy, isRunning), on `this.ctx.container` instead of the SDK:
 *
 *  - Processes are directories in the container, managed by the image's
 *    process runner (`ticks-proc`, tick x9d): `exec()` returns a process
 *    object that belongs to the request that made it, so a later request can
 *    only find a process through files — its pid, its merged output, its
 *    exit code. That is Cloudflare's own background-process recipe
 *    (sandbox/commands/run-background-processes).
 *  - Output is read by BYTE cursor in bounded chunks. `exec().output()` holds
 *    everything a command prints in the Durable Object's memory, so a whole
 *    run's log is never read in one call; the cursor never splits a UTF-8
 *    character.
 *  - Lifetime: `setInactivityTimeout` (re-armed in the CONSTRUCTOR, because a
 *    deploy restarts every Durable Object and the timeout does not survive
 *    that) bounds a container nobody addresses; a keepAlive boot (the
 *    orchestrator, a worker that has launched its work) additionally gets an
 *    alarm heartbeat — an alarm is activity — until it is destroyed.
 *  - `enableInternet: true`, said explicitly: every call must pass it, and the
 *    0.x container this replaces had Internet access (the clone, the gateway,
 *    the git door, package registries).
 *
 * It is OFF by default: the binding is SANDBOXES_V1 and nothing routes a run
 * through it until a run is submitted on that substrate (tick 1hq).
 */

import { DurableObject } from "cloudflare:workers";
import type { Env } from "./index";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessView,
} from "./sandbox";

// ------------------------------------------------------------ constants ---

/**
 * The named image in wrangler.toml (`[containers.images.factory]`) a boot
 * starts on when it is not pinned to another.
 */
export const FACTORY_IMAGE_NAME = "factory";

/**
 * The instance size a boot gets when it names none: the 0.x application's
 * `instance_type`, so moving substrate changes nothing about the machine.
 */
export const DEFAULT_INSTANCE: InstanceSize = "standard-3";

/**
 * The image's process runner (tick x9d, image/proc.sh). It keeps one
 * directory per process under its own root (/var/run/ticks-proc) and answers
 * one short verb per question; every call returns at once.
 */
export const PROCESS_RUNNER = "/usr/local/bin/ticks-proc";

/**
 * Where the boot entrypoint records the image's own environment.
 *
 * `exec()` hands a process only the variables it is given (and PATH): the
 * Dockerfile's `ENV` lines — TICKS_TK_VERSION, which the entrypoint verifies
 * tk against, among them — would be invisible to every process. The
 * container's main process DOES see them, so it writes them down once, and
 * every process sources them before its own variables are laid over them.
 */
export const IMAGE_ENV_FILE = "/var/lib/ticfac/image.env";

/** The working directory 0.x ran every process in. */
export const PROCESS_CWD = "/workspace";

/**
 * The container's main process: record the image's environment, then idle.
 * The work is started with `exec`, never as the entrypoint, so a process that
 * ends does not end the container (and its exit status stays readable).
 */
export const BOOT_ENTRYPOINT = [
  "/bin/sh",
  "-c",
  `mkdir -p /var/lib/ticfac ${PROCESS_CWD} && export -p > ${IMAGE_ENV_FILE} && exec sleep infinity`,
];

/**
 * How long a container nobody addresses keeps running: the 0.x `sleepAfter`
 * ceiling (SANDBOX_SLEEP_AFTER, 20 minutes) for a boot that is not kept alive.
 */
export const IDLE_TIMEOUT_MS = 20 * 60 * 1000;

/**
 * The inactivity timeout of a keepAlive boot. Its alarm fires every
 * {@link HEARTBEAT_MS}, and every alarm is activity, so this only matters
 * once the heartbeat has stopped — i.e. after a destroy or a lost alarm.
 */
export const KEEPALIVE_TIMEOUT_MS = 10 * 60 * 1000;

/** How often a keepAlive boot's alarm fires. */
export const HEARTBEAT_MS = 60 * 1000;

/**
 * The most output one read returns. `exec().output()` buffers the whole
 * answer in the Durable Object's memory (128 MB, shared with everything
 * else), and a cursor that resumes makes a bounded chunk cost nothing.
 */
export const READ_CHUNK_BYTES = 512 * 1024;

/**
 * The most output the `run` door returns (see {@link FactorySandboxCore.run}).
 * Sized under {@link READ_CHUNK_BYTES}: a short command's whole answer must
 * always fit one bounded read.
 */
export const RUN_MAX_BYTES = 256 * 1024;

/**
 * How long a fresh container may take to answer its first command.
 *
 * `start()` returns before the container is ready, and an `exec` that arrives
 * too early is refused outright ("Command `sh` was not found in the
 * container") rather than waited for. The first container on a freshly
 * deployed image is a cold pull of a multi-GB image: on 2026-10-01 one took
 * longer than five minutes on staging, and the umq proof run's orchestrator
 * (run_6b9f…) failed because its Workflow boot step — five minutes — waited
 * on that pull inline. So the wait is never inline any more (see
 * {@link FactorySandboxCore.startProcess}): a process asked for before the
 * container answers is recorded as PENDING and started by the object's own
 * alarm once it does, and this deadline is sized for a cold pull, not for a
 * request.
 */
export const READY_TIMEOUT_MS = 20 * 60 * 1000;

/** How often a container that has not answered yet is asked again. */
export const READY_POLL_MS = 5_000;

/** The most one readiness question may take before it counts as "not yet". */
export const READY_PROBE_TIMEOUT_MS = 10_000;

const STORAGE = {
  keepAlive: "keep_alive",
  inactivityMs: "inactivity_ms",
  command: (id: string) => `command:${id}`,
  lastStop: "last_stop",
  startedImage: "started_image",
  /** A process asked for before the container answered: its command and env. */
  pending: (id: string) => `pending:${id}`,
  pendingPrefix: "pending:",
  /** A process that could never be started, and why. */
  failed: (id: string) => `failed:${id}`,
  failedPrefix: "failed:",
  /** When the starting container must have answered by. */
  readyBy: "ready_by",
} as const;

// --------------------------------------------------------------- types ---

/** A named instance type, or a custom size (camelCase, as the runtime takes it). */
export type InstanceSize =
  | "lite"
  | "standard-1"
  | "standard-2"
  | "standard-3"
  | "standard-4"
  | { vcpu: number; memoryMib: number; diskMb: number };

/** How a boot wants its container: lifetime, size and (v1d) image. */
export type FactoryBootOptions = {
  keepAlive?: boolean;
  instance?: InstanceSize;
  /**
   * A digest-pinned image reference from this deployment's
   * `ctx.container.images` (a run's pin, tick v1d). Absent: the deployment's
   * current {@link FACTORY_IMAGE_NAME}.
   */
  pinnedImage?: string;
};

/**
 * The subset of `ctx.container` this class uses, declared structurally so a
 * test can hand the class a fake container — and because the workers-types
 * this bundle pins predate `images` and `inspect`.
 */
export type DoContainer = {
  readonly running: boolean;
  readonly images?: Record<string, string>;
  start(options: {
    image: string;
    enableInternet: boolean;
    instance?: InstanceSize;
    env?: Record<string, string>;
    entrypoint?: string[];
    labels?: Record<string, string>;
  }): void;
  exec(
    cmd: string[],
    options?: {
      cwd?: string;
      env?: Record<string, string>;
      stdout?: "pipe" | "ignore";
      stderr?: "pipe" | "ignore" | "combined";
    },
  ): Promise<DoExecProcess>;
  monitor(): Promise<void>;
  destroy(reason?: unknown): Promise<void>;
  setInactivityTimeout(durationMs: number): Promise<void>;
  inspect?(): Promise<{ image: string; labels: Record<string, string> } | null>;
};

export type DoExecProcess = {
  readonly pid: number;
  readonly exitCode: Promise<number>;
  output(): Promise<{ stdout: ArrayBuffer; stderr: ArrayBuffer; exitCode: number }>;
};

/** The storage subset this class uses. */
type DoStorage = Pick<
  DurableObjectStorage,
  "get" | "put" | "delete" | "setAlarm" | "deleteAlarm" | "getAlarm" | "list"
>;

type PendingProcess = { command: string; env: Record<string, string> };

/**
 * The answer of the `run` door: one short command's exit code and output, or
 * that the container was not ready to answer a command at all.
 *
 * `ready: false` is a REFUSAL to guess, not an error: a container that is
 * still booting (a cold image pull can take minutes) has no exit code to
 * report. The caller — the harness package's `FactorySandboxEnv`, epic 43y
 * step 3 — decides how long to keep asking, and a boot's lifetime is settled
 * by `startProcess`, the door that owns the pending-process machinery.
 */
export type SandboxRunOutcome =
  | { ready: false }
  | { ready: true; exitCode: number; output: string; truncated: boolean };

/** What a `run` door caller may ask of the boot, when it must start one. */
export type SandboxRunOptions = {
  /** The most bytes of output to return; more is truncated, not buffered. */
  maxBytes?: number;
  /**
   * How long a container that has not answered yet may keep being asked
   * before the door returns `ready: false`. Default: no wait — a caller that
   * wants to wait for readiness owns that loop (the alternative is an RPC
   * parked for a cold image pull's five minutes, which no caller wants).
   */
  readyWaitMs?: number;
  /** The boot the run starts the container on, when it must. */
  boot?: FactoryBootOptions;
};

/** What the runner says about one process directory. */
export type RunnerState =
  | { state: "starting" }
  | { state: "running"; pid: number }
  | { state: "exited"; exitCode: number }
  | { state: "lost" }
  | { state: "missing" };

// -------------------------------------------------------------- runner ---

/**
 * The runner's command lines, in one place (tick x9d owns the script; this is
 * the only TypeScript that knows its verbs).
 */
export const runner = {
  /** Starts `argv` as process `id` in the working directory; exit 4 when `id` is taken. */
  start: (id: string, argv: string[]) => [
    PROCESS_RUNNER,
    "start",
    id,
    "--cwd",
    PROCESS_CWD,
    "--",
    ...argv,
  ],
  /** `state=…`, `pid=…` and, once exited, `exit_code=…`; exit 3 for an unknown id. */
  status: (id: string) => [PROCESS_RUNNER, "status", id],
  /**
   * At most `max` bytes of stdout from byte `offset`. The runner prints the
   * whole remainder; `head -c` is what keeps one answer bounded in the
   * Durable Object's memory.
   */
  read: (id: string, offset: number, max: number) => [
    "sh",
    "-c",
    `${PROCESS_RUNNER} read "$1" stdout "$2" | head -c "$3"`,
    "sh",
    id,
    String(offset),
    String(max),
  ],
  /** TERM to the process group, KILL after the runner's grace. */
  kill: (id: string) => [PROCESS_RUNNER, "kill", id],
  list: () => [PROCESS_RUNNER, "list"],
};

/** The runner's `status` answer, parsed. Anything unreadable (an unknown id) is `missing`. */
export function parseRunnerState(text: string): RunnerState {
  const fields = new Map<string, string>();
  for (const line of text.split("\n")) {
    const eq = line.indexOf("=");
    if (eq > 0) fields.set(line.slice(0, eq).trim(), line.slice(eq + 1).trim());
  }
  switch (fields.get("state")) {
    case "starting":
      return { state: "starting" };
    case "running": {
      const pid = Number(fields.get("pid"));
      return Number.isInteger(pid) && pid > 0 ? { state: "running", pid } : { state: "starting" };
    }
    case "exited": {
      const raw = fields.get("exit_code") ?? "";
      const exitCode = Number(raw);
      return raw !== "" && Number.isInteger(exitCode)
        ? { state: "exited", exitCode }
        : { state: "lost" };
    }
    case "lost":
      return { state: "lost" };
    default:
      return { state: "missing" };
  }
}

/**
 * The runner's state in the seam's vocabulary, or null for a process this
 * container does not know (the seam's "gone" — a container that died and came
 * back empty).
 *
 * `lost` (the process ended without recording an exit code — SIGKILLed with
 * its recorder) is `failed` with no exit code: the supervision loop's question
 * is "is it still working", and the answer is no, not cleanly.
 */
export function runnerView(
  id: string,
  state: RunnerState,
  command: string | undefined,
): SandboxProcessView | null {
  const base = command === undefined ? { id } : { id, command };
  switch (state.state) {
    case "missing":
      return null;
    case "starting":
    case "running":
      return { ...base, state: "running", exit_code: null };
    case "exited":
      return {
        ...base,
        state: state.exitCode === 0 ? "completed" : "failed",
        exit_code: state.exitCode,
      };
    case "lost":
      return { ...base, state: "failed", exit_code: null };
  }
}

/**
 * The longest prefix of `bytes` that ends on a UTF-8 character boundary.
 *
 * A byte cursor that stopped inside a multi-byte character would decode a
 * replacement character at the end of one chunk and another at the start of
 * the next. Backing off to the boundary costs at most three bytes, which the
 * next read returns whole.
 */
export function utf8Boundary(bytes: Uint8Array): number {
  const n = bytes.length;
  // Look back at most 3 bytes for the lead byte of a trailing character.
  for (let back = 1; back <= Math.min(3, n); back++) {
    const b = bytes[n - back] as number;
    if ((b & 0xc0) === 0x80) continue; // a continuation byte: keep looking
    const need = b >= 0xf0 ? 4 : b >= 0xe0 ? 3 : b >= 0xc0 ? 2 : 1;
    return need > back ? n - back : n;
  }
  return n;
}

/** The command a process string is run with: bash, stderr merged into stdout. */
export function processArgv(command: string): string[] {
  // One merged stream, so one byte cursor stays correct (src/sandbox.ts's
  // MERGE_STDERR, for the same reason).
  // `exec 2>&1` FIRST, so it holds for every command of a compound line: a
  // trailing `2>&1` binds to the last command only (staging: `echo a >&2;
  // exit 5` lost the `a`).
  return ["bash", "-c", `exec 2>&1; ${command}`];
}

/**
 * The shell line a process is started through: the image's environment
 * first, the process's own variables laid back over it, then the runner.
 */
export const START_WRAPPER = 'own=$(export -p); . "$0" 2>/dev/null; eval "$own"; exec "$@"';

const decoder = new TextDecoder();

// --------------------------------------------------------- the DO class ---

/**
 * What the class needs of its Durable Object state, structurally: the
 * runtime's `DurableObject` base refuses to be constructed over anything but a
 * real state, and the lifecycle is what needs testing.
 */
export type SandboxState = {
  readonly container?: DoContainer;
  readonly storage: DoStorage;
  blockConcurrencyWhile<T>(fn: () => Promise<T>): Promise<T>;
  waitUntil(promise: Promise<unknown>): void;
};

/** The class's behaviour, over a {@link SandboxState} (tested directly). */
export class FactorySandboxCore {
  /**
   * Whether THIS instance has seen the container answer. In memory on
   * purpose: a restarted object re-checks once, which costs one command.
   */
  private ready = false;

  constructor(
    private readonly ctx: SandboxState,
    private readonly now: () => number = Date.now,
  ) {
    // A deploy restarts every Durable Object, and an inactivity timeout does
    // not survive the restart: a container still running under a fresh
    // instance would otherwise be stopped "shortly after" the object goes
    // idle — exactly the deploy-kills-a-live-run failure this class exists
    // to end. Re-armed here, before any request runs.
    const container = this.container();
    if (container?.running) {
      void ctx.blockConcurrencyWhile(async () => {
        await this.rearm(container);
      });
    }
  }

  // ---------------------------------------------------------------- RPC ---

  /**
   * Starts `command` as a background process, booting the container first.
   *
   * Never waits for a cold container: when the container has not answered
   * yet, the process is recorded PENDING and this returns at once with its id
   * in state `running` — a boot is under way. The object's alarm starts it
   * the moment the container answers, or fails it (with the reason, readable
   * as the process's output) when the container never does within
   * {@link READY_TIMEOUT_MS}. A caller with its own short deadline (a
   * Workflow step, an HTTP door) is therefore never the thing a cold image
   * pull has to fit inside.
   */
  async startProcess(
    command: string,
    env: Record<string, string>,
    options: FactoryBootOptions = {},
  ): Promise<SandboxProcessView> {
    const container = await this.ensureRunning(options);
    const id = crypto.randomUUID();
    await this.ctx.storage.put(STORAGE.command(id), command);
    if (this.ready || (await this.answers(container))) {
      this.ready = true;
      await this.startNow(container, id, command, env);
      return { id, state: "running", exit_code: null, command };
    }
    await this.ctx.storage.put(STORAGE.pending(id), { command, env } satisfies PendingProcess);
    await this.scheduleAlarm(this.now() + READY_POLL_MS);
    return { id, state: "running", exit_code: null, command };
  }

  /**
   * The `run` door: one SHORT command, started, waited for and read in one
   * RPC (epic 43y step 3, the `FactorySandboxEnv` of the harness package).
   *
   * Why a door: the env's file operations — `cat`, `mkdir`, `mv`, the guard
   * shim's own install — are short commands whose result is needed before the
   * next one. Over `startProcess` each costs three RPCs (start, poll until
   * exit, read); here the whole exchange is one call to this Durable Object,
   * and the output it can buffer is bounded by `head -c` so a runaway answer
   * can never hold more than {@link RUN_MAX_BYTES} of the Durable Object's
   * memory. Model commands (the bash tool) do NOT go through here: they are
   * killable, resumable things and belong to `startProcess`.
   *
   * The wait for the container to answer is bounded by `readyWaitMs` and
   * returns `{ ready: false }` when it elapses — the caller owns the retry
   * loop, and no RPC is parked for a cold image pull.
   */
  async run(
    command: string,
    env: Record<string, string>,
    options: SandboxRunOptions = {},
  ): Promise<SandboxRunOutcome> {
    const container = await this.ensureRunning(options.boot ?? {});
    if (!this.ready) {
      const deadline = this.now() + (options.readyWaitMs ?? 0);
      while (!(await this.answers(container))) {
        if (this.now() >= deadline) {
          return { ready: false };
        }
        await sleep(READY_POLL_MS);
      }
      this.ready = true;
    }
    const max = options.maxBytes ?? RUN_MAX_BYTES;
    // PIPESTATUS[0] keeps the command's own exit code, which head would
    // otherwise swallow (a truncated command may report 141, SIGPIPE — a
    // caller that reads `truncated` has already stopped caring). Its `${` is
    // the container's bash, escaped out of this file's own interpolation.
    const line = `exec 2>&1; { ${command}; } | head -c ${max + 1}; exit "\${PIPESTATUS[0]}"`;
    const argv = ["sh", "-c", START_WRAPPER, IMAGE_ENV_FILE, "bash", "-c", line];
    const started = await container.exec(argv, {
      cwd: PROCESS_CWD,
      env,
      stdout: "pipe",
      stderr: "combined",
    });
    const out = await started.output();
    const bytes = new Uint8Array(out.stdout);
    // The bound is taken on the first maxBytes, so a character straddling the
    // bound is not split: the next read (none here) would carry it whole.
    const whole = utf8Boundary(bytes.subarray(0, max));
    return {
      ready: true,
      exitCode: out.exitCode,
      output: decoder.decode(bytes.subarray(0, whole)),
      truncated: bytes.length > max,
    };
  }

  /** Starts one process through the runner in a container that answers. */
  private async startNow(
    container: DoContainer,
    id: string,
    command: string,
    env: Record<string, string>,
  ): Promise<void> {
    const argv = [
      "sh",
      "-c",
      START_WRAPPER,
      IMAGE_ENV_FILE,
      ...runner.start(id, processArgv(command)),
    ];
    const started = await container.exec(argv, {
      cwd: PROCESS_CWD,
      env,
      stdout: "pipe",
      stderr: "combined",
    });
    // The runner returns once the process is recorded (or refused); the
    // process itself outlives this request.
    const out = await started.output();
    if (out.exitCode !== 0) {
      throw new Error(
        `factory sandbox: the process runner refused to start a process (exit ${out.exitCode}): ` +
          decoder.decode(out.stdout).trim().slice(0, 400),
      );
    }
  }

  /** The process's state, or null when this container does not know it. */
  async getProcess(id: string): Promise<SandboxProcessView | null> {
    const early = await this.notStarted(id);
    if (early !== null) return early;
    const container = this.container();
    // Never boots: a stopped container knows no process, which is what the
    // seam's null means.
    if (container === undefined || !container.running) return null;
    const state = await this.inspect(container, id);
    return runnerView(id, state, await this.ctx.storage.get<string>(STORAGE.command(id)));
  }

  /** Every process this container knows — the live list, never a remembered one. */
  async listProcesses(): Promise<SandboxProcessView[]> {
    const views: SandboxProcessView[] = [];
    for (const prefix of [STORAGE.pendingPrefix, STORAGE.failedPrefix]) {
      for (const key of (await this.ctx.storage.list({ prefix })).keys()) {
        const view = await this.notStarted(key.slice(prefix.length));
        if (view !== null) views.push(view);
      }
    }
    const container = this.container();
    // Never boots, and never trusts this instance's ready flag alone: a
    // deploy restarts this object — in memory, the flag is lost — while the
    // container and its processes live on, and the nonce replay of a
    // tracked bash calls here FIRST, with no earlier `run` to have marked
    // the container ready (the guardless env runs no short command before
    // it). A container that answers a probe IS ready; one that does not (a
    // cold boot still pulling its image) is left to the pending-process
    // machinery, exactly as before.
    if (container === undefined || !container.running) return views;
    if (!this.ready) {
      if (!(await this.answers(container))) return views;
      this.ready = true;
    }
    const listed = await run(container, runner.list());
    for (const id of listed.stdout.split("\n").map((l) => l.trim())) {
      if (id === "") continue;
      const view = runnerView(
        id,
        await this.inspect(container, id),
        await this.ctx.storage.get<string>(STORAGE.command(id)),
      );
      if (view !== null) views.push(view);
    }
    return views;
  }

  /**
   * Output after byte `offset`, at most {@link READ_CHUNK_BYTES} of it, and
   * the cursor to resume from. A container that is not running has nothing
   * new to say: the cursor stays where it was. A process that could never be
   * started says why, as its whole output.
   */
  async readOutput(id: string, offset: number): Promise<SandboxOutput> {
    const start = Number.isInteger(offset) && offset > 0 ? offset : 0;
    const failed = await this.ctx.storage.get<string>(STORAGE.failed(id));
    if (failed !== undefined) {
      const bytes = new TextEncoder().encode(`${failed}\n`);
      if (start >= bytes.length) return { text: "", offset: start };
      return { text: decoder.decode(bytes.subarray(start)), offset: bytes.length };
    }
    if ((await this.ctx.storage.get(STORAGE.pending(id))) !== undefined) {
      return { text: "", offset: start };
    }
    const container = this.container();
    if (container === undefined || !container.running) return { text: "", offset: start };
    const process = await container.exec(runner.read(id, start, READ_CHUNK_BYTES), {
      stdout: "pipe",
      stderr: "ignore",
    });
    const out = await process.output();
    if (out.exitCode !== 0) return { text: "", offset: start };
    const bytes = new Uint8Array(out.stdout);
    const whole = utf8Boundary(bytes);
    return { text: decoder.decode(bytes.subarray(0, whole)), offset: start + whole };
  }

  /** Signals the process's group. A process that is not running is left alone. */
  async killProcess(id: string): Promise<void> {
    if ((await this.ctx.storage.get(STORAGE.pending(id))) !== undefined) {
      await this.ctx.storage.delete(STORAGE.pending(id));
      await this.ctx.storage.put(STORAGE.failed(id), "killed before its container answered");
      return;
    }
    const container = this.container();
    if (container === undefined || !container.running) return;
    await run(container, runner.kill(id));
  }

  /** Tears the container down and stops the heartbeat. */
  async destroy(): Promise<void> {
    await this.failPending("the container was destroyed before it answered");
    await this.ctx.storage.delete(STORAGE.keepAlive);
    await this.ctx.storage.deleteAlarm();
    const container = this.container();
    if (container?.running) await container.destroy();
  }

  /** Whether the container is up. Never starts one. */
  async isRunning(): Promise<boolean> {
    return this.container()?.running === true;
  }

  /**
   * The image reference this deployment starts a container on, from
   * `ctx.container.images` — what a run pins at submit (tick v1d). Null when
   * the deployment names no such image.
   */
  async imageRef(): Promise<string | null> {
    const ref = this.container()?.images?.[FACTORY_IMAGE_NAME];
    return typeof ref === "string" && ref !== "" ? ref : null;
  }

  /**
   * The image the running container STARTED on: `inspect()` when the runtime
   * answers it, else what this object recorded at start. Null when no
   * container is running. The proof that a deploy did not move a live
   * container (umq [A2]).
   */
  /** How the last kept-alive container stopped, as monitor() saw it; null when none has. */
  async lastStop(): Promise<{ at: string; how: string } | null> {
    return (await this.ctx.storage.get<{ at: string; how: string }>(STORAGE.lastStop)) ?? null;
  }

  async runningImage(): Promise<string | null> {
    const container = this.container();
    if (container === undefined || !container.running) return null;
    const info = await container.inspect?.();
    if (info !== undefined && info !== null && info.image !== "") return info.image;
    return (await this.ctx.storage.get<string>(STORAGE.startedImage)) ?? null;
  }

  /**
   * The heartbeat of a keepAlive boot: every alarm is activity, so a container
   * someone asked to keep alive outlives its inactivity timeout until it is
   * destroyed. Checks its own state first — an alarm can fire after a
   * destroy, or after the container stopped on its own.
   */
  async alarm(): Promise<void> {
    const waiting = await this.drainPending();
    const keepAlive = (await this.ctx.storage.get<boolean>(STORAGE.keepAlive)) === true;
    const container = this.container();
    const alive = keepAlive && container?.running;
    if (!alive) await this.ctx.storage.delete(STORAGE.keepAlive);
    if (alive) await container.setInactivityTimeout(KEEPALIVE_TIMEOUT_MS);
    const next = Math.min(
      waiting ? this.now() + READY_POLL_MS : Number.POSITIVE_INFINITY,
      alive ? Date.now() + HEARTBEAT_MS : Number.POSITIVE_INFINITY,
    );
    if (Number.isFinite(next)) await this.ctx.storage.setAlarm(next);
  }

  /**
   * Starts every pending process once the container answers, or fails them
   * all — with the reason — once it has stopped or its deadline has passed.
   * True while some are still waiting.
   */
  private async drainPending(): Promise<boolean> {
    const pending = await this.ctx.storage.list<PendingProcess>({ prefix: STORAGE.pendingPrefix });
    if (pending.size === 0) return false;
    const container = this.container();
    if (container === undefined || !container.running) {
      const stop = await this.lastStop();
      await this.failPending(
        "factory sandbox: the container stopped before it answered" +
          (stop === null ? "" : ` (${stop.how}, ${stop.at})`),
      );
      return false;
    }
    if (!(await this.answers(container))) {
      const readyBy = (await this.ctx.storage.get<number>(STORAGE.readyBy)) ?? 0;
      if (this.now() < readyBy) return true;
      const stop = await this.lastStop();
      await this.failPending(
        `factory sandbox: the container did not answer within ${READY_TIMEOUT_MS / 60_000} ` +
          `minutes of its start (image ${(await this.ctx.storage.get<string>(STORAGE.startedImage)) ?? "unknown"})` +
          (stop === null ? "" : `; it stopped: ${stop.how}`) +
          "; it was destroyed so the next boot starts fresh",
      );
      await container.destroy().catch(() => {});
      return false;
    }
    this.ready = true;
    for (const [key, process] of pending) {
      const id = key.slice(STORAGE.pendingPrefix.length);
      try {
        await this.startNow(container, id, process.command, process.env);
      } catch (error) {
        await this.ctx.storage.put(STORAGE.failed(id), String(error).slice(0, 600));
      }
      // The env holds the run's credentials: kept only as long as it is needed.
      await this.ctx.storage.delete(key);
    }
    return false;
  }

  /** Fails every pending process with `reason`. */
  private async failPending(reason: string): Promise<void> {
    const pending = await this.ctx.storage.list({ prefix: STORAGE.pendingPrefix });
    for (const key of pending.keys()) {
      await this.ctx.storage.put(STORAGE.failed(key.slice(STORAGE.pendingPrefix.length)), reason);
      await this.ctx.storage.delete(key);
    }
  }

  /** A pending process is running (its boot is); a failed one is failed. */
  private async notStarted(id: string): Promise<SandboxProcessView | null> {
    const command = await this.ctx.storage.get<string>(STORAGE.command(id));
    const base = command === undefined ? { id } : { id, command };
    if ((await this.ctx.storage.get(STORAGE.failed(id))) !== undefined) {
      return { ...base, state: "failed", exit_code: null };
    }
    if ((await this.ctx.storage.get(STORAGE.pending(id))) !== undefined) {
      return { ...base, state: "running", exit_code: null };
    }
    return null;
  }

  /** Whether the container answers a command now (bounded; never throws). */
  private async answers(container: DoContainer): Promise<boolean> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      const probe = await Promise.race([
        run(container, ["test", "-s", IMAGE_ENV_FILE]),
        new Promise<null>((resolve) => {
          timer = setTimeout(() => resolve(null), READY_PROBE_TIMEOUT_MS);
        }),
      ]);
      return probe !== null && probe.exitCode === 0;
    } catch {
      return false;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
    }
  }

  /** Sets the alarm for `at` unless one is already due sooner. */
  private async scheduleAlarm(at: number): Promise<void> {
    const current = await this.ctx.storage.getAlarm();
    if (current === null || current > at) await this.ctx.storage.setAlarm(at);
  }

  // ----------------------------------------------------------- internals ---

  private container(): DoContainer | undefined {
    return this.ctx.container;
  }

  /** Boots the container when it is not running, and applies the lifetime. */
  private async ensureRunning(options: FactoryBootOptions): Promise<DoContainer> {
    const container = this.container();
    if (container === undefined) {
      throw new Error(
        "factory sandbox: this Durable Object has no container — the SANDBOXES_V1 " +
          "[[containers]] application is not configured on this deployment",
      );
    }
    if (!container.running) {
      const image = options.pinnedImage ?? container.images?.[FACTORY_IMAGE_NAME];
      if (image === undefined || image === "") {
        throw new Error(
          `factory sandbox: no image to start — the deployment declares no ` +
            `[containers.images.${FACTORY_IMAGE_NAME}] and the boot pinned none`,
        );
      }
      container.start({
        image,
        instance: options.instance ?? DEFAULT_INSTANCE,
        enableInternet: true,
        entrypoint: BOOT_ENTRYPOINT,
      });
      this.ready = false;
      await this.ctx.storage.put(STORAGE.startedImage, image);
      await this.ctx.storage.put(STORAGE.readyBy, this.now() + READY_TIMEOUT_MS);
      await this.ctx.storage.delete(STORAGE.lastStop);
      // Every new container is watched from its start, so a start that fails
      // (a bad image, an entrypoint that exits) leaves its reason behind.
      this.watch(container);
    }
    const keepAlive =
      options.keepAlive === true ||
      (await this.ctx.storage.get<boolean>(STORAGE.keepAlive)) === true;
    const timeout = keepAlive ? KEEPALIVE_TIMEOUT_MS : IDLE_TIMEOUT_MS;
    await this.ctx.storage.put(STORAGE.inactivityMs, timeout);
    try {
      await container.setInactivityTimeout(timeout);
    } catch (error) {
      // A container whose lifetime cannot be bounded is not one to run work
      // in: the next request starts a new one.
      await container.destroy();
      throw error;
    }
    if (keepAlive && (await this.ctx.storage.get<boolean>(STORAGE.keepAlive)) !== true) {
      await this.ctx.storage.put(STORAGE.keepAlive, true);
      await this.scheduleAlarm(Date.now() + HEARTBEAT_MS);
    }
    return container;
  }

  /** The constructor's half of the lifetime: re-arm what a restart dropped. */
  private async rearm(container: DoContainer): Promise<void> {
    const timeout = (await this.ctx.storage.get<number>(STORAGE.inactivityMs)) ?? IDLE_TIMEOUT_MS;
    try {
      await container.setInactivityTimeout(timeout);
    } catch (error) {
      console.error(`factory sandbox: could not re-arm the inactivity timeout: ${String(error)}`);
    }
    // A restart can land between a pending process and its alarm; the alarm
    // is what starts it, so it must exist.
    if ((await this.ctx.storage.list({ prefix: STORAGE.pendingPrefix, limit: 1 })).size > 0) {
      await this.scheduleAlarm(this.now() + READY_POLL_MS);
    }
    if ((await this.ctx.storage.get<boolean>(STORAGE.keepAlive)) === true) {
      if ((await this.ctx.storage.getAlarm()) === null) {
        await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
      }
      this.watch(container);
    }
  }

  /**
   * Records how a kept-alive container stopped. Only those: a pending
   * `monitor()` holds the inactivity timeout off, which is right for a
   * container someone is keeping alive and wrong for one that should idle out.
   */
  private watch(container: DoContainer): void {
    const record = (how: string) =>
      this.ctx.storage.put(STORAGE.lastStop, { at: new Date().toISOString(), how });
    this.ctx.waitUntil(
      container.monitor().then(
        () => record("exited 0"),
        (error: unknown) => record(`stopped: ${String(error).slice(0, 300)}`),
      ),
    );
  }

  private async inspect(container: DoContainer, id: string): Promise<RunnerState> {
    const out = await run(container, runner.status(id));
    return parseRunnerState(out.stdout);
  }
}

/**
 * The Durable Object class `[[containers]] class_name = "FactorySandbox"`
 * attaches the durable_object-policy application to. Every method is the
 * core's; the class exists to be constructed by the runtime.
 */
export class FactorySandbox extends DurableObject<Env> {
  private readonly core: FactorySandboxCore;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.core = new FactorySandboxCore(ctx as unknown as SandboxState);
  }

  startProcess(
    command: string,
    env: Record<string, string>,
    options: FactoryBootOptions = {},
  ): Promise<SandboxProcessView> {
    return this.core.startProcess(command, env, options);
  }
  run(
    command: string,
    env: Record<string, string>,
    options: SandboxRunOptions = {},
  ): Promise<SandboxRunOutcome> {
    return this.core.run(command, env, options);
  }
  getProcess(id: string): Promise<SandboxProcessView | null> {
    return this.core.getProcess(id);
  }
  listProcesses(): Promise<SandboxProcessView[]> {
    return this.core.listProcesses();
  }
  readOutput(id: string, offset: number): Promise<SandboxOutput> {
    return this.core.readOutput(id, offset);
  }
  killProcess(id: string): Promise<void> {
    return this.core.killProcess(id);
  }
  destroy(): Promise<void> {
    return this.core.destroy();
  }
  isRunning(): Promise<boolean> {
    return this.core.isRunning();
  }
  imageRef(): Promise<string | null> {
    return this.core.imageRef();
  }
  lastStop(): Promise<{ at: string; how: string } | null> {
    return this.core.lastStop();
  }
  runningImage(): Promise<string | null> {
    return this.core.runningImage();
  }
  override alarm(): Promise<void> {
    return this.core.alarm();
  }
}

/** Runs a short command to completion and returns its output. */
async function run(
  container: DoContainer,
  argv: string[],
): Promise<{ exitCode: number; stdout: string }> {
  const process = await container.exec(argv, { stdout: "pipe", stderr: "ignore" });
  const out = await process.output();
  return { exitCode: out.exitCode, stdout: decoder.decode(out.stdout) };
}

/** Resolves after `ms`. The run door's readiness loop pokes at this scale. */
function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ------------------------------------------------------- the seam adapter ---

/** The RPC surface the adapter calls — the class above, structurally. */
export type FactorySandboxStub = Pick<
  FactorySandboxCore,
  | "run"
  | "startProcess"
  | "getProcess"
  | "listProcesses"
  | "readOutput"
  | "killProcess"
  | "destroy"
  | "isRunning"
  | "imageRef"
  | "runningImage"
  | "lastStop"
>;

/** The SANDBOXES_V1 namespace, structurally. */
export type FactorySandboxNamespace = {
  idFromName(name: string): DurableObjectId;
  get(id: DurableObjectId): FactorySandboxStub;
};

/** Options a boot may carry on this substrate beyond the seam's own. */
export type FactoryGetOptions = {
  image?: string;
  keepAlive?: boolean;
  instance?: InstanceSize;
  pinnedImage?: string;
};

/**
 * The SANDBOXES_V1 namespace behind the seam in src/sandbox.ts.
 *
 * `get` addresses the object and nothing else: the container boots on the
 * first `startProcess`, which carries the boot's lifetime, size and image —
 * every other call answers from a running container or not at all, so no
 * question ever boots a container just to be told it is empty (the reclaim's
 * lesson in container-capacity.ts, which 0.x could only work around).
 *
 * `options.image` (the repository's declared `[sandbox].image`) is accepted
 * and not passed on, exactly as the 0.x adapter does: the image a container
 * starts on is the deployment's ({@link FACTORY_IMAGE_NAME}) or the run's pin.
 */
export function factorySandboxBinding(namespace: FactorySandboxNamespace): SandboxBinding {
  return {
    async get(name: string, options?: FactoryGetOptions): Promise<OrchestratorSandbox> {
      const stub = namespace.get(namespace.idFromName(name));
      const boot: FactoryBootOptions = {
        ...(options?.keepAlive === true ? { keepAlive: true } : {}),
        ...(options?.instance === undefined ? {} : { instance: options.instance }),
        ...(options?.pinnedImage === undefined ? {} : { pinnedImage: options.pinnedImage }),
      };
      return {
        startProcess: (command, opts) => stub.startProcess(command, opts.env, boot),
        getProcess: (id) => stub.getProcess(id),
        listProcesses: () => stub.listProcesses(),
        readOutput: (id, offset) => stub.readOutput(id, offset),
        killProcess: (id) => stub.killProcess(id),
        destroy: () => stub.destroy(),
        isRunning: () => stub.isRunning(),
      };
    },
  };
}

/**
 * The SANDBOXES_V1 binding behind the seam, or null when this deployment has
 * none. A namespace is told apart from a test's seam-shaped fake by
 * `idFromName`, as `isSandboxNamespace` does for SANDBOXES.
 */
export function factorySandboxBindingFromEnv(env: Env): SandboxBinding | null {
  const binding = env.SANDBOXES_V1;
  if (binding === undefined || binding === null) return null;
  return typeof (binding as { idFromName?: unknown }).idFromName === "function"
    ? factorySandboxBinding(binding as FactorySandboxNamespace)
    : (binding as SandboxBinding);
}
