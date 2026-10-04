/**
 * The staging GATEWAY Worker (epic 43y, tick oq4): the factory's model path —
 * `proxyModelRequest`, the run token table, the kill switch — alone, so the
 * harness's gateway provider (harness/src/gateway/workers-ai.ts) can be proved
 * against the real route without the production factory.
 *
 * What it proves, with `harness/proof/gateway-staging.ts` driving it:
 *
 * - a pi-durable conversation on the provider reaches Workers AI through
 *   `/api/gateway/workers-ai/v1` on a run token this deployment minted;
 * - the attribution on the AI Gateway's own log for each request is the run's
 *   (`cf-aig-metadata`, stamped from the token's row), read back from the
 *   gateway, not from what the route sent;
 * - revoking the run's tokens refuses the next request.
 *
 * Everything on the model path is the production code. The one difference is
 * the last hop: production reaches the operator's AI Gateway over HTTPS with
 * the account API token, and staging reaches an AI Gateway through this
 * Worker's AI binding (`env.AI.fetch`, pre-authenticated in-account), because
 * no credential this host holds can open an AI Gateway over HTTPS (spike n0b
 * round 2, pre-existing problem 2). The binding is also how the proof reads
 * each request's gateway log back (`env.AI.gateway(id).getLog`).
 *
 * Routes:
 * - `/api/gateway/*` — the run's model path, authenticated by the run token.
 * - `/proof/*` — staging-only controls, behind `PROOF_TOKEN`: mint a run and
 *   a worker token, revoke the run's tokens, read what reached the gateway.
 */

import { getRun, insertRun } from "./db";
import { issueWorkerRunToken, proxyModelRequest, revokeRunTokens } from "./gateway";
import type { Env } from "./index";

/** The AI binding's passthrough fetch, present at runtime, not yet in workers-types. */
type AiWithFetch = Ai & {
  fetch?: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
};

type StagingGatewayEnv = Env & {
  AI: AiWithFetch;
  /** The bearer the /proof/* controls answer to. No token configured is a closed door. */
  PROOF_TOKEN?: string;
  /** The AI Gateway the binding routes through (the last segment of AI_GATEWAY_BASE_URL). */
  PROOF_AI_GATEWAY_ID?: string;
};

/** Constant-time enough for a staging token: lengths first, then every byte. */
function sameToken(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

/** What reached the AI Gateway for one proxied request, recorded by the last hop. */
const SEEN_TABLE = `CREATE TABLE IF NOT EXISTS proof_seen (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  path TEXT NOT NULL,
  sent_metadata TEXT,
  status INTEGER NOT NULL,
  log_id TEXT
)`;

/**
 * The last hop: what production does over HTTPS to the AI Gateway, done here
 * through the AI binding. The credentials the route attached for the HTTPS
 * gateway (the account token as `authorization` and `cf-aig-authorization`)
 * are dropped, because the binding carries the account's identity and a
 * request-supplied `authorization` would be read as a BYOK provider key.
 * Everything the route stamped — `cf-aig-metadata`, the session affinity — goes
 * through unchanged, which is what the proof is about.
 */
function bindingUpstream(env: StagingGatewayEnv): typeof fetch {
  return async (input, init) => {
    const request = new Request(input, init);
    const headers = new Headers(request.headers);
    headers.delete("authorization");
    headers.delete("cf-aig-authorization");
    const binding = env.AI.fetch;
    if (typeof binding !== "function") {
      return Response.json(
        { error: "no_ai_binding_fetch", detail: "this workerd's AI binding has no fetch()" },
        { status: 502 },
      );
    }
    const response = await binding.call(env.AI, request.url, {
      method: request.method,
      headers,
      body: request.body,
      // A streaming request body must not be buffered on its way through.
      ...(request.body === null ? {} : { duplex: "half" }),
    } as RequestInit);
    await env.DB.exec(SEEN_TABLE.replace(/\s+/g, " "));
    await env.DB.prepare(
      "INSERT INTO proof_seen (at, path, sent_metadata, status, log_id) VALUES (?, ?, ?, ?, ?)",
    )
      .bind(
        new Date().toISOString(),
        new URL(request.url).pathname,
        headers.get("cf-aig-metadata"),
        response.status,
        response.headers.get("cf-aig-log-id"),
      )
      .run();
    return response;
  };
}

/** The /proof/* controls. */
async function proofRoute(
  request: Request,
  env: StagingGatewayEnv,
  verb: string,
  url: URL,
): Promise<Response> {
  const token = env.PROOF_TOKEN ?? "";
  const given = (request.headers.get("authorization") ?? "").replace(/^Bearer\s+/i, "");
  if (token === "" || !sameToken(given, token))
    return new Response("unauthorized", { status: 401 });

  switch (verb) {
    case "run": {
      // A run row in a spendable state and one worker token for it: exactly
      // what a run's boot does before a worker's first model call.
      if (request.method !== "POST") return new Response("POST", { status: 405 });
      const runId = url.searchParams.get("run_id") ?? `run_proof_${Date.now().toString(36)}`;
      const tickId = url.searchParams.get("tick_id") ?? "oq4";
      if ((await getRun(env.DB, runId)) === null) {
        await insertRun(env.DB, {
          run_id: runId,
          project: "staging-gateway-proof",
          epic: "43y",
          base_sha: "0000000000000000000000000000000000000000",
          requested_by: "staging-gateway-proof",
          state: "running",
          started_at: new Date().toISOString(),
          ended_at: null,
          cost_usd: 0,
          trace_id: `trace_${runId}`,
          credential_grade: "read_only",
        });
      }
      const issued = await issueWorkerRunToken(env, { run_id: runId, tick_id: tickId, attempt: 1 });
      return Response.json({ run_id: runId, tick_id: tickId, token: issued.token });
    }
    case "revoke": {
      if (request.method !== "POST") return new Response("POST", { status: 405 });
      const runId = url.searchParams.get("run_id") ?? "";
      const reason = url.searchParams.get("reason") ?? "staging proof: kill switch";
      return Response.json({ revoked: await revokeRunTokens(env, runId, reason) });
    }
    case "seen": {
      // Every request that reached the gateway since `after`, with the AI
      // Gateway's OWN log of it: the attribution the gateway recorded, not the
      // header this Worker sent.
      await env.DB.exec(SEEN_TABLE.replace(/\s+/g, " "));
      const after = Number(url.searchParams.get("after") ?? "0");
      const rows = await env.DB.prepare(
        "SELECT seq, at, path, sent_metadata, status, log_id FROM proof_seen WHERE seq > ? ORDER BY seq",
      )
        .bind(after)
        .all<{
          seq: number;
          at: string;
          path: string;
          sent_metadata: string | null;
          status: number;
          log_id: string | null;
        }>();
      const gatewayId = env.PROOF_AI_GATEWAY_ID ?? "";
      const seen = [];
      for (const row of rows.results) {
        let logged: unknown = null;
        if (row.log_id !== null && gatewayId !== "") {
          try {
            const log = (await env.AI.gateway(gatewayId).getLog(row.log_id)) as unknown as Record<
              string,
              unknown
            >;
            logged = {
              metadata: log.metadata ?? null,
              model: log.model ?? null,
              provider: log.provider ?? null,
              status_code: log.status_code ?? null,
              tokens_in: log.tokens_in ?? null,
              tokens_out: log.tokens_out ?? null,
            };
          } catch (error) {
            logged = { error: String(error) };
          }
        }
        seen.push({ ...row, gateway_log: logged });
      }
      return Response.json({ seen });
    }
    default:
      return new Response("not found", { status: 404 });
  }
}

export async function stagingGatewayFetch(
  request: Request,
  env: StagingGatewayEnv,
): Promise<Response> {
  const url = new URL(request.url);
  const segments = url.pathname.split("/").filter((segment) => segment !== "");
  if (segments[0] === "api" && segments[1] === "gateway") {
    return await proxyModelRequest(env, request, segments.slice(2), {
      fetcher: bindingUpstream(env),
    });
  }
  if (segments[0] === "proof" && segments.length === 2) {
    return await proofRoute(request, env, segments[1] as string, url);
  }
  return new Response("not found", { status: 404 });
}

export default {
  fetch: (request: Request, env: StagingGatewayEnv) => stagingGatewayFetch(request, env),
};
