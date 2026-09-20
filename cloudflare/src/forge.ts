/**
 * The code-hosting surface behind the PR + CI close-out rule a target
 * repository may declare in its own `.tick/config.md` — the TypeScript port
 * of `internal/forge` (tick cxk), widened by exactly the two reads the local
 * reconciler gets from its git checkout and this host cannot: the branch's
 * commit history and the paths a range of commits changed.
 *
 * Why the port exists at all: the Go `admitCloseout` and `gateCloseoutClose`
 * refuse a close-out whose epic PR is not CI-green, and the Workflow-hosted
 * reconciler had no counterpart — so a Workflow-hosted closeout job that
 * reported `done` closed like an implement tick, and the repository's own
 * PR + CI rule (the one `.tick/config.md` declares, and the Go side refuses
 * to run without) was simply absent on this host.
 *
 * The interface stays deliberately narrow — the same "everything a close-out
 * needs and nothing a future caller could abuse into a general GitHub
 * client" rule the Go package holds: this module never merges, never
 * comments, and never writes anything but the epic pull request the run's
 * own configuration demands.
 *
 * The walk's two extra reads exist because 9da's fix — gating on the newest
 * ancestor CI actually ran on when the head has no verdict, after proving
 * every commit between them touched only run state — is not optional on
 * this host: the run's durable state lives under `.ticfac/` ON the
 * integration branch the PR's head IS, so the act of gating rewrites the
 * thing being gated, exactly as it did locally. Without the walk the gate
 * would deadlock on its own checkpoint commits (closeout_ci.go documents
 * the live deadlock; the port must not reintroduce it).
 *
 * The GitHub implementation speaks GitHub's REST API the same way
 * `git-contents.ts` does: a bearer token from `GITHUB_TOKEN`, the base URL
 * overridable for a test proxy, and third-party tooling (`gh`, an App
 * installation) an optional rung, never a dependency.
 */

import type { Env } from "./index";
import { GITHUB_API_BASE_URL } from "./progress";

// ------------------------------------------------------------ the seam ---

/**
 * The epic integration PR: the head ref this run integrates on, the base ref
 * it asks to merge into, and where the forge says it lives.
 */
export type PullRequest = {
  number: number;
  url: string;
  head_ref: string;
  head_sha: string;
  base_ref: string;
};

/**
 * What CI says on one commit, closed. The vocabulary is closed because each
 * member sends the next repair somewhere different: `green` admits a
 * close-out, `red` is a failing job to NAME — the whole point of the typed
 * refusal that consumes it — `pending` is a wait the run bounds, and `none`
 * is a workflow that never ran on the PR at all.
 */
export type CIState = "green" | "red" | "pending" | "none";

/** CI's answer on one commit: its state, and the NAMES of the jobs that failed. */
export type CIReport = { state: CIState; failing: string[] };

/**
 * The seam the close-out rule needs: find the PR for the epic branch, open
 * one if none exists, ask what CI says on a commit, write the body the PR
 * carries, and — the walk's needs — read the branch's commit history and the
 * paths a range of commits changed.
 *
 * `find` answers null when no open PR exists for the head: "no PR yet" is
 * the state the rule is about, not a failure. GitHub allows one open PR per
 * head branch, so `open` is idempotent against `find` the way the forge
 * enforces it: a resumed run cut between the two finds the one the previous
 * incarnation opened.
 *
 * `updateBody` is an EDIT of the PR's body rather than a comment, on
 * purpose (the Go seam's own argument, tick 4sb): the durable record is the
 * run's state, and the body is a VIEW of that record, recomposed and
 * overwritten — an append is a shape no resume survives honestly, while an
 * overwrite is idempotent by construction.
 *
 * `ancestors` returns the branch's commits, newest first, bounded by
 * `limit`; an unreadable history is an empty list, never an error, because
 * the walk simply finds nothing and falls back to the head's own answer.
 * `changedPaths` returns the paths that changed between two commits, or
 * null when it cannot be read — and null is "changed", because a gate that
 * cannot prove the tree is unchanged does not get to assume it.
 */
export interface PullRequests {
  find(headRef: string, baseRef: string): Promise<PullRequest | null>;
  open(input: {
    headRef: string;
    baseRef: string;
    title: string;
    body: string;
  }): Promise<PullRequest>;
  updateBody(pr: PullRequest, body: string): Promise<void>;
  ci(headSHA: string): Promise<CIReport>;
  ancestors(headRef: string, limit: number): Promise<string[]>;
  changedPaths(from: string, to: string): Promise<string[] | null>;
}

// ------------------------------------------------- the classification ---

/**
 * Reads GitHub's check runs for one commit into a report. The
 * classification is the Go `forge.GitHub.CI` port, closed and conservative:
 * a check that has not concluded leaves the whole report `pending` (a green
 * beside a running one is not a verdict), a concluded check fails the report
 * when its conclusion names a failure, `neutral` and `skipped` neither pass
 * nor fail it, and everything else (`cancelled`, `action_required`, `stale`)
 * reads as pending — a state a person can fix by re-running — unless
 * something already failed outright.
 */
export function classifyCheckRuns(
  runs: Array<{ name: string; status: string; conclusion: string }>,
): CIReport {
  if (runs.length === 0) return { state: "none", failing: [] };
  let state: CIState = "green";
  const failing: string[] = [];
  for (const run of runs) {
    if (run.status !== "completed") {
      state = "pending";
      continue;
    }
    switch (run.conclusion) {
      case "failure":
      case "timed_out":
        if (state !== "pending") state = "red";
        failing.push(run.name);
        break;
      case "success":
      case "neutral":
      case "skipped":
        break; // neither fails the report nor rescues a pending one
      default:
        if (state === "green") state = "pending";
    }
  }
  return { state, failing };
}

// ------------------------------------------------------- the deployment ---

/** A non-2xx answer, carrying the status and the API's own first-line message. */
function refusal(method: string, path: string, status: number, message: string): Error {
  return new Error(`GitHub answered ${status} for ${method} ${path}: ${message}`);
}

/**
 * The GitHub REST API as a `PullRequests` seam, or null when this
 * deployment holds no token — the supported state the reconciler refuses a
 * rule-declaring repository against, exactly the way `forge.ResolveToken`'s
 * empty answer is (the Go comment: an operator learns to provision one from
 * the refusal, not a startup crash).
 */
export function pullRequestsFromEnv(
  env: Pick<Env, "GITHUB_TOKEN" | "GITHUB_API_BASE_URL">,
  project: string,
): PullRequests | null {
  const token = typeof env.GITHUB_TOKEN === "string" ? env.GITHUB_TOKEN.trim() : "";
  if (token === "") return null;
  const base = (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
  const headers: Record<string, string> = {
    accept: "application/vnd.github+json",
    authorization: `Bearer ${token}`,
    "x-github-api-version": "2022-11-28",
    "user-agent": "ticfac",
  };
  const owner = project.split("/")[0];

  /** One REST call; a successful body decoded as JSON, errors thrown typed. */
  async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const response = await fetch(`${base}${path}`, {
      method,
      headers: body === undefined ? headers : { ...headers, "content-type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const raw = await response.text();
    if (!response.ok) {
      let message = "";
      try {
        message = String((JSON.parse(raw) as { message?: unknown }).message ?? "");
      } catch {
        message = raw;
      }
      const first = message.split("\n")[0] ?? "";
      throw refusal(method, path, response.status, first);
    }
    return (raw === "" ? undefined : JSON.parse(raw)) as T;
  }

  const prFrom = (payload: {
    number?: unknown;
    html_url?: unknown;
    head?: { ref?: unknown; sha?: unknown };
    base?: { ref?: unknown };
  }): PullRequest => {
    const headRef = typeof payload.head?.ref === "string" ? payload.head.ref : "";
    const headSHA = typeof payload.head?.sha === "string" ? payload.head.sha : "";
    if (typeof payload.number !== "number" || headRef === "" || headSHA === "") {
      throw new Error(`GitHub served a pull request this reader cannot address (${project})`);
    }
    return {
      number: payload.number,
      url: typeof payload.html_url === "string" ? payload.html_url : "",
      head_ref: headRef,
      head_sha: headSHA,
      base_ref: typeof payload.base?.ref === "string" ? payload.base.ref : "",
    };
  };

  return {
    async find(headRef, _baseRef) {
      // _baseRef is carried for the caller's record, not matched here: the
      // one open PR for a branch is the PR for that branch (the Go Find rule).
      const found = await call<PullRequest[]>(
        "GET",
        `/repos/${project}/pulls?head=${encodeURIComponent(`${owner}:${headRef}`)}&state=open`,
      );
      for (const payload of found ?? []) {
        const pr = prFrom(payload);
        if (pr.head_ref === headRef) return pr;
      }
      return null;
    },

    async open(input) {
      return prFrom(
        await call("POST", `/repos/${project}/pulls`, {
          title: input.title,
          head: input.headRef,
          base: input.baseRef,
          body: input.body,
        }),
      );
    },

    async updateBody(pr, body) {
      if (pr.number === 0) throw new Error("the PR to rewrite names no number to address");
      await call("PATCH", `/repos/${project}/pulls/${pr.number}`, { body });
    },

    async ci(headSHA) {
      if (headSHA === "") throw new Error("no head sha to read CI from");
      const answer = await call<{
        check_runs?: Array<{ name?: unknown; status?: unknown; conclusion?: unknown }>;
      }>("GET", `/repos/${project}/commits/${encodeURIComponent(headSHA)}/check-runs`);
      const runs: Array<{ name: string; status: string; conclusion: string }> = [];
      for (const run of answer?.check_runs ?? []) {
        runs.push({
          name: typeof run.name === "string" ? run.name : "",
          status: typeof run.status === "string" ? run.status : "",
          conclusion: typeof run.conclusion === "string" ? run.conclusion : "",
        });
      }
      return classifyCheckRuns(runs);
    },

    async ancestors(headRef, limit) {
      // Best effort by contract: an unreadable history is an empty list, so
      // the walk finds nothing and the head's own answer stands.
      try {
        const history = await call<Array<{ sha?: unknown }>>(
          "GET",
          `/repos/${project}/commits?sha=${encodeURIComponent(headRef)}&per_page=${limit}`,
        );
        const shas: string[] = [];
        for (const commit of history ?? []) {
          if (typeof commit?.sha === "string" && commit.sha !== "") shas.push(commit.sha);
        }
        return shas;
      } catch {
        return [];
      }
    },

    async changedPaths(from, to) {
      // Unreadable is null, and null is "changed": this is the proof that
      // lets an older verdict stand for this tree, and an unreadable proof
      // is not a proof.
      try {
        const compare = await call<{ files?: Array<{ filename?: unknown }> }>(
          "GET",
          `/repos/${project}/compare/${encodeURIComponent(from)}...${encodeURIComponent(to)}`,
        );
        const paths: string[] = [];
        for (const file of compare?.files ?? []) {
          if (typeof file.filename === "string") paths.push(file.filename);
        }
        return paths;
      } catch {
        return null;
      }
    },
  };
}
