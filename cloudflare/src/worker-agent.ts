/**
 * WorkerAgent — one cloud worker attempt, hosted as a Durable Object on
 * pi-durable (epic 43y, step 6 of docs/spikes/n0b-round2-pi-durable.md, tick
 * xd3).
 *
 * ## Why this exists
 *
 * Until this tick a cloud worker was one process in its container: the image's
 * entrypoint ran the pi CLI and pushed its branch, and a container lost
 * mid-turn lost the turn. Under this class the attempt's CONVERSATION lives
 * here — in this object's own DO SQLite, under pi-durable's unchanged
 * `SqliteStorage`, committed before anything is shown — and only its TOOLS
 * run in the attempt's FactorySandbox container. A container lost mid-turn is
 * restored from the last wip checkpoint (tick dwn) and the turn goes on; this
 * object restarted by a deploy reopens its storage and resumes (a tracked
 * bash reattaches to the process it left running, tick kgk); any number of
 * clients watch and steer the conversation over this object's hibernatable
 * WebSockets.
 *
 * ## What it is
 *
 * The host of the harness package's `WorkerAttemptHost` (harness/src/host/
 * worker-attempt.ts — every phase of the attempt is there, tested there):
 *
 *  - **start** records the attempt once, arms the heartbeat and starts the
 *    drive: `ticks-worker --boot` in the container, the conversation on the
 *    prompt it printed, `ticks-worker --finish <status>`. A second start is
 *    an adoption: it answers the record already there.
 *  - **the heartbeat** (`alarm`, every {@link HEARTBEAT_MS}) re-drives an
 *    unsettled attempt — a deploy restarts every Durable Object, and the
 *    in-memory drive goes with it; the next alarm reopens the storage and
 *    resumes from the record. A settled attempt arms no alarm.
 *  - **state / readLog** are what the dispatch door's state route answers
 *    from (sandbox-dispatch.ts): the phase and, once settled, the attempt's
 *    exit code — the finish phase's, which is what the all-in-one
 *    entrypoint's exit code always was — and the attempt's log by cursor.
 *  - **steer** places operator input after the running tool round; the
 *    door's steer route and the watch socket both reach it.
 *  - **reclaim** stops the attempt where it stands (the run is over), and
 *    **release** destroys its container (the attempt settled).
 *  - **fetch** accepts a watch socket: the attempt's state, then the
 *    conversation's agent events (pi-durable `watchEvents`) and the log as
 *    they are committed; a `{"type":"steer","text":…}` message steers.
 *
 * Model access is the gateway's route ANSWERED IN THIS ISOLATE: the provider's
 * requests are handed to `proxyModelRequest` with the attempt's own run token
 * — exchanged, attributed and revocable exactly as a container's request is
 * (D17), with no hop out to the factory's public hostname and back. The
 * container, where the untrusted code runs, never needs the model credential
 * for the conversation.
 *
 * Only a run on the `do_v1` substrate was hosted here at first, because the
 * env the tools run through needed FactorySandbox's `run` door, which the
 * 0.x Sandbox class did not have. Tick hxd closed that gap from the other
 * side: the 0.x class's own `exec` became the run door
 * (src/sandbox.ts `sdkSandboxDoor`), so every run's workers are WorkerAgents
 * now, on whichever substrate its containers live — and
 * {@link workerAgentsFromEnv} answers for every run, never null for a
 * substrate.
 */

import { DurableObject } from "cloudflare:workers";
import {
  type AgentEventStream,
  gatewayModelAccess,
  openDurableObjectStorage,
  type SandboxDoor,
  WorkerAttemptHost,
  type WorkerAttemptPhase,
  type WorkerAttemptRecord,
  type WorkerAttemptSpec,
  watchAttemptEvents,
} from "ticfac-harness";
import { getSandbox } from "@cloudflare/sandbox";
import type { FactoryBootOptions, FactorySandboxNamespace, FactorySandboxStub } from "./factory-sandbox";
import { isSandboxNamespace, sdkBootOptions, sdkSandboxDoor, type SdkSandboxDoor } from "./sandbox";
import { type ProxyOptions, proxyModelRequest } from "./gateway";
import type { Env } from "./index";
import {
  DO_V1,
  INSTANCE_BY_JOB_KIND,
  jobKindOfSandboxName,
  readRunSubstrate,
  runIDOfSandboxName,
  type RunSubstrate,
} from "./run-substrate";
import type { SandboxOutput } from "./sandbox";
import {
  WORKER_BOOT_COMMAND,
  WORKER_BOOT_MARKER,
  WORKER_BOOT_PROMPT_BEGIN,
  WORKER_BOOT_PROMPT_END,
  WORKER_FINISH_COMMAND,
} from "./worker-boot";

// ------------------------------------------------------------ constants ---

/** The harness every attempt this class hosts runs on. */
export const WORKER_AGENT_HARNESS = "pi-durable";

/** How often an unsettled attempt's alarm re-drives it. */
export const HEARTBEAT_MS = 60_000;

/** The most log one read returns. */
export const LOG_READ_MAX = 256 * 1024;

/** Where the host's record lives in this object's storage. */
const RECORD_KEY = "ticfac:worker-attempt";

// ---------------------------------------------------------------- types ---

/** What the door asks of an attempt: its phase, and once settled its exit code. */
export type WorkerAgentState = {
  /** `absent`: this object holds no attempt (never started, or not here). */
  phase: "absent" | WorkerAttemptPhase;
  /** The attempt's exit code once settled; null before, or when no phase gave one. */
  exit_code: number | null;
  /** Why it settled as it did. */
  detail: string | null;
  settled_at: string | null;
  /** The routed model the conversation runs on. */
  model: string | null;
  /** The attempt branch the boot named. */
  branch: string | null;
  started_at: string | null;
  harness: typeof WORKER_AGENT_HARNESS;
};

/** What a steer answers. */
export type WorkerAgentSteer = { ok: true; submission: number } | { ok: false; error: string };

/** The RPC surface the door, the status route and the reclaim call. */
export type WorkerAgentStub = {
  start(spec: WorkerAttemptSpec): Promise<WorkerAgentState>;
  state(): Promise<WorkerAgentState>;
  readLog(offset: number): Promise<SandboxOutput>;
  steer(text: string, requestId?: string): Promise<WorkerAgentSteer>;
  reclaim(reason: string): Promise<WorkerAgentState>;
  release(): Promise<void>;
  fetch(request: Request): Promise<Response>;
};

/** The WORKER_AGENTS namespace, structurally. */
export type WorkerAgentNamespace = {
  idFromName(name: string): DurableObjectId;
  get(id: DurableObjectId): WorkerAgentStub;
};

/**
 * How one run's attempts are hosted, when they are: the attempt's agent by
 * container name, and the boot its container starts on (the run's image pin,
 * the job's instance size).
 */
export type WorkerAgentHosting = {
  agent(name: string): WorkerAgentStub;
  boot(name: string): FactoryBootOptions;
};

/** Asked per run: null for a run whose workers are not WorkerAgents. */
export type WorkerAgentResolver = (runID: string) => Promise<WorkerAgentHosting | null>;

/**
 * The door a hosted attempt's tools run through: FactorySandbox's stub on
 * the `do_v1` substrate, the 0.x SDK behind the harness door on `sdk0`. Both
 * spell the harness package's `SandboxDoor`, plus the `destroy` a settled
 * attempt's release asks (tick hxd).
 */
export type AgentDoor = FactorySandboxStub | SdkSandboxDoor;

/**
 * Seams a test hands the object before it starts an attempt: the container
 * door and the model access. Production builds both from its bindings.
 */
export type WorkerAgentSeams = {
  door?: (name: string) => SandboxDoor;
  /** What releasing the seam's container does; nothing when unset. */
  destroy?: (name: string) => Promise<void>;
  models?: () => ReturnType<typeof gatewayModelAccess>;
  pollMs?: number;
};

// ------------------------------------------------------------- resolver ---

/**
 * The boot every container of a hosted attempt starts on. The substrate
 * decides what the boot can honestly ask: a do_v1 container starts on the
 * run's pinned image at its job's own instance size, while the 0.x
 * application has ONE image and ONE size for every boot (the app's, fixed
 * at deploy time), so a run pin or a per-job size there would be a promise
 * nothing honors — `keepAlive` is the whole of it, and the attempt's agent
 * destroys the container itself when it settles or the run reclaims it
 * (tick hxd).
 */
export function hostedBoot(
  name: string,
  image: string | null,
  substrate: RunSubstrate,
): FactoryBootOptions {
  return {
    keepAlive: true,
    ...(substrate === DO_V1
      ? { instance: INSTANCE_BY_JOB_KIND[jobKindOfSandboxName(name)] }
      : {}),
    ...(image === null ? {} : { pinnedImage: image }),
  };
}

/**
 * Which runs' workers are WorkerAgents: every one, on a deployment that binds
 * WORKER_AGENTS. A run's substrate decides only where its attempt's TOOLS
 * run (the DO's own `attemptDoor`): FactorySandbox on `do_v1`, the 0.x SDK
 * behind the harness door (`sdkSandboxDoor`) on `sdk0` — the default, the
 * substrate every run that asks for none is on (tick hxd). Undefined for a
 * deployment with no binding — every run then keeps the container's own
 * all-in-one worker.
 *
 * A test may bind WORKER_AGENTS to a seam-shaped `{ agent(name) }` instead of
 * a namespace; it is told apart by `idFromName`, as the sandbox bindings are.
 */
export function workerAgentsFromEnv(env: Env): WorkerAgentResolver | undefined {
  const binding = env.WORKER_AGENTS;
  if (binding === undefined || binding === null || env.DB === undefined) return undefined;
  const agent =
    typeof (binding as { idFromName?: unknown }).idFromName === "function"
      ? (name: string) => {
          const namespace = binding as WorkerAgentNamespace;
          return namespace.get(namespace.idFromName(name));
        }
      : (name: string) => (binding as { agent(name: string): WorkerAgentStub }).agent(name);
  const db = env.DB;
  return async (runID) => {
    const record = await readRunSubstrate(db, runID);
    return { agent, boot: (name) => hostedBoot(name, record.image, record.substrate) };
  };
}

// --------------------------------------------------------------- state ---

function stateOf(record: WorkerAttemptRecord | undefined): WorkerAgentState {
  if (record === undefined) {
    return {
      phase: "absent",
      exit_code: null,
      detail: null,
      settled_at: null,
      model: null,
      branch: null,
      started_at: null,
      harness: WORKER_AGENT_HARNESS,
    };
  }
  return {
    phase: record.phase,
    exit_code: record.settled?.exitCode ?? null,
    detail: record.settled?.detail ?? null,
    settled_at: record.settled?.at ?? null,
    model: record.spec.model,
    branch: record.boot?.branch ?? null,
    started_at: record.startedAt,
    harness: WORKER_AGENT_HARNESS,
  };
}

// ---------------------------------------------------------------- the DO ---

/**
 * The Durable Object `[[durable_objects.bindings]] WORKER_AGENTS` binds: one
 * instance per attempt, addressed by the attempt's container name.
 */
export class WorkerAgent extends DurableObject<Env> {
  /** Test seams; see {@link WorkerAgentSeams}. Unset in production. */
  seams: WorkerAgentSeams | undefined;
  private host: WorkerAttemptHost | undefined;
  private driving: Promise<void> | undefined;
  private stream: AgentEventStream | undefined;
  private logEnd: number;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.logEnd = this.prepareLog();
  }

  /** The log table, created when missing; answers where the log ends. */
  private prepareLog(): number {
    this.ctx.storage.sql.exec(
      `CREATE TABLE IF NOT EXISTS ticfac_worker_log (
         seq INTEGER PRIMARY KEY AUTOINCREMENT,
         start INTEGER NOT NULL,
         end INTEGER NOT NULL,
         text TEXT NOT NULL)`,
    );
    const last = this.ctx.storage.sql
      .exec<{ end: number }>("SELECT end FROM ticfac_worker_log ORDER BY seq DESC LIMIT 1")
      .toArray()[0];
    return last?.end ?? 0;
  }

  // ------------------------------------------------------------- the RPCs ---

  /** Records the attempt (once) and starts driving it; answers its state. */
  async start(spec: WorkerAttemptSpec): Promise<WorkerAgentState> {
    // A start under a SETTLED identity is a fresh attempt, as a fresh boot
    // under a settled container's name is a new work process (the door
    // records the boot again, which clears the settlement): everything this
    // object held — the conversation, the record, the log — goes, and the
    // attempt starts from nothing. An unsettled one is the adoption below.
    if ((await this.load())?.phase === "settled") await this.forget();
    const host = await this.hostFor(spec);
    const { fresh } = await host.start(spec);
    if (fresh) await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
    const record = await host.record();
    if (record !== undefined && record.phase !== "settled") this.kick(host);
    return stateOf(record);
  }

  async state(): Promise<WorkerAgentState> {
    return stateOf(await this.load());
  }

  /**
   * The attempt's log after `offset`, and the cursor to resume from — the
   * same contract as a container process's `readOutput`, so the door's log
   * drain reads either without telling them apart. Offsets are UTF-16 code
   * units of the log text.
   */
  async readLog(offset: number): Promise<SandboxOutput> {
    const rows = this.ctx.storage.sql
      .exec<{ start: number; end: number; text: string }>(
        "SELECT start, end, text FROM ticfac_worker_log WHERE end > ? ORDER BY seq LIMIT 512",
        offset,
      )
      .toArray();
    let text = "";
    let cursor = offset;
    for (const row of rows) {
      const piece = row.start < cursor ? row.text.slice(cursor - row.start) : row.text;
      if (text.length > 0 && text.length + piece.length > LOG_READ_MAX) break;
      text += piece;
      cursor = row.end;
    }
    return { text, offset: cursor };
  }

  async steer(text: string, requestId?: string): Promise<WorkerAgentSteer> {
    const host = this.host;
    if (host === undefined || host.live() === undefined) {
      return { ok: false, error: "the attempt is not conversing; there is nothing to steer" };
    }
    try {
      const steered = await host.steer(text, requestId);
      return { ok: true, submission: steered.submission };
    } catch (error) {
      return { ok: false, error: error instanceof Error ? error.message : String(error) };
    }
  }

  /**
   * The run is over: stop the attempt where it stands — no finish phase; the
   * wip checkpoints already pushed its work — and destroy its container.
   */
  async reclaim(reason: string): Promise<WorkerAgentState> {
    const record = await this.load();
    if (record === undefined) return stateOf(undefined);
    const host = await this.hostFor(record.spec);
    await host.reclaim(reason);
    await this.ctx.storage.deleteAlarm();
    await this.release();
    const state = stateOf(await this.load());
    this.broadcast({ type: "state", state });
    return state;
  }

  /** Drops everything this object holds, for a fresh attempt under its name. */
  private async forget(): Promise<void> {
    const host = this.host;
    this.host = undefined;
    await host?.close();
    const stream = this.stream;
    this.stream = undefined;
    await stream?.stop();
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.logEnd = this.prepareLog();
  }

  /** Destroys the attempt's container: a settled attempt holds a slot for nothing. */
  async release(): Promise<void> {
    const record = await this.load();
    if (record === undefined) return;
    if (this.seams?.door !== undefined) {
      await this.seams.destroy?.(record.spec.name);
      return;
    }
    const door = await this.attemptDoor(record.spec.name);
    if (door !== null) await door.destroy();
  }

  // ------------------------------------------------------- the heartbeat ---

  override async alarm(): Promise<void> {
    const record = await this.load();
    if (record === undefined || record.phase === "settled") return;
    await this.ctx.storage.setAlarm(Date.now() + HEARTBEAT_MS);
    this.kick(await this.hostFor(record.spec));
  }

  /** Starts the drive unless one is already running in this life. */
  private kick(host: WorkerAttemptHost): void {
    if (this.driving !== undefined) return;
    const drive = host
      .drive()
      .then(
        async (record) => {
          if (record.phase === "settled") {
            await this.ctx.storage.deleteAlarm();
            this.broadcast({ type: "state", state: stateOf(record) });
          }
        },
        async (error: unknown) => {
          // The next heartbeat drives again, in a fresh host: whatever this
          // life held (a harness, a stuck door call) is dropped with it.
          await this.appendLog(
            `ticfac-harness: the drive failed (${error instanceof Error ? error.message : String(error)}); ` +
              `the next heartbeat drives again\n`,
          );
          await host.close();
          if (this.host === host) this.host = undefined;
        },
      )
      .finally(() => {
        this.driving = undefined;
      });
    this.driving = drive;
    this.ctx.waitUntil(drive);
  }

  // ------------------------------------------------------------ watching ---

  /** A watch socket: the state, the live conversation's snapshot, then everything as it lands. */
  override async fetch(request: Request): Promise<Response> {
    if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
      return Response.json(
        { error: "upgrade_required", detail: "the watch route is a WebSocket" },
        { status: 426 },
      );
    }
    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    this.ctx.acceptWebSocket(server);
    server.send(JSON.stringify({ type: "state", state: await this.state() }));
    const live = this.host?.live();
    if (live !== undefined) {
      const snapshot = await watchAttemptEvents(live);
      server.send(JSON.stringify({ type: "events", events: [snapshot.snapshot] }));
      await snapshot.stop();
    }
    return new Response(null, { status: 101, webSocket: client });
  }

  override async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    let parsed: { type?: unknown; text?: unknown; request_id?: unknown };
    try {
      parsed = JSON.parse(
        typeof message === "string" ? message : new TextDecoder().decode(message),
      );
    } catch {
      ws.send(JSON.stringify({ type: "error", error: "a watch message is JSON" }));
      return;
    }
    if (parsed.type !== "steer" || typeof parsed.text !== "string") {
      ws.send(
        JSON.stringify({
          type: "error",
          error: 'the one message a watcher sends is {"type":"steer","text":…}',
        }),
      );
      return;
    }
    const steered = await this.steer(
      parsed.text,
      typeof parsed.request_id === "string" ? parsed.request_id : undefined,
    );
    ws.send(
      JSON.stringify(
        steered.ok
          ? { type: "steered", submission: steered.submission }
          : { type: "error", error: steered.error },
      ),
    );
  }

  override async webSocketClose(ws: WebSocket, code: number): Promise<void> {
    try {
      ws.close(code === 1005 ? 1000 : code, "closed");
    } catch {
      // Already closed.
    }
  }

  private broadcast(message: unknown): void {
    const text = JSON.stringify(message);
    for (const socket of this.ctx.getWebSockets()) {
      try {
        socket.send(text);
      } catch {
        // A socket that cannot take a frame is gone; its close handler runs.
      }
    }
  }

  /** Attaches the broadcast to the conversation this life just opened. */
  private async attach(live: Parameters<typeof watchAttemptEvents>[0]): Promise<void> {
    const previous = this.stream;
    this.stream = undefined;
    await previous?.stop();
    const stream = await watchAttemptEvents(live);
    stream.start(async (events) => {
      this.broadcast({ type: "events", events });
    });
    this.stream = stream;
  }

  // ------------------------------------------------------------- wiring ---

  private load(): Promise<WorkerAttemptRecord | undefined> {
    return this.ctx.storage.get<WorkerAttemptRecord>(RECORD_KEY);
  }

  /**
   * The attempt's container door, on whichever substrate its run was
   * submitted on (tick hxd): FactorySandbox's stub on `do_v1`, the 0.x SDK
   * behind the harness door on `sdk0`. A test's seam, when one is set, is
   * preferred to both — `WorkerAgentSeams.door`.
   */
  private async attemptDoor(name: string): Promise<AgentDoor | null> {
    const record = await readRunSubstrate(this.env.DB, runIDOfSandboxName(name));
    if (record.substrate === DO_V1) return this.sandboxStub(name);
    return this.sdkDoor(name);
  }

  /** The 0.x Sandbox namespace behind the harness door, or null without one. */
  private sdkDoor(name: string): SdkSandboxDoor | null {
    const binding = this.env.SANDBOXES;
    if (binding === undefined || binding === null) return null;
    if (!isSandboxNamespace(binding)) return null;
    // keepAlive, not sleepAfter: the conversation's gaps between tool rounds
    // are model calls that address no container, and an attempt's container
    // is destroyed the moment it settles (release) or its run ends
    // (reclaim) — the two endings that keepAlive was said to require.
    return sdkSandboxDoor(getSandbox(binding, name, sdkBootOptions({ keepAlive: true })));
  }

  private async hostFor(spec: WorkerAttemptSpec): Promise<WorkerAttemptHost> {
    if (this.host !== undefined) return this.host;
    const seams = this.seams;
    const door = seams?.door?.(spec.name) ?? (await this.attemptDoor(spec.name));
    if (door === null || door === undefined) {
      throw new Error(
        "this deployment binds no container namespace for a WorkerAgent's tools: " +
          "SANDBOXES_V1 (a run on the do_v1 substrate) or SANDBOXES (one on sdk0)",
      );
    }
    this.host = new WorkerAttemptHost({
      door,
      storage: () => openDurableObjectStorage(this.ctx.storage),
      models: seams?.models?.() ?? this.models(spec),
      records: {
        load: () => this.load(),
        save: (record) => this.ctx.storage.put(RECORD_KEY, record),
      },
      log: (text) => this.appendLog(text),
      protocol: {
        bootCommand: WORKER_BOOT_COMMAND,
        finishCommand: WORKER_FINISH_COMMAND,
        bootMarker: WORKER_BOOT_MARKER,
        promptBegin: WORKER_BOOT_PROMPT_BEGIN,
        promptEnd: WORKER_BOOT_PROMPT_END,
      },
      ...(seams?.pollMs === undefined ? {} : { pollMs: seams.pollMs, bashPollMs: seams.pollMs }),
      onReport: (error) => {
        console.error(`worker agent ${spec.name}: ${String(error)}`);
      },
      onConversation: (live) => {
        this.ctx.waitUntil(
          this.attach(live).catch((error: unknown) => {
            console.error(`worker agent ${spec.name}: the watch stream: ${String(error)}`);
          }),
        );
      },
    });
    return this.host;
  }

  /**
   * The conversation's model access: Workers AI through the gateway route,
   * on the attempt's own run token — answered by `proxyModelRequest` in this
   * isolate rather than over the factory's public hostname.
   */
  private models(spec: WorkerAttemptSpec) {
    const gateway = spec.env.AI_GATEWAY_BASE_URL ?? "";
    const token = spec.env.AI_GATEWAY_TOKEN ?? "";
    const env = this.env;
    const inProcess = ((input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input, init);
      const path = new URL(request.url).pathname.split("/").filter((part) => part !== "");
      const at = path.indexOf("gateway");
      return proxyModelRequest(env, request, path.slice(at + 1), this.gatewayProxyOptions());
    }) as typeof fetch;
    return gatewayModelAccess({ gateway, token, fetch: inProcess });
  }

  /**
   * The gateway route's options for this object's in-process model calls:
   * production's last hop is the global fetch to the operator's AI Gateway.
   * A staging subclass (src/staging-agent.ts) reaches it through an AI
   * binding instead — the one difference the staging gateway already makes.
   */
  protected gatewayProxyOptions(): ProxyOptions {
    return {};
  }

  /**
   * The attempt's FactorySandbox stub (`do_v1`), or null on a deployment
   * that binds none. The `sdk0` door is {@link sdkDoor}.
   */
  private sandboxStub(name: string) {
    const namespace = this.env.SANDBOXES_V1 as FactorySandboxNamespace | undefined;
    if (namespace === undefined || typeof namespace.idFromName !== "function") return null;
    return namespace.get(namespace.idFromName(name));
  }

  private async appendLog(text: string): Promise<void> {
    if (text === "") return;
    const start = this.logEnd;
    const end = start + text.length;
    this.ctx.storage.sql.exec(
      "INSERT INTO ticfac_worker_log (start, end, text) VALUES (?, ?, ?)",
      start,
      end,
      text,
    );
    this.logEnd = end;
    this.broadcast({ type: "log", text });
  }
}
