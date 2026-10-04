<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-10/z3p`, base `5ec1022a8946df7588d9a9ea95f6021121e3ac8a`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# An earlier run's hold reaches needs-you across runs (tick z3p, epic hn6)

The epic-across-runs merge (tick gmo) reads prior runs' RECORDS but not their
FEEDS, so a hold an earlier run left — an attempt struck out for a person,
findings nobody triaged — showed in the tick row's state (rejected, the work
stage crossed) but never as the model's held-for-person wait or attention
entry: the newest run failed at boot, its own feed holds no run_held line, and
"needs you" answered nothing while a person's decision was actually standing.
This tick gives `buildWaits` the prior runs' feeds and makes their unanswered
holds surface the same way the newest run's own hold does. One commit,
`1c3e8ad`, on `tick/hn6/attempt-10/z3p` (base `5ec1022`).

## What changed (commit `1c3e8ad`, 13 files)

- `internal/statusmodel/build.go` — the core:
  - `Sources` gains `PriorFeeds map[string][]runfeed.Event`, the earlier
    runs' own feeds keyed by the run id their records are keyed by (the
    checkpoint's run_id). A hold is a fact the feed is the only writer of:
    the records say the tick was rejected, only the line says the run held
    it FOR A PERSON. A run whose feed could not be read is absent from the
    map — a missing hint, never a degraded source.
  - `buildWaits` walks the prior runs newest-first and surfaces every
    unanswered run_held line as `held-for-person` — claiming `waits_on` when
    nothing stronger claimed it (the dead-run wait and the newest run's own
    hold still outrank it; merge, ci and the standing workers come after it)
    and always entering `attention` — with `Since` from the line's own stamp
    and the clearing command addressed to the HOLDING run: prior holds carry
    `--run-id <holding run>` on both the settle and the triage command,
    because attempt numbers and findings drafts are per run and the default
    (`epic-<epic-id>`) cannot name another run's attempt.
  - A prior hold is answered — and therefore not shown — when any of these
    holds: a resume line stands after it in its own feed (the position rule
    the newest run's wait already used, tick usx); a settlement decision
    (`op: settle_attempt`, read as fields, not prose) for that (tick,
    attempt) sits in the holding run's own records — the only store a
    release can land in; the merged records close the tick; or a newer run
    already spoke for that tick (its dispatch, its own hold, or a settled
    line in the current feed or an intermediate prior feed) — so the newest
    unanswered word per tick is the live one and no decision is double-alarmed.
  - `unansweredHolds` is now the ONE rule both the newest run's own hold and
    every prior run's are read by (the old `latestStage` +
    `holdSettledByAResume` pair for the current feed is replaced by its last
    unanswered line — verified equivalent for every resume/hold ordering).
  - New helpers: `settledByRelease`, `mergedStateOf`.
- `internal/statusmodel/commands.go` — `SettleCommand` (empty run id = the
  holding run's own spelling, unchanged byte-for-byte; a named run id adds
  `--run-id`) and `TriageCommandForRun` spelled once beside the existing
  verbs.
- `internal/cli/status_model.go` — the wiring: `priorFeedsLocal` reads each
  prior run's feed from the logs this checkout keeps
  (`.ticfac/logs/<run-id>/events.jsonl`); `priorFeedsCloud` reads it from the
  factory's events route per run id, the same route the run's own feed comes
  from (only for a run this checkout can claim). Both best-effort per run.
- `internal/reconcile` — `settleOp` renamed to exported `SettleOp` so the
  model names the settlement marker the writer wrote rather than carrying a
  second copy of the string (mechanical rename, 6 sites).
- Tests:
  - `internal/statusmodel/build_epic_test.go` — the epic-across-runs fixture
    now carries the two earlier runs' feeds: run bbb holds at3 for a person
    and nobody answered; run aaa's own hold was answered by a resume. The
    fixture's `waits_on` expectation flips from the standing worker to the
    held-for-person hold (the person's decision outranks it) with the
    `--run-id run_bbb` release command, and run aaa's answered hold is
    asserted absent. New `TestAPriorHoldStandsUntilAnswered` covers eight
    cases: the hold stands and names the command; a resume answers it; a
    person's release answers it; the newest run's own hold on the tick is
    the live word (no duplicate); a newer run taking the tick up supersedes
    it; a closed tick's hold is history; the untriaged-findings hold is
    cleared by triage addressed to the holding run; a hold naming no attempt
    carries no release command.
  - `internal/cli/status_model_test.go` — `TestStatusModelSurfacesAPriorRunsHold`
    (local: prior records in `.ticfac/runs/`, prior feed in `.ticfac/logs/`,
    the model's attention carries the hold with `ticfac settle rmod t3 1
    --run-id run_old --release "<who>"`) and
    `TestStatusModelForACloudRunSurfacesAPriorRunsHold` (the prior feed read
    from the factory's `/api/runs/<prior-id>/events` route, the release
    addressed to the factory's run id).

One blemish to attribute honestly: the commit MESSAGE body garbled one line
(`holdSettledByAbuse...`) — it should read "holdSettledByAResume (unchanged
semantics, kept for the pipeline's mirror)". The instructions forbid amending
an already-made commit, so the typo stands in the message; the code and every
other line of the message are accurate.

## What I ran (all green, on tree `1c3e8ad`)

- **Red first**: the epic fixture at base asserted `waits_on` = the standing
  worker (`want its own standing worker`, base line 291). With the fix's
  assertions, the base cannot even compile the test — `Sources` had no
  `PriorFeeds` field — and stashing the implementation while keeping the
  tests reproduced exactly that failure.
- `go test ./internal/statusmodel/` (full package) — ok.
- `go test -parallel 4 ./internal/cli/` (full package, 52s) — ok.
- `go test ./internal/contracts/parity/` — ok (the model's emitted shape is
  unchanged; only the waits' content grew, so no contract or golden moves).
- Targeted reconcile tests over the rename's seam: `-run
  'TestBuild|TestAPriorHold|TestNextStep|TestDuplicateOf|TestSettle|TestRejectedWork|TestHeld'`
  on both touched packages — ok.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0: gofmt clean,
  `go vet ./...` clean, the whole-repo short suite ok (45 packages).

## What the next tick has to know

- The epic-across-runs fixture's `waits_on` is now `held-for-person`, not
  `workers` — any test pinning that document's wait reads the new
  expectation, and the standing worker is still asserted via `workers`.
- Prior holds claim `waits_on` AFTER the newest run's own hold and the
  dead-run wait, BEFORE merge/ci/workers; every prior hold enters
  `attention` regardless of what claimed the slot.
- The settle/triage commands for PRIOR holds carry `--run-id`; the CURRENT
  run's own hold command deliberately does not (its pinned string is
  unchanged) — see the defect finding below.
- `unansweredHolds` in `build.go` is the single hold-standing rule; a change
  to hold semantics (a new resume stage, a new answered-by record) belongs
  there, not in a second copy.
- Feeds are local exhaust by design (`.ticfac/logs/` is gitignored, and
  nothing pushes them), so a prior LOCAL run's feed is readable only on the
  machine that ran it; the store-based prior records still carry everything
  else. That matches the tick's ask ("local .ticfac/logs, or the factory's
  events route per run id for cloud runs") and is not a gap this tick left
  open.

```findings v2
[
  {
    "kind": "defect",
    "title": "A held cloud run's needs-you command cannot release the attempt it names",
    "severity": "medium",
    "body": "The CURRENT run's held-for-person unblock command is pinned as `ticfac settle <epic> <tick> <attempt> --release \"<who>\"` with no --run-id, and settle without the flag defaults its run id to epic-<epic-id>. Today's cloud runs write their records under the factory's run_<hex> (the orchestrator container execs `ticfac run-epic --run-id \"$run_id\"`), so the command a person copies from needs-you addresses a store that carries no such attempt and refuses — the hold the header points at cannot be cleared by the command the header gives. This tick deliberately kept the current-run spelling unchanged (it is prior holds that must name their run); the fix is one call-site: pass the model's own run id to SettleCommand whenever it is not the epic spelling, and update the pinned strings in the cli/watch/overview tests.",
    "breaks": {"item": "A2", "check": "go"},
    "evidence": "internal/statusmodel/build.go buildWaits SettleCommand(m.EpicID, *held.TickID, *held.Attempt, \"\"); internal/reconcile/reconcile.go:1373 (RunID default epic-<id>); internal/reconcile/settle.go store.Attempt refusal \"no run dispatch #%d\"; internal/factory/ticfacentrypoint.go:334 (--run-id \"$run_id\"); pinned shape internal/cli/status_model_test.go:428"
  },
  {
    "kind": "proposal",
    "title": "Untriaged findings without a hold line never reach needs-you across runs",
    "severity": "medium",
    "body": "The WaitFinding attention reads only the newest run's records, and a run that died before its close-out raised no run_held line — so a prior run's untriaged drafts are invisible to needs-you even though a person's decision is standing. Worse, the close-out's findings gate reads only the run's own store (r.untriagedFindings -> r.store.Findings), so a resumed-under-a-new-id run never gates on the dead run's drafts: they can silently never gate anything. A tick could surface every run's untriaged drafts in needs-you (the per-run triage command this tick already spells, TriageCommandForRun) and decide whether a new run should adopt or gate on inherited drafts.",
    "evidence": "internal/statusmodel/build.go buildWaits WaitFinding block iterates recs.Findings only; internal/reconcile/findings.go:395 gateCloseoutOnFindings -> r.untriagedFindings() -> r.store.Findings() (own store); merged prior findings reach only the per-tick drill-in (epic.go findingByKey)"
  },
  {
    "kind": "proposal",
    "title": "Try panel's next step disagrees with needs-you for a prior-held tick",
    "severity": "low",
    "body": "pipeline.go's attentionCommand mirrors only the current feed's hold, so a tick a prior run held for a person now gets its release command in the needs-you header while its try's next step still says \"nothing of that run is working — <resume> takes it up\" — the exact drift that mirror's comment exists to prevent. A run-epic resume alone does not clear the hold (the new run holds again on the unreleased attempt), so the panel's word sends the person to the wrong first move. Fix shape: share the prior-hold answer (the same rules buildWaits now applies) with the pipeline index, and let a standing prior hold outrank the generic non-newest-owner resume line in nextStepOf.",
    "evidence": "internal/statusmodel/pipeline.go:597 attentionCommand reads only p.feed; nextStepOf's nonNewestOwner branch fires before it; the epic-across-runs fixture's at3 shows both words after this tick"
  }
]
```

STATUS: DONE
