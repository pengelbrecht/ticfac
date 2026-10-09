<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-1-repair-1-745127ae/ugm`, base `81b4dd2fd8805ed931e2937128a310de3d98e37b`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Repair report — tick `ugm` (epic `ymf`), failing gate `gate-ugm-1-go`

## Inputs read

- EVIDENCE records of run `run_6a569ee0f05740ec88858323a007661f` (tick `ugm`, attempt 1, phase `integrated`, over `refs/heads/epic/ymf` @ `c8c57d30c5ad3fc3124a2ac49e5b81cb83cc77f8`):
  - `.ticfac/runs/run_6a569ee0f05740ec88858323a007661f/evidence/gate-ugm-1-go.json` — **fail** (exit 1)
  - `.ticfac/runs/run_6a569ee0f05740ec88858323a007661f/evidence/gate-ugm-1-go-touched.json` — pass (exit 0)
  - `.ticfac/runs/run_6a569ee0f05740ec88858323a007661f/evidence/gate-ugm-1-ts.json` — pass (exit 0)
- TICK record `.tick/issues/ugm.json` — "Render the new dashboard layout at 80x24 and 120x40, with golden frames through a terminal emulator": the dashboard view rewrite per `docs/design/watch-redesign-2026-10.md`, golden frames for five scenarios × two sizes through the terminal-emulator harness, enter/e drill-down kept.
- EPIC record `.tick/issues/ymf.json` — "ticfac watch answers the operator's three questions" (watch redesign, A1–A8).
- The merge that landed the failing work: `c8c57d30` "Merge branch 'ticfac/run-run_6a569ee0f05740ec88858323a007661f/tick-ugm/attempt-1' into epic/ymf" (first parent `9521a41`, tick content `65c7730` "ymf/ugm: render the watch dashboard per the 2026-10 redesign"). Its diff (epic-side view) touches `internal/cli/watch_view.go` + tests + testdata + `docs/design/watch-redesign-2026-10/` screenshots only — nothing under `internal/exec/subprocess`.

## The failing check, in its own words

The declared `go` gate (`.tick/runners.toml` `[testing.commands]`, byte-identical to `make gate`):

```
gofmt -l . | grep -v '^contracts/' | (! grep .) && go vet ./... && \
env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.useConfigOnly \
GIT_CONFIG_VALUE_0=true go test -short -timeout 45m -parallel 12 ./...
```

gofmt and vet passed; the whole tree's short suite passed except one test, in one package:

```
--- FAIL: TestEveryTestRunsInParallelOrSaysWhyItCannot (0.08s)
    parallel_test.go:88: activity_test.go: TestReadTranscriptEventsAnswersTheLastAssistantSentence
    neither calls t.Parallel() nor carries a `serial:` doc-comment line
    naming why it cannot — the suite's wall-clock is what rots when this is forgotten
FAIL	github.com/pengelbrecht/ticfac/internal/exec/subprocess	27.770s
```

`internal/exec/subprocess/parallel_test.go` is this package's own AST-scanning guard (tick x73) enforcing the discipline: every top-level test either calls `t.Parallel()` or carries a doc-comment line beginning `serial:`. Nothing else in the gate failed — in particular all of tick ugm's own dashboard work (`internal/cli`, 68s) passed.

## Root cause, from the history

Tick 93n ("live activity excerpt per running worker, local and cloud") wrote `TestReadTranscriptEventsAnswersTheLastAssistantSentence` (commit `026a6288`, merged into `epic/ymf` as `a69efe51`) **without** the annotation — the epic branch's copy of the suite predates the parallel discipline, and 93n's own gate passed on that branch's tree because the guard was not there yet.

The base-branch merge `ac181a94` ("Merge the base branch into epic/ymf through the resolve-conflict job", carrying tick x73's guard) brought the discipline into the epic tree. That merge resolved `activity_test.go` by taking the base's annotated versions of the tests the two branches share (`TestClaudeTranscriptsAreReadWhereTheHarnessWritesThem`, `TestReadTranscriptEventsAnswersTheWindowAndTheLastToolCall`, `TestReadTranscriptEventsRedactsCredentialsFromTheLastToolCall` — all annotated `serial:`), but 93n's test exists **only** on the epic side, so no conflict ever put it in front of anyone; it came through the merge unannotated. The ugm merge (`c8c57d3`) did not touch the file — the merged tree is simply what the guard polices now, and it refuses it. This is the classic stale-after-merge residue: the tick's own diff was never wrong; the tree it landed into acquired a contract the test predates.

## The repair

The test cannot be parallel: it calls `t.Setenv(EnvTranscriptHome, home)` — the env override the tail reader reads, and the process-wide env is exactly why Go refuses a parallel test that has called `t.Setenv`. So the repair is the annotation the discipline demands, added to the test's doc comment, byte-identical to the line its two siblings in the same file already carry:

```go
// serial: t.Setenv points the transcript home writeTranscript writes through, and t.Setenv refuses a parallel test.
```

- The test's behaviour and its assertions are untouched; tick 93n's intent (the last-assistant-sentence excerpt) and tick ugm's intent (the dashboard rendering it feeds) are both preserved exactly.
- The gate was **not** edited: the gate is this repository's own declared contract and it was right about the tree. No gate configuration change was needed or made.
- One file changed, one line added: `internal/exec/subprocess/activity_test.go`. Source only — no build output, caches, or `runs/` artifacts.

## Verification, run by me over the repaired tree

1. `gofmt -l .` → clean; `go vet ./...` → ok.
2. The guard on its own: `go test -short -run TestEveryTestRunsInParallelOrSaysWhyItCannot -v ./internal/exec/subprocess/` → **PASS**.
3. The failing package's short suite: `go test -short -timeout 15m -parallel 12 ./internal/exec/subprocess/` → ok, 21.9s.
4. **The full declared `go` gate command, byte-identical to the evidence's** → **exit 0**, every package ok (`internal/cli` 75.5s, `internal/exec/subprocess` 26.2s on the first run; a re-run over the committed HEAD also exited 0).
5. The touched half's shape for this repair — the FULL non-short suite of the package my commit touched: `go test -timeout 45m -parallel 12 ./internal/exec/subprocess/` → ok, 31.7s.
6. `gate-ugm-1-ts` is the TypeScript half and is untouched by a one-line Go comment; it had already passed.

## Commit

`4c36fd123eff022fa6b1775822da9cda80cba2fa` — "ymf/ugm repair: give 93n's transcript-sentence test the serial line the merged discipline demands" — one commit on top of the integration head `81b4dd2f` on `tick/ymf/attempt-1-repair-1-745127ae/ugm`; 1 file changed, 1 insertion. Working tree clean; nothing rebased, amended or reset.

## Findings

The repair work itself found nothing outside its tick worth filing: the missing annotation is the whole defect, it is fixed in the commit above, and nothing else in the merged tree failed the gate (the other two checks passed both before and after the repair). No new defect or note was promoted to a backlog tick from this dispatch.

```findings v2
[]
```

STATUS: DONE
