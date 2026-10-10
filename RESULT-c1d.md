<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-6/c1d`, base `e2798863eac23ad51faacf832a591fe2097f6b9d`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# c1d — final review of epic ozw (watch polish), as integrated

Read-only review of the epic as integrated. My checkout is
`tick/ozw/attempt-6/c1d` at `e279886` — the frontier `epic/ozw` held when c1d
was claimed: `323f8135` (the etl merge, the last code change) plus run-state
bookkeeping; the remote tip adds only c1d's own claim records. The base the
epic was cut from is `f17d1220` (main's "tick: close ymf (#272); epic ozw watch
polish"), so `git diff f17d1220..e279886` is the epic's own work: 73 files,
~2,650 insertions over the watch view (`internal/cli/watch_view.go`), the
status model's verdict and note spelling (`internal/statusmodel`), the
committed-PNG capture machinery (`internal/cli/watch_screenshots_test.go`,
`docs/design/watch-redesign-2026-10/capture.sh`), ten re-cut scenario goldens
plus the epic-state golden, contract bundle 2.7.0→2.8.0, and the phone page's
`paused` arm (`cloudflare/src/status.ts`, `phone.ts`).

One caveat on the item marks below: `.tick/issues/ozw.json` carries its five
criteria on one line, and the acceptance parser takes a `[A<n>]` mark only at
a line's start, so the epic's definition of done parses to exactly one item of
record (A1); `[A2]`..`[A5]` are mid-line references no machine can key on.
I judge each criterion anyway, by the epic's own words; the third finding
files the parse defect (it recurs from the ymf review).

## What I ran over the integrated tree

- `gofmt -l`, `go vet ./...`, `go test -short ./...` — **green**, every
  package ok (cli 74s, factory 27s, reconcile 26s, statusmodel, contracts,
  contracts/parity, schema, …). I re-ran the whole short gate myself, not
  only the run's recorded evidence.
- The epic's own new tests, by name: `TestVerdictPausedWhenNeedsYouIsNotEmpty`,
  `TestVerdictPausedOnAMergeWait`, `TestVerdictDegradedOutranksPaused`,
  `TestAtEightyColumnsTheTitleTakesItsWidthBeforeTheStatus`,
  `TestDashboardGroupsAreSpaced`, `TestDashboardSpacingFitsEightyByTwentyFour`,
  `TestDashboardSpacingCollapsesUpNextFirst`,
  `TestCommittedScreenshotsShowTheColourAccents`,
  `TestScreenshotSourceRespellsForTheCapture`, `TestHueFamilyOfRefusesMonochrome`,
  and `TestDashboardProperties` (500 seeded models × 8 widths × 5 heights,
  five properties) plus its non-vacuity half `TestDashboardPropertiesCatchSeededBugs`
  — all pass.
- `go test ./internal/cli -run TestDashboardScreenshotSources -screenshot-dir=/tmp/shots`
  — the five capture sources render from the current tree and equal the
  pinned goldens with colour on; the held source reads `● paused · needs you`
  with the groups spaced (this matters below).
- `pnpm exec vitest run test/phone-page.test.ts` — 30 pass, including the new
  "spells the paused verdict in the terminal's own words (tick etl)".
- `pnpm contracts:check` — ok, 16 contracts at bundle 2.8.0.
- CI, read from the forge: the runs on `epic/ozw` at `36834236b8` (cl7 merge),
  `1aaa20c8e` (az1 merge), `f2472bf01` (h3s resolution) and `323f81355`
  (etl merge) all concluded **success**; the last is the only commit after it
  that touches anything but `.ticfac/` run state.
- Known, tracked, outside this diff: `internal/reconcile`'s full (non-short)
  suite fails `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree` on
  the integrated tree — I reproduced it **identically at the base `f17d1220`**
  in a scratch worktree, it is hai/e1k's pre-existing claim-width hold
  (filed 2026-10-04, before this epic), and CI's go jobs are green at the
  etl merge. Not this epic's regression and not a finding here.

## The acceptance criteria, item by item

**[A1] with anything in needs-you, the health line no longer reads healthy —
met, and well held.** `buildVerdict` derives the word from the needs-you
list itself (`internal/statusmodel/verdict.go:110-112`): a non-empty
attention list and the verdict is `paused` with summary "needs you", after
stopped and degraded keep their precedence — the three new verdict tests pin
all three orderings, including a dead run with the same wait reading
stopped, and `assertValidatesAgainstTheContract` runs on every model they
build. The renderer spells it "● paused · needs you" in amber
(`internal/cli/watch_view.go:1308-1320`), never a colon; the phone page
spells it the same through `verdictWord`'s new `paused` arm with its own
amber dot class, and the contract's closed vocabulary grows the word at
bundle 2.8.0 — enum, ledger entry, CHANGELOG, two hand-authored goldens
re-derived to what the builder produces, the negative re-cut from `paused`
to `halted`, `cloudflare/contracts.pin.json` bumped, and the Go/contract
enum-agreement test extended. The held goldens at 80 and 120 read
"● paused · needs you · 1 of 5 done" byte for byte. The box and the line
render from the same list (`dashboardNeedsBoxes` and `buildVerdict` both read
`m.Attention`), so they cannot disagree; P5 (`propHealthAgreesWithNeedsYou`)
holds the agreement over generated shapes, with a breaker that rewrites the
headline to "● healthy" on a needs-you model and is proven to fail.

**[A2] at 80x24 the title gets the width before the status — met.**
`dashRowColumns` gives the name the room up to its natural width before the
excerpt claims any of it (`internal/cli/watch_view.go:1086-1098`), and the
row's status note renders in its compact spelling — `CompactException`
(`internal/statusmodel/statusword.go`) turns "attempt 2, model escalated"
into "attempt 2 · escalated" for the row only, never widening the note, with
its own never-wider test over the whole note vocabulary. The busy 80 golden
shows `order-feed service skeleton` whole, the widest title at nearly whole
(`smoke validation vs kofoed du…`), the status "writing code (attempt 2 ·
escalated)", and the excerpt the column that gave up its seat; no line
exceeds 80. P5's `propTitleBeforeExcerpt` (a quoted excerpt implies a whole
title) runs over every generated width with its own breaker, and the
non-vacuity test proves both breakers fail. The narrow-width ladder (80/63/47)
is re-pinned to the new allocation.

**[A3] colour accents on a colour terminal, and the committed screenshots
show them — the code half met, the committed-evidence half NOT.** The accents
are real and tested on the styled output: `TestWatchColourGridPerState` now
asserts the paused verdict amber and a held row red — the two accents this
epic named that had no in-process assertion — beside the existing green
done / amber in-flight / red needs-you / dim rows / cyan identities, and the
phone page's paused dot is amber. But the five committed PNGs do not show
this epic's dashboard: see the blocking finding below. The hue guard passes
on them (they were genuinely captured in colour), so the gate stayed green
while the evidence went stale — the guard reads hue families only.

**[A4] a blank line separates the groups and the phase track, within 80x24
for the five scenarios — met.** One blank after the track (the `spacer` in
`renderWatchFrame`), one between each adjacent group (`gap` in
`dashboardMiddle`, never around the census note or cost line), UP NEXT the
first thing that collapses — its "then:" line, then the group — before the
spacing yields, and the five scenarios fit 80x24 with the spacing standing,
pinned by `TestDashboardSpacingFitsEightyByTwentyFour` per scenario and by
the goldens byte for byte at both sizes. The height-fit ladder's renumbered
levels (0 whole, 1 no "then:", 2 UP NEXT gone, 3 DONE folded) are used
consistently by every caller.

**[A5] goldens and screenshots updated; gate passes; CI green — goldens yes,
gate yes, CI yes, screenshots no.** All four workers re-cut the goldens from
the merged renderer (the az1/h3s conflict in three busy/held frames was
resolved by the resolve-conflict job as a regeneration, not a hand edit —
the merged goldens carry both ticks' changes). The run's integrated gate
(go, go-touched, ts) is exit 0 at the etl merge for all three, and my own
re-run is green. CI is green on the branch at every merge, including the
last code commit. The screenshots are the one item the epic did not finish:
captured once at 17:23 for cl7, never regenerated after the three merges
that changed the frames they depict.

## The blocking defect: the committed screenshots show the pre-epic frame

The five PNGs under `docs/design/watch-redesign-2026-10/` were last touched
at `0f14d342c` (2026-10-10 17:23, cl7's attempt). The three merges after
that each changed the pinned frames the capture renders from — az1 at 18:09
(the compact note, the 120-column row allocation), h3s at 19:00 (the blank
lines), etl at 19:31 (the paused health line) — and none re-captured. So the
committed evidence shows a dashboard the integrated code no longer produces:

- `watch-held-120x40.png` shows **"● healthy · 1 of 5 done · ~1h left" in
  green beneath its needs-you box** — the exact contradiction A1 was cut to
  remove, committed as this epic's own verification artifact. At the capture
  commit the held golden read exactly that (`git show
  0f14d342c:internal/cli/testdata/watch_scenario_held_120.txt`, line 7), and
  `TestDashboardScreenshotSources` pins the capture to that golden.
- none of the five shows A4's blank lines — every scenario gained one after
  the phase track, so all five PNGs show the old unspaced layout;
- `watch-busy-120x40.png` shows the status column in the long spelling
  ("attempt 2, model escalated"), one revision of A2 behind.

The workers each saw it and each handed it on: az1's report says
"regenerating it belongs to cl7, alongside the colour work" — but cl7 had
already merged an hour before az1 did; h3s's says the PNGs "show the old
unspaced layout until then"; etl's says the committed screenshots "still show
the pre-etl held page (`watch-held-120x40.png` reads `● healthy` over its
box). The epic's own [A3]/[A5] already require regenerating the screenshots…
No vhs/freeze tool is installed in this checkout, so this tick could not
regenerate them faithfully and did not try." After etl only the review (this
tick) and a bare close-out remain, and 72z carries no description naming the
screenshots. Four green ticks, one stale artifact, no owner left — this
review is where it lands, or nowhere.

Nothing in the gate can catch it: `TestCommittedScreenshotsShowTheColourAccents`
asserts hue families only (a stale capture with good colour passes), and the
one test that pins the capture source to the goldens
(`TestDashboardScreenshotSources`) is skipped without `-screenshot-dir`.
cl7's own report claims "the guard test will refuse a monochrome or stale
capture" — half of that is true. That gap is the second finding.

The fix is one the repository already owns: `capture.sh` regenerates all
five from the integrated renderer, asserts each source equals the pinned
golden with colour on, and verifies itself in place (needs `freeze` on
PATH). The committed evidence will then show the paused health line, the
spacing and the compact note — the dashboard this epic actually ships.

## Non-blocking findings

Both are in the findings block below. The guard gap (medium): nothing in the
gate ties the committed PNGs to the current renderer, which is the mechanism
that let three green merges leave the evidence stale; committing the five
capture sources and asserting them in the gate would make a stale capture
fail `make gate` instead of waiting for a person to read the PNG. The
criteria parse (low): the same one-line acceptance-criteria defect the ymf
review filed low, now recurring on ozw — it is why the blocking finding
above carries no `breaks` claim.

## What I did not find, and what I decided on the deferred findings

- No defect in the verdict precedence I could trip: stopped outranks paused
  for a dead run, degraded outranks it for a live one, a completed run
  without needs-you reads healthy, and a merge wait reads paused — all
  pinned by tests I ran.
- No defect in the title-first allocation I could trip at any width: the
  floors, the separator return, and the fit all hold over the property
  population, and the renumbered level ladder has no stale caller.
- The design doc's status-words section still shows a retry reading
  "attempt 2, on opus" where the row now reads "attempt 2 · escalated" —
  az1's dated-record call, which I accept; the section is prose about the
  vocabulary, not a golden.
- Deferred findings: my tick's notes list none. The two findings the run
  already absorbed from this epic's workers are both **low**, both already
  backlog ticks with owners, and I leave them there: 3j0 (width 0 caps the
  title column at 32 — pre-existing at the base, on the pipe/log path the
  80-column acceptance never touches, and P5 states its expectation) and
  2yn (freeze upstream, render-only, already worked around in-repo by the
  capture source's re-spelling). Neither is a reason this epic is not
  ready.

```findings v2
[
  {
    "kind": "defect",
    "title": "The committed watch screenshots predate the epic: held still reads '● healthy'",
    "severity": "high",
    "body": "The five PNGs in docs/design/watch-redesign-2026-10/ were captured at 0f14d342c (2026-10-10 17:23, cl7's attempt) and never again: the az1 (18:09), h3s (19:00) and etl (19:31) merges each changed the pinned frames the capture renders from, so the epic's committed evidence shows a superseded dashboard. watch-held-120x40.png reads '● healthy · 1 of 5 done' beneath its needs-you box — the exact line [A1] removed, in green — none of the five shows [A4]'s blank lines, and busy's status column reads the long note spelling. etl's report flagged it and named the epic's own [A3]/[A5] as the owner, but no remaining tick carries it: after etl only the review and a bare close-out remain. Run docs/design/watch-redesign-2026-10/capture.sh on the integrated tree (it asserts each source equals the pinned golden and verifies itself in place) and commit the five PNGs. No breaks claim: the epic's criteria parse as one item, so [A3]/[A5] cannot be keyed (third finding).",
    "evidence": "git log -1 --format=%ci -- docs/design/watch-redesign-2026-10/watch-held-120x40.png → 2026-10-10 17:23:06, before merges 1aaa20c8e59e/f2472bf0140e/323f813550e4; git show 0f14d342c:internal/cli/testdata/watch_scenario_held_120.txt:7 → '● healthy · 1 of 5 done · ~1h left'"
  },
  {
    "kind": "defect",
    "title": "No gate check ties the committed PNGs to the current renderer",
    "severity": "medium",
    "body": "TestCommittedScreenshotsShowTheColourAccents asserts only hue families, and TestDashboardScreenshotSources skips without -screenshot-dir, so nothing in the gate compares the committed PNGs — or their sources — with the frames the goldens pin. That is how three green merges left the committed evidence a frame behind this epic's own headline fix, and it makes cl7's claim that the guard 'will refuse a monochrome or stale capture' only half true: monochrome it refuses, stale it cannot see. Commit the five capture sources (watch-<scenario>.ansi) beside the PNGs and assert them in the gate against the styled render, so a stale capture fails make gate instead of waiting for a person to read the images.",
    "evidence": "internal/cli/watch_screenshots_test.go:104-106 (the source writer skips in the gate), :221 (the hue-only assertion)"
  },
  {
    "kind": "defect",
    "title": "ozw's acceptance criteria parse as one item again: [A2]..[A5] are mid-line",
    "severity": "low",
    "body": "internal/acceptance's parser takes a [A<n>] mark only at a line's start, and .tick/issues/ozw.json carries its five criteria on one line, so the epic's definition of done has exactly one item of record (A1) — the same defect the ymf review filed low, recurring, and the run has not applied the tracker edit. This review's blocking finding breaks the committed-screenshots clause of [A3] and [A5]'s 'screenshots updated', but breaks.item cannot name them, so it carries no claim a machine can key on. The fix is a tracker edit — re-mark the criteria one item per line — which is the run's to apply.",
    "evidence": ".tick/issues/ozw.json acceptance_criteria (one line, [A1]..[A5]); internal/acceptance/acceptance.go:18 (attemptedMark is line-anchored)"
  }
]
```

REVIEW-VERDICT: NOT READY — regenerate the five committed screenshots from the integrated renderer (docs/design/watch-redesign-2026-10/capture.sh) and commit them: the held PNG still shows "● healthy" over its needs-you box, and none shows the epic's spacing or compact note

STATUS: DONE
