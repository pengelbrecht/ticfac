# ugm — the watch dashboard rendered per the 2026-10 redesign

Branch `tick/ymf/attempt-1/ugm`, one commit on top of the dispatch snapshot:
`65c7730 ymf/ugm: render the watch dashboard per the 2026-10 redesign`.

## What changed

**`internal/cli/watch_view.go` — rewritten.** The dashboard now renders the
layout in `docs/design/watch-redesign-2026-10.md` from the status-model
fields the four closed blocker ticks built (lck's status words/groups/track,
93n's worker activity, 47j's feed sentences, b13's cost), top to bottom:

1. Identity line: `epic title (epic id)` left; `config · host · running
   55m` right — the state word is `running` while alive, else the run's own
   terminal word (`done` / `failed` / `stopped`), with the model's elapsed
   only when measured. Under it, a full-width `━` rule.
2. Needs-you first: when a hold stands, one box per hold (red, the
   announcement bold, `Needs you: <what>` then `clear with: <command>`),
   wrapped inside the box — never truncated, every word of the command stays
   on the screen. When nothing stands, the dim quiet answer `Needs you:
   nothing` sits left of the health line (splitting to its own line first
   when the pane is too narrow).
3. Health on one line: `● healthy · 3 of 18 done · ~8h left` — verdict, N of
   M done, ETA, each only where the model measured it. The progress bar, the
   TIER/ATTEMPTS columns, the per-tick pipeline cell and the CI checks line
   are gone: the health count and the status words carry what they did, and
   the CI's verdict now arrives through the phase track and the status words
   (`waiting for CI`, `failed: CI is red on the epic PR`).
4. Phase track: `Building ──── Reviewing ──── Closing out ──── PR & CI ────
   Merged` with `▲ here (wave 2 of 3)` under the step the model's
   `lifecycle.track`/`lifecycle.here` name (the marker slides left rather
   than being cut on narrow panes; the track line truncates).
5. Ticks grouped by state from the model's own `groups` — NOW, DONE (n), UP
   NEXT (n), HELD (n). Row = `id  what  status (exception)  elapsed
   "excerpt"`, columns sized to content and aligned; what and excerpt share
   the leftover width and drop (excerpt first, then what, then elapsed)
   below 64/48/40 columns; id and status never drop. The exception note
   rides the status word inline (`writing code (attempt 2, model
   escalated)`) — the inline-only rule of the epic's A3. A running worker's
   row carries its live excerpt in quotes (the activity's last action, else
   the last turn with its role prefix stripped); a gate the run itself
   drives reads `gate running`. Absorbed ticks keep their `+` marker,
   duplicates render dim and named; child indentation is gone (groups make
   row adjacency meaningless — this is what removes zrl's manifestation,
   see findings).
6. UP NEXT is always the collapsed line the design draws: `UP NEXT (12)
   s3r kanpla-etl /sync/intraday · …` (folded with an ellipsis where the
   pane ends), plus a dim `then: Reviewing → Closing out → PR & CI →
   Merged` naming the track steps still ahead of the marker.
7. Cost line (b13 unchanged in content): rendered after the groups, only
   when there is metered cost or a leased subscription.
8. `─ latest ─` rule, then the two most recent readable sentences, newest
   first (47j's mapping; no shas; the try prefix still counted from the
   model's whole histories), with `[enter] details  [e] all events  [q]
   quit` seated right.

The height fit now yields things in a stated order: DONE rows fold into
their group header, then rows trim from the bottom with one `+N more` at
the fold, then the UP NEXT items, then the phase track (a pane crowded
with holds owes its rows a seat before it owes the map one), then the
whole middle — and it never trims the drill-in cursor's row (the cap
extends to include it; this fixed a real defect the pty harness caught:
at 80x24 with two holds, `j` moved a cursor the frame no longer showed).

**Kept working unchanged:** `enter` (tick drill-down, `watch_drind.go`'s
`renderTickView`) and `e` (the raw feed view) — the keys, the reducer, the
raw `stage: detail` lines and the whole stream path are untouched. The
bare `ticfac` overview still renders the dashboard headline from the same
functions (`dashboardHeadline` / `dashboardAttentionLines`).

**Tests:** `watch_view_test.go` rewritten around the five design scenarios —
fresh run, busy wave, held tick (needs-you box), ended landed run, ended
failed run — each pinned byte for byte at **80x24 and 120x40**
(`internal/cli/testdata/watch_scenario_{name}_{80,120}.txt`, regenerated
with `go test -run TestDashboardScenarioGolden ./internal/cli/ -update`).
`watch_props_test.go`'s properties were re-pointed at the new layout (rows
follow the model's own groups; needs-you boxes whole or absent; cost
metered-only; frame fits the pane) over the same 500 seeded models, the
generator now emitting the model fields the renderer reads (status words,
groups, track). `watch_colour_test.go`'s grid assertions, the epic-state
golden (`watch_dashboard_epic_state.txt`, re-recorded from
`statusmodel.Build`), the block/pty wiring tests and the overview headline
tests were updated to the new strings. The old `watch_dashboard_120/60`
goldens were removed (replaced by the scenario goldens).

**Screenshots (epic A7):** the five scenarios at 120x40 rendered with
`freeze v0.2.2` (installed into a scratch prefix, nothing machine-wide
touched) and committed under `docs/design/watch-redesign-2026-10/`
(`watch-fresh-120x40.png`, `watch-busy-120x40.png`, `watch-held-120x40.png`,
`watch-landed-120x40.png`, `watch-failed-120x40.png`) for the epic PR —
please attach these five to the PR description at close-out.

## What I ran

- `make gate` — green (gofmt, `go vet ./...`, full short suite, 48 packages ok).
- `go test ./internal/cli/ -timeout 25m` (not just `-short`) — green,
  including `TestWatchOnAPtyRendersWithoutAStaircase` at 120x40 and 80x24
  through a real pty (the terminal-emulator harness) and
  `TestWatchOnAPtyExitsWhileTheKeysStillHoldStdin`.
- `go test ./internal/cli/ -run TestDashboardScenarioGolden -update` to
  record the ten goldens, then each reviewed by eye against the design doc.

## What the next tick has to know

- The status-model contract did not change; no contract-bundle edit is
  proposed. The renderer now reads `tick.Status`, `tick.Exception`,
  `groups`, `lifecycle.track`/`here` — the fields lck versioned — and
  `statusmodel` itself is untouched.
- The UP NEXT group has no rows anywhere: it is the collapsed line, by
  design. Anything that wants per-queued-tick status words must go through
  the drill-down or the phone page, which still render them from the model.
- The needs-you box wording is `Needs you: <what>` / `clear with:
  <command>`; the overview's attention lines keep the older
  `needs you: <what> — <command>` sentence form (`dashboardAttentionLines`)
  because the overview test and the run's own reports pin that shape.
- The property generator mirrors the status-word derivation locally
  (`propStatusWord` in `watch_props_test.go`) because `statusWordOf` is
  unexported — if lck's vocabulary grows a word, that mirror and
  `dashboardStatusColor` both need the new word.

```findings v2
[
  {
    "kind": "defect",
    "title": "Health line's done-count and DONE group can disagree on duplicates",
    "severity": "low",
    "body": "t0y's class of inconsistency survives the redesign in a narrower form: the model's progress counts exclude closed duplicates (internal/statusmodel/build.go skips ticks with DuplicateOf set) while the renderer's DONE group lists every tick whose status word is merged/done, so an epic carrying a closed duplicate can render '5 of 5 done' beside 'DONE (6)'. The old manifestations (bar vs table vs CI line vs phase strip) are gone; only this one count pair can still disagree.",
    "evidence": "internal/statusmodel/build.go:437 (progress skips DuplicateOf); internal/cli/watch_view.go dashboardMiddle (DONE group takes every merged/done status)"
  },
  {
    "kind": "defect",
    "title": "Watch's stream-path hold alert still dereferences a nil command",
    "severity": "medium",
    "body": "dfb is open and untouched by this tick: in the stream path's hold alert, the switch on hold reason dereferences *clearing with no nil check for several classes, while HoldClearingCommand returns nil whenever the epic id cannot be stated (its own tick-mwt rule), so a run_held line on a run whose epic is unstated panics the watch. One nil guard (fall back to the default case's wording) fixes it.",
    "evidence": "internal/cli/watch.go:440-469 (*clearing); internal/statusmodel/commands.go:185 (return nil when epicID == \"\")"
  },
  {
    "kind": "defect",
    "title": "A dead run's dispatched tick still grows a live per-tick elapsed",
    "severity": "medium",
    "body": "4dn is open and untouched by this tick: the status model treats the newest run's dispatched/reported row as a live attempt without checking whether that run itself ended, so a failed run's stale dispatched row keeps a growing per-tick elapsed while the run-level clock is frozen. The redesigned frame renders the model's field as-is, so the defect now shows as a wrong elapsed on a HELD-group row.",
    "evidence": "internal/statusmodel/build.go isLiveAttempt (per 4dn's record); renderer renders tick.duration_seconds unmodified"
  },
  {
    "kind": "defect",
    "title": "zrl's manifestation is gone; the tick can be closed as fixed",
    "severity": "low",
    "body": "zrl reported that below 48 cells a child row's columns shifted by its 3-cell indent in the old table. The redesigned dashboard has no child indentation in its group rows (groups make row adjacency meaningless), so the defect can no longer occur anywhere in the watch; zrl can be closed against this commit.",
    "evidence": "internal/cli/watch_view.go dashboardRowLine (no ParentTickID handling); old behaviour pinned in git history internal/cli/watch_view.go@base dashboardTable"
  }
]
```

No tracker edits are proposed: the tick's acceptance criteria stand as
recorded, and the screenshot deliverable is fulfilled by the committed
files named above.

STATUS: DONE_WITH_CONCERNS — the five scenario goldens and all gates are green; the concerns are the four open epic-defect findings filed above (t0y's residual count pair, dfb's nil deref, 4dn's stale elapsed, zrl closable), which belong to the epic's A6 and need ticks or closure decisions beyond this tick's scope
