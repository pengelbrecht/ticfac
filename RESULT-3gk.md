# Status model derives each tick's pipeline, parent, tries and findings (tick 3gk, run_911b556f attempt 3)

Attempt 3 of the wave-2 pipeline tick. Attempt 1 (run_0133fee3, then taken
over by run_911b556f's first incarnation) died on the infrastructure the
hn6 retro notes — the cloud runs never reached the work; nothing of this
tick's deliverable was in the base (base 8558ae16 is the wave-1 tree:
`decorateTicks` was still the all-pending stub). This attempt implemented
the whole tick from the stub up.

One note on the boundary: at the very start I ran `tk --version` and
`tk show 3gk --json` trying to read the tracker record the prompt names —
both read-only, both refused by the guard, both recorded. Nothing under
`.tick/` was written; I read the record from this repository's own mirror
of it (`.tick/issues/3gk.json`) and carried on. No further `tk` call was
made.

## What changed (one work commit, 3174c77c)

- `internal/statusmodel/pipeline.go` — the whole derivation, replacing the
  wave-1 stub. `decorateTicks` keeps its signature and its call site; a
  per-model `pipelineIndex` groups the records once (graph tasks by id,
  markers by tick and by (tick, attempt), absorptions by created tick and
  by finding key, findings by the tick their DiscoveredFrom names, the
  census, the feed) and each tick is decorated from it:
  - **pipeline cell**: each stage's state from the records (claim from a
    marker or `claimed` line; work/review done on reported/integrated/closed
    or a `collected` line for the current attempt, failed on a refused
    current try nothing redispatched, active while dispatched or standing;
    gate done on `gate_passed`, on the reported try's all-pass evidence, or
    on the close itself, failed on `gate_failed` or a rejection whose detail
    begins `gate_failed` (RefusedGate), active on
    `gate_started`/`gate_running`; ci failed when CI names failure, done on
    green or the close, active on `closeout_held`/pending CI; merged/closed
    done on closed — plus integrated for merged), then the left-to-right
    fill: done stages first, at most one live stage, every stage after the
    first non-done one pending. All stage-line checks are scoped to the
    CURRENT attempt: a refused earlier try behind a live one is history the
    ATTEMPTS column counts.
  - **parent_tick_id**: the graph task's Parent when it is not the epic,
    else the absorption's provenance tick id for a tick the run created
    (the field of runstate.Absorption that names the attempt that reported
    the finding — both real records on this repo's own runs carry it that
    way: 9dy's names r5i, n68's names rzi), else null.
  - **duration_seconds**: earliest parseable marker stamp to the close
    (latest gate-evidence FinishedAt, else the `closed` line's time) while
    closed/integrated, and to `src.Now` while open; null when no stamp
    parses.
  - **findings**: recs.Findings whose DiscoveredFrom names the tick
    (`run-<run>/tick-<tick>/attempt-<n>`), title from the draft, gating
    from the matching absorption's verdict, null when none decided it.
  - **try tier/reason/next_step**: tier from each try's own marker
    provenance; reason, for a refused try, from the latest `rejected` or
    `gate_failed` line for that (tick, attempt), cut to its first 160
    characters on a word boundary, and null when no line exists; next step,
    on the last try only, in the description's priority order — "retrying
    (try N+1)" on a later dispatch (marker or feed line), the attention
    entry's unblock command when a standing `run_held` names the tick
    (mirrored from buildWaits' own line and rule, since decorateTicks runs
    before buildWaits assembles the list — same sources, one spelling
    there, one mirror here), "held — see needs-you" while a `held` line for
    the tick stands un-released by a later settle, else "the run will retry
    or escalate the tier".
- `internal/statusmodel/pipeline_test.go` (new) — the table: the tick's
  nine named cases as subtests (closed all done; in flight at the gate;
  rejected on a failed gate with the truncated reason and the ladder's next
  step; rejected-then-redispatched with both next steps null; closeout in
  the ci phase; the review role's own stages; the absorbed repair under its
  source; never-dispatched all pending with null duration; the attempt-2
  finding listed under its tick) plus rows pinning the same rules' branches:
  a work refusal failing the work stage, no reason invented when no line
  explains, "retrying (try 2)" behind a dispatch the marker lost, the settle
  command from a held run, "held — see needs-you", a released hold as
  history, a `claimed` line without a marker, a stamp that does not parse,
  red CI failing the ci stage, and integrated reading merged done. Every
  case validates its whole Build output against the contract via the
  contract_test.go helper. TestPipelineReasonCut pins the word-boundary cut
  itself (short whole, boundary-on-space, hard cut, multibyte).
  TestPipelineRowsKeepTheRoleStageLists pins the role lists.
- `internal/statusmodel/build_dashboard_test.go` — the wave-1 fence, at the
  lines the wave-1 header said the wave-2 ticks would replace: the
  all-pending and all-empty assertions for MY fields became the fixture's
  real derivations (nwj done all the way, 6dh at its work, the untouched
  ticks pending; nwj's finding with gating null; the durations 2898/5400
  and nulls; the tries' tiers and 6dh's first try's reason). The fence
  still fences the fields the OTHER wave-2 ticks own: report null, worker
  activity and handle null, the verdict stub, the cost lines empty. The
  dead `pendingPipeline` helper (never called since wave 1) went with them.
  This is the one file outside the tick's own Files list I touched, and
  only because its assertions pinned the stub my tick replaces — without it
  the tick's own "must still pass" command cannot pass.

Two interpretation decisions, both logged in the code where they live:
- **gate done past the `gate_passed` line**: the description says "Done on
  gate_passed" only, but a closed tick whose feed line was lost (this
  repository's own running fixture is one — closed on all-pass evidence
  with no gate_passed line) would otherwise sit one stage short with merged
  done, which the left-to-right rule then forces back to pending,
  contradicting case (a) for any real closed tick. The gate is also done on
  the current try's all-pass evidence outcome (TryReported) and on
  closed/integrated: the close is the durable thing only a passed gate
  allows. gates[] stays the evidence surface; the cell states position.
- **ci failed reads the forge's `red`**: the description says
  "m.CI.State is 'failure'", but the contract's closed ci.state vocabulary
  is green/red/pending/none and the forge says red — I treat red (and the
  literal "failure", defensively) as the failure word. The wave-1
  `dashboard` golden shows ci active under a red CI; it is a rendering
  fixture, not a Build output, and the description's rule governs the
  derivation.

## What I ran (all green, on tree 3174c77c)

- `go test -short -timeout 20m -run 'TestPipeline|TestTheContract' ./internal/statusmodel/` — ok, 19 subtests plus both contract bindings (the pinned acceptance command; all nine named cases pass).
- `go test -short -timeout 20m ./internal/statusmodel/ ./internal/cli/` — ok (the tick's named regression surface, including the updated wave-1 fence).
- `make gate` — rc=0: gofmt clean, `go vet ./...` clean, 44 packages ok (run at low priority per the host rules).
- Red-at-base check: with pipeline.go reverted to the wave-1 stub the new suite fails (it cannot even compile against the stub — the derivation, its helpers and the nine cell assertions did not exist there), and with the implementation in place the stub's own fence assertions fail on nwj/6dh — the tests pin the derivation, not the fixture.

## What the next tick has to know

- `decorateTicks` still runs right after `buildWaves` and before
  `buildWorkers`/`buildLifecycle`/`buildWaits` — its ci-stage and
  next-step attention facts read `src.CI` and the feed directly, mirroring
  buildLifecycle and buildWaits over the same sources. If a later tick moves
  model assembly around, those mirrors (ciState, attentionCommand) must
  move with it or read from the model instead.
- The wave-3 renderer (u5n) gets every field it renders: rows never move
  (waves order + parent_tick_id for indentation), the cell fills left to
  right with at most one live stage, tries carry tier/reason/next_step,
  findings carry key/title/gating. The `dashboard` golden is NOT a Build
  output — its 46x try-1 next_step and its red-CI-but-active ci cell are
  wave-1 illustrations; render against it as a shape fixture only.
- Sibling wave-2 branches will conflict on `build_dashboard_test.go`'s
  header comment and on nothing else: 7uv's attempt-2 branch (65dc23f7, not
  in my base) rewrites the verdict/cost half of the same fence, I rewrote
  the pipeline half. The resolve is a union of intents — both halves are
  the fence, updated for their own derivations.
- The reason cut is rune-safe: 160 characters, not bytes; a detail whose
  161st rune is a space cuts at 160, one with no boundary inside cuts hard.
- `cutToWordBoundary`, `tryKey`, `tryOfAttempt`, `roleStages` and
  `tickIntegrated` are pipeline.go's own; build.go was not touched (it
  belongs to other ticks in this wave, per the tick's Files note).

```findings v2
[
  {
    "kind": "defect",
    "title": "The dashboard golden's ci cell and try next_step contradict the wave-2 rules",
    "severity": "low",
    "body": "Two values in the wave-1 `dashboard` golden (contracts/status-model.json) illustrate the layout with data the wave-2 derivation can never produce: v7z's ci cell reads active while the golden's own ci.state is red (the 3gk rule, and the hn6 phase it mirrors, make a red CI the ci stage's FAILURE), and 46x's first try carries a non-null next_step while a later try stands (the 3gk rule states a next step on the last try only). The golden is a rendering fixture rather than a Build output, so no suite fails today, but u5n and 0rx render it as THE shape fixture, and a fixture that contradicts the rules it exists to exercise is the disagreement A5's one-model rule exists to prevent. The fix is two field values in the golden, and the file belongs to the finished wave 1 / the contract bundle, not to a wave-2 tick's Files list.",
    "evidence": "contracts/status-model.json golden 'dashboard': v7z pipeline carries {stage: ci, state: active} beside ci.state 'red'; 46x tries[0] carries next_step 'reformat and re-push on the next try' with tries[1] in-flight"
  }
]
```

STATUS: DONE
