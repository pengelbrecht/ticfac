/**
 * The one git write this host's attempt executor makes: putting a pushed
 * branch's head on the attempt's write_ref (tick us2).
 *
 * WHY THIS EXISTS. job-protocol.json's `collect` reads "terminal facts from
 * durable source refs" — and the attempt's identity IS its ref
 * (`refs/heads/ticfac/run-<run>/tick-<tick>/attempt-<n>`, the vocabulary the
 * marker, the settle's integrate call and a person reading the run all name).
 * The LOCAL executor guarantees the work lands there by construction: it
 * creates the worker's worktree ON the write ref and pushes the ref itself
 * ("this attempt is the only writer of its own ref", pushBranch in
 * internal/exec/subprocess/git.go).
 *
 * The cloud worker container CANNOT push that ref: its branch is derived
 * inside the vendored image from `tick/${TICKS_EPIC}/${TICKS_TICK}` alone
 * (image/worker.sh worker_branch_name, pinned by
 * contracts/worker-boot-contract.json), so the control plane's only lever on
 * where a container lands is the epic slot — which the boot fills with a
 * per-attempt value (worker-boot.ts `attemptLandingBranch`). The executor then
 * makes the attempt's own push at collect: the container's landing branch
 * head goes onto the attempt's write_ref, exactly the push the local
 * executor makes, and the collect reads the write_ref from there on. Plain,
 * never forced — the same rule the local push keeps, because the attempt is
 * the only writer of its own ref and a non-fast-forward is something to fail
 * loudly on rather than overwrite.
 *
 * A seam for the same reason `WorkerCollector` is one: the ordering — read
 * the landing branch, create-or-advance the write_ref, never force — is what
 * needs testing, and a push exercisable only against a live GitHub is a
 * push nobody tests.
 */

import type { Env } from "./index";

export const GITHUB_API_BASE_URL = "https://api.github.com";

// ------------------------------------------------------------- the seam ---

/** What one put did. Every case a caller must tell apart is its own state. */
export type RefPut =
  /** The write_ref did not exist; it was created at the landing branch's head. */
  | { state: "created"; sha: string }
  /** The write_ref existed behind; it was advanced, fast-forward only. */
  | { state: "advanced"; sha: string }
  /** The write_ref already carries the landing branch's head: a no-op. */
  | { state: "already"; sha: string }
  /** The landing branch is not on origin: nothing to put. */
  | { state: "missing" }
  /** The put could not be made. NEVER a clean verdict — name what happened. */
  | { state: "refused"; detail: string };

/** Puts one pushed branch's head on a ref of origin, create-or-advance. */
export interface GitRefWriter {
  put(input: { branch: string; ref: string }): Promise<RefPut>;
}

/**
 * `refs/heads/<branch>` — the branch name a ref spells, the form the collect
 * API and `tick/<epic>/<tick>` spellings speak.
 */
export function writeRefBranch(ref: string): string {
  return ref.replace(/^refs\/heads\//, "");
}

// ------------------------------------------------------- the GitHub writer ---

function apiBase(env: Env): string {
  return (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
}

function headers(env: Env): Record<string, string> {
  const out: Record<string, string> = {
    accept: "application/vnd.github+json",
    "user-agent": "ticks-factory",
  };
  const token = env.GITHUB_TOKEN;
  if (typeof token === "string" && token.trim() !== "") {
    out.authorization = `Bearer ${token.trim()}`;
  }
  return out;
}

type RefRead = { ok: true; sha: string } | { ok: false; missing: boolean; detail: string };

/** GET /git/ref/heads/<branch> — the branch's head, or its absence. */
async function readBranchHead(env: Env, project: string, branch: string): Promise<RefRead> {
  const url = `${apiBase(env)}/repos/${project}/git/ref/${encodeURIComponent(`heads/${branch}`)}`;
  const response = await fetch(url, { headers: headers(env) });
  if (response.status === 404)
    return { ok: false, missing: true, detail: `${branch} is not on origin` };
  if (!response.ok) {
    return {
      ok: false,
      missing: false,
      detail: `GitHub answered HTTP ${response.status} reading ${branch}`,
    };
  }
  const body = (await response.json()) as { object?: { sha?: string } };
  if (typeof body.object?.sha !== "string" || body.object.sha === "") {
    return { ok: false, missing: false, detail: `GitHub returned no head for ${branch}` };
  }
  return { ok: true, sha: body.object.sha };
}

type RefCreate = { ok: true } | { ok: false; exists: boolean; detail: string };

/** POST /git/refs — create the ref at a head, once. */
async function createRef(env: Env, project: string, ref: string, sha: string): Promise<RefCreate> {
  const response = await fetch(`${apiBase(env)}/repos/${project}/git/refs`, {
    method: "POST",
    headers: { ...headers(env), "content-type": "application/json" },
    body: JSON.stringify({ ref, sha }),
  });
  if (response.ok) return { ok: true };
  const exists = response.status === 422;
  return { ok: false, exists, detail: `GitHub answered HTTP ${response.status} creating ${ref}` };
}

type RefUpdate = { ok: true } | { ok: false; detail: string };

/** PATCH /git/refs/heads/<branch> — advance the ref, fast-forward only. */
async function updateRef(env: Env, project: string, ref: string, sha: string): Promise<RefUpdate> {
  const branch = writeRefBranch(ref);
  const response = await fetch(
    `${apiBase(env)}/repos/${project}/git/refs/${encodeURIComponent(`heads/${branch}`)}`,
    {
      method: "PATCH",
      headers: { ...headers(env), "content-type": "application/json" },
      // force is the whole rule: absent means fast-forward only, and a
      // refusal here is reported rather than forced over.
      body: JSON.stringify({ sha, force: false }),
    },
  );
  if (response.ok) return { ok: true };
  return { ok: false, detail: `GitHub answered HTTP ${response.status} advancing ${ref}` };
}

/** The writer a deployment uses: GitHub's git-data API against origin. */
export function gitRefWriter(env: Env, project: string): GitRefWriter {
  return {
    async put(input: { branch: string; ref: string }): Promise<RefPut> {
      let landing: RefRead;
      let written: RefRead;
      try {
        landing = await readBranchHead(env, project, input.branch);
      } catch (error) {
        return { state: "refused", detail: `reading ${input.branch} raised ${String(error)}` };
      }
      if (!landing.ok) {
        return landing.missing
          ? { state: "missing" }
          : { state: "refused", detail: landing.detail };
      }
      try {
        written = await readBranchHead(env, project, writeRefBranch(input.ref));
      } catch (error) {
        return { state: "refused", detail: `reading ${input.ref} raised ${String(error)}` };
      }
      if (written.ok && written.sha === landing.sha) return { state: "already", sha: landing.sha };
      if (written.ok) {
        const advanced = await updateRef(env, project, input.ref, landing.sha);
        return advanced.ok
          ? { state: "advanced", sha: landing.sha }
          : { state: "refused", detail: advanced.detail };
      }
      if (!written.missing) return { state: "refused", detail: written.detail };
      const created = await createRef(env, project, input.ref, landing.sha);
      return created.ok
        ? { state: "created", sha: landing.sha }
        : { state: "refused", detail: created.detail };
    },
  };
}
