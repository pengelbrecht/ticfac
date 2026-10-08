<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-9/tgx`, base `d0f2873d8350259d01631900003dd52d9a48b313`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# tgx — Protected changes applied in key order wipe 2pn's harness gate cell

Epic ex6's tgx. One commit on `tick/ex6/attempt-9/tgx`: `289c888 "Apply protected changes contents-first per path (tgx)"`.

## What changed

`applyProtectedChanges` (internal/reconcile/protected_changes.go) iterated the run's
still-proposed findings in key order (runstate `Findings()` sorts by key) and applied each
finding's protected change as its own commit. With three proposals to the same file — 2pn's
append of the harness gate cell (`2911b0ce…`) and q6z's and 2p3's whole-file
`.tick/runners.toml` replacements (`4fe8b808…`, `b916a378…`, both composed from bases before
the cell existed) — the append landed first and each replacement then overwrote the file
whole, so the cell was gone from the branch the PR merges. Nothing caught it: the integrated
gate ran before the protected changes, the append's finding was triaged FIXED by its own
commit, and the parity guard treats an undeclared harness entry as the accepted window.

The fix is the tick's second option — the apply path orders content replacements before
appends per path:

- New `orderForApplication` reorders the findings before the application loop: one stable
  pass that puts every whole-file content replacement (and every finding that carries no
  protected change) before every append, each group keeping its key order. An append is
  relative to the file that finally stands, so it now lands on the last content; proposals
  for different paths do not interact, so one pass serves them all. `Apply`ped()'s existing
  "already ends with the appended text" check keeps the operation idempotent.
- The per-finding commit shape is unchanged: each finding is still applied as its own
  labelled commit and triaged FIXED by it.
- The big header comment in `protected_changes.go` names the ordering rule where the apply
  flow is described (epic ex6's tgx referenced).

Among the two content proposals themselves nothing changed: key order still decides, so
`b916a378…` (2p3) wins over `4fe8b808…` (q6z) exactly as before — the fix only moves the
append after both. The run's other pending proposals (`492d466f…` → `.tick/runners.local.toml`,
`6d07a495…` → `.tick/runners.cloud.toml`) are single-content per path and unaffected.

Simulating the three real payloads through both orders: the old order yields a
`.tick/runners.toml` without `[testing.commands.harness]` (the bug); the new order yields
the content plus the harness cell, keeps the base `go`/`ts` gate cells, and parses as TOML.

## Tests (test-first)

New file `internal/reconcile/protected_changes_order_test.go`:

- `TestProtectedChangesApplyContentsBeforeAppendsOnTheSameFile` (end-to-end,
  `shorttest.EndToEnd`) plus a new fake-runner mode `protected_change_order` in
  `internal/reconcile/testdata/fake-runner.sh`: three ticks (a1, a2, b1) each report a
  protected change to `.tick/runners.toml` — an append and two whole-file contents composed
  from the worktree's file, without the appended lines. The finding titles were chosen so
  their keys sort append, content, content — exactly the order that lost the cell (keys are
  sha256 over source+kind+title+target; verified). The test asserts its own premise (the
  drafts come out append-then-contents in key order), that the run completes, that all three
  findings are FIXED by commits, and that the branch's final file carries the appended cell
  AND the last content's marker, not the earlier one. Before the fix it failed on exactly
  the incident's assertion ("the epic branch's runners.toml lost
  [testing.commands.lint]"); after the fix it passes, stable over
  `-count=3`.
- `TestOrderForApplicationPutsContentsBeforeAppends` (`short:` doc-commented, pure
  functions): the incident shape, key order kept within each group, mixed paths still
  contents-before-append per path, nil input.

Ran, in order: `go test -run 'TestProtectedChangesApplyContentsBeforeAppendsOnTheSameFile'
-timeout 20m ./internal/reconcile/` (fail before the fix, pass after); the package's whole
protected-change set plus the harness/gate parity guards
(`TestAFindingCarryingAProtectedChangeIsAppliedAfterTheCloseOutNotAbsorbed`,
`TestATickWhoseOnlyDeliverableIsAProtectedChangeClosesAndTheRunAppliesIt`,
`TestAFindingWhoseDeliverableIsAProtectedEditIsNotAbsorbedAsAWorkersTick`,
`TestAWorkerWritingAProtectedFileIsStillRefused`, `TestProtectedDeliverableReadsTheTitleOrAnUnwritableBody`,
`TestTheDeclaredHarnessGatePairsWithItsMakefileTwin`, `TestTheHarnessGateTargetRunsTheHarnessSuites`)
— ok; new tests with `-count=3` — ok; `make gate` — exit 0 (gofmt, vet, 47 packages under
`-short`). The full non-short `internal/reconcile` suite is not run locally per AGENTS.md;
the epic PR CI runs it in its reconcile shards, and the new end-to-end test joins it there.

## What the next tick has to know

- The close-out that applies this run's pending proposals will now produce, on the epic
  branch: `b916a378…`'s content, then `2911b0ce…`'s append on top of it — the harness cell
  survives, as one labelled commit per finding. The three commits land in the order
  contents (key order), then appends, so feed order of `protected_change_applied` events
  changes for multi-proposal runs; nothing reads that order except the file it produces.
- Known limit, inside the seam this tick changed and accepted: the append dedup only
  recognises its text at the file's end. A whole-file proposal that already incorporated an
  append's lines NOT at the end would get them appended again. No proposal in the wild does
  this — appends are applied only at the close-out, so a content composer never sees an
  applied append — and widening the dedup to "already contained anywhere" would wrongly
  swallow legitimate repeats, so I left `Applied()` alone.
- `fake-runner.sh` gained mode `protected_change_order` and two helpers (`json_string`,
  `content_proposal`, `report_protected_change`); the mode composes its content proposals
  from the worktree's actual `.tick/runners.toml`, so it tracks `passingGate` changes
  without edits.

```findings v2
[]
```

STATUS: DONE
