<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-6/4oj`, base `306f537e4c72611f5ca84f0689473d1389f23ad1`, harness `pi-durable` exited 0, 0 work commit(s), 0 uncommitted path(s)._

# Review of epic ymf — round 2 (tick 4oj)

The epic: **ticfac watch answers the operator's three questions: needs me? healthy? what's
happening now?** (the 2026-10 watch redesign: needs-you first, one health line, one phase
track, ticks grouped NOW/DONE/UP NEXT/HELD with status words, worker excerpts, readable
sentences, metered-only cost, folding t0y/dfb/4dn/zrl). The tree under review is the
integrated `epic/ymf` (my checkout matches it modulo run bookkeeping; the epic's diff
against its base daffbf60a is 76 files, +9473/−2346).

## What I re-verified this round

**The round-1 blocker is fixed.** vii re-pointed the terminal property suite at the new
dashboard, and it is green everywhere I ran it:

- `make bombadil` — both honest runs pass (exit 0): the watch spec's frame marker is now
  the key-hints footer, the grouped-order property pins the NOW/DONE/UP NEXT/HELD headers
  and the plan order within each group, the needs-you wording matches the redesign's
  capital, and the overview runs on a 45-row pane that seats its grown listing.
- `make bombadil-seeded` — all 8 seeded programs violate as they must, including the two
  new ones (`rows-reordered.sh` breaks a group's rows, `groups-reordered.sh` the groups'
  order), so the re-pointed properties are not vacuous.
- CI run **37930297384** on `epic/ymf@35a5caa913` (the code head; every commit after it is
  `.ticfac`/`.tick` bookkeeping under CI's `paths-ignore`) — **12 of 12 jobs green,
  bombadil included**. The three earlier red runs were pre-vii heads.
- `make gate` — exits 0 locally (gofmt, vet, the whole short suite).

**The rest of the acceptance holds on the integrated tree.**

- [A1] the five goldens (`internal/cli/testdata/watch_scenario_{fresh,busy,held,landed,failed}_{120,80}.txt`)
  are committed and checked byte-for-byte by `TestDashboardScenarioGolden` in the gate;
  needs-you first, `● health · N of M done · ETA` on one line, the track with `▲ here`,
  groups with status words — verified at both sizes.
- [A2] worker excerpts on running rows, local (`TestDashboardRowsCarryTheWorkersExcerpt`) and
  cloud (the busy golden's `"adding the /feed handler"`, `"running pytest"`); routing by host
  with its own tests (`internal/cli/worker_activity_test.go`).
- [A3] no TIER/ATTEMPTS columns anywhere; `5az`'s "attempt 2, model escalated" is inline on
  its row only.
- [A4] readable sentences (`TestEveryFeedStageHasASentence`); raw events stay on `[e]` and
  the tick drill-down on enter.
- [A5] cost only when metered, an exact $0.00 is silence, and the busy golden shows the
  subscription window (`MAX1 · 34% of 5h`) — `dashCost` plus `internal/factory/claudesub.go`.
- [A6] all four folded fixes are in the diff with real tests: t0y
  (`TestAnEndedMergedRunReadsItsCIAndMergeDone`, `TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes`),
  dfb (`TestTheHoldAlertWordingsReadANilCommandAsNoCommand`), 4dn
  (`TestAnEndedRunLeavesItsDispatchedRowUnmeasured`), zrl
  (`TestNarrowChildRowsAlignWithTheirParent`). The four tracker records still read open —
  that is the close-out's own move at landing and is already filed (backlog tick pzi).
- [A7] the five 120x40 screenshots are committed in `docs/design/watch-redesign-2026-10/`
  and match the goldens' layout (I decoded them); attaching them to the epic PR is the
  close-out's (jml) move, which runs after this review.

## The deferred finding: reproduced, and I judge it blocking

My tick's notes carry one deferred finding (backlog tick kv3): *"The dashboard words a
tracker-closed tick \"up next\" while counting it done"* — severity high, claiming done item
A1, demonstrated over the tui fixture world. I reproduced it on the integrated tree with the
real binary:

- `ticfac watch --json epic-hld` over the suite's fixture world answers tick **t01 state
  "closed", status "up next"**, `groups.up_next [t01,t02,t03,t04]`, `progress.ticks.closed 2` —
  t02 likewise; `groups.done` is empty.
- The honest watch frame the (now green) suite holds shows both halves at once: `… · 2 of 4
  done` on the health line over `UP NEXT (4)   t01 first wave tick one · t02 … · t03 … · t04 …`.

The cause is in the epic's own new code (`internal/statusmodel/statusword.go`, 514 lines
added by this epic): `statusWordOf` takes the cell's first not-done stage, and for a tick
whose only closure evidence is the tracker — closed by hand, or watched from a checkout
without the closing run's records — `claimState` (pipeline.go:326) answers pending because no
dispatch marker or `claimed` line exists, while `workState`, `gateState` and `endState` all
read the tracker's close as done; `pipelineCell`'s left-to-right fill then renders the whole
cell pending and the word comes out "up next". The model contradicts itself: the same screen
counts the tick done and words it "up next". That input is reachable and steady — it never
self-corrects while the watch is open — and it is the same count-vs-group disagreement t0y
was folded in for ("two renderings of one number cannot disagree"), which the epic pinned a
sibling of with `TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes`. No test pins the
current word as intended; the case is simply uncovered. This is why the epic is not ready:
A1's "ticks grouped by state with status words" is violated on a reachable input, on the
very frame the CI gate certifies green.

The suite itself never catches it: its five properties pin order, wording, fit and the
first-frame bound, but none pins that the grouping agrees with the health line's count — the
hole the contradiction sailed through — which is my second finding below.

The other previously-filed findings (ijh — merged-but-not-closed window; pzi — the four tick
records still open; baq — the criteria parse as one item) stay backlog as triaged; I add
nothing to them.

```findings v2
[
  {
    "kind": "defect",
    "title": "The dashboard words a tracker-closed tick \"up next\" while counting it done",
    "severity": "high",
    "body": "Deferred finding kv3, confirmed on the integrated tree and judged blocking. statusWordOf takes the cell's first not-done stage, and for a tick whose only closure evidence is the tracker (closed by hand, or watched from a checkout without the closing run's records) claimState answers pending while workState, gateState and endState read the close as done — the word is \"up next\" and the tick lands in UP NEXT while the same screen's health line counts it done. It is a steady contradiction on a reachable input, in the epic's own new statusword.go, and it breaks A1's \"ticks grouped by state with status words\": the suite's honest watch frame reads \"2 of 4 done\" over \"UP NEXT (4) t01 · t02 · t03 · t04\" with groups.done empty. The fix is one rule: a tick whose state is closed with no readable records reads done/merged and groups DONE, consistent with the health line.",
    "breaks": {"item": "A1", "check": "pnpm -C tui test"},
    "evidence": "internal/statusmodel/statusword.go:71 (liveStageOf picks claim) with internal/statusmodel/pipeline.go:326-333 (claimState pending with no markers); over tui's fixture world, ticfac watch --json epic-hld answers t01 state \"closed\", status \"up next\", groups.up_next [t01,t02,t03,t04], progress.ticks.closed 2, and the suite's honest frame (tui/.tui-work trace) shows \"· 2 of 4 done\" above \"UP NEXT (4) t01 first wave tick one · t02 …\""
  },
  {
    "kind": "proposal",
    "title": "The watch suite pins order and wording but nothing pins the counts agree",
    "severity": "low",
    "body": "The re-pointed terminal property suite holds the dashboard's frame marker, group order, needs-you wording, pane fit and first-frame bound honest, but no property asserts that the health line's \"N of M done\" agrees with the grouping — the gap through which the blocking contradiction stood green on the suite's own honest frame. A property (with its seeded program) that a tick the health line counts done never reads \"up next\" would close it, and would have caught this round's defect in CI instead of a worker's eye.",
    "evidence": "tui/specs/watch.spec.ts (P1-P5 are the suite's whole watch property set); the honest frame it holds green shows \"2 of 4 done\" over \"UP NEXT (4)\" containing the two counted-done ticks"
  }
]
```

REVIEW-VERDICT: NOT READY — fix the status-word derivation so a tick whose only closure evidence is the tracker reads done/merged and sits in DONE rather than "up next" in UP NEXT while the health line counts it done (tick kv3, breaking A1), and land it so the suite's honest frame renders "2 of 4 done" over DONE (2) and UP NEXT (2); everything else this round re-verified holds — make gate green, make bombadil green (honest 2/2, seeded 8/8) and CI green on epic/ymf@35a5caa913, the five goldens at both sizes, the t0y/dfb/4dn/zrl fixes with their tests, the worker excerpts and metered-only cost

STATUS: DONE — reviewed epic ymf round 2 on the integrated tree: the round-1 bombadil blocker is verified fixed and green (local suite, seeded half and CI 37930297384), the rest of the acceptance re-verified, and the deferred finding kv3 reproduced with the real binary and named blocking — the verdict above is NOT READY on that one finding
