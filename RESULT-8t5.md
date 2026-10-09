<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-3/8t5`, base `2311a2804c461cbeb43025395dd3046add68dba1`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# 8t5 — final review of epic ymf (the watch redesign), as integrated

Read-only review of the epic as integrated. My checkout is
`tick/ymf/attempt-3/8t5` at `2311a28` — the frontier `epic/ymf` held when 8t5
was claimed: `0aaef7e` ("close jym") plus one run-state commit; the remote tip
`4cac443` adds only 8t5's own claim bookkeeping. The base the epic was cut
from is `daffbf60a` (main's "tick: close t8u (#264)"), which is also the
merge-base — the base fold at `ac181a94` is inside the diff — so
`git diff daffbf60a..HEAD` is the epic's own work: 165 files, ~13,000
insertions. The dashboard rewrite (`internal/cli/watch_view.go`), the status
model's words/groups/track (`internal/statusmodel`), the worker activity
line, the feed sentences, the metered cost + claude-sub line, contract
bundle 2.4.0→2.7.0, ten scenario goldens, five screenshots.

## What I ran over the integrated tree

- `make gate` — **exit 0** (gofmt, vet, the whole short suite, every
  package ok; the scenario goldens and the real-pty test at 80x24 and
  120x40 run in it, verified).
- `make ts-gate` — **exit 0** (biome + its refusal proof, contracts:check at
  bundle 2.7.0, tsc, vitest: 83 files, 1891 tests).
- `make bombadil` — **exit 2, both specifications fail** (below). The tree
  I ran it on is code-identical to `6e95c4c2` (the jym merge): the two
  differ only in `.ticfac/` and `.tick/` run-state files.
- CI, read from the forge: the last three runs on `epic/ymf` — `c8c57d30`
  (ugm merge), `52d7cced` (ugm repair), `6e95c4c2` (jym merge) — all
  concluded **failure**, and the only red job each time is **bombadil**;
  the other eleven jobs (go, go race, contracts, typescript, the reconcile
  shards…) are green. The last green CI on the branch was `39a19d87`
  (2026-10-08 23:49), before the dashboard rewrite landed.

## The acceptance criteria, item by item

**[A1] five scenarios at 80x24 and 120x40, goldens in the gate — met.** The
ten golden frames (`internal/cli/testdata/watch_scenario_{fresh,busy,held,
landed,failed}_{80,120}.txt`) are committed and pinned byte for byte by
`TestDashboardScenarioGolden`, which runs in `-short` (in `make gate`), and
`TestDashboardScenariosAnswerTheThreeQuestions` checks each frame answers
needs-you first, then health with the count, then the track with its marker.
The terminal-emulator half is real: `TestWatchOnAPtyRendersWithoutAStaircase`
drives the binary over a genuine pty at both sizes (carriage returns, pane
fit, the drill-in cursor), and it runs in the gate too. The frames read the
way the design doc draws them — needs-you line/box first, one health line
with `N of M done` and ETA, the phase track with `▲ here`, groups
NOW/DONE/UP NEXT/HELD, status words everywhere, no unlabelled symbol.

**[A2] a live one-line excerpt per running worker, local and cloud — met.**
The model carries `workers[].activity.last_action` (+ its stamp, rendered
with its age on the phone page), gathered locally from the transcript reader
with the pi-durable watch door as fallback (`internal/cli/worker_activity.go`,
`statusmodel.ExecStateRoots` exported for it) and for a cloud run from the
factory's watch socket plus a checkpoint-derived census
(`cloudStandingAttempts`). Both paths are tested with recorded streams
(`worker_activity_test.go`, `status_model_test.go`), including redaction —
no token or URL can appear.

**[A3] no TIER/ATTEMPTS columns; exceptions inline only when they happen —
met.** The row carries id/what/status/elapsed/excerpt; the words `TIER` and
`ATTEMPTS` appear nowhere in the view code. The exception note
(`attempt N`, `model escalated`, `stalled Nm`) is derived only when it
applies (`statusword.go exceptionOf`) and renders inside the status word's
cell (`writing code (attempt 2, model escalated)` in the busy golden).

**[A4] readable latest sentences, raw events on [e] — met.** Every feed stage
maps to an operator sentence with no shas (`feed_sentences.go`), a guard
test (`TestEveryFeedStageHasASentence`) fails when a new stage has no entry,
mechanics are dropped from the section, and the [e] view still prints the
raw `stage: detail` lines (`watchEventLineStyled`).

**[A5] cost only when metered; claude-sub window use — met.** `dashCost`
prints nothing without a metered, non-zero spend; a metered run shows the
rivers; a run whose jobs lease the claude-sub subscription shows
`MAX1 · 34% of 5h` from the factory's `/api/claude-sub` read under the
operator's own token, reduced to the run's lease
(`internal/factory/claudesub.go`, `statusClaudeSub`), never a token. All
three cases are tested (`TestDashboardRendersTheMeteredRiverOnly`,
`TestDashboardCostLineRendersTheSubscription`,
`TestDashboardCostLineRendersMeteredOnly`), and the contract carries
`cost.subscription` (bundle 2.5.0, required-and-null).

**[A6] t0y, dfb, 4dn, zrl fixed — met in the diff, with two caveats.**
t0y's ended-run truths: the DONE header counts the way the health line does
(`TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes`) and a completed
run with no PR left reads its ci chapter done
(`TestAnEndedMergedRunReadsItsCIAndMergeDone`). dfb: the stream path's hold
alert is one nil-safe builder (`holdAlertWording`,
`TestTheHoldAlertWordingsReadANilCommandAsNoCommand`). 4dn: `isLiveAttempt`
now refuses a dead subject run (`TestAnEndedRunLeavesItsDispatchedRowUnmeasured`),
with the rule stated in contract bundle 2.7.0 and the stopped golden
corrected. zrl: proved gone by `TestNarrowChildRowsAlignWithTheirParent`.
The caveats are findings 2 and 3 below: the live-window count pair can
still disagree (a tick merged but not yet closed), and the four defect
ticks are still open in the tracker.

**[A7] screenshots on the epic PR — half done, and structurally so.** The
five 120x40 PNGs are committed under `docs/design/watch-redesign-2026-10/`
(real terminal renders, 1220x850, one per scenario). The epic PR does not
exist yet — the run opens it at land, after this review — and jml's own
title carries "screenshots on the PR"; ugm's report asks the close-out to
attach these five. Not a finding: nothing in the diff can act on a PR that
is not open.

**[A8] make gate passes and CI is green — the first half yes, the second
NO.** `make gate` is green on this tree. CI is red on the epic branch, and
it is red because of this epic's own change: see the blocking finding.

## The blocking defect

The epic rewrote the watch dashboard and the overview's per-run headline
and did not touch `tui/` — `git diff daffbf60a..HEAD --stat -- tui/` is
empty — the terminal property suite that CI's `bombadil` job
(`pnpm run test:all` in `tui/`) drives over the real binary on a real pty,
which the Makefile itself calls "the outside half of the CLI's testing,
where `make gate`'s in-process tests cannot see". Two failures, both
reproduced locally with `make bombadil` (exit 2, 2 of 2 runs):

1. `tui/specs/watch.spec.ts` `frameShown` keys on the literal `"TICK"` —
   the old table's column-header row, which the redesign removed (the base
   `watch_view.go:602` drew `" TICK  WHAT … TIER … ATTEMPTS"`; the new
   dashboard has no column headers at all). So `firstFrameWithinItsBound`
   fails — "frameShown.current was never true before 00:06" over a screen
   that is visibly rendering frames — and the other four watch properties,
   every one of them `always(() => !frameShown.current || holds())`, are
   **vacuous**: the suite now pins nothing about the new dashboard.
2. `tui/specs/overview.spec.ts` `everyStopShowsItsClearingCommand` fails
   because the new per-run headline is three lines (blank line + phase
   track + `▲ here` marker) where the old one-line phase strip was, which
   grows the fixture listing from 25 to 37 lines at 80 columns — past the
   30-row pane the suite's world was deliberately sized for ("the whole
   listing fits a 30-row pane", `tui/README.md`) — so the held run's row,
   the row the property reads for its clearing command, scrolls off the
   screen.

CI shows it: `37889870803` (`c8c57d30`), `37894993735` (`52d7cced`) and
`37903617070` (`6e95c4c2`) all failed, bombadil the only red job, from the
ugm merge on 2026-10-09 05:42 through the tree under review. No tick's
evidence covers this — the per-tick gate is `go`/`go-touched`/`ts`, and the
Makefile and `runners.toml` both leave the pty suite to CI — so the run
closed ugm and jym on gates that never ran it. The epic's own
config rule ("the epic close-out may not complete until CI is green on that
PR") means the run cannot land this epic while the job is red.

The fix is work in `tui/`, and it is re-pointing, not a one-line marker
swap: the watch spec's frame marker must become something the new frame
always shows, P1's row-order property (screen ids a subsequence of plan
order) contradicts the redesign's own principle — "Group ticks by state …
never by plan order" — so it must become a group-order invariant, P2's
lower-case `"needs you: nothing"` no longer matches the dashboard's
`"Needs you: nothing"`, the overview's listing must fit its pane again (or
its world must be re-sized to the new headline's height), and the seeded
programs still print old-layout screens with `TICK` headers, so the
non-vacuity proofs need re-cutting with them.

## Three non-blocking findings

All three are real, all three are in the diff, and none breaks a pinned
acceptance item as written, so all three go to backlog rather than
blocking. The live-run count pair: a tick merged into the epic but not yet
closed sits in DONE and is counted by the `DONE (n)` header while the
health line's `N of M done` still excludes it — `2 of 3 done` beside
`DONE (3)` for the whole integrated-gate window, reproduced through
`statusmodel.Build` over one integrated + two closed ticks. The four defect
ticks' records still read `status: "open"` after jym closed on "each fixed
with a test and closed". And the epic's own acceptance criteria are
written as one line, so the tracker's parser sees exactly one item of
record (A1) and `[A2]`..`[A8]` — the item this review's blocking finding
breaks among them — are mid-line references no machine can key on, which
is also why that finding carries no `breaks`.

## What I did not find, and what I could not check

- No defect in the status-word derivation I could trip: the stalled/escalated/
  attempt exceptions apply only to unfinished attempts, a stopped run's
  in-flight words re-word to `waiting: …` (`statusWordOf`), and the
  word/golden/property tests all hold on this tree.
- The `harness` half of the gate is untouched by this epic and green in CI
  at `6e95c4c2`; `go race` likewise (it was red only on the pre-repair
  `c8c57d30`, fixed by the ugm repair commit).
- Deferred findings: my tick's notes list none. The five findings the run
  absorbed from ugm and jym (h4u, ugt, c3i, zvj, 5u1) are all low/medium,
  already promoted to backlog ticks, and jym's round of them is fixed in
  this diff with tests (ugt≈dfb, c3i≈4dn, h4u≈t0y's count, zvj≈zrl).
  Nothing to re-report at high.
- The PR attachment half of A7 and the tracker closures above both land
  with the close-out (jml) if it does its tick's own title.

```findings v2
[
  {
    "kind": "defect",
    "title": "CI red on epic/ymf: the bombadil property suite still pins the old dashboard",
    "severity": "high",
    "body": "The epic redesigned the watch dashboard and the overview's per-run headline but left tui/ — the terminal property suite CI's bombadil job runs over the real binary on a real pty — untouched, so CI has been red on epic/ymf since the ugm merge (runs 37889870803, 37894993735 and 37903617070 on c8c57d30, 52d7cced and 6e95c4c2, bombadil the only red job; make bombadil fails locally, 2 of 2 specifications). Two causes: watch.spec.ts's frame marker is the \"TICK\" column header the redesign removed, so firstFrameWithinItsBound fails (\"frameShown.current was never true\") and the other four watch properties — all '!frameShown || holds' — are vacuous, pinning nothing about the new screen; and overview.spec.ts's clearing-command property fails because the new three-line per-run headline (blank + phase track + \"▲ here\") grows the fixture listing from 25 to 37 lines at 80 columns, past the 30-row pane the world was sized for, scrolling the held run's row off the screen. This is the epic's unmet \"[A8] make gate passes and CI is green\" — named in the verdict rather than in breaks because the epic's record parses only [A1] as an item (see the fourth finding) — and the repository's own rule holds the close-out on a red PR. The fix is re-pointing the suite at the new layout, not a marker swap: the watch spec's row-order property (a subsequence of plan order) contradicts the redesign's grouping by state, its \"needs you: nothing\" string no longer matches the dashboard's \"Needs you: nothing\", the overview must fit its pane again, and the seeded programs still print old-layout screens with \"TICK\" headers.",
    "evidence": "make bombadil exits 2: 'firstFrameWithinItsBound was violated … frameShown.current was never true' and 'everyStopShowsItsClearingCommand was violated'; CI run 37903617070 (epic/ymf@6e95c4c2, 2026-10-09 08:13) job bombadil=failure, the other eleven jobs green; git diff daffbf60a..HEAD --stat -- tui/ is empty; tui/specs/watch.spec.ts:21-27 (frameShown keys on \"TICK\")"
  },
  {
    "kind": "defect",
    "title": "DONE header and health line disagree while a tick is merged but not closed",
    "severity": "medium",
    "body": "A tick the run merged into the epic but has not yet closed (checkpoint state \"integrated\", the window its integrated gate runs in — minutes to a quarter hour in this repository) reads status word \"merged\" and sits in the DONE group, and the new DONE (n) header counts it, while the health line's \"N of M done\" counts only tracker-closed ticks: one integrated tick beside two closed ones renders \"● healthy · 2 of 3 done\" over \"DONE (3)\". This is the count-pair self-disagreement t0y was about, surviving jym's duplicate-only fix, in the live window every implement tick passes through; the pinned t0y acceptance (an ended+merged fixture, where all ticks are closed) still holds.",
    "evidence": "internal/cli/watch_view.go dashSectionHeader (the DONE count excludes duplicates only); internal/statusmodel/build.go buildWaves (tickProgress.Closed counts tickClosed only); internal/statusmodel/pipeline.go endState (tickIntegrated reads merged done); statusmodel.Build over one integrated + two closed ticks renders \"2 of 3 done\" beside \"DONE (3)\""
  },
  {
    "kind": "defect",
    "title": "t0y, dfb, 4dn and zrl are fixed but the tracker still shows them open",
    "severity": "medium",
    "body": "jym closed on the acceptance \"t0y, dfb, 4dn, zrl each fixed with a test and closed\", and the fixes are in the diff with tests — but all four tick records still read status \"open\" with only the 2026-10-08 planning note \"fixed under epic ymf (tick jym)\", so the epic's own tracker disagrees with the work its diff carries and A6 reads false against the tracker until they close. jym's report says closing is the run's move rather than the worker's; the close-out (jml) should close the four ticks as part of landing, or they stay on the open list and on the epic's PR.",
    "evidence": ".tick/issues/t0y.json, .tick/issues/dfb.json, .tick/issues/4dn.json, .tick/issues/zrl.json — all still status \"open\" after jym closed at 2026-10-09T08:43:41Z"
  },
  {
    "kind": "defect",
    "title": "The epic's acceptance criteria parse as one item: [A2]..[A8] are mid-line",
    "severity": "low",
    "body": ".tick/issues/ymf.json's acceptance_criteria is one line, and internal/acceptance's parser takes a bracketed [A<n>] only at a line's start (a mid-line bracket is a reference, never a definition), so the epic's definition of done carries exactly one item of record — A1 — and [A2] through [A8] are references no machine can key on: [evidence.acceptance] cannot bind a command to them and a findings block cannot name the item it breaks (this review's blocking finding names [A8] only in its body and on the verdict line, because breaks.item: \"A8\" is refused as \"not an acceptance item of the epic\"). The fix is a tracker edit — re-mark the criteria one item per line — which is the run's to apply.",
    "evidence": ".tick/issues/ymf.json acceptance_criteria (one line, [A1]..[A8]); internal/acceptance/acceptance.go attemptedMark ('^[ \\t]*\\[A([^\\]]*)\\]', line-anchored); lint-report on this report refused breaks.item \"A8\" with \"the epic's items are A1\""
  }
]
```

REVIEW-VERDICT: NOT READY — re-point the tui terminal property suite at the new dashboard layout (the watch frame marker, the group-order rows and the needs-you wording; the overview's pane fit; the seeded programs) so `make bombadil` and CI's bombadil job are green on epic/ymf — A8's "CI is green" is the one unmet item

STATUS: DONE
