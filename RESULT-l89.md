<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1/l89`, base `6a32e734a9a37c53468031c2cd878d01f78e7014`, harness `pi` exited 1, 1 work commit(s), 0 uncommitted path(s)._

# RESULT-l89 — the run now applies the tracker fix a BLOCKED answer names, instead of redispatching into the wall

## What changed

Tick l89 asked: when an implement-tick report is BLOCKED and its named fix is a
tracker-record write, the orchestrator applies it itself through its own tracker
authority and closes the tick, instead of redispatching. Implemented, test-first.

**`internal/reconcile/tracker_edits.go` — `acceptTrackerEdits`.** The collect's
tracker-edit admission previously applied a proposal only behind a DONE or
DONE_WITH_CONCERNS answer; a BLOCKED answer's proposal was ignored entirely and
the answer went to the blocked ladder (escalate / decide / hold), which is the
redispatch-into-the-wall this tick records — yjq's three attempts each ended
there with the edit in hand. Now a BLOCKED answer whose fix is the tracker write
itself — the typed `tracker-edits` block — commits nothing — is taken as the
same delivery a DONE answer's proposal is: the run applies the edits through its
own durable writer, the integrated gate runs over the edited branch, and the
tick closes behind it. The ladder is never entered and no blocked-answer record
is written.

Two edges keep the disposition honest:

- A BLOCKED answer that **also committed work** keeps the ladder (verdict
  ready-to-merge + BLOCKED still goes to `answerBlocked`): its question rides on
  work a person may still have to settle, not on an empty branch.
- A question in an **always-ask class of the standing orders** is never answered
  by applying what the report happened to carry: `acceptTrackerEdits` classifies
  the BLOCKED detail with the same `alwaysAskClass` reader the ladder uses, and
  on a hit records `tracker_edit_refused` and lets the question hold.

A blocked proposal the run **refuses** (it drops an item the record marks, names
a closed record, falls outside the epic) is not converted: `tracker_edit_refused`
is recorded with the reason, and the collection is left as it arrived, so the
existing ladder answers the question — a refused edit leaves a real question,
which is not a missing report.

**`internal/reconcile/dispatch.go` — the collect's what-the-worker-SAID check.**
That check re-enters the ladder for any ready-to-merge collect whose
role-result status is BLOCKED/NEEDS_CONTEXT, which would have re-litigated the
new delivery. It now skips a converted delivery via `trackerEditDelivery` — the
converted state's signature (ready-to-merge over a branch with no head of its
own, proposals on the report), reachable only through the conversion:
`deliverCarriedWork` always names a head, and the executor's own no-commits
never reads ready-to-merge. NEEDS_CONTEXT answers are untouched: the worker
asked for information, and its fix is not the record.

## What I ran

- New tests, all in `internal/reconcile/tracker_edits_test.go` with three new
  fake-runner modes (`tracker_edit_blocked`, `tracker_edit_blocked_drop`,
  `tracker_edit_blocked_ask`):
  - `TestABlockedAnswerWhoseFixIsTheTrackerEditItProposesClosesOnTheRunsOwnWrite`
    — run completes; the epic's acceptance carries the proposed re-flow; the
    note is appended; b1 closed; `tracker_edited` + gate passed; no
    rejected/escalated/decide/held/redispatched stage for b1; no blocked-answer
    record.
  - `TestABlockedAnswerWhoseProposedEditIsRefusedIsAnsweredByTheLadderNotTheApply`
    — the dropping edit is never applied (the record keeps the held criteria);
    `tracker_edit_refused` names the dropped item; the ladder's decide step
    dispatched the second try, which closed b1 once.
  - `TestAnAlwaysAskQuestionBesideAValidNamedEditStillHoldsAndIsNotApplied` — a
    BLOCKED detail naming credentials plus a fully valid edit: the question
    holds (`RefusedNeedsHuman`), and no edit/note write reaches the tracker.
  All were watched failing before the fix (the first two) and green after.
- `go test -run 'TrackerEdit' ./internal/reconcile/` — green (covers the four
  pre-existing tracker-edit tests beside the three new ones).
- Blocked-ladder sweep (`Blocked|AlwaysAsk|DecideAndLog|Held|Hold|Escalat|...`)
  and the collect/delivery sweep (`Collect|Deliver|Carried|RoleJob|Finding|Absorb`)
  in `internal/reconcile/` — green except the pre-existing failure below.
- `go test ./internal/exec/subprocess/` — green.
- `make gate` (gofmt, `go vet ./...`, short suite across the repo) — green,
  twice.

## What the next tick has to know

- The refusal semantics are now status-dependent by design: behind a DONE
  answer a refused proposal is `missing-result` (retried); behind a BLOCKED
  answer it is left to the ladder. Both record `tracker_edit_refused` first.
- A later try of the decide rung that answers BLOCKED again with the valid
  named fix is also applied by this path (before `decidedBefore` can hold it):
  a fix the run can apply mechanically outranks the asked-again rule. Only an
  always-ask question still holds.
- The always-ask guard reads the standing orders fresh at the collect
  (`r.standingOrders()`), matching the ladder's own behavior.
- Nothing outside `internal/reconcile` changed; no prompt, contract or
  lint-report change was needed — the typed `tracker-edits` block and its
  validation already existed.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "Close-out BLOCKED over red CI is not resumed into the repair job; the supervised run ends failed",
    "severity": "high",
    "body": "Both closeout red-CI tests fail deterministically (repeated runs, standalone) on this tick's base 6a32e734 and on origin/main tip e0658034: after the close-out answers BLOCKED over code whose CI the run reads red, the supervised run ends failed at closeout_dispatched_over_red_ci instead of the next incarnation's admission repairing the red CI and dispatching a fresh close-out over the green. Unrelated to this tick's change (the closeout_red fixture's reports carry no tracker-edits block and the path is closeout-epic-only); it reproduces at the base, so it predates this branch.",
    "breaks": {"check": "go test -run TestACloseoutBlockedOnRedCI ./internal/reconcile/"},
    "evidence": "internal/reconcile/closeout_test.go:658 TestACloseoutBlockedOnRedCIIsRepairedAndRedispatchedWithoutAPerson and internal/reconcile/closeout_carry_test.go:42 both end failed with the RefusedCloseoutOverRedCI message on base 6a32e734 and on main e0658034"
  }
]
```

STATUS: DONE
