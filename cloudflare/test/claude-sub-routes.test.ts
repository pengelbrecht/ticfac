import { env, SELF } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import {
  type ClaudeSubEnv,
  claudeSubPool,
  claudeSubRoute,
  TOKEN_SECRET_PREFIX,
} from "../src/claude-sub";

// The claude-sub pool's OPERATOR surface in production (tick 6fv): which
// subscriptions this factory has a token for — their LABELS, their leases and
// their benches, never a token value — and the one operator action, unbenching
// a subscription whose token was rotated. Authenticated by the operator's
// factory token like every other /api route, because the caller is a person
// reading what their subscriptions are doing, never a run.

describe("the claude-sub routes", () => {
  const url = "https://factory.example.com/api/claude-sub";
  let token: string;

  beforeEach(async () => {
    token = mintFactoryToken();
    env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
    env[`${TOKEN_SECRET_PREFIX}MAX1`] = "sk-ant-oat01-not-a-real-token-1";
    env[`${TOKEN_SECRET_PREFIX}MAX2`] = "sk-ant-oat02-not-a-real-token-2";
  });

  afterEach(() => {
    delete env.FACTORY_TOKEN_HASH;
    delete env[`${TOKEN_SECRET_PREFIX}MAX1`];
    delete env[`${TOKEN_SECRET_PREFIX}MAX2`];
  });

  const get = (path = "", init: RequestInit = {}) =>
    SELF.fetch(`https://factory.example.com/api/claude-sub${path}`, {
      ...init,
      headers: { Authorization: `Bearer ${token}`, ...init.headers },
    });

  it("refuses an unauthenticated caller with 401, never 404", async () => {
    const res = await SELF.fetch(url);
    expect(res.status).toBe(401);
  });

  it("reports the configured subscriptions' labels, leases and benches, never a token", async () => {
    const res = await get();
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      labels: string[];
      subscriptions: { label: string; active_leases: string[] }[];
    };
    expect(body.labels).toEqual(["MAX1", "MAX2"]);
    expect(body.subscriptions.map((s) => s.label).sort()).toEqual(["MAX1", "MAX2"]);
    // Never the value: the whole answer is safe to paste into a log.
    const raw = JSON.stringify(body);
    expect(raw).not.toContain("sk-ant");
    expect(raw).not.toContain("oat01");
  });

  it("unbenches one label, leaving the others benched", async () => {
    const pool = claudeSubPool(env as unknown as ClaudeSubEnv)!;
    const now = Date.now();
    await pool.bench("MAX1", { until: now + 3600_000, reason: "auth", detail: "HTTP 401" });
    await pool.bench("MAX2", { until: now + 3600_000, reason: "quota", detail: "HTTP 429" });

    const res = await get("/unbench/MAX1", { method: "POST" });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { labels: string[] };
    expect(body.labels).toEqual(["MAX1", "MAX2"]);

    const snapshot = await pool.snapshot();
    expect(snapshot.find((s) => s.label === "MAX1")?.benched).toBeNull();
    expect(snapshot.find((s) => s.label === "MAX2")?.benched).not.toBeNull();
  });

  it("refuses an unbench of a label no token exists for", async () => {
    const res = await get("/unbench/NOPE", { method: "POST" });
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error: string };
    expect(body.error).toBe("unknown_subscription");
  });

  it("answers 503, naming the binding, when the deployment wires no pool", async () => {
    const unbound: ClaudeSubEnv = {};
    const res = await claudeSubRoute(
      new Request("https://factory.example.com/api/claude-sub"),
      unbound,
    );
    expect(res.status).toBe(503);
    const body = (await res.json()) as { error: string; detail: string };
    expect(body.error).toBe("no_claude_sub_pool");
    expect(body.detail).toContain("CLAUDE_SUB_POOL");
  });
});
