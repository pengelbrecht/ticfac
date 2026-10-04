<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-14/l1t`, base `1b9ba9fba861750c60ddaa0d3c430ec74758f8aa`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# l1t: attentionCommand spells the settle command a second time beside SettleCommand

## What changed

Commit `1e85000` on `tick/hn6/attempt-14/l1t`, three files, source and tests only:

- **`internal/statusmodel/pipeline.go`** — the one-line fix. `attentionCommand`
  formatted the empty-run-id settle command inline
  (`fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"", …)` at old line
  643); it now calls `SettleCommand(p.epicID, tickID, *held.Attempt, "")`, the
  same shared spelling `buildWaits` already used for the header's needs-you
  command. Behavior is byte-identical today (both spellings produced the same
  string); the change removes the second spelling, so a wording change to
  `SettleCommand` propagates to the row automatically instead of drifting it
  from the header. The comment above `attentionCommand` was updated to say the
  command is the shared spelling and the mirror keeps only the decision of
  WHICH wait stands.
- **`internal/statusmodel/commands_guard_test.go`** (new) —
  `TestEveryClearingCommandInTheModelIsSpelledOnce`, a source-shape guard in
  the repository's `go/parser` idiom (like `internal/cli`'s
  operator-strings guard): it scans the package's non-test Go sources for
  string literals whose value names a clearing command
  (`ticfac settle|triage|run-epic|run` as a word) and fails on any spelling
  outside `commands.go`. Test files and comments are exempt — comments may
  quote the commands and tests may assert the exact wording. The branch
  namespace (`ticfac/run-…`) and the schema id (`ticfac.status.v1`) share the
  prefix but not the space and never match.
- **`internal/statusmodel/build_epic_test.go`** —
  `TestAParkedTrysNextStepAgreesWithTheNeedsYouHeader` gains a subtest,
  "a standing current-run hold names the header's release command": the
  current-run half of the row-agrees-with-header contract. Tick eli covered
  the prior-hold half; this subtest builds a fixture where the current run
  holds a tick on a refused last try and asserts
  `held.Tries[last].NextStep == *model.WaitsOn.UnblockCommand`.

## Test-first evidence

The guard was written first and reproduced the finding on the old code:

```
--- FAIL: TestEveryClearingCommandInTheModelIsSpelledOnce
    commands_guard_test.go:84: pipeline.go:643: a clearing command is spelled
    outside commands.go ("ticfac settle %s %s %d --release \"<who>\"") — call
    the one spelling there, so a wording change cannot drift a row from the header
```

After the fix, green:

- `go test -run 'TestEveryClearingCommandInTheModelIsSpelledOnce|TestAParkedTrysNextStepAgreesWithTheNeedsYouHeader|TestAHeldRunNamesTheCommandThatReleasesIt' ./internal/statusmodel/` — ok (all subtests, including the four in the agreement test)
- `go test -timeout 20m ./internal/statusmodel/` — ok
- `go test -timeout 20m ./internal/cli/` — ok (the renderer consumes the model; its string assertions are unchanged because the fix is byte-identical)
- `make gate` (gofmt, `go vet ./...`, short suite across the repository) — exit 0

## What the next tick has to know

- **The one-spelling rule in `internal/statusmodel` is now enforced, not
  remembered.** Any new clearing command added to `commands.go` is covered by
  the guard automatically; any renderer code in the package that wants to
  spell one inline fails `make gate`. A command needed by a DIFFERENT package
  (e.g. the reconciler's prose, the CLI's usage text) is outside this guard's
  scan by design — the scan is package-scoped, not repo-scoped.
- The fixture shape for "a tick the current run holds on a refused last try"
  (checkpoint row `rejected` + attempt marker + feed `rejected` + feed
  `run_held`) now exists in `TestAParkedTrysNextStepAgreesWithTheNeedsYouHeader`
  and is the cheapest base for any future test of the current-run hold row.
- No contract bundle, golden or TypeScript change: the fix is
  behavior-identical, so `contracts/` and `cloudflare/` are untouched.

## Findings

```findings v2
[
  {
    "kind": "proposal",
    "title": "Reconciler prose spells the settle imperative inline in 4 files, unguarded",
    "severity": "low",
    "body": "The same second-spelling class l1t fixed in the status model exists one layer down: internal/reconcile/blocked.go, dispatch.go, finish.go and resolve.go each format the full `ticfac settle ... --release ...` imperative into worker/person-facing prose. If the CLI's settle syntax ever changes (flags, --carry-work wording), those sentences drift from the command a person must actually type. A package-scoped guard like statusmodel's commands_guard_test.go over internal/reconcile (with the prose composed from a shared spelling or pinned by test) would close it.",
    "target": "pengelbrecht/ticfac",
    "evidence": "grep -rn 'ticfac settle %s %s %d --release' internal/reconcile/*.go — blocked.go:244,473,479; dispatch.go:785,2805; finish.go:172; resolve.go:191,203"
  }
]
```

STATUS: DONE
