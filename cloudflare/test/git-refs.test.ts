import { env } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { gitRefWriter } from "../src/git-refs";
import type { Env } from "../src/index";

/**
 * The attempt's own push at collect (tick us2), and WHICH branch it pushes
 * from (epic hn6's second cloud run). A landing branch is per attempt but not
 * per run, so a second run's attempt 1 lands on the name the first run's
 * attempt 1 already pushed. The container sees that branch is not cut from
 * its base, leaves it alone and pushes `<landing>-<run id>` instead
 * (image/worker.sh adopt_worker_branch). A put that read the landing name
 * regardless put the OTHER run's work on this attempt's write ref. The Go
 * collect follows the container's rule since #129; this is the same rule on
 * the Workflow-side executor's put.
 */

const saved: Record<string, unknown> = {};
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

const API = "https://github.example.test";
const PROJECT = "acme/project";
const BASE = "b".repeat(40);
const OTHER_RUN_HEAD = "1".repeat(40);
const OWN_HEAD = "2".repeat(40);
const LANDING = "tick/xte/attempt-1/k4s";
const RUN = "run_second";
const OWN = `${LANDING}-${RUN}`;
const WRITE_REF = `refs/heads/ticfac/run-${RUN}/tick-k4s/attempt-1`;

type Origin = {
  /** Branch heads on origin. */
  heads: Record<string, string>;
  /** Heads the base is an ancestor of. */
  descendants: Set<string>;
};

/** Stands in for GitHub's ref, compare and ref-write endpoints. */
function stubOrigin(origin: Origin): {
  created: Array<{ ref: string; sha: string }>;
  restore: () => void;
} {
  const created: Array<{ ref: string; sha: string }> = [];
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    if (!url.startsWith(API)) return original(input as RequestInfo, init);
    const read = url.match(/\/git\/ref\/(.+)$/);
    if (read !== null) {
      const branch = decodeURIComponent(read[1]!).replace(/^heads\//, "");
      const sha = origin.heads[branch];
      return sha === undefined
        ? new Response("not found", { status: 404 })
        : Response.json({ object: { sha } });
    }
    const compare = url.match(/\/compare\/([^.]+)\.\.\.(.+)$/);
    if (compare !== null) {
      const head = decodeURIComponent(compare[2]!);
      const ours = origin.descendants.has(head);
      return Response.json({
        status: ours ? "ahead" : "behind",
        ahead_by: ours ? 1 : 0,
        behind_by: ours ? 0 : 3,
      });
    }
    if (url.endsWith("/git/refs") && init?.method === "POST") {
      created.push(JSON.parse(String(init.body)) as { ref: string; sha: string });
      return Response.json({}, { status: 201 });
    }
    return new Response("unexpected request in test", { status: 500 });
  }) as typeof fetch;
  return { created, restore: () => void (globalThis.fetch = original) };
}

let restore: (() => void) | undefined;
beforeEach(() => {
  set("GITHUB_API_BASE_URL", API);
  set("GITHUB_TOKEN", "ghp_repo_scoped");
});
afterEach(() => {
  restore?.();
  restore = undefined;
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

describe("gitRefWriter.put — the branch the container actually pushed", () => {
  it("puts the run's own push, not another run's branch that holds the landing name", async () => {
    const stub = stubOrigin({
      heads: { [LANDING]: OTHER_RUN_HEAD, [OWN]: OWN_HEAD },
      descendants: new Set([OWN_HEAD]),
    });
    restore = stub.restore;
    const put = await gitRefWriter(env as unknown as Env, PROJECT).put({
      branch: LANDING,
      ref: WRITE_REF,
      base_sha: BASE,
      run_id: RUN,
    });
    expect(put).toEqual({ state: "created", sha: OWN_HEAD, branch: OWN });
    expect(stub.created).toEqual([{ ref: WRITE_REF, sha: OWN_HEAD }]);
  });

  it("puts nothing when only another run's branch is there: that is not this attempt's work", async () => {
    const stub = stubOrigin({ heads: { [LANDING]: OTHER_RUN_HEAD }, descendants: new Set() });
    restore = stub.restore;
    const put = await gitRefWriter(env as unknown as Env, PROJECT).put({
      branch: LANDING,
      ref: WRITE_REF,
      base_sha: BASE,
      run_id: RUN,
    });
    expect(put).toEqual({ state: "missing" });
    expect(stub.created).toEqual([]);
  });

  it("puts the landing branch when it is cut from the attempt's base", async () => {
    const stub = stubOrigin({ heads: { [LANDING]: OWN_HEAD }, descendants: new Set([OWN_HEAD]) });
    restore = stub.restore;
    const put = await gitRefWriter(env as unknown as Env, PROJECT).put({
      branch: LANDING,
      ref: WRITE_REF,
      base_sha: BASE,
      run_id: RUN,
    });
    expect(put).toEqual({ state: "created", sha: OWN_HEAD, branch: LANDING });
  });
});
