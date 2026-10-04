/**
 * Test-only: a faux model under the gateway provider's id, for a suite that
 * drives a WorkerAgent without a gateway (cloudflare/test/worker-agent.test.ts).
 * Exported as `ticfac-harness/testing`, never from the package root, so no
 * production import reaches it.
 */

import {
  createModels,
  type FauxResponseFactory,
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Message,
  type Models,
} from "@earendil-works/pi-ai";
import { GATEWAY_PROVIDER_ID } from "../gateway/workers-ai.js";

export type { FauxResponseFactory, Message };
export { fauxAssistantMessage, fauxToolCall };

/** Models whose only provider is a scripted faux, answering as the gateway's Workers AI. */
export function fauxGatewayModels(responses: FauxResponseFactory[]): {
  models: Models;
  calls: () => number;
} {
  const faux = fauxProvider({
    provider: GATEWAY_PROVIDER_ID,
    models: [{ id: "@cf/zai-org/glm-5.3" }, { id: "@cf/zai-org/glm-5.3-flash" }],
  });
  faux.setResponses(responses);
  const models = createModels();
  models.setProvider(faux.provider);
  return { models, calls: () => faux.state.callCount };
}
