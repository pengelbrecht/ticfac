# Final review — epic umq (SDK 1.0 / durable_object), as integrated

Reviewed the epic's ticks (fsy, x9d, nmd, 1hq, v1d, dax — all closed; l76 is this
review) and the integration branch `epic/umq` at `41553f3957fe` (the merge that
closed dax, and the tree this review runs on) against the base the branch was cut
from, main `44ce1b407`. The epic's work is wider than the branch: fsy, x9d, nmd,
1hq and v1d landed on main on 2026-10-01 as PRs #187/#188/#189/#192/#193 with the
follow-ups #194/#195/#196/#198/#200 the same week; dax — the cutover — is the one
tick this run (`run_7445005ff17447c3ae351f5ba99ea01d`) landed on the branch
(48 files, +1449/−2493, plus the repair `4f742e41d` that made the integrated gate
green). My tick carries no DEFERRED FINDINGS notes; nothing to re-report from a
worker's claim.

## [A1] workers and orchestrator through the new DO class, pinned image, per-job size — MET

- The submit-time default is `do_v1` (`DEFAULT_RUN_SUBSTRATE`, src/run-substrate.ts),
  recorded with the image pin for every new container run before the Workflow
  instance exists (`runMarks` → `recordRunSubstrate`, awaited in `bootRun` before
  `boot()`, undone on failure); an explicit `sdk0` ask survives to ignition, and
  `sdk0`+`--queue` is refused because the parked record carries no substrate. The
  routing is by run record under the seam (`routedSandboxBinding`), so the
  orchestrator boot and every WorkerAgent boot pick up the per-job instance
  (`INSTANCE_BY_JOB_KIND`) and the pin (`hostedBoot`) without a call site
  changing.
- wrangler.toml declares exactly one container application now —
  `ticks-factory-sandbox`, `scheduling_policy = "durable_object"`, named image
  `factory` from the shared image context — pinned by
  `TestBundleDeclaresOnlyTheFactorySandboxApplication`, which also bans
  `max_instances`, `rollout_active_grace_period` and `name = "ticks-orchestrator"`.
- Live proof from inside this run's own review container: PID 1 is
  `sleep infinity` and `/var/lib/ticfac/image.env` exists — the `BOOT_ENTRYPOINT`
  of `FactorySandboxCore`, not the 0.x image's sandbox control server — so this
  job's container was started by the new DO class. Its image is stamped
  `TICFAC_VERSION=ce124e8ca47a` (the hn6-merge image), the image the factory
  served when the run was submitted at 04:38Z, and it kept booting containers on
  it all day while main moved (#236–#241 landed this morning) — the per-run pin
  doing exactly what the epic said it would.

## [A2] a deploy during a live run leaves containers on their startup image — MET

The staging Worker (cloudflare/staging/, `wrangler.toml` of its own, `/image`
route that answers the running image beside the deployed one) exists for the
rehearsal; tick nmd was closed on 2026-10-01 naming that staging proof, and the
inactivity-timeout re-arm in the constructor — the DO-restart-on-deploy risk the
epic named — is implemented (`rearm`) and unit-tested
("re-arms the inactivity timeout in its constructor, because a deploy restarts
every object"). Corroborated live: the deploys of this morning's main commits did
not take this run's containers, which are on their submit-time image.

## [A3] grace period, rollout_held.go, rollout wait deleted; old app deleted — MET (deletion fires post-merge, by construction)

`rollout_active_grace_period` + its pin test, `internal/factory/rollout_held.go`
(+tests), the confirm wait in `rollout.go` (replaced by `containers.go`'s
`resolveDeploymentImage`), `--skip-rollout-wait`, `--keep-images`,
`VerifyContainerCapacity`, the workflow's rollout wait and the "held by live
run(s)" summary branch are all gone; the only remaining mentions are comments and
the ban-list tests that pin the deletions. The old application `ticks-orchestrator`
is deleted by the deploy (`legacyapp.go`) behind a live-run guard that reads the
factory's own D1 — a live run with no substrate row holds it open, and so does
anything unreadable — and its image repository is emptied with it. The physical
deletion can only happen at the first deploy AFTER this epic merges (the code that
deletes it is the code being merged); the guard is tested against the stateful
fake wrangler (delete when clear, hold while a run is live, hold when unreadable,
no-op when absent). The first post-merge deploy's "deleted the ticks-orchestrator
container application" line is the [A3] evidence for the close-out to look for.

## [A4] prune never deletes a digest a live run pins — MET

`livePinsSQL` reads every live run's pin from D1; an unreadable answer stops the
prune entirely; `selectImagesToPrune` keeps every protected digest, every tag
sharing a kept digest, and every image whose digest or creation time is unknown.
Tested both at the selection level (`TestSelectImagesToPruneNeverDeletesAProtectedImage`)
and through the D1 read (`TestPinnedDigestsReadTheLiveRunsPins`: 6 images, keep 3,
pin the oldest → prunes t1,t2 only).

## [A5] gate green; a real hn6-style cloud run completes a tick on the new path — MET

The run's integrated gate evidence is exit 0 for both halves at `41553f3957fe`
(gate-dax-1-go, gate-dax-1-ts). I re-ran the checks in this container: `gofmt -l`
clean, `go vet ./...` clean, `go test -short -count=1` green for internal/factory,
internal/cli and internal/exec/subprocess (the latter 74s, in exactly the
PID-1-reaps-nobody container the zombie fix targets), and the full cloudflare
suite `pnpm exec vitest run` 1783/1783 across 77 files (523s). This run is the
proof: a real cloud run on do_v1 that completed a worker tick (dax, merged
41553f3957fe, closed) — with this review running in another container of the same
run on the same path.

## Judging the tests

The suite exercises the change, not a shadow of it: the run-workflow tests now set
`SANDBOXES_V1` in `beforeEach` so every orchestration test boots through the
routed binding; run-substrate tests pin the new default at submit (a run that
asked for nothing gets `do_v1` and the same image-ref DO ask as one that asked);
factory-sandbox.test.ts drives the class's whole lifecycle over a fake container
(40+ cases: pending-process machinery, lifetime re-arm, byte cursors, the run
door); the deploy tests run the new deploy end to end against the stateful fake
wrangler, extended to the new command surface (`containers delete`, the two
`d1 execute --json` reads, the two-application listing); phase0-compat's pins were
rewritten for what replaced the deleted machinery rather than dropped.

## Worth a look by the close-out (not defects in the tree)

- The first real deploy should confirm real wrangler's output carries the
  digest-pinned FactorySandbox image line; if not, the fallback reads the
  application record, and if neither names one the deploy warns and records
  nothing (runs still pin their own image from the DO). Already flagged by the
  dax worker; the code handles all three shapes and the fake models them.
- The deploy-side run-state vocabulary is duplicated between Go and the Worker
  (finding below).
- Backlog ticks 8ct and oog were this run's own promotions of dax's two findings;
  8ct's subject is now fixed inside this epic (finding below), oog's declared-image
  seam is unchanged and still correctly owned by its tick.

```findings v2
[
  {
    "kind": "defect",
    "title": "run_image stamp names the deployment's image, not the image the run pinned",
    "severity": "low",
    "body": "A run's containers now start on the digest pinned in run_substrate at submit, but stampRunImage still writes the deployment-level factory_deployment_image row at ignition, so `ticfac cloud status`'s image line can name an image the run did not boot when a deploy lands between submit and ignition, and a deploy that resolved no image leaves no stamp at all even though the run booted a real one. Stamp from the run's pin first, falling back to the deployment record.",
    "evidence": "cloudflare/src/runs.ts:808 (stampRunImage reads getDeploymentImage); cloudflare/src/run-substrate.ts (the pin recorded at submit); internal/cli/cloud.go (cloudRunImage, reported so a fix that never ran is not read as one that did not work)"
  },
  {
    "kind": "defect",
    "title": "the deploy's Go SQL restates the Worker's run states with nothing pinning them",
    "severity": "low",
    "body": "liveLegacyRunsSQL (internal/factory/legacyapp.go) and livePinsSQL (internal/factory/prune.go) each hard-code ('starting','running','stopping'), the states ACTIVE_RUN_STATES names in cloudflare/src/runs.ts. Nothing ties the Go copies to the TypeScript one — the same shape of drift the deleted capacity mirror was refused for (tick 7fl). If the Worker ever grows a live state, the deploy's legacy-app guard and its pin protection silently stop seeing those runs, and the prune could delete a digest a live run pins.",
    "evidence": "internal/factory/legacyapp.go (liveLegacyRunsSQL) and internal/factory/prune.go (livePinsSQL) vs cloudflare/src/runs.ts:119 (ACTIVE_RUN_STATES)"
  },
  {
    "kind": "defect",
    "title": "backlog tick 8ct still reads open though its fix landed inside epic umq",
    "severity": "low",
    "body": "The repair the run merged to make dax's integrated gate green (4f742e41d, \"Liveness in exec/subprocess: existence is not life\", named for 8ct) fixes exactly what backlog tick 8ct (\"Subprocess interrupt tests fail at base in Linux containers\") describes: the three tests it names pass on the epic tree, re-verified in this review's own container of the same kind. 8ct can be closed naming the merge that carries the fix, so its owner does not re-derive it.",
    "evidence": ".tick/issues/8ct.json (status open); internal/exec/subprocess/zombie_linux.go (names 8ct and the three tests); commit 4f742e41d on epic/umq"
  }
]
```

REVIEW-VERDICT: READY

STATUS: DONE
