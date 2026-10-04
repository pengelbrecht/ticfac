<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-6-resolve-6-fb94bc79/gmo`, base `d93e551781193054ee22b3d42d0501035a83c2f0`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict report: tick gmo attempt-6 into epic/hn6

Branch `tick/hn6/attempt-6-resolve-6-fb94bc79/gmo`, resolution commit `e25a2d1`
on top of the conflicted merge `d93e551` (run
`run_5c7c16d199414da2a8a40d97b524e987`).

## The two intents

- **Ours — tick gmo, attempt 6** (`.tick/issues/gmo.json`): "watch dashboard
  shows the epic's state, not only the latest run's". Rows and progress bar
  derived from the epic's whole state across runs; duplicates closed as
  duplicates are shown dimmed, named for the tick their work belongs to;
  the run header says what the newest run ended as ("cloud · failed").
  Acceptance: golden + model tests over a fixture epic with ticks closed by
  two earlier runs and a failed newest run.
- **The other side — epic/hn6**, carrying tick r3x, closed 02:02
  (`.tick/issues/r3x.json`): the dashboard table refactor — `dashTickCells`
  returns a `dashCells` struct, columns sized to their content (capped),
  instead of rows written as strings padded to fixed column caps.

## The conflict

One conflicted file: `internal/cli/watch_view.go`, two hunks, both inside
`dashTickCells`. gmo's attempt branch was cut before r3x's refactor landed
on the epic branch, so gmo's duplicate work was written against the old
row-string code (`row.WriteString(...)`, `return line`). Everything else of
gmo's attempt — the `DuplicateOf` model derivation
(`internal/statusmodel/build.go`, `epic.go`, `statusmodel.go`), the drill-in
line (`watch_drill.go`), the epic-state tests (`watch_epic_state_test.go`),
the failed-run header word — merged cleanly into the tree already.

## The resolution — a union; both intents survive

- **The WHAT cell (hunk 1):** gmo's duplicate override kept verbatim — a
  duplicate's WHAT says `"duplicate of <id>"` (when `DuplicateOf` is set and
  non-empty) — applied *before* r3x's `dashCell(what, dashWhatCols)` cap, so
  the duplicate cell is capped exactly as every other WHAT cell is. gmo's
  leftover `row.WriteString` dropped: r3x replaced that code path with the
  struct cells; the column is written by `dashCells.render` now.
- **The row (hunk 2):** gmo's dim-whole-row intent kept, re-expressed in the
  struct world: `dashCells` gains a `duplicate bool` field (set from
  `tick.DuplicateOf != nil`, exactly gmo's dimming condition), and
  `dashboardTable` dims the assembled line — with gmo's own comment kept
  verbatim: the row keeps its place (rows never move) and its history, but
  nothing about it is the frontier's business.
- Nothing was irreconcilable; nothing was chosen between the sides.

## One derived artifact re-derived

gmo's own golden `internal/cli/testdata/watch_dashboard_epic_state.txt` was
generated under its pre-r3x fixed-cap column layout (WHAT padded to 32,
PIPELINE to 32). The union renderer draws the identical rows and facts —
the `dup` row named `duplicate of t2`, the closed ticks t1/t2/t5 with their
last runs' tier/time/attempts, `3/5 ticks` progress, the header saying the
newest run failed — under r3x's content-sized columns. I regenerated the
golden with the test's own `-update` flag; the diff is column widths only.
This is the tree a better wave partition would have produced: gmo rebasing
onto r3x and re-pinning its golden.

## Verification

- `TestDashboardEpicStateGolden` (gmo, byte-for-byte pin) passes.
- `TestDashboardDuplicateRowIsDimmedAndNamed` (gmo, dim-whole + naming)
  passes.
- `TestDashboardColumnsSizeToContent` and `TestDashboardGolden` at 120/60
  (r3x/u5n, content sizing + the other goldens) pass unchanged — the union
  does not move any byte their fixtures carry.
- Full `internal/cli` package: pass (48.6s).
- `make gate` (gofmt, `go vet ./...`, whole-repo short suite): exit 0.
- No conflict markers remain anywhere in the tree (scanned all file types,
  not just Go).
- The commit is source only: `internal/cli/watch_view.go` and
  `internal/cli/testdata/watch_dashboard_epic_state.txt`; no build output,
  no caches, nothing under `.ticfac/runs/`.

## Findings

The collision was the wave's, not the text's: the two intents composed
cleanly, and the findings below are the two planning observations the
collision left. They go in the typed block, not prose.

```findings v2
[
  {
    "kind": "defect",
    "title": "The wave gave gmo and r3x one dashboard function to reshape",
    "severity": "low",
    "body": "gmo's attempt branch was cut before r3x's dashboard-table refactor landed on epic/hn6, so both ticks reshaped the same function's text: gmo's duplicate-row work was written against the old row-string code the refactor removed. The intents composed cleanly — the conflict was textual, not an incompatibility — but sequencing \"duplicate rows in the dashboard\" after \"the dashboard table refactor\" would have produced this tree with no conflict at all.",
    "evidence": "internal/cli/watch_view.go — the two hunks resolved in dashTickCells (commit e25a2d1)"
  },
  {
    "kind": "proposal",
    "title": "Byte-pinned goldens must be re-derived when a union relayouts them",
    "severity": "low",
    "body": "gmo pinned its acceptance golden (internal/cli/testdata/watch_dashboard_epic_state.txt) byte for byte while r3x relayouted the same renderer, so any union of the two code intents necessarily changes the golden's bytes: the conflict reappears at the golden's regeneration point and the resolver must re-derive it (the test's -update flag made that cheap here). A tick whose acceptance pins bytes of a file another in-flight tick can relayout will always conflict this way; the wave planner or the resolve-conflict job could account for it.",
    "evidence": "internal/cli/testdata/watch_dashboard_epic_state.txt (regenerated in commit e25a2d1); internal/cli/watch_epic_state_test.go:215"
  }
]
```

STATUS: DONE
