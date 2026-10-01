# The goldens' durations, elapsed, progress and gates corrected, and the guard extended to read them (tick fq0, hn6 attempt 3)

Tick fq0 is oro's absorbed finding: the same fixture-drift class oro removed
from two pipeline cells was still live in values no guard read — durations,
elapsed, progress and gates across all three goldens. I extended
`TestEveryGoldenAgreesWithThePipelineDerivation` test-first with
duration/elapsed/progress/gates rules, each derivable from the document
alone, watched it fire on exactly the contradictions the finding names (plus
the running wave's progress, which the same class puts beside a list of four
ticks), corrected the values through the bundle's cut procedure, and cut
bundle 1.2.3. One commit on `tick/hn6/attempt-3/fq0`, head `9f3de3d`.

## What changed (commit `9f3de3d`, five files)

- `contracts/status-model.json` — the values corrected to what the builder
  produces from each golden's own facts:
  - `status_model_running_wave` (generated 05:30:00Z): nwj's
    `duration_seconds` null → **2898** (its try stamp 03:19:05Z to its gate
    record's finished_at 04:07:23Z — the gate finish wins over its closed
    line, exactly durationOf's precedence); 6dh's `duration_seconds` null →
    **4912** and `elapsed_seconds` 4892 → **4912** (04:08:08Z dispatch to
    generated_at, state dispatched, worker standing); the 6dh worker entry's
    `elapsed_seconds` 4892 → **4912** (buildWorkers measures the same span);
    progress.ticks {14, 1, 13} → **{4, 1, 3}** (the four ticks the waves
    list carries, one closed). progress.waves {3, 0, 1} unchanged.
  - `status_model_completed_awaiting_merge`: `"waves": []` → **null** and
    progress → **{ticks: null, waves: null}**. buildWaves counts progress in
    the same loop that lays the waves out and answers an unread or waveless
    tracker by returning nil — never an empty list, never numbers — so the
    golden's 14-closed-ticks/3-waves progress beside an empty list was
    unproducible on both sides, and `[]` itself is a shape the builder
    cannot emit.
  - `dashboard` (generated 19:20:00Z): v7z's `elapsed_seconds` null →
    **2400** (state reported is live, current attempt 5 dispatched
    18:40:00Z); the gates array gains pass records **gate-060-1-go**
    (finished 18:00:00Z) and **gate-823-3-go** (finished 18:35:00Z) so the
    closed ticks' durations — 060's 2940 and 823's 2070 — trace to the array
    the way durationOf measures them, instead of tracing to nothing. The
    durations themselves stay, and 46x's 3600/1318 and v7z's 2400 duration
    were already the derivation's answers.
- `internal/statusmodel/contract_test.go` — the guard now binds the values
  as well as the cells. Four new rules, each a mirror of one builder branch
  and each reading only what the golden document states: **duration**
  (durationOf: earliest parseable try stamp to the latest gate record naming
  the tick, else the closed line in the tail, else generated_at while open;
  null when no stamp states a start); **elapsed** (buildTick's rule for the
  tick — current attempt's stamp to generated_at while live — and
  buildWorkers's for each worker); **progress** (buildWaves: the counts are
  the loop that lists the ticks, a null or empty waves list answers null
  counters, and each wave's state — done when every tick in it closed, the
  first not-done wave active, the rest upcoming — is demanded with the
  counts it feeds); **gates** (buildGates: sorted by key then started_at,
  and every closed or integrated tick whose tries carry a start must have
  its close STATED — a gate record naming it or its closed line — because
  the array is every evidence record and the duration measures the close to
  the latest one). The guard's stage-state rules are unchanged.
- `contracts/bundle.json` — version 1.2.2 → **1.2.3**, the status-model.json
  digest re-cut (`f35e66e6…` → `dbe7d6ab…`), ledger entry 1.2.3 →
  `782ba748…` recorded. Both re-verified by `contracts check` and by
  `pnpm contracts:check`.
- `contracts/CHANGELOG.md` — the 1.2.3 PATCH entry, in the file's own style.
- `cloudflare/contracts.pin.json` — `bundleVersion` 1.2.2 → 1.2.3.

## What I ran (all green, on tree `9f3de3d`)

- **Red first**: `go test -short -run TestEveryGoldenAgreesWithThePipelineDerivation
  ./internal/statusmodel/` against the uncorrected fixture FAILS with exactly
  eleven contradictions and nothing else — the finding's named values (nwj
  and 6dh durations, 6dh's tick and worker elapsed, completed's
  progress-vs-waves, v7z's elapsed, 060's and 823's unbacked durations) plus
  the running wave's progress.ticks {14,1,13} beside its four listed ticks,
  the same class one golden over. No honest cell or value was refused. (The
  sort rule also caught my own first cut — I had inserted gate-823-3-go
  ahead of gate-46x-4-go.)
- After the fix: the same test passes; `go test -short -count=1
  ./internal/statusmodel/ ./internal/contracts/... ./internal/cli/` — ok
  (the parity suite re-admits the corrected goldens and the Go Model
  round-trips them, including the completed golden's null shape).
- `go run ./cmd/contracts check` — bundle verifies, ticks-owned files match
  the pin.
- TypeScript half: `make ts-gate` — biome clean, `pnpm contracts:check`
  ("16 contract(s) at bundle 1.2.3"), `tsc --noEmit` clean; the full factory
  vitest suite — 69 files, 1618 tests, all passed.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — exit 0: gofmt clean,
  `go vet ./...` clean, the whole-repo short suite ok (reconcile included).

## What the next tick has to know

- The bundle is now **1.2.3**: the fold of this branch delivers the corrected
  goldens and `version_digests` carries 1.2.3 — do not re-cut it. Any vendor
  pin naming 1.2.2 by exact value moves with the fold.
- The guard's four value rules live in `internal/statusmodel/contract_test.go`
  (`goldenDurationAgreesWithTheStamps`, `goldenElapsedAgreesWithTheStamps`,
  `goldenWorkersAgreeWithTheStamps`, `goldenProgressAgreesWithTheWaves`,
  `goldenGatesAgreeWithTheRecords`); a new golden inherits them. A golden
  must now state only durations, elapsed, progress and gate shapes the
  derivation produces from its own bytes — in particular a closed tick's
  duration must trace to a stamp the document states.
- The value rules do NOT read the try outcomes — see the finding below,
  which is the same class one derivation over, still unguarded.

```findings v2
[
  {
    "kind": "defect",
    "title": "Dashboard golden's try outcomes and one reason the derivation cannot produce",
    "severity": "low",
    "body": "The same fixture-drift class this tick removed from durations, elapsed, progress and gates is still live in the dashboard golden's try vocabulary, which the new value rules do not read: 46x's try 1 (attempt 4, not current) carries outcome \"rejected\" beside the fail record gate-46x-4-go that names its attempt, where tryOutcome reads the evidence first for a non-current try and answers \"gate-failed\"; 823's try 1 (attempt 2, not current, nothing standing) carries outcome \"gate-failed\" with no gate record for 823#2 in the array, where the derivation answers \"dispatched\"; v7z's try 1 (attempt 5, current, nothing standing) carries outcome \"reported\" with no record for v7z#5, where the derivation answers \"dispatched\" too; and 46x's try 1 carries reason \"gofmt drifted in two files\" where decorateTries copies the refusal line's detail whole — \"attempt 4 of 46x struck out: gofmt drifted in two files\". All four are provable from the document alone (the gates array is the complete evidence inventory and the refusal lines are in the tail, so they are the newest). Fix shape as this tick's: correct the outcomes and the reason through the bundle's cut procedure, and extend the guard with a try-outcome rule that reads the array and the census.",
    "breaks": {"item": "A1", "check": "go"},
    "evidence": "contracts/status-model.json dashboard golden: 46x tries[0].outcome rejected + reason beside gates[] gate-46x-4-go (fail); 823 tries[0].outcome gate-failed with no 823#2 record; v7z tries[0].outcome reported with no v7z#5 record; derivation tryOutcome internal/statusmodel/build.go (evidence before the census for a non-current try), decorateTries internal/statusmodel/pipeline.go (reason = the line's detail cut at 160)"
  }
]
```

STATUS: DONE
