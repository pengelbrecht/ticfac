<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-7/quz`, base `f8ef001dcfd0bf180031efda81a09b8f73562a92`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# The final-review hold names the run again, never a settle (tick quz, hn6 attempt 7)

Tick quz (created by absorbing finding 5300f9f6, gating A2): the
`land_review_not_ready` hold fires after the close-out's attempt was
dispatched, so its `run_held` line carries an attempt and the per-kind
decision (`statusmodel.HoldClearingCommand`, tick gf0) named the settle that
attempt addresses — but releasing that attempt clears nothing: the hold is the
final review's NOT READY verdict recorded on the PR, which the next resume
re-reads and holds on again. I reproduced that test-first (both new tests
failed on exactly the settle the tick names: `ticfac settle 2jn rrl 2
--release "<who>"`), then made the per-kind decision answer the run again —
the same shape the world holds (claim_width, foreign_claim) got — and gave the
watch's hold alert its own sentence for the kind. One commit on
`tick/hn6/attempt-7/quz`, head `67ba33b42ad275c573467faba672b47d5f45ab4e`.

## What changed (commit `67ba33b`, five files)

- `internal/statusmodel/commands.go` — `HoldClearingCommand` answers
  `ResumeCommand(host, epicID)` for `RefusedLandReviewNotReady`: the local
  `ticfac run-epic <epic>`, the cloud resubmission `ticfac run <epic> --cloud`.
  The doc comment's per-kind list gains the bullet that says why: the hold is
  the one that fires AFTER a dispatch and its line CARRIES an attempt — a
  settle would happily take it — but the reason decides, never the attempt the
  line happens to carry; the refusal's own moves (fix and run again, merge the
  PR by hand — a re-run then finds it merged — or close it) end at a resume or
  never need the run again. Every model surface reads this one decision:
  needs-you header, the rows' next steps (`attentionCommand` /
  `priorHoldCommand`), the bare `ticfac` overview row, the notify and phone
  pages (`unblock_command`).
- `internal/cli/watch.go` — the hold alert's per-kind switch gains a
  `RefusedLandReviewNotReady` case: it names the run again and the three moves
  the line itself names, and says outright that the release the carried attempt
  might suggest clears nothing — never the default branch's
  release-and-carry-work prose. The block comment above the switch records the
  kind's shape (carries an attempt, still not released by one).
- `internal/statusmodel/build.go` — the `buildWaits` comment that words the
  per-kind decision for the header is extended to the final-review hold, so
  the prose a reader meets at the call site names the same rule the decision
  spells.
- Tests: `TestAFinalReviewHoldNamesTheRunAgainCommand`
  (`internal/statusmodel/build_test.go`) — local, cloud and prior-run variants
  of a `land_review_not_ready` line that carries the close-out's attempt;
  `TestWatchHoldAlertForAFinalReviewHoldNamesTheRunAgainCommand`
  (`internal/cli/watch_test.go`) — the alert names `ticfac run-epic qeu` and
  never `ticfac settle`, and still reads the reason and tick off the line's
  fields.

The refusal's own prose (land.go, `landingReviewHold`) was already right — it
names the real moves — so the reconciler needed no change, and neither did
the contract: `contracts/status-model.json` describes `unblock_command` as
"the one command that moves the wait on" with no per-kind pin, and its
fixture's hold is a `rejected_attempt_carries_work`, whose settle answer is
unchanged.

## What I ran

- The new tests first, both failing on the settle before the fix:
  `go test -run TestAFinalReviewHoldNamesTheRunAgainCommand ./internal/statusmodel/`
  and `go test -run TestWatchHoldAlertForAFinalReviewHoldNamesTheRunAgainCommand ./internal/cli/`.
- The touched packages whole: `go test ./internal/statusmodel/` (pass),
  `go test ./internal/cli/` (pass, 50.7s).
- The gf0-family neighbours (`TestAHoldAboutTheWorldNamesTheRunAgainCommand`,
  the absorption bound, prior-run world holds, the parked-try agreement, the
  watch hold alerts) — pass.
- `make gate` (gofmt, `go vet ./...`, the short suite across the whole
  repository, at `GOTEST_PARALLEL=4 GOFLAGS=-p=2`) — green, including
  `internal/reconcile` in its short form and the `internal/shorttest` guard.

## What the next tick has to know

- The per-kind decision for holds now has four answers: triage
  (finding_untriaged, absorption_depth_exceeded), run again (claim_width,
  foreign_claim, **land_review_not_ready**), settle (every other hold that
  carries an attempt), nil (unknown). A new refusal reason that joins
  `holdsForAPerson` must be decided in `HoldClearingCommand` and, where its
  alert prose differs, in `cli/watch.go`'s switch — the two are the only
  per-kind spellings; everything else renders the model.
- The world holds' distinguishing mark ("they carry a NULL attempt, so settle
  refuses their dash") does not generalise: `land_review_not_ready` carries a
  real attempt, the settle takes it, and the release still clears nothing.
  Key the decision on the reason, which is what the code now does.
- `TestWatchHoldAlertForAFinalReviewHoldNamesTheRunAgainCommand` and the
  statusmodel test pin this: if a future change moves this hold back into the
  settle branch, both fail naming the settle.

```findings v2
[
  {
    "kind": "defect",
    "title": "holdsForAPerson's doc bullet list omits land_review_not_ready",
    "severity": "low",
    "body": "The comment on holdsForAPerson promises that each member of the set says why it is there, and the switch does include RefusedLandReviewNotReady, but the bullet list stops at RefusedAbsorptionDepth — the final-review hold joined the set without its stated reason. A reader deciding whether a new reason is a hold reads an incomplete contract.",
    "evidence": "internal/reconcile/reconcile.go:2947-2965 (the bullet list) against the case at internal/reconcile/reconcile.go:2967-2969"
  }
]
```

STATUS: DONE
