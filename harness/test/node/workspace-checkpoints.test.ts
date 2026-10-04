import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
import { FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import { createTrackedBashTool } from "../../src/tools/tracked-bash.js";
import {
  pushWipCheckpoint,
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

  it("destroys the container mid-turn, and the next turn sees both edits", async () => {
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
    expect(subjects.slice(0, 3)).toEqual([
      WIP_COMMIT_SUBJECT,
      WIP_COMMIT_SUBJECT,
      WIP_COMMIT_SUBJECT,
    ]);
    expect(subjects.at(-1)).toBe("the base commit");

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
});
