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
 * The wrapper adds exactly these, all harness scaffolding rather than worker
 * behaviour, and all named here so nobody mistakes them for the deployed
 * surface:
 *
 *   - the fake `SANDBOXES` binding below, substituted into `env` before every
 *     request is delegated, whose fake container answers the green-start
 *     probe and keeps its work process RUNNING — a worker genuinely mid-tick,
 *     the only state in which "the door returned without waiting for the
 *     attempt" and "a second start adopts it" mean anything;
 *   - the fake `WORKER_AGENTS` seam (tick yhe), substituted the same way when
 *     the harness was started with `--with-worker-agents` (a plain var binding
 *     the wrapper reads, so this suite's OTHER tests keep the unhosted door
 *     they are about): agents by name that record every `start`, so a Go test
 *     can assert the negative the door's own answers cannot express — that a
 *     claude-sub job routed to its container started no agent beside it;
 *   - `GET /__door_harness/sandboxes`, the harness's observation route, which
 *     answers what the fake bindings were asked to boot so the Go test
 *     can assert the negative the door's own answers cannot express: that a
 *     restarted orchestrator's second dispatch booted NO rival container;
 *   - `GET /__door_harness/claude-sub`, the same for the REAL claude-sub
 *     pool's leases (the pool itself is the deployed Durable Object, bound by
 *     run.mjs — only the container interception it installs is out of reach
 *     here), so a test can see a rung job's lease live and released.
 *     Nothing under `/__door_harness/` exists in the deployed Worker.
 */

import { claudeSubPool } from "../../src/claude-sub";
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
// a lease faked beside the door would prove nothing about the door; the
// claude-sub pool (tick yhe) must be the real one for the same reason — a
// lease faked beside it would prove nothing about the rung's cap. Sandbox is
// re-exported too because `src/index.ts` exports it; the harness binds no
// SANDBOXES namespace (the fake below plays that binding), so the class is
// present but never addressed.
export {
  ClaudeSubPool,
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
  /** Every claude-sub lease a boot asked this binding to install (tick 6fv). */
  readonly claudeSubBoots: { name: string; label: string; jobId: string }[] = [];

  async get(
    name: string,
    options?: { keepAlive?: boolean; claudeSub?: { label: string; jobId: string } },
  ): Promise<OrchestratorSandbox> {
    if (options?.claudeSub !== undefined) {
      this.claudeSubBoots.push({ name, ...options.claudeSub });
    }
    let sandbox = this.#byName.get(name);
    if (sandbox === undefined) {
      sandbox = new FakeSandbox(name);
      this.#byName.set(name, sandbox);
    }
    return sandbox;
  }

  /** Finishes the named container's live work process; false when it has none. */
  finish(name: string, code: number): boolean {
    const sandbox = this.#byName.get(name);
    const work = sandbox?.processes.find(
      (p) => p.command === WORKER_COMMAND && p.state === "running",
    );
    if (work === undefined) return false;
    work.finish(code);
    return true;
  }

  /** What the Go test reads back: every container ever addressed, every process it was asked to run, and every claude-sub interception a boot asked for (tick yhe). */
  describe(): {
    sandboxes: Array<{
      name: string;
      processes: Array<{
        id: string;
        command: string;
        state: string;
        exit_code: number | null;
        /** The boot environment the process was started with (tick 9iz): the harness and the role prompt a dispatch carried are proven delivered by reading them off the container the door booted. */
        env: Record<string, string>;
      }>;
    }>;
    /** Every claude-sub interception the door asked the binding to install, with the lease's label and job. */
    claude_sub_boots: Array<{ name: string; label: string; jobId: string }>;
  } {
    return {
      sandboxes: [...this.#byName.values()].map((sandbox) => ({
        name: sandbox.name,
        processes: sandbox.processes.map((p) => ({
          id: p.id,
          command: p.command,
          state: p.state,
          exit_code: p.exit_code,
          env: p.env,
        })),
      })),
      claude_sub_boots: [...this.claudeSubBoots],
    };
  }
}

const binding = new FakeSandboxes();

// -------------------------------------------------- the fake agent seam ---

/**
 * One fake WorkerAgent (tick yhe): a seam-shaped `{ agent(name) }` binding is
 * what the vitest suite binds and what `workerAgentsFromEnv` tells apart by
 * `idFromName`'s absence, so the wrapper plays the same shape. The agent
 * records every `start` — the assertion the door's own answers cannot make —
 * and answers its state as absent, which is the truth for a job routed to its
 * container: no agent holds it.
 */
class FakeAgent {
  readonly started: unknown[] = [];

  async start(spec: unknown): Promise<{ phase: string }> {
    this.started.push(spec);
    return { phase: "booting" };
  }
  async state(): Promise<{ phase: string }> {
    return { phase: "absent" };
  }
  async readLog(offset: number): Promise<SandboxOutput> {
    return { text: "", offset };
  }
  async steer(): Promise<{ ok: false; error: string }> {
    return { ok: false, error: "the attempt is not conversing" };
  }
  async reclaim(_reason: string): Promise<{ phase: string }> {
    return { phase: "settled" };
  }
  async release(): Promise<void> {}
  async fetch(_request: Request): Promise<Response> {
    const pair = new WebSocketPair();
    pair[1].accept();
    pair[1].send(JSON.stringify({ type: "state", state: { phase: "absent" } }));
    return new Response(null, { status: 101, webSocket: pair[0] });
  }
}

/** The WORKER_AGENTS seam: agents by name, created on first address. */
class FakeAgents {
  readonly #byName = new Map<string, FakeAgent>();

  agent(name: string): FakeAgent {
    let agent = this.#byName.get(name);
    if (agent === undefined) {
      agent = new FakeAgent();
      this.#byName.set(name, agent);
    }
    return agent;
  }

  /** Every `start` every agent was asked for, by name — the no-agent assertion. */
  describe(): Record<string, number> {
    const started: Record<string, number> = {};
    for (const [name, agent] of this.#byName) started[name] = agent.started.length;
    return started;
  }
}

const agents = new FakeAgents();

// ---------------------------------------------------------------- the door ---

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/__door_harness/sandboxes") {
      return Response.json({ ...binding.describe(), agents_started: agents.describe() });
    }
    if (url.pathname === "/__door_harness/claude-sub") {
      // The REAL pool's answer, exactly as /api/claude-sub serves it: labels
      // and their leases, never a token value.
      const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0]);
      if (pool === null) return Response.json({ labels: [], subscriptions: [] });
      return Response.json({ subscriptions: await pool.snapshot() });
    }
    if (url.pathname === "/__door_harness/finish" && request.method === "POST") {
      // The worker in one named container finishes, with the exit code the
      // test names — the only way a Go test can SETTLE a job the real door
      // booted, and so prove a settled job never answers for another.
      const name = url.searchParams.get("sandbox") ?? "";
      const code = Number(url.searchParams.get("code") ?? "0");
      if (!binding.finish(name, code)) {
        return new Response(`no live work process in ${name}`, { status: 404 });
      }
      return new Response(null, { status: 204 });
    }
    // The substitutions, applied per request exactly the way the worker's
    // own vitest suite applies them per test: the SANDBOXES seam takes a test
    // binding by design (`isSandboxNamespace` is the discriminator), and so
    // does the WORKER_AGENTS seam (`workerAgentsFromEnv` tells a
    // seam-shaped binding apart by `idFromName`) — bound only when run.mjs
    // was started with `--with-worker-agents`, so the suite's other tests
    // keep the unhosted door they are about.
    (env as { SANDBOXES?: unknown }).SANDBOXES = binding;
    if ((env as unknown as Record<string, unknown>).TICFAC_DOOR_HARNESS_WORKER_AGENTS === "1") {
      (env as { WORKER_AGENTS?: unknown }).WORKER_AGENTS = agents;
    }
    return worker.fetch(request, env);
  },
};
