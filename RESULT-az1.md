<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-5/az1`, base `81c9e74e23d3b379a02a8cf7e6fc35a834410191`, harness `pi-durable` exited 0, 2 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `81c9e74e23d3b379a02a8cf7e6fc35a834410191` is the head of the work it continued, which was cut from `938dc53d361deeec2559bdedfc931845d51a2871`; its work commits are counted from the carried head._

# az1 — At 80 columns a tick title gets the width before its status

## What changed

The busy 80x24 golden spent half the pane on the status column —
`writing code (attempt 2, model escalated)` is 40 cells — and cut every
title to `order-feed …`. Three changes, in the order the tick names
(commit `0b7110e40`, refined in `cc8f61e50`):

1. **The row's exception note renders in its compact spelling.**
   `statusmodel.CompactException` (new, in `internal/statusmodel/statusword.go`)
   maps `attempt 2, model escalated` → `attempt 2 · escalated`: the same
   components, `model escalated` as `escalated`, the comma join as ` · `.
   The model's own `Exception` field is untouched — it is the contract's
   own words, the records and the phone page keep it — so this is a
   rendering of the same facts, and it never widens the note it renders
   (pinned in `internal/statusmodel`).
2. **What is left after the id, the status and the elapsed time is the
   name's before it is the excerpt's.** `dashRowColumns`
   (`internal/cli/watch_view.go`) used to split that room half and half;
   the title now takes it to its natural width and the worker's live
   excerpt claims only what remains, dropping below its floor with its
   separator returning to the title. The drop thresholds (excerpt 64,
   name 48, time 40) are unchanged.
3. **The goldens.** `watch_scenario_busy_80` now reads
   `order-feed service skeleton` whole (was `order-feed …`), with the
   escalated row carrying the shortened note and no line over 80 cells;
   `watch_scenario_busy_120` and `watch_scenario_held_80` re-render with
   the same rule (the busy 120 frame keeps its excerpt; the held 80 frame
   keeps its titles whole and yields its excerpt).

The 80-column busy frame, for the reviewer's eye:

```
NOW
  tgi  order-feed service skeleton     writing code                          19m
  5az  smoke validation vs kofoed du…  writing code (attempt 2 · escalated)  8m
  v1g  safe intraday kanpla pulls      testing                               32m
```

Tests, test-first:

- `TestAtEightyColumnsTheTitleTakesItsWidthBeforeTheStatus` — the tick's
  own claim read off the busy 80x24 frame: the named title whole, the
  shortened note on the escalated row, no long spelling anywhere, no line
  wider than the pane.
- New property **P5** in `internal/cli/watch_props_test.go`
  (`propTitleBeforeExcerpt`): a row that still seats a live excerpt is a
  row whose title is whole, at every pane width — with its own seeded
  breaker (`breakCutsTheTitleBesideAnExcerpt`), so
  `TestDashboardPropertiesCatchSeededBugs` proves it can fail. Verified
  the other way too: **reverting only the allocation rule makes the
  property fail at seed 3, width 99** (`the t01 row's title "property
  work for t01" is cut beside a live excerpt`) — the check is not
  vacuous, and it is the regression guard for this tick.
- `TestTheStatusExceptions` grew the compact-form cases, read off the
  builder's own notes (escalated, single component, never wider than the
  note).
- Re-pointed the tests that pinned the old spelling/geometry:
  `TestDashboardNarrowWidths` (at 80 the name column is now whole and the
  excerpt is the column that yielded), `TestDashboardRowsCarryStatusWords`,
  `TestTheFrameCarriesTheContractGolden`, `TestWatchColourGridPerState`.

## What I ran

- `make gate` — green (gofmt, `go vet ./...`, the whole-repo short suite),
  on the final tree, exit 0.
- The touched half of the gate, by hand (this worktree has no
  `TICFAC_GATE_TOUCHED_*` pair to give `cmd/gate-touched`; I computed the
  same selection with `internal/gatescope` over my changed files —
  `cmd/ticfac`, `internal/cli`, `internal/contracts/parity`,
  `internal/statusmodel` — and ran their full non-short suites): all four
  green, `internal/cli` 77s.
- `go test -short -run 'TestDashboardProperties|TestDashboardPropertiesCatchSeededBugs' ./internal/cli/`
  — green, 7s (the property suite now runs five properties over the same
  500 seeds and pane shapes).
- internal/cli's `-short` wall, for the record a tick adding tests owes the
  reader: 71s on this loaded host (4 cores). The fifth property adds
  ~0.4s to `TestDashboardProperties` (7.2s → 7.6s, measured before and
  after); the rest of the new tests are sub-100ms.
- `make bombadil` (the pty suite, CI's own job, not the gate): green —
  "all 2 runs answered as they must", including its frame-fits-the-pane
  property over the generated resizes against the real binary.
- `internal/reconcile`'s full suite is left to CI, as the repo's own
  gate-touched declaration states; nothing in this tick touches it (no
  reconcile file, and no reconcile test renders the dashboard — checked
  with a grep over `renderWatchFrame`/`model escalated`).

## What the next tick has to know

- **The scenario goldens are shared artifacts across wave 1.** etl (the
  health line), h3s (group spacing) and cl7 (colour) all re-render
  `internal/cli/testdata/watch_scenario_*.txt`. This tick rewrote
  `busy_80`, `busy_120` and `held_80`; a same-wave merge that conflicts
  there should be resolved by re-running
  `go test ./internal/cli/ -run TestDashboardScenarioGolden -update` on the
  merged renderer, never by hand-editing the frames.
- **The committed screenshots are cl7's**, per its own acceptance and the
  epic's [A3]/[A5]. `docs/design/watch-redesign-2026-10/watch-busy-120x40.png`
  now shows the note's long spelling where the frame reads the short one;
  regenerating it belongs to cl7, alongside the colour work.
- `docs/design/watch-redesign-2026-10.md` still shows a retry reading
  `writing code (attempt 2, on opus)` in its principles section; the row's
  spelling is now `attempt 2 · escalated`. I left the design record alone
  (it is a dated record, and its target layout is what this change moves
  the 80-column frame TOWARDS).
- The exception's compact spelling is Go-only. Nothing in `cloudflare/`
  renders the `exception` field at all (checked), so there is no TS/Go
  parity seam to guard for it; the contract bundle is untouched.

```findings v2
[
  {
    "kind": "defect",
    "title": "Width 0 says \"no truncation\" but caps the row's title column at 32",
    "severity": "low",
    "body": "renderWatchFrame's contract comment says an unknown width means no column drops, no truncation, but dashRowColumns' width<=0 branch caps the WHAT column at dashWhatCols (32) and the excerpt at 36, so a title longer than 32 cells IS cut on the pipe/log path. Pre-existing, outside this tick's rule (the pane never imposed it there); my new P5 states its expectation with the cap rather than fixing it, so a future fix to either side has a guard already in place.",
    "evidence": "internal/cli/watch_view.go:1023-1026 against the doc comment at internal/cli/watch_view.go:30"
  }
]
```

STATUS: DONE
