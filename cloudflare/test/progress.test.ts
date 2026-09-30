import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import {
  changedBranches,
  compareSnapshots,
  githubRepoRefs,
  MAX_REF_PAGES,
  nextPageUrl,
  type RepoRefs,
  runRefPrefixes,
  snapshotRefs,
} from "../src/progress";

/**
 * The durable-evidence probe: what the remote's refs say happened.
 *
 * These are the unit half of tick ehy. The lifecycle half — that a run whose
 * harness exits 0 with none of this is `stopped` and not `completed` — is in
 * run-workflow.test.ts, driving the real Workflow.
 */

const saved: Record<string, unknown> = {};

function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

const SHA_A = "a".repeat(40);
const SHA_B = "b".repeat(40);

describe("what counts as the epic moving", () => {
  it("sees a branch that appeared", () => {
    expect(changedBranches({ main: SHA_A }, { main: SHA_A, "epic/ko8": SHA_B })).toEqual([
      "epic/ko8",
    ]);
  });

  it("sees a branch that advanced", () => {
    expect(changedBranches({ main: SHA_A }, { main: SHA_B })).toEqual(["main"]);
  });

  // Merging the epic branch and deleting it is the successful ending. A
  // comparison that only looked forward would read it as a run that did nothing.
  it("sees a branch that was merged and cleaned up", () => {
    expect(changedBranches({ main: SHA_A, "epic/ko8": SHA_B }, { main: SHA_A })).toEqual([
      "epic/ko8",
    ]);
  });

  it("sees nothing when the remote is untouched", () => {
    expect(changedBranches({ main: SHA_A }, { main: SHA_A })).toEqual([]);
  });
});

describe("the verdict two reads support", () => {
  it("calls an unchanged remote no progress, and says so in the operator's words", () => {
    const verdict = compareSnapshots(
      { ok: true, refs: { main: SHA_A } },
      { ok: true, refs: { main: SHA_A } },
    );
    expect(verdict.state).toBe("none");
    expect(verdict.detail).toMatch(/no branch on origin changed/i);
  });

  it("names the branches that moved", () => {
    const verdict = compareSnapshots(
      { ok: true, refs: { main: SHA_A } },
      { ok: true, refs: { main: SHA_A, "epic/ko8": SHA_B } },
    );
    expect(verdict.state).toBe("advanced");
    expect(verdict.detail).toContain("epic/ko8");
  });

  // "Nothing moved" and "nobody could tell" are different facts about a run —
  // the same distinction `cost_source` draws between a zero and an unknown.
  it("refuses to call an unreadable remote either way", () => {
    const before = compareSnapshots(
      { ok: false, detail: "GitHub answered HTTP 503" },
      { ok: true, refs: {} },
    );
    expect(before.state).toBe("unknown");
    expect(before.detail).toContain("503");

    const after = compareSnapshots(
      { ok: true, refs: {} },
      { ok: false, detail: "GitHub answered HTTP 403" },
    );
    expect(after.state).toBe("unknown");
    expect(after.detail).toContain("403");
  });
});

// ------------------------------------------------------------ the reader ---

type Ref = { ref: string; object: { sha: string } };

function heads(names: string[], sha = SHA_A): Ref[] {
  return names.map((name) => ({ ref: `refs/heads/${name}`, object: { sha } }));
}

/**
 * Stands in for GitHub's `matching-refs`, the way GitHub actually answers it:
 * a PREFIX match on the path, `page` and `per_page` ignored, and — only when
 * `pageSize` is given — further pages offered through a `Link` header and
 * nothing else. The reader tick hn6's run broke on asked for `page=1..20` and
 * got the whole listing every time.
 */
function stubGitHub(
  refs: Ref[],
  pageSize: number | null = null,
): { urls: string[]; headers: Record<string, string>[]; restore: () => void } {
  const urls: string[] = [];
  const headers: Record<string, string>[] = [];
  const original = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    if (!url.startsWith("https://github.example.test")) return original(input as RequestInfo, init);
    urls.push(url);
    headers.push((init?.headers ?? {}) as Record<string, string>);
    const parsed = new URL(url);
    const marker = "/git/matching-refs/";
    const prefix = decodeURIComponent(
      parsed.pathname.slice(parsed.pathname.indexOf(marker) + marker.length),
    );
    const matching = refs.filter((entry) => entry.ref.startsWith(`refs/${prefix}`));
    if (pageSize === null) return Response.json(matching);
    const cursor = Number(parsed.searchParams.get("cursor") ?? "0");
    const page = matching.slice(cursor, cursor + pageSize);
    const next = cursor + pageSize;
    const link =
      next < matching.length
        ? `<${parsed.origin}${parsed.pathname}?cursor=${next}>; rel="next"`
        : null;
    return Response.json(page, link === null ? {} : { headers: { link } });
  }) as typeof fetch;
  return { urls, headers, restore: () => void (globalThis.fetch = original) };
}

describe("the branches a run reads", () => {
  it("are its epic's, its run branches, its workers' and its own dispatches", () => {
    expect(runRefPrefixes("hn6", "run_abc")).toEqual([
      "epic/hn6",
      "tick-run/hn6",
      "tick/hn6/",
      "ticfac/run-run_abc/",
    ]);
  });
});

describe("reading the remote's heads", () => {
  it("returns branch heads under the prefixes asked for, without refs/heads/", async () => {
    set("GITHUB_API_BASE_URL", "https://github.example.test");
    set("GITHUB_TOKEN", "ghp_repo_scoped");
    const github = stubGitHub([...heads(["main", "epic/ko9"]), ...heads(["epic/ko8"], SHA_B)]);

    try {
      await expect(githubRepoRefs(env).list("acme/project", ["epic/ko8"])).resolves.toEqual({
        "epic/ko8": SHA_B,
      });
      expect(github.urls).toEqual([
        "https://github.example.test/repos/acme/project/git/matching-refs/heads/epic/ko8",
      ]);
      // GitHub rejects an API request with no user agent outright.
      expect(github.headers[0]!["user-agent"]).toBeTruthy();
      expect(github.headers[0]!.authorization).toBe("Bearer ghp_repo_scoped");
    } finally {
      github.restore();
    }
  });

  // Tick hn6's run: origin held 281 branches, GitHub answered all of them to
  // every `page=N`, and the run's progress was recorded as unknown because the
  // repository "has more than 2000 branches". A run reads its own namespaces,
  // so how many branches other runs and people left behind does not enter.
  it("reads a run's refs on a remote crowded with other runs' branches", async () => {
    set("GITHUB_API_BASE_URL", "https://github.example.test");
    const crowd = Array.from(
      { length: 2500 },
      (_unused, index) => `ticfac/run-old${index}/tick-a/attempt-1`,
    );
    const github = stubGitHub([
      ...heads(["main", "epic/hn7", "tick-run/hn7", ...crowd]),
      ...heads(
        [
          "epic/hn6",
          "tick-run/hn6",
          "tick-run/hn6-run_abc",
          "tick/hn6/attempt-2/7uv",
          "ticfac/run-run_abc/tick-3gk/attempt-1",
        ],
        SHA_B,
      ),
    ]);

    try {
      const snapshot = await snapshotRefs(env, "acme/project", runRefPrefixes("hn6", "run_abc"));
      expect(snapshot).toEqual({
        ok: true,
        refs: {
          "epic/hn6": SHA_B,
          "tick-run/hn6": SHA_B,
          "tick-run/hn6-run_abc": SHA_B,
          "tick/hn6/attempt-2/7uv": SHA_B,
          "ticfac/run-run_abc/tick-3gk/attempt-1": SHA_B,
        },
      });
      // One request per namespace, and never the bare `heads/` listing.
      expect(github.urls).toHaveLength(4);
      for (const url of github.urls) expect(url).not.toMatch(/matching-refs\/heads\/$/);
    } finally {
      github.restore();
    }
  });

  it("follows GitHub's Link header across pages", async () => {
    set("GITHUB_API_BASE_URL", "https://github.example.test");
    const attempts = Array.from(
      { length: 250 },
      (_unused, index) => `ticfac/run-r/tick-${index}/attempt-1`,
    );
    const github = stubGitHub(heads(attempts), 100);

    try {
      const refs = await githubRepoRefs(env).list("acme/project", ["ticfac/run-r/"]);
      expect(Object.keys(refs)).toHaveLength(250);
      expect(github.urls).toHaveLength(3);
    } finally {
      github.restore();
    }
  });

  // A truncated listing is worse than no listing: the branch that moved could
  // be the one past the cut, and the comparison would answer "none" with
  // confidence it has not earned.
  it("refuses rather than truncating a listing longer than the page cap", async () => {
    set("GITHUB_API_BASE_URL", "https://github.example.test");
    const attempts = Array.from(
      { length: MAX_REF_PAGES * 10 + 1 },
      (_unused, index) => `ticfac/run-r/tick-${index}/attempt-1`,
    );
    const github = stubGitHub(heads(attempts), 10);

    try {
      const snapshot = await snapshotRefs(env, "acme/project", ["ticfac/run-r/"]);
      expect(snapshot.ok).toBe(false);
      expect(snapshot.ok === false && snapshot.detail).toContain("ticfac/run-r/");
      expect(github.urls).toHaveLength(MAX_REF_PAGES);
    } finally {
      github.restore();
    }
  });

  it("refuses to list every branch when no prefix is named", async () => {
    set("GITHUB_API_BASE_URL", "https://github.example.test");
    const github = stubGitHub(heads(["main"]));

    try {
      const snapshot = await snapshotRefs(env, "acme/project", [""]);
      expect(snapshot.ok).toBe(false);
      expect(github.urls).toEqual([]);
    } finally {
      github.restore();
    }
  });

  it("reports an unreachable remote as unreadable rather than throwing", async () => {
    const failing: RepoRefs = {
      async list(): Promise<Record<string, string>> {
        throw new Error("GitHub answered HTTP 503 for the branch listing");
      },
    };
    set("REPO_REFS", failing);

    const snapshot = await snapshotRefs(env, "acme/project", ["epic/x"]);
    expect(snapshot.ok).toBe(false);
    expect(snapshot.ok === false && snapshot.detail).toContain("503");
  });
});

describe("the Link header", () => {
  it("names the next page when there is one", () => {
    expect(
      nextPageUrl(
        '<https://api.example.test/x?cursor=2>; rel="next", <https://api.example.test/x?cursor=9>; rel="last"',
      ),
    ).toBe("https://api.example.test/x?cursor=2");
    expect(nextPageUrl('<https://api.example.test/x?cursor=1>; rel="prev"')).toBeNull();
    expect(nextPageUrl(null)).toBeNull();
  });
});
