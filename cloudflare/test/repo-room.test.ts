import { env, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import {
  DEFAULT_LEASE_TTL_MS,
  MAX_LEASE_TTL_MS,
  MIN_LEASE_TTL_MS,
  type RepoRoom,
} from "../src/repo-room";

/**
 * Tick ef7 (SPEC §12 Phase 4 item 3): the repository Durable Object — one
 * slot, one serialized publisher.
 *
 * The slot cases below are RunRoom's existing lease tests, run against this
 * room's slot: the acceptance is that RunRoom's lease behaviour is PRESERVED,
 * and both rooms share `src/lease.ts`, so the same cases must hold here. Where
 * a case cites one, it is quoting `run-room.test.ts`.
 */
function room(project: string) {
  return env.REPO_ROOMS.get(env.REPO_ROOMS.idFromName(project));
}

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/** Reads the slot row past the public API, to tell a real delete from a lazy read. */
async function slotRows(stub: DurableObjectStub<RepoRoom>): Promise<{ run_id: string }[]> {
  let rows: { run_id: string }[] = [];
  await runInDurableObject(stub, (_instance, state) => {
    rows = [...state.storage.sql.exec<{ run_id: string }>("SELECT run_id FROM publish_slot")];
  });
  return rows;
}

async function scheduledAlarm(stub: DurableObjectStub<RepoRoom>): Promise<number | null> {
  let at: number | null = null;
  await runInDurableObject(stub, async (_instance, state) => {
    at = await state.storage.getAlarm();
  });
  return at;
}

// -------------------------------------------------------- the contents fake ---

/**
 * The in-memory contents store the DO's publishes land in. Reaches the DO the
 * same way the Workflow's injected store would: through `env.TICK_CONTENTS`,
 * which a DO reads from its own env — the seam `contentsStore()` honours.
 *
 * `delayMs` plus the in-flight counters are what the serialization test
 * observes: a publisher that lets two writes overlap cannot pass a fake that
 * remembers its own high-water mark.
 */
class RecordingContents implements ContentsStore {
  readonly files = new Map<string, StoredFile>();
  readonly order: { op: string; path: string }[] = [];
  delayMs = 0;
  #inFlight = 0;
  maxInFlight = 0;
  #n = 0;

  async list(prefix: string): Promise<string[]> {
    return [...this.files.keys()].filter((p) => p.startsWith(prefix)).sort();
  }

  async read(path: string): Promise<StoredFile | null> {
    return this.files.get(path) ?? null;
  }

  #enter(): void {
    this.#inFlight += 1;
    this.maxInFlight = Math.max(this.maxInFlight, this.#inFlight);
  }

  #leave(): void {
    this.#inFlight -= 1;
  }

  async create(path: string, input: { content: string; message: string }): Promise<StoreWrite> {
    this.#enter();
    try {
      await wait(this.delayMs);
      if (this.files.has(path)) {
        return { state: "exists", detail: `${path} already exists` };
      }
      this.#n += 1;
      this.files.set(path, { content: input.content, sha: `blob-${this.#n}` });
      this.order.push({ op: "create", path });
      return { state: "written", commit_sha: `commit-${this.#n}`, content_sha: `blob-${this.#n}` };
    } finally {
      this.#leave();
    }
  }

  async update(
    path: string,
    sha: string,
    input: { content: string; message: string },
  ): Promise<StoreWrite> {
    this.#enter();
    try {
      await wait(this.delayMs);
      const existing = this.files.get(path);
      if (existing === undefined) return { state: "missing", detail: `${path} is not on the ref` };
      if (existing.sha !== sha) {
        return { state: "conflict", detail: `${path} moved from ${sha} to ${existing.sha}` };
      }
      this.#n += 1;
      this.files.set(path, { content: input.content, sha: `blob-${this.#n}` });
      this.order.push({ op: "update", path });
      return { state: "written", commit_sha: `commit-${this.#n}`, content_sha: `blob-${this.#n}` };
    } finally {
      this.#leave();
    }
  }
}

/** The set() pattern the suite uses: mutate a binding, restore it after. */
const saved: Record<string, unknown> = {};

function set(name: string, value: unknown): void {
  if (!(name in saved)) saved[name] = (env as unknown as Record<string, unknown>)[name];
  (env as unknown as Record<string, unknown>)[name] = value;
}

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete (env as unknown as Record<string, unknown>)[name];
    else (env as unknown as Record<string, unknown>)[name] = value;
    delete saved[name];
  }
});

function contentsFor(project: string, ref: string): RecordingContents {
  const store = new RecordingContents();
  set("TICK_CONTENTS", { project, ref, store });
  return store;
}

// ---------------------------------------------------------------- the slot ---

describe("the slot is RunRoom's lease behaviour, preserved (see run-room.test.ts)", () => {
  it("is addressable by repository and reports an empty status", async () => {
    const res = await room("owner/repo-room-status").fetch("https://repo-room/status");

    expect(res.status).toBe(200);
    await expect(res.json()).resolves.toEqual({ object: "RepoRoom", slot: null });
  });

  it("grants the slot to the first acquirer", async () => {
    const stub = room("owner/repo-first");

    const result = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.renewed).toBe(false);
    expect(result.lease).toMatchObject({ run_id: "run_1", epic: "ko8", origin: "cloud" });
    expect(result.lease.token).toMatch(/^[0-9a-f-]{20,}$/i);
    expect(Date.parse(result.lease.expires_at) - Date.parse(result.lease.acquired_at)).toBe(
      DEFAULT_LEASE_TTL_MS,
    );

    await expect(stub.slotStatus()).resolves.toMatchObject({ run_id: "run_1" });
  });

  it("yields one holder and one rejection naming it when two acquires race", async () => {
    const stub = room("owner/repo-race");

    const results = await Promise.all([
      stub.acquireSlot({ run_id: "run_a", epic: "ko8", origin: "cloud" }),
      stub.acquireSlot({ run_id: "run_b", epic: "ko8", origin: "local" }),
    ]);

    const granted = results.filter((r) => r.ok);
    const refused = results.filter((r) => !r.ok);
    expect(granted).toHaveLength(1);
    expect(refused).toHaveLength(1);

    const holder = granted[0]!;
    const loser = refused[0]!;
    if (holder.ok !== true || loser.ok !== false || loser.error !== "lease_held") {
      throw new Error("expected exactly one grant and one lease_held refusal");
    }

    expect(loser.reason).toBe(`lease_held_by:${holder.lease.run_id}`);
    expect(loser.detail).toContain(holder.lease.run_id);

    await expect(stub.slotStatus()).resolves.toMatchObject({ run_id: holder.lease.run_id });
  });

  it("never hands a loser the holder's release token", async () => {
    const stub = room("owner/repo-token");
    await stub.acquireSlot({ run_id: "run_holder", epic: "ko8" });

    const refused = await stub.acquireSlot({ run_id: "run_other", epic: "ko8" });
    const status = await stub.slotStatus();

    if (refused.ok !== false || refused.error !== "lease_held") {
      throw new Error("expected a lease_held refusal");
    }
    expect(refused.holder).not.toHaveProperty("token");
    expect(status).not.toBeNull();
    expect(status).not.toHaveProperty("token");
  });

  it("treats a re-acquire by the same run as a renewal, not a conflict", async () => {
    const stub = room("owner/repo-reacquire");
    const first = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });
    if (!first.ok) throw new Error("expected the first acquire to win");

    await wait(5);
    const again = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });

    expect(again.ok).toBe(true);
    if (!again.ok) return;
    expect(again.renewed).toBe(true);
    expect(again.lease.token).toBe(first.lease.token);
    expect(again.lease.acquired_at).toBe(first.lease.acquired_at);
    expect(Date.parse(again.lease.expires_at)).toBeGreaterThan(Date.parse(first.lease.expires_at));
  });

  it("renews only for the holder's own token", async () => {
    const stub = room("owner/repo-renew");
    const held = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });
    if (!held.ok) throw new Error("expected the acquire to win");

    const renewed = await stub.renewSlot({ run_id: "run_1", token: held.lease.token });
    const impostor = await stub.renewSlot({ run_id: "run_1", token: "not-the-token" });

    expect(renewed.ok).toBe(true);
    expect(impostor.ok).toBe(false);
    if (impostor.ok !== false) return;
    expect(impostor.error).toBe("lease_lost");
  });

  it("tells an expired slot apart from one another run took", async () => {
    const stub = room("owner/repo-lost");
    const mine = await stub.acquireSlot({
      run_id: "run_mine",
      epic: "ko8",
      ttl_ms: MIN_LEASE_TTL_MS,
    });
    if (!mine.ok) throw new Error("expected the acquire to win");

    // Nobody took it; it simply ran out.
    await wait(MIN_LEASE_TTL_MS + 20);
    const lapsed = await stub.renewSlot({ run_id: "run_mine", token: mine.lease.token });
    expect(lapsed.ok).toBe(false);
    if (lapsed.ok !== false || lapsed.error !== "lease_lost")
      throw new Error("expected lease_lost");
    expect(lapsed.lost).toBe("expired");
    expect(lapsed.holder).toBeNull();
    expect(lapsed.detail).toContain("expired");
    expect(lapsed.detail).toContain("no other run has taken it");

    // Now somebody really does hold it, and the same call says so differently.
    const theirs = await stub.acquireSlot({ run_id: "run_theirs", epic: "ko8" });
    expect(theirs.ok).toBe(true);
    const taken = await stub.renewSlot({ run_id: "run_mine", token: mine.lease.token });
    if (taken.ok !== false || taken.error !== "lease_lost") throw new Error("expected lease_lost");
    expect(taken.lost).toBe("taken");
    expect(taken.holder?.run_id).toBe("run_theirs");
    expect(taken.detail).toContain("run_theirs");
  });

  it("refuses a ttl outside the pinned bounds instead of clamping it", async () => {
    const stub = room("owner/repo-ttl");

    const tooShort = await stub.acquireSlot({
      run_id: "run_1",
      epic: "ko8",
      ttl_ms: MIN_LEASE_TTL_MS - 1,
    });
    const tooLong = await stub.acquireSlot({
      run_id: "run_1",
      epic: "ko8",
      ttl_ms: MAX_LEASE_TTL_MS + 1,
    });

    for (const refused of [tooShort, tooLong]) {
      expect(refused.ok).toBe(false);
      if (refused.ok === false) {
        expect(refused.error).toBe("invalid_request");
        expect(refused.detail).toMatch(/ttl/i);
      }
    }
    // A refused acquire takes no slot.
    await expect(stub.slotStatus()).resolves.toBeNull();
  });

  it("refuses a malformed acquire rather than throwing across RPC", async () => {
    const stub = room("owner/repo-invalid");

    const noRun = await stub.acquireSlot({ run_id: "", epic: "ko8" });
    const noEpic = await stub.acquireSlot({ run_id: "run_1", epic: "  " });

    for (const refused of [noRun, noEpic]) {
      expect(refused.ok).toBe(false);
      if (refused.ok === false) expect(refused.error).toBe("invalid_request");
    }
  });
});

describe("the slot releases by compare-and-delete (see run-room.test.ts)", () => {
  it("releases for the holder", async () => {
    const stub = room("owner/repo-release");
    const held = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });
    if (!held.ok) throw new Error("expected the acquire to win");

    const released = await stub.releaseSlot({ run_id: "run_1", token: held.lease.token });

    expect(released.ok).toBe(true);
    await expect(stub.slotStatus()).resolves.toBeNull();
    expect(await slotRows(stub)).toEqual([]);
    expect(await scheduledAlarm(stub)).toBeNull();
  });

  it("refuses to release a slot it does not hold", async () => {
    const stub = room("owner/repo-cad");
    const held = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });
    if (!held.ok) throw new Error("expected the acquire to win");

    const wrongToken = await stub.releaseSlot({ run_id: "run_1", token: "stale-token" });
    const wrongRun = await stub.releaseSlot({ run_id: "run_2", token: held.lease.token });

    for (const refused of [wrongToken, wrongRun]) {
      expect(refused.ok).toBe(false);
      if (refused.ok === false && refused.error === "not_holder") {
        expect(refused.holder).toMatchObject({ run_id: "run_1" });
        expect(refused.holder).not.toHaveProperty("token");
      }
    }

    await expect(stub.slotStatus()).resolves.toMatchObject({ run_id: "run_1" });
  });

  it("does not let a superseded holder free its successor's slot", async () => {
    const stub = room("owner/repo-supersede");
    const stale = await stub.acquireSlot({
      run_id: "run_old",
      epic: "ko8",
      ttl_ms: MIN_LEASE_TTL_MS,
    });
    if (!stale.ok) throw new Error("expected the first acquire to win");

    await wait(MIN_LEASE_TTL_MS + 40);
    const fresh = await stub.acquireSlot({ run_id: "run_new", epic: "ko8" });
    if (!fresh.ok) throw new Error("expected takeover of an expired slot");

    const refused = await stub.releaseSlot({ run_id: "run_old", token: stale.lease.token });

    expect(refused.ok).toBe(false);
    if (refused.ok === false && refused.error === "not_holder") {
      expect(refused.holder).toMatchObject({ run_id: "run_new" });
    }
    await expect(stub.slotStatus()).resolves.toMatchObject({ run_id: "run_new" });
  });
});

describe("the slot expires by alarm, like RunRoom's lease (see run-room.test.ts)", () => {
  it("schedules the alarm at the slot deadline", async () => {
    const stub = room("owner/repo-alarm-set");
    const held = await stub.acquireSlot({ run_id: "run_1", epic: "ko8" });
    if (!held.ok) throw new Error("expected the acquire to win");

    expect(await scheduledAlarm(stub)).toBe(Date.parse(held.lease.expires_at));
  });

  it("expires an abandoned slot by alarm", async () => {
    const stub = room("owner/repo-alarm-expire");
    const held = await stub.acquireSlot({
      run_id: "run_abandoned",
      epic: "ko8",
      ttl_ms: MIN_LEASE_TTL_MS,
    });
    if (!held.ok) throw new Error("expected the acquire to win");
    expect(await slotRows(stub)).toEqual([{ run_id: "run_abandoned" }]);

    await wait(MIN_LEASE_TTL_MS + 40);
    await runDurableObjectAlarm(stub);

    expect(await slotRows(stub)).toEqual([]);
    await expect(stub.slotStatus()).resolves.toBeNull();
    expect(await scheduledAlarm(stub)).toBeNull();
  });

  it("follows a renewed slot instead of dropping it when the alarm runs early", async () => {
    const stub = room("owner/repo-alarm-live");
    const held = await stub.acquireSlot({ run_id: "run_live", epic: "ko8" });
    if (!held.ok) throw new Error("expected the acquire to win");

    await runDurableObjectAlarm(stub);

    expect(await slotRows(stub)).toEqual([{ run_id: "run_live" }]);
    await expect(stub.slotStatus()).resolves.toMatchObject({ run_id: "run_live" });
    expect(await scheduledAlarm(stub)).toBe(Date.parse(held.lease.expires_at));
  });
});

// ------------------------------------------------------------- the publisher ---

describe("the one serialized publisher", () => {
  const PROJECT = "owner/repo-pub";
  const REF = "epic/ko8";

  async function holder(stub: DurableObjectStub<RepoRoom>, runID = "run_1") {
    const acquired = await stub.acquireSlot({ run_id: runID, epic: "ko8" });
    if (!acquired.ok) throw new Error("expected the acquire to win");
    return { run_id: runID, token: acquired.lease.token };
  }

  it("publishes for the holder through the repository's contents", async () => {
    const stub = room(PROJECT);
    const contents = contentsFor(PROJECT, REF);
    const credentials = await holder(stub);

    const created = await stub.publish({
      holder: credentials,
      ref: REF,
      write: {
        op: "create",
        path: ".ticfac/runs/run_1/checkpoint.json",
        content: "{}",
        message: "ticfac run run_1: open the run state",
      },
    });

    expect(created.ok).toBe(true);
    if (!created.ok) return;
    expect(created.write.state).toBe("written");
    await expect(contents.read(".ticfac/runs/run_1/checkpoint.json")).resolves.toMatchObject({
      content: "{}",
    });
  });

  it("publishes an update guarded by the blob sha, like the store it stands in for", async () => {
    const stub = room("owner/repo-pub-update");
    const contents = contentsFor("owner/repo-pub-update", REF);
    const credentials = await holder(stub);

    const created = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "create", path: "notes.md", content: "one", message: "first" },
    });
    if (!created.ok || created.write.state !== "written") throw new Error("expected a write");
    const sha = created.write.content_sha;

    const updated = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "update", path: "notes.md", sha, content: "two", message: "second" },
    });
    const stale = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "update", path: "notes.md", sha, content: "three", message: "third" },
    });

    expect(updated.ok && updated.write.state).toBe("written");
    expect(stale.ok && stale.write.state).toBe("conflict");
    await expect(contents.read("notes.md")).resolves.toMatchObject({ content: "two" });
  });

  it("refuses a publish from a run that does not hold the slot, naming the holder", async () => {
    const stub = room("owner/repo-pub-holder");
    const contents = contentsFor("owner/repo-pub-holder", REF);
    const mine = await holder(stub, "run_mine");

    const refused = await stub.publish({
      holder: { run_id: "run_other", token: "a-token-it-cannot-have" },
      ref: REF,
      write: { op: "create", path: "notes.md", content: "x", message: "x" },
    });

    expect(refused.ok).toBe(false);
    if (refused.ok !== false || refused.error !== "not_holder") {
      throw new Error("expected a not_holder refusal");
    }
    expect(refused.holder).toMatchObject({ run_id: "run_mine" });
    expect(refused.holder).not.toHaveProperty("token");
    expect(refused.detail).toContain("run_mine");
    // Nothing was written, and the real holder still can be.
    expect(contents.files.size).toBe(0);
    const ok = await stub.publish({
      holder: mine,
      ref: REF,
      write: { op: "create", path: "notes.md", content: "x", message: "x" },
    });
    expect(ok.ok && ok.write.state).toBe("written");
  });

  it("refuses a publish when nobody holds the slot at all", async () => {
    const stub = room("owner/repo-pub-nobody");
    contentsFor("owner/repo-pub-nobody", REF);

    const refused = await stub.publish({
      holder: { run_id: "run_1", token: "any" },
      ref: REF,
      write: { op: "create", path: "notes.md", content: "x", message: "x" },
    });

    expect(refused.ok).toBe(false);
    if (refused.ok !== false || refused.error !== "not_holder") {
      throw new Error("expected a not_holder refusal");
    }
    expect(refused.holder).toBeNull();
  });

  it("serializes concurrent publishes: one write in flight at a time, in arrival order", async () => {
    const stub = room("owner/repo-pub-serial");
    const contents = contentsFor("owner/repo-pub-serial", REF);
    contents.delayMs = 30; // slow enough that unserialized publishes must overlap
    const credentials = await holder(stub);

    const [first, second] = await Promise.all([
      stub.publish({
        holder: credentials,
        ref: REF,
        write: { op: "create", path: "a.md", content: "a", message: "a" },
      }),
      stub.publish({
        holder: credentials,
        ref: REF,
        write: { op: "create", path: "b.md", content: "b", message: "b" },
      }),
    ]);

    expect(first.ok && first.write.state).toBe("written");
    expect(second.ok && second.write.state).toBe("written");
    // The one thing this room exists to prove: never two writes at once.
    expect(contents.maxInFlight).toBe(1);
    expect(contents.order.map((w) => w.path)).toEqual(["a.md", "b.md"]);
  });

  it("refuses a malformed publish rather than throwing across RPC", async () => {
    const stub = room("owner/repo-pub-invalid");
    const credentials = await holder(stub);

    const noPath = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "create", path: "", content: "x", message: "x" },
    });
    const noContent = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "create", path: "notes.md", content: "", message: "x" },
    });
    const updateWithoutSha = await stub.publish({
      holder: credentials,
      ref: REF,
      write: { op: "update", path: "notes.md", sha: "", content: "x", message: "x" },
    });
    const noRef = await stub.publish({
      holder: credentials,
      ref: " ",
      write: { op: "create", path: "notes.md", content: "x", message: "x" },
    });

    for (const refused of [noPath, noContent, updateWithoutSha, noRef]) {
      expect(refused.ok).toBe(false);
      if (refused.ok === false) expect(refused.error).toBe("invalid_request");
    }
  });
});

// ------------------------------------------------------------- the acceptance ---

describe("two concurrent runs of the same epic cannot both write", () => {
  const PROJECT = "owner/repo-two-runs";
  const REF = "epic/ko8";

  it("hands one run the slot and the other an actionable refusal, in both directions", async () => {
    const stub = room(PROJECT);
    const contents = contentsFor(PROJECT, REF);

    // Run A is the working run: it holds the one slot.
    const acquiredA = await stub.acquireSlot({ run_id: "run_a", epic: "ko8" });
    if (!acquiredA.ok) throw new Error("expected run_a to acquire the slot");
    const a = { run_id: "run_a", token: acquiredA.lease.token };

    // Run B of the same epic cannot ignite past the slot...
    const acquiredB = await stub.acquireSlot({ run_id: "run_b", epic: "ko8" });
    if (acquiredB.ok !== false || acquiredB.error !== "lease_held") {
      throw new Error("expected run_b's acquire to be refused");
    }
    expect(acquiredB.reason).toBe("lease_held_by:run_a");

    // ...and cannot write either, even presenting a token it guessed.
    const refused = await stub.publish({
      holder: { run_id: "run_b", token: "guessed" },
      ref: REF,
      write: {
        op: "create",
        path: ".ticfac/runs/run_b/checkpoint.json",
        content: "{}",
        message: "b",
      },
    });
    if (refused.ok !== false || refused.error !== "not_holder") {
      throw new Error("expected run_b's publish to be refused");
    }
    expect(refused.holder).toMatchObject({ run_id: "run_a" });

    // Run A writes; run B has written nothing.
    const written = await stub.publish({
      holder: a,
      ref: REF,
      write: {
        op: "create",
        path: ".ticfac/runs/run_a/checkpoint.json",
        content: "{}",
        message: "a",
      },
    });
    expect(written.ok && written.write.state).toBe("written");
    await expect(contents.list(".ticfac/runs")).resolves.toEqual([
      ".ticfac/runs/run_a/checkpoint.json",
    ]);

    // Run A releases; the slot is free, and run B can take it and write.
    const released = await stub.releaseSlot(a);
    expect(released.ok).toBe(true);
    const acquiredB2 = await stub.acquireSlot({ run_id: "run_b", epic: "ko8" });
    if (!acquiredB2.ok) throw new Error("expected run_b to acquire the freed slot");
    const b = { run_id: "run_b", token: acquiredB2.lease.token };
    const writtenB = await stub.publish({
      holder: b,
      ref: REF,
      write: {
        op: "create",
        path: ".ticfac/runs/run_b/checkpoint.json",
        content: "{}",
        message: "b",
      },
    });
    expect(writtenB.ok && writtenB.write.state).toBe("written");
    expect(contents.maxInFlight).toBe(1);
  });
});
