import { describe, expect, it } from "vitest";
import { proofOriginUrl, type StagingAgentEnv, stagingAgentFetch } from "../src/staging-agent";
import { type StagingOriginStorage, stagingGitOriginFetch } from "../src/staging-git-origin";

/** The git origin's own storage, as a Map: the DO's SQLite in miniature. */
class FakeOriginStorage implements StagingOriginStorage {
  private readonly files = new Map<string, unknown>();

  async get(key: string): Promise<unknown> {
    return this.files.get(key);
  }

  async put(key: string, value: ArrayBuffer | Uint8Array): Promise<void> {
    this.files.set(key, value);
  }

  async delete(key: string): Promise<boolean> {
    return this.files.delete(key);
  }

  async list(options: { prefix: string }): Promise<Map<string, unknown>> {
    return new Map(
      [...this.files.entries()].filter(([key]) => key.startsWith(options.prefix)).sort(),
    );
  }
}

/** One origin: a storage, its token, and the requests git makes of it. */
function originWith(token: string): {
  storage: FakeOriginStorage;
  request: (path: string, init?: RequestInit) => Promise<Response>;
} {
  const storage = new FakeOriginStorage();
  return {
    storage,
    request: async (path: string, init?: RequestInit) => {
      await storage.put("staging-git-origin:token", new TextEncoder().encode(token));
      return stagingGitOriginFetch(
        storage,
        new Request(`https://staging.example.com${path}`, init),
      );
    },
  };
}

/** The staging agent's env with ONLY the git origins bound: the git routes
 * need no bearer token, so an env that carries nothing else must serve them. */
function gitOnlyEnv(
  stubs: Map<
    string,
    { init(token: string): Promise<void>; fetch(request: Request): Promise<Response> }
  >,
): StagingAgentEnv {
  return {
    PROOF_TOKEN: "proof-secret",
    GIT_ORIGINS: {
      idFromName: (name: string) => ({ name }),
      get: (id: { name: string }) => {
        const stub = stubs.get(id.name);
        if (stub === undefined) throw new Error(`no stub for ${id.name}`);
        return stub;
      },
    },
  } as unknown as StagingAgentEnv;
}

describe("the staging git origin's Durable Object half", () => {
  const token = "tkr_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

  it("keeps its whole surface behind the run token in the repo URL", async () => {
    const storage = new FakeOriginStorage();
    const ask = (path: string) =>
      stagingGitOriginFetch(storage, new Request(`https://staging.example.com${path}`));

    // Before the start route records a token (init), nothing answers: an
    // origin nobody started is not a repository at all.
    expect((await ask(`/proof/git/proof-a2l/${token}/info/refs`)).status).toBe(404);

    // A wrong token is a 404, never a 401: the origin does not challenge
    // (git's dumb push cannot answer one), and "no such repository" is all
    // a guesser who has not earned "wrong token" gets.
    await storage.put("staging-git-origin:token", new TextEncoder().encode(token));
    expect((await ask(`/proof/git/proof-a2l/tkr_wrong/info/refs`)).status).toBe(404);

    // The right token opens the whole surface.
    expect((await ask(`/proof/git/proof-a2l/${token}/info/refs`)).status).toBe(200);
  });

  it("serves the empty repository a first boot clones", async () => {
    const { request } = originWith(token);
    const refs = await request(`/proof/git/proof-a2l/${token}/info/refs?service=git-upload-pack`);
    expect(refs.status).toBe(200);
    // A dumb server's advertisement: plain text, empty until a ref lands —
    // the response git inspects to fall back to dumb mode, never a 401.
    expect(refs.headers.get("content-type")).not.toBe(
      "application/x-git-upload-pack-advertisement",
    );
    expect(await refs.text()).toBe("");
    const head = await request(`/proof/git/proof-a2l/${token}/HEAD`);
    expect(await head.text()).toBe("ref: refs/heads/main\n");
  });

  it("takes a dumb push's whole WebDAV dance, and then serves it back", async () => {
    const { request } = originWith(token);
    const repo = `/proof/git/proof-a2l/${token}`;

    // The locking probe: without its supportedlock answer, git-http-push
    // refuses the push outright ("no DAV locking support").
    const probe = await request(`${repo}/`, { method: "PROPFIND" });
    expect(probe.status).toBe(207);
    expect(await probe.text()).toContain("<D:supportedlock>");

    // The existence checks a push makes before it writes.
    expect((await request(`${repo}/info/refs`, { method: "HEAD" })).status).toBe(200);
    expect((await request(`${repo}/objects/info/packs`, { method: "HEAD" })).status).toBe(404);
    expect((await request(`${repo}/refs/`, { method: "MKCOL" })).status).toBe(201);
    expect((await request(`${repo}/refs/heads/`, { method: "MKCOL" })).status).toBe(201);

    // A lock, held statelessly: the token git echoes in its UNLOCK.
    const lock = await request(`${repo}/refs/heads/main`, { method: "LOCK" });
    expect(lock.status).toBe(200);
    expect(await lock.text()).toContain("<D:locktoken>");
    expect((await request(`${repo}/refs/heads/main`, { method: "UNLOCK" })).status).toBe(204);

    // An object: PUT under the temp name git's lock generates, then MOVE into
    // place — with the Destination header the real client sends.
    const object = new Uint8Array([1, 2, 3, 4]);
    expect(
      (
        await request(
          `${repo}/objects/db/c799000000000000000000000000000000000000_lock00000000000000`,
          {
            method: "PUT",
            body: object,
          },
        )
      ).status,
    ).toBe(201);
    expect(
      (
        await request(
          `${repo}/objects/db/c799000000000000000000000000000000000000_lock00000000000000`,
          {
            method: "MOVE",
            headers: {
              destination: `https://staging.example.com${repo}/objects/db/c799000000000000000000000000000000000000`,
            },
          },
        )
      ).status,
    ).toBe(201);
    const served = await request(`${repo}/objects/db/c799000000000000000000000000000000000000`);
    expect(served.status).toBe(200);
    expect(new Uint8Array(await served.arrayBuffer())).toEqual(object);

    // The ref itself, and the two listings git reads it back through.
    expect(
      (
        await request(`${repo}/refs/heads/main`, {
          method: "PUT",
          body: new TextEncoder().encode("c799000000000000000000000000000000000000\n"),
        })
      ).status,
    ).toBe(201);
    const refs = await request(`${repo}/info/refs?service=git-receive-pack`);
    expect(await refs.text()).toBe("c799000000000000000000000000000000000000\trefs/heads/main\n");
    const index = await request(`${repo}/refs/`);
    const page = await index.text();
    expect(page).toContain('<a href="/proof/git/proof-a2l/');
    expect(page).toContain("/refs/heads/main</a>");
    const listing = await request(`${repo}/refs/`, { method: "PROPFIND" });
    expect(await listing.text()).toContain("/proof/git/proof-a2l/");
  });
});

describe("the staging agent's git routes", () => {
  const token = "tkr_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

  it("hand a git request to the attempt's origin, with no bearer token to carry", async () => {
    const forwarded: Request[] = [];
    const stubs = new Map([
      [
        "proof-a2l",
        {
          init: async () => {},
          fetch: async (request: Request) => {
            forwarded.push(request);
            return new Response("origin-answer", { status: 200 });
          },
        },
      ],
    ]);
    const env = gitOnlyEnv(stubs);
    const answer = await stagingAgentFetch(
      new Request(`https://staging.example.com/proof/git/proof-a2l/${token}/info/refs`),
      env,
    );
    expect(answer.status).toBe(200);
    expect(await answer.text()).toBe("origin-answer");
    expect(forwarded).toHaveLength(1);
    // The request arrives whole — the object reads the attempt's name, the
    // run token and the repository path out of it.
    expect(forwarded[0]?.url).toBe(
      `https://staging.example.com/proof/git/proof-a2l/${token}/info/refs`,
    );
  });

  it("route by the attempt the URL names, and refuse a missing binding", async () => {
    const forwarded: string[] = [];
    const stubs = new Map([
      [
        "other",
        {
          init: async () => {},
          fetch: async (request: Request) => {
            forwarded.push(new URL(request.url).pathname);
            return new Response(null, { status: 204 });
          },
        },
      ],
    ]);
    const env = gitOnlyEnv(stubs);
    await stagingAgentFetch(
      new Request("https://staging.example.com/proof/git/other/tkr_x/HEAD"),
      env,
    );
    expect(forwarded).toEqual(["/proof/git/other/tkr_x/HEAD"]);

    const noBinding = await stagingAgentFetch(
      new Request("https://staging.example.com/proof/git/other/tkr_x/HEAD"),
      { PROOF_TOKEN: "proof-secret" } as unknown as StagingAgentEnv,
    );
    expect(noBinding.status).toBe(503);
  });

  it("every non-git route still demands the bearer token", async () => {
    const env = gitOnlyEnv(new Map());
    const answer = await stagingAgentFetch(
      new Request("https://staging.example.com/proof/state?name=proof-a2l"),
      env,
    );
    expect(answer.status).toBe(401);
  });
});

describe("the proof origin URL", () => {
  it("carries the attempt's name and its run token, and nothing that can drift", () => {
    expect(proofOriginUrl("https://staging.example.com/", "kill-host-mid-tool", "tkr_abc")).toBe(
      "https://staging.example.com/proof/git/kill-host-mid-tool/tkr_abc",
    );
    expect(proofOriginUrl("https://host", "proof", "tkr_t")).toMatch(/\/proof\/git\/proof\/tkr_t$/);
  });
});
