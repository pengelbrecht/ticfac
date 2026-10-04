import { type ChildProcess, spawn } from "node:child_process";
import { open } from "node:fs/promises";
import { join } from "node:path";
import type { SandboxDoor } from "../../src/env/sandbox-door.js";

/**
 * A stand-in FactorySandbox door that runs REAL bash (the node suite's half:
 * the workerd suite proves the env's RPC shapes, this proves its command
 * lines). It mirrors what the FactorySandbox Durable Object serves —
 * processes by id, the exact command string recorded, byte-cursor reads,
 * merged output — over local processes instead of a container:
 *
 *  - `run` executes a short command to completion and answers its exit code
 *    and merged output, bounded like the real door's `head -c`;
 *  - `startProcess` spawns a detached child whose stdout and stderr append
 *    to one log file (the runner's merged-output recipe, image/proc.sh);
 *  - a process's state is its child's real liveness.
 */

export function localSandboxDoor(
  options: { cwd: string; env?: Record<string, string> } = {
    cwd: process.cwd(),
  },
) {
  type LocalProcess = {
    id: string;
    command: string;
    child: ChildProcess;
    logPath: string;
    exitCode: number | null;
    settled: boolean;
  };
  const processes = new Map<string, LocalProcess>();
  const startCommands: string[] = [];
  let nextId = 0;

  const childEnv = (vars: Record<string, string>) => ({
    ...process.env,
    ...options.env,
    ...vars,
  });

  const state = (p: LocalProcess) =>
    p.settled
      ? {
          state: (p.exitCode ?? 0) === 0 ? ("completed" as const) : ("failed" as const),
          exit_code: p.exitCode,
        }
      : { state: "running" as const, exit_code: null };

  const door: SandboxDoor = {
    async run(command, env, runOptions) {
      const max = runOptions?.maxBytes ?? 256 * 1024;
      // The same bounding line the real door builds: the `;` before `}` —
      // without it `{ cmd }` is a bash syntax error (the node suite caught the
      // local door's copy of this exact line missing it) — `head -c` keeps
      // the answer finite and PIPESTATUS keeps the command's own exit code.
      const line = `exec 2>&1; { ${command}; } | head -c ${max + 1}; exit "\${PIPESTATUS[0]}"`;
      const child = spawn("bash", ["-c", line], {
        cwd: options.cwd,
        env: childEnv(env),
        stdio: ["ignore", "pipe", "ignore"],
      });
      const [exitCode, stdout] = await Promise.all([
        new Promise<number>((resolve, reject) => {
          child.on("error", reject);
          child.on("close", (code) => resolve(code ?? 127));
        }),
        new Promise<Buffer>((resolve, reject) => {
          const chunks: Buffer[] = [];
          child.stdout?.on("data", (c) => chunks.push(c));
          child.stdout?.on("end", () => resolve(Buffer.concat(chunks)));
          child.stdout?.on("error", reject);
        }),
      ]);
      const bytes = stdout.subarray(0, max);
      return {
        ready: true,
        exitCode,
        output: bytes.toString("utf8"),
        truncated: stdout.length > max,
      };
    },
    async startProcess(command, env) {
      nextId += 1;
      const id = `local-${nextId}`;
      const logPath = join(options.cwd, `.local-door-${id}.log`);
      const fd = await open(logPath, "a");
      const child = spawn("bash", ["-c", `exec 2>&1; ${command}`], {
        cwd: options.cwd,
        env: childEnv(env),
        detached: true,
        stdio: ["ignore", fd.fd, fd.fd],
      });
      startCommands.push(command);
      const p: LocalProcess = { id, command, child, logPath, exitCode: null, settled: false };
      processes.set(id, p);
      child.on("exit", (code, signal) => {
        p.settled = true;
        // The runner's vocabulary: a signalled process is its 128+signal code
        // (TERM is the only signal this door sends).
        p.exitCode = code !== null ? code : signal === "SIGKILL" ? 137 : 143;
        void fd.close();
      });
      return {
        id,
        state: "running",
        exit_code: null,
        command,
      };
    },
    async getProcess(id) {
      const p = processes.get(id);
      if (p === undefined) return null;
      const s = state(p);
      return {
        id,
        state: s.state,
        exit_code: s.exit_code,
        command: p.command,
      };
    },
    async listProcesses() {
      const views = [];
      for (const p of processes.values()) {
        const s = state(p);
        views.push({ id: p.id, state: s.state, exit_code: s.exit_code, command: p.command });
      }
      return views;
    },
    async readOutput(id, offset) {
      const p = processes.get(id);
      if (p === undefined) return { text: "", offset };
      const { createReadStream, statSync } = await import("node:fs");
      const size = statSync(p.logPath).size;
      if (offset >= size) return { text: "", offset };
      const text: string[] = [];
      await new Promise<void>((resolve, reject) => {
        const stream = createReadStream(p.logPath, { start: offset, end: size - 1 });
        stream.on("data", (c) => text.push(c.toString("utf8")));
        stream.on("end", () => resolve());
        stream.on("error", reject);
      });
      return { text: text.join(""), offset: size };
    },
    async killProcess(id) {
      const p = processes.get(id);
      if (p === undefined || p.settled) return;
      // TERM to the group, the runner's rule.
      const pid = p.child.pid;
      if (pid !== undefined) {
        try {
          process.kill(-pid, "SIGTERM");
        } catch {
          p.child.kill("SIGTERM");
        }
      }
    },
  };

  return {
    sandbox: door,
    startCommands,
    /** How many processes were actually spawned — the "no re-run" witness. */
    get spawnCount() {
      return processes.size;
    },
    get processes() {
      return [...processes.values()];
    },
  };
}
