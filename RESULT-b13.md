<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-3/b13`, base `8505bb2420f7434776c8c2a11232a10e89313d5e`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# b13 — Cost line: only when metered; a claude-sub run shows subscription window use

## What changed

The dashboard's cost line (`internal/cli/watch_view.go`, `dashCost`) no longer
renders the recital it used to — `config cla · cost decisions not metered ·
Workers AI $0.00` — and now renders exactly the three cases the tick names:

1. **Nothing when there is no metered cost.** No "cost not metered", no
   fabricated `$0.00`, and no config name floating on an otherwise empty
   line: `dashCost` returns `""`, and `dashboardCICost` renders the CI
   segment alone without padding to an edge that no longer exists.
2. **The metered cost when there is one** — each measured river's number
   (`cost Workers AI $0.41`), with the config still leading the line (tick
   tda) whenever the line has something to say. Unmetered rivers and
   measured zeros are silence on the glance line; the full per-river story
   (including each unmetered line's basis and the coverage statement a
   partially joined gateway number makes) stays in the model's own lines,
   where `status --json` and the phone page read it.
3. **On a run whose jobs lease the claude-sub subscription, the leased
   subscription label and its 5h/7d window utilization** — `config claude ·
   MAX1 · 34% of 5h · 8% of 7d` (the shape the design names), beside any
   wallet money the run did spend. Never a token: the model's subscription
   object carries the label only.

Supporting changes:

- **`internal/statusmodel`** — `Cost` gains `Subscription *CostSubscription`
  (`{label, five_hour, seven_day}`, utilization fractions 0–1, null per
  window until the factory's proxy has answered for it), filled from a new
  `Sources.ClaudeSub`. Required-and-null in the model's JSON, never omitted.
- **`internal/factory/claudesub.go` (new)** — `FetchClaudeSub` reads the
  pool's snapshot (`GET /api/claude-sub`, the operator's own bearer token from
  `~/.ticfacrc` — the same auth `ticfac factory status` uses); `LeasedLabels`
  matches the leases that belong to this run by the job id the factory mints
  (sandbox names are built from the run id, `<run-id>-<tick>-<attempt>`, so
  another run's lease on the same pool never reads as ours); `WindowUtilization`
  reads the `anthropic-ratelimit-unified-5h/7d-utilization` headers the proxy
  records. A factory with no pool or no route answers `ErrNoClaudeSubPool` —
  the optional state, never a failure.
- **`internal/cli`** — the cloud gathering wires the read
  (`statusClaudeSub`, `modelGatherers.claudeSub`): a factory that *should*
  have answered but did not degrades the model's new `claude-sub` source
  (the same treatment the gateway cost read gets); no factory, no pool, or
  no lease answers nil and degrades nothing. The watch caches it at the
  shared 30s TTL (`watchClaudeSubCache`) so a frame every two seconds does
  not ask the pool every two seconds. Local runs gather nothing here: a
  local run's jobs lease nothing, the leases live in the factory's pool.
- **Contract bundle re-cut 2.3.0 → 2.4.0** — `contracts/status-model.json`
  gains `$defs/cost_subscription` and the cost object's `subscription`
  field (required-and-null, nullable ref), all six goldens and the affected
  negatives carry `"subscription": null`, the changelog documents the bump,
  and `cloudflare/contracts.pin.json` moved to 2.4.0 in the same commit.
  The factory's TypeScript reader (`cloudflare/src/status.ts`) types the
  new field (`StatusCostSubscription`).

Design note, stated plainly: "a run whose config is claude-sub" is
implemented as **a run whose jobs actually hold a lease** — the pool's live
answer is the run's own proof of being on the subscription, and it avoids
parsing the config name against a repo's runner files. Consequences: a
claude-sub run shows the line while its jobs hold leases and not between
them or after they end (the pool releases leases at collect); a run whose
every job stepped down to Workers AI shows no subscription line (nothing
leased — the step-down story lives in the feed lines it already has); a GLM
run never leases and never grows a segment. When several labels are leased
the first in the snapshot's order is shown — one glance line, and the pool's
per-label picture remains `/api/claude-sub`'s to give in full.

## Tests

- `TestDashboardCostLineRendersMeteredOnly` (rewritten from
  `TestDashboardNeverPrintsZeroForUnmetered`): the three cases at the render
  level — metered renders, unmetered is silent, a measured zero is silence,
  nothing-at-all renders nothing.
- `TestDashboardCostLineRendersTheSubscription` (new): the subscription
  segment — both windows, one window, no utilization yet, rounding to whole
  percents, money beside it, no lease no segment.
- `TestDashboardRendersTheMeteredRiverOnly` (rewritten from tick kf4's
  `TestDashboardCallsUnjoinedAttemptsNotMeteredBesideTheNumber`): the
  partially joined river renders its measured number only; the split and its
  coverage bases remain in the model's lines (asserted there).
- `TestStatusModelCloudGathersTheLeasedSubscription` (new, `internal/cli`):
  the cloud gathering reads the pool route under the operator's token, picks
  only this run's leases, and degrades `claude-sub` only when the factory
  should have answered.
- `TestCostCarriesTheLeasedSubscription` (new, `internal/statusmodel`): the
  field rides through and is required-and-null in the JSON.
- `internal/factory`: the fetch, its refusals (503 no-pool, 404 route-less →
  optional; 401 → real error), the lease matching (including the separator
  that keeps `run_abc` from claiming `run_abc12`'s lease), and the
  utilization parsing (decimal fractions from the spike, percent form,
  out-of-range and unparsable refused).
- Property tests updated: P3 is now "the cost line shows only what is
  metered" (no `$0.00` anywhere, no "not metered", whole-line check kept),
  with a new breaker; the generator draws the subscription segment so the
  longest line shape is exercised.
- Golden frames regenerated (`watch_dashboard_120/60`,
  `watch_dashboard_epic_state`): the line now reads
  `config claude · cost Workers AI $0.41`, and the unsynced-cloud frame's
  cost segment is gone.

## What I ran

- `make gate` — gofmt, `go vet ./...`, and the short suite across the whole
  repository: **48/48 packages pass**.
- `make suite` (short suite, `-count=1`, cache refused): **no failures**.
- `go test -count=1 ./internal/cli/` (full, not just `-short`): ok, 57s.
- `go test -count=1 ./internal/statusmodel/ ./internal/factory/`: ok.
- The TypeScript half: `pnpm lint`, `pnpm contracts:check` (bundle 2.4.0
  verified from the factory side), `tsc --noEmit`, and the full `vitest run`
  (80 files, 1880 tests) — all green, so the re-cut bundle is consistent for
  both readers.

## What the next tick has to know

- **The contract bundle is now 2.4.0.** `status-model.json`'s cost object
  gained `subscription` (required-and-null); `cloudflare/contracts.pin.json`
  moved with it in the same commit. Any further model change this epic makes
  (lck's status words) re-cuts again the same way — the ledger entry for
  2.4.0 is already recorded.
- **The cost line can now be empty.** `dashCost` returns `""` and
  `dashboardCICost` renders the CI segment alone; ugm's rewrite should keep
  that (or move config to the header per the design, dropping the prefix
  here). The config prefix still leads whenever the line has content.
- **The subscription facts live at `m.Cost.Subscription`** — ugm's new
  layout renders it from the model, not from a side channel; the phone page
  can pick it up from the same JSON (it currently still renders the old
  per-line "not metered" story).
- The lease match keys on the job id the factory mints (sandbox names built
  from the run id). If the factory ever changes its sandbox naming, the
  matching must move with it — the rule is documented at
  `internal/factory/claudesub.go` (`LeasedLabels`).

```findings v2
[
  {
    "kind": "proposal",
    "title": "Phone page still renders the unmetered recital; subscription field is available",
    "severity": "low",
    "body": "b13 changed the watch dashboard's cost line only. The phone page (cloudflare/src/phone.ts) still renders one line per cost source with 'not metered' for unmetered rivers, and does not show cost.subscription, which the model now carries (bundle 2.4.0). The epic's A5 ('cost shows only when metered; a claude-sub run shows subscription window use') arguably reaches this surface too, since it reads the same model.",
    "evidence": "cloudflare/src/phone.ts:930-945 (cost section), cloudflare/src/status.ts (StatusCostSubscription type now exists)"
  }
]
```

## Status

The three cases render as specified, are tested at the render, model, and
factory layers, and `make gate` passes. Committed as f78abf2 on
`tick/ymf/attempt-3/b13` (24 files: the rendering, the model field, the
factory read, the gathering and caching, the contract re-cut, and the tests).

STATUS: DONE
