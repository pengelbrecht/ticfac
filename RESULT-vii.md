<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-5/vii`, base `fcc9be9290e3c53698d00cb8dd8898f9c470c8b6`, harness `pi-durable` exited 0, 4 work commit(s), 0 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `fcc9be9290e3c53698d00cb8dd8898f9c470c8b6` is the head of the work it continued, which was cut from `5dc592bdbdfef3d0e95cb48e9ac7b6d2903d7c43`; its work commits are counted from the carried head._

# RESULT-vii — the terminal property suite holds the redesigned dashboard

## What this attempt inherited, and what it did with it

Attempt 5 was resumed from attempt 4's branch (`fcc9be929`): attempt 4 had
re-pointed the Bombadil property suite at the redesigned watch dashboard — the
frame marker, the grouped row-order property, the "Needs you" spelling, the
45-row pane, the three re-pointed seeded watch programs — and was then released
as `missing-result`, so none of it had ever been verified. I verified it first,
claim by claim, before changing anything:

- `make bombadil` (the honest half) passed as inherited: 2 of 2 runs.
- `pnpm run test:seeded` (the non-vacuity half) passed as inherited: 7 of 7.
- I read the honest frames out of the driver's own traces rather than trusting
  the verdicts: the watch frame at every generated size (60×18 … 140×40) draws
  the key-hints footer the new marker keys on — every settled frame, one
  state per resize being the pane before the redraw — the needs-you box with
  the clearing command, and the collapsed `UP NEXT (4)` line in plan order; the
  overview frame at 100×45 seats the whole ~40-line listing, so the held runs'
  rows and their commands stay on screen. The first frame lands at +0.91s
  against the 5s bound.
- `make gate` passed on the inherited tree.

That verification found two gaps, and they are the whole of my diff.

## What changed

**`c352ad05a` — the grouped-order property's group-order claim gets its proof.**
Attempt 4's re-pointed P1 makes two claims: the groups stand in the order the
design draws (NOW, DONE, UP NEXT, HELD), and within a group the ids keep the
plan's order. Only the second had a proof — `rows-reordered.sh` breaks the rows
and leaves the groups in the design's order, and the honest binary over the
fixture world draws a single group, so no honest frame exercised the first
either. The suite's own rule is that a property that cannot fail pins nothing,
so the group-order half was prose. `seeded/groups-reordered.sh` is that proof:
the redesigned layout with the groups standing in an order the design never
drew (UP NEXT before DONE) and every group's own rows in plan order, so the
one violation it forces is the order of the groups — Bombadil names
`groupedTicksKeepPlanOrder` and nothing else. The seeded half now runs eight
programs. The README says the two-programs-per-property shape, and the spec
loses the unused `rows` extract the re-pointing had left behind.

**`72c6e4de1` — the dev window reads the traces the driver actually writes.**
`trace-frame.mjs` read its trace as one string, and how many states the driver
samples is the host's business: the honest overview run on this loaded host
left a 1.2 GB trace, past Node's per-string limit, and the tool failed on
exactly that trace with `ERR_STRING_TOO_LONG` — the dev window answered
nothing for the one run a person would be debugging. Reproduced on the old
code with a 550 MB trace (the same states five times over): exit 1. It now
streams line by line; the frames it prints are unchanged (diffed identical
against the old tool on the watch trace, in both `last` and `all` modes), and
it reads all ten of the current traces, 1 MB to 489 MB.

Nothing outside `tui/` was touched, and nothing in `tui/` was re-pointed a
second time: attempt 4's marker, property, spelling, pane and seeded screens
stand as they were.

## What I ran (foreground, on the committed tree)

| command | verdict |
| --- | --- |
| `make bombadil` | exit 0 — "all 2 runs answered as they must" |
| `make bombadil-seeded` | exit 0 — 8 of 8 "violated, as it must be", the new program included |
| `cd tui && pnpm run test:all` (CI's bombadil job command) | exit 0 — "all 10 runs answered as they must" |
| `make gate` | exit 0 — gofmt, vet, 50 packages ok |
| `node trace-frame.mjs` over all ten current traces | exit 0 on each |

The suite is hermetic (a fixture world, a stub `tk`, no factory, no operator
state), so these runs read nothing outside the checkout. What I could not run
is CI itself: the `bombadil` job on the epic PR is the run's to observe, and
it runs the same `pnpm run test:all` that passed above.

## What the next tick needs to know

- The honest watch frame over the fixture world draws a single group —
  `UP NEXT (4)   t01 · t02 · t03 · t04` — under a health line that says
  "2 of 4 done". That is not a fixture choice: the world's tracker closes
  t01/t02 while the run's own records never name them, and the status model
  words such a tick "up next" (see the first finding below). If that defect is
  fixed the honest frame gains a `DONE (2)` group and P1 starts pinning it
  with no change to the suite — the fixture already carries the case.
- Every claim of the suite now has a seeded program that isolates it except
  the three the second finding names; they predate this tick, so I left them
  for a tick of their own rather than widening this one.
- Timing headroom for CI's slower runners: first frame +0.91s of a 5s bound;
  the honest runs' time limits are 25s (overview) and 15s (watch), and the
  suite ran green on CI before the redesign on the same bounds.
- Traces under load can exceed Node's per-string limit; `trace-frame.mjs`
  streams now, but `.tui-work/traces/` is scratch and can reach a gigabyte —
  it is gitignored, and nothing reads it but the dev window.

```findings v2
[
  {
    "kind": "defect",
    "title": "The dashboard words a tracker-closed tick \"up next\" while counting it done",
    "severity": "high",
    "body": "statusWordOf takes the first not-done stage of a tick's pipeline, and for a tick whose only closure evidence is the tracker, claimState answers pending while workState, gateState and endState all read the tracker's close as done — so the word is \"up next\" and the tick lands in the UP NEXT group while the same screen's health line counts it done. The suite's honest watch frame shows both halves of the contradiction at once: \"UP NEXT (4) t01 · t02 · t03 · t04\" under \"2 of 4 done\". epic.go's stateOf already carries the case (\"the answer came from the tracker or from nothing\"), so it is reached whenever the readable records do not cover the tracker's closure — a tick closed by hand, or watched from a checkout whose run records are still on the run branch.",
    "breaks": {"item": "A1", "check": "pnpm -C tui test"},
    "evidence": "internal/statusmodel/statusword.go:71 with internal/statusmodel/pipeline.go:327-331; over tui's fixture world, `ticfac watch --json epic-hld` answers tick t01 state \"closed\", status \"up next\", groups.up_next [t01,t02,t03,t04], progress.ticks.closed 2"
  },
  {
    "kind": "proposal",
    "title": "Three claims of the terminal suite have no seeded program that isolates them",
    "severity": "low",
    "body": "The suite's non-vacuity rule is one deliberately broken program per property, but three claims still rest on nothing: the watch's frame-fits-the-pane has no seeded program at all; the watch's needs-you property has a second claim — every word of the clearing command on screen — that needs-you-nothing.sh never isolates, because its violation fires on the \"Needs you: nothing\" lie first; and the overview's exits-with-the-table's-own-code has none either. Each needs a program that breaks only that claim, the shape groups-reordered.sh takes for the grouped-order property's second claim.",
    "evidence": "tui/seeded/ holds 8 programs; the uncovered claims are tui/specs/watch.spec.ts:136 (the command words), tui/specs/watch.spec.ts:153 (frame fits the pane) and tui/specs/overview.spec.ts:86 (the listing answers and ends)"
  }
]
```

STATUS: DONE
