/**
 * The private-key rung of the GitHub credential ladder (D11, epic dm6): the
 * operator's OWN GitHub App, whose private key only this Worker holds,
 * minting one installation token per run, scoped to that run's repository.
 *
 * ## Why this rung is the top one
 *
 * The rungs below it hand the factory ONE long-lived credential — a PAT, or a
 * device-flow user-to-server token — that reaches every repository it was
 * granted, for as long as it lives. An installation token is the opposite on
 * both axes: it is minted per run, it names exactly one repository in its
 * `repositories` list, it asks for the permissions a run uses and no more, and
 * GitHub kills it an hour after it was minted. A leaked sandbox environment
 * leaks something that reaches one repository for under an hour.
 *
 * It could not ship with `tk`, and still does not: a shared App's private key
 * would mint tokens for every installation of that App. This key is the
 * OPERATOR's, for an App registered under the operator's own account by
 * `ticfac factory setup` (the manifest flow), so holding it here is holding
 * the operator's own credential in the operator's own Cloudflare account —
 * exactly where the PAT was.
 *
 * ## Where the key comes from, and where it lives
 *
 * The factory registers the App ITSELF, through GitHub's App manifest flow
 * ({@link manifestStartRoute} → GitHub → {@link manifestCallbackRoute}):
 * `ticfac factory setup` mints a one-time state, prints one link, and the
 * operator — on any device signed in to GitHub, a phone included — clicks
 * "Create GitHub App" and then "Install". GitHub hands the manifest's `code`
 * to THIS Worker's callback, the Worker exchanges it for the App's id, slug,
 * private key and webhook secret, and seals the two secrets into D1 under
 * `GITHUB_APP_SEALING_KEY` (a Worker secret setup puts once). The key never
 * reaches the machine that ran setup, and setup needs no browser and no
 * listener of its own, so it works the same from a headless box.
 *
 * An App configured by hand as Worker secrets (`GITHUB_APP_ID` +
 * `GITHUB_APP_PRIVATE_KEY`) takes precedence over the sealed record.
 *
 * Key ROTATION still needs one visit to the App's settings page: GitHub has
 * no API that generates an App private key. Re-running the manifest flow
 * registers a new App instead.
 *
 * ## Rung selection
 *
 * {@link githubRung}: the App when BOTH its id and key are configured, else the
 * stored `GITHUB_TOKEN` (the device-flow or PAT rung — the Worker cannot tell
 * those apart and does not need to), else none. Configured means THE rung: an
 * App that is not installed on a run's repository is a refusal naming the
 * install URL ({@link installationToken}), never a quiet fallback to a PAT
 * the operator is trying to retire.
 *
 * ## Lifetime
 *
 * Installation tokens live an hour, and a run lives up to six. So the token a
 * container boots with is only its FIRST: a write-grade container is also
 * told {@link GITHUB_TOKEN_PATH}, where its git credential helper and its
 * forge client ask this Worker for a fresh one with the run's own credential
 * ({@link runGitHubTokenRoute}). Tokens are cached here until
 * {@link TOKEN_REFRESH_MARGIN_MS} before they expire, so that door costs
 * GitHub one mint per repository per ~55 minutes, not one per git command.
 */

import { authorizeRunCredential } from "./gateway";
import type { Env } from "./index";

// ------------------------------------------------------------ constants ---

/** Where a container asks for a fresh installation token. Auth-exempt; see src/auth.ts. */
export const GITHUB_TOKEN_PATH = "/api/github/token";

/** The operator's view of the App rung: which rung is live, and a live mint check. */
export const GITHUB_APP_PATH = "/api/github/app";

/** GitHub's REST root, overridable for tests (`GITHUB_API_BASE_URL`). */
export const GITHUB_API_BASE_URL = "https://api.github.com";

/** GitHub's web root, where an App's install page lives. */
export const GITHUB_WEB_BASE_URL = "https://github.com";

/** A cached token is handed out only while it has at least this long to live. */
export const TOKEN_REFRESH_MARGIN_MS = 5 * 60 * 1000;

/** How long an installation lookup is trusted before it is asked again. */
export const INSTALLATION_CACHE_MS = 10 * 60 * 1000;

/**
 * What a write-grade run's token asks for, and — the same object — what the
 * manifest flow registers the App with ({@link githubAppManifest}), so the
 * installation always grants what a run asks for.
 *
 * Each line is a caller, not a guess:
 *
 *  - contents: write — clone, push, and the Worker's own contents/refs writes
 *    (tracker-write.ts, git-refs.ts, git-contents.ts);
 *  - pull_requests: write — the orchestrator opens and edits its epic PR, and
 *    pr-review.ts comments on one;
 *  - checks: write — ci-remediation.ts re-requests a check run; reading check
 *    runs is part of the same grant;
 *  - actions: write — the close-out re-runs failed jobs and dispatches a
 *    workflow that never ran (internal/forge);
 *  - workflows: write — ticks edit `.github/workflows/*` (CI is code this
 *    repository's own epics change), and GitHub refuses a push touching that
 *    directory from a token without it;
 *  - issues: read — github-issues.ts reads an issue's labels for consent;
 *  - statuses: read — commit statuses beside check runs;
 *  - metadata: read — implied by every other grant; stated so the manifest
 *    and the request say the same thing.
 */
export const GITHUB_APP_PERMISSIONS = {
  actions: "write",
  checks: "write",
  contents: "write",
  issues: "read",
  metadata: "read",
  pull_requests: "write",
  statuses: "read",
  workflows: "write",
} as const satisfies Record<string, "read" | "write">;

/**
 * What a READ is minted with: the git door's upload-pack forwarding for a
 * read-only run, and the live check `ticfac factory status` makes. A token
 * that can only read is the right thing to mint for a question that only
 * reads.
 */
export const GITHUB_APP_READ_PERMISSIONS = {
  contents: "read",
  metadata: "read",
} as const satisfies Record<string, "read" | "write">;

export type GitHubPermissions = Readonly<Record<string, "read" | "write">>;

// ----------------------------------------------------------- the config ---

export type GitHubRung = "app" | "token" | "none";

export type GitHubAppConfig = {
  app_id: string;
  private_key: string;
  /** The App's slug, which names its install page. Null when not provisioned. */
  slug: string | null;
  /** Worker secrets set by hand, or the record the manifest flow sealed into D1. */
  source: "secrets" | "manifest";
  owner?: string | null;
  html_url?: string | null;
};

/**
 * What this deployment knows about its App. `present` with a null `config`
 * is an App that exists and cannot be used — a sealed key this deployment
 * cannot open — which is a refusal naming that, never a quiet fallback.
 */
export type GitHubAppState = {
  present: boolean;
  config: GitHubAppConfig | null;
  error: string | null;
};

function secret(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  return trimmed === "" ? null : trimmed;
}

/** How long the sealed record read from D1 is trusted before it is read again. */
const APP_RECORD_CACHE_MS = 30_000;
let appRecordCache: { at_ms: number; state: GitHubAppState } | null = null;

type AppRow = {
  app_id: string;
  slug: string;
  owner: string | null;
  html_url: string | null;
  private_key_sealed: string;
};

/**
 * The App, when this deployment has one: hand-set Worker secrets first, then
 * the record the manifest flow sealed into D1.
 */
export async function loadGitHubApp(env: Env): Promise<GitHubAppState> {
  const appID = secret(env.GITHUB_APP_ID);
  const key = secret(env.GITHUB_APP_PRIVATE_KEY);
  if (appID !== null && key !== null) {
    return {
      present: true,
      config: {
        app_id: appID,
        private_key: key,
        slug: secret(env.GITHUB_APP_SLUG),
        source: "secrets",
      },
      error: null,
    };
  }
  if (env.DB === undefined || env.DB === null) return { present: false, config: null, error: null };
  if (appRecordCache !== null && Date.now() - appRecordCache.at_ms < APP_RECORD_CACHE_MS) {
    return appRecordCache.state;
  }
  let state: GitHubAppState;
  const row = await env.DB.prepare(
    "SELECT app_id, slug, owner, html_url, private_key_sealed FROM github_app WHERE singleton = 1",
  ).first<AppRow>();
  if (row === null) {
    state = { present: false, config: null, error: null };
  } else {
    const opened = await unseal(env, row.private_key_sealed);
    state = opened.ok
      ? {
          present: true,
          config: {
            app_id: row.app_id,
            private_key: opened.value,
            slug: row.slug,
            source: "manifest",
            owner: row.owner,
            html_url: row.html_url,
          },
          error: null,
        }
      : {
          present: true,
          config: null,
          error:
            `the GitHub App ${row.slug} (app ${row.app_id}) is registered, but its sealed private ` +
            `key cannot be opened: ${opened.detail}`,
        };
  }
  appRecordCache = { at_ms: Date.now(), state };
  return state;
}

/** Which rung answers for this deployment. */
export async function githubRung(env: Env): Promise<GitHubRung> {
  if ((await loadGitHubApp(env)).present) return "app";
  return secret(env.GITHUB_TOKEN) === null ? "none" : "token";
}

function webBase(env: Env): string {
  return (env.GITHUB_WEB_BASE_URL ?? GITHUB_WEB_BASE_URL).replace(/\/+$/, "");
}

/** Where the operator installs the App on another repository. */
export function githubAppInstallURL(slug: string | null, base = GITHUB_WEB_BASE_URL): string {
  return slug === null
    ? `${base}/settings/apps (your ticfac App → Install App)`
    : `${base}/apps/${slug}/installations/new`;
}

function apiBase(env: Env): string {
  return (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
}

// --------------------------------------------------------- the sealing ---

/**
 * The Worker secret the sealed App record is encrypted under: 32 random
 * bytes, base64, put once by `ticfac factory setup` before the manifest flow
 * starts. D1 holds only ciphertext, so a database export alone holds no key.
 */
export const SEALING_KEY_SECRET = "GITHUB_APP_SEALING_KEY";

async function sealingKey(env: Env): Promise<CryptoKey | null> {
  const raw = secret(env.GITHUB_APP_SEALING_KEY);
  if (raw === null) return null;
  let bytes: Uint8Array;
  try {
    bytes = base64Decode(raw);
  } catch {
    return null;
  }
  if (bytes.length !== 32) return null;
  return crypto.subtle.importKey("raw", bytes, { name: "AES-GCM" }, false, ["encrypt", "decrypt"]);
}

/** Whether the manifest flow can store what GitHub hands back. */
export async function sealingConfigured(env: Env): Promise<boolean> {
  return (await sealingKey(env)) !== null;
}

function base64Encode(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

async function seal(key: CryptoKey, plaintext: string): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const sealed = new Uint8Array(
    await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, new TextEncoder().encode(plaintext)),
  );
  return `v1.${base64Encode(iv)}.${base64Encode(sealed)}`;
}

async function unseal(
  env: Env,
  sealed: string,
): Promise<{ ok: true; value: string } | { ok: false; detail: string }> {
  const key = await sealingKey(env);
  if (key === null) {
    return { ok: false, detail: `the ${SEALING_KEY_SECRET} Worker secret is missing or malformed` };
  }
  const parts = sealed.split(".");
  if (parts.length !== 3 || parts[0] !== "v1") {
    return { ok: false, detail: "the sealed record is not in a format this bundle reads" };
  }
  try {
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: base64Decode(parts[1]!) },
      key,
      base64Decode(parts[2]!),
    );
    return { ok: true, value: new TextDecoder().decode(plain) };
  } catch {
    return {
      ok: false,
      detail: `it was sealed under a different ${SEALING_KEY_SECRET} (was the secret replaced?)`,
    };
  }
}

// ------------------------------------------------------------ the key ---

function base64Decode(text: string): Uint8Array {
  const binary = atob(text.replace(/\s+/g, ""));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function base64UrlEncode(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function derLength(length: number): number[] {
  if (length < 0x80) return [length];
  const bytes: number[] = [];
  for (let rest = length; rest > 0; rest >>= 8) bytes.unshift(rest & 0xff);
  return [0x80 | bytes.length, ...bytes];
}

function derConcat(...parts: (Uint8Array | number[])[]): Uint8Array {
  const total = parts.reduce((sum, part) => sum + part.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

/** AlgorithmIdentifier { rsaEncryption, NULL }. */
const RSA_ALGORITHM_IDENTIFIER = [
  0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00,
];

/**
 * The key as PKCS#8 DER, which is the only RSA private-key encoding WebCrypto
 * imports.
 *
 * GitHub hands an App's key out as PKCS#1 (`BEGIN RSA PRIVATE KEY`); setup
 * re-encodes it to PKCS#8 before storing it, and this accepts both anyway, so
 * a key pasted straight from GitHub's download works too. A PKCS#1 body is
 * wrapped in the PrivateKeyInfo PKCS#8 defines — a version, the rsaEncryption
 * algorithm identifier, and the PKCS#1 bytes as an OCTET STRING.
 */
export function privateKeyDER(pem: string): Uint8Array {
  const match = /-----BEGIN ((?:RSA )?PRIVATE KEY)-----([\s\S]*?)-----END \1-----/.exec(pem);
  if (match === null) {
    throw new Error(
      "GITHUB_APP_PRIVATE_KEY is not a PEM private key (no BEGIN PRIVATE KEY / BEGIN RSA PRIVATE KEY block)",
    );
  }
  const body = base64Decode(match[2]!);
  if (match[1] === "PRIVATE KEY") return body;
  const version = [0x02, 0x01, 0x00];
  const octet = derConcat([0x04], derLength(body.length), body);
  const inner = derConcat(version, RSA_ALGORITHM_IDENTIFIER, octet);
  return derConcat([0x30], derLength(inner.length), inner);
}

/**
 * The App's own credential: a JWT signed with its private key, which is what
 * GitHub's `/app/...` endpoints accept and nothing else does.
 *
 * `iat` is back-dated a minute for clock drift and `exp` is nine minutes out —
 * GitHub refuses anything over ten, and the JWT is only ever used for the one
 * or two calls that mint an installation token.
 */
export async function appJWT(appID: string, pem: string, nowMs: number): Promise<string> {
  const key = await crypto.subtle.importKey(
    "pkcs8",
    privateKeyDER(pem),
    { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const now = Math.floor(nowMs / 1000);
  const encoder = new TextEncoder();
  const header = base64UrlEncode(encoder.encode(JSON.stringify({ alg: "RS256", typ: "JWT" })));
  const payload = base64UrlEncode(
    encoder.encode(
      JSON.stringify({
        iat: now - 60,
        exp: now + 9 * 60,
        iss: /^\d+$/.test(appID) ? Number(appID) : appID,
      }),
    ),
  );
  const signature = await crypto.subtle.sign(
    "RSASSA-PKCS1-v1_5",
    key,
    encoder.encode(`${header}.${payload}`),
  );
  return `${header}.${payload}.${base64UrlEncode(new Uint8Array(signature))}`;
}

// ------------------------------------------------------------- minting ---

export type GitHubAppDenial = { status: number; error: string; detail: string };

export type InstallationToken = {
  token: string;
  /** ISO 8601, as GitHub reported it. */
  expires_at: string;
  installation_id: number;
  /** The permissions GitHub actually granted this token. */
  permissions: Record<string, string>;
};

export type InstallationTokenResult =
  | ({ ok: true; cached: boolean } & InstallationToken)
  | { ok: false; denial: GitHubAppDenial };

export type GitHubAppOptions = {
  /** The permissions to ask for; {@link GITHUB_APP_PERMISSIONS} by default. */
  permissions?: GitHubPermissions;
  /** Substituted in tests; the deployment uses the global fetch. */
  fetcher?: typeof fetch;
  /** The clock, in ms. Tests move it to walk a token past its refresh margin. */
  now?: () => number;
  /**
   * How long a CACHED token must still have to live to be handed out; never
   * less than {@link TOKEN_REFRESH_MARGIN_MS}. A container's boot token asks
   * for {@link BOOT_TOKEN_MIN_LIFE_MS}: see {@link containerGitHub}.
   */
  minLifeMs?: number;
};

/**
 * What a container's BOOT token must have left to live. The container's git
 * credential helper asks the token door before every push, and falls back to
 * the token it booted with only when the door does not answer — so the boot
 * token is the fallback, and a fallback minted 55 minutes before the boot
 * (the cache's own margin is five) dies five minutes in. Epic hn6's cloud run
 * (2026-09-29) lost two orchestrator boots, 47 and 22 minutes in, to a 403 on
 * its own pushes with nothing to say which token git had sent.
 */
export const BOOT_TOKEN_MIN_LIFE_MS = 45 * 60 * 1000;

type CachedToken = InstallationToken & { expires_at_ms: number };

const tokenCache = new Map<string, CachedToken>();
const installationCache = new Map<string, { id: number; at_ms: number }>();

/** Forgets every cached token and installation (tests; a key rotation). */
export function resetGitHubAppCaches(): void {
  tokenCache.clear();
  installationCache.clear();
  appRecordCache = null;
}

function permissionsKey(permissions: GitHubPermissions): string {
  return Object.keys(permissions)
    .sort()
    .map((name) => `${name}=${permissions[name]}`)
    .join(",");
}

function githubHeaders(bearer: string): Record<string, string> {
  return {
    accept: "application/vnd.github+json",
    authorization: `Bearer ${bearer}`,
    "user-agent": "ticks-factory",
    "x-github-api-version": "2022-11-28",
  };
}

async function githubMessage(response: Response): Promise<string> {
  const text = await response.text().catch(() => "");
  try {
    const parsed = JSON.parse(text) as { message?: unknown };
    if (typeof parsed.message === "string" && parsed.message !== "") return parsed.message;
  } catch {
    // Not JSON; the text itself is the message.
  }
  return text.trim().slice(0, 200);
}

function splitProject(project: string): { owner: string; repo: string } | null {
  const parts = project.split("/");
  if (parts.length !== 2 || parts[0] === "" || parts[1] === "") return null;
  return { owner: parts[0]!, repo: parts[1]! };
}

function notInstalled(config: GitHubAppConfig, project: string): GitHubAppDenial {
  return {
    status: 409,
    error: "github_app_not_installed",
    detail:
      `the factory's GitHub App${config.slug === null ? "" : ` (${config.slug})`} is not ` +
      `installed on ${project}, so no token can be minted for it — install it there: ` +
      `${githubAppInstallURL(config.slug)}`,
  };
}

function keyRejected(config: GitHubAppConfig, detail: string): GitHubAppDenial {
  return {
    status: 502,
    error: "github_app_key_rejected",
    detail:
      `GitHub refused the factory's App credential (app ${config.app_id}): ${detail} — ` +
      "the private key or app id stored as Worker secrets is wrong or was revoked; run " +
      "`ticfac factory setup` to register the App again",
  };
}

/**
 * Which installation of the App covers `project`, from GitHub's
 * `GET /repos/{owner}/{repo}/installation` — asked per repository rather than
 * stored per factory, so an App installed on a user AND an organisation
 * answers for both.
 */
async function installationFor(
  env: Env,
  config: GitHubAppConfig,
  project: string,
  jwt: string,
  fetcher: typeof fetch,
  now: number,
): Promise<{ ok: true; id: number } | { ok: false; denial: GitHubAppDenial }> {
  const cacheKey = `${config.app_id}:${project}`;
  const cached = installationCache.get(cacheKey);
  if (cached !== undefined && now - cached.at_ms < INSTALLATION_CACHE_MS) {
    return { ok: true, id: cached.id };
  }
  const response = await fetcher(`${apiBase(env)}/repos/${project}/installation`, {
    headers: githubHeaders(jwt),
  });
  if (response.status === 404) return { ok: false, denial: notInstalled(config, project) };
  if (response.status === 401) {
    return { ok: false, denial: keyRejected(config, await githubMessage(response)) };
  }
  if (!response.ok) {
    return {
      ok: false,
      denial: {
        status: 502,
        error: "github_app_unavailable",
        detail: `GitHub answered ${response.status} looking up the App's installation on ${project}: ${await githubMessage(response)}`,
      },
    };
  }
  const body = (await response.json()) as { id?: unknown };
  if (typeof body.id !== "number") {
    return {
      ok: false,
      denial: {
        status: 502,
        error: "github_app_unavailable",
        detail: `GitHub named no installation id for ${project}`,
      },
    };
  }
  installationCache.set(cacheKey, { id: body.id, at_ms: now });
  return { ok: true, id: body.id };
}

/**
 * One installation token for `project`: from the cache while it has more than
 * {@link TOKEN_REFRESH_MARGIN_MS} to live, minted otherwise.
 *
 * The mint is DOWN-SCOPED on both axes GitHub offers: `repositories` names the
 * run's one repository — an App installed on "all repositories" still hands
 * this run a token for exactly one — and `permissions` names what the run
 * uses. A refusal names its remedy: an App not installed on the repository
 * names the install URL; a permission the installation has not accepted names
 * the installation's settings.
 */
export async function installationToken(
  env: Env,
  project: string,
  options: GitHubAppOptions = {},
): Promise<InstallationTokenResult> {
  const app = await loadGitHubApp(env);
  if (app.config === null) {
    return {
      ok: false,
      denial: app.present
        ? { status: 500, error: "github_app_key_unusable", detail: app.error ?? "unusable" }
        : {
            status: 404,
            error: "github_app_not_configured",
            detail: "this factory has no GitHub App; run `ticfac factory setup`",
          },
    };
  }
  const config = app.config;
  const split = splitProject(project);
  if (split === null) {
    return {
      ok: false,
      denial: {
        status: 400,
        error: "github_repo_required",
        detail: `${JSON.stringify(project)} is not owner/repo`,
      },
    };
  }
  const permissions = options.permissions ?? GITHUB_APP_PERMISSIONS;
  const fetcher = options.fetcher ?? fetch;
  const now = (options.now ?? Date.now)();

  const cacheKey = `${config.app_id}:${project}:${permissionsKey(permissions)}`;
  const cached = tokenCache.get(cacheKey);
  const minLife = Math.max(TOKEN_REFRESH_MARGIN_MS, options.minLifeMs ?? 0);
  if (cached !== undefined && cached.expires_at_ms - now > minLife) {
    const { expires_at_ms: _unused, ...token } = cached;
    return { ok: true, cached: true, ...token };
  }

  let jwt: string;
  try {
    jwt = await appJWT(config.app_id, config.private_key, now);
  } catch (error) {
    return {
      ok: false,
      denial: {
        status: 500,
        error: "github_app_key_unusable",
        detail: `the factory's GitHub App private key cannot sign: ${String((error as Error).message ?? error)}`,
      },
    };
  }

  const installation = await installationFor(env, config, project, jwt, fetcher, now);
  if (!installation.ok) return installation;

  const response = await fetcher(
    `${apiBase(env)}/app/installations/${installation.id}/access_tokens`,
    {
      method: "POST",
      headers: { ...githubHeaders(jwt), "content-type": "application/json" },
      body: JSON.stringify({ repositories: [split.repo], permissions }),
    },
  );
  if (response.status === 404) {
    // The installation exists but does not cover this repository ("only
    // select repositories", and this one was not selected), or it was
    // removed since the lookup above was cached.
    installationCache.delete(`${config.app_id}:${project}`);
    return { ok: false, denial: notInstalled(config, project) };
  }
  if (response.status === 401) {
    return { ok: false, denial: keyRejected(config, await githubMessage(response)) };
  }
  if (response.status === 422 || response.status === 403) {
    return {
      ok: false,
      denial: {
        status: 403,
        error: "github_app_permissions",
        detail:
          `GitHub refused a token for ${project} with ${permissionsKey(permissions)}: ` +
          `${await githubMessage(response)} — the installation has not granted every ` +
          `permission the factory asks for; accept the App's requested permissions under ` +
          `${GITHUB_WEB_BASE_URL}/settings/installations`,
      },
    };
  }
  if (!response.ok) {
    return {
      ok: false,
      denial: {
        status: 502,
        error: "github_app_unavailable",
        detail: `GitHub answered ${response.status} minting a token for ${project}: ${await githubMessage(response)}`,
      },
    };
  }
  const body = (await response.json()) as {
    token?: unknown;
    expires_at?: unknown;
    permissions?: unknown;
  };
  const expiresAtMs =
    typeof body.expires_at === "string" ? Date.parse(body.expires_at) : Number.NaN;
  if (typeof body.token !== "string" || body.token === "" || !Number.isFinite(expiresAtMs)) {
    return {
      ok: false,
      denial: {
        status: 502,
        error: "github_app_unavailable",
        detail: `GitHub's token answer for ${project} carried no token or no expiry`,
      },
    };
  }
  const minted: CachedToken = {
    token: body.token,
    expires_at: body.expires_at as string,
    expires_at_ms: expiresAtMs,
    installation_id: installation.id,
    permissions:
      body.permissions !== null && typeof body.permissions === "object"
        ? (body.permissions as Record<string, string>)
        : {},
  };
  tokenCache.set(cacheKey, minted);
  const { expires_at_ms: _unused, ...token } = minted;
  return { ok: true, cached: false, ...token };
}

// ------------------------------------------------ what everything asks ---

export type GitHubCredential =
  | {
      ok: true;
      /** Empty when the deployment has no credential at all (anonymous reads). */
      token: string;
      rung: GitHubRung;
      /** ISO 8601 for an App token; absent for a stored token. */
      expires_at?: string;
    }
  | { ok: false; denial: GitHubAppDenial };

/**
 * The GitHub credential for one repository, from whichever rung answers.
 *
 * Every GitHub call the factory makes asks here — the run's containers, the
 * git door, and each of the Worker's own readers — so there is one rung
 * selection and one refusal, not a dozen reads of `GITHUB_TOKEN`.
 */
export async function githubCredentialFor(
  env: Env,
  project: string,
  options: GitHubAppOptions = {},
): Promise<GitHubCredential> {
  const rung = await githubRung(env);
  if (rung === "app") {
    const minted = await installationToken(env, project, options);
    if (!minted.ok) return minted;
    return { ok: true, token: minted.token, rung, expires_at: minted.expires_at };
  }
  return { ok: true, token: secret(env.GITHUB_TOKEN) ?? "", rung };
}

/**
 * The `authorization` header for a GitHub API call about `project`, or none
 * for an anonymous one. Throws with the rung's own refusal — every reader
 * that calls this already treats a throw as "could not ask".
 */
export async function githubAuthorization(
  env: Env,
  project: string,
  options: GitHubAppOptions = {},
): Promise<Record<string, string>> {
  const credential = await githubCredentialFor(env, project, options);
  if (!credential.ok) throw new Error(credential.denial.detail);
  return credential.token === "" ? {} : { authorization: `Bearer ${credential.token}` };
}

/** The variable a container reads {@link GITHUB_TOKEN_PATH}'s full URL from. */
export const GITHUB_TOKEN_URL_ENV = "TICKS_GITHUB_TOKEN_URL";

export type ContainerGitHub =
  | {
      ok: true;
      /** The operator-side token the container boots with; undefined for a run-token plan. */
      token?: string;
      /** Where it asks for the next one; set only on the App rung. */
      token_url?: string;
    }
  | { ok: false; denial: GitHubAppDenial };

/**
 * What one container is handed for GitHub, per boot.
 *
 * A plan whose `token_source` is `run` (the read-only grade) gets nothing
 * here — its credential is its own run token (credentials.ts). An `operator`
 * plan gets the rung's token for the run's repository and, on the App rung,
 * the URL its git credential helper and forge client refresh it from: the
 * token it boots with dies an hour later, the run does not.
 *
 * Minted per BOOT rather than per run, and never returned from a Workflow
 * step: a step's result is journalled, and a token has no business being in
 * a journal.
 */
export async function containerGitHub(
  env: Env,
  project: string,
  plan: { token_source: "operator" | "run" },
  factoryURL: string | null,
  options: GitHubAppOptions = {},
): Promise<ContainerGitHub> {
  if (plan.token_source !== "operator") return { ok: true };
  const credential = await githubCredentialFor(env, project, {
    minLifeMs: BOOT_TOKEN_MIN_LIFE_MS,
    ...options,
  });
  if (!credential.ok) return credential;
  return {
    ok: true,
    token: credential.token,
    ...(credential.rung === "app" && factoryURL !== null
      ? { token_url: `${factoryURL.replace(/\/+$/, "")}${GITHUB_TOKEN_PATH}` }
      : {}),
  };
}

// ------------------------------------------------------------- the doors ---

function extractBearer(request: Request): string | null {
  const header = request.headers.get("authorization");
  if (header === null) return null;
  const match = /^Bearer[ \t]+([^\s]+)$/i.exec(header.trim());
  return match === null ? null : match[1]!;
}

function refusal(denial: GitHubAppDenial): Response {
  return Response.json({ error: denial.error, detail: denial.detail }, { status: denial.status });
}

/**
 * `POST /api/github/token`: a fresh installation token for the calling run's
 * own repository.
 *
 * The run's credential decides which repository — there is no repository in
 * the request, so a container cannot ask for someone else's — and only a
 * write-grade run is answered: a read-only run holds no GitHub credential by
 * design (credentials.ts), and this door must not become the way it gets one.
 * A deployment without an App answers 404, and the container keeps the token
 * it booted with.
 */
export async function runGitHubTokenRoute(
  env: Env,
  request: Request,
  options: GitHubAppOptions = {},
): Promise<Response> {
  if (request.method !== "POST") {
    return Response.json(
      { error: "method_not_allowed", detail: "allowed: POST" },
      { status: 405, headers: { Allow: "POST" } },
    );
  }
  const authorized = await authorizeRunCredential(env, extractBearer(request));
  if (!authorized.ok) return refusal(authorized.denial);
  const run = authorized.run;
  const grade = (run.credential_grade ?? "").trim();
  if (grade !== "" && grade !== "write") {
    return refusal({
      status: 403,
      error: "github_token_refused",
      detail: `run ${run.run_id} holds a ${grade} credential, and a run that may not write is never handed a GitHub token`,
    });
  }
  const minted = await installationToken(env, run.project, options);
  if (!minted.ok) return refusal(minted.denial);
  return Response.json({ token: minted.token, expires_at: minted.expires_at });
}

/**
 * `GET /api/github/app[?repo=owner/name]`: which rung is live and, with a
 * repository, a LIVE check of it — a real, read-only token mint for that
 * repository, whose token is never returned.
 *
 * This is what `ticfac factory status` and `ticfac doctor` ask: only the
 * Worker holds the App's key, so only the Worker can say whether the App rung
 * works. Operator-authenticated, like every /api route.
 */
export async function githubAppRoute(
  env: Env,
  request: Request,
  options: GitHubAppOptions = {},
): Promise<Response> {
  if (request.method !== "GET") {
    return Response.json(
      { error: "method_not_allowed", detail: "allowed: GET" },
      { status: 405, headers: { Allow: "GET" } },
    );
  }
  const rung = await githubRung(env);
  const state = await loadGitHubApp(env);
  const repo = new URL(request.url).searchParams.get("repo")?.trim() ?? "";
  const body: Record<string, unknown> = {
    rung,
    // Whether this deployment can take part in the manifest flow at all: the
    // sealed record needs the key setup puts before it starts one.
    sealing_key: await sealingConfigured(env),
  };

  if (state.present && state.config === null) {
    body.app = { error: state.error };
  }
  const config = state.config;
  if (config !== null) {
    const app: Record<string, unknown> = {
      app_id: config.app_id,
      slug: config.slug,
      source: config.source,
      owner: config.owner ?? null,
      html_url: config.html_url ?? null,
      install_url: githubAppInstallURL(config.slug, webBase(env)),
    };
    const fetcher = options.fetcher ?? fetch;
    try {
      const jwt = await appJWT(config.app_id, config.private_key, (options.now ?? Date.now)());
      const response = await fetcher(`${apiBase(env)}/app/installations?per_page=100`, {
        headers: githubHeaders(jwt),
      });
      if (response.ok) {
        const listed = (await response.json()) as {
          id: number;
          account?: { login?: string } | null;
          repository_selection?: string;
        }[];
        app.installations = listed.map((installation) => ({
          id: installation.id,
          account: installation.account?.login ?? null,
          repository_selection: installation.repository_selection ?? null,
        }));
      } else if (response.status === 401) {
        app.error = keyRejected(config, await githubMessage(response)).detail;
      } else {
        app.error = `GitHub answered ${response.status} listing the App's installations: ${await githubMessage(response)}`;
      }
    } catch (error) {
      app.error = `the App's private key cannot sign: ${String((error as Error).message ?? error)}`;
    }
    body.app = app;
  }

  if (repo !== "") {
    if (rung !== "app") {
      body.check = {
        repo,
        ok: false,
        checked: false,
        detail: "no GitHub App is configured, so there is no token for the factory to mint",
      };
    } else {
      const minted = await installationToken(env, repo, {
        ...options,
        permissions: GITHUB_APP_READ_PERMISSIONS,
      });
      body.check = minted.ok
        ? {
            repo,
            ok: true,
            checked: true,
            installation_id: minted.installation_id,
            expires_at: minted.expires_at,
            permissions: minted.permissions,
          }
        : {
            repo,
            ok: false,
            checked: true,
            error: minted.denial.error,
            detail: minted.denial.detail,
          };
    }
  }
  return Response.json(body);
}

// ------------------------------------------------- the manifest flow ---
//
// GitHub requires a signed-in person to approve an App's creation and its
// installation, and no API does either. Everything else is the factory's:
//
//  1. `ticfac factory setup` mints a one-time state and registers it here
//     (`POST /api/github/app/manifest`, operator-authenticated). Only its
//     SHA-256 is stored.
//  2. It prints one link, {@link GITHUB_APP_START_PATH}?state=…, which the
//     operator opens on any device signed in to GitHub. The page posts the
//     manifest to GitHub's "new App" form — click one: "Create GitHub App".
//  3. GitHub redirects to {@link GITHUB_APP_CALLBACK_PATH} with a `code` and
//     the state. The state is consumed (CSRF: a code arriving with a state
//     this factory did not mint, or minted and already used, is refused);
//     the code is exchanged at `POST /app-manifests/{code}/conversions`; the
//     key and webhook secret are sealed into D1; and the browser is sent
//     straight on to the App's install page — click two: "Install".
//  4. Setup, polling `GET /api/github/app`, sees the installation appear.

/** The page that posts the manifest to GitHub. Auth-exempt: the state is the capability. */
export const GITHUB_APP_START_PATH = "/github/app/start";
/** Where GitHub returns the manifest code. Auth-exempt: the state is the capability. */
export const GITHUB_APP_CALLBACK_PATH = "/github/app/callback";
/** Where GitHub sends the browser after an installation. A static page. */
export const GITHUB_APP_INSTALLED_PATH = "/github/app/installed";
/** Where setup registers a flow's state. Operator-authenticated. */
export const GITHUB_APP_MANIFEST_PATH = "/api/github/app/manifest";

/** How long a started flow waits for its callback. */
export const MANIFEST_FLOW_TTL_MS = 30 * 60 * 1000;

/** The homepage every factory App names: the software, not the deployment. */
export const GITHUB_APP_HOMEPAGE = "https://github.com/pengelbrecht/ticfac";

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)),
  );
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function validState(state: string | null): state is string {
  return state !== null && /^[A-Za-z0-9_-]{32,128}$/.test(state);
}

function validOrg(org: string): boolean {
  return /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$/.test(org);
}

/**
 * The manifest a factory registers its App from.
 *
 * The name is suffixed from the state because App names are unique across
 * GitHub; the operator can rename it on GitHub's form before creating it. The
 * webhook is declared INACTIVE: the factory reads what it needs by API, and an
 * active hook would deliver events to a route that does not want them.
 */
export function githubAppManifest(factoryURL: string, state: string): Record<string, unknown> {
  const factory = factoryURL.replace(/\/+$/, "");
  return {
    name: `ticfac-${state.slice(0, 8).toLowerCase()}`,
    url: GITHUB_APP_HOMEPAGE,
    description: "A ticfac factory: per-run installation tokens for the repositories it works on.",
    public: false,
    redirect_url: `${factory}${GITHUB_APP_CALLBACK_PATH}`,
    setup_url: `${factory}${GITHUB_APP_INSTALLED_PATH}`,
    setup_on_update: false,
    request_oauth_on_install: false,
    hook_attributes: { url: `${factory}/api/hooks/github`, active: false },
    default_events: [],
    default_permissions: { ...GITHUB_APP_PERMISSIONS },
  };
}

function escapeHTML(text: string): string {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function page(title: string, body: string, status = 200): Response {
  return new Response(
    `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
      `<meta name="viewport" content="width=device-width,initial-scale=1">` +
      `<meta name="referrer" content="no-referrer">` +
      `<title>${escapeHTML(title)}</title>` +
      `<style>body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem}` +
      `button{font:inherit;padding:.6rem 1.2rem}</style></head><body>${body}</body></html>`,
    {
      status,
      headers: {
        "content-type": "text/html; charset=utf-8",
        "cache-control": "no-store",
        "x-frame-options": "DENY",
      },
    },
  );
}

type FlowRow = { state_hash: string; org: string | null; expires_at_ms: number; status: string };

async function readFlow(env: Env, state: string): Promise<FlowRow | null> {
  return env.DB.prepare(
    "SELECT state_hash, org, expires_at_ms, status FROM github_app_flows WHERE state_hash = ?",
  )
    .bind(await sha256Hex(state))
    .first<FlowRow>();
}

/**
 * `POST /api/github/app/manifest` `{state, org?}`: registers a flow setup is
 * about to hand the operator, and answers the link to print.
 */
export async function manifestFlowRoute(
  env: Env,
  request: Request,
  options: GitHubAppOptions = {},
): Promise<Response> {
  if (request.method !== "POST") {
    return Response.json(
      { error: "method_not_allowed", detail: "allowed: POST" },
      { status: 405, headers: { Allow: "POST" } },
    );
  }
  let body: { state?: unknown; org?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return refusal({ status: 400, error: "invalid_json", detail: "the body is not JSON" });
  }
  const state = typeof body.state === "string" ? body.state : null;
  if (!validState(state)) {
    return refusal({
      status: 400,
      error: "github_app_state_invalid",
      detail: "state must be 32-128 URL-safe characters, minted fresh by `ticfac factory setup`",
    });
  }
  const org = typeof body.org === "string" ? body.org.trim() : "";
  if (org !== "" && !validOrg(org)) {
    return refusal({
      status: 400,
      error: "github_org_invalid",
      detail: `${JSON.stringify(org)} is not a GitHub organization name`,
    });
  }
  const factory = factoryURLOf(env);
  if (factory === null) {
    return refusal({
      status: 503,
      error: "factory_url_unknown",
      detail:
        "this deployment does not know its own base URL (FACTORY_BASE_URL), so GitHub has nowhere to send the App back to; re-run `ticfac factory deploy`",
    });
  }
  if (!(await sealingConfigured(env))) {
    return refusal({
      status: 503,
      error: "github_app_sealing_key_missing",
      detail: `the ${SEALING_KEY_SECRET} Worker secret is not set, so the App's private key would have nowhere safe to go; \`ticfac factory setup\` puts it before starting the flow`,
    });
  }
  const now = (options.now ?? Date.now)();
  const expires = now + MANIFEST_FLOW_TTL_MS;
  // Expired flows go with every new one: nothing else ever reads them.
  await env.DB.prepare("DELETE FROM github_app_flows WHERE expires_at_ms < ?").bind(now).run();
  await env.DB.prepare(
    "INSERT INTO github_app_flows (state_hash, org, created_at, expires_at_ms, status) VALUES (?, ?, ?, ?, 'pending')",
  )
    .bind(await sha256Hex(state), org === "" ? null : org, new Date(now).toISOString(), expires)
    .run();
  return Response.json(
    {
      start_url: `${factory}${GITHUB_APP_START_PATH}?state=${encodeURIComponent(state)}`,
      expires_at: new Date(expires).toISOString(),
    },
    { status: 201 },
  );
}

function factoryURLOf(env: Env): string | null {
  const raw = secret(env.FACTORY_BASE_URL);
  if (raw === null) return null;
  try {
    const parsed = new URL(raw);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return null;
  } catch {
    return null;
  }
  return raw.replace(/\/+$/, "");
}

/** `GET /github/app/start?state=…`: the page that hands GitHub the manifest. */
export async function manifestStartRoute(
  env: Env,
  request: Request,
  options: GitHubAppOptions = {},
): Promise<Response> {
  const state = new URL(request.url).searchParams.get("state");
  if (!validState(state)) {
    return page(
      "ticfac: link incomplete",
      "<h1>This link is incomplete</h1><p>Copy the whole link `ticfac factory setup` printed.</p>",
      400,
    );
  }
  const flow = await readFlow(env, state);
  const now = (options.now ?? Date.now)();
  if (flow === null || flow.status !== "pending" || flow.expires_at_ms < now) {
    return page(
      "ticfac: link expired",
      "<h1>This link is no longer valid</h1><p>It was already used, or it expired. Run <code>ticfac factory setup</code> again for a fresh one.</p>",
      410,
    );
  }
  const factory = factoryURLOf(env);
  if (factory === null) {
    return page(
      "ticfac: factory misconfigured",
      "<h1>This factory does not know its own URL</h1><p>Re-run <code>ticfac factory deploy</code>.</p>",
      503,
    );
  }
  const target =
    flow.org === null
      ? `${webBase(env)}/settings/apps/new?state=${encodeURIComponent(state)}`
      : `${webBase(env)}/organizations/${encodeURIComponent(flow.org)}/settings/apps/new?state=${encodeURIComponent(state)}`;
  const manifest = JSON.stringify(githubAppManifest(factory, state));
  return page(
    "ticfac: create your factory's GitHub App",
    `<h1>Create your factory's GitHub App</h1>` +
      `<p>GitHub will show the App, pre-filled. Click <b>Create GitHub App</b>, then choose the repositories and click <b>Install</b>.</p>` +
      `<form id="manifest" method="post" action="${escapeHTML(target)}">` +
      `<input type="hidden" name="manifest" value="${escapeHTML(manifest)}">` +
      `<button type="submit">Continue to GitHub</button></form>` +
      `<script>document.getElementById("manifest").submit()</script>`,
  );
}

/**
 * `GET /github/app/callback?code=…&state=…`: exchanges the manifest code,
 * seals what it returns, and sends the browser on to the install page.
 */
export async function manifestCallbackRoute(
  env: Env,
  request: Request,
  options: GitHubAppOptions = {},
): Promise<Response> {
  const params = new URL(request.url).searchParams;
  const state = params.get("state");
  const code = params.get("code");
  if (!validState(state) || code === null || !/^[A-Za-z0-9_-]{1,200}$/.test(code)) {
    return page(
      "ticfac: not a callback",
      "<h1>This is not a GitHub App callback this factory started</h1>",
      400,
    );
  }
  const now = (options.now ?? Date.now)();
  const stateHash = await sha256Hex(state);
  // Consume the state FIRST, atomically: a second arrival of the same state
  // (a replayed or forged callback) finds nothing pending and is refused.
  const consumed = await env.DB.prepare(
    "UPDATE github_app_flows SET status = 'consumed' WHERE state_hash = ? AND status = 'pending' AND expires_at_ms >= ?",
  )
    .bind(stateHash, now)
    .run();
  if ((consumed.meta?.changes ?? 0) !== 1) {
    return page(
      "ticfac: link expired",
      "<h1>This callback is not for a flow this factory is waiting on</h1><p>It was already used, or it expired. Run <code>ticfac factory setup</code> again.</p>",
      410,
    );
  }
  const key = await sealingKey(env);
  if (key === null) {
    return page(
      "ticfac: factory misconfigured",
      `<h1>The ${SEALING_KEY_SECRET} secret is missing</h1><p>Run <code>ticfac factory setup</code> again.</p>`,
      503,
    );
  }
  const fetcher = options.fetcher ?? fetch;
  const response = await fetcher(
    `${apiBase(env)}/app-manifests/${encodeURIComponent(code)}/conversions`,
    {
      method: "POST",
      headers: {
        accept: "application/vnd.github+json",
        "user-agent": "ticks-factory",
        "x-github-api-version": "2022-11-28",
      },
    },
  );
  if (!response.ok) {
    const message = await githubMessage(response);
    return page(
      "ticfac: GitHub refused the exchange",
      `<h1>GitHub did not hand over the App</h1><p>${escapeHTML(`${response.status}: ${message}`)}</p><p>Run <code>ticfac factory setup</code> again.</p>`,
      502,
    );
  }
  const app = (await response.json()) as {
    id?: unknown;
    slug?: unknown;
    pem?: unknown;
    webhook_secret?: unknown;
    client_id?: unknown;
    html_url?: unknown;
    owner?: { login?: unknown } | null;
  };
  if (typeof app.id !== "number" || typeof app.slug !== "string" || typeof app.pem !== "string") {
    return page(
      "ticfac: GitHub answered oddly",
      "<h1>GitHub's answer named no App id, slug or key</h1>",
      502,
    );
  }
  await env.DB.prepare(
    "INSERT INTO github_app (singleton, app_id, slug, owner, html_url, client_id, private_key_sealed, webhook_secret_sealed, created_at) " +
      "VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?) " +
      "ON CONFLICT(singleton) DO UPDATE SET app_id = excluded.app_id, slug = excluded.slug, owner = excluded.owner, " +
      "html_url = excluded.html_url, client_id = excluded.client_id, private_key_sealed = excluded.private_key_sealed, " +
      "webhook_secret_sealed = excluded.webhook_secret_sealed, created_at = excluded.created_at",
  )
    .bind(
      String(app.id),
      app.slug,
      typeof app.owner?.login === "string" ? app.owner.login : null,
      typeof app.html_url === "string" ? app.html_url : null,
      typeof app.client_id === "string" ? app.client_id : null,
      await seal(key, app.pem),
      typeof app.webhook_secret === "string" && app.webhook_secret !== ""
        ? await seal(key, app.webhook_secret)
        : null,
      new Date(now).toISOString(),
    )
    .run();
  await env.DB.prepare(
    "UPDATE github_app_flows SET status = 'created', app_id = ? WHERE state_hash = ?",
  )
    .bind(String(app.id), stateHash)
    .run();
  // A new App is a new key: nothing cached for the old one may be served.
  resetGitHubAppCaches();
  return Response.redirect(
    `${webBase(env)}/apps/${encodeURIComponent(app.slug)}/installations/new`,
    302,
  );
}

/** `GET /github/app/installed`: where GitHub sends the browser after "Install". */
export function manifestInstalledRoute(): Response {
  return page(
    "ticfac: GitHub App installed",
    "<h1>Installed</h1><p>The factory's GitHub App is installed. You can close this page — <code>ticfac factory setup</code> picks it up from here.</p>",
  );
}
