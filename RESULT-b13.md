<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-5-resolve-5-r-62fd7096/b13`, base `0ff09765c9d91599e1b9196e46a8c6604d712ff2`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict report — tick b13 attempt-5 into epic/ymf

Branch: `tick/ymf/attempt-5-resolve-5-r-62fd7096/b13`
Resolution commit: `2c1d906` — "Resolve the b13 × watch-redesign conflict: the
claude-sub cost line joins the epic's status model"

## The two intents in conflict

Mine first: **b13** — "Cost line: only when metered; a claude-sub run shows
subscription window use": replace the unmetered cost recital with nothing
when no metered cost exists, the metered cost when it does, and — on a run
whose config is claude-sub — the leased subscription label plus its 5h/7d
window utilization read from the factory's `/api/claude-sub` under operator
auth. Never a token.

The other side is the epic **ymf** integration branch (watch redesign), whose
line already carries the merged work of sibling ticks: **lck** (plain-language
tick status, tick groups, epic phase track, needs-you — versioned in the same
`status-model.json` contract), **93n** (live per-worker activity excerpt,
local and cloud), **47j** (readable feed sentences). Four files carried
git's markers where b13's work met 93n's/lck's.

## Per-file resolutions — all unions, nothing dropped

- **`internal/cli/status_model.go`** (2 conflicts)
  - `modelGatherers` keeps **both** fields with both doc comments: 93n's
    `activity` (two-host reader: factory watch socket for cloud, worker's own
    watch door for local) and b13's `claudeSub` (cloud-only; a nil reader is
    the local shape).
  - `cloudStatusModel` keeps both blocks in sequence: 93n's checkpoint-word
    census (`standing`) + `remoteActivity` wiring, then b13's subscription
    gathering (degrades `claude-sub` only when a factory that should answer
    cannot be asked; no lease/no pool/no factory answers nil — the optional
    state). `statusBuild` already names both halves (`Standing`,
    `RemoteActivity`, `ClaudeSub`).
- **`internal/cli/status.go`** (2 conflicts)
  - The **local** one-shot gathering keeps 93n's `activity: workerActivity`
    AND b13's comment + rule: a local run's jobs lease nothing, so the local
    gathering carries no claude-sub reader.
  - The **cloud** one-shot gathering passes **both** readers:
    `modelGatherers{…, activity: workerActivity, claudeSub: statusClaudeSub}`.
- **`internal/cli/watch.go`** (3 conflicts)
  - `watchGatherModel` (the watch's one document) passes both readers.
  - The live watch keeps **both** per-watch caches — 93n's `watchActivityCache`
    at its own 5-second TTL ("what is happening right now" must read live)
    beside b13's `watchClaudeSubCache` at the source TTL — and the frame's
    `modelGatherers` wires `activity: activityCache.Activity,
    claudeSub: claudeSubCache.Subscription`.
  - Both cache types (`watchActivityCache`/`watchActivityTTL` and
    `watchClaudeSubCache`) stand in full.
- **`contracts/bundle.json`** (2 conflicts) — the conflict the bundle's own
  rule had to decide. Both sides cut version **2.4.0** from the same 2.3.0
  bytes with different `status-model.json` digests: the epic's cut carries
  lck's watch-redesign fields, b13's cut carries the cost object's
  `subscription`. The merged fixture — the union both ticks want, verified:
  status words + `exception` + `track`/`here` + `groups` **and**
  `subscription` — hashes to `a164de87…`, matching **neither** side's
  binding, and `version_digests` forbids re-cutting a version it already
  binds. So neither digest could be picked: either choice fails
  `contracts.Verify` against the on-disk fixture. Per the repository's own
  fold precedent (1.2.0 for the 1.1.0 double cut, 2.1.0 for 1.3.0/1.4.0), the
  union re-cuts at the next MINOR version:
  - `version` → **2.5.0**; `digests["status-model.json"]` → the merged
    fixture's sha256 `a164de87676710055c78b90e291dc253d7fe2aa5daab20ebf3250c48f8f2ccb2`.
  - `version_digests` keeps the **epic line's** binding of 2.4.0
    (`3f5cc4ca…` — the entry the fold's surviving line had already written,
    never rewritten) and adds
    `2.5.0: f5d1222cd8a278d2c3c03afa6679be1f0474be6566570fed282b7cc0d96aff37`.
  - `contracts/CHANGELOG.md` gains the `## 2.5.0` fold entry, which records
    the attempt branch's own 2.4.0 cut (`5c2fab2c…`) in prose, as the
    precedents do; the two 2.4.0 texts stay **verbatim** under `## 2.4.0`,
    attributed as the two parallel cuts (the 1.1.0 entry's style).
  - `cloudflare/contracts.pin.json` → `bundleVersion` **2.5.0**.

No conflict required choosing one side over the other; the only judgement was
the version arithmetic, and the bundle's ledger rule left exactly one green
answer.

## Verification (all in the foreground, all green)

- `make gate` — gofmt (clean outside `contracts/`), `go vet ./...`,
  `go test -short -timeout 45m -parallel 12 ./...`: exit 0, 48 packages ok,
  no FAILs. Both sides' tests pass in the union tree: b13's
  `TestStatusModelCloudGathersTheLeasedSubscription` (6 legs) and `dashCost`
  three-case rendering; 93n's activity-cache and worker-activity tests; lck's
  statusword/pipeline/group tests.
- `make ts-gate` — `pnpm lint` (Biome; the one remaining info is a
  pre-existing `useTemplate` note in `src/phone.ts`, untouched here),
  `pnpm contracts:check` ("contracts: ok — 16 contract(s) at bundle 2.5.0"),
  `pnpm exec tsc --noEmit`: exit 0.
- `go run ./cmd/contracts check`: clean — the bundle verifies and the
  ticks-owned files match the root pin.
- `pnpm contracts:test` (TS negative controls): 13 pass, 0 fail.
- The vitest suites that read the changed contract pass: `status-model`,
  `phone-page` (29), `claude-sub` (34), `claude-sub-routes` (5), `progress`
  (15), `status-alerts` (15) — 98 tests.
- No conflict marker remains in any tracked file (`git grep` clean);
  node_modules/pnpm artifacts are gitignored and were not committed; the
  resolution commit touches exactly the six source files.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "Same-wave ticks b13 and lck both cut contract bundle version 2.4.0",
    "severity": "medium",
    "body": "The merge of b13's attempt-5 into epic/ymf met a bundle cut twice at one version: lck's watch-redesign fields and b13's cost `subscription` each bumped 2.4.0 from the same 2.3.0 bytes, and the append-only version_digests ledger forbids either digest standing for the merged fixture, so the union had to re-cut as 2.5.0 (the fold precedent the 1.2.0 and 2.1.0 entries set). This is the third double cut of this kind in the ledger's history; a wave that contains a bundle bump should sequence it, or the wave planner should treat contracts/bundle.json's `version` line as a single-writer resource.",
    "evidence": "contracts/bundle.json — version_digests[\"2.4.0\"] was bound twice, 3f5cc4ca… (epic line) and 5c2fab2c… (attempt branch); contracts/CHANGELOG.md ## 2.5.0"
  },
  {
    "kind": "proposal",
    "title": "Gather the claude-sub subscription in ticfac overview's models",
    "severity": "low",
    "body": "`ticfac status <cloud-run> --json` and the watch show the leased subscription, but `ticfac overview` builds its rows' models without the claudeSub reader, so the same run's cost.subscription reads null there. The overview runs operator-authed and could read the pool once per refresh and share it across rows; the cloud run's in-situ pushed snapshot genuinely cannot (it holds the run-credential, not operator auth). Outside b13's own scope, so left as found in this resolution.",
    "evidence": "internal/cli/overview.go:273 — modelGatherers carries activity but no claudeSub; internal/cli/statuspush.go:333 — the pushed snapshot likewise"
  }
]
```

## Notes outside the findings

- The full `pnpm test` (vitest) run cannot complete in this sandbox: workerd
  hangs and cancels itself in `test/observe` ("The Workers runtime canceled
  this request because it detected that your Worker's code had hung"). The
  gate deliberately excludes vitest (CI runs it as its own job); every suite
  that reads the changed contract was run individually and passes, so this
  reads as an environment limitation, not a tree defect.
- The one Biome "info" in `make ts-gate` is a pre-existing `useTemplate`
  style note in `src/phone.ts`, untouched by this resolution.

STATUS: DONE_WITH_CONCERNS — the resolution itself is committed and green (make gate and make ts-gate exit 0, contracts verify at 2.5.0, all four conflicted files carry the union, no marker remains); what to double-check is CI's own vitest job: the full behavioural suite could not complete in this sandbox (workerd hangs in test/observe), though every contract-reading suite passes individually.
