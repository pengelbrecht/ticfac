import { env, SELF } from "cloudflare:test";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { deriveTokenHash, mintFactoryToken } from "../src/auth";
import { listStatusSnapshots } from "../src/status";

/**
 * The snapshot door (tick i1r): `POST /api/status-snapshots`, the one seam
 * between the Go half of "follow ticfac from a phone" (the pusher inside
 * `ticfac run-epic`) and the TS half (the store, the page, the alerts).
 *
 * The contract pinned here is the one the Go pusher builds to: the envelope
 * version, the model's own `ticfac.status.v1` version, a local host, and the
 * run identity the envelope names matching the one its model carries. These
 * run against real workerd with the real D1 tables, so what is proven is the
 * door the binary talks to, not a mock of it.
 */

const BASE = "https://factory.example.com";

let token: string;
const originalHash = env.FACTORY_TOKEN_HASH;

beforeAll(async () => {
  token = mintFactoryToken();
  env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
});

afterAll(() => {
  if (originalHash === undefined) delete env.FACTORY_TOKEN_HASH;
  else env.FACTORY_TOKEN_HASH = originalHash;
});

afterEach(async () => {
  await env.DB.prepare("DELETE FROM status_snapshots").run();
  await env.DB.prepare("DELETE FROM status_alerts").run();
});

const auth = (): Record<string, string> => ({ Authorization: `Bearer ${token}` });

function post(path: string, body: unknown, headers: Record<string, string> = auth()) {
  return SELF.fetch(`${BASE}${path}`, {
    method: "POST",
    headers,
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

/** One `ticfac.status.v1` document as the Go side emits it. */
function localDoc(runID: string, epicID: string, extra: Record<string, unknown> = {}) {
  return {
    schema_version: 1,
    run_id: runID,
    epic_id: epicID,
    host: "local",
    generated_at: new Date().toISOString(),
    degraded: [],
    liveness: { alive: true, state: "alive", reason: "", source: "run.pid" },
    lifecycle: { phase: "waves", phases: [], wave: { active: 1, total: 2 } },
    progress: { ticks: { total: 4, closed: 2, open: 2 }, waves: { total: 2, done: 1, active: 1 } },
    waves: [
      {
        wave: 1,
        state: "active",
        ticks: [{ tick_id: "i1r", title: "Follow ticfac from a phone", state: "in-flight" }],
      },
    ],
    workers: null,
    waits_on: { kind: "workers", what: "2 in-flight attempt(s)", needs_person: false },
    attention: [],
    health: { remote_retries: 0, interventions: 0, stall_warnings: 0, wall_clocks_fired: 0 },
    gates: [],
    ci: null,
    cost: { recorded_usd: 0, attempts: 3, basis: "model exchanges only" },
    remaining: null,
    ...extra,
  };
}

function envelope(
  runID: string,
  doc: Record<string, unknown>,
  extra: Record<string, unknown> = {},
) {
  return {
    schema_version: 1,
    run_id: runID,
    host: "local",
    pushed_at: new Date().toISOString(),
    model: doc,
    ...extra,
  };
}

describe("the snapshot door is an authenticated route", () => {
  it("refuses a request without the operator's token", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-2jn", "2jn")),
      {},
    );
    expect(res.status).toBe(401);
    await expect(res.json()).resolves.toEqual({ error: "unauthorized" });
  });

  it("refuses a wrong token", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-2jn", "2jn")),
      {
        Authorization: `Bearer ${mintFactoryToken()}`,
      },
    );
    expect(res.status).toBe(401);
  });
});

describe("a valid snapshot is stored", () => {
  it("answers 201 and stores the model and the tick labels", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-2jn", "2jn"), {
        tick_labels: { i1r: "Follow ticfac from a phone" },
      }),
    );
    expect(res.status).toBe(201);
    const body = (await res.json()) as { stored: boolean; run_id: string };
    expect(body).toMatchObject({ stored: true, run_id: "epic-2jn" });

    const rows = await listStatusSnapshots(env.DB);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.run_id).toBe("epic-2jn");
    expect(rows[0]!.epic_id).toBe("2jn");
    expect(rows[0]!.host).toBe("local");
    expect(rows[0]!.model.liveness.alive).toBe(true);
    expect(rows[0]!.tick_labels).toEqual({ i1r: "Follow ticfac from a phone" });
  });

  it("keeps ONE row per run: a newer push replaces the older snapshot", async () => {
    const first = new Date(Date.now() - 60_000).toISOString();
    const second = new Date().toISOString();
    await post("/api/status-snapshots", {
      ...envelope("epic-2jn", localDoc("epic-2jn", "2jn")),
      pushed_at: first,
    });
    await post("/api/status-snapshots", {
      ...envelope("epic-2jn", localDoc("epic-2jn", "2jn")),
      pushed_at: second,
    });

    const rows = await listStatusSnapshots(env.DB);
    expect(rows).toHaveLength(1);
    expect(rows[0]!.pushed_at).toBe(second);
  });
});

describe("the envelope contract the Go pusher builds to", () => {
  it("refuses an unknown envelope version rather than guessing", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-2jn", "2jn"), { schema_version: 2 }),
    );
    expect(res.status).toBe(422);
    const body = (await res.json()) as { error: string };
    expect(body.error).toBe("unsupported_version");
  });

  it("refuses an unknown model schema version rather than guessing", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-2jn", "2jn", { schema_version: 2 })),
    );
    expect(res.status).toBe(422);
    const body = (await res.json()) as { error: string };
    expect(body.error).toBe("unsupported_version");
  });

  it("refuses a cloud host: a cloud run needs no push, the factory composes it", async () => {
    const res = await post("/api/status-snapshots", {
      ...envelope("run_ab12", localDoc("run_ab12", "2jn")),
      host: "cloud",
    });
    expect(res.status).toBe(400);
  });

  it("refuses an envelope whose model names another run", async () => {
    const res = await post(
      "/api/status-snapshots",
      envelope("epic-2jn", localDoc("epic-other", "other")),
    );
    expect(res.status).toBe(400);
  });

  it("refuses an empty run id", async () => {
    const res = await post("/api/status-snapshots", envelope("", localDoc("", "2jn")));
    expect(res.status).toBe(400);
  });

  it("refuses a body that is not JSON", async () => {
    const res = await post("/api/status-snapshots", "not json at all", {
      ...auth(),
      "Content-Type": "application/json",
    });
    expect(res.status).toBe(400);
  });

  it("refuses a snapshot over the size bound", async () => {
    const doc = localDoc("epic-2jn", "2jn", { pad: "a".repeat(256 * 1024) });
    const res = await post("/api/status-snapshots", envelope("epic-2jn", doc));
    expect(res.status).toBe(413);
  });

  it("is POST only", async () => {
    const res = await SELF.fetch(`${BASE}/api/status-snapshots`, { headers: auth() });
    expect(res.status).toBe(405);
  });
});
