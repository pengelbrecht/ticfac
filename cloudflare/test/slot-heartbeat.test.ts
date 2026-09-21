import { describe, expect, it } from "vitest";

import { HEARTBEAT_FRACTION, heartbeat } from "../src/epic-reconciler";

/**
 * A pass is as long as the work inside it (tick q35). The first cloud run to
 * reach a dispatched worker spent seven minutes in one pass while a model did
 * the tick's work, then published under a publish slot that had expired four
 * minutes earlier:
 *
 *   plan-1  6:34:39 -> 6:41:39   slot expires_at 6:37:38
 *   Error: the repository publisher refused the write: no publish slot is held
 *
 * The lease was renewed once, at the top of the pass. These hold the fix: it
 * keeps beating WHILE the work runs, and it stops when the work does.
 */

const TTL = 300; // beats every 100ms at HEARTBEAT_FRACTION 3

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("the slot heartbeat", () => {
  it("keeps beating while work outlives the lease", async () => {
    let beats = 0;
    const beat = heartbeat(async () => {
      beats += 1;
      return true;
    }, TTL);

    // Work that runs well past the TTL — the shape that failed in production.
    await sleep(TTL * 3);
    beat.stop();

    // Without a heartbeat this is 0 and the lease is long gone. With one it is
    // roughly work / (ttl/3); assert the property, not an exact count.
    expect(beats).toBeGreaterThanOrEqual(HEARTBEAT_FRACTION * 2);
  });

  it("stops when the work does, and beats no more", async () => {
    let beats = 0;
    const beat = heartbeat(async () => {
      beats += 1;
      return true;
    }, TTL);

    await sleep(TTL);
    beat.stop();
    const atStop = beats;

    await sleep(TTL * 2);

    expect(beats).toBe(atStop);
  });

  it("gives up when the lease is lost, rather than beating against nothing", async () => {
    let beats = 0;
    const beat = heartbeat(async () => {
      beats += 1;
      return beats < 2; // the second beat reports the slot gone
    }, TTL);

    await sleep(TTL * 3);
    beat.stop();

    expect(beats).toBe(2);
  });

  it("survives a beat that throws instead of taking the pass with it", async () => {
    let beats = 0;
    const beat = heartbeat(async () => {
      beats += 1;
      throw new Error("the room did not answer");
    }, TTL);

    // The throw must not reject anything the pass is awaiting; if it did, this
    // test would fail with that error rather than reaching its assertion.
    await sleep(TTL * 3);
    beat.stop();

    expect(beats).toBe(1);
  });

  it("beats well inside the lease, so one missed beat is not an expiry", async () => {
    const at: number[] = [];
    const started = Date.now();
    const beat = heartbeat(async () => {
      at.push(Date.now() - started);
      return true;
    }, TTL);

    await sleep(TTL * 2);
    beat.stop();

    expect(at.length).toBeGreaterThan(1);
    for (let i = 1; i < at.length; i += 1) {
      // Every gap leaves room to miss one beat and still renew in time.
      expect(at[i] - at[i - 1]).toBeLessThan(TTL / 2);
    }
  });
});
