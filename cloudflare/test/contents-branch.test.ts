import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import { githubContentsStore } from "../src/git-contents";

/**
 * Every write must name the branch it is for (tick 99f).
 *
 * GitHub's contents API takes the target ref differently for reads and writes:
 * a read takes `?ref=` in the query string, a write takes `branch` in the
 * BODY. A write with no branch commits to the repository's DEFAULT branch and
 * answers 201, so nothing anywhere reports a problem. A cloud run wrote its
 * run state and a tracker update to `main` this way while reading from
 * `epic/uzc`, and it was found by noticing the commits in a `git pull`.
 *
 * These assert the REQUEST, not the answer. A test that checks what the store
 * returns passes against the broken code — that is the whole reason the bug
 * survived.
 */

const REF = "epic/uzc";

type Sent = { url: string; method: string; body: Record<string, unknown> };

function recorder(answer: () => Response): { sent: Sent[]; restore: () => void } {
  const original = globalThis.fetch;
  const sent: Sent[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({
      url: typeof input === "string" ? input : input.toString(),
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? JSON.parse(init.body) : {},
    });
    return answer();
  }) as typeof globalThis.fetch;
  return { sent, restore: () => void (globalThis.fetch = original) };
}

const created = () =>
  new Response(
    JSON.stringify({ commit: { sha: "c".repeat(40) }, content: { sha: "b".repeat(40) } }),
    {
      status: 201,
    },
  );

let rec: { sent: Sent[]; restore: () => void } | undefined;
afterEach(() => {
  rec?.restore();
  rec = undefined;
});

describe("a contents write", () => {
  it("names its branch when creating a file", async () => {
    rec = recorder(created);
    const store = githubContentsStore(env as never, "owner/repo", REF);

    await store.create(".ticfac/runs/r1/checkpoint.json", {
      content: "{}",
      message: "open the run state",
    });

    const put = rec.sent.find((s) => s.method === "PUT");
    expect(put).toBeDefined();
    expect(put?.body.branch).toBe(REF);
  });

  it("names its branch when updating a file", async () => {
    rec = recorder(created);
    const store = githubContentsStore(env as never, "owner/repo", REF);

    await store.update(".tick/issues/f8l.json", "b".repeat(40), {
      content: "{}",
      message: "tracker update",
    });

    const put = rec.sent.find((s) => s.method === "PUT");
    expect(put?.body.branch).toBe(REF);
    // The blob guard must survive alongside it: this is a compare-and-swap on
    // both the ref and the blob, not one or the other.
    expect(put?.body.sha).toBe("b".repeat(40));
  });

  it("never writes without a branch, whatever the caller passed", async () => {
    rec = recorder(created);
    const store = githubContentsStore(env as never, "owner/repo", REF);

    await store.create("a.json", { content: "{}", message: "m" });
    await store.update("b.json", "b".repeat(40), { content: "{}", message: "m" });

    const writes = rec.sent.filter((s) => s.method === "PUT");
    expect(writes).toHaveLength(2);
    for (const write of writes) {
      expect(write.body.branch, `${write.url} wrote with no branch`).toBe(REF);
    }
  });
});
