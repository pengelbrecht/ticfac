/**
 * One tracked-file store, the mechanism every durable read and write this
 * Workflow host makes through GitHub's contents API goes through.
 *
 * SPEC §3.1 (line 191) and `contracts/tk-json-manifest.json`'s `hosts`
 * section say what this seam is FOR: a Cloudflare Workflow or isolate cannot
 * execute a Go binary, so on this host the reconciler cannot shell out to tk.
 * `tk --json` defines the tracker contract; a host that cannot run tk
 * implements the same contract in its own language — reading `.tick/` records
 * through the contents API and writing tracker commits the same way — and
 * proves it against the pinned fixtures. This module is deliberately the
 * narrowest possible mechanism for that: list, read, create, update, each
 * against one ref of one repository, with the contents API's own
 * compare-and-swap semantics surfaced as answers rather than exceptions.
 *
 * Two consumers, one mechanism, so the rules cannot drift between them:
 *
 *  - `tracker-client.ts` reaches `.tick/issues/**` on the run branch — the
 *    reads and controlled writes of the tk --json contract.
 *  - `run-state-store.ts` reaches `.ticfac/runs/<run-id>/**` on the same
 *    branch — the checkpoint (SHA-guarded update) and the attempt markers
 *    (create-if-absent), exactly the two CAS modes
 *    `contracts/ticfac-run-state.json` pins.
 *
 * A test assigns an in-memory store; a deployment gets the GitHub one below.
 */

import type { Env } from "./index";
import { GITHUB_API_BASE_URL } from "./progress";

// ------------------------------------------------------------- the seam ---

/** One tracked file as the contents API serves it: bytes and their blob sha. */
export type StoredFile = {
  content: string;
  sha: string;
};

/**
 * What one write attempt did. Every case a caller must tell apart is its own
 * state, because they are three different answers about one ref:
 *
 *  - `written`: committed. `commit_sha` names the commit the ref now points at.
 *  - `exists`: the path already holds a file and this create supplied no sha.
 *    For a create-if-absent marker this is the REPOSITORY refusing a second
 *    dispatch of the same attempt — the idempotency rule, not an error.
 *  - `conflict`: the ref moved between the read and the write. Nothing was
 *    committed. A retry, not a failure.
 *  - `missing`: an update named a path the ref does not hold. The caller's
 *    view of the ref is stale or the record never existed.
 */
export type StoreWrite =
  | { state: "written"; commit_sha: string; content_sha: string }
  | { state: "exists"; detail: string }
  | { state: "conflict"; detail: string }
  | { state: "missing"; detail: string };

/**
 * A store of tracked files at one ref of one repository.
 *
 * The ref is fixed at construction, not passed per call: every read and write
 * this Workflow makes is against the RUN BRANCH (the EpicRun's integration
 * branch, where `.tick/` and `.ticfac/` are one push away from every worker
 * that needs them), and a call-site-choosable ref is a call site that can
 * read the tracker from somewhere it did not write it to.
 */
export interface ContentsStore {
  /** The tracked file paths under `prefix`, in path order. Empty when none. */
  list(prefix: string): Promise<string[]>;
  /** One file, or null when the ref does not hold it. */
  read(path: string): Promise<StoredFile | null>;
  /** Create one new file. The ref's head is the compare-and-swap. */
  create(path: string, input: { content: string; message: string }): Promise<StoreWrite>;
  /**
   * Replace one file whose blob sha is `sha`. The contents API is a
   * compare-and-swap on both the ref and the blob: a ref that moved OR a blob
   * that changed under the write is a refusal, never a lost update.
   */
  update(
    path: string,
    sha: string,
    input: { content: string; message: string },
  ): Promise<StoreWrite>;
}

// -------------------------------------------------------- the deployment ---

/**
 * Base64 of a UTF-8 string, which `btoa` alone is not.
 *
 * Identical to the copy in `tracker-write.ts`; kept local rather than imported
 * so this module has no consumer-specific imports — it is the mechanism layer
 * both consumers build on.
 */
export function base64Utf8(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(binary);
}

function decodeBase64Utf8(text: string, encoding: string): string {
  if (encoding !== "base64") {
    throw new Error(`GitHub served a contents payload encoded as ${encoding}, not base64`);
  }
  const binary = atob(text.replace(/\s+/g, ""));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

/**
 * GitHub's contents API as a `ContentsStore`.
 *
 * The status codes are the contract (the same ones `tracker-write.ts` pinned
 * for its create-only writer):
 *
 *  - 200: read. 404: absent — an answer, not an error.
 *  - 201: created or updated; the response names the blob and the commit.
 *  - 422 on a create with no sha: the path is already a file.
 *  - 409: the ref (or the blob an update guards) moved. Nothing committed.
 *  - 404 on an update: the path the caller believes in is not on this ref.
 *  - anything else: thrown, because it is not an answer about this write.
 */
export function githubContentsStore(env: Env, project: string, ref: string): ContentsStore {
  const base = (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
  const headers: Record<string, string> = {
    accept: "application/vnd.github+json",
    "content-type": "application/json",
    "user-agent": "ticks-factory",
  };
  const token = env.GITHUB_TOKEN;
  if (typeof token === "string" && token.trim() !== "") {
    headers.authorization = `Bearer ${token.trim()}`;
  }

  const entryURL = (path: string) =>
    `${base}/repos/${project}/contents/${path}?ref=${encodeURIComponent(ref)}`;

  async function getJSON(url: string, method: "GET" = "GET"): Promise<Response> {
    return fetch(url, { method, headers });
  }

  async function put(path: string, body: Record<string, unknown>): Promise<StoreWrite> {
    const response = await fetch(entryURL(path), {
      method: "PUT",
      headers,
      body: JSON.stringify(body),
    });
    if (response.status === 409) {
      return {
        state: "conflict",
        detail: `GitHub answered 409 writing ${path} to ${project} at ${ref}: the ref or the guarded blob moved, nothing was committed`,
      };
    }
    if (response.status === 422) {
      return { state: "exists", detail: `${path} already exists in ${project} at ${ref}` };
    }
    if (response.status === 404) {
      return { state: "missing", detail: `${path} is not on ${project} at ${ref}` };
    }
    if (!response.ok) {
      throw new Error(
        `GitHub answered HTTP ${response.status} writing ${path} to ${project} at ${ref}`,
      );
    }
    const payload = (await response.json()) as {
      content?: { sha?: unknown };
      commit?: { sha?: unknown };
    };
    const contentSHA = typeof payload.content?.sha === "string" ? payload.content.sha : "";
    const commitSHA = typeof payload.commit?.sha === "string" ? payload.commit.sha : "";
    if (commitSHA === "") {
      // A 2xx with no commit is not a commit. Reporting it as one would put a
      // marker into the run's durable state for a dispatch that did not land
      // — and the retry that would have fixed it is exactly what the marker
      // then suppresses.
      throw new Error(`GitHub accepted ${path} in ${project} at ${ref} but named no commit`);
    }
    return { state: "written", commit_sha: commitSHA, content_sha: contentSHA };
  }

  return {
    async list(prefix) {
      const response = await getJSON(
        `${base}/repos/${project}/contents/${prefix}?ref=${encodeURIComponent(ref)}`,
      );
      if (response.status === 404) return [];
      if (!response.ok) {
        throw new Error(
          `GitHub answered HTTP ${response.status} listing ${prefix} in ${project} at ${ref}`,
        );
      }
      const entries = (await response.json()) as Array<{
        type?: unknown;
        path?: unknown;
      }>;
      const paths: string[] = [];
      for (const entry of entries) {
        if (entry.type !== "file" || typeof entry.path !== "string") continue;
        paths.push(entry.path);
      }
      paths.sort();
      return paths;
    },

    async read(path) {
      const response = await getJSON(entryURL(path));
      if (response.status === 404) return null;
      if (!response.ok) {
        throw new Error(
          `GitHub answered HTTP ${response.status} reading ${path} in ${project} at ${ref}`,
        );
      }
      const payload = (await response.json()) as {
        content?: unknown;
        sha?: unknown;
        encoding?: unknown;
      };
      if (
        typeof payload.content !== "string" ||
        typeof payload.sha !== "string" ||
        typeof payload.encoding !== "string"
      ) {
        throw new Error(`GitHub served no file body for ${path} in ${project} at ${ref}`);
      }
      return { content: decodeBase64Utf8(payload.content, payload.encoding), sha: payload.sha };
    },

    async create(path, input) {
      return put(path, {
        message: input.message,
        content: base64Utf8(input.content),
      });
    },

    async update(path, sha, input) {
      return put(path, {
        message: input.message,
        content: base64Utf8(input.content),
        sha,
      });
    },
  };
}

/** The store this deployment uses for `ref`: a test's fake, or GitHub's. */
export function contentsStore(env: Env, project: string, ref: string): ContentsStore {
  const injected = env.TICK_CONTENTS;
  if (injected !== undefined && injected !== null) {
    // A test's fake stands in for ONE (project, ref); make a mismatch loud
    // rather than letting a fake silently serve a different repository's view.
    if (injected.project === project && injected.ref === ref) return injected.store;
    throw new Error(
      `the injected contents store serves ${injected.project} at ${injected.ref}, ` +
        `not ${project} at ${ref}`,
    );
  }
  return githubContentsStore(env, project, ref);
}
