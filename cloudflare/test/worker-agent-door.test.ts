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
import { reclaimRunWorkers } from "../src/container-capacity";
import { insertRun, type Run } from "../src/db";
import { issueWorkerRunToken } from "../src/gateway";
import { recordRunSubstrate } from "../src/run-substrate";
import { roomFor } from "../src/runs";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessView,
} from "../src/sandbox";
import { attemptSandboxName, type SandboxJobHandle } from "../src/sandbox-executor";
import {
  WORKER_AGENT_HARNESS,
  type WorkerAgentState,
  type WorkerAgentSteer,
  type WorkerAgentStub,
  workerAgentsFromEnv,
} from "../src/worker-agent";
import { WORKER_PUSH_MARGIN_MS } from "../src/worker-boot";
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

/** A container seam that records every address — a hosted attempt must address none. */
class RecordingSandboxes implements SandboxBinding {
  readonly addressed: string[] = [];
  readonly destroyed: string[] = [];
  async get(name: string): Promise<OrchestratorSandbox> {
    this.addressed.push(name);
    const destroyed = this.destroyed;
    return {
      async startProcess(command: string): Promise<SandboxProcessView> {
        return { id: "p1", state: "running", exit_code: null, command };
      },
      async getProcess() {
        return null;
      },
      async listProcesses() {
        return [];
      },
      async readOutput(_id: string, offset: number) {
        return { text: "", offset };
      },
      async killProcess() {},
      async destroy() {
        destroyed.push(name);
      },
      async isRunning() {
        return false;
      },
    };
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
    harness: "pi",
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
    // The handle names the harness the dispatch resolved, as every start does.
    expect(body.handle.handle.harness).toBe("pi");

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

  it("leaves a run on the 0.x substrate to its container, agents bound or not", async () => {
    await liveRun("sdk0");
    // The fake container never confirms a work process; the start's own
    // outcome is not this test's — only where the door went.
    await postStart(startBody());
    expect(agents.byName.size).toBe(0);
    expect(sandboxes.addressed.length).toBeGreaterThan(0);
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

    await liveRun("sdk0");
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
