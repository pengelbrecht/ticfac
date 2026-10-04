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

# Staging proof: a cloud worker attempt on its WorkerAgent (tick xd3)

`agent-staging.ts` runs ONE worker attempt end to end on the real platform:
the `WorkerAgent` Durable Object (`cloudflare/src/worker-agent.ts`) on its own
DO SQLite, its tools in a real FactorySandbox container on the
`durable_object` policy, its model Workers AI GLM 5.3 through the factory's
own gateway route (`proxyModelRequest`, the run token, the kill switch). It is
not part of CI: it spends (cents of Workers AI and container time) and needs a
deployed Worker.

Two stand-ins, both named in `cloudflare/src/staging-agent.ts`: the last model
hop goes through the Worker's AI binding (the staging gateway's one difference,
above), and the container's `ticks-worker` is a stand-in
(`cloudflare/staging/agent.Dockerfile`) that honours the pinned
`--boot`/`--finish` contract — markers, prompt handoff, branch record, the
fast-forward-only push, exit codes — on a throwaway repository inside the
container, because the factory image needs the deploy pipeline, a GitHub
repository and tk, which staging does not carry.

## Running it

From `cloudflare/` (wrangler logged in; the deploy provisions the D1 database by
name, so no id is committed):

```bash
pnpm exec wrangler deploy -c staging/agent.wrangler.toml
pnpm exec wrangler d1 migrations apply ticfac-staging-agent --remote -c staging/agent.wrangler.toml
openssl rand -hex 24 | pnpm exec wrangler secret put PROOF_TOKEN -c staging/agent.wrangler.toml
```

From `harness/`:

```bash
PROOF_URL=https://ticfac-staging-agent.<subdomain>.workers.dev PROOF_TOKEN=<the token> \
  node proof/agent-staging.ts evidence.json
```

Tear down afterwards:

```bash
pnpm exec wrangler delete -c staging/agent.wrangler.toml
pnpm exec wrangler d1 delete ticfac-staging-agent
```

## Recorded result, 2026-10-04 (pi-ai / pi-durable 1.0.2)

`PROOF HOLDS`, every claim, in 39 s (attempt `proof-muu5bo90`): boot 9.9 s,
conversation 26 s (6 tool calls), finish 3 s.

| Claim | Evidence |
|---|---|
| The attempt settled with the finish phase's exit 0 | the agent's state: `settled`, `exit_code: 0`, "the finish phase exited 0"; the stand-in's fast-forward push `pushed tick/proof/xd3 at 446590c` |
| The boot ran in the container and handed off | `ticks-worker-boot-ok branch=tick/proof/xd3 result=RESULT-xd3.md`, then the prompt between the markers, submitted as the conversation's input |
| The model ran its tools in the container | `bash`, `edit`, `read`, `write` calls in the agent's log; `./greet.sh` in the container prints `Hello, world` |
| The work is on the pushed branch | `main..tick/proof/xd3`: the agent's two commits and the finish's report commit; the fix and `RESULT-xd3.md` (`STATUS: DONE`) read back from the bare origin |
| Wip checkpoints were pushed after tool rounds, invisibly | four pushes (three snapshots, one clean round pushing the agent's own commit `9785c02`); the agent's own `git commit`s succeeded and the final branch carries no wip |
| A watcher saw the conversation live | 126 frames on the agent's WebSocket: a `snapshot`, then `message_*`, `tool_execution_*`, `turn_*`, `submission`, `inbox_update`, `run_end` events and the log |
| A steer was placed while it worked | sent on the socket at the first `tool_execution_start` (0.2 s after it), answered `steered`; the model appended the steered line to `README.md` and committed it |

What the staging runs FOUND, both fixed in this tick with a regression test
each (`test/node/workspace-checkpoints.test.ts`):

- **The host shell's git ran in the container's `/workspace`, not the
  checkout** (`/work/repo`): every wip checkpoint failed "not in a git
  directory", and a restore would have cleared and cloned the wrong
  directory. The node suite's door had started in the checkout, which hid it.
- **A wip COMMITTED on the agent's branch broke the agent's own git**: its
  `git commit` answered "nothing to commit", the model rewrote the history it
  could not explain (`git reset --soft HEAD^`), and every push after — the
  finish phase's fast-forward-only one included — was refused (exit 9). The
  checkpoint is now a snapshot built in a throwaway index on top of the
  agent's HEAD, never a commit on its branch.

Not proven here, and not claimed: a deploy mid-run (one was made 10 s into the
conversation of a further attempt, which settled 0 two seconds after the
deploy finished, with no restart observable in its log), a harness killed
mid-tool, a container destroyed mid-turn — epic 43y's [A2], tick jhp's proof
runs.
