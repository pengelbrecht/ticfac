import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { boundaryGuardShim } from "../../src/env/boundary-guard.js";
import { FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import { localSandboxDoor } from "./local-sandbox-door.js";

/**
 * The guard must never name ITSELF as the real tk (run_7445005f, epic umq,
 * tick dax: a hosted worker hung for hours on `tk --help`).
 *
 * The env installs its guard into `<cwd>.guard`, and every short command —
 * the installer's own `command -v tk` among them — runs with that directory
 * at the head of PATH. So whenever a `tk` shim is ALREADY there when the
 * install runs, `command -v tk` answers the shim, the shim is recorded as
 * its own `real-tk`, and every tk read the shim passes through (`--help`,
 * `list`, `show`, …) becomes an `exec` of itself, forever: a CPU-burning
 * hang that never prints, never exits, and keeps the stuck watch's CPU
 * signal moving. Two ways the shim is already there, both of them routine:
 *
 *  - the container's boot (image/worker.sh install_boundary_guard) installs
 *    the IMAGE's guard into the very same `<workdir>.guard` before the
 *    conversation starts;
 *  - a second host life (a DO restart, a local relaunch) builds a fresh env
 *    over a box the first life already guarded.
 */

const CONTEXT = BACKGROUND_CONTEXT;

describe("the guard install over a guard directory that already holds a shim", () => {
  let root: string;
  let checkout: string;
  let guardDir: string;
  let realTkDir: string;
  let oldPath: string | undefined;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ticfac-guard-reinstall-"));
    checkout = join(root, "repo");
    mkdirSync(checkout, { recursive: true });
    execFileSync("git", ["init", "-q", checkout]);
    guardDir = `${checkout}.guard`;
    realTkDir = join(root, "bin");
    mkdirSync(realTkDir);
    writeFileSync(
      join(realTkDir, "tk"),
      '#!/usr/bin/env bash\nprintf "real tk ran: %s\\n" "$*"\nexit 0\n',
    );
    chmodSync(join(realTkDir, "tk"), 0o755);
    oldPath = process.env.PATH;
    process.env.PATH = `${realTkDir}:${process.env.PATH}`;
  });

  afterEach(() => {
    if (oldPath !== undefined) process.env.PATH = oldPath;
    rmSync(root, { recursive: true, force: true });
  });

  function envOver(): FactorySandboxEnv {
    const door = localSandboxDoor({ cwd: checkout });
    return new FactorySandboxEnv({ sandbox: door.sandbox, cwd: checkout, pollMs: 10 });
  }

  async function tkHelp(env: FactorySandboxEnv) {
    let output = "";
    // A bound the real answer meets in milliseconds: the broken guard never
    // answers at all, and the timeout is how this test hears it.
    const result = await env.exec(
      "tk --help 2>&1 | head -5",
      { timeout: 20, onOutput: (t) => (output += t) },
      CONTEXT,
    );
    return {
      exit: result.ok ? result.value.exitCode : `error: ${result.error.message}`,
      output,
    };
  }

  it("a second host life's env still reaches the real tk", { timeout: 300_000 }, async () => {
    const life1 = envOver();
    const first = await tkHelp(life1);
    expect(first.output).toContain("real tk ran: --help");
    // A refusal the first life's guard recorded, for the finish phase's report.
    await life1.exec("tk close kga", {}, CONTEXT);
    expect(readFileSync(join(guardDir, "attempts"), "utf8")).toContain("ran `tk close kga`");

    // A new life: a fresh env over the same box, the first life's shim on disk.
    const second = await tkHelp(envOver());
    // Its install kept the ledger: the first life's refusal is still reported.
    expect(readFileSync(join(guardDir, "attempts"), "utf8")).toContain("ran `tk close kga`");
    expect(second.exit).toBe(0);
    expect(second.output).toContain("real tk ran: --help");
    expect(readFileSync(join(guardDir, "real-tk"), "utf8").trim()).toBe(join(realTkDir, "tk"));
  });

  it("an image-installed guard in the same directory is not taken for the real tk", {
    timeout: 300_000,
  }, async () => {
    // What the container's boot leaves: its own shim and its own companions.
    mkdirSync(guardDir, { recursive: true });
    writeFileSync(join(guardDir, "tk"), boundaryGuardShim());
    chmodSync(join(guardDir, "tk"), 0o755);
    writeFileSync(join(guardDir, "real-tk"), `${join(realTkDir, "tk")}\n`);
    writeFileSync(join(guardDir, "checkout"), `${checkout}/.git\n`);

    const help = await tkHelp(envOver());
    expect(help.exit).toBe(0);
    expect(help.output).toContain("real tk ran: --help");
  });

  it("a shim whose real-tk names itself refuses rather than exec itself forever", {
    timeout: 300_000,
  }, () => {
    // Defence in depth: whatever wrote the companion, the shim never loops.
    mkdirSync(guardDir, { recursive: true });
    writeFileSync(join(guardDir, "tk"), boundaryGuardShim());
    chmodSync(join(guardDir, "tk"), 0o755);
    writeFileSync(join(guardDir, "real-tk"), `${join(guardDir, "tk")}\n`);
    writeFileSync(join(guardDir, "checkout"), "");
    const out = execFileSync("bash", ["-c", `"${join(guardDir, "tk")}" --help; echo "exit=$?"`], {
      encoding: "utf8",
      timeout: 20_000,
    });
    expect(out).toContain("exit=127");
  });
});
