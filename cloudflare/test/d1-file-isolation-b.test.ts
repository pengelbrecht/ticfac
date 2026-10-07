import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { insertRun } from "../src/db";

// The other half of the per-file D1 isolation guard — read
// d1-file-isolation-a.test.ts first; this file is deliberately its mirror.
// The assertions are the same from the other side so the guard fails in
// EITHER execution order: whichever of the two files runs second in a
// runtime that shares a D1 between files sees the other's marker row here.
const OWN_MARKER = "run-d1-isolation-b";

describe("per-file D1 isolation (file b)", () => {
  it("sees the whole deploy migration set applied", async () => {
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
      base_sha: "b".repeat(40),
      requested_by: "operator@example.com",
      state: "running",
      started_at: "2026-10-07T12:00:00.000Z",
      ended_at: null,
      cost_usd: 0,
      cost_source: null,
      trace_id: null,
      credential_grade: "write",
    });

    const visible = await env.DB.prepare(
      "SELECT run_id FROM runs WHERE run_id LIKE 'run-d1-isolation-%' ORDER BY run_id",
    ).all<{ run_id: string }>();

    expect(visible.results.map(({ run_id }) => run_id)).toEqual([OWN_MARKER]);
  });
});
