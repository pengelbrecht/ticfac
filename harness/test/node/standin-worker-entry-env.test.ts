import { execFile } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterEach, describe, expect, it } from "vitest";

/**
 * The staging stand-in ticks-worker (cloudflare/staging/agent.Dockerfile)
 * against the restore's ENV contract. The host's restoreEnv
 * (src/host/worker-attempt.ts) deliberately carries NONE of the boot's env
 * to a restored box — "the model's credentials in particular never reach a
 * restore" — and the production setup entry takes none of the boot's inputs
 * either (image/worker.sh run_setup_entry: "It takes NONE of the boot's
 * inputs… no TICKS_TICK to require and no clone to make"). The stand-in
 * must hold the same contract, because the [A2] whole-container destroy
 * (proof/agent-faults-staging.ts, scenario 2) is exactly the client that
 * runs `--setup` in a box the restore rebuilt.
 *
 * This is the node suite because it certifies SHELL behaviour: real bash,
 * real processes, the same discipline as boundary-guard.test.ts. The first
 * whole-container staging destroy (tick jpy) died on it: the replacement
 * box's `--setup` refused at source time on TICKS_REPO_URL — "the container
 * was lost between rounds and the restore failed: the setup command: exit
 * 1: ticks-worker: line 11: TICKS_REPO_URL unset" — while the restore's own
 * git plumbing had already fetched and checked out.
 */

const execFileAsync = promisify(execFile);

/** The stand-in's script, out of the Dockerfile heredoc. */
const standinScript = (() => {
  const dockerfile = readFileSync(
    new URL("../../../cloudflare/staging/agent.Dockerfile", import.meta.url),
    "utf8",
  );
  const lines = dockerfile.split("\n");
  const begin = lines.indexOf("COPY <<'WORKER' /usr/local/bin/ticks-worker");
  expect(begin).toBeGreaterThanOrEqual(0); // the heredoc this suite pins
  const end = lines.indexOf("WORKER", begin + 1);
  expect(end).toBeGreaterThan(begin);
  return lines.slice(begin + 1, end).join("\n");
})();

/** Where `case "${1:-}" in` starts: everything before it is source time. */
const sourceTime = (() => {
  const script = standinScript.split("\n");
  const caseAt = script.findIndex((line) => /^case "\$\{1:-\}" in$/.test(line));
  expect(caseAt).toBeGreaterThanOrEqual(0);
  return script.slice(0, caseAt).join("\n");
})();

/** One entry's case body, from its `--<entry>)` line to the next case line. */
const entryBody = (entry: string): string => {
  const script = standinScript.split("\n");
  const at = script.findIndex((line) => line.trim() === `--${entry})`);
  expect(at).toBeGreaterThanOrEqual(0);
  let end = script.length;
  for (let i = at + 1; i < script.length; i += 1) {
    if (/^\s*--\w+\)/.test(script[i])) {
      end = i;
      break;
    }
  }
  return script.slice(at, end).join("\n");
};

const homes: string[] = [];
afterEach(() => {
  for (const home of homes.splice(0)) rmSync(home, { recursive: true, force: true });
});

/**
 * Runs bash on a script, in the env a RESTORED box has: a PATH, a HOME the
 * git config lines may write, and none of the boot's inputs — no
 * TICKS_REPO_URL above all.
 */
async function runInRestoreEnv(
  script: string,
  args: string[],
): Promise<{ code: number; output: string; file: string }> {
  const home = mkdtempSync(join(tmpdir(), "standin-entry-"));
  homes.push(home);
  const file = join(home, "ticks-worker");
  writeFileSync(file, script);
  try {
    const result = await execFileAsync("bash", [file, ...args], {
      env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: home },
      encoding: "utf8",
    });
    // execFile resolves only on exit 0; any other exit lands in the catch.
    return { code: 0, output: `${result.stdout}${result.stderr}`, file };
  } catch (error) {
    const failure = error as { code?: number; stdout?: string; stderr?: string };
    return {
      code: failure.code ?? -1,
      output: `${failure.stdout ?? ""}${failure.stderr ?? ""}`,
      file,
    };
  }
}

describe("the staging stand-in's entry env (agent.Dockerfile ticks-worker)", () => {
  it("source time demands none of the boot's inputs — a restored box gets no TICKS_REPO_URL", {
    timeout: 300_000,
  }, async () => {
    // The lines the whole-container destroy's --setup runs first. The tick
    // jpy staging run died here: the `:?` at source time refused before the
    // case ever dispatched, so the RESTORE failed on the setup entry.
    const answer = await runInRestoreEnv(sourceTime, []);
    expect(answer.code).toBe(0);
    expect(answer.output).not.toContain("TICKS_REPO_URL");
  });

  it("--boot without TICKS_REPO_URL refuses legibly with the boot's config exit (2)", {
    timeout: 300_000,
  }, async () => {
    // a2l's intent kept: a boot with no origin URL still fails legibly, as
    // the production boot's require_common_inputs does (EXIT_CONFIG=2).
    const answer = await runInRestoreEnv(standinScript, ["--boot"]);
    expect(answer.code).toBe(2);
    expect(answer.output).toContain("TICKS_REPO_URL unset");
  });

  it("--setup references none of the boot's origin inputs (the restore contract)", () => {
    const setup = entryBody("setup");
    expect(setup).not.toContain("TICKS_REPO_URL");
    expect(setup).not.toMatch(/\$\{?origin\}?/);
  });

  it("the script parses (bash -n), whatever the heredoc edits did to it", {
    timeout: 300_000,
  }, async () => {
    const answer = await runInRestoreEnv(standinScript, []);
    await execFileAsync("bash", ["-n", answer.file]);
  });
});
