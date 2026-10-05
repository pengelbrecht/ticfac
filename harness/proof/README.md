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
fast-forward-only push, exit codes — on a throwaway repository that lives
on the staging Worker itself (the `/proof/git` origin, since tick a2l),
because the factory image needs the deploy pipeline, a GitHub repository and
tk, which staging does not carry.

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

# Staging proof: the fault claims of epic 43y's [A2] (tick jhp)

`agent-faults-staging.ts` drives the same staging agent Worker as
`agent-staging.ts` (deployed and torn down the same way, above) and injects
the three faults the epic's acceptance names, one attempt each:

1. **Host lost mid-tool** — a `wrangler deploy` of the staging Worker fired
   at the model's first `tool_execution_start`, a ninety-second bash (a
   staging deploy takes about twenty seconds). The deploy kills the
   WorkerAgent Durable Object while the tracked bash keeps running in its
   container; the next heartbeat's host resumes the conversation from DO
   SQLite and the tracked bash reattaches by its nonce.
2. **Container destroyed mid-turn** — since tick a2l the proof destroys the
   whole container between tool rounds, not just the workspace: the
   attempt's throwaway origin is the staging Worker's own (`/proof/git`, in
   Durable Object storage), so it outlives the box, and the next round's
   ready check boots the replacement, fetches from that origin and checks
   out the last wip checkpoint before the model's next request.
3. **Deploy mid-run** — a deploy fired the moment the boot has handed off
   and the first model request is in flight.

The mid-turn destroy is the [A2] claim it is because the origin OUTLIVES
THE BOX (tick a2l): the container platform has no volumes to put it in
(wrangler 4.147's container config carries no volumes key), so the
repository lives in the `GIT_ORIGINS` Durable Objects and the box reaches
it over the network — git's dumb HTTP protocol, whose wire behaviour is
pinned against real git by the node suite's `dumb-git-origin.test.ts`.

Tear-down also needs the container application, which `wrangler delete`
leaves behind: `pnpm exec wrangler containers list`, then
`pnpm exec wrangler containers delete <id>` for `ticfac-staging-agent-sandbox`.

<<<<<<< HEAD
Since tick umx the "wip checkpoints kept landing after the resume" claim is
checked in order, not read by hand: the checkpoint lines must FOLLOW the
resume line in the log (the proof's log is an append-only stream, so a line's
position in it is its order in time — a checkpoint from before the kill no
longer passes it), the count is recorded in the evidence
(`checkpoints_after_resume`), and the read itself lives in
`proof/fault-claims.ts`, pinned by `test/agent-faults-claims.test.ts`.

## Recorded result, 2026-10-05 (pi-ai / pi-durable 1.0.2)
=======
## Recorded result, 2026-10-05, the whole-container destroy (tick jpy, pi-ai / pi-durable 1.0.2)
>>>>>>> 3cecaa75328174fc5fe4eb4dcae759edd0df7e67

`PROOF HOLDS`, every claim, in 493 s (three attempts, GLM 5.3 on Workers
AI, one pass on a fresh deployment — the second scenario destroys the
whole CONTAINER between rounds, in its post-a2l shape):

| Claim | Evidence |
|---|---|
| Host lost mid-tool: the attempt settles 0 | `settled`, `exit_code: 0`, "the finish phase exited 0"; the deploy fired 13 s into the attempt, at the sleep's start, and finished at 30 s |
| …a new host life resumed the conversation from its storage | the log line `a new host life resumed the conversation from its storage (submission 8)` right after `tool bash: {"command":"sleep 90 && …"}` |
| …the tool did not run twice | `tool-runs.txt` on the pushed branch reads exactly `tool-ran-once` — one line |
| …wip checkpoints kept landing after the resume | three `wip checkpoint … pushed` lines after the resume |
| Container destroyed mid-turn: the attempt settles 0 | `settled`, `exit_code: 0`; the whole container `run_proof_container_destroyed_mid_turn-xd3-1` destroyed 27 s into the attempt, between rounds, after that round's checkpoint had landed on the origin that outlives the box |
| …restored from the last wip commit | `the container was lost between rounds; workspace restored to 63195b25f3fe` — the first round's checkpoint; the second write had landed in the dying box ("not in a git directory"), and the replacement box was booted, fetched from the origin and checked out before the model's next request |
| …the restore's setup ran in the replacement box | `.setup-ran` in the pushed tree — the real apt build-essential + golang install, running only because the restore runs it — committed by the model on the restored tree; the destroy-to-settle span of ~175 s carries it |
| …the turn completed on the restored tree | the model saw its second write fail, retried it on the restored tree, verified both files and committed; `first-step.txt` = `step one`, `second-step.txt` = `step two` on the pushed branch, which the finish pushed (`ab57305`) through the origin that had outlived the destroyed box |
| Deploy mid-run: the attempt settles 0 | `settled`, `exit_code: 0`; the deploy fired 12 s into the attempt, with the first model request in flight, and finished 25 s later |
| …a new host life resumed the conversation | `a new host life resumed the conversation from its storage (submission 8)` after the first tool call |
| …the work was delivered | `RESULT-xd3.md` with `STATUS: DONE` on the pushed branch |

What this pass FOUND, fixed in this tick (jpy) with a test
(`harness/test/node/standin-worker-entry-env.test.ts`): the stand-in's
`ticks-worker` demanded `TICKS_REPO_URL` at source time, so the first
whole-container destroy failed one claim — the restore's git had already
fetched and checked out the replacement box, but its `--setup` refused
("TICKS_REPO_URL unset"), because the restore's env carries none of the
boot's inputs (`restoreEnv`, `harness/src/host/worker-attempt.ts`) and the
production setup entry takes none either (`image/worker.sh`
`run_setup_entry`). The stand-in now requires the origin URL in `--boot`
only, refusing legibly with the boot's config exit (2).

Run the proof ONCE per deployment: the attempt origins live in Durable
Object storage and outlive a pass, so a second run against the same
Worker inherits the first's pushed branches — an intermediate second run
of this pass failed "the tool did not run twice" on a doubled
`tool-runs.txt` line that the first run's branch carried into the second
run's boot; the reattach itself was sound. The recorded pass above ran on
a fresh deployment.

What the first staging start FOUND, fixed in tick jhp with a test
(`cloudflare/test/staging-agent.test.ts`): since tick hxd the WorkerAgent
reads its run's substrate row by the run id it parses from the container
name, and the staging Worker passed the bare proof name — so every start
fell to the sdk0 door staging does not bind ("binds no container
namespace"). The staging Worker now names the container `<run>-<tick>-1`
(the tick a start names, `xd3` by default).

A first full pass (before the resume line existed) also held every claim,
but its log could not tell a resumed host from one that never died; the
host now logs the resume (`harness/src/host/worker-attempt.ts`, pinned in
`test/worker-attempt-host.test.ts`), and the pass recorded above claims it.

The first full pass (tick jhp, 455 s, before a2l) held the host-lost and
deploy-mid-run claims with the same evidence shapes, and its second
scenario was then the WORKSPACE loss — the throwaway origin lived inside
the container, so a destroyed box could fetch nothing back. Since tick
a2l the origin outlives the box (the staging Worker's own `/proof/git`
door) and the proof destroys the whole container, as the recorded pass
above does.

The staging Worker, its D1 database and its container application were
deleted after the proof.

# Staging proof: the restore's setup survives a real dependency install (tick cni)

`restore-setup-staging.ts` proves the one long line of the restore — the
repository's `[sandbox]` setup, re-run on a rebuilt workspace — on the real
platform, and answers the run door's open question along the way. Not part of
CI: it spends (cents of Workers AI and container time) and needs the staging
agent Worker deployed (above).

Two questions, both observed on the staging agent Worker:

1. **THE RESTORE'S SETUP.** An attempt's workspace is emptied between tool
   rounds (the same setup line a destroyed container's restore runs — the
   workspace loss, so the observation is about the INSTALL and not the
   container's whole boot; the mid-turn container destroy is the jhp
   proof's, carried by the origin that outlives the box since tick a2l).
   The round that meets the empty workspace
   restores it — clear, clone, fetch of the attempt branch, checkout of the
   wip tip, then `ticks-worker --setup`: a REAL dependency install (apt
   build-essential + golang, ~139 s, 172 packages) — and the turn continues
   on the restored tree, its dependencies live.
2. **THE RUN DOOR'S HOLD** — tick cni's open question, now an observation:
   does one `run` RPC, holding one container exec await for minutes, answer
   with the command's own exit code? (The restore's setup no longer rides
   this door: its bounding `head -c` SIGPIPEs any command that prints past
   256 KiB — a chatty install dies `exit 141` mid-way, reproduced by the node
   suite — so the setup rides the process doors instead, polled, its output
   read by cursor.)

## Running it

The staging agent Worker deployed as the xd3 proof above (its `--setup`
entry, its `tick` parameter and its `destroy` route are this tick's — exec
and destroy take the same `tick` as the start, because the container's name
`<run>-<tick>-1` carries it), then from `harness/`:

```bash
PROOF_URL=https://ticfac-staging-agent.<subdomain>.workers.dev PROOF_TOKEN=<the token> \
  node proof/restore-setup-staging.ts evidence.json
```

Tear down afterwards, as the xd3 proof says. The 150 s run-door probe sits
under undici's 300 s headers timeout; keep it under that on any rerun.

## Recorded result, 2026-10-05 (pi-ai / pi-durable 1.0.2)

`PROOF HOLDS`, every claim (attempt `proof-cni-…`, 10.5 min end to end):
boot 10 s, conversation 9.7 min of which the restore's install was 139 s,
finish 1 s, then the 150 s run-door probe.

| Claim | Evidence |
|---|---|
| The attempt settled with exit 0 | `settled`, `exit_code: 0`, "the finish phase exited 0"; the finish pushed `tick/proof/cni` at `93a13f8` |
| The lost workspace was rebuilt by the restore | `.setup-ran` in the pushed tree — written by the setup entry, which only the restore runs, into a tree the loss had emptied |
| The restore's setup was a real dependency install, minutes of it | `.setup-ran`: `gcc (Debian 14.2.0-19) 14.2.0`, `go version go1.24.4 linux/amd64`, `packages: 172`, `seconds: 139`; the round that met the empty workspace (bash `./greet.sh`) ran 154 s → 295 s wall clock — the install inside it |
| The install is live in the container the turn continued in | `gcc --version` in the restored container answers `gcc (Debian 14.2.0-19) 14.2.0` |
| The round's edit survived the loss | the `greet.sh` fix — a wip, uncommitted, when the workspace was emptied — prints `Hello, world` on the pushed branch, beside the model's own commit and the finish's report commit |
| The report is on the pushed branch | `RESULT-cni.md` with `STATUS: DONE` read back from the bare origin |
| One run-door RPC held one 150 s container exec | `{ready: true, exitCode: 0, output: "held", truncated: false}` after 150.1 s, twice (and once at 150.1 s in an earlier run of the same probe): the whole exchange — one RPC awaiting one container exec — holds minutes |

The restore fired through the tracked bash's nonce check (the round was
already in flight when the workspace was emptied), which restores silently;
the ready check's announced path — `the container was lost between rounds;
workspace restored to <sha>` — was observed in the proof's first run, at the
eighth loss, and both paths are the same `restoreLostWorkspace`, pinned by
the node and workerd suites. An earlier run of this proof also emptied the
workspace after EVERY round to chase the announced line: the model restored
seven times, investigated the harness ("the environment re-ran a setup
process between my tool calls"), re-committed its report and STILL settled
exit 0 — the resilience note this proof did not plan and would not claim.

What this proof does NOT claim: a container destroyed mid-TURN (jhp's), a
deploy mid-run (xd3's note above), and the run door holding a command that
prints past its bound — that one it REFUSES by design: the bounding `head -c`
SIGPIPEs the writer, which is why the restore's setup no longer rides it.

The staging Worker and its D1 database were deleted after the proof.
