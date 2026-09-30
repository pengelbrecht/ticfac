# Status model reads worker activity and attempt reports (hn6, ltg)

Wave 2 of epic hn6: the two per-attempt readers the dashboard shows from this
machine, filled behind the wave-1 contract's fixed shapes. Nothing outside
`Sources.Activity` / `Sources.Report` changed its wiring; cloud runs still
pass nil readers and their workers and reports state null.

## What changed

**`internal/exec/subprocess/activity.go`** — the one exported helper this
tick adds (the only file this tick may edit in that package this wave):
`ReadTranscriptEvents(home, kind, cwd)` reads the newest `*.jsonl` session
transcript of one worktree — at most the last 256 KiB — and answers every
dated line's stamp plus the LAST tool call as one bounded line: the tool's
own name plus its first argument ("bash: go test ./internal/reconcile"),
pi's `toolCall`/`arguments` and Claude Code's `tool_use`/`input` spellings
both read, a non-text first argument falls back to the tool's name alone,
and the line is cut at 80 runes with an ellipsis. `TranscriptDir` gained the
home-parameterised half (`transcriptDirIn`) so a reader passes its own home;
`LastTranscriptEvent` and the stuck watch read exactly as before, now over
the shared newest-file and tail readers (their tests are unchanged and
green).

**`internal/statusmodel/activity.go`** — `TranscriptActivity(home)` maps the
runner to the harness (a string naming claude → Claude Code's layout, else
pi's) and answers `ActivityInput`; `decorateWorkers` buckets the moments
into the stated window — `window_seconds` 600, ten one-minute counts of
events in (now-600s, now], oldest first — carries `last_action` and
`last_action_at` from the input, counts the run's `stuck_nudged` lines
(`reconcile.StageStuckNudged`) per (tick, attempt) even when no transcript
could be read (empty buckets only when nudges > 0, null otherwise), and
reads the executor's handle off the attempt record's job handle map. The
string the activity seam is addressed by is the attempt's MODEL when its
provenance names one (the model is what says which harness runs the
worker), else the executor — the mapping accepts either, as the tick's
"an executor or model naming claude" says.

**`internal/statusmodel/report.go`** — `AttemptReports(repo, runID)` answers
a (tick, attempt) report: the summary from the archived `report.md`
(`subprocess.FileReportArchive`) beside the attempt record under the
executor state root, located the way `priorReports`/`findAttemptState` in
internal/reconcile do — the state-root walk for `attempt.json`, with the
attempt-record-names-this-tick guard the cross-run discovery holds — over
`$TICFAC_EXEC_STATE_DIR/runs` when the environment names one, else every
executor's `runs` under `~/.ticfac/exec`; the first non-empty non-heading
paragraph, bounded to 300 characters. The diff is
`git diff --shortstat <merge-base>..<attempt branch>` in the repo, the
attempt branch spelled as internal/reconcile writes it
(`ticfac/run-<run>/tick-<tick>/attempt-<n>`, confirmed), run through
`gitbin.Path()` with `gitbin.TransportEnv()` bound (the whole-repo guard
`TestEveryGitThatCanReachARemoteIsBounded` refuses an unbounded git and
caught my first draft — fixed, not whitelisted). `DiffRead` is false when
the branch is gone or no run ref resolves a merge base. Results are cached
per (tick, attempt) in a `sync.Map` inside the returned func, nil answers
included: a two-second redraw spawns no git. `decorateReports` lays the
drill-in on every tick with a current attempt whose state is not dispatched,
null wherever the reader answered nothing.

### One ambiguity, resolved and logged

The tick's "the merge base is against `ticfac/run-<runID>`" cannot mean a
BRANCH: `refs/heads/ticfac/run-<run-id>` is a file/directory conflict git
refuses inside the attempt branches' own namespace (`refs/heads/ticfac/
run-<run-id>/tick-…/attempt-…` — the test proved it by failing). The only
ref of that name that can exist is the run TAG `runstate.TagName` places at
terminal state. So the reader resolves the run ref as `ticfac/run-<run-id>`
first (the tag, which `git merge-base` accepts once the run placed it) and
as the integration branch a local run derives from its run id
(`epic-<epic>` → `epic/<epic>` — the branch every attempt is cut from, and
the live-run case where the tag does not exist yet) second. A run that
carried a custom `--branch` names nothing either way and its diffs state
"not read" — never a number against a base nobody resolved.

### One grep the tick asked for, and what it found

"check what keys the herdr and subprocess executors write by grepping … for
job_handle": herdr names its worker in the nested `handle` object of the
executor's own `JobHandle` (`agent_name`, `pane_id`); the subprocess
executor's nested handle carries pid/worktree/branch; and the RUN's attempt
record — `runstate.Attempt.JobHandle`, which is what decorateWorkers is
given — is the reconciler's `attemptHandle.asMap()`, which carries executor,
job_id, write_ref, base_sha, model and friends but **no agent, pane or name
key for any executor**. `handleOf` therefore accepts the tick's literal keys
(`agent`, `pane`, `name`) and the nested-object spellings the grep found
(`agent_name`, `pane_id`), and answers null when nothing names the worker.
That the production records never name one is filed as a finding below: the
handle cell is dead until the marker or a reader learns where herdr's names
live.

## What was run

Test-first: the six tests the tick names were written before the
implementations, run red (both packages failing to build or failing on the
stub assertions), then made green.

- `go test -short -timeout 20m -run 'TestActivity|TestReport|TestTheContract' ./internal/statusmodel/` — **passes** (the tick's named command; all headless: temp homes via `TICFAC_TRANSCRIPT_HOME`, temp executor state roots, no herdr, no real `~/.pi`).
- `go test -short -timeout 20m ./internal/statusmodel/ ./internal/exec/subprocess/ ./internal/cli/` — **passes** (subprocess 64s, cli 28s; the wave-1 `TestBuildEmitsTheDashboardFieldsEmpty` still passes over the running-epic fixture — the fixture's attempt markers carry no handle names and its feed no nudges, so the empty assertions hold as the wave-1 test said they would until wave 2 fills them).
- `make gate` — **passes** (exit 0, 44 packages ok, after the one fix the gate's whole-repo guard forced: `runGit` now carries `gitbin.TransportEnv()`).
- `go test -race -short -count=1 ./internal/statusmodel/ ./internal/gitbin/` — **passes** (CI's race job runs the same code).

New tests: `internal/statusmodel/activity_test.go`
(TestActivityBucketsTranscriptEvents, TestActivityCountsNudgesWithoutATranscript,
TestActivityIsNullWithNothingToSay, TestActivityCarriesTheWorkersExecutorHandle,
TestActivityReadsAClaudeTranscriptByItsModel),
`internal/statusmodel/report_test.go` (TestReportReadsSummaryAndDiff,
TestReportIsAlsoFoundUnderTheDefaultStateRoots, TestReportIsNullWhenNothingIsArchived,
TestReportCachesPerAttempt, TestReportsDecorateTheSettledTicks), and
`TestReadTranscriptEventsAnswersTheWindowAndTheLastToolCall` in
`internal/exec/subprocess/activity_test.go`. The report tests build real git
repos; the whole statusmodel package runs in ~2s.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "Worker.handle stays null: no executor names the worker on the attempt marker",
    "severity": "low",
    "body": "runstate.Attempt.JobHandle is the reconciler's attemptHandle.asMap(), which carries executor, job_id, write_ref, base_sha and model but no agent, pane or name key for any executor. herdr's agent_name/pane_id live only in the executor's own state (the herdrHandle inside its JobHandle), which the run's attempt record never carries, so the dashboard's workers-panel handle cell — 'herdr pane tick-v7z-a11' in the hn6 spec — renders null in every real run until the marker or a reader is taught where those names live.",
    "evidence": "internal/reconcile/dispatch.go:864 (JobHandle: marker.asMap()) and internal/exec/herdr/record.go:42-56 (herdrHandle's agent_name/pane_id ride only the executor's own JobHandle)"
  }
]
```

## What the next tick has to know

- **Wave 3 (u5n, 0rx):** `worker.handle` will be null in every real run
  today (the finding below) — render its absence quietly, exactly as null
  `activity`. `report` is null for in-flight attempts by design
  (decorateReports never asks for a dispatched attempt's report), and
  `report.diff` is null — not zeros — when `DiffRead` was false: "no
  changes" and "not looked" are different claims the renderer must not
  collapse.
- **The drill-in (hn6 rule 6):** the summary is the archived report.md's
  first non-heading paragraph, one line, ≤ 300 chars; the diff is three
  counts keyed by the attempt's own write ref.
- **Wave-2 siblings (3gk, 7uv):** no shared files. decorateWorkers and
  decorateReports read only their own `Sources` seams and the durable
  records; nothing in build.go, statusmodel.go, contract.json or
  internal/cli changed, per the tick's file list.
- The commit's diff is exactly the tick's files: activity.go, report.go,
  their tests, and the subprocess helper (+ its test).

STATUS: DONE
