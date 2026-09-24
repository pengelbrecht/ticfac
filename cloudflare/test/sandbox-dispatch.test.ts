/**
 * The per-tick sandbox dispatch door (tick 8ty): the orchestrator container's
 * HTTP surface for starting one attempt's worker container and reading one
 * back by identity.
 *
 * Every test drives the REAL route through `SELF.fetch` rather than by calling
 * the door's handlers directly, because half of what this tick must prove is
 * the gate itself: the route is exempt from the operator's factory bearer
 * token and authorized by the run's own gateway credential instead, and a
 * test that bypassed the Worker's routing and auth would prove nothing about
 * either. The only substitution is the container itself, through the
 * `SANDBOXES` seam — the same rule every suite in this bundle runs by.
 *
 * The pinned job-protocol contract is what the responses are validated
 * against, not this file's own expectations: the Go client (tick keh) parses
 * the same contract, and a record only one side would take is drift the
 * contract exists to catch.
 */
import { env, SELF } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import jobProtocol from "../../contracts/job-protocol.json";
import { readWorkerLogTail } from "../src/artifacts";
import { insertRun, type Run } from "../src/db";
import { issueWorkerRunToken, revokeRunTokens } from "../src/gateway";
import { roomFor } from "../src/runs";
import type {
  OrchestratorSandbox,
  SandboxBinding,
  SandboxOutput,
  SandboxProcessState,
  SandboxProcessView,
} from "../src/sandbox";
import {
  attemptJobID,
  attemptSandboxName,
  SANDBOX_EXECUTOR_NAME,
  type SandboxJobHandle,
} from "../src/sandbox-executor";
import { WORKER_COMMAND, WORKER_PROBE_MARKER } from "../src/worker-boot";
import { type Defs, parseDefs, parseSchema, validate } from "./json-schema";

// --------------------------------------------------------- the fake sandbox ---

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
 * One fake container. The green-start probe answers its marker and EXITS;
 * the work process prints and KEEPS RUNNING — a worker genuinely mid-tick,
 * which is the only state in which "the door returned without waiting for
 * the attempt" and "a second call adopts it" mean anything.
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
  /** The names ever addressed — a second boot of a fresh name is a rival. */
  readonly addressed: string[] = [];

  async get(name: string): Promise<OrchestratorSandbox> {
    this.addressed.push(name);
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

// ---------------------------------------------------------------- the harness ---

const BASE = "https://factory.example.com";
const EPIC = "xte";
const BASE_SHA = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2";
const TICK = "8ty";

// Per test, because the D1 index and the RunRoom DO are real and persist
// across the tests in this file: two runs of one project would contend for
// one lease, and two rows with one id would not insert. Uniqueness is
// identity hygiene, not an accident of a shared counter.
let RUN_ID = "";
let project = "";
let runCounter = 0;

// The pinned job-protocol definitions, so a door-produced handle and status
// are validated against the same contract the Go client parses (tick us2):
// the compatibility claim is checked, not asserted.
const protocolDefs: Defs = parseDefs((jobProtocol as { $defs: unknown }).$defs);
const jobHandleSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_handle,
  "$",
);
const jobStatusSchema = parseSchema(
  (jobProtocol as { $defs: Record<string, unknown> }).$defs.job_status,
  "$",
);

/**
 * The model a dispatch names (tick a08): pi's spelling of GLM 5.3 Flash, as a
 * cloud profile resolves it — deliberately NOT the deployment's
 * `RUN_WORKER_MODEL` nor the built-in default, so a container booted on either
 * of those is told apart from one booted on what the request carried.
 */
const REQUESTED_MODEL = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash";

/**
 * The harness a dispatch names (tick 9iz): the profile's runner — deliberately
 * spelled the same as the built-in default so the test that proves the request
 * outranks the deployment sets `RUN_WORKER_HARNESS` to something else, and a
 * container booted on the deployment's standing choice is told apart from one
 * booted on what the request carried.
 */
const REQUESTED_HARNESS = "pi";

/**
 * The rendered role prompt a dispatch carries (tick 9iz): the profile's own
 * prompt text, multi-line prose with the report contract in it — deliberately
 * NOT anything the container could derive for itself, so a boot that dropped
 * it is a boot the assertion tells apart from one that carried it.
 */
const ROLE_PROMPT = [
  "# implement-tick",
  "",
  "You are implementing ONE unit of work from the ticks tracker, headless, in",
  "an isolated git worktree that is yours alone. Nobody will answer a question.",
  "",
  "Work test-first, and end your report with a STATUS line.",
].join("\n");

/** The start body every test fills around, in the door's documented shape. */
function startBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    epic: EPIC,
    tick_id: TICK,
    attempt: 1,
    role: "implement-tick",
    write_ref: `refs/heads/ticfac/run-${RUN_ID}/tick-${TICK}/attempt-1`,
    base_ref: `refs/heads/epic/${EPIC}`,
    title: "Expose per-tick sandbox dispatch over HTTP from the Worker",
    base_sha: BASE_SHA,
    model: REQUESTED_MODEL,
    harness: REQUESTED_HARNESS,
    prompt: ROLE_PROMPT,
    ...overrides,
  };
}

let binding: FakeSandboxes;
let runToken: string;
/** The run row's lease token, so a test can hand the lease elsewhere. */
let leaseToken: string;
const saved: Record<string, unknown> = {};

/** Sets a deployment variable for this suite, restored after each test. */
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
  else (env as unknown as Record<string, unknown>)[name] = value;
}

/** A live run in the index, holding its own project's dispatch lease. */
async function liveRun(): Promise<Run> {
  runCounter += 1;
  RUN_ID = `run_8ty_${runCounter}`;
  project = `example-org/example-repo-${runCounter}`;
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

  const room = roomFor(env, run.project);
  const lease = await room.acquireDispatchLease({
    run_id: run.run_id,
    epic: EPIC,
    origin: "cloud",
  });
  if (!lease.ok) throw new Error(`the lease was refused: ${JSON.stringify(lease)}`);
  leaseToken = lease.lease.token;
  return run;
}

/** The run credential the orchestrator container holds: minted, never rotated. */
async function mintRunToken(): Promise<string> {
  const issued = await issueWorkerRunToken(env, {
    run_id: RUN_ID,
    tick_id: EPIC,
    attempt: 1,
  });
  runToken = issued.token;
  return runToken;
}

/** POST the start route with exactly the headers a container sends. */
function postStart(token: string | null, body: Record<string, unknown>): Promise<Response> {
  return SELF.fetch(`${BASE}/api/sandbox/attempts`, {
    method: "POST",
    headers: {
      ...(token === null ? {} : { authorization: `Bearer ${token}` }),
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

/** GET the state route for one attempt's identity. */
function getState(token: string | null, tickID: string, attempt: number): Promise<Response> {
  return SELF.fetch(`${BASE}/api/sandbox/attempts/${tickID}/${attempt}`, {
    method: "GET",
    headers: token === null ? {} : { authorization: `Bearer ${token}` },
  });
}

/** Unwraps a response body as the door's refusal vocabulary. */
async function denialOf(response: Response): Promise<{ error: string; detail: string }> {
  return (await response.json()) as { error: string; detail: string };
}

beforeEach(async () => {
  binding = new FakeSandboxes();
  set("SANDBOXES", binding);
  set("FACTORY_BASE_URL", BASE);
  set("GITHUB_TOKEN", "gh-operator-test-token");
  await liveRun();
  await mintRunToken();
});

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

// ------------------------------------------------------------ authorization ---

describe("authorization", () => {
  it("refuses an unauthenticated call", async () => {
    const response = await postStart(null, startBody());
    expect(response.status).toBe(401);
    const denial = await denialOf(response);
    // The gateway's own vocabulary, not a generic 401: this is the run
    // credential's refusal, the same one the model path answers with.
    expect(denial.error).toBe("run_token_required");
    // And nothing was booted: a refusal is not a half-dispatch.
    expect(binding.addressed).toEqual([]);
  });

  it("refuses the operator's factory token — the door takes the run's credential only", async () => {
    // The operator's bearer token commands the whole control plane; a route
    // that accepted it would be the leak D17 exists to prevent. It lands on
    // the run-credential gate as an unknown token, proving the door is NOT
    // behind the factory bearer gate and is closed anyway.
    const response = await postStart("tkf_not_a_run_token", startBody());
    expect(response.status).toBe(401);
    expect((await denialOf(response)).error).toBe("run_token_unknown");
    expect(binding.addressed).toEqual([]);
  });

  it("refuses a revoked credential — the kill switch reaches dispatch, not only spending", async () => {
    expect(await revokeRunTokens(env, RUN_ID, "stopped:hard")).toBeGreaterThanOrEqual(1);
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(403);
    expect((await denialOf(response)).error).toBe("run_token_revoked");
    expect(binding.addressed).toEqual([]);
  });

  it("refuses a run that is over, on both routes", async () => {
    // A second suite-local run row is not needed: flip the row's state, which
    // is what the Workflow's finalize does.
    await env.DB.prepare("UPDATE runs SET state = ? WHERE run_id = ?").bind("failed", RUN_ID).run();
    const start = await postStart(runToken, startBody());
    expect(start.status).toBe(403);
    expect((await denialOf(start)).error).toBe("run_not_active");
    const state = await getState(runToken, TICK, 1);
    expect(state.status).toBe(403);
    expect((await denialOf(state)).error).toBe("run_not_active");
    expect(binding.addressed).toEqual([]);
  });

  it("demands the credential on the state route too", async () => {
    const response = await getState(null, TICK, 1);
    expect(response.status).toBe(401);
    expect((await denialOf(response)).error).toBe("run_token_required");
  });
});

// ------------------------------------------------------------------- start ---

describe("start", () => {
  it("starts the attempt in a sandbox NAMED BY ITS IDENTITY and returns a handle without waiting", async () => {
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(201);

    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(false);
    const handle = body.handle;
    // The contract's closed top level (job-protocol $defs.job_handle): the
    // Go client parses this record with the same schema.
    const errors = validate(
      jobHandleSchema,
      protocolDefs,
      handle as unknown as Record<string, unknown>,
    );
    expect(errors, `job_handle must satisfy the pinned contract: ${errors.join("; ")}`).toEqual([]);
    expect(handle.executor).toBe(SANDBOX_EXECUTOR_NAME);
    expect(handle.job_id).toBe(attemptJobID(RUN_ID, TICK, 1));

    // NAMED BY IDENTITY, not by a fresh id: the container's name is the
    // attempt's, which is what a second call for the same identity will
    // resolve — and what the tick's third bullet rides on.
    const name = attemptSandboxName(RUN_ID, TICK, 1);
    expect(handle.handle.sandbox).toBe(name);
    expect(handle.handle.write_ref).toBe(startBody().write_ref);
    expect(handle.handle.base_sha).toBe(BASE_SHA);
    expect(handle.handle.launched).toBe(true);

    // The boot the machinery composed, inside the container it named.
    const sandbox = binding.named(name);
    expect(binding.addressed).toContain(name);
    const work = sandbox.workProcess();
    expect(work).toBeDefined();
    expect(work?.env.TICKS_TICK).toBe(TICK);
    expect(work?.env.TICKS_EPIC).toBe(`${EPIC}/attempt-1`);
    expect(work?.env.TICKS_BASE_SHA).toBe(BASE_SHA);
    expect(work?.env.TICKS_RUN_ID).toBe(RUN_ID);
    // The only model credential the container holds is run-scoped and fresh
    // (D17, tick 53s): minted per dispatch, revoking nothing.
    expect(work?.env.AI_GATEWAY_TOKEN).toMatch(/^tkr_[0-9a-f]{64}$/);
    expect(work?.env.TICKS_FACTORY_TOKEN).toBe(work?.env.AI_GATEWAY_TOKEN);

    // NOTHING WAITED: the handle came back while the work process is still
    // running — the door's answer is a handle, not the attempt's fate.
    expect(work?.state).toBe("running");
  });

  it("boots the worker on the model the request carries, and the handle names it (tick a08)", async () => {
    // The deployment's standing choice says something else: the request's
    // model is a choice about THIS attempt, and it outranks a standing one.
    set("RUN_WORKER_MODEL", "workers-ai/@cf/zai-org/some-other-model");
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };

    const work = binding.named(attemptSandboxName(RUN_ID, TICK, 1)).workProcess();
    expect(work?.env.TICKS_MODEL).toBe(REQUESTED_MODEL);
    // The handle states the model the container was booted with, so the
    // record a caller keeps names the model that actually ran.
    expect(body.handle.handle.model).toBe(REQUESTED_MODEL);
    expect(body.handle.handle.model).toBe(work?.env.TICKS_MODEL);

    // An adoption names it too — the same attempt, the same model.
    const again = await postStart(runToken, startBody());
    expect(again.status).toBe(200);
    const adopted = (await again.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(adopted.adopted).toBe(true);
    expect(adopted.handle.handle.model).toBe(REQUESTED_MODEL);
  });

  it("refuses a start that names no model rather than booting the factory's own", async () => {
    // A request with no model would boot on RUN_WORKER_MODEL or the default,
    // and the caller's record would name a model that never ran.
    const { model: _dropped, ...withoutModel } = startBody();
    for (const body of [
      withoutModel,
      startBody({ model: "" }),
      startBody({ model: "has space" }),
    ]) {
      const response = await postStart(runToken, body);
      expect(response.status, JSON.stringify(body.model)).toBe(400);
      const denial = await denialOf(response);
      expect(denial.error).toBe("invalid_request");
      expect(denial.detail).toContain("model");
    }
    expect(binding.addressed).toEqual([]);
  });

  it("boots the worker on the harness the request carries, and the handle names it (tick 9iz)", async () => {
    // The deployment's standing choice says omp: the request's harness is a
    // choice about THIS attempt, and it outranks a standing one — the same
    // ladder the model rides, applied to the harness the worker binds.
    set("RUN_WORKER_HARNESS", "omp");
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(201);
    const body = (await response.json()) as { handle: SandboxJobHandle; adopted: boolean };

    const work = binding.named(attemptSandboxName(RUN_ID, TICK, 1)).workProcess();
    expect(work?.env.TICKS_HARNESS).toBe(REQUESTED_HARNESS);
    // The handle states the harness the container was booted with, so the
    // record a caller keeps names the harness that actually ran — never the
    // deployment's own agreeing with it by luck.
    expect(body.handle.handle.harness).toBe(REQUESTED_HARNESS);
    expect(body.handle.handle.harness).toBe(work?.env.TICKS_HARNESS);

    // An adoption names it too — the same attempt, the same harness.
    const again = await postStart(runToken, startBody());
    expect(again.status).toBe(200);
    const adopted = (await again.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(adopted.adopted).toBe(true);
    expect(adopted.handle.handle.harness).toBe(REQUESTED_HARNESS);
  });

  it("delivers the rendered prompt to the worker's boot environment (tick 9iz)", async () => {
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(201);

    const sandbox = binding.named(attemptSandboxName(RUN_ID, TICK, 1));
    // The work process is the one the harness runs in: the prompt the
    // dispatch carried is in ITS environment, exactly as given — a door
    // that dropped it would leave the worker running a prompt nobody chose.
    const work = sandbox.workProcess();
    expect(work?.env.TICKS_ROLE_PROMPT).toBe(ROLE_PROMPT);
    // And the probe answers in the same environment: a probe run in a
    // different one proves something about a container nobody will use.
    const probe = sandbox.processes.find((p) => p.command.includes("--probe"));
    expect(probe?.env.TICKS_ROLE_PROMPT).toBe(ROLE_PROMPT);
  });

  it("refuses a start that names no harness or no prompt rather than booting unbound", async () => {
    // A request with no harness would boot on RUN_WORKER_HARNESS or the
    // default, and the caller's record would name a harness that never ran; a
    // request with no prompt would boot a worker on a prompt nobody chose.
    const { harness: _droppedHarness, prompt: _droppedPrompt, ...withoutEither } = startBody();
    for (const body of [
      withoutEither,
      startBody({ harness: "" }),
      startBody({ harness: "has space" }),
      startBody({ prompt: "" }),
      startBody({ prompt: "a control\u0007 character" }),
      startBody({ prompt: "a".repeat(65537) }),
    ]) {
      const response = await postStart(runToken, body);
      expect(response.status, JSON.stringify(body)).toBe(400);
      const denial = await denialOf(response);
      expect(denial.error).toBe("invalid_request");
      expect(
        denial.detail.includes("harness") || denial.detail.includes("prompt"),
        `the refusal must name the missing field: ${denial.detail}`,
      ).toBe(true);
    }
    expect(binding.addressed).toEqual([]);
  });

  it("streams the worker's output to the run's logs as the container produces it (tick 9iz)", async () => {
    // The wave path wired a workerLogSink into every spawn; the door's path
    // is the only dispatch path now, so its spawns stream the same way: the
    // probe's marker and the work process's first output land in the run's
    // own R2 stream, where the log read routes serve them.
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(201);

    const tail = await readWorkerLogTail(env.ARTIFACTS, project, RUN_ID, TICK);
    expect(tail.text).toContain(WORKER_PROBE_MARKER);
    expect(tail.text).toContain("implementing the tick");
  });

  it("a second call with the same identity returns the SAME running attempt, not a rival", async () => {
    const first = await postStart(runToken, startBody());
    expect(first.status).toBe(201);
    const firstBody = (await first.json()) as { handle: SandboxJobHandle; adopted: boolean };
    const sandbox = binding.named(attemptSandboxName(RUN_ID, TICK, 1));
    const processesBefore = sandbox.processes.length;
    const workBefore = sandbox.workProcess();

    // The caller that cannot know whether its first call landed: the same
    // identity, asked again — a restarted orchestrator, a retried request.
    const second = await postStart(runToken, startBody());
    expect(second.status).toBe(200);
    const secondBody = (await second.json()) as { handle: SandboxJobHandle; adopted: boolean };

    expect(secondBody.adopted).toBe(true);
    expect(secondBody.handle.job_id).toBe(firstBody.handle.job_id);
    expect(secondBody.handle.handle.sandbox).toBe(firstBody.handle.handle.sandbox);
    // THE acceptance clause: the SAME work process, and no second one beside
    // it — two containers on one tick is two workers pushing one branch.
    expect(secondBody.handle.handle.process_id).toBe(firstBody.handle.handle.process_id);
    expect(secondBody.handle.handle.process_id).toBe(workBefore?.id);
    expect(sandbox.processes.length).toBe(processesBefore);
    expect(sandbox.workProcess()).toBe(workBefore);
  });

  it("a different attempt number is a different identity, and lands in a fresh container", async () => {
    await postStart(runToken, startBody());
    const second = await postStart(runToken, startBody({ attempt: 2 }));

    expect(second.status).toBe(201);
    const body = (await second.json()) as { handle: SandboxJobHandle; adopted: boolean };
    expect(body.adopted).toBe(false);
    expect(body.handle.handle.sandbox).toBe(attemptSandboxName(RUN_ID, TICK, 2));
    // The spent attempt's container keeps its own process list: nothing was
    // adopted and nothing was shared.
    expect(binding.named(attemptSandboxName(RUN_ID, TICK, 1)).workProcess()).toBeDefined();
    expect(binding.named(attemptSandboxName(RUN_ID, TICK, 2)).workProcess()).toBeDefined();
  });

  it("refuses a body whose epic is not the run's own", async () => {
    const response = await postStart(runToken, startBody({ epic: "zzz" }));
    expect(response.status).toBe(400);
    const denial = await denialOf(response);
    expect(denial.error).toBe("invalid_request");
    expect(denial.detail).toContain(JSON.stringify(EPIC));
    expect(binding.addressed).toEqual([]);
  });

  it("refuses a malformed shape rather than guessing it", async () => {
    for (const overrides of [
      { tick_id: "" },
      { tick_id: "not a name" },
      { tick_id: "../escape" },
      { attempt: 0 },
      { attempt: 1.5 },
      { role: "" },
      { write_ref: "refs/heads/with spaces" },
      { base_sha: "short" },
      { base_sha: "z".repeat(40) },
    ]) {
      const response = await postStart(runToken, startBody(overrides));
      expect(response.status, JSON.stringify(overrides)).toBe(400);
      expect((await denialOf(response)).error).toBe("invalid_request");
    }
    expect(binding.addressed).toEqual([]);
  });

  it("refuses a run that no longer holds the project's dispatch lease", async () => {
    // Release ours, take the lease away as another run — and the door refuses
    // exactly as the wave door does: a lapsed arbiter must not boot containers.
    const room = roomFor(env, project);
    const released = await room.releaseDispatchLease({ run_id: RUN_ID, token: leaseToken });
    if (!released.ok) throw new Error(`the lease was not released: ${JSON.stringify(released)}`);
    const taken = await room.acquireDispatchLease({ run_id: "run_successor", epic: EPIC });
    if (!taken.ok) throw new Error(`the lease was not taken: ${JSON.stringify(taken)}`);

    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(409);
    const denial = await denialOf(response);
    expect(denial.error).toBe("lease_held_by");
    expect(denial.detail).toContain("run_successor");
    expect(binding.addressed).toEqual([]);
  });

  it("refuses a dispatch when no lease is live at all", async () => {
    const room = roomFor(env, project);
    const released = await room.releaseDispatchLease({ run_id: RUN_ID, token: leaseToken });
    if (!released.ok) throw new Error(`the lease was not released: ${JSON.stringify(released)}`);

    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(409);
    expect((await denialOf(response)).error).toBe("lease_lost");
    expect(binding.addressed).toEqual([]);
  });

  it("refuses with the deployment's own diagnosis when it cannot boot a container", async () => {
    set("SANDBOXES", undefined);
    const response = await postStart(runToken, startBody());
    expect(response.status).toBe(503);
    const denial = await denialOf(response);
    expect(denial.error).toBe("sandbox_dispatch_not_wired");
    // The refusal names the gap in the response, not only on the Worker's
    // log: the caller is a container reading one answer.
    expect(denial.detail).toContain("SANDBOXES");
  });
});

// ------------------------------------------------------------------- state ---

describe("state", () => {
  it("reports a running attempt, in the contract's own vocabulary", async () => {
    const started = (await (await postStart(runToken, startBody())).json()) as {
      handle: SandboxJobHandle;
    };
    const response = await getState(runToken, TICK, 1);
    expect(response.status).toBe(200);

    const status = (await response.json()) as Record<string, unknown>;
    const errors = validate(jobStatusSchema, protocolDefs, status);
    expect(errors, `job_status must satisfy the pinned contract: ${errors.join("; ")}`).toEqual([]);
    // RUNNING, and the SAME job id the handle carries: a client keying on
    // job_id reads one value from either route.
    expect(status).toMatchObject({
      schema_version: 1,
      job_id: started.handle.job_id,
      state: "running",
      terminal: false,
    });
  });

  it("reports a finished attempt WITH ITS RESULT: the exit code, riding an observation", async () => {
    await postStart(runToken, startBody());
    const work = binding.named(attemptSandboxName(RUN_ID, TICK, 1)).workProcess();
    work?.finish(0);

    const response = await getState(runToken, TICK, 1);
    expect(response.status).toBe(200);
    const status = (await response.json()) as {
      state: string;
      terminal: boolean;
      observations: Array<{ kind: string; detail: string }>;
    };
    expect(status.state).toBe("succeeded");
    expect(status.terminal).toBe(true);
    expect(status.observations[0]?.kind).toBe("exited");
    expect(status.observations[0]?.detail).toContain("0");

    // A failure is a result too — the vocabulary the reconciler branches on.
    work?.finish(11);
    const failed = (await (await getState(runToken, TICK, 1)).json()) as { state: string };
    expect(failed.state).toBe("failed");
  });

  it("reports an identity that never started as ABSENT — `lost`, never `failed`", async () => {
    const response = await getState(runToken, "nope", 7);
    expect(response.status).toBe(200);
    const status = (await response.json()) as { state: string; terminal: boolean };
    // The contract's own words for `lost`: a statement about the observer,
    // not a verdict on the job — deliberately NOT terminal, because recovery
    // may re-adopt. A caller must never read absent as "finished".
    expect(status.state).toBe("lost");
    expect(status.terminal).toBe(false);
  });

  it("reports a container that lost its own record as lost, not as a clean failure", async () => {
    await postStart(runToken, startBody());
    const work = binding.named(attemptSandboxName(RUN_ID, TICK, 1)).workProcess();
    // `gone`: the container died and came back empty — its process record is
    // an evidence gap, and the job's fate is exactly as unknown as if the
    // container had never been addressed.
    work!.state = "gone";

    const status = (await (await getState(runToken, TICK, 1)).json()) as {
      state: string;
      terminal: boolean;
    };
    expect(status.state).toBe("lost");
    expect(status.terminal).toBe(false);
  });

  it("refuses a malformed identity in the path", async () => {
    const bad = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/0`, {
      headers: { authorization: `Bearer ${runToken}` },
    });
    expect(bad.status).toBe(400);
    expect((await denialOf(bad)).error).toBe("invalid_request");
  });
});

// ------------------------------------------------------------------ routing ---

describe("routing", () => {
  it("answers only the two documented paths, with their methods", async () => {
    // A deeper path is not the door at all.
    const deep = await SELF.fetch(`${BASE}/api/sandbox/attempts/${TICK}/1/extra`, {
      headers: { authorization: `Bearer ${runToken}` },
    });
    expect(deep.status).toBe(404);

    // The start route is POST; the state route is GET.
    const wrongMethod = await SELF.fetch(`${BASE}/api/sandbox/attempts`, {
      method: "GET",
      headers: { authorization: `Bearer ${runToken}` },
    });
    expect(wrongMethod.status).toBe(405);
    expect((await denialOf(wrongMethod)).error).toBe("method_not_allowed");
    // Nothing was booted for either.
    expect(binding.addressed).toEqual([]);
  });
});
