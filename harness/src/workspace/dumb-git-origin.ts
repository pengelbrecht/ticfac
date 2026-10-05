/**
 * A dumb-HTTP git origin (epic 43y, tick a2l): a file store that speaks the
 * wire protocol git's own dumb HTTP client speaks, so a THROWAWAY repository
 * can live where a container cannot take it — the staging Worker's Durable
 * Object storage — while the container's git, real git, pushes and fetches
 * to it unchanged over the network.
 *
 * WHY THIS SHAPE. The staging stand-in (`cloudflare/staging/agent.Dockerfile`)
 * used to seed its bare origin INSIDE the container (`/srv/origin.git`), so a
 * container destroyed mid-turn took its own origin with it and the restore
 * had nothing to fetch back: the [A2] mid-turn destroy could not be proven on
 * staging at all. The two durable options the tick names are a container
 * volume (the platform's container config — wrangler 4.147's schema, pinned
 * by cloudflare/pnpm-lock.yaml — has no volumes key at all, so that route
 * does not exist) and an origin the container reaches over the network. A
 * network origin for real git without a server-side git binary is exactly
 * git's DUMB protocol:
 *
 * - FETCH and CLONE are plain file reads: `info/refs` (the ref listing a
 *   dumb server would keep current with `git update-server-info` — here
 *   SYNTHESIZED from the stored refs on every read, so it can never go
 *   stale), `HEAD`, and one GET per loose object.
 * - PUSH is git's dumb WebDAV client (`git-http-push`, still in git 2.50,
 *   verified live against this core in harness/test/node/dumb-git-origin.test.ts):
 *   it PROPFINDs for lock support, LOCKs the ref and the server info, PUTs
 *   each missing object under a temp name and MOVEs it into place, PUTs the
 *   ref, and rewrites `info/refs` itself. The server is a FILE STORE: no
 *   pack parsing, no negotiation, no git server-side at all.
 * - The ref listing (`GET <repo>/refs/`, an HTML index git scrapes) is what
 *   lets the client enforce fast-forward on a plain push — without it every
 *   push is a blind overwrite. The wip checkpoint's push is forced BY DESIGN
 *   (its snapshot is a commit on top of the agent's HEAD, replaced every
 *   round); the finish phase's push is not.
 *
 * The credential it rides is in the URL the caller hands out (the staging
 * Worker puts the attempt's run token in the repo's path), so this core
 * answers no challenge: git only consults a credential helper when a remote
 * 401s, and this one never does. What the token is and who checks it is the
 * caller's — the Durable Object glue in cloudflare/src/staging-git-origin.ts.
 *
 * Runtime-neutral on purpose, like the checkpoint code it serves
 * (./checkpoints.ts): everything runs over the narrow {@link OriginStore} and
 * the standard Request/Response, so the same code serves the staging Worker's
 * Durable Object (where /work/repo is a container a destroy can take) and the
 * node suite, where real git drives it over a real socket.
 */

// ------------------------------------------------------------- the store ---

/**
 * The byte store an origin is: `get`, `put`, `delete`, and every key under a
 * prefix. All values are bytes — a loose object is zlib, a ref is its sha —
 * so the same interface drives Durable Object storage and a test's Map.
 */
export type OriginStore = {
  get(key: string): Promise<Uint8Array | null>;
  put(key: string, value: Uint8Array): Promise<void>;
  delete(key: string): Promise<void>;
  /** Every stored key that starts with `prefix`, sorted. */
  list(prefix: string): Promise<string[]>;
};

// ------------------------------------------------------------ the origin ---

/** What one origin is built over: its store, and the URL path it is served at. */
export type DumbGitOriginOptions = {
  readonly store: OriginStore;
  /**
   * The repository's own URL path, exactly as the client addresses it —
   * `/origin.git` behind a plain file server, the staging Worker's
   * `/proof/git/<attempt>/<run token>` on the platform. The directory listing
   * and PROPFIND responses carry it in their hrefs (git's parsers scrape
   * hrefs, not names, and the shapes below are the ones it was verified
   * against — do not "simplify" them).
   */
  readonly repoPrefix: string;
};

/** The ref the origin tells a client to clone: the stand-in's default branch. */
export const ORIGIN_HEAD_REF = "refs/heads/main";

const DECODER = new TextDecoder();

/** A plain refusal: the file store has no such path. */
const notFound = () => new Response(null, { status: 404 });

/** A WebDAV success with nothing to say: MKCOL of a directory that needs no creating. */
const created = () => new Response(null, { status: 201 });

/** An unlocked resource. */
const noContent = () => new Response(null, { status: 204 });

/** XML, as every PROPFIND and LOCK answer is. */
function xml(status: number, body: string): Response {
  return new Response(body, { status, headers: { "content-type": "application/xml" } });
}

/** One `<D:response>` of a multistatus: the path, and that it may be LOCKed. */
function propfindResponse(href: string): string {
  return (
    `<D:response><D:href>${href}</D:href><D:propstat><D:prop>` +
    "<D:supportedlock><D:lockentry>" +
    "<D:locktype><D:write/></D:locktype>" +
    "<D:lockscope><D:exclusive/></D:lockscope>" +
    "</D:lockentry></D:supportedlock>" +
    "</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>"
  );
}

/** The LOCK answer git accepts: an exclusive write lock it can later UNLOCK. */
const LOCK_BODY =
  '<?xml version="1.0"?>' +
  '<D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>' +
  "<D:locktype><D:write/></D:locktype>" +
  "<D:lockscope><D:exclusive/></D:lockscope>" +
  "<D:timeout>Second-600</D:timeout>" +
  "<D:owner><D:href>https://example.com/git</D:href></D:owner>" +
  "<D:locktoken><D:href>opaquelocktoken:ticfac-dumb-origin</D:href></D:locktoken>" +
  "</D:activelock></D:lockdiscovery></D:prop>";

/**
 * A dumb-HTTP git origin: one repository, over one {@link OriginStore}. One
 * instance per request is fine — everything it holds is the options.
 */
export class DumbGitOrigin {
  private readonly store: OriginStore;
  private readonly repoPrefix: string;

  constructor(options: DumbGitOriginOptions) {
    this.store = options.store;
    this.repoPrefix = options.repoPrefix.replace(/\/+$/, "");
  }

  /**
   * One git request, as it arrived: the URL carries the repository path (the
   * URL the caller hands out — clone, fetch and push all address it), the
   * method is the verb, and the body is read in full before the verb runs.
   */
  async handle(request: Request): Promise<Response> {
    const path = new URL(request.url).pathname;
    if (!path.startsWith(`${this.repoPrefix}/`) && path !== this.repoPrefix) {
      return notFound();
    }
    const body = new Uint8Array(await request.arrayBuffer());
    const rest = path.slice(this.repoPrefix.length).replace(/^\/+|\/+$/g, "");
    switch (request.method.toUpperCase()) {
      case "GET":
        return this.read(rest, false);
      case "HEAD":
        return this.read(rest, true);
      case "PUT":
        return this.put(rest, body);
      case "MOVE":
        return this.move(rest, request.headers.get("destination"));
      case "MKCOL":
        return created();
      case "PROPFIND":
        return this.propfind(rest);
      case "LOCK":
        return xml(200, LOCK_BODY);
      case "UNLOCK":
        return noContent();
      case "OPTIONS":
        return new Response(null, {
          status: 200,
          headers: {
            dav: "1, 2",
            allow: "OPTIONS, GET, HEAD, PUT, PROPFIND, MKCOL, MOVE, LOCK, UNLOCK",
          },
        });
      default:
        return notFound();
    }
  }

  // ------------------------------------------------------------- reads ---

  /** GET and HEAD: the dumb protocol's whole fetch surface, plus the refs index. */
  private async read(path: string, headOnly: boolean): Promise<Response> {
    const answer = (status: number, body: Uint8Array | string, type: string): Response =>
      new Response(headOnly ? null : body, {
        status,
        headers: {
          "content-type": type,
          "content-length": String(typeof body === "string" ? body.length : body.length),
        },
      });
    if (path === "info/refs") {
      // Synthesized on every read: the store cannot go stale the way a
      // file-server's update-server-info output can.
      return answer(200, await this.infoRefs(), "text/plain");
    }
    if (path === "HEAD") {
      return answer(200, `ref: ${ORIGIN_HEAD_REF}\n`, "text/plain");
    }
    const stored = await this.store.get(path);
    if (stored !== null) return answer(200, stored, "application/octet-stream");
    // A directory GET is git's remote-heads listing: it scrapes an HTML index
    // of hrefs (the shape Apache's autoindex serves, and the one this
    // protocol was verified against).
    const entries = path === "" ? [] : await this.store.list(`${path}/`);
    if (entries.length > 0) {
      const repo = this.repoPrefix.replace(/^\//, "");
      const lines = entries.map((key) => `<a href="${this.repoPrefix}/${key}">${repo}/${key}</a>`);
      return answer(200, `<html><body>\n${lines.join("\n")}\n</body></html>\n`, "text/html");
    }
    return notFound();
  }

  /** `info/refs` as a dumb server serves it: `<sha>\t<ref>` per ref, one per line. */
  private async infoRefs(): Promise<string> {
    const keys = await this.store.list("refs/");
    const lines: string[] = [];
    for (const key of keys) {
      if (!key.startsWith("refs/")) continue;
      const value = await this.store.get(key);
      if (value === null) continue;
      const sha = DECODER.decode(value).trim();
      if (/^[0-9a-f]{40}$/.test(sha)) lines.push(`${sha}\t${key}\n`);
    }
    return lines.join("");
  }

  // ------------------------------------------------------------ writes ---

  /** A PUT: store the bytes at the path verbatim (git's temp object names arrive verbatim too). */
  private async put(path: string, body: Uint8Array): Promise<Response> {
    if (path === "") return notFound();
    await this.store.put(path, body);
    return created();
  }

  /** A MOVE: git uploads an object under a temp name and moves it into place. */
  private async move(path: string, rawDestination: string | null): Promise<Response> {
    if (rawDestination === null) return notFound();
    let destination: string;
    try {
      destination = rawDestination.startsWith("/")
        ? rawDestination
        : new URL(rawDestination).pathname;
    } catch {
      return notFound();
    }
    if (!destination.startsWith(`${this.repoPrefix}/`)) return notFound();
    const to = destination.slice(this.repoPrefix.length).replace(/^\/+|\/+$/g, "");
    const from = await this.store.get(path);
    if (from === null || to === "") return notFound();
    await this.store.put(to, from);
    await this.store.delete(path);
    return created();
  }

  // -------------------------------------------------------------- DAV ---

  /**
   * PROPFIND, both depths git asks for: the lock-support probe on the
   * repository (which decides the push can lock at all) and the listings of
   * refs and object directories (which let the client skip existing objects
   * and enforce fast-forward). Every response advertises `supportedlock`, the
   * one property the probe exists to see.
   */
  private async propfind(path: string): Promise<Response> {
    const entries = path === "" ? await this.store.list("") : await this.store.list(`${path}/`);
    const responses =
      entries.length === 0
        ? [propfindResponse(`${this.repoPrefix}/${path}`)]
        : entries.map((key) => propfindResponse(`${this.repoPrefix}/${key}`));
    return xml(
      207,
      `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">${responses.join("")}</D:multistatus>`,
    );
  }
}

/** The store the node suite drives the origin with: bytes in a Map. */
export class MemoryOriginStore implements OriginStore {
  private readonly files = new Map<string, Uint8Array>();

  get(key: string): Promise<Uint8Array | null> {
    return Promise.resolve(this.files.get(key) ?? null);
  }

  put(key: string, value: Uint8Array): Promise<void> {
    this.files.set(key, value);
    return Promise.resolve();
  }

  delete(key: string): Promise<void> {
    this.files.delete(key);
    return Promise.resolve();
  }

  list(prefix: string): Promise<string[]> {
    return Promise.resolve([...this.files.keys()].filter((key) => key.startsWith(prefix)).sort());
  }
}
