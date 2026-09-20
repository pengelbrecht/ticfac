import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import { ensureBranch } from "../src/git-refs";

/**
 * A fresh cloud epic run reads its tracker FROM its integration branch, and
 * on a first run nothing has cut that branch yet (tick ant). Before the fix,
 * run_76c209eed46f4081bdd56d497b149c59 died in its first plan step with
 * "epic uzc is not readable" one second after it started, because
 * refs/heads/epic/uzc did not exist and a read of a branch that is not there
 * returns nothing.
 */

const BASE = `${"0".repeat(39)}1`;

type Call = { method: string; url: string; body: unknown };

function github(routes: (call: Call) => Response | undefined): {
  calls: Call[];
  restore: () => void;
} {
  const original = globalThis.fetch;
  const calls: Call[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    const call: Call = {
      method: init?.method ?? "GET",
      url,
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(call);
    const answer = routes(call);
    if (answer === undefined) throw new Error(`no stub for ${call.method} ${url}`);
    return answer;
  }) as typeof globalThis.fetch;
  return { calls, restore: () => void (globalThis.fetch = original) };
}

let stub: { calls: Call[]; restore: () => void } | undefined;
afterEach(() => {
  stub?.restore();
  stub = undefined;
});

describe("ensureBranch", () => {
  it("cuts the branch from the base when origin does not have it", async () => {
    stub = github((call) => {
      if (call.method === "GET") return new Response("", { status: 404 });
      if (call.method === "POST") return new Response(JSON.stringify({}), { status: 201 });
      return undefined;
    });

    const result = await ensureBranch(env as never, "owner/repo", "epic/uzc", BASE);

    expect(result).toEqual({ state: "created", sha: BASE });
    const created = stub.calls.find((c) => c.method === "POST");
    expect(created?.body).toEqual({ ref: "refs/heads/epic/uzc", sha: BASE });
  });

  it("leaves a branch that is already there alone", async () => {
    stub = github((call) => {
      if (call.method === "GET")
        return new Response(JSON.stringify({ object: { sha: "a".repeat(40) } }), { status: 200 });
      return undefined;
    });

    const result = await ensureBranch(env as never, "owner/repo", "epic/uzc", BASE);

    expect(result).toEqual({ state: "already", sha: "a".repeat(40) });
    expect(stub.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("refuses rather than guessing when no base sha was named", async () => {
    stub = github((call) => {
      if (call.method === "GET") return new Response("", { status: 404 });
      return undefined;
    });

    const result = await ensureBranch(env as never, "owner/repo", "epic/uzc", "");

    expect(result.state).toBe("refused");
    expect(result.state === "refused" && result.detail).toContain("no base sha");
    expect(stub.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("takes the winner's branch when another creator races it", async () => {
    let reads = 0;
    stub = github((call) => {
      if (call.method === "GET") {
        reads += 1;
        return reads === 1
          ? new Response("", { status: 404 })
          : new Response(JSON.stringify({ object: { sha: "b".repeat(40) } }), { status: 200 });
      }
      // 422 is GitHub for "that ref already exists".
      if (call.method === "POST") return new Response("", { status: 422 });
      return undefined;
    });

    const result = await ensureBranch(env as never, "owner/repo", "epic/uzc", BASE);

    expect(result).toEqual({ state: "already", sha: "b".repeat(40) });
  });

  it("reports a read failure that is not an absence instead of cutting over it", async () => {
    stub = github((call) => {
      if (call.method === "GET") return new Response("", { status: 500 });
      return undefined;
    });

    const result = await ensureBranch(env as never, "owner/repo", "epic/uzc", BASE);

    expect(result.state).toBe("refused");
    expect(stub.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
