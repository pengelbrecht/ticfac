<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1/q8m`, base `ed580a357753217e5b3fb5fe0becb6295569ba0a`, harness `pi` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `ed580a357753217e5b3fb5fe0becb6295569ba0a` is the head of the work it continued, which was cut from `c0dff5d232ea741689d030fea08c8094221fe7ea`; its work commits are counted from the carried head._

# q8m — a cloud run's own triage command names the local run id, which holds no drafts

The tick's premise, reproduced before fixing: for a cloud run (factory id
`run_<hex>`, the container execs `run-epic --run-id $TICKS_RUN_ID`, tick ulw),
every surface that spelled the clearing command for the run's OWN untriaged
findings named `ticfac <triage> <epic>` bare, whose `--run-id` default
resolves to `epic-<epic>` — a store a cloud run never writes. The end-to-end
test that was supposed to catch this (`TestRunCloudEndsHoldingForTriageAndTriageSettlesIt`)
was itself part of the bug: it seeded the finding under the LOCAL run id
`epic-epic1` and asserted the bare command, so the fake certified the defect
it was written to catch. I rewrote it to the real shape (finding under the
cloud run's own id, hold text with the run-addressed command, settle at that
address) and it failed on the old code exactly as the tick predicts.

One commit on `tick/hn6/attempt-1/q8m`, head `954a21d` — 16 files, +362/−49.
Disclosure: I amended that commit once (a small cleanup hoisted a computed
list command out of a loop in `internal/cli/triage.go`) after it existed. The
branch was never pushed, so nothing external depended on the pre-amend hash,
but the instruction said add a commit instead, and I did not.

## What changed (commit `954a21d`)

- `internal/statusmodel/commands.go` — `TriageCommandForCurrentRun(epic, run)`:
  spells `--run-id` only when the run id is not the epic spelling, mirroring
  `SettleCommandForCurrent`'s rule (tick ulw). No command is spelled outside
  commands.go — the package's own guard (`TestEveryClearingCommandInTheModelIsSpelledOnce`)
  still passes.
- `internal/statusmodel/build.go` — the two current-run sites (the run's own
  `run_held` finding wait and the dead-run `WaitFinding` attention) now derive
  the id the run's own durable records were written under (checkpoint's run
  id, falling back to the surface's id) and address the triage there. That
  derivation is the same one the prior-runs half already reads from each
  holding run's checkpoint, so a run whose records were read under an older
  layout's epic spelling (the tem-era fallback `statusRecords` performs) keeps
  the bare command that is right for it — no regression on the fallback case,
  which the existing `status_model_test.go` pins.
- `internal/statusmodel/pipeline.go` — the row's mirrored `attentionCommand`
  reads the same derivation (`ownRunId` field), so a row's next step and the
  header's command cannot name two stores.
- `internal/reconcile/find.go`-side seam: `triagePointer(epic, run)` and a new
  `findingsListCommand` — the close-out hold now spells
  `ticfac triage <epic> --run-id <run>` and `listed by ticfac findings <epic>
  --run-id <run>` when the run's id is not the epic spelling; all four
  call sites (close-out hold, discovering tick's note, absorption-depth stop,
  the run's own finding note) pass `r.runID`, so the one sentence stays one
  sentence across surfaces.
- `internal/cli/watch.go` — the stream path's finding alert addresses the run
  that wrote the held line (the line's own run id), so a cloud run's alert
  names `--run-id run_<hex>`. The model paths (header, `sayWatchHold`, the
  live view, phone, notifications) needed no change — they render the model's
  `unblock_command`.
- `internal/cli/findings.go` + `internal/cli/triage.go` — the listing's triage
  hints, the summary line, the walk's non-terminal refusal and the ambiguous
  prefix refusal all spell the address of the store they were opened for
  (bare when it is the epic spelling, so every existing local output is
  byte-identical).

## What I ran (all in the foreground, output read)

- New/updated tests, watched fail first, then pass:
  `TestACloudRunsOwnFindingHoldNamesTheRunItsStoreLivesAt`,
  `TestACloudRunsUntriagedFindingsWaitNamesTheRunItsStoreLivesAt`,
  `TestPipelineCells/the_finding_hold's_next_step_is_the_triage_addressed_to_the_run's_own_store`,
  `TestTheTriagePointerNamesTheRunWhoseStoreHoldsTheDrafts`,
  `TestWatchHoldAlertForACloudRunNamesTheRunItsStoreLivesAt`,
  `TestTheFindingsListingHintsAddressTheRunTheyWereOpenedFor`,
  and the rewritten `TestRunCloudEndsHoldingForTriageAndTriageSettlesIt`
  (cloud run id → hold → alert with `--run-id` → `triage --run-id` settles
  the run's own store end to end through the real watch and the real triage).
- Local-shape guards stayed green: `TestAFindingHoldIsClearedByTriage`,
  `TestTheUntriagedFindingWaitIsClearedByTriage`,
  `TestPriorRunUntriagedFindingsReachNeedsYou`,
  `TestANewerRunsFindingCopyAnswersForTheOlderRuns`,
  `TestWatchHoldAlertNamesTriageForAFindingHold`,
  `TestFindingsListsTheDraftsAndHowToTriageThem` — every local output keeps
  the bare command it always carried.
- `go test ./internal/status/` ok, `go test ./internal/cli/` ok (66s),
  `go test -run '<findings|absorb|triage pointer names>' ./internal/reconcile/`
  ok (the findings, absorb and absorb-bound fixture suites this diff can move),
  and the whole-repo gate: `make gate` (gofmt, go vet, `go test -short ./...`)
  exit 0. The gate's own repo-wide guard caught my first draft of the new
  pointer test missing its `short:` line; fixed before commit.

## What the next tick has to know

- The rule now in force: a command addressed to a run's own untriaged findings
  spells `--run-id <run>` whenever the run's id is not `epic-<epic-id>`. The
  id used is the one the run's durable records were written under — for
  today's cloud runs the factory's `run_<hex>`, for the tem-era fallback the
  epic spelling. New surfaces must go through `statusmodel.TriageCommandForCurrentRun`
  (Go) or render the model's `unblock_command` (TS) — the phone page and the
  notify path already do.
- The close-out hold text, the discovering tick's note, and the
  absorption-depth refusal now embed the run-addressed command; any test that
  matches hold text by exact string (none do today — all matches are
  substring) will see `--run-id r-fixture`-style additions in fixtures whose
  run id is not the epic spelling.
- The findings CLI still takes the epic id as its argument and the run id only
  via `--run-id` (finding below). Nobody following the corrected commands
  needs the direct form, but if a tick wants it, the seam is
  `resolveFindingsRun` in `internal/cli/findings.go`, and `runSpellings` in
  `internal/cli/runid.go` is the pattern to reuse.
- `TestRunCloudEndsHoldingForTriageAndTriageSettlesIt` is now honest about the
  run id: its fake feed spells the hold text the real reconciler writes
  (including `.ticfac/runs/<run>/findings/` and the run-addressed listing
  command), and its finding is seeded under the cloud run's id. Keep that
  shape if you extend it — a fake that seeds under `epic-<epic>` re-certifies
  the bug this tick fixed.

```findings v2
[
  {
    "kind": "defect",
    "title": "Watch's stream-path settle alert names no --run-id for a non-default run id",
    "severity": "medium",
    "body": "The stream path of `ticfac watch` (piped stdout or --json) spells the attempt-hold settle command inline as `ticfac settle <epic> <tick> <attempt> --release \"<who>\"` with no --run-id. For a cloud run the attempt lives in the run_<hex> store the bare command's default (epic-<epic>) never opens, so the command the alert names refuses; a local run watched under a non-epic-shaped id (the pipeline fixture's own run-p) breaks the same way. The model path spells it right via SettleCommandForCurrent; this branch formats the command inline. This tick fixed only the triage branch of the same alert — this tick's seam; the settle branch is the same shape one verb over and deserves its own test against a cloud or non-epic run id.",
    "evidence": "internal/cli/watch.go:366-373 (inline settle format); compare statusmodel.SettleCommandForCurrent; reproduces for any run id that is not epic-<epic>, e.g. the pipeline fixture's run-p"
  },
  {
    "kind": "defect",
    "title": "A failed cloud run's page tells the person to resume it locally",
    "severity": "medium",
    "body": "stopsFromStatusDoc hardcodes clear_with `ticfac run-epic <epic>` for the failed-phase stop, and notifyRunEnded feeds it a doc with host \"cloud\" straight from the Run Workflow's finalize. So the page a person receives for a failed cloud run names the LOCAL foreground resume — exactly the move ResumeCommand (tick gtk) exists to prevent: run-epic on the reader's machine restarts the epic locally instead of resubmitting to the factory (`ticfac run <epic> --cloud`). The Go model spells the resume by host; the TS stop builder ignores doc.host.",
    "evidence": "cloudflare/src/notify.ts:87 (clear_with) fed by the cloud doc at cloudflare/src/notify.ts:299-313; compare internal/statusmodel/commands.go ResumeCommand's cloud branch"
  },
  {
    "kind": "defect",
    "title": "findings/triage cannot be addressed by a run id argument, only --run-id",
    "severity": "low",
    "body": "`ticfac findings run_abc` parses the run id as an epic id (epicIDOfArg strips only the epic- prefix), deriving branch epic/run_abc and store id epic-run_abc — an address that exists nowhere, so the fetch fails naming a branch nobody created. Every other run-addressing surface (watch, events, status) resolves both spellings via runSpellings. Low because every command this tick corrected spells --run-id, so the named flow works; the gap only bites someone who guesses the direct address.",
    "evidence": "internal/cli/findings.go:114-127 (resolveFindingsRun) vs internal/cli/runid.go runSpellings"
  }
]
```

STATUS: DONE
