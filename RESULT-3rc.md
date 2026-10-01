<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-13/3rc`, base `8b043727a1b2e146e04980aa97ab05f0559c6213`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# 3rc — The bare `ticfac` overview shows the dashboard headline

Epic hn6, wave 4, deliverable (c): every run row the bare `ticfac` overview
lists with a gathered model now carries the dashboard's own headline under
its unchanged first line, rendered by the same functions `ticfac watch`
renders with — so a person glancing at the listing sees the same progress,
health verdict and needs-you that `ticfac watch` shows for that run, and the
two surfaces cannot disagree about one model.

## What changed

`internal/cli/overview.go` only, beside its tests:

- `renderOverview` prints, under each non-history run's first line,
  `dashboardHeadline(run.Model, styles, width)[1:]` — the progress bar with
  the health verdict and the phase bar with the needs-you answer — indented
  two spaces, then `dashboardAttentionLines(run.Model, styles)` indented the
  same way, which is where a held run's "needs you: … — <unblock command>"
  line comes from, exactly where the watch's frame puts it. A row whose
  model was never gathered (`run.provisional`) and every history row print
  exactly as they did; `--json` is untouched.
- The styles and width are the watch's own seams, reused without editing
  watch.go: `watchIsTerminal` decides identity styles (a pipe or a log gets
  `overviewIdentityStyles`, the words, no escape codes) against ANSI
  (`ansiWatchStyles`), and `watchTerminalSize` gives the width, with
  `overviewHeadlineFallbackWidth` = 100 when the terminal does not say.
- The attention lines ride for every gathered row, empty for the ones that
  need nobody — the same function the watch's frame appends after the same
  headline — so an orphaned-failed cloud run shows the same needs-you line
  under its row that the watch shows under its headline.

No edit to watch.go, watch_view.go, internal/statusmodel or cloudflare/.

## Tests

Added in `internal/cli/overview_test.go`:

- `TestTheBareOverviewShowsTheDashboardHeadline` — a fixture of one running
  run (six ticks, five closed, healthy verdict) and one held run: the
  running row's first line stays free of the progress it names ("running —
  wave 1 of 1") while the lines under it carry "5/6 ticks" and "● healthy";
  the held row keeps `clear with: ticfac settle hdh t1 2 --release "<who>"`
  on the first line AND names the same command in its "needs you:" line.
- `TestTheBareOverviewHeadlineMatchesWatch` — the one-model-two-renderers
  guarantee: for each run, the block under its first line is exactly
  `dashboardHeadline(model, identity styles, 100)[1:]` (the tick's [1:3]
  plus the attention lines the watch appends) with two spaces of indent,
  byte for byte, the model taken from the same run's `--json` entry.
- `TestTheBareOverviewHeadlineStylesAndWidth` — the seams: a faked 40-column
  TTY drops the progress bar the width cannot seat and colours the verdict
  (`\x1b[32m● healthy\x1b[0m`); a non-TTY keeps the bar at the 100 fallback
  and carries no escape codes.

In `internal/cli/overview_history_test.go`: the exact-output assertion that
counted the screen's lines now counts the rows' first lines and the one
summary line (the headline lines are indented under them), and the `--all`
section pins that every history row still prints exactly one line even
though `--all` gathers it in full. The prose first lines themselves are
untouched everywhere: scripts and tests grep them.

## What I ran

- `go test -short -timeout 20m -run 'TestTheBareOverview' ./internal/cli/` —
  pass (including the three new tests and the history collapse).
- `go test -short -timeout 20m -run 'TestDashboard|TestWatchPlural|TestTheFrame|TestWatch|TestStatus' ./internal/cli/` — pass (the dashboard renderer's own suite the tick shares functions with).
- `go test -short -timeout 20m ./internal/cli/` — pass (29.6s).
- `make gate` — exit 0 (gofmt, go vet ./..., the whole-repo short suite,
  44 packages ok).

## What the next tick has to know

- The overview row's first line is byte-identical to before — the tick's
  grep contract — and the dashboard lines under it are indented exactly two
  spaces; anything that parses the listing below a row's first line must
  skip `  `-prefixed lines.
- The headline's width for a piped overview is 100 (`overviewHeadlineFallbackWidth`),
  not the watch's width-0-means-unknown: the listing is one screen of words,
  so it lays out at the golden width instead of rendering everything
  unbounded.
- Held runs now say their clearing command TWICE — the row's `clear with:`
  and the headline's needs-you line — by design: the first line is the
  script-grep contract, the headline is the same words the watch shows.
- `run.provisional` is the "model was never gathered" flag the render
  guards on; under prose and `--all` every printed non-history row is
  gathered, so the flag is belt-and-braces, not a live path.

```findings v2
[]
```

STATUS: DONE
