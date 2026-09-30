/**
 * Did the run actually move the epic?
 *
 * A harness exits 0 when it has nothing left to say, which is not the same
 * thing as having done something. The first cloud run whose boot chain fully
 * succeeded produced 271 bytes — it named the substrate it had resolved, quoted
 * the note it intended to record, and exited — and the Workflow marked it
 * COMPLETED and charged for it. No wave was dispatched, no branch was pushed,
 * no note landed, and the epic still had every one of its open ticks.
 *
 * The herdr adapter states the rule the Workflow was breaking, in as many
 * words: lifecycle state is a scheduling signal, never the completion
 * authority; the durable layer is. So completion is decided HERE, against the
 * durable layer, and the process's exit status is only ever an input.
 *
 * **The durable layer is the remote's refs.** Commits that were never pushed
 * die with the container, so they are not evidence of anything; tracker
 * mutations are `.tick/` files committed and pushed like any other; a merged
 * epic is a branch that moved. All three reduce to one question — did any ref
 * on origin change between the moment the run started and the moment it
 * ended — and that question is answerable without trusting a word the
 * orchestrator printed.
 *
 * The comparison is over the branch families a run of this epic writes to,
 * never over every head on origin (tick hn6's run ended `unknown` on exactly
 * that: a whole-repository listing is a read whose cost grows with every
 * branch anyone ever left behind, and it stopped answering at all). The
 * families are {@link runRefPrefixes}: the epic branch, the run branches, the
 * harness workers' `tick/<epic>/` branches and this run's own dispatch
 * branches. Each is a PREFIX, not a name, so a run branch suffixed with a run
 * id or a worker branch the harness names differently inside its epic's
 * namespace still reads as progress, and the forgiving direction survives: a
 * push to any of them — the run's, or a person's — counts.
 *
 * A probe that cannot answer says so. "Nothing moved" and "nobody could tell"
 * are different facts about a run, exactly as they are for `cost_usd`, and the
 * unreadable case must not invent a no-op any more than it may invent a
 * success.
 */

import { githubAuthorization } from "./github-app";
import type { Env } from "./index";

// -------------------------------------------------------------- the verdict ---

/**
 * What the durable layer says about a run.
 *
 * - `advanced` — a ref on origin changed: something was committed and pushed.
 * - `none` — nothing on origin changed: the run stopped, it did not complete.
 * - `unknown` — the refs could not be read, so neither claim can be made.
 */
export type RunProgressState = "advanced" | "none" | "unknown";

export type RunProgress = {
  state: RunProgressState;
  /** Why, in the operator's words — named branches, or why nothing could be read. */
  detail: string;
};

/** How many branch names a detail line will spell before it summarises. */
const NAMED_BRANCHES = 3;

// ----------------------------------------------------------------- the seam ---

/**
 * The branch prefixes one run of `epic` writes to.
 *
 * - `epic/<epic>` — the integration branch the run merges ticks onto.
 * - `tick-run/<epic>` — the run branch the orchestrator commits the tracker
 *   to, and its `tick-run/<epic>-<run-id>` variant (branch-ownership.ts).
 * - `tick/<epic>/` — a harness worker's per-tick branch (worker-boot.ts).
 * - `ticfac/run-<run id>/` — this run's dispatched attempts, resolves and
 *   repairs (internal/reconcile's job ids).
 *
 * Prefixes rather than names because GitHub's `matching-refs` is a prefix
 * match, and so is the question: anything under the run's namespaces moving
 * is the run's evidence.
 */
export function runRefPrefixes(epic: string, runId: string): string[] {
  return [`epic/${epic}`, `tick-run/${epic}`, `tick/${epic}/`, `ticfac/run-${runId}/`];
}

/**
 * The remote's branch heads under `prefixes`, as `branch -> sha`.
 *
 * Scoped by construction: there is no way to ask for every head, because a
 * reader whose cost grows with every branch anybody left on origin is a reader
 * that eventually stops answering.
 *
 * A seam for the same reason `SandboxBinding` is one: the finalize rule is what
 * needs testing, and a rule exercisable only by pushing to a real GitHub
 * repository is a rule nobody tests. A deployment gets the GitHub reader below;
 * a test assigns its own to `env.REPO_REFS`.
 */
export interface RepoRefs {
  list(project: string, prefixes: readonly string[]): Promise<Record<string, string>>;
}

/** A read of the remote's heads, or the reason there isn't one. */
export type RefSnapshot =
  | { ok: true; refs: Record<string, string> }
  | { ok: false; detail: string };

export const GITHUB_API_BASE_URL = "https://api.github.com";

/**
 * Pages one prefix's listing will walk before it gives up.
 *
 * GitHub's `matching-refs` ignores `page` and `per_page`: it answers every
 * matching ref at once and, when it does paginate, says so in a `Link`
 * header. The reader this replaced asked for `page=1..20` and stopped on a
 * short page, so a repository with 100 or more branches got the SAME full
 * listing twenty times and was refused as having "more than 2000 branches"
 * (tick hn6's run, with 281). So the walk follows `rel="next"` and nothing
 * else, and a chain longer than this is a refusal, not a trim: a truncated
 * listing is worse than none, because the branch that moved could be the one
 * past the cut and the comparison would answer "none" with confidence it has
 * not earned.
 */
export const MAX_REF_PAGES = 20;

/** The `rel="next"` target of a GitHub `Link` header, if it names one. */
export function nextPageUrl(link: string | null): string | null {
  if (link === null) return null;
  for (const part of link.split(",")) {
    const match = /<([^>]+)>\s*;\s*rel="?next"?/.exec(part);
    if (match) return match[1]!;
  }
  return null;
}

/** A branch prefix as a URL path, slash-separated segments each encoded. */
function prefixPath(prefix: string): string {
  return prefix.split("/").map(encodeURIComponent).join("/");
}

/**
 * The reader this deployment uses: a test's fake, or GitHub.
 *
 * Unlike the sandbox binding, an absent reader is not a broken deploy — it is a
 * run whose progress cannot be verified, which `snapshotRefs` reports as
 * `unknown` rather than failing the run.
 */
export function repoRefs(env: Env): RepoRefs {
  const injected = env.REPO_REFS;
  return injected === undefined || injected === null ? githubRepoRefs(env) : injected;
}

/**
 * GitHub's `matching-refs/heads/<prefix>` listing, one per prefix.
 *
 * Unauthenticated for a public repository and authenticated when the factory
 * holds the PAT it clones with; either way this is a read, and the token it
 * would use is already scoped to exactly this repository (D11).
 */
export function githubRepoRefs(env: Env): RepoRefs {
  const base = (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
  const baseHeaders: Record<string, string> = {
    accept: "application/vnd.github+json",
    // GitHub rejects an API request with no user agent outright.
    "user-agent": "ticks-factory",
  };

  return {
    async list(project: string, prefixes: readonly string[]): Promise<Record<string, string>> {
      const headers = { ...baseHeaders, ...(await githubAuthorization(env, project)) };
      const refs: Record<string, string> = {};
      for (const prefix of prefixes) {
        if (prefix === "") {
          // An empty prefix is every head on origin: the read this reader
          // exists to never make.
          throw new Error(`refusing to list every branch of ${project}: no prefix was named`);
        }
        let url: string | null =
          `${base}/repos/${project}/git/matching-refs/heads/${prefixPath(prefix)}`;
        let pages = 0;
        while (url !== null) {
          if (++pages > MAX_REF_PAGES) {
            throw new Error(
              `${project} answered more than ${MAX_REF_PAGES} pages of branches under ` +
                `${prefix}, so its refs cannot be compared without truncating the listing`,
            );
          }
          const response: Response = await fetch(url, { headers });
          if (!response.ok) {
            throw new Error(
              `GitHub answered HTTP ${response.status} for the branch listing of ${project} ` +
                `under ${prefix}`,
            );
          }
          const body = (await response.json()) as { ref?: string; object?: { sha?: string } }[];
          if (!Array.isArray(body)) {
            throw new Error(`GitHub returned a non-list branch listing for ${project}`);
          }
          for (const entry of body) {
            const ref = entry.ref;
            const sha = entry.object?.sha;
            if (typeof ref === "string" && typeof sha === "string") {
              refs[ref.replace(/^refs\/heads\//, "")] = sha;
            }
          }
          url = nextPageUrl(response.headers.get("link"));
        }
      }
      return refs;
    },
  };
}

/**
 * One read of the remote's heads under `prefixes`.
 *
 * Never throws: an unreadable remote is a fact about the record, not a reason
 * to fail a run that may well have done the work.
 */
export async function snapshotRefs(
  env: Env,
  project: string,
  prefixes: readonly string[],
): Promise<RefSnapshot> {
  try {
    return { ok: true, refs: await repoRefs(env).list(project, prefixes) };
  } catch (error) {
    return { ok: false, detail: String(error instanceof Error ? error.message : error) };
  }
}

// ------------------------------------------------------------ the comparison ---

/** Branches that appeared, moved or were deleted between two reads. */
export function changedBranches(
  before: Record<string, string>,
  after: Record<string, string>,
): string[] {
  const changed = new Set<string>();
  for (const [branch, sha] of Object.entries(after)) {
    if (before[branch] !== sha) changed.add(branch);
  }
  // A deletion counts. Merging an epic branch and cleaning it up is the
  // successful ending, and a comparison that only looked forward would read it
  // as a run that did nothing.
  for (const branch of Object.keys(before)) {
    if (!(branch in after)) changed.add(branch);
  }
  return [...changed].sort();
}

/** The verdict two reads of the remote support. */
export function compareSnapshots(before: RefSnapshot, after: RefSnapshot): RunProgress {
  if (!before.ok) {
    return {
      state: "unknown",
      detail: `the repository's branches could not be read when the run started: ${before.detail}`,
    };
  }
  if (!after.ok) {
    return {
      state: "unknown",
      detail: `the repository's branches could not be read when the run ended: ${after.detail}`,
    };
  }

  const changed = changedBranches(before.refs, after.refs);
  if (changed.length === 0) {
    return {
      state: "none",
      detail:
        "no branch on origin changed while the run was alive: nothing was committed, " +
        "pushed, or recorded on the tracker",
    };
  }

  const named = changed.slice(0, NAMED_BRANCHES).join(", ");
  const rest = changed.length - NAMED_BRANCHES;
  return {
    state: "advanced",
    detail:
      `${changed.length} branch${changed.length === 1 ? "" : "es"} on origin changed ` +
      `while the run was alive (${named}${rest > 0 ? `, and ${rest} more` : ""})`,
  };
}

/** The verdict for a run that never got far enough to read a baseline. */
export function unverifiedProgress(detail: string): RunProgress {
  return { state: "unknown", detail };
}
