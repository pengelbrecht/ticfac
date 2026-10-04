/**
 * What the cloud `WorkerAgent` Durable Object (cloudflare/src/worker-agent.ts)
 * needs from pi-durable, and nothing more (epic 43y step 6, tick xd3).
 *
 * The factory worker never imports `@earendil-works/*` itself: this package is
 * the one place pi-durable lands in the repository, so its API churn lands in
 * one place too. The DO gets its storage, its model access and its watch
 * stream through these three functions.
 */

import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Models } from "@earendil-works/pi-ai";
import { createModels } from "@earendil-works/pi-ai/models";
import {
  type AgentEvent,
  type AgentEventStream,
  type Conversation,
  type Harness,
  type Storage,
  watchEvents,
} from "@earendil-works/pi-durable";
import { SqliteStorage } from "@earendil-works/pi-durable/storage/sqlite";
import {
  type WorkersAIGatewayProviderOptions,
  workersAIGatewayProvider,
} from "../gateway/workers-ai.js";
import {
  DurableObjectSqliteDatabase,
  type DurableObjectSqliteTarget,
} from "../storage/durable-object-sqlite.js";

export type { AgentEvent, AgentEventStream };

/**
 * pi-durable's own `SqliteStorage` over a Durable Object's SQLite — the store
 * the spike chose (docs/spikes/n0b-round2-pi-durable.md: 0 hops, acknowledged
 * only once durably replicated, single-instance by construction).
 */
export function openDurableObjectStorage(target: DurableObjectSqliteTarget): Promise<Storage> {
  return SqliteStorage.open(new DurableObjectSqliteDatabase(target));
}

/** A `Models` whose only provider is Workers AI through the factory's gateway. */
export function gatewayModelAccess(options: WorkersAIGatewayProviderOptions): Models {
  const models = createModels();
  models.setProvider(workersAIGatewayProvider(options));
  return models;
}

/**
 * The live conversation's agent events (pi-durable `watchEvents`): a
 * snapshot at attachment, then one batch per commit — what a watcher's
 * socket is sent.
 */
export function watchAttemptEvents(live: {
  harness: Harness;
  conversation: Conversation;
}): Promise<AgentEventStream> {
  return watchEvents(live.harness, live.conversation.id, BACKGROUND_CONTEXT);
}
