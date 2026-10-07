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
  type AgentEvent,
  type AgentEventStream,
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
  watchEvents,
} from "@earendil-works/pi-durable";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { bashNonceMarker, FactorySandboxEnv } from "../env/factory-sandbox.js";
import type { SandboxBootOptions, SandboxDoor } from "../env/sandbox-door.js";
import { gatewayModelRef } from "../gateway/workers-ai.js";
import { createTrackedBashTool } from "../tools/tracked-bash.js";
import { armWallDeadline, WORKER_HEADLESS_LINE, workerOnYield } from "../worker-contract.js";
import {
  retireWipSnapshot,
  type WorkspaceGit,
  workspaceCheckpointExtension,
} from "../workspace/checkpoints.js";
import {
  CONTAINER_CPU_COMMAND,
  checkEveryMs,
  DEFAULT_STUCK_MS,
  type DurableWatch,
  decideStuck,
  judgeResume,
  lookTimeoutMs,
  observeCpu,
  parseContainerCpuMs,
  type StuckSignals,
  type StuckStep,
  type StuckWatchState,
  stuckEvidence,
  stuckPrompt,
} from "./stuck-watch.js";

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
   * What the attempt IS (tick 8gd). A `worker` implements one tick: its work
   * lands on the branch its boot reports, its wip is checkpointed there, and
   * its finish pushes a report. A `review` reads one pull request and writes
   * one findings file: it commits nothing, so it has no branch to checkpoint
   * to (the boot's `branch=` is the ref that was reviewed) and no workspace
   * to restore — a container lost before the finish has lost its findings,
   * and the finish fails honestly. Default `worker`, for every spec recorded
   * before the field existed.
   */
  readonly kind?: "worker" | "review";
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
  /**
   * The stuck watch's window, in ms (tick xba): how long the attempt may
   * show no activity — no conversation commit, no container CPU — before the watch
   * nudges it with a steer, and again before it stops it. Absent is the
   * default window (stuck-watch.ts `DEFAULT_STUCK_MS`, the local watch's
   * own); zero turns the watch off (the run's negative `StuckAfter`, which
   * the dispatch door spells as zero because it refuses negatives).
   */
  readonly stuckMs?: number;
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
  /**
   * The stuck watch's memory across host lives (stuck-watch.ts
   * `DurableWatch`): the conversation's newest entry and when it appeared,
   * its last progress commit, and the run of quiet resumes — so a new life
   * neither resets the window nor reads its own resume as activity.
   */
  readonly watch?: DurableWatch;
  /** Set once the watch stopped the attempt as stuck: a later life finishes, never resumes. */
  readonly stuck?: { readonly reason: string; readonly at: string };
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

/**
 * The attempt's kind, defaulted: specs recorded before the field existed are
 * workers, which is what they all were.
 */
export function kindOf(spec: { readonly kind?: "worker" | "review" }): "worker" | "review" {
  return spec.kind ?? "worker";
}

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
  /** When the conversation last committed progress — the stuck watch's transcript signal. */
  private lastCommitAt: number | null = null;
  /** The conversation's newest entry id, as this life has seen it. */
  private lastEntryId: number | undefined;
  /** This life's own subscription to the conversation's commit stream. */
  private commits: AgentEventStream | undefined;
  /** When this life last wrote its commit time into the record. */
  private commitSavedAt = 0;
  private stuckStopped = false;
  /** Ends the conversation's wait without it: armed by a stuck stop, after its grace. */
  private giveUp: (() => void) | undefined;
  private giveUpTimer: ReturnType<typeof setTimeout> | undefined;
  /** The record's write chain: one write at a time (see {@link save}). */
  private writes: Promise<void> = Promise.resolve();

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
    const commits = this.commits;
    this.commits = undefined;
    if (commits !== undefined) {
      await commits.stop().catch((error: unknown) => this.report(error));
    }
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
    // A life that finds the attempt already stopped as stuck (the stopping
    // life died before it could finish) finishes it; it never resumes it.
    if (record.stuck !== undefined) return this.toFinish(record, HARNESS_STATUS_UNANSWERED);
    const live = await this.openConversation(record);
    let submission: Submission | undefined;
    if (record.submissionId !== undefined) {
      submission = await live.harness.submission(
        record.submissionId as SubmissionId,
        BACKGROUND_CONTEXT,
      );
      // The record names a submission only a previous life made: this host
      // is a new one, picking the conversation up where the last one died.
      if (submission !== undefined) {
        await this.say(
          `a new host life resumed the conversation from its storage (submission ${record.submissionId})`,
        );
        record = await this.judgeResume(record);
        if (record.stuck !== undefined) {
          await this.killTrackedTools();
          await this.close();
          return this.toFinish(record, HARNESS_STATUS_UNANSWERED);
        }
      }
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
    // The stuck stop's backstop: an abort reaches a tool only at its next
    // poll, and a tool parked on a door call that never comes back never
    // polls again — so a stopped conversation that has not settled within
    // its grace is left behind, and the attempt finishes without it.
    const gaveUp = new Promise<"gave-up">((resolve) => {
      this.giveUp = () => resolve("gave-up");
    });
    // The stuck watch (tick xba), armed beside the wall for the
    // conversation's life in THIS host: quiet for the window on every
    // signal → one nudge, a steer; still quiet a window past it → the
    // stop, which is the wall's own mechanism — the conversation aborted,
    // the finish phase run with the unanswered status, the run's retry
    // ladder (not the wall clock) taking it from there.
    const stuck = this.armStuckWatch(live, record);
    let settled: Awaited<ReturnType<Submission["wait"]>> | "gave-up";
    try {
      settled = await Promise.race([
        (async () => {
          const ended = await submission.wait(BACKGROUND_CONTEXT);
          // Follow-ups (the nudge, the report pushback) continue the SAME
          // run; the run is over only when the conversation is idle.
          await live.conversation.waitForIdle(BACKGROUND_CONTEXT);
          return ended;
        })(),
        gaveUp,
      ]);
    } finally {
      this.giveUp = undefined;
      if (this.giveUpTimer !== undefined) clearTimeout(this.giveUpTimer);
      this.giveUpTimer = undefined;
      stuck.cancel();
      wall?.cancel();
    }
    if (this.reclaimed) return (await this.deps.records.load()) ?? record;
    if (settled === "gave-up") {
      await this.say(
        "the stopped conversation did not settle within its grace (a tool call is parked on a " +
          "container call that never answered); finishing the attempt without it",
      );
      // Not awaited: a harness whose tool is wedged may never close.
      void this.close();
      return this.toFinish((await this.deps.records.load()) ?? record, HARNESS_STATUS_UNANSWERED);
    }
    const status =
      settled.status === "done"
        ? HARNESS_STATUS_DONE
        : this.wallFired || (record.deadlineAt !== undefined && this.now() >= record.deadlineAt)
          ? HARNESS_STATUS_WALL
          : HARNESS_STATUS_UNANSWERED;
    await this.say(
      settled.status === "done"
        ? "the conversation settled done"
        : this.stuckStopped
          ? `the conversation was stopped as stuck and settled unanswered; finishing with status ${status}`
          : `the conversation settled unanswered (${settled.reason ?? "no reason"}); finishing with status ${status}`,
    );
    await this.close();
    return this.toFinish((await this.deps.records.load()) ?? record, status);
  }

  /** The conversation is over (or abandoned): on to the finish phase with `status`. */
  private toFinish(record: WorkerAttemptRecord, status: number): Promise<WorkerAttemptRecord> {
    return this.save({
      ...record,
      phase: "finishing",
      harnessStatus: status,
      finish: { tries: 0 },
    });
  }

  /**
   * A new life resumed the conversation: count it (stuck-watch.ts
   * `judgeResume`), and stop the attempt as stuck in a resume loop when
   * lives keep resuming it where it stands. The watch off (a zero window)
   * is off here too.
   */
  private async judgeResume(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    const windowMs = record.spec.stuckMs ?? DEFAULT_STUCK_MS;
    const startedAt = Date.parse(record.startedAt);
    const now = this.now();
    const judged = judgeResume(
      record.watch,
      this.lastEntryId,
      now,
      windowMs,
      Number.isFinite(startedAt) ? startedAt : now,
    );
    if (!judged.stuck) {
      return (await this.saveWatchFields(() => ({ watch: judged.watch }))) ?? record;
    }
    const reason =
      `stuck in a resume loop: ${judged.idleResumes} host lives in a row resumed the conversation at ` +
      `entry ${this.lastEntryId ?? "none"}, which has gained no entry for ${Math.round(judged.quietMs / 1000)}s`;
    this.stuckStopped = true;
    await this.say(`the attempt is being stopped: ${reason}`);
    return (
      (await this.saveWatchFields(() => ({
        watch: judged.watch,
        stuck: { reason, at: new Date(now).toISOString() },
      }))) ?? record
    );
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
    // A review restores nothing (tick 8gd): its findings live in the
    // container's /tmp, not on a pushed branch, so a container lost before
    // the finish has lost them and the finish will fail honestly. The
    // worker-only steps below — the workspace restore, the wip retirement,
    // the branch record — are about work a review never holds.
    if (kindOf(record.spec) === "review") return;
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
    await this.watchCommits(harness, conversation, record);
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
      // Only a worker's tools restore from an attempt branch (tick 8gd): a
      // review commits nothing, so its boot's `branch=` (the reviewed ref)
      // must never be given to the restore — restoring a review's workspace
      // from the pull request would check out hostile code, the one thing
      // the review phase exists not to do.
      ...(kindOf(spec) === "worker" && git.branch !== "" ? { workspace: git } : {}),
      // The nonce path's ear (tick dbi): a tracked bash whose nonce no
      // container knows restores before it re-starts — until this wiring
      // that restore reached no log anywhere, and the operator watching the
      // run saw only a mysteriously slow tool round. The between-rounds
      // loss keeps its own line (the checkpoint extension's onRestore in
      // registryFor), so one restore says one line.
      onRestore: (outcome) =>
        void this.say(
          outcome.kind === "restored"
            ? `a tracked bash found a fresh container; the workspace was restored to ${outcome.sha.slice(0, 12)} (${outcome.subject})`
            : `a tracked bash found a fresh container and the workspace could not be restored: ${outcome.error}`,
        ),
    });
    return this.env;
  }

  private registryFor(record: WorkerAttemptRecord, env: FactorySandboxEnv): Registry {
    const spec = record.spec;
    const review = kindOf(spec) === "review";
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
              review
                ? // The review's own framing (tick 8gd): same headless line, but
                  // what it works against and what it owes are the review's —
                  // a diff to read as evidence, a findings file to write, no
                  // branch to commit to and no report to push.
                  `${WORKER_HEADLESS_LINE}\n\nYou are reviewing a pull request. Your working directory is ${workdir}, ` +
                  `the checkout at its base; the pull request itself is fetched as ${record.boot?.branch ?? "(unknown ref)"}, ` +
                  `read as evidence and never run. Every tool runs in the working directory. Write your ` +
                  `findings to ${reportPath} — that file is this attempt's only output.`
                : `${WORKER_HEADLESS_LINE}\n\nYour working directory is ${workdir}, a checkout of the ` +
                  `repository on branch ${record.boot?.branch ?? "(unknown)"}. Every tool runs there.`,
            { tag: false },
          ),
        ],
        hooks: [
          hook(GenerationTask, {
            // The report contract (the yield pushback, the lint-report check)
            // is a worker's: it commits on a branch and writes a STATUS'd
            // report. A review has neither — its findings file is posted by
            // the finish phase as-is — so no yield hook stands over it
            // (tick 8gd); the boot prompt carries the review's whole
            // instruction and the wall is the bound.
            ...(review
              ? {}
              : {
                  onYield: workerOnYield(env, {
                    reportPath,
                    repoDir: workdir,
                    branch: record.boot?.branch ?? "",
                    tick: spec.tick,
                    role: spec.role,
                    log: (line) => void this.say(line),
                  }),
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
    if (!review && record.boot?.branch !== undefined) {
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

  // ----------------------------------------------- the stuck watch ---

  /**
   * This life's subscription to the conversation's COMMIT STREAM — the stuck
   * watch's transcript signal (stuck-watch.ts). Progress is a model delta, a
   * tool's start, output or result, an appended entry, a turn; never the
   * host's own log, never an inbox change (the watch's own steer is one).
   * The newest entry is written into the record as it appears — entries are
   * one per model turn or tool result, so this is cheap — and the commit
   * time at most once a minute, so the next life starts from them.
   */
  private async watchCommits(
    harness: Harness,
    conversation: Conversation,
    record: WorkerAttemptRecord,
  ): Promise<void> {
    let stream: AgentEventStream;
    try {
      stream = await watchEvents(harness, conversation.id, BACKGROUND_CONTEXT);
    } catch (error) {
      // The watch is blinder without it, never the attempt's failure — and
      // a watch that cannot see commits must not read the attempt as silent.
      this.lastCommitAt = record.watch?.lastCommitAt ?? this.now();
      this.report(error);
      return;
    }
    this.lastEntryId = newestEntry(stream.snapshot.entries, undefined);
    // A new life dates the conversation's last progress from what it can
    // see, not from nothing: the record's last commit, or the newest dated
    // entry in the snapshot — whichever is newer. An attempt whose record
    // predates the watch's memory (a live attempt across the deploy that
    // brought it) and whose entries carry no date is given the benefit of
    // the doubt, dated now, rather than read as silent since its start.
    this.lastCommitAt = seedCommitAt(
      record.watch?.lastCommitAt,
      stream.snapshot.entries,
      record.watch === undefined ? this.now() : undefined,
    );
    this.commits = stream;
    stream.start(async (events) => {
      let entry = this.lastEntryId;
      let progressed = false;
      for (const event of events) {
        if (PROGRESS_EVENTS.has(event.type)) progressed = true;
        entry = newestEntry(entriesOf(event), entry);
      }
      const now = this.now();
      if (progressed) this.lastCommitAt = now;
      const newEntry = entry !== undefined && entry !== this.lastEntryId;
      this.lastEntryId = entry;
      if (newEntry || (progressed && now - this.commitSavedAt >= COMMIT_SAVE_EVERY_MS)) {
        await this.saveWatch(newEntry ? { lastEntryId: entry, lastEntryAt: now } : {});
      }
    });
  }

  /** Folds this life's commit time (and `patch`) into the record's durable watch. */
  private async saveWatch(patch: Partial<DurableWatch>): Promise<void> {
    try {
      this.commitSavedAt = this.now();
      const lastCommitAt = this.lastCommitAt;
      await this.saveWatchFields((current) => ({
        watch: {
          ...current.watch,
          ...(lastCommitAt === null ? {} : { lastCommitAt }),
          ...patch,
        },
      }));
    } catch (error) {
      this.report(error);
    }
  }

  /**
   * Arms this conversation's stuck watch (tick xba; the policy and the
   * signals are stuck-watch.ts). The look cadence is a tenth of the
   * window; one look at a time, so a slow door call cannot stack looks —
   * and a look never waits on the container past its own bound
   * (`lookTimeoutMs`), so a door call that never answers cannot blind the
   * watch either. The watch dies with the conversation it watches; a host
   * life that opens the conversation again arms a fresh one, but from the
   * record's last commit, not from nothing.
   */
  private armStuckWatch(
    live: { conversation: Conversation },
    record: WorkerAttemptRecord,
  ): { cancel: () => void } {
    const windowMs = record.spec.stuckMs ?? DEFAULT_STUCK_MS;
    if (!(windowMs > 0)) return { cancel: () => {} };
    const startedAt = Date.parse(record.startedAt);
    const state: StuckWatchState = {
      // The baseline is the attempt's start, the local watch's own rule
      // (activity.go): nothing the attempt did can be older, so a host
      // life that opens a long-quiet conversation does not reset the
      // window. Its own "a new host life resumed" line is NOT activity
      // (run_7445005f): only the conversation's commits are.
      firstSeenAt: Number.isFinite(startedAt) ? Math.min(startedAt, this.now()) : this.now(),
      cpuMarkMs: 0,
      cpuMarkAt: null,
      nudgedAt: null,
      steers: 0,
    };
    let looking = false;
    let stopped = false;
    const timer = setInterval(() => {
      if (looking || stopped) return;
      looking = true;
      void this.stuckLook(live, state, windowMs)
        .then((step: StuckStep) => {
          // The stop is terminal for the watch as for the attempt: no look
          // past it, so a stopped attempt is neither nudged nor stopped twice.
          if (step === "stop") {
            stopped = true;
            clearInterval(timer);
          }
        })
        .catch((error: unknown) => {
          this.report(error);
        })
        .finally(() => {
          looking = false;
        });
    }, checkEveryMs(windowMs));
    return {
      cancel: () => {
        stopped = true;
        clearInterval(timer);
      },
    };
  }

  /** One look: the signals, the decision, and what the decision does. */
  private async stuckLook(
    live: { conversation: Conversation },
    state: StuckWatchState,
    windowMs: number,
  ): Promise<StuckStep> {
    let cpuMs: number | null = null;
    try {
      const out = await bounded(
        this.deps.door.run(CONTAINER_CPU_COMMAND, {}, { maxBytes: 4096 }),
        lookTimeoutMs(windowMs),
      );
      // A container that cannot be asked — a refusal, an error, a call that
      // does not come back in time — is a signal that cannot be read: named
      // in the evidence, never decided on.
      if (out !== TIMED_OUT && out.ready) cpuMs = parseContainerCpuMs(out.output);
    } catch (error) {
      this.report(error);
    }
    if (cpuMs !== null) observeCpu(state, cpuMs, this.now(), windowMs);
    if (this.lastCommitAt !== null && this.now() - this.commitSavedAt >= COMMIT_SAVE_EVERY_MS) {
      await this.saveWatch({});
    }
    const signals: StuckSignals = { lastCommitAt: this.lastCommitAt, cpuRead: cpuMs !== null };
    const step = decideStuck(state, signals, this.now(), windowMs);
    if (step === "none") return step;
    const evidence = stuckEvidence(state, signals, this.now());
    if (step === "nudge") {
      state.steers += 1;
      // The nudge is a STEER (the local watch's durable-runner rule, tick
      // hpk): placed after the running tool round, joining the running
      // work — and each is its own idempotent submission, so a retry of an
      // unacknowledged nudge cannot place the message twice. A nudge that
      // could not be delivered is not counted: the next look re-attempts
      // it, exactly as the local watch re-delivers through herdr.
      try {
        await this.steer(stuckPrompt(evidence, windowMs), `ticfac-stuck-nudge-${state.steers}`);
        state.nudgedAt = this.now();
      } catch (error) {
        this.report(error);
      }
      return "nudge";
    }
    // The stop: the wall's own mechanism, so the finish phase still runs —
    // the ledger, the salvage, the report, the push — and the attempt
    // settles failed as the run's retry ladder expects, never as a reclaim.
    // Recorded first, so a life that dies mid-stop is finished by the next
    // one rather than resumed.
    this.stuckStopped = true;
    await this.say(`the attempt appears stuck and is being stopped: ${evidence}`);
    const at = new Date(this.now()).toISOString();
    await this.saveWatchFields(() => ({ stuck: { reason: `stopped as stuck: ${evidence}`, at } }));
    // If the conversation does not settle — its tool parked on a door call
    // that never answers, which the abort itself then waits on — the attempt
    // finishes without it. Armed before the abort, because the abort can be
    // what never comes back.
    this.giveUpTimer = setTimeout(() => this.giveUp?.(), Math.max(windowMs, 10));
    await bounded(
      live.conversation.abort(BACKGROUND_CONTEXT).catch((error: unknown) => {
        this.report(error);
      }),
      lookTimeoutMs(windowMs),
    );
    // The hung tool itself: an abort leaves a tracked process running (a
    // replay must find it), but a STOPPED attempt has no replay, and a tool
    // spinning in the container would burn it under the finish phase.
    await this.killTrackedTools();
    return "stop";
  }

  /** Kills the model's tracked bash processes still running in the container, each call bounded. */
  private async killTrackedTools(): Promise<void> {
    try {
      const listed = await bounded(this.deps.door.listProcesses(), KILL_CALL_MS);
      if (listed === TIMED_OUT) return;
      for (const view of listed) {
        if (view.state !== "running" || !view.command?.includes(BASH_NONCE_PREFIX)) continue;
        await bounded(this.deps.door.killProcess(view.id), KILL_CALL_MS);
        await this.say(`killed the stopped attempt's running tool process ${view.id}`);
      }
    } catch (error) {
      this.report(error);
    }
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
   *
   * The stuck watch writes the record too (its `watch` and `stuck`, from
   * the commit stream, concurrently with the drive), so writes are
   * serialized, and a write that does not own those fields carries the
   * stored ones forward rather than the stale copy its caller spread.
   */
  private save(record: WorkerAttemptRecord): Promise<WorkerAttemptRecord> {
    return this.write((current) => ({
      ...record,
      ...(current?.watch === undefined ? {} : { watch: current.watch }),
      ...(current?.stuck === undefined ? {} : { stuck: current.stuck }),
    }));
  }

  /** The stuck watch's own write: `patch` applied to the record as it stands, in the chain. */
  private saveWatchFields(
    patch: (current: WorkerAttemptRecord) => Pick<WorkerAttemptRecord, "watch" | "stuck">,
  ): Promise<WorkerAttemptRecord | undefined> {
    return this.write((current) =>
      current === undefined ? undefined : { ...current, ...patch(current) },
    );
  }

  private write<R extends WorkerAttemptRecord | undefined>(
    next: (current: WorkerAttemptRecord | undefined) => R,
  ): Promise<WorkerAttemptRecord | R> {
    const write = this.writes.then(async () => {
      const current = await this.deps.records.load();
      if (current?.phase === "settled") return current;
      const record = next(current);
      if (record === undefined) return record;
      await this.deps.records.save(record);
      return record;
    });
    this.writes = write.then(
      () => undefined,
      () => undefined,
    );
    return write;
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

/**
 * The commit-stream events that are the conversation's PROGRESS: what the
 * model and its tools did. Not an inbox change or a submission (the watch's
 * own steer is one), not usage or agent bookkeeping, not a retry's backoff.
 */
const PROGRESS_EVENTS: ReadonlySet<AgentEvent["type"]> = new Set([
  "message_start",
  "message_update",
  "message_end",
  "tool_execution_start",
  "tool_execution_update",
  "tool_execution_end",
  "entry_appended",
  "turn_start",
  "turn_end",
  "compaction_end",
]);

/** How often the conversation's commit time is written into the record. */
const COMMIT_SAVE_EVERY_MS = 60_000;

/** The bound on each door call a stuck stop makes to kill the hung tool. */
const KILL_CALL_MS = 10_000;

/** What every tracked bash's container command starts with (factory-sandbox.ts `bashNonceMarker`). */
const BASH_NONCE_PREFIX = bashNonceMarker("");

const TIMED_OUT: unique symbol = Symbol("timed out");

/** `promise`, or {@link TIMED_OUT} once `ms` pass first. The promise itself is left to run. */
function bounded<T>(promise: Promise<T>, ms: number): Promise<T | typeof TIMED_OUT> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    promise,
    new Promise<typeof TIMED_OUT>((resolve) => {
      timer = setTimeout(() => resolve(TIMED_OUT), ms);
    }),
  ]).finally(() => clearTimeout(timer));
}

/** The entries an agent event carries. */
function entriesOf(event: AgentEvent): readonly { id: unknown }[] {
  switch (event.type) {
    case "snapshot":
      return event.entries;
    case "message_end":
    case "entry_appended":
      return [event.entry];
    case "tool_execution_end":
      return event.entry === undefined ? [] : [event.entry];
    default:
      return [];
  }
}

/**
 * When a new life dates the conversation's last progress (see
 * `watchCommits`): the newer of the recorded commit and the newest message
 * timestamp among the snapshot's entries; `fallback` when neither is known.
 */
export function seedCommitAt(
  recorded: number | undefined,
  entries: readonly { model?: readonly unknown[] }[],
  fallback: number | undefined,
): number | null {
  let at = recorded ?? null;
  for (const entry of entries) {
    for (const message of entry.model ?? []) {
      const ts = (message as { timestamp?: unknown }).timestamp;
      if (typeof ts === "number" && Number.isFinite(ts) && (at === null || ts > at)) at = ts;
    }
  }
  return at ?? fallback ?? null;
}

/** The newest numeric entry id among `entries`, or `current`. */
function newestEntry(entries: readonly { id: unknown }[], current: number | undefined) {
  let newest = current;
  for (const entry of entries) {
    const id = Number(entry.id);
    if (Number.isFinite(id) && (newest === undefined || id > newest)) newest = id;
  }
  return newest;
}

function brief(text: string, max: number): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length <= max ? flat : `${flat.slice(0, max - 1)}…`;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
