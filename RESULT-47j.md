<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-1/47j`, base `dda6796705730d2fc607a2bc10f2bf52ff7fca8a`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# 47j: readable feed sentences for the dashboard's latest section

## What changed

- `internal/cli/feed_sentences.go` (new): a map from every stage the run
  feed can emit (every `Stage*` constant in `internal/reconcile` and
  `internal/runfeed`) to a short, people-first sentence — no shas, no
  internal stage names. The four stages the tick names as pure mechanics
  (`pushed`, `push_queued`, `policy_stated`, `cleaned_up`) map to `nil` and
  are dropped from this section entirely. `StageGitHubErrorPrefix`'s whole
  family (`github_error_*`) is handled by prefix match. `feedSentenceFor`
  is the lookup `dashboardTail` now calls.
- `internal/cli/watch_view.go`: `dashboardTail` now walks `m.Recent`
  backwards collecting the last two events `feedSentenceFor` accepts
  (skipping filtered mechanics, reaching further back if needed) and
  renders each as `clock  who  sentence` instead of `clock  who  stage:
  detail`. The raw `[e]` events view (`watchEventLineWith`,
  `renderFeedView`) is untouched — it still shows the feed's own words.
- `internal/cli/feed_sentences_guard_test.go` (new): `go/ast`-parses every
  non-test source file in `internal/reconcile` and `internal/runfeed`,
  collects every `Stage*` constant's declared value (resolving the handful
  that alias a `runfeed` constant, e.g. `reconcile.StageRunFinished =
  runfeed.StageRunFinished`), and fails if any discovered value has no
  entry — sentence or explicit `nil` — in `feedSentences`. This is the
  acceptance's "a test fails when a new stage has none": a future stage
  nobody taught a sentence breaks `TestEveryFeedStageHasASentence`, not
  silently.
- Test updates for the new behaviour: `TestTheFrameTailIsReadableSentences`
  (renamed from `TestTheFrameTailIsTheFeedOwnWords`) asserts the tail shows
  sentences and does NOT show the raw `stage: detail` form;
  `TestTheTailDropsPureMechanicsAndReachesBack` (new) proves a
  `pushed`/`cleaned_up` event is dropped and the tail reaches back for a
  readable one instead; `TestTheTailCountsTheTryFromTheWholeModel`'s
  fixture now uses real stage constants instead of invented ones
  (`report_ready`, `merged` were never real feed stages);
  `TestWatchOnATerminalRendersTheEpicInPlace` (watch_block_test.go) waits
  on the new sentence text instead of the raw one.
- Three golden dashboard frames regenerated with `-update`
  (`watch_dashboard_120.txt`, `watch_dashboard_60.txt`,
  `watch_dashboard_epic_state.txt`): only the "─ recent" tail lines changed,
  from e.g. `dispatched: 46x try 2 dispatched (run dispatch #6, branch
  ticfac/run-epic-6in/tick-46x/attempt-6)` to `dispatched, writing code`,
  and `closeout_held: the close-out waits for CI green on the PR` to
  `closing out is waiting on CI`. Diffs reviewed by hand.

## Scope note

The tick's acceptance and description name exactly four stages as "pure
mechanics" to drop. I filtered exactly those four and gave every other
declared stage (including ones the wider epic doc also calls mechanical,
like `tier_derived`, `gate_running`, `push_queue_overdue`) a real sentence,
since the tick's own acceptance criteria does not ask for a broader filter
and nothing tests one. If the operator wants more stages hidden from this
section, that is a follow-up, not a reading of this tick's scope.

## What I ran

- `go build ./internal/cli/...`
- `go vet ./internal/cli/...`
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 go test ./internal/cli/... -timeout 10m` — all pass
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — gofmt clean, `go vet ./...` clean, `go test -short ./...` green across all 53 packages (internal/reconcile's short suite included, 12.5s — I did not run the full non-short reconcile suite per AGENTS.md's guidance not to as a matter of course)

## For the next tick

`feedSentenceFor` and `feedSentences` (`internal/cli/feed_sentences.go`) are
the one place to extend when the epic's later ticks build the NOW/DONE/UP
NEXT groupings, the needs-you line, or anything else that wants a stage's
words rather than its name — the guard test will catch a stage this map
doesn't yet cover. The design doc's own example sentences ("v1g merged into
the epic, now testing", "ww2 finished and merged", "paused: needs you")
are reproduced close to verbatim for `StageIntegrated`, `StageClosed` and
`StageRunHeld`.

## Findings

```findings v2
[]
```

STATUS: DONE
