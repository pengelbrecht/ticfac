# b13 — resolve-conflict: b13 (cost line) × lck (status model), epic ymf

This is the report of the resolve-conflict job on `tick/ymf/attempt-5-resolve-5-r-63fd7229/b13`.
It replaces the ticks-worker placeholder that stands in this file on the branch's
base: the two earlier resolve containers for this tick (resolve-3 ×3, resolve-5
`e0ec461e`) exited without writing one, and this job is the retry. The run's own
records carry those failures; nothing here is about them.

## The conflict

`b13` ("Cost line: only when metered; a claude-sub run shows subscription window
use", attempt 5, try 2) was merged into `epic/ymf`, which already carried `lck`
("Status model: plain-language tick status, epic phase track, needs-you, ticks
grouped by state", merged as 76af4578d5ca). Four files did not merge:

- `internal/cli/status.go` (2 hunks) — both sides build the `modelGatherers`
  that feed the one status model: `lck`'s side of the epic (via tick 93n) adds
  `activity: workerActivity`; `b13` adds `claudeSub: statusClaudeSub` on the
  cloud path and a comment explaining why the LOCAL one-shot gathering passes
  no claude-sub reader.
- `internal/cli/status_model.go` (2 hunks) — the `modelGatherers` struct gained
  a field per side, and `cloudStatusModel` gained one source-gathering block
  per side (the cloud census / `remoteActivity` from 93n, the leased
  subscription read from b13).
- `internal/cli/watch.go` (3 hunks) — `watchGatherModel`'s gatherers, the
  per-watch source caches in `watchLive`, and the two cache types themselves
  (`watchActivityCache` on the epic side, `watchClaudeSubCache` on b13's).
- `contracts/bundle.json` (2 hunks) — both sides had cut contract bundle
  **2.4.0** over the 2.3.0 they shared, each re-cutting `status-model.json`
  with different bytes: the epic's cut carries lck's status words/track/groups
  (fixture digest `72fc79c6…`), b13's carries the cost `subscription` object
  (`2fb8b9fb…`), and the two ledger entries for 2.4.0 differ.

## The resolution — a union everywhere, no side dropped

- `status.go`: the local `status --json` gathering keeps b13's no-reader comment
  AND 93n's `activity` gatherer; the cloud gathering passes both `activity` and
  `claudeSub`.
- `status_model.go`: `modelGatherers` carries both fields with both comments;
  `cloudStatusModel` keeps both gathering blocks (the census/`remoteActivity`
  block, then the claude-sub block) — the merged `statusBuild(Sources{…})` call
  site already reads `Standing`, `RemoteActivity` and `ClaudeSub`, so both were
  required for the file to compile at all.
- `watch.go`: `watchGatherModel` and `watchLive`'s cached gatherers pass both
  `activity` and `claudeSub`; both cache types are kept, the activity cache with
  its own shorter TTL as the epic side wrote it, the claude-sub cache at the
  source TTL as b13 wrote it. A local watch still gathers no claude-sub fact
  (`localStatusModel` never reads the reader), exactly as b13's design states.
- `contracts/bundle.json` was **re-cut at 2.5.0**, not merged at 2.4.0: two
  parallel cuts claimed one version string, and the bundle's own rule ("a
  version string must never mean two different sets of bytes") is what the
  `version_digests` ledger exists to enforce. I followed the repository's own
  fold precedent (CHANGELOG 2.1.0 and 1.7.0, main folded into epic/hn6): the
  union keeps the epic's binding of 2.4.0 in the ledger
  (`3f5cc4ca…`), records b13's own cut of 2.4.0 (`5c2fab2c…`) in the CHANGELOG
  text instead, re-cuts the merged fixture (`a164de87…` — both sides' fields:
  `status`/`exception`/`track`/`here`/`groups` beside `cost.subscription`) at
  the next MINOR, and adds the `## 2.5.0` fold entry with b13's entry
  re-published under it as "b13's 2.4.0, re-cut here".
  `cloudflare/contracts.pin.json` moves to 2.5.0 in the same commit, as the
  changelog's own how-to-cut rule requires.

Both intents hold in the resolved tree: the status words, phase track and
groups render from the shared model (lck), and the cost line renders nothing
when nothing is metered, the metered cost when there is one, and the leased
subscription label with its 5h/7d window use on a claude-sub run (b13) —
`make gate` and both contract verifiers confirm the union.

## What I ran

- `go run ./cmd/contracts check` — green at 2.5.0.
- `cloudflare`: `pnpm install --frozen-lockfile`, `pnpm contracts:check` —
  "16 contract(s) at bundle 2.5.0", green; `pnpm lint` (Biome) and
  `pnpm exec tsc --noEmit` — green.
- `make gate` (gofmt over the whole tree, `go vet ./...`,
  `go test -short ./...`) — green, every package.
- `pnpm exec vitest run` (80 files, 1880 tests) — green, not part of the gate
  but the bundle and the phone page's model both changed hands.
- `go test ./internal/cli/` without `-short` — green (the watch's pty
  end-to-end tests ride in this package and exercise the cached gatherers).
- `go test -short ./internal/statusmodel/ ./internal/contracts/...
  ./internal/factory/` — green.

## What the next tick has to know

- The bundle is at **2.5.0** and `cloudflare/contracts.pin.json` says so. Any
  later tick that touches a fixture cuts **2.6.0**, never re-cuts 2.5.0.
- `ugm` (dashboard layout) and the watch-redesign ticks render from the shared
  model: every field both sides added is already in the merged
  `contracts/status-model.json` and populated in its goldens.
- The phone page still renders only the cost LINES; rendering the new
  `cost.subscription` segment there is already filed as backlog tick `bt4`
  (b13 try 1's finding) — untouched here, and it is not this merge's work.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "Two same-wave ticks each cut contract bundle 2.4.0 (lck and b13)",
    "severity": "medium",
    "body": "lck and b13 were created in the same second and dispatched in the same wave of run run_91f2952af63f42f08a0ad243b4488842 (both priority 2, both touching contracts/status-model.json), and each cut its own 2.4.0 of the contract bundle over the 2.3.0 they shared. The wave, not the text, was wrong: the union of the two fixtures merges cleanly, but one version string cannot name two different sets of bytes, so the fold forced a re-cut at 2.5.0 (CHANGELOG entry added there) and this same collision produced the 3-conflict merge that three resolve jobs for b13 try 1 failed to answer. A wave that puts two ticks on one fixture needs one of them to own the bundle bump, or the version bump needs to move to the fold where the union is cut — either rule would have made this a clean merge."
  }
]
```

STATUS: DONE
