<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-13/ulw`, base `9d8488e83667e56d7e6ac52c3b52f52892785804`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# ulw: a held cloud run's needs-you command cannot release the attempt it names

## What changed

The status model's needs-you wait for the CURRENT run's hold spelled the
release command without `--run-id` (`internal/statusmodel/build.go` `buildWaits`,
`SettleCommand(…, "")`). Today's cloud runs write their records and feed under
the factory's `run_<hex>` (the orchestrator container execs `ticfac run-epic
--run-id "$run_id"`, `internal/factory/ticfacentrypoint.go:334`), and `settle`
without the flag defaults its run id to `epic-<epic-id>`
(`internal/reconcile/reconcile.go:1373`) — a store that carries no such
attempt, so the command a person copied from needs-you refused. The fix:

- `internal/statusmodel/commands.go` — new `SettleCommandForCurrentRun`: passes
  the run id through to `SettleCommand` whenever it is neither empty nor the
  epic spelling (`epic-<epic-id>`); a run under the epic spelling keeps the
  bare command every local run has always printed (the current-run spelling
  is deliberately unchanged there).
- `internal/statusmodel/build.go` — the current-run hold's call-site now spells
  `SettleCommandForCurrentRun(m.EpicID, *held.TickID, *held.Attempt, m.RunID)`
  instead of `SettleCommand(…, "")`. The prior-holds call-site (tick z3p,
  `SettleCommand(…, prior.Checkpoint.RunID)`) is untouched, as the tick says.
- `internal/statusmodel/pipeline.go` — the per-tick row's next step
  (`attentionCommand`) is the header command's mandated mirror ("a
  disagreement between a row's next step and the header's command is a drift
  this comment points at", its own comment). It spelled the settle sentence
  inline without a run id, so fixing only the header would have left the model
  disagreeing with itself for cloud runs. It now spells the same sentence
  through the same helper, with the run id the index carries from
  `m.RunID` (added to `pipelineIndex`, threaded through `newPipelineIndex`).

Test-first: all three affected tests failed at base for the right reason (the
command lacked `--run-id`) before the change.

- `internal/statusmodel/build_test.go` — new
  `TestAHeldRunWhoseIDIsNotTheEpicSpellingNamesIt`: a run whose model id is
  `run_1a2b3c4d5e6f` with a `run_held` line must name that id;
  `TestAHeldRunNamesTheCommandThatReleasesIt` (run id `epic-2jn`) pins that
  the epic spelling keeps the bare command, and it still passes unchanged.
- `internal/statusmodel/pipeline_test.go` — the pinned `want` of "a held run's
  refused try names the command that clears it" gains `--run-id run-pip`.
- `internal/cli/status_model_test.go` — `TestStatusJSONEmitsTheModelForACloudRun`
  (the pinned shape the tick's evidence names, line 428) pins
  ``ticfac settle cld t1 1 --run-id run_62c289d1e6f4a2b3c4d --release "<who>"``.

The overview pins (`epic-hld`, `epic-hdh`) and the watch pins (`epic-rmod`)
all use the epic spelling, so they are unchanged by design — checked, not
edited. The status-model JSON contract is untouched: `unblock_command` is a
nullable string; only its value changed.

## What I ran

All foreground, in this worktree:

- `go test -run 'TestAHeldRunWhoseIDIsNotTheEpicSpellingNamesIt|TestAHeldRunNamesTheCommandThatReleasesIt|TestAHoldAResumeSettledIsHistory' ./internal/statusmodel/` — FAIL at base (the new test, on the missing `--run-id`), then ok after the fix.
- `go test -run 'TestPipelineCells/a_held_run' ./internal/statusmodel/` and `go test -run TestStatusJSONEmitsTheModelForACloudRun ./internal/cli/` — FAIL at base, then ok.
- `go test ./internal/statusmodel/` — ok (0.47s).
- `go test ./internal/cli/` — ok (43s, GOTEST_PARALLEL=4, -p=2).
- `make gate` (gofmt, `go vet ./...`, the short suite across the repository) — exit 0.

## Commits

- `184ff65` — ulw: a held cloud run's needs-you settle command names its run
  (source and tests only; no build output, caches or coverage files).

## What the next tick has to know

- The needs-you command for a hold the CURRENT run left now names the run
  whenever the run id is not `epic-<epic-id>`; the epic spelling keeps the
  bare spelling. Any renderer reading the model (`watch`, `status --json`,
  the overview's `clear_with`, the phone page) inherits the fix; nothing
  downstream re-spells the command except the pipeline mirror, which was
  updated in the same commit.
- `settle` already accepts `--run-id` (`internal/cli/cli.go:931`), and the
  prior-holds path has emitted it since tick z3p — no settle-side change was
  needed or made.
- The reconciler's own refusal prose and the watch's stderr hold alert still
  spell the release command without `--run-id` (see the finding): a cloud
  run's log keeps telling people a command that refuses. Not fixed here on
  the tick's one-call-site scope.

```findings v2
[
  {
    "kind": "defect",
    "title": "Release commands in run prose and the watch alert still omit --run-id",
    "severity": "medium",
    "body": "The needs-you fix (ulw) covers the status model, but the same release command is spelled without --run-id in the reconciler's own refusal prose and in the watch's stderr hold alert. For a cloud run whose records live under the factory's run_<hex>, those commands default to epic-<epic-id>, a store that carries no such attempt, and refuse — the same broken release ulw fixed on the model surface, still live on the surfaces a person reads during a hold. The run's own run id is available at every site (r.opts.RunID, runID in the watch's fprintf).",
    "evidence": "internal/cli/watch.go:357; internal/reconcile/dispatch.go:785,2805; internal/reconcile/finish.go:172; internal/reconcile/blocked.go:245,473,479; internal/reconcile/resolve.go:191,203"
  }
]
```

STATUS: DONE
