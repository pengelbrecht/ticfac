# Epic ozw closed out: the watch polish the operator accepted from ymf's merge

The retro of epic `ozw` from its close-out (tick `72z`, attempt 9, run
`run_1fac91062aa546f29910c5796a468976`). It is written from this checkout of the epic's
integrated tree — unshallowed against origin, so the branch's history, the five merge commits and
the attempt branches are readable — the run's records under
`.ticfac/runs/run_1fac91062aa546f29910c5796a468976/`, the ticks' own reports
(`RESULT-az1.md`, `RESULT-cl7.md`, `RESULT-etl.md`, `RESULT-h3s.md`, `RESULT-7px.md`,
`RESULT-c1d.md`, `RESULT-z0b.md`, read off their attempt branches), the tracker records and the
activity feed. Nothing below is taken from memory of the plan, and every acceptance item was
re-checked against the tree as it stands rather than taken from a tick's own account.

## What the epic set out to do

The four follow-ups the operator accepted when merging ymf (PR #272, 2026-10-10), recorded on
`.tick/issues/ozw.json` from reading ymf's golden frames and its committed screenshots: the health
line contradicted the needs-you box (`● healthy` under a hold); tick titles were truncated at 80
columns; the committed screenshots showed no colour; the NOW/DONE/UP NEXT/HELD groups and the phase
track ran together. Five acceptance items, [A1]–[A5], with the one caveat that recurs from ymf:
the record carries them on ONE line, so the machinery parses exactly one item of record (A1) and
[A2]–[A5] are mid-line references nothing can key on — the round-1 review's own third finding
(tick 1ip, open), and the reason its blocking finding carried no `breaks` claim. This close-out
carries the re-flow as a `tracker_edit` finding for the run to apply; the items below are scored by
the epic's own words.

## How the run went

One incarnation, one integration branch, nine attempt dispatches plus one resolve-conflict job.
`epic/ozw` was cut from `f17d12208` (main's "close ymf (#272)"), and its diff against that base is
36 files, +1232/−123: the watch view (`internal/cli/watch_view.go`), the status model's verdict and
note spelling (`internal/statusmodel`), the committed-PNG capture machinery
(`internal/cli/watch_screenshots_test.go`, `docs/design/watch-redesign-2026-10/capture.sh`), ten
re-cut scenario goldens plus the epic-state golden, contract bundle 2.7.0→2.8.0, and the phone
page's `paused` arm (`cloudflare/src/status.ts`, `phone.ts`).

| tick | role | model, tries | outcome |
|---|---|---|---|
| az1 | implement | GLM flash, try 1 settled rejected missing-result (its head carried) → GLM 5.3, try 2 | merged `1aaa20c8e59e` (18:35): title-first row allocation, `CompactException`, busy goldens re-cut, property P5 |
| cl7 | implement | GLM flash, 1 | merged `36834236b8c1` (18:09): the colour diagnosis — the capture ran without colour; `capture.sh`, the source writer, the committed-PNG guard, the five PNGs re-captured |
| etl | implement | GLM flash, 1 | merged `323f813550e4` (20:02): `paused` derived from needs-you in the status model, contract 2.8.0, the phone page's paused arm, the health-agreement property |
| h3s | implement | GLM flash, 1, through one resolve-conflict job over three golden frames | merged `f2472bf0140e` (19:26): the group gap, the phase-track spacer, the fold ladder renumbered |
| c1d | review, round 1 | GLM 5.3 | **NOT READY** — one blocking finding (the committed PNGs predate the epic) and two backlog findings |
| 7px | implement (absorbed, basis `reviewer`) | GLM flash, 1 | merged `29d4f5e31390` (21:25): the five PNGs re-captured in place from the integrated renderer |
| z0b | review, round 2 | GLM 5.3 | **READY** — the committed PNGs verified byte-identical to a fresh capture |
| 72z | close-out | GLM 5.3 | this record |

Every dispatch was a hosted pi-durable conversation on Workers AI GLM in a cloud container; the
reviews and the strong-tier retry on `glm-5.3`, the rest on `glm-5.3-flash` at the economy tier.
The four implement ticks of wave 1 ran in parallel over the same renderer and the same goldens —
the shared-artifact hazard every one of their reports named — and h3s's merge met it in three
busy/held frames, resolved by the resolve-conflict job as a regeneration, not a hand edit
(decision 6). Both review rounds are spent; the bound is 2, and the second verdict is READY, so
the run holds nothing for a person over the bound.

## Delivered against the definition of done

- **[A1] With anything in needs-you, the health line no longer reads healthy — MET, re-verified
  here.** The verdict is derived in the status model, so the terminal and the phone page render
  one word: `buildVerdict` (`internal/statusmodel/verdict.go:111`) returns `paused` with summary
  "needs you" whenever the attention list carries anything, with stopped and degraded keeping
  their precedence — a dead run still reads stopped, a live run with something wrong still reads
  degraded, and a completed run waiting on its merge reads the same calm amber instead of a green
  that hid the person the merge is for. The renderer spells it `● paused · needs you` in amber
  (`internal/cli/watch_view.go:1312-1320`); the held goldens read
  `● paused · needs you · 1 of 5 done · ~1h left` byte for byte at both sizes
  (`internal/cli/testdata/watch_scenario_held_{80,120}.txt:7`); the contract bundle is re-cut at
  2.8.0 with the word in `health_verdict.state`, two hand-authored goldens re-derived to what the
  builder produces, and the phone page agrees through `verdictWord`'s new arm
  (`cloudflare/src/status.ts:681`). Pinned by `TestVerdictPausedWhenNeedsYouIsNotEmpty`,
  `TestVerdictPausedOnAMergeWait`, `TestVerdictDegradedOutranksPaused`, the property "the health
  line agrees with needs-you" (`internal/cli/watch_props_test.go`) with a seeded breaker that
  reproduces the pre-etl defect whole, and the cross-renderer phone-page test — all run green at
  this close-out.
- **[A2] At 80x24 a tick title gets the width before the status — MET, re-verified here.**
  `dashRowColumns` gives the name the room to its natural width before the excerpt claims any of
  it (`internal/cli/watch_view.go:1034-1098`), and the row's status note renders in its compact
  spelling — `statusmodel.CompactException` (`internal/statusmodel/statusword.go:277`) turns
  "attempt 2, model escalated" into "attempt 2 · escalated" for the row only, never widening the
  note. The busy 80 golden shows `order-feed service skeleton` whole with the status shortened;
  measured at this close-out, no line of any 80-column golden exceeds 80 cells. Pinned by
  `TestAtEightyColumnsTheTitleTakesItsWidthBeforeTheStatus`, the re-pointed
  `TestDashboardNarrowWidths`, and the `propTitleBeforeExcerpt` property with its seeded breaker,
  which az1's report proves fails on the reverted allocation at seed 3, width 99.
- **[A3] Colour accents on a colour terminal, and the committed screenshots show them — MET as
  integrated; the committed-evidence half was the round-1 blocker.** The accents are real and
  asserted on the styled output (`TestWatchColourGridPerState`: green merged/healthy, amber
  in-flight/paused, red failed/held/needs-you, dim rows and mechanics, cyan identities, bold
  headers — cl7 added the held-row red and the paused amber, the two this epic named that had no
  in-process assertion). cl7's diagnosis, reproduced on the old files, was that the view never
  dropped its accents — the ymf capture ran without colour — so it built the capture path that
  cannot silently lose them again: `capture.sh`, the source writer whose styled frames are
  asserted equal to the pinned plain goldens
  (`TestDashboardScreenshotSources`), and the committed-PNG guard
  (`TestCommittedScreenshotsShowTheColourAccents`) that decodes the actual committed images per
  scenario and asserts their hue families, with its own oracle refusing the old captures' washed
  greys (`TestHueFamilyOfRefusesMonochrome`). Three later merges then changed the pinned frames
  the capture renders from, and nothing re-captured — the round-1 review found the committed set a
  frame behind this epic's own headline fix (the held PNG still reading `● healthy` in green under
  its needs-you box). The run absorbed that finding as 7px, which re-captured the five in place;
  round 2 verified the fix the strong way — a fresh capture from the integrated renderer is
  byte-identical to the committed set (sha256, per scenario) — and this close-out re-ran the
  committed-PNG guard green on the tree it stands on. What the guard still cannot see is a STALE
  capture with good colour: that gap is m8b, open, below.
- **[A4] A blank line separates the groups and the phase track, within 80x24 — MET, re-verified
  here.** One blank after the track (`renderWatchFrame`'s `spacer`), one between each adjacent
  group pair (`dashboardMiddle`'s `gap`, never around the census note or cost line), UP NEXT the
  first thing that collapses — its `then:` line, then the group — before the spacing yields, with
  the fold ladder renumbered (0 full / 1 no-`then:` / 2 UP NEXT gone / 3 DONE folded) and no stale
  caller. Measured at this close-out: the five 80x24 goldens run 24, 24, 23, 18 and 13 lines
  (busy, failed, held, landed, fresh) — all inside 24 rows with the spacing standing. Pinned by
  `TestDashboardGroupsAreSpaced`, `TestDashboardSpacingFitsEightyByTwentyFour` and
  `TestDashboardSpacingCollapsesUpNextFirst`.
- **[A5] Golden frames and screenshots updated; make gate passes and CI is green — MET at this
  head.** All ten scenario goldens plus the epic-state golden are re-cut from the merged renderer
  (the az1/h3s conflict in three busy/held frames resolved as a regeneration). Every merged tick's
  integrated gate (go, go-touched, ts) passed on its own merge — five evidence records in this
  run's state, the last (7px's) exit 0 over a diff that touches no Go package. This close-out ran
  the whole-repo gate fresh: `make gate` exit 0, gofmt, vet and the short suite over 50 packages,
  plus the committed-PNG guard, the scenario goldens and the three-questions test. CI: the round-1
  review read the forge green at every merge of this epic; the round-2 review read it green at
  `29d4f5e31390`, the head that carries the regenerated screenshots (run concluded success
  2026-10-10 20:59); and this run's own checkpoint admits the close-out on "CI is green on the
  epic PR #273". Since that head the branch has gained only run-state and tracker records.

## What the run absorbed

Six findings, every one named in `.ticfac/runs/run_1fac91062aa546f29910c5796a468976/absorptions/`,
none silent:

- **One on the basis `reviewer`** — the round-1 review judged the epic NOT READY and named the
  stale-screenshots finding blocking, so it became the epic's work, fixed before the re-review:
  the finding → **7px**, re-placed `before-review`. It had first been decided `backlog-default`
  (the reporter named no done item it breaks — and could not: the criteria parse as one item, which
  is 1ip's own defect), so the epic's shape changed once, from its own review, and the record
  says so.
- **Five on the basis `backlog-default`**, each a backlog tick with an owner, listed on the epic
  PR: 22f7 (cl7: freeze's ANSI renderer ignores SGR 2 and drops fills when bold follows) → **2yn**;
  f85f (az1: width 0 says "no truncation" but caps the title column at 32) → **3j0**; 64ca (c1d:
  no gate check ties the committed PNGs to the current renderer) → **m8b**; 7172 (c1d: the
  acceptance criteria parse as one item again) → **1ip**; 0a13 (7px: pin the freeze version
  capture.sh installs) → **s7a**.
- **No amendments**: no worker-proposed note is filed on the epic's record, and `ozw.json` carries
  no notes at all — nothing needs confirming or marking unconfirmed.
- The 2yn routing is the interesting one: the finding is `charmbracelet/freeze`'s, this run cannot
  fix another repository, and `.tick/runners.toml` declares no `[findings.route]` for it, so the
  run filed a backlog tick here carrying the finding verbatim, for a person to carry there.

## What is left open

1. **Five backlog ticks, all open, all on the epic PR.** m8b — the gate gap: nothing ties the
   committed PNGs, or their sources, to the frames the goldens pin, so a stale capture with good
   colour passes `make gate`; the staleness class this epic paid a review round for can recur until
   the capture sources are committed and asserted (or freeze joins CI, which m8b's own report
   notes). s7a — `capture.sh` recommends `freeze@latest`, unpinned, so a re-capture's PNG diffs
   cannot be attributed to the dashboard rather than the tool (today's `freeze@latest` still
   reproduces the committed set byte for byte, per round 2). 3j0 — the width-0 branch caps the
   title column at 32 against its own "no truncation" contract comment, pre-existing at the base.
   1ip — the criteria re-flow, which this close-out carries as its tracker edit. 2yn — the
   upstream carry to `charmbracelet/freeze`, a person's move: the run cannot file into a
   repository it does not route to.
2. **The criteria-parse defect recurred from ymf, and nothing enforces it.** The one-item-per-line
   rule was written into the learnings at ymf's close-out and applied to ymf's own record the same
   day; ozw was planned one-line the next morning, spent the whole epic with [A2]–[A5] unkeyable,
   and its round-1 review could not name the item its blocking finding broke — which is why the
   run's absorption had to fall back on the reviewer basis rather than a done-item claim. The
   run's only reader of the criteria is the PR's done-evidence section, after the fact; there is
   no check at admission. Filed as a proposal finding in this report: the run that admits an epic
   whose marks parse as fewer items than it marks should re-flow or refuse.
3. **The known-failure in `internal/reconcile`, pre-existing and owned.** etl's and both reviews'
   reports each note it: `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree` (and its
   sibling) fail on the integrated tree, reproduced identically at the epic's base `f17d1220` in a
   scratch worktree — hai/e1k's claim-width hold, filed 2026-10-04, before this epic. CI's go
   jobs are green at every merge of this epic. Not this epic's regression, and not a finding here.
4. **The review bound is spent** — two rounds, ending READY. Nothing is held for a person by the
   bound; the epic lands on its own verdict.
5. **`72z.json` carries no description** — the close-out tick was created bare, which the round-1
   review noted in passing ("after etl only the review and a bare close-out remain; 72z carries no
   description naming the screenshots"). The run's absorption machinery filled the gap; the record
   could carry the epic's own summary for the next close-out's reader. Cosmetic, named here for
   the record, not filed.

## The learnings

The compaction rides this report's `protected_change` finding (a cloud close-out still cannot
commit `.tick/learnings.md` — tick 3hw, open: the container's pre-commit hook refuses every
`.tick/` path), the same channel bo9's and ymf's rode. Two rules gain the halves this epic paid
for, at the file's 150-line hard cap, with two same-family merges out to pay for them:

- The prose-data rule gains the enforcement half: ozw's five marks went one line again the day
  after ymf's re-flow, and its round-1 review could key no item onto its blocking finding — so the
  rule is enforced where the record is first read, not repaired at close-out.
- The gate-coverage rule gains committed rendered evidence as the same claim: ozw's five watch
  PNGs aged under three green merges — the gate pins the frames they render, nothing tied the
  images to them — so an epic that names committed screenshots puts the re-capture in the tick
  that LAST changes a pinned frame, or asserts the capture sources in the gate.
- The two boundary pairs (instruction-following, and bo9's read-as-violation noise) merge into one;
  the dz1 resume pair merges into the hn6 restart pair it shares a family with.

## What this close-out leaves in the tree

This document and the worker report on the branch: the record is the delivery. The tracker
re-flow and the learnings compaction travel as findings in the report, applied by the run; the
implementation is untouched here.
