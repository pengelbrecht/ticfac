/**
 * The pinned fixtures certify the TypeScript tracker client (tick z23).
 *
 * SPEC §3.2's rule is that a host that cannot run tk proves its contract
 * implementation against the pinned bundle — "a copied JSON file without an
 * executable check is not a contract". This file is the executable check:
 *
 *  - every read's output is validated against the SCHEMAS in the pinned
 *    manifest itself (`contracts/tk-json-manifest.json`), through the same
 *    strict validator the Go twin shares (`test/json-schema.ts`);
 *  - the behavioural rules the manifest's command DESCRIPTIONS state are
 *    asserted as behaviour (null versus [], the ready ordering, the claim's
 *    idempotence, the note's provenance boundary, the close's refusals) —
 *    written from the fixture text, not by inspecting tk's output;
 *  - the record the encoder writes is driven through
 *    `contracts/tracker-layout.json`'s pins;
 *  - a NEGATIVE CONTROL proves the checks enforce: a deliberately broken
 *    copy of the fixture fails them.
 */

import { describe, expect, it } from "vitest";

import manifestJSON from "../../contracts/tk-json-manifest.json";
import trackerLayout from "../../contracts/tracker-layout.json";
import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import {
  encodeTick,
  MANIFEST_CONTRACT,
  MANIFEST_MIN_TK_VERSION,
  parseTick,
  TICK_REQUIRED_FIELDS,
  type Tick,
  TrackerClient,
} from "../src/tracker-client";
import { type Defs, parseDefs, parseSchema, validate } from "./json-schema";

// ----------------------------------------------------------- the fixtures ---

const manifest = manifestJSON as {
  contract: number;
  min_tk_version: string;
  commands: Array<{ id: string; schema: unknown }>;
  $defs: unknown;
};

const defs: Defs = parseDefs(manifest.$defs);

/** The manifest's schema for one command — read from the pin, never inlined. */
function commandSchema(id: string) {
  const entry = manifest.commands.find((command) => command.id === id);
  if (entry === undefined) throw new Error(`the pinned manifest has no command ${id}`);
  return parseSchema(entry.schema, "$");
}

function expectValid(id: string, output: unknown): void {
  const errors = validate(commandSchema(id), defs, output);
  expect(errors, `${id} output must satisfy the pinned schema: ${errors.join("; ")}`).toEqual([]);
}

const layout = trackerLayout as {
  record_dir: string;
  epic_type: string;
  written_by_the_control_plane: {
    required_fields: string[];
    json_indent: number;
  };
  human_gate: { statuses: string[]; committed_status: string };
};

// --------------------------------------------------------- the memory store ---

/**
 * An in-memory contents store with the contents API's own CAS: a create that
 * finds a file is `exists`; an update with a stale sha is `conflict`; nothing
 * else writes. The ref is ONE map, so two clients over it see each other's
 * commits — the shape a run pushing tracker state under a signal's write
 * actually has.
 */
class MemoryContents implements ContentsStore {
  readonly files = new Map<string, StoredFile>();
  #next = 0;

  constructor(seed: Record<string, string> = {}) {
    for (const [path, content] of Object.entries(seed)) {
      this.files.set(path, { content, sha: this.#mint() });
    }
  }

  #mint(): string {
    this.#next += 1;
    return `blob-${this.#next}`;
  }

  async list(prefix: string): Promise<string[]> {
    return [...this.files.keys()].filter((path) => path.startsWith(prefix)).sort();
  }

  async read(path: string): Promise<StoredFile | null> {
    const file = this.files.get(path);
    return file === undefined ? null : { content: file.content, sha: file.sha };
  }

  async create(path: string, input: { content: string; message: string }): Promise<StoreWrite> {
    if (this.files.has(path)) {
      return { state: "exists", detail: `${path} already exists` };
    }
    const sha = this.#mint();
    this.files.set(path, { content: input.content, sha });
    return { state: "written", commit_sha: `commit-${this.#next}`, content_sha: sha };
  }

  async update(
    path: string,
    sha: string,
    input: { content: string; message: string },
  ): Promise<StoreWrite> {
    const file = this.files.get(path);
    if (file === undefined) {
      return { state: "missing", detail: `${path} is not on this ref` };
    }
    if (file.sha !== sha) {
      return { state: "conflict", detail: `${path} moved under the write` };
    }
    const next = this.#mint();
    this.files.set(path, { content: input.content, sha: next });
    return { state: "written", commit_sha: `commit-${this.#next}`, content_sha: next };
  }

  /** A test stand-in for the run pushing records: moves the ref under a reader. */
  move(path: string, content: string): void {
    this.files.set(path, { content, sha: this.#mint() });
  }
}

// ------------------------------------------------------------ the tracker ---

const NOW = new Date("2026-09-19T10:30:00Z");

function tick(overrides: Partial<Tick> & Pick<Tick, "id" | "title">): Tick {
  return {
    status: "open",
    priority: 2,
    type: "task",
    owner: "worker@example.com",
    created_by: "planner@example.com",
    created_at: "2026-09-18T09:00:00Z",
    updated_at: "2026-09-18T09:00:00Z",
    ...overrides,
  };
}

const EPIC = "ex1";
const BRANCH = "epic/ex1";

/** A tracker with the shapes every rule below is asserted against. */
function seedTracker(): Record<string, string> {
  const records: Record<string, string> = {};
  const add = (t: Tick) => {
    records[`${layout.record_dir}/${t.id}.json`] = encodeTick(t);
  };

  add(tick({ id: EPIC, title: "Example epic", type: "epic", priority: 1 }));

  // Wave 1 work, three priorities/orders to pin the sort.
  add(tick({ id: "t01", title: "First", priority: 1, parent: EPIC }));
  add(tick({ id: "t02", title: "Second", priority: 2, parent: EPIC }));
  add(
    tick({
      id: "t03",
      title: "Third",
      priority: 2,
      parent: EPIC,
      created_at: "2026-09-18T09:05:00Z",
    }),
  );

  // Blocked children.
  add(tick({ id: "t04", title: "After first", parent: EPIC, blocked_by: ["t01"] }));
  add(tick({ id: "t05", title: "After a cross-epic blocker", parent: EPIC, blocked_by: ["zz9"] }));

  // Human gates.
  add(tick({ id: "t06", title: "Awaiting input", parent: EPIC, awaiting: "input" }));
  add(tick({ id: "t07", title: "Awaiting checkpoint", parent: EPIC, awaiting: "checkpoint" }));
  add(tick({ id: "t08", title: "Requires approval", parent: EPIC, requires: "approval" }));
  add(
    tick({
      id: "t09",
      title: "Requires review, justified",
      parent: EPIC,
      requires: "review",
      description: "gate: the provider choice needs human taste",
    }),
  );

  // Deferred.
  add(tick({ id: "t10", title: "Deferred", parent: EPIC, defer_until: "2027-01-01T00:00:00Z" }));

  // The skeleton.
  add(
    tick({ id: "rev1", title: "Final review", parent: EPIC, role: "review", blocked_by: ["t01"] }),
  );
  add(
    tick({ id: "clo1", title: "Close out", parent: EPIC, role: "closeout", blocked_by: ["rev1"] }),
  );

  // The cross-epic open blocker.
  add(tick({ id: "zz9", title: "Another epic's tick", type: "task" }));

  records[".tick/runners.toml"] = "[orchestration]\nmax_parallel = 2\n";
  return records;
}

function seededClient(seed = seedTracker(), now: () => Date = () => NOW): TrackerClient {
  return new TrackerClient(new MemoryContents(seed), "example/owner", BRANCH, { now });
}

// --------------------------------------------------------------- the tests ---

describe("the record encoder, through the tracker-layout fixture", () => {
  it("writes the fixture's required fields, at the fixture's indent", () => {
    const encoded = encodeTick(tick({ id: "t01", title: "First" }));
    const parsed = JSON.parse(encoded) as Record<string, unknown>;
    for (const field of layout.written_by_the_control_plane.required_fields) {
      expect(parsed[field], `field ${field} is required by the fixture`).not.toBeUndefined();
    }
    expect(JSON.stringify(parsed, null, layout.written_by_the_control_plane.json_indent)).toBe(
      encoded,
    );
  });

  it("round-trips a record byte for byte, and omits the empties", () => {
    const record = tick({
      id: "t04",
      title: "After first",
      parent: EPIC,
      blocked_by: ["t01"],
      labels: ["a", "b"],
      notes: "some notes",
    });
    const encoded = encodeTick(record);
    expect(encodeTick(parseTick(encoded) as Tick)).toBe(encoded);
    expect(encoded).not.toContain('"after"');
    expect(encoded).not.toContain('"verdict"');
  });

  it("keeps created_by/created_at/updated_at even when the value is empty, as Go's struct does", () => {
    const encoded = encodeTick(tick({ id: "t00", title: "Empty", created_by: "" }));
    expect(encoded).toContain('"created_by": ""');
  });

  it("pins the tick status vocabulary to the fixture's human gate", () => {
    const closed = tick({ id: "t01", title: "x", status: "closed" });
    expect(layout.human_gate.statuses).toContain(closed.status);
    expect(layout.human_gate.statuses).toContain("open");
    expect(layout.human_gate.statuses).toContain("in_progress");
  });
});

describe("the reads, against the pinned manifest schemas", () => {
  it("show returns one tick the manifest's tick schema accepts", async () => {
    const client = seededClient();
    const t01 = await client.show("t01");
    expect(t01).not.toBeNull();
    expectValid("show", t01);
    expect((t01 as Tick).parent).toBe(EPIC);
  });

  it("list returns the shared envelope, null when nothing matches", async () => {
    const client = seededClient();
    const listed = await client.list();
    expectValid("list", listed);
    expect((listed.ticks as Tick[]).length).toBe(14);

    const empty = new TrackerClient(new MemoryContents({}), "example/owner", BRANCH, {
      now: () => NOW,
    });
    expectValid("list", await empty.list());
    expect((await empty.list()).ticks).toBeNull();
  });

  it("status is the honest null of a host with no working tree", async () => {
    const client = seededClient();
    expectValid("status", client.status());
    expect(client.status().changes).toBeNull();
  });

  it("ready is the agent-eligible pool, sorted by in_progress, priority, creation, id", async () => {
    const client = seededClient();
    const ready = await client.ready();
    expectValid("ready", ready);
    const ids = (ready.ticks as Tick[]).map((t) => t.id);
    // The ready pool is every tick, epics included: deferred (t10), gated
    // (t06, t07) and blocked (t04, t05, rev1, clo1) are out; the epic and
    // the cross-epic zz9 are open and unblocked, so they are in — sorted
    // in_progress first, then priority, then creation, then id (t03 was
    // created later than its p2 siblings, so it sorts behind zz9).
    expect(ids).toEqual(["ex1", "t01", "t02", "t08", "t09", "zz9", "t03"]);
  });

  it("next is the first ready tick with its action, or the plan answer, or null", async () => {
    const client = seededClient();
    const next = await client.next();
    expectValid("next", next);
    // The epic sorts first in the ready pool (priority 1), and an epic WITH
    // children is implementable work to a consumer — planning is for the
    // childless ones.
    expect((next as { id: string }).id).toBe("ex1");
    expect((next as { action: string }).action).toBe("implement");

    const childless = seededClient({
      [`${layout.record_dir}/${EPIC}.json`]: encodeTick(
        tick({ id: EPIC, title: "Childless", type: "epic" }),
      ),
    });
    const plan = await childless.next();
    expectValid("next", plan);
    expect((plan as { id: string; action: string }).action).toBe("plan");

    const nothing = new TrackerClient(new MemoryContents({}), "example/owner", BRANCH, {
      now: () => NOW,
    });
    expect(await nothing.next()).toBeNull();
  });

  it("deps returns edges as stored, null (not []) when empty", async () => {
    const client = seededClient();
    const deps = await client.deps("t01");
    expectValid("deps", deps);
    expect(deps?.blocked_by).toBeNull();
    expect(deps?.blocks?.map((t) => t.id).sort()).toEqual(["rev1", "t04"]);

    const leaf = await client.deps("t02");
    expectValid("deps", leaf);
    expect(leaf?.blocked_by).toBeNull();
    expect(leaf?.blocks).toBeNull();

    expect(await client.deps("nope1")).toBeNull();
  });

  it("graph is the wave plan, with virtual blockers, and the dispatch answer", async () => {
    const client = seededClient();
    const graph = await client.graph(EPIC);
    expectValid("graph", graph);
    expect(graph).not.toBeNull();

    // Wave 1: the unblocked, ungated, non-closed children — priority then id.
    const wave1 = graph!.waves!.find((w) => w.wave === 1)!;
    expect(wave1.tasks!.map((t) => t.id)).toEqual(["t01", "t02", "t03", "t08", "t09", "t10"]);
    // A deferred task stays IN the wave (defer gates dispatch, not shape),
    // but is not agent-ready.
    expect(wave1.tasks!.find((t) => t.id === "t10")!.agent_ready).toBe(false);
    expect(wave1.tasks!.find((t) => t.id === "t01")!.agent_ready).toBe(true);

    // The cross-epic open blocker is virtual: t05 is held to wave 2 without
    // zz9 being emitted anywhere.
    const wave2 = graph!.waves!.find((w) => w.wave === 2)!;
    // Wave order is priority then id, the way tk sorts: rev1 sorts before t04.
    expect(wave2.tasks!.map((t) => t.id)).toEqual(["rev1", "t04", "t05"]);
    expect(graph!.waves!.flatMap((w) => (w.tasks ?? []).map((t) => t.id))).not.toContain("zz9");

    // Blocked children are not agent-ready.
    expect(wave2.tasks!.find((t) => t.id === "t04")!.agent_ready).toBe(false);

    // The skeleton closes the critical path.
    expect(graph!.critical_path).toBe(3);

    // stats.max_parallel is the widest WAVE; dispatch.max_parallel is the
    // CONFIGURED width — the two the manifest warns must not be conflated.
    expect(graph!.stats.max_parallel).toBe(6);
    expect(graph!.dispatch.max_parallel).toBe(2);
    expect(graph!.dispatch.source).toBe(".tick/runners.toml");

    // dispatch.now is wave 1 capped to the free slots, and in-flight ticks
    // are not offered again.
    expect(graph!.dispatch.now).toEqual(["t01", "t02"]);
    expect(graph!.dispatch.in_flight_ids).toEqual([]);

    // An in-progress sibling holds its slot and is not re-offered.
    const withFlight = seedTracker();
    const inProgress = JSON.parse(withFlight[`${layout.record_dir}/t01.json`]) as Tick;
    inProgress.status = "in_progress";
    withFlight[`${layout.record_dir}/t01.json`] = encodeTick(inProgress);
    const flown = await seededClient(withFlight).graph(EPIC);
    expectValid("graph", flown);
    expect(flown!.dispatch.in_flight).toBe(1);
    expect(flown!.dispatch.in_flight_ids).toEqual(["t01"]);
    expect(flown!.dispatch.free).toBe(1);
    expect(flown!.dispatch.now).toEqual(["t02"]);
  });

  it("graph reports the skeleton lint and the gate lint, and a closed sibling does not count", async () => {
    const client = seededClient();
    const graph = await client.graph(EPIC);
    // rev1 (review) and clo1 (closeout) exist, open — skeleton complete.
    expect(graph!.missing_process_ticks).toEqual([]);
    // t06 (awaiting input, no gate: line) and t08 (requires, no gate: line) are
    // unjustified; t09 carries a gate: justification; t07's checkpoint gate
    // is structural and never linted.
    expect(graph!.unjustified_gates!.sort()).toEqual(["t06", "t08"]);

    // Closing the review tick leaves the skeleton satisfied — a closed
    // process tick still counts.
    const closedRev = seedTracker();
    const rev = JSON.parse(closedRev[`${layout.record_dir}/rev1.json`]) as Tick;
    rev.status = "closed";
    closedRev[`${layout.record_dir}/rev1.json`] = encodeTick(rev);
    const after = await seededClient(closedRev).graph(EPIC);
    expect(after!.missing_process_ticks).toEqual([]);
  });

  it("a childless epic answers needs_planning, with Go's zero values", async () => {
    const client = seededClient({
      [`${layout.record_dir}/${EPIC}.json`]: encodeTick(
        tick({ id: EPIC, title: "Childless", type: "epic" }),
      ),
    });
    const graph = await client.graph(EPIC);
    expectValid("graph", graph);
    expect(graph!.needs_planning).toBe(true);
    expect(graph!.waves).toBeNull();
    expect(graph!.dispatch.in_flight_ids).toBeNull();
    expect(graph!.dispatch.free).toBe(0);
    expect(graph!.missing_process_ticks).toEqual([]);

    // An epic that is gated or blocked is not plannable now.
    const gated = seededClient({
      [`${layout.record_dir}/${EPIC}.json`]: encodeTick(
        tick({ id: EPIC, title: "Gated epic", type: "epic", awaiting: "input" }),
      ),
    });
    expect((await gated.graph(EPIC))!.needs_planning).toBe(false);
  });

  it("graph refuses a non-epic id", async () => {
    const client = seededClient();
    expect(await client.graph("t01")).toBeNull();
  });
});

describe("the controlled writes, as tk's", () => {
  it("claim is update --status in_progress: started once, idempotent after", async () => {
    const store = new MemoryContents(seedTracker());
    const client = new TrackerClient(store, "example/owner", BRANCH, { now: () => NOW });

    const first = await client.claim("t01", "worker@example.com");
    expect(first.state).toBe("written");
    expectValid("claim", (first as { tick: Tick }).tick);
    const claimed = (first as { tick: Tick }).tick;
    expect(claimed.status).toBe("in_progress");
    expect(claimed.started_at).toBe(NOW.toISOString());

    // A re-claim keeps its started_at — the stale-recovery invariant.
    const later = new Date("2026-09-19T11:00:00Z");
    const client2 = new TrackerClient(store, "example/owner", BRANCH, { now: () => later });
    const again = await client2.claim("t01", "another@example.com");
    expect(again.state).toBe("written");
    expect((again as { tick: Tick }).tick.started_at).toBe(NOW.toISOString());
    expect((again as { tick: Tick }).tick.owner).toBe("another@example.com");
  });

  it("claim is where the wave-width gate runs: a claim past the width is refused", async () => {
    const seed = seedTracker();
    for (const id of ["t01", "t02"]) {
      const record = JSON.parse(seed[`${layout.record_dir}/${id}.json`]) as Tick;
      record.status = "in_progress";
      seed[`${layout.record_dir}/${id}.json`] = encodeTick(record);
    }
    const client = seededClient(seed);
    const refused = await client.claim("t03", "worker@example.com");
    expect(refused.state).toBe("refused");
    expect((refused as { reason: string }).reason).toBe("wave_full");
    expect((refused as { detail: string }).detail).toContain("in flight");
    // A full window is a wait, not a rejection: the tick itself is untouched.
    const t03 = await client.show("t03");
    expect(t03?.status).toBe("open");
  });

  it("a re-claim of an in-flight tick holds its own slot and is admitted", async () => {
    const seed = seedTracker();
    const t01 = JSON.parse(seed[`${layout.record_dir}/t01.json`]) as Tick;
    t01.status = "in_progress";
    t01.started_at = NOW.toISOString();
    seed[`${layout.record_dir}/t01.json`] = encodeTick(t01);
    const t02 = JSON.parse(seed[`${layout.record_dir}/t02.json`]) as Tick;
    t02.status = "in_progress";
    t02.started_at = NOW.toISOString();
    seed[`${layout.record_dir}/t02.json`] = encodeTick(t02);

    const client = seededClient(seed);
    const reClaim = await client.claim("t01", "worker@example.com");
    expect(reClaim.state).toBe("written");
  });

  it("update sets only the flags passed, and an empty value clears the field", async () => {
    const client = seededClient();
    const seedNotes = await client.show("t01");
    const withNotes = await client.update("t01", { notes: "a note" });
    expect(withNotes.state).toBe("written");
    expect((withNotes as { tick: Tick }).tick.notes).toBe("a note");
    expect((withNotes as { tick: Tick }).tick.title).toBe(seedNotes?.title);

    const cleared = await client.update("t01", { notes: "" });
    expect(cleared.state).toBe("written");
    expect((cleared as { tick: Tick }).tick.notes).toBeUndefined();
  });

  it("note appends a timestamped line, and --from human is the provenance boundary", async () => {
    const store = new MemoryContents(seedTracker());
    const clock = new Date("2026-09-19T10:30:00Z");
    const client = new TrackerClient(store, "example/owner", BRANCH, { now: () => clock });

    const first = await client.note("t01", "PR ready");
    expect((first as { tick: Tick }).tick.notes).toBe("2026-09-19 10:30 - PR ready");

    clock.setUTCMinutes(45);
    const second = await client.note("t01", "direction from the operator", { from: "human" });
    expect((second as { tick: Tick }).tick.notes).toBe(
      "2026-09-19 10:30 - PR ready\n2026-09-19 10:45 - [human] direction from the operator",
    );
    expectValid("note", (second as { tick: Tick }).tick);

    const blank = await client.note("t01", "   ");
    expect(blank.state).toBe("refused");
    expect((blank as { detail: string }).detail).toBe("note text is required");
  });

  it("close sets closed_at and closed_reason and clears started_at", async () => {
    const store = new MemoryContents(seedTracker());
    const client = new TrackerClient(store, "example/owner", BRANCH, { now: () => NOW });
    await client.claim("t01", "worker@example.com");
    const closed = await client.close("t01", { reason: "done" });
    expect(closed.state).toBe("written");
    expectValid("close", (closed as { tick: Tick }).tick);
    const tick = (closed as { tick: Tick }).tick;
    expect(tick.status).toBe("closed");
    expect(tick.closed_at).toBe(NOW.toISOString());
    expect(tick.closed_reason).toBe("done");
    expect(tick.started_at).toBeUndefined();
  });

  it("a tick with an unmet requires gate is routed to awaiting, never closed", async () => {
    const client = seededClient();
    const refused = await client.close("t08");
    expect(refused.state).toBe("refused");
    expect((refused as { reason: string }).reason).toBe("gate");
    expect((refused as { detail: string }).detail).toContain("requires approval");

    const routed = await client.show("t08");
    expect(routed?.status).toBe("open");
    expect(routed?.awaiting).toBe("approval");
    expect(routed?.notes).toContain("Work complete, awaiting approval");
  });

  it("an epic with open children is refused, and reopen clears the close", async () => {
    const client = seededClient();
    const refused = await client.close(EPIC);
    expect(refused.state).toBe("refused");
    expect((refused as { reason: string }).reason).toBe("gate");
    expect((refused as { detail: string }).detail).toContain("open children");

    const seed = seedTracker();
    for (const id of [
      "t01",
      "t02",
      "t03",
      "t04",
      "t05",
      "t06",
      "t07",
      "t08",
      "t09",
      "t10",
      "rev1",
      "clo1",
    ]) {
      const record = JSON.parse(seed[`${layout.record_dir}/${id}.json`]) as Tick;
      record.status = "closed";
      seed[`${layout.record_dir}/${id}.json`] = encodeTick(record);
    }
    const closedAll = seededClient(seed);
    const closed = await closedAll.close(EPIC, { reason: "epic complete" });
    expect(closed.state).toBe("written");

    const reopened = await closedAll.reopen("t01");
    expectValid("reopen", (reopened as { tick: Tick }).tick);
    const after = (reopened as { tick: Tick }).tick;
    expect(after.status).toBe("open");
    expect(after.closed_at).toBeUndefined();
    expect(after.closed_reason).toBeUndefined();
  });

  it("a write whose ref moved under it retries on a fresh read, never over a stale one", async () => {
    const store = new MemoryContents(seedTracker());
    const client = new TrackerClient(store, "example/owner", BRANCH, { now: () => NOW });

    // Slip a foreign write under the read the claim is about to make: the
    // store's update refuses the stale sha, and the client retries having
    // re-read the record — landing on TOP of the foreign note, not over it.
    const original = store.files.get(`${layout.record_dir}/t01.json`)!;
    const moved = JSON.parse(original.content) as Tick;
    moved.notes = "a foreign writer got here first";
    store.files.set(`${layout.record_dir}/t01.json`, {
      content: encodeTick(moved),
      sha: "blob-moved-1",
    });

    const claimed = await client.claim("t01", "worker@example.com");
    expect(claimed.state).toBe("written");
    const after = await store.read(`${layout.record_dir}/t01.json`);
    const parsed = JSON.parse(after!.content) as Tick;
    expect(parsed.notes).toBe("a foreign writer got here first");
    expect(parsed.status).toBe("in_progress");
  });
});

describe("the fixtures enforce, not decorate (negative controls)", () => {
  it("a broken manifest schema FAILS this client's output, so a fixture break fails the build", async () => {
    const client = seededClient();
    // Break the tick schema's required list the way a fixture edit would: a
    // new required field the client does not know about.
    const brokenDefs = parseDefs(manifest.$defs);
    brokenDefs.tick = parseSchema(
      {
        ...((manifest.$defs as Record<string, unknown>).tick as Record<string, unknown>),
        required: [
          ...(manifest.$defs as { tick: { required: string[] } }).tick.required,
          "a_field_nobody_writes",
        ],
      },
      "$",
    );
    const errors = validate(commandSchema("show"), brokenDefs, { id: "t01" });
    expect(errors.length).toBeGreaterThan(0);

    const real = validate(commandSchema("show"), defs, { id: "t01" });
    expect(real.length).toBeGreaterThan(0); // the stub record is not a tick either
    const tick = await client.show("t01");
    expect(validate(commandSchema("show"), brokenDefs, tick).length).toBeGreaterThan(0);
    expect(validate(commandSchema("show"), defs, tick)).toEqual([]);
  });

  it("the client's required-field set IS the pinned manifest's, or the pin has drifted", () => {
    const pinned = (manifest.$defs as { tick: { required: string[] } }).tick.required;
    expect([...TICK_REQUIRED_FIELDS].sort()).toEqual([...pinned].sort());
  });

  it("the client's record path IS the pinned tracker layout's", async () => {
    const client = seededClient();
    const shown = await client.show("t01");
    expect(shown).not.toBeNull();
    // The store the fixture seeded is keyed by the layout's record_dir; a
    // client reading any other directory would have found nothing.
    expect(layout.record_dir).toBe(".tick/issues");
  });

  it("manifestContract reports the pinned contract, not a tk build", () => {
    const client = seededClient();
    const contract = client.manifestContract();
    expect(contract.contract).toBe(manifest.contract);
    expect(contract.min_tk_version).toBe(manifest.min_tk_version);
    // The mirrored constants ARE the pinned manifest's own numbers — a
    // manifest bump that does not pass through the client fails here.
    expect(contract.contract).toBe(MANIFEST_CONTRACT);
    expect(contract.min_tk_version).toBe(MANIFEST_MIN_TK_VERSION);
  });
});
