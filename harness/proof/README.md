# Staging proof: the Workers AI gateway provider (tick oq4)

`gateway-staging.ts` proves `workersAIGatewayProvider`
(`src/gateway/workers-ai.ts`) against the factory's real model path, deployed
on its own as the staging gateway Worker (`cloudflare/src/staging-gateway.ts`,
`cloudflare/staging/gateway.wrangler.toml`). It is not part of CI: it spends
(a few cents of Workers AI) and needs a deployed Worker.

## What runs where

```
 this host: node proof/gateway-staging.ts
   pi-durable Harness ── workersAIGatewayProvider(gateway, run token)
     │  HTTPS, Authorization: Bearer <run token>
     ▼
 staging gateway Worker: /api/gateway/workers-ai/v1/chat/completions
   proxyModelRequest (production code): token row → run row → cf-aig-metadata,
   x-session-affinity, kill switch; workers-ai content-part rewrite
     │  last hop: env.AI.fetch (the one staging-only difference, see below)
     ▼
 AI Gateway "default" ──▶ Workers AI @cf/zai-org/glm-5.3
```

Everything on the model path is the production code, run tokens and
revocation included (`issueWorkerRunToken`, `revokeRunTokens`, the factory's
own D1 migrations). Production reaches the operator's AI Gateway over HTTPS
with the account API token; staging reaches an AI Gateway through the Worker's
AI binding, because no credential this host holds can open an AI Gateway over
HTTPS (spike n0b round 2, pre-existing problem 2). The binding is also how the
proof reads each request's AI Gateway log back (`env.AI.gateway(id).getLog`),
so the attribution checked is the one the gateway RECORDED, not the header the
route sent.

## Running it

From `cloudflare/` (wrangler logged in to the account; the deploy provisions
the D1 database by name, so no id is committed):

```bash
pnpm exec wrangler deploy -c staging/gateway.wrangler.toml
pnpm exec wrangler d1 migrations apply ticfac-staging-gateway --remote -c staging/gateway.wrangler.toml
openssl rand -hex 24 | pnpm exec wrangler secret put PROOF_TOKEN -c staging/gateway.wrangler.toml
```

From `harness/` (Node 24, which runs the TypeScript directly):

```bash
PROOF_URL=https://ticfac-staging-gateway.<subdomain>.workers.dev PROOF_TOKEN=<the token> \
  node proof/gateway-staging.ts evidence.json
```

It exits 0 when every claim holds and 1 naming the ones that do not. Tear down
afterwards — the Worker answers model traffic for any run token its D1 holds:

```bash
pnpm exec wrangler delete -c staging/gateway.wrangler.toml
pnpm exec wrangler d1 delete ticfac-staging-gateway
```

## Recorded result, 2026-10-04 (pi-ai / pi-durable 1.0.2)

`PROOF HOLDS`, every claim (run `run_proof_mutw7mg3`, tick `oq4`):

| Claim | Evidence |
|---|---|
| A pi-durable conversation with a tool round completes through the provider on the live token | turn 1 `done`; the echo tool ran; 2 requests (one each side of the tool round), both 200 |
| Every request went to the gateway's route on the run token, claiming nothing | path `/api/gateway/workers-ai/v1/chat/completions`; `Authorization: Bearer <run token>`; no `cf-*` header from the harness |
| The GLM overrides are on the wire | `max_completion_tokens: 65536`, `thinking: {"type":"enabled"}`, `reasoning_effort: "high"` on every request |
| **Attribution holds**: the AI Gateway's own log attributes each request to the run | both logs read back via the binding: `metadata {run_id: run_proof_mutw7mg3, tick_id: oq4, project, epic: 43y, attempt: 1}`, provider `workers-ai`, model `@cf/zai-org/glm-5.3`, status 200 (180→13 and 200→5 tokens) |
| **A revoked token stops the next request** | `revokeRunTokens` between turns (1 row); turn 2's one request answered `403 {"error":"run_token_revoked",…}`; the input settled `unanswered`, reason `model_error`, detail `403 "run_token_revoked"` |
| The refusal is final, not retried | exactly one request after the revocation, with pi-durable's retry policy on (2 retries) |
| The refused request never reached the AI Gateway | the gateway saw exactly the 2 live requests |

Two more things the same deployment showed:

- **Workers AI accepts every GLM 5.3 effort.** `reasoning_effort` `low`,
  `medium`, `high`, `max` and `none` with `thinking: {"type":"enabled"}` all
  answered 200. pi-ai's catalog marks `medium` unsupported, so pi-ai clamps a
  `medium` thinking level UP to `high` before the request is built (pinned by
  the provider's unit test): that is why the pi CLI recorded `high` for
  `--thinking medium` (epic 43y, note 6). It is the catalog, not the endpoint.
- **The AI Gateway keeps only five metadata entries.** The route stamps six
  (`run_id`, `tick_id`, `project`, `epic`, `attempt`, `trace_id`); every log
  read back carried the first five and no `trace_id`. A gateway-log query by
  trace id therefore matches nothing. Reported as a finding of tick oq4.

The staging Worker and its D1 database were deleted after the proof.
