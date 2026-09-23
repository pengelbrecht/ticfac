import { env, SELF } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import { DONE_PATH } from "../src/auth";
import { insertRun, type Run } from "../src/db";
import { issueRunToken, revokeRunTokens } from "../src/gateway";
import {
  DONE_EVENT_PAYLOAD_CAP_BYTES,
  DONE_EVENT_TYPE,
  EVENT_TYPE_PATTERN,
  readDoneSignal,
  signalRunDone,
} from "../src/run-done";
import { MIN_EVENT_WAIT_MS } from "../src/run-workflow";

/**
 * The completion door (tick 7eq): a finished orchestrator POSTs here, the
 * Worker calls `instance.sendEvent()`, and the Run Workflow's wait returns
 * without polling.
 *
 * These cover the DOOR: who may knock, what may be carried, and what happens
 * when delivery cannot happen. The wake itself — the Workflow's half, including
 * the case the acceptance names as the other half (a callback that never
 * lands) — is proved in run-workflow.test.ts, which has the sandbox harness.
 *
 * The platform facts this module rests on are pinned here rather than trusted,
 * because each of them fails silently if violated: an event type with a dot
 * makes `waitForEvent` never fire (back to polling, nothing red), a timeout
 * below the documented floor is a request-shape error on the deployed
 * platform, and an oversize payload is refused by `sendEvent` with an error
 * nobody asked for.
 */

const PROJECT = "example-org/run-done";
const FACTORY = "https://factory.example.com";

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

let counter = 0;

/** A run in the index, live enough to signal. */
async function liveRun(state = "running"): Promise<{ run: Run; token: string }> {
  const run: Run = {
    run_id: `run_done_${++counter}`,
    project: PROJECT,
    epic: "ko8",
    base_sha: "b".repeat(40),
    requested_by: "operator",
    state,
    started_at: new Date().toISOString(),
    ended_at: null,
    cost_usd: 0,
    trace_id: null,
    credential_grade: "write",
  };
  await insertRun(env.DB, run);
  const { token } = await issueRunToken(env, {
    run_id: run.run_id,
    tick_id: "ko8",
    attempt: 1,
  });
  return { run, token };
}

/** The POST a finished orchestrator makes, exactly as the Go client makes it. */
function doneRequest(
  token: string | null,
  body: string,
  headers: Record<string, string> = {},
): Request {
  return new Request(`${FACTORY}${DONE_PATH}`, {
    method: "POST",
    headers: {
      ...(token === null ? {} : { authorization: `Bearer ${token}` }),
      "content-type": "application/json",
      ...headers,
    },
    body,
  });
}

// ------------------------------------------- the platform facts, pinned ---

describe("the completion event, against the platform's documented shape", () => {
  it("uses an event type the platform accepts — no dots", () => {
    // ^[a-zA-Z0-9_][a-zA-Z0-9-_]*$: an out-of-vocabulary type makes
    // waitForEvent never fire, which silently degrades the signal back into
    // polling — the failure this pin exists to make loud.
    expect(DONE_EVENT_TYPE).toMatch(EVENT_TYPE_PATTERN);
    expect(DONE_EVENT_TYPE).not.toContain(".");
  });

  it("keeps the event timeout at or above the documented floor", () => {
    // Timeouts are settable between 1 second and 365 days; below the floor is
    // a request-shape error on the deployed platform. The supervisor floors
    // its waits at this number and falls back to a plain sleep below it.
    expect(MIN_EVENT_WAIT_MS).toBe(1_000);
  });

  it("refuses a body over the 1 MiB event payload cap", async () => {
    const { token } = await liveRun();
    // No giant string is built: the door refuses on the declared length, which
    // is what the Go client (and every HTTP client with a body) states.
    const response = await SELF.fetch(
      doneRequest(token, "x", { "content-length": String(DONE_EVENT_PAYLOAD_CAP_BYTES + 1) }),
    );
    expect(response.status).toBe(413);
  });

  it("reads the payload shape defensively — a Workflow step never throws on it", () => {
    expect(readDoneSignal({ branch: "epic/ko8", head: "a".repeat(40) })).toEqual({
      branch: "epic/ko8",
      head: "a".repeat(40),
    });
    // A head of "" is absent, not present-and-empty.
    expect(readDoneSignal({ branch: "epic/ko8", head: "" })).toEqual({ branch: "epic/ko8" });
    // Anything unreadable is a lost optimisation, never a lost run.
    expect(readDoneSignal(null)).toBeNull();
    expect(readDoneSignal("done")).toBeNull();
    expect(readDoneSignal({ head: "a".repeat(40) })).toBeNull();
    expect(readDoneSignal({ branch: 7 })).toBeNull();
  });
});

// ------------------------------------------------------------- the door ---

describe("who may knock", () => {
  it("refuses a request with no run credential", async () => {
    const response = await SELF.fetch(doneRequest(null, '{"branch":"epic/ko8"}'));
    expect(response.status).toBe(401);
  });

  it("refuses a credential no run ever held", async () => {
    const response = await SELF.fetch(
      doneRequest("tkr_nobody_ever_minted_this", '{"branch":"epic/ko8"}'),
    );
    expect(response.status).toBe(401);
  });

  it("refuses a revoked run — an operator's stop reaches the signal too", async () => {
    const { run, token } = await liveRun();
    await revokeRunTokens(env, run.run_id, "stopped:hard");
    const response = await SELF.fetch(doneRequest(token, '{"branch":"epic/ko8"}'));
    expect(response.status).toBe(403);
    const refusal = (await response.json()) as { error: string };
    expect(refusal.error).toBe("run_token_revoked");
  });

  it("refuses a finished run — there is no supervisor left to wake", async () => {
    const { token } = await liveRun("completed");
    const response = await SELF.fetch(doneRequest(token, '{"branch":"epic/ko8"}'));
    expect(response.status).toBe(403);
    const refusal = (await response.json()) as { error: string };
    expect(refusal.error).toBe("run_not_active");
  });
});

describe("what may be carried", () => {
  it("refuses a body that is not JSON", async () => {
    const { token } = await liveRun();
    const response = await SELF.fetch(doneRequest(token, "not json at all"));
    expect(response.status).toBe(400);
  });

  it("refuses a body with no branch", async () => {
    const { token } = await liveRun();
    const response = await SELF.fetch(doneRequest(token, `{"head":"${"a".repeat(40)}"}`));
    expect(response.status).toBe(400);
  });

  it("refuses a branch that is not a branch name", async () => {
    const { token } = await liveRun();
    for (const branch of ["epic no8", "epic/../ko8", "epic~ko8", ""]) {
      const response = await SELF.fetch(doneRequest(token, JSON.stringify({ branch })));
      expect(response.status, `branch ${JSON.stringify(branch)}`).toBe(400);
    }
  });

  it("refuses a head that is not a full commit", async () => {
    const { token } = await liveRun();
    const response = await SELF.fetch(doneRequest(token, '{"branch":"epic/ko8","head":"short"}'));
    expect(response.status).toBe(400);
  });

  it("accepts a branch with no head — a run that pushed nothing still wakes", async () => {
    const { token } = await liveRun();
    // No Workflow instance exists for this run, so delivery cannot land —
    // which is the case the next block pins.
    const response = await SELF.fetch(doneRequest(token, '{"branch":"epic/ko8"}'));
    expect(response.status).toBe(202);
  });
});

describe("when delivery cannot happen", () => {
  it("answers 202 with delivered:false for a run with no Workflow instance", async () => {
    const { token } = await liveRun();
    const response = await SELF.fetch(
      doneRequest(token, `{"branch":"epic/ko8","head":"${"c".repeat(40)}"}`),
    );
    expect(response.status).toBe(202);
    const answered = (await response.json()) as { delivered: boolean; detail: string };
    // The callback is an optimisation; the branch is the source of truth, so
    // a caller that cannot be delivered to is TOLD so, not refused.
    expect(answered.delivered).toBe(false);
    expect(answered.detail).not.toBe("");
  });

  it("refuses 503 when the deployment has no Run Workflow binding at all", async () => {
    set("RUN_WORKFLOW", undefined);
    const { token } = await liveRun();
    const response = await SELF.fetch(doneRequest(token, '{"branch":"epic/ko8"}'));
    expect(response.status).toBe(503);
  });

  it("is exempt from the factory bearer token, and refuses other methods", async () => {
    // The route-table grade is pinned in phase0-compat; this is the method
    // half: a door is POST-only, and answering GET with 405 rather than 401
    // is what proves the exemption (the auth gate would have said 401 first).
    const response = await SELF.fetch(`${FACTORY}${DONE_PATH}`);
    expect(response.status).toBe(405);
  });

  it("passes the whole request through signalRunDone unchanged in vocabulary", async () => {
    // The route answers with the module's own result; this keeps the route
    // honest without a Workflow: an unauthorized caller through the function
    // gets the same refusal the door serves.
    const refusal = await signalRunDone(env, doneRequest(null, '{"branch":"epic/ko8"}'));
    expect(refusal.ok).toBe(false);
    if (!refusal.ok) expect(refusal.error).toBe("run_token_required");
  });
});
