# Phone page renders the dashboard model (tick 0rx, hn6/attempt-1)

## The state I inherited, and what my attempt did

This attempt's base (709813e) is the salvage commit ticks-worker made from
attempt-3 of this same tick: a cancelled container's uncommitted tree,
committed whole. That tree already contained a complete-looking
implementation of the tick — `cloudflare/src/status.ts` extended, the phone
card rendering the dashboard, and the golden-driven tests. Attempt-3's own
report says explicitly that its content is PARTIAL by construction and that
"a human has to review what is there before this tick is run again".

So this attempt's work was: review that salvaged implementation line by line
against the tick's spec (nobody had), prove the tests actually bind it, and
run every command the acceptance names. The implementation survived the
review unchanged — every item the tick names is present and correct — so the
branch needs no new code commit; the deliverable is the salvaged tree itself,
now verified, plus this report. Details of the review are under "What
changed". One boundary note, disclosed the way 3gk's report did: I read the
tick and sibling records with `tk show`, `tk list --json` and `tk graph hn6`
— all read-only, all a worker is told to do — and nothing under `.tick/` was
written by any of them (the tree stayed clean; the guard's log may record
the calls).

## What changed (branch tick/hn6/attempt-1/0rx; work is commit 709813e)

The branch carries four paths over the true base (ddf3cec), all from the
salvage, reviewed this attempt:

- `cloudflare/src/status.ts` — `StatusDoc` extended with the hn6 fields,
  every one optional (lifecycle.phases, waves carrying `StatusTick` with
  gloss/pipeline/parent_tick_id/duration_seconds/tries, progress,
  remaining, health.verdict with recovered[], cost.lines, recent[]); new
  types `StatusPipelineStage`, `StatusTry`, `StatusCostLine`,
  `StatusRecentEvent`. `cloudStatusDoc` gained the one cloud-statable
  field: `cost.lines` with a single `{source:"workers-ai", metered:true,
  usd:run.cost_usd, basis:"AI Gateway logs"}` line when `run.cost_usd` is a
  number, an empty array otherwise; `waves` stays null and the rest of the
  dashboard fields stay absent. The version checks
  (`STATUS_SCHEMA_VERSION`/`parseSnapshotEnvelope`) are untouched — diffed
  against the base to confirm.
- `cloudflare/src/phone.ts` — the run card's dashboard sections, all
  conditional on the model stating its facts: headline (progress bar as a
  CSS width percentage, `n/m ticks`, `ETA ~X` only when
  `remaining.approximate_seconds` is non-null, the verdict with a coloured
  dot — healthy green, degraded amber, stopped red — plus
  `(recovered: net ×14, sleep 41m)`); the phase row with ✓/●/○ glyphs;
  `needs you: nothing` (dim) or one line per needs-person attention with
  its command; the tick table inside the existing `<details>` (one row per
  tick in plan order, children indented `└` under `parent_tick_id`,
  pipeline cell with the same glyph vocabulary the tick names — ✓ done,
  ● active, ✗ failed, one … for the pending tail — per-tick duration, one
  glyph per try ✓/✗/●/○); the honest cost line ("not metered" for an
  unmetered line, never a fabricated $0.00); the last two recent events.
  Every interpolated value goes through `escapeHTML`; the table collapses
  to stacked labelled rows under 480px. A doc without the hn6 fields
  renders byte-for-byte the pre-hn6 card (the dashboard block gates on
  `health.verdict !== undefined`, the one field the model made required).
- `cloudflare/test/phone-page.test.ts` — the cross-language guarantee: the
  "dashboard" golden imported straight from the contract bundle and pushed
  through the local-snapshot path, asserting every tick id in plan order,
  the ✗ on the rejected try's tick (46x), `healthy (recovered: net ×14)`,
  `claude not metered` with no `$0.00` anywhere, `Workers AI $0.41`, the
  needs-you text, the headline/phase row with the ETA only where stated, a
  needs-person attention with its command, a cloud run's cost from its own
  row, the pre-hn6 card byte-for-byte, and the golden envelope well under
  `MAX_SNAPSHOT_BYTES` (asserted < 1/4 of the 256 KiB door; ~8.4 KB).
- `RESULT-0rx.md` — the salvage's placeholder note, replaced by this
  report.

Review findings, all resolved as "already correct": the verdict marker is
sound because the schema makes `verdict` required in `health` (checked the
$defs in contracts/status-model.json); the pre-hn6 card equality is proven
by the exact-markup assertions; escapeHTML covers every interpolation
(attributes are double-quoted, and `"` is escaped); a metered line with a
measured 0 would read `$0.00` — that is honest (it was measured), the rule
forbids only the unmetered one, and the code keeps those apart.

## What I ran (all green, on tree 709813e + this report)

- `cd cloudflare && pnpm exec vitest run test/phone-page.test.ts
  test/status-snapshots.test.ts` — 2 files, 31 tests, all pass (the
  acceptance command; the per-tick gate does not run vitest, CI's ts job
  does).
- `cd cloudflare && pnpm install --frozen-lockfile --prefer-offline && pnpm
  lint && pnpm contracts:check && pnpm exec tsc --noEmit` — exit 0 (the
  gate's ts command; Biome clean, 16 contracts ok, types clean — so
  `status-snapshots.test.ts` needed no change for the type extension).
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2, low priority per the host
  rules) — rc=0: gofmt clean, `go vet ./...` clean, the short suite green
  across the repository.
- A mutation check, because this code's author never got to see it pass:
  breaking the cost line's "not metered" branch made the golden test fail
  (and only that one), then the file was restored byte-identical. The
  golden assertions bind the implementation; they are not
  transcriptions of it.

## What the next tick has to know

- The golden fixture lives at `contracts/status-model.json`, not at the
  `internal/statusmodel/contract.json` path the tick's description names —
  the fixture moved into the pinned bundle at tick 4i8 (commit dbd6b8cf)
  after hn6 was planned. The test imports `../../contracts/status-model.json`
  the way the description intends ("the same way other tests import
  ../../contracts/*.json"), and files the stale path as a finding below.
- My base already carries 378's two golden corrections (v7z's ci cell
  `failed`, 46x try-1 `next_step` null) — the salvage snapshot was taken
  from a tree that had them. My test asserts exactly those corrected
  values, so my branch and 378's should merge without a golden conflict;
  if 378's final form differs, the merge gate (and CI's ts job) is where
  it will surface, since the test imports the golden rather than
  transcribing it.
- fq0 ("the goldens carry more values the wave-2 derivation can never
  produce") may edit the same golden my test renders through; the test's
  assertions are on values the current golden states (tick ids and order,
  glyphs, verdict text, cost numbers, needs-you). A value change that
  keeps the rule the test asserts will pass; a change that breaks one will
  fail loudly in CI's ts job, which is the guarantee the tick wanted.
- For u5n (the watch frame): the phone's glyph helpers are `pipelineGlyphs`
  and `tryGlyphs` in `cloudflare/src/phone.ts`, and the phase row's ✓/●/○
  match `watchProgressLines` in `internal/cli/watch_view.go`. The terminal
  may want the same vocabulary verbatim.
- The dashboard block's presence marker is `health.verdict !== undefined`:
  any future renderer reading these snapshots should use the same marker,
  not `health !== undefined` — a pre-hn6 snapshot carries health's four
  counters with no verdict.

```findings v2
[
  {
    "kind": "defect",
    "title": "Tick 0rx's description names the golden at a path it left at tick 4i8",
    "severity": "low",
    "body": "The tick's description says the dashboard golden lives in internal/statusmodel/contract.json; that file moved into the pinned contract bundle (contracts/status-model.json) at tick 4i8, before this tick ran. A worker following the description's path literally hits a missing file; the intended reading — 'the same way other tests import ../../contracts/*.json' — is what the test does. Worth correcting in the tick record so a re-run does not stumble on the stale path.",
    "target": "pengelbrecht/ticfac",
    "evidence": "contracts/status-model.json holds golden 'dashboard'; git log dbd6b8cf 'contracts: ticfac authors its own bundle' removed internal/statusmodel/contract.json"
  }
]
```

STATUS: DONE
