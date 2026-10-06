/**
 * The staging git origin (epic 43y, tick a2l): one Durable Object per proof
 * attempt, holding the attempt's throwaway bare repository in the object's
 * own storage — the origin that OUTLIVES THE BOX, so a container destroyed
 * mid-turn has something to fetch its workspace back from.
 *
 * Why the repository lives HERE (the staging stand-in's third deviation from
 * production, named with the other two in src/staging-agent.ts): the factory
 * reaches GitHub, staging carries no repository of its own, and the
 * previous stand-in seeded its bare origin INSIDE the container
 * (`/srv/origin.git`), so destroying the container destroyed the origin with
 * it and the [A2] restore could never run on staging. A container volume
 * would outlive the box, but the pinned container platform has no volumes
 * (wrangler 4.147's container config schema carries no volumes key at all),
 * so the origin is the one thing the container reaches over the network.
 *
 * The protocol is git's dumb HTTP (harness/src/workspace/dumb-git-origin.ts):
 * fetch is plain file reads, push is git-http-push's WebDAV dance — all over
 * the narrow OriginStore, so the same core serves this object and the node
 * suite's real-git proof of the wire itself.
 *
 * The credential is the attempt's own worker run token, carried IN THE REPO
 * URL the start route hands out (`/proof/git/<attempt>/<run token>/…`):
 * possession of the URL is the credential, exactly like the factory's own
 * read-only git door hands its run token to git as a password
 * (src/credentials.ts `runGitEndpoint`). No 401 is ever issued (git's dumb
 * push cannot answer one — its locking probe PROPFIND does not retry with
 * credentials, verified against git 2.50), so git needs no credential helper:
 * the restore's production git lines run unchanged. The cost is named: the
 * token is readable in `.git/config` by anything that can read the
 * workspace — on staging that is the model itself, holding the same run's
 * token that the gateway would take, and the Worker is torn down with its
 * tokens when the proof is over.
 */

import { DurableObject } from "cloudflare:workers";
import { DumbGitOrigin, type OriginStore } from "ticfac-harness";
import type { Env } from "./index";

/** Where this origin's run token lives in its own storage. */
const TOKEN_KEY = "staging-git-origin:token";

/**
 * The storage subset this object uses, structurally — the same shape
 * factory-sandbox.ts gives its core, so the workerd suite drives the real
 * logic over a fake the way it drives FactorySandboxCore.
 */
export type StagingOriginStorage = {
  get(key: string): Promise<unknown>;
  put(key: string, value: ArrayBuffer | Uint8Array): Promise<void>;
  delete(key: string): Promise<boolean>;
  list(options: { prefix: string }): Promise<Map<string, unknown>>;
};

/** The run token one origin answers to, as UTF-8 bytes or nothing. */
async function storedToken(storage: StagingOriginStorage): Promise<string | null> {
  const value = await storage.get(TOKEN_KEY);
  if (value === undefined || value === null) return null;
  if (typeof value === "string") return value;
  if (value instanceof ArrayBuffer) return new TextDecoder().decode(value);
  if (ArrayBuffer.isView(value)) {
    return new TextDecoder().decode(value as Uint8Array);
  }
  return null;
}

/** Constant-time enough: lengths first, then every byte (staging-agent's rule). */
function sameToken(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

/** The object's storage as the runtime-neutral core's OriginStore. */
function originStore(storage: StagingOriginStorage): OriginStore {
  return {
    async get(key) {
      const value = await storage.get(key);
      if (value === undefined || value === null) return null;
      if (value instanceof ArrayBuffer) return new Uint8Array(value);
      if (ArrayBuffer.isView(value)) {
        const view = value as Uint8Array;
        return new Uint8Array(
          view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength),
        );
      }
      return null;
    },
    async put(key, value) {
      await storage.put(key, value);
    },
    async delete(key) {
      await storage.delete(key);
    },
    async list(prefix) {
      const keys = [...(await storage.list({ prefix })).keys()];
      keys.sort();
      return keys;
    },
  };
}

/**
 * One git request, as the staging Worker forwarded it: the path names this
 * origin's attempt and its run token, and everything after the token is the
 * repository the dumb protocol serves. Structural over the storage, so the
 * workerd suite pins the whole answer without a container.
 */
export async function stagingGitOriginFetch(
  storage: StagingOriginStorage,
  request: Request,
): Promise<Response> {
  const segments = new URL(request.url).pathname.split("/").filter((s) => s !== "");
  // ["proof", "git", "<attempt>", "<run token>", …repository path]. The
  // repository path may be empty — the dumb push's first request is the
  // PROPFIND of the repository root.
  if (segments.length < 4 || segments[0] !== "proof" || segments[1] !== "git") {
    return new Response(null, { status: 404 });
  }
  const token = segments[3] ?? "";
  const expected = await storedToken(storage);
  // A wrong or absent token is a 404, never a 401: this origin does not
  // challenge (git's dumb push cannot answer one), and a 404 says "no such
  // repository" to a guesser who has not earned "wrong token".
  if (expected === null || token === "" || !sameToken(token, expected)) {
    return new Response(null, { status: 404 });
  }
  const repoPrefix = `/${segments.slice(0, 4).join("/")}`;
  const origin = new DumbGitOrigin({ store: originStore(storage), repoPrefix });
  return origin.handle(request);
}

/**
 * The Durable Object the staging agent's `GIT_ORIGINS` binding addresses, one
 * per proof attempt by name: its throwaway repository, its own run token as
 * the credential, and the git protocol over both.
 */
export class StagingGitOrigin extends DurableObject<Env> {
  /**
   * The start route's own record of which run token this origin answers to.
   * Called as RPC (`stub.init(token)`) before the attempt starts, so the
   * URL it hands the container is the only URL that works.
   */
  async init(token: string): Promise<void> {
    if (token === "") throw new Error("staging git origin: no token to answer");
    await this.ctx.storage.put(TOKEN_KEY, token);
  }

  override async fetch(request: Request): Promise<Response> {
    return stagingGitOriginFetch(this.ctx.storage, request);
  }
}

/** The RPC surface the Worker calls, structurally. */
export type StagingGitOriginStub = {
  init(token: string): Promise<void>;
  fetch(request: Request): Promise<Response>;
};

/** The GIT_ORIGINS namespace, structurally. */
export type StagingGitOriginNamespace = {
  idFromName(name: string): DurableObjectId;
  get(id: DurableObjectId): StagingGitOriginStub;
};
