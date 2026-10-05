/**
 * The dumb-HTTP origin against REAL git (epic 43y, tick a2l): the staging
 * stand-in's throwaway repository lives on the Worker side — an origin that
 * OUTLIVES THE BOX — and this is the wire proof that real git clones from it,
 * seeds it, pushes wip snapshots to it, is restored from it into a fresh
 * "container", and keeps its fast-forward discipline over it, with the
 * production checkpoint lines doing the pushing and fetching
 * (../../src/workspace/checkpoints.js, exactly the code a destroyed staging
 * container's restore runs).
 *
 * The workerd suite (cloudflare/test/staging-git-origin.test.ts) proves the
 * Durable Object glue: the token in the repo path, the rewritten prefix, the
 * verb dispatch over storage. What only a real `git` can prove is the
 * protocol itself — `git-http-push`'s PROPFIND/LOCK/PUT/MOVE dance against
 * this server's exact response shapes — so it runs here, in the node half,
 * over a real socket, the same way the pi-durable harness proves the shell
 * behaviour no workerd test can.
 */
import { execFile } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { FactorySandboxEnv } from "../../src/env/factory-sandbox.js";
import {
  pushWipCheckpoint,
  restoreWorkspace,
  WIP_COMMIT_SUBJECT,
  type WorkspaceGit,
} from "../../src/workspace/checkpoints.js";
import { DumbGitOrigin, MemoryOriginStore } from "../../src/workspace/dumb-git-origin.js";
import { localSandboxDoor } from "./local-sandbox-door.js";

const execFileAsync = promisify(execFile);

/**
 * git against the ORIGIN, ALWAYS asynchronous: this suite's HTTP server runs
 * in the process that runs the test, so a synchronous spawn would block the
 * very event loop git's answer has to arrive on — a deadlock where git waits
 * for the server and the server waits for the event loop. Every git call in
 * this file can reach the origin, so every one goes through here.
 */
async function git(dir: string, args: string[]): Promise<string> {
  const out = await execFileAsync(
    "git",
    ["-C", dir, "-c", "user.name=ticfac worker", "-c", "user.email=worker@example.com", ...args],
    { encoding: "utf8" },
  );
  return out.stdout;
}

/**
 * The failure of a git command that is EXPECTED to fail, as a resolved value:
 * the refusal itself (non-fast-forward) is the assertion, not an exception.
 */
async function gitRefusal(dir: string, args: string[], why: string): Promise<number> {
  let status = 0;
  let stderr = "";
  try {
    await git(dir, args);
  } catch (error) {
    const failure = error as { code?: number; stderr?: string; stdout?: string };
    status = failure.code ?? -1;
    stderr = `${failure.stderr ?? ""}${failure.stdout ?? ""}`;
  }
  expect(status).not.toBe(0);
  expect(stderr).toContain(why);
  return status;
}

describe("a dumb-HTTP origin, over real git", () => {
  let root: string;
  let server: Server;
  let url: string;
  let door: ReturnType<typeof localSandboxDoor>;

  beforeEach(async () => {
    root = mkdtempSync(join(tmpdir(), "ticfac-dumb-origin-"));
    const origin = new DumbGitOrigin({ store: new MemoryOriginStore(), repoPrefix: "/origin.git" });
    server = createServer((incoming, res) => {
      const chunks: Buffer[] = [];
      incoming.on("data", (chunk: Buffer) => chunks.push(chunk));
      incoming.on("end", () => {
        void (async () => {
          const body = Buffer.concat(chunks);
          const init: RequestInit = {
            method: incoming.method ?? "GET",
            headers: incoming.headers as Record<string, string>,
          };
          if (body.length > 0 && incoming.method !== "GET" && incoming.method !== "HEAD") {
            init.body = new Uint8Array(body);
          }
          const answer = await origin.handle(
            new Request(`http://${incoming.headers.host}${incoming.url}`, init),
          );
          const bytes = new Uint8Array(await answer.arrayBuffer());
          const headers = [...answer.headers].map(
            ([name, value]) => [name, value] as [string, string],
          );
          res.writeHead(answer.status, headers);
          res.end(bytes);
        })().catch((error: unknown) => {
          res.writeHead(500);
          res.end(String(error));
        });
      });
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    if (address === null || typeof address === "string") throw new Error("no listen address");
    url = `http://127.0.0.1:${address.port}/origin.git`;
  });

  afterEach(() => {
    for (const p of door?.processes ?? []) {
      if (!p.settled) {
        const pid = p.child.pid;
        if (pid !== undefined) {
          try {
            process.kill(-pid, "SIGKILL");
          } catch {
            /* already gone */
          }
        }
      }
    }
    server.close();
    rmSync(root, { recursive: true, force: true });
  });

  /**
   * The staging stand-in's whole story (cloudflare/staging/agent.Dockerfile
   * `--boot`, harness/src/workspace/checkpoints.js) — the throwaway origin on
   * the far side of the network, a container that is destroyed mid-turn, the
   * restore that rebuilds the workspace from the last wip commit, and the
   * turn that continues from the restored tree:
   */
  it("an origin that outlives the box carries the boot, the wip and the restore", {
    // A process-driving test (harness-timeout-discipline.test.ts): real git
    // through the real local door over a live socket — the very shape that
    // crossed a 120s bound at 138s under load — so it states the 300s bound.
    timeout: 300_000,
  }, async () => {
    const branch = "tick/proof/a2l";
    const checkout = join(root, "repo");

    // 1. THE BOOT, against an ORIGIN THAT IS STILL EMPTY: clone (the empty
    //    warning is the boot's own first-time case), seed, push main and the
    //    attempt branch — the branch is what the restore fetches, so even a
    //    container lost before the first wip restores to the boot state.
    const cloneEmpty = (await execFileAsync("git", ["clone", url, checkout], {
      encoding: "utf8",
    }).catch((error: unknown) => error as { code?: number; stderr?: string; stdout?: string })) as {
      code?: number;
      stderr?: string;
      stdout?: string;
    };
    expect(cloneEmpty.code).toBeUndefined();
    expect(`${cloneEmpty.stderr ?? ""}${cloneEmpty.stdout ?? ""}`).toContain("empty repository");
    writeFileSync(join(checkout, "greet.sh"), '#!/bin/sh\necho "Helo, world"\n');
    await git(checkout, ["add", "-A"]);
    await git(checkout, ["commit", "-q", "-m", "seed"]);
    await git(checkout, ["push", "-q", url, "HEAD:refs/heads/main", `HEAD:refs/heads/${branch}`]);

    // A fresh boot of a box whose origin is already seeded: the clone carries
    // the seed, and the branch already exists.
    const reboot = join(root, "reboot");
    mkdirSync(reboot);
    await git(reboot, ["init", "-q", "--initial-branch=main", "."]);
    await git(reboot, ["fetch", "-q", url, branch]);
    await git(reboot, ["checkout", "-q", "-B", branch, "FETCH_HEAD"]);
    expect(readFileSync(join(reboot, "greet.sh"), "utf8")).toContain("Helo, world");

    // 2. THE AGENT'S ROUND: an edit lands in the working tree, and the
    //    production checkpoint line pushes it as a wip snapshot — over the
    //    network origin, exactly as every tool round does on staging.
    const gitSpec: WorkspaceGit = {
      remote: url,
      branch,
      identity: { name: "ticfac worker", email: "worker@example.com" },
      base: "0000000000000000000000000000000000000000",
      setup: "printf ticfac-setup-ok > .setup-marker",
      // Keeps the restore's GLOBAL git config off this test host.
      env: { GIT_CONFIG_GLOBAL: join(root, "git-config-global") },
    };
    door = localSandboxDoor({ cwd: checkout, env: gitSpec.env });
    const env = new FactorySandboxEnv({
      sandbox: door.sandbox,
      cwd: checkout,
      guardDir: join(root, "guard"),
      pollMs: 10,
      workspace: gitSpec,
    });
    writeFileSync(join(checkout, "greet.sh"), '#!/bin/sh\necho "Hello, world"\n');
    const wip = await pushWipCheckpoint(env.hostShell(), gitSpec);
    expect(wip.kind).toBe("pushed");

    // 3. THE BOX IS DESTROYED: the origin lives where the container cannot
    //    take it, so this destroy costs nothing but the workspace.
    rmSync(checkout, { recursive: true, force: true });
    mkdirSync(checkout);

    // 4. THE RESTORE, the production line a destroyed container's harness
    //    runs in its replacement: clear, clone, fetch the attempt branch,
    //    check out the last wip commit, run the setup.
    const restored = await restoreWorkspace(env.hostShell(), gitSpec);
    expect(restored.kind).toBe("restored");
    if (restored.kind !== "restored") return;
    expect(restored.subject).toBe(WIP_COMMIT_SUBJECT);
    expect(restored.sha).toBe(wip.kind === "pushed" ? wip.sha : "");
    // The snapshot is unwrapped: the agent's history as it made it (seed),
    // its uncommitted edit back in the tree (staged — the unwrap is a soft
    // reset that leaves the snapshot's tree in the index), and the setup's
    // own marker untracked beside it.
    expect((await git(checkout, ["log", "--format=%s", "-1"])).trim()).toBe("seed");
    expect(readFileSync(join(checkout, "greet.sh"), "utf8")).toContain("Hello, world");
    expect(await git(checkout, ["status", "--porcelain"])).toContain("M  greet.sh");
    expect(readFileSync(join(checkout, ".setup-marker"), "utf8")).toBe("ticfac-setup-ok");

    // 5. THE TURN CONTINUES: the agent commits on the restored tree, makes a
    //    NEW edit, and the next round's snapshot force-pushes over the
    //    previous round's — the lineage every round after a restore actually
    //    produces (a new snapshot is never a fast-forward of the last).
    await git(checkout, ["add", "-A"]);
    await git(checkout, ["commit", "-q", "-m", "the agent's own commit"]);
    writeFileSync(join(checkout, "README.md"), "# proof\n\nthe second round's edit\n");
    const second = await pushWipCheckpoint(env.hostShell(), gitSpec);
    expect(second.kind).toBe("pushed");
    if (second.kind === "pushed") expect(second.sha).not.toBe(restored.sha);

    // 6. The origin answers a fresh clone with the newest snapshot's tree.
    const after = join(root, "after");
    mkdirSync(after);
    await git(after, ["init", "-q", "--initial-branch=main", "."]);
    await git(after, ["fetch", "-q", url, branch]);
    await git(after, ["checkout", "-q", "-B", branch, "FETCH_HEAD"]);
    expect(readFileSync(join(after, "greet.sh"), "utf8")).toContain("Hello, world");
    expect((await git(after, ["log", "--format=%s", "-1"])).trim()).toBe(WIP_COMMIT_SUBJECT);
    expect(readdirSync(after)).toContain("greet.sh");
  });

  /** The finish phase's push is not forced: the origin must hold that line. */
  it("keeps the fast-forward-only push a dumb origin must", { timeout: 60_000 }, async () => {
    const branch = "tick/proof/a2l";
    const checkout = join(root, "repo");
    mkdirSync(checkout);
    await git(checkout, ["init", "-q", "--initial-branch=main", "."]);
    writeFileSync(join(checkout, "greet.sh"), '#!/bin/sh\necho "Helo, world"\n');
    await git(checkout, ["add", "-A"]);
    await git(checkout, ["commit", "-q", "-m", "seed"]);
    await git(checkout, ["push", "-q", url, `HEAD:refs/heads/${branch}`]);

    // A second commit, pushed: the origin's tip moves.
    writeFileSync(join(checkout, "greet.sh"), '#!/bin/sh\necho "Hello, world"\n');
    await git(checkout, ["add", "-A"]);
    await git(checkout, ["commit", "-q", "-m", "the fix"]);
    await git(checkout, ["push", "-q", url, `HEAD:refs/heads/${branch}`]);

    // A rewrite of that history is a NON-FAST-FORWARD: the remote-heads
    // listing (GET <repo>/refs/) is what lets the client tell, and the
    // refusal is the discipline the finish phase's push keeps.
    await git(checkout, ["reset", "-q", "--hard", "HEAD~1"]);
    writeFileSync(join(checkout, "greet.sh"), "divergent\n");
    await git(checkout, ["add", "-A"]);
    await git(checkout, ["commit", "-q", "-m", "divergent"]);
    await gitRefusal(checkout, ["push", url, `HEAD:refs/heads/${branch}`], "non-fast-forward");

    // The branch tip did not move: the origin still holds the pushed fix.
    const probe = join(root, "probe");
    mkdirSync(probe);
    await git(probe, ["init", "-q", "--initial-branch=main", "."]);
    await git(probe, ["fetch", "-q", url, branch]);
    await git(probe, ["checkout", "-q", "-B", branch, "FETCH_HEAD"]);
    expect(readFileSync(join(probe, "greet.sh"), "utf8")).toContain("Hello, world");
  });
});
