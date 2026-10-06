import { BACKGROUND_CONTEXT, withCancel } from "@earendil-works/chord/context";
import { describe, expect, it } from "vitest";
import { guardPathPrefix } from "../src/env/boundary-guard.js";
import { BASH_NONCE_VAR, bashNonceMarker, FactorySandboxEnv } from "../src/env/factory-sandbox.js";
import { fakeSandboxDoor, waitFor } from "./sandbox-door-helper.js";

/**
 * FactorySandboxEnv over the run door and the tracked bash, in workerd: the
 * RPC SHAPES the env speaks — one run per file operation, the nonce marker,
 * reattach without a second start, timeouts that kill, aborts that do not.
 * What the env's command LINES actually do against a real shell is the node
 * suite's half (test/node/), and what the FactorySandbox DO does with them
 * is cloudflare/test/factory-sandbox.test.ts's.
 */

const CONTEXT = BACKGROUND_CONTEXT;
const NONCE = "bash-0000-test";

describe("FactorySandboxEnv: the run door behind every file operation", () => {
  it("runs each short command through the door, in one RPC, with the path as a value", async () => {
    const fake = fakeSandboxDoor();
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null });

    const read = await env.readTextFile("notes/RESULT.md", CONTEXT);
    expect(read.ok).toBe(true);

    // One run RPC per file operation (the whole point of the door), with the
    // path passed as an env var: a path with a space or a quote is a value,
    // never shell text.
    expect(fake.runs.length).toBe(1);
    expect(fake.runs[0]?.env.P).toBe("notes/RESULT.md");
    expect(fake.runs[0]?.command).toContain('cat "$P"');
  });

  it("prefixes every command with the guard's PATH, once installed", async () => {
    const fake = fakeSandboxDoor();
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: "/workspace.guard" });

    // The guard's install runs through the door first — the shim, its
    // companions, the chmod — and then every command carries the guard's
    // PATH in front of its own.
    await env.readTextFile("a.txt", CONTEXT);
    const last = fake.runs.at(-1);
    expect(last?.command.startsWith(guardPathPrefix("/workspace.guard"))).toBe(true);
    expect(last?.command).toContain('cat "$P"');
    expect(fake.runs.some((r) => r.command.includes('mkdir -p "$DIR"'))).toBe(true);
    expect(fake.runs.some((r) => r.command.includes("command -v tk"))).toBe(true);
    expect(fake.runs.some((r) => r.command.includes("chmod +x"))).toBe(true);

    // The install happened once: a second operation pays only its own RPC.
    const after = fake.runs.length;
    await env.exists("a.txt", CONTEXT);
    expect(fake.runs.length).toBe(after + 1);
  });

  it("keeps asking a container that is not ready until it answers", async () => {
    const fake = fakeSandboxDoor();
    fake.refuseReady(2);
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null });

    const read = await env.readTextFile("late.txt", CONTEXT);
    expect(read.ok).toBe(true);
    expect(fake.runs.length).toBe(3);
  });
});

describe("FactorySandboxEnv: the tracked bash", () => {
  it("marks a tracked command with its nonce and streams its output until it exits", async () => {
    const fake = fakeSandboxDoor({ commandMs: 60 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    const chunks: string[] = [];
    const result = await env.exec(
      "sleep 0.06; echo done",
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: (t) => chunks.push(t) },
      CONTEXT,
    );
    expect(result.ok ? "ok" : result.error.code).toBe("ok");
    if (!result.ok) return;
    expect(result.value.exitCode).toBe(0);
    expect(chunks.join("")).toBe("done\n");

    // The command the container got: the nonce marker first (how a replay
    // finds this process), then the working directory, then the command.
    expect(fake.starts.length).toBe(1);
    expect(fake.starts[0]?.command.startsWith(`${bashNonceMarker(NONCE)}\n`)).toBe(true);
    expect(fake.starts[0]?.command).toContain("sleep 0.06; echo done");
    expect(fake.starts[0]?.command).toContain('cd "$TICFAC_CWD"');
    expect(fake.starts[0]?.env.TICFAC_CWD).toBe("/workspace");
  });

  it("reattaches to a still-running process on replay instead of starting it again", async () => {
    const fake = fakeSandboxDoor({ commandMs: 120 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    // Invocation 1: starts the process, then the harness dies — the call is
    // cancelled. The process is NOT killed: a replay must find it.
    const first = withCancel(CONTEXT);
    const promise1 = env.exec(
      "slow command",
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: () => {} },
      first.context,
    );
    await waitFor("the process to start", () => fake.starts.length === 1);
    first.cancel("harness killed");
    const result1 = await promise1;
    expect(result1.ok ? "ok" : result1.error.code).toBe("aborted");
    expect(fake.starts.length).toBe(1);
    expect(fake.kills.length).toBe(0);
    expect(fake.processes[0]?.state).toBe("running");

    // Invocation 2 — the replay, with the same nonce (the durable memo is
    // what makes it the same): reattaches, no second start, reads to the end.
    const chunks: string[] = [];
    const result2 = await env.exec(
      "slow command",
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: (t) => chunks.push(t) },
      CONTEXT,
    );
    expect(fake.starts.length).toBe(1);
    expect(result2.ok ? "ok" : result2.error.code).toBe("ok");
    if (!result2.ok) return;
    expect(result2.value.exitCode).toBe(0);
    expect(chunks.join("")).toBe("done\n");
  });

  // Tick 2oa: the door's object restarted (a deploy: in-memory ready flag
  // lost) between the death and the replay, and with guardDir null there is
  // no `run` before the list — the nonce replay must reattach anyway, on
  // the list's own probe, or the command would run a second time.
  it("reattaches across a restarted object, with no run before the list", async () => {
    const fake = fakeSandboxDoor({ commandMs: 120 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    const first = withCancel(CONTEXT);
    const promise1 = env.exec(
      "slow command",
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: () => {} },
      first.context,
    );
    await waitFor("the process to start", () => fake.starts.length === 1);
    first.cancel("harness killed");
    await promise1;

    // A deploy restarts the Durable Object while the container and its
    // process live on: the object's ready flag is gone.
    fake.restartObject();
    expect(fake.objectReady).toBe(false);

    const result2 = await env.exec("slow command", { env: { [BASH_NONCE_VAR]: NONCE } }, CONTEXT);
    expect(result2.ok ? "ok" : result2.error.code).toBe("ok");
    if (!result2.ok) return;
    expect(result2.value.exitCode).toBe(0);

    // Reattached on the list alone: one start across both invocations, and
    // not ONE run RPC — the guardless path never marked the object ready.
    expect(fake.starts.length).toBe(1);
    expect(fake.runs.length).toBe(0);
  });

  it("answers an already-finished process with its exit code, without running it again", async () => {
    const fake = fakeSandboxDoor({ commandMs: 30 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    const first = withCancel(CONTEXT);
    const promise1 = env.exec("short", { env: { [BASH_NONCE_VAR]: NONCE } }, first.context);
    await waitFor("the process to start", () => fake.starts.length === 1);
    first.cancel("harness killed");
    await promise1;
    await waitFor("the process to finish", () => fake.processes[0]?.state !== "running");

    // A replay that arrives late must not re-run: the exit code it left is
    // the answer.
    const result = await env.exec("short", { env: { [BASH_NONCE_VAR]: NONCE } }, CONTEXT);
    expect(fake.starts.length).toBe(1);
    expect(result.ok && result.value.exitCode).toBe(0);
  });

  it("times a tracked command out by killing its process", async () => {
    const fake = fakeSandboxDoor({ commandMs: 10_000 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    const result = await env.exec(
      "slow",
      { env: { [BASH_NONCE_VAR]: NONCE }, timeout: 0.05 },
      CONTEXT,
    );
    expect(result.ok ? "ok" : result.error.code).toBe("timeout");
    expect(fake.kills.length).toBe(1);
  });

  it("reports a process the container lost mid-command", async () => {
    const fake = fakeSandboxDoor({ commandMs: 10_000 });
    const env = new FactorySandboxEnv({ sandbox: fake.sandbox, guardDir: null, pollMs: 5 });

    const promise = env.exec("slow", { env: { [BASH_NONCE_VAR]: NONCE } }, CONTEXT);
    await waitFor("the process to start", () => fake.starts.length === 1);
    fake.loseRunning();
    const result = await promise;
    expect(result.ok ? "ok" : result.error.code).toBe("unknown");
    if (result.ok) return;
    expect(result.error.message).toContain("lost without an exit code");
  });
});
