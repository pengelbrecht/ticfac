<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-4-resolve-4-e5234e4c/h3s`, base `d6c904665b562bf248639e120e0e877fe9b61688`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict report — tick h3s vs epic/ozw (az1's merged work)

Branch: `tick/ozw/attempt-4-resolve-4-e5234e4c/h3s`
Resolution commit: `8e9746c` ("Resolve the h3s spacing goldens against az1's
title-first columns"), on top of the conflicted merge snapshot `d6c9046`.

## What was in conflict

Tick **h3s** — "Blank lines between the dashboard's groups" (`parent: ozw`,
the watch-polish epic; its attempt-4 is the side being merged in) against the
**epic/ozw** integration branch, which by merge time already carried sibling
tick **az1** — "At 80 columns a tick title gets the width before its status"
(merged 18:35 as `1aaa20c8e59e`, while h3s's attempt, cut at 16:40, was still
in flight). Both are same-wave siblings of the epic (both block the `c1d`
review tick).

Three files carried git's markers — the watch dashboard **golden fixtures**,
each a byte-exact pin of the renderer's frame:

- `internal/cli/testdata/watch_scenario_busy_120.txt`
- `internal/cli/testdata/watch_scenario_busy_80.txt`
- `internal/cli/testdata/watch_scenario_held_80.txt`

Other files matching a bare `<<<<<<<` grep (under `internal/reconcile/`,
`profiles*/resolve-conflict.md`, `internal/reconcile/testdata/fake-runner.sh`)
contain the marker *as legitimate content* — this repo's own
conflict-resolution code and prose. They are untouched by the merge and carry
no marker at line start.

## How it resolved — a line-by-line union, no side chosen

Both sides' **code** had already auto-merged cleanly in
`internal/cli/watch_view.go`: h3s's blank-line `gap` between groups plus its
height-fit ladder ("the UP NEXT group collapses first … tick h3s"), and az1's
title-first `dashRowColumns` ("Title-first (tick az1)"). Only the fixtures
conflicted, because each tick had regenerated them from the pre-sibling
renderer. The union of the two intents is therefore the merged renderer's own
output, so the three goldens were regenerated with the suite's own `-update`
flag (`go test -run TestDashboardScenarioGolden ./internal/cli/ -update`) and
the union was verified side by side with precision diffs against each side of
the original conflict:

- **from az1 / the epic's side**: title-first columns at 80 columns
  ("order-feed service skeleton" in full, the excerpt column dropped below
  its floor, status shortened to "attempt 2 · escalated"); the 120-column
  frame keeps its note column.
- **from h3s's side**: one blank line between each of NOW/DONE/UP NEXT/HELD
  and under the phase track — and where an 80x24 pane cannot seat both, the
  queue collapses first: held_80 drops the UP NEXT group entirely so the
  spacing survives, exactly the collapse h3s's description names
  ("collapsing UP NEXT first so the five scenarios still fit 80x24").
- The seven unconflicted goldens regenerate **byte for byte unchanged** —
  proof the merged code is the union of both intents and not a third thing.
  Nothing of either side was dropped; the intents are complementary.

The result also lands the goldens onto the design doc's target layout
(`docs/design/watch-redesign-2026-10.md`), which already drew blank lines
between the groups (the epic's A4 criterion).

## Evidence

- `TestDashboardScenarioGolden` (all ten goldens), `TestDashboardGroupsAreSpaced`,
  `TestDashboardSpacingFitsEightyByTwentyFour`, `TestDashboardSpacingCollapsesUpNextFirst`
  — all pass. Every 80-column frame fits 24 rows (busy_80 = exactly 24,
  held_80 = 23); every 120-column frame fits 40.
- **`make gate` exit code 0** (gofmt, go vet, full short suite: 50 packages ok).
- Zero conflict markers remain anywhere in the tree (line-start scan of every
  file, `.git` excluded: 0 hits).
- The commit contains source only — the three golden fixtures; the working
  tree is otherwise clean and nothing under the run's `runs/` prefix was
  touched.

## Findings and notes

The collision itself is filed as a finding below (a planning observation: the
wave, not the text, is where the friction was). One further note for the next
wave, not a defect: tick `etl` (the health word vs needs-you, still
`in_progress`) is *not* in this tree — the held goldens still read "● healthy"
beside a needs-you box, correctly so. When etl lands it will re-lengthen the
health line and re-run the held frames' height fit (held_80 currently drops
UP NEXT to keep its spacing within 24 rows), so etl's goldens should be
regenerated with the same `-update` path rather than hand-edited.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Same-wave siblings of the watch epic collide on the shared golden fixtures",
    "severity": "low",
    "body": "h3s (group spacing) and az1 (titles at 80 columns) partitioned the renderer's code cleanly, but each tick's change affects every frame, so both legitimately regenerate all ten watch_scenario_*.txt goldens and any two renderer-touching siblings of one wave collide in testdata/ however the wave is cut. The collision was mechanical to resolve (regenerate the three conflicted goldens from the merged renderer with the suite's -update flag) and no work was lost, so this is an observation for wave planning rather than a defect: the golden fixtures are the epic's canonical collision surface, and a wave partition that sequences renderer-touching siblings — or a resolve step that regenerates fixtures instead of hand-merging them — avoids the manual merge entirely.",
    "evidence": "internal/cli/testdata/watch_scenario_busy_120.txt — the conflicted golden; internal/cli/watch_view.go carries both ticks' changes (the 'tick h3s' gap and the 'Title-first (tick az1)' columns), so the fixtures were the only collision"
  }
]
```

STATUS: DONE
