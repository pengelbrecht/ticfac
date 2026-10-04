/**
 * The worker attempt host (epic 43y, step 6 of
 * docs/spikes/n0b-round2-pi-durable.md, tick xd3): ONE attempt of one tick,
 * driven end to end on pi-durable — the container's boot phase, the
 * conversation, the container's finish phase — from a host that is NOT the
 * container.
 *
 * This is the runtime-neutral core of the cloud `WorkerAgent` Durable Object
 * (cloudflare/src/worker-agent.ts): the DO supplies the storage (its own DO
 * SQLite under pi-durable's `SqliteStorage`), the FactorySandbox door, the
 * model access (the gateway provider on the attempt's run token) and the
 * durable home of this host's own small record; everything the attempt DOES
 * is here, so it is tested here against a scripted door and a faux model
 * without a Durable Object in sight.
 *
 * ## The phases, and where each one's durability lives
 *
 *  1. **booting** — `ticks-worker --boot` runs in the attempt's container as
 *     its first command (tick pom): clone, branch, probes, toolchain, setup,
 *     guard, prompt. Its process id is recorded BEFORE it is polled, so a
 *     host that dies mid-boot reattaches to the same process instead of
 *     starting a second clone beside it. A non-zero exit is the attempt's
 *     verdict, with the boot's own exit code (2-8, 13-15: the boot-stopped
 *     classes collect keys on). A container lost mid-boot boots again — the
 *     boot clones fresh, so a second run of it is the same boot.
 *  2. **conversing** — the prompt the boot printed between its markers is
 *     submitted to the root conversation under a fixed `requestId`, so a
 *     host that dies between the submit and the record finds the SAME
 *     submission on its next life. Every model turn and tool call is
 *     pi-durable's own commit; a reopened harness resumes them
 *     (`harness.resume()`), and a tracked bash reattaches to its process.
 *     The wall deadline is ABSOLUTE (recorded at start), so a restart does
 *     not buy the attempt a fresh wall.
 *  3. **finishing** — `ticks-worker --finish <status>` runs once the
 *     conversation settles (0 done, 124 the wall fired, 1 anything else):
 *     the ledger, the sweep, the salvage, the report, the commit, the push,
 *     and the exit code 9/10/11 from git facts. Before it runs the host
 *     makes sure the box still holds the workspace (a container lost after
 *     the last round is restored from the attempt branch) and that the boot
 *     record the finish phase reads is there.
 *  4. **settled** — the finish phase's exit code is the attempt's, exactly
 *     what the all-in-one entrypoint's exit code was: the dispatch door's
 *     state route answers it as `succeeded`/`failed` with that code, and
 *     collect reads the pushed branch as it always has.
 *
 * Every phase transition is a write of the host's record, and the record is
 * what a restarted host drives from: `drive()` is idempotent across lives.
 */

import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Models } from "@earendil-works/pi-ai";
import {
  type Conversation,
  createRegistry,
  defineExtension,
  GenerationTask,
  Harness,
  hook,
  type Registry,
  type Storage,
  type Submission,
  type SubmissionId,
  section,
  ToolTask,
} from "@earendil-works/pi-durable";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { FactorySandboxEnv } from "../env/factory-sandbox.js";
import type { SandboxBootOptions, SandboxDoor } from "../env/sandbox-door.js";
import { gatewayModelRef } from "../gateway/workers-ai.js";
import { createTrackedBashTool } from "../tools/tracked-bash.js";
import { armWallDeadline, WORKER_HEADLESS_LINE, workerOnYield } from "../worker-contract.js";
import {
  retireWipSnapshot,
  type WorkspaceGit,
  workspaceCheckpointExtension,
} from "../workspace/checkpoints.js";

// ------------------------------------------------------------ constants ---

/**
 * The container's half of the contract, as contracts/worker-boot-contract.json
 * pins it (`boot_command`, `finish_command`, `setup_command`, `boot_marker`,
 * `boot_prompt_begin`, `boot_prompt_end`). Defaults only: the cloud host hands
 * in the factory's own constants (cloudflare/src/worker-boot.ts, pinned to the
 * same file by its own suite), and this package's suite pins these to it.
 */
export const WORKER_BOOT_PROTOCOL = {
  bootCommand: "/usr/local/bin/ticks-worker --boot",
  finishCommand: "/usr/local/bin/ticks-worker --finish",
  setupCommand: "/usr/local/bin/ticks-worker --setup",
  bootMarker: "ticks-worker-boot-ok",
  promptBegin: "ticks-worker-boot-prompt-begin",
  promptEnd: "ticks-worker-boot-prompt-end",
} as const;

export type WorkerBootProtocol = { readonly [K in keyof typeof WORKER_BOOT_PROTOCOL]: string };

/** The `requestId` the worker prompt is submitted under: one prompt per attempt, ever. */
export const WORKER_PROMPT_REQUEST_ID = "ticfac-worker-prompt";

/** The harness status the finish phase is handed when the conversation answered. */
export const HARNESS_STATUS_DONE = 0;
/** The harness status for a conversation the wall deadline aborted: timeout(1)'s own. */
export const HARNESS_STATUS_WALL = 124;
/** The harness status for a conversation that settled unanswered for any other reason. */
export const HARNESS_STATUS_UNANSWERED = 1;

/** How many times a container lost under a boot or a finish phase is asked again. */
export const PHASE_RETRIES = 2;

/** How often a container phase process is asked for state and output. */
const PHASE_POLL_MS = 2_000;

/** The image's git identity (image/common.sh `git_identity_name`/`_email`). */
export const WORKER_GIT_IDENTITY = {
  name: "ticks sandbox",
  email: "ticks-sandbox@ticks.invalid",
} as const;

/** image/worker.sh's default state dir, where the boot records its branch. */
const DEFAULT_STATE_DIR = "/tmp/ticks-worker";

/** image/common.sh's default checkout. */
const DEFAULT_WORKDIR = "/work/repo";

// ---------------------------------------------------------------- types ---

/**
 * Everything one attempt is started with — JSON, recorded once at start, so
 * a restarted host drives the same attempt and no other.
 */
export type WorkerAttemptSpec = {
  /** The attempt's container name (the FactorySandbox it runs its tools in). */
  readonly name: string;
  readonly tick: string;
  readonly role: string;
  /**
   * The container environment the boot and finish phases run with — the
   * factory's `workerBootEnv`: the run's inputs, the gateway, the git
   * credentials, the prompt. The model's tool commands never see it.
   */
  readonly env: Readonly<Record<string, string>>;
  /** The routed model (`workers-ai/@cf/…`); refused unless it is Workers AI. */
  readonly model: string;
  /** The repository the attempt branch lives on — the restore's remote. */
  readonly repoUrl: string;
  /** The commit the attempt was cut from — the restore's floor. */
  readonly baseSha: string;
  /** The dispatch's harness budget, in ms; absent is unbounded. */
  readonly wallMs?: number;
  /** How the container is booted (lifetime, size, the run's image pin). */
  readonly boot?: SandboxBootOptions;
};

/** The phases of {@link WorkerAttemptRecord}. */
export type WorkerAttemptPhase = "booting" | "conversing" | "finishing" | "settled";

/** How an attempt settled: the exit code its finish (or its boot) phase gave. */
export type WorkerAttemptSettlement = {
  /** The attempt's exit code, or null when no phase could give one. */
  readonly exitCode: number | null;
  /** The phase that decided it. */
  readonly phase: Exclude<WorkerAttemptPhase, "settled"> | "reclaimed";
  readonly detail: string;
  readonly at: string;
};

/** The host's own durable record: the phase, and what each phase left the next. */
export type WorkerAttemptRecord = {
  readonly schema: 1;
  readonly spec: WorkerAttemptSpec;
  readonly phase: WorkerAttemptPhase;
  readonly startedAt: string;
  /** Epoch ms the wall fires at, fixed at start; absent is unbounded. */
  readonly deadlineAt?: number;
  readonly boot?: {
    readonly processId?: string;
    readonly tries: number;
    readonly branch?: string;
    readonly result?: string;
    readonly prompt?: string;
  };
  /** The worker prompt's submission (a pi-durable `SubmissionId`, a number). */
  readonly submissionId?: number;
  readonly harnessStatus?: number;
  readonly finish?: { readonly processId?: string; readonly tries: number };
  readonly settled?: WorkerAttemptSettlement;
};

/** Where the host keeps its record: the DO's own storage in the cloud, memory in tests. */
export type WorkerAttemptRecordStore = {
  load(): Promise<WorkerAttemptRecord | undefined>;
  save(record: WorkerAttemptRecord): Promise<void>;
};

/** What the host is built over — none of it JSON, all of it re-supplied every life. */
export type WorkerAttemptDeps = {
  /** The attempt's FactorySandbox, structurally. */
  readonly door: SandboxDoor;
  /** The conversation's storage, opened once per host life. */
  readonly storage: () => Promise<Storage>;
  /** Model access: the gateway provider on the attempt's run token, registered. */
  readonly models: Models;
  readonly records: WorkerAttemptRecordStore;
  /** The attempt's log: what the boot and finish printed, and what the agent did. */
  readonly log: (text: string) => void | Promise<void>;
  /** The container's half of the contract; default {@link WORKER_BOOT_PROTOCOL}. */
  readonly protocol?: WorkerBootProtocol;
  readonly now?: () => number;
  /** Poll interval for the container phases; tests make it short. */
  readonly pollMs?: number;
  /** The env's tracked-bash poll interval; tests make it short. */
  readonly bashPollMs?: number;
  /** The boundary guard's directory; null leaves the boot's own guard to stand alone. */
  readonly guardDir?: string | null;
  /** Extension failures pi-durable does not fail an operation for. */
  readonly onReport?: (error: unknown) => void;
  /**
   * Called each time this life opens the attempt's conversation — where a
   * host attaches its watchers (the WorkerAgent's sockets) to the live
   * harness. Never awaited; a throw is reported, not the attempt's.
   */
  readonly onConversation?: (live: { harness: Harness; conversation: Conversation }) => void;
};

/** What a parsed boot handoff carries. */
export type BootHandoff = { branch: string; result: string; prompt: string };

// ------------------------------------------------------- the handoff parse ---

/**
 * Reads the boot phase's handoff out of its whole output: the marker line
 * (`<marker> branch=<b> result=<r>`, possibly behind the script's own
 * `ticks-worker:` prefix) and the prompt between the two prompt markers,
 * whole and verbatim. Null when the handoff is not all there — a boot that
 * exited 0 without printing it is a boot the host must not converse on.
 */
export function parseBootHandoff(
  output: string,
  protocol: WorkerBootProtocol = WORKER_BOOT_PROTOCOL,
): BootHandoff | null {
  const lines = output.split("\n");
  let branch: string | undefined;
  let result: string | undefined;
  let begin = -1;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] as string;
    const at = line.indexOf(`${protocol.bootMarker} `);
    if (at >= 0 && begin < 0) {
      const fields = line.slice(at + protocol.bootMarker.length + 1).trim();
      branch = /(?:^|\s)branch=(\S+)/.exec(fields)?.[1];
      result = /(?:^|\s)result=(\S+)/.exec(fields)?.[1];
    }
    if (line === protocol.promptBegin && branch !== undefined) {
      begin = i;
      break;
    }
  }
  if (branch === undefined || result === undefined || begin < 0) return null;
  let end = -1;
  for (let i = lines.length - 1; i > begin; i--) {
    if (lines[i] === protocol.promptEnd) {
      end = i;
      break;
    }
  }
  if (end < 0) return null;
  const prompt = lines.slice(begin + 1, end).join("\n");
  if (prompt.trim() === "") return null;
  return { branch, result, prompt };
}

// -------------------------------------------------------------- the host ---

/** A container phase process's ending. */
type PhaseEnd = { kind: "exited"; exitCode: number; output: string } | { kind: "lost" };

/**
 * One attempt's host. Construct one per host LIFE (a DO instance, a test's
 * "process"); the record carries the attempt across lives.
 */
export class WorkerAttemptHost {
  private readonly protocol: WorkerBootProtocol;
  private readonly now: () => number;
  private readonly pollMs: number;
  private harness: Harness | undefined;
  private conversation: Conversation | undefined;
  private env: FactorySandboxEnv | undefined;
  private driving: Promise<WorkerAttemptRecord> | undefined;
  private reclaimed = false;
  private wallFired = false;

  constructor(private readonly deps: WorkerAttemptDeps) {
    this.protocol = deps.protocol ?? WORKER_BOOT_PROTOCOL;
    this.now = deps.now ?? Date.now;
    this.pollMs = deps.pollMs ?? PHASE_POLL_MS;
  }

  /**
   * Records the attempt — once. A second start of the same attempt (a door
   * retry, an adoption) answers the record that is already there and starts
   * nothing: the attempt's identity is its record.
   */
  async start(spec: WorkerAttemptSpec): Promise<{ record: WorkerAttemptRecord; fresh: boolean }> {
    const existing = await this.deps.records.load();
    if (existing !== undefined) return { record: existing, fresh: false };
    // Refused before anything is recorded: a model the gateway cannot serve
    // is a stop at the door, not a conversation that settles `no_model`.
    gatewayModelRef(spec.model);
    const now = this.now();
    const record: WorkerAttemptRecord = {
      schema: 1,
      spec,
      phase: "booting",
      startedAt: new Date(now).toISOString(),
      ...(spec.wallMs === undefined ? {} : { deadlineAt: now + spec.wallMs }),
      boot: { tries: 0 },
    };
    await this.deps.records.save(record);
    await this.say(`worker attempt ${spec.name}: recorded; booting the container`);
    return { record, fresh: true };
  }

  /** The record as it stands, or undefined before a start. */
  record(): Promise<WorkerAttemptRecord | undefined> {
    return this.deps.records.load();
  }

  /** The open harness and conversation, while the attempt is conversing in this life. */
  live(): { harness: Harness; conversation: Conversation } | undefined {
    return this.harness === undefined || this.conversation === undefined
      ? undefined
      : { harness: this.harness, conversation: this.conversation };
  }

  /**
   * Drives the attempt from wherever its record stands to settlement. One
   * drive per life at a time (a second call joins the first); safe to call
   * again in a new life — every phase resumes from its record.
   */
  drive(): Promise<WorkerAttemptRecord> {
    this.driving ??= this.driveOnce().finally(() => {
      this.driving = undefined;
    });
    return this.driving;
  }

  /**
   * An operator's (or the stuck watch's) steer: placed after the current tool
   * round, joining the running work (pi-durable `whenBusy: "steer"`). Refused
   * while the attempt is not conversing in this life.
   */
  async steer(text: string, requestId?: string): Promise<{ submission: number }> {
    if (text.trim() === "") throw new Error("a steer needs text");
    const live = this.live();
    if (live === undefined)
      throw new Error("the attempt is not conversing; there is nothing to steer");
    const submission = await live.conversation.submit(
      {
        type: "input",
        content: text,
        whenBusy: "steer",
        ...(requestId === undefined ? {} : { requestId }),
      },
      BACKGROUND_CONTEXT,
    );
    await this.say(`steer: ${brief(text, 300)}`);
    return { submission: submission.id };
  }

  /**
   * The run is over (or the slot is wanted): stop the attempt where it
   * stands, without a finish phase. Settled `reclaimed` — whatever the wip
   * checkpoints pushed is on the attempt branch already.
   */
  async reclaim(reason: string): Promise<WorkerAttemptRecord | undefined> {
    this.reclaimed = true;
    const record = await this.deps.records.load();
    if (record === undefined) return undefined;
    if (record.phase === "settled") return record;
    const live = this.live();
    if (live !== undefined) {
      await live.conversation.abort(BACKGROUND_CONTEXT).catch((error: unknown) => {
        this.report(error);
      });
    }
    const settled = await this.settle(record, {
      exitCode: null,
      phase: "reclaimed",
      detail: `reclaimed: ${reason}`,
    });
    await this.close();
    return settled;
  }

  /** Releases this life's harness. The record stays; a later life resumes from it. */
  async close(): Promise<void> {
    const harness = this.harness;
    this.harness = undefined;
    this.conversation = undefined;
    if (harness !== undefined) {
      await harness.close(BACKGROUND_CONTEXT).catch((error: unknown) => this.report(error));
    }
  }

  // ---------------------------------------------------------- the drive ---

  private async driveOnce(): Promise<WorkerAttemptRecord> {
    let record = await this.deps.records.load();
    if (record === undefined) throw new Error("no attempt recorded: start it before driving it");
    while (record.phase !== "settled") {
      if (this.reclaimed) {
        record = (await this.deps.records.load()) ?? record;
        if (record.phase === "settled") break;
      }
      switch (record.phase) {
        case "booting":
          record = await this.bootPhase(record);
          break;
        case "conversing":
          record = await this.conversePhase(record);
          break;
        case "finishing":
          record = await this.finishPhase(record);
          break;
      }
    }
    return record;
  }

  private async bootPhase(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    const spec = record.spec;
    const boot = record.boot ?? { tries: 0 };
    let processId = boot.processId;
    let tries = boot.tries;
    if (processId === undefined) {
      if (tries > PHASE_RETRIES) {
        return this.settle(record, {
          exitCode: null,
          phase: "booting",
          detail: `the container was lost under the boot phase ${tries} time(s); no boot finished`,
        });
      }
      tries += 1;
      const started = await this.deps.door.startProcess(
        this.protocol.bootCommand,
        { ...spec.env },
        { keepAlive: true, ...spec.boot },
      );
      processId = started.id;
      // Recorded BEFORE it is polled: a host that dies now reattaches to this
      // process in its next life rather than cloning a second time beside it.
      record = await this.save({ ...record, boot: { ...boot, tries, processId } });
      await this.say(`boot phase started (try ${tries}): ${this.protocol.bootCommand}`);
    }
    const end = await this.waitPhase(processId);
    if (end.kind === "lost") {
      await this.say("the container was lost under the boot phase; booting it again");
      return this.save({ ...record, boot: { tries } });
    }
    if (end.exitCode !== 0) {
      return this.settle(record, {
        exitCode: end.exitCode,
        phase: "booting",
        detail: `the boot phase stopped with exit ${end.exitCode}: the container never reached its conversation`,
      });
    }
    const handoff = parseBootHandoff(end.output, this.protocol);
    if (handoff === null) {
      return this.settle(record, {
        exitCode: 1,
        phase: "booting",
        detail:
          "the boot phase exited 0 without its handoff (the boot marker and the prompt between " +
          "its markers); there is no prompt to converse on",
      });
    }
    await this.say(
      `booted: branch ${handoff.branch}, report ${handoff.result}; conversing on ${spec.model}`,
    );
    return this.save({
      ...record,
      phase: "conversing",
      boot: { tries, processId, ...handoff },
    });
  }

  private async conversePhase(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    const live = await this.openConversation(record);
    let submission: Submission | undefined;
    if (record.submissionId !== undefined) {
      submission = await live.harness.submission(
        record.submissionId as SubmissionId,
        BACKGROUND_CONTEXT,
      );
    }
    if (submission === undefined) {
      // Idempotent by requestId: a host that died between this submit and the
      // record below finds the same submission on its next life.
      submission = await live.conversation.submit(
        {
          type: "input",
          content: record.boot?.prompt ?? "",
          requestId: WORKER_PROMPT_REQUEST_ID,
        },
        BACKGROUND_CONTEXT,
      );
      record = await this.save({ ...record, submissionId: submission.id });
    }
    live.harness.resume();

    const wall =
      record.deadlineAt === undefined
        ? undefined
        : armWallDeadline(
            {
              abort: async (context) => {
                this.wallFired = true;
                await this.say("the wall deadline fired: aborting the conversation");
                await live.conversation.abort(context);
              },
            },
            Math.max(0, record.deadlineAt - this.now()),
            BACKGROUND_CONTEXT,
          );
    let settled: Awaited<ReturnType<Submission["wait"]>>;
    try {
      settled = await submission.wait(BACKGROUND_CONTEXT);
      // Follow-ups (the nudge, the report pushback) continue the SAME run;
      // the run is over only when the conversation is idle.
      await live.conversation.waitForIdle(BACKGROUND_CONTEXT);
    } finally {
      wall?.cancel();
    }
    if (this.reclaimed) return (await this.deps.records.load()) ?? record;
    const status =
      settled.status === "done"
        ? HARNESS_STATUS_DONE
        : this.wallFired || (record.deadlineAt !== undefined && this.now() >= record.deadlineAt)
          ? HARNESS_STATUS_WALL
          : HARNESS_STATUS_UNANSWERED;
    await this.say(
      settled.status === "done"
        ? "the conversation settled done"
        : `the conversation settled unanswered (${settled.reason ?? "no reason"}); finishing with status ${status}`,
    );
    await this.close();
    return this.save({
      ...record,
      phase: "finishing",
      harnessStatus: status,
      finish: { tries: 0 },
    });
  }

  private async finishPhase(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    const spec = record.spec;
    const finish = record.finish ?? { tries: 0 };
    let processId = finish.processId;
    let tries = finish.tries;
    if (processId === undefined) {
      if (tries > PHASE_RETRIES) {
        return this.settle(record, {
          exitCode: null,
          phase: "finishing",
          detail: `the container was lost under the finish phase ${tries} time(s); nothing was pushed by it`,
        });
      }
      tries += 1;
      await this.prepareFinish(record);
      const status = record.harnessStatus ?? HARNESS_STATUS_UNANSWERED;
      const started = await this.deps.door.startProcess(
        `${this.protocol.finishCommand} ${status}`,
        { ...spec.env },
        { keepAlive: true, ...spec.boot },
      );
      processId = started.id;
      record = await this.save({ ...record, finish: { tries, processId } });
      await this.say(
        `finish phase started (try ${tries}): ${this.protocol.finishCommand} ${status}`,
      );
    }
    const end = await this.waitPhase(processId);
    if (end.kind === "lost") {
      await this.say("the container was lost under the finish phase; finishing again");
      return this.save({ ...record, finish: { tries } });
    }
    return this.settle(record, {
      exitCode: end.exitCode,
      phase: "finishing",
      detail: `the finish phase exited ${end.exitCode}`,
    });
  }

  /**
   * Before the finish phase: the box must still hold the workspace (a
   * container lost after the last round boots empty — restore it from the
   * attempt branch, where the last round's wip landed), and the finish
   * phase's own process must find the branch the boot recorded (a restored
   * container never ran the boot, so the record is written again).
   */
  private async prepareFinish(record: WorkerAttemptRecord): Promise<void> {
    const env = this.envFor(record);
    const ready = await env.ensureWorkspaceReady();
    if (ready.kind === "restored") {
      await this.say(
        `the workspace was lost before the finish phase; restored to ${ready.sha.slice(0, 12)}`,
      );
    } else if (ready.kind === "failed") {
      await this.say(`the workspace could not be checked before the finish phase: ${ready.error}`);
    }
    const branch = record.boot?.branch;
    if (branch === undefined) return;
    // The attempt branch back to the agent's own HEAD: the last round's wip
    // snapshot sits on it, and the finish phase's push is fast-forward only.
    const retired = await retireWipSnapshot(env.hostShell(), this.workspaceGit(record));
    if (retired.kind === "failed") {
      await this.say(`could not put the attempt branch back on the agent's HEAD: ${retired.error}`);
    }
    const stateDir = record.spec.env.TICKS_WORKER_STATE_DIR ?? DEFAULT_STATE_DIR;
    const out = await this.deps.door.run(
      'mkdir -p "$S" && { [ -s "$S/branch" ] || printf \'%s\\n\' "$B" > "$S/branch"; }',
      { S: stateDir, B: branch },
      { boot: { keepAlive: true, ...record.spec.boot } },
    );
    if (!out.ready || out.exitCode !== 0) {
      await this.say("could not confirm the boot's branch record for the finish phase");
    }
  }

  // ---------------------------------------------------- the conversation ---

  private async openConversation(
    record: WorkerAttemptRecord,
  ): Promise<{ harness: Harness; conversation: Conversation }> {
    const open = this.live();
    if (open !== undefined) return open;
    const spec = record.spec;
    const env = this.envFor(record);
    const registry = this.registryFor(record, env);
    const storage = await this.deps.storage();
    const harness = await Harness.open(
      storage,
      {
        models: this.deps.models,
        registry,
        env: () => env,
        onReport: (error) => this.report(error),
      },
      BACKGROUND_CONTEXT,
    );
    const conversation = await harness.root(BACKGROUND_CONTEXT, {
      agent: { model: gatewayModelRef(spec.model), cwd: env.cwd },
    });
    this.harness = harness;
    this.conversation = conversation;
    try {
      this.deps.onConversation?.({ harness, conversation });
    } catch (error) {
      this.report(error);
    }
    return { harness, conversation };
  }

  /** The env the tools run through: the attempt's container, its workspace, its guard. */
  private envFor(record: WorkerAttemptRecord): FactorySandboxEnv {
    if (this.env !== undefined) return this.env;
    const spec = record.spec;
    const workdir = spec.env.TICKS_WORKDIR ?? DEFAULT_WORKDIR;
    const git = this.workspaceGit(record);
    this.env = new FactorySandboxEnv({
      sandbox: pinnedDoor(this.deps.door, spec.boot),
      name: spec.name,
      cwd: workdir,
      ...(this.deps.guardDir === undefined ? {} : { guardDir: this.deps.guardDir }),
      ...(this.deps.bashPollMs === undefined ? {} : { pollMs: this.deps.bashPollMs }),
      ...(git.branch === "" ? {} : { workspace: git }),
    });
    return this.env;
  }

  private registryFor(record: WorkerAttemptRecord, env: FactorySandboxEnv): Registry {
    const spec = record.spec;
    const result = record.boot?.result ?? `RESULT-${spec.tick}.md`;
    const workdir = env.cwd;
    const reportPath = result.startsWith("/") ? result : `${workdir}/${result}`;
    const registry = createRegistry();
    registry.install(CodingTools);
    registry.install(
      defineExtension({
        name: "ticfac-worker",
        // A later extension's tool of the same name replaces CodingTools' own:
        // the bash every round runs is the replay-safe tracked one.
        tools: [createTrackedBashTool()],
        sections: [
          section(
            "ticfac-worker",
            () =>
              `${WORKER_HEADLESS_LINE}\n\nYour working directory is ${workdir}, a checkout of the ` +
              `repository on branch ${record.boot?.branch ?? "(unknown)"}. Every tool runs there.`,
            { tag: false },
          ),
        ],
        hooks: [
          hook(GenerationTask, {
            onYield: workerOnYield(env, {
              reportPath,
              repoDir: workdir,
              branch: record.boot?.branch ?? "",
              tick: spec.tick,
              role: spec.role,
              log: (line) => void this.say(line),
            }),
            afterResponse: (message) => {
              const text = message.content
                .flatMap((block) => (block.type === "text" ? [block.text] : []))
                .join(" ");
              if (text.trim() !== "") void this.say(`assistant: ${brief(text, 500)}`);
            },
          }),
          hook(ToolTask, {
            beforeTool: (call) => {
              void this.say(`tool ${call.name}: ${brief(JSON.stringify(call.arguments), 300)}`);
              return undefined;
            },
          }),
        ],
      }),
    );
    if (record.boot?.branch !== undefined) {
      registry.install(
        workspaceCheckpointExtension({
          shell: env.hostShell(),
          workspace: this.workspaceGit(record),
          // The between-rounds ready check (tick 4fs): a container lost while
          // no tool was in flight boots empty, and only the host sees it.
          ensureReady: () => env.ensureWorkspaceReady(),
          onRestore: (outcome) =>
            void this.say(
              outcome.kind === "restored"
                ? `the container was lost between rounds; workspace restored to ${outcome.sha.slice(0, 12)}`
                : `the container was lost between rounds and the restore failed: ${outcome.error}`,
            ),
          onCheckpoint: (outcome) => {
            if (outcome.kind === "pushed")
              void this.say(`wip checkpoint ${outcome.sha.slice(0, 12)} pushed`);
            if (outcome.kind === "failed") void this.say(`wip checkpoint failed: ${outcome.error}`);
          },
        }),
      );
    }
    return registry;
  }

  // ------------------------------------------------------------- helpers ---

  /**
   * The attempt's workspace git: its branch (the boot's), its remote, its
   * credentials — and its SETUP (tick i3h): the restore re-runs the
   * repository's `[sandbox]` setup through the contract's `--setup` entry, so
   * a container lost mid-turn comes back with its dependency installs, not
   * just its tree. The setup rides the same env the restore's fetch needed,
   * because the entry honours the wave's TICKS_WORKER_SETUP lever and checks
   * the declared image against the one the container boots on.
   */
  private workspaceGit(record: WorkerAttemptRecord): WorkspaceGit {
    const spec = record.spec;
    return {
      remote: spec.repoUrl,
      branch: record.boot?.branch ?? "",
      identity: WORKER_GIT_IDENTITY,
      base: spec.baseSha,
      setup: this.protocol.setupCommand,
      env: restoreEnv(spec.env),
    };
  }

  /** Polls a container phase process to its end, logging its output as it comes. */
  private async waitPhase(processId: string): Promise<PhaseEnd> {
    const door = this.deps.door;
    let cursor = 0;
    let output = "";
    while (true) {
      if (this.reclaimed) return { kind: "lost" };
      const view = await door.getProcess(processId);
      if (view === null) return { kind: "lost" };
      const chunk = await door.readOutput(processId, cursor);
      if (chunk.text !== "") {
        output += chunk.text;
        await this.deps.log(chunk.text);
      }
      cursor = chunk.offset;
      if (view.state !== "running") {
        // The rest of the output, to its end: the handoff is at the tail.
        for (let more = await door.readOutput(processId, cursor); more.text !== ""; ) {
          output += more.text;
          await this.deps.log(more.text);
          cursor = more.offset;
          more = await door.readOutput(processId, cursor);
        }
        if (view.exit_code === null) return { kind: "lost" };
        return { kind: "exited", exitCode: view.exit_code, output };
      }
      await sleep(this.pollMs);
    }
  }

  private async settle(
    record: WorkerAttemptRecord,
    settlement: Omit<WorkerAttemptSettlement, "at">,
  ): Promise<WorkerAttemptRecord> {
    const current = (await this.deps.records.load()) ?? record;
    if (current.phase === "settled") return current;
    const settled = await this.save({
      ...current,
      phase: "settled",
      settled: { ...settlement, at: new Date(this.now()).toISOString() },
    });
    await this.say(
      `settled: exit ${settlement.exitCode === null ? "none" : settlement.exitCode} (${settlement.detail})`,
    );
    return settled;
  }

  /**
   * Writes the record — never over a settled one: a reclaim settles the
   * attempt from outside the drive, and a phase that was mid-poll when it
   * did must not write the attempt back to life.
   */
  private async save(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    const current = await this.deps.records.load();
    if (current?.phase === "settled") return current;
    await this.deps.records.save(record);
    return record;
  }

  private async say(line: string): Promise<void> {
    try {
      await this.deps.log(`ticfac-harness: ${line}\n`);
    } catch (error) {
      this.report(error);
    }
  }

  private report(error: unknown): void {
    try {
      this.deps.onReport?.(error);
    } catch {
      // onReport must not throw; a broken one is not the attempt's problem.
    }
  }
}

// --------------------------------------------------------------- helpers ---

/**
 * The door every container call of the attempt goes through: the run's boot
 * options (its image pin, its instance size) merged into every call that may
 * start a container — so a container lost and booted again by the env's
 * next command comes back on the RUN's image, not the deployment's default.
 */
export function pinnedDoor(door: SandboxDoor, boot: SandboxBootOptions | undefined): SandboxDoor {
  if (boot === undefined) return door;
  return {
    run: (command, env, options) =>
      door.run(command, env, { ...options, boot: { ...options?.boot, ...boot } }),
    startProcess: (command, env, options) =>
      door.startProcess(command, env, { ...options, ...boot }),
    getProcess: (id) => door.getProcess(id),
    listProcesses: () => door.listProcesses(),
    readOutput: (id, offset) => door.readOutput(id, offset),
    killProcess: (id) => door.killProcess(id),
  };
}

/**
 * The env a RESTORE needs from the boot's: the git credentials its fetches
 * run under, and the slots the restored box's setup re-run reads — the
 * wave's setup lever (TICKS_WORKER_SETUP), the image the declared-image check
 * compares against (TICKS_SANDBOX_IMAGE) and the workspace the entry runs
 * in (TICKS_WORKDIR, whose default the host's own cwd already matches).
 * Nothing else of the boot env: the model's credentials in particular never
 * reach a restore.
 */
function restoreEnv(env: Readonly<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const name of [
    "GITHUB_TOKEN",
    "TICKS_GITHUB_TOKEN_URL",
    "TICKS_FACTORY_TOKEN",
    "TICKS_WORKER_SETUP",
    "TICKS_SANDBOX_IMAGE",
    "TICKS_WORKDIR",
  ]) {
    const value = env[name];
    if (value !== undefined && value !== "") out[name] = value;
  }
  return out;
}

function brief(text: string, max: number): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length <= max ? flat : `${flat.slice(0, max - 1)}…`;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
