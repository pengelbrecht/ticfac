<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-3/oro`, base `818fcbeeddc8299dcdd4d3defd711e94cd552f8d`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# The running_wave golden's cells corrected and every golden bound to the wave-2 derivation (tick oro, hn6 attempt 3)

Tick oro was created by absorbing the first finding of tick 378's report: the
wave-1 `status_model_running_wave` golden carried pipeline cells the wave-2
derivation (tick 3gk) can never produce, the same fixture-drift class 378
removed from the `dashboard` golden, and no suite failed on it because 378's
agreement guard bound only the dashboard golden. I extended the guard
test-first, watched it fail on exactly the two named contradictions, corrected
the cells through the bundle's cut procedure, and cut bundle 1.2.2. One commit
on `tick/hn6/attempt-3/oro`, head `2d20d3b97956d4e74ba6fe0657b5f826b0d392fe`.

## What changed (commit `2d20d3b`, five files)

- `contracts/status-model.json` — the two cells the tick names, corrected to
  what the derivation produces from the golden's own facts:
  - nwj (state `closed`, try 1 `closed`, dispatched 03:19:05Z): the cell is now
    claim/work/gate/merged all `done` — `workState` and `gateState` make a
    closed tick's stages done and `endState` makes its merge done, and the try
    (one is cut per dispatch marker) makes the claim done, so the cell's fill
    reaches every stage.
  - 6dh (state `dispatched`, try 1 `in-flight`, attempt 2, dispatched
    04:08:08Z): the cell is now claim `done`, work `active`, gate `pending`,
    merged `pending` — a dispatch marker alone makes the claim done and, with
    the state dispatched, work done-or-active (never pending, which it read).
- `internal/statusmodel/contract_test.go` — the guard 378 added is now EVERY
  golden's: `TestTheDashboardGoldenAgreesWithThePipelineDerivation` is
  `TestEveryGoldenAgreesWithThePipelineDerivation`, one subtest per golden
  (sorted), plus the state agreements the two contradictions name. Each rule
  mirrors a `pipeline.go` branch and reads only what the golden document
  carries (cell, state, tries, workers, feed tail): a claim is never
  active/failed and any try makes it done; a state that answers the work
  (reported/integrated/closed) makes work done and a closed or integrated one
  makes gate and end done; a dispatched state leaves work done-or-active; a
  reported current try makes its gate done where the fill reaches it; nothing
  dispatched leaves the gate pending; a failed work stage names a refused
  current try nothing stands behind; a done merged or closed stage names a
  tick the records closed or integrated. The rules that DEMAND a value demand
  it only where the cell's fill provably reaches the stage; the rules that
  FORBID one forbid it wherever the cell shows it — the fill can only ever
  downgrade a stage to pending (`pipelineCell`), so a rendered done/active/
  failed value is always the derivation's own answer.
- `contracts/bundle.json` — version 1.2.1 → 1.2.2, the `status-model.json`
  digest re-cut (`e3f793c5…` → `f35e66e6…`), and the ledger entry
  `1.2.2 → 95bf8de9…` recorded. Both numbers re-verified: the file's sha256
  matches the digest line, and the ledger digest reproduces as sha256 over
  version + sorted digest lines.
- `contracts/CHANGELOG.md` — the 1.2.2 PATCH entry, in the file's own words:
  same class as 1.1.1, both corrected values remain schema-admitted, no
  consumer has anything to do, and the guard now reads every golden.
- `cloudflare/contracts.pin.json` — `bundleVersion` 1.2.1 → 1.2.2 (step 5 of
  the bump procedure).

## What I ran (all green, on tree `2d20d3b`)

- **Red first**: `go test -short -timeout 20m -run
  'TestEveryGoldenAgreesWithThePipelineDerivation' ./internal/statusmodel/`
  against the uncorrected fixture FAILS with exactly the tick's two defects and
  nothing else — nwj's claim/work/gate/merged each "pending, want done" beside
  state `closed`, and 6dh's claim and work "pending" beside state `dispatched`.
  The dashboard and completed_awaiting_merge subtests passed before the fix, so
  the new rules do not fire on honest cells.
- After the fix: the same test passes; `go test -short -timeout 20m -count=1
  ./internal/statusmodel/ ./internal/contracts/...` — ok ×3 (the parity suite
  re-admits the corrected golden, and the Go Model round-trips it).
- `go test -short -timeout 20m -count=1 ./internal/cli/` — ok (the other Go
  readers of the model).
- `go run ./cmd/contracts check` — "ticfac's bundle verifies, and the
  ticks-owned files match contracts.pin.json (offline check)".
- TypeScript half: `pnpm lint` clean, `pnpm contracts:check` ("16 contract(s)
  at bundle 1.2.2"), `pnpm exec tsc --noEmit` clean, and the full factory
  vitest suite — 69 files, 1611 tests, all passed (the phone page's reader
  validates every golden against the schema).
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — rc=0: gofmt clean,
  `go vet ./...` clean, the whole-repo short suite ok.

## What the next tick has to know

- The bundle is now **1.2.2**: the fold of this branch delivers the corrected
  golden, and `version_digests` carries 1.2.2 — do not re-cut it. Any vendor
  pin that names 1.2.1 by exact value moves with the fold.
- The guard's name is `TestEveryGoldenAgreesWithThePipelineDerivation` in
  `internal/statusmodel/contract_test.go`; it iterates every golden under
  `golden` in the fixture, so a new golden inherits the rules automatically —
  it must state only values the derivation produces, and the state rules now
  cover the cell class this tick's two contradictions came from.
- The guard does NOT cover duration/elapsed/progress/gates values — see the
  finding below, which is the same class in values no rule reads yet.
- The `dashboard` golden keeps its own populated-anchor test
  (`TestTheContractBindsTheDashboardGolden`) unchanged, including the
  no-next-step-anywhere anchor's rationale.

```findings v2
[
  {
    "kind": "defect",
    "title": "The goldens carry more values the wave-2 derivation can never produce",
    "severity": "low",
    "body": "The same fixture-drift class tick oro removed from the two named cells is still live in values no guard reads: in status_model_running_wave, nwj carries duration_seconds null though the document's own facts derive 2898 (its try stamp to its gate's finished_at) and 6dh carries duration_seconds null with elapsed_seconds 4892 where generated_at minus its own dispatched_at is 4912; in status_model_completed_awaiting_merge, progress counts 14 closed ticks and 3 waves beside an empty waves list; in the dashboard golden, v7z carries elapsed_seconds null though its reported state is live with a stamped current try, and its gates array backs only 46x of its closed ticks. The builder cannot emit any of these pairs: durationOf and the elapsed rule read the very stamps the document carries, buildWaves counts progress in the same loop that lists the ticks, and buildGates emits one gate per evidence record. Fix shape as oro's: correct the values through the bundle's cut procedure and extend TestEveryGoldenAgreesWithThePipelineDerivation with duration/elapsed/progress/gates rules, each derivable from the document alone.",
    "breaks": {"item": "A1", "check": "go"},
    "evidence": "contracts/status-model.json goldens status_model_running_wave (nwj/6dh duration+elapsed), status_model_completed_awaiting_merge (progress vs waves []), dashboard (v7z elapsed, gates); derivation: durationOf internal/statusmodel/pipeline.go:347, elapsed internal/statusmodel/build.go:366, progress internal/statusmodel/build.go:271, buildGates internal/statusmodel/build.go:490"
  }
]
```

STATUS: DONE
