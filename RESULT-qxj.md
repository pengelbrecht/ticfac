<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-15/qxj`, base `f6a7ee2badcc0436e2264a338f4ffe987af4f3b2`, harness `pi` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# Result — tick qxj: release commands in run prose and the watch alert still omit `--run-id`

## What changed

A hold for a person names the command that clears it. Until now every release
command the reconciler's own refusals and the watch's hold alert spelled was
addressed by the epic alone, so for a cloud run — whose records live under the
factory's `run_<hex>` — the command defaulted to `epic-<epic-id>`, a store that
carries no such attempt, and refused. The same broken release ulw fixed on the
model's surface, live on the surfaces a person reads during a hold. This tick
makes the spelling one, keyed on the run whose store the attempt is actually
recorded under:

- **`internal/reconcile/settle_command.go` (new)** — `SettleReleaseCommand` is
  the one spelling of the release command: plain `--release "<who>"` when the
  run id is empty or the epic spelling (the default a bare `settle` opens, so
  the flag would be redundant), `--run-id <run-id>` otherwise. The
  `Reconciler` method `settleCommand(tick, attempt)` is what every refusal
  prose interpolates, so the address a hold's prose names is always the store
  the run writes under.
- **All eight refusal prose sites** now take the command from the helper
  instead of spelling it themselves: `dispatch.go` (the rejected-work backstop
  and the unaddressed-past-the-wall-clock hold), `finish.go` (an integrated
  attempt whose head vanished), `blocked.go` (the in-run hold, the resume
  hold with nothing committed, and the resume hold with work — the `--carry`
  variants still append `--carry-work` to the interpolated command),
  `resolve.go` (both conflict-stop sites).
- **`internal/cli/watch.go`** — the hold alert's settle branch now spells the
  command through `statusmodel.SettleCommandForCurrentRun(epicID, tick,
  attempt, runID)`, so a cloud run's alert names `--run-id run_<hex>`. The
  degenerate fallback (a hold event with no attempt, where no settle command
  works regardless) keeps its old shape.
- **`internal/statusmodel/commands.go`** — `SettleCommandForCurrentRun` no
  longer mirrors the rule; it reads the reconciler's `SettleReleaseCommand`,
  so the model's needs-you and the run's refusals cannot drift apart
  (behaviour unchanged, ulw's pinned strings intact).

Local runs are untouched in what they print: a run under `epic-<epic-id>`
(the spelling every local run's prose has always carried) still gets the
plain command, verified by the existing pinned strings.

## Tests (written first, each failing before its fix)

- `internal/reconcile/settle_command_test.go` (new) — the spelling rule's
  four cases (empty, epic-spelling, `run_<hex>`, any other id) through both
  the helper and the `Reconciler` method; plus a source guard
  (`TestNoReconcileProseSpellsTheSettleCommand`) that parses the package's
  non-test string literals and refuses any prose that spells the command's
  format itself again — the helper must hold exactly the two forms.
- One assertion added per prose site, through the existing end-to-end test
  that reaches it: the rejected-work backstop (`TestRejectedWorkDisposal
  IsBoundedByTheLadder` — asserts `--run-id r-fixture` on attempt 5), the
  unaddressed hold (`TestTheSettlementDeadlineIsSpentInTheRunsOwnClock`),
  the vanished-head refusal (`TestAnIntegratedAttemptWhoseHeadVanished
  IsRefusedNotCollected`), the three blocked-question holds
  (`TestAnAlwaysAskQuestionAtTheCeilingHoldsNamingIt`,
  `TestANoCommitQuestionAtTheCeilingIsDecidedOrHeldByName`'s resume case,
  and a new resume added to `TestAQuestionAskedAgainByTheTryToldToDecide
  Holds` for the with-work case), and both conflict stops
  (`TestASecondConflictOnTheSameTryGoesToTheLadder`,
  `TestResolvesThatNeverAnswerAreBoundedAndTheStopNamesEveryOne`).
- `internal/cli/run_cloud_test.go` — new
  `TestRunCloudHeldAttemptReleaseCommandNamesTheRun`: a cloud run's hold
  through the real attach path ends held (exit 3) with the alert spelling
  `ticfac settle epic1 a1 2 --run-id run_<hex> --release "<who>"`.
- `internal/cli/watch_test.go` — the local non-epic run id case (`r-1`) now
  asserts `--run-id r-1` (its old pinned string was the defect itself); the
  epic-spelling case asserts no `--run-id` appears.
- `internal/cli/remedy_test.go` — the printed-remedy guard learned the
  helper's whole-literal spelling (the refusals interpolate `%s`, so the
  backticked form vanished from its scan), and now parses both helper forms
  through the real CLI — which is also the proof that
  `settle <epic> <tick> <n> --run-id <run> --release "<who>"` parses.

## What I ran

- The new and extended tests, individually and together — all pass.
- `go test ./internal/cli/` (whole package) — ok.
- `go test ./internal/statusmodel/` — ok.
- `make gate` (gofmt, `go vet ./...`, short suite across the repo) — exit 0,
  45 packages ok.
- `go test -timeout 45m ./internal/reconcile/` (the full suite, since this
  tick touches `dispatch.go`) — all tests pass except
  `TestACloseoutBlockedOnRedCIIsRepairedAndRedispatchedWithoutAPerson` and
  `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree`, which fail
  identically at the base (reproduced in a clean worktree at HEAD, three
  consecutive runs) and are already filed as open backlog tick e1k.

## What the next tick has to know

- The release command's spelling lives in one place now:
  `internal/reconcile/settle_command.go`. Any new prose that names a release
  command must take it from `SettleReleaseCommand` / `Reconciler.settleCommand`
  — the source guard in `settle_command_test.go` fails a literal spelling, and
  the printed-remedy guard in `internal/cli/remedy_test.go` parses whatever the
  helper spells through the real CLI.
- `statusmodel.SettleCommand` (the explicit-address form, used for prior
  runs' holds) still has its own two format strings; only
  `SettleCommandForCurrentRun` delegates. If a third form is ever needed,
  decide which of the two files owns it before writing it.
- The full `internal/reconcile` suite on this base is red on exactly the two
  closeout red-CI tests e1k owns — do not treat a red closeout-epic test as a
  regression from a change to dispatch/prose without first checking e1k.

```findings v2
[
  {
    "kind": "defect",
    "title": "Untriaged-hold prose points at triage without --run-id for cloud runs",
    "severity": "medium",
    "body": "The same broken-address shape this tick fixes for the release command is live on the triage verb: the close-out's untriaged-findings hold tells the person to run `ticfac triage <epic>` with no --run-id (triagePointer), the watch's triage alert branch spells the same bare command, and the model's current-run needs-you does too (TriageCommand, not TriageCommandForRun). triage's default resolves to epic-<epic-id> (runIDOfEpic), but a cloud run's drafts live under .ticfac/runs/run_<hex>/findings/ — the hold's own prose even prints the correct path while pointing at the default that finds nothing, so the hold the surface points at cannot be cleared by the command the surface gives.",
    "evidence": "internal/reconcile/findings.go:371 (triagePointer); internal/cli/watch.go:349; internal/statusmodel/commands.go TriageCommand used at build.go:812 and pipeline.go:643; internal/cli/runid.go:29 runIDOfEpic"
  },
  {
    "kind": "defect",
    "title": "Reconcile suite red at base: two closeout red-CI tests fail (e1k)",
    "severity": "medium",
    "body": "TestACloseoutBlockedOnRedCIIsRepairedAndRedispatchedWithoutAPerson and TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree fail on this tick's base with the closeout_dispatched_over_red_ci refusal — the supervised run ends failed instead of the next incarnation repairing the red CI. Reproduced three consecutive times in a clean worktree at this branch's base, so it predates this change; open backlog tick e1k already owns it (filed from tick-l89's finding). Reported so the close-out's suite evidence is read with e1k in hand, not as a surprise regression.",
    "evidence": "internal/reconcile/closeout_test.go:682, closeout_carry_test.go:42; fails identically at base f6a7ee2 with -count=3"
  }
]
```

STATUS: DONE
