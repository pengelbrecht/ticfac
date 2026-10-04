<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-2/lkq`, base `3643b3b7e76332a7731d134804940893d751bb31`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk --json show hn6`
> - the agent ran `tk --json children hn6` (2 times)

# The bundle carries a refused-last-try golden: next step and reason now have fixture coverage (tick lkq, hn6 attempt 2)

Tick lkq was created by absorbing a finding from c2u's report: all three goldens
in `contracts/status-model.json` carry `next_step` null on every try — tick
378's anchor stops at the reason on purpose (the dashboard golden's one refusal
was superseded by a live try and no next step is derivable from it), so the one
field the first-use bugs are about had zero fixture coverage. Every renderer
downstream of the model — the phone page (A5), z7w's properties, any future
golden test — had no document that shows the field rendering, and c2u had to
shape a next step in test code to pin the tick view's "— next:" line. I cut the
fourth golden the fix shape names, bound it with a new anchor and with the
existing derivation guard, and cut bundle 1.3.0. One commit on
`tick/hn6/attempt-2/lkq`, head `1d302371ce8ecf11da2961c2ced7b5d63dff6f5d`.

## What changed (commit `1d30237`, five files)

- `contracts/status-model.json` — the new golden
  `status_model_refused_last_try`, a snapshot of epic yoh mid-waves where tick
  78v's LAST try was rejected by its worker and the run parked the tick for a
  person:
  - the refused try carries the run's own reason — the `rejected` feed line's
    detail ("rejected: the rule still keys on the substrate — …", 139 chars, so
    the 160-cut is a no-op, the same detail `decorateTries` copies) — and its
    next step is `ticfac settle yoh 78v 2 --release "<who>"`, derived from the
    golden's own facts: the `run_held` line in the tail names the tick and
    attempt, and `nextStepOf` mirrors that line as the settle command, the same
    sentence the document's own `waits_on` and `attention` entry carry as the
    `unblock_command`. The field is derivable from the golden's bytes, not
    decorative.
  - the shapes around it a renderer meets, all internally coherent: a
    superseded first try (outcome `dispatched` — the records state the dispatch
    and no evidence answers it, `tryOutcome`'s own answer), a closed neighbour
    (7eq) whose gate evidence backs its 720s duration, the workers panel empty
    because the rejected attempt was torn down, the held-for-person wait and
    attention entry, and the verdict still `healthy` — a hold that needs a
    person is attention, not degradation (`buildVerdict` reads degraded causes,
    not attention).
  - the dashboard golden and its try anchor are untouched, per the tick.
- `internal/statusmodel/contract_test.go` — `TestTheContractBindsTheRefusedLastTryGolden`,
  the new anchor, five claims: the golden exists under
  `refusedLastTryGoldenName` (pinned here, like the dashboard name); the schema
  admits it; the Go Model round-trips it; it stays POPULATED (a refused last
  try with a null reason or null next step fails here — the decay this tick
  found the bundle in can no longer re-enter silently); and the next step is
  DERIVABLE (it must be an attention entry's own `unblock_command`, so a
  next step nobody's fact states fails here). `TestEveryGoldenAgreesWithThePipelineDerivation`
  now runs its rules over the new golden as its own subtest — including the
  placement rule this tick is about: a next step on any try that is not the
  last of a refusal fails there.
- `contracts/bundle.json` — version 1.2.4 → 1.3.0, the `status-model.json`
  digest re-cut (`4ff079db…` → `f0acc5ef…`), ledger entry `1.3.0 →
  d7186399…` recorded and re-derived as sha256 over version + sorted digest
  lines.
- `contracts/CHANGELOG.md` — the 1.3.0 MINOR entry: a golden (a case) was
  added, so an unchanged consumer stays correct but the bundle is no longer
  complete; the schema is unchanged and no consumer has anything to do.
- `cloudflare/contracts.pin.json` — `bundleVersion` 1.2.4 → 1.3.0 (step 5 of
  the bump procedure).

## What I ran (all green, on tree `1d30237`)

- **Red first**: `go test -run TestTheContractBindsTheRefusedLastTryGolden
  ./internal/statusmodel/` before the fixture existed FAILS with "the contract
  carries no golden named \"status_model_refused_last_try\"" — the test is
  load-bearing, not decorative.
- After: `go test -short -timeout 20m -count=1 ./internal/statusmodel/
  ./internal/contracts/...` — ok. The derivation guard's
  `status_model_refused_last_try` subtest runs and passes (verified with `-v`:
  the golden is exercised, not skipped); the parity suite re-admits the new
  golden and round-trips it through the Go Model.
- `go test -short -timeout 20m -count=1 ./internal/cli/` — ok (the other Go
  reader of the model).
- `go run ./cmd/contracts check` — ticfac's bundle verifies, ticks-owned files
  match the pin (offline check).
- TypeScript half: `pnpm contracts:check` ("16 contract(s) at bundle 1.3.0"),
  `pnpm lint` clean, `pnpm exec tsc --noEmit` clean, and the full factory
  vitest suite — 71 files, 1681 tests, all passed (the TS reader validates
  every golden against the schema, new one included).
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0: gofmt clean,
  `go vet ./...` clean, the whole-repo short suite ok, 45 packages.

## What the next tick has to know

- The bundle is now **1.3.0**: any vendor pin that references 1.2.4 by exact
  value moves with the next fold, and `version_digests` now carries 1.3.0 —
  do not re-cut 1.3.0.
- The golden's `next_step` value is the **attention mirror** branch of
  `nextStepOf` (the settle command for a tick the run holds for a person). The
  other three derivable shapes — `retrying (try N)` behind a later dispatch
  line, `held — see needs-you` behind a struck-out attempt, and the ladder's
  `the run will retry or escalate the tier` — are still shown by no fixture.
  Any renderer test that needs one of those can shape them the way c2u did, or
  cut a fifth golden through the same procedure; the anchor here only binds
  the mirror branch it demonstrates.
- c2u's `watch_drill_test.go` still shapes `NextStep` in test code (finding
  below): the new golden's 78v can now serve that pin without shaping, but the
  change belongs to c2u's seam, not this tick's.

```findings v2
[
  {
    "kind": "proposal",
    "title": "c2u's drill test can pin the next-step line on the new golden's 78v, unshaped",
    "severity": "low",
    "body": "Bundle 1.3.0's status_model_refused_last_try golden carries a real refused last try with reason and next step (tick lkq). c2u's tick-view test still shapes NextStep onto the dashboard golden's 46x because no fixture carried the field when it was written; switching that one pin to the new golden's 78v removes the last shaped value from the renderer tests and exercises the drill view on the fixture the model itself ships. Small change, c2u's seam only, no product code involved.",
    "evidence": "internal/cli/watch_drill_test.go:33-37 (the shaping); contracts/status-model.json golden 'status_model_refused_last_try' tick 78v (the fixture to read instead)"
  }
]
```

STATUS: DONE
