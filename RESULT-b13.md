<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-3-resolve-3-r-35b70118/b13`, base `82f10fea57f5de8cb185e7efcea722270b4c284b`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# b13 resolve-conflict — the b13 × 93n merge: both gatherers in one `modelGatherers`

Branch: `tick/ymf/attempt-3-resolve-3-r-35b70118/b13`
Role: resolve-conflict (profile `profiles/resolve-conflict.md`, local-subprocess)
Ticks in conflict: **b13** (mine — the cost line: only when metered, and a
claude-sub run's leased subscription window use) × **93n** (already merged into
`epic/ymf` — a live activity excerpt per running worker, local and cloud).

## What state I found, and what I did about it

This worktree was cut at `82f10fe` — a commit whose message claims the b13 × 93n
resolution and whose tree carries **no conflict markers**. That is an earlier
session of this same resolve job (the run's start refs name both `6d5ec60…`,
the conflicted merge, and `82f10fe`, its outcome). I did not take its word for
it and did not re-resolve blind: I fetched the merge's own parents from origin
and audited the resolution against them.

- conflicted merge `6d5ec60` = parents `8376acc` (the epic/ymf head, with 93n
  integrated) × `7019eeb` (the b13 attempt); merge base `8505bb2`.
- **Seven markers over three files**: `internal/cli/status.go` (×2),
  `internal/cli/status_model.go` (×2), `internal/cli/watch.go` (×3) — exactly
  the seam both ticks grew from the same base: the `modelGatherers` per-frame
  source policy struct, its call sites, and the watch's per-frame cache block.

## The resolution: a union at every hunk, nothing chosen over anything

- `modelGatherers` carries **both** readers: 93n's `activity` (keyed by the host
  the caller names — a non-nil client is the factory's watch socket, nil a local
  watch door; nil = not-measured) and b13's `claudeSub` (cloud-only, the leased
  subscription; a nil reader is the local shape).
- The local `status --json` call site keeps 93n's `activity` reader and b13's
  no-local-claude-sub comment — local jobs lease nothing.
- The cloud `status --json` call site and `cloudStatusModel` itself gather
  **both**: 93n's cloud census + `RemoteActivity` block and b13's leased-
  subscription block, each with its own degraded source name (`claude-sub`), and
  each block properly closed (the two sides' braces were interleaved in the
  marker — the union closes both).
- The watch wires **both** in both of its gathers: `watchGatherModel` passes both
  direct reads, and `watchLive`'s frame builder keeps both per-frame caches side
  by side — activity at its own shorter 5s TTL, claude-sub at the shared 30s.

## Audit against the parents (every path either side touched, 64 files)

| group | result |
|---|---|
| 39 paths the epic side (93n's wave) alone changed | byte-identical to the epic side |
| 13 paths b13 alone changed | byte-identical to b13's branch |
| 11 paths both changed | 8 byte-identical to git's own auto-merge; the 3 hand-resolved ones differ from it **only** by the markers coming out with both sides' text kept in |
| `RESULT-b13.md` | absent **by design** — `internal/reconcile/report_merge.go` drops every worker report from every integration merge, so no reader loses anything |
| contract drift | none: 93n added a *source* (`Sources.RemoteActivity`), not a JSON field, so b13's 2.4.0 re-cut stays correct for both halves; `contracts:check` is green at 2.4.0 |

## What I committed (`1499cc2`, source only, one new file)

The one obligation the union owed that **no parent could have paid**: each
tick's tests were written against a tree carrying only its own half, so no test
anywhere asked ONE gathering for BOTH readers. A third reader grown onto this
struct later could drop a first one at a call site with no test naming the frame
it cost. `internal/cli/status_model_gatherers_test.go` (a path no other tick
owns) adds:

- `TestCloudStatusModelGathersActivityAndTheLeasedSubscription` — one cloud
  gathering, both readers observable in the one `Sources` every surface renders;
  93n's keyed by tick+attempt **with the cloud client**, b13's lease reduced to
  label + both windows.
- `TestWatchGatherModelCarriesBothReaders` — the watch's own gather (the exact
  line both ticks rewrote and the merge had to union) asks for both, and the
  pool is asked under the operator's own bearer token.
- `TestWatchClaudeSubCacheServesWithinTTLAndReturnsErrorsFresh` — b13's cache
  contract (an error is a fact of this frame and never cached; a success, nil
  included, is served within the TTL; the pool is asked again past it), the twin
  of the activity cache's test that was the only one of the pair to exist.

Mutation-checked before landing: dropping 93n's activity reader or b13's
claude-sub reader from the watch's gather, or either block from
`cloudStatusModel`, fails these pins.

## What I ran

- `gofmt -l .` clean; `go vet ./...` clean.
- The short suite across the whole repository (`go test -short -count=1
  -timeout 40m -parallel 8 ./...`): **48/48 packages pass**.
- `go test -count=1 ./internal/cli/` (the touched package, full): ok.
- The TypeScript half of the gate: `pnpm lint` (biome, error-on-warnings),
  `pnpm contracts:check` (bundle 2.4.0 read in place), `pnpm exec tsc
  --noEmit` — all green. Its `node_modules` was removed again, so the worktree
  holds source only.
- Zero conflict markers anywhere in the tracked tree; nothing uncommitted
  besides this report; no build output or cache left behind.

## What the next tick has to know

- The wave, not the text, was the defect: b13 and 93n were dispatched 19 seconds
  apart in one wave and both grew the same seam. **lck** (attempt 4 of this run,
  still dispatched) also derives its work in `internal/statusmodel` and the
  status model's JSON — the same partition is set to cost another resolve job
  when it lands. See the finding below.
- The phone page still renders the old per-line "not metered" recital and not
  `cost.subscription`. That is b13's own reported gap, promoted to backlog tick
  `bt4` with an owner — it is untouched by this merge and not a merge artifact.
- If a later tick adds a third reader to `modelGatherers`, grow the pins in
  `internal/cli/status_model_gatherers_test.go` with it; they are the seam's
  contract now.
- I could not push: this container's credential helper got no GitHub token from
  the factory token door and origin refused the push. Both commits are on the
  branch in this worktree, which is where the tree both intents live in stands.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Wave partition: 93n, b13 and lck all grew the modelGatherers seam",
    "severity": "medium",
    "body": "The b13 x 93n merge conflict (7 markers over internal/cli/status.go, status_model.go and watch.go) was a planning defect, not a modelling problem: two ticks of one wave independently added a reader to the same per-frame source policy struct (modelGatherers), wired it at the same call sites, and gave the watch a per-frame cache for it, so both parents rewrote the same lines from the same base. lck (attempt 4 of the same run, still dispatched) also derives its work in internal/statusmodel and the status model's JSON, so the same partition is set to cost another resolve job when it lands. Consider partitioning this epic's status-model ticks by seam ownership, or sequencing them. The text union is made and pinned (internal/cli/status_model_gatherers_test.go), so nothing here blocks integration.",
    "evidence": "internal/cli/status_model.go:546 — the modelGatherers struct both ticks grew; internal/cli/watch.go:791 and :921 — the two call-site hunks the merge had to union"
  }
]
```

STATUS: DONE
