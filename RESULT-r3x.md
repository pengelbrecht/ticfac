<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-7/r3x`, base `7e37b4c4f586967d58718808703e86c0ef14bf45`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_tick r3x — watch dashboard renders as a staircase in a real terminal: raw mode needs CRLF_

## What changed

Branch `tick/hn6/attempt-7/r3x`, one commit `78df2d2` on top of the run's fold
commit `7e37b4c`.

**1. Raw-mode CRLF (the staircase).** `watchLive` now derives its line ending
from the keyboard attachment: while the keys are attached the terminal is raw
— `term.MakeRaw` on stdin disables the line discipline's `ONLC` for the whole
device, stdout and stderr included — so every line the live view writes ends
`\r\n` (frames in `draw`, the kept lines in `insertAboveBlock`). After the
watch's own end, plain `\n` is right again. The restore is idempotent
(`sync.OnceFunc`) and is now called **before** the watch's last words: the
run's terminal line, the closing held/failed/cancelled summary and the
interrupted message are written after `restore()`, so they no longer
staircase. This covers every exit path — run ended, `q`/Ctrl-C, context
cancellation, first-model-read failure — plus the panic path via the deferred
restore.

**2. The lines kept above the block are word-wrapped to the pane.** The
attention alert and a failed model reading were written unbounded; a kept
line wider than the pane wraps in the terminal, pushes the block down rows
the redraw's cursor arithmetic does not know about, and the next frame draws
over the block's own rows. `keepAboveBlock` wraps the plain text at word
boundaries (`wrapWords`), styles each line, and only then inserts — the
alert's full command stays readable, which truncation would have broken.

**3. The tick table's columns are sized to content.** `dashboardTable` now
builds each row's raw cells first (`dashCells`), sizes every column to the
widest cell it carries — header label included — capped at the layout's
existing maxima (WHAT 32, TIER 10, PIPELINE 32 words / 12 glyphs, TIME 7),
measured by display width so styled cells size honestly. The identity column
keeps the spec's five-cell floor (the hn6 spec's own layout shows ` 060  `).
The narrow-pane column drops (100/64/48) are unchanged. Both dashboard
goldens (`internal/cli/testdata/watch_dashboard_{120,60}.txt`) are re-cut
with `-update`: WHAT 32→30, TIER 10→8, PIPELINE 32→29, TIME 7→4 at the
contract fixture, and the huge-gap shape is gone.

**4. Tests.**
- `internal/cli/watch_pty_test.go` (new, the acceptance): re-execs the test
  binary with a **real pty** on stdin, stdout and stderr at 120x40 and 80x24,
  runs `ticfac watch` over a live fixture run with a standing hold, proves
  the keys are on for real (a `j` keystroke moves the drill-in cursor), then
  asserts (a) every newline that reached the master carries its carriage —
  no bare `\n` anywhere, which is exactly "every frame line starts at column
  0" while raw — (b) every drawn line fits the pane width, (c) the
  end-of-watch message (run ended still holding → exit 3) names the hold and
  the settle command and rode the same wire, and (d) a probe line the helper
  writes after `Run` returns with a plain `\n` is delivered `\r\n` — the
  restore happened first. `github.com/creack/pty v1.1.24` is the only new
  dependency (test-only).
- `TestWatchKeyedFramesEndCRLF` (block test): headless pin — with the fake
  keyboard attached, frames end `\r\n`, the last word ends plain `\n`, and
  the restore flag is set.
- `TestWatchKeepsInsertedLinesInsideThePane`: the ~180-cell kept alert no
  longer exceeds a 100-column pane.
- `TestDashboardColumnsSizeToContent`: exact header/rows for a tiny fixture
  pinning content-sized columns.

## What I ran

- `go test ./internal/cli/ -run 'TestWatch|TestDashboard|TestTheFrame|...'`
  targeted suites during development; full `go test ./internal/cli/` (46s) —
  all green.
- The new pty test both ways: with the fix it passes (8.6s); with the CRLF
  derivation temporarily reverted it fails at both sizes with "a bare \n
  reached the terminal at byte N — the staircase", so the acceptance test
  actually catches the defect it exists for.
- `go test -race -short ./internal/cli/` over the watch/dashboard suites —
  green.
- `make gate` (gofmt, `go vet ./...`, full short suite) — exit 0, twice,
  including after `go mod tidy`.

## What the next tick has to know

- The dashboards' table columns now move horizontally as content changes:
  a column widens when any cell outgrows it (e.g. PIPELINE as stages
  complete). Rows never move vertically — the ordering and height-fit rules
  are untouched and re-pinned. If a future tick wants frozen column widths
  across frames, the seam is `dashColumnWidths`.
- `insertAboveBlock`'s signature changed: `(w, previous, width, eol, lines)`.
  It defensively truncates to the pane; the word-wrapping happens earlier in
  `keepAboveBlock`, before styling, because wrapping a styled line loses the
  SGR state at the break.
- The drill-in views and the feed view ride the same `draw`/eol path, so they
  are CRLF-correct; the tick view's *width* overflow is a separate defect
  (finding below).
- The pty test runs the test binary as a child twice (~9s total; ~16s under
  `-race`). It needs `/dev/ptmx` (present on every runner CI and this host
  use). Its helper is `TestWatchPtyHelperProcess`, selected by
  `-test.run=TestWatchPtyHelperProcess$` and the `TICFAC_WATCH_PTY_HELPER`
  env var — do not rename either casually, and keep the helper's probe line,
  which is the restore's behavioral proof.

```findings v2
[
  {
    "kind": "defect",
    "title": "Tick drill-in view (enter) renders lines wider than the pane",
    "severity": "medium",
    "body": "renderTickView/renderTickContent never cut to the pane's width (unlike renderWatchFrame and renderFeedView, which truncate), so a long report summary, try reason or finding title is written to the terminal unbounded. In the live view a line wider than the pane wraps, pushes the block down rows the redraw's cursor arithmetic does not know about, and the next frame draws over the block's own rows — the same corruption family this tick fixed for the dashboard and the kept alerts, one seam over. Verified: a fixture with a 200-cell report summary renders a 200-cell line in an 80-column pane.",
    "evidence": "internal/cli/watch_drill.go:37-108 (renderTickView/renderTickContent: no width truncation; the try lines, report summary, gate rows and finding titles are appended unbounded)"
  }
]
```

STATUS: DONE
