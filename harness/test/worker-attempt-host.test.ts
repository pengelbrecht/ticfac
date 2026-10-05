import {
  createModels,
  type FauxResponseFactory,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Message,
} from "@earendil-works/pi-ai";
import { MemoryStorage } from "@earendil-works/pi-durable";
import { describe, expect, it } from "vitest";
import contract from "../../contracts/worker-boot-contract.json";
import { GATEWAY_PROVIDER_ID } from "../src/gateway/workers-ai.js";
import {
  HARNESS_STATUS_WALL,
  parseBootHandoff,
  WORKER_BOOT_PROTOCOL,
  WorkerAttemptHost,
  type WorkerAttemptRecord,
  type WorkerAttemptRecordStore,
  type WorkerAttemptSpec,
} from "../src/host/worker-attempt.js";
import { fakeSandboxDoor, waitFor } from "./sandbox-door-helper.js";

/**
 * The worker attempt host (epic 43y step 6, tick xd3): one attempt driven end
 * to end — the container's boot phase, the conversation, the finish phase —
 * on the REAL pi-durable Harness over a scripted FactorySandbox door and a
 * faux model registered under the gateway provider's own id (so the host's
 * Workers-AI-only model check runs as in production).
 *
 * What stands in: the door (./sandbox-door-helper.ts — it answers with the
 * FactorySandbox's shapes; cloudflare/test/factory-sandbox.test.ts holds the
 * object's own behaviour) and the model. What is real: the host, its record,
 * the Harness, the tools, the env, the hooks, the storage.
 */

const MODEL = "workers-ai/@cf/zai-org/glm-5.3";
const PROMPT = "# implement-tick\n\nImplement tick abc.\nEnd your report with a STATUS line.";
const BRANCH = "tick/43y-a1/abc";

/** What `ticks-worker --boot` prints on success, per the pinned contract. */
function bootOutput(prompt = PROMPT): string {
  return [
    "ticks-worker: worker run_1: tick abc of epic 43y at deadbeef (harness pi)",
    "ticks-worker: repository setup took 3s",
    `ticks-worker: ${contract.boot_marker} branch=${BRANCH} result=RESULT-abc.md`,
    contract.boot_prompt_begin,
    prompt,
    contract.boot_prompt_end,
    "",
  ].join("\n");
}

function spec(overrides: Partial<WorkerAttemptSpec> = {}): WorkerAttemptSpec {
  return {
    name: "run_1-abc-1",
    tick: "abc",
    role: "implement-tick",
    env: {
      TICKS_TICK: "abc",
      TICKS_RUN_ID: "run_1",
      TICKS_WORKDIR: "/work/repo",
      GITHUB_TOKEN: "ghs_example",
      AI_GATEWAY_TOKEN: "run-token",
    },
    model: MODEL,
    repoUrl: "https://example.com/repo.git",
    baseSha: "a".repeat(40),
    ...overrides,
  };
}

/** The host's record in memory — a DO's storage in production. */
function memoryRecords(): WorkerAttemptRecordStore & { saves: WorkerAttemptRecord[] } {
  let current: WorkerAttemptRecord | undefined;
  const saves: WorkerAttemptRecord[] = [];
  return {
    saves,
    load: async () => current,
    save: async (record) => {
      current = record;
      saves.push(record);
    },
  };
}

function textOf(message: Message): string {
  if (typeof message.content === "string") return message.content;
  return message.content.map((block) => (block.type === "text" ? block.text : "")).join("");
}

/** A faux model under the gateway provider's id, so `gatewayModelRef` resolves to it. */
function gatewayFaux(responses: FauxResponseFactory[]) {
  const faux = fauxProvider({
    provider: GATEWAY_PROVIDER_ID,
    models: [{ id: "@cf/zai-org/glm-5.3" }],
  });
  faux.setResponses(responses);
  const models = createModels();
  models.setProvider(faux.provider);
  return { faux, models };
}

/** A door whose boot prints the handoff and whose finish exits `finishExit`. */
function scriptedDoor(
  options: {
    boot?: { output?: string; exit?: number | null; ms?: number };
    finishExit?: number;
    bashMs?: number;
  } = {},
) {
  return fakeSandboxDoor({
    // Every short command succeeds: the report exists, the checker passes,
    // the git lines answer, the ready marker is there.
    runOutput: (command) => (command.includes("git rev-parse HEAD") ? "cafef00d\n" : ""),
    processScript: (command) => {
      if (command === WORKER_BOOT_PROTOCOL.bootCommand) {
        return { output: bootOutput(), exit: 0, ms: 20, ...options.boot };
      }
      if (command.startsWith(WORKER_BOOT_PROTOCOL.finishCommand)) {
        return { output: "ticks-worker: pushed\n", exit: options.finishExit ?? 0, ms: 20 };
      }
      return { output: "bash ran\n", exit: 0, ms: options.bashMs ?? 20 };
    },
  });
}

describe("the boot handoff", () => {
  it("is the contract's own markers", () => {
    expect(WORKER_BOOT_PROTOCOL).toEqual({
      bootCommand: contract.boot_command,
      finishCommand: contract.finish_command,
      setupCommand: contract.setup_command,
      bootMarker: contract.boot_marker,
      promptBegin: contract.boot_prompt_begin,
      promptEnd: contract.boot_prompt_end,
    });
  });

  it("reads the branch, the report and the prompt whole", () => {
    expect(parseBootHandoff(bootOutput())).toEqual({
      branch: BRANCH,
      result: "RESULT-abc.md",
      prompt: PROMPT,
    });
  });

  it("is absent when any part of it is", () => {
    expect(parseBootHandoff("ticks-worker: cloned\n")).toBeNull();
    const noEnd = bootOutput().replace(contract.boot_prompt_end, "");
    expect(parseBootHandoff(noEnd)).toBeNull();
    expect(parseBootHandoff(bootOutput("   "))).toBeNull();
  });
});

describe("a worker attempt driven by the host", () => {
  // Tick fim: every test here drives a whole attempt — boot, conversation,
  // finish — on a whole Harness opened INSIDE the host (src/host/
  // worker-attempt.ts), so no `Harness.open` ever appears in this file and
  // the old guard (in-body opens, test/node only) could not see them. These
  // are full-Harness tests of the workerd pool: the 120s bound, not the
  // 30s quiet-host default.
  it("boots, converses on the boot's prompt, finishes and settles with the finish phase's exit code", {
    timeout: 120_000,
  }, async () => {
    const door = scriptedDoor({ finishExit: 10 });
    const { faux, models } = gatewayFaux([
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("done; the report is written"),
    ]);
    const records = memoryRecords();
    const log: string[] = [];
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records,
      log: (text) => {
        log.push(text);
      },
      pollMs: 5,
      bashPollMs: 5,
      guardDir: null,
    });

    const started = await host.start(spec());
    expect(started.fresh).toBe(true);
    const settled = await host.drive();

    expect(settled.phase).toBe("settled");
    expect(settled.settled).toMatchObject({ exitCode: 10, phase: "finishing" });
    expect(settled.boot).toMatchObject({ branch: BRANCH, result: "RESULT-abc.md", prompt: PROMPT });
    expect(settled.harnessStatus).toBe(0);

    // The container ran exactly: the boot, the model's one bash, the finish
    // with the conversation's status — in that order, with the boot env.
    const commands = door.starts.map((s) => s.command);
    expect(commands[0]).toBe(WORKER_BOOT_PROTOCOL.bootCommand);
    expect(commands.at(-1)).toBe(`${WORKER_BOOT_PROTOCOL.finishCommand} 0`);
    expect(commands.filter((c) => c.includes("make test")).length).toBe(1);
    expect(door.starts[0]?.env.TICKS_TICK).toBe("abc");
    expect(door.starts.at(-1)?.env.TICKS_TICK).toBe("abc");
    // The model's own tool command never sees the boot env's credentials.
    const bash = door.starts.find((s) => s.command.includes("make test"));
    expect(bash?.env.AI_GATEWAY_TOKEN).toBeUndefined();
    expect(bash?.env.GITHUB_TOKEN).toBeUndefined();

    // The model was asked the boot's prompt, verbatim.
    expect(faux.state.callCount).toBe(2);
    // The finish phase found the boot's branch record written for it.
    expect(door.runs.some((r) => r.env.B === BRANCH && r.command.includes("/branch"))).toBe(true);
    // The wip checkpoint ran after the tool round, as a snapshot; and before
    // the finish the attempt branch went back to the agent's own HEAD.
    expect(door.runs.some((r) => r.command.includes("git commit-tree"))).toBe(true);
    expect(door.runs.some((r) => r.command.includes('"HEAD:refs/heads/$BRANCH"'))).toBe(true);
    expect(log.join("")).toContain("ticks-worker: pushed");
    expect(log.join("")).toContain("tool bash");
  });

  it("settles with the boot's own exit code when the boot stops, and never converses", {
    timeout: 120_000,
  }, async () => {
    const door = scriptedDoor({ boot: { output: "ticks-worker: no model route\n", exit: 5 } });
    const { faux, models } = gatewayFaux([]);
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: () => {},
      pollMs: 5,
      guardDir: null,
    });
    await host.start(spec());
    const settled = await host.drive();
    expect(settled.settled).toMatchObject({ exitCode: 5, phase: "booting" });
    expect(faux.state.callCount).toBe(0);
    expect(door.starts.map((s) => s.command)).toEqual([WORKER_BOOT_PROTOCOL.bootCommand]);
  });

  it("refuses a boot that exits 0 without its handoff", {
    timeout: 120_000,
  }, async () => {
    const door = scriptedDoor({ boot: { output: "ticks-worker: booted?\n", exit: 0 } });
    const { models } = gatewayFaux([]);
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: () => {},
      pollMs: 5,
      guardDir: null,
    });
    await host.start(spec());
    const settled = await host.drive();
    expect(settled.settled?.exitCode).toBe(1);
    expect(settled.settled?.detail).toContain("without its handoff");
  });

  it("boots again on a container lost under the boot phase", {
    timeout: 120_000,
  }, async () => {
    let boots = 0;
    const door = fakeSandboxDoor({
      processScript: (command) => {
        if (command === WORKER_BOOT_PROTOCOL.bootCommand) {
          boots += 1;
          return boots === 1
            ? { output: "ticks-worker: cloning\n", exit: null, ms: 10 }
            : { output: bootOutput(), exit: 0, ms: 10 };
        }
        return { output: "", exit: 0, ms: 10 };
      },
    });
    const { models } = gatewayFaux([() => fauxAssistantMessage("nothing to do")]);
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: () => {},
      pollMs: 5,
      guardDir: null,
    });
    await host.start(spec());
    const settled = await host.drive();
    expect(boots).toBe(2);
    expect(settled.boot?.tries).toBe(2);
    expect(settled.settled).toMatchObject({ exitCode: 0, phase: "finishing" });
  });

  it("refuses a model that is not Workers AI before it records anything", {
    timeout: 120_000,
  }, async () => {
    const records = memoryRecords();
    const host = new WorkerAttemptHost({
      door: scriptedDoor().sandbox,
      storage: async () => new MemoryStorage(),
      models: createModels(),
      records,
      log: () => {},
    });
    await expect(host.start(spec({ model: "anthropic/claude-sonnet" }))).rejects.toThrow(
      /not a Workers AI model/,
    );
    expect(records.saves.length).toBe(0);
  });

  it("starts an attempt once: a second start answers the record already there", {
    timeout: 120_000,
  }, async () => {
    const records = memoryRecords();
    const host = new WorkerAttemptHost({
      door: scriptedDoor().sandbox,
      storage: async () => new MemoryStorage(),
      models: gatewayFaux([]).models,
      records,
      log: () => {},
    });
    const first = await host.start(spec());
    const second = await host.start(spec({ tick: "other" }));
    expect(first.fresh).toBe(true);
    expect(second.fresh).toBe(false);
    expect(second.record.spec.tick).toBe("abc");
  });

  it("resumes in a new host life: the prompt is submitted once, the tool runs once", {
    timeout: 120_000,
  }, async () => {
    // The model's bash is slow enough for the first life to die under it.
    const door = scriptedDoor({ bashMs: 400 });
    const users: number[] = [];
    const { faux, models } = gatewayFaux([
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      (context) => {
        users.push(context.messages.filter((m) => m.role === "user").length);
        return fauxAssistantMessage("finished after the restart");
      },
    ]);
    const records = memoryRecords();
    const storage = new MemoryStorage();
    const lines: string[] = [];
    const life = () =>
      new WorkerAttemptHost({
        door: door.sandbox,
        storage: async () => storage,
        models,
        records,
        log: async (text) => {
          lines.push(text);
        },
        pollMs: 5,
        bashPollMs: 5,
        guardDir: null,
      });

    const first = life();
    await first.start(spec());
    void first.drive();
    await waitFor("the model's bash to start", () =>
      door.starts.some((s) => s.command.includes("make test")),
    );
    // The first life dies mid-tool: its door calls never answer again.
    door.die();
    await waitFor("the dead life to park on a dead call", () => door.deadCalls >= 1);
    door.thaw();

    const second = life();
    const settled = await second.drive();
    expect(settled.settled).toMatchObject({ exitCode: 0, phase: "finishing" });
    expect(door.starts.filter((s) => s.command === WORKER_BOOT_PROTOCOL.bootCommand).length).toBe(
      1,
    );
    expect(door.starts.filter((s) => s.command.includes("make test")).length).toBe(1);
    expect(faux.state.callCount).toBe(2);
    // One user message: the prompt, submitted once across both lives.
    expect(users).toEqual([1]);
    // The resume says so in the attempt's log — the one line a watcher (and
    // the staging fault proof, tick jhp) can tell a new life from: the first
    // life recorded the submission, the second found it there.
    expect(
      lines.filter((l) => l.includes("resumed the conversation from its storage")),
    ).toHaveLength(1);
  });

<<<<<<< HEAD
  // Tick dbi: the first life dies under the model's bash and the CONTAINER
  // dies with it — the replacement boots EMPTY and knows none of its
  // predecessor's processes, so the resumed life's replay finds its nonce
  // nowhere and restores before it re-starts the command. Until the env's
  // onRestore ear, that restore reached no log anywhere: the operator
  // watching the run saw only a mysteriously slow tool round.
  it("says which sha the nonce path rebuilt a fresh container's workspace from", async () => {
    let freshBox = false;
    const door = fakeSandboxDoor({
      runExit: (command) => {
        if (freshBox && command.includes('test -e "$CWD/.git"')) {
          freshBox = false;
          return 1;
        }
        return undefined;
      },
      // The restore's read-back answers the attempt branch's tip; the wip
      // snapshot's rev-parse answers the round's sha.
      runOutput: (command) =>
        command.includes("git rev-parse HEAD && git log -1 --format=%s")
          ? "cafef00d\nwip: tool round\n"
          : command.includes("git rev-parse HEAD")
            ? "cafef00d\n"
            : "",
      processScript: (command) => {
        if (command === WORKER_BOOT_PROTOCOL.bootCommand) {
          return { output: bootOutput(), exit: 0, ms: 20 };
        }
        if (command.startsWith(WORKER_BOOT_PROTOCOL.finishCommand)) {
          return { output: "ticks-worker: pushed\n", exit: 0, ms: 20 };
        }
        return { output: "bash ran\n", exit: 0, ms: 400 };
      },
    });
    const { models } = gatewayFaux([
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("finished on the rebuilt workspace"),
    ]);
    const records = memoryRecords();
    const storage = new MemoryStorage();
    const lines: string[] = [];
    const life = () =>
      new WorkerAttemptHost({
        door: door.sandbox,
        storage: async () => storage,
        models,
        records,
        log: async (text) => {
          lines.push(text);
        },
        pollMs: 5,
        bashPollMs: 5,
        guardDir: null,
      });

    const first = life();
    await first.start(spec());
    void first.drive();
    await waitFor("the model's bash to start", () =>
      door.starts.some((s) => s.command.includes("make test")),
    );
    door.die();
    await waitFor("the dead life to park on a dead call", () => door.deadCalls >= 1);
    door.thaw();
    // The container came back EMPTY: its process list starts over, so the
    // resumed bash's nonce is known by no process anywhere.
    door.forget();
    freshBox = true;

    const second = life();
    const settled = await second.drive();
    expect(settled.settled).toMatchObject({ exitCode: 0, phase: "finishing" });

    // The resumed bash re-STARTED (no process to reattach to) on the
    // workspace the nonce path rebuilt — once more, not twice.
    expect(door.starts.filter((s) => s.command.includes("make test")).length).toBe(2);
    // And the log names the sha it rebuilt from: the operator watching the
    // run hears the restore, not a mysteriously slow round.
    const nonce = lines.filter((l) => l.includes("a tracked bash found a fresh container"));
    expect(nonce).toHaveLength(1);
    expect(nonce[0]).toContain("restored to cafef00d");
    // Not twice, and not as a between-rounds loss: this loss sat mid-tool,
    // where only the nonce path sees it.
    expect(lines.some((l) => l.includes("the container was lost between rounds"))).toBe(false);
  });

  it("places an operator's steer after the running tool round", async () => {
=======
  it("places an operator's steer after the running tool round", {
    timeout: 120_000,
  }, async () => {
>>>>>>> cdc4f5d0a7aaf9d56e34b128c226979a5301fca7
    const door = scriptedDoor({ bashMs: 200 });
    const seen: string[][] = [];
    const { models } = gatewayFaux([
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
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: () => {},
      pollMs: 5,
      bashPollMs: 5,
      guardDir: null,
    });
    await host.start(spec());
    await expect(host.steer("too early")).rejects.toThrow(/not conversing/);
    const done = host.drive();
    await waitFor("the model's bash to start", () =>
      door.starts.some((s) => s.command.includes("make test")),
    );
    await host.steer("also add a docstring");
    const settled = await done;
    expect(settled.settled?.exitCode).toBe(0);
    expect(seen[0]).toEqual([PROMPT, "also add a docstring"]);
  });

  it("restores a lost workspace with the repository's setup: the contract's setup entry rides the restore", {
    timeout: 120_000,
  }, async () => {
    // The ready check fails ONCE: the box a between-rounds loss booted is
    // empty, and the pre-round check (tick 4fs) is what sees it.
    let lostOnce = false;
    const door = fakeSandboxDoor({
      runExit: (command) => {
        if (!lostOnce && command.includes('test -e "$CWD/.git"')) {
          lostOnce = true;
          return 1;
        }
        return undefined;
      },
      // The restore's checkout answers the tip it restored to.
      runOutput: (command) =>
        command.includes("git checkout -q -B") ? "cafef00d\nwip: tool round\n" : "",
      processScript: (command) => {
        if (command === WORKER_BOOT_PROTOCOL.bootCommand) {
          return { output: bootOutput(), exit: 0, ms: 20 };
        }
        if (command.startsWith(WORKER_BOOT_PROTOCOL.finishCommand)) {
          return { output: "ticks-worker: pushed\n", exit: 0, ms: 20 };
        }
        return { output: "bash ran\n", exit: 0, ms: 20 };
      },
    });
    const log: string[] = [];
    const { models } = gatewayFaux([
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("done; the report is written"),
    ]);
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: (text) => {
        log.push(text);
      },
      pollMs: 5,
      bashPollMs: 5,
      guardDir: null,
    });
    await host.start(spec({ env: { ...spec().env, TICKS_WORKER_SETUP: "always" } }));
    const settled = await host.drive();
    expect(settled.settled).toMatchObject({ exitCode: 0, phase: "finishing" });

    // The restore ran the contract's setup entry (tick i3h): a container lost
    // mid-turn comes back with its dependency installs, not just its tree.
    // It rides the PROCESS doors, never the run door (tick cni): a real
    // install is minutes of chatty output, and the run door's bounding
    // `head -c` SIGPIPEs a writer past its bound.
    const setup = door.starts.find((r) => r.command.includes(WORKER_BOOT_PROTOCOL.setupCommand));
    expect(setup).toBeDefined();
    expect(setup?.command).toContain('cd "$TICFAC_WORKSPACE"');
    expect(setup?.env.TICFAC_WORKSPACE).toBe("/work/repo");
    expect(door.runs.some((r) => r.command.includes(WORKER_BOOT_PROTOCOL.setupCommand))).toBe(
      false,
    );
    // The wave's setup lever rides the restore, and none of the model's
    // credentials do.
    expect(setup?.env.TICKS_WORKER_SETUP).toBe("always");
    expect(setup?.env.AI_GATEWAY_TOKEN).toBeUndefined();
    expect(log.join("")).toContain(
      "the container was lost between rounds; workspace restored to cafef00d",
    );
  });

  it("aborts the conversation at the wall and finishes with the timeout status", {
    timeout: 120_000,
  }, async () => {
    const door = scriptedDoor({ bashMs: 300 });
    const { models } = gatewayFaux([
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "make test" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("never asked"),
    ]);
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records: memoryRecords(),
      log: () => {},
      pollMs: 5,
      bashPollMs: 5,
      guardDir: null,
    });
    await host.start(spec({ wallMs: 150 }));
    const settled = await host.drive();
    expect(settled.harnessStatus).toBe(HARNESS_STATUS_WALL);
    expect(door.starts.at(-1)?.command).toBe(
      `${WORKER_BOOT_PROTOCOL.finishCommand} ${HARNESS_STATUS_WALL}`,
    );
  });

  it("is reclaimed where it stands: settled, no finish phase, and a later drive changes nothing", {
    timeout: 120_000,
  }, async () => {
    const door = scriptedDoor({ boot: { output: bootOutput(), exit: 0, ms: 5_000 } });
    const { models } = gatewayFaux([]);
    const records = memoryRecords();
    const host = new WorkerAttemptHost({
      door: door.sandbox,
      storage: async () => new MemoryStorage(),
      models,
      records,
      log: () => {},
      pollMs: 5,
      guardDir: null,
    });
    await host.start(spec());
    const driving = host.drive();
    await waitFor("the boot to start", () => door.starts.length === 1);
    const reclaimed = await host.reclaim("the run is over");
    expect(reclaimed?.settled).toMatchObject({ exitCode: null, phase: "reclaimed" });
    const after = await driving;
    expect(after.phase).toBe("settled");
    expect(after.settled?.phase).toBe("reclaimed");
    expect(door.starts.length).toBe(1);
  });
});
