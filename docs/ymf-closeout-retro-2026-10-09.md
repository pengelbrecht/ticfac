# Epic ymf closed out: the watch answers the operator's three questions

The retro of epic `ymf` from its close-out (tick `jml`, attempt 7, run
`run_6a569ee0f05740ec88858323a007661f`, which took over the claim of the failed
`run_91f2952af63f42f08a0ad243b4488842`, which had taken over `run_1a8cce01b53348ef83b341f36f1499bf`).
It is written from this checkout of the epic's integrated tree, the three runs'
records under `.ticfac/runs/`, and the ticks' own records and decision envelopes —
never from memory of the plan. Every acceptance item below was re-checked against
the tree at this close-out rather than taken from a tick's own account.

## What the epic set out to do

The operator's remark of 2026-10-08 — "frankly i don't think the tui is very
intuitive/clear" — became a redesign of the `ticfac watch` dashboard per
`docs/design/watch-redesign-2026-10.md`: the screen answers the operator's three
questions in order (does it need me? is it healthy, and how far along? what is
happening right now?), with a needs-you line first, one health + N-of-M + ETA
line, one epic-level phase track with a you-are-here marker, ticks grouped
NOW/DONE/UP NEXT/HELD with plain-language status words, a live one-line activity
excerpt per running worker, exceptions inline only when they happen, mechanics
behind keys, readable feed sentences, cost only when metered and subscription
window use on a claude-sub run — the drill-down (enter) and events (e) views kept
working — plus the four open watch defects t0y, dfb, 4dn and zrl folded in.

## How the run went

Three incarnations, one integration branch. `run_1a8cce01…` stopped after
dispatching 47j and 93n. `run_91f2952…` took over both, closed the status-model
wave (47j, 93n, b13, lck — b13 through three attempts and six failed
resolve-conflict jobs over `status.go`/`status_model.go`/`watch.go`), dispatched
ugm twice and failed with nothing collected from either. This run took over
ugm's claim at the base the dead one left (`3b5b8bd5`), folded the base in
itself (decision 1: `daffbf60a` → `ac181a94`, contracts conflicts resolved,
bundle re-cut 2.3.0 → 2.6.0), and closed the rest.

| tick | role | model, tries | outcome |
|---|---|---|---|
| 47j | implement | GLM flash, 1 (prior run) | merged `1e6ca05c`: feed stages → operator sentences, mechanics filtered; a test fails when a stage has no mapping |
| 93n | implement | GLM flash, 1 (prior run) | merged `a69efe51`: per-worker activity excerpt with its age, local and cloud, from the conversation streams |
| b13 | implement | GLM flash → 5.3, 3 (prior run) | merged `39a19d87`: the cost line — nothing unmetered, the metered figure, the claude-sub lease label and window use |
| lck | implement | GLM flash, 1 (prior run) | merged `76af4578`: the status model's words — status words, exception notes, phase track, needs-you, NOW/DONE/UP NEXT/HELD groups |
| ugm | implement | GLM flash, 1 (this run) | merged `52d7cced` after one plan-repair (`46d34b83`): the dashboard itself — `internal/cli/watch_view.go`, the five scenario goldens at both sizes, the screenshots |
| jym | implement | GLM flash, 1 (this run) | merged `6e95c4c2`: the four watch defects fixed with tests; 4dn's fix re-cut the pinned contract (bundle 2.7.0, `dashboard_stopped` golden corrected) |
| 8t5 | review, round 1 | GLM 5.3 | **NOT READY** — one blocking finding (the bombadil property suite still pinned the old dashboard) and three backlog findings (ijh, pzi, baq) |
| vii | implement (absorbed from 8t5) | GLM flash, 2 | merged `35a5caa9`: `tui/` re-pointed at the new layout — frame marker, grouped order, needs-you wording, pane fit, eight seeded programs; try 1 was a missing-result whose work was carried |
| 4oj | review, round 2 | GLM 5.3 | **NOT READY** — one blocking finding (kv3, the deferred status-word defect, reproduced with the real binary) and one backlog finding (k6z) |
| jml | close-out | GLM 5.3 | this record |

Every dispatch was a hosted pi-durable conversation on Workers AI GLM in a cloud
container; the reviews and the strong-tier retry ran on `glm-5.3`, the rest on
`glm-5.3-flash` at the economy tier. The two review rounds spent the run's bound
of 2 exactly, and the second verdict names a finding the run cannot absorb past
that bound — see "What is left open".

## Delivered against the definition of done

The epic's record states its eight `[A<n>]` marks on one line, so the machinery
parses exactly one item of record — A1 — and A2–A8 are references nothing can key
on (the round-1 review's own finding, tick baq; this close-out carries the
re-flow as a `tracker_edit` finding in its report). The items are scored here as they read.

- **[A1] Five scenarios at 80x24 and 120x40 — MET in the tree and re-verified
  here, with one live contradiction on a reachable input (kv3, the round-2
  blocking finding).** All ten golden frames are committed
  (`internal/cli/testdata/watch_scenario_{fresh,busy,held,landed,failed}_{80,120}.txt`)
  and byte-compared by `TestDashboardScenarioGolden` in the short gate, which
  this close-out ran green; `TestDashboardScenariosAnswerTheThreeQuestions`
  holds each frame to the three questions in order — needs-you first, the
  health line ("● healthy · N of M done · ETA") second, the phase track with
  `▲ here`, groups with status words, no unlabelled symbol anywhere. The
  exception: over the suite's own fixture world the honest frame reads
  `● stopped: … · 2 of 4 done` above `UP NEXT (4) t01 … t04`, both t01 and t02
  closed — the status-word rule and the health line disagree for a tick whose
  only closure evidence is the tracker. Reproduced at this close-out with the
  real binary (`ticfac watch --json epic-hld` over `tui/`'s world: t01 state
  `closed`, status `up next`, `groups.up_next` all four, `progress.ticks.closed`
  2). That is kv3, tick open, and it is the reason the final verdict is NOT
  READY.
- **[A2] A live one-line excerpt per running worker — MET.** 93n's derivation
  (`internal/statusmodel/activity.go`) and the busy golden's rows render it:
  `"adding the /feed handler"` at 19m, `"running pytest"` at 8m, each with its
  age. `TestDashboardRowsCarryTheWorkersExcerpt` pins the row half in the gate;
  93n's own acceptance bound the local (runner log / worker.sqlite) and cloud
  (the factory's worker stream) halves with recorded streams.
- **[A3] No TIER/ATTEMPTS columns; exceptions inline only when they happen —
  MET.** The goldens carry no tier or attempt column; the one retry in the
  fixtures reads inline as `writing code (attempt 2, model escalated)`
  (`exceptionOf`: the note exists only when the try is not the first, the tier
  ladder re-cut the model, or the worker is stalled past the run's own
  threshold), and every other row carries nothing.
- **[A4] Readable sentences, no shas; raw events stay on [e] — MET.** The
  goldens' `─ latest ─` sections read `13:08 v1g merged into the epic, now
  testing`; 47j's mapping covers every stage the feed emits with a test that
  fails when a new stage has none, and the mechanics (pushes, shas) are filtered
  out of this section. `[e]` still opens the raw feed (`events.go`), and the
  key-hints footer keeps its line on every frame.
- **[A5] Cost only when metered; a claude-sub run shows window use — MET.**
  The landed golden carries `config claude · cost Workers AI $0.41`; the busy
  golden (a claude-sub run) carries `config claude · MAX1 · 34% of 5h`; the
  fresh, held and failed goldens carry no cost line at all.
  `TestDashboardRendersTheMeteredRiverOnly`, `TestDashboardCostLineRendersMeteredOnly`
  and `TestDashboardCostLineRendersTheSubscription` pin the three cases in the
  gate. b13's half beyond the dashboard (the phone page) has two open backlog
  ticks (rpc, bt4) — the phone page is not this epic's item.
- **[A6] t0y, dfb, 4dn, zrl fixed — fixed in the tree, with tests, but the
  tracker still shows all four open.** The fixes are real and re-verified here:
  `TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes` (t0y, folding h4u's
  duplicate count), `TestTheHoldAlertWordingsReadANilCommandAsNoCommand` (dfb —
  and the stream-path echo it was reported through, tick ugt), the 4dn clamp
  (`TestAnEndedRunLeavesItsDispatchedRowUnmeasured`, with the pinned contract
  re-cut at 2.7.0 to state the rule), and `TestNarrowChildRowsAlignWithTheirParent`
  proving zrl's manifestation gone in the grouped layout. All four records
  still read status "open" with only the 2026-10-08 planning note — that is
  pzi's finding, an open tick: jym's report had closing them as the run's move
  rather than the worker's, and nothing since has. Against the tracker A6
  reads false.
- **[A7] Screenshots of the five scenarios at 120x40 attached to the epic PR —
  MET as the branch carries them.** All five are committed under
  `docs/design/watch-redesign-2026-10/` — `watch-{fresh,busy,held,landed,failed}-120x40.png`
  — so PR #272 shows them with the branch. Linked here for the operator's eye:
  [fresh](design/watch-redesign-2026-10/watch-fresh-120x40.png),
  [busy](design/watch-redesign-2026-10/watch-busy-120x40.png),
  [held](design/watch-redesign-2026-10/watch-held-120x40.png),
  [landed](design/watch-redesign-2026-10/watch-landed-120x40.png),
  [failed](design/watch-redesign-2026-10/watch-failed-120x40.png).
- **[A8] make gate passes and CI is green — MET at this head, re-run here.**
  `make gate` exit 0 (gofmt, vet, the short suite over 50 packages, including
  every dashboard and status-model test named above); `make bombadil` exit 0 —
  the honest half, 2 of 2 specifications over the real binary on a real pty;
  `make bombadil-seeded` exit 0 — 8 of 8 deliberately broken programs fail as
  they must. Each merged tick's integrated gate (go, go-touched, ts) passed on
  its own merge (the evidence records are in this run's state); the round-2
  review verified CI green on `epic/ymf@35a5caa9` (run 37930297384), and this
  run's own checkpoint admits the close-out on "CI is green on the epic PR
  #272". Since that head the branch has gained only tracker and run-state
  records — the review's own close among them — and this retro.

## What the run absorbed

From `.ticfac/runs/run_6a569ee0f05740ec88858323a007661f/absorptions/` — twelve
decision records, all named, none silent:

- **One on the basis `reviewer`** — the round-1 review judged the epic NOT
  READY and named the bombadil finding blocking, so it became the epic's work,
  fixed before the re-review: the suite-pinning finding → **vii**, re-placed
  `before-review` (it had first been decided `backlog-default`). vii's merge
  re-pointed `tui/` at the new layout and added the seeded half's
  non-vacuity proofs.
- **One on the basis `worker-asserted-high`, deferred** — kv3: vii's second
  try reported the tracker-closed-tick status-word defect rated high, naming
  item A1 (`pnpm -C tui test`) — after the epic's work was done, so the policy
  deferred it to the final reviewer. Round 2 reproduced it with the real binary
  and named it blocking: the verdict this epic ends on. Because that round also
  spent the review bound, the run absorbed nothing further — kv3 stays a
  backlog tick with an owner, listed on the epic PR under deferred findings.
- **Ten on the basis `backlog-default`** — h4u, ugt, c3i, zvj (from ugm), 5u1
  (from jym), ijh, pzi, baq (from the round-1 review), xbf (from vii), k6z
  (from the round-2 review) — each a backlog tick with an owner, listed on the
  epic PR.
- The failed `run_91f2952…`'s seven promoted findings (pl5, pta, 0by, r58, rpc,
  bt4, lgs — all `backlog-default`, all still open) ride the same PR.
- **No amendments**: no worker-proposed note on the epic's own record is filed
  with this run, and `ymf.json` carries no notes at all — nothing here needs
  confirming or marking unconfirmed.

## What is left open

1. **kv3 — the blocking finding, and the person's three options.** The run does
   not merge an epic its final review judged NOT READY: its landing hold will
   name decision 8, "after 2 review round(s), the bound being 2", and the
   verdict rides PR #272. Fix kv3 (the review's stated rule: a tick whose state
   is closed with no readable records reads done/merged and groups DONE) and
   run the epic again; merge the PR by hand to accept it as it stands; or close
   the epic with the defect standing. The fix itself is small — one rule in
   `statusWordOf` — and k6z (open) names the property the suite should gain so
   this class cannot stand green again.
2. **The review bound met a deferred finding at its last round.** kv3 was
   reported inside the epic's own window but after round 1, deferred to the
   reviewer by the 2026-10-06 policy, and adopted as blocking on the bound's
   final round — where `answerNotReadyReview` absorbs nothing. The run ended
   held over a defect it could have fixed in the same shape vii fixed round
   1's. Whether a blocking finding first named on the last round should buy
   the round its fix needs is an operator question; filed as a finding in the report.
3. **A6 against the tracker.** t0y, dfb, 4dn, zrl — and the defect echoes the
   workers filed while fixing them (ugt for the hold alert's stream path, zvj
   recording zrl's manifestation gone) — are all open records whose content
   the tree already delivers. pzi names the four; closing them is a person's
   move (the run never closed ticks outside its own epic, and a worker cannot
   write the tracker).
4. **The epic's acceptance criteria parse as one item** — baq (open), and the
   reason no command could bind evidence to A2–A8 all epic. This close-out
   carries the one-item-per-line re-flow as a `tracker_edit` finding for the
   run to apply; baq remains the record of it.
5. **The backlog.** Eighteen open backlog ticks across the two runs that reported
   findings: from this run — h4u, ugt, c3i, zvj, 5u1, ijh, pzi, baq, kv3, xbf,
   k6z; from the failed run — pl5 (the one contract finding: the status-model
   contract still says a cloud run's census cannot be taken), pta, 0by, r58,
   rpc, bt4, lgs. The PR lists every one.
6. **The learnings destination (tick 3hw, open).** A cloud close-out still
   cannot commit `.tick/learnings.md` — the container's pre-commit hook
   refuses every `.tick/` path and the boundary sweep restores the directory
   before the salvage. The compaction below therefore rides the same channel
   bo9's did: a finding carrying the whole new file as a `protected_change`,
   which the run applies onto the epic branch as its own labelled commit for
   the merger to review.
7. **The review bound is spent**: two rounds, two NOT READYs. A third review is
   a person's, by the run's own rule — or the next run's, over a tree that
   changes.

## The learnings

Three additions, carried as the protected change in the report's findings (the
whole new 150-line `.tick/learnings.md`, a compaction — one new pair and two
rules extended in, four same-family merges out, at the file's hard cap):

- The prose-data rule gains ONE PER LINE: ymf's eight `[A<n>]` marks on one
  line parsed as item A1 alone and blinded the machinery to A2–A8 all epic —
  ex6's four marks did the same, and the rule was lost in a later compaction.
- The render-state rule gains the suite half: an epic that renders state lists
  every suite that asserts each surface — gate or CI-only — with the re-pointing
  in scope; ymf shipped its goldens and left `tui/` pinning the old screen, so
  every CI run on the branch was red from the first merge while the per-tick
  gate stayed green, and the final review was CI's first reader.
- A new rule for derived-fact pairs: the re-pointed suite pinned wording, order
  and pane fit and held green over a frame whose health line read "2 of 4
  done" while UP NEXT held all four ticks — a screen that renders two facts
  from one model gets a property pinning their agreement, with a seeded program
  that breaks only that claim.

## The next feasible epic

**1lq, "Halve the internal/reconcile test suite"** — planned 2026-10-08 from
t8u's final review and the operator's re-scope ("merge them all"), its items
already one per line, its two structural work ticks (9xx, the tracker mirror's
per-write path; kf1, the reconciler's origin reads) unblocked, its review (yet)
and close-out (vkh) behind them, and its before recorded in the record itself
(t8u's baseline: 293 serial minutes, CI reconcile shards 287/247/183s) — the
shape the speed-epic learning asks for. dm6 (GitHub App access, priority 1)
needs the operator's own App clicks and installs before any run can prove its
items; dha's premise predates umq's cutover and wants a re-read before
planning (ex6's retro said so and nothing has answered); ymf's own leftovers
are single ticks and one small fix, not epic material.

## What this close-out leaves in the tree

This document (`docs/ymf-closeout-retro-2026-10-09.md`) and nothing else: the
record is the delivery. The tracker edit and the learnings compaction travel
as findings in the report, applied by the run; the implementation is untouched
here.
