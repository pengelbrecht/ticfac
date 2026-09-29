<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1/r5i-run_0f64adbcecb5484fb20cbe33d5e97e17`, base `e89c11bee73779bfc24693a862f18a1e9130ea42`, harness `pi` exited 0, 1 work commit(s), 1 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`

# Status model declares the dashboard fields (tick r5i, run_0f64adb attempt 1)

r5i arrived at this attempt already implemented in its own base. The base
e89c11be is the previous attempt's end state (its parent is d4215948, the
second attempt's audit commit; the deliverable itself is 82cf262d and
216e9d1c from run_1960aa14, folded in by that run). This attempt verified
every acceptance criterion over the pinned commands on the post-merge tree,
audited the whole tick description field by field, closed the one real gap
the audit found, and reports that as its work. The earlier reports
(committed at b6effb18 and e89c11be) describe the implementation itself;
this one does not repeat them.

## What the audit verified on the base (all green, before any change)

- Acceptance [1]: `contracts/status-model.json` (the contract moved from
  internal/statusmodel/contract.json to the bundle at tick 4i8, before this
  tick was attempted) carries every hn6 field — tick `pipeline`,
  `parent_tick_id`, `duration_seconds`, `findings`, `report`, try
  `tier`/`reason`/`next_step`, worker `handle` and `activity`,
  `health.verdict`, `cost.lines`, top-level `recent` and `epic_title`, tick
  `gloss` — as required-and-null/required-and-empty inside schema_version 1,
  with a closed schema (additionalProperties false) and the per-role stage
  lists documented in the `pipeline` description. The `dashboard` golden is
  populated at every anchor the tick's spec names (4-tick epic: closed
  implement tick all-done, in-flight tick with gate active, repair child
  under `parent_tick_id`, closeout with ci active; rejected try with
  tier/reason/next_step; worker with activity buckets and a handle; healthy
  verdict recovered `[{what:"net",count:14,seconds:null}]`; one metered
  workers-ai line beside one unmetered claude line with usd null; 5 recent
  events), and the three new negatives (unknown pipeline stage, metered
  false with a number usd, unknown verdict state) are refused. Both
  pre-existing goldens carry the new fields at their honest empty values.
- Acceptance [2]: the five stub builders exist (pipeline.go `decorateTicks`
  after buildWaves, verdict.go `buildHealth` moved unchanged plus
  `buildVerdict` after buildWaits, cost.go `buildCost` moved unchanged plus
  `lines: []`, activity.go `decorateWorkers` no-op plus `TranscriptActivity`,
  report.go `decorateReports` no-op plus `AttemptReports`), Build calls every
  one, and localStatusModel/cloudStatusModel wire `Activity`/`Report`/nil
  and `WorkerCost` from the factory record's `cost_usd` (which
  cloudflare/src/runs.ts and db.ts really carry). `recent`, `epic_title` and
  `gloss` are filled in build.go/buildTick; the schema version stays 1.
- Acceptance [3] and the tick's named tests: all pass (commands below).

## What changed (one commit, 58c405df)

The audit found one real gap: acceptance [2]'s wiring sentence — "wired from
localStatusModel/cloudStatusModel" — was guarded nowhere. At wave 1 the
dashboard readers answer nil, so `ticfac status --json` emits an identical
model whether the gathering passes them or not. If the wiring dropped, every
wave-2 fill would land in readers no gathering ever calls, `status --json`
would leave activity, report and the cost lines' river null/empty in
production forever, and every suite would stay green: the package tests pin
the readers, the renderer tests pin the golden, and internal/cli's test
file — whose own header declares "What this test pins is the WIRING" — did
not know the new sources existed.

- internal/cli/status_model.go: `statusBuild`, the model's assembly point as
  a seam in the file's established `epicGraph`/`statusCI` shape; both
  gathering functions hand their Sources to it instead of calling
  statusmodel.Build directly. (Decision, standing order "internal API shape:
  decide and log": a function var, not an interface — the two existing
  seams in the same file set the pattern, and a test swaps it exactly the
  way it swaps those.)
- internal/cli/status_model_test.go: `captureStatusSources` plus two tests.
  The local leg pins that localStatusModel passes non-nil `Activity` and
  `Report` readers. The cloud leg runs the full `status --json` command
  against the fake factory in both shapes — record with `cost_usd` and
  record without — and pins that a cloud gathering passes nil readers and
  `WorkerCost` equal to the record's own number sourced "gateway" only when
  the factory carried one; going through the command means the record's
  `cost_usd` is pinned through the same fetch the liveness answer rides on,
  not just through the function's parameter.

Each guard leg was proven red on its own mutation and green on the tree:
the `Activity` wire dropped, the `Report` wire dropped, the `WorkerCost`
pass dropped, the river renamed, and the `cost_usd` json tag dropped from
cloudRunRecord — five mutations, each caught by the leg that guards it,
each reverted. Both files are in the tick's own Files list; the tests guard
the tick's own deliverable, and no wave-2 tick needs to touch them: the
pin is the wire, not the stubs' nil answers, so filling a reader does not
move it.

## What I ran (all green, on tree 58c405df)

- `go test -count=1 -short -timeout 20m -run 'TestTheContract|TestTheGoType|TestBuildEmitsTheDashboardFieldsEmpty' ./internal/statusmodel/` — ok (the pinned acceptance command; `TestTheGoType` matches nothing in this package since 4i8 — its substance, the golden round-trip, was bound under the command's `TestTheContract` arm by the previous attempt, and I re-verified that binding stands).
- `go test -count=1 -short -run 'TestTheStatusModel' ./internal/contracts/parity/` — ok: goldens admitted including `dashboard`, the Go Model round-trips every golden, 10 negatives refused with the pinned messages, boundary pinned.
- `go test -count=1 -short -run 'TestStatusModelLocalWiringPassesTheDashboardReaders|TestStatusModelCloudWiringCarriesTheHostCost' ./internal/cli/` — ok (the new guard, both cloud legs).
- `go test -count=1 -short -timeout 20m ./internal/statusmodel/ ./internal/cli/` — ok (the tick's regression surface).
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0, gofmt clean, `go vet ./...` clean, 44 packages ok. Run in this worker container, which carries live factory credentials in its environment; `TestCloudBranchNamesTheConsequenceOfANakedContainer` passes here, so #123's runenv scrubbing still holds the 9dy finding's root cause closed.
- `go run ./cmd/contracts check` — ok (the bundle verifies offline at 1.1.0).
- `make ts-gate` — ok (biome clean, `contracts:check` verifies 16 contracts at bundle 1.1.0, `tsc --noEmit` clean).
- `cd cloudflare && pnpm exec vitest run test/status-model.test.ts` — 6 passed (the phone page's tests over the same bundle, the `dashboard` golden included).
- Five mutation runs proving each new guard leg red on its own defect, each reverted before the commit.

## What the next tick has to know

- The wave-2 fill points are unchanged and remain the five stub files only:
  `pipeline.go` (`decorateTicks`), `verdict.go` (`buildVerdict`), `cost.go`
  (`buildCost` + `Sources.WorkerCost`), `activity.go` (`decorateWorkers` +
  `TranscriptActivity`), `report.go` (`decorateReports` + `AttemptReports`).
  `Build` already calls every one; no wave-2 tick edits build.go.
- The wiring guard in internal/cli survives the wave-2 fills untouched: it
  asserts the readers are PASSED, not what they answer. Only a change to
  status_model.go's wiring itself moves it.
- `Health` and `Cost` are not comparable with `==` (slice-bearing fields);
  compare the counters, as build_test.go does.
- Cloud runs pass nil `Activity`/`Report` and their workers stay null; the
  wave-3 renderers must render the null honestly, not as "no activity".
- The `dashboard` golden is the rendering fixture for the wave-3 ticks and
  the phone page; its name is pinned Go-side by
  `TestTheContractBindsTheDashboardGolden`. The cost-line "usd must be null
  when metered is false" rule is schema-pinned (anyOf) and refused by a
  negative — never print $0.00 for unmetered spend.
- Any contract change re-cuts `version` in contracts/bundle.json with a
  CHANGELOG entry in the same commit, and the pins follow; the r5i cut is
  1.1.0 and nothing here moved it.
- Backlog tick 9dy's disposition (close as superseded by #123, or apply the
  setenv anyway) still belongs to its owner; this attempt only adds the
  evidence that the test passes in a credentialed worker container today.

```findings v2
[
  {
    "kind": "proposal",
    "title": "A re-dispatched tick whose work is already in its base cannot collect green",
    "severity": "medium",
    "body": "When a run replays a wave whose tick a previous run already merged into the integration branch, the fresh dispatch cuts that tick from a base that already carries its deliverable. On the cloudflare-sandbox substrate a worker that verifies the tree and reports honestly still leaves only the report commit, which collect classifies as no-commits (the report-only branch), so the tick climbs the refusal ladder to a person unless it finds new in-scope work. The carried-work rule (tick isp, deliverCarriedWork) only recognises attempts cut from a carried head, not fresh re-dispatches over an already-merged tree. r5i has now hit this on three consecutive dispatches: run_1960aa14's re-dispatch (run_aaf6886d) and this run (run_0f64adb, base e89c11be whose parent is d4215948) each had to find a real in-scope gap to close so the attempt could leave a change beyond the base at all.",
    "evidence": "internal/exec/cloudflaresandbox/collect.go:344 reportIsOnlyChange and the reportOnly no-commits case; internal/reconcile/dispatch.go deliverCarriedWork's resumed_from guard; this run's base e89c11be already containing the r5i deliverable (82cf262d, 216e9d1c, d4215948)"
  }
]
```

STATUS: DONE
