<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-8/9um`, base `23002ae7ce185ddd3ad3dc30555107e7c6540b76`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk --json tick get hn6`

# The dashboard wraps a hold's clearing command under the announcement instead of truncating it (tick 9um, hn6 attempt 8)

Tick 9um was absorbed from u4l's finding: the dashboard truncates a hold line
at the pane's edge, so at narrow widths the one command that clears the hold
falls off entirely — the evidence named the render at width 30,
`needs you: attempt 2 of t2 str`. I reproduced that exact truncation with a
failing test first, then implemented the smallest change the tick names: a
renderer rule that wraps the line under its announcement. One commit on
`tick/hn6/attempt-8/9um`, head `53883fbe7` — source and tests only.

## What changed (commit `53883fbe`, five files)

- `internal/cli/watch_view.go` —
  - `dashboardHoldLines` renders ONE hold: the announcement with its
    clearing command as the one line wherever the pane seats it (unchanged
    bytes there), and where it does not, the line wraps under the
    announcement instead of being truncated — the what flows on with a
    hanging indent, the command keeps every one of its words on indented
    lines of its own, and every wrapped line is amber like the hold.
  - `dashWrapWords` is the wrap itself: break on spaces, keep every word
    whole, never lose a character; a word wider than a whole line is cut
    where it stands (no such word exists at any width the tests or real
    panes reach — it is the safety net that keeps the width rule honest).
  - `dashboardAttentionLines` takes the width now (callers: the watch passes
    the pane's width; the bare overview passes the width its two-space
    indent leaves, so its holds wrap too and stay inside the terminal);
    `dashboardAttentionBlocks` is the same lines one block per hold.
  - `fitDashboard` takes the blocks and, when a pane is too short for the
    whole headline, keeps whole hold blocks (`fitHead`) instead of slicing
    one mid-block — a hold shown without the command under it is the same
    defect by another axis. A hold that does not fit a short pane is not
    shown at all; the widths and heights the properties sweep are the ones
    this can happen in.
- `internal/cli/watch_view_test.go` — two new tests: the wrap pinned
  exactly at width 30 (five lines, announcement first, command whole,
  every line ≤ 30 cells, every line amber) with the whole-line behavior
  re-pinned at widths 0 and 120; and the short-pane rule pinned at 30×8
  (first hold whole, second dropped whole, 8 lines).
- `internal/cli/watch_props_test.go` — P2 extended to the tick's rule over
  500 generated models × 8 widths × 5 heights: wherever the pane seats the
  whole frame, a hold whose line the width seats pins that line exactly;
  wherever it does not, a hold is shown whole or not at all — its
  announcement line in the frame implies every wrapped line and every word
  of the clearing command with them. The blocks are read back in order, so
  a hold whose twin shares its announcement cannot borrow the twin's lines.
- `internal/cli/overview.go`, `internal/cli/overview_test.go` — the
  overview's attention lines take `width - 2` (the indent's own width) and
  the headline-match test builds the same expectation.

## What I ran

- `go test -run 'TestDashboardHoldCommandSurvivesNarrowPanes|
  TestDashboardShortPaneKeepsHoldBlocksWhole' ./internal/cli/` — both red
  first (width 30 rendered exactly the truncated line the finding quotes),
  then green after the change.
- `go test -timeout 20m -run 'TestDashboardProperties|TestDashboardGolden'
  ./internal/cli/` — ok; one iteration taught me the height fold could drop
  a later hold at short heights, which is where `fitHead` came from.
- `go test -timeout 20m ./internal/cli/` (full package) — ok.
- `go test -timeout 20m ./internal/statusmodel/ ./internal/contracts/` — ok.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0: gofmt, `go vet`,
  whole-repo short suite.

## What the next tick has to know

- The two dashboard goldens did NOT change. The tick description guessed
  they would; in fact the golden fixture carries no hold, so the wrap never
  engages in them — no bundle re-cut, no golden churn.
- The contract bundle and the status model are untouched: this is purely a
  renderer rule, and rule 8's one-model-two-renderers guarantee still holds
  (the phone page renders from the same JSON fields; nothing there reads a
  rendered line).
- The wrapped form drops the " — " separator: the command starts its own
  indented line instead. The separator stays pinned wherever the pane seats
  the one-line form (P2's first check).
- `fitDashboard`'s signature grew a `holds [][]string` parameter (its only
  caller is `renderWatchFrame`); `dashboardHoldLines` is the seam the P2
  property oracle renders holds through.

```findings v2
[]
```

STATUS: DONE
