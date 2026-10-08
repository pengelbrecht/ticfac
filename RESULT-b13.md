<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-5/b13`, base `7019eeb42de17299c0bdf49d4bc40df64cb4652a`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `7019eeb42de17299c0bdf49d4bc40df64cb4652a` is the head of the work it continued, which was cut from `8505bb2420f7434776c8c2a11232a10e89313d5e`; its work commits are counted from the carried head._

# b13 — Cost line: only when metered; a claude-sub run shows subscription window use

## What changed

The three cases the tick names were already in this branch's base (the
rendering in `dashCost`, `Cost.Subscription` in the model, the
`/api/claude-sub` read, the 2.4.0 contract re-cut, goldens, and their
tests). This attempt re-verified them against the factory's real answers
and repaired the one seam the first delivery got wrong — the seam that
decides whether the third case exists at all on a real run:

- **`ClaudeSubSnapshot.LeasedLabels` matched leases by `<run-id>-`, a
  shape no real lease carries.** The pool keys a lease by the job id of
  the job that took it, and a run's own jobs arrive under two spellings,
  both minted by the factory:
  - the door's job id, `run-<run>/tick-<tick>/attempt-<n>` — every worker
    the reconciler dispatches leases under it (`specJobID` →
    `attemptJobID`, cloudflare/src/sandbox-executor.ts), the role
    variants (`…/repair-1-r2`, `run-<run>/base-fold-2`) ride the same
    `run-<run>/` prefix the attempt protocol bounds `job_id` at
    (sandbox-dispatch.ts), and the factory's own suite pins
    `active_leases` to exactly this spelling
    (cloudflare/test/sandbox-dispatch.test.ts:477);
  - the review boot's sandbox name, `<run>-<boot>` — the one job that
    leases under its container's name (run-workflow.ts's
    `pool.lease(name)`, pinned by run-workflow.test.ts:2352).
  The old matcher matched only the second. On a real claude-sub run —
  whose implement, review and close-out workers hold pool leases under
  their door job ids — `ticfac watch` and `status --json` gathered
  `cost.subscription: null` while the jobs ran: the tick's third case was
  silent on the very run it describes, and the unit tests hid it because
  their fixtures hand-typed `<run>-<tick>-<attempt>`, the "forgiving
  fake" this repository's own learnings warn about. The real shape was
  already pinned on the other side of the seam by the real-door e2e
  (`internal/exec/cloudflaresandbox/claude_sub_e2e_test.go:143-146`
  asserts the live lease IS the job id); the Go read just never keyed on
  it.
- **`LeasedLabels` now keys on both real spellings** (`run-<run>/` and
  `<run>-`, plus the bare run id), with the separators stated as the
  whole match: `run_abc` claims neither `run_abc12`'s worker lease (the
  `/`) nor its review boot (the `-`). The comments on
  `ClaudeSubView.ActiveLeases`, `LeasedLabels` and the CLI gathering now
  state the two spellings and where each is minted, replacing the
  invented `<run>-<tick>-<attempt>` claim.

Test-first, as the repo's fixture rule demands:

- **The fixtures now reproduce the real identities** —
  `TestLeasedLabelsMatchTheRunOwnJobs` spells the run's own four leases
  (worker dispatch, gate repair, base fold, review boot) beside another
  run's and the staging door's, and asserts the other run reads its own
  two; `TestFetchClaudeSubReadsThePoolSnapshot`'s body carries one lease
  of each spelling; `TestStatusModelCloudGathersTheLeasedSubscription`
  gathers from a worker-job lease in one leg and a review-boot lease in
  another — one spelling per leg, so one drifting alone is the failure
  it is, not a wash beside the other. All of these failed on the base
  (`run_abc12's leases are [MAX2]`, the worker-job leg gathered nil) and
  pass on the fix.
- **`TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints` (new)** is the
  parity guard for the seam, the same pattern as the subscription rung's
  (`internal/profile/claude_sub_parity_test.go`): it reads
  `attemptJobID`'s and `sandboxName`'s templates out of the factory's
  TypeScript, spells ids with them for this run and for another, and
  asserts the Go matcher agrees — each spelling on its own. A factory
  change to either spelling now fails this table first, not a live run.

The rest of the delivery is verified, not changed: the three render cases
(`TestDashboardCostLineRendersMeteredOnly`,
`TestDashboardCostLineRendersTheSubscription`,
`TestDashboardRendersTheMeteredRiverOnly`), the model field
(`TestCostCarriesTheLeasedSubscription`), the degradation policy (a
factory that should answer but cannot degrades `claude-sub`; no pool, no
route, no lease are the optional state), the watch's 30s cache, the
property P3, and the goldens reading `config claude · cost Workers AI
$0.41` all stand as the base left them.

## What I ran

- The failing tests first, at the base of my change:
  `go test -count=1 -run 'TestLeasedLabelsMatchTheRunOwnJobs|TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints' ./internal/factory/`
  and the gathering leg — all three failed with the messages quoted
  above; they pass on the fix.
- The tick's named tests, green:
  `go test -count=1 -run 'TestDashboardCostLineRendersMeteredOnly|TestDashboardCostLineRendersTheSubscription|TestDashboardRendersTheMeteredRiverOnly|TestStatusModelCloudGathersTheLeasedSubscription|TestCostCarriesTheLeasedSubscription|TestLeasedLabelsMatchTheRunOwnJobs|TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints|TestFetchClaudeSub' ./internal/cli/ ./internal/statusmodel/ ./internal/factory/`.
- `make gate` — gofmt, `go vet ./...`, short suite whole-repo: **green**.
- `make suite` (short, `-count=1`, cache refused): **green**.
- Full (not `-short`) package suites:
  `./internal/cli/` (44s), `./internal/factory/`, `./internal/statusmodel/`,
  and `./internal/exec/cloudflaresandbox/` — the last including the
  real-door claude-sub e2e, which exercises the pool's real lease ids on
  the deployed Durable Object: **all ok**.
- The TypeScript half (`make ts-gate`'s checks and the suite):
  `pnpm lint`, `pnpm contracts:check` (bundle 2.4.0), `tsc --noEmit`,
  and `vitest run` — **80 files, 1880 tests, all green**.
- `go test -count=1 ./internal/reconcile/` (full, 25m timeout, per
  AGENTS.md, because the tick's surfaces touch the cloud executor path):
  three failures, **all pre-existing at this branch's base and none
  reachable from this change** — the two closeout red-CI tests are the
  owned backlog defect (ticks e1k and hai, reproduced at base), and
  `TestAResumedRunDoesNotCloseATickOnGateEvidenceFromAChangedCommand`
  is a same-second timestamp flake reproduced at base with `-count=3`
  (finding below). `make gate`'s short suite, which is the tick's
  acceptance, skips all three (`shorttest.EndToEnd`).

## What the next tick has to know

- **The lease match keys on the factory's two spellings** — the door's
  job id `run-<run>/tick-…` and the review boot's sandbox name
  `<run>-<boot>`. If the factory ever changes either speller
  (`attemptJobID`, `sandboxName`), the parity guard
  `TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints` fails first and
  must move with it — never hand-type the shapes in fixtures again.
- The rest of the base's delivery stands: the contract bundle is 2.4.0
  (`status-model.json`'s cost object carries `subscription`
  required-and-null; any further model change this epic makes re-cuts
  again); `dashCost` returns `""` when nothing is metered and the CI
  segment renders alone; ugm's layout renders the subscription from
  `m.Cost.Subscription`.
- The phone page (cloudflare/src/phone.ts) still renders the old
  "cost: not metered" recital and does not show `cost.subscription` —
  that is the prior attempt's filed proposal (low, backlog); it is not
  this tick's surface and it stays with that finding.

```findings v2
[
  {
    "kind": "defect",
    "title": "restart_test flake: same-second evidence timestamps fail late.After(dead)",
    "severity": "medium",
    "body": "TestAResumedRunDoesNotCloseATickOnGateEvidenceFromAChangedCommand fails intermittently in the full reconcile suite: the backing evidence's started_at and the dead incarnation's finished_at are both RFC3339 second-granular, so when both land in the same second late.After(dead) is false even though the evidence was produced after the gate. It reproduced at this branch's base with -count=3 (once in three), independent of any change here, and it only runs in the full suite (shorttest.EndToEnd skips it in the gate) — so it can turn a main or epic-branch CI shard red spuriously, and a red main blocks every deploy behind it.",
    "evidence": "internal/reconcile/restart_test.go:351; go test -count=3 -run 'TestAResumedRunDoesNotCloseATickOnGateEvidenceFromAChangedCommand' ./internal/reconcile/ fails 'the evidence backing the close started at 2026-10-08T19:22:39Z, before the dead incarnation's gate had even finished at 2026-10-08T19:22:39Z'"
  }
]
```

## Status

The three cases render as specified and are tested at the render, model,
gathering and factory layers — now against the lease spellings the
factory actually mints, which the first delivery's matcher missed.
`make gate` passes. Committed as 9e58be5 on `tick/ymf/attempt-5/b13`
(4 files: the matcher, its parity guard, and the fixtures and gathering
tests that reproduce the real shapes).

STATUS: DONE
