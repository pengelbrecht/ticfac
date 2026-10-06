import {
  createProvider,
  type Model,
  type OpenAICompletionsCompat,
  type Provider,
  type ProviderStreams,
} from "@earendil-works/pi-ai";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { CLOUDFLARE_WORKERS_AI_MODELS } from "@earendil-works/pi-ai/providers/cloudflare-workers-ai.models";
import type { ModelRef } from "@earendil-works/pi-durable";

/**
 * The harness's model access in the cloud (epic 43y, step 2 of
 * docs/spikes/n0b-round2-pi-durable.md): Workers AI through the factory's
 * gateway route, on the run's gateway token, and nothing else.
 *
 * This is the pi-durable counterpart of what `image/common.sh` does for the pi
 * CLI (`configure_pi_provider`), and it keeps the same three choices:
 *
 * - **It overrides pi's built-in `cloudflare-workers-ai` provider** rather than
 *   defining a new one. The routing config spells models
 *   `cloudflare-workers-ai/@cf/…`, pi-ai detects Workers AI's request dialect
 *   from that provider id, and the catalog entries (context window, reasoning,
 *   thinking levels) stay pi's own. Only the address and the credential are the
 *   run's.
 * - **The address is the factory's gateway**, `<gateway>/workers-ai/v1`, never
 *   the Cloudflare API: the gateway exchanges the run token for the operator's
 *   account token, stamps the run's attribution (`cf-aig-metadata`, from the
 *   token's own row) and stops answering when the token is revoked (D17). The
 *   harness holds the run token; it never holds an account id or a vendor key.
 * - **GLM 5.3's catalog entry is corrected** (`GATEWAY_MODEL_OVERRIDES`), the
 *   same table `pi_model_overrides` writes into pi's models.json.
 *
 * There is deliberately no other provider here: a cloud worker runs on Workers
 * AI only, never claude, never a cash-billed vendor (`gatewayModelRef` refuses
 * them by name).
 */

/** The provider id the harness registers: pi's own name for Workers AI, overridden. */
export const GATEWAY_PROVIDER_ID = "cloudflare-workers-ai";

/** The gateway route Workers AI is served on, OpenAI-compatible under /v1. */
export const GATEWAY_WORKERS_AI_ROUTE = "workers-ai/v1";

/**
 * Per-model corrections to pi-ai's catalog, merged onto the catalog entry
 * field by field (compat merged key by key), so everything else about the model
 * is kept.
 *
 * GLM 5.3 (and Flash): pi-ai 1.0.2's catalog gives it maxTokens 1048576. pi-ai
 * asks for nearly all of that as max_completion_tokens, and a near-1M
 * max-output drew a bodyless 400 from Workers AI (.tick/learnings.md); 8192
 * truncated GLM. 65536 and thinkingFormat "deepseek" are the values the pi CLI
 * path runs GLM 5.3 with (`pi_model_overrides` in image/common.sh, tick ha9):
 * the thinking format is what GLM's reasoning needs on the second turn of a
 * conversation, and off-vendor it is what keeps `<think>` tags out of the text.
 *
 * A table, not a blanket cap: lowering maxTokens is safe, raising it for a
 * model whose real limit is smaller would create the 400 for it.
 */
export const GATEWAY_MODEL_OVERRIDES: Readonly<
  Record<string, { maxTokens?: number; compat?: OpenAICompletionsCompat }>
> = {
  "@cf/zai-org/glm-5.3": { maxTokens: 65536, compat: { thinkingFormat: "deepseek" } },
  "@cf/zai-org/glm-5.3-flash": { maxTokens: 65536, compat: { thinkingFormat: "deepseek" } },
};

/** The run's gateway token, or a function that answers it at every request. */
export type RunToken = string | (() => string | Promise<string>);

export type WorkersAIGatewayProviderOptions = {
  /**
   * The run's gateway endpoint, `<factory>/api/gateway` — what a worker
   * container is handed as AI_GATEWAY_BASE_URL.
   */
  gateway: string;
  /**
   * The run's gateway token. A function is asked at EVERY request, so a host
   * whose token is rotated (a budget trip mints the close-out a fresh one)
   * presents the current one on its next request without a new provider.
   */
  token: RunToken;
  /**
   * The fetch the requests go through: a service binding to the factory
   * Worker in the cloud host, a recording stand-in in tests. Defaults to the
   * global fetch.
   */
  fetch?: typeof fetch;
};

/**
 * Hosts that are not the factory's gateway. A run token sent to one of these
 * is a token sent past the exchange, attribution and kill switch the gateway
 * is for: the Cloudflare API (pi's own Workers AI default — the xte finding
 * that a pi worker would send the run token straight there), the AI Gateway
 * itself, and the vendors `image/common.sh` refuses.
 */
const NOT_A_GATEWAY = [
  "api.cloudflare.com",
  "gateway.ai.cloudflare.com",
  "api.anthropic.com",
  "api.openai.com",
  "openrouter.ai",
  "generativelanguage.googleapis.com",
];

/** The gateway base URL without a trailing slash, or a throw naming what is wrong. */
function gatewayBase(raw: string): string {
  const base = raw.trim().replace(/\/+$/, "");
  let parsed: URL;
  try {
    parsed = new URL(base);
  } catch {
    throw new Error(`the run's gateway is not a base URL: "${raw}"`);
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    throw new Error(`the run's gateway is not an http(s) base URL: "${raw}"`);
  }
  if (NOT_A_GATEWAY.includes(parsed.hostname)) {
    throw new Error(
      `the run's gateway points at ${parsed.hostname}, which is not the factory's gateway: ` +
        "the run token must go to <factory>/api/gateway, where it is exchanged, attributed " +
        "and revocable",
    );
  }
  return base;
}

/** The catalog's Workers AI chat models, addressed at the gateway, with the overrides applied. */
export function gatewayModels(gateway: string): Model<"openai-completions">[] {
  const baseUrl = `${gatewayBase(gateway)}/${GATEWAY_WORKERS_AI_ROUTE}`;
  const catalog = Object.values(CLOUDFLARE_WORKERS_AI_MODELS) as Model<"openai-completions">[];
  return catalog.map((model) => {
    const override = GATEWAY_MODEL_OVERRIDES[model.id];
    return {
      ...model,
      provider: GATEWAY_PROVIDER_ID,
      baseUrl,
      ...(override?.maxTokens === undefined ? {} : { maxTokens: override.maxTokens }),
      ...(override?.compat === undefined
        ? {}
        : { compat: { ...model.compat, ...override.compat } }),
    };
  });
}

/** Every request of `streams` goes through `fetcher`. */
function throughFetch(streams: ProviderStreams, fetcher: typeof fetch): ProviderStreams {
  return {
    stream: (model, context, options) =>
      streams.stream(model, context, { ...options, fetch: fetcher }),
    streamSimple: (model, context, options) =>
      streams.streamSimple(model, context, { ...options, fetch: fetcher }),
  };
}

/**
 * The provider a cloud harness registers on its `Models`, and the only one:
 * Workers AI through the run's gateway route, on the run's token.
 */
export function workersAIGatewayProvider(
  options: WorkersAIGatewayProviderOptions,
): Provider<"openai-completions"> {
  const base = gatewayBase(options.gateway);
  const { token } = options;
  if (typeof token === "string" && token.trim() === "") {
    throw new Error("no run gateway token: the gateway answers every request without one with 401");
  }
  const fetcher = options.fetch ?? ((input, init) => fetch(input, init));
  return createProvider({
    id: GATEWAY_PROVIDER_ID,
    name: "Workers AI via the factory gateway",
    baseUrl: `${base}/${GATEWAY_WORKERS_AI_ROUTE}`,
    auth: {
      apiKey: {
        name: "Run gateway token",
        // Asked at every request (pi-ai resolves auth per request), so a
        // rotated token is what the next request presents. An empty answer is
        // "not configured", which pi-ai reports as a stream error naming the
        // provider rather than sending a request with no credential.
        resolve: async () => {
          const current = typeof token === "string" ? token : await token();
          return current.trim() === ""
            ? undefined
            : { auth: { apiKey: current }, source: "run gateway token" };
        },
      },
    },
    models: gatewayModels(base),
    api: throughFetch(openAICompletionsApi(), fetcher),
  });
}

/**
 * The harness `ModelRef` for a model the dispatch routed, in any spelling the
 * routing config writes for Workers AI: `cloudflare-workers-ai/@cf/…` (pi's),
 * `workers-ai/@cf/…` (the gateway's), or either with the constant `@cf/`
 * namespace left out (restored, as `select_model_route` does).
 *
 * Anything else is refused by name — claude, anthropic, openai, openrouter, a
 * bare id: a cloud worker runs on Workers AI through the gateway, and a model
 * this provider cannot serve is a stop here rather than a conversation that
 * settles "no_model" after it started. So is a Workers AI id the catalog does
 * not carry.
 */
export function gatewayModelRef(routed: string): ModelRef {
  const match = /^(?:cloudflare-workers-ai|workers-ai)\/(.+)$/.exec(routed.trim());
  if (match === null) {
    throw new Error(
      `"${routed}" is not a Workers AI model: a cloud worker runs on Workers AI through the ` +
        "factory gateway only — route it as workers-ai/@cf/… or cloudflare-workers-ai/@cf/…",
    );
  }
  const rest = match[1] as string;
  const modelId = rest.startsWith("@") ? rest : `@cf/${rest}`;
  const known = Object.keys(CLOUDFLARE_WORKERS_AI_MODELS);
  if (!known.includes(modelId)) {
    throw new Error(
      `${modelId} is not a Workers AI model this harness's catalog carries (pi-ai's ` +
        `cloudflare-workers-ai catalog: ${known.join(", ")})`,
    );
  }
  return { provider: GATEWAY_PROVIDER_ID, modelId };
}
