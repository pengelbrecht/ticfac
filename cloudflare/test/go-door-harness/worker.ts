/**
 * The real factory Worker, wrapped for the GO end-to-end suite
 * (internal/exec/cloudflaresandbox, tick 6gr): `run.mjs` in this directory
 * builds this entry with esbuild and serves it in real workerd through
 * miniflare on a real HTTP port, so the Go executor — not a fixture — is the
 * only client on the far side of the door.
 *
 * ## What is real and what is substituted
 *
 * Every request is answered by `src/index.ts`'s own fetch handler — routing,
 * authorization, the lease check, `sandbox-dispatch.ts`'s validation and
 * `sandbox-executor.ts`'s adoption machinery are the deployed code, exercised
 * over the wire the deployed door speaks. The ONLY substitution is the
 * container itself, through the `SANDBOXES` seam — the same rule every suite
 * in this bundle runs by (sandbox-dispatch.test.ts's header says it; the seam
 * `isSandboxNamespace` discriminates a test binding BY DESIGN). A container is
 * Cloudflare's to boot: this harness runs where no container can exist, and
 * the worker's own test suite could not run either if the seam did not exist.
 *
 * The wrapper adds exactly two things, both harness scaffolding rather than
 * worker behaviour, and both named here so nobody mistakes them for the
 * deployed surface:
 *
 *   - the fake `SANDBOXES` binding below, substituted into `env` before every
 *     request is delegated, whose fake container answers the green-start
 *     probe and keeps its work process RUNNING — a worker genuinely mid-tick,
 *     the only state in which "the door returned without waiting for the
 *     attempt" and "a second start adopts it" mean anything;
 *   - `GET /__door_harness/sandboxes`, the harness's one observation route,
 *     which answers what the fake binding was asked to boot so the Go test
 *     can assert the negative the door's own answers cannot express: that a
 *     restarted orchestrator's second dispatch booted NO rival container.
 *     Nothing under `/__door_harness/` exists in the deployed Worker.
 */
import worker, { type Env } from "../../src/index";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../../src/sandbox";
import { WORKER_COMMAND, WORKER_PROBE_MARKER } from "../../src/worker-boot";

// The Durable Object classes the deployed worker exports, re-exported so the
// miniflare binding table can declare them: one RunRoom per project is what
// holds the dispatch lease the door verifies, and it must be the real one —
// a lease faked beside the door would prove nothing about the door. Sandbox is
// re-exported too because `src/index.ts` exports it; the harness binds no
// SANDBOXES namespace (the fake below plays that binding), so the class is
// present but never addressed.
export {
  RepoRoom,
  RunRoom,
  RunWorkflow,
  Sandbox,
  SignalInbox,
} from "../../src/index";

// ----------------------------------------------------- the fake container ---

/** One process inside a fake container. */
class FakeProcess {
  constructor(
    readonly id: string,
    readonly command: string,
    readonly env: Record<string, string>,
  ) {}
  state: SandboxProcessState = "running";
  exit_code: number | null = null;
  output = "";

  /** The harness finished: terminal, with its exit status. */
  finish(code: number): void {
    this.state = code === 0 ? "completed" : "failed";
    this.exit_code = code;
  }

  get view(): SandboxProcessView {
    return { id: this.id, state: this.state, exit_code: this.exit_code, command: this.command };
  }
}

/**
 * One fake container: the green-start probe answers its marker and EXITS; the
 * work process prints and KEEPS RUNNING — ported from
 * `sandbox-dispatch.test.ts`'s `FakeSandbox` so the two fakes cannot disagree
 * about what a container's own rules are.
 */
class FakeSandbox implements OrchestratorSandbox {
  readonly processes: FakeProcess[] = [];
  #next = 0;

  constructor(readonly name: string) {}

  async startProcess(
    command: string,
    options: { env: Record<string, string> },
  ): Promise<SandboxProcessView> {
    const process = new FakeProcess(`${this.name}-p${++this.#next}`, command, options.env);
    if (command.includes("--probe")) {
      // The real probe prints its marker and exits; watchProbe only evaluates
      // it once terminal.
      process.output = `${WORKER_PROBE_MARKER}\n`;
      process.finish(0);
    } else if (command === WORKER_COMMAND) {
      process.output = "implementing the tick\n";
      // Deliberately still `running`: the whole point of the handle.
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

  async destroy(): Promise<void> {}
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

  /** What the Go test reads back: every container ever addressed, and every process it was asked to run. */
  describe(): {
    sandboxes: Array<{
      name: string;
      processes: Array<{ id: string; command: string; state: string; exit_code: number | null }>;
    }>;
  } {
    return {
      sandboxes: [...this.#byName.values()].map((sandbox) => ({
        name: sandbox.name,
        processes: sandbox.processes.map((p) => ({
          id: p.id,
          command: p.command,
          state: p.state,
          exit_code: p.exit_code,
        })),
      })),
    };
  }
}

const binding = new FakeSandboxes();

// ---------------------------------------------------------------- the door ---

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/__door_harness/sandboxes") {
      return Response.json(binding.describe());
    }
    // The one substitution, applied per request exactly the way the worker's
    // own vitest suite applies it per test: the SANDBOXES seam takes a test
    // binding by design (`isSandboxNamespace` is the discriminator), and a
    // deployment-shaped binding is what the door would refuse to boot through.
    (env as { SANDBOXES?: unknown }).SANDBOXES = binding;
    return worker.fetch(request, env);
  },
};
