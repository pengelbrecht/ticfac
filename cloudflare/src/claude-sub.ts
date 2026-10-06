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

/** The beta the subscription (OAuth) dialect of the Messages API needs. */
export const OAUTH_BETA = "oauth-2025-04-20";

/**
 * Where Containers puts the ephemeral CA that HTTPS interception signs with
 * (it exists only at runtime; never bake it into the image).
 */
export const CONTAINER_CA_PATH = "/etc/cloudflare/certs/cloudflare-containers-ca.crt";

/** Jobs per subscription at once unless CLAUDE_SUB_MAX_CONCURRENT says otherwise. */
export const DEFAULT_MAX_CONCURRENT = 2;

/**
 * A lease nobody released stops counting after this long — the backstop for a
 * job whose container died without its caller releasing (a review-epic job is
 * well under it).
 */
export const LEASE_TTL_MS = 2 * 60 * 60 * 1000;

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
 *  - any other 429: throttling, not the quota — benched briefly.
 *  - 401/403: the token itself is refused — benched until rotated.
 */
export function classifyAnswer(status: number, headers: Headers, now: number): Bench | null {
  if (status === 401 || status === 403) {
    return { until: now + AUTH_BENCH_MS, reason: "auth", detail: `HTTP ${status}` };
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
export function upstreamRequest(request: Request, token: string): Request {
  const url = new URL(request.url);
  url.protocol = "https:";
  url.host = CLAUDE_SUB_HOST;
  url.port = "";
  const headers = new Headers(request.headers);
  headers.delete("x-api-key");
  headers.delete("host");
  headers.set("authorization", `Bearer ${token}`);
  const betas = (headers.get("anthropic-beta") ?? "")
    .split(",")
    .map((b) => b.trim())
    .filter((b) => b !== "");
  if (!betas.includes(OAUTH_BETA)) betas.push(OAUTH_BETA);
  headers.set("anthropic-beta", betas.join(","));
  const hasBody = request.method !== "GET" && request.method !== "HEAD";
  return new Request(url.toString(), {
    method: request.method,
    headers,
    ...(hasBody ? { body: request.body } : {}),
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
    CLAUDE_CODE_OAUTH_TOKEN: CLAUDE_SUB_PLACEHOLDER,
    NODE_EXTRA_CA_CERTS: CONTAINER_CA_PATH,
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
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
};

const KEY = {
  lease: (jobId: string) => `lease:${jobId}`,
  leasePrefix: "lease:",
  bench: (label: string) => `bench:${label}`,
  stats: (label: string) => `stats:${label}`,
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

  /** Counts one answer for `label`, keeping its last limit headers. */
  async observe(label: string, status: number, limits: Record<string, string>): Promise<void> {
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
  observe(label: string, status: number, limits: Record<string, string>): Promise<void> {
    return this.core.observe(label, status, limits);
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

// ----------------------------------------------------------- the proxy ---

/**
 * The container's traffic to {@link CLAUDE_SUB_HOST}, authenticated with the
 * subscription the job leased (its props), and every answer reported to the
 * pool: a quota answer benches the subscription until its reset. The answer
 * itself goes back to the container unchanged — the job sees its own 429 and
 * ends; it is the NEXT lease that steps down, never this job.
 */
export class ClaudeSubProxy extends WorkerEntrypoint<ClaudeSubEnv, ClaudeSubProps> {
  override async fetch(request: Request): Promise<Response> {
    const { label, jobId } = this.ctx.props;
    const host = new URL(request.url).hostname.toLowerCase();
    if (host !== CLAUDE_SUB_HOST) {
      return new Response(`claude-sub proxies ${CLAUDE_SUB_HOST} only`, { status: 403 });
    }
    const token = normalizeToken(
      (this.env as Record<string, unknown>)[`${TOKEN_SECRET_PREFIX}${label}`],
    );
    if (token === null) {
      return new Response(`claude-sub: no token for subscription ${label}`, { status: 503 });
    }
    const response = await fetch(upstreamRequest(request, token));
    const pool = claudeSubPool(this.env);
    const bench = classifyAnswer(response.status, response.headers, Date.now());
    const limits = limitHeaders(response.headers);
    if (bench !== null) {
      // The token never reaches a log; the label and the answer's shape do.
      console.log(
        JSON.stringify({
          claude_sub: "benched",
          label,
          job: jobId,
          status: response.status,
          ...bench,
          limits,
        }),
      );
    }
    if (pool !== null) {
      const report = (async () => {
        if (bench !== null) await pool.bench(label, bench);
        await pool.observe(label, response.status, limits);
      })();
      this.ctx.waitUntil(report.catch(() => {}));
    }
    return response;
  }
}
