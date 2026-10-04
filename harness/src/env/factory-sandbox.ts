/**
 * FactorySandboxEnv — a pi-durable `ExecutionEnv` over the factory's
 * FactorySandbox Durable Object (epic 43y, step 3 of
 * docs/spikes/n0b-round2-pi-durable.md, tick kgk).
 *
 * The spike's `ContainerEnv`, productised: the `FileSystem` subset the
 * built-in tools use runs as SHORT container commands through the
 * `FactorySandbox.run` door (one RPC per operation, bounded output), and the
 * `Shell` is a REPLAY-SAFE tracked bash:
 *
 *  - every command the env runs resolves `tk` through the boundary guard's
 *    shim (see ./boundary-guard.ts) — the agent's PATH, not the container's,
 *    exactly as image/worker.sh installs for the pi-CLI path;
 *  - a tracked bash call mints a nonce, memoises it durably (the bash tool's
 *    `prepare`, see ./tracked-bash.ts), and embeds it in the container
 *    command; on replay — a harness killed mid-bash, resumed in a new
 *    process — the env finds the STILL-RUNNING process by nonce through
 *    `listProcesses` and reattaches to it, reading on from the cursor,
 *    instead of running the command again. This is the prototype's
 *    experiment 3, and tick kgk's acceptance test.
 *
 * Workspace checkpoints (epic 43y step 4, tick dwn): with `workspace` set,
 * a container LOST mid-command — its process gone, or ended with no exit
 * code — is restored from the attempt branch (./workspace/checkpoints.ts):
 * the fresh box boots empty, the env clears, clones, checks out the last
 * wip commit, runs setup, and hands the model a "restored to …; re-check
 * and re-run" result so the turn continues. A replay whose process is
 * gone checks the workspace is there before re-starting on it. And a
 * container lost BETWEEN tool rounds — no harness call in flight, no loss
 * signal anywhere in the door's RPCs — is caught by the host-side ready
 * check before the next round (tick 4fs): `ensureWorkspaceReady`, wired
 * into the checkpoint extension's `beforeRequest` hook, verifies the
 * ready marker and restores before the round's request goes out.
 *
 * Runtime-neutral on purpose: the door is structural (./sandbox-door.ts), so
 * the same env code runs in the cloud (a DO stub) and in tests (a local
 * stand-in door over real bash — the node suite).
 */

import type { Context } from "@earendil-works/chord";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  type ExecutionEnv,
  ExecutionError,
  err,
  FileError,
  type FileInfo,
  ok,
  type Result,
  type ShellExecOptions,
  type ShellExecResult,
  type TextLineReader,
} from "@earendil-works/pi-durable/env";
import {
  type HostShell,
  type RestoreOutcome,
  restoreWorkspace,
  type WorkspaceGit,
  type WorkspaceReadyOutcome,
} from "../workspace/checkpoints.js";
import {
  defaultGuardDir,
  type GuardInstallBase,
  guardPathPrefix,
  installBoundaryGuard,
} from "./boundary-guard.js";
import {
  PROCESS_CWD,
  type SandboxDoor,
  type SandboxOutput,
  type SandboxRunOutcome,
} from "./sandbox-door.js";

// ------------------------------------------------------------ constants ---

/** The env var the tracked bash's nonce arrives through (set by the bash tool's prepare hook). */
export const BASH_NONCE_VAR = "TICFAC_BASH_NONCE";

/** The comment the env prefixes a tracked command with; how a replay finds it. */
export const bashNonceMarker = (nonce: string) => `# ticfac-bash-nonce ${nonce}`;

/** How often a tracked process is asked for state and new output. */
export const BASH_POLL_MS = 250;

/** Exit-code sentinels the file operations classify their failures with. */
const ENOENT = 42;
const EISDIR = 43;
const ENOTDIR = 44;

/** The most one file read may move through the door (an error beyond). */
const READ_MAX_BYTES = 4 * 1024 * 1024;

/** One write chunk, before base64: an env var's value is bounded (Linux caps one at 128 KiB). */
const WRITE_CHUNK_BYTES = 24 * 1024;

/** How long the env keeps asking a booting container for its first answer. */
const READY_TIMEOUT_MS = 20 * 60 * 1000;

/** How long one run-door call may itself wait for a starting container. */
const READY_WAIT_MS = 30_000;

/**
 * One directory listing, one command: kinds by shell tests, sizes and
 * mtimes by whichever `stat` answers — GNU (the image) or BSD (the node
 * suite's macOS host) — sorted for a stable answer. `find -printf` would
 * be one command shorter, and BSD find does not have it.
 */
const LISTING_LINE = [
  'find "$P" -mindepth 1 -maxdepth 1 -exec sh -c',
  "'for f do",
  'if [ -L "$f" ]; then t=l; elif [ -d "$f" ]; then t=d; else t=f; fi;',
  's=$(stat -c %s "$f" 2>/dev/null || stat -f %z "$f");',
  'm=$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f");',
  'printf "%s|%s|%s|%s\\n" "$t" "$(basename "$f")" "$s" "$m"; done\' sh {} +',
  "| sort",
].join(" ");

export type FactorySandboxEnvOptions = {
  /** The FactorySandbox DO, structurally (or a test's stand-in for it). */
  readonly sandbox: SandboxDoor;
  /** The sandbox's name — what the factory addresses containers by. */
  readonly name?: string;
  /** The working directory; the image's is /workspace. */
  readonly cwd?: string;
  /**
   * The boundary guard's directory. Default: a sibling of the working
   * directory (`<cwd>.guard`, the image's own convention). `null` disables
   * the guard — for a stand-in door that cannot run the shim, or a sandbox
   * whose container installs its own.
   */
  readonly guardDir?: string | null;
  /** How long to keep asking a booting container before failing. Default 20 min. */
  readonly readyTimeoutMs?: number;
  /** The tracked-bash poll interval; tests make it short. */
  readonly pollMs?: number;
  /** A clock, for tests. */
  readonly now?: () => number;
  /**
   * This attempt's workspace git (epic 43y step 4, tick dwn): the attempt
   * branch a lost container is restored from, and whose wip commits the
   * checkpoint extension pushes ({@link hostShell} feeds it). Without it a
   * lost container is only reported, never restored.
   */
  readonly workspace?: WorkspaceGit;
};

/** One short command's outcome, as the env classifies it. */
type ShortOutcome = { exitCode: number; output: string; truncated: boolean };

/** What one file read is allowed to ask of the target: a file, a directory, or either. */
type TargetGuard = "file" | "dir" | "exists";

/**
 * The container was not ready to answer a command within the env's deadline.
 * Distinct from every FileError: nothing failed, the container never
 * answered its first command.
 */
export class SandboxUnavailableError extends Error {
  constructor(readonly detail: string) {
    super(`factory sandbox: ${detail}`);
  }
}

// ---------------------------------------------------------------- base64 ---

const B64_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

function base64Encode(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i] as number;
    const b1 = i + 1 < bytes.length ? (bytes[i + 1] as number) : 0;
    const b2 = i + 2 < bytes.length ? (bytes[i + 2] as number) : 0;
    out += B64_ALPHABET[b0 >> 2];
    out += B64_ALPHABET[((b0 & 0x03) << 4) | (b1 >> 4)];
    out += i + 1 < bytes.length ? B64_ALPHABET[((b1 & 0x0f) << 2) | (b2 >> 6)] : "=";
    out += i + 2 < bytes.length ? B64_ALPHABET[b2 & 0x3f] : "=";
  }
  return out;
}

function base64Decode(text: string): Uint8Array {
  const clean = text.replace(/[^A-Za-z0-9+/=]/g, "");
  const out: number[] = [];
  for (let i = 0; i < clean.length; i += 4) {
    const c = [0, 1, 2, 3].map((k) => {
      const ch = clean[i + k];
      return ch === "=" || ch === undefined ? -1 : B64_ALPHABET.indexOf(ch);
    });
    const n0 = c[0] ?? -1;
    const n1 = c[1] ?? -1;
    if (n0 < 0 || n1 < 0) break;
    out.push((n0 << 2) | (n1 >> 4));
    if ((c[2] ?? -1) >= 0) out.push(((n1 & 0x0f) << 4) | ((c[2] as number) >> 2));
    if ((c[3] ?? -1) >= 0) out.push((((c[2] as number) & 0x03) << 6) | (c[3] as number));
  }
  return new Uint8Array(out);
}

// ---------------------------------------------------------------- the env ---

export class FactorySandboxEnv implements ExecutionEnv {
  /** Each container sees its own files; ids are per name. */
  readonly id: string;
  cwd: string;

  private readonly door: SandboxDoor;
  private readonly guardDir: string | null;
  private readonly readyTimeoutMs: number;
  private readonly pollMs: number;
  private readonly now: () => number;
  private readonly workspace: WorkspaceGit | null;
  private guardInstalling: Promise<void> | undefined;
  private restoring: Promise<RestoreOutcome> | undefined;

  constructor(options: FactorySandboxEnvOptions) {
    this.door = options.sandbox;
    this.cwd = options.cwd ?? PROCESS_CWD;
    this.id = `factory-sandbox:${options.name ?? "default"}`;
    this.guardDir = options.guardDir === undefined ? defaultGuardDir(this.cwd) : options.guardDir;
    this.readyTimeoutMs = options.readyTimeoutMs ?? READY_TIMEOUT_MS;
    this.pollMs = options.pollMs ?? BASH_POLL_MS;
    this.now = options.now ?? Date.now;
    this.workspace = options.workspace ?? null;
  }

  // ------------------------------------------------------------ the guard ---

  /** Installs the boundary guard once; every public command runs behind it. */
  private ensureGuard(): Promise<void> {
    if (this.guardDir === null) return Promise.resolve();
    this.guardInstalling ??= installBoundaryGuard(this.guardBase(), {
      dir: this.guardDir,
      context: BACKGROUND_CONTEXT,
    }).catch((error: unknown) => {
      // A failed install is retried by the next command, never cached.
      this.guardInstalling = undefined;
      throw error;
    });
    return this.guardInstalling;
  }

  /** The guard installer's view of this env: raw short commands, no guard. */
  private guardBase(): GuardInstallBase {
    return {
      cwd: this.cwd,
      short: (command, vars) => this.rawShort(command, vars),
      writeTextFile: async (path, content) => {
        const wrote = await this.rawWrite(path, content, false);
        if (!wrote.ok) throw new Error(`boundary guard: could not write ${path}`);
      },
    };
  }

  // -------------------------------------------------------- short commands ---

  /**
   * One short command through the run door, with the guard's PATH in front.
   * Retries `ready: false` until the env's own readiness deadline — a cold
   * boot is a minutes-long image pull, and the door refuses to park one RPC
   * for that long, so the waiting is here, in the env.
   */
  private short(
    line: string,
    vars: Record<string, string>,
    maxBytes?: number,
  ): Promise<ShortOutcome> {
    return this.ensureGuard().then(
      () => this.rawShort(line, vars, maxBytes),
      (error) => Promise.reject(error),
    );
  }

  private async rawShort(
    line: string,
    vars: Record<string, string>,
    maxBytes?: number,
  ): Promise<ShortOutcome> {
    const deadline = this.now() + this.readyTimeoutMs;
    while (true) {
      const out: SandboxRunOutcome = await this.door.run(this.withGuard(line), vars, {
        boot: { keepAlive: true },
        readyWaitMs: READY_WAIT_MS,
        ...(maxBytes === undefined ? {} : { maxBytes }),
      });
      if (out.ready)
        return { exitCode: out.exitCode, output: out.output, truncated: out.truncated };
      if (this.now() >= deadline) {
        throw new SandboxUnavailableError(
          "the container did not answer its first command in time — the boot may still be pulling",
        );
      }
    }
  }

  /** The command line the door sees: the guard's `tk` ahead of every other. */
  private withGuard(line: string): string {
    return this.guardDir === null ? line : guardPathPrefix(this.guardDir) + line;
  }

  /**
   * The host-side git shell: one short command per line through the run
   * door, the guard's PATH in front, at the workspace root — what the wip
   * checkpoint and the lost-container restore drive `git` through
   * (./workspace/checkpoints.ts). Host commands, never the model's tracked
   * bash: untracked, un-nonce'd, and the door's output bound applies.
   */
  hostShell(): HostShell {
    // AT the workspace root: the door runs a command in the container's own
    // process directory (the factory's /workspace), and the worker's checkout
    // is elsewhere (TICKS_WORKDIR, /work/repo by default) — every wip
    // checkpoint of the xd3 staging run failed "not in a git directory"
    // until the line said where it runs. Created when missing: a fresh
    // container after a loss has no checkout directory at all, and the
    // restore is what rebuilds it.
    return {
      execLine: (line, vars) =>
        this.short(`mkdir -p "$TICFAC_WORKSPACE" && cd "$TICFAC_WORKSPACE" || exit 1\n${line}`, {
          ...vars,
          TICFAC_WORKSPACE: this.cwd,
        }),
    };
  }

  /** Writes `content` as `path`, in chunks: one env var's value is bounded. */
  private async rawWrite(
    path: string,
    content: string | Uint8Array,
    append: boolean,
  ): Promise<Result<void, FileError>> {
    const bytes = typeof content === "string" ? new TextEncoder().encode(content) : content;
    if (bytes.length === 0) {
      const out = await this.rawShort(append ? 'touch "$F"' : ': >"$F"', { F: path });
      return this.voidOf(out, path);
    }
    for (let offset = 0; offset < bytes.length; offset += WRITE_CHUNK_BYTES) {
      const chunk = bytes.subarray(offset, Math.min(offset + WRITE_CHUNK_BYTES, bytes.length));
      const redirect = offset === 0 && !append ? ">" : ">>";
      const out = await this.rawShort(`printf '%s' "$B64" | base64 -d ${redirect} "$F"`, {
        B64: base64Encode(chunk),
        F: path,
      });
      const done = this.voidOf(out, path);
      if (!done.ok) return done;
    }
    return ok(undefined);
  }

  // ------------------------------------------------------ failure mapping ---

  /** Every fallible op of this env runs inside `attempt`: nothing throws out. */
  private async attempt<T>(op: () => Promise<Result<T, FileError>>): Promise<Result<T, FileError>> {
    try {
      return await op();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return err(new FileError("unknown", message));
    }
  }

  /** One short command's outcome, as the void FileSystem methods answer it. */
  private voidOf(out: ShortOutcome, path: string): Result<void, FileError> {
    return out.exitCode === 0 ? ok(undefined) : err(this.fileError(out.exitCode, out.output, path));
  }

  private fileError(exitCode: number, output: string, path: string): FileError {
    if (exitCode === ENOENT) return new FileError("not_found", "No such file or directory", path);
    if (exitCode === EISDIR) return new FileError("is_directory", "Is a directory", path);
    if (exitCode === ENOTDIR) return new FileError("not_directory", "Not a directory", path);
    if (/permission denied/i.test(output)) {
      return new FileError("permission_denied", output.trim().slice(0, 200), path);
    }
    return new FileError("unknown", `exit ${exitCode}: ${output.trim().slice(0, 200)}`, path);
  }

  // ------------------------------------------------------------- FileSystem ---

  absolutePath(path: string, _context: Context): Promise<Result<string, FileError>> {
    return Promise.resolve(ok(resolvePosix(this.cwd, path)));
  }

  joinPath(parts: string[], _context: Context): Promise<Result<string, FileError>> {
    return Promise.resolve(ok(joinPosix(parts)));
  }

  canonicalPath(path: string, _context: Context): Promise<Result<string, FileError>> {
    return this.attempt(() =>
      this.guardedRead(path, "exists", 'realpath "$P"', (out) => out.output.trim()),
    );
  }

  readTextFile(path: string, _context: Context): Promise<Result<string, FileError>> {
    return this.attempt(() => this.guardedRead(path, "file", 'cat "$P"', (out) => out.output));
  }

  readTextLines(
    path: string,
    options: { maxLines?: number } | undefined,
    _context: Context,
  ): Promise<Result<string[], FileError>> {
    return this.attempt(() =>
      this.guardedRead(path, "file", 'cat "$P"', (out) => {
        const lines = out.output.split("\n");
        const withoutTail = lines.at(-1) === "" ? lines.slice(0, -1) : lines;
        return options?.maxLines === undefined
          ? withoutTail
          : withoutTail.slice(0, options.maxLines);
      }),
    );
  }

  readBinaryFile(path: string, _context: Context): Promise<Result<Uint8Array, FileError>> {
    return this.attempt(() =>
      // Redirected, not an operand: BSD base64 reads no file arguments, and
      // the decode strips the line wraps GNU adds anyway (`-w 0` is not
      // portable either).
      this.guardedRead(path, "file", 'base64 < "$P"', (out) => base64Decode(out.output)),
    );
  }

  async openTextLineReader(
    path: string,
    _context: Context,
  ): Promise<Result<TextLineReader, FileError>> {
    const read = await this.readTextFile(path, BACKGROUND_CONTEXT);
    if (!read.ok) return read;
    // The whole file at once: the tool reads this package serves are small,
    // and a streaming reader would need container-side read state this env
    // does not have. The last split piece carries whether it ended a line.
    const lines = read.value.split("\n");
    const content = lines.slice(0, -1).map((text) => ({ text: `${text}\n`, terminated: true }));
    const tail = lines.at(-1);
    if (tail !== undefined && tail !== "") content.push({ text: tail, terminated: false });
    let index = 0;
    return ok({
      readLine: (_lineContext: Context) =>
        Promise.resolve(
          index < content.length
            ? ok(content[index++] as { text: string; terminated: boolean })
            : ok(undefined),
        ),
      close: (_closeContext: Context) => Promise.resolve(),
    });
  }

  writeFile(
    path: string,
    content: string | Uint8Array,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return this.attempt(() => this.withKindGuard(path, () => this.rawWrite(path, content, false)));
  }

  appendFile(
    path: string,
    content: string | Uint8Array,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return this.attempt(() => this.withKindGuard(path, () => this.rawWrite(path, content, true)));
  }

  truncateFile(path: string, size: number, _context: Context): Promise<Result<void, FileError>> {
    return this.attempt(() =>
      this.withKindGuard(path, async () => {
        const out = await this.short('truncate -s "$SIZE" "$F"', { F: path, SIZE: String(size) });
        return this.voidOf(out, path);
      }),
    );
  }

  flushFile(_path: string, _context: Context): Promise<Result<void, FileError>> {
    // Nothing to flush: the door's answer IS the container's durable state.
    return Promise.resolve(ok(undefined));
  }

  renameFile(
    sourcePath: string,
    destinationPath: string,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return this.attempt(() =>
      this.withKindGuard(sourcePath, async () => {
        const out = await this.short('mv "$S" "$D"', { S: sourcePath, D: destinationPath });
        return this.voidOf(out, sourcePath);
      }),
    );
  }

  fileInfo(path: string, _context: Context): Promise<Result<FileInfo, FileError>> {
    return this.attempt(() =>
      // GNU stat first (the image), BSD stat as the fallback (the node
      // suite's macOS host): the two agree on nothing except being stat.
      this.guardedRead(
        path,
        "exists",
        'stat -c "%F|%s|%Y" "$P" 2>/dev/null || stat -f "%HT|%z|%m" "$P"',
        (out) => {
          const fields = out.output.trim().split("\n")[0]?.split("|") ?? [];
          const kindText = (fields[0] ?? "").toLowerCase();
          const kind = kindText.includes("directory")
            ? "directory"
            : kindText.includes("symbolic")
              ? "symlink"
              : kindText.includes("regular")
                ? "file"
                : undefined;
          const size = Number(fields[1]);
          const mtime = Number(fields[2]);
          if (kind === undefined || !Number.isFinite(size) || !Number.isFinite(mtime)) {
            throw new Error(`stat answered oddly: ${out.output.trim().slice(0, 200)}`);
          }
          return {
            name: path.split("/").at(-1) ?? path,
            path,
            kind,
            size,
            mtimeMs: mtime * 1000,
          } satisfies FileInfo;
        },
      ),
    );
  }

  listDir(path: string, _context: Context): Promise<Result<FileInfo[], FileError>> {
    return this.attempt(() =>
      this.guardedRead(
        path,
        "dir",
        // One listing command, portable across the image's GNU userland and
        // the BSD one the node suite's macOS host runs: kinds by test (not
        // by find's `-printf`, which BSD find does not have), sizes and
        // mtimes by whichever stat answers. Built by join, not one string
        // with continuations: an escaped backslash before a raw newline is
        // an unterminated string, and this line is long enough to need two.
        LISTING_LINE,
        (out) => {
          const entries: FileInfo[] = [];
          for (const line of out.output.split("\n")) {
            if (line === "") continue;
            const fields = line.split("|");
            const kind =
              fields[0] === "f"
                ? "file"
                : fields[0] === "d"
                  ? "directory"
                  : fields[0] === "l"
                    ? "symlink"
                    : undefined;
            const name = fields[1];
            const size = Number(fields[2]);
            const mtime = Number(fields[3]);
            if (
              kind === undefined ||
              name === undefined ||
              !Number.isFinite(size) ||
              !Number.isFinite(mtime)
            ) {
              continue;
            }
            entries.push({
              name,
              path: `${path.replace(/\/+$/, "")}/${name}`,
              kind,
              size,
              mtimeMs: Math.round(mtime * 1000),
            });
          }
          return entries;
        },
      ),
    );
  }

  exists(path: string, _context: Context): Promise<Result<boolean, FileError>> {
    return this.attempt(async () => {
      const out = await this.short('test -e "$P"', { P: path });
      return ok(out.exitCode === 0);
    });
  }

  createDir(
    path: string,
    options: { recursive?: boolean } | undefined,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return this.attempt(async () => {
      const out = await this.short(options?.recursive === false ? 'mkdir "$P"' : 'mkdir -p "$P"', {
        P: path,
      });
      return this.voidOf(out, path);
    });
  }

  remove(
    path: string,
    options: { recursive?: boolean; force?: boolean } | undefined,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return this.attempt(async () => {
      const flags = options?.recursive === true ? "-rf" : "-f";
      const out = await this.short(
        options?.force === true
          ? `rm ${flags} "$P"`
          : `test -e "$P" || exit ${ENOENT}; rm ${flags} "$P"`,
        { P: path },
      );
      return this.voidOf(out, path);
    });
  }

  createTempDir(prefix: string | undefined, _context: Context): Promise<Result<string, FileError>> {
    return this.attempt(async () => {
      const out = await this.short('mktemp -d "$T"', { T: `${prefix || "/tmp/ticfac"}-XXXXXXXX` });
      return out.exitCode === 0
        ? ok(out.output.trim())
        : err(this.fileError(out.exitCode, out.output, prefix ?? ""));
    });
  }

  createTempFile(
    options: { prefix?: string; suffix?: string } | undefined,
    _context: Context,
  ): Promise<Result<string, FileError>> {
    return this.attempt(async () => {
      const out = await this.short(
        // The suffix is appended AFTER mktemp answers: BSD mktemp only
        // replaces TRAILING Xs, so the template cannot carry one.
        'f=$(mktemp "$T"); { [ -z "$S" ] || mv "$f" "$f$S"; } && printf "%s\\n" "$f$S"',
        {
          T: `/tmp/${options?.prefix ?? "ticfac"}-XXXXXXXX`,
          S: options?.suffix ?? "",
        },
      );
      return out.exitCode === 0
        ? ok(out.output.trim())
        : err(this.fileError(out.exitCode, out.output, options?.prefix ?? ""));
    });
  }

  cleanup(_context: Context): Promise<void> {
    // The container's lifetime belongs to the door's callers, not to one env.
    return Promise.resolve();
  }

  // ----------------------------------------------------------------- Shell ---

  /**
   * The tracked bash. A call carrying a nonce in `options.env` (the bash
   * tool's prepare hook puts it there, durably memoised per tool call) is
   * REPLAY-SAFE: on replay the env finds the process by nonce through
   * `listProcesses` and reattaches — reading on from the cursor — instead of
   * starting the command again. A call without one is a plain, untracked
   * command: used by the host, never by the model.
   */
  exec(
    command: string,
    options: ShellExecOptions | undefined,
    context: Context,
  ): Promise<Result<ShellExecResult, ExecutionError>> {
    return this.execAttempt(command, options, context).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : String(error);
      return err(new ExecutionError("unknown", message));
    });
  }

  private async execAttempt(
    command: string,
    options: ShellExecOptions | undefined,
    context: Context,
  ): Promise<Result<ShellExecResult, ExecutionError>> {
    const signal = context.abortSignal;
    if (signal?.aborted) return err(new ExecutionError("aborted", "aborted"));
    const timeoutMs = options?.timeout === undefined ? undefined : options.timeout * 1000;
    if (timeoutMs !== undefined && (!Number.isFinite(timeoutMs) || timeoutMs <= 0)) {
      return err(
        new ExecutionError("timeout", "Invalid timeout: must be a finite number of seconds"),
      );
    }
    await this.ensureGuard();

    const nonce = options?.env?.[BASH_NONCE_VAR];
    const cwd = options?.cwd === undefined || options.cwd === "" ? this.cwd : options.cwd;
    const vars: Record<string, string> = { ...options?.env, TICFAC_CWD: cwd };
    const line = `${this.withGuard("")}cd "$TICFAC_CWD" 2>/dev/null || exit 1\n${command}`;

    let id: string;
    if (nonce === undefined) {
      id = (await this.door.startProcess(line, vars, { keepAlive: true })).id;
    } else {
      // REPLAY-SAFE: find this nonce's process first; only start when there
      // is none. A replay that finds one does not run the command again —
      // whether it is still running (reattach) or already finished (its exit
      // code is the answer).
      const marker = bashNonceMarker(nonce);
      const found = (await this.door.listProcesses()).find((view) =>
        view.command?.includes(marker),
      )?.id;
      if (found !== undefined) {
        id = found;
      } else {
        // No process for this nonce: either the command never started, or
        // the CONTAINER it ran in is gone and the fresh one booted empty.
        // Tell the two apart before starting — a replay that starts on an
        // empty workspace would silently lose everything since the last wip
        // (epic 43y step 4). One short command, on this rare path only.
        await this.ensureWorkspaceRestored();
        id = (await this.door.startProcess(`${marker}\n${line}`, vars, { keepAlive: true })).id;
      }
    }

    const deadline = timeoutMs === undefined ? Number.POSITIVE_INFINITY : this.now() + timeoutMs;
    let cursor = 0;
    while (true) {
      const view = await this.door.getProcess(id);
      if (view === null) {
        return this.containerLost("the container no longer knows this process");
      }
      const chunk: SandboxOutput = await this.door.readOutput(id, cursor);
      if (chunk.text !== "" && options?.onOutput !== undefined) {
        options.onOutput(chunk.text, context);
      }
      cursor = chunk.offset;
      if (view.state !== "running") {
        if (view.exit_code === null) {
          return this.containerLost("the process was lost without an exit code");
        }
        return ok({ exitCode: view.exit_code });
      }
      if (signal?.aborted) {
        // NOT killed: a replay must find this process and reattach to it.
        return err(new ExecutionError("aborted", "aborted"));
      }
      if (this.now() >= deadline) {
        await this.door.killProcess(id);
        return err(new ExecutionError("timeout", "command timed out"));
      }
      const slept = await this.nap(this.pollMs, signal);
      if (slept === "aborted") return err(new ExecutionError("aborted", "aborted"));
    }
  }

  /** Sleeps `ms`, waking early when `signal` aborts. */
  private nap(ms: number, signal: AbortSignal | undefined): Promise<"slept" | "aborted"> {
    if (signal?.aborted) return Promise.resolve("aborted");
    return new Promise((resolve) => {
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve("slept");
      }, ms);
      const onAbort = () => {
        clearTimeout(timer);
        resolve("aborted");
      };
      signal?.addEventListener("abort", onAbort, { once: true });
    });
  }

  // ------------------------------------------- workspace restore (step 4) ---

  /**
   * Whether a replay's fresh start is on a live workspace: a container
   * destroyed while no harness watched boots EMPTY, and a re-started
   * command would run on nothing. One short command; restore only when the
   * workspace is missing. Throws the restore's failure — the replay must
   * not start on a box it could not rebuild.
   */
  private async ensureWorkspaceRestored(): Promise<void> {
    const ready = await this.ensureWorkspaceReady();
    if (ready.kind === "failed") {
      throw new Error(`the workspace could not be restored: ${ready.error}`);
    }
  }

  /**
   * The host-side ready check before a round (epic 43y, tick 4fs): a
   * container destroyed BETWEEN tool rounds — no harness call in flight, so
   * none of the env's loss signals can fire — boots empty, and the next
   * round's first file operation would fail plainly (ENOENT) instead of
   * restoring. One short command verifies the ready marker; a missing one
   * restores the workspace from the attempt branch
   * ({@link restoreLostWorkspace}). Public: the host wires it into the
   * checkpoint extension's `ensureReady`, before every round's request, and
   * the WorkerAgent host (epic step 6) may call it ahead of a round it knows
   * the box under.
   */
  async ensureWorkspaceReady(): Promise<WorkspaceReadyOutcome> {
    if (this.workspace === null) return { kind: "ready" };
    const check = await this.short('test -e "$CWD/.git"', { CWD: this.cwd });
    if (check.exitCode === 0) return { kind: "ready" };
    return this.restoreLostWorkspace();
  }

  /**
   * Restores this attempt's workspace into the container the door boots
   * next — the fresh one after a loss boots EMPTY: clear it, clone the
   * attempt branch, check out its tip (the last wip commit, the round's
   * checkpoint) and run the setup command
   * (./workspace/checkpoints.ts). One restore at a time per env; a failed
   * one is retried by the next loss. Public: the WorkerAgent host (epic
   * step 6), which owns the container's lifetime, may restore ahead of a
   * round it knows the box under.
   */
  async restoreLostWorkspace(): Promise<RestoreOutcome> {
    const git = this.workspace;
    if (git === null) {
      return {
        kind: "failed",
        error: "this env carries no workspace git, so the workspace cannot be restored",
      };
    }
    // The guard directory died with the container; the next command into the
    // fresh box reinstalls it.
    this.guardInstalling = undefined;
    // The memo collapses only CONCURRENT callers — a mid-command loss racing
    // a pre-round check on the same fresh box. Once it settles the memo
    // clears, so the NEXT loss restores again rather than answering the
    // last one's outcome.
    this.restoring ??= restoreWorkspace(this.hostShell(), git).then(
      (outcome) => {
        this.restoring = undefined;
        return outcome;
      },
      (error: unknown) => {
        this.restoring = undefined;
        throw error;
      },
    );
    return this.restoring;
  }

  /**
   * The container was lost mid-command (its process is gone, or ended with
   * no exit code): restore the workspace from the last wip commit and hand
   * the model the message the prototype's experiment 4 proved — restored
   * to N, re-check and re-run. The model re-reads the files and re-runs the
   * command on the restored tree; edits since the last tool round are the
   * only loss.
   */
  private async containerLost(what: string): Promise<Result<ShellExecResult, ExecutionError>> {
    if (this.workspace === null) {
      return err(
        new ExecutionError(
          "unknown",
          `the container was lost mid-command (${what}) and this env carries no workspace git to restore from`,
        ),
      );
    }
    const restore = await this.restoreLostWorkspace();
    if (restore.kind === "failed") {
      return err(
        new ExecutionError(
          "unknown",
          `the container was lost mid-command (${what}) and the workspace restore failed: ${restore.error}`,
        ),
      );
    }
    return err(
      new ExecutionError(
        "unknown",
        `the container was lost mid-command (${what}); the workspace was restored to ` +
          `${restore.sha.slice(0, 12)} (${restore.subject}) — ` +
          "re-check the files you were working on and re-run what you were doing",
      ),
    );
  }

  // --------------------------------------------------------------- helpers ---

  /**
   * Runs `line` on `path` behind the target guards, mapping the outcome
   * through `pick`. One RPC per read; the exit-code sentinels keep failure
   * classification out of a second round trip.
   */
  private async guardedRead<T>(
    path: string,
    guard: TargetGuard,
    line: string,
    pick: (out: ShortOutcome) => T,
  ): Promise<Result<T, FileError>> {
    const guards =
      `if [ ! -e "$P" ]; then exit ${ENOENT}; fi; ` +
      (guard === "file"
        ? `if [ -d "$P" ]; then exit ${EISDIR}; fi; `
        : guard === "dir"
          ? `if [ ! -d "$P" ]; then exit ${ENOTDIR}; fi; `
          : "") +
      line;
    const out = await this.short(guards, { P: path }, READ_MAX_BYTES);
    if (out.exitCode !== 0) return err(this.fileError(out.exitCode, out.output, path));
    if (out.truncated) {
      return err(
        new FileError(
          "unknown",
          `the answer was truncated at ${READ_MAX_BYTES} bytes — too large for one read`,
          path,
        ),
      );
    }
    return ok(pick(out));
  }

  /** A write's guard: the target must not be a directory. */
  private async withKindGuard(
    path: string,
    write: () => Promise<Result<void, FileError>>,
  ): Promise<Result<void, FileError>> {
    const kind = await this.short(
      `if [ -e "$P" ] && [ -d "$P" ]; then exit ${EISDIR}; fi; exit 0`,
      { P: path },
    );
    if (kind.exitCode === EISDIR) {
      return err(new FileError("is_directory", "Is a directory", path));
    }
    return write();
  }
}

// ---------------------------------------------------------------- posix ---

/** Resolves `path` against `cwd`, POSIX-style, without touching a filesystem. */
function resolvePosix(cwd: string, path: string): string {
  if (path === "") return cwd;
  const leading = path.startsWith("/");
  const base = leading ? path : `${cwd}/${path}`;
  const parts: string[] = [];
  for (const piece of base.split("/")) {
    if (piece === "" || piece === ".") continue;
    if (piece === "..") parts.pop();
    else parts.push(piece);
  }
  return `/${parts.join("/")}`;
}

/** Joins parts the way `node:path.join` would, POSIX-style. */
function joinPosix(parts: string[]): string {
  const joined = parts.join("/");
  const leading = joined.startsWith("/");
  const out: string[] = [];
  for (const piece of joined.split("/")) {
    if (piece === "" || piece === ".") continue;
    if (piece === "..") out.pop();
    else out.push(piece);
  }
  return (leading ? "/" : "") + out.join("/");
}
