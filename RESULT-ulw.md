<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-13-resolve-13-52255f48/ulw`, base `3b9c6a6742ed70bd50b79ca4d36becbaf0743149`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict: ulw into epic/hn6, against eli's merged work

The branch is `tick/hn6/attempt-13-resolve-13-52255f48/ulw`: the merge of
tick ulw's attempt-13 (`115cda72…`) into epic/hn6, left unresolved. Exactly
one file carried genuine conflict markers — `internal/statusmodel/pipeline.go`,
two hunks, and both hunks were the same kind of collision: the two ticks had
each added a different parameter to the same two function signatures.

## The two intents

- **ulw** (mine, the `115cda72…` side): "A held cloud run's needs-you command
  cannot release the attempt it names." Cloud runs write their records under
  the factory's `run_<hex>` id, not the `epic-<id>` spelling `ticfac settle`
  defaults to, so the unblock command the header pins cannot clear the hold it
  points at. The fix threads the model's own run id into the pipeline index,
  so the row's mirrored command spells
  `SettleCommandForCurrentRun(p.epicID, tickID, *held.Attempt, p.runID)` —
  `newPipelineIndex` gains a `runID` parameter, passed from `decorateTicks`
  as `m.RunID`.
- **eli** (the other side, closed and merged into epic/hn6 as `e81e6d25153c`):
  "Try panel's next step disagrees with needs-you for a prior-held tick." A
  prior run's standing hold must outrank the generic non-newest-owner resume
  line in `nextStepOf`, and the release command a row shows must be the same
  answer the header's needs-you words — computed once by `standingPriorHolds`
  and shared. `decorateTicks`/`newPipelineIndex` gain a
  `priorHolds []PriorHold` parameter.

## The resolution — a pure union, nothing chosen

Both parameters survive, in the order that reads naturally (the shared
answer, then the ids):

```go
func decorateTicks(src Sources, merged *mergedRuns, priorHolds []PriorHold, m *Model) {
	index := newPipelineIndex(src, merged, priorHolds, m.EpicID, m.RunID)
...
func newPipelineIndex(src Sources, merged *mergedRuns, priorHolds []PriorHold, epicID, runID string) *pipelineIndex {
```

Eli's doc sentence over `decorateTicks` ("priorHolds is the standing prior-run
hold answer the model computed once for both of its readers…") survives
verbatim — it is a superset of the text ulw's side carried. The intents never
touched each other; only the same signature lines did. Everything both ticks
built *around* those signatures had already merged cleanly and needed no
edits, which is what made the union safe to take without choosing:

- the `pipelineIndex` struct carries BOTH `priorHold` (eli) and `runID` (ulw);
- the constructor populates both — the `priorHolds` loop and
  `runID: runID` sit side by side;
- `nextStepOf`'s prior-hold branch (eli) and `attentionCommand`'s
  `SettleCommandForCurrentRun(…, p.runID)` (ulw) each read their own field;
- `build.go`'s call site `decorateTicks(src, merged, priorHolds, &m)` and
  `buildWaits`' `SettleCommandForCurrentRun(m.EpicID, …, m.RunID)` were
  already consistent with the union, and `Model.RunID`,
  `SettleCommandForCurrentRun` and `standingPriorHolds` are all defined.

There was no genuinely irreconcilable point: this conflict was two
complementary parameters on one line each, and the tree that holds both
intents is the tree with both parameters.

## Finding: the collision is a wave-partition defect, not a text defect

ulw and eli are same-wave ticks of one run (both absorbed from findings
reported by the same z3p attempt, both placed before the final review), and
each independently edited the same two signatures of one file — each change
correct in isolation, each unaware of the other. A better wave partition —
eli landing before ulw cut its attempt, or one tick carrying both the
prior-holds threading and the run-id pass — would have produced exactly this
union with no conflict at all. The wave planner should avoid pointing two
same-wave ticks at one file's signatures; the collision cost a resolve pass
that changed two lines of intent and ten lines of marker.

## Verification

All run on this worktree, all foreground, all green:

- `go build ./...` — ok
- `go test -timeout 20m -parallel 4 ./internal/statusmodel/` — ok
- `go test -run 'StatusModel|Overview|Watch|Status' -timeout 20m -parallel 4 ./internal/cli/` — ok
  (the cli/watch/overview surface ulw's record names as where the pinned
  strings live; both sides' fixtures — the current-run settle spelling and the
  epic-across-runs prior-hold one — pass against the union)
- `make gate` (gofmt, `go vet ./...`, short suite across the repository) — exit 0
- No conflict markers remain: a strict line-start scan for
  `<<<<<<<`/`=======`/`>>>>>>>` finds none in tracked files. The other
  `<<<<<<<` hits in the tree (`profiles*/resolve-conflict.md`,
  `internal/reconcile/{resolve.go,rerere_test.go,refresh_test.go,refresh_resolve_test.go,testdata/fake-runner.sh}`,
  `RESULT-hn6.md`) are documentation and test fixtures about conflict
  handling, untouched by this merge.

## Commits

- `f17ccb9` — resolve the ulw/eli collision in pipeline.go: both the
  prior-holds threading and the run id reach the index
  (`internal/statusmodel/pipeline.go` — source only; no build output, no
  caches, nothing under the run's `runs/` artifact prefix)

STATUS: DONE
