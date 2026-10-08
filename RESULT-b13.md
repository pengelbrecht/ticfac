<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-6/b13`, base `b2f06b98bca1699cb185702c6873119754f53bef`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `b2f06b98bca1699cb185702c6873119754f53bef` is the head of the work it continued, which was cut from `8505bb2420f7434776c8c2a11232a10e89313d5e`; its work commits are counted from the carried head._

# b13 — Cost line: only when metered; a claude-sub run shows subscription window use

## What changed

The three cases the tick names stand in this branch's base — the render in
`dashCost` (nothing when nothing is metered, each measured river's number
when one is, the leased label and its window use on a claude-sub run), the
model field (`Cost.Subscription`), the `/api/claude-sub` read under the
operator's own token, the 2.4.0 contract re-cut, the goldens and their tests.
This attempt verified them end to end against the factory's real answers and
repaired the two defects that were still on the seam this tick owns — one in
the matcher the fifth attempt re-cut, one in the contract prose the earlier
attempts' re-cut left behind:

- **`ClaudeSubSnapshot.LeasedLabels` also matched a lease spelled the bare
  run id** — a shape the factory cannot mint. Its real spellings are two, and
  the matcher's own comment said so: the door's job id `run-<run>/…`
  (`attemptJobID`, and a spec's own `job_id` is bounded under the same
  prefix) and the review boot's sandbox name `<run>-<boot>` (`sandboxName`).
  The staging door leases under the caller's sandbox name, whose route binds
  `[a-z0-9-]` — a run id carries an underscore, so a staging name cannot even
  spell one. The code matched a third spelling nobody writes: the
  forgiving-fixture shape this repository's own learnings warn about, in a
  matcher, reading another job's lease as this run's on evidence that cannot
  exist. Test-first, as the repo's rule demands:
  `TestLeasedLabelsMatchOnlyTheSpellingsTheFactoryMints` fails on the base
  (`a lease spelled the bare run id "run_abc1" read as this run's: [MAX1]`)
  and passes on the fix; `LeasedLabels` now matches exactly the two
  spellings the parity guard reads out of the factory's TypeScript, and its
  comment says so and names the test that holds the line.

- **The contract's own `why` claimed something its golden does not do.**
  2.4.0 wove `cost.subscription` into the sentence that enumerates the
  dashboard fields and then said "The `dashboard` golden carries every one of
  them populated — it is the fixture the wave-3 renderers and the phone page
  test against". The golden's `cost.subscription` is null, and rightly so:
  the golden is a LOCAL run, and a local run's jobs lease nothing from the
  factory's pool — the local gathering reads no pool at all. So the one
  fixture the renderers test against stated nothing about the one field a
  claude-sub run's cost line is made of, while the contract's own description
  said otherwise. Repaired in **2.4.1** (PATCH: words only, no shape moved,
  no golden changed — an unchanged consumer is still correct):
  - `status-model.json`'s `why` now states the null and its host, and says
    why populating the golden would hand the renderers a document no
    gathering produces;
  - what the golden cannot carry is bound by DERIVING it from the golden —
    `TestTheContractBindsTheLeasedSubscription`, new in
    `internal/statusmodel`: the same document with the subscription set the
    way a leasing run's model carries it, held to the three guarantees the
    golden gives every field it does populate — the schema admits it, the
    field is required in the cost object (2.4.0's required-and-null rule),
    and the Go Model round-trips it, label and both windows. That derivation
    is what a wave-3 renderer (ugm's dashboard, the phone page) reads the
    field's shape from, imported from the golden rather than transcribed.
  - I proved the anchor bites before trusting it: populating the golden makes
    it fail, dropping `subscription` from the cost object's `required` makes
    it fail, and a stray `omitempty` on the field is already refused by the
    existing golden round-trip.
  - The bundle re-cut is whole: `bundle.json` at 2.4.1 with the new digests
    and the `version_digests` ledger entry, the 2.4.1 `CHANGELOG.md` entry,
    and `cloudflare/contracts.pin.json` moved to 2.4.1 in the same commit.

The rest of the delivery is verified, not changed: the three render cases
(`TestDashboardCostLineRendersMeteredOnly`,
`TestDashboardCostLineRendersTheSubscription`,
`TestDashboardRendersTheMeteredRiverOnly`), the model field
(`TestCostCarriesTheLeasedSubscription`), the gathering and its degradation
policy (`TestStatusModelCloudGathersTheLeasedSubscription` — a worker-job
lease and a review-boot lease each on their own leg, another run's lease
refused, no pool / no route the optional state, an unreachable factory the
degraded `claude-sub` source), the lease-match parity guard
(`TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints`, which reads
`attemptJobID`'s and `sandboxName`'s templates out of the factory's
TypeScript), the watch's 30s cache, the property P3 (whose generator draws a
subscription on one run in three), and the goldens reading
`config claude · cost Workers AI $0.41` all stand as the base left them.

## What I ran

- The failing test first, at the base of my change:
  `go test -count=1 -run 'TestLeasedLabelsMatchOnlyTheSpellingsTheFactoryMints' ./internal/factory/`
  — it failed as quoted above and passes on the fix. (The contract repair is
  prose inside a contract file; its guard is an anchor test that pins the
  truth, and I verified each of its anchors fails when the thing it pins is
  broken rather than trusting a green run.)
- The tick's named tests, green:
  `go test -count=1 -run 'TestDashboardCostLineRendersMeteredOnly|TestDashboardCostLineRendersTheSubscription|TestDashboardRendersTheMeteredRiverOnly|TestStatusModelCloudGathersTheLeasedSubscription|TestCostCarriesTheLeasedSubscription|TestLeasedLabelsMatchTheRunOwnJobs|TestLeasedLabelsMatchOnlyTheSpellingsTheFactoryMints|TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints|TestFetchClaudeSub|TestTheContractBindsTheLeasedSubscription' ./internal/cli/ ./internal/statusmodel/ ./internal/factory/`
- Full (not `-short`) package suites, all ok: `./internal/factory/` (82s),
  `./internal/cli/` (88s), `./internal/statusmodel/`,
  `./internal/contracts/...`, and
  `./internal/exec/cloudflaresandbox/` — the last including the real-door
  claude-sub e2e (`TestTheRealDoorRunsAClaudeSubJobOnAHostedDeployment`), which
  exercises the pool's real lease ids on the real Durable Object: it passes.
- The contract check in both worlds: `go run ./cmd/contracts check` and
  `pnpm contracts:check` — both verify the bundle at 2.4.1.
- `make gate` (gofmt, `go vet ./...`, the short suite whole-repo): **green**.
- `make ts-gate` (`pnpm lint`, `pnpm contracts:check`, `tsc --noEmit`):
  **green**.
- `make suite` (short, `-count=1`, cache refused): **green**, 48 packages.
- The whole TypeScript suite, because a contract file moved:
  `npx vitest run` — **80 files, 1880 tests, all green**, plus
  `pnpm contracts:test` (13 node tests, green).
- I did not run the full `internal/reconcile` suite this attempt: nothing in
  this change touches it, and AGENTS.md asks for it only when a tick touches
  dispatch, claims or a profile. `make gate`'s short suite runs the package
  short (15s, green). I did run the one test the repeated finding below names,
  three times — see the finding for what that did and did not show.

## What the next tick has to know

- **The lease match is exactly the factory's two spellings** — the door's job
  id `run-<run>/tick-…` and the review boot's sandbox name `<run>-<boot>`,
  separators included, and nothing else. If the factory ever changes either
  speller (`attemptJobID`, `sandboxName`), the parity guard
  `TestTheLeaseMatchAgreesWithTheJobIdsTheFactoryMints` fails first and must
  move with it; if a third spelling ever appears, it gets added to the
  matcher AND to the factory-mints test, never assumed.
- **The contract bundle is 2.4.1** and the cloudflare pin moves with it. The
  `dashboard` golden is a LOCAL run and carries `cost.subscription` null by
  design; the populated shape is bound by
  `TestTheContractBindsTheLeasedSubscription`, which derives it from the
  golden. A cloud run's own golden — one that could carry the subscription
  populated in its own right — does not exist yet; ugm (the wave-3 renderer)
  is the tick that would want one, and the derivation pattern is the shape to
  copy rather than hand-typing a document.
- The rest of the base's delivery stands: `dashCost` returns `""` when
  nothing is metered and the CI segment renders alone; any further model
  change this epic makes re-cuts the bundle again (2.4.2, 2.5.0, …).
- The phone page (cloudflare/src/phone.ts) still renders the old
  "cost: not metered" recital and does not show `cost.subscription` — the
  proposal below, unchanged from the previous attempt: it is not this tick's
  surface and it stays with that finding.

```findings v2
[
  {
    "kind": "defect",
    "title": "restart evidence tests demand strictly-after on second-granular stamps",
    "severity": "medium",
    "body": "Repeat of the fifth attempt's finding, unchanged. Two tests in internal/reconcile/restart_test.go assert the backing evidence's started_at is strictly after the dead incarnation's finished_at, but both stamps are RFC3339 at second granularity, so when the resumed gate starts in the same second the first one finished, late.After(dead) is false and the test fails on a true reuse: the failure is a flake of the clock, not of the code under test. It only runs in the full suite (shorttest.EndToEnd skips it in the gate), so it can turn a main or epic-branch CI shard red spuriously, and a red main blocks every deploy behind it. The fifth attempt reproduced it once in three at this branch's base; I could not reproduce it in three runs here (23s), which is what a one-in-three same-second race looks like from the other side — the shape is in the code either way, at both sites below.",
    "evidence": "internal/reconcile/restart_test.go:350 (TestAResumedRunDoesNotCloseATickOnGateEvidenceFromAChangedCommand) and :488 (…FromAChangedProfile) — both `if !late.After(dead)` over RFC3339 second-granular stamps"
  },
  {
    "kind": "proposal",
    "title": "the phone page's cost section ignores cost.subscription and recites not metered",
    "severity": "low",
    "body": "Repeat of the earlier attempts' proposal, unchanged. cloudflare/src/phone.ts's costHTML renders one segment per cost line and 'cost: not metered' when the model carries none, so the phone page — a renderer of the same status model the dashboard renders, and the surface a person reads away from their laptop — still recites what nobody measured and never shows the leased subscription's window use that a claude-sub run's cost is. The tick's words named the dashboard's string, so this was deliberately left out of b13's scope; the model field and the contract's populated shape (now bound by TestTheContractBindsTheLeasedSubscription, derivable from the imported golden) are what a tick that takes this would render.",
    "evidence": "cloudflare/src/phone.ts:933-944 (costHTML), pinned by cloudflare/test/phone-page.test.ts asserting 'claude not metered'"
  }
]
```

## Status

The three cases render as specified and are tested at the render, model,
gathering and factory layers; the lease match is now exactly the two
spellings the factory mints (a bare-run-id match nobody could produce is
gone, held by a test that fails without it), and the contract's claim about
its own golden is true again — the populated shape bound by deriving it from
the golden, with the bundle bumped to 2.4.1 and both verifiers green.
`make gate`, `make ts-gate` and `make suite` pass. Committed as 431aa3e on
`tick/ymf/attempt-6/b13` (7 files: the matcher and its new test, the contract
prose with its binding test, and the bundle re-cut — manifest, changelog and
the cloudflare pin).

STATUS: DONE
