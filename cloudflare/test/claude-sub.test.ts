import { describe, expect, it } from "vitest";
import {
  AUTH_BENCH_MS,
  allowedRoute,
  type Bench,
  CLAUDE_SUB_HOST,
  CLAUDE_SUB_PLACEHOLDER,
  ClaudeSubPoolCore,
  classifyAnswer,
  claudeSubProcessEnv,
  DEFAULT_MAX_CONCURRENT,
  JOB_QUOTA_TTL_MS,
  LEASE_REFRESH_MS,
  LEASE_TTL_MS,
  limitHeaders,
  maxConcurrent,
  normalizeToken,
  OAUTH_BETA,
  OVERAGE_BENCH_FALLBACK_MS,
  type PoolStorage,
  type ProxyDeps,
  type ProxyPool,
  parseReset,
  proxyClaudeSub,
  refusedBeta,
  subscriptionLabels,
  subscriptionModel,
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

  it("reports leases, benches and counts — per route, with the concrete model — never a token", async () => {
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => NOW);
    await pool.lease("a", ["MAX1"], 2);
    await pool.observe("MAX1", {
      status: 200,
      limits: { "anthropic-ratelimit-unified-5h-utilization": "0.1" },
      route: "POST /v1/messages",
      model: "claude-sonnet-5",
    });
    await pool.observe("MAX1", { status: 404, limits: {}, route: "GET /api/claude_code/settings" });
    await pool.observe("MAX1", {
      status: 429,
      limits: {},
      route: "POST /v1/messages",
      model: "claude-sonnet-5",
    });
    expect(await pool.snapshot(["MAX1"])).toEqual([
      {
        label: "MAX1",
        active_leases: ["a"],
        benched: null,
        requests: 3,
        limited: 1,
        last_status: 429,
        last_limits: { "anthropic-ratelimit-unified-5h-utilization": "0.1" },
        routes: {
          "POST /v1/messages": { requests: 2, last_status: 429 },
          "GET /api/claude_code/settings": { requests: 1, last_status: 404 },
        },
        models: { "claude-sonnet-5": 2 },
        last_model: "claude-sonnet-5",
      },
    ]);
  });

  it("keeps a live job's lease from lapsing: a touch refreshes it, at most once a minute", async () => {
    let now = NOW;
    const storage = memoryStorage();
    const pool = new ClaudeSubPoolCore(storage, () => now);
    await pool.lease("long-job", ["MAX1"], 1);
    // The job keeps talking for longer than the TTL, touching as it goes.
    for (let t = 0; t < LEASE_TTL_MS * 2; t += 30 * 60 * 1000) {
      now = NOW + t;
      await pool.touch("long-job");
    }
    now = NOW + LEASE_TTL_MS * 2;
    expect(await pool.lease("other", ["MAX1"], 1)).toMatchObject({ ok: false, reason: "busy" });
    // Within the interval a touch writes nothing.
    await pool.touch("long-job");
    const before = await storage.get("lease:long-job");
    expect(before).toEqual({ label: "MAX1", at: now });
    now += LEASE_REFRESH_MS - 1;
    await pool.touch("long-job");
    expect(await storage.get("lease:long-job")).toEqual(before);
  });

  it("never resurrects a released lease on a straggling touch", async () => {
    let now = NOW;
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => now);
    await pool.lease("done", ["MAX1"], 1);
    await pool.release("done");
    now += LEASE_REFRESH_MS * 2;
    await pool.touch("done");
    expect(await pool.lease("next", ["MAX1"], 1)).toMatchObject({ ok: true, label: "MAX1" });
  });

  it("remembers a job's quota answer for its collect, the first one standing, then forgets it", async () => {
    let now = NOW;
    const pool = new ClaudeSubPoolCore(memoryStorage(), () => now);
    const first: Bench = { until: NOW + 3600_000, reason: "quota", detail: "first" };
    await pool.recordJobQuota("job-a", "MAX1", first);
    await pool.recordJobQuota("job-a", "MAX1", { ...first, detail: "second" });
    expect(await pool.jobQuota("job-a")).toEqual({ label: "MAX1", bench: first, at: NOW });
    expect(await pool.jobQuota("job-b")).toBeNull();
    now += JOB_QUOTA_TTL_MS;
    expect(await pool.jobQuota("job-a")).toBeNull();
  });
});

describe("claude-sub: what the proxy forwards on the token", () => {
  it("allows the inference calls and the pinned CLI's startup reads, and nothing else", () => {
    expect(allowedRoute("POST", "/v1/messages")).toBe("inference");
    expect(allowedRoute("post", "/v1/messages/count_tokens")).toBe("inference");
    expect(allowedRoute("GET", "/api/claude_code/policy_limits")).toBe("startup");
    expect(allowedRoute("GET", "/api/claude_code/settings")).toBe("startup");
    expect(allowedRoute("GET", "/v1/messages")).toBeNull();
    expect(allowedRoute("POST", "/v1/messages/batches")).toBeNull();
    expect(allowedRoute("POST", "/api/oauth/claude_cli/create_api_key")).toBeNull();
    expect(allowedRoute("GET", "/v1/files")).toBeNull();
  });

  it("refuses a beta that bills per token", () => {
    expect(refusedBeta("claude-code-20250219, context-1m-2025-08-07")).toBe(
      "context-1m-2025-08-07",
    );
    expect(refusedBeta(`claude-code-20250219,${OAUTH_BETA}`)).toBeNull();
    expect(refusedBeta(null)).toBeNull();
  });

  it("admits only a concrete claude model", () => {
    expect(subscriptionModel("claude-sonnet-5")).toBe(true);
    expect(subscriptionModel("claude-opus-4-1-20250805")).toBe(true);
    expect(subscriptionModel("claude-sonnet-5[1m]")).toBe(false);
    expect(subscriptionModel("gpt-5")).toBe(false);
    expect(subscriptionModel(undefined)).toBe(false);
  });

  it("reads an answer served on usage credits as the quota, even a 200", () => {
    const headers = new Headers({
      "anthropic-ratelimit-unified-status": "allowed",
      "anthropic-ratelimit-unified-reset": String(NOW / 1000 + 7200),
      "anthropic-ratelimit-unified-overage-in-use": "true",
    });
    expect(classifyAnswer(200, headers, NOW)).toMatchObject({
      reason: "quota",
      until: NOW + 7200_000,
    });
    headers.delete("anthropic-ratelimit-unified-reset");
    expect(classifyAnswer(200, headers, NOW)).toMatchObject({
      reason: "quota",
      until: NOW + OVERAGE_BENCH_FALLBACK_MS,
    });
    headers.set("anthropic-ratelimit-unified-overage-in-use", "false");
    expect(classifyAnswer(200, headers, NOW)).toBeNull();
  });
});

describe("claude-sub: the proxy", () => {
  type Call = { method: string; args: unknown[] };

  function harness(answer: (request: Request) => Response | Promise<Response>) {
    const calls: Call[] = [];
    const sent: Request[] = [];
    const logs: string[] = [];
    const work: Promise<unknown>[] = [];
    let now = NOW;
    const pool: ProxyPool = {
      async bench(...args) {
        calls.push({ method: "bench", args });
      },
      async observe(...args) {
        calls.push({ method: "observe", args });
      },
      async touch(...args) {
        calls.push({ method: "touch", args });
      },
      async recordJobQuota(...args) {
        calls.push({ method: "recordJobQuota", args });
      },
    };
    const deps: ProxyDeps = {
      token: "real-token",
      upstream: async (r) => {
        sent.push(r);
        return answer(r);
      },
      pool,
      now: () => now,
      waitUntil: (p) => work.push(p),
      log: (line) => logs.push(line),
      touched: new Map(),
    };
    return {
      calls,
      sent,
      logs,
      deps,
      advance: (ms: number) => {
        now += ms;
      },
      async run(request: Request) {
        const response = await proxyClaudeSub(request, { label: "MAX1", jobId: "job-1" }, deps);
        await Promise.all(work.splice(0));
        return response;
      },
    };
  }

  const messages = (body: unknown = { model: "claude-sonnet-5" }, headers: HeadersInit = {}) =>
    new Request("http://api.anthropic.com/v1/messages?beta=true", {
      method: "POST",
      headers: { authorization: `Bearer ${CLAUDE_SUB_PLACEHOLDER}`, ...headers },
      body: JSON.stringify(body),
    });

  it("refuses a route off the allowlist with 403, logs its path, and never attaches the token", async () => {
    const h = harness(() => new Response("{}"));
    const response = await h.run(
      new Request("https://api.anthropic.com/api/oauth/claude_cli/create_api_key", {
        method: "POST",
        body: "{}",
      }),
    );
    expect(response.status).toBe(403);
    expect(h.sent).toHaveLength(0);
    expect(h.logs.join("\n")).toContain("POST /api/oauth/claude_cli/create_api_key");
    expect(h.logs.join("\n")).not.toContain("real-token");
  });

  it("refuses a 1M-context beta and a model the subscription does not bill", async () => {
    const h = harness(() => new Response("{}"));
    expect(
      (await h.run(messages(undefined, { "anthropic-beta": "context-1m-2025-08-07" }))).status,
    ).toBe(403);
    expect((await h.run(messages({ model: "gpt-5" }))).status).toBe(403);
    expect((await h.run(messages({ messages: [] }))).status).toBe(403);
    expect(h.sent).toHaveLength(0);
  });

  it("forwards an inference call with the token and counts it per route and model", async () => {
    const h = harness(() => new Response("{}", { status: 200 }));
    const response = await h.run(messages());
    expect(response.status).toBe(200);
    expect(h.sent[0]?.headers.get("authorization")).toBe("Bearer real-token");
    expect(await h.sent[0]?.text()).toBe('{"model":"claude-sonnet-5"}');
    expect(h.calls.find((c) => c.method === "observe")?.args).toEqual([
      "MAX1",
      { status: 200, limits: {}, route: "POST /v1/messages", model: "claude-sonnet-5" },
    ]);
  });

  it("never benches a subscription on a startup read's 401, 403 or 429", async () => {
    for (const status of [401, 403, 429]) {
      const h = harness(() => new Response("{}", { status }));
      const response = await h.run(
        new Request("https://api.anthropic.com/api/claude_code/settings", { method: "GET" }),
      );
      expect(response.status).toBe(status);
      expect(h.calls.map((c) => c.method)).not.toContain("bench");
    }
    const h = harness(() => new Response("{}", { status: 401 }));
    await h.run(messages());
    expect(h.calls.find((c) => c.method === "bench")?.args[1]).toMatchObject({ reason: "auth" });
  });

  it("remembers a mid-job quota answer against the job, for its collect", async () => {
    const h = harness(
      () =>
        new Response("{}", {
          status: 429,
          headers: {
            "anthropic-ratelimit-unified-status": "rejected",
            "anthropic-ratelimit-unified-reset": String(NOW / 1000 + 3600),
          },
        }),
    );
    const response = await h.run(messages());
    expect(response.status).toBe(429);
    expect(h.calls.find((c) => c.method === "recordJobQuota")?.args).toEqual([
      "job-1",
      "MAX1",
      expect.objectContaining({ reason: "quota", until: NOW + 3600_000 }),
    ]);
  });

  it("withholds an answer served on usage credits: the job gets a 429 and the subscription is benched", async () => {
    const h = harness(
      () =>
        new Response('{"content":"billed per token"}', {
          status: 200,
          headers: {
            "anthropic-ratelimit-unified-status": "allowed",
            "anthropic-ratelimit-unified-overage-in-use": "true",
            "anthropic-ratelimit-unified-reset": String(NOW / 1000 + 600),
          },
        }),
    );
    const response = await h.run(messages());
    expect(response.status).toBe(429);
    expect(response.headers.get("retry-after")).toBe("600");
    expect(response.headers.get("anthropic-ratelimit-unified-status")).toBe("rejected");
    expect(await response.text()).not.toContain("billed per token");
    expect(h.calls.find((c) => c.method === "bench")?.args[1]).toMatchObject({
      reason: "quota",
      until: NOW + 600_000,
    });
    expect(h.calls.map((c) => c.method)).toContain("recordJobQuota");
  });

  it("refreshes the job's lease as its traffic passes, at most once a minute", async () => {
    const h = harness(() => new Response("{}"));
    await h.run(messages());
    await h.run(messages());
    h.advance(LEASE_REFRESH_MS);
    await h.run(messages());
    expect(h.calls.filter((c) => c.method === "touch").map((c) => c.args)).toEqual([
      ["job-1"],
      ["job-1"],
    ]);
  });
});
