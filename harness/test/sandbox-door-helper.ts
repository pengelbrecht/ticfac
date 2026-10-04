import type { SandboxDoor } from "../src/env/sandbox-door.js";

/**
 * A stand-in FactorySandbox door for the workerd suite: it ANSWERS with the
 * same shapes the Durable Object serves (cloudflare/test/factory-sandbox.
 * test.ts holds the object's own behaviour) — processes by id, byte-cursor
 * reads, the exact command strings recorded, and the running → exited
 * transition a container command makes — without running anything. What the
 * env's command LINES do is proven against real bash by the node suite.
 *
 * `die()` models a harness process killed mid-call: every door call issued
 * while it is on returns a promise that never resolves, and stays dead
 * after `thaw()` — exactly what a dead process's pending RPCs look like, and
 * the property that makes the crash deterministic in-process (the dead
 * harness can never wake up and race the resumed one).
 */
export function fakeSandboxDoor(
  options: {
    commandMs?: number;
    commandOutput?: string;
    /** Scripts `run` answers by command: what a git line would print. */
    runOutput?: (command: string, env: Record<string, string>) => string;
  } = {},
) {
  type FakeProcess = {
    id: string;
    command: string;
    output: string;
    state: "running" | "completed" | "failed";
    exit: number | null;
  };
  const commandMs = options.commandMs ?? 100;
  const commandOutput = options.commandOutput ?? "done\n";
  const processes = new Map<string, FakeProcess>();
  const starts: { command: string; env: Record<string, string> }[] = [];
  const runs: { command: string; env: Record<string, string> }[] = [];
  const kills: string[] = [];
  let dead = false;
  let deadCalls = 0;
  let readyRefusals = 0;

  const view = (p: FakeProcess) => ({
    id: p.id,
    state: p.state,
    exit_code: p.exit,
    command: p.command,
  });
  const maybe = <T>(answer: () => T): Promise<T> => {
    if (dead) {
      deadCalls += 1;
      return new Promise(() => {});
    }
    return Promise.resolve(answer());
  };

  const door: SandboxDoor = {
    run(command, env) {
      runs.push({ command, env });
      if (readyRefusals > 0) {
        readyRefusals -= 1;
        return Promise.resolve({ ready: false } as const);
      }
      return Promise.resolve({
        ready: true,
        exitCode: 0,
        output: options.runOutput?.(command, env) ?? "",
        truncated: false,
      });
    },
    async startProcess(command, env) {
      const id = crypto.randomUUID();
      const p: FakeProcess = { id, command, output: "", state: "running", exit: null };
      processes.set(id, p);
      starts.push({ command, env });
      // The container command's behaviour, timed: it prints, then finishes.
      setTimeout(() => {
        if (p.state === "running") {
          p.output += commandOutput;
          p.state = "completed";
          p.exit = 0;
        }
      }, commandMs);
      return view(p);
    },
    getProcess(id) {
      return maybe(() => {
        const p = processes.get(id);
        return p === undefined ? null : view(p);
      });
    },
    listProcesses() {
      return maybe(() => [...processes.values()].map(view));
    },
    readOutput(id, offset) {
      return maybe(() => {
        const p = processes.get(id);
        if (p === undefined) return { text: "", offset };
        return { text: p.output.slice(offset), offset: p.output.length };
      });
    },
    async killProcess(id) {
      const p = processes.get(id);
      if (p === undefined) return;
      kills.push(id);
      p.state = "failed";
      p.exit = 143;
    },
  };

  return {
    sandbox: door,
    starts,
    runs,
    kills,
    get processes() {
      return [...processes.values()];
    },
    /** Makes the next `refusals` run calls answer `ready: false`. */
    refuseReady(refusals: number) {
      readyRefusals = refusals;
    },
    /** Simulated SIGTERM on every running process, with its exit code. */
    killRunning() {
      for (const p of processes.values()) {
        if (p.state === "running") {
          p.state = "failed";
          p.exit = 143;
        }
      }
    },
    /** The container lost the box: running processes end with no exit code. */
    loseRunning() {
      for (const p of processes.values()) {
        if (p.state === "running") {
          p.state = "failed";
          p.exit = null;
        }
      }
    },
    /** The container died and came back EMPTY: it knows none of its processes. */
    forget() {
      processes.clear();
    },
    /** From here on, calls never answer — a process dying mid-call. */
    die() {
      dead = true;
    },
    /** Resumes answering; calls already issued dead stay dead forever. */
    thaw() {
      dead = false;
    },
    get deadCalls() {
      return deadCalls;
    },
  };
}

/** Waits for `check` to hold, polling a live condition — never a blind sleep. */
export async function waitFor(
  what: string,
  check: () => boolean,
  timeoutMs = 10_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!check()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}
