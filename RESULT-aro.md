<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-9/aro`, base `aa792068a3448ca4fc12d636d05f211c2cee5086`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# The drill-in tick view cuts every line to the pane's width (tick aro, epic hn6)

## What the tick was

`renderTickView`/`renderTickContent` never cut to the pane's width, unlike
`renderWatchFrame` and `renderFeedView` which truncate: a long report summary,
try reason or finding title was written to the terminal unbounded. In the live
view a line wider than the pane wraps, pushes the block down rows the redraw's
cursor arithmetic does not know about, and the next frame draws over the
block's own rows — the same corruption family earlier ticks fixed for the
dashboard and the kept alerts, one seam over. Verified at the base: a fixture
with a 200-cell report summary rendered a 200-cell line in an 80-column pane.

## What changed

- `internal/cli/watch_drill.go` — `renderTickContent` now runs over its lines
  the same final pass the dashboard frame and the feed view run over theirs:
  trailing padding trimmed (a trailing space is a column nobody reads), then
  every line cut to the pane's width with `ansi.Truncate` when width > 0. One
  pass covers every unbounded appender the finding named — the gloss header,
  the try lines with reason and next step, the report summary, the gate rows
  and the finding titles — and `renderTickView` inherits it, since it renders
  exactly `renderTickContent`'s lines plus the (constant, short) footer.
  Width 0 keeps the unknown-width contract the file's other views state:
  everything, unbounded, for the caller that knows nothing.
- `internal/cli/watch_drill_test.go` — new `TestTickViewFitsThePane` (written
  first, red at the base): the finding's 200-cell summary, a 200-cell try
  reason and next step, a 200-cell gloss and a 200-cell finding title on the
  contract fixture's t2; asserts every line of `renderTickView` and of
  `renderTickContent` fits widths 80 and 47, that the cut keeps the line's
  start (the summary's first 80 cells survive), that styled lines are cut by
  display width with `ansiWatchStyles()`, that width 0 still draws the whole
  200-cell lines, and that a try line's `%-9s`/`%-11s` padding no longer
  reaches the edge.

## What I ran

- Test-first: `go test -run 'TestTickViewFitsThePane' ./internal/cli/` at the
  base — **failed** reproducing the finding exactly: line 4 was the 200-cell
  report summary in an 80-column pane, plus a 204-cell gloss line, a 439-cell
  try line (reason + "— next:"), a 202-cell finding title, and at width 47
  even a 56-cell try line ("try 1 strong rejected gofmt drifted in two
  files") overflowed the pane. **Passes** after the fix.
- `go test -short -timeout 45m -parallel 4 -count=1 ./internal/cli/` —
  **passes** (whole package, 50s; run twice, before and after a test-tightening
  edit; includes the pty, block, keys, view and property suites).
- `make gate` (`GOTEST_PARALLEL=4 GOFLAGS=-p=2`) — **passes**, exit 0, 45
  packages ok, no FAIL (gofmt, `go vet ./...`, the short suite repo-wide).
- Headless throughout: no git, no processes, no wall clock in the new test —
  it renders pure functions of the contract fixture.

## What the next tick has to know

- The drill views' width contract is now uniform: the dashboard frame, the
  feed view and the tick view all trim trailing spaces and cut to the pane's
  width when width > 0; width 0 on any of them means unknown → unbounded.
  A test asserting on tick-view lines no longer sees the try cells' padding.
- No golden moved: the contract bundle's `dashboard` golden pins
  `renderWatchFrame` only, and every line the tick view's fixtures produce
  fits the widths those tests use (100/120), so no pinned bytes changed.
- The tick view's height behavior is unchanged: footer always kept, what does
  not fit counted as "+N more". Truncation happens in `renderTickContent`
  before the height fit counts lines, so line counts are unaffected.
- The finding below is the next seam in this corruption family: the live
  view's pane size is read once, not per frame — the renderers all fit
  whatever size they are handed, but a resized terminal hands them the stale
  one for the rest of the watch.

```findings v2
[
  {
    "kind": "defect",
    "title": "watch sizes the pane once; a mid-run resize draws later frames at stale size",
    "severity": "medium",
    "body": "watchLive reads watchTerminalSize once at start and never re-reads it in the redraw loop, so resizing the terminal while a watch runs makes every later frame — dashboard, feed view and tick view alike — draw at the stale width and height: lines wrap, the block pushes down rows the redraw's cursor arithmetic does not know about, and the next frame draws over its own rows. The renderer seams all fit whatever size they are handed; the gap is at the size seam, so the fix shape is re-reading the size in the redraw loop (and re-clamping the feed view's scroll to the new height), not another truncation.",
    "evidence": "internal/cli/watch.go:693-695 (size read once before the draw loop; no SIGWINCH handling and no per-frame re-read anywhere in watch.go)"
  }
]
```

STATUS: DONE
