import { describe, expect, it } from "vitest";
import {
  AUTH_BENCH_MS,
  CLAUDE_SUB_HOST,
  CLAUDE_SUB_PLACEHOLDER,
  ClaudeSubPoolCore,
  classifyAnswer,
  claudeSubProcessEnv,
  DEFAULT_MAX_CONCURRENT,
  LEASE_TTL_MS,
  limitHeaders,
  maxConcurrent,
  normalizeToken,
  OAUTH_BETA,
  type PoolStorage,
  parseReset,
  subscriptionLabels,
  THROTTLE_COOLDOWN_MS,
  upstreamRequest,
} from "../src/claude-sub";

const NOW = 1_800_000_000_000;

function memoryStorage(): PoolStorage {
  const store = new Map<string, unknown>();
  return {
    async get(key: string) {
      return store.get(key);
    },
    async put(key: string, value: unknown) {
      store.set(key, value);
    },
    async delete(key: string) {
      return store.delete(key);
    },
    async list(options: { prefix?: string } = {}) {
      const out = new Map<string, unknown>();
      for (const [k, v] of store)
        if (!options.prefix || k.startsWith(options.prefix)) out.set(k, v);
      return out;
    },
  } as unknown as PoolStorage;
}

describe("claude-sub: which subscriptions exist", () => {
  it("reads one label per non-empty CLAUDE_SUB_TOKEN_<LABEL> secret, sorted", () => {
    expect(
      subscriptionLabels({
        CLAUDE_SUB_TOKEN_MAX2: "t2",
        CLAUDE_SUB_TOKEN_MAX1: "t1",
        CLAUDE_SUB_TOKEN_EMPTY: " ",
        CLAUDE_SUB_TOKEN_bad: "lowercase labels are not labels",
        ANTHROPIC_API_KEY: "not a subscription",
      }),
    ).toEqual(["MAX1", "MAX2"]);
  });

  it("uses a token with the line breaks a wrapped paste left inside it removed", () => {
    expect(normalizeToken("sk-ant-oat01-abc\ndef \r\n")).toBe("sk-ant-oat01-abcdef");
    expect(normalizeToken(" \n")).toBeNull();
    expect(normalizeToken(undefined)).toBeNull();
  });

  it("caps per subscription at CLAUDE_SUB_MAX_CONCURRENT, else the default", () => {
    expect(maxConcurrent({ CLAUDE_SUB_MAX_CONCURRENT: "1" })).toBe(1);
    expect(maxConcurrent({ CLAUDE_SUB_MAX_CONCURRENT: "zero" })).toBe(DEFAULT_MAX_CONCURRENT);
    expect(maxConcurrent({})).toBe(DEFAULT_MAX_CONCURRENT);
  });
});

describe("claude-sub: what an answer says about the subscription", () => {
  it("benches a quota rejection until the unified reset (epoch seconds)", () => {
    const headers = new Headers({
      "anthropic-ratelimit-unified-status": "rejected",
      "anthropic-ratelimit-unified-reset": String(NOW / 1000 + 3600),
      "anthropic-ratelimit-unified-representative-claim": "five_hour",
    });
    expect(classifyAnswer(429, headers, NOW)).toEqual({
      until: NOW + 3600_000,
      reason: "quota",
      detail: "HTTP 429, unified rejected, claim five_hour",
    });
  });

  it("reads a 429 the limiter ALLOWED as throttling, though every answer carries a reset", () => {
    // The shape of a real 200 on staging (2026-10-06): status allowed, a
    // reset, and overage rejected because overage is disabled for the org.
    const headers = new Headers({
      "anthropic-ratelimit-unified-status": "allowed",
      "anthropic-ratelimit-unified-reset": String(NOW / 1000 + 3600),
      "anthropic-ratelimit-unified-5h-status": "allowed",
      "anthropic-ratelimit-unified-overage-status": "rejected",
    });
    expect(classifyAnswer(429, headers, NOW)).toMatchObject({ reason: "throttle" });
    headers.set("anthropic-ratelimit-unified-7d-status", "rejected");
    expect(classifyAnswer(429, headers, NOW)).toMatchObject({
      reason: "quota",
      until: NOW + 3600_000,
    });
  });

  it("benches a 429 that is not the quota only briefly", () => {
    expect(classifyAnswer(429, new Headers(), NOW)).toMatchObject({
      reason: "throttle",
      until: NOW + THROTTLE_COOLDOWN_MS,
    });
    expect(classifyAnswer(429, new Headers({ "retry-after": "7" }), NOW)).toMatchObject({
      reason: "throttle",
      until: NOW + 7000,
    });
  });

  it("benches a refused token until it is rotated, and says nothing about success or 5xx", () => {
    expect(classifyAnswer(401, new Headers(), NOW)).toMatchObject({
      reason: "auth",
      until: NOW + AUTH_BENCH_MS,
    });
    expect(classifyAnswer(200, new Headers(), NOW)).toBeNull();
    expect(classifyAnswer(529, new Headers(), NOW)).toBeNull();
  });

  it("reads a reset in seconds, milliseconds or ISO form", () => {
    expect(parseReset("1800000000")).toBe(1_800_000_000_000);
    expect(parseReset("1800000000000")).toBe(1_800_000_000_000);
    expect(parseReset("2027-01-15T08:00:00Z")).toBe(Date.parse("2027-01-15T08:00:00Z"));
    expect(parseReset(null)).toBeNull();
    expect(parseReset("soon")).toBeNull();
  });

  it("keeps only the unified limit headers and retry-after", () => {
    const headers = new Headers({
      "anthropic-ratelimit-unified-5h-utilization": "0.4",
      "retry-after": "3",
      "content-type": "application/json",
    });
    expect(limitHeaders(headers)).toEqual({
      "anthropic-ratelimit-unified-5h-utilization": "0.4",
      "retry-after": "3",
    });
  });
});

describe("claude-sub: the request the proxy sends", () => {
  it("swaps the placeholder for the token, drops an API key, keeps the path, forces HTTPS and the OAuth beta", async () => {
    const original = new Request("http://api.anthropic.com/v1/messages?beta=true", {
      method: "POST",
      headers: {
        authorization: `Bearer ${CLAUDE_SUB_PLACEHOLDER}`,
        "x-api-key": "should-not-travel",
        "anthropic-beta": "claude-code-20250219",
        "anthropic-version": "2023-06-01",
      },
      body: '{"model":"opus"}',
    });
    const up = upstreamRequest(original, "real-token");
    expect(up.url).toBe(`https://${CLAUDE_SUB_HOST}/v1/messages?beta=true`);
    expect(up.method).toBe("POST");
    expect(up.headers.get("authorization")).toBe("Bearer real-token");
    expect(up.headers.get("x-api-key")).toBeNull();
    expect(up.headers.get("anthropic-beta")).toBe(`claude-code-20250219,${OAUTH_BETA}`);
    expect(up.headers.get("anthropic-version")).toBe("2023-06-01");
    expect(await up.text()).toBe('{"model":"opus"}');
  });

  it("does not repeat a beta the CLI already sent", () => {
    const up = upstreamRequest(
      new Request("https://api.anthropic.com/api/hello", {
        method: "HEAD",
        headers: { "anthropic-beta": `${OAUTH_BETA},x` },
      }),
      "t",
    );
    expect(up.headers.get("anthropic-beta")).toBe(`${OAUTH_BETA},x`);
  });

  it("gives the container only the placeholder, never an API key", () => {
    const env = claudeSubProcessEnv();
    expect(env.CLAUDE_CODE_OAUTH_TOKEN).toBe(CLAUDE_SUB_PLACEHOLDER);
    expect(env).not.toHaveProperty("ANTHROPIC_API_KEY");
  });
});

describe("claude-sub: the pool", () => {
  it("is sticky: a job keeps its subscription, even after it is benched", async () => {
    let now = NOW;
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => now);
    const first = await pool.lease("job-a", ["MAX1", "MAX2"], 2);
    expect(first).toEqual({ ok: true, label: "MAX1", reused: false });
    await pool.benchLabel("MAX1", { until: now + 3600_000, reason: "quota", detail: "x" });
    now += 1000;
    expect(await pool.lease("job-a", ["MAX1", "MAX2"], 2)).toEqual({
      ok: true,
      label: "MAX1",
      reused: true,
    });
    // A NEW job steps over the benched subscription.
    expect(await pool.lease("job-b", ["MAX1", "MAX2"], 2)).toMatchObject({ label: "MAX2" });
  });

  it("spreads jobs to the least-leased subscription and refuses past the cap as busy", async () => {
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => NOW);
    expect(await pool.lease("a", ["MAX1", "MAX2"], 1)).toMatchObject({ label: "MAX1" });
    expect(await pool.lease("b", ["MAX1", "MAX2"], 1)).toMatchObject({ label: "MAX2" });
    expect(await pool.lease("c", ["MAX1", "MAX2"], 1)).toEqual({
      ok: false,
      reason: "busy",
      retry_at: null,
    });
    await pool.release("a");
    expect(await pool.lease("c", ["MAX1", "MAX2"], 1)).toMatchObject({ label: "MAX1" });
  });

  it("says exhausted, with the earliest reset, when every subscription is benched", async () => {
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => NOW);
    await pool.benchLabel("MAX1", { until: NOW + 5000, reason: "quota", detail: "" });
    await pool.benchLabel("MAX2", { until: NOW + 9000, reason: "quota", detail: "" });
    expect(await pool.lease("a", ["MAX1", "MAX2"], 2)).toEqual({
      ok: false,
      reason: "exhausted",
      retry_at: NOW + 5000,
    });
    expect(await pool.lease("a", [], 2)).toEqual({ ok: false, reason: "none", retry_at: null });
  });

  it("brings a subscription back once its reset has passed, and never shortens a bench", async () => {
    let now = NOW;
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => now);
    await pool.benchLabel("MAX1", { until: now + 5000, reason: "quota", detail: "long" });
    await pool.benchLabel("MAX1", { until: now + 1000, reason: "throttle", detail: "short" });
    now += 2000;
    expect(await pool.lease("a", ["MAX1"], 1)).toMatchObject({ ok: false, reason: "exhausted" });
    now += 4000;
    expect(await pool.lease("a", ["MAX1"], 1)).toMatchObject({ ok: true, label: "MAX1" });
  });

  it("stops counting a lease nobody released after its TTL", async () => {
    let now = NOW;
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => now);
    await pool.lease("leaked", ["MAX1"], 1);
    expect(await pool.lease("b", ["MAX1"], 1)).toMatchObject({ ok: false, reason: "busy" });
    now += LEASE_TTL_MS;
    expect(await pool.lease("b", ["MAX1"], 1)).toMatchObject({ ok: true, label: "MAX1" });
  });

  it("reports leases, benches and counts, never a token", async () => {
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => NOW);
    await pool.lease("a", ["MAX1"], 2);
    await pool.observe("MAX1", 200, { "anthropic-ratelimit-unified-5h-utilization": "0.1" });
    await pool.observe("MAX1", 429, {});
    expect(await pool.snapshot(["MAX1"])).toEqual([
      {
        label: "MAX1",
        active_leases: ["a"],
        benched: null,
        requests: 2,
        limited: 1,
        last_status: 429,
        last_limits: { "anthropic-ratelimit-unified-5h-utilization": "0.1" },
      },
    ]);
  });
});
