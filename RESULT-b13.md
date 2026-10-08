<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-6-resolve-6-dfec448b/b13`, base `6a00ad6685e7233c1f0d2fc4a91c6c0f2ebcc95c`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict — tick b13 (attempt 6) into epic/ymf

Branch: `tick/ymf/attempt-6-resolve-6-dfec448b/b13`
Resolution commit: `a5d5cb0` (on top of the conflicted merge `6a00ad6`)
Working tree: clean, no conflict markers left in any file git conflicted.

## The two intents

- **Mine — tick b13**, "Cost line: only when metered; a claude-sub run shows
  subscription window use": the status model's `cost` gains `subscription` —
  the leased claude-sub label plus the factory proxy's last-seen 5h/7d window
  use, required-and-null, never a token, read through the factory's
  `/api/claude-sub` (operator auth). A *local* gathering deliberately carries
  no claude-sub reader: a local run's jobs lease nothing from the factory's
  pool.
- **Other side — epic/ymf's integrated work**: tick **lck** (plain-language
  tick status words, epic phase track, you-are-here, tick groups) and tick
  **93n** (per-worker activity excerpts), both wired through the same
  `modelGatherers` seam, and lck's own re-cut of the same contract fixture
  `contracts/status-model.json`.

## Resolutions — six conflicted files, all unions

1. `internal/cli/status_model.go` — `modelGatherers` carries **both**
   `activity` (epic, tick 93n) and `claudeSub` (tick b13), each with its
   comment; `cloudStatusModel` keeps **both** the census/remote-activity
   block (93n) and the leased-subscription block (b13). The auto-merged
   `Sources{...}` already passed both variables, so the union is what the
   tree demanded.
2. `internal/cli/status.go` — the local `status --json` gathering wires
   `activity` (epic) **and** keeps b13's note that it reads no claude-sub
   reader; the cloud gathering wires `activity` **and** `claudeSub`.
3. `internal/cli/watch.go` — `watchGatherModel` and `watchLive`'s gatherers
   carry both fields; both cache types stand (`watchActivityCache` at its
   own 5s TTL, `watchClaudeSubCache` at the sources' 30s TTL).
4. `contracts/status-model.json` — the `why` narrative carries b13's
   cost.subscription material (the dashboard golden's one *stated* null, the
   derivation that binds the populated shape,
   `TestTheContractBindsTheLeasedSubscription`) **and** lck's watch-redesign
   paragraph beside it. Both halves' claims remain true in the union: the
   golden carries the watch-redesign fields populated with `cost.subscription`
   null.
5. `contracts/bundle.json` / `cloudflare/contracts.pin.json` — the version
   collision both lineages made cutting 2.4.0 with different bytes is
   resolved the way the repo's own folds (CHANGELOG 1.7.0, 2.1.0) resolved
   theirs: the union re-cuts at **2.5.0**, the next MINOR over the tick's
   own 2.4.1; `version_digests` keeps the **epic's** 2.4.0 binding
   (`3f5cc4ca…`, the fold's base line) and b13's 2.4.1 (`ebd89846…`, its
   number stands — nothing else cut it); b13's own 2.4.0 binding
   (`5c2fab2c…`) is recorded in the CHANGELOG prose as the precedent
   dictates, and b13's entry survives verbatim as "### tick b13's 2.4.0,
   re-cut here" (contracts/CHANGELOG.md:86); `cloudflare/contracts.pin.json`
   moves to 2.5.0 and the per-file digests are the union's bytes. This was
   the one genuinely irreconcilable point — a version string must never mean
   two different sets of bytes, and I verified both sides' ledger bindings
   recompute exactly from their digests before choosing.

The `<<<<<<<` strings still present in the tree are pre-existing fixtures,
not unresolved conflicts: `internal/reconcile`'s conflict-handling tests and
`testdata/fake-runner.sh` fabricate markers on purpose, and
`profiles*/resolve-conflict.md` are the job briefs. None were touched.

## Gates on the union tree

- `make gate` — gofmt, `go vet`, full `go test -short ./...`: **pass**.
- `go run ./cmd/contracts check` (offline bundle gate): **verifies at 2.5.0**.
- Factory TS suite: `pnpm lint`, `pnpm contracts:check`,
  `pnpm contracts:test`, `pnpm operator-strings`, `tsc --noEmit`, and the
  full `vitest run` (80 files, 1880 tests, incl. status-model, phone page,
  claude-sub routes): **all pass**.

Committed source only — no build output, caches, or anything under the
run's `runs/` artifact prefix; `node_modules` from the TS gate run is
gitignored and untracked.

```findings v2
[
  {
    "kind": "defect",
    "title": "Wave partition let two parallel ymf ticks cut contract bundle 2.4.0 twice",
    "severity": "medium",
    "body": "Two parallel ticks of one epic each re-cut contracts/bundle.json at version 2.4.0 with different bytes: lck's watch-redesign status-model.json on the epic line, b13's cost.subscription status-model.json on the tick line (patched at 2.4.1). A version string must never mean two different sets of bytes, so the merge could not keep either 2.4.0 and had to fold the way the repo's 1.7.0 and 2.1.0 folds did — the union re-cuts at 2.5.0 and the tick's own 2.4.0 loses its version_digests slot to the epic's binding (recorded in CHANGELOG prose instead). The text union needed no choice; the version collision was pure wave partition: parallel ticks both owned the shared mutable resource that contracts/ is. Serialising bundle re-cuts per integration order — or giving one tick per wave ownership of contracts/ — would have produced 2.4.0 then 2.4.1 with no fold.",
    "evidence": "contracts/CHANGELOG.md:56 — the 2.5.0 fold entry records both 2.4.0 bindings (3f5cc4ca… epic / 5c2fab2c… tick); both recompute exactly from their respective digests maps via Bundle.ContentDigest"
  },
  {
    "kind": "defect",
    "title": "The changelog gate passes a merge that scrambles version headings",
    "severity": "low",
    "body": "git auto-merged contracts/CHANGELOG.md into an incoherent state that the offline gate accepted: two '## 2.4.0' headings with '## 2.4.1' sandwiched between them (newest-first violated, no blank line). VerifyChangelog only checks that the bundle's CURRENT version has a heading, so duplicate and out-of-order headings for other versions pass silently — the scramble was caught only by reading it. A check that refuses duplicate '## <version>' headings and headings out of descending order would make the artifact the gate already claims to enforce. Resolved in this tree by reordering into the fold-precedent shape (2.5.0, sub-entry, 2.4.1, 2.4.0, 2.3.0); the hazard stands for the next parallel-cut merge.",
    "evidence": "internal/contracts/bundle.go:272 — VerifyChangelog returns on the first heading that equals '## ' + version; nothing checks for a second heading or the ordering of the rest"
  }
]
```

STATUS: DONE
