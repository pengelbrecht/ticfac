import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createModels, type Message, Type } from "@earendil-works/pi-ai";
import {
  createRegistry,
  defineExtension,
  defineTool,
  Harness,
  MemoryStorage,
} from "@earendil-works/pi-durable";
import { describe, expect, it } from "vitest";
import {
  GATEWAY_MODEL_OVERRIDES,
  GATEWAY_PROVIDER_ID,
  gatewayModelRef,
  workersAIGatewayProvider,
} from "../src/gateway/workers-ai";

const GATEWAY = "https://factory.example.com/api/gateway";
const RUN_TOKEN = "run-token-0123456789abcdef";
const GLM = "@cf/zai-org/glm-5.3";

/** One request the fake gateway received, as the factory Worker would read it. */
type Seen = {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: Record<string, unknown>;
};

/** One scripted answer: an assistant text, or a tool call. */
type Answer = { text: string } | { tool: string; args: Record<string, unknown> };

/** An OpenAI chat-completions SSE stream for one answer, the shape Workers AI streams. */
function sse(answer: Answer): string {
  const chunk = (delta: Record<string, unknown>, finish: string | null) =>
    `data: ${JSON.stringify({
      id: "chatcmpl-1",
      object: "chat.completion.chunk",
      created: 1,
      model: GLM,
      choices: [{ index: 0, delta, finish_reason: finish }],
    })}\n\n`;
  const usage = `data: ${JSON.stringify({
    id: "chatcmpl-1",
    object: "chat.completion.chunk",
    created: 1,
    model: GLM,
    choices: [],
    usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 },
  })}\n\n`;
  if ("text" in answer) {
    return (
      chunk({ role: "assistant", content: answer.text }, null) +
      chunk({}, "stop") +
      usage
    ).concat("data: [DONE]\n\n");
  }
  return (
    chunk(
      {
        role: "assistant",
        tool_calls: [
          {
            index: 0,
            id: "call_1",
            type: "function",
            function: { name: answer.tool, arguments: JSON.stringify(answer.args) },
          },
        ],
      },
      null,
    ) +
    chunk({}, "tool_calls") +
    usage
  ).concat("data: [DONE]\n\n");
}

/**
 * A recording stand-in for the factory's gateway route (cloudflare/src/gateway.ts):
 * it accepts only `Authorization: Bearer <run token>` for a live token, answers
 * a revoked one with the same 403 body `authorizeRunCredential` writes, and
 * otherwise streams the next scripted answer. It demands the identity the real
 * route demands (learnings: a fake more forgiving than the real thing certifies
 * the defect it hides).
 */
function fakeGateway(answers: Answer[]) {
  const seen: Seen[] = [];
  const revoked = new Set<string>();
  const fetcher = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const request = new Request(input, init);
    const headers: Record<string, string> = {};
    request.headers.forEach((value, name) => {
      headers[name] = value;
    });
    const text = request.method === "GET" ? "" : await request.text();
    seen.push({
      url: request.url,
      method: request.method,
      headers,
      body: text === "" ? {} : (JSON.parse(text) as Record<string, unknown>),
    });
    const presented = (headers.authorization ?? "").replace(/^Bearer\s+/i, "");
    if (presented !== RUN_TOKEN) {
      return Response.json(
        { error: "run_token_unknown", detail: "no such run gateway token" },
        { status: 401 },
      );
    }
    if (revoked.has(presented)) {
      // Verbatim the shape of the real denial, with a timestamp and a reason
      // that, read as free text, match pi-ai's retryable patterns ("500",
      // "timeout"). pi-ai 1.0.2 reports this as `403 "run_token_revoked"`
      // (the OpenAI SDK reads `error`, not `detail`), which is final; a pinned
      // version that starts surfacing the detail would make pi-durable retry a
      // refusal, and the third request in the revocation test is what says so.
      return Response.json(
        {
          error: "run_token_revoked",
          detail:
            "the gateway token for run run_6b9f was revoked at 2026-10-04T12:15:00.500Z " +
            "(wall-clock timeout)",
        },
        { status: 403 },
      );
    }
    const answer = answers.shift();
    if (answer === undefined) {
      return Response.json({ error: "script_exhausted" }, { status: 418 });
    }
    return new Response(sse(answer), {
      status: 200,
      headers: { "content-type": "text/event-stream" },
    });
  };
  return { seen, revoke: (token: string) => revoked.add(token), fetch: fetcher };
}

function textOf(message: Message): string {
  if (typeof message.content === "string") return message.content;
  return message.content.flatMap((block) => (block.type === "text" ? [block.text] : [])).join("");
}

/** A harness over the gateway provider alone: the cloud host's model access. */
async function openHarness(gateway: ReturnType<typeof fakeGateway>, onEcho?: () => void) {
  const echo = defineTool({
    name: "echo",
    description: "Echo text back as the tool result.",
    parameters: Type.Object({ text: Type.String() }),
    replay: "safe",
    async execute(args) {
      onEcho?.();
      return { content: [{ type: "text", text: args.text }] };
    },
  });
  const models = createModels();
  models.setProvider(
    workersAIGatewayProvider({ gateway: GATEWAY, token: RUN_TOKEN, fetch: gateway.fetch }),
  );
  const registry = createRegistry();
  registry.install(defineExtension({ name: "echo", tools: [echo] }));
  const context = BACKGROUND_CONTEXT;
  const harness = await Harness.open(
    new MemoryStorage(),
    // Retries on, with a short delay: a refusal that WERE retryable would show
    // up as a third request inside the test's time.
    { models, registry, settings: { retry: { enabled: true, maxRetries: 2, baseDelayMs: 1 } } },
    context,
  );
  const root = await harness.root(context, {
    agent: { model: gatewayModelRef(`cloudflare-workers-ai/${GLM}`), thinkingLevel: "high" },
  });
  return { harness, root, context };
}

describe("the Workers AI gateway provider", () => {
  // Tick hv3: the only test in this file that opens the full Harness, and
  // the one that failed once in a full `pnpm test` on the loaded host,
  // passing on re-run. Its wall clock grows with pool contention — the
  // workerd pool runs its files beside each other, and synthetic load 200
  // measured it at 15.6s against ~1s quiet — so it may not borrow the
  // quiet-host 30s default: it states its own 120s bound (the kjs rule
  // the node suite already enforces for its full-Harness tests).
  it("sends every request to the run's gateway route with the run token, and nothing that claims attribution", {
    timeout: 120_000,
  }, async () => {
    const gateway = fakeGateway([{ tool: "echo", args: { text: "heard" } }, { text: "done" }]);
    const { harness, root, context } = await openHarness(gateway);

    const submission = await root.submit({ type: "input", content: "echo heard" }, context);
    const settled = await submission.wait(context);
    expect(settled.status).toBe("done");
    expect(gateway.seen.length).toBe(2);

    for (const request of gateway.seen) {
      // The factory's workers-ai route, OpenAI-compatible under /v1: never the
      // Cloudflare API, never the AI Gateway directly.
      expect(request.method).toBe("POST");
      expect(request.url).toBe(`${GATEWAY}/workers-ai/v1/chat/completions`);
      // The run token is the only credential, presented as a bearer the way
      // extractRunToken reads it.
      expect(request.headers.authorization).toBe(`Bearer ${RUN_TOKEN}`);
      // Attribution is stamped by the gateway from the token's row (D17): the
      // harness sends no cf-aig-* header and no account or gateway id.
      expect(Object.keys(request.headers).filter((name) => name.startsWith("cf-"))).toEqual([]);
      expect(request.headers["x-api-key"]).toBeUndefined();
      expect(request.body.model).toBe(GLM);
      expect(request.body.stream).toBe(true);
    }

    const view = await root.context(context);
    const last = view.messages.at(-1);
    expect(last?.role).toBe("assistant");
    expect(textOf(last as Message)).toBe("done");
    await harness.close(context);
  });

  // Tick fim: the two siblings above are full-Harness tests too — the open
  // sits inside the openHarness helper, which is why the old guard (in-body
  // opens only) never saw them. Same rule, same bound as the first test.
  it("carries the GLM maxTokens and thinkingFormat overrides on the wire", {
    timeout: 120_000,
  }, async () => {
    const gateway = fakeGateway([{ text: "ok" }]);
    const { harness, root, context } = await openHarness(gateway);
    const settled = await (await root.submit({ type: "input", content: "hi" }, context)).wait(
      context,
    );
    expect(settled.status).toBe("done");

    const body = gateway.seen[0]?.body ?? {};
    // 65536, not the catalog's 1048576: a near-1M max-output drew a bodyless
    // 400 from Workers AI (learnings), and 8192 truncated GLM.
    expect(body.max_completion_tokens).toBe(65536);
    expect(body.max_tokens).toBeUndefined();
    // thinkingFormat "deepseek": thinking enabled as an object plus the mapped
    // effort, the shape GLM's second turn needs (image/common.sh, tick ha9).
    expect(body.thinking).toEqual({ type: "enabled" });
    expect(body.reasoning_effort).toBe("high");
    await harness.close(context);
  });

  it("stops the conversation at the next request once the run token is revoked, without retrying the refusal", {
    timeout: 120_000,
  }, async () => {
    const gateway = fakeGateway([{ tool: "echo", args: { text: "heard" } }, { text: "never" }]);
    // The kill switch fires while the tool runs: the request that follows the
    // tool round is the first one the revocation can stop.
    const { harness, root, context } = await openHarness(gateway, () => gateway.revoke(RUN_TOKEN));

    const submission = await root.submit({ type: "input", content: "echo heard" }, context);
    const settled = await submission.wait(context);
    // The input settles unanswered with a model error naming the refusal: the
    // conversation is over, not waiting on a retry.
    expect(settled.status).toBe("unanswered");
    expect(settled.reason).toBe("model_error");
    expect(settled.detail ?? "").toContain("run_token_revoked");

    // Exactly two requests: the one before the revocation and the refused one.
    // A third would be pi-durable retrying a refusal it read as transient.
    expect(gateway.seen.length).toBe(2);
    expect(gateway.seen[1]?.headers.authorization).toBe(`Bearer ${RUN_TOKEN}`);
    await harness.close(context);
  });

  it("resolves the token at every request, so a rotated token is what the next request presents", async () => {
    const gateway = fakeGateway([{ text: "one" }, { text: "two" }]);
    let current = "stale-token";
    const models = createModels();
    models.setProvider(
      workersAIGatewayProvider({ gateway: GATEWAY, token: () => current, fetch: gateway.fetch }),
    );
    const model = models.getModel(GATEWAY_PROVIDER_ID, GLM);
    expect(model).toBeDefined();
    if (model === undefined) return;

    const first = await models.completeSimple(model, {
      messages: [{ role: "user", content: "hi", timestamp: 0 }],
    });
    expect(first.stopReason).toBe("error");
    expect(first.errorMessage ?? "").toContain("run_token_unknown");

    current = RUN_TOKEN;
    const second = await models.completeSimple(model, {
      messages: [{ role: "user", content: "hi", timestamp: 0 }],
    });
    expect(second.stopReason).toBe("stop");
    expect(gateway.seen.map((request) => request.headers.authorization)).toEqual([
      "Bearer stale-token",
      `Bearer ${RUN_TOKEN}`,
    ]);
  });

  it("offers only Workers AI models, every one addressed at the gateway", () => {
    const provider = workersAIGatewayProvider({ gateway: `${GATEWAY}/`, token: RUN_TOKEN });
    const models = provider.getModels();
    expect(models.length).toBeGreaterThan(0);
    for (const model of models) {
      expect(model.provider).toBe(GATEWAY_PROVIDER_ID);
      expect(model.api).toBe("openai-completions");
      expect(model.id.startsWith("@cf/")).toBe(true);
      expect(model.baseUrl).toBe(`${GATEWAY}/workers-ai/v1`);
    }
    // Every override names a model the catalog has: an override for a model
    // that left the catalog is a silent no-op, and that must fail here.
    for (const id of Object.keys(GATEWAY_MODEL_OVERRIDES)) {
      expect(models.some((model) => model.id === id)).toBe(true);
    }
    const glm = models.find((model) => model.id === GLM);
    expect(glm?.maxTokens).toBe(65536);
    expect(glm?.compat).toMatchObject({ thinkingFormat: "deepseek" });
    // The rest of the catalog entry is kept (context window, reasoning).
    expect(glm?.contextWindow).toBe(1048576);
    expect(glm?.reasoning).toBe(true);
  });

  it("refuses a gateway that is not the factory's: a vendor, the Cloudflare API, or not a URL", () => {
    for (const gateway of [
      "https://api.cloudflare.com/client/v4/accounts/abc/ai",
      "https://gateway.ai.cloudflare.com/v1/abc/gw",
      "https://api.anthropic.com",
      "https://api.openai.com/v1",
      "https://openrouter.ai/api",
      "factory.example.com/api/gateway",
      "",
    ]) {
      expect(() => workersAIGatewayProvider({ gateway, token: RUN_TOKEN }), gateway).toThrow();
    }
    expect(() => workersAIGatewayProvider({ gateway: GATEWAY, token: "" })).toThrow(/token/);
  });

  // Epic 43y's note (6): "--thinking medium recorded thinkingLevel high" on the
  // pi CLI. This is why, pinned: pi-ai's catalog marks GLM 5.3's `medium` (and
  // `minimal`, `off`, `xhigh`) unsupported, so pi-ai clamps medium UP to high
  // before the request is built. Workers AI itself accepts reasoning_effort
  // low/medium/high/max (staging, 2026-10-04: harness/proof/README.md).
  it("sends GLM 5.3 the effort pi-ai's catalog maps each thinking level to", async () => {
    const levels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
    const gateway = fakeGateway(levels.map((level) => ({ text: level })));
    const models = createModels();
    models.setProvider(
      workersAIGatewayProvider({ gateway: GATEWAY, token: RUN_TOKEN, fetch: gateway.fetch }),
    );
    const model = models.getModel(GATEWAY_PROVIDER_ID, GLM);
    if (model === undefined) throw new Error(`${GLM} is not in the provider's catalog`);
    const sent: Record<string, unknown> = {};
    for (const level of levels) {
      await models.completeSimple(
        model,
        { messages: [{ role: "user", content: "hi", timestamp: 0 }] },
        level === "off" ? {} : { reasoning: level },
      );
      const body = gateway.seen.at(-1)?.body ?? {};
      sent[level] = { thinking: body.thinking, reasoning_effort: body.reasoning_effort };
    }
    expect(sent).toEqual({
      // Off sends no thinking field at all: the catalog marks off unsupported,
      // so GLM 5.3 thinks at its own default.
      off: { thinking: undefined, reasoning_effort: undefined },
      minimal: { thinking: { type: "enabled" }, reasoning_effort: "low" },
      low: { thinking: { type: "enabled" }, reasoning_effort: "low" },
      medium: { thinking: { type: "enabled" }, reasoning_effort: "high" },
      high: { thinking: { type: "enabled" }, reasoning_effort: "high" },
      xhigh: { thinking: { type: "enabled" }, reasoning_effort: "max" },
      max: { thinking: { type: "enabled" }, reasoning_effort: "max" },
    });
  });
});

describe("gatewayModelRef", () => {
  it("reads every spelling the routing config writes for a Workers AI model", () => {
    const want = { provider: GATEWAY_PROVIDER_ID, modelId: GLM };
    expect(gatewayModelRef(`cloudflare-workers-ai/${GLM}`)).toEqual(want);
    expect(gatewayModelRef(`workers-ai/${GLM}`)).toEqual(want);
    // The @cf namespace is a constant, restored when omitted (select_model_route).
    expect(gatewayModelRef("workers-ai/zai-org/glm-5.3")).toEqual(want);
  });

  it("never routes a cloud worker to claude or any cash-billed vendor", () => {
    for (const routed of [
      "anthropic/claude-opus-5-5",
      "claude-opus-5-5",
      "opus",
      "sonnet",
      "openai/gpt-5",
      "gpt-5",
      "openrouter/anthropic/claude-opus-5-5",
      "@cf/zai-org/glm-5.3",
      "",
    ]) {
      expect(() => gatewayModelRef(routed), routed).toThrow(/Workers AI/);
    }
  });

  it("refuses a Workers AI model the provider does not carry, rather than a run with no model", () => {
    expect(() => gatewayModelRef("workers-ai/@cf/example/not-a-model")).toThrow(/not-a-model/);
  });
});
