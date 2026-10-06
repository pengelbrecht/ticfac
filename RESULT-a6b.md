<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/umq/attempt-4/a6b`, base `c78d987c304c6d0b9c757fafcb5ce496a98c5a0a`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# Re-review (round 2) — epic umq, containers on the durable_object policy, AS INTEGRATED

This is review round 2 (tick a6b): the final review (l76) judged the epic READY at
`c6b0d67c17f9`, and the tree changed after it — the close-out (rlx) merged as
`58287c219cc0`. I reviewed that delta and re-judged the whole integrated tree, not
only the delta: the epic's ticks (fsy, x9d, nmd, 1hq, v1d — landed on main as
PRs #187/#188/#189/#192/#193; dax, l76, rlx — this run's) and the integration
branch `epic/umq` against the base the epic was cut from, main `44ce1b407`.
My tick carries no DEFERRED FINDINGS notes; nothing to re-report from a worker's
claim.

## What changed since the READY review

The delta `c6b0d67c17f9 → c78d987` (the tree this review runs on; its product
content is exactly `58287c219cc0` minus the stripped worker report) contains **no
product code change at all**. It adds `docs/umq-closeout-retro-2026-10-06.md`
(254 lines) and run bookkeeping (`.ticfac/` absorption, attempt, decision and
gate-evidence records; `.tick/` issue state and activity). I verified the retro
against the tree rather than trusting it: the deletions it names are gone (only
ban-list tests and comments mention them), its appendix is a faithful 150-line
compaction of `.tick/learnings.md` plus the three new umq entries (exactly 150
lines, no rule dropped), and its account of the five backlog findings matches the
absorption records.

## The acceptance criteria, re-judged on this tree

- **[A1] MET.** The submit-time default is `do_v1` (`DEFAULT_RUN_SUBSTRATE`,
  run-substrate.ts), recorded with the per-run image pin for every new container
  run before its Workflow exists (`runMarks` → `recordRunSubstrate`, the pin read
  from the FactorySandbox DO's `ctx.container.images`), routed per run under the
  seam (`routedSandboxBinding`) with the per-job instance size
  (`INSTANCE_BY_JOB_KIND`); wrangler.toml declares exactly one container
  application — `ticks-factory-sandbox` on `scheduling_policy = "durable_object"`,
  no `max_instances` — pinned by `TestBundleDeclaresOnlyTheFactorySandboxApplication`.
  Verified live from inside this review's own container: PID 1 is `sleep infinity`
  (the new class's `BOOT_ENTRYPOINT`), `/var/lib/ticfac/image.env` carries
  `TICFAC_VERSION=ce124e8ca47a` — the run's submit-time pin — and
  `/usr/local/bin/ticks-proc` (x9d's runner) is present, while main has been
  deployed twice since the run submitted.
- **[A2] MET.** The staging Worker that rehearses a deploy under a live container
  exists (`cloudflare/src/staging.ts`, its `/image` route answering the running
  image beside the deployed one), nmd closed on that staging proof, and the
  constructor re-arm of `setInactivityTimeout` (the DO-restart-on-deploy risk the
  epic named) is implemented and unit-tested. Corroborated live: the two mid-run
  deploys of main did not move this run's containers, which are on their
  submit-time image.
- **[A3] MET, the physical deletion by construction post-merge.**
  `rollout_active_grace_period` + its pin test, `internal/factory/rollout_held.go`
  (+tests), `rollout.go`'s confirm wait, `--skip-rollout-wait`, `--keep-images`,
  `VerifyContainerCapacity` and the workflow's rollout wait are deleted; the only
  remaining mentions are comments and the ban-list tests. The old application
  `ticks-orchestrator` is deleted by the deploy behind a live-run guard that reads
  the factory's own D1 (a live run with no substrate row holds it open, and so
  does anything unreadable; `legacyapp.go`), tested for delete/hold/unreadable/
  absent against the stateful fake wrangler. The deletion can only fire at the
  first deploy AFTER this merge — the retro names that summary line as the
  observable for the close-out read, and I checked that the guard's SQL answers
  live 0.x runs correctly: an explicit `sdk0` ask records no row, so live runs on
  it hold the application, and a queued submission cannot carry one.
- **[A4] MET.** `livePinsSQL` reads every live run's pin from D1; an unreadable
  answer stops the prune rather than guessing; `selectImagesToPrune` keeps every
  protected digest, tags sharing a kept digest, and unreadable images — tested
  at both the selection and the D1-read level.
- **[A5] MET.** The run's integrated gate evidence is exit 0 for both halves at
  the dax and rlx merges; CI is green on both integration heads (11/11 checks,
  verified against the GitHub API), and CI's typescript job runs the vitest suite
  the per-tick gate deliberately omits. I re-ran the evidence myself on this exact
  tree in this container: `gofmt -l` clean, `go vet ./...` clean, `go test -short`
  green for internal/factory, internal/cli and internal/exec/subprocess (the
  zombie-repair package, in exactly the PID-1-reaps-nobody container it targets),
  and the full cloudflare suite `pnpm exec vitest run` — 1783/1783 across 77 files.
  The hn6-style cloud run the item names is this run itself: a do_v1 run whose
  worker tick (dax) merged and closed end to end.

## Judging the tests

The suite exercises the change, not a shadow of it: the deploy tests drive the new
deploy end to end through the extended fake wrangler (`containers delete`, the two
`d1 execute --json` reads, the two-application listing), including the legacy-app
hold under live runs and under an unreadable count; the prune tests cover the pin
protection at both levels; run-workflow's tests now route every orchestration test
through `SANDBOXES_V1`; run-substrate's tests pin the new submit-time default, the
explicit `sdk0` carry and the queued-0.x refusal; phase0-compat's pins were
rewritten for what replaced the deleted machinery rather than dropped. A green
suite that never touched the change would have been caught here — it was not the
case.

## The standing backlog findings

The previous review's three findings (xtd — the `run_image` stamp names the
deployment's image rather than the run's pin; oyn — the Go SQL restates
`ACTIVE_RUN_STATES` with nothing pinning the two copies together, which I
confirmed still match today; 2b2/8ct — the open backlog tick whose subject the
epic's gate repair fixed), the close-out's two (dob — retire the `sdk0` opt-out
and residual class once the 0.x app is gone; 3hw — a cloud close-out cannot write
`.tick/learnings.md`, whose compaction therefore rides in the retro's appendix
ready to apply), and dax's two (8ct, oog) are all absorbed as owned backlog ticks.
None gates an acceptance item on this tree, and my tick carries no deferred
finding to re-report.

Two residual cosmetic defects in the epic's own work, both actionable, neither
blocking, filed below.

```findings v2
[
  {
    "kind": "defect",
    "title": "wrangler.toml keeps the deleted 0.x application's comment above the new block",
    "severity": "low",
    "body": "Tick dax deleted the `[[containers]]` block for the Sandbox class but left its comment paragraph in place, so the FactorySandbox block's comment now opens with \"The container application the Sandbox class runs\" and describes an `image =` key no block carries, directly above text saying it is the ONLY container application — internally contradictory, in the file a deploy operator edits first. Delete or rewrite the orphaned paragraph (the image path it explains lives on in the `[containers.images.factory]` comment).",
    "evidence": "cloudflare/wrangler.toml:92-98 (the orphaned head of the deleted block, now merged into the FactorySandbox comment)"
  },
  {
    "kind": "defect",
    "title": "runningImage()'s doc comment sits above lastStop() and is orphaned",
    "severity": "low",
    "body": "Two consecutive doc comments precede lastStop() in factory-sandbox.ts; the closer one wins, so runningImage() — the method umq [A2] names as the deploy-did-not-move-it proof — carries no doc, and a reader of lastStop() first sees an image doc that does not describe it. Move the five-line comment down to runningImage().",
    "evidence": "cloudflare/src/factory-sandbox.ts:688-704 (the runningImage doc block immediately above lastStop's own)"
  }
]
```

REVIEW-VERDICT: READY

STATUS: DONE — round-2 re-review of epic umq at the rlx close-out head: the delta since the READY review (l76) is the retro document and run bookkeeping only, and the integrated tree satisfies [A1]–[A5] on my own re-run (go and the full 1783-test cloudflare suite green, CI green on both integration heads) and on live in-container evidence; two low-severity comment defects filed as backlog.
