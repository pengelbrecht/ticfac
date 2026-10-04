/**
 * The local worker's environment: pi-durable's own `NodeExecutionEnv`, with
 * the same boundary guard the FactorySandbox env carries (epic 43y step 3).
 *
 * This file is the package's one NODE-ONLY entry (hence the separate
 * `./env/node` export, mirroring pi-durable's own split): it imports
 * `node:fs` transitively and may not be pulled into the workerd bundle. The
 * local host (epic step 7) constructs it per worker; the node test suite
 * proves the guard behaves.
 *
 * The guard is the same shim, the same pass list and the same refusal
 * (./boundary-guard.ts): every command the env runs resolves `tk` through
 * it, so a local worker's agent meets the same boundary a container's does
 * — the boundary is a property of the WORK, not of the substrate it runs on.
 */

import type { Context } from "@earendil-works/chord";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
  type ExecutionEnv,
  type ExecutionError,
  err,
  type FileInfo,
  type Result,
  type ShellExecOptions,
  type ShellExecResult,
  type TextLineReader,
} from "@earendil-works/pi-durable/env";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import {
  defaultGuardDir,
  type GuardInstallBase,
  guardPathPrefix,
  installBoundaryGuard,
} from "./boundary-guard.js";

export type GuardedNodeEnvOptions = {
  readonly cwd: string;
  readonly shellPath?: string;
  readonly shellEnv?: Record<string, string>;
  /** The guard directory; default a sibling of the cwd. `null` disables the guard. */
  readonly guardDir?: string | null;
};

/**
 * A `NodeExecutionEnv` whose every `exec` runs behind the boundary guard:
 * the guard's directory is installed on first use (the shim, its
 * `real-tk`/`checkout` companions, and the ledger), and each command is
 * prefixed with the PATH export that puts the shim's `tk` first.
 */
export class GuardedNodeExecutionEnv implements ExecutionEnv {
  readonly id: string;
  cwd: string;

  private readonly base: NodeExecutionEnv;
  private readonly guardDir: string | null;
  private installing: Promise<void> | undefined;

  constructor(options: GuardedNodeEnvOptions) {
    this.base = new NodeExecutionEnv({
      cwd: options.cwd,
      shellPath: options.shellPath,
      shellEnv: options.shellEnv,
    });
    this.cwd = options.cwd;
    this.id = this.base.id;
    this.guardDir =
      options.guardDir === undefined ? defaultGuardDir(options.cwd) : options.guardDir;
  }

  async exec(
    command: string,
    options: ShellExecOptions | undefined,
    context: Context,
  ): Promise<Result<ShellExecResult, ExecutionError>> {
    if (this.guardDir === null) return this.base.exec(command, options, context);
    try {
      await this.ensureInstalled();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      return err({ code: "unknown", message } as ExecutionError);
    }
    return this.base.exec(guardPathPrefix(this.guardDir) + command, options, context);
  }

  private ensureInstalled(): Promise<void> {
    this.installing ??= installBoundaryGuard(this.installBase(), {
      dir: this.guardDir as string,
      context: BACKGROUND_CONTEXT,
    }).catch((error: unknown) => {
      this.installing = undefined;
      throw error;
    });
    return this.installing;
  }

  /** The installer's view of the base env: short commands and whole-file writes. */
  private installBase(): GuardInstallBase {
    return {
      cwd: this.base.cwd,
      short: async (command, vars) => {
        let output = "";
        const result = await this.base.exec(
          command,
          { env: vars, onOutput: (text) => (output += text) },
          BACKGROUND_CONTEXT,
        );
        return { exitCode: result.ok ? result.value.exitCode : 1, output };
      },
      writeTextFile: async (path, content) => {
        const wrote = await this.base.writeFile(path, content, BACKGROUND_CONTEXT);
        if (!wrote.ok) throw new Error(`could not write ${path}: ${wrote.error.message}`);
      },
    };
  }

  // ------------------------------------------------------------ delegation ---

  absolutePath(path: string, context: Context) {
    return this.base.absolutePath(path, context);
  }
  joinPath(parts: string[], context: Context) {
    return this.base.joinPath(parts, context);
  }
  canonicalPath(path: string, context: Context) {
    return this.base.canonicalPath(path, context);
  }
  readTextFile(path: string, context: Context) {
    return this.base.readTextFile(path, context);
  }
  readTextLines(path: string, options: { maxLines?: number } | undefined, context: Context) {
    return this.base.readTextLines(path, options, context);
  }
  readBinaryFile(path: string, context: Context) {
    return this.base.readBinaryFile(path, context);
  }
  openTextLineReader(path: string, context: Context): Promise<Result<TextLineReader, never>> {
    return this.base.openTextLineReader(path, context) as Promise<Result<TextLineReader, never>>;
  }
  writeFile(path: string, content: string | Uint8Array, context: Context) {
    return this.base.writeFile(path, content, context);
  }
  appendFile(path: string, content: string | Uint8Array, context: Context) {
    return this.base.appendFile(path, content, context);
  }
  truncateFile(path: string, size: number, context: Context) {
    return this.base.truncateFile(path, size, context);
  }
  flushFile(path: string, context: Context) {
    return this.base.flushFile(path, context);
  }
  renameFile(sourcePath: string, destinationPath: string, context: Context) {
    return this.base.renameFile(sourcePath, destinationPath, context);
  }
  fileInfo(path: string, context: Context): Promise<Result<FileInfo, never>> {
    return this.base.fileInfo(path, context) as Promise<Result<FileInfo, never>>;
  }
  listDir(path: string, context: Context) {
    return this.base.listDir(path, context);
  }
  exists(path: string, context: Context) {
    return this.base.exists(path, context);
  }
  createDir(path: string, options: { recursive?: boolean } | undefined, context: Context) {
    return this.base.createDir(path, options, context);
  }
  remove(
    path: string,
    options: { recursive?: boolean; force?: boolean } | undefined,
    context: Context,
  ) {
    return this.base.remove(path, options, context);
  }
  createTempDir(prefix: string | undefined, context: Context) {
    return this.base.createTempDir(prefix, context);
  }
  createTempFile(options: { prefix?: string; suffix?: string } | undefined, context: Context) {
    return this.base.createTempFile(options, context);
  }
  cleanup(context: Context): Promise<void> {
    return this.base.cleanup(context);
  }
}

/** The local worker's env: pi-durable's NodeExecutionEnv behind the boundary guard. */
export function createGuardedNodeExecutionEnv(options: GuardedNodeEnvOptions): ExecutionEnv {
  return new GuardedNodeExecutionEnv(options);
}
