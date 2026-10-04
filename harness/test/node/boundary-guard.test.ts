import { execFileSync } from "node:child_process";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { boundaryGuardShim, WORKER_TK_DENIED } from "../../src/env/boundary-guard.js";
import { createGuardedNodeExecutionEnv } from "../../src/env/node.js";

/**
 * The boundary guard of a local worker's environment, against real bash:
 * the refusal is the acceptance test's second half ("the guard refuses
 * tracker writes"), and it is tested the way the Go suite tests the
 * container's own guard (internal/sandboximage/worker_boundary_test.go) —
 * by really invoking `tk` behind the shim and reading what comes back.
 */

// A minimal "tracker" checkout: a git repository with a .tick/ directory,
// and a stand-in real `tk` on PATH that records being run and answers
// version reads.
function makeCheckout(root: string, name: string) {
  const dir = join(root, name);
  mkdirSync(join(dir, ".tick/issues"), { recursive: true });
  writeFileSync(join(dir, ".tick/issues/kga.json"), '{\n  "id": "kga"\n}\n');
  execFileSync("git", ["init", "-q", dir]);
  return dir;
}

function makeRealTk(root: string) {
  const dir = join(root, "bin");
  mkdirSync(dir, { recursive: true });
  const path = join(dir, "tk");
  writeFileSync(
    path,
    [
      "#!/usr/bin/env bash",
      // Every invocation records itself: the refusal tests assert the real
      // tk NEVER ran, and the pass-through tests assert it did.
      'printf "%s\\n" "real-tk $*" >> "$TICFAC_REAL_TK_CALLS"',
      'case "$1" in version) printf "tk 9.9.9-test\\n"; exit 0;; esac',
      "printf 'real tk ran: %s\\n' \"$*\"",
      "exit 0",
      "",
    ].join("\n"),
  );
  chmodSync(path, 0o755);
  return { dir, path };
}

describe("the boundary guard of a local worker environment", () => {
  let root: string;
  let checkout: string;
  let realTkCalls: string;
  let oldPath: string | undefined;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "ticfac-guard-"));
    checkout = makeCheckout(root, "worktree");
    const realTk = makeRealTk(root);
    realTkCalls = join(root, "real-tk-calls");
    // The stand-in tk must be the one `command -v tk` finds when the guard
    // installs, the way the image's tk is what the container's install finds.
    oldPath = process.env.PATH;
    process.env.PATH = `${realTk.dir}:${process.env.PATH}`;
    process.env.TICFAC_REAL_TK_CALLS = realTkCalls;
  });

  afterEach(() => {
    if (oldPath !== undefined) process.env.PATH = oldPath;
    delete process.env.TICFAC_REAL_TK_CALLS;
    rmSync(root, { recursive: true, force: true });
  });

  function ledger() {
    const guard = `${checkout}.guard`;
    return existsSync(join(guard, "attempts")) ? readFileSync(join(guard, "attempts"), "utf8") : "";
  }

  it("refuses a tracker write, says the pinned refusal, and never reaches the real tk", async () => {
    const env = createGuardedNodeExecutionEnv({ cwd: checkout });
    let output = "";
    const result = await env.exec(
      "tk close kga",
      { onOutput: (t) => (output += t) },
      BACKGROUND_CONTEXT,
    );

    expect(result.ok && result.value.exitCode).toBe(1);
    expect(output).toContain(WORKER_TK_DENIED);
    // The attempt is recorded beside the shim, for the report to surface.
    expect(ledger()).toContain("ran `tk close kga`");
    // The real tk never ran: it never even created its call log — no
    // tracker write happened through it.
    expect(existsSync(realTkCalls)).toBe(false);
    // The guard lives OUTSIDE the checkout — a sibling, never inside it —
    // and it exists after the first command: the install is not optional.
    expect(existsSync(`${checkout}.guard/tk`)).toBe(true);
    expect(existsSync(join(checkout, "attempts"))).toBe(false);
  });

  it("passes tracker reads through to the real tk and does not report them", async () => {
    const env = createGuardedNodeExecutionEnv({ cwd: checkout });
    let output = "";
    const result = await env.exec(
      "tk version",
      { onOutput: (t) => (output += t) },
      BACKGROUND_CONTEXT,
    );

    expect(result.ok && result.value.exitCode).toBe(0);
    expect(output).toContain("tk 9.9.9-test");
    expect(ledger()).toBe("");
  });

  it("passes a write against a tracker that is not this checkout's", async () => {
    const fixture = makeCheckout(root, "fixture-repo");
    const env = createGuardedNodeExecutionEnv({ cwd: checkout });
    let output = "";
    const result = await env.exec(
      `cd "${fixture}" && tk close fixture-tick`,
      { onOutput: (t) => (output += t) },
      BACKGROUND_CONTEXT,
    );

    expect(result.ok && result.value.exitCode).toBe(0);
    expect(output).toContain("real tk ran: close fixture-tick");
    expect(ledger()).toBe("");
  });

  it("carries the pinned refusal from the shared contract, so the shims cannot drift", () => {
    // Three readers hold this string: the container's worker.sh, this
    // harness shim, and the shared contract that internal/sandboximage
    // asserts the container against. This is the fourth: the harness side
    // checked against the contract itself.
    const contract = JSON.parse(
      readFileSync(
        join(import.meta.dirname, "../../../contracts/worker-boot-contract.json"),
        "utf8",
      ),
    );
    expect(contract.boundary.tk_denied).toBe(WORKER_TK_DENIED);
    expect(boundaryGuardShim()).toContain(WORKER_TK_DENIED);
    // The pass list is the container's: a read that only one shim let
    // through would behave differently local and cloud.
    expect(boundaryGuardShim()).toContain("version | --version | help | --help | -h | show | list");
  });
});
