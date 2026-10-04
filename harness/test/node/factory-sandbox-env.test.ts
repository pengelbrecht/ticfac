import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import { localSandboxDoor } from "./local-sandbox-door.js";

/**
 * FactorySandboxEnv's FILE OPERATIONS against real bash: the workerd suite
 * proves the env's RPC shapes over a scripted door, and this proves what the
 * command lines it builds actually do — `cat` really reads, base64 really
 * survives bytes, `find` really lists, and the failure sentinels really
 * classify — over the local stand-in door (./local-sandbox-door.ts).
 *
 * A git repository, so the guard's install finds the same "this checkout"
 * the container's would, and a stand-in tk on PATH for the guard tests that
 * live in ./boundary-guard.test.ts.
 */

const CONTEXT = BACKGROUND_CONTEXT;

describe("FactorySandboxEnv's file operations over real bash", () => {
  let root: string;
  let checkout: string;
  let env: FactorySandboxEnv;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ticfac-fsenv-"));
    checkout = join(root, "worktree");
    mkdirSync(checkout, { recursive: true });
    execFileSync("git", ["init", "-q", checkout]);
    const door = localSandboxDoor({ cwd: checkout });
    env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard"),
      pollMs: 10,
    });
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  it("writes and reads text back, with a path that is a value, not shell text", async () => {
    const odd = 'notes "quoted" name.txt';
    const content = "first line\nsecond line with ünicode 🚀\n";
    const wrote = await env.writeFile(join(checkout, odd), content, CONTEXT);
    expect(wrote.ok).toBe(true);
    const read = await env.readTextFile(join(checkout, odd), CONTEXT);
    expect(read.ok ? read.value : read.error).toBe(content);
    // Relative to the working directory, too.
    const relative = await env.readTextFile(odd, CONTEXT);
    expect(relative.ok && relative.value).toBe(content);
  });

  it("round-trips binary bytes untouched", async () => {
    const bytes = new Uint8Array([0, 1, 2, 255, 254, 0, 10, 13, 26, 200, 0, 0]);
    const path = join(checkout, "blob.bin");
    await env.writeFile(path, bytes, CONTEXT);
    const read = await env.readBinaryFile(path, CONTEXT);
    expect(read.ok).toBe(true);
    if (!read.ok) return;
    expect(Array.from(read.value)).toEqual(Array.from(bytes));
  });

  it("appends, renames, truncates and removes", async () => {
    const path = join(checkout, "a.txt");
    await env.writeFile(path, "one\n", CONTEXT);
    expect((await env.appendFile(path, "two\n", CONTEXT)).ok).toBe(true);
    expect((await env.renameFile(path, join(checkout, "b.txt"), CONTEXT)).ok).toBe(true);
    expect((await env.truncateFile(join(checkout, "b.txt"), 3, CONTEXT)).ok).toBe(true);
    const read = await env.readTextFile(join(checkout, "b.txt"), CONTEXT);
    expect(read.ok ? read.value : read.error).toBe("one");
    expect((await env.remove(join(checkout, "b.txt"), {}, CONTEXT)).ok).toBe(true);
    const gone = await env.exists(join(checkout, "b.txt"), CONTEXT);
    expect(gone.ok ? gone.value : "err").toBe(false);
  });

  it("lists a directory with kinds and dotfiles, and stats a file", async () => {
    mkdirSync(join(checkout, "sub"));
    writeFileSync(join(checkout, "x.md"), "hello\n");
    writeFileSync(join(checkout, ".hidden"), "x");
    mkdirSync(join(checkout, "nested"));

    const list = await env.listDir(join(checkout, "sub"), CONTEXT);
    // A fresh subdirectory lists empty.
    expect(list.ok ? JSON.stringify(list.value) : list.error).toBe("[]");

    const all = await env.listDir(checkout, CONTEXT);
    expect(all.ok).toBe(true);
    if (!all.ok) return;
    const names = all.value.map((e) => e.name).sort();
    expect(names).toEqual([".git", ".hidden", "nested", "sub", "x.md"]);
    const kinds = new Map(all.value.map((e) => [e.name, e.kind]));
    expect(kinds.get("x.md")).toBe("file");
    expect(kinds.get("nested")).toBe("directory");
    expect(kinds.get(".hidden")).toBe("file");

    const info = await env.fileInfo(join(checkout, "x.md"), CONTEXT);
    expect(info.ok).toBe(true);
    if (!info.ok) return;
    expect(info.value.name).toBe("x.md");
    expect(info.value.kind).toBe("file");
    expect(info.value.size).toBe(6);
    expect(info.value.mtimeMs).toBeGreaterThan(0);
  });

  it("reads lines and answers the failure sentinels the tools classify by", async () => {
    writeFileSync(join(checkout, "lines.txt"), "a\nb\nc");
    const lines = await env.readTextLines(join(checkout, "lines.txt"), { maxLines: 2 }, CONTEXT);
    expect(lines.ok ? lines.value : lines.error).toEqual(["a", "b"]);

    const missing = await env.readTextFile(join(checkout, "nope.txt"), CONTEXT);
    expect(missing.ok).toBe(false);
    if (missing.ok) return;
    expect(missing.error.code).toBe("not_found");

    const dirRead = await env.readTextFile(checkout, CONTEXT);
    expect(dirRead.ok ? "ok" : dirRead.error.code).toBe("is_directory");

    const dirList = await env.listDir(join(checkout, "lines.txt"), CONTEXT);
    expect(dirList.ok ? "ok" : dirList.error.code).toBe("not_directory");

    const wrote = await env.writeFile(checkout, "x", CONTEXT);
    expect(wrote.ok ? "ok" : wrote.error.code).toBe("is_directory");

    // canonicalPath resolves files as well as directories (realpath, the
    // same command `stat`'s world comes from) — through symlinks the host
    // may put in the way (/var → /private/var on macOS).
    const canonical = await env.canonicalPath(join(checkout, "lines.txt"), CONTEXT);
    expect(canonical.ok && canonical.value.endsWith("lines.txt")).toBe(true);
    const canonicalDir = await env.canonicalPath(join(checkout, "nested/.."), CONTEXT);
    expect(canonicalDir.ok && canonicalDir.value.endsWith("worktree")).toBe(true);
  });

  it("creates directories, recursively and not, and makes temp files", async () => {
    expect((await env.createDir(join(checkout, "a/b"), {}, CONTEXT)).ok).toBe(true);
    const shallow = await env.createDir(join(checkout, "c/d"), { recursive: false }, CONTEXT);
    expect(shallow.ok ? "ok" : shallow.error.code).toBe("unknown");

    const temp = await env.createTempDir(undefined, CONTEXT);
    expect(temp.ok).toBe(true);
    const tempFile = await env.createTempFile({ prefix: "report", suffix: ".md" }, CONTEXT);
    expect(tempFile.ok && tempFile.value.endsWith(".md")).toBe(true);

    const abs = await env.absolutePath("relative/x.txt", CONTEXT);
    expect(abs.ok ? abs.value : abs.error).toBe(`${checkout}/relative/x.txt`);
    const joined = await env.joinPath([checkout, "sub", "file.txt"], CONTEXT);
    expect(joined.ok ? joined.value : joined.error).toBe(`${checkout}/sub/file.txt`);
  });
});
