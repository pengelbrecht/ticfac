<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-13/8wa`, base `a26d29e1633371be0e4858e08a30dc507186a73d`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `a26d29e1633371be0e4858e08a30dc507186a73d` is the head of the work it continued, which was cut from `552b0d9cdc360e755e92c7e872660ce44273fe32`; its work commits are counted from the carried head._

# 8wa — Close-out PR body: amendments under the body budget

## What the tick asked for

`composePRBodyAt` condensed the findings text (level 1) and the absorption
decisions (level 2) toward `prBodyBudget`, but the 7sn "Amendments to the
epic's record" section wrote each amendment's full `Value` unconditionally, so
a run with many or long worker-proposed notes fell to the forge's last-resort
fit — which truncates the tail, where the readiness section sits.

## What I found at this branch's base

The base **already carries the level the tick names**: `prBodyOmitAmendmentText
= 3` (the most condensed level) omits each amendment's value, keeps its status
line and writes a pointer to the record its value lives in
(`internal/reconcile/closeout_body.go`), with
`TestTheMostCondensedPRBodyOmitsAmendmentValuesNotTheTail` over it. So the
first half of the tick was in hand; nothing here re-implements it, and nothing
moves the section or re-spells the seam.

## What was still open: the level does not bound a one-line note

The level drops the value's text but still quotes `FirstLine()` in the status
line — and a worker's note that is **one line** (a pasted log, a paragraph with
no line break in it, an exception argued in one sentence) is its *own whole
headline*, so the level drops none of it. Written test-first,
`TestTheMostCondensedPRBodyBoundsAnAmendmentWhoseWholeValueIsOneLine` failed at
the base on exactly the tick's failure mode:

- the most condensed body carried the whole one-line value as its headline;
- the composed body was **120142 characters against the 60000 budget**, so it
  fell to the forge's last-resort fit and lost the readiness section at its
  tail.

## What changed

- `internal/reconcile/closeout_body.go`: `prBodyHeadlineChars = 200` and
  `boundHeadline(amendment, condense)`. At the most condensed level the
  headline is cut to 200 runes, by rune never by byte, ending with the `…` mark
  that says the line continues in the record. Below that level every headline
  stays whole — the value is written in full under it there, and a headline
  shorter than its own value would say less than the body already does. The
  operator's decision surface survives as before: status, first words, the
  proposing attempt, the full key that names the record, and the decision.
- `internal/reconcile/pr_reviewer.go`: the "Where to look first" item no longer
  promises the undecided amendments are "listed with their full text under the
  amendments below" — which the condensed body has dropped — and points at the
  record those words live in instead, so the first thing a person reads on the
  PR names where to find what they are being asked to decide.
- `internal/reconcile/closeout_body_limit_test.go`: the two tests above, plus
  `TestBoundHeadlineIsWholeBelowTheMostCondensedLevelAndCutAtIt`, a **short**
  test so the per-tick gate exercises the bound too (the EndToEnd composition
  tests are skipped under `-short`): whole below the most condensed level, cut
  and rune-valid at it, and a multi-line value keeps only its own first line.

## What I ran

- `go test -run 'TestBoundHeadline|TestTheMostCondensed|TestCondensePRBody|TestACondensedPRBody'
  -count=1 ./internal/reconcile/` — ok. The new test was run at the base first,
  where it failed on the two assertions above (reproduced the defect), and
  again with the bound disabled in the fix, same two failures — it is a real
  guard, not a tautology.
- `go test -run 'Body|Amendment|CloseOut|Closeout|Admission|Readiness|LookFirst|Condense|Land'
  ./internal/reconcile/` (58 tests, ~121s, GOTEST_PARALLEL=4 GOFLAGS=-p=2) —
  all green except the two known-red close-out red-CI tests,
  `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree` and
  `TestACloseoutBlockedOnRedCIIsRepairedAndRedispatchedWithoutAPerson`. Both
  fail **identically at this branch's base with my changes stashed**, and both
  are already owned by the open backlog tick **e1k** (see also hai); I am
  deliberately not re-filing them.
- `make gate` (gofmt, `go vet ./...`, the whole-repo short suite) — **exit 0**.

## What the next tick needs to know

- The condensation levels are 0 full, 1 no finding text, 2 absorptions counted,
  3 no amendment value with a **bounded** headline (`prBodyHeadlineChars`).
  `prBodyMostCondensed` is level 3; anything that adds a level must keep the
  selection honest (`TestCondensePRBodyAnswersTheFirstLevelWithinBudget` pins
  the loop).
- A new condensation level should bound what it keeps: the finding level keeps
  every title and key unbounded, so a level 4 that ever exists has the same
  one-line-many-amendments residual the headline bound closed here.

```findings v2
[
  {
    "kind": "defect",
    "title": "A byte-slice cut in epicSummary splits a rune and puts invalid UTF-8 on the PR",
    "severity": "low",
    "body": "pr_reviewer.go bounds the epic's description in the PR body with len() and a byte slice: when the 2000-byte boundary falls inside a multi-byte rune the quoted description is invalid UTF-8 (verified: a description 1999 ASCII chars long followed by a two-byte é yields valid=false and a lone 0xc3 byte). The repo's own discipline is to cut by rune — forge.FitBody and the new boundHeadline both do — so this is the one composition site that can hand a malformed character to the forge's write.",
    "evidence": "internal/reconcile/pr_reviewer.go:167 (len(description) > maxEpicDescription, then description[:maxEpicDescription])"
  },
  {
    "kind": "proposal",
    "title": "No condensation level fitting still costs the PR body its readiness section",
    "severity": "low",
    "body": "The readiness section and the closing 'durable record' line are composed at the very tail of the body, so a run whose findings' titles and keys alone overflow the budget — or one with a few hundred amendments, each keeping its status line at level 3 — still falls to forge.FitBody, which keeps the head and drops exactly those. The tick 8wa description anticipated this with its other option: compose the readiness section before the condensable bulk, or have the last-resort fit keep the sections a person merging reads.",
    "evidence": "internal/forge/body.go:24 FitBody keeps the head and truncates the tail; internal/reconcile/closeout_body.go writes readinessSection after the deferred-findings section"
  }
]
```

STATUS: DONE
