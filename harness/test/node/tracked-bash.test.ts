import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT, withCancel } from "@earendil-works/chord/context";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { BASH_NONCE_VAR, FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import { localSandboxDoor } from "./local-sandbox-door.js";

/**
 * The tracked bash reattaching to a REAL process: the second half of the
 * tick's acceptance test. The workerd suite proves the reattach on the real
 * pi-durable harness and recovery (tracked-bash-resume.test.ts); this proves
 * the process underneath is real — a child bash that keeps running when the
 * harness's invocation is cancelled, is found by nonce through the live
 * process list, and finishes exactly once.
 */

const CONTEXT = BACKGROUND_CONTEXT;
const NONCE = "bash-0001-node-test";

describe("the tracked bash over a real running process", () => {
  // Tick fim: this test drives the door's real child bash and waits on its
  // progress — process-driving, so it states its own 300s bound
  // (harness-timeout-discipline, the 7wg rule) instead of borrowing the
  // quiet-host 30s default.
  let root: string;
  let door: ReturnType<typeof localSandboxDoor>;
  let env: FactorySandboxEnv;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ticfac-tracked-"));
    const checkout = join(root, "worktree");
    mkdirSync(checkout, { recursive: true });
    execFileSync("git", ["init", "-q", checkout]);
    door = localSandboxDoor({ cwd: checkout });
    env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard"),
      pollMs: 10,
    });
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

  it("cancels an invocation without killing the process, then reattaches to the same one", {
    timeout: 300_000,
  }, async () => {
    // A command slow enough that the crash beats it, printing a marker when
    // it finishes.
    const command = "sleep 0.8; echo tick-finished";

    // Invocation 1: the harness's call is cancelled mid-run — the process
    // keeps running, exactly as a container process does when the harness
    // that started it dies.
    const first = withCancel(CONTEXT);
    const promise1 = env.exec(
      command,
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: () => {} },
      first.context,
    );
    await waitFor("the process to be running", () => door.spawnCount === 1);
    const pidBefore = door.processes[0]?.child.pid;
    first.cancel("harness killed");
    const result1 = await promise1;
    expect(result1.ok ? "ok" : result1.error.code).toBe("aborted");
    expect(door.spawnCount).toBe(1);

    // The process survived the cancel — the thing a reattach needs.
    await waitFor("the abandoned process to finish", () => door.processes[0]?.settled === true);

    // Invocation 2 — the replay, same nonce: it finds the process through
    // the live list, runs NOTHING again, and reads the marker it printed.
    const chunks: string[] = [];
    const result2 = await env.exec(
      command,
      { env: { [BASH_NONCE_VAR]: NONCE }, onOutput: (t) => chunks.push(t) },
      CONTEXT,
    );
    expect(door.spawnCount).toBe(1);
    expect(result2.ok ? "ok" : result2.error.code).toBe("ok");
    if (!result2.ok) return;
    expect(result2.value.exitCode).toBe(0);
    expect(chunks.join("")).toContain("tick-finished");
    expect(door.processes[0]?.child.pid).toBe(pidBefore);
  });
});
