<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-11/d23`, base `a15a070923ea69e2955520a0f3c5553cf744311d`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Untriaged drafts now reach needs-you and gate across runs (tick d23, hn6 attempt 11)

Tick d23: the WaitFinding attention read only the newest run's records, so a
prior run that died before its close-out — one that raised no `run_held` line,
because the hold its close-out would have raised never happened — left its
untriaged drafts invisible to needs-you, although a person's decision about
them was standing; and the close-out's findings gate read only the run's own
store, so a run resumed under a NEW id never gated on the dead run's drafts:
they could silently never gate anything.

I implemented both halves test-first — the statusmodel tests failed on
exactly the named gap (no attention from a dead prior run's drafts), and the
reconcile test failed with the run COMPLETING over a standing inherited draft
— then made them pass. One commit, `ed90778`, on `tick/hn6/attempt-11/d23`:

## What changed

- `internal/statusmodel/build.go` — the WaitFinding block in `buildWaits` now
  walks EVERY run's findings, newest run first, with prior runs newest-first:
  one attention per prior run that still carries live untriaged drafts, each
  naming the run and carrying the triage command addressed to that run's own
  store (`TriageCommandForRun` — `ticfac triage <epic> --run-id <run>`),
  regardless of the newest run's liveness (a prior run cannot triage its own
  drafts whatever the newest run is doing). Findings dedupe newest-first by
  content key: a key a newer run's records carry — adopted into the live
  run's store, or decided, its decision standing — answers for every older
  run's copy. A prior run whose records name no run id is skipped. The newest
  run's own attention is unchanged (asked only when it can no longer triage
  them itself).
- `internal/reconcile/inherit.go` (new) — the adopt decision, made for the
  run: `adoptInheritedFindings()`, a boot sweep taking every ENDED sibling
  run's untriaged proposals into this run's own store before anything is
  planned. Ended means a terminal checkpoint, or the answer of the same host
  the claim staleness asks (`Options.ClaimHolder`); alive and unknown keep
  their drafts, and only this epic's runs are read (the base branch folds
  other epics' runs in). ADOPT, not gate on the foreign drafts: adopted, the
  run's own rules decide them (`decideUndecidedFindings`) and its close-out's
  gate holds for a person what the rules cannot — the same funnel a finding
  of its own rides; gating on the foreign drafts instead would hold a person
  over what the run's rules already answer.
- `internal/reconcile/takeover.go` — the adoption core is now shared
  (`adoptRunFindings`, the claim takeover's `adoptFindings` and the sweep both
  call it), and gains the cross-run dedup the report path keeps
  (`decidedElsewhere`): a draft another run already decided — same content
  key, promoted or discarded — is linked and never re-decided, which would
  mint the same finding as a second tick (hn6's oro and log). The feed line's
  wording is parameterised by the path ("with the claim on X this run took
  over" vs "at this run's start, before anything is planned"); the takeover
  tests' assertions (holder, key in the line) still hold.
- `internal/reconcile/reconcile.go` — the sweep is called in `Run` right
  after the post-fold store fetch, before `closeDuplicatePromotions`, the
  graph read, `answerNotReadyReview` and the plan — so inherited drafts are
  in this run's store before any decision point that can decide them, and
  the close-out's `gateCloseoutOnFindings` (unchanged) covers them because
  they are now the run's own.
- Tests: `internal/statusmodel/build_test.go` —
  `TestPriorRunUntriagedFindingsReachNeedsYou` (the named gap: a dead prior
  run's draft is attention with the per-run command; a nameless prior run
  raises nothing) and `TestANewerRunsFindingCopyAnswersForTheOlderRuns` (the
  dedup, three subtests). `internal/reconcile/inherit_test.go` —
  `TestARunUnderANewIdAdoptsADeadRunsDraftsAndGatesItsCloseOutOnThem` (a dead
  sibling's draft is adopted with its discovery kept and HOLDS the close-out;
  a still-running sibling's and another epic's drafts are not adopted) and
  `TestADraftAnotherRunAlreadyDecidedIsNotAdoptedAgain` (the decided link,
  said in the feed as `finding_duplicate`).

## What I ran

- `go test ./internal/statusmodel/` — green (the two new tests, then the whole
  package).
- `go test -short ./internal/cli/` — green (the watch/overview renderers take
  the new attention entries as one amber line per hold, each with its own
  command).
- `go test -run 'TakeOver|Finding|Adopt|Absorbing|Absorbed|Duplicate|Untriaged|Triage' -parallel 4 ./internal/reconcile/` — green (212s).
- The full `./internal/reconcile/` suite, `GOTEST_PARALLEL=4`/`GOFLAGS=-p=2`
  under `nice`: green except `TestACloseoutBlockedOnRedCIIsRepairedAndRedispatchedWithoutAPerson`
  and `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree`, which I
  reproduced in a separate worktree at this branch's base (a15a070) — they are
  exactly the two tests the OPEN backlog tick e1k names as failing
  deterministically at base; not this tick's change, nothing new to file.
- `make gate` — exit 0, whole repo.

## What the next tick has to know

- The statusmodel dedup is DIRECTIONAL: only a NEWER run's copy answers for an
  older run's copy (any status). An OLDER run's decided copy never suppresses
  a newer proposed one — a newer proposed copy gates the newer run's own
  close-out, so needs-you keeps asking. If a cross-run triage wrinkle ever
  needs smoothing (a person triaging the old store after the new run
  adopted), it is the `decidedElsewhere`/`linkDecidedElsewhere` pair to grow,
  not the model.
- The sweep makes the newest run's store the single place its close-out gates
  on; `gateCloseoutOnFindings` was deliberately left unchanged. If a future
  tick wants inherited drafts decided EARLIER than the close-out dispatch
  (a gating one lands "after review" when decided there), the decision point
  to move is `decideUndecidedFindings`'s call sites, not the adoption.
- A run re-entered after completion (the PR-readying path) returns before the
  sweep: it adopts nothing, and inherited drafts stay a person's via
  needs-you with the per-run command.

```findings v2
[
  {
    "kind": "defect",
    "title": "A cloud run's own triage command names the local run id, which holds no drafts",
    "severity": "medium",
    "body": "For a cloud run (id run_…), every surface that spells the clearing command for the run's OWN untriaged findings — the close-out hold's triagePointer and the status model's WaitFinding attention — names `ticfac triage <epic>`, whose --run-id default resolves to the local id epic-<epic>: a store a cloud run never writes. A person following the named command finds \"run epic-<epic> has no findings drafted for triage\" and must discover --run-id on their own, while the hold the command was meant to clear stands. TriageCommandForRun already exists and is what this tick wired for prior runs' drafts; the current-run sites are the remaining half of the same seam.",
    "breaks": {"item": "A2", "check": "go"},
    "evidence": "internal/statusmodel/commands.go:35 (TriageCommand) used at internal/statusmodel/build.go:805 and :1007; internal/reconcile/findings.go:370 (triagePointer in the close-out hold); the wrong default resolves at internal/cli/findings.go:121 runID = runIDOfEpic(epicID)"
  }
]
```

STATUS: DONE
