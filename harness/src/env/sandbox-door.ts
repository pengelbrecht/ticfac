/**
 * The FactorySandbox Durable Object's RPC surface, as the harness package
 * calls it (epic 43y, step 3 of docs/spikes/n0b-round2-pi-durable.md).
 *
 * This package cannot import cloudflare/src — the factory worker and the
 * harness are separate packages, and the harness must stay runtime-neutral —
 * so the door is declared STRUCTURALLY here, in the shapes
 * cloudflare/src/factory-sandbox.ts actually serves: `SandboxProcessView`,
 * `SandboxOutput`, `SandboxRunOutcome` and the methods of
 * `FactorySandboxStub`. The drift risk is paid for where the two finally
 * meet (the WorkerAgent host, epic step 6), and the factory's own tests
 * (cloudflare/test/factory-sandbox.test.ts) hold the other side of the
 * contract: a change there that this shape does not follow fails CI here
 * the moment any harness test drives a door.
 */

/** cloudflare/src/sandbox.ts `SandboxProcessState`, spelled the same. */
export type SandboxProcessState = "running" | "completed" | "failed" | "gone";

/** What `startProcess`/`getProcess`/`listProcesses` answer about one process. */
export type SandboxProcessView = {
  id: string;
  state: SandboxProcessState;
  exit_code: number | null;
  /** The exact command line the process was started with. */
  command?: string;
};

/** What `readOutput` answers: new text, and the cursor to resume from. */
export type SandboxOutput = { text: string; offset: number };

/** The `run` door's answer: one short command's whole exchange, or not ready. */
export type SandboxRunOutcome =
  | { ready: false }
  | { ready: true; exitCode: number; output: string; truncated: boolean };

/** The boot options the factory's `FactoryBootOptions` accepts. */
export type SandboxBootOptions = {
  keepAlive?: boolean;
  /** A named instance size, or a custom one; the factory names its own default. */
  instance?: string | { vcpu: number; memoryMib: number; diskMb: number };
  /** A digest-pinned image from the deployment, when a run pins one. */
  pinnedImage?: string;
};

/** What a `run` door caller may ask of the boot, when it must start one. */
export type SandboxRunOptions = {
  /** The most bytes of output to return; more is truncated, not buffered. */
  maxBytes?: number;
  /** How long to keep asking a starting container before `ready: false`. */
  readyWaitMs?: number;
  /** The boot the run starts the container on, when it must. */
  boot?: SandboxBootOptions;
};

/**
 * The FactorySandbox DO, structurally: the run door for short commands, the
 * process doors for the tracked bash, and the questions the env asks while
 * driving either.
 */
export type SandboxDoor = {
  /** One short command: started, waited for and read in one RPC. */
  run(
    command: string,
    env: Record<string, string>,
    options?: SandboxRunOptions,
  ): Promise<SandboxRunOutcome>;
  /** Starts a long command as a background process, booting the container. */
  startProcess(
    command: string,
    env: Record<string, string>,
    options?: SandboxBootOptions,
  ): Promise<SandboxProcessView>;
  /** The process's state, or null when this sandbox no longer knows it. */
  getProcess(id: string): Promise<SandboxProcessView | null>;
  /** Every process this container knows — the live list, never a remembered one. */
  listProcesses(): Promise<SandboxProcessView[]>;
  /** Output after byte `offset`, and the cursor to resume from. */
  readOutput(id: string, offset: number): Promise<SandboxOutput>;
  /** Signals the process's group; a process that is not running is left alone. */
  killProcess(id: string): Promise<void>;
};

/** The factory's default working directory, image/common.sh `PROCESS_CWD`. */
export const PROCESS_CWD = "/workspace";
