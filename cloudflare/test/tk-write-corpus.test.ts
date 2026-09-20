/**
 * The tracker client's writes, byte-compared against tk's OWN output for a
 * generated corpus (tick v3q).
 *
 * `tracker-client.test.ts` certifies the client against the pinned manifest's
 * schemas and the behavioural rules its descriptions state — but every fixture
 * in it was written by the same hand that wrote the client, from the same
 * reading of the same contract. It cannot fail when the client drifts from
 * what tk ACTUALLY writes, because nothing in it ever asked tk. That was the
 * b9w failure shape returning, and this file is the repair's consumer half:
 *
 *  - `cloudflare/test/fixtures/tk-write-corpus.json` is GENERATED, by
 *    `internal/tkcorpus`, by driving a real tk binary command by command
 *    (creates, claims, notes, updates, closes, reopens) in a throwaway
 *    repository and capturing the exact record bytes tk committed. Only two
 *    things cannot be byte-stable across runs are canonicalized — the wall
 *    clock and the random ids — and the corpus header documents both.
 *  - This test replays every corpus case through the REAL client against an
 *    in-memory contents store, at the case's pinned instant, and asserts the
 *    bytes the store then holds are EXACTLY the bytes tk wrote: field order,
 *    omitempty, indent, note-line format, status transitions, the works.
 *
 * The two halves refuse drift in both directions: when the client changes
 * what it writes, this test fails (here, and in CI's vitest job); when tk
 * changes what it writes, `go test ./internal/tkcorpus` fails on every host
 * that has tk — which is every host the per-tick gate runs on — and the corpus
 * is regenerated deliberately with `go test ./internal/tkcorpus -update`.
 *
 * The fixture pin exists because this host cannot run tk (SPEC §3.1) — the
 * tests run inside real workerd, where there is no subprocess — so the
 * comparison is against the pinned corpus rather than a live binary, and the
 * Go guard is what keeps the pin honest about the live binary.
 */

import { describe, expect, it } from "vitest";

import type { ContentsStore, StoredFile, StoreWrite } from "../src/git-contents";
import { TrackerClient } from "../src/tracker-client";
import corpusJSON from "./fixtures/tk-write-corpus.json";

type CorpusOp = {
  kind: "create" | "claim" | "note" | "update" | "close" | "reopen";
  argv: string[];
  owner?: string;
  text?: string;
  from?: string;
  notes?: string | null;
  reason?: string | null;
};

type CorpusCase = {
  id: string;
  op: CorpusOp;
  now: string;
  result: "written" | "refused";
  before: string | null;
  after: string;
};

const corpus = corpusJSON as {
  generated: Record<string, unknown>;
  cases: CorpusCase[];
};

const cases = corpus.cases;

// ------------------------------------------------------------ the memory store ---

/**
 * The same in-memory contents store tracker-client.test.ts uses: the
 * contents API's own compare-and-swap, one map per ref, no other writes. The
 * corpus chains each case's `before` to the previous case's `after`, so a
 * single store seeded with the creates replays the whole corpus in order.
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
}

// --------------------------------------------------------------- the replay ---

const RECORD_DIR = ".tick/issues";

function pathOf(id: string): string {
  return `${RECORD_DIR}/${id}.json`;
}

/** Replays one case against a fresh store seeded with `state`, returns the bytes the store holds after. */
async function replay(
  testCase: CorpusCase,
  state: Map<string, string>,
): Promise<{ writeState: string; bytes: string }> {
  const seed: Record<string, string> = {};
  for (const [path, content] of state) {
    seed[path] = content;
  }
  const store = new MemoryContents(seed);
  const client = new TrackerClient(store, "example/owner", "epic/corpus", {
    now: () => new Date(testCase.now),
  });

  let write: { state: string } = { state: "unmapped" };
  switch (testCase.op.kind) {
    case "claim":
      write = await client.claim(testCase.id, testCase.op.owner ?? "");
      break;
    case "note":
      write = await client.note(testCase.id, testCase.op.text ?? "", {
        from: testCase.op.from === "human" ? "human" : "agent",
      });
      break;
    case "update":
      write = await client.update(testCase.id, { notes: testCase.op.notes ?? "" });
      break;
    case "close":
      write =
        testCase.op.reason === undefined || testCase.op.reason === null
          ? await client.close(testCase.id)
          : await client.close(testCase.id, { reason: testCase.op.reason });
      break;
    case "reopen":
      write = await client.reopen(testCase.id);
      break;
    case "create":
      // The client has no create — creates belong to tracker-write.ts. The
      // corpus carries them to seed the store with records tk itself wrote.
      write = { state: "seeded" };
      break;
  }

  const file = await store.read(pathOf(testCase.id));
  return { writeState: write.state, bytes: file === null ? "" : file.content };
}

// ----------------------------------------------------------------- the tests ---

describe("the tracker client's writes, byte-compared against tk's own (v3q)", () => {
  it("the corpus is generated from tk, not hand-picked", () => {
    // Structural refusals: a corpus trimmed to the cases that happen to pass,
    // or curated to avoid the shapes the client gets wrong, fails HERE rather
    // than passing quietly. The Go twin of these checks
    // (TestTkWriteCorpusPinIsWellFormed) enforces the same on the generator's
    // side of the pin.
    expect(cases.length).toBeGreaterThanOrEqual(30);

    const kinds = new Set(cases.map((c) => c.op.kind));
    for (const kind of ["claim", "note", "update", "close", "reopen"]) {
      expect(kinds, `the corpus has no ${kind} case`).toContain(kind);
    }
    // tk writes these with Go's encoding/json HTML escaping; the corpus must
    // carry them, or it cannot certify the client's escaping.
    expect(
      cases.some((c) => c.after.includes("\\u0026") || c.after.includes("\\u003c")),
      "no case pins Go's HTML escaping — the divergence this corpus exists to catch",
    ).toBe(true);
    // Both refusals: a routed close (bytes change) and a plain refusal (bytes unchanged).
    expect(cases.some((c) => c.result === "refused" && c.after !== c.before)).toBe(true);
    expect(cases.some((c) => c.result === "refused" && c.after === c.before)).toBe(true);
    // The provenance boundary.
    expect(cases.some((c) => c.op.kind === "note" && c.op.from === "human")).toBe(true);
    // The stale-recovery invariant: a re-claim whose pinned before is already
    // in_progress, so the byte pin covers started_at being KEPT.
    expect(
      cases.some((c) => c.op.kind === "claim" && /"status": "in_progress"/.test(c.before ?? "")),
    ).toBe(true);
    // The chain: every non-create case's before is some earlier case's after.
    const afters = new Set(cases.filter((c) => c.op.kind === "create").map((c) => c.after));
    for (const c of cases) {
      if (c.op.kind === "create") continue;
      expect(
        afters,
        `case ${c.id} (${c.op.kind}): before is not part of the corpus chain`,
      ).toContain(c.before);
      afters.add(c.after);
    }
  });

  it("every write commits exactly the bytes tk committed", async () => {
    const state = new Map<string, string>();
    for (const testCase of cases) {
      if (testCase.op.kind === "create") {
        // Seed the store with the record exactly as tk wrote it.
        expect(testCase.before).toBeNull();
        state.set(pathOf(testCase.id), testCase.after);
        continue;
      }
      expect(
        state.get(pathOf(testCase.id)),
        `case ${testCase.id} (${testCase.op.kind}): the store does not hold the before bytes tk held`,
      ).toBe(testCase.before);

      const { writeState, bytes } = await replay(testCase, state);
      expect(writeState, `case ${testCase.id} (${testCase.op.kind})`).toBe(testCase.result);
      // THE byte compare. Not a shape check, not a semantic subset: the exact
      // bytes tk wrote for this write, with only the clock and id values
      // canonicalized — and both sides canonicalize identically, because the
      // client's clock is the case's own pinned instant.
      expect(bytes, `case ${testCase.id} (${testCase.op.kind}) must commit tk's bytes`).toBe(
        testCase.after,
      );
      state.set(pathOf(testCase.id), bytes);
    }
  });

  it("the byte compare has teeth: the pre-fix divergence fails it", async () => {
    // Negative control. Before v3q the client wrote `&`, `<` and `>` raw,
    // because JSON.stringify does not do Go's HTML escaping. Take a case that
    // pins an escaped byte, un-escape it, and the replay must refuse it.
    const pinned = cases.find((c) => c.after.includes("\\u0026") && c.op.kind !== "create");
    expect(pinned).toBeDefined();
    const corrupted = (pinned as CorpusCase).after.replace("\\u0026", "&");
    expect(corrupted).not.toBe((pinned as CorpusCase).after);

    const state = new Map<string, string>();
    for (const c of cases) {
      if (c.op.kind !== "create") continue;
      state.set(pathOf(c.id), c.after);
    }
    // The case may not be the first op on its tick: seed the whole chain up to it.
    for (const c of cases) {
      if (c === pinned) break;
      if (c.id === (pinned as CorpusCase).id && c.op.kind !== "create") {
        state.set(pathOf(c.id), c.after);
      }
    }
    const { bytes } = await replay(pinned as CorpusCase, state);
    expect(bytes).toBe((pinned as CorpusCase).after); // the client's real bytes match tk
    expect(bytes).not.toBe(corrupted); // and would NOT match the un-escaped bytes
  });
});
