import { DurableObject } from "cloudflare:workers";
import type { ConformanceCaseResult } from "./storage/conformance.js";
import { runStorageConformanceCase, storageConformanceCaseNames } from "./storage/conformance.js";

/**
 * The harness worker: test-only today, the seed of the epic's `WorkerAgent`
 * DO (docs/spikes/n0b-round2-pi-durable.md, step 6). One Durable Object
 * instance is one attempt's storage: DO SQLite, through
 * `DurableObjectSqliteDatabase`, under pi-durable's own `SqliteStorage`
 * unchanged.
 */
export class HarnessStorage extends DurableObject<unknown> {
  /** The conformance suite's case names, for a driver that registers one test per case. */
  async conformanceCaseNames(): Promise<string[]> {
    return [...storageConformanceCaseNames()];
  }

  /**
   * Run one conformance case against THIS instance's own SQLite. Drive each
   * case on its own instance (`idFromName`, one case per name): a fresh
   * instance is a fresh database, the isolation the cases assume.
   */
  async runConformanceCase(name: string): Promise<ConformanceCaseResult> {
    return runStorageConformanceCase(this.ctx.storage, name);
  }
}

export default {
  async fetch(): Promise<Response> {
    return new Response("ticfac harness package (test-only worker)", { status: 200 });
  },
};
