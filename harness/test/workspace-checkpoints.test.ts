import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  createModels,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
} from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { createReadTool, createWriteTool } from "@earendil-works/pi-durable/tools";
import { describe, expect, it } from "vitest";
import { BASH_NONCE_VAR, FactorySandboxEnv } from "../src/env/factory-sandbox.js";
import { PROCESS_CWD } from "../src/env/sandbox-door.js";
import { createTrackedBashTool } from "../src/tools/tracked-bash.js";
import {
  type RestoreOutcome,
  WIP_COMMIT_SUBJECT,
  type WipOutcome,
  type WorkspaceGit,
  workspaceCheckpointExtension,
} from "../src/workspace/checkpoints.js";
import { fakeSandboxDoor, waitFor } from "./sandbox-door-helper.js";

/**
 * Workspace checkpoints (epic 43y, step 4 of
 * docs/spikes/n0b-round2-pi-durable.md, tick dwn): the afterTools wip commit
 * pushed to the attempt branch, and the restore of a container lost mid-turn.
 *
 * This is the WORKERD half — the door's RPC shapes, the hook wiring and the
 * turn's continuation on the real pi-durable Harness — over the scripted
 * stand-in door (./sandbox-door-helper.ts). What the git command LINES
 * actually do is proven against real git by the node suite
 * (test/node/workspace-checkpoints.test.ts), which carries the tick's
 * acceptance test: destroy the container mid-turn; the next turn sees both
 * edits.
 */

/** The attempt's workspace git, as the tests address it: the factory's shape, example.com values. */
const GIT: WorkspaceGit = {
  remote: "https://example.com/ticfac.git",
  branch: "ticfac/run-epic-43y/tick-dwn/attempt-6",
  identity: { name: "ticfac worker", email: "worker@example.com" },
};

describe("the wip checkpoint after every tool round", () => {
  it("pushes one wip commit to the attempt branch per round, on the real hook wiring", async () => {
    const context = BACKGROUND_CONTEXT;
    // The door answers a scripted sha for the commit line; everything else
    // succeeds. The door runs nothing — the node suite proves the lines.
    const door = fakeSandboxDoor({
      // The snapshot line answers the snapshot's sha and its record key.
      runOutput: (command) =>
        command.includes("git commit-tree") ? "f00dcafef00d\nf00d cafe\n" : "",
    });
    const env = new FactorySandboxEnv({ sandbox: door.sandbox, guardDir: null, pollMs: 10 });

    const faux = fauxProvider();
    faux.setResponses([
      () =>
        fauxAssistantMessage([fauxToolCall("write", { path: "a.txt", content: "edit one" })], {
          stopReason: "toolUse",
        }),
      () =>
        fauxAssistantMessage([fauxToolCall("bash", { command: "echo nothing changed" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("the turn completed"),
    ]);
    const models = createModels();
    models.setProvider(faux.provider);

    const outcomes: WipOutcome[] = [];
    const registry = createRegistry();
    registry.install(
      defineExtension({
        name: "tools",
        tools: [createWriteTool(), createTrackedBashTool()],
      }),
    );
    registry.install(
      workspaceCheckpointExtension({
        shell: env.hostShell(),
        workspace: GIT,
        onCheckpoint: (outcome) => {
          outcomes.push(outcome);
        },
      }),
    );

    const storage = new MemoryStorage();
    const harness = await Harness.open(storage, { models, registry, env: () => env }, context);
    const root = await harness.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });
    const submission = await root.submit({ type: "input", content: "make an edit" }, context);
    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");
    await harness.close(context);

    // One wip per tool round, each pushed with its sha — and the turn
    // completed normally behind the checkpoints.
    expect(outcomes).toEqual([
      { kind: "pushed", sha: "f00dcafef00d" },
      { kind: "pushed", sha: "f00dcafef00d" },
    ]);

    const runs = door.runs;
    // A snapshot (plumbing in a throwaway index, parent the agent's HEAD),
    // never a commit on the agent's branch (tick xd3).
    const commits = runs.filter((r) => r.command.includes("git commit-tree"));
    expect(commits.length).toBe(2);
    expect(commits[0]?.command).toContain("GIT_INDEX_FILE");
    expect(runs.some((r) => r.command.includes("git commit -q"))).toBe(false);
    expect(commits[0]?.env.MSG).toBe(WIP_COMMIT_SUBJECT);
    expect(commits[0]?.env.NAME).toBe(GIT.identity.name);
    expect(commits[0]?.env.EMAIL).toBe(GIT.identity.email);

    const pushes = runs.filter((r) => r.command.includes("git push -q"));
    expect(pushes.length).toBe(2);
    expect(pushes[0]?.command).toContain('git push -q -f "$REMOTE" "$SHA:refs/heads/$BRANCH"');
    expect(pushes[0]?.env.SHA).toBe("f00dcafef00d");
    expect(pushes[0]?.env.REMOTE).toBe(GIT.remote);
    expect(pushes[0]?.env.BRANCH).toBe(GIT.branch);
  });
});

describe("a container lost mid-turn", () => {
  /** A door that answers the restore's read-back line with a wip tip. */
  const lostDoor = () =>
    fakeSandboxDoor({
      commandMs: 500,
      runOutput: (command) =>
        command.includes("git rev-parse HEAD && git log -1 --format=%s")
          ? "cafef00d\nwip: tool round\n"
          : "",
    });

  it("is restored from the attempt branch's tip, and the model is told to re-run", async () => {
    const door = lostDoor();
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      guardDir: null,
      pollMs: 10,
      workspace: { ...GIT, setup: "printf restored-setup-ok > .setup-marker" },
    });

    const promise = env.exec(
      "sleep 0.5; echo done",
      { env: { [BASH_NONCE_VAR]: "bash-lost-1" } },
      BACKGROUND_CONTEXT,
    );
    await waitFor("the tracked command to start", () => door.starts.length === 1);
    // The container is destroyed mid-command: its process is LOST — it ended
    // with no exit code, the runner's "lost".
    door.loseRunning();
    const result = await promise;

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error.code).toBe("unknown");
    expect(result.error.message).toContain("the container was lost mid-command");
    expect(result.error.message).toContain("restored to cafef00d (wip: tool round)");
    expect(result.error.message).toContain("re-run");

    // The restore drove the whole sequence in order: one line clearing and
    // cloning the workspace, then the fetch of the attempt branch and the
    // checkout of its tip — all SHORT lines through the run door — and the
    // SETUP as a background process the door polls (tick cni): through the
    // run door its bounding `head -c` SIGPIPEs a chatty install, so the one
    // long line of the restore rides the process doors instead.
    const lines = door.runs.map((r) => r.command);
    const at = (needle: string) => lines.findIndex((line) => line.includes(needle));
    const prepare = at("find . -mindepth 1 -maxdepth 1");
    expect(prepare).toBeGreaterThanOrEqual(0);
    const prepareLine = lines[prepare] ?? "";
    expect(prepareLine).toContain("git init -q .");
    expect(prepareLine).toContain('git remote add origin "$REMOTE"');
    const fetch = at('git fetch -q origin "$BRANCH"');
    const checkout = at('git checkout -q -B "$BRANCH" FETCH_HEAD');
    expect(fetch).toBeGreaterThan(prepare);
    expect(checkout).toBeGreaterThan(fetch);
    const fetchRun = door.runs[fetch];
    expect(fetchRun?.env.BRANCH).toBe(GIT.branch);
    const started = door.starts.findIndex((s) => s.command.includes(".setup-marker"));
    expect(started).toBeGreaterThanOrEqual(0);
    expect(lines.some((line) => line.includes(".setup-marker"))).toBe(false);
    const setupLine = door.starts[started]?.command ?? "";
    expect(setupLine).toContain('mkdir -p "$TICFAC_WORKSPACE"');
    expect(setupLine).toContain('cd "$TICFAC_WORKSPACE"');
    expect(door.starts[started]?.env.TICFAC_WORKSPACE).toBe(PROCESS_CWD);
  });

  it("is restored when the fresh container knows none of its processes", async () => {
    const door = lostDoor();
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      guardDir: null,
      pollMs: 10,
      workspace: GIT,
    });

    const promise = env.exec(
      "sleep 0.5; echo done",
      { env: { [BASH_NONCE_VAR]: "bash-forgotten-1" } },
      BACKGROUND_CONTEXT,
    );
    await waitFor("the tracked command to start", () => door.starts.length === 1);
    // The container died and came back empty: the seam answers `gone` — the
    // process list starts over, so not even the id is known.
    door.forget();
    const result = await promise;

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error.message).toContain("no longer knows this process");
    expect(result.error.message).toContain("restored to cafef00d");
  });

  it("fails loudly when no workspace git is configured to restore from", async () => {
    const door = lostDoor();
    const env = new FactorySandboxEnv({ sandbox: door.sandbox, guardDir: null, pollMs: 10 });

    const promise = env.exec(
      "sleep 0.5; echo done",
      { env: { [BASH_NONCE_VAR]: "bash-nogit-1" } },
      BACKGROUND_CONTEXT,
    );
    await waitFor("the tracked command to start", () => door.starts.length === 1);
    door.loseRunning();
    const result = await promise;

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.error.message).toContain("lost mid-command");
    expect(result.error.message).toContain("no workspace git");
    // And the restore drove no git through the door.
    expect(door.runs.some((r) => r.command.includes("git"))).toBe(false);
  });

  it("checks the workspace before a replay re-starts a command, and leaves a live one alone", async () => {
    const door = fakeSandboxDoor({ commandMs: 100 });
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      guardDir: null,
      pollMs: 10,
      workspace: GIT,
    });

    // A plain start (no nonce) never pays the marker check.
    const plain = await env.exec("echo plain", undefined, BACKGROUND_CONTEXT);
    expect(plain.ok).toBe(true);
    expect(door.runs.some((r) => r.command.includes('test -e "$CWD/.git"'))).toBe(false);

    // A replay whose process is gone — a container that died while no
    // harness watched — checks that the workspace is there before starting
    // on it, so an empty booted box is restored instead of run on.
    const replay = await env.exec(
      "echo replayed",
      { env: { [BASH_NONCE_VAR]: "bash-replay-9" } },
      BACKGROUND_CONTEXT,
    );
    expect(replay.ok).toBe(true);
    expect(door.runs.some((r) => r.command.includes('test -e "$CWD/.git"'))).toBe(true);
    expect(door.starts.length).toBe(2);
  });
});

describe("a container lost BETWEEN tool rounds", () => {
  /**
   * The pre-round ready check (epic 43y, tick 4fs): a container destroyed
   * while NO harness call is in flight — after one round's wip push, before
   * the next round's first request — boots empty, and the run-door RPC has
   * no loss signal to hook, so only a HOST-side check before the round can
   * see it. The extension's `beforeRequest` verifies the ready marker and
   * restores before the model's next request goes out; the node suite
   * (test/node/workspace-checkpoints.test.ts) carries the real-git half.
   */
  it("is restored by the ready check before the next round's request, on the real hook wiring", async () => {
    const context = BACKGROUND_CONTEXT;
    // The destroyed box's replacement boots EMPTY: the ready marker is
    // gone, so `test -e` fails — armed the moment round 1's wip lands, so
    // the loss sits exactly BETWEEN the rounds.
    let emptied = false;
    let pushedRounds = 0;
    const door = fakeSandboxDoor({
      // The wip push's rev-parse answers the round's commit sha; the
      // restore's read-back answers the attempt branch's tip.
      runOutput: (command) =>
        command.includes("git rev-parse HEAD && git log -1 --format=%s")
          ? "cafef00d\nwip: tool round\n"
          : command.includes("git commit-tree")
            ? "f00dcafef00d\nf00d cafe\n"
            : "",
      runExit: (command) => (emptied && command.includes('test -e "$CWD/.git"') ? 1 : 0),
    });
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      guardDir: null,
      pollMs: 10,
      workspace: { ...GIT, setup: "printf restored-setup-ok > .setup-marker" },
    });

    const faux = fauxProvider();
    faux.setResponses([
      () =>
        fauxAssistantMessage([fauxToolCall("write", { path: "a.txt", content: "the edit" })], {
          stopReason: "toolUse",
        }),
      () =>
        fauxAssistantMessage([fauxToolCall("read", { path: "a.txt" })], {
          stopReason: "toolUse",
        }),
      () => fauxAssistantMessage("the round after the loss completed on the restored workspace"),
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
        workspace: GIT,
        // The host's wiring, as the WorkerAgent and local hosts do it: the
        // env's ready check, before every round.
        ensureReady: () => env.ensureWorkspaceReady(),
        // Round 1's wip landed: the box under it is destroyed — the loss
        // this tick is about, invisible to every other path.
        onCheckpoint: () => {
          pushedRounds += 1;
          if (pushedRounds === 1) emptied = true;
        },
        onRestore: (outcome) => {
          restores.push(outcome);
          emptied = false;
        },
      }),
    );

    const storage = new MemoryStorage();
    const harness = await Harness.open(storage, { models, registry, env: () => env }, context);
    const root = await harness.root(context, {
      agent: { model: { provider: "faux", modelId: "faux-1" } },
    });
    const submission = await root.submit(
      { type: "input", content: "make an edit, then read it back" },
      context,
    );
    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");
    await harness.close(context);

    // The check restored the box from the attempt branch's tip before the
    // next round's request went out, and the host saw the outcome.
    expect(restores).toEqual([{ kind: "restored", sha: "cafef00d", subject: "wip: tool round" }]);

    // The ready check ran before EVERY request — three of them — and only
    // the one on the empty box restored.
    const lines = door.runs.map((r) => r.command);
    const checkAt = lines
      .map((line, index) => (line.includes('test -e "$CWD/.git"') ? index : -1))
      .filter((index) => index >= 0);
    expect(checkAt.length).toBe(3);
    const at = (needle: string) => lines.findIndex((line) => line.includes(needle));
    const firstPush = at("git push -q");
    const prepare = at("find . -mindepth 1 -maxdepth 1");
    const read = at('base64 < "$P"');
    expect(firstPush).toBeGreaterThanOrEqual(0);
    expect(checkAt[0]).toBeLessThan(firstPush); // round 1 checked ready
    expect(checkAt[1]).toBeGreaterThan(firstPush); // round 2 checked the EMPTY box
    expect(prepare).toBeGreaterThan(checkAt[1]); // and restored before its tools
    expect(read).toBeGreaterThan(prepare); // the round read on the restored tree
  });
});
