import { env } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { GITHUB_APP_FLOW_PAGES, GITHUB_TOKEN_DOOR, isAuthExempt } from "../src/auth";
import { insertRun, type Run } from "../src/db";
import { issueRunToken } from "../src/gateway";
import {
  appJWT,
  containerGitHub,
  GITHUB_APP_CALLBACK_PATH,
  GITHUB_APP_INSTALLED_PATH,
  GITHUB_APP_MANIFEST_PATH,
  GITHUB_APP_PATH,
  GITHUB_APP_PERMISSIONS,
  GITHUB_APP_READ_PERMISSIONS,
  GITHUB_APP_START_PATH,
  GITHUB_TOKEN_PATH,
  githubAppManifest,
  githubAppRoute,
  githubCredentialFor,
  githubRung,
  installationToken,
  loadGitHubApp,
  MANIFEST_FLOW_TTL_MS,
  manifestCallbackRoute,
  manifestFlowRoute,
  manifestStartRoute,
  privateKeyDER,
  resetGitHubAppCaches,
  runGitHubTokenRoute,
  TOKEN_REFRESH_MARGIN_MS,
} from "../src/github-app";
import { githubRepoRefs } from "../src/progress";
import { orchestratorEnv } from "../src/sandbox";
import { workerBootEnv } from "../src/worker-boot";

/**
 * The private-key rung (epic dm6): the operator's own GitHub App, minting a
 * per-run installation token down-scoped to one repository.
 *
 * Everything runs in real workerd with real WebCrypto — the key is a real RSA
 * key generated per file, and every JWT is verified against its public half —
 * so the one substitution is GitHub itself, a fake fetcher that records what
 * it was asked. All identifiers below are placeholders.
 */

const API = "https://github-api.example.test";
const WEB = "https://github-web.example.test";
const FACTORY = "https://factory.example.test";
const PROJECT = "example-org/example-repo";
const APP_ID = "123456";
const SLUG = "example-ticfac-app";
const T0 = Date.parse("2026-01-01T00:00:00Z");

const saved: Record<string, unknown> = {};
function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
  else (env as unknown as Record<string, unknown>)[name] = value;
}

function pem(label: string, der: Uint8Array): string {
  let binary = "";
  for (const byte of der) binary += String.fromCharCode(byte);
  const body = btoa(binary).replace(/.{1,64}/g, "$&\n");
  return `-----BEGIN ${label}-----\n${body}-----END ${label}-----\n`;
}

function b64urlJSON(part: string): Record<string, unknown> {
  const padded = part.replaceAll("-", "+").replaceAll("_", "/");
  return JSON.parse(atob(padded + "=".repeat((4 - (padded.length % 4)) % 4)));
}

function b64urlBytes(part: string): Uint8Array {
  const padded = part.replaceAll("-", "+").replaceAll("_", "/");
  const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  return Uint8Array.from(binary, (c) => c.charCodeAt(0));
}

let keys: CryptoKeyPair;
let pkcs8: Uint8Array;
let PKCS8_PEM: string;

async function verifies(jwt: string): Promise<boolean> {
  const [header, payload, signature] = jwt.split(".");
  return crypto.subtle.verify(
    "RSASSA-PKCS1-v1_5",
    keys.publicKey,
    b64urlBytes(signature!),
    new TextEncoder().encode(`${header}.${payload}`),
  );
}

type Recorded = { method: string; url: string; auth: string | null; body: unknown };

/** GitHub, as far as the rung talks to it. */
function fakeGitHub(options: {
  installed?: boolean;
  status?: number;
  expiresInMs?: number;
  now?: () => number;
}) {
  const calls: Recorded[] = [];
  let minted = 0;
  const fetcher = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    const headers = new Headers(init?.headers);
    const body = typeof init?.body === "string" ? JSON.parse(init.body) : null;
    calls.push({ method: init?.method ?? "GET", url, auth: headers.get("authorization"), body });
    const path = new URL(url).pathname;
    if (path === `/repos/${PROJECT}/installation`) {
      return options.installed === false
        ? Response.json({ message: "Not Found" }, { status: 404 })
        : Response.json({ id: 42 });
    }
    if (path === "/app/installations/42/access_tokens") {
      if (options.status !== undefined) {
        return Response.json({ message: "refused" }, { status: options.status });
      }
      minted++;
      const now = (options.now ?? (() => T0))();
      return Response.json(
        {
          token: `ghs_placeholder_${minted}`,
          expires_at: new Date(now + (options.expiresInMs ?? 3_600_000)).toISOString(),
          permissions: (body as { permissions: unknown }).permissions,
        },
        { status: 201 },
      );
    }
    if (path === "/app/installations") {
      return Response.json([
        { id: 42, account: { login: "example-org" }, repository_selection: "selected" },
      ]);
    }
    return Response.json({ message: "Not Found" }, { status: 404 });
  }) as typeof fetch;
  return { calls, fetcher, minted: () => minted };
}

beforeEach(async () => {
  if (keys === undefined) {
    keys = (await crypto.subtle.generateKey(
      {
        name: "RSASSA-PKCS1-v1_5",
        modulusLength: 2048,
        publicExponent: new Uint8Array([1, 0, 1]),
        hash: "SHA-256",
      },
      true,
      ["sign", "verify"],
    )) as CryptoKeyPair;
    pkcs8 = new Uint8Array(
      (await crypto.subtle.exportKey("pkcs8", keys.privateKey)) as ArrayBuffer,
    );
    PKCS8_PEM = pem("PRIVATE KEY", pkcs8);
  }
  resetGitHubAppCaches();
  await env.DB.prepare("DELETE FROM github_app").run();
  await env.DB.prepare("DELETE FROM github_app_flows").run();
  set("GITHUB_API_BASE_URL", API);
  set("GITHUB_WEB_BASE_URL", WEB);
  set("FACTORY_BASE_URL", FACTORY);
  set("GITHUB_TOKEN", undefined);
  set("GITHUB_APP_ID", APP_ID);
  set("GITHUB_APP_PRIVATE_KEY", PKCS8_PEM);
  set("GITHUB_APP_SLUG", SLUG);
});

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
  resetGitHubAppCaches();
});

// ---------------------------------------------------------------- the JWT ---

describe("the App's JWT", () => {
  it("is RS256 over the App id, short-lived, and verifies against the key's public half", async () => {
    const jwt = await appJWT(APP_ID, PKCS8_PEM, T0);
    const [header, payload] = jwt.split(".");
    expect(b64urlJSON(header!)).toEqual({ alg: "RS256", typ: "JWT" });
    const claims = b64urlJSON(payload!) as { iat: number; exp: number; iss: unknown };
    expect(claims.iss).toBe(123456);
    // Back-dated for drift, and under GitHub's ten-minute ceiling.
    expect(claims.iat).toBe(T0 / 1000 - 60);
    expect(claims.exp - claims.iat).toBeLessThanOrEqual(600);
    expect(await verifies(jwt)).toBe(true);
  });

  it("takes GitHub's own PKCS#1 download as well as PKCS#8", async () => {
    // A 2048-bit PKCS#8 PrivateKeyInfo is a 26-byte header around the PKCS#1
    // RSAPrivateKey — exactly what GitHub's .pem download contains.
    const pkcs1 = pkcs8.slice(26);
    const rsaPem = pem("RSA PRIVATE KEY", pkcs1);
    expect(privateKeyDER(rsaPem)).toEqual(pkcs8);
    expect(await verifies(await appJWT(APP_ID, rsaPem, T0))).toBe(true);
  });

  it("refuses something that is not a PEM key, naming the secret", () => {
    expect(() => privateKeyDER("not a key")).toThrow(/GITHUB_APP_PRIVATE_KEY/);
  });
});

// ------------------------------------------------------------- the mint ---

describe("minting an installation token", () => {
  it("asks for the run's ONE repository and the run's permissions, signed by the App", async () => {
    const github = fakeGitHub({});
    const minted = await installationToken(env, PROJECT, {
      fetcher: github.fetcher,
      now: () => T0,
    });

    expect(minted.ok).toBe(true);
    expect(minted.ok && minted.token).toBe("ghs_placeholder_1");
    expect(minted.ok && minted.installation_id).toBe(42);

    const post = github.calls.find((call) => call.method === "POST")!;
    expect(post.url).toBe(`${API}/app/installations/42/access_tokens`);
    // Down-scoped on both axes: one repository by NAME, and the permissions
    // the run uses — never the installation's whole grant.
    expect(post.body).toEqual({
      repositories: ["example-repo"],
      permissions: GITHUB_APP_PERMISSIONS,
    });
    for (const call of github.calls) {
      expect(call.auth).toMatch(/^Bearer [\w-]+\.[\w-]+\.[\w-]+$/);
      expect(await verifies(call.auth!.slice("Bearer ".length))).toBe(true);
    }
  });

  it("serves the cached token until five minutes before it expires, then mints again", async () => {
    let now = T0;
    const github = fakeGitHub({ now: () => now });
    const options = { fetcher: github.fetcher, now: () => now };

    const first = await installationToken(env, PROJECT, options);
    now = T0 + 50 * 60_000;
    const second = await installationToken(env, PROJECT, options);
    expect(second.ok && second.cached).toBe(true);
    expect(second.ok && second.token).toBe(first.ok && first.token);
    expect(github.minted()).toBe(1);

    // Inside the refresh margin: a container handed this token now could see
    // it die mid-push, so a fresh one is minted instead.
    now = T0 + 3_600_000 - TOKEN_REFRESH_MARGIN_MS + 1;
    const third = await installationToken(env, PROJECT, options);
    expect(third.ok && third.cached).toBe(false);
    expect(third.ok && third.token).toBe("ghs_placeholder_2");
    expect(github.minted()).toBe(2);
  });

  it("caches a read-only mint separately from the write mint", async () => {
    const github = fakeGitHub({});
    const options = { fetcher: github.fetcher, now: () => T0 };
    await installationToken(env, PROJECT, options);
    const read = await installationToken(env, PROJECT, {
      ...options,
      permissions: GITHUB_APP_READ_PERMISSIONS,
    });
    expect(read.ok && read.cached).toBe(false);
    expect(github.calls.filter((c) => c.method === "POST").at(-1)!.body).toEqual({
      repositories: ["example-repo"],
      permissions: { contents: "read", metadata: "read" },
    });
  });

  it("refuses a repository the App is not installed on, naming the install URL", async () => {
    const github = fakeGitHub({ installed: false });
    const minted = await installationToken(env, PROJECT, { fetcher: github.fetcher });
    expect(minted.ok).toBe(false);
    expect(minted.ok === false && minted.denial.error).toBe("github_app_not_installed");
    expect(minted.ok === false && minted.denial.detail).toContain(
      `https://github.com/apps/${SLUG}/installations/new`,
    );
    expect(minted.ok === false && minted.denial.detail).toContain(PROJECT);
    // Nothing was minted for a repository nobody installed the App on.
    expect(github.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("names the installation's settings when it has not accepted the permissions asked for", async () => {
    const github = fakeGitHub({ status: 422 });
    const minted = await installationToken(env, PROJECT, { fetcher: github.fetcher });
    expect(minted.ok === false && minted.denial.error).toBe("github_app_permissions");
    expect(minted.ok === false && minted.denial.detail).toContain("settings/installations");
  });

  it("says the key is wrong when GitHub refuses the JWT", async () => {
    const github = fakeGitHub({ status: 401 });
    const minted = await installationToken(env, PROJECT, { fetcher: github.fetcher });
    expect(minted.ok === false && minted.denial.error).toBe("github_app_key_rejected");
  });
});

// -------------------------------------------------------- rung selection ---

describe("which rung answers", () => {
  it("is the App when both its id and key are set, else the stored token, else none", async () => {
    expect(await githubRung(env)).toBe("app");
    set("GITHUB_TOKEN", "github_pat_placeholder");
    expect(await githubRung(env)).toBe("app");
    set("GITHUB_APP_PRIVATE_KEY", undefined);
    expect(await githubRung(env)).toBe("token");
    set("GITHUB_TOKEN", undefined);
    expect(await githubRung(env)).toBe("none");
  });

  it("does not fall back to a stored PAT when the App is not installed on the repository", async () => {
    set("GITHUB_TOKEN", "github_pat_placeholder");
    const github = fakeGitHub({ installed: false });
    const credential = await githubCredentialFor(env, PROJECT, { fetcher: github.fetcher });
    expect(credential.ok).toBe(false);
  });

  it("hands out the stored token unchanged when there is no App", async () => {
    set("GITHUB_APP_ID", undefined);
    set("GITHUB_TOKEN", "github_pat_placeholder");
    const credential = await githubCredentialFor(env, PROJECT);
    expect(credential).toEqual({ ok: true, token: "github_pat_placeholder", rung: "token" });
  });

  it("is what the Worker's own GitHub readers authenticate with", async () => {
    const github = fakeGitHub({});
    const original = globalThis.fetch;
    const seen: (string | null)[] = [];
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/matching-refs/")) {
        seen.push(new Headers(init?.headers).get("authorization"));
        return Response.json([]);
      }
      return github.fetcher(input, init);
    }) as typeof fetch;
    try {
      await githubRepoRefs(env).list(PROJECT);
    } finally {
      globalThis.fetch = original;
    }
    expect(seen).toEqual(["Bearer ghs_placeholder_1"]);
  });
});

// ----------------------------------------------------- what a box is given ---

describe("what a container is handed", () => {
  it("on the App rung: an installation token and the door to refresh it", async () => {
    const github = fakeGitHub({});
    const handed = await containerGitHub(env, PROJECT, { token_source: "operator" }, FACTORY, {
      fetcher: github.fetcher,
    });
    expect(handed).toEqual({
      ok: true,
      token: "ghs_placeholder_1",
      token_url: `${FACTORY}${GITHUB_TOKEN_PATH}`,
    });
    const orchestrator = orchestratorEnv({
      run_id: "run_x",
      epic: "dm6",
      base_sha: "a".repeat(40),
      repo_url: `https://github.com/${PROJECT}.git`,
      gateway_base_url: `${FACTORY}/api/gateway`,
      gateway_token: "tkr_placeholder",
      phase: "run",
      github_token: handed.ok ? handed.token : "",
      github_token_url: handed.ok ? handed.token_url : "",
    });
    expect(orchestrator.GITHUB_TOKEN).toBe("ghs_placeholder_1");
    expect(orchestrator.TICKS_GITHUB_TOKEN_URL).toBe(`${FACTORY}/api/github/token`);
    const worker = workerBootEnv({
      repo_url: `https://github.com/${PROJECT}.git`,
      base_sha: "a".repeat(40),
      epic: "dm6",
      tick: "t1",
      run_id: "run_x",
      gateway_base_url: `${FACTORY}/api/gateway`,
      gateway_token: "tkr_placeholder",
      github_token: handed.ok ? handed.token : "",
      github_token_url: handed.ok ? handed.token_url : "",
    });
    expect(worker.TICKS_GITHUB_TOKEN_URL).toBe(`${FACTORY}/api/github/token`);
  });

  it("on the token rung: the stored token and no refresh door, exactly as before", async () => {
    set("GITHUB_APP_ID", undefined);
    set("GITHUB_TOKEN", "github_pat_placeholder");
    const handed = await containerGitHub(env, PROJECT, { token_source: "operator" }, FACTORY);
    expect(handed).toEqual({ ok: true, token: "github_pat_placeholder" });
  });

  it("for a read-only plan: nothing at all", async () => {
    const github = fakeGitHub({});
    const handed = await containerGitHub(env, PROJECT, { token_source: "run" }, FACTORY, {
      fetcher: github.fetcher,
    });
    expect(handed).toEqual({ ok: true });
    expect(github.calls).toEqual([]);
  });

  it("refuses, naming the install URL, when the App does not cover the repository", async () => {
    const github = fakeGitHub({ installed: false });
    const handed = await containerGitHub(env, PROJECT, { token_source: "operator" }, FACTORY, {
      fetcher: github.fetcher,
    });
    expect(handed.ok === false && handed.denial.detail).toContain(
      `/apps/${SLUG}/installations/new`,
    );
  });
});

// ------------------------------------------------------------ the doors ---

let counter = 0;
async function liveRun(grade: string): Promise<{ run: Run; token: string }> {
  const run: Run = {
    run_id: `run_ghapp_${++counter}`,
    project: PROJECT,
    epic: "dm6",
    base_sha: "c".repeat(40),
    requested_by: "operator@example.com",
    state: "running",
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: `tr_${String(counter).padStart(32, "0")}`,
    credential_grade: grade,
  };
  await insertRun(env.DB, run);
  const issued = await issueRunToken(env, { run_id: run.run_id, tick_id: run.epic, attempt: 1 });
  return { run, token: issued.token };
}

describe("the token door", () => {
  it("is exempt from the operator's token, and spelled the same in auth.ts", () => {
    expect(GITHUB_TOKEN_DOOR).toBe(GITHUB_TOKEN_PATH);
    expect(isAuthExempt(GITHUB_TOKEN_PATH)).toBe(true);
    expect(isAuthExempt("/api/github/app")).toBe(false);
  });

  it("hands a write run a fresh token for its OWN repository", async () => {
    const { token } = await liveRun("write");
    const github = fakeGitHub({});
    const response = await runGitHubTokenRoute(
      env,
      new Request(`${FACTORY}${GITHUB_TOKEN_PATH}`, {
        method: "POST",
        headers: { authorization: `Bearer ${token}` },
      }),
      { fetcher: github.fetcher },
    );
    expect(response.status).toBe(200);
    const body = (await response.json()) as { token: string; expires_at: string };
    expect(body.token).toBe("ghs_placeholder_1");
    expect(github.calls[0]!.url).toBe(`${API}/repos/${PROJECT}/installation`);
  });

  it("refuses a read-only run: this door must not become how it gets a GitHub token", async () => {
    const { token } = await liveRun("read_only");
    const github = fakeGitHub({});
    const response = await runGitHubTokenRoute(
      env,
      new Request(`${FACTORY}${GITHUB_TOKEN_PATH}`, {
        method: "POST",
        headers: { authorization: `Bearer ${token}` },
      }),
      { fetcher: github.fetcher },
    );
    expect(response.status).toBe(403);
    expect(github.calls).toEqual([]);
  });

  it("refuses a caller with no run credential", async () => {
    const response = await runGitHubTokenRoute(
      env,
      new Request(`${FACTORY}${GITHUB_TOKEN_PATH}`, { method: "POST" }),
    );
    expect(response.status).toBe(401);
  });
});

describe("the operator's live check", () => {
  it("mints a read-only token for the repository and never returns it", async () => {
    const github = fakeGitHub({});
    const response = await githubAppRoute(
      env,
      new Request(`${FACTORY}/api/github/app?repo=${PROJECT}`),
      { fetcher: github.fetcher, now: () => T0 },
    );
    const text = await response.text();
    expect(text).not.toContain("ghs_placeholder");
    const body = JSON.parse(text) as {
      rung: string;
      app: { app_id: string; slug: string; installations: unknown[] };
      check: { ok: boolean; installation_id: number; permissions: unknown };
    };
    expect(body.rung).toBe("app");
    expect(body.app.app_id).toBe(APP_ID);
    expect(body.app.installations).toEqual([
      { id: 42, account: "example-org", repository_selection: "selected" },
    ]);
    expect(body.check.ok).toBe(true);
    expect(body.check.installation_id).toBe(42);
    expect(body.check.permissions).toEqual(GITHUB_APP_READ_PERMISSIONS);
  });

  it("reports the token rung without asking GitHub anything", async () => {
    set("GITHUB_APP_ID", undefined);
    set("GITHUB_TOKEN", "github_pat_placeholder");
    const github = fakeGitHub({});
    const response = await githubAppRoute(env, new Request(`${FACTORY}/api/github/app`), {
      fetcher: github.fetcher,
    });
    expect(await response.json()).toEqual({ rung: "token", sealing_key: false });
    expect(github.calls).toEqual([]);
  });
});

// ------------------------------------------------------ the manifest flow ---

/** 32 bytes, base64: what setup puts as GITHUB_APP_SEALING_KEY. */
const SEALING_KEY = btoa(String.fromCharCode(...new Uint8Array(32).map((_v, i) => i + 1)));
const OTHER_SEALING_KEY = btoa(String.fromCharCode(...new Uint8Array(32).map((_v, i) => 200 - i)));
const STATE = "st4te_placeholder_0123456789abcdefghijklmnop";

function manifestApp() {
  // No App configured by hand: the flow is what provides one.
  set("GITHUB_APP_ID", undefined);
  set("GITHUB_APP_PRIVATE_KEY", undefined);
  set("GITHUB_APP_SLUG", undefined);
  set("GITHUB_APP_SEALING_KEY", SEALING_KEY);
}

/** GitHub's manifest conversion, handing back the test key as GitHub would: PKCS#1. */
function fakeConversion() {
  const calls: string[] = [];
  const fetcher = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input);
    calls.push(`${init?.method ?? "GET"} ${url}`);
    if (new URL(url).pathname === "/app-manifests/code_placeholder/conversions") {
      return Response.json(
        {
          id: 777,
          slug: "ticfac-st4te_pl",
          html_url: `${WEB}/apps/ticfac-st4te_pl`,
          owner: { login: "example-owner" },
          client_id: "Iv1.placeholder",
          client_secret: "placeholder-client-secret",
          webhook_secret: "placeholder-webhook-secret",
          pem: pem("RSA PRIVATE KEY", pkcs8.slice(26)),
        },
        { status: 201 },
      );
    }
    return Response.json({ message: "Not Found" }, { status: 404 });
  }) as typeof fetch;
  return { calls, fetcher };
}

async function startFlow(org?: string): Promise<Response> {
  return manifestFlowRoute(
    env,
    new Request(`${FACTORY}${GITHUB_APP_MANIFEST_PATH}`, {
      method: "POST",
      body: JSON.stringify(org === undefined ? { state: STATE } : { state: STATE, org }),
    }),
  );
}

function callback(state: string, fetcher: typeof fetch): Promise<Response> {
  return manifestCallbackRoute(
    env,
    new Request(`${FACTORY}${GITHUB_APP_CALLBACK_PATH}?code=code_placeholder&state=${state}`),
    { fetcher },
  );
}

describe("the manifest flow", () => {
  it("serves its three browser pages without the operator's token, spelled as auth.ts spells them", () => {
    expect([...GITHUB_APP_FLOW_PAGES]).toEqual([
      GITHUB_APP_START_PATH,
      GITHUB_APP_CALLBACK_PATH,
      GITHUB_APP_INSTALLED_PATH,
    ]);
    for (const path of GITHUB_APP_FLOW_PAGES) expect(isAuthExempt(path)).toBe(true);
    // Registering a flow is the operator's act, and stays behind their token.
    expect(isAuthExempt(GITHUB_APP_MANIFEST_PATH)).toBe(false);
  });

  it("refuses to start before the sealing key exists — the key would have nowhere safe to go", async () => {
    manifestApp();
    set("GITHUB_APP_SEALING_KEY", undefined);
    const response = await startFlow();
    expect(response.status).toBe(503);
    expect(((await response.json()) as { error: string }).error).toBe(
      "github_app_sealing_key_missing",
    );
  });

  it("answers one link to print, and keeps only the state's hash", async () => {
    manifestApp();
    const response = await startFlow();
    expect(response.status).toBe(201);
    const body = (await response.json()) as { start_url: string };
    expect(body.start_url).toBe(`${FACTORY}${GITHUB_APP_START_PATH}?state=${STATE}`);
    const rows = await env.DB.prepare("SELECT state_hash, status FROM github_app_flows").all();
    expect(rows.results).toHaveLength(1);
    expect(JSON.stringify(rows.results)).not.toContain(STATE);
  });

  it("posts a manifest whose permissions are exactly what a run's token asks for", async () => {
    manifestApp();
    await startFlow();
    const response = await manifestStartRoute(
      env,
      new Request(`${FACTORY}${GITHUB_APP_START_PATH}?state=${STATE}`),
    );
    expect(response.status).toBe(200);
    const html = await response.text();
    expect(html).toContain(`action="${WEB}/settings/apps/new?state=${STATE}"`);
    const encoded = /name="manifest" value="([^"]*)"/.exec(html)![1]!;
    const manifest = JSON.parse(
      encoded
        .replaceAll("&quot;", '"')
        .replaceAll("&#39;", "'")
        .replaceAll("&lt;", "<")
        .replaceAll("&gt;", ">")
        .replaceAll("&amp;", "&"),
    ) as Record<string, unknown>;
    expect(manifest).toEqual(githubAppManifest(FACTORY, STATE));
    expect(manifest.default_permissions).toEqual(GITHUB_APP_PERMISSIONS);
    expect(manifest.redirect_url).toBe(`${FACTORY}${GITHUB_APP_CALLBACK_PATH}`);
    expect(manifest.setup_url).toBe(`${FACTORY}${GITHUB_APP_INSTALLED_PATH}`);
    expect(manifest.public).toBe(false);
    expect(manifest.hook_attributes).toEqual({ url: `${FACTORY}/api/hooks/github`, active: false });
  });

  it("posts to the organization's form when setup named one", async () => {
    manifestApp();
    await startFlow("example-org");
    const html = await (
      await manifestStartRoute(
        env,
        new Request(`${FACTORY}${GITHUB_APP_START_PATH}?state=${STATE}`),
      )
    ).text();
    expect(html).toContain(
      `action="${WEB}/organizations/example-org/settings/apps/new?state=${STATE}"`,
    );
  });

  it("exchanges the code, seals the key into D1, and sends the browser on to install", async () => {
    manifestApp();
    await startFlow();
    const github = fakeConversion();
    const response = await callback(STATE, github.fetcher);

    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe(`${WEB}/apps/ticfac-st4te_pl/installations/new`);
    expect(github.calls).toEqual([`POST ${API}/app-manifests/code_placeholder/conversions`]);

    // Sealed: the row holds no usable credential on its own.
    const row = (await env.DB.prepare("SELECT * FROM github_app").first()) as Record<
      string,
      string
    >;
    expect(row.app_id).toBe("777");
    expect(JSON.stringify(row)).not.toContain("PRIVATE KEY");
    expect(JSON.stringify(row)).not.toContain("placeholder-webhook-secret");

    // …and the rung is live on it: the JWT it signs verifies against the key.
    expect(await githubRung(env)).toBe("app");
    const app = await loadGitHubApp(env);
    expect(app.config?.app_id).toBe("777");
    expect(app.config?.source).toBe("manifest");
    const jwt = await appJWT(app.config!.app_id, app.config!.private_key, T0);
    expect(await verifies(jwt)).toBe(true);
  });

  it("consumes the state: a replayed callback is refused and GitHub is not asked twice", async () => {
    manifestApp();
    await startFlow();
    const github = fakeConversion();
    await callback(STATE, github.fetcher);
    const replay = await callback(STATE, github.fetcher);
    expect(replay.status).toBe(410);
    expect(github.calls).toHaveLength(1);
  });

  it("refuses a callback carrying a state this factory never minted", async () => {
    manifestApp();
    const github = fakeConversion();
    const forged = await callback("forged_state_placeholder_0123456789abcdef", github.fetcher);
    expect(forged.status).toBe(410);
    expect(github.calls).toEqual([]);
    expect(await githubRung(env)).toBe("none");
  });

  it("refuses an expired flow", async () => {
    manifestApp();
    await startFlow();
    const github = fakeConversion();
    const late = await manifestCallbackRoute(
      env,
      new Request(`${FACTORY}${GITHUB_APP_CALLBACK_PATH}?code=code_placeholder&state=${STATE}`),
      { fetcher: github.fetcher, now: () => Date.now() + MANIFEST_FLOW_TTL_MS + 1 },
    );
    expect(late.status).toBe(410);
    expect(github.calls).toEqual([]);
  });

  it("says so, rather than falling back, when the sealing key was replaced", async () => {
    manifestApp();
    await startFlow();
    await callback(STATE, fakeConversion().fetcher);
    set("GITHUB_APP_SEALING_KEY", OTHER_SEALING_KEY);
    set("GITHUB_TOKEN", "github_pat_placeholder");
    resetGitHubAppCaches();

    expect(await githubRung(env)).toBe("app");
    const minted = await installationToken(env, PROJECT, { fetcher: fakeGitHub({}).fetcher });
    expect(minted.ok === false && minted.denial.error).toBe("github_app_key_unusable");
    expect(minted.ok === false && minted.denial.detail).toContain("GITHUB_APP_SEALING_KEY");
  });

  it("reports the sealing key and the sealed App to the operator's status call", async () => {
    manifestApp();
    await startFlow();
    await callback(STATE, fakeConversion().fetcher);
    const response = await githubAppRoute(env, new Request(`${FACTORY}${GITHUB_APP_PATH}`), {
      fetcher: fakeGitHub({}).fetcher,
    });
    const body = (await response.json()) as {
      rung: string;
      sealing_key: boolean;
      app: { app_id: string; source: string; install_url: string };
    };
    expect(body.rung).toBe("app");
    expect(body.sealing_key).toBe(true);
    expect(body.app.app_id).toBe("777");
    expect(body.app.source).toBe("manifest");
    expect(body.app.install_url).toBe(`${WEB}/apps/ticfac-st4te_pl/installations/new`);
  });
});
