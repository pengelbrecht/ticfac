<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-2/jym`, base `8098cc013cd8704aa4e13ecd425b6fa843591d3b`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# jym — Fold in the open watch defects: t0y, dfb, 4dn, zrl

One commit on `tick/ymf/attempt-2/jym`: `c5898c1`, parent `8098cc0` (the
epic/ymm head as this attempt received it, post-`ugm`). 11 files, +540/−76:
three code fixes, one proof, five new tests, and contract bundle **2.7.0**
(one golden corrected, one rule stated).

## What changed, per defect

**t0y — an ended, merged run disagrees with itself (two fixes).** On the new
dashboard the old manifestations (CI line, phase strip) are gone; the two that
survived were (a) the count pair and (b) the ci phase record.

- *Count pair (the h4u remnant):* `internal/cli/watch_view.go`
  `dashSectionHeader` now counts the DONE group the way the health line does —
  the epic's real ticks, excluding closed duplicates (the contract pins a
  duplicate out of `total` and `closed`). An ended run with a duplicate says
  "4 of 4 done" beside "DONE (4)"; the duplicate's row still stands, dimmed,
  naming the tick its work belongs to — the difference is said, on the row,
  exactly as t0y asked. Scoped to DONE only: a duplicate parked in UP NEXT
  still counts there, because that header describes the rows it sits over
  (excluding it produced "UP NEXT (0)" above a visible row in the
  epic-state golden, which the uniform version broke and I reverted).
- *CI/phase:* `internal/statusmodel/build.go` `buildLifecycle` — a completed
  run with no PR left has finished the CI chapter: the close-out's held line
  stays in the feed as the run's last word, but the PR it waited on is merged
  (the forge's open-PR read answers nothing), so `ci` reads done beside
  `merge` done instead of active-beside-done. The track already folded the
  pair correctly (`prCIState`); this fixes the phase record under it, the
  surface the old dashboard drew "◐ ci" from.

**dfb / ugt — the hold alert dereferences a nil clearing command.**
`internal/cli/watch.go`: the stream path's inline switch on the hold reason
dereferenced `*clearing` in five cases with no nil check, while
`HoldClearingCommand` legitimately returns
nil (an epic it cannot state names no command, tick mwt; a hold no closed rule
knows names none). The wording is now one nil-safe builder,
`holdAlertWording(reason, clearing, epic)`, which falls back
to the no-command wording the unrecognised-hold case already spoke; every
case reads the command through it. Honest reachability note: `watchEpicID`
never returns "" today (it falls back to the run id), so I could not
reproduce the panic end-to-end through the CLI — the defect is the code
shape against the builder's contract, and the test pins the nil path at the
unit level (`TestTheHoldAlertWordingsReadANilCommandAsNoCommand`); the
end-to-end wordings for every reachable hold stay pinned by the existing
CLI tests, which pass unchanged.

**4dn — an ended run's dispatched row still grows a live elapsed.**
`internal/statusmodel/build.go`: `isLiveAttempt` now takes whether the
subject run itself has ended, by the same `runEndedAt` authority the
run-level clock clamps at, and answers false for a dispatched/reported row on
an ended run — so `elapsed_seconds` is null there, and the per-tick clock
stops where the run-level one already does. Precedence kept: the census's
standing answer still wins (a live attempt is measured even on ended-run
records), and a non-subject owner's row was already history. Test
`TestAnEndedRunLeavesItsDispatchedRowUnmeasured` reproduces at base ("reads
a live elapsed of 3600s, want null") and pins both controls (a live run
still measures; a standing attempt is measured).

**zrl — below 48 cells a child row's columns shifted by its indent.** No code
change needed: the redesign has no child indentation (groups make row
adjacency meaningless; a row's only indent is the drill-in cursor). Proven by
`TestNarrowChildRowsAlignWithTheirParent` at widths 40 and 47 (the widths the
tick named): parent and absorbed child rows have their status word and time
in the same columns, and the old "└" indent appears nowhere. This matches the
previous attempt's zvj finding; the test is what makes it closeable as fixed.

**Contract bundle 2.7.0** (required by the 4dn fix, not incidental): the
`dashboard_stopped` golden carried the bug fossilised — ticks 46x and v7z
kept counting (1318s / 2400s to `generated_at`) on a run whose liveness says
`stopped`. They are null now, and `checked_beyond_schema` states the rule: a tick's `elapsed_seconds` is
measured only while its attempt is in flight — the census's standing answer,
or its state's dispatched/reported word while *that run* has not ended. The
contract test's golden mirror (`isLive` + new `goldenRunEnded`) derives the
end from the document's own evidence (terminal lines in the tail, or the
probe's end-word). `contracts/bundle.json` cut at 2.7.0 with the ledger
entry; `cloudflare/contracts.pin.json` bumped; `contracts/CHANGELOG.md`
entry added. No schema or field shape changed.

## What I ran

- `go test ./internal/statusmodel/ -count=1` — **ok** (includes
  `TestEveryGoldenAgreesWithThePipelineDerivation` over the corrected golden).
- `go test ./internal/cli/ -count=1` — **ok** (71.98s; includes the five
  scenario goldens, the epic-state golden byte-for-byte, and all hold-alert
  CLI tests).
- Base reproduction: with the fixes stashed (tests kept),
  `TestAnEndedRunLeavesItsDispatchedRowUnmeasured` fails at base with "the
  ended run's dispatched row reads a live elapsed of 3600s, want null",
  `TestAnEndedMergedRunReadsItsCIAndMergeDone` fails with "the merged run's
  ci phase reads active, want done", and
  `TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes` fails with
  "DONE (5)" beside "4 of 4 done" — each on exactly the assertion that names
  its defect. The zrl proof passes at base too (it proves absence, no fix to
  revert).
- `make gate` — **exit 0** (gofmt, vet, short suite, 50 packages ok).
- `make gate-touched` — **exit 0**, run with
  `TICFAC_GATE_TOUCHED_BASE=8098cc0… TICFAC_GATE_TOUCHED_HEAD=HEAD` because
  this worktree has no origin to diff against; full (non-short) suites of
  cmd/ticfac, internal/cli, internal/contracts/parity, internal/statusmodel.
- TS gate, all four stages: `pnpm lint` (biome, clean), `pnpm lint:test`,
  `pnpm contracts:check` ("16 contract(s) at bundle 2.7.0"),
  `tsc --noEmit`, and `vitest run` — **83 files, 1891 tests passed** (619s).

## What the next tick has to know

- **t0y, dfb, 4dn, zrl are ready to close** on `c5898c1` (their notes already
  say "fixed under epic ymf (tick jym)"). Closing is the run's move, not mine.
- **The backlog ticks the previous attempt filed are now addressed** and can
  be closed as duplicates of this work: c3i (≈4dn), ugt (≈dfb), h4u (≈t0y's
  count), zvj (≈zrl). They are outside the epic, so I could not edit them.
- **The row TIME cell decision (see findings):** 4dn's named field
  (`elapsed_seconds`) is fixed, but the dashboard's per-tick TIME cell
  renders `duration_seconds`, whose contract meaning is "dispatch → close,
  or → generated while open" — it still grows on a dead run's open tick,
  beside the run header's frozen clock. Freezing it is a separate contract
  rule change (the golden's durations cannot be derived from that document,
  which carries no end stamp), so I left it and filed it as a proposal. The
  row's status word already says "waiting: the run is stopped", so nothing on
  the row claims live work.
- The five 120x40 screenshots `ugm` attached to the epic PR are unchanged in
  substance by this commit (no scenario carries a duplicate or an active ci
  beside a done merge); the epic's A7 screenshots do not need re-shooting,
  but the final review may want to eyeball `TestTheDashboardCounts…`'s
  fixture if it wants a frame with a duplicate in it.
- `internal/reconcile` untouched; per the standing order its suite stays with
  CI.

```findings v2
[
  {
    "kind": "proposal",
    "title": "A dead run's open tick still grows the row TIME cell (duration, not elapsed)",
    "severity": "low",
    "body": "4dn's named defect is fixed (elapsed_seconds is null on an ended run's dispatched/reported row), but the new dashboard's per-tick TIME cell renders duration_seconds, contract-defined as dispatch-to-close or dispatch-to-generated-while-open, which still grows on a dead run's open tick next to a run-level clock frozen at the end — the same grows-past-the-end pattern t0y was about, in a narrower cell. Freezing it is a contract rule change of its own: duration_seconds' wording in status-model.json, the contract mirror, and the dashboard_stopped golden's durations, which cannot be derived from a document that carries no end stamp (the tolerance pattern would leave them unverifiable). Filed so the choice is explicit; the row's status word already says the run is stopped.",
    "evidence": "internal/statusmodel/pipeline.go durationOf (end = src.Now while open); internal/cli/watch_view.go dashboardRowSource (TIME cell renders DurationSeconds)"
  }
]
```

STATUS: DONE
