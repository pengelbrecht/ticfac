<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-4/h3s`, base `37a656963267555909894a8f5aa618dda0d38459`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# h3s — Blank lines between the dashboard's groups

**Done.** Two commits on `tick/ozw/attempt-4/h3s` (`022f87411`, `a3bec24c4`), working tree clean, `make gate` and the touched packages' full suites green, Bombadil's honest and seeded halves green.

## What changed

The dashboard's groups ran together — the phase track ran straight into the first group and the groups ran into each other. Now, where the screen has room:

- **One blank line between the phase track and the first group** (`renderWatchFrame`'s new `spacer`): added only when the head actually ends with the track — a model without a track already ends its head with the blank the headline puts there, and a second would read as an empty band.
- **One blank line between each adjacent pair of rendered groups** (`dashboardMiddle`'s new `gap`): never before the first group, never around the census note or the cost line, which are not groups. This matches the design doc's target layout (`docs/design/watch-2026-10.md` already drew these blanks — no doc change needed).

**The fit gives up the spacing before it folds any row, and collapses UP NEXT first** — the tick's own mechanism, so the five scenarios keep both their spacing and their rows at 80x24. The fold ladder (first that fits wins) is now:

1. spaced, everything full
2. spaced, UP NEXT without its `then:` line (its queue items stay on the collapsed line)
3. spaced, UP NEXT gone entirely
4. unspaced, everything full; then unspaced UP NEXT graduation (levels 1, 2)
5. unspaced, DONE folded (new level 3; was level 1) + trim descent
6. minimal head (track dropped), the ladder repeating unspaced
7. middle dropped entirely; then the head-only fallback

Fold-level semantics in `dashboardMiddle` changed accordingly: **0 = full, 1 = UP NEXT without `then:`, 2 = UP NEXT gone, 3 = DONE folded** (old levels 1/2 are renumbered; old "DONE folded with UP NEXT full" is no longer reachable — the tick's ordering never folds DONE while the queue still shows extras). `dashboardMiddle` gained a `spaced bool` parameter; it is only called from `renderWatchFrame`.

**Results** (all pinned in the regenerated goldens):

- 80x24: busy 24 lines (keeps its queue and every row, loses only `then:`), held 23 (loses the UP NEXT group whole, keeps every row), failed 24, landed 18, fresh 13 — **spacing stands in all five**.
- 120x40: everything full with spacing (busiest, held, 26 lines ≤ 40).

## Tests

Written first, verified failing against the old renderer (implementation reverted, all three red, then restored):

- `TestDashboardGroupsAreSpaced` — one blank between track and first group and between each adjacent group pair, no two blanks together, at 120x40, all five scenarios (helpers `dashGroupHeaderName`, `dashSpacedGroups`).
- `TestDashboardSpacingFitsEightyByTwentyFour` — every scenario fits 24 lines at 80x24 **with** the spacing structure intact.
- `TestDashboardSpacingCollapsesUpNextFirst` — pins the shed order: busy@80x24 has no `then:` line but keeps `UP NEXT (12)` and all six row ids; held@80x24 has no `UP NEXT` at all but keeps h3/h1/h2.

Plus the comment-only second commit: `TestDashboardFitsHeight`'s doc comment still described the old fold order ("the phase track yields first"); it now states the real order and notes the contract golden carries no UP NEXT group, so the collapse steps are no-ops there (its 15/12/11-line panes produce byte-identical frames to before).

## What I ran

- `go test ./internal/cli/` (full, non-short): **ok, 71.4s** — includes the props suite (P1–P4 over 500 seeded models × 8 widths × 5 heights), fits/colour/drill/pty tests.
- `make gate`: **exit 0** (gofmt, vet, short suite, 50 packages ok).
- `make gate-touched` (the real declared command, with the diff pair exported the way the gate exports it: `TICFAC_GATE_TOUCHED_BASE=37a656963 TICFAC_GATE_TOUCHED_HEAD=HEAD`): **exit 0** — selected exactly `internal/cli` (full suite ok, 78.9s) and `cmd/ticfac` (no test files), the only package whose code imports internal/cli. Without the exported pair the command's by-hand fallback needs a merge base with origin/main, which this seeded worktree branch doesn't have — noted for the next worker, not a defect (the gate exports the pair itself at integration, as cl7's evidence shows).
- `make bombadil` and `make bombadil-seeded`: **all runs answered as they must** — the real-binary terminal suite watches this exact dashboard; its oracles read group headers and the tail marker line-by-line, so the spacing doesn't disturb them, and the fit property holds at every generated size down to 18 rows.

## What the next tick has to know

- **Parallel-tick merge hazard:** az1, cl7 and etl are editing the same renderer and will regenerate the same 11 golden files. The goldens are machine-written (`go test ./internal/cli -run 'TestDashboardScenarioGolden|TestDashboardEpicStateGolden' -update`); merge conflicts in `internal/cli/testdata/*.txt` must be resolved by taking either side and **re-running `-update` on the merged tree**, never by hand-merging frames. Same for `watch_view.go`: my hunks are the `spacer`/`gap` additions, the `spaced` parameter, and the renumbered fold levels (0 full / 1 no-`then:` / 2 gone / 3 DONE-folded) — anything reusing `dashboardMiddle`'s level parameter needs the new numbering.
- The 80x24 goldens for busy (no `then:` line) and held (no UP NEXT line) are **this tick's mechanism, not a bug** — that is "collapsing UP NEXT first".
- cl7's screenshot regeneration will pick up this spacing automatically (the PNGs in `docs/design/watch-2026-10/` show the old unspaced layout until then).

No findings outside this tick's scope.

```findings v2
[]
```

STATUS: DONE
