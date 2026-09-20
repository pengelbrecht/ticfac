import { describe, expect, it } from "vitest";

import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import { READ_CONCURRENCY, TrackerClient } from "../src/tracker-client";

/**
 * A cloud plan pass reads every tick record on the ref, and a real tracker
 * has thousands of them (tick 0h9). Read one at a time, pengelbrecht/ticks'
 * 1104 records took a plan pass past fourteen minutes and past the
 * three-minute TTL of the publish slot it was holding.
 *
 * These hold the two properties that matter: the reads overlap, and they
 * overlap by a BOUND rather than all at once.
 */

const RECORDS = 200;

function tick(id: string): string {
  return JSON.stringify({
    id,
    title: `tick ${id}`,
    status: "open",
    priority: 2,
    type: "task",
    owner: "ticfac",
    created_by: "test",
    created_at: "2026-09-20T00:00:00Z",
    updated_at: "2026-09-20T00:00:00Z",
    parent: "epic1",
  });
}

/** A store that records how many reads are in flight at their peak. */
function countingStore(paths: string[]): { store: ContentsStore; peak: () => number } {
  let inFlight = 0;
  let peak = 0;
  const store: ContentsStore = {
    async list(): Promise<string[]> {
      return paths;
    },
    async read(path: string): Promise<StoredFile | null> {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      // Yield twice so an overlapping reader is actually observable: a single
      // microtask would let a sequential loop look concurrent.
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));
      inFlight -= 1;
      const id = path.replace(".tick/issues/", "").replace(".json", "");
      return { content: tick(id), sha: `sha-${id}` };
    },
    async create(): Promise<StoreWrite> {
      throw new Error("not used");
    },
    async update(): Promise<StoreWrite> {
      throw new Error("not used");
    },
  };
  return { store, peak: () => peak };
}

const paths = Array.from(
  { length: RECORDS },
  (_, i) => `.tick/issues/t${String(i).padStart(4, "0")}.json`,
);

describe("tracker reads", () => {
  it("overlaps its reads instead of doing one at a time", async () => {
    const { store, peak } = countingStore(paths);
    const client = new TrackerClient(store, "owner/repo", "epic/x");

    await client.list();

    expect(peak()).toBeGreaterThan(1);
  });

  it("keeps the overlap inside the declared bound", async () => {
    const { store, peak } = countingStore(paths);
    const client = new TrackerClient(store, "owner/repo", "epic/x");

    await client.list();

    expect(peak()).toBeLessThanOrEqual(READ_CONCURRENCY);
  });

  it("answers in tracker order, not in the order the network resolved", async () => {
    // Reads resolve fastest LAST, so a result that followed completion order
    // would come back reversed.
    let remaining = RECORDS;
    const store: ContentsStore = {
      async list(): Promise<string[]> {
        return paths;
      },
      async read(path: string): Promise<StoredFile | null> {
        const delay = remaining;
        remaining -= 1;
        await new Promise((resolve) => setTimeout(resolve, delay % 5));
        const id = path.replace(".tick/issues/", "").replace(".json", "");
        return { content: tick(id), sha: `sha-${id}` };
      },
      async create(): Promise<StoreWrite> {
        throw new Error("not used");
      },
      async update(): Promise<StoreWrite> {
        throw new Error("not used");
      },
    };
    const client = new TrackerClient(store, "owner/repo", "epic/x");

    const listed = await client.list();

    expect(listed.ticks).not.toBeNull();
    const ids = (listed.ticks ?? []).map((t) => t.id);
    expect(ids).toEqual([...ids].sort());
    expect(ids).toHaveLength(RECORDS);
  });

  it("reads every record exactly once", async () => {
    const seen: string[] = [];
    const store: ContentsStore = {
      async list(): Promise<string[]> {
        return paths;
      },
      async read(path: string): Promise<StoredFile | null> {
        seen.push(path);
        const id = path.replace(".tick/issues/", "").replace(".json", "");
        return { content: tick(id), sha: `sha-${id}` };
      },
      async create(): Promise<StoreWrite> {
        throw new Error("not used");
      },
      async update(): Promise<StoreWrite> {
        throw new Error("not used");
      },
    };
    const client = new TrackerClient(store, "owner/repo", "epic/x");

    await client.list();

    expect(seen).toHaveLength(RECORDS);
    expect(new Set(seen).size).toBe(RECORDS);
  });
});
