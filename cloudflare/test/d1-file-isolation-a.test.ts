import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { insertRun } from "../src/db";

// Half of a two-file guard (with d1-file-isolation-b.test.ts) for the
// isolation uhe refused to trade away: every test file must see a schema-
// correct D1 and NO rows written by another test file.
//
// One file cannot observe that property — a row is invisible from the next
// file only when there IS a next file — so this pair is one guard. Each file
// writes a marker row through the production query layer and then asserts
// that its OWN marker is the only `run-d1-isolation-*` row it can see. If the
// runtime ever hands files a shared database (vitest's `isolate: false` is
// the switch; tick 5qj recorded the corruption that shape caused), whichever
// of the two files runs second sees the other's marker and fails, in either
// execution order. Until 2026-10-07 this guarantee was measured by hand and
// the measurement left with the terminal it ran in; now the suite fails
// instead of silently going green under a bleed.
//
// The 0.22 pool gives each test file its own workerd and its own in-memory
// D1, so both files pass today — that is the point: the guard pins the
// mechanism the suite relies on, and a runtime change that quietly widens
// what a file can see has to fail a test here before it can fail a run.

/** The row THIS file writes; file B writes `run-d1-isolation-b`. */
const OWN_MARKER = "run-d1-isolation-a";

describe("per-file D1 isolation (file a)", () => {
  it("sees the whole deploy migration set applied", async () => {
    // The setup file applies the deploy-authored set once per test file;
    // counting the bookkeeping table against the binding (rather than naming
    // tables) keeps this correct as migrations are added, and makes "setup
    // silently stopped applying" a loud failure instead of a green suite
    // whose queries happen to be re-run by every test.
    const applied = await env.DB.prepare("SELECT count(*) AS n FROM d1_migrations").all<{
      n: number;
    }>();
    expect(applied.results[0].n).toBe(env.TEST_MIGRATIONS.length);
  });

  it("writes a marker and sees no other file's marker", async () => {
    await insertRun(env.DB, {
      run_id: OWN_MARKER,
      project: "isolation-guard",
      epic: "uhe",
      base_sha: "a".repeat(40),
      requested_by: "operator@example.com",
      state: "running",
      started_at: "2026-10-07T12:00:00.000Z",
      ended_at: null,
      cost_usd: 0,
      cost_source: null,
      trace_id: null,
      credential_grade: "write",
    });

    // insertRun is also the schema assertion: it fails on any column the
    // deployed migrations no longer provide.
    const visible = await env.DB.prepare(
      "SELECT run_id FROM runs WHERE run_id LIKE 'run-d1-isolation-%' ORDER BY run_id",
    ).all<{ run_id: string }>();

    expect(visible.results.map(({ run_id }) => run_id)).toEqual([OWN_MARKER]);
  });
});
