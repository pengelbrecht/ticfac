/**
 * claude-sub — a cloud job runs `claude -p` on the operator's Claude
 * SUBSCRIPTION, with the subscription's OAuth token injected by this Worker
 * and never handed to the container (spike tick jvj).
 *
 * ## The shape
 *
 *  - The token is a Worker SECRET, one per subscription:
 *    `CLAUDE_SUB_TOKEN_<LABEL>` (from `claude setup-token`). Nothing else in
 *    the factory reads it, no log prints it, and it is never in a container's
 *    env, argv or disk.
 *  - The container's claude CLI runs with a PLACEHOLDER OAuth token
 *    ({@link CLAUDE_SUB_PLACEHOLDER}): with `CLAUDE_CODE_OAUTH_TOKEN` set the
 *    CLI speaks the subscription (OAuth) dialect itself — `Authorization:
 *    Bearer <token>` plus the `oauth-2025-04-20` beta — and validates nothing
 *    locally, so the only thing the proxy changes is the bearer value.
 *  - FactorySandbox intercepts the container's traffic to
 *    {@link CLAUDE_SUB_HOST} (Containers' per-host outbound interception,
 *    `ctx.container.interceptOutboundHttp(s)`), and hands it to
 *    {@link ClaudeSubProxy}, a loopback entrypoint whose PROPS name the
 *    subscription this job leased. Props are fixed when the interception is
 *    installed, so a job can never switch tokens mid-flight.
 *  - {@link ClaudeSubPool} (one Durable Object) holds which subscriptions are
 *    usable: per-subscription concurrency leases (the capped rung), and a
 *    subscription marked EXHAUSTED on a quota answer until the reset the
 *    answer names. A job that finds every subscription exhausted or busy is
 *    told so at lease time, and its caller steps down to the Workers AI
 *    ladder rather than waiting.
 *
 * Everything here is OFF unless a deployment binds CLAUDE_SUB_POOL, exports
 * {@link ClaudeSubProxy} from its main module and sets at least one token
 * secret. Production does none of these (only cloudflare/staging does).
 */

import { DurableObject, WorkerEntrypoint } from "cloudflare:workers";

// ------------------------------------------------------------ constants ---

/** The one host whose traffic is intercepted and authenticated. */
export const CLAUDE_SUB_HOST = "api.anthropic.com";

/**
 * The OAuth "token" the container's claude CLI is given. Not a credential:
 * the proxy replaces it on every request, and a request that escapes the
 * interception reaches Anthropic with this and is refused.
 */
export const CLAUDE_SUB_PLACEHOLDER = "ticfac-claude-sub-placeholder-not-a-token";

/** The secret-name prefix of a subscription token: CLAUDE_SUB_TOKEN_<LABEL>. */
export const TOKEN_SECRET_PREFIX = "CLAUDE_SUB_TOKEN_";

/** The env name the claude CLI reads the subscription's OAuth token from. */
export const CLAUDE_CODE_OAUTH_TOKEN = "CLAUDE_CODE_OAUTH_TOKEN";

/**
 * The marker the control plane sets on a claude-sub job's process environment
 * (tick 6fv): what image/common.sh's claude-sub route keys on. The pair
 * (placeholder, marker) is what tells the container's entrypoint its claude
 * traffic is billed to the operator's subscription and intercepted by this
 * Worker — never per-token through the gateway.
 */
export const TICKS_CLAUDE_SUB = "TICKS_CLAUDE_SUB";

/**
 * The claude-sub rung, the TypeScript half of [profile.CloudRule]'s
 * SubscriptionRungs (internal/profile/cloudworker.go, tick 6fv): the harness
 * whose CLI speaks the subscription's OAuth dialect, and the VERSIONLESS
 * aliases a config may name on it — never a pinned id, which bills per token
 * and the rule refuses. The Go parity test
 * (internal/profile/cloudworker_test.go) pins this list to CloudRule's, so
 * the two languages cannot admit different rungs.
 */
export const SUBSCRIPTION_HARNESS = "claude";
export const SUBSCRIPTION_ALIASES = ["sonnet", "opus"] as const;

/**
 * Whether a resolved harness/model pair IS the subscription rung — the
 * factory's half of the routing rule: a pair this answers true for leases a
 * subscription at dispatch and falls back to the Workers AI rung when none
 * is free; every other pair never touches the pool at all.
 */
export function isSubscriptionRung(
  harness: string | null | undefined,
  model: string | null | undefined,
): boolean {
  if (harness !== SUBSCRIPTION_HARNESS) return false;
  return (SUBSCRIPTION_ALIASES as readonly string[]).includes(model ?? "");
}

/** The beta the subscription (OAuth) dialect of the Messages API needs. */
export const OAUTH_BETA = "oauth-2025-04-20";

/**
 * What the proxy forwards with the subscription's token: the inference calls a
 * `claude -p` worker makes, and the startup reads the pinned CLI makes before
 * them — and NOTHING else. The token is the operator's whole subscription, so
 * the interception is an allowlist, never a pass-through: a request the list
 * does not name is refused 403 (and its path logged) before the token is ever
 * attached.
 *
 * The startup half was observed, not guessed: the pinned CLI (2.1.227) run the
 * way a worker runs it — `-p`, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1, the
 * placeholder OAuth token, no base URL — against a fake api.anthropic.com
 * behind a test CA called HEAD /api/hello, GET /api/claude_code/policy_limits,
 * GET /api/claude_code/settings and POST /v1/messages?beta=true. HEAD
 * /api/hello — the CLI's connectivity check, sent on every start with no
 * credential — showed only inside the Linux container (the image's real-CLI
 * smoke test, internal/sandboximage/claude_cli_smoke_test.go, which holds a
 * pin bump to this list); a proxy without it refused every claude-sub start.
 *
 * Only `inference` answers say anything about the subscription's quota or
 * token ({@link classifyAnswer}); a startup read's 401/403/429 is that
 * endpoint's own, and never benches a subscription. A `connectivity` check is
 * forwarded WITHOUT the token: the CLI sends it unauthenticated, and the
 * token goes nowhere it is not needed.
 */
export type ClaudeSubRouteKind = "inference" | "startup" | "connectivity";
export const CLAUDE_SUB_ROUTES: readonly {
  method: string;
  path: string;
  kind: ClaudeSubRouteKind;
}[] = [
  { method: "POST", path: "/v1/messages", kind: "inference" },
  { method: "POST", path: "/v1/messages/count_tokens", kind: "inference" },
  { method: "GET", path: "/api/claude_code/policy_limits", kind: "startup" },
  { method: "GET", path: "/api/claude_code/settings", kind: "startup" },
  { method: "HEAD", path: "/api/hello", kind: "connectivity" },
];

/** The allowlisted route a request is, or null when the proxy must refuse it. */
export function allowedRoute(method: string, pathname: string): ClaudeSubRouteKind | null {
  const verb = method.toUpperCase();
  const route = CLAUDE_SUB_ROUTES.find((r) => r.method === verb && r.path === pathname);
  return route?.kind ?? null;
}

/**
 * Betas the proxy refuses: each one bills outside the subscription's own
 * quota. The 1M-token context window is charged as Extra Usage (per token) on
 * a subscription — the one billing the cloud must never draw.
 */
export const REFUSED_BETAS: readonly RegExp[] = [/^context-1m-/i];

/**
 * Betas the proxy STRIPS rather than refuses: ones the pinned CLI sends on
 * every request of a rung (so refusing them would refuse the rung) whose
 * billing is not shown to stay inside the subscription. 2.1.227 sends
 * `fallback-credit-2026-06-01` on every opus request (the image's real-CLI
 * smoke test saw it); a credit-backed fallback is exactly the per-token
 * drawing the cloud must never do, and without the beta the request is the
 * plain subscription one.
 */
export const STRIPPED_BETAS: readonly RegExp[] = [/^fallback-credit-/i];

/** The first beta of a request the proxy refuses, or null. */
export function refusedBeta(header: string | null): string | null {
  for (const beta of (header ?? "").split(",").map((b) => b.trim())) {
    if (beta !== "" && REFUSED_BETAS.some((re) => re.test(beta))) return beta;
  }
  return null;
}

/**
 * Whether a Messages request's model is one the subscription bills: a concrete
 * claude model id, which is what the CLI resolves `sonnet`/`opus` to before it
 * sends (2.1.227: claude-sonnet-5, claude-opus-5). Anything else — an absent
 * model, another vendor's, a `[1m]` suffix that escaped the CLI — has no
 * business on the operator's token.
 */
export function subscriptionModel(model: unknown): model is string {
  return typeof model === "string" && /^claude-[a-z0-9.-]+$/i.test(model);
}

/**
 * Whether an answer says the subscription is drawing USAGE CREDITS: its
 * window is spent and Anthropic served the request as Extra Usage, billed per
 * token. The operator's rule is no per-token billing in the cloud, so this is
 * a quota answer however the request itself fared.
 */
export function overageInUse(headers: Headers): boolean {
  return (headers.get("anthropic-ratelimit-unified-overage-in-use") ?? "").toLowerCase() === "true";
}

/**
 * Where Containers puts the ephemeral CA that HTTPS interception signs with
 * (it exists only at runtime; never bake it into the image).
 */
export const CONTAINER_CA_PATH = "/etc/cloudflare/certs/cloudflare-containers-ca.crt";

/** Jobs per subscription at once unless CLAUDE_SUB_MAX_CONCURRENT says otherwise. */
export const DEFAULT_MAX_CONCURRENT = 2;

/**
 * A lease nobody released stops counting after this long — the backstop for a
 * job whose container died without its caller releasing. A job that is still
 * talking to Anthropic keeps its lease fresh ({@link LEASE_REFRESH_MS}), so the
 * TTL measures SILENCE, not the job's length: a worker may run for hours (its
 * wall is the dispatch's), and a lease that lapsed under a live job would hand
 * its cap slot to another one.
 */
export const LEASE_TTL_MS = 2 * 60 * 60 * 1000;

/**
 * How often, at most, the proxy refreshes a job's lease as its traffic passes:
 * once a minute per job is plenty against a two-hour TTL, and keeps the pool's
 * Durable Object out of the per-request path.
 */
export const LEASE_REFRESH_MS = 60 * 1000;

/**
 * How long the pool remembers that a JOB's own answer was a quota answer (its
 * subscription spent mid-job): long enough for the orchestrator's collect to
 * ask, which follows the job's end by minutes, not days.
 */
export const JOB_QUOTA_TTL_MS = 24 * 60 * 60 * 1000;

/**
 * How long a subscription drawing usage credits is benched when the answer
 * names no reset: the five-hour window, the shortest a subscription has.
 */
export const OVERAGE_BENCH_FALLBACK_MS = 5 * 60 * 60 * 1000;

/** How long a 429 that is NOT a quota answer (server-side throttling) benches a subscription. */
export const THROTTLE_COOLDOWN_MS = 60 * 1000;

/** How long a refused token (401/403) is benched: until someone rotates it. */
export const AUTH_BENCH_MS = 24 * 60 * 60 * 1000;

const LABEL = /^[A-Z0-9_]{1,32}$/;

// --------------------------------------------------------------- types ---

/** The bindings this module reads. All optional: absent means off. */
export type ClaudeSubEnv = {
  CLAUDE_SUB_POOL?: DurableObjectNamespace<ClaudeSubPool>;
  CLAUDE_SUB_MAX_CONCURRENT?: string;
  [secret: `${typeof TOKEN_SECRET_PREFIX}${string}`]: string | undefined;
};

/** What a job's interception carries: the subscription it leased, and who it is. */
export type ClaudeSubProps = { label: string; jobId: string };

/** Why a subscription is benched, and until when (epoch ms). */
export type Bench = { until: number; reason: "quota" | "throttle" | "auth"; detail: string };

/** One subscription's state, as the pool reports it (never the token). */
export type SubscriptionView = {
  label: string;
  active_leases: string[];
  benched: Bench | null;
  requests: number;
  limited: number;
  last_status: number | null;
  /** The last `anthropic-ratelimit-unified-*` headers seen, verbatim. */
  last_limits: Record<string, string>;
  /**
   * Per allowlisted path (`POST /v1/messages`, …): how many answers and the
   * last status — so a startup read failing reads apart from inference.
   */
  routes: Record<string, { requests: number; last_status: number }>;
  /**
   * The concrete models the CLI sent, counted: what `sonnet`/`opus`
   * resolved to inside the pinned CLI (2.1.227: claude-sonnet-5,
   * claude-opus-5), stated rather than inferred from the pin.
   */
  models: Record<string, number>;
  last_model: string | null;
};

/**
 * One job's quota answer, remembered for the job's collect (finding: a job
 * whose subscription ran out MID-JOB is infrastructure, not a failed attempt
 * at the tick — the collect asks the pool, which saw the wire).
 */
export type JobQuota = { label: string; bench: Bench; at: number };

/** What one answer through the proxy was, for the pool's per-path stats. */
export type Observation = {
  status: number;
  limits: Record<string, string>;
  /** `METHOD /path` of the allowlisted route. */
  route?: string;
  /** The concrete model a Messages request named. */
  model?: string;
};

export type LeaseOutcome =
  | { ok: true; label: string; reused: boolean }
  | {
      ok: false;
      /** `none` no token configured; `exhausted` every one benched; `busy` every usable one at its cap. */
      reason: "none" | "exhausted" | "busy";
      /** When the earliest benched subscription comes back (epoch ms), if any. */
      retry_at: number | null;
    };

// ------------------------------------------------------- pure functions ---

/** The subscription labels this deployment has a token secret for, sorted. */
export function subscriptionLabels(env: Record<string, unknown>): string[] {
  const labels: string[] = [];
  for (const [key, value] of Object.entries(env)) {
    if (!key.startsWith(TOKEN_SECRET_PREFIX)) continue;
    const label = key.slice(TOKEN_SECRET_PREFIX.length);
    if (LABEL.test(label) && normalizeToken(value) !== null) labels.push(label);
  }
  return labels.sort();
}

/**
 * A token secret as it is used: every whitespace character removed, or null
 * when nothing is left. `claude setup-token` prints the token wrapped across
 * terminal lines, and a paste into `wrangler secret put` keeps the break
 * INSIDE the token — the first real secret on staging was 109 characters with
 * one newline in it, and Anthropic answered "401 OAuth access token is
 * invalid". A token never contains whitespace, so none is ever meant.
 */
export function normalizeToken(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  const token = raw.replace(/\s+/g, "");
  return token === "" ? null : token;
}

/** The cap per subscription: CLAUDE_SUB_MAX_CONCURRENT when it is a positive integer. */
export function maxConcurrent(env: { CLAUDE_SUB_MAX_CONCURRENT?: string }): number {
  const n = Number(env.CLAUDE_SUB_MAX_CONCURRENT);
  return Number.isInteger(n) && n > 0 ? n : DEFAULT_MAX_CONCURRENT;
}

/** The `anthropic-ratelimit-unified-*` headers of an answer, and retry-after. */
export function limitHeaders(headers: Headers): Record<string, string> {
  const out: Record<string, string> = {};
  headers.forEach((value, key) => {
    const k = key.toLowerCase();
    if (k.startsWith("anthropic-ratelimit-unified") || k === "retry-after") out[k] = value;
  });
  return out;
}

/**
 * What an answer from Anthropic says about the SUBSCRIPTION behind it, or
 * null when it says nothing (success, a 5xx, a bad request).
 *
 *  - 429 with the unified limiter rejecting (`anthropic-ratelimit-unified-
 *    status: rejected`, or a unified reset): the subscription's quota is
 *    spent — benched until the reset it names (or retry-after).
 *  - any answer — a 200 included — with `overage-in-use: true`: the window
 *    is spent and the request was served on usage credits, per token. That
 *    is the quota too (the operator's rule: no per-token billing in the
 *    cloud), benched until the window's reset.
 *  - any other 429: throttling, not the quota — benched briefly.
 *  - 401/403: the token itself is refused — benched until rotated.
 *
 * The caller classifies INFERENCE answers only ({@link allowedRoute}): a
 * startup read's status is that endpoint's, never the subscription's.
 */
export function classifyAnswer(status: number, headers: Headers, now: number): Bench | null {
  if (status === 401 || status === 403) {
    return { until: now + AUTH_BENCH_MS, reason: "auth", detail: `HTTP ${status}` };
  }
  if (overageInUse(headers)) {
    const reset =
      parseReset(headers.get("anthropic-ratelimit-unified-reset")) ??
      parseReset(headers.get("anthropic-ratelimit-unified-overage-reset"));
    return {
      until: Math.max(reset ?? now + OVERAGE_BENCH_FALLBACK_MS, now + 1000),
      reason: "quota",
      detail: `HTTP ${status}, overage in use: the window is spent and the answer drew usage credits`,
    };
  }
  if (status !== 429) return null;
  const unified = (headers.get("anthropic-ratelimit-unified-status") ?? "").toLowerCase();
  const resetRaw = headers.get("anthropic-ratelimit-unified-reset");
  const retryAfter = Number(headers.get("retry-after"));
  const retryAt = Number.isFinite(retryAfter) && retryAfter > 0 ? now + retryAfter * 1000 : null;
  const reset = parseReset(resetRaw);
  // EVERY answer carries the unified headers (status "allowed", a reset), so a
  // reset alone proves nothing: only a rejection — overall or of one window
  // (`-5h-status`, `-7d-status`) — is the quota. A 429 the limiter allowed is
  // throttling.
  // A window's rejection — `-5h-status`, `-7d-status` — is the quota on a
  // 429. The overage half rejected is NOT, alone: an org with overage
  // disabled carries `overage-status: rejected` on every answer (staging,
  // 2026-10-06), so it says only that no credits back the window. With the
  // window itself rejected (above), it is the quota either way.
  let windowRejected = false;
  headers.forEach((value, key) => {
    if (/^anthropic-ratelimit-unified-[a-z0-9_]+-status$/i.test(key) && /^rejected$/i.test(value)) {
      if (!/overage/i.test(key)) windowRejected = true;
    }
  });
  if (unified === "rejected" || windowRejected || (unified === "" && reset !== null)) {
    const until = reset ?? retryAt ?? now + THROTTLE_COOLDOWN_MS;
    const claim = headers.get("anthropic-ratelimit-unified-representative-claim");
    return {
      until: Math.max(until, now + 1000),
      reason: "quota",
      detail: `HTTP 429, unified ${unified || "?"}${claim ? `, claim ${claim}` : ""}`,
    };
  }
  return {
    until: retryAt ?? now + THROTTLE_COOLDOWN_MS,
    reason: "throttle",
    detail: "HTTP 429 without a unified quota rejection",
  };
}

/** A unified reset: epoch seconds (what Anthropic sends) or an ISO date. */
export function parseReset(raw: string | null): number | null {
  if (raw === null || raw.trim() === "") return null;
  const n = Number(raw);
  if (Number.isFinite(n) && n > 0) return n < 1e12 ? n * 1000 : n;
  const t = Date.parse(raw);
  return Number.isNaN(t) ? null : t;
}

/**
 * The request the proxy sends upstream: the container's request to the real
 * host over HTTPS, its credentials replaced by the subscription token, the
 * OAuth beta guaranteed.
 */
export function upstreamRequest(request: Request, token: string | null, body?: string): Request {
  const url = new URL(request.url);
  url.protocol = "https:";
  url.host = CLAUDE_SUB_HOST;
  url.port = "";
  const headers = new Headers(request.headers);
  headers.delete("x-api-key");
  headers.delete("host");
  if (token === null) {
    // A connectivity check: the CLI sends it with no credential, and it
    // travels upstream with none — the token goes nowhere it is not needed.
    headers.delete("authorization");
  } else {
    headers.set("authorization", `Bearer ${token}`);
    const betas = (headers.get("anthropic-beta") ?? "")
      .split(",")
      .map((b) => b.trim())
      .filter((b) => b !== "" && !STRIPPED_BETAS.some((re) => re.test(b)));
    if (!betas.includes(OAUTH_BETA)) betas.push(OAUTH_BETA);
    headers.set("anthropic-beta", betas.join(","));
  }
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  return new Request(url.toString(), {
    method: request.method,
    headers,
    ...(hasBody ? { body: body ?? request.body } : {}),
    redirect: "manual",
  });
}

/**
 * The environment a claude-sub process runs `claude -p` in: the placeholder
 * token (which puts the CLI in its subscription dialect), the interception's
 * CA for the CLI's own TLS stack, and no telemetry to hosts nobody intercepts.
 * ANTHROPIC_API_KEY must NOT be set beside it: an API key wins over OAuth.
 */
export function claudeSubProcessEnv(): Record<string, string> {
  return {
    [CLAUDE_CODE_OAUTH_TOKEN]: CLAUDE_SUB_PLACEHOLDER,
    NODE_EXTRA_CA_CERTS: CONTAINER_CA_PATH,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
    [TICKS_CLAUDE_SUB]: "1",
  };
}

// ------------------------------------------------------------ the pool ---

/** The storage subset the pool uses (a Durable Object's, or a test's map). */
export type PoolStorage = Pick<DurableObjectStorage, "get" | "put" | "delete" | "list">;

type Lease = { label: string; at: number };
type Stats = {
  requests: number;
  limited: number;
  last_status: number | null;
  last_limits: Record<string, string>;
  routes?: Record<string, { requests: number; last_status: number }>;
  models?: Record<string, number>;
  last_model?: string | null;
};

const KEY = {
  lease: (jobId: string) => `lease:${jobId}`,
  leasePrefix: "lease:",
  bench: (label: string) => `bench:${label}`,
  stats: (label: string) => `stats:${label}`,
  jobQuota: (jobId: string) => `jobquota:${jobId}`,
  jobQuotaPrefix: "jobquota:",
} as const;

/** The pool's behaviour over a {@link PoolStorage} (tested directly). */
export class ClaudeSubPoolCore {
  constructor(
    private readonly storage: PoolStorage,
    private readonly now: () => number = Date.now,
  ) {}

  /**
   * A subscription for `jobId`. Sticky: a job that already holds a lease gets
   * the same subscription back, benched or not — a job never switches tokens
   * (its interception is already bound to it). A new job gets the usable
   * subscription with the fewest live leases under `cap`.
   */
  async lease(jobId: string, labels: string[], cap: number): Promise<LeaseOutcome> {
    const now = this.now();
    const held = await this.storage.get<Lease>(KEY.lease(jobId));
    if (held !== undefined && now - held.at < LEASE_TTL_MS) {
      return { ok: true, label: held.label, reused: true };
    }
    if (labels.length === 0) return { ok: false, reason: "none", retry_at: null };
    const active = await this.activeLeases(now);
    let best: { label: string; count: number } | null = null;
    let benchedCount = 0;
    let retryAt: number | null = null;
    for (const label of labels) {
      const bench = await this.bench(label, now);
      if (bench !== null) {
        benchedCount += 1;
        retryAt = retryAt === null ? bench.until : Math.min(retryAt, bench.until);
        continue;
      }
      const count = active.get(label)?.length ?? 0;
      if (count >= cap) continue;
      if (best === null || count < best.count) best = { label, count };
    }
    if (best === null) {
      return {
        ok: false,
        reason: benchedCount === labels.length ? "exhausted" : "busy",
        retry_at: retryAt,
      };
    }
    await this.storage.put(KEY.lease(jobId), { label: best.label, at: now } satisfies Lease);
    return { ok: true, label: best.label, reused: false };
  }

  /** Ends `jobId`'s lease. Releasing nothing is fine. */
  async release(jobId: string): Promise<void> {
    await this.storage.delete(KEY.lease(jobId));
  }

  /**
   * Keeps a LIVE job's lease from lapsing: its proxy calls this as the job's
   * traffic passes ({@link LEASE_REFRESH_MS}), so the TTL only ever reclaims
   * a lease whose job has gone silent. A released lease stays released —
   * a straggling request after the job's end must not resurrect a cap slot —
   * and a refresh within the interval writes nothing.
   */
  async touch(jobId: string): Promise<void> {
    const now = this.now();
    const held = await this.storage.get<Lease>(KEY.lease(jobId));
    if (held === undefined || now - held.at < LEASE_REFRESH_MS) return;
    await this.storage.put(KEY.lease(jobId), { label: held.label, at: now } satisfies Lease);
  }

  /**
   * Remembers that `jobId`'s own answer benched its subscription on the quota
   * — the wire truth its collect reads ({@link jobQuota}) to tell a job the
   * subscription ran out under from one that failed at its tick. The first
   * record stands: it is the moment the job's quota ended.
   */
  async recordJobQuota(jobId: string, label: string, bench: Bench): Promise<void> {
    const now = this.now();
    for (const [key, record] of await this.storage.list<JobQuota>({
      prefix: KEY.jobQuotaPrefix,
    })) {
      if (now - record.at >= JOB_QUOTA_TTL_MS) await this.storage.delete(key);
    }
    if ((await this.jobQuota(jobId)) !== null) return;
    await this.storage.put(KEY.jobQuota(jobId), { label, bench, at: now } satisfies JobQuota);
  }

  /** The quota answer `jobId` ran into, or null (none, or long forgotten). */
  async jobQuota(jobId: string): Promise<JobQuota | null> {
    const record = await this.storage.get<JobQuota>(KEY.jobQuota(jobId));
    return record !== undefined && this.now() - record.at < JOB_QUOTA_TTL_MS ? record : null;
  }

  /** Benches `label` until `bench.until` — never shortens a longer bench. */
  async benchLabel(label: string, bench: Bench): Promise<void> {
    const current = await this.storage.get<Bench>(KEY.bench(label));
    if (current !== undefined && current.until >= bench.until) return;
    await this.storage.put(KEY.bench(label), bench);
  }

  /** Clears a bench (an operator's reset, or an experiment's). */
  async unbench(label: string): Promise<void> {
    await this.storage.delete(KEY.bench(label));
  }

  /**
   * Counts one answer for `label`: its status, its last limit headers, its
   * route and the concrete model the request named.
   */
  async observe(label: string, observation: Observation): Promise<void> {
    const { status, limits, route, model } = observation;
    const stats = (await this.storage.get<Stats>(KEY.stats(label))) ?? {
      requests: 0,
      limited: 0,
      last_status: null,
      last_limits: {},
    };
    stats.requests += 1;
    if (status === 429) stats.limited += 1;
    stats.last_status = status;
    if (Object.keys(limits).length > 0) stats.last_limits = limits;
    if (route !== undefined) {
      const routes = stats.routes ?? {};
      routes[route] = { requests: (routes[route]?.requests ?? 0) + 1, last_status: status };
      stats.routes = routes;
    }
    if (model !== undefined) {
      const models = stats.models ?? {};
      models[model] = (models[model] ?? 0) + 1;
      stats.models = models;
      stats.last_model = model;
    }
    await this.storage.put(KEY.stats(label), stats);
  }

  /** Every configured subscription's state. Never a token. */
  async snapshot(labels: string[]): Promise<SubscriptionView[]> {
    const now = this.now();
    const active = await this.activeLeases(now);
    const views: SubscriptionView[] = [];
    for (const label of labels) {
      const stats = await this.storage.get<Stats>(KEY.stats(label));
      views.push({
        label,
        active_leases: active.get(label) ?? [],
        benched: await this.bench(label, now),
        requests: stats?.requests ?? 0,
        limited: stats?.limited ?? 0,
        last_status: stats?.last_status ?? null,
        last_limits: stats?.last_limits ?? {},
        routes: stats?.routes ?? {},
        models: stats?.models ?? {},
        last_model: stats?.last_model ?? null,
      });
    }
    return views;
  }

  private async bench(label: string, now: number): Promise<Bench | null> {
    const bench = await this.storage.get<Bench>(KEY.bench(label));
    return bench !== undefined && bench.until > now ? bench : null;
  }

  /** Live leases by label (jobs), expired ones dropped. */
  private async activeLeases(now: number): Promise<Map<string, string[]>> {
    const byLabel = new Map<string, string[]>();
    for (const [key, lease] of await this.storage.list<Lease>({ prefix: KEY.leasePrefix })) {
      if (now - lease.at >= LEASE_TTL_MS) {
        await this.storage.delete(key);
        continue;
      }
      const jobs = byLabel.get(lease.label) ?? [];
      jobs.push(key.slice(KEY.leasePrefix.length));
      byLabel.set(lease.label, jobs);
    }
    return byLabel;
  }
}

/**
 * The pool's Durable Object: ONE instance (`idFromName("pool")`), so every
 * lease and bench is decided in one place.
 */
export class ClaudeSubPool extends DurableObject<ClaudeSubEnv> {
  private readonly core: ClaudeSubPoolCore;

  constructor(ctx: DurableObjectState, env: ClaudeSubEnv) {
    super(ctx, env);
    this.core = new ClaudeSubPoolCore(ctx.storage);
  }

  lease(jobId: string): Promise<LeaseOutcome> {
    return this.core.lease(
      jobId,
      subscriptionLabels(this.env as Record<string, unknown>),
      maxConcurrent(this.env),
    );
  }
  release(jobId: string): Promise<void> {
    return this.core.release(jobId);
  }
  bench(label: string, bench: Bench): Promise<void> {
    return this.core.benchLabel(label, bench);
  }
  unbench(label: string): Promise<void> {
    return this.core.unbench(label);
  }
  observe(label: string, observation: Observation): Promise<void> {
    return this.core.observe(label, observation);
  }
  touch(jobId: string): Promise<void> {
    return this.core.touch(jobId);
  }
  recordJobQuota(jobId: string, label: string, bench: Bench): Promise<void> {
    return this.core.recordJobQuota(jobId, label, bench);
  }
  jobQuota(jobId: string): Promise<JobQuota | null> {
    return this.core.jobQuota(jobId);
  }
  snapshot(): Promise<SubscriptionView[]> {
    return this.core.snapshot(subscriptionLabels(this.env as Record<string, unknown>));
  }
}

/** The pool's one instance, or null when this deployment binds none. */
export function claudeSubPool(env: ClaudeSubEnv): DurableObjectStub<ClaudeSubPool> | null {
  const ns = env.CLAUDE_SUB_POOL;
  return ns === undefined ? null : ns.get(ns.idFromName("pool"));
}

/**
 * The pool's release half as one function (tick 6fv): what a caller that
 * ends a job — collect, cancel, the settled container's reclaim — calls with
 * the job id the lease was taken under. Releasing a job that never leased is
 * a no-op; the lease's TTL is the backstop for an ending nothing observes.
 * Undefined when this deployment wires no pool.
 */
export function claudeSubRelease(
  env: ClaudeSubEnv,
): ((jobId: string) => Promise<void>) | undefined {
  const pool = claudeSubPool(env);
  // An arrow, never `pool.release.bind(pool)`: an RPC stub carries no `bind`.
  return pool === null ? undefined : (jobId: string) => pool.release(jobId);
}

// ------------------------------------------------------- the operator view ---

/**
 * The pool's OPERATOR surface, mounted at /api/claude-sub in production (tick
 * 6fv): which subscriptions this deployment has a token for, their leases,
 * their benches and the last limit headers the proxy saw — never a token
 * value, so the whole answer is safe to paste into a log or a terminal.
 *
 * The one action is UNBENCHING a label: a subscription benched `auth` until
 * its token is rotated stays benched until somebody rotates it and says so
 * here — the rotation itself is a secret (`wrangler secret put
 * CLAUDE_SUB_TOKEN_<LABEL>`), which this Worker can never see being done.
 *
 * Authentication is NOT this function's: it mounts behind the factory's own
 * bearer check like every other /api route, because the caller is the
 * operator reading what their subscriptions are doing, never a run.
 */
export async function claudeSubRoute(request: Request, env: ClaudeSubEnv): Promise<Response> {
  const pool = claudeSubPool(env);
  if (pool === null) {
    return Response.json(
      {
        error: "no_claude_sub_pool",
        detail:
          "this deployment binds no CLAUDE_SUB_POOL, so no claude-sub job can lease a subscription — every one falls back to the Workers AI ladder",
      },
      { status: 503 },
    );
  }
  const labels = subscriptionLabels(env as Record<string, unknown>);
  const url = new URL(request.url);
  // The pool view itself, and the one action under it. Anything else under
  // /api/claude-sub is a 404 rather than a silently answered GET: an
  // operator's typo should not read like the pool's whole answer.
  const unbench = /^\/api\/claude-sub\/unbench\/([A-Za-z0-9_]{1,32})$/.exec(url.pathname);
  if (unbench === null && url.pathname !== "/api/claude-sub") {
    return Response.json({ error: "not_found", labels }, { status: 404 });
  }
  if (unbench !== null) {
    if (request.method !== "POST") return new Response("method not allowed", { status: 405 });
    const label = unbench[1]!;
    if (!labels.includes(label)) {
      return Response.json(
        {
          error: "unknown_subscription",
          detail: `no CLAUDE_SUB_TOKEN_${label} secret is configured on this deployment`,
          labels,
        },
        { status: 404 },
      );
    }
    await pool.unbench(label);
  } else if (request.method !== "GET") {
    return new Response("method not allowed", { status: 405 });
  }
  return Response.json({ labels, subscriptions: await pool.snapshot() });
}

// ----------------------------------------------------------- the proxy ---

/** The pool's half the proxy reports to (the Durable Object's stub, or a test's). */
export type ProxyPool = {
  bench(label: string, bench: Bench): Promise<void>;
  observe(label: string, observation: Observation): Promise<void>;
  touch(jobId: string): Promise<void>;
  recordJobQuota(jobId: string, label: string, bench: Bench): Promise<void>;
};

/** What {@link proxyClaudeSub} runs on: the token, the wire, the pool, a clock. */
export type ProxyDeps = {
  /** The leased subscription's token, normalized, or null when none is set. */
  token: string | null;
  upstream: (request: Request) => Promise<Response>;
  pool: ProxyPool | null;
  now: () => number;
  /** Background work the answer must not wait for (the pool's bookkeeping). */
  waitUntil: (work: Promise<unknown>) => void;
  log: (line: string) => void;
  /** When each job's lease was last refreshed from this isolate. */
  touched: Map<string, number>;
};

/** Refreshes per isolate are remembered for this many jobs at most. */
const TOUCHED_CAP = 1000;

/** The isolate's memory of lease refreshes, shared by every proxy call in it. */
const isolateTouched = new Map<string, number>();

/**
 * An answer in the Messages API's own error shape, so the CLI prints the
 * proxy's sentence rather than a bare status.
 */
function refusal(
  status: number,
  type: string,
  message: string,
  headers: Record<string, string> = {},
): Response {
  return new Response(JSON.stringify({ type: "error", error: { type, message } }), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });
}

/**
 * One request from a job's container to {@link CLAUDE_SUB_HOST}, as the proxy
 * answers it:
 *
 *  1. refused unless it is an allowlisted route ({@link CLAUDE_SUB_ROUTES}),
 *     carries no beta that bills per token ({@link REFUSED_BETAS}) and — for
 *     inference — names a claude model; the refusal is logged by path, and
 *     the token is never attached to it;
 *  2. the job's lease refreshed, at most once a minute ({@link LEASE_REFRESH_MS});
 *  3. forwarded with the subscription's token;
 *  4. an INFERENCE answer classified ({@link classifyAnswer}) — a startup
 *     read's never is — and a bench reported to the pool; a quota bench is
 *     also remembered against THIS job, for its collect;
 *  5. an answer served on usage credits (overage in use) is not handed back:
 *     the job gets a 429 instead, so it stops drawing per-token billing at
 *     the first answer that did.
 */
export async function proxyClaudeSub(
  request: Request,
  props: ClaudeSubProps,
  deps: ProxyDeps,
): Promise<Response> {
  const { label, jobId } = props;
  const url = new URL(request.url);
  if (url.hostname.toLowerCase() !== CLAUDE_SUB_HOST) {
    return new Response(`claude-sub proxies ${CLAUDE_SUB_HOST} only`, { status: 403 });
  }
  const route = `${request.method.toUpperCase()} ${url.pathname}`;
  const kind = allowedRoute(request.method, url.pathname);
  const refuse = (why: string, message: string) => {
    deps.log(JSON.stringify({ claude_sub: "refused", label, job: jobId, route, why }));
    return refusal(403, "permission_error", `claude-sub proxy: ${message}`);
  };
  if (kind === null) {
    return refuse(
      "not_allowlisted",
      `${route} is not a route this proxy forwards on a subscription token`,
    );
  }
  const beta = refusedBeta(request.headers.get("anthropic-beta"));
  if (beta !== null) {
    return refuse(
      "refused_beta",
      `the ${beta} beta bills per token outside the subscription, and the cloud never draws per-token billing`,
    );
  }
  let body: string | undefined;
  let model: string | undefined;
  if (kind === "inference") {
    body = await request.text();
    let named: unknown;
    try {
      named = (JSON.parse(body) as { model?: unknown }).model;
    } catch {
      named = undefined;
    }
    if (!subscriptionModel(named)) {
      return refuse(
        "refused_model",
        `the request names model ${JSON.stringify(named ?? null)}, not a claude model the subscription bills`,
      );
    }
    model = named;
  }
  if (deps.token === null && kind !== "connectivity") {
    return new Response(`claude-sub: no token for subscription ${label}`, { status: 503 });
  }

  const now = deps.now();
  const pool = deps.pool;
  if (pool !== null && now - (deps.touched.get(jobId) ?? 0) >= LEASE_REFRESH_MS) {
    if (deps.touched.size >= TOUCHED_CAP) deps.touched.clear();
    deps.touched.set(jobId, now);
    deps.waitUntil(pool.touch(jobId).catch(() => {}));
  }

  const response = await deps.upstream(
    upstreamRequest(request, kind === "connectivity" ? null : deps.token, body),
  );
  const limits = limitHeaders(response.headers);
  const bench =
    kind === "inference" ? classifyAnswer(response.status, response.headers, now) : null;
  if (bench !== null) {
    // The token never reaches a log; the label and the answer's shape do.
    deps.log(
      JSON.stringify({
        claude_sub: "benched",
        label,
        job: jobId,
        route,
        status: response.status,
        ...bench,
        limits,
      }),
    );
  }
  if (pool !== null) {
    deps.waitUntil(
      (async () => {
        if (bench !== null) {
          await pool.bench(label, bench);
          if (bench.reason === "quota") await pool.recordJobQuota(jobId, label, bench);
        }
        await pool.observe(label, {
          status: response.status,
          limits,
          route,
          ...(model === undefined ? {} : { model }),
        });
      })().catch(() => {}),
    );
  }
  if (kind === "inference" && overageInUse(response.headers) && bench !== null) {
    // Served on usage credits: the answer is dropped, never relayed, and the
    // job sees the quota it ran into. Its retry-after names the reset, so the
    // CLI stops rather than asking again (2.1.227 exits on such a 429).
    await response.body?.cancel().catch(() => {});
    const retryAfter = Math.max(1, Math.ceil((bench.until - now) / 1000));
    return refusal(
      429,
      "rate_limit_error",
      `claude-sub proxy: subscription ${label}'s usage window is spent and Anthropic served this ` +
        `request on usage credits (per token); the cloud never draws per-token billing, so the ` +
        `subscription is benched until ${new Date(bench.until).toISOString()} and this job stops here`,
      {
        "anthropic-ratelimit-unified-status": "rejected",
        "anthropic-ratelimit-unified-reset": String(Math.floor(bench.until / 1000)),
        "retry-after": String(retryAfter),
      },
    );
  }
  return response;
}

/**
 * The container's traffic to {@link CLAUDE_SUB_HOST}, authenticated with the
 * subscription the job leased (its props) — {@link proxyClaudeSub} is the
 * whole of what it does, over the deployment's own token, wire and pool.
 * The answer goes back to the container unchanged (but for an overage answer,
 * which is withheld) — the job sees its own 429 and ends; it is the NEXT
 * lease that steps down, never this job.
 */
export class ClaudeSubProxy extends WorkerEntrypoint<ClaudeSubEnv, ClaudeSubProps> {
  override async fetch(request: Request): Promise<Response> {
    const { label } = this.ctx.props;
    return proxyClaudeSub(request, this.ctx.props, {
      token: normalizeToken(
        (this.env as Record<string, unknown>)[`${TOKEN_SECRET_PREFIX}${label}`],
      ),
      upstream: (r) => fetch(r),
      pool: claudeSubPool(this.env),
      now: Date.now,
      waitUntil: (work) => this.ctx.waitUntil(work),
      log: (line) => console.log(line),
      touched: isolateTouched,
    });
  }
}
