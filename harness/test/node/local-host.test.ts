import type { ChildProcessByStdio } from "node:child_process";
import { execFileSync, spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import type { Readable } from "node:stream";
import { fileURLToPath } from "node:url";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Message } from "@earendil-works/pi-ai";
import { createModels } from "@earendil-works/pi-ai";
import { createRegistry, Harness } from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { piAuthStore } from "../../src/local/pi-auth-store.js";
import { steerOnce, type WatchFrame, watchOnce } from "../../src/local/steer-socket.js";
import {
  inputRequestId,
  type LocalWorkerConfig,
  loadFauxResponses,
  localModelRef,
  localWorkersAIProvider,
} from "../../src/local/worker-host.js";

/**
 * The local worker host's acceptance tests (epic 43y step 7, tick hpk):
 * "a local epic run's workers run on the harness; the stuck nudge is a
 * steer."
 *
 * These drive the REAL entry (`src/local/main.ts`) as a real child process,
 * the way the Go executor's supervisor launches it — plain `node` with the
 * runtime register shim, type stripping, a `worker.json` config — over a
 * real git worktree, a real origin and real local SQLite. The only stand-in
 * is the model: the faux provider, scripted by a transcript file, because a
 * test that needs a credential or a network cannot run on every host.
 *
 * Why here and not the workerd suite: the child is a separate PROCESS, so
 * what these tests exercise is the storage's cross-process resume — the
 * whole point of the local SQLite rung — and the steer socket: a live
 * conversation steered from outside the process that owns it. Nothing in
 * the workerd pool can spawn.
 *
 * The wall clock (tick 7wg): a full-worker test's runtime is load-dependent —
 * it waits on the child's own progress, a boot, a socket, a file a running
 * tool writes — and on a shared host at load average 23-47 one of these
 * starved past every quiet-host bound and failed 'waiting for the tool to
 * be running' after 1040s, twice in one night. No full-worker test borrows
 * the node config's 30s default: each declares its own 300s bound and the
 * fixture waits below it carry a 120s default — enforced by
 * harness-timeout-discipline.test.ts, the same guard kjs pointed at the
 * full-Harness tests.
 */

/** The harness package root, from this test's own place in it. */
const harnessRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

/**
 * The entry's invocation, exactly as the Go runner table spells it:
 * `--experimental-strip-types` because the pinned CI node is 22 (strip-types
 * is default only from 23.6), and the register shim because the sources
 * import each other the way `tsc` writes them.
 */
const NODE_FLAGS = [
  "--experimental-strip-types",
  "--import",
  join(harnessRoot, "runtime", "register.mjs"),
];

type RunChild = { readonly output: () => string } & ChildProcessByStdio<null, Readable, Readable>;

type Fixture = {
  root: string;
  origin: string;
  worktree: string;
  configPath: string;
  /** Writes worker.json and answers the merged config. */
  config: (over: Partial<LocalWorkerConfig>) => LocalWorkerConfig;
  transcript: (turns: unknown[]) => string;
  run: (
    over: Partial<LocalWorkerConfig>,
    env?: Record<string, string>,
    message?: string,
  ) => RunChild;
  /** Every child `run` spawned, so afterEach can reap one still alive. */
  readonly children: RunChild[];
};

/** A real repository, a real origin, a real worktree — the attempt's shape. */
function makeFixture(): Fixture {
  const root = mkdtempSync(join(tmpdir(), "ticfac-local-host-"));
  const origin = join(root, "origin.git");
  execFileSync("git", ["init", "--quiet", "--bare", "-b", "main", origin]);
  const seed = join(root, "seed");
  execFileSync("git", ["init", "--quiet", "-b", "main", seed]);
  writeFileSync(join(seed, "README.md"), "# the base\n");
  execFileSync("git", ["-C", seed, "add", "-A"]);
  execFileSync("git", [
    "-C",
    seed,
    "-c",
    "user.name=seed",
    "-c",
    "user.email=seed@example.com",
    "commit",
    "--quiet",
    "-m",
    "the base commit",
  ]);
  execFileSync("git", ["-C", seed, "remote", "add", "origin", origin]);
  execFileSync("git", ["-C", seed, "push", "--quiet", "origin", "HEAD:refs/heads/main"]);
  const worktree = join(root, "worktree");
  execFileSync("git", ["clone", "--quiet", origin, worktree]);
  execFileSync("git", ["-C", worktree, "config", "user.name", "ticfac worker"]);
  execFileSync("git", ["-C", worktree, "config", "user.email", "worker@example.com"]);

  const base: LocalWorkerConfig = {
    storage: join(root, "worker.sqlite"),
    steerSock: join(root, "steer.sock"),
    worktree,
    remote: origin,
    branch: "ticfac/run-epic-43y/tick-hpk/attempt-9",
    base: execFileSync("git", ["--git-dir", origin, "rev-parse", "main"], {
      encoding: "utf8",
    }).trim(),
    report: join(worktree, "RESULT-hpk.md"),
    // The tests' report check runs a script that always passes, so the
    // contract's onYield pushback is not what these tests measure.
    checker: join(root, "checker.sh"),
    tick: "hpk",
    role: "implement",
    model: "faux/faux-1",
  };
  writeFileSync(base.checker, "#!/bin/sh\nexit 0\n");
  execFileSync("chmod", ["+x", base.checker]);
  const configPath = join(root, "worker.json");
  const config = (over: Partial<LocalWorkerConfig>): LocalWorkerConfig => {
    const merged = { ...base, ...over };
    writeFileSync(configPath, JSON.stringify(merged));
    return merged;
  };
  let transcriptN = 0;
  /** The children this fixture spawned, for afterEach to reap. */
  const children: RunChild[] = [];
  const transcript = (turns: unknown[]): string => {
    // A distinct file per script: the faux provider is stateless per
    // process, so a two-process test (the resume below) hands each process
    // its own conversation-continuation script.
    transcriptN += 1;
    const file = join(root, `transcript-${transcriptN}.json`);
    writeFileSync(file, JSON.stringify(turns));
    return file;
  };
  const run = (
    over: Partial<LocalWorkerConfig>,
    env?: Record<string, string>,
    message = "do the job",
  ): RunChild => {
    config(over);
    const child = spawn(
      process.execPath,
      [
        ...NODE_FLAGS,
        join(harnessRoot, "src", "local", "main.ts"),
        "--config",
        configPath,
        "--message",
        message,
      ],
      // Detached, in its own process group: a failed test's child is reaped
      // by afterEach's group kill — SIGKILL to the group takes the child AND
      // the bash its env spawned (the gate a failed watcher test leaves
      // behind must not outlive the suite on a shared host).
      {
        cwd: root,
        env: { ...process.env, ...(env ?? {}) },
        detached: true,
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    let said = "";
    child.stdout.on("data", (c: Buffer) => (said += c.toString("utf8")));
    child.stderr.on("data", (c: Buffer) => (said += c.toString("utf8")));
    const run = Object.assign(child, { output: () => said }) as unknown as RunChild;
    children.push(run);
    return run;
  };
  return { root, origin, worktree, configPath, config, transcript, run, children };
}

/**
 * Waits for a live condition, polling briefly — never a blind sleep.
 *
 * The default bound is a loaded-host number (tick 7wg): what it waits on is
 * the worker CHILD's own progress, and the old 30s quiet-host default was
 * the bound a shared host at load average 23-47 starved twice in one night
 * ('timed out waiting for the tool to be running'). Room for a loaded
 * host, still a bound a genuinely stuck worker fails inside — and always
 * below the 300s per-test bound the discipline test enforces.
 */
async function waitFor(what: string, check: () => boolean, timeoutMs = 120_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!check()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

/** The settled child's exit code, waited on. */
async function exitOf(child: RunChild): Promise<number> {
  return new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("exit", (code: number | null) => resolve(code ?? -1));
  });
}

/** The messages of the attempt's conversation, read from its own storage. */
async function messagesOf(storage: string): Promise<Message[]> {
  const harness = await Harness.open(
    await openNodeSqliteStorage(storage),
    // A read-only viewer: no providers, no extensions, no scheduling —
    // `root()` only resolves the conversation the writer process left.
    { models: createModels(), registry: createRegistry() },
    BACKGROUND_CONTEXT,
  );
  const root = await harness.root(BACKGROUND_CONTEXT);
  const view = await root.context(BACKGROUND_CONTEXT);
  await harness.close(BACKGROUND_CONTEXT);
  return [...view.messages];
}

function textOf(message: Message): string {
  return typeof message.content === "string"
    ? message.content
    : message.content.flatMap((block) => (block.type === "text" ? [block.text] : [])).join(" ");
}

describe("the local worker host", () => {
  let f: Fixture;
  // The fixture itself is real git — init, clone, push — under the same
  // loaded host the tests run on, so the hooks get the same room as the
  // tests instead of the config's quiet-host 30s hookTimeout.
  beforeEach(() => {
    f = makeFixture();
  }, 120_000);
  afterEach(() => {
    // A test that failed before its child settled leaves the child (and a
    // gated bash round it is holding) still running: the fixture's root is
    // about to vanish under it, so the child is reaped first, by its own
    // process group — the same shape the workspace-checkpoints suite's
    // afterEach uses for its door's detached processes.
    for (const child of f.children) {
      if (child.exitCode === null && child.signalCode === null && child.pid !== undefined) {
        try {
          process.kill(-child.pid, "SIGKILL");
        } catch {
          /* already gone */
        }
      }
    }
    rmSync(f.root, { recursive: true, force: true });
  }, 120_000);

  it("runs a whole worker to settlement: tools in the worktree, wip on the branch, report written", {
    timeout: 300_000,
  }, async () => {
    const transcript = f.transcript([
      {
        toolCalls: [
          { name: "write", args: { path: "work.txt", content: "the harness wrote this" } },
        ],
      },
      {
        toolCalls: [
          {
            name: "write",
            args: {
              path: "RESULT-hpk.md",
              content: "# hpk\n\nthe local host ran\n\nSTATUS: DONE\n",
            },
          },
        ],
      },
      { text: "the work is done and reported" },
    ]);
    const config = f.config({ fauxTranscript: transcript });
    const child = f.run({ fauxTranscript: transcript });
    const code = await exitOf(child);
    expect(code).toBe(0);

    // The tools really ran in the worktree, behind the boundary guard.
    expect(readFileSync(join(config.worktree, "work.txt"), "utf8")).toBe("the harness wrote this");
    expect(readFileSync(config.report, "utf8")).toContain("STATUS: DONE");

    // Every tool round pushed its wip snapshot to the attempt branch — the
    // carried-work mechanism at tool-round granularity, on a real origin —
    // and then the FINISH PHASE (tick nou) retired the last one and
    // salvaged the uncommitted tree. Since tick xd3 a snapshot is a commit
    // ON TOP of the agent's own HEAD, force-pushed over the previous
    // round's (src/workspace/checkpoints.ts: "a checkpoint must be
    // invisible to the work it checkpoints"), so during the run the
    // branch carried ONE snapshot — the last round's — holding both
    // rounds' files, which the runner log names push by push.
    const pushes = child.output().match(/wip checkpoint pushed to /g) ?? [];
    expect(pushes.length).toBe(2);

    // The finish phase, in this host's own words: the branch back on the
    // agent's HEAD, the uncommitted tree its own commit. The push of that
    // commit is the supervisor's (its durability timer's final beat) —
    // the Go end-to-end test proves the whole chain — so what this
    // process leaves behind is the retired branch and the salvage commit
    // in the worktree it hands over.
    expect(child.output()).toContain(
      "the attempt branch is back on the agent's own HEAD (the last round's wip snapshot retired)",
    );
    expect(child.output()).toContain("salvaged the worker's uncommitted work into its own commit");
    const onOrigin = execFileSync(
      "git",
      ["--git-dir", f.origin, "log", "--format=%s", config.branch],
      {
        encoding: "utf8",
      },
    )
      .trim()
      .split("\n");
    expect(onOrigin).toEqual(["the base commit"]);

    // The salvage commit is the worktree's own HEAD — the commit the
    // supervisor's final push lands. It carries the WORK, never the report:
    // the report is read from its own path (or committed by its own owner
    // on the cloud), and a salvage commit carrying it would make the work
    // indistinguishable from the account of it.
    const subjects = execFileSync("git", ["-C", config.worktree, "log", "--format=%s"], {
      encoding: "utf8",
    })
      .trim()
      .split("\n");
    expect(subjects).toEqual([
      "tick hpk: work in progress salvaged by the local worker host (the conversation settled done)",
      "the base commit",
    ]);
    const files = execFileSync("git", ["-C", config.worktree, "ls-tree", "--name-only", "HEAD"], {
      encoding: "utf8",
    })
      .trim()
      .split("\n");
    expect(files).toEqual(expect.arrayContaining(["work.txt", "README.md"]));
    expect(files).not.toContain("RESULT-hpk.md");

    // The conversation in storage ends with the faux answer.
    const messages = await messagesOf(config.storage);
    expect(textOf(messages.at(-1) as Message)).toBe("the work is done and reported");
  });

  it("delivers the stuck nudge as a STEER: live socket, placed after the current tool round", {
    timeout: 300_000,
  }, async () => {
    const transcript = f.transcript([
      {
        toolCalls: [
          { name: "bash", args: { command: "sleep 2; echo stepped > step.txt" } },
          {
            name: "write",
            args: { path: "RESULT-hpk.md", content: "# hpk\n\nsteered\n\nSTATUS: DONE\n" },
          },
        ],
      },
      { text: "the steer joined the run" },
    ]);
    const config = f.config({ fauxTranscript: transcript });
    const child = f.run({ fauxTranscript: transcript });
    await waitFor("the steer socket to listen", () => existsSync(config.steerSock));

    // The supervisor's move on a stuck runner: one request line, one ack —
    // and the runner is NOT stopped, killed or relaunched for it.
    const reply = await steerOnce(
      config.steerSock,
      "You appear stuck: commit and carry on.",
      "stuck-nudge-1",
    );
    expect(reply).toEqual({ ok: true, requestId: "stuck-nudge-1" });

    const code = await exitOf(child);
    expect(code).toBe(0);

    // The bash tool ran to its end (the steer does not interrupt it)…
    expect(readFileSync(join(config.worktree, "step.txt"), "utf8")).toBe("stepped\n");
    // …and the steer landed in the conversation AFTER the tool round: a
    // user message after the bash result, before the final answer.
    const messages = await messagesOf(config.storage);
    const steerAt = messages.findIndex(
      (m) => m.role === "user" && textOf(m).includes("You appear stuck"),
    );
    const toolResultAt = messages.findIndex((m) => m.role === "toolResult");
    expect(steerAt).toBeGreaterThan(-1);
    expect(steerAt).toBeGreaterThan(toolResultAt);
    expect(textOf(messages.at(-1) as Message)).toBe("the steer joined the run");
  });

  it("serves the live conversation to a watcher, and a steer round-trips through what it watches", {
    timeout: 300_000,
  }, async () => {
    // Tick y03: `ticfac watch <run> <tick>` reads a local worker through
    // the same door the stuck nudge steers through. The watcher sees the
    // snapshot, then every commit — thinking, tool calls, live tool output,
    // tool results — and the steer it sends lands in the stream it reads.
    //
    // Tick hv3: the bash round waits on a file the TEST writes, not on a
    // wall-clock `sleep 2` window. A watcher on a loaded host starves at
    // the attach moment (this failed once on the loaded host, passing on
    // re-run): the whole ungated run was ~2.5s, and a watcher delayed past
    // the tool's window met a conversation that was already over. The run
    // now holds still for the watcher — see the delayed-attach sibling
    // below, which is that failure made a deterministic regression test.
    // The gate is capped at its own 120s (600 × 0.2s): a test that died
    // before releasing it leaves a child that still ends on its own, and
    // afterEach reaps it by process group regardless.
    // This test still states its own wall clock (300s, tick 7wg's
    // full-worker bound): its runtime is the whole child boot, the rounds
    // and the finish phase, which grows with host load — 21s measured at
    // synthetic load 200 against the quiet-host 30s default.
    const transcript = f.transcript([
      {
        thinking: "The tick wants a step file; a bash round writes it.",
        toolCalls: [
          {
            name: "bash",
            args: {
              command:
                'echo working; n=0; until [ -e proceed.txt ] || [ "$n" -ge 600 ]; do sleep 0.2; n=$((n+1)); done; echo stepped > step.txt',
            },
          },
        ],
      },
      {
        thinking: "The steer asked for a report; write it.",
        toolCalls: [
          {
            name: "write",
            args: { path: "RESULT-hpk.md", content: "# hpk\n\nwatched\n\nSTATUS: DONE\n" },
          },
        ],
      },
      { thinking: "Both rounds landed.", text: "watched and steered" },
    ]);
    const config = f.config({ fauxTranscript: transcript });
    const child = f.run({ fauxTranscript: transcript });
    await waitFor("the steer socket to listen", () => existsSync(config.steerSock));

    const frames: WatchFrame[] = [];
    const lines = (): unknown[] =>
      frames.flatMap((frame) => (frame.type === "events" ? frame.events : []));
    const watch = watchOnce(config.steerSock, (frame) => frames.push(frame));
    const typed = (type: string) =>
      lines().filter((event) => (event as { type?: string }).type === type) as Record<
        string,
        unknown
      >[];
    // A watcher that attaches mid-tool meets the running bash in the
    // snapshot's tool slots; one that attaches earlier sees its start event.
    const bashRunning = (): boolean =>
      typed("tool_execution_start").some((e) => e.toolName === "bash") ||
      typed("snapshot").some((e) =>
        (e.tools as { name: string; status: string }[]).some(
          (slot) => slot.name === "bash" && slot.status === "running",
        ),
      );
    await waitFor("the bash tool to run, seen through the watch", bashRunning);

    // The operator's steer — what `ticfac steer` sends — while the tool runs.
    const reply = await steerOnce(config.steerSock, "Write the report next.", "operator-steer-1");
    expect(reply).toEqual({ ok: true, requestId: "operator-steer-1" });

    // The steer is placed mid-tool; the gated round may finish now.
    writeFileSync(join(config.worktree, "proceed.txt"), "");

    expect(await exitOf(child)).toBe(0);
    const end = await watch.ended;
    expect(end).toEqual({ type: "end", reason: "the worker process is exiting" });

    // The first frame is the snapshot; every frame after it is one commit.
    const first = frames[0] as WatchFrame & { type: "events" };
    expect((first.events[0] as { type: string }).type).toBe("snapshot");

    // Thinking and tool calls arrived as settled assistant entries — in the
    // snapshot for what settled before the watcher attached, as message_end
    // events for everything after: together, the whole conversation.
    type Entry = { id: number; model?: Message[] };
    const seen = new Set<number>();
    const settled: Message[] = [];
    for (const entry of [
      ...typed("snapshot").flatMap((e) => e.entries as Entry[]),
      ...typed("message_end").map((e) => e.entry as Entry),
    ]) {
      const message = entry.model?.[0];
      if (seen.has(entry.id) || message === undefined || message.role === "system") continue;
      seen.add(entry.id);
      settled.push(message);
    }
    const assistant = settled.filter((m) => m.role === "assistant");
    const blocks = assistant.flatMap((m) => (Array.isArray(m.content) ? m.content : []));
    expect(blocks.some((b) => b.type === "thinking" && b.thinking.includes("step file"))).toBe(
      true,
    );
    expect(blocks.some((b) => b.type === "toolCall" && b.name === "bash")).toBe(true);
    // …the tool's live output, in the snapshot's slot and the updates after it…
    const output = [
      ...typed("snapshot").flatMap((e) => (e.tools as { output?: string }[]).map((t) => t.output)),
      ...typed("tool_execution_update").map((e) => JSON.stringify(e.output ?? {})),
    ].join("");
    expect(output).toContain("working");
    // …and the steer came back through the watch as the conversation's
    // input, AFTER the bash result: the round trip, observed from outside.
    const steerAt = settled.findIndex(
      (m) => m.role === "user" && textOf(m).includes("Write the report next."),
    );
    const bashResultAt = settled.findIndex((m) => m.role === "toolResult" && m.toolName === "bash");
    expect(steerAt).toBeGreaterThan(-1);
    expect(steerAt).toBeGreaterThan(bashResultAt);
    expect(textOf(settled.at(-1) as Message)).toBe("watched and steered");

    // The Go reader's fixture (internal/workerview/testdata) is this stream:
    // TICFAC_CAPTURE_WATCH=<file> writes it out, one frame per line.
    const capture = process.env.TICFAC_CAPTURE_WATCH;
    if (capture !== undefined && capture !== "") {
      writeFileSync(capture, `${frames.map((frame) => JSON.stringify(frame)).join("\n")}\n`);
    }
  });

  it("meets the live conversation when the watcher attaches late, whatever the host load", {
    timeout: 300_000,
  }, async () => {
    // Tick hv3: the reproduced flake, made a permanent regression test.
    // The full `pnpm test` runs failed this file's watcher test once on a
    // loaded host, passing on re-run: the whole worker child runs ~2.5s of
    // wall clock while the test process starves at the attach moment, and a
    // watcher delayed past the tool's `sleep 2` window met a conversation
    // that was already over — the socket ENOENT, the test failed on the
    // host's timing, not the tree's. The tool round below therefore waits
    // on a file THIS test writes — capped at its own 120s, so a test that
    // died before releasing it leaves a child that still ends on its own —
    // so the run holds still for the watcher: there is no window to miss,
    // and the 3s delayed attach this test makes is exactly the
    // starved-watcher case that failed — now deterministic.
    const transcript = f.transcript([
      {
        thinking: "The tick wants a step file; a bash round writes it.",
        toolCalls: [
          {
            name: "bash",
            args: {
              command:
                'echo working; n=0; until [ -e proceed.txt ] || [ "$n" -ge 600 ]; do sleep 0.2; n=$((n+1)); done; echo stepped > step.txt',
            },
          },
        ],
      },
      {
        thinking: "The steer asked for a report; write it.",
        toolCalls: [
          {
            name: "write",
            args: {
              path: "RESULT-hpk.md",
              content: "# hpk\n\nlate watcher\n\nSTATUS: DONE\n",
            },
          },
        ],
      },
      { thinking: "Both rounds landed.", text: "watched late" },
    ]);
    const config = f.config({ fauxTranscript: transcript });
    const child = f.run({ fauxTranscript: transcript });
    await waitFor("the steer socket to listen", () => existsSync(config.steerSock));
    // A loaded host's starved watcher process: 3s late at the attach, later
    // than the whole ungated run ever took. The gated tool round makes that
    // safe — the conversation is still live, still mid-tool.
    await new Promise((resolve) => setTimeout(resolve, 3000));

    const frames: WatchFrame[] = [];
    const lines = (): unknown[] =>
      frames.flatMap((frame) => (frame.type === "events" ? frame.events : []));
    const watch = watchOnce(config.steerSock, (frame) => frames.push(frame));
    const typed = (type: string) =>
      lines().filter((event) => (event as { type?: string }).type === type) as Record<
        string,
        unknown
      >[];
    const bashSeen = (): boolean =>
      typed("tool_execution_start").some((e) => e.toolName === "bash") ||
      typed("snapshot").some((e) =>
        (e.tools as { name: string; status: string }[]).some(
          (slot) => slot.name === "bash" && slot.status === "running",
        ),
      );
    await waitFor("the bash tool to run, seen through the late watch", bashSeen);

    const reply = await steerOnce(config.steerSock, "Write the report next.", "late-watcher-1");
    expect(reply).toEqual({ ok: true, requestId: "late-watcher-1" });

    // The steer is placed; the tool round may finish now.
    writeFileSync(join(config.worktree, "proceed.txt"), "");
    expect(await exitOf(child)).toBe(0);
    const end = await watch.ended;
    expect(end).toEqual({ type: "end", reason: "the worker process is exiting" });

    // What the late watcher saw: the bash round live (its start event when it
    // started after the attach, its running slot when it started before),
    // its output, and the steer landing after the bash result.
    expect(bashSeen()).toBe(true);
    const output = [
      ...typed("snapshot").flatMap((e) => (e.tools as { output?: string }[]).map((t) => t.output)),
      ...typed("tool_execution_update").map((e) => JSON.stringify(e.output ?? {})),
    ].join("");
    expect(output).toContain("working");
    type Entry = { id: number; model?: Message[] };
    const seen = new Set<number>();
    const settled: Message[] = [];
    for (const entry of [
      ...typed("snapshot").flatMap((e) => e.entries as Entry[]),
      ...typed("message_end").map((e) => e.entry as Entry),
    ]) {
      const message = entry.model?.[0];
      if (seen.has(entry.id) || message === undefined || message.role === "system") continue;
      seen.add(entry.id);
      settled.push(message);
    }
    const steerAt = settled.findIndex(
      (m) => m.role === "user" && textOf(m).includes("Write the report next."),
    );
    const bashResultAt = settled.findIndex((m) => m.role === "toolResult" && m.toolName === "bash");
    expect(steerAt).toBeGreaterThan(-1);
    expect(steerAt).toBeGreaterThan(bashResultAt);
    expect(textOf(settled.at(-1) as Message)).toBe("watched late");
  });

  it("refuses a request that is neither a steer nor a watch, by name", {
    timeout: 300_000,
  }, async () => {
    const transcript = f.transcript([
      {
        toolCalls: [
          { name: "bash", args: { command: "sleep 1" } },
          {
            name: "write",
            args: { path: "RESULT-hpk.md", content: "# hpk\n\nrefused\n\nSTATUS: DONE\n" },
          },
        ],
      },
      { text: "done" },
    ]);
    const config = f.config({ fauxTranscript: transcript });
    const child = f.run({ fauxTranscript: transcript });
    await waitFor("the steer socket to listen", () => existsSync(config.steerSock));
    const { createConnection } = await import("node:net");
    const answer = await new Promise<string>((resolve, reject) => {
      const socket = createConnection(config.steerSock);
      let said = "";
      socket.on("connect", () => socket.write(`${JSON.stringify({ type: "dance" })}\n`));
      socket.on("data", (c: Buffer) => (said += c.toString("utf8")));
      socket.on("close", () => resolve(said));
      socket.on("error", reject);
    });
    expect(JSON.parse(answer)).toEqual({
      ok: false,
      error: 'a request is a steer ({"text":…}) or {"type":"watch"}',
    });
    expect(await exitOf(child)).toBe(0);
  });

  it("resumes from the storage after the harness is killed mid-tool, without re-running the tool", {
    timeout: 300_000,
  }, async () => {
    // The transcript file is the FAUX provider's script, and the faux
    // provider is stateless per process — so the second process's script
    // starts where the conversation picks up again: its first request is
    // the recovery continuation, handed the interrupted tool's result.
    const firstTranscript = f.transcript([
      { toolCalls: [{ name: "bash", args: { command: "echo started >> marker.txt; sleep 30" } }] },
    ]);
    const config = f.config({ fauxTranscript: firstTranscript });
    const first = f.run({ fauxTranscript: firstTranscript });
    await waitFor("the tool to be running", () => existsSync(join(config.worktree, "marker.txt")));
    first.kill("SIGKILL");
    await exitOf(first);

    // A relaunched process opens the SAME storage and resumes the run the
    // killed one left unfinished: the bash tool is NOT re-run (the marker
    // says "started" exactly once), the model is handed the interrupted
    // result, and the conversation finishes.
    const secondTranscript = f.transcript([
      {
        toolCalls: [
          {
            name: "write",
            args: { path: "RESULT-hpk.md", content: "# hpk\n\nresumed\n\nSTATUS: DONE\n" },
          },
        ],
      },
      { text: "recovered after the kill" },
    ]);
    const second = f.run({ fauxTranscript: secondTranscript });
    const code = await exitOf(second);
    expect(code).toBe(0);
    expect(readFileSync(join(config.worktree, "marker.txt"), "utf8")).toBe("started\n");
    expect(readFileSync(config.report, "utf8")).toContain("STATUS: DONE");

    const messages = await messagesOf(config.storage);
    const interrupted = messages.find(
      (m) => m.role === "toolResult" && textOf(m).toLowerCase().includes("interrupt"),
    );
    expect(interrupted).toBeDefined();
    expect(textOf(messages.at(-1) as Message)).toBe("recovered after the kill");
  });

  it("continues the SAME conversation when relaunched with a follow-up message", {
    timeout: 300_000,
  }, async () => {
    // The first process finishes its prompt without a report. The
    // contract's onYield nudges it twice IN the conversation (each nudge
    // consumes a scripted response); with the bound spent the yield stands,
    // the run settles done, and the process exits 0 with no report — the
    // supervisor's own nudge's trigger.
    const firstTranscript = f.transcript([
      { toolCalls: [{ name: "write", args: { path: "work.txt", content: "first pass" } }] },
      { text: "ended without a report" },
      { text: "nudged once, still nothing" },
      { text: "nudged twice, still nothing" },
    ]);
    const config = f.config({ fauxTranscript: firstTranscript });
    const first = f.run({ fauxTranscript: firstTranscript });
    expect(await exitOf(first)).toBe(0);

    // The supervisor's nudge relaunches the SAME runner with the nudge text
    // as its message (TICFAC_NUDGE tells the host which follow-up it is) —
    // the pi-CLI path's `--session-id`, durable: the whole first exchange
    // is still in the conversation the second process continues.
    const nudge =
      "You ended your turn without writing your report. Finish the work you were doing, " +
      `then write your report to ${config.report}, ending with its STATUS line.`;
    const secondTranscript = f.transcript([
      {
        toolCalls: [
          {
            name: "write",
            args: { path: "RESULT-hpk.md", content: "# hpk\n\nafter the nudge\n\nSTATUS: DONE\n" },
          },
        ],
      },
      { text: "finished after the nudge" },
    ]);
    const second = f.run({ fauxTranscript: secondTranscript }, { TICFAC_NUDGE: "1" }, nudge);
    expect(await exitOf(second)).toBe(0);

    const messages = await messagesOf(config.storage);
    const firstAnswerAt = messages.findIndex((m) => textOf(m) === "ended without a report");
    const nudgeAt = messages.findIndex(
      (m) =>
        m.role === "user" && textOf(m).includes("You ended your turn without writing your report"),
    );
    expect(firstAnswerAt).toBeGreaterThan(-1);
    expect(nudgeAt).toBeGreaterThan(firstAnswerAt);
    expect(textOf(messages.at(-1) as Message)).toBe("finished after the nudge");
  });
});

describe("the local host's credential resolution", () => {
  it("resolves the stored pi credential the way the pi CLI does: stored key first, env fallback", async () => {
    const dir = mkdtempSync(join(tmpdir(), "ticfac-pi-auth-"));
    try {
      const file = join(dir, "auth.json");
      writeFileSync(
        file,
        JSON.stringify({
          "cloudflare-workers-ai": {
            type: "api_key",
            key: "stored-key",
            env: { CLOUDFLARE_ACCOUNT_ID: "stored-account" },
          },
        }),
      );
      const models = createModels({ credentials: piAuthStore(file) });
      models.setProvider(localWorkersAIProvider());
      const auth = await models.getAuth("cloudflare-workers-ai");
      if (auth === undefined) {
        throw new Error("the stored credential did not configure the provider");
      }
      expect(auth.source).toBe("stored credential");
      expect((auth.auth as { apiKey?: string }).apiKey).toBe("stored-key");

      // A store that holds nothing leaves the ambient environment to answer,
      // and an unconfigured provider stays unconfigured rather than guessed.
      const empty = join(dir, "empty-auth.json");
      const models2 = createModels({ credentials: piAuthStore(empty) });
      models2.setProvider(localWorkersAIProvider());
      expect(await models2.getAuth("cloudflare-workers-ai")).toBeUndefined();
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it("lists credential metadata only, and a write goes back to the same file", async () => {
    const dir = mkdtempSync(join(tmpdir(), "ticfac-pi-auth-"));
    try {
      const file = join(dir, "auth.json");
      const store = piAuthStore(file);
      const written = await store.modify("cloudflare-workers-ai", async () => ({
        type: "api_key" as const,
        key: "first-key",
      }));
      expect(written?.type).toBe("api_key");
      expect(JSON.parse(readFileSync(file, "utf8"))["cloudflare-workers-ai"].key).toBe("first-key");
      const listed = await store.list();
      expect(listed).toEqual([{ providerId: "cloudflare-workers-ai", type: "api_key" }]);
      await store.delete("cloudflare-workers-ai");
      expect(await store.read("cloudflare-workers-ai")).toBeUndefined();
      expect(JSON.parse(readFileSync(file, "utf8"))).toEqual({});
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

describe("the local host's model and request identities", () => {
  it("accepts the routed Workers AI spellings and restores the @cf namespace", () => {
    expect(localModelRef("cloudflare-workers-ai/@cf/zai-org/glm-5.3")).toEqual({
      provider: "cloudflare-workers-ai",
      modelId: "@cf/zai-org/glm-5.3",
    });
    expect(localModelRef("workers-ai/@cf/zai-org/glm-5.3")).toEqual({
      provider: "cloudflare-workers-ai",
      modelId: "@cf/zai-org/glm-5.3",
    });
  });

  it("refuses a model the local rung cannot run — claude is the frontier CLI's, not this harness's", () => {
    expect(() => localModelRef("anthropic/claude-opus-4")).toThrow(/is not a Workers AI model/);
  });

  it("accepts the tests' faux rung", () => {
    expect(localModelRef("faux/faux-1")).toEqual({ provider: "faux", modelId: "faux-1" });
    expect(localModelRef("faux")).toEqual({ provider: "faux", modelId: "faux-1" });
  });

  it("names a follow-up by WHICH one it is, so the same text twice is two submissions", () => {
    const text = "You ended your turn without writing your report.";
    expect(inputRequestId(text)).not.toBe(inputRequestId(text, { TICFAC_NUDGE: "1" }));
    expect(inputRequestId(text, { TICFAC_NUDGE: "1" })).toBe(
      inputRequestId(text, { TICFAC_NUDGE: "1" }),
    );
  });

  it("refuses a faux transcript that is not a JSON array of turns", () => {
    const root = mkdtempSync(join(tmpdir(), "ticfac-faux-"));
    try {
      const file = join(root, "transcript.json");
      writeFileSync(file, JSON.stringify({ text: "not an array" }));
      expect(() => loadFauxResponses(file)).toThrow(/is not a JSON array/);
      writeFileSync(file, JSON.stringify([{}]));
      expect(() => loadFauxResponses(file)).toThrow(/carries neither text nor toolCalls/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
