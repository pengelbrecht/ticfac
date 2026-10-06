<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/umq/attempt-3/rlx`, base `07f4d74de006b005a9e90010f05977674dfc312f`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent staged .tick/learnings.md for commit

# rlx — closing out epic umq: containers on the durable_object scheduling policy (SDK 1.0)

Attempt 3 of `rlx`, the close-out tick of epic `umq`, run `run_7445005ff17447c3ae351f5ba99ea01d`.
Everything below is read from the records on the integration branch (`epic/umq`, the tree this
attempt carries) and the ticks' own reports — `RESULT-dax.md` on `tick/umq/attempt-1/dax` and
`RESULT-l76.md` on `tick/umq/attempt-2/l76` — never from memory of the plan.

## What the epic set out to do, and what it delivered

The plan (`.tick/issues/umq.json`, created 2026-10-01): move every factory container — workers,
then the orchestrator — off Sandbox SDK 0.12.x's `Sandbox` class and the app-wide image rollout,
onto our own Durable Object class under `scheduling_policy = "durable_object"`, in eight ticks.

What actually landed, from the tracker's close records and the run's own:

- **fsy** (wrangler ≥4.142, pending-I/O keep-alive), **x9d** (the process-runner script in the
  image), **nmd** (the `FactorySandbox` DO class, the new container application, the staging
  Worker), **1hq** (worker containers routed per run through `SANDBOXES_V1`, per-job instance
  size) and **v1d** (the orchestrator on FactorySandbox, the per-run image pin, prune protecting
  pinned digests) all landed on **main on 2026-10-01 as PRs #187, #188, #189, #192 and #193**,
  closed by the operator the day the epic was planned. The run's graph therefore had exactly one
  implement tick left to dispatch: the cutover.
- **dax**, the cutover, is the one tick this run landed: work commit `49955d68` merged into
  `epic/umq` as `b50e45a2`; its integrated gate came back red on go; the run's own repair
  `4f742e41` ("Liveness in exec/subprocess: existence is not life", named for backlog tick 8ct)
  merged as `41553f3957fe`; the integrated gate then passed both halves at that merge. dax's
  report states what the cutover deleted and what replaced it; l76's review checked the result.
- **l76**, the final review, returned a validated `ticfac.job-result.review-epic.v1` envelope at
  `c6b0d67c17f9`: DONE (READY), three findings, none blocking, no boundary violations, no
  deferred findings on its own record to re-report.

### The acceptance items

**[A1] Workers and orchestrator through the new DO class, pinned image, per-job size — MET.** The
submit-time default is `do_v1` (`DEFAULT_RUN_SUBSTRATE`, `run-substrate.ts:38`), recorded with the
image pin in `run_substrate` before the Workflow instance exists; every container is routed by its
run's record under the seam (`routedSandboxBinding`), so the orchestrator boot and every worker
pick up the per-job instance (`INSTANCE_BY_JOB_KIND`) and the pin without a call site changing;
an explicit `sdk0` ask survives to ignition as the run's own choice, and `sdk0` + `--queue` is
refused. `wrangler.toml` declares exactly one container application — `ticks-factory-sandbox`,
`scheduling_policy = "durable_object"`, no `max_instances` — pinned by
`TestBundleDeclaresOnlyTheFactorySandboxApplication`. Live proof from this close-out's own
container, booted 14:18Z: two factory deploys had already run during this run (main `4cec450a` at
10:27Z and `782ccd5c` at 10:43/10:50Z, both successful per the deploy workflow's records), yet
the container started on the run's submit-time pin — PID 1 is `sleep infinity` (FactorySandbox's
`BOOT_ENTRYPOINT`, not the 0.x image's control server), and `/var/lib/ticfac/image.env` says
`TICFAC_VERSION=ce124e8ca47a`, the image the factory served when the run was submitted at 04:38Z.

**[A2] A deploy during a live run leaves containers on their startup image — MET.** The staging
Worker (`cloudflare/staging/`) carried the rehearsal; nmd's close note names it: "#189 (6d65990b):
FactorySandbox + durable_object app + staging proof of [A2]". The DO-restart-on-deploy risk the
epic itself named is answered in code — the constructor re-arms the inactivity timeout
(`factory-sandbox.ts:878`), unit-tested. Corroborated live, not just on staging: the two deploys
above ran while dax's worker container (04:38Z–11:40Z) was live, and it finished its tick on its
startup image; the review's container, booted after both deploys, was on the same pinned image.

**[A3] Grace period, rollout_held.go, rollout wait deleted; the old app deleted — MET, with the
physical deletion by construction post-merge.** In this tree: `internal/factory/rollout_held.go`
is gone, `rollout_active_grace_period` and `max_instances` appear only in comments and the
ban-list tests that pin their deletion (`bundle_test.go:355,390`), the deploy's confirm wait is
replaced by `containers.go`'s `resolveDeploymentImage`, and `deploy-factory.yml`'s rollout wait
is gone. The 0.x application `ticks-orchestrator` is deleted by the deploy (`legacyapp.go`) — but
only when nothing holds it: a live run with no substrate row (any pre-cutover run) holds it open,
and so does an unreadable answer; refusals are reported and retried. Because the code that deletes
the application is the code being merged, the deletion itself can only fire at the **first
post-merge deploy**, and that deploy's summary line — the deletion, or exactly what held it — is
the [A3] evidence for the operator to read. Nothing in the item is left unimplemented; what is
left is that one read, named below.

**[A4] Prune never deletes a digest a live run pins — MET.** `livePinsSQL` reads every live run's
pin from D1; an unreadable answer stops the prune entirely; the selection keeps every protected
digest, every tag sharing a kept digest, and every image whose digest or creation time is unknown.
Tested at the selection level (`TestSelectImagesToPruneNeverDeletesAProtectedImage`) and through
the D1 read (`TestPinnedDigestsReadTheLiveRunsPins`), both re-verified by the review in its own
container.

**[A5] Gate green, cloudflare suite green, a real cloud run completing a tick on the new path —
MET.** The run's integrated gate evidence is exit 0 for both halves at `41553f3957fe`
(`gate-dax-1-go`, `gate-dax-1-ts`). CI is green on the epic PR #242 — the run's checkpoint records
it and admits this close-out; CI's push-triggered run on `41553f3957fe`, the last commit touching
anything CI reads, succeeded, and every commit from there to the PR head changes only `.ticfac/`
and `.tick/` paths CI ignores by design. The cloudflare suite — deliberately CI's side of the
gate, not the per-tick gate's — ran green in CI and twice by hand: the dax worker (1783/1783, 77
files) and the review (1783/1783, 523s). And the hn6-style cloud run the item names is this one:
a `do_v1` run from submit to close whose worker tick (dax) completed end to end inside it, with
the review and this close-out running in further containers of the same run on the same path. At
this close-out I re-ran the whole-repo gate on this tree: `make gate` (gofmt, vet, the short
suite, 3m) and `make ts-gate` (biome, contracts:check, tsc) both green, including
`internal/exec/subprocess` (75s) in exactly the PID-1-reaps-nobody container the zombie fix
targets.

## What the run absorbed

From `.ticfac/runs/run_7445005ff17447c3ae351f5ba99ea01d/absorptions/`: five findings, every one
decided on the basis `backlog-default`, none gating, none promoted into the running epic — no
finding was reviewer-named blocking, none was a high-severity claim naming a done item. Each
became a backlog tick with an owner, listed on PR #242:

- dax's two: "Subprocess interrupt tests fail at base in Linux containers" → **8ct** (the
  reporter reproduced it at base, and the run's repair later fixed the underlying defect); the
  proposed tick "Declared-image check still names the deleted 0.x application" → **oog**.
- l76's three: "run_image stamp names the deployment's image, not the image the run pinned" →
  **xtd**; "the deploy's Go SQL restates the Worker's run states with nothing pinning them" →
  **oyn**; "backlog tick 8ct still reads open though its fix landed inside epic umq" → **2b2**.

The repair `4f742e41` was not an absorption: it was the run's own gate repair for dax (decision 2,
plan-repair) that made the integrated go gate green. That it also landed the subject of backlog
tick 8ct is exactly what finding 287edb27 records — and why 8ct still reads open: nothing wrote
the linkage on 8ct's record, so 2b2 owns closing it once the merge is on main. No
`.ticfac/runs/<run>/amendments/` records exist and the epic's notes carry no worker-proposed
exception, so there is nothing here to mark confirmed or unconfirmed; the PR body states the same.

## What is left open

1. **The two post-merge reads on the first deploy this run's merge triggers.** [A3]'s physical
   deletion line ("deleted the ticks-orchestrator container application", or the named holder),
   and — dax's watch item, seconded by the review — whether real wrangler's output carries the
   digest-pinned FactorySandbox image line. The code handles all three shapes (parse it, fall
   back to the application record, warn and record nothing) and the fake wrangler models them,
   so the read confirms, it does not repair.
2. **The five backlog ticks**, open, operator-owned: 8ct (subject now fixed in this epic; 2b2
   records how to close it once the merge names a main commit), oog, xtd, oyn, 2b2.
3. **The learnings compaction's apply**: produced in full, durable on this branch at
   `docs/umq-closeout-retro-2026-10-06.md`, awaiting its move into `.tick/learnings.md` — which
   this substrate refuses a close-out (the defect below); until the tick lands, the doc is the
   live copy and the apply is a paste.
4. **The 0.x residue** — filed as the finding below.
5. **The stall in this run's own dispatch**, named and owned elsewhere: the operator's epic ex6
   record (created on main during this run, 2026-10-06 10:03Z) cites this run as its motivating
   evidence — the WorkerAgent DO's host kept dying and resuming without progress during dax's
   morning, and no stall detection fired for a pi-durable worker. It is ex6's premise; the
   close-out does not re-file it.
6. **Tracker state on this branch's fold**: the epics hn6 and 43y still read open in this tree
   because the operator's closes landed on main after this branch's base; the run's PR merge
   folds the base in and carries them. Naming it so the state is not read as a disagreement.

## Learnings

The retro compacts three entries: the planning rule now says a post-merge clause names the
OBSERVABLE its evidence is (umq's [A3] was re-derived by three workers); the gate-host rule now
covers an epic whose own new image changes the host shape (umq's gate went red on tests green on
CI because the new image's PID 1 reaps nobody and the liveness oracle read zombies as alive —
fix the oracle, never skip the test); and a new rule that a repair landing a backlog tick's
subject writes the merge on that tick's notes in the same change.

**They could not land in `.tick/learnings.md` from this substrate.** This attempt produced the
full compaction (150 lines, at the file's cap, older examples tightened, no rule dropped) and
staged it — and the container's pre-commit hook refused the commit (`ticks-worker: refusing this
commit — it stages tracker state`), because it rejects every `.tick/` path, the learnings file
the write boundary itself exempts (tick 54n) included; the finish phase's `sweep_boundary_state`
would discard an uncommitted edit the same way. The guard recorded the attempt, as it is built
to. So the compaction is this attempt's work commit instead, durable and ready to apply verbatim:
`docs/umq-closeout-retro-2026-10-06.md` — the retro beside the full compacted file in its
appendix — and the gap itself is filed as the defect below, because a cloud close-out that
cannot write its named deliverable will hit this again.

## The next feasible epic

It is already on the record: **ex6, "pi-durable in the cloud: prove and harden"** (open, created
by the operator during this run). Its premise is this run's stall, and its [A2] — kill the
harness host mid-tool and destroy the container mid-turn in a real cloud run, both resuming
without redoing work — is exactly the durability umq's substrate now makes testable: a deploy no
longer takes a container, and every container of a run boots on the run's pinned image. umq's
own leftovers are single ticks — the five backlog ticks plus the two findings below — not epic
material. The bigger move the n0b spike sketched — the agent loop itself in a Durable Object —
stays behind its two recorded triggers, neither of which has fired: container deaths have not
yet been counted on the new path, and pi-durable still calls itself experimental.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Retire the sdk0 opt-out and the residual Sandbox class once the 0.x app is gone",
    "severity": "medium",
    "body": "The cutover keeps TICFAC_CLOUD_SUBSTRATE=sdk0 as an explicit opt-out, and the 0.x Sandbox class with its SANDBOXES binding for pre-cutover records — both meaningful only while the 0.x application exists. The first post-merge deploy deletes ticks-orchestrator (behind the live-run guard), after which a run that asks for sdk0 is still accepted at submit but its containers can no longer boot, and the failure lands at ignition, after the boundary is pushed. A tick should refuse sdk0 submissions once the application is gone, and later drop the residual class and binding once no pre-cutover run can exist; dax's report named both halves as the next tick's business.",
    "evidence": "internal/cli/run_cloud.go:244 (the opt-out documented as \"while a deployment still serves it\"); cloudflare/src/run-substrate.ts:27 (sdk0 routing to the legacy binding); cloudflare/wrangler.toml:60,110 (the Sandbox class and SANDBOXES binding stay for pre-cutover records); RESULT-dax.md, \"What the next tick has to know\" and \"Residual seams\""
  },
  {
    "kind": "defect",
    "title": "A cloud close-out cannot write .tick/learnings.md: the guard refuses the file",
    "severity": "medium",
    "body": "The write boundary exempts .tick/learnings.md and the close-out's own contract names the learnings it compacts as its write deliverable, but a cloud attempt can reach the file by no route: worker.sh's pre-commit hook refuses any commit that stages a .tick/ path, and sweep_boundary_state restores the directory before the salvage, so an uncommitted compaction is discarded with the container. A close-out that then delivered only its report would be rejected as no-commits — the exemption the boundary declares is not one the cloud substrate can deliver, and this run's close-out had to carry its compaction in a docs file instead (docs/umq-closeout-retro-2026-10-06.md).",
    "evidence": "image/worker.sh:531 (the hook refuses every staged .tick/ path) and image/worker.sh:596 (sweep_boundary_state restores .tick/ before the salvage); internal/exec/subprocess/report.go:190 (the boundary exempts .tick/learnings.md); internal/reconcile/rolejob.go:365 (a close-out's empty branch is rejected: its learnings are its write deliverable); this attempt's refused staging is recorded in the container's guard ledger, which the finish phase's report header surfaces"
  }
]
```

STATUS: DONE_WITH_CONCERNS — the epic's five items hold on the integration branch and CI is green on PR #242; the concerns are the two reads the first post-merge deploy owes ([A3]'s deletion line, and whether real wrangler names the FactorySandbox image), the two filed findings (the 0.x residue, the learnings destination a cloud close-out cannot reach), and the five backlog ticks
