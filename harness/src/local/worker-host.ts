/**
 * The local worker host (epic 43y step 7, tick hpk): the Go executor's
 * `pi` runner is this process — a Node host running the worker's whole
 * conversation on pi-durable over LOCAL SQLite, with its tools in the
 * attempt worktree through the guarded Node env.
 *
 * docs/spikes/n0b-round2-pi-durable.md, "Where the harness lives", the local
 * row: a Node process spawned by the Go executor per worker, local SQLite in
 * the attempt's state directory, `createGuardedNodeExecutionEnv` in the
 * worktree, and nothing else differs from the cloud host but the storage
 * backend and the env — one harness, two hosts (the cloud DO host is epic
 * step 6, tick xd3).
 *
 * What this module assembles, and why each piece is here:
 *
 * - **Storage** — pi-durable's own `openNodeSqliteStorage`, one file per
 *   attempt beside the supervisor's own state. One process owns it while it
 *   lives; a relaunch (the supervisor's nudge, pushback or stuck re-prompt
 *   of THIS runner) reopens the same file, `resume()`s what the last process
 *   left unfinished, and the conversation continues with its whole history —
 *   the pi-CLI path's `--session-id`, but durable.
 * - **The env** — `createGuardedNodeExecutionEnv` (tick kgk): every tool
 *   command resolves `tk` through the boundary guard, so a local worker
 *   meets the same boundary a container's does.
 * - **The contract as hooks** (tick pom) — `workerOnYield` carries the
 *   early-exit nudge and the report linter pushback as follow-ups in the
 *   SAME conversation, and `armWallDeadline` is the wall as `abort()`.
 * - **Workspace checkpoints** (tick dwn) — the `afterTools` wip commit
 *   pushed to the attempt branch: the same carried-work mechanism at
 *   tool-round granularity, and what a stopped attempt's successor starts
 *   from.
 * - **The finish phase** (tick nou) — the half this host owns where there
 *   is no container to run it in: the last round's wip snapshot RETIRED
 *   (the attempt branch back on the agent's own HEAD, the way the cloud
 *   host's prepareFinish does), and the uncommitted tree salvaged into its
 *   own commit (the cloud finish phase's own move). Without it a worker
 *   that settles without committing leaves the branch holding a snapshot
 *   its own history cannot count, and the supervisor's fast-forward push of
 *   HEAD is refused over it.
 * - **The steer socket** (`./steer-socket.ts`) — the door the supervisor's
 *   stuck watch steers a live worker through, instead of killing it and
 *   re-prompting a fresh process. THE STUCK NUDGE IS A STEER: the tick's
 *   acceptance criterion, and the one behaviour this host adds to the
 *   supervisor's ladder.
 *
 * The model access is the LOCAL rung's: pi-ai's own
 * `cloudflare-workers-ai` provider, credentials resolved from the host
 * environment exactly as the pi CLI resolved them (the operator's ambient
 * `CLOUDFLARE_API_KEY`/`CLOUDFLARE_ACCOUNT_ID`; this host holds no
 * factory gateway), with the same per-model catalog corrections the gateway
 * provider applies (`GATEWAY_MODEL_OVERRIDES`: GLM 5.3's `maxTokens` and
 * `thinkingFormat`). A routed model that is not Workers AI (or the tests'
 * faux) is refused by name — claude is the frontier rung's CLI, not this
 * harness's; the operator's decision recorded on the epic keeps the claude
 * CLI as the local frontier exception, and everything this host runs is
 * Workers AI.
 */

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { isAbsolute, relative } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Message } from "@earendil-works/pi-ai";
import {
  createModels,
  type FauxResponseStep,
  fauxAssistantMessage,
  fauxProvider,
  fauxText,
  fauxThinking,
  fauxToolCall,
  type Provider,
} from "@earendil-works/pi-ai";
import { cloudflareWorkersAIProvider } from "@earendil-works/pi-ai/providers/cloudflare-workers-ai";
import {
  createRegistry,
  defineExtension,
  GenerationTask,
  Harness,
  hook,
  type ModelRef,
  watchEvents,
} from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { createGuardedNodeExecutionEnv } from "../env/node.js";
import { GATEWAY_MODEL_OVERRIDES, gatewayModelRef } from "../gateway/workers-ai.js";
import { armWallDeadline, type WorkerContractOptions, workerOnYield } from "../worker-contract.js";
import {
  type HostShell,
  retireWipSnapshot,
  salvageUncommittedWork,
  type WorkspaceGit,
  workspaceCheckpointExtension,
} from "../workspace/checkpoints.js";
import { piAuthStore } from "./pi-auth-store.js";
import { openSteerServer, type SteerServer } from "./steer-socket.js";

/**
 * The config the Go executor writes beside the attempt record
 * (`worker.json`, internal/exec/subprocess) and `main.ts` reads. It is the
 * WHOLE interface between the halves: the runner argv carries only this
 * file's path, the model and the message.
 */
export type LocalWorkerConfig = {
  /** The attempt's local SQLite storage file, in the state directory. */
  readonly storage: string;
  /** The steer socket the supervisor's stuck watch steers through. */
  readonly steerSock: string;
  /** The attempt worktree: the tools' cwd, and where the report goes. */
  readonly worktree: string;
  /** The remote the attempt branch is pushed to ("" disables checkpoints). */
  readonly remote: string;
  /** The attempt branch — the run's write ref. */
  readonly branch: string;
  /** The base commit the attempt was cut from (a lost-workspace restore). */
  readonly base?: string;
  /** The report's absolute path — `RESULT-<tick>.md` inside the worktree. */
  readonly report: string;
  /** The report checker binary, absolute (the supervisor's own binary). */
  readonly checker: string;
  /** The tick this attempt implements. */
  readonly tick: string;
  /** The role the report is checked as, "" for role-neutral. */
  readonly role?: string;
  /** The routed model: `cloudflare-workers-ai/@cf/…`, or `faux/…` in tests. */
  readonly model: string;
  /** The wall as an ABSOLUTE epoch-ms deadline; 0 or past means none. */
  readonly wallDeadlineMs?: number;
  /**
   * A scripted faux transcript, tests only: one JSON array element per model
   * turn. The production path never sets it, and a config that does is a
   * config the executor's own tests wrote.
   */
  readonly fauxTranscript?: string;
};

/** How the worker's run ended, in the shape `main.ts` maps to exit codes. */
export type LocalWorkerOutcome =
  | { readonly status: "done"; readonly answer: string }
  | { readonly status: "unanswered"; readonly reason: string };

/** The environment a follow-up's identity is read from — the process's own. */
type RequestEnv = Record<string, string | undefined>;

/** How the input of one process is identified, durably: same input, same id. */
export function inputRequestId(message: string, env: RequestEnv = process.env): string {
  // The supervisor's relaunches carry WHICH follow-up this is in their own
  // environment variables (TICFAC_NUDGE, TICFAC_LINT_PUSHBACK,
  // TICFAC_STUCK_NUDGE — internal/exec/subprocess), which is exactly the
  // uniqueness a re-prompt needs: the same nudge text twice must be two
  // submissions (the second nudge is real new work), while the same argv
  // replayed after a crash must find the same one again.
  const variant =
    env.TICFAC_NUDGE !== undefined
      ? `nudge-${env.TICFAC_NUDGE}`
      : env.TICFAC_LINT_PUSHBACK !== undefined
        ? `pushback-${env.TICFAC_LINT_PUSHBACK}`
        : env.TICFAC_STUCK_NUDGE !== undefined
          ? `stuck-nudge-${env.TICFAC_STUCK_NUDGE}`
          : "job";
  const digest = createHash("sha256").update(message, "utf8").digest("hex").slice(0, 24);
  return `ticfac:${variant}:${digest}`;
}

/**
 * The local rung's provider: pi-ai's own `cloudflare-workers-ai`, with the
 * GLM catalog corrections the gateway provider applies merged onto the
 * catalog entries. Ambient credentials (`CLOUDFLARE_API_KEY` /
 * `CLOUDFLARE_ACCOUNT_ID`), resolved by pi-ai at every request exactly the
 * way the pi CLI resolves them — the difference from the gateway provider
 * (tick oq4) is only the address and the credential: the cloud worker
 * presents a run token to the factory, the local worker presents the
 * operator's own ambient credentials.
 */
export function localWorkersAIProvider(): Provider<"openai-completions"> {
  const base = cloudflareWorkersAIProvider();
  return {
    ...base,
    getModels: () =>
      base.getModels().map((model) => {
        const override = GATEWAY_MODEL_OVERRIDES[model.id];
        if (override === undefined) return model;
        return {
          ...model,
          ...(override.maxTokens === undefined ? {} : { maxTokens: override.maxTokens }),
          ...(override.compat === undefined
            ? {}
            : { compat: { ...model.compat, ...override.compat } }),
        };
      }),
  };
}

/**
 * The model this worker runs on, in any spelling the routing config writes
 * for it: Workers AI exactly as the gateway provider accepts it
 * (`gatewayModelRef` restores the `@cf/` namespace and refuses anything
 * else), or the tests' `faux/…`. Anything else is refused by name, at boot,
 * before a single model call — a cloud-only or CLI-only model routed to the
 * local harness is a stop here, not a conversation that settles
 * "no model" after it started.
 */
export function localModelRef(routed: string): ModelRef {
  const trimmed = routed.trim();
  const faux = /^faux(?:\/(.*))?$/.exec(trimmed);
  if (faux !== null) {
    const modelId = (faux[1] ?? "").trim() || "faux-1";
    return { provider: "faux", modelId };
  }
  return gatewayModelRef(trimmed);
}

/** One scripted faux turn, as the transcript file spells it. */
type FauxStep = {
  /** The turn's reasoning, streamed ahead of its text or tool calls (the watch tests). */
  readonly thinking?: string;
  readonly text?: string;
  readonly toolCalls?: readonly { readonly name: string; readonly args?: unknown }[];
};

/**
 * The faux transcript loader (tests only): a JSON array, one element per
 * model turn — `{ "text": "…" }` for a final answer, `{ "toolCalls": [{…}] }`
 * for a tool round. Mapped onto pi-ai's own faux helpers, so what the tests
 * script is exactly what the pinned pi-ai API consumes.
 */
export function loadFauxResponses(file: string): FauxResponseStep[] {
  const raw = JSON.parse(readFileSync(file, "utf8")) as unknown;
  if (!Array.isArray(raw)) {
    throw new Error(`the faux transcript at ${file} is not a JSON array of turns`);
  }
  return (raw as FauxStep[]).map((step, index) => {
    const at = `${file}[${index}]`;
    const calls = (step.toolCalls ?? []).map((call, callIndex) =>
      fauxToolCall(call.name, (call.args ?? {}) as never, { id: `faux-${index}-${callIndex}` }),
    );
    if (calls.length === 0 && typeof step.text !== "string") {
      throw new Error(`${at} carries neither text nor toolCalls`);
    }
    const thinking = typeof step.thinking === "string" ? [fauxThinking(step.thinking)] : [];
    return fauxAssistantMessage(
      calls.length === 0 ? [...thinking, fauxText(step.text as string)] : [...thinking, ...calls],
      calls.length === 0 ? {} : { stopReason: "toolUse" },
    );
  });
}

/** The text blocks of a message, whatever shape its content takes. */
function textOf(message: Message): string[] {
  if (typeof message.content === "string") return [message.content];
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : []));
}

/** One command line through the guarded env, at the worktree root. */
async function execLine(
  env: ReturnType<typeof createGuardedNodeExecutionEnv>,
  worktree: string,
  line: string,
  vars: Record<string, string>,
): Promise<{ exitCode: number; output: string }> {
  let output = "";
  const result = await env.exec(
    line,
    { cwd: worktree, env: vars, onOutput: (text: string) => (output += text) },
    BACKGROUND_CONTEXT,
  );
  return result.ok
    ? { exitCode: result.value.exitCode, output }
    : { exitCode: 1, output: String(result.error) };
}

/**
 * The git identity the wip checkpoints commit under, resolved the way the
 * worktree's own git resolves it (repo-local, then the host's global
 * config) — the same identity the worker's own commits carry, never one
 * invented here. A worktree with none is a configuration the run's
 * preflight should have caught; failing the boot names it.
 */
async function resolveIdentity(
  env: ReturnType<typeof createGuardedNodeExecutionEnv>,
  worktree: string,
): Promise<{ name: string; email: string }> {
  const read = async (key: string): Promise<string> => {
    const out = await execLine(env, worktree, `git config --get ${key}`, {});
    return out.exitCode === 0 ? out.output.trim() : "";
  };
  const name = await read("user.name");
  const email = await read("user.email");
  if (name === "" || email === "") {
    throw new Error(
      `the attempt worktree ${worktree} has no git identity ` +
        `(${name === "" ? "user.name" : "user.email"} unset): the wip checkpoints cannot commit, and the ` +
        "worker's own commits would be unattributable — configure the identity the run's checkout carries",
    );
  }
  return { name, email };
}

export type LocalWorkerOptions = {
  readonly config: LocalWorkerConfig;
  /**
   * The input this process submits: the job prompt on the attempt's first
   * process, or the follow-up a relaunch was sent (the supervisor's nudge,
   * report-check pushback or stuck re-prompt) — with the whole conversation
   * still in the storage either way.
   */
  readonly message: string;
  /** Where the host says what it did; default console.log (the runner log). */
  readonly log?: (line: string) => void;
};

/**
 * Runs one local worker to settlement: opens the storage, resumes what an
 * earlier process left unfinished, arms the wall, listens on the steer
 * socket, submits the input (the job prompt, or this relaunch's follow-up),
 * and waits for the conversation to settle — the submitted run AND every
 * steer the supervisor sent while it ran.
 *
 * The function resolves when everything has settled; it never resolves on a
 * harness that is still able to run more work. `main.ts` maps the outcome to
 * the process's exit code and that is the whole process: the storage, the
 * worktree and the attempt branch carry the durable state.
 */
export async function runLocalWorker(options: LocalWorkerOptions): Promise<LocalWorkerOutcome> {
  const config = options.config;
  const log = options.log ?? ((line: string) => console.log(line));
  const context = BACKGROUND_CONTEXT;
  const env = createGuardedNodeExecutionEnv({ cwd: config.worktree });

  // The models: the local rung's Workers AI, or the tests' faux.
  // The Workers AI credentials resolve the way the pi CLI resolves them: the
  // stored credential in pi's own auth.json first, the ambient environment
  // as pi-ai's own fallback (pi-auth-store.ts) — the cloud rung's gateway
  // token is the cloud host's, and this one holds no credential the host did
  // not already have.
  const models = createModels({ credentials: piAuthStore() });
  let modelRef: ModelRef;
  if (config.fauxTranscript !== undefined && config.fauxTranscript !== "") {
    const faux = fauxProvider();
    faux.setResponses(loadFauxResponses(config.fauxTranscript));
    models.setProvider(faux.provider);
    modelRef = localModelRef(config.model);
  } else {
    models.setProvider(localWorkersAIProvider());
    modelRef = localModelRef(config.model);
  }

  // The registry: the coding tools, the worker contract's hooks, and the
  // per-round workspace checkpoint. Named extensions, installed in order,
  // selected explicitly by the conversation so the order is the host's.
  const registry = createRegistry();
  const contractOptions: WorkerContractOptions = {
    reportPath: config.report,
    repoDir: config.worktree,
    branch: config.branch,
    tick: config.tick,
    ...(config.role === undefined || config.role === "" ? {} : { role: config.role }),
    ...(config.checker === "" ? {} : { checkerBinary: config.checker }),
  };
  const contract = defineExtension({
    name: "ticfac-worker-contract",
    hooks: [hook(GenerationTask, { onYield: workerOnYield(env, contractOptions) })],
  });
  registry.install(CodingTools);
  registry.install(contract);
  let checkpoints: ReturnType<typeof workspaceCheckpointExtension> | undefined;
  // The finish phase's inputs (tick nou), hoisted out of the wiring block
  // below: the shell and workspace the end-of-run retire and salvage run
  // through — undefined when the checkpoints are off, which is also when
  // there is no branch to retire anything onto.
  let finish: { readonly shell: HostShell; readonly workspace: WorkspaceGit } | undefined;
  const selected = [CodingTools, contract];
  if (config.remote !== "" && config.branch !== "") {
    const identity = await resolveIdentity(env, config.worktree);
    const shell: HostShell = {
      execLine: (line, vars) => execLine(env, config.worktree, line, vars),
    };
    const workspace: WorkspaceGit = {
      remote: config.remote,
      branch: config.branch,
      identity,
      ...(config.base === undefined || config.base === "" ? {} : { base: config.base }),
    };
    finish = { shell, workspace };
    checkpoints = workspaceCheckpointExtension({
      shell,
      workspace,
      onCheckpoint: (outcome) => {
        log(
          outcome.kind === "pushed"
            ? `wip checkpoint pushed to ${config.branch}: ${outcome.sha}`
            : outcome.kind === "empty"
              ? "wip checkpoint: the tool round changed nothing"
              : `the wip checkpoint FAILED: ${outcome.error}`,
        );
      },
    });
    registry.install(checkpoints);
    selected.push(checkpoints);
  }

  const storage = await openNodeSqliteStorage(config.storage);
  const harness = await Harness.open(
    storage,
    {
      models,
      registry,
      env: () => env,
      onReport: (error) => {
        log(`harness report: ${String(error)}`);
      },
    },
    context,
  );
  // Whatever an earlier process of this attempt left unfinished — a killed
  // harness's interrupted tool, a half-answered generation — resumes here,
  // before the new input: a relaunch continues the same run.
  harness.resume();

  const root = await harness.root(context, {
    agent: {
      model: modelRef,
      cwd: config.worktree,
      extensions: selected,
    },
  });

  // The wall, as the abort() it is on this path (tick pom): the config
  // carries an absolute deadline, so a RELAUNCHED process arms the wall the
  // attempt already runs under, not a fresh one.
  let wall: ReturnType<typeof armWallDeadline> | undefined;
  if ((config.wallDeadlineMs ?? 0) > Date.now()) {
    wall = armWallDeadline(
      { abort: (ctx) => root.abort(ctx) },
      (config.wallDeadlineMs as number) - Date.now(),
      context,
    );
  }

  // The steer socket: every request line becomes a durable steer on the
  // root conversation, and the run below does not settle while a steer's
  // own run can still run. The same socket serves `ticfac watch`'s reads
  // (tick y03): the conversation's agent events, from the one process that
  // owns the storage.
  const steerWaits: Promise<void>[] = [];
  let server: SteerServer | undefined;
  if (config.steerSock !== "") {
    const watch = () => watchEvents(harness, root.id, context);
    server = await openSteerServer(
      config.steerSock,
      async (request) => {
        log(`steer admitted${request.requestId === undefined ? "" : ` (${request.requestId})`}`);
        const submission = await root.submit(
          {
            type: "input",
            content: request.text,
            whenBusy: "steer",
            ...(request.requestId === undefined ? {} : { requestId: request.requestId }),
          },
          context,
        );
        steerWaits.push(
          submission.wait(context).then((settled) => {
            log(
              settled.status === "done"
                ? "the steer's run settled"
                : `the steer's run settled ${settled.status}`,
            );
          }),
        );
      },
      { watch },
    );
    log(`steer socket listening on ${server.path}`);
  }

  const input = {
    type: "input",
    content: options.message,
    requestId: inputRequestId(options.message),
  } as const;
  const submission = await root.submit(input, context);
  const settled = await submission.wait(context);

  // Steers that joined the run are covered by the wait above; a steer
  // admitted in the gap between the run settling and this loop — or one
  // whose run outlived it — is waited on here, until none is left.
  for (;;) {
    const pending = steerWaits.splice(0);
    if (pending.length === 0) break;
    await Promise.all(pending);
  }

  wall?.cancel();
  await server?.close();
  const view = await root.context(context);
  const last = view.messages.filter((m) => m.role === "assistant").at(-1);
  const answer = last === undefined ? "" : textOf(last).join("\n");
  await harness.close(context);

  // THE FINISH PHASE (tick nou): the half of the cloud's finish this host
  // owns, because on local there is no container to run it in. The cloud
  // host's prepareFinish retires the last round's wip snapshot (the branch
  // back on the agent's own HEAD) before the container's finish phase
  // salvages the uncommitted tree; here one process is both, in the same
  // order — retire first, so the push after the salvage is a fast-forward,
  // then the salvage, so a worker that settles without committing still
  // leaves a commit collect can count. Whatever the conversation's outcome:
  // the durable layer gets the branch and the work, never only the verdict.
  if (finish !== undefined) {
    const retired = await retireWipSnapshot(finish.shell, finish.workspace);
    if (retired.kind === "retired") {
      log(
        `the attempt branch is back on the agent's own HEAD (the last round's wip snapshot retired)`,
      );
    } else {
      log(`could not put the attempt branch back on the agent's own HEAD: ${retired.error}`);
    }
    const reportRel = relative(config.worktree, config.report);
    const salvaged = await salvageUncommittedWork(finish.shell, finish.workspace, {
      subject: `tick ${config.tick}: work in progress salvaged by the local worker host (the conversation settled ${settled.status})`,
      // The report's exclusion is only meaningful inside the worktree; one
      // placed outside it is nothing `git add -A` could stage anyway.
      ...(isAbsolute(reportRel) || reportRel.startsWith("..") ? {} : { reportPath: reportRel }),
    });
    switch (salvaged.kind) {
      case "salvaged":
        log(
          `salvaged the worker's uncommitted work into its own commit on ${config.branch}: ${salvaged.sha}`,
        );
        break;
      case "failed":
        log(`could not salvage the worker's uncommitted work: ${salvaged.error}`);
        break;
      default:
        log("the worker's uncommitted tree was empty: nothing to salvage");
    }
  }

  if (settled.status !== "done") {
    const reason =
      settled.reason === undefined
        ? "the run settled without an answer"
        : `the run settled unanswered: ${settled.reason}${settled.detail === undefined ? "" : ` (${settled.detail})`}`;
    return { status: "unanswered", reason };
  }
  return { status: "done", answer };
}
