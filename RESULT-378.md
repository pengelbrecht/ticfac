<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-1/378`, base `16491a01784953772dd7f4f8c30a5abdb9599464`, harness `pi` exited 0, 0 work commit(s), 1 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk show 378 --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`
> - the agent ran `tk version --json`

# The dashboard golden's ci cell and try next_step contradict the wave-2 rules (tick 378, run_3f034e683 attempt 1)

Attempt 1 of this run, dispatched as a CARRY: the run took over tick 378's
claim from run_911b556 (whose harness died at attempt 5) and seeded this
container from that attempt's salvage, so the tick's fix is already committed
at my branch tip and this attempt's work is the review-and-verify that
attempt-5's `NEEDS_CONTEXT` report asked a human for. I did that review,
reproduced the defect at the salvage's base, verified the fix against the
wave-2 derivation in the tree, ran the gates, and found the salvage complete:
I changed nothing, so this attempt adds no commit of its own — the deliverable
is the carried head `16491a01`, whose parent `c7d58787` is the salvage commit
("tick 378: work in progress salvaged by ticks-worker (harness exited 1)",
base `912dc2c7`, five salvaged paths). The collect will read this attempt as
report-only against my dispatched base; tick isp's carried-delivery rule
(`deliverCarriedWork`, internal/reconcile/dispatch.go:3022) is the path that
delivers the carried head as ready-to-merge, and this report is the evidence
that the head is the tick's fix.

## What changed (no new commit — the work is the carried salvage at my tip)

The carried range `912dc2c7..16491a01` is six files, +182/−12:

- `contracts/status-model.json` — the two field values the tick names,
  corrected to what the wave-2 pipeline derivation (tick 3gk, merged into
  epic/hn6 as `2a3b219`) actually produces in the `dashboard` golden:
  - v7z (closeout, wave 3) pipeline ci stage `active` → `failed` — the
    golden's own `ci.state` is `red`, and `ciState`
    (internal/statusmodel/pipeline.go:280) makes red the ci stage's FAILURE
    while the lifecycle phase bar stays `active` (`buildLifecycle`,
    internal/statusmodel/build.go:580 — red maps the PHASE to active, the
    CELL to failed; the golden now says exactly that: phase active, cell
    failed).
  - 46x (wave 2) `tries[0]` (try 1, attempt 4, rejected) `next_step`
    `"reformat and re-push on the next try"` → `null` — `decorateTries`
    states a next step on the LAST try of a refusal only, and a later try
    (attempt 6, in-flight) stands.
- `contracts/CHANGELOG.md` — the 1.1.1 PATCH entry, in the file's own words:
  the fixture is a rendering fixture, not a Build output, and both corrected
  values remain schema-admitted.
- `contracts/bundle.json` — version 1.1.0 → 1.1.1, `status-model.json`
  digest re-cut (`2c523d0b…` → `e3f793c5…`), and the append-only ledger
  entry `1.1.1 → 5b337c8f…` recorded. I re-verified both numbers by hand:
  the file's sha256 matches the digest line, and the ledger digest reproduces
  as sha256 over version + sorted digest lines.
- `cloudflare/contracts.pin.json` — `bundleVersion` 1.1.0 → 1.1.1 (step 5 of
  the bump procedure in contracts/CHANGELOG.md).
- `internal/statusmodel/contract_test.go` (+149) — the guard that makes the
  class impossible to re-enter silently:
  `TestTheDashboardGoldenAgreesWithThePipelineDerivation` reads the dashboard
  golden back over three rules 3gk spelled, each faithful to the production
  code (I read the mirrors against `pipelineCell`/`ciState`/`decorateTries`
  line by line): a cell is its role's own stage list filled left to right
  (done first, at most one live stage, everything after pending); a
  closeout's ci stage says what the model's own CI answer makes the
  derivation say (`goldenCIStateOf` mirrors `ciState` including the red and
  failure words and the closeout-held branch); a next step exists only on the
  last try of a refusal and a reason only on a refusal. The populated-anchor
  in `TestTheContractBindsTheDashboardGolden` was narrowed with it
  (`tryWhyAndNext` → `tryRefusedReason`, with the comment explaining why the
  golden states no next step anywhere).
- `RESULT-378.md` — attempt-5's own report, committed in the salvage.

## What I ran (all green, on tree 16491a01)

- **Red at base first** (worktree at `912dc2c7`, the salvage's base, with
  only the salvaged test file overlaid):
  `go test -short -timeout 20m -run 'TestTheDashboardGoldenAgreesWithThePipelineDerivation' ./internal/statusmodel/`
  FAILS with exactly the tick's two defects, nothing else:

      contract_test.go:343: 46x's try 1 (attempt 4, rejected) carries the
      next step "reformat and re-push on the next try": the derivation
      states one on the last try of a refusal only
      contract_test.go:330: v7z's ci stage is active, want failed: the
      model's own CI answer is "red", and the cell and the PR cannot say two
      things about one another

- With the fix (my tip): the same test PASSES, as does
  `TestTheContractBindsTheDashboardGolden` (schema admits the golden, Go
  Model round-trips it, anchors populated, vocabularies agree).
- `go test -short -timeout 20m -count=1 ./internal/statusmodel/ ./internal/contracts/...` — ok ×3 (statusmodel, contracts, contracts/parity; the parity suite re-admits the corrected golden TS-side too).
- `make gate` — rc=0 at GOTEST_PARALLEL=4/GOFLAGS=-p=2: gofmt clean, `go vet ./...` clean, the whole-repo short suite ok.
- TypeScript half: `pnpm lint` (biome, 141 files, clean), `pnpm contracts:check` ("contracts: ok — 16 contract(s) at bundle 1.1.1"), `pnpm exec tsc --noEmit` clean — no TS consumer pins the old values or the old version.
- `go run ./cmd/contracts check` — "ticfac's bundle verifies, and the ticks-owned files match contracts.pin.json (offline check)".
- Scope check of the OTHER two goldens in the same file: `status_model_completed_awaiting_merge` carries no ticks (nothing to contradict); `status_model_running_wave` DOES — filed as the finding below, not fixed here, because that golden is not this tick's named subject.

## What the next tick has to know

- The fix is at the carried head `16491a01`, not on epic/hn6 yet: I confirmed
  origin's `epic/hn6` still carries the old values (`{stage: ci, state:
  active}` beside red; the non-null next step). The fold of this branch
  delivers them; until then u5n and 0rx (both still `ready` in the
  checkpoint) would render the contradicting fixture if they ran from the
  integration branch — they should fold or wait on this tick first.
- The guard's name is `TestTheDashboardGoldenAgreesWithThePipelineDerivation`
  in `internal/statusmodel/contract_test.go`; it binds ONLY the dashboard
  golden by name (`dashboardGoldenName`). A future golden that illustrates
  derived shape must either satisfy the same rules or extend that test —
  the finding below is exactly the case of a golden it does not read.
- The bundle is now 1.1.1: any vendor pin in another repository that pins
  1.1.0 by exact value moves with this fold, and `version_digests` now
  carries 1.1.1 — do not re-cut 1.1.1.
- The lifecycle phase bar and the pipeline ci cell disagree BY DESIGN on a
  red CI (phase `active`, cell `failed`); if a renderer ever asserts they
  match, one of the two rules is being misread — see `buildLifecycle`'s red
  branch and `ciState`'s doc comment.

```findings v2
[
  {
    "kind": "defect",
    "title": "The running_wave golden carries cells the wave-2 derivation can never produce",
    "severity": "low",
    "body": "The same class of fixture drift this tick removed from the `dashboard` golden is present in the wave-1 `status_model_running_wave` golden (contracts/status-model.json, the epic-2jn illustration): nwj reads state \"closed\" with a closed try beside an all-pending pipeline cell — the wave-2 derivation (tick 3gk) makes a closed tick claim/work/gate/merged all done — and 6dh reads state \"dispatched\" with an in-flight try beside an all-pending cell, where a dispatch marker alone makes claim done and work active. No suite fails on it today because the guard this tick's salvage added binds only the dashboard golden. Same fix shape as this tick: correct the illustrated values through the bundle's cut procedure (version, digest, ledger, cloudflare pin), and extend the agreement test over every golden so the class cannot re-enter.",
    "breaks": {"item": "A1", "check": "go"},
    "evidence": "contracts/status-model.json golden 'status_model_running_wave': nwj {state: closed, try 1 closed, duration null} with pipeline [claim pending, work pending, gate pending, merged pending]; 6dh {state: dispatched} with the same all-pending cell"
  },
  {
    "kind": "defect",
    "title": "hn6's acceptance list parses as one item: A2–A6 are unaddressable",
    "severity": "medium",
    "body": "The epic's acceptance_criteria puts all six [A<n>] marks on one line, and the acceptance parser takes the first mark per line (internal/acceptance/acceptance.go:107, one mark per line, the rest is that item's text) — so hn6's done parses as the single item A1 with A2..A6 inside its prose. The machinery can then only ever bind or claim A1: this report's own linter refuses breaks.item A2..A6 (\"the epic's items are A1\"), and per-item gate binding at planning can only reach A1, the exact shape the repo's own learnings record as the way epics close with their goal unmet. The record is the tracker's to re-flow one [A<n>] item per line; no code change is needed.",
    "breaks": {"item": "A1", "check": "go"},
    "evidence": "Parse on .tick/issues/hn6.json acceptance_criteria returns exactly one item [A1] (probe run against internal/acceptance); the linter prints 'the epic's items are A1'"
  }
]
```

STATUS: DONE
