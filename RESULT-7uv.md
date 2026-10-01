<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-2/7uv-run_6d88e3de89ca466c8dab3841185931c1`, base `2fa0a4f1e907d9d202c07e2d213e38e5ed817b20`, harness `pi` exited 0, 0 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `2fa0a4f1e907d9d202c07e2d213e38e5ed817b20` is the head of the work it continued, which was cut from `341ce14a6da712c0d29b39a8829f79c537646d47`; its work commits are counted from the carried head._

# 7uv — Status model states a health verdict and honest cost lines

Epic hn6, wave 2, tick 7uv: the two run-level headline fields the dashboard
header renders — `health.verdict` and `cost.lines` — filled from the wave-1
stubs into their real derivations in `buildVerdict`
(internal/statusmodel/verdict.go) and `buildCost` (internal/statusmodel/cost.go),
both pure functions of the Sources, the Records, the feed and the Model built
so far.

## What this attempt is

**The tick's completed work is in the tree this attempt was seeded with, and it
is the newest version of it.** The seed commit `2fa0a4f1` ("tick 7uv: worker
report", the squashed tree of the previous attempts) carries the whole change:
the two derivations, their two new test files, and the three justified touches
outside the tick's file list (the one-line `build.go` call site —
`buildCost(src, recs)`, the lines read Sources; the two wave-1 fence
assertions in `build_test.go`/`build_dashboard_test.go`, whose own comments
name this tick as the one to replace them; and the `Cost` doc comment in
`statusmodel.go`, made stale by the fill). `git diff 341ce14a..HEAD` restricted
to source shows only those eight statusmodel files. My job was therefore to
hold the seeded work to the tick's letter, prove the tests bite, run every
named command, and report — and to establish which of the **two existing
versions** of this fill is the one to fold (see "The two versions" below).

## The acceptance criteria, verified on this tree

- **[1] health.verdict with recovered counts; the named command passes.**
  `go test -short -timeout 20m -run 'TestVerdict|TestCost|TestTheContract'
  ./internal/statusmodel/` — ok. Verdict states `stopped` (not alive, phase
  neither done nor cancelled; summary the liveness reason, else the dead-run
  wait's what), `degraded` (first cause in the tick's order: unreadable
  sources, an unresolved stuck nudge named with its age, an exhausted remote,
  a stall warning inside the reconciler's own 15-minute window), `healthy`
  (the word itself as the summary). The recovered list (net, sleep with the
  summed stated spans, interventions, wall clocks) rides in every state.
- **[2] No cost line is ever metered:false with a number; unmeasured spend is
  a usd-null line; a cloud run's gateway cost is metered.** Pinned three ways:
  the built struct, the marshalled JSON (the test walks the emitted document
  for an unmetered line with a number), and the contract, which every case's
  Build output is validated against through `assertValidatesAgainstTheContract`.
  Local claude states "not metered (subscription)", local pi "not metered",
  local Workers AI names exactly why its calls are not joined to gateway logs;
  a cloud run's workers-ai line carries the host's gateway number metered.
- **[3] `make gate` passes.** rc=0 (run at low priority, `GOTEST_PARALLEL=4
  GOFLAGS=-p=2`): gofmt clean over the repository, `go vet ./...` clean, the
  short suite green across all 44 test-bearing packages, `internal/reconcile`
  included.
- Also per the tick's record: `go test -short -count=1 -timeout 20m
  ./internal/statusmodel/ ./internal/cli/` — ok both.

## The red-check: the tests bite

I fetched the tick's work base (`341ce14a`, the wave-1 stubs), rebuilt it in a
throwaway worktree with only this seed's two new test files dropped in, and ran
the suite there: **all 12** `TestVerdict*`/`TestCost*` tests fail at the stubs,
each on its own rule's assertion (state, summary, recovered, lines,
recorded_usd) — no unrelated failures:

```
TestVerdictHealthyWhenNothingHappened          TestCostLocalSpendIsUnmeteredNeverADollarZero
TestVerdictHealthyCountsWhatTheRunGotPast      TestCostTheCloudsWorkersAISpendIsMeteredFromTheGateway
TestVerdictDegradedNamesTheUnreadableSource    TestCostDecisionsCarryTheirOwnMeteredUsage
TestVerdictDegradedOnAnUnresolvedStuckNudge    TestCostSplitsRiversTheProvenanceNames
TestVerdictDegradedOnAFreshStallWarning
TestVerdictDegradedOnAnExhaustedRemote
TestVerdictStoppedOnARunThatIsNotGoing
TestVerdictACompletedOrCancelledRunIsNotStopped
```

Worktree removed afterwards; this branch's tree is untouched (still byte-identical
to the seed commit, `git status` clean).

## The two versions of this fill — fold the seed's, not the pushed tip

Two branches carry work for this tick, and they are NOT the same version:

- `origin/tick/hn6/attempt-2/7uv` tip `65dc23f7`, work commit `3938d669`
  (2026-09-30 07:33) — an earlier fill. It deviates from the tick's letter in
  three ways: (a) its degraded summaries have **no `degraded: ` prefix**
  (`"forge unreadable"`, `"nwj warned as stalled 5m ago"`), against the tick's
  own examples (`"degraded: forge unreadable"`, `"degraded: v7z nudged as
  stuck 4m ago"`) and epic rule 3; (b) it detects the claude river by
  **substring** (`namesClaude`: any executor/model containing
  claude/sonnet/opus/haiku), so a provider-qualified claude id
  (`openrouter/…/claude-opus-5`) would read as subscription claude instead of
  the pi-local pay-per-token spend it is; (c) it resolves a stuck nudge by a
  **blacklist** (any later line about the attempt except four stages) rather
  than the closed set of stages that record the attempt moving; and its tests
  pin summaries only with loose `strings.Contains` assertions, so none of that
  is caught.
- This branch's seed `2fa0a4f1` — the later, corrected fill: the `degraded: `
  prefix on every degraded summary, pinned exact; `isClaudeModel`, exactly the
  runner config's own family check (`internal/runconfig/kinds.go`
  `claudeFamily`: aliases opus/sonnet/haiku/fable or a `claude-` prefix,
  provider-qualified refused, fail-closed) so the cost line and the spawn-time
  family check agree on one spelling; the `nudgeResolvers` closed set
  (gate stages, waiting, wip_nudged, collected/rejected/integrated/published/
  settled/closed/cleaned_up/redispatched — the run's own confirming silence
  `stuck_stopped` and its bookkeeping do not answer a nudge).

Both versions pass their own tests, so the fold cannot discover the difference
by running the gate: **the fold must take this branch's
`verdict.go`/`cost.go`/`verdict_test.go`/`cost_test.go` whole**, plus this
branch's verdict/cost sections of `build_test.go` and
`build_dashboard_test.go`. The stale branch adds nothing the seed lacks. The
remaining fold conflicts are unchanged from the previous attempt's report:
`build_dashboard_test.go` against 3gk's landed pipeline half is a
union-in-intent resolve (keep 3gk's pipeline assertions, this tick's
verdict/cost assertions); `build.go`'s one-line call site is this tick's.

## Semantic readings the code carries (grounded, not guessed)

- **pi-local = provider-qualified ids**: the pi kind's own model rule is
  `modelOK: providerQualified` (`internal/runconfig/kinds.go`), so a pi
  dispatch always records a slash in its model; bare non-claude ids
  (`gpt-5.6-luna`) belong to other rivers and land on `other`. A local pi
  serving `cloudflare-workers-ai/…` reads workers-ai and states unmetered when
  no host number exists — tested explicitly.
- **Provenance has no harness-kind field** (runstate's `Provenance`: Executor,
  Model, tier, digest — nothing names the harness kind), so the tick's
  "executor or harness kind claude" is decided from the only evidence a
  provenance carries: the model spelling. The compile-time refusal "claude
  refuses a provider-qualified id that pi would take" is what makes the
  pi-local reading correct rather than a guess.
- **The pathological decisions shape** (a decision carrying a usage object
  without `cost_usd`) would state a metered $0.00 line; the schema's metered
  arm admits it and no production decision records usage without the price.
  Carried as a logged decision from the previous attempts; not changed.

## What I ran

- Red-check at the stubs (worktree at `341ce14a` + this seed's two test
  files): 12/12 fail as above; worktree then removed.
- `go test -short -timeout 20m -run 'TestVerdict|TestCost|TestTheContract'
  ./internal/statusmodel/` — ok.
- `go test -short -count=1 -timeout 20m ./internal/statusmodel/ ./internal/cli/`
  — ok both.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0, 44 packages ok, no
  failures anywhere in the log.
- No contract change: the bundle, all three goldens and the dashboard golden's
  anchors are untouched, and every new case's Build output validates against
  the pinned schema.

## Process disclosure

- **No `tk` command was run at all.** The tick's record was read from
  `.tick/issues/7uv.json` and the epic's from `.tick/issues/hn6.json`
  directly; the container flags any `tk` invocation as an attempted tracker
  write, and two earlier attempts on this tick were rejected over exactly that
  boundary. Nothing in this session touched the tracker or the tk binary.
- **No new commit was made**: the verification and runs produced no change to
  make — the branch tip remains the seed commit `2fa0a4f1`, a single orphan
  commit whose tree is the tick's base plus exactly the eight statusmodel
  files. The fold driver should expect an orphan branch, not a merge-base.
- The previous attempt's report (preserved in the seed commit) was read before
  this report was finalized; its three findings are carried below unchanged
  where they still hold — each re-verified on this tree — per the
  deduplication rule.

## What the next tick has to know

- **Reconcile the two versions at the fold** (details above): this branch's
  files whole; the pushed tip is stale and its own tests will not catch the
  difference.
- **The phase-merge question needs an epic decision** (finding 1) before the
  goldens are refreshed: as the tick's literal rule stands, a completed run
  waiting for its merge headlines `stopped`, while the wave-1 golden for
  exactly that shape states healthy and the rule's own parenthetical reads
  stopped as "a dead run or a failed run". Both renderers will print this
  shape to every reader at close-out time.
- **Wave-3 renderers**: compose the headline from `state` + `summary` —
  `Build` states `healthy` as the healthy summary (the golden's longer
  sentence is that fixture's own value, not the builder's), `degraded: <why>`
  names its cause with an age suffix, and stopped's summary can be empty when
  neither the probe nor a dead-run wait said anything. Recovered's `seconds`
  is a SUM across the counted suspensions, not the last one's span (see
  finding 3).
- **ltg (worker activity)**: the fence sections for worker activity, handles
  and the report stay wave-1 empties in `build_dashboard_test.go`; the
  verdict/cost blocks are this tick's real values. Same union discipline as
  above applies to its edits.
- **The first-use bug this tick ends**: the header that read
  `cost $0.00 recorded (10 attempts)` (internal/cli/watch_view.go) now has
  the model behind it that says what was and was not measured; rendering it
  is wave 3's job (u4l/qjl), not this tick's.

```findings v2
[
  {
    "kind": "defect",
    "title": "Two versions of 7uv's fill exist; the pushed tip is the stale one",
    "severity": "medium",
    "body": "origin/tick/hn6/attempt-2/7uv (work commit 3938d669) carries an earlier version of this fill whose degraded summaries lack the 'degraded: ' prefix the tick's examples and epic rule 3 state, detects the claude river by substring (an openrouter claude id would read as subscription claude, not pi-local), and resolves stuck nudges by a blacklist; its tests pin summaries with loose Contains so none of this is caught. The squashed seed 2fa0a4f1 this branch carries is the corrected version. Both pass the gate, so the fold must choose deliberately: fold this branch's four tick files whole, not the pushed tip.",
    "evidence": "git show 65dc23f7:internal/statusmodel/verdict.go — degradedCause returns 'forge unreadable' with no prefix; git show 2fa0a4f1:internal/statusmodel/verdict.go — 'degraded: forge unreadable'; namesClaude (substring) vs isClaudeModel (runconfig claudeFamily) in the respective cost.go"
  },
  {
    "kind": "proposal",
    "title": "A completed run awaiting its merge reads 'stopped' under 7uv's rule",
    "severity": "medium",
    "body": "buildVerdict implements the tick's literal condition — stopped whenever the run is not alive and the lifecycle phase is neither done nor cancelled — so a run that finished cleanly and waits on the person's merge (phase 'merge', process gone) headlines as stopped, while the wave-1 golden for exactly that shape states a healthy verdict and the tick's own parenthetical says stopped means 'a dead run or a failed run'. Both renderers will show this shape to every reader at close-out time; the epic should decide deliberately whether phase merge joins done/cancelled as not-stopped, or the golden's verdict is the one to refresh. Nothing mechanical breaks either way today.",
    "breaks": {"item": "A1", "check": "go test -short -timeout 20m -run 'TestVerdict|TestCost|TestTheContract' ./internal/statusmodel/"},
    "evidence": "internal/statusmodel/verdict.go buildVerdict (phase != done && != cancelled); contracts/status-model.json golden status_model_completed_awaiting_merge (alive false, lifecycle.phase merge, health.verdict.state healthy)"
  },
  {
    "kind": "contract-change",
    "title": "Recovery.seconds prose says 'the last one'; the 7uv fill sums the spans",
    "severity": "low",
    "body": "The bundle's $defs.recovery.seconds description (\"How long the last one took, where anything measured it\") and the Go field's doc comment describe a per-occurrence span, while the tick's rule — and this fill — state the SUM of the stated suspension durations across the counted host_suspended lines. A renderer reading the contract prose would print the wrong number for any run that slept twice; the bundle's wording should be re-cut to name the sum the fill states (or the fill changed, if the prose is what binds).",
    "evidence": "contracts/status-model.json $defs.recovery.properties.seconds description; internal/statusmodel/verdict.go buildRecovered's sleep accumulation"
  },
  {
    "kind": "contract-change",
    "title": "The two wave-1 status goldens still state the stub's verdict answers",
    "severity": "low",
    "body": "status_model_running_wave and status_model_completed_awaiting_merge carry the wave-1 stub's verdict blocks (summary empty, recovered empty, no cost lines). After this fill those documents are unreachable from the builder — a healthy run always states the word 'healthy' as its summary and the empty summary is now only reachable in the stopped state — so the fixtures depict shapes no run can produce. They still validate and nothing pins their values, but wave-3 renders against these fixtures and should not meet stub-era answers; refresh their verdict and cost blocks once the phase-merge question above is decided.",
    "evidence": "contracts/status-model.json goldens status_model_running_wave (alive true, verdict.state healthy, verdict.summary \"\", recovered []) and status_model_completed_awaiting_merge (same shape); internal/statusmodel/verdict.go buildVerdict returns VerdictHealthy as the summary for every healthy run"
  }
]
```

STATUS: DONE
