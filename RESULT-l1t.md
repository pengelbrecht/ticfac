<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-14-resolve-14-3e0f6c24/l1t`, base `a15057ab448e19076a904b1c8592a391dfb7959e`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict report — l1t × ulw (run_5c7c16d199414da2a8a40d97b524e987)

Branch: `tick/hn6/attempt-14-resolve-14-3e0f6c24/l1t`
Conflicted merge: a15057a (epic/hn6 × tick-l1t/attempt-14, left unresolved)
Resolution commit: 7da5a8a

## The conflict

One file, `internal/statusmodel/pipeline.go`, two blocks, both in/around
`attentionCommand`:

- **l1t** (mine, the attempt being merged; HEAD of the incoming side): the
  current-run mirror `attentionCommand` must not spell the settle command a
  second time beside `commands.go` — one sentence there, one call here, so a
  wording change can never drift a row's next step from the header's
  needs-you command. Literal fix: `SettleCommand(p.epicID, tickID,
  *held.Attempt, "")`, plus a package-wide guard
  (`TestEveryClearingCommandInTheModelIsSpelledOnce`) that fails on any
  clearing-command string literal outside commands.go.
- **ulw** (its work already on epic/hn6, merged as 69bc252c): the
  current-run hold's unblock command must name the run id whenever the run's
  own id is not the `epic-<id>` spelling settle defaults to — cloud runs
  write their records under `run_<hex>`, and the bare command's default
  store refuses the attempt it names. Done via `SettleCommandForCurrentRun`,
  already what the header's `buildWaits` (build.go:815) calls.

## The resolution: the union that holds both

```go
command := SettleCommandForCurrentRun(p.epicID, tickID, *held.Attempt, p.runID)
```

- l1t's intent holds: the call goes into `commands.go`'s shared spelling —
  `SettleCommandForCurrentRun` is defined there and itself delegates to
  `SettleCommand` — no inline second spelling, and l1t's guard test passes.
- ulw's intent holds: the mirror names the run exactly as the header's
  needs-you entry does; ulw's own pin (pipeline_test.go:627,
  `ticfac settle pip mmm 4 --run-id run-pip --release "<who>"`) passes, and
  epic-spelled/empty run ids produce the identical output through
  `SettleCommandForCurrentRun`'s fall-through to `SettleCommand(..., "")`.

The doc comment merges both halves: ulw's "including the run id it names
when the run's own id is not the epic spelling settle defaults to (tick
ulw)" and l1t's "the command itself is the shared spelling in commands.go,
the same one the header carries — one sentence there, one call here (tick
l1t)".

**What did not survive as text**: l1t's literal call
`SettleCommand(p.epicID, tickID, *held.Attempt, "")`. Under ulw's header it
would drift the row from the header for every run whose id is not the epic
spelling — the exact class of disagreement l1t exists to prevent — so it
was genuinely irreconcilable at the text level. Its intent survives
completely through the commands.go call.

## Verification

- `go test ./internal/statusmodel/` — green (guard test and pipeline pins).
- `go test ./internal/cli/ -run 'TestStatusModel|TestTheBareOverview|TestWatch|TestRemedy'` — green (the pinned-string consumers).
- `make gate` (gofmt, `go vet ./...`, short suite) — green.
- No conflict markers anywhere in the tree (only the reconciler's own
  marker-detection source strings remain).
- Committed source only: one file, `internal/statusmodel/pipeline.go`
  (5 insertions, 16 deletions); working tree clean apart from this report.

## Findings

The collision itself is reported as a finding: a wave-partition defect, not
a modelling problem (details in the block below).

```findings v2
[
  {
    "kind": "proposal",
    "title": "Same-wave ticks ulw (#13) and l1t (#14) both rewired attentionCommand",
    "severity": "low",
    "body": "The run dispatched ulw (#13, created 2026-10-04T03:42:03Z, merged as 69bc252c) and l1t (#14, created 2026-10-04T04:14:38Z) as consecutive same-wave ticks, both gating done-item A2, both rewiring the same call site and comment region of attentionCommand in internal/statusmodel/pipeline.go and both touching the settle-command spelling in commands.go. l1t's one-line refactor of a call site ulw already owned produced a conflicted merge that cost a resolve-conflict tick neither tick's work needed. A better wave partition would sequence a one-line refactor behind the merged owner of the call site, or fold it into that owner's tick.",
    "evidence": ".tick/issues/l1t.json (created 2026-10-04T04:14:38Z, dispatch #14); .tick/issues/ulw.json (created 2026-10-04T03:42:03Z, dispatch #13, merged 69bc252c); conflicted merge a15057a, internal/statusmodel/pipeline.go attentionCommand"
  }
]
```

STATUS: DONE
