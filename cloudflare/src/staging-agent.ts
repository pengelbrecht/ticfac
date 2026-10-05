/**
 * The staging AGENT Worker (epic 43y, tick xd3): the WorkerAgent Durable
 * Object on the real platform — its own DO SQLite, a real FactorySandbox
 * container on the durable_object policy, real Workers AI through the
 * factory's own gateway route — so a cloud attempt can be run end to end on
 * staging without the production factory (staging/agent.wrangler.toml).
 *
 * Everything on the attempt's path is production code: the WorkerAgent, the
 * harness package's attempt host, pi-durable, the FactorySandbox class, the
 * gateway route with its run token and kill switch. Two things stand in:
 *
 * - **The last model hop** goes through this Worker's AI binding instead of
 *   HTTPS to the operator's AI Gateway — the staging gateway's one difference
 *   (src/staging-gateway.ts `bindingUpstream`), for its reason: no credential
 *   this host holds can open an AI Gateway over HTTPS.
 * - **The container's half of the contract** is a stand-in `ticks-worker`
 *   (staging/agent.Dockerfile) that honours the pinned `--boot`/`--finish`
 *   contract on a throwaway repository inside the container: the factory
 *   image needs a deploy pipeline, a GitHub repository and the tk toolchain
 *   that staging does not carry (spike n0b round 2, pre-existing problem 1).
 *
 * Routes, all behind `PROOF_TOKEN` (no token configured is a closed door):
 * - `POST /proof/start?name=<attempt>` `{prompt, model?, wall_s?}` — a run row,
 *   its worker token, and the attempt started on its WorkerAgent;
 * - `GET  /proof/state?name=` / `GET /proof/log?name=&offset=`;
 * - `POST /proof/steer?name=` `{text}`; `GET /proof/watch?name=` (WebSocket);
 * - `POST /proof/reclaim?name=`;
 * - `POST /proof/exec?name=` `{command}` — one short command in the
 *   attempt's container, for the evidence (the branch the finish pushed).
 */

import { getRun, insertRun } from "./db";
import { FactorySandbox, type FactorySandboxNamespace } from "./factory-sandbox";
import type { ProxyOptions } from "./gateway";
import { issueWorkerRunToken } from "./gateway";
import { DO_V1, recordRunSubstrate } from "./run-substrate";
import { bindingUpstream, type StagingGatewayEnv } from "./staging-gateway";
import { WorkerAgent as BaseWorkerAgent, type WorkerAgentNamespace } from "./worker-agent";

type StagingAgentEnv = StagingGatewayEnv & {
  WORKER_AGENTS?: WorkerAgentNamespace;
};

/** The WorkerAgent, its last model hop through the AI binding. */
export class StagingWorkerAgent extends BaseWorkerAgent {
  protected override gatewayProxyOptions(): ProxyOptions {
    return { fetcher: bindingUpstream(this.env as StagingAgentEnv) };
  }
}

export { FactorySandbox };

/** Constant-time enough for a staging token: lengths first, then every byte. */
function sameToken(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

/**
 * The run and the container a proof attempt named `name` runs under. The
 * container name is `<run>-<tick>-<n>`, the production shape: since tick hxd
 * the WorkerAgent finds its door by the substrate row of the run PARSED from
 * that name (runIDOfSandboxName), so a name that does not parse back to the
 * run whose row this Worker wrote is routed to the sdk0 door staging does
 * not bind. The run id carries no dash, so the parse is exact.
 */
export function proofAttempt(name: string): { runId: string; sandbox: string } {
  const runId = `run_proof_${name.replaceAll("-", "_")}`;
  return { runId, sandbox: `${runId}-xd3-1` };
}

async function body(request: Request): Promise<Record<string, unknown>> {
  try {
    const parsed = (await request.json()) as unknown;
    return parsed !== null && typeof parsed === "object" ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

export async function stagingAgentFetch(request: Request, env: StagingAgentEnv): Promise<Response> {
  const url = new URL(request.url);
  const token = env.PROOF_TOKEN ?? "";
  const given = (request.headers.get("authorization") ?? "").replace(/^Bearer\s+/i, "");
  if (token === "" || !sameToken(given, token))
    return new Response("unauthorized", { status: 401 });
  const agents = env.WORKER_AGENTS;
  const sandboxes = env.SANDBOXES_V1 as FactorySandboxNamespace | undefined;
  if (agents === undefined || sandboxes === undefined) {
    return new Response("no WORKER_AGENTS / SANDBOXES_V1", { status: 503 });
  }
  const match = /^\/proof\/([a-z]+)$/.exec(url.pathname);
  const name = url.searchParams.get("name") ?? "";
  if (match === null || !/^[a-z0-9-]{1,63}$/.test(name)) {
    return new Response("not found", { status: 404 });
  }
  const agent = agents.get(agents.idFromName(name));
  switch (match[1]) {
    case "start": {
      if (request.method !== "POST") return new Response("POST", { status: 405 });
      const input = await body(request);
      const prompt = typeof input.prompt === "string" ? input.prompt : "";
      if (prompt.trim() === "") return new Response("a prompt", { status: 400 });
      const model =
        typeof input.model === "string" ? input.model : "workers-ai/@cf/zai-org/glm-5.3";
      const { runId, sandbox } = proofAttempt(name);
      if ((await getRun(env.DB, runId)) === null) {
        await insertRun(env.DB, {
          run_id: runId,
          project: "staging-agent-proof",
          epic: "43y",
          base_sha: "0000000000000000000000000000000000000000",
          requested_by: "staging-agent-proof",
          state: "running",
          started_at: new Date().toISOString(),
          ended_at: null,
          cost_usd: 0,
          trace_id: `trace_${runId}`,
          credential_grade: "read_only",
        });
        // The proof's containers ARE FactorySandbox ones (SANDBOXES_V1,
        // the durable_object policy): the run is on `do_v1`, and the
        // attempt's door routes there (tick hxd — before it, every run was
        // hosted whose record said do_v1, so a row nobody read was free to
        // be missing; now the row is what the door is keyed on).
        await recordRunSubstrate(env.DB, runId, DO_V1);
      }
      const issued = await issueWorkerRunToken(env, { run_id: runId, tick_id: "xd3", attempt: 1 });
      const state = await agent.start({
        name: sandbox,
        tick: "xd3",
        role: "implement-tick",
        env: {
          TICKS_TICK: "xd3",
          TICKS_RUN_ID: runId,
          TICKS_WORKDIR: "/work/repo",
          TICKS_WORKER_STATE_DIR: "/tmp/ticks-worker",
          TICKS_ROLE_PROMPT: prompt,
          AI_GATEWAY_BASE_URL: `${url.origin}/api/gateway`,
          AI_GATEWAY_TOKEN: issued.token,
        },
        model,
        repoUrl: "/srv/origin.git",
        baseSha: "0000000000000000000000000000000000000000",
        ...(typeof input.wall_s === "number" ? { wallMs: input.wall_s * 1000 } : {}),
        boot: { keepAlive: true, instance: "standard-1" },
      });
      return Response.json({ run_id: runId, state });
    }
    case "state":
      return Response.json(await agent.state());
    case "log":
      return Response.json(await agent.readLog(Number(url.searchParams.get("offset") ?? "0")));
    case "steer": {
      const input = await body(request);
      return Response.json(await agent.steer(typeof input.text === "string" ? input.text : ""));
    }
    case "watch":
      return agent.fetch(request);
    case "reclaim":
      return Response.json(await agent.reclaim("staging proof: reclaim"));
    case "exec": {
      const input = await body(request);
      const command = typeof input.command === "string" ? input.command : "";
      const stub = sandboxes.get(sandboxes.idFromName(proofAttempt(name).sandbox));
      return Response.json(await stub.run(command, {}, { readyWaitMs: 5_000 }));
    }
    default:
      return new Response("not found", { status: 404 });
  }
}

export default {
  fetch: (request: Request, env: StagingAgentEnv) => stagingAgentFetch(request, env),
};
