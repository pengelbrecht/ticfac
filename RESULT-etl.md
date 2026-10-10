<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-3/etl`, base `23bb51cd3e649703bcb66e7a72613fe2cf456ce7`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# tick etl — the health line says "paused · needs you" whenever needs-you is not empty

## What changed

Two commits on `tick/ozw/attempt-3/etl` (17b9363ca, 2e20cfe8b), test-first:

1. **The status model derives the verdict from the needs-you state too.**
   `internal/statusmodel/verdict.go` → `buildVerdict` now checks `m.Attention` after the
   stopped and degraded arms: anything in needs-you and the headline reads
   `VerdictPaused` with `"needs you"` as its summary. The harder words keep their
   precedence — a run that is not going still reads `stopped` (red), a live run
   with something wrong still reads `degraded` — so a held run (the tick's
   scenario) reads `paused · needs you` (amber) and a completed run waiting on
   its merge reads the same calm amber instead of a green that hid the person the
   merge is for. The vocabulary lives in `internal/statusmodel/statusmodel.go`
   (`VerdictPaused`), with the derivation documented on `buildVerdict`.

2. **The renderers spell it.** `internal/cli/watch_view.go` `dashVerdict` renders
   paused as `● paused · needs you` — the summary behind the health line's own
   separator, never a colon — in amber. `cloudflare/src/status.ts` `verdictWord`
   spells the same words for the phone page, and `cloudflare/src/phone.ts` gives
   `.verdict-paused .dot` the amber the degraded dot carries. One model, two
   renderers, one word — the phone page agrees because it renders the model's
   verdict, not its own.

3. **The contract carries the word.** `contracts/status-model.json`'s
   `health_verdict.state` enum gains `paused` with a description of the
   derivation; the negative that refused `"paused"` as outside the vocabulary now
   refuses `"halted"` instead (same rule, a word still outside). The bundle is
   re-cut at **2.8.0**: digest, `version_digests` ledger entry, `contracts/CHANGELOG.md`
   entry (MINOR), and `cloudflare/contracts.pin.json`'s `bundleVersion` moved in
   the same commits. Two hand-authored goldens whose verdict field said
   `healthy` beside a needs-you entry (`status_model_completed_awaiting_merge`,
   `status_model_refused_last_try`) now carry the `paused` verdict the builder
   derives for them — a golden documents what the derivation produces.

4. **The tests pin it everywhere it can drift.**
   - `internal/statusmodel/verdict_test.go`: three new tests — held run → paused
     (validated against the contract), merge wait → paused / nothing-in-needs-you
     → healthy, degraded outranks paused, and the dead run still reads stopped.
     `TestVerdictACompletedOrCancelledRunIsNotStopped` now expects paused for the
     merge case and asks its completed case clean (the fixture's untriaged
     finding is attention since etl, so it was cleared to keep the test about the
     done word alone). `contract_test.go`'s `enumAgrees` includes `VerdictPaused`.
   - `internal/cli`: `TestDashboardVerdictColours` gains the paused row
     (amber/33); the colour-grid test asserts `● paused · needs you` renders
     amber; `TestTheWatchAndThePhoneSpellOneVerdictWord` gains the paused and
     empty-summary shapes (separator, never a colon); `scenarioHeld` carries the
     verdict the builder would derive, and the two held goldens
     (`watch_scenario_held_{80,120}.txt`) regenerate to
     `● paused · needs you · 1 of 5 done · ~1h left`.
   - A new dashboard property, **"the health line agrees with needs-you"**
     (`watch_props_test.go`): with anything in needs-you, `● healthy` appears
     nowhere in the frame; with nothing in it, `● paused` appears nowhere. The
     generator now enforces the same agreement the builder enforces (healthy
     becomes paused when attention carries a person's decision), and the
     property's seeded breaker (`breakHealthyDespiteNeedsYou`) reproduces the
     pre-etl defect whole — the box saying a hold stands, the line beneath it
     saying everything is fine — and the seeded-bugs test confirms it fails.
   - `cloudflare/test/phone-page.test.ts`: a cross-renderer test renders the
     dashboard golden with a held-for-person attention entry and the paused
     verdict, asserts `paused · needs you`, the `verdict-paused` chip, no
     healthy chip, and the box beneath still naming the hold.

## What I ran

- `make gate` (gofmt, vet, the short suite over 50 packages): **green** — final
  run on the committed tree, twice.
- Full (non-short) suites of every package the change reaches:
  `internal/statusmodel`, `internal/cli` (~95 s), `internal/contracts`,
  `internal/contracts/parity`, `internal/factory`, `cmd/...`: **green**.
- `make ts-gate` (lint, lint:test, contracts:check, tsc, vitest): **green** —
  83 files, 1895 tests, final run on the committed tree.
- `make bombadil` (honest half) and `make bombadil-seeded`: **green** (4 and 1
  consecutive runs respectively; seeded 8/8). One cold-start run timed out on
  the first honest spec ("exited after time limit hit") while the binary was
  still being built on this loaded host; three consecutive re-runs passed and
  the suite pins nothing this tick changes. CI runs these officially.

## What the next tick has to know

- **The held golden now reads `● paused · needs you · 1 of 5 done · ~1h left.`
  Any open tick re-touching `scenarioHeld` or the health line's layout
  (the epic's A2 width tick, A3 colour tick, A4 spacing tick) regenerates from
  this shape, and the colour for the verdict is amber (`33`), same hue as
  degraded — red stays with the box, the held row and stopped.
- **The committed screenshots under `docs/design/watch-redesign-2026-10/`
  still show the pre-etl held page** (`watch-held-120x40.png` reads
  `● healthy` over its box). The epic's own [A3]/[A5] already require
  regenerating the screenshots; when that happens the held one will show the
  paused line automatically. No vhs/freeze tool is installed in this checkout,
  so this tick could not regenerate them faithfully and did not try.
- **A hand-built model must now carry the paused verdict itself** — the
  scenarios in `watch_scenario_*` tests and any fixture that hand-writes
  `Health.Verdict` do not run `buildVerdict`. A fixture with a non-empty
  `Attention` and a healthy verdict is now a model the builder never produces;
  the new property P5 will catch it in generated shapes, and the contract's
  goldens show the convention.
- **Known-failure context, already tracked:** `internal/reconcile`'s full suite
  still fails `TestACloseoutBlockedOnRedCICarriesItsWorkOntoTheRepairedTree`
  (and its sibling) with the claim-width hold — reproduced identically at this
  branch's base with this tick's changes stashed; `.tick/issues/hai.json`
  records it and backlog tick e1k owns it. Not this tick's regression; the
  close-out should read its reconcile evidence with e1k in hand.
- Contract bundle is now **2.8.0**; `cloudflare/contracts.pin.json` matches.
  The `paused` verdict is backward-tolerant for consumers (unknown states fall
  through `verdictWord`'s default arm), but a renderer that wants the colour
  needs the new case.

## Findings

```findings v2
[]
```

## Tracker edits

None — the tick's record is accurate as written.

STATUS: DONE
