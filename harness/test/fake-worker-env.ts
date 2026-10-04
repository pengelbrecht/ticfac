/**
 * A scripted ExecutionEnv for the worker-contract tests (epic 43y, tick pom):
 * an in-memory file tree, a shell whose every command is answered by a
 * script the test hands it, and everything else a structural stub.
 *
 * It is a FAKE in the sense the learnings demand ("a fake demands the
 * identity and reproduces the real shape"): `exec` records the exact command
 * line it was handed — so the tests assert the checker was asked with the
 * same argv worker.sh composes — and the report the hooks look for is the
 * same `RESULT-<tick>.md` path the real contract reads, in the fake's own
 * cwd-relative namespace.
 */

import type { Context } from "@earendil-works/chord";
import {
  type ExecutionEnv,
  type ExecutionError,
  FileError,
  type FileInfo,
  type Result,
  type ShellExecOptions,
  type ShellExecResult,
  type TextLine,
  type TextLineReader,
} from "@earendil-works/pi-durable/env";

export type FakeExecAnswer = { exitCode: number; output: string };

/** What one scripted `exec` was asked, in order. */
export type FakeExecCall = { command: string; options: ShellExecOptions | undefined };

export type FakeWorkerEnvOptions = {
  /** The checkout's directory — the paths the contract joins onto it are fake too. */
  readonly cwd: string;
  /** Answers each `exec` in order; the last answer serves every call after it. */
  readonly execScript: readonly FakeExecAnswer[];
};

const notFound = (path: string): FileError =>
  new FileError("not_found", `no such file: ${path}`, path);
const unsupported = (): FileError => new FileError("not_supported", "not implemented");

export class FakeWorkerEnv implements ExecutionEnv {
  readonly id = "fake-worker";
  cwd: string;
  readonly files = new Map<string, string>();
  readonly execCalls: FakeExecCall[] = [];
  private readonly execScript: readonly FakeExecAnswer[];
  private execIndex = 0;

  constructor(options: FakeWorkerEnvOptions) {
    this.cwd = options.cwd;
    this.execScript = options.execScript;
  }

  /** The test's stand-in agent: write a file into the env. */
  writeFileDirect(path: string, content: string): void {
    this.files.set(this.resolve(path), content);
  }

  private resolve(path: string): string {
    if (path.startsWith("/")) {
      return path;
    }
    return `${this.cwd.replace(/\/$/, "")}/${path}`;
  }

  async absolutePath(path: string, _context: Context): Promise<Result<string, FileError>> {
    return { ok: true, value: this.resolve(path) };
  }

  async joinPath(parts: string[], _context: Context): Promise<Result<string, FileError>> {
    return { ok: true, value: parts.join("/") };
  }

  async readTextFile(path: string, _context: Context): Promise<Result<string, FileError>> {
    const body = this.files.get(this.resolve(path));
    return body === undefined ? { ok: false, error: notFound(path) } : { ok: true, value: body };
  }

  async openTextLineReader(
    _path: string,
    _context: Context,
  ): Promise<Result<TextLineReader, FileError>> {
    return { ok: false, error: unsupported() };
  }

  async readTextLines(
    path: string,
    _options: { maxLines?: number } | undefined,
    context: Context,
  ): Promise<Result<string[], FileError>> {
    const body = await this.readTextFile(path, context);
    return body.ok ? { ok: true, value: body.value.split("\n") } : body;
  }

  async readBinaryFile(_path: string, _context: Context): Promise<Result<Uint8Array, FileError>> {
    return { ok: false, error: unsupported() };
  }

  async writeFile(
    path: string,
    content: string | Uint8Array,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    this.files.set(
      this.resolve(path),
      typeof content === "string" ? content : new TextDecoder().decode(content),
    );
    return { ok: true, value: undefined };
  }

  async appendFile(
    path: string,
    content: string | Uint8Array,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    const current = this.files.get(this.resolve(path)) ?? "";
    const text = typeof content === "string" ? content : new TextDecoder().decode(content);
    this.files.set(this.resolve(path), current + text);
    return { ok: true, value: undefined };
  }

  async truncateFile(
    _path: string,
    _size: number,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return { ok: true, value: undefined };
  }

  async flushFile(_path: string, _context: Context): Promise<Result<void, FileError>> {
    return { ok: true, value: undefined };
  }

  async renameFile(
    sourcePath: string,
    destinationPath: string,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    const body = this.files.get(this.resolve(sourcePath));
    if (body === undefined) {
      return { ok: false, error: notFound(sourcePath) };
    }
    this.files.set(this.resolve(destinationPath), body);
    return { ok: true, value: undefined };
  }

  async fileInfo(path: string, _context: Context): Promise<Result<FileInfo, FileError>> {
    if (!this.files.has(this.resolve(path))) {
      return { ok: false, error: notFound(path) };
    }
    return {
      ok: true,
      value: {
        name: path,
        path: this.resolve(path),
        kind: "file",
        size: 0,
        mtimeMs: 0,
      },
    };
  }

  async listDir(_path: string, _context: Context): Promise<Result<FileInfo[], FileError>> {
    return { ok: true, value: [] };
  }

  async canonicalPath(path: string, _context: Context): Promise<Result<string, FileError>> {
    return { ok: true, value: this.resolve(path) };
  }

  async exists(path: string, _context: Context): Promise<Result<boolean, FileError>> {
    return { ok: true, value: this.files.has(this.resolve(path)) };
  }

  async createDir(
    _path: string,
    _options: { recursive?: boolean } | undefined,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return { ok: true, value: undefined };
  }

  async remove(
    _path: string,
    _options: { recursive?: boolean; force?: boolean } | undefined,
    _context: Context,
  ): Promise<Result<void, FileError>> {
    return { ok: true, value: undefined };
  }

  async createTempDir(
    _prefix: string | undefined,
    _context: Context,
  ): Promise<Result<string, FileError>> {
    return { ok: true, value: "/tmp/fake" };
  }

  async createTempFile(
    _options: { prefix?: string; suffix?: string } | undefined,
    _context: Context,
  ): Promise<Result<string, FileError>> {
    return { ok: true, value: "/tmp/fake-file" };
  }

  async cleanup(_context: Context): Promise<void> {}

  async exec(
    command: string,
    options: ShellExecOptions | undefined,
    context: Context,
  ): Promise<Result<ShellExecResult, ExecutionError>> {
    this.execCalls.push({ command, options });
    const answer = this.execScript[Math.min(this.execIndex, this.execScript.length - 1)];
    this.execIndex += 1;
    if (options?.onOutput) {
      await options.onOutput(answer.output, context);
    }
    return { ok: true, value: { exitCode: answer.exitCode } };
  }
}
