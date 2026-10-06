# The cloud half of [A4] and the real-run half of [A2], post-merge: the owned runbook (ticks kkt and ewd, epic 43y)

Epic 43y's [A4] asks for "a real hn6-style cloud epic run and a local epic run
complete on pi-durable". The local half ran on 2026-10-05 and is recorded in
`docs/pi-durable-local-epic-run-2026-10-05.md` (tick qdg). The cloud half
cannot: the factory deploys **main only** (AGENTS.md, `deploy-factory.yml`),
main is strictly behind epic/43y until the close-out merge, and the final
review gates the merge — so the run is sequenced **after** the merge by
construction, and this runbook is how it does not fall through the crack
afterwards. Without an owner it closes at best *predicted*; this document is
the ownership.

**Who owns it:** the operator, from a post-merge checkout. Not a dispatched
worker: starting a cloud run spends money on the operator's Cloudflare
account and touches a live external system, and `.tick/config.md`'s standing
orders put both in the *always ask* column. A tick inside this run can only
ship what makes the runbook mechanically safe — the harness preflight below,
and the evidence-parity guard that pins this runbook's [A2] fault lines to the
host that prints them — and write the runbook down.

Since tick ewd this document owns one more thing: [A2]'s "proven … in a real
run" clause. It has the same shape as [A4]'s cloud half — proven on staging
already (`harness/proof/`, tick jhp), with no real run on record — and the
section "What 'observed' means for [A2]'s real-run half" below is its
ownership.

## Preconditions, in the order they unblock

1. **The close-out merge.** epic/43y merges to main through the run's own
   PR + CI close-out (`.tick/config.md`). This carries, in one merge: the
   pi-durable image (jhp — `image/common.sh`'s kind set is
   `omp | claude | pi-durable`), the hosted WorkerAgent (xd3, 4uj), and the
   cloud harness binding (twa — profile resolution binds the hosted kind
   `pi-durable` for every profile that dispatches into Cloudflare, so
   `.tick/runners.cloud.toml`'s `kind = "pi"` cells need no flip), because
   they are all children of the same epic.
2. **The deploy.** `ticfac factory wait-deployed <merge sha>` — exit 0.
   Never a hand-rolled `gh run list` loop: a deploy superseded by a newer
   main commit is normal and the loop never ends (AGENTS.md). The
   preflight below will also say this if you forget.
3. **Forge access for runs of THIS repository.** The PR rule
   (`.tick/config.md`) requires the factory to push and open PRs; until the
   GitHub App rung lands (epic dm6) that is not available factory-side.
   Either dm6 has landed, or run the cloud epic against a repository that
   declares no PR rule.

## The run

```
ticfac run hn6 --cloud        # hn6 is the operator's named candidate, or the next feasible epic
```

The command pushes the current branch, submits to the factory, attaches the
live view (Ctrl-C detaches without stopping; run it again to re-attach or,
once finished, to resume). The run's implement and closeout workers boot
factory sandbox containers as hosted WorkerAgents on pi-durable: the
conversation in the factory's Durable Object, the tools in the container,
the substrate recorded per dispatch on the run branch. Two boots are not
hosted, by design: the orchestrator container execs `ticfac run-epic`
itself, and the review — "the one cloud boot that still runs a CLI harness
in its container" (run-workflow.ts, tick jhp) — runs omp on GLM through
its own floor (`reviewHarness`), whatever the overlay's review cell names.

## What "observed" means for [A4]'s cloud half

The cloud half is **observed** — not predicted — when all three hold:

1. The preflight passed: the factory reported `harness_kinds` containing
   `pi-durable` (i.e. the answering factory is the post-merge deploy) and
   the branch's cloud routing resolved to shipped kinds.
2. The run completed: `run_state: completed` in its `ticfac.run-epic.v1`
   document, every tick closed behind the integrated gate.
3. The run's implement (and closeout) dispatches record their workers on
   `pi-durable` containers (`harness: pi-durable` in the attempt records) —
   a cloud run whose workers ran `omp` or `claude` completes fine and
   observes nothing about [A4]. The review's omp boot is the designed
   exception above, not a gap in the observation: what [A4] must show is
   the WORKERS hosted. (Whether the review's CLI boot satisfies [A1]'s
   "every worker … runs on pi-durable" is a question for the epic's
   close-out, not for this runbook — the review's floor is a recorded
   design decision inside the epic, tick jhp.)

Record the result beside qdg's local-half record (a doc under `docs/`, plus
the run's artifact space), and note in the epic's tracker record that [A4]'s
cloud half moved from *predicted* to *observed*.

## What "observed" means for [A2]'s real-run half (tick ewd)

[A2]'s letter: "A harness process killed mid-tool resumes in a new process
without re-running the tool; a container destroyed mid-turn is restored from
the last wip commit and the turn completes — proven on staging and in a real
run." The staging half is on record (`harness/proof/agent-faults-staging.ts`,
tick jhp: `PROOF HOLDS`, every claim, 2026-10-05). The real-run half was not:
qdg's local run record explicitly disclaims both faults, and until this
section nothing recorded a harness kill or a container loss inside a real
epic run. Until the run below observes both faults, the close-out scores
[A2]'s real-run clause *predicted* — visibly, with this runbook as its owner,
the same posture as [A4]'s cloud half above.

**Why the cloud run, not another local one.** The tick that added this
section could not inject a harness kill into a real *local* run, and the
reason was mechanical, not a preference. Locally, a harness process killed
mid-tool settled its attempt — a non-zero exit was never nudged
(`internal/exec/subprocess/nudge.go`: "the runner failed"), so the run's
recovery was the redispatch ladder starting the successor from the killed
attempt's carried wip commits (tick dwn's afterTools checkpoint: "what a
stopped attempt's successor starts from"): the restored-from-wip fault at
run granularity, but not "resumes in a new process without re-running the
tool". Tick 3c2 changed that mechanics: the local supervisor now replays a
signal-killed durable runner's same argv, bounded and in-attempt, and the
relaunched process resumes the killed turn mid-tool from the attempt
storage — proven mechanically by
`TestAKilledDurableRunnerIsRelaunchedAndResumesMidTool`
(`internal/exec/subprocess/local_host_e2e_test.go`), so a local run can
observe this fault line below too. The cloud run remains the owner this
document records, for the half only it risks: the WorkerAgent host's own
version of the fault — the Durable Object's life lost mid-tool while the
tracked bash keeps running in its container, the conversation resumed from
the DO's SQLite and the tool reattached by its nonce — is a mechanism the
local rung does not have, and the real run that exercises it is the cloud
one this document owns. The local rung's other resume paths — the nudge,
the report pushback, the stuck re-prompt — remain supervisor relaunches of
a runner that is still an attempt, never recoveries from a kill.

The two faults are injected into the run above, once each, during one hosted
worker dispatch's turn (implement or closeout — the review's omp boot is the
recorded [A1] exception, not a hosted dispatch):

1. **The host's life lost mid-tool.** While the worker's tool call is in
   flight — watch it live on `ticfac watch`, or `ticfac cloud logs <run-id>
   --tick <tick> --follow` — trigger a factory deploy: `gh workflow run
   deploy-factory.yml`, the runbook's own retry door (AGENTS.md; no console,
   no code change). A long tool call (a test suite, a build) is the easy
   window; a deploy that lands between tool calls proves nothing. The deploy
   ends the WorkerAgent Durable Object's life mid-tool while the tracked bash
   keeps running in its container — the staging proof fired one at the
   model's first `tool_execution_start` — and the next heartbeat's host life
   resumes the conversation from the Durable Object's SQLite and the tracked
   bash reattaches by its nonce.

   Observed when all three hold:

   - the attempt's log carries `ticfac-harness: a new host life resumed the
     conversation from its storage (submission …)` after the in-flight
     tool's start;
   - the in-flight tool ran ONCE — one start and one completion in the log,
     its effect once on the branch: a reattach, not a re-run;
   - the attempt settles 0 and its work collects like any other dispatch.

2. **The container destroyed mid-turn.** While the turn is in flight, delete
   that attempt's container by its exact name
   `<run>-<tick>-<attempt>[-<slot>]` (`attemptSandboxNameForSlot`,
   `cloudflare/src/sandbox-executor.ts`): `pnpm exec wrangler containers
   list` names the account's container instances, `pnpm exec wrangler
   containers delete <instance>` removes the one whose name matches. The
   origin outlives the box (tick a2l: the repository lives in the `GIT_ORIGINS`
   Durable Objects, reached over the network), so the next round's ready
   check boots a replacement, fetches from that origin and checks out the
   last wip checkpoint before the model's next request.

   Observed when all three hold:

   - the attempt's log carries `ticfac-harness: the container was lost
     between rounds; workspace restored to <sha>` — or, when the loss lands
     under a tracked bash, `ticfac-harness: a tracked bash found a fresh
     container; the workspace was restored to <sha> (<subject>)`;
   - the restored `<sha>` is the last `wip checkpoint … pushed` line the lost
     life's own log shows;
   - the turn completed on the restored tree: the attempt settles 0, its
     commits and report on its branch.

Where the log is read: live on `ticfac watch` (the conversation and its log
as they land), and durably from the run's R2 worker stream — the dispatch
door drains a hosted attempt's log out by cursor on every state read, so
`ticfac cloud logs <run-id> --tick <tick>` serves the whole thing after the
attempt settles, container or no container.

**Bounds.** Once each, and only this run's own dispatches. The container
name carries the run id — match it exactly, and never a container of
another run: this account hosts other runs' containers, and this host's rule
is exact identity or nothing (AGENTS.md). The deploy is designed to be safe
for live runs — `rollout_active_grace_period` keeps the new rollout off
their containers, and the resume is the recovery the staging proof already
held, not damage. The destroy is the fault: not designed-safe, but
designed-recoverable, with the run's own redispatch-from-carried-wip ladder
behind it if the restore itself fails. Never the orchestrator's container —
it is the run, not a worker dispatch.

**Recording** — the same shape as [A4]'s: write the result beside qdg's
local-half record (a doc under `docs/`, plus the run's artifact space), and
note in the epic's tracker record that [A2]'s real-run half moved from
*predicted* to *observed*.

## The guard this tick shipped: the harness preflight

`ticfac run <epic> --cloud` refuses, **at the knee, before anything is pushed
or booted**, a submission whose branch routes a cloud job to a harness kind
the deployed factory's image does not ship — the trap twa found, where every
container of a run died at boot with `unknown harness kind 'pi'`. twa closed
the branch side of it (a cloud job resolves only to `pi-durable` now); what
remains is the factory side, a deployment whose image predates the kind:

- **The branch half** is HEAD's `.tick/runners.toml` with the
  `.tick/runners.cloud.toml` overlay, resolved exactly the way the run's own
  containers resolve it (`reconcile.CheckRouting` on the embedded cloud
  profile set: every role, every tier the ladder can reach, the on-demand
  jobs at the ceiling). A routing that does not resolve is refused here too,
  earlier than the run's own start refusal.
- **The factory half** is the factory's own answer: `GET /api/deployment`'s
  `harness_kinds`, the kind set `image/common.sh` accepts (pinned to the
  shell by a parity test). A factory that predates the field reports
  nothing — the preflight warns with the missing answer and
  `ticfac factory wait-deployed`, and proceeds; an unreadable factory is the
  same honest "cannot ask", never a refusal on transport.
- A repo with no `.tick/runners.toml` routes on the factory's defaults and
  is skipped silently.
- The expert `ticfac cloud run` submits without the preflight: the expert
  verbs are the operator's escape hatch, as with their other flags.

The refusal names each offending job by the kind its container would be
told (`implement-tick: pi-durable`), the factory version that refused, and
the runbook's fix — which is the factory's, never the overlay's: no cell the
branch could flip names a kind an older image ships instead, so the fix is
the merge that ships the kind and `ticfac factory wait-deployed <merge sha>`
for its deploy. The trap the runbook exists for cannot be executed by
accident.

## The guard ewd shipped: the fault evidence pinned to the host

The [A2] observation criteria above cite the attempt's log by exact line
text, and those lines are strings in `harness/src/host/worker-attempt.ts` —
a host that rewords one leaves the runbook's operator hunting a line that
no longer prints, with nothing failing anywhere. The container name the
mid-turn destroy matches is a template in `cloudflare/src/sandbox-executor.ts`.
`internal/cli/runbook_fault_evidence_test.go` pins every string the criteria
name against the source that prints or formats it — the same cross-tree
parity pattern as `internal/factory`'s payload checks — so the runbook
cannot drift from the code and the code cannot drift from the runbook,
silently.
