<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-1/47j`, base `1e6856e10498274aa79dd504f25952d4ce24f9b9`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `1e6856e10498274aa79dd504f25952d4ce24f9b9` is the head of the work it continued, which was cut from `dda6796705730d2fc607a2bc10f2bf52ff7fca8a`; its work commits are counted from the carried head._

# 47j: readable feed sentences for the dashboard's latest section

## State found on the branch

This branch (`tick/ymf/attempt-1/47j`) already carries tick 47j's
implementation, delivered by the preceding harness invocation on this same
attempt and committed as the branch's one work commit (`1e6856e`,
"tick 47j: worker report" — a single whole-tree commit). I verified that
implementation against the tick's acceptance criteria end-to-end rather than
rewriting it. The pieces:

- `internal/cli/feed_sentences.go`: `feedSentences`, a map from every stage
  value the run feed can emit (every `Stage*` constant in
  `internal/reconcile` and `internal/runfeed`) to a short people-first
  sentence — no shas, no internal stage names. The four pure-mechanics
  stages the tick names (`pushed`, `push_queued`, `policy_stated`,
  `cleaned_up`) map to `nil` and are dropped from the dashboard's recent
  section entirely. The `github_error_*` family is matched by prefix.
  `feedSentenceFor(event)` is the lookup, returning `(sentence, ok)`;
  `ok=false` means "drop this event from this section".
- `internal/cli/watch_view.go`: `dashboardTail` walks `m.Recent` backwards
  collecting the last two events `feedSentenceFor` accepts (skipping
  filtered mechanics, reaching further back when needed) and renders each
  as `clock  who  sentence`. The raw `[e]` events view
  (`watchEventLineWith`/`renderFeedView`) is untouched — it still shows the
  feed's own `stage: detail` words, as the description requires.
- `internal/cli/feed_sentences_guard_test.go`:
  `TestEveryFeedStageHasASentence` go/ast-parses every non-test source file
  in `internal/reconcile` and `internal/runfeed`, resolves every
  `Stage*` constant's declared value (including the handful that alias a
  `runfeed` constant, e.g. `reconcile.StageRunFinished =
  runfeed.StageRunFinished`), and fails if any discovered value has no
  entry — sentence or explicit `nil` — in `feedSentences`.
- Three golden dashboard frames regenerated
  (`testdata/watch_dashboard_120.txt`, `watch_dashboard_60.txt`,
  `watch_dashboard_epic_state.txt`); their "─ recent" tail lines now read
  e.g. `18:58:02 46x#2  dispatched, writing code` and
  `19:04:12 run  closing out is waiting on CI`.

## What I verified this invocation

- **The guard fails when a new stage has none** (the acceptance's parenthetical):
  I appended a temporary `StageProbeUnmapped47j` constant to
  `internal/reconcile`, ran `go test -run TestEveryFeedStageHasASentence
  ./internal/cli/`, and it failed naming exactly that constant
  (`feedSentences (feed_sentences.go) has no entry … for: StageProbeUnmapped47j
  ("probe_unmapped_47j")`). I then removed the probe file; `git status` is
  clean and the probe is not committed.
- **Mechanics filtered**: `TestTheTailDropsPureMechanicsAndReachesBack`
  proves a `pushed`/`cleaned_up` event is dropped from the tail and the tail
  reaches back for a readable line instead. All four mechanics map to
  explicit `nil` in `feedSentences`.
- **The tick's named tests all pass** (`go test -run
  'TestEveryFeedStageHasASentence|TestTheFrameTailIsReadableSentences|TestTheTailDropsPureMechanicsAndReachesBack|TestTheTailCountsTheTryFromTheWholeModel|TestWatchOnATerminalRendersTheEpicInPlace'
  -timeout 10m ./internal/cli/`): 5/5 pass.
- `go build ./internal/cli/...` and `go vet ./internal/cli/...`: clean.
- **`make gate`**: green, exit 0 — gofmt clean, `go vet ./...` clean,
  `go test -short` green across all 53 packages, including
  `internal/reconcile`'s short suite (14.2s). I did not run the full
  non-short reconcile suite, per AGENTS.md's guidance against running it
  locally as a matter of course.

## Scope notes

- The tick's description names exactly four stages as pure mechanics. The
  map filters exactly those four and gives every other declared stage
  (including ones the wider epic prose also calls mechanical, like
  `tier_derived`, `gate_running`, `push_queue_overdue`) a real sentence,
  because the acceptance criteria do not ask for a broader filter and no
  test asserts one. If the operator wants more stages hidden from this
  section, that is a follow-up tick, not a reading of this one.
- The description's four example sentences are reproduced:
  `merged into the epic, now testing` (`integrated`), `finished and merged`
  (`closed`), `review says not ready: <one line>` (`review_round`), and
  `paused: needs you` (`run_held`).

## For the next tick

`feedSentenceFor`/`feedSentences` (`internal/cli/feed_sentences.go`) are the
one place to extend when the epic's later ticks build the NOW/DONE/UP NEXT
groupings, the needs-you line, or anything else that wants a stage's words
rather than its name — `TestEveryFeedStageHasASentence` fails the moment a
new feed stage lacks an entry there. The raw `[e]` view lives in
`watch_drill.go` (`watchEventLineWith`), deliberately untouched.

## Findings

```findings v2
[]
```

STATUS: DONE
