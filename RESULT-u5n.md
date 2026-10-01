<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-9/u5n`, base `29aa245689e9e8f4a3dd15134068ff6aa8b74ffd`, harness `pi` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `29aa245689e9e8f4a3dd15134068ff6aa8b74ffd` is the head of the work it continued, which was cut from `07ea92912b6f7a6f8d484a64fc1c9972bb037f4d`; its work commits are counted from the carried head._

# RESULT — tick u5n: watch renders the dashboard frame

## Shape

`ticfac watch`'s frame renderer (internal/cli/watch_view.go) is rewritten
into the hn6 dashboard layout, as one pure function of the status model plus
the pane's width and height — no terminal, pty or herdr anywhere in its
tests. One frame, top to bottom, answers in the operator's order:

1. the run's identity with its liveness — `<epic-id> <title>` with
   `<host> · alive|not alive` seated at the pane's right edge;
2. the progress bar, `n/m ticks`, the elapsed, `ETA ~…` where the model
   measured a remaining, and the health verdict — green `● healthy`, amber
   `● degraded: …`, red `● stopped: …`, with `(recovered: net ×14, sleep
   41m)` riding in brackets as calm;
3. the phase bar — `✓ done`, `◐ active`, `○ pending`, `waves 2/3` naming
   the active wave — with dim `needs you: nothing` at its right, or one
   amber `needs you: <what> — <clearing command>` line per hold;
4. the tick table: ONE row per tick in plan order, children indented
   (` └ `) under parent_tick_id, absorbed ticks marked `+`, the drill-in
   cursor's `▸` on the selected row — the pipeline cell filling left to
   right (`claim ▸ work ▸ ●gate ▸ …`, a closed tick ending `merged ✓` /
   `closed ✓`), TIER, TIME from duration_seconds, ATTEMPTS as len(tries)
   with `⤴` when the tier changed between first and last try;
5. the workers panel: model · executor · handle, the activity sparkline
   scaled to the window's own maximum (dots when the window is empty), the
   last action with its age, amber `nudged ×N`; a null census says dimly
   that the workers run in the cloud;
6. the CI line — `CI #<pr> <check> <✓|✗|◐ age>` per check, dim
   `CI: no PR yet` without one — with the cost beside it: one segment per
   cost line, a metered segment's measured number, `not metered` for an
   unmetered one, never a fabricated `$0.00`, `cost not metered` when no
   lines exist at all;
7. the `─ recent` rule over the feed's last two lines through the existing
   watchEventLine, with `[e] events  [enter] tick` at the pane's right.

Narrow panes drop columns in the tick's fixed order — the pipeline's words
below 100 (one glyph per stage, and the worker's LAST moves under its row),
TIER and ATTEMPTS below 64, WHAT below 48 — every line truncated to the
pane. A pane shorter than the frame always keeps the headline and the tail,
collapses each contiguous run of closed rows in place into one dim
`✓ N closed` line standing where the run's first row stood, and counts
whatever still does not fit in one dim `+N more` at the fold.

`watchPlural` now pluralizes a consonant+y as `ies` — `2 remote retries`,
never the first-use bug's `remote retrys` — pinned by TestWatchPluralRetries.

## Files

- internal/cli/watch_view.go — the renderer, rewritten (dashboardHeadline,
  the table, workers, CI/cost, tail, height fit; watchStyles gains green;
  the old wave-compression and silence-grading helpers are gone with the
  layout they served).
- internal/cli/watch_view_test.go — rewritten for the new layout: the eight
  tests the tick names (TestDashboardGolden, RowsKeepTheirOrder, NeedsYou,
  NeverPrintsZeroForUnmetered, VerdictColours, NarrowWidths at 99/63/47,
  FitsHeight at 12, TestWatchPluralRetries) plus the frame's content pins
  (truncation, the unreadable tracker, the drill-in marker, the workers
  panel, the CI line, the tail, the ETA, children and absorbed rows).
- internal/cli/testdata/watch_dashboard_120.txt, watch_dashboard_60.txt —
  the contract's `dashboard` golden rendered byte-for-byte at widths 120
  and 60; `-update` regenerates them.
- internal/cli/watch.go — the one call site, passing the new `selected`
  parameter as "".
- internal/cli/watch_block_test.go — only where it asserted old frame text:
  `● waves 2/3` → `◐ waves 2/3`, and the wave-line/row assertions now name
  the dashboard's tick rows and the cloud frame's `cloud · alive`.

Nothing else: overview.go, internal/statusmodel and cloudflare/ are
untouched, as the tick's Files note requires.

## What ran

- `go test -short -timeout 20m -run 'TestDashboard|TestWatchPlural|TestTheFrame|TestWatch' ./internal/cli/` — ok.
- `go test -short -timeout 20m ./internal/cli/` — ok.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2 for the shared host) — ok:
  gofmt, `go vet ./...` and the whole short suite.

## What the next tick has to know

- **The golden lives in the bundle, not beside the package.** The tick text
  says `internal/statusmodel/contract.json`; that file moved to
  `contracts/status-model.json` when the phone page became its second
  reader (tick 4i8). TestDashboardGolden decodes it from the bundle with
  `contracts.Dir()` — u4l and any other renderer should do the same.
- **dashboardHeadline returns the first three header lines, and a fourth
  when the pane cannot seat the needs-you answer beside the phase bar** —
  the answer reflows onto its own line rather than being truncated away
  (at width 60 the golden shows it). v16's bare-overview reuse gets the
  whole headline block, needs-you answer included, by calling it with the
  overview's own width.
- **The active phase is `◐`; `●` belongs to the pipeline cell's active
  stage**, following the spec's own glyphs. watch_block_test.go's
  `waves 2/3` assertion moved with it.
- **The progress bar is decoration, the counts and the verdict are
  answers**: the bar renders only while bar and answers fit the pane
  together, so the 60-column golden has no bar. `ETA ~…` shows only where
  m.Remaining measured one.
- **Elapsed is the earliest dispatched try to the model's GeneratedAt**
  (watchRunElapsed, kept from the old frame). The height fit counts LINES
  in `+N more`; the `✓ N closed` collapse counts ROWS.
- **Worker silence is no longer rendered anywhere**: the hn6 workers panel
  is activity + last action + nudges, so silenceGrade and its thresholds
  went with the old row. Worker.SilenceSeconds stays in the model, so a
  review that wants the quiet timer back is a renderer change only.
- The tail counts a line's `<tick>#<n>` try from m.Recent — the only feed
  the renderer is given; the golden's tail therefore reads `46x#2`.

```findings v2
[]
```

STATUS: DONE
