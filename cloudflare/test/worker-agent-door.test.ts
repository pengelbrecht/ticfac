/**
 * The dispatch door for a run whose workers are WorkerAgents (epic 43y, tick
 * xd3): the start, state, watch and steer routes answer from the attempt's
 * agent, and the reclaim stops the agent before its container goes.
 *
 * Driven through `SELF.fetch` — the real routing and the real run-credential
 * gate — with the agent and the containers behind their seams
 * (`WORKER_AGENTS`, `SANDBOXES`, `SANDBOXES_V1`), the same rule
 * sandbox-dispatch.test.ts runs by. The agent's own behaviour is
 * worker-agent.test.ts's; the attempt's phases are the harness package's
 * (harness/test/worker-attempt-host.test.ts).
 */
import { env, SELF } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import jobProtocol from "../../contracts/job-protocol.json";
import { readWorkerLogTail } from "../src/artifacts";
import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import {
  CLAUDE_CODE_OAUTH_TOKEN,
  CLAUDE_SUB_PLACEHOLDER,
  claudeSubPool,
  TICKS_CLAUDE_SUB,
  TOKEN_SECRET_PREFIX,
} from "../src/claude-sub";
import { reclaimRunWorkers } from "../src/container-capacity";
import { insertRun, type Run } from "../src/db";
import { issueWorkerRunToken } from "../src/gateway";
import { recordRunSubstrate } from "../src/run-substrate";
import { roomFor } from "../src/runs";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import { attemptJobID, attemptSandboxName, type SandboxJobHandle } from "../src/sandbox-executor";
import {
  WORKER_AGENT_HARNESS,
  type WorkerAgentState,
  type WorkerAgentSteer,
  type WorkerAgentStub,
  workerAgentsFromEnv,
} from "../src/worker-agent";
import { WORKER_COMMAND, WORKER_PROBE_MARKER, WORKER_PUSH_MARGIN_MS } from "../src/worker-boot";
import { parseDefs, parseSchema, validate } from "./json-schema";

// ------------------------------------------------------------- the fakes ---

type StartedSpec = Parameters<WorkerAgentStub["start"]>[0];

/** One attempt's WorkerAgent, scripted: its phase, its exit code, its log. */
class FakeAgent implements WorkerAgentStub {
  readonly started: StartedSpec[] = [];
  readonly steers: { text: string; requestId?: string }[] = [];
  readonly reclaimed: string[] = [];
  phase: WorkerAgentState["phase"] = "absent";
  exitCode: number | null = null;
  conversing = false;
  log = "";
  released = 0;
  stateReads = 0;
  model: string | null = null;

  async start(spec: StartedSpec): Promise<WorkerAgentState> {
    this.started.push(spec);
    // A settled attempt is started afresh, as the real agent does.
    if (this.phase === "absent" || this.phase === "settled") {
      this.phase = "booting";
      this.exitCode = null;
      this.log = "";
      this.model = spec.model;
    }
    return this.state();
  }
  async state(): Promise<WorkerAgentState> {
    this.stateReads += 1;
    return {
      phase: this.phase,
      exit_code: this.exitCode,
      detail: null,
      settled_at: this.phase === "settled" ? new Date().toISOString() : null,
      model: this.model,
      branch: null,
      started_at: null,
      harness: WORKER_AGENT_HARNESS,
    };
  }
  async readLog(offset: number): Promise<SandboxOutput> {
    return { text: this.log.slice(offset), offset: this.log.length };
  }
  async steer(text: string, requestId?: string): Promise<WorkerAgentSteer> {
    if (!this.conversing) return { ok: false, error: "the attempt is not conversing" };
    this.steers.push({ text, ...(requestId === undefined ? {} : { requestId }) });
    return { ok: true, submission: 7 };
  }
  async reclaim(reason: string): Promise<WorkerAgentState> {
    this.reclaimed.push(reason);
    this.phase = "settled";
    return this.state();
  }
  async release(): Promise<void> {
    this.released += 1;
  }
  async fetch(_request: Request): Promise<Response> {
    const pair = new WebSocketPair();
    pair[1].accept();
    pair[1].send(JSON.stringify({ type: "state", state: { phase: this.phase } }));
    return new Response(null, { status: 101, webSocket: pair[0] });
  }
}

/** The WORKER_AGENTS seam: agents by name, created on first address. */
class FakeAgents {
  readonly byName = new Map<string, FakeAgent>();
  agent(name: string): FakeAgent {
    let agent = this.byName.get(name);
    if (agent === undefined) {
      agent = new FakeAgent();
      this.byName.set(name, agent);
    }
    return agent;
  }
}

/** One process inside a fake container. */
class DoorProcess {
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
 * work process prints and KEEPS RUNNING — the shape a dispatched worker
 * genuinely mid-tick is, ported from sandbox-dispatch.test.ts's FakeSandbox so
 * the two suites cannot disagree about what a container's own rules are.
 */
class DoorSandbox implements OrchestratorSandbox {
  readonly processes: DoorProcess[] = [];
  destroyed = false;
  running = true;
  #next = 0;

  constructor(
    readonly name: string,
    /** Records the destroy, so a test can assert what was reclaimed. */
    readonly onDestroy: (name: string) => void,
  ) {}

  async startProcess(
    command: string,
    options?: { env: Record<string, string> },
  ): Promise<SandboxProcessView> {
    const process = new DoorProcess(`${this.name}-p${++this.#next}`, command, options?.env ?? {});
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
    if (this.destroyed)
      throw new Error(`listProcesses cold-booted destroyed container ${this.name}`);
    if (!this.running) throw new Error(`listProcesses cold-booted stopped container ${this.name}`);
    return this.processes.map((p) => ({ ...p.view }));
  }

  async isRunning(): Promise<boolean> {
    return this.running && !this.destroyed;
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
    this.onDestroy(this.name);
  }

  /** The work process, if this container has one. */
  workProcess(): DoorProcess | undefined {
    return this.processes.find((p) => p.command === WORKER_COMMAND);
  }
}

/** The binding: containers by name, provisioned on first address. */
class RecordingSandboxes implements SandboxBinding {
  readonly #byName = new Map<string, DoorSandbox>();
  /** The names ever addressed — a second boot of a fresh name is a rival. */
  readonly addressed: string[] = [];
  readonly destroyed: string[] = [];
  /** Every claude-sub lease a boot asked the binding to install (tick 6fv). */
  readonly claudeSubBoots: { name: string; label: string; jobId: string }[] = [];

  async get(
    name: string,
    options?: { keepAlive?: boolean; claudeSub?: { label: string; jobId: string } },
  ): Promise<OrchestratorSandbox> {
    this.addressed.push(name);
    if (options?.claudeSub !== undefined) {
      this.claudeSubBoots.push({ name, ...options.claudeSub });
    }
    let sandbox = this.#byName.get(name);
    if (sandbox === undefined) {
      sandbox = new DoorSandbox(name, (destroyed) => this.destroyed.push(destroyed));
      this.#byName.set(name, sandbox);
    }
    return sandbox;
  }

  /** The named container, or the refusal that no code path may take. */
  named(name: string): DoorSandbox {
    const sandbox = this.#byName.get(name);
    if (sandbox === undefined) throw new Error(`no sandbox named ${name} was addressed`);
    return sandbox;
  }
}

// ------------------------------------------------------------ the harness ---

const BASE = "https://factory.example.com";
const EPIC = "43y";
const BASE_SHA = "b1c2d3e4f5a6b1c2d3e4f5a6b1c2d3e4f5a6b1c2";
const TICK = "xd3";
const MODEL = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash";
const PROMPT = "# implement-tick\n\nImplement the tick — end with a STATUS line.";
const PINNED = `registry.example.com/factory@sha256:${"c".repeat(64)}`;

const protocolDefs = parseDefs((jobProtocol as { $defs: unknown }).$defs);
const jobHandleSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_handle,
  "$",
);
const jobStatusSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_status,
  "$",
);

let RUN_ID = "";
let project = "";
let runCounter = 0;
let runToken = "";
let agents: FakeAgents;
let sandboxes: RecordingSandboxes;
let sandboxesV1: RecordingSandboxes;
const saved: Record<string, unknown> = {};

function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
  else (env as unknown as Record<string, unknown>)[name] = value;
}

/** A live run holding its project's lease, on the do_v1 substrate unless told otherwise. */
async function liveRun(substrate: "do_v1" | "sdk0" = "do_v1"): Promise<void> {
  runCounter += 1;
  RUN_ID = `run_xd3_${runCounter}`;
  project = `example-org/example-agent-repo-${runCounter}`;
  const run: Run = {
    run_id: RUN_ID,
    project,
    epic: EPIC,
    base_sha: BASE_SHA,
    requested_by: "operator",
    state: "running",
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
  await insertRun(env.DB, run);
  if (substrate === "do_v1") await recordRunSubstrate(env.DB, RUN_ID, "do_v1", PINNED);
  const lease = await roomFor(env, run.project).acquireDispatchLease({
    run_id: RUN_ID,
    epic: EPIC,
    origin: "cloud",
  });
  if (!lease.ok) throw new Error(`the lease was refused: ${JSON.stringify(lease)}`);
  runToken = (await issueWorkerRunToken(env, { run_id: RUN_ID, tick_id: EPIC, attempt: 1 })).token;
}

function startBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    epic: EPIC,
    tick_id: TICK,
    attempt: 1,
    role: "implement-tick",
    write_ref: `refs/heads/ticfac/run-${RUN_ID}/tick-${TICK}/attempt-1`,
    base_ref: `refs/heads/epic/${EPIC}`,
    title: "Cloud host: a WorkerAgent DO per attempt",
    base_sha: BASE_SHA,
    model: MODEL,
    harness: "pi-durable",
    prompt: PROMPT,
    ...overrides,
  };
}

function postStart(body: Record<string, unknown>): Promise<Response> {
  return SELF.fetch(`${BASE}/api/sandbox/attempts`, {
    method: "POST",
    headers: { authorization: `Bearer ${runToken}`, "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

function getState(attempt = 1): Promise<Response> {
  return SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/${attempt}`, {
    headers: { authorization: `Bearer ${runToken}` },
  });
}

function postSteer(body: Record<string, unknown>, token: string | null = runToken) {
  return SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/steer`, {
    method: "POST",
    headers: {
      ...(token === null ? {} : { authorization: `Bearer ${token}` }),
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

/** The attempt's agent, by the name the door derives. */
function agentOf(attempt = 1): FakeAgent {
  return agents.agent(attemptSandboxName(RUN_ID, TICK, attempt));
}

beforeEach(async () => {
  agents = new FakeAgents();
  sandboxes = new RecordingSandboxes();
  sandboxesV1 = new RecordingSandboxes();
  set("WORKER_AGENTS", agents);
  set("SANDBOXES", sandboxes);
  set("SANDBOXES_V1", sandboxesV1);
  set("FACTORY_MAX_INSTANCES", "100000");
  set("FACTORY_BASE_URL", BASE);
  set("GITHUB_TOKEN", "gh-operator-test-token");
  await liveRun();
});

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

// ------------------------------------------------------------------ start ---

describe("the start route on a run whose workers are WorkerAgents", () => {
  it("records the attempt on its agent — the boot env, the model, the wall, the run's image pin — and addresses no container", async () => {
    const response = await postStart(startBody({ wall_seconds: 3600 }));
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(false);
    expect(validate(jobHandleSchema, protocolDefs, body.handle)).toEqual([]);
    expect(body.handle.handle.process_id).toBeNull();
    expect(body.handle.handle.model).toBe(MODEL);
    // The handle names the harness the attempt RUNS on (tick 4uj): the
    // agent's own, not the container harness the dispatch resolved — the
    // attempt is the agent's, and provenance names what ran.
    expect(body.handle.handle.harness).toBe(WORKER_AGENT_HARNESS);

    const agent = agentOf();
    expect(agent.started.length).toBe(1);
    const spec = agent.started[0]!;
    expect(spec.name).toBe(attemptSandboxName(RUN_ID, TICK, 1));
    expect(spec.tick).toBe(TICK);
    expect(spec.role).toBe("implement-tick");
    expect(spec.model).toBe(MODEL);
    expect(spec.baseSha).toBe(BASE_SHA);
    expect(spec.env.TICKS_TICK).toBe(TICK);
    expect(spec.env.TICKS_ROLE_PROMPT).toBe(PROMPT);
    expect(spec.env.AI_GATEWAY_BASE_URL).toBe(`${BASE}/api/gateway`);
    expect(spec.env.AI_GATEWAY_TOKEN).toMatch(/.+/);
    expect(spec.wallMs).toBe(3600 * 1000 - WORKER_PUSH_MARGIN_MS);
    expect(spec.boot).toEqual({ keepAlive: true, instance: "standard-4", pinnedImage: PINNED });

    // The attempt's container is the agent's to boot; the door asked none.
    expect(sandboxes.addressed).toEqual([]);
    expect(sandboxesV1.addressed).toEqual([]);
  });

  it("boots the container's halves on the hosted kind whatever harness the dispatch named (tick jhp)", async () => {
    // The pi CLI is deleted from the image, and the container's kind set
    // refuses `pi`: a dispatch from a client whose profiles still say
    // runner "pi" (or any CLI) must not reach the boot env, or the hosted
    // attempt dies at its own --boot. The container runs only the hosted
    // halves, so it is told the hosted kind.
    for (const [attempt, harness] of [
      [1, "pi"],
      [2, "omp"],
    ] as const) {
      const response = await postStart(startBody({ attempt, harness }));
      expect(response.status).toBe(201);
      const spec = agentOf(attempt).started[0]!;
      expect(spec.env.TICKS_HARNESS).toBe(WORKER_AGENT_HARNESS);
    }
  });

  it("adopts an attempt its agent already holds: one start, the recorded model", async () => {
    expect((await postStart(startBody())).status).toBe(201);
    const again = await postStart(startBody({ model: "workers-ai/@cf/zai-org/glm-5.3" }));
    expect(again.status).toBe(200);
    const body = (await again.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(true);
    expect(body.handle.handle.model).toBe(MODEL);
    expect(agentOf().started.length).toBe(1);
  });

  it("starts a settled attempt afresh — running again, not its old settlement", async () => {
    expect((await postStart(startBody())).status).toBe(201);
    const agent = agentOf();
    agent.phase = "settled";
    agent.exitCode = 1;
    expect(((await (await getState()).json()) as { state: string }).state).toBe("failed");

    const again = await postStart(startBody());
    expect(again.status).toBe(201);
    expect(((await again.json()) as { adopted: boolean }).adopted).toBe(false);
    expect(agent.started.length).toBe(2);
    expect(((await (await getState()).json()) as { state: string }).state).toBe("running");
  });

  it("hosts a run on the 0.x substrate too: the attempt is the agent's, and the boot asks only what that substrate can honor", async () => {
    await liveRun("sdk0");
    const response = await postStart(startBody());
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(false);

    const agent = agentOf();
    expect(agent.started.length).toBe(1);
    const spec = agent.started[0]!;
    // The 0.x application has one image and one instance size for every
    // boot: keepAlive is the whole of the boot, no pin and no per-job size.
    expect(spec.boot).toEqual({ keepAlive: true });

    // The attempt's container is the agent's to boot; the door asked none —
    // and not the 0.x binding either, which is where its tools will run.
    expect(sandboxes.addressed).toEqual([]);
    expect(sandboxesV1.addressed).toEqual([]);

    // And the state route answers from the agent, not the container — the
    // 0.x path's own cold-boot-on-a-read hazard gone with the container's
    // all-in-one worker.
    const state = await getState();
    expect(state.status).toBe(200);
    expect(((await state.json()) as { state: string }).state).toBe("running");
  });
});

// --------------------------------------------------- the claude-sub rung ---

describe("a claude-sub job on a hosted deployment (tick yhe)", () => {
  // The rung under a bound WORKER_AGENTS (production's shape): a dispatch
  // that resolves claude on a versionless alias and leases a subscription is
  // the CONTAINER's own all-in-one claude worker under the interception —
  // never its agent's. The agent hosts pi-durable conversations on the
  // factory's gateway, which serves no claude alias, and the container halves
  // it drives are booted on the hosted kind, which image/common.sh dies on
  // under the claude-sub marker — so before this routing, every rung job on a
  // hosted deployment died at its own boot with its handle naming
  // pi-durable/sonnet, a pairing no client's cloud billing rule admits, and
  // its lease held a cap slot until the TTL. The Go executor's acceptance is
  // the pairing this handle must name: claude/sonnet, the rung the door
  // leased (the client's own predicate is pinned on its side,
  // TestAStartOnTheSubscriptionRungIsAccepted).
  it("boots a rung dispatch on the subscription as the container's own claude worker, not its agent", async () => {
    set(`${TOKEN_SECRET_PREFIX}MAX1`, "sk-ant-oat01-not-a-real-token");
    const response = await postStart(startBody({ harness: "claude", model: "sonnet" }));
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(false);
    expect(validate(jobHandleSchema, protocolDefs, body.handle)).toEqual([]);
    // The pairing the cloud billing rule admits — the rung's own, never the
    // agent's harness over an alias the gateway cannot serve.
    expect(body.handle.handle.harness).toBe("claude");
    expect(body.handle.handle.model).toBe("sonnet");
    // The process is the container's, so the handle addresses one.
    expect(body.handle.handle.process_id).not.toBeNull();

    // The agent was never asked to host it: the door may READ its state for
    // the adoption check, but no WorkerAgent was STARTED for this attempt —
    // the subscription's claude CLI runs in the container.
    expect([...agents.byName.values()].flatMap((agent) => agent.started)).toEqual([]);

    // The container's own worker: the claude CLI under the interception,
    // with the placeholder only — the subscription's token never enters the
    // container.
    const name = attemptSandboxName(RUN_ID, TICK, 1);
    const work = sandboxesV1.named(name).workProcess();
    expect(work).toBeDefined();
    expect(work?.env.TICKS_HARNESS).toBe("claude");
    expect(work?.env.TICKS_MODEL).toBe("sonnet");
    expect(work?.env[CLAUDE_CODE_OAUTH_TOKEN]).toBe(CLAUDE_SUB_PLACEHOLDER);
    expect(work?.env[TICKS_CLAUDE_SUB]).toBe("1");
    expect(JSON.stringify(work?.env)).not.toContain("sk-ant");

    // The interception install was asked of the binding, with the lease's
    // label and the job id the pool keyed the lease under.
    expect(sandboxesV1.claudeSubBoots).toEqual([
      { name, label: "MAX1", jobId: attemptJobID(RUN_ID, TICK, 1) },
    ]);

    // The lease is live: one job under the subscription, addressed by the
    // attempt's job id. The pool's storage outlives this test, so the lease
    // is released for the next one.
    const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0])!;
    const snapshot = await pool.snapshot();
    expect(snapshot[0]!.label).toBe("MAX1");
    expect(snapshot[0]!.active_leases).toEqual([attemptJobID(RUN_ID, TICK, 1)]);
    await pool.release(attemptJobID(RUN_ID, TICK, 1));
  });

  // No subscription is free — none configured here — and the rung dispatch
  // steps down to the deployment's Workers AI pair, which IS the agent's to
  // host: the agent starts on the stepped-down model, its container's halves
  // are booted on the hosted kind, no claude-sub marker or interception
  // reaches anything, and the handle names what the agent runs. The routing
  // must not send a stepped-down boot to the container, whose image dies on
  // the hosted kind.
  it("steps a rung dispatch with no free subscription down to the hosted Workers AI pair", async () => {
    const response = await postStart(startBody({ harness: "claude", model: "sonnet" }));
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle };
    expect(body.handle.handle.harness).toBe(WORKER_AGENT_HARNESS);
    expect(body.handle.handle.model).toBe("workers-ai/@cf/zai-org/glm-5.3");

    const agent = agentOf();
    expect(agent.started.length).toBe(1);
    const spec = agent.started[0]!;
    expect(spec.model).toBe("workers-ai/@cf/zai-org/glm-5.3");
    expect(spec.env.TICKS_HARNESS).toBe(WORKER_AGENT_HARNESS);
    expect(spec.env[CLAUDE_CODE_OAUTH_TOKEN]).toBeUndefined();
    expect(spec.env[TICKS_CLAUDE_SUB]).toBeUndefined();

    // The door asked no container, and no interception was installed.
    expect(sandboxesV1.addressed).toEqual([]);
    expect(sandboxesV1.claudeSubBoots).toEqual([]);
  });

  // An attempt the agent already holds is ADOPTED there, whatever a later
  // dispatch resolved — never a second worker beside the agent's conversation.
  // When that later dispatch leased a subscription (one freed up between the
  // two), the lease is not the attempt's to hold: the agent's attempt is a
  // pi-durable conversation, not the claude-sub job the lease exists for, so
  // the cap slot goes back with the refusal-free adoption rather than sitting
  // held until the lease's TTL.
  it("adopts the attempt its agent holds even when the re-ask leased a subscription, and gives the cap slot back", async () => {
    // No subscription configured: the first ask steps down, hosted.
    expect((await postStart(startBody({ harness: "claude", model: "sonnet" }))).status).toBe(201);
    const agent = agentOf();
    expect(agent.started.length).toBe(1);

    // A subscription appears; the same attempt is asked again.
    set(`${TOKEN_SECRET_PREFIX}MAX1`, "sk-ant-oat01-not-a-real-token");
    const again = await postStart(startBody({ harness: "claude", model: "sonnet" }));
    expect(again.status).toBe(200);
    const body = (await again.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(true);
    expect(body.handle.handle.harness).toBe(WORKER_AGENT_HARNESS);
    expect(body.handle.handle.model).toBe("workers-ai/@cf/zai-org/glm-5.3");

    // One start, one conversation: no second worker beside the agent's, no
    // container worker booted under this attempt's name.
    expect(agent.started.length).toBe(1);
    expect(sandboxesV1.addressed).toEqual([]);
    expect(sandboxesV1.claudeSubBoots).toEqual([]);

    // And the lease the re-ask took is not held: the attempt it adopted is
    // not a claude-sub job, so the subscription's cap is free again.
    const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0])!;
    expect((await pool.snapshot())[0]!.active_leases).toEqual([]);
  });

  // A rung worker already running in its container is adopted there, on the
  // pairing its recorded boot names: the sticky lease answers the re-ask
  // with the same subscription, and no second worker starts beside the live
  // one — the rule that held for the container door before hosting, restated
  // for the one job a hosted deployment still runs in its container.
  it("adopts a rung worker already running in its container", async () => {
    set(`${TOKEN_SECRET_PREFIX}MAX1`, "sk-ant-oat01-not-a-real-token");
    expect((await postStart(startBody({ harness: "claude", model: "sonnet" }))).status).toBe(201);
    const name = attemptSandboxName(RUN_ID, TICK, 1);
    const first = sandboxesV1.named(name).workProcess();
    expect(first).toBeDefined();

    const again = await postStart(startBody({ harness: "claude", model: "sonnet" }));
    expect(again.status).toBe(200);
    const body = (await again.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(true);
    // The recorded boot's model, the fresh boot's harness: the rung's pair.
    expect(body.handle.handle.model).toBe("sonnet");
    expect(body.handle.handle.harness).toBe("claude");
    expect(body.handle.handle.process_id).toBe(first!.id);

    // Still one work process, and the lease is still held under it.
    const sandbox = sandboxesV1.named(name);
    expect(sandbox.processes.filter((p) => p.command === WORKER_COMMAND)).toHaveLength(1);
    const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0])!;
    expect((await pool.snapshot())[0]!.active_leases).toEqual([attemptJobID(RUN_ID, TICK, 1)]);
    await pool.release(attemptJobID(RUN_ID, TICK, 1));
  });

  // The state route follows the start's routing: a rung job's container is
  // read as the container it is, never through an agent that holds nothing —
  // the read that would have answered `lost` for every rung job on a hosted
  // deployment, for as long as the worker ran.
  it("reads a rung worker's state from its container: running, then settled, and the lease ends with it", async () => {
    set(`${TOKEN_SECRET_PREFIX}MAX1`, "sk-ant-oat01-not-a-real-token");
    expect((await postStart(startBody({ harness: "claude", model: "sonnet" }))).status).toBe(201);
    const name = attemptSandboxName(RUN_ID, TICK, 1);

    const running = await getState();
    expect(running.status).toBe(200);
    const status = (await running.json()) as Record<string, unknown>;
    expect(validate(jobStatusSchema, protocolDefs, status)).toEqual([]);
    expect(status.state).toBe("running");
    expect(status.terminal).toBe(false);

    // The worker settles with a failure: read from the container, with its
    // own exit code — and the settlement reclaims the container and ends the
    // lease, exactly as a container job's does on a deployment that binds no
    // WORKER_AGENTS.
    sandboxesV1.named(name).workProcess()!.finish(7);
    const settled = (await (await getState()).json()) as {
      state: string;
      terminal: boolean;
      observations: { detail: string }[];
    };
    expect(settled.state).toBe("failed");
    expect(settled.terminal).toBe(true);
    expect(settled.observations[0]?.detail).toContain("exited 7");
    expect(sandboxesV1.destroyed).toContain(name);
    const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0])!;
    expect((await pool.snapshot())[0]!.active_leases).toEqual([]);
  });

  // The run-end reclaim asks the rung worker's container to stop and push —
  // through the agent only for the attempts the agent hosts. A rung job
  // destroyed unasked would lose its unpushed tail for no reason: the agent
  // holds nothing of it, and the container's own salvage door is the one
  // every container worker gets.
  it("asks a rung worker's container to stop and push at the run's end, not an agent that holds nothing", async () => {
    set(`${TOKEN_SECRET_PREFIX}MAX1`, "sk-ant-oat01-not-a-real-token");
    expect((await postStart(startBody({ harness: "claude", model: "sonnet" }))).status).toBe(201);
    const name = attemptSandboxName(RUN_ID, TICK, 1);

    const agents = workerAgentsFromEnv(env);
    const reclaimed = await reclaimRunWorkers(env.DB, sandboxesV1, RUN_ID, {
      reason: "run_ended:failed",
      graceMs: 0,
      ...(agents === undefined ? {} : { agents }),
    });
    expect(reclaimed.length).toBe(1);
    expect(reclaimed[0]?.detail).toContain("asked to stop and push");
    expect(reclaimed[0]?.detail).not.toContain("WorkerAgent");
    // The salvage door ran in the container: the cancel command started a
    // second process beside the live work one.
    const sandbox = sandboxesV1.named(name);
    expect(sandbox.processes.some((p) => p.command.includes("--cancel"))).toBe(true);
    expect(sandbox.destroyed).toBe(true);

    const pool = claudeSubPool(env as unknown as Parameters<typeof claudeSubPool>[0])!;
    await pool.release(attemptJobID(RUN_ID, TICK, 1));
  });
});

// ------------------------------------------------------------------ state ---

describe("the state route on a hosted attempt", () => {
  it("answers running while the agent drives, and copies its log on every look", async () => {
    await postStart(startBody());
    const agent = agentOf();
    agent.log = "ticfac-harness: booted\n";
    const running = await getState();
    expect(running.status).toBe(200);
    const status = (await running.json()) as Record<string, unknown>;
    expect(validate(jobStatusSchema, protocolDefs, status)).toEqual([]);
    expect(status.state).toBe("running");
    expect(status.terminal).toBe(false);

    agent.log += "ticfac-harness: tool bash: make test\n";
    await getState();
    const tail = await readWorkerLogTail(env.ARTIFACTS, project, RUN_ID, TICK);
    expect(tail.text).toContain("ticfac-harness: booted\nticfac-harness: tool bash: make test\n");
    expect(tail.text.split("booted").length - 1).toBe(1);
    expect(sandboxesV1.addressed).toEqual([]);
  });

  it("answers the settled attempt's exit code, records it, and releases its container once", async () => {
    await postStart(startBody());
    const agent = agentOf();
    agent.phase = "settled";
    agent.exitCode = 10;
    const first = (await (await getState()).json()) as {
      state: string;
      terminal: boolean;
      observations: { detail: string }[];
    };
    expect(first.state).toBe("failed");
    expect(first.terminal).toBe(true);
    expect(first.observations[0]?.detail).toContain("exited 10");
    expect(agent.released).toBe(1);

    // The settlement is recorded: the next read answers from it, asking the
    // agent nothing and releasing nothing twice.
    const reads = agent.stateReads;
    const second = (await (await getState()).json()) as { state: string };
    expect(second.state).toBe("failed");
    expect(agent.stateReads).toBe(reads);
    expect(agent.released).toBe(1);
  });

  it("answers a clean finish as succeeded", async () => {
    await postStart(startBody());
    const agent = agentOf();
    agent.phase = "settled";
    agent.exitCode = 0;
    const status = (await (await getState()).json()) as { state: string };
    expect(status.state).toBe("succeeded");
  });

  it("answers lost — not terminal — for a booted attempt its agent never recorded", async () => {
    await postStart(startBody());
    // The start died between the boot record and the agent: the agent holds nothing.
    agentOf().phase = "absent";
    const status = (await (await getState()).json()) as { state: string; terminal: boolean };
    expect(status.state).toBe("lost");
    expect(status.terminal).toBe(false);
  });
});

// ---------------------------------------------------------- watch and steer ---

describe("the steer route", () => {
  it("places the text on the conversing attempt", async () => {
    await postStart(startBody());
    agentOf().conversing = true;
    const response = await postSteer({ text: "also add a docstring", request_id: "steer-1" });
    expect(response.status).toBe(202);
    expect(await response.json()).toEqual({ submission: 7 });
    expect(agentOf().steers).toEqual([{ text: "also add a docstring", requestId: "steer-1" }]);
  });

  it("refuses when nothing is conversing, when the run is not hosted, and without the credential", async () => {
    await postStart(startBody());
    const idle = await postSteer({ text: "hello" });
    expect(idle.status).toBe(409);
    expect(((await idle.json()) as { error: string }).error).toBe("not_conversing");

    expect((await postSteer({ text: "hello" }, null)).status).toBe(401);
    expect((await postSteer({ text: "" })).status).toBe(400);

    // A deployment that binds no WORKER_AGENTS hosts nothing on either
    // substrate (tick hxd): the refusal is the binding's, never the run's.
    set("WORKER_AGENTS", undefined);
    await liveRun();
    const unhosted = await postSteer({ text: "hello" });
    expect(unhosted.status).toBe(409);
    expect(((await unhosted.json()) as { error: string }).error).toBe("not_hosted");
  });
});

describe("the watch route", () => {
  it("hands the agent's WebSocket through on an upgrade, and refuses a plain GET", async () => {
    await postStart(startBody());
    const plain = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/watch`, {
      headers: { authorization: `Bearer ${runToken}` },
    });
    expect(plain.status).toBe(426);

    const upgraded = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/watch`, {
      headers: { authorization: `Bearer ${runToken}`, upgrade: "websocket" },
    });
    expect(upgraded.status).toBe(101);
    const socket = upgraded.webSocket;
    if (socket === null) throw new Error("no socket");
    const first = new Promise<string>((resolve) => {
      socket.addEventListener("message", (event) => resolve(String(event.data)));
    });
    socket.accept();
    expect(JSON.parse(await first)).toEqual({ type: "state", state: { phase: "booting" } });
    socket.close();
  });

  it("refuses an upgrade without the run credential", async () => {
    const response = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/watch`, {
      headers: { upgrade: "websocket" },
    });
    expect(response.status).toBe(401);
  });
});

// ------------------------------------------------- the operator's window ---

describe("the operator's worker routes (tick y03)", () => {
  let operatorToken = "";
  beforeEach(async () => {
    operatorToken = mintFactoryToken();
    set("FACTORY_TOKEN_HASH", await deriveTokenHash(operatorToken));
  });

  function operator(path: string, init: RequestInit = {}, token: string | null = operatorToken) {
    return SELF.fetch(`${BASE}/api/runs/${RUN_ID}/workers/${path}`, {
      ...init,
      headers: {
        ...(token === null ? {} : { authorization: `Bearer ${token}` }),
        ...(init.headers as Record<string, string> | undefined),
      },
    });
  }

  it("answers the newest booted attempt's state for `latest`, and a numbered one as asked", async () => {
    expect((await postStart(startBody())).status).toBe(201);
    expect((await postStart(startBody({ attempt: 2 }))).status).toBe(201);
    agents.agent(attemptSandboxName(RUN_ID, TICK, 2)).phase = "conversing";

    const latest = await operator(`${TICK}/latest`);
    expect(latest.status).toBe(200);
    const body = (await latest.json()) as {
      attempt: number;
      name: string;
      state: WorkerAgentState;
    };
    expect(body.attempt).toBe(2);
    expect(body.name).toBe(attemptSandboxName(RUN_ID, TICK, 2));
    expect(body.state.phase).toBe("conversing");

    const first = (await (await operator(`${TICK}/1`)).json()) as {
      attempt: number;
      state: WorkerAgentState;
    };
    expect(first.attempt).toBe(1);
    expect(first.state.phase).toBe("booting");
  });

  it("steers the attempt's conversation on the operator's token, and only on it", async () => {
    await postStart(startBody());
    agentOf().conversing = true;
    const steer = (token: string | null) =>
      operator(
        `${TICK}/latest/steer`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ text: "write the report next", request_id: "operator-steer-1" }),
        },
        token,
      );
    expect((await steer(null)).status).toBe(401);
    // The run's own credential is not the operator's: this window is the
    // person's, never a worker's.
    expect((await steer(runToken)).status).toBe(401);
    const placed = await steer(operatorToken);
    expect(placed.status).toBe(202);
    expect(await placed.json()).toEqual({ submission: 7 });
    expect(agentOf().steers).toEqual([
      { text: "write the report next", requestId: "operator-steer-1" },
    ]);

    agentOf().conversing = false;
    const idle = await steer(operatorToken);
    expect(idle.status).toBe(409);
    expect(((await idle.json()) as { error: string }).error).toBe("not_conversing");
  });

  it("hands the agent's watch socket through on an upgrade", async () => {
    await postStart(startBody());
    expect((await operator(`${TICK}/1/watch`)).status).toBe(426);
    const upgraded = await operator(`${TICK}/1/watch`, { headers: { upgrade: "websocket" } });
    expect(upgraded.status).toBe(101);
    const socket = upgraded.webSocket;
    if (socket === null) throw new Error("no socket");
    const first = new Promise<string>((resolve) => {
      socket.addEventListener("message", (event) => resolve(String(event.data)));
    });
    socket.accept();
    expect(JSON.parse(await first)).toEqual({ type: "state", state: { phase: "booting" } });
    socket.close();
  });

  it("refuses an unknown run, a tick with no booted worker, and a run whose workers are not hosted", async () => {
    const unknown = await SELF.fetch(`${BASE}/api/runs/run_nope/workers/${TICK}/latest`, {
      headers: { authorization: `Bearer ${operatorToken}` },
    });
    expect(unknown.status).toBe(404);

    const none = await operator(`${TICK}/latest`);
    expect(none.status).toBe(404);
    expect(((await none.json()) as { error: string }).error).toBe("no_attempt");

    expect((await operator(`${TICK}/1/dance`)).status).toBe(404);

    // Every run's workers are WorkerAgents on a deployment that binds them,
    // on either substrate (tick hxd): unhosted is the binding's absence.
    set("WORKER_AGENTS", undefined);
    await liveRun();
    const unhosted = await operator(`${TICK}/1`);
    expect(unhosted.status).toBe(409);
    expect(((await unhosted.json()) as { error: string }).error).toBe("not_hosted");
  });
});

// ---------------------------------------------------------------- reclaim ---

describe("the reclaim of a hosted worker", () => {
  it("stops the agent with the reason before its container is destroyed", async () => {
    await postStart(startBody());
    const agents = workerAgentsFromEnv(env);
    const reclaimed = await reclaimRunWorkers(env.DB, sandboxesV1, RUN_ID, {
      reason: "run_ended:failed",
      graceMs: 0,
      ...(agents === undefined ? {} : { agents }),
    });
    expect(agentOf().reclaimed).toEqual(["run_ended:failed"]);
    expect(sandboxesV1.destroyed).toEqual([attemptSandboxName(RUN_ID, TICK, 1)]);
    expect(reclaimed.length).toBe(1);
    expect(reclaimed[0]?.detail).toContain("WorkerAgent was stopped");
  });
});
