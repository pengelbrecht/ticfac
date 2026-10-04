/**
 * The WorkerAgent Durable Object (epic 43y, tick xd3), end to end through the
 * dispatch door: the REAL class, on its real DO SQLite (pi-durable's storage,
 * the host's record, the log table), started by the door's start route and
 * answered by its state, watch and steer routes.
 *
 * Two seams, set on the live instance before the door starts it
 * (`WorkerAgent.seams`): the container door — the harness package's
 * scripted FactorySandbox stand-in, which answers with the object's shapes
 * (cloudflare/test/factory-sandbox.test.ts holds the object itself) — and the
 * model, a faux under the gateway provider's id. Everything else is
 * production: the door, the run credential, the boot inputs, the agent, the
 * attempt host, the harness, the tools, the hooks.
 */
import { env, runInDurableObject, SELF } from "cloudflare:test";
import {
  type FauxResponseFactory,
  fauxAssistantMessage,
  fauxGatewayModels,
  fauxToolCall,
  type Message,
} from "ticfac-harness/testing";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import contract from "../../contracts/worker-boot-contract.json";
import { fakeSandboxDoor, waitFor } from "../../harness/test/sandbox-door-helper";
import { insertRun, type Run } from "../src/db";
import { issueWorkerRunToken } from "../src/gateway";
import { recordRunSubstrate } from "../src/run-substrate";
import { roomFor } from "../src/runs";
import type { SandboxNamespace, SdkSandbox } from "../src/sandbox";
import { attemptSandboxName } from "../src/sandbox-executor";
import type { WorkerAgent, WorkerAgentNamespace, WorkerAgentState } from "../src/worker-agent";
import type { SandboxDoor } from "ticfac-harness";

const BASE = "https://factory.example.com";
const EPIC = "43y";
const TICK = "xd3";
const BASE_SHA = "d1e2f3a4b5c6d1e2f3a4b5c6d1e2f3a4b5c6d1e2";
const PROMPT = "# implement-tick\n\nImplement the tick, then write the report.";
const BRANCH = "tick/43y-a1/xd3";

let RUN_ID = "";
let runToken = "";
let counter = 0;
const saved: Record<string, unknown> = {};

function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

/** What `ticks-worker --boot` prints on success, per the pinned contract. */
const BOOT_OUTPUT = [
  `ticks-worker: ${contract.boot_marker} branch=${BRANCH} result=RESULT-${TICK}.md`,
  contract.boot_prompt_begin,
  PROMPT,
  contract.boot_prompt_end,
  "",
].join("\n");

/** The container: the boot hands off, the model's bash takes `bashMs`, the finish exits `finishExit`. */
function scriptedContainer(bashMs: number, finishExit: number) {
  return fakeSandboxDoor({
    runOutput: (command) => (command.includes("git rev-parse HEAD") ? "cafef00d\n" : ""),
    processScript: (command) => {
      if (command === contract.boot_command) return { output: BOOT_OUTPUT, exit: 0, ms: 10 };
      if (command.startsWith(contract.finish_command)) {
        return { output: "ticks-worker: pushed\n", exit: finishExit, ms: 10 };
      }
      return { output: "tests pass\n", exit: 0, ms: bashMs };
    },
  });
}

/** A live run holding its project's lease, on the do_v1 substrate unless told otherwise. */
async function liveRun(substrate: "do_v1" | "sdk0" = "do_v1"): Promise<void> {
  counter += 1;
  RUN_ID = `run_xd3_agent_${counter}`;
  const run: Run = {
    run_id: RUN_ID,
    project: `example-org/example-agent-e2e-${counter}`,
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
  if (substrate === "do_v1") await recordRunSubstrate(env.DB, RUN_ID, "do_v1", null);
  const lease = await roomFor(env, run.project).acquireDispatchLease({
    run_id: RUN_ID,
    epic: EPIC,
    origin: "cloud",
  });
  if (!lease.ok) throw new Error("no lease");
  runToken = (await issueWorkerRunToken(env, { run_id: RUN_ID, tick_id: EPIC, attempt: 1 })).token;
}

/** The attempt's real WorkerAgent, with its seams set before anything starts it. */
async function seededAgent(
  container: ReturnType<typeof fakeSandboxDoor>,
  responses: FauxResponseFactory[],
): Promise<{ calls: () => number; stub: DurableObjectStub<WorkerAgent> }> {
  const namespace = env.WORKER_AGENTS as unknown as DurableObjectNamespace<WorkerAgent>;
  const stub = namespace.get(namespace.idFromName(attemptSandboxName(RUN_ID, TICK, 1)));
  const faux = fauxGatewayModels(responses);
  await runInDurableObject(stub, (instance: WorkerAgent) => {
    instance.seams = { door: () => container.sandbox, models: () => faux.models, pollMs: 5 };
  });
  return { calls: faux.calls, stub };
}

/**
 * The attempt's real WorkerAgent on the 0.x substrate: NO door seam — the
 * object builds its own door from `env.SANDBOXES`, the SDK namespace a
 * test substitutes (tick hxd). Only the model is seeded.
 */
async function seededAgentOnSdk0(
  container: ReturnType<typeof fakeSandboxDoor>,
  responses: FauxResponseFactory[],
): Promise<{ calls: () => number; stub: DurableObjectStub<WorkerAgent> }> {
  const namespace = env.WORKER_AGENTS as unknown as DurableObjectNamespace<WorkerAgent>;
  const stub = namespace.get(namespace.idFromName(attemptSandboxName(RUN_ID, TICK, 1)));
  const faux = fauxGatewayModels(responses);
  await runInDurableObject(stub, (instance: WorkerAgent) => {
    instance.seams = { models: () => faux.models, pollMs: 5 };
  });
  return { calls: faux.calls, stub };
}

/**
 * A 0.x SANDBOXES namespace for the sdk0 e2e: the structural fake the door's
 * `isSandboxNamespace` recognises, handing every name the same scripted
 * SDK sandbox.
 */
function fakeSdkNamespace(sandbox: SdkSandbox): SandboxNamespace {
  return {
    idFromName: (name: string) => name as unknown as DurableObjectId,
    get: () => sandbox,
  } as unknown as SandboxNamespace;
}

/** One scripted FactorySandbox door, behind the SDK's own shapes. */
function sdkOverDoor(door: SandboxDoor): SdkSandbox & { destroyedCount: () => number } {
  let destroyed = 0;
  const statusOf = (state: string): string =>
    state === "running" || state === "completed" || state === "failed" ? state : "error";
  return {
    async exec(command, options) {
      const out = await door.run(command, (options?.env ?? {}) as Record<string, string>, {});
      return out.ready
        ? { exitCode: out.exitCode, stdout: out.output, stderr: "" }
        : { exitCode: 1, stdout: "", stderr: "" };
    },
    async startProcess(command, options) {
      const started = await door.startProcess(
        command,
        (options?.env ?? {}) as Record<string, string>,
      );
      return {
        id: started.id,
        status: statusOf(started.state),
        exitCode: started.exit_code,
        ...(started.command === undefined ? {} : { command: started.command }),
      };
    },
    async getProcess(id) {
      const view = await door.getProcess(id);
      if (view === null) return null;
      return {
        id: view.id,
        status: statusOf(view.state),
        exitCode: view.exit_code,
        ...(view.command === undefined ? {} : { command: view.command }),
      };
    },
    async listProcesses() {
      const listed = await door.listProcesses();
      return listed.map((view) => ({
        id: view.id,
        status: statusOf(view.state),
        exitCode: view.exit_code,
        ...(view.command === undefined ? {} : { command: view.command }),
      }));
    },
    async getProcessLogs(id) {
      const whole = await door.readOutput(id, 0);
      return { stdout: whole.text, stderr: "" };
    },
    async killProcess(id) {
      await door.killProcess(id);
    },
    async destroy() {
      destroyed += 1;
    },
    async getState() {
      return { status: "running" };
    },
    destroyedCount: () => destroyed,
  };
}

function postStart(): Promise<Response> {
  return SELF.fetch(`${BASE}/api/sandbox/attempts`, {
    method: "POST",
    headers: { authorization: `Bearer ${runToken}`, "content-type": "application/json" },
    body: JSON.stringify({
      epic: EPIC,
      tick_id: TICK,
      attempt: 1,
      role: "implement-tick",
      write_ref: `refs/heads/ticfac/run-${RUN_ID}/tick-${TICK}/attempt-1`,
      base_ref: `refs/heads/epic/${EPIC}`,
      title: "Cloud host: a WorkerAgent DO per attempt",
      base_sha: BASE_SHA,
      model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
      harness: "pi",
      prompt: PROMPT,
    }),
  });
}

async function doorState(): Promise<{ state: string; observations?: { detail: string }[] }> {
  const response = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1`, {
    headers: { authorization: `Bearer ${runToken}` },
  });
  return (await response.json()) as { state: string; observations?: { detail: string }[] };
}

function textOf(message: Message): string {
  if (typeof message.content === "string") return message.content;
  return message.content.map((block) => (block.type === "text" ? block.text : "")).join("");
}

beforeEach(async () => {
  // The real WORKER_AGENTS namespace (wrangler.toml); the containers are the
  // agent's seam, so no container binding is ever addressed for the attempt.
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

describe("a cloud worker attempt on its WorkerAgent", () => {
  it("runs end to end through the door: boot, conversation, finish — and the door answers the finish's exit code", async () => {
    const container = scriptedContainer(20, 0);
    const { calls, stub } = await seededAgent(container, [
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("done: the report is written"),
    ]);

    const started = await postStart();
    expect(started.status).toBe(201);

    // The door answers from the agent until it settles.
    let status = await doorState();
    const deadline = Date.now() + 20_000;
    while (status.state === "running" && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 50));
      status = await doorState();
    }
    expect(status.state).toBe("succeeded");
    expect(status.observations?.[0]?.detail).toContain("exited 0");

    // The container ran exactly the boot, the model's bash and the finish.
    const commands = container.starts.map((s) => s.command);
    expect(commands[0]).toBe(contract.boot_command);
    expect(commands.filter((c) => c.includes("make test")).length).toBe(1);
    expect(commands.at(-1)).toBe(`${contract.finish_command} 0`);
    // The boot ran on the door's own boot env: the run, the tick, the prompt.
    expect(container.starts[0]?.env.TICKS_RUN_ID).toBe(RUN_ID);
    expect(container.starts[0]?.env.TICKS_ROLE_PROMPT).toBe(PROMPT);
    expect(calls()).toBe(2);

    // The agent's own record and log, from its own storage.
    const state = (await stub.state()) as WorkerAgentState;
    expect(state).toMatchObject({ phase: "settled", exit_code: 0, branch: BRANCH });
    const log = await stub.readLog(0);
    expect(log.text).toContain("booted: branch");
    expect(log.text).toContain("tool bash");
    expect(log.text).toContain("ticks-worker: pushed");
    // A cursor at the end reads nothing new.
    expect((await stub.readLog(log.offset)).text).toBe("");
  });

  it("runs end to end on the 0.x substrate too: the object builds its own door from SANDBOXES, and the SDK's exec serves the run door (tick hxd)", async () => {
    // sdk0: no substrate row at all — the substrate every run that asks for
    // none is on, which is the gap this tick closes. The container is the
    // same scripted door, behind the SDK's own shapes, handed to the object
    // through the SANDBOXES binding — no door seam, so `attemptDoor` reads
    // the run's substrate itself and routes to the SDK adapter.
    await liveRun("sdk0");
    const container = fakeSandboxDoor({
      runOutput: (command) => (command.includes("git rev-parse HEAD") ? "cafef00d\n" : ""),
      processScript: (command) => {
        // The 0.x door merges stderr with a ` 2>&1` suffix, so every match
        // here is a contents match, never an equality.
        if (command.includes(contract.boot_command))
          return { output: BOOT_OUTPUT, exit: 0, ms: 10 };
        if (command.startsWith(contract.finish_command))
          return { output: "ticks-worker: pushed\n", exit: 0, ms: 10 };
        return { output: "tests pass\n", exit: 0, ms: 20 };
      },
    });
    const sdk = sdkOverDoor(container.sandbox);
    set("SANDBOXES", fakeSdkNamespace(sdk));
    const { calls } = await seededAgentOnSdk0(container, [
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("done: the report is written"),
    ]);

    const started = await postStart();
    expect(started.status).toBe(201);

    let status = await doorState();
    const deadline = Date.now() + 20_000;
    while (status.state === "running" && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 50));
      status = await doorState();
    }
    expect(status.state).toBe("succeeded");
    expect(calls()).toBe(2);

    // The boot, the model's bash and the finish all ran in the 0.x sandbox,
    // stderr merged into the one buffer the cursor follows.
    const commands = container.starts.map((s) => s.command);
    expect(commands[0]).toBe(`${contract.boot_command} 2>&1`);
    expect(commands.filter((c) => c.includes("make test")).length).toBe(1);
    expect(commands.at(-1)).toBe(`${contract.finish_command} 0 2>&1`);
    expect(container.starts[0]?.env.TICKS_RUN_ID).toBe(RUN_ID);

    // The short commands went through the run door — the SDK's exec, under
    // the bounded POSIX line.
    expect(
      container.runs.some(
        (r) => r.command.includes("git rev-parse HEAD") && r.command.includes("head -c"),
      ),
    ).toBe(true);

    // And a settled attempt released its container: destroyed once, by the
    // agent, through the same door it drove the attempt with.
    expect(sdk.destroyedCount()).toBe(1);
  });

  it("is watched and steered over its socket while it converses", async () => {
    const container = scriptedContainer(1_500, 0);
    const seen: string[][] = [];
    await seededAgent(container, [
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      (context) => {
        seen.push(
          context.messages.filter((m) => m.role === "user").map((m) => textOf(m as Message)),
        );
        return fauxAssistantMessage("steered and done");
      },
    ]);
    expect((await postStart()).status).toBe(201);
    await waitFor(
      "the model's bash to start",
      () => container.starts.some((s) => s.command.includes("make test")),
      20_000,
    );

    const upgraded = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/watch`, {
      headers: { authorization: `Bearer ${runToken}`, upgrade: "websocket" },
    });
    expect(upgraded.status).toBe(101);
    const socket = upgraded.webSocket;
    if (socket === null) throw new Error("no socket");
    const frames: { type: string; [key: string]: unknown }[] = [];
    socket.addEventListener("message", (event) => {
      frames.push(JSON.parse(String(event.data)));
    });
    socket.accept();

    await waitFor("the state and the snapshot", () => frames.length >= 2, 10_000);
    expect(frames[0]).toMatchObject({ type: "state", state: { phase: "conversing" } });
    expect(frames[1]).toMatchObject({ type: "events" });
    expect((frames[1]?.events as { type: string }[] | undefined)?.[0]?.type).toBe("snapshot");

    socket.send(JSON.stringify({ type: "steer", text: "also add a docstring" }));
    await waitFor("the steer's answer", () => frames.some((f) => f.type === "steered"), 10_000);

    let status = await doorState();
    const deadline = Date.now() + 20_000;
    while (status.state === "running" && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 50));
      status = await doorState();
    }
    expect(status.state).toBe("succeeded");
    expect(seen[0]).toEqual([PROMPT, "also add a docstring"]);
    // The watcher saw the run's events and the log as they landed.
    expect(frames.some((f) => f.type === "log")).toBe(true);
    expect(frames.some((f) => f.type === "events" && f !== frames[1])).toBe(true);
    socket.close();
  });

  it("starts afresh under a settled name: a new conversation, a new log, a new settlement", async () => {
    const container = scriptedContainer(10, 0);
    const { calls, stub } = await seededAgent(container, [
      () => fauxAssistantMessage("the first attempt's answer"),
      () => fauxAssistantMessage("the second attempt's answer"),
    ]);
    const settle = async () => {
      let status = await doorState();
      const deadline = Date.now() + 20_000;
      while (status.state === "running" && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 50));
        status = await doorState();
      }
      return status.state;
    };
    expect((await postStart()).status).toBe(201);
    expect(await settle()).toBe("succeeded");
    const firstLog = (await stub.readLog(0)).text;
    expect(firstLog).toContain("the first attempt's answer");

    expect((await postStart()).status).toBe(201);
    expect(await settle()).toBe("succeeded");
    expect(calls()).toBe(2);
    const secondLog = (await stub.readLog(0)).text;
    expect(secondLog).toContain("the second attempt's answer");
    expect(secondLog).not.toContain("the first attempt's answer");
    expect(container.starts.filter((s) => s.command === contract.boot_command).length).toBe(2);
  });

  it("answers the door's lost for an agent that never started, and a reclaim stops it for good", async () => {
    const namespace = env.WORKER_AGENTS as unknown as WorkerAgentNamespace;
    const stub = namespace.get(namespace.idFromName(attemptSandboxName(RUN_ID, TICK, 9)));
    expect((await stub.state()).phase).toBe("absent");
    expect((await stub.reclaim("run_ended:failed")).phase).toBe("absent");
  });
});
