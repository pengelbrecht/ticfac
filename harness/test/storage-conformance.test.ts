import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { storageConformanceCaseNames } from "../src/storage/conformance.js";

/**
 * pi-durable's own storage conformance suite
 * (`@earendil-works/pi-durable/testing`), run against the
 * `DurableObjectSqlite` adapter: pi-durable's `SqliteStorage`, unchanged, over
 * a Durable Object instance's own `ctx.storage.sql`.
 *
 * Each case runs inside its own DO instance (`idFromName`, one case per name):
 * a fresh instance is a fresh SQLite database, the per-case isolation the
 * suite's provider contract expects, and the case executes exactly where the
 * storage it tests lives — inside workerd, not in a Node process speaking RPC.
 * The DO answers `{ok, error}`; the assertions themselves are ours, since no
 * `expect` runs inside a DO.
 *
 * This is the suite that fails CI when pi-durable's storage contract moves
 * under the pinned version (tick 92k): the API churn the spike warned about
 * must break here, not in a run.
 */
describe("DurableObjectSqlite storage conformance", () => {
  const names = [...storageConformanceCaseNames()];

  it("runs pi-durable's whole conformance suite", () => {
    // A suite that quietly lost its cases would be a green check that
    // certifies nothing: the names come from the pinned package, so a
    // churn that empties them is a failure, not a pass.
    expect(names.length).toBeGreaterThan(20);
  });

  for (const [index, name] of names.entries()) {
    it(name, async () => {
      const id = env.HARNESS_STORAGE.idFromName(`conformance-${index}`);
      const result = await env.HARNESS_STORAGE.get(id).runConformanceCase(name);
      // The DO's own error text is the assertion message: it is the
      // DO-side failure, the one that matters.
      expect(result.ok, result.error ?? name).toBe(true);
    });
  }
});
