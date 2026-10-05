/**
 * The stuck watch's container CPU sampler against REAL bash (epic 43y,
 * tick xba): the sampler line runs inside the attempt's container, but its
 * correctness is shell behaviour — count the `/proc/<pid>/stat` fields past
 * the LAST `)`, and spell the positional parameters with their braces —
 * and a shell behaviour test that never runs a shell certifies nothing
 * (the same reason this suite exists, per its own config).
 *
 * What stands in: the `/proc`-shaped tree, synthesized per test. What is
 * real: bash, the glob, the read, the field arithmetic — the exact line the
 * WorkerAgent's stuck watch sends through the door.
 */
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { describe, expect, it } from "vitest";
import { containerCpuCommand, parseContainerCpuMs } from "../../src/host/stuck-watch.js";

const execFileAsync = promisify(execFile);

/** One `/proc`-shaped tree: <pid>/stat rows, the kernel's own layout. */
async function procTree(rows: Record<string, string>): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), "ticfac-proc-"));
  for (const [pid, stat] of Object.entries(rows)) {
    await mkdir(join(root, pid), { recursive: true });
    await writeFile(join(root, pid, "stat"), `${stat}\n`);
  }
  return root;
}

/** The sampler's answer over one tree, as the watch reads it (milliseconds). */
async function sampledMs(root: string): Promise<number | null> {
  const out = await execFileAsync("bash", ["-c", containerCpuCommand(root)]);
  return parseContainerCpuMs(out.stdout);
}

describe("the stuck watch's container CPU sampler", () => {
  it("sums utime+stime of every process, counting from the last ')' of each stat", {
    timeout: 300_000,
  }, async () => {
    // The second command name carries a `)` and spaces of its own — the
    // reason the fields are counted past the LAST `)`, never the first.
    const root = await procTree({
      "1": "1 (systemd) S 0 1 1 0 -1 4194560 0 0 0 0 130 45 0 0 20 0 1 0",
      "22": "22 (my cmd with ) and spaces) S 0 1 1 0 -1 4194560 0 0 0 0 900 60 0 0 20 0 1 0",
    });
    try {
      // 130+45 + 900+60 ticks at 100 ticks a second, and the parse answers
      // milliseconds.
      expect(await sampledMs(root)).toBe(11_350);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("answers zero for a root with no process table, and the parse refuses junk", {
    timeout: 300_000,
  }, async () => {
    const root = await mkdtemp(join(tmpdir(), "ticfac-proc-empty-"));
    try {
      const out = await execFileAsync("bash", ["-c", containerCpuCommand(root)]);
      // No stat matched the glob: the sampler still answers a number, and
      // the watch reads it as a baseline, never as an error.
      expect(parseContainerCpuMs(out.stdout)).toBe(0);
      // Junk answers null: a signal that could not be read is named in the
      // evidence, never decided on.
      expect(parseContainerCpuMs("bash: /proc/1/stat: No such file\n")).toBeNull();
      expect(parseContainerCpuMs("")).toBeNull();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});
