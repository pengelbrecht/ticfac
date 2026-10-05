import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  createModels,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Message,
} from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { createReadTool, createWriteTool } from "@earendil-works/pi-durable/tools";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { BASH_NONCE_VAR, FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import { createTrackedBashTool } from "../../src/tools/tracked-bash.js";
import {
  pushWipCheckpoint,
  type RestoreOutcome,
  retireWipSnapshot,
  salvageUncommittedWork,
  WIP_COMMIT_SUBJECT,
  type WipOutcome,
  type WorkspaceGit,
  workspaceCheckpointExtension,
} from "../../src/workspace/checkpoints.js";
import { localSandboxDoor } from "./local-sandbox-door.js";

/**
 * The tick's ACCEPTANCE TEST (dwn), over real git and real processes: destroy
 * the container mid-turn; the next turn sees both edits.
 *
 * The workerd suite (test/workspace-checkpoints.test.ts) proves the door's
 * RPC shapes and the hook wiring on the real pi-durable Harness; this is the
 * shell half — the wip commit really lands on the attempt branch on a real
 * origin, and the restore really clears, fetches, checks out and re-runs
 * setup, so the model's next reads see the edits the last wip carried. The
 * full Harness runs here too (MemoryStorage is runtime-neutral): the turn
 * that loses its container continues on the restored workspace.
 */

/** The text blocks of a message, whatever shape its content takes. */
function textOf(message: Message): string[] {
  if (typeof message.content === "string") return [message.content];
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : []));
}

describe("workspace checkpoints over real git", () => {
  let root: string;
  let origin: string;
  let checkout: string;
  let door: ReturnType<typeof localSandboxDoor>;
  let env: FactorySandboxEnv;
  let git: WorkspaceGit;
  let outcomes: WipOutcome[];

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ticfac-wip-"));
    // The origin the attempt pushes its wip commits to: a bare repository
    // standing in for the forge's, with the base commit the boot clones at.
    origin = join(root, "origin.git");
    execFileSync("git", ["init", "-q", "--bare", "-b", "main", origin]);
    const seed = join(root, "seed");
    execFileSync("git", ["init", "-q", "-b", "main", seed]);
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
      "-q",
      "-m",
      "the base commit",
    ]);
    execFileSync("git", ["-C", seed, "remote", "add", "origin", origin]);
    execFileSync("git", ["-C", seed, "push", "-q", "origin", "HEAD:refs/heads/main"]);
    // The container's checkout: what worker.sh's boot leaves behind — a
    // clone at the base, on the branch the worker then works from.
    checkout = join(root, "worktree");
    execFileSync("git", ["clone", "-q", origin, checkout]);

    door = localSandboxDoor({ cwd: checkout });
    git = {
      remote: origin,
      branch: "ticfac/run-epic-43y/tick-dwn/attempt-6",
      identity: { name: "ticfac worker", email: "worker@example.com" },
      setup: "printf ticfac-setup-ok > .setup-marker",
      // Keeps the restore's GLOBAL git config off this test host.
      env: { GIT_CONFIG_GLOBAL: join(root, "git-config-global") },
    };
    env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard"),
      pollMs: 10,
      workspace: git,
    });
    outcomes = [];
  });

  afterEach(() => {
    for (const p of door.processes) {
      if (!p.settled) {
        const pid = p.child.pid;
        if (pid !== undefined) {
          try {
            process.kill(-pid, "SIGKILL");
          } catch {
            /* already gone */
          }
        }
      }
    }
    rmSync(root, { recursive: true, force: true });
  });

  /** Waits for a live condition, polling — never a blind sleep. */
  async function waitFor(what: string, check: () => boolean, timeoutMs = 15_000) {
    const deadline = Date.now() + timeoutMs;
    while (!check()) {
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }

  it("destroys the container mid-turn, and the next turn sees both edits", {
    timeout: 120_000,
  }, async () => {
    const context = BACKGROUND_CONTEXT;
    const faux = fauxProvider();
    faux.setResponses([
      () =>
        fauxAssistantMessage(
          [fauxToolCall("write", { path: "a.txt", content: "the first edit" })],
          {
            stopReason: "toolUse",
          },
        ),
      () =>
        fauxAssistantMessage(
          [fauxToolCall("write", { path: "b.txt", content: "the second edit" })],
          { stopReason: "toolUse" },
        ),
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "sleep 2; echo checked" })], {
          stopReason: "toolUse",
        }),
      () =>
        fauxAssistantMessage([fauxToolCall("read", { path: "a.txt" })], { stopReason: "toolUse" }),
      () =>
        fauxAssistantMessage([fauxToolCall("read", { path: "b.txt" })], { stopReason: "toolUse" }),
      () => fauxAssistantMessage("both edits are there after the restore"),
    ]);
    const models = createModels();
    models.setProvider(faux.provider);

    const registry = createRegistry();
    registry.install(
      defineExtension({
        name: "tools",
        tools: [createReadTool(), createWriteTool(), createTrackedBashTool()],
      }),
    );
    registry.install(
      workspaceCheckpointExtension({
        shell: env.hostShell(),
        workspace: git,
        onCheckpoint: (outcome) => {
          outcomes.push(outcome);
        },
      }),
    );

    const storage = new MemoryStorage();
    const harness = await Harness.open(storage, { models, registry, env: () => env }, context);
    const conversation = await harness.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });
    const submission = await conversation.submit(
      { type: "input", content: "make both edits and run the check" },
      context,
    );

    // Mid-turn, mid-tool: the check is RUNNING when the container dies.
    await waitFor("the check to be running", () => door.spawnCount === 1);
    door.destroyContainer();
    // The destroyed box is gone; the next one boots with an EMPTY workspace —
    // the state the restore has to recover from.
    for (const entry of readdirSync(checkout)) {
      rmSync(join(checkout, entry), { recursive: true, force: true });
    }

    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");

    // Every round pushed a wip: both edits, the restored round's own tree
    // (the setup marker it re-created), then the two read rounds that changed
    // nothing — the carried-work mechanism at tool-round granularity, on the
    // attempt branch the run reads.
    const shas = outcomes.map((o) => o.kind);
    expect(shas).toEqual(["pushed", "pushed", "pushed", "empty", "empty"]);
    const subjects = execFileSync("git", ["--git-dir", origin, "log", "--format=%s", git.branch], {
      encoding: "utf8",
    })
      .trim()
      .split("\n");
    // A snapshot replaces the last (tick xd3): the branch is the newest
    // round's snapshot on top of the agent's own HEAD — here the base, as
    // the faux agent commits nothing.
    expect(subjects).toEqual([WIP_COMMIT_SUBJECT, "the base commit"]);

    const view = await conversation.context(context);
    const messages = view.messages;
    const text = (keep: (m: Message) => boolean) =>
      messages
        .filter(keep)
        .map((m) => textOf(m).join(" "))
        .join(" ");

    // The bash result the model received: the container was lost, and the
    // workspace restored to the LAST wip — the commit carrying BOTH edits.
    const lostResult = text(
      (m) => m.role === "toolResult" && textOf(m).join(" ").includes("lost mid-command"),
    );
    expect(lostResult).toContain("re-run");
    const restoredSha = lostResult.match(/restored to ([0-9a-f]+)/)?.[1];
    expect(restoredSha).toBeDefined();
    expect(
      execFileSync("git", ["--git-dir", origin, "show", "-s", "--format=%s", restoredSha ?? ""], {
        encoding: "utf8",
      }).trim(),
    ).toBe(WIP_COMMIT_SUBJECT);
    // THE ACCEPTANCE: the next turn sees both edits — read back through the
    // env from the workspace the restore rebuilt, from the wip's own tree.
    const readResults = text(
      (m) =>
        m.role === "toolResult" &&
        (textOf(m).join(" ").includes("the first edit") ||
          textOf(m).join(" ").includes("the second edit")),
    );
    expect(readResults).toContain("the first edit");
    expect(readResults).toContain("the second edit");

    // The restored container ran setup, and its tree is the wip's.
    expect(readFileSync(join(checkout, ".setup-marker"), "utf8")).toBe("ticfac-setup-ok");
    expect(readFileSync(join(checkout, "a.txt"), "utf8")).toBe("the first edit");
    expect(readFileSync(join(checkout, "b.txt"), "utf8")).toBe("the second edit");

    await harness.close(context);
  });

  /**
   * A REAL dependency install's shape (tick cni): a repository's `[sandbox]`
   * setup — pnpm install, go mod download, an apt toolchain — prints far more
   * than the run door's whole-output bound (256 KiB, cloudflare/src
   * `RUN_MAX_BYTES`) and runs for minutes. Through the run door the
   * bounding `head -c` SIGPIPEs the install the moment it prints past the
   * bound — the restore would report `the setup command: exit 141` and hand
   * the model a workspace without its dependencies — so the restore's setup
   * line rides the PROCESS doors instead (started once, polled to its end,
   * its output a file a cursor reads: the same doors that already carry the
   * minutes-long tracked bash and the boot and finish phases).
   */
  const chattyInstall =
    'count=0; for i in $(seq 40); do printf "resolving packages %03d: " "$i"; ' +
    'head -c 8192 /dev/zero | tr "\\0" x; printf "\\n"; count=$((count+8217)); done; ' +
    "sleep 0.3; " + // the minutes a real install takes, compressed
    'printf "%s\\n" "$count" > .setup-bytes && printf ticfac-setup-ok > .setup-marker';

  it("a restore's setup that prints past the run door's bound survives it", {
    timeout: 120_000,
  }, async () => {
    const installEnv = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard-install"),
      pollMs: 10,
      workspace: { ...git, setup: chattyInstall },
    });

    // One round's wip first — the state a mid-turn loss restores from:
    // the round's own edit, snapshotted by the checkpoint extension's own
    // producer through the env's own host shell.
    writeFileSync(join(checkout, "a.txt"), "the first edit");
    const wip = await pushWipCheckpoint(installEnv.hostShell(), {
      ...git,
      setup: chattyInstall,
    });
    expect(wip.kind).toBe("pushed");

    // The container is lost: the fresh box boots with an EMPTY workspace —
    // the state the restore rebuilds from the attempt branch.
    for (const entry of readdirSync(checkout)) {
      rmSync(join(checkout, entry), { recursive: true, force: true });
    }
    const restore = await installEnv.restoreLostWorkspace();

    // THE ACCEPTANCE: the install ran to its end — the marker it only writes
    // AFTER the last byte — and the restore held, from the last wip.
    expect(restore).toEqual({
      kind: "restored",
      ...(wip.kind === "pushed" ? { sha: wip.sha } : {}),
      subject: WIP_COMMIT_SUBJECT,
    });
    expect(readFileSync(join(checkout, ".setup-marker"), "utf8")).toBe("ticfac-setup-ok");
    // 328,680 bytes — past the run door's 262,144 — all of them survived.
    expect(Number(readFileSync(join(checkout, ".setup-bytes"), "utf8"))).toBe(328680);

    // The setup line rode the process doors, never the run door: a chatty
    // install through `run` is the SIGPIPE this test exists to end.
    expect(door.runCommands.some((line) => line.includes(".setup-marker"))).toBe(false);
    expect(door.startCommands.some((line) => line.includes(".setup-marker"))).toBe(true);
    // And at the workspace root, like every other restore line.
    const setupLine = door.startCommands.find((line) => line.includes(".setup-marker"));
    expect(setupLine).toContain('mkdir -p "$TICFAC_WORKSPACE"');
  });

  /**
   * The between-rounds loss (tick 4fs): the box is destroyed while NO
   * harness call is in flight — after one round's wip push, before the next
   * round's request — so no loss signal ever reaches the env; its
   * replacement boots EMPTY. Clears the workspace exactly there, twice, so
   * the acceptance also proves the SECOND between-rounds loss restores
   * again.
   */
  const destroyBetweenRounds = () => {
    for (const entry of readdirSync(checkout)) {
      rmSync(join(checkout, entry), { recursive: true, force: true });
    }
  };

  it("destroys the container BETWEEN tool rounds, and the ready check restores before each next round", {
    timeout: 120_000,
  }, async () => {
    const context = BACKGROUND_CONTEXT;
    const faux = fauxProvider();
    faux.setResponses([
      () =>
        fauxAssistantMessage(
          [fauxToolCall("write", { path: "a.txt", content: "the first edit" })],
          { stopReason: "toolUse" },
        ),
      () =>
        fauxAssistantMessage([fauxToolCall("read", { path: "a.txt" })], { stopReason: "toolUse" }),
      () =>
        fauxAssistantMessage([fauxToolCall("read", { path: "a.txt" })], { stopReason: "toolUse" }),
      () => fauxAssistantMessage("every read saw the edit after each restore"),
    ]);
    const models = createModels();
    models.setProvider(faux.provider);

    const restores: RestoreOutcome[] = [];
    const registry = createRegistry();
    registry.install(
      defineExtension({
        name: "tools",
        tools: [createReadTool(), createWriteTool(), createTrackedBashTool()],
      }),
    );
    registry.install(
      workspaceCheckpointExtension({
        shell: env.hostShell(),
        workspace: git,
        // The host's wiring: the env's ready check before every round.
        ensureReady: () => env.ensureWorkspaceReady(),
        // The box is destroyed after the first two rounds' wip pushes — the
        // loss sits BETWEEN rounds, invisible to every other path.
        onCheckpoint: (outcome) => {
          outcomes.push(outcome);
          if (outcomes.length <= 2) destroyBetweenRounds();
        },
        onRestore: (outcome) => {
          restores.push(outcome);
        },
      }),
    );

    const storage = new MemoryStorage();
    const harness = await Harness.open(storage, { models, registry, env: () => env }, context);
    const conversation = await harness.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });
    const submission = await conversation.submit(
      { type: "input", content: "write the edit, then read it twice" },
      context,
    );
    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");

    // THE ACCEPTANCE: every read after a between-rounds loss saw the edit on
    // the workspace the ready check rebuilt — not ENOENT, not an empty tree.
    const view = await conversation.context(context);
    const readResults = view.messages
      .map((m) => textOf(m).join(" "))
      .filter((t) => t.includes("the first edit"));
    expect(readResults.length).toBe(2);
    const lost = view.messages
      .map((m) => textOf(m).join(" "))
      .filter((t) => t.includes("No such file or directory"));
    expect(lost).toEqual([]);

    // Both losses restored, from the attempt branch's real tip: the sha each
    // restore answered is a real wip commit on origin — the second loss
    // proves one restore does not hold the next one's answer.
    expect(restores.length).toBe(2);
    for (const restore of restores) {
      expect(restore.kind).toBe("restored");
      if (restore.kind !== "restored") continue;
      expect(
        execFileSync("git", ["--git-dir", origin, "show", "-s", "--format=%s", restore.sha], {
          encoding: "utf8",
        }).trim(),
      ).toBe(WIP_COMMIT_SUBJECT);
    }

    // The restored workspace really is the wip's tree, setup re-run.
    expect(readFileSync(join(checkout, "a.txt"), "utf8")).toBe("the first edit");
    expect(readFileSync(join(checkout, ".setup-marker"), "utf8")).toBe("ticfac-setup-ok");

    // The ready check ran before every round's request — four of them —
    // ahead of the tools that would otherwise have failed on the empty box.
    const checks = door.runCommands.filter((line) => line.includes('test -e "$CWD/.git"'));
    expect(checks.length).toBe(4);
    const prepares = door.runCommands.filter((line) =>
      line.includes("find . -mindepth 1 -maxdepth 1"),
    );
    expect(prepares.length).toBe(2);

    await harness.close(context);
  });

  // Tick dbi: the nonce path — a tracked bash whose nonce no container
  // knows — restored the workspace with no log line anywhere (the cni
  // staging proof's 139s round was exactly this). The env now hands the
  // restore's outcome to the host's ear, so the host can say which sha the
  // workspace was rebuilt from. Real git, a really emptied box, a really
  // replayed nonce: the sha the callback carries is the real wip on origin.
  it("a nonce-path restore on an emptied box reaches the host's ear with the real sha", {
    timeout: 120_000,
  }, async () => {
    const restores: RestoreOutcome[] = [];
    const heardEnv = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard-heard"),
      pollMs: 10,
      workspace: git,
      onRestore: (outcome) => {
        restores.push(outcome);
      },
    });

    // One round's wip: the state a nonce-path restore rebuilds from.
    writeFileSync(join(checkout, "a.txt"), "the carried edit\n");
    const wip = await pushWipCheckpoint(heardEnv.hostShell(), git);
    expect(wip.kind).toBe("pushed");

    // The container died while no harness watched: the fresh box booted
    // EMPTY, and the replay's nonce is known by no process list.
    for (const entry of readdirSync(checkout)) {
      rmSync(join(checkout, entry), { recursive: true, force: true });
    }
    const result = await heardEnv.exec(
      "cat a.txt",
      { env: { [BASH_NONCE_VAR]: "bash-nonce-heard-1" } },
      BACKGROUND_CONTEXT,
    );
    expect(result.ok).toBe(true);

    // THE ACCEPTANCE: the host heard which sha the nonce path rebuilt from —
    // the real wip on origin, not a mysteriously slow tool round and
    // nothing else.
    expect(restores).toEqual([
      { kind: "restored", sha: wip.kind === "pushed" ? wip.sha : "", subject: WIP_COMMIT_SUBJECT },
    ]);

    // And the command ran on the rebuilt tree: the edit the wip carried,
    // the setup the restore re-ran.
    expect(readFileSync(join(checkout, "a.txt"), "utf8")).toBe("the carried edit\n");
    expect(readFileSync(join(checkout, ".setup-marker"), "utf8")).toBe("ticfac-setup-ok");
  });

  it("pushes nothing for a round that changed no file, and the changed round's wip lands on origin", async () => {
    // A clean clone: the round changed nothing, so nothing commits and
    // nothing pushes — an empty wip would be noise on the attempt branch.
    const empty = await pushWipCheckpoint(env.hostShell(), git);
    expect(empty).toEqual({ kind: "empty" });
    expect(() =>
      execFileSync("git", ["--git-dir", origin, "rev-parse", "--verify", git.branch], {
        stdio: "ignore",
      }),
    ).toThrow(); // no attempt branch on origin yet: nothing was pushed

    // One change: the wip commits, and the branch appears on origin — the
    // ref the run reads for the next attempt's carried work.
    writeFileSync(join(checkout, "a.txt"), "a change\n");
    const pushed = await pushWipCheckpoint(env.hostShell(), git);
    expect(pushed).toEqual({ kind: "pushed", sha: expect.stringMatching(/^[0-9a-f]{40}$/) });
    const onOrigin = execFileSync("git", ["--git-dir", origin, "log", "--format=%s", git.branch], {
      encoding: "utf8",
    }).trim();
    expect(onOrigin).toBe(`${WIP_COMMIT_SUBJECT}\nthe base commit`);
  });

  // The factory's container runs every door command in /workspace (the
  // FactorySandbox's process cwd), while the worker's checkout is
  // TICKS_WORKDIR — /work/repo by default. The host shell's git lines must
  // run AT the checkout, never at whatever directory the door starts in:
  // the xd3 staging run's every wip checkpoint failed "not in a git
  // directory" until they did.
  //
  // Tick hv3: this test's wall clock is process-bound and grows with host
  // load (it failed once in a full `pnpm test` on the loaded host, passing
  // on re-run) — every wip line and the restore are a spawned bash, so a
  // host running a spawn storm (a live reconcile.test, an Xprotect scan)
  // multiplies its runtime: 1.4s quiet, 4.6s at synthetic load 200. It
  // states its own 120s bound, the kjs rule its two Harness-opening
  // siblings in this file already follow (harness-timeout-discipline).
  it("runs the host shell's git at the env's checkout, whatever the door's own directory", {
    timeout: 120_000,
  }, async () => {
    door = localSandboxDoor({ cwd: root });
    env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard"),
      pollMs: 10,
      workspace: git,
    });
    writeFileSync(join(checkout, "a.txt"), "a change\n");
    const pushed = await pushWipCheckpoint(env.hostShell(), git);
    expect(pushed).toEqual({ kind: "pushed", sha: expect.stringMatching(/^[0-9a-f]{40}$/) });
    const onOrigin = execFileSync("git", ["--git-dir", origin, "log", "--format=%s", git.branch], {
      encoding: "utf8",
    }).trim();
    expect(onOrigin).toBe(`${WIP_COMMIT_SUBJECT}\nthe base commit`);

    // And the restore of a fresh container — whose checkout directory does
    // not exist at all — rebuilds it AT the checkout, not in the door's cwd.
    rmSync(checkout, { recursive: true, force: true });
    const restored = await env.restoreLostWorkspace();
    expect(restored.kind).toBe("restored");
    expect(readFileSync(join(checkout, "a.txt"), "utf8")).toBe("a change\n");
    expect(readdirSync(root)).not.toContain(".git");
  });

  // The xd3 staging run: a wip that COMMITTED on the agent's branch made the
  // agent's own `git commit` answer "nothing to commit"; the model rewrote
  // the history it could not explain, and every push after that — the finish
  // phase's fast-forward-only one included — was refused. A checkpoint must
  // be invisible to the work it checkpoints.
  it("snapshots without touching the agent's branch: its commits land, and the finish's fast-forward push holds", async () => {
    const run = (...args: string[]) =>
      execFileSync(
        "git",
        ["-C", checkout, "-c", "user.name=agent", "-c", "user.email=agent@example.com", ...args],
        {
          encoding: "utf8",
        },
      ).trim();
    const onOrigin = (rev: string) =>
      execFileSync("git", ["--git-dir", origin, "rev-parse", rev], { encoding: "utf8" }).trim();
    const base = run("rev-parse", "HEAD");

    // Round 1: an edit, snapshotted — HEAD, index and status untouched.
    writeFileSync(join(checkout, "a.txt"), "the agent's edit\n");
    const first = await pushWipCheckpoint(env.hostShell(), git);
    expect(first.kind).toBe("pushed");
    expect(run("rev-parse", "HEAD")).toBe(base);
    expect(run("status", "--porcelain")).toBe("?? a.txt");
    const snapshot = onOrigin(git.branch);
    expect(
      execFileSync("git", ["--git-dir", origin, "log", "-1", "--format=%s%n%P", snapshot], {
        encoding: "utf8",
      }).trim(),
    ).toBe(`${WIP_COMMIT_SUBJECT}\n${base}`);

    // The agent commits its own work: it is there to commit.
    run("add", "a.txt");
    run("commit", "-q", "-m", "the agent's commit");
    const agentCommit = run("rev-parse", "HEAD");

    // Round 2, clean: the agent's commit is what the branch carries now.
    expect((await pushWipCheckpoint(env.hostShell(), git)).kind).toBe("pushed");
    expect(onOrigin(git.branch)).toBe(agentCommit);
    // Round 3, unchanged: nothing to push.
    expect((await pushWipCheckpoint(env.hostShell(), git)).kind).toBe("empty");

    // Round 4: an uncommitted edit on top, snapshotted over the agent's commit.
    writeFileSync(join(checkout, "b.txt"), "uncommitted\n");
    expect((await pushWipCheckpoint(env.hostShell(), git)).kind).toBe("pushed");
    expect(onOrigin(`${git.branch}~1`)).toBe(agentCommit);

    // A container lost now restores the agent's state exactly: HEAD its own
    // commit, the uncommitted edit still uncommitted.
    for (const entry of readdirSync(checkout))
      rmSync(join(checkout, entry), { recursive: true, force: true });
    const restored = await env.restoreLostWorkspace();
    expect(restored.kind).toBe("restored");
    expect(run("rev-parse", "HEAD")).toBe(agentCommit);
    expect(run("rev-parse", "--abbrev-ref", "HEAD")).toBe(git.branch);
    expect(readFileSync(join(checkout, "b.txt"), "utf8")).toBe("uncommitted\n");
    expect(run("status", "--porcelain").split("\n")).toContain("A  b.txt");

    // Before the finish: the branch back on the agent's HEAD, and the
    // finish phase's fast-forward-only push of its salvage commit holds.
    expect((await retireWipSnapshot(env.hostShell(), git)).kind).toBe("retired");
    expect(onOrigin(git.branch)).toBe(agentCommit);
    run("commit", "-q", "-m", "the finish phase's salvage");
    run("push", "-q", "origin", `HEAD:refs/heads/${git.branch}`);
    expect(onOrigin(git.branch)).toBe(run("rev-parse", "HEAD"));
  });

  // The local host's finish phase (tick nou): a worker that settles with
  // its work uncommitted — the whole shape the wip snapshots were holding
  // on the attempt branch — leaves nothing the supervisor's fast-forward
  // push can land and collect counts. The cloud's finish phase salvages the
  // tree into its own commit (image/worker.sh, tick 5fg); the salvage is
  // that half, runtime-neutral, for the local host to run after it retires
  // the last snapshot. What may ride the salvage is the WORK — never the
  // report (its owner commits or reads it, and a salvage commit carrying it
  // makes the work indistinguishable from the account of it) and never
  // tracker state (.tick/, .ticfac/ — the boundary the guards enforce).
  it("salvages the uncommitted tree after retiring the snapshot, without the report or tracker state", async () => {
    // A dirty round, snapshotted: the state a settled worker leaves behind.
    writeFileSync(join(checkout, "a.txt"), "the worker's uncommitted edit\n");
    writeFileSync(join(checkout, "RESULT-dwn.md"), "# dwn\n\nSTATUS: DONE\n");
    mkdirSync(join(checkout, ".tick"));
    writeFileSync(join(checkout, ".tick", "state"), "a boundary write the guard missed\n");
    expect((await pushWipCheckpoint(env.hostShell(), git)).kind).toBe("pushed");

    // The finish phase's order: retire first (the branch back on the
    // agent's own HEAD, so the push after the salvage is a fast-forward),
    // then the salvage commit on top.
    expect((await retireWipSnapshot(env.hostShell(), git)).kind).toBe("retired");
    const salvaged = await salvageUncommittedWork(env.hostShell(), git, {
      subject: "tick dwn: work in progress salvaged by the test",
      reportPath: "RESULT-dwn.md",
    });
    expect(salvaged).toEqual({
      kind: "salvaged",
      sha: expect.stringMatching(/^[0-9a-f]{40}$/),
    });

    // The salvage carries the WORK. Not the report, not tracker state —
    // both are still in the working tree, uncommitted, for their owners.
    const files = execFileSync("git", ["-C", checkout, "ls-tree", "--name-only", "HEAD"], {
      encoding: "utf8",
    })
      .trim()
      .split("\n");
    expect(files).toContain("a.txt");
    expect(files).toContain("README.md");
    expect(files).not.toContain("RESULT-dwn.md");
    expect(files).not.toContain(".tick");
    const subject = execFileSync("git", ["-C", checkout, "log", "-1", "--format=%s"], {
      encoding: "utf8",
    }).trim();
    expect(subject).toBe("tick dwn: work in progress salvaged by the test");

    // The push after the salvage is a fast-forward — the supervisor's final
    // push lands, where a push over the un-retired snapshot was refused.
    execFileSync("git", ["-C", checkout, "push", "-q", "origin", `HEAD:refs/heads/${git.branch}`]);
    const onOrigin = execFileSync("git", ["--git-dir", origin, "log", "--format=%s", git.branch], {
      encoding: "utf8",
    }).trim();
    expect(onOrigin).toBe("tick dwn: work in progress salvaged by the test\nthe base commit");

    // A tree the salvage already carried answers empty: nothing staged,
    // nothing committed, and the second look changes no commit.
    expect(
      await salvageUncommittedWork(env.hostShell(), git, {
        subject: "twice",
        reportPath: "RESULT-dwn.md",
      }),
    ).toEqual({ kind: "empty" });
    expect(
      execFileSync("git", ["-C", checkout, "log", "-1", "--format=%s"], {
        encoding: "utf8",
      }).trim(),
    ).toBe("tick dwn: work in progress salvaged by the test");
  });
});
