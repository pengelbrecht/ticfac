<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-1-base-fold-1-7e762438/ymf`, base `b1f6d8ededac1e7a758428d0de7d5d08995f9bf0`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict — the fold of main into epic/ymf

Branch: `tick/ymf/attempt-1-base-fold-1-7e762438/ymf`
Resolution commit: `f86cf5f` (on top of the conflicted fold `b1f6d8e`, whose
parents are epic/ymf `049d10cb` and main `daffbf60a`).

## What the conflict was

The base fold of main into `epic/ymf`, left unresolved in three files. All
three are one question — what does the versioned contract bundle call the
union of two lines that each cut the SAME version numbers (2.4.0, 2.4.1) with
DIFFERENT bytes. The two lines last shared bundle **2.3.0**:

- **main** (daffbf60a): 2.3.1 (tick p0n, commit 9bdbcf9b2 —
  `lifecycle-invariants.json`'s `today` cross-references follow the watch's
  decision core into `run-watch.ts`), 2.4.0 (epic ex6, tick 8gd, commit
  91cc78f79 — `worker-boot-contract.json` gains the review job's boot/finish
  contract), and its own 2.4.1 fold of the two.
- **epic/ymf** (049d10cb): 2.4.0 (tick lck — the status-model watch-redesign
  fields: per-tick `status`/`exception`, `track`/`here`, `groups`), 2.4.1
  (tick b13's words patch), 2.5.0 (the b13 fold — `cost.subscription`).

The conflicted files were `cloudflare/contracts.pin.json` (2.5.0 vs 2.4.1),
`contracts/bundle.json` (`version`, and the `version_digests` tail where the
two lines' 2.4.0/2.4.1 bindings disagree), and `contracts/CHANGELOG.md` (both
lines' new entries inserted above the shared 2.3.0 entry).

## How I resolved it

The repo has a documented convention for exactly this collision — it has hit
and resolved the same class at 1.2.0, 1.7.0, 2.1.0 and (on the epic's own line)
2.5.0: "a version string must never mean two different sets of bytes", so a
fold re-cuts the union at the next version over the SURVIVING line, keeps the
surviving line's ledger bindings for the contested numbers, records the folded
line's displaced cuts in the changelog, and lets an un-contested version on
the folded side join the ledger whole (the 1.2.1/1.1.1 precedent).

- **`contracts/bundle.json`** — the union is cut at **2.6.0**, the next MINOR
  over the epic's 2.5.0 (the surviving, higher line). The fixtures themselves
  merged cleanly: the merged per-file `digests` map already matched every file
  on disk (re-hashed all 16), and I verified the union carries BOTH sides'
  fields — lck's `status`/`exception`/`track`/`here`/`groups` and b13's
  `cost.subscription` in `status-model.json`; 8gd's
  `review_boot_*`/`review_finish_*` in `worker-boot-contract.json`; p0n's
  `run-watch.ts` cross-references in `lifecycle-invariants.json`. The union is
  each side's bytes unchanged (the two halves touch different files). The
  ledger keeps the epic's bindings of 2.4.0 (3f5cc4ca…) and 2.4.1 (ebd89846…),
  the entries the fold's surviving line had already written; main's 2.3.1
  (6cc4290a…) joins whole — a version the epic never cut; and 2.6.0 records
  the ContentDigest of this cut (7cb3cff0…).
- **`contracts/CHANGELOG.md`** — a new `## 2.6.0` fold entry documents the
  collision, both lines' halves, the ledger decisions and the consumer cost.
  Main's contested texts are re-cut verbatim as subsections (`### main's
  2.4.1, re-cut here`, `### main's 2.4.0, re-cut here`); the epic's
  2.5.0/2.4.1/2.4.0 entries and main's 2.3.1 entry stand verbatim (byte-checked
  against the pre-resolution file). Every ledger version keeps exactly one
  `##` heading, in descending order.
- **`cloudflare/contracts.pin.json`** — `bundleVersion` moves to 2.6.0, in the
  same commit, as the changelog's "how to cut a bump" rule requires.

No side's intent was dropped and nothing was irreconcilable: the union keeps
every change either line made — the epic keeps building on everything main
now has (the review boot/finish contract, the run-watch.ts cross-references)
and keeps every change its own ticks made (the watch-redesign fields, the
subscription cost line and its words fix). This was not a wave-partition
defect: the collision is between the epic's line and main's line, both
cutting the bundle legitimately, and the repo's own machinery
(`internal/reconcile/union_merge.go`) deliberately leaves the bundle conflict
to the resolve job rather than giving it the mechanical union path changelogs
get — I followed the documented convention instead of inventing one.

## Verification (all run in the foreground, all green)

- `go run ./cmd/contracts check` — the offline gate verifies the bundle
  (per-file digests vs disk, version↔ledger entry) and the ticks-owned pin.
- `go test ./internal/contracts/...` — bundle tests incl. parity, ok.
- `go test ./internal/statusmodel/...` — b13/lck's fixture bindings, ok.
- TypeScript: `verifyBundle` + `verifySchemaIds` against the real pin, ok at
  2.6.0; `node --test cloudflare/scripts/contracts.test.mjs` — 13/13 pass.
- No conflict markers remain in the three files; working tree clean; only
  source files touched (no build output, nothing under the run's `runs/`
  artifact prefix). Residual `<<<<<<<`-shaped strings elsewhere in the tree
  are the repo's own resolve-machinery sources, tests and skill docs —
  pre-existing and intentional, not unresolved conflicts.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Base-fold bundle-version collisions cost a ceiling-tier resolve job each time",
    "severity": "low",
    "body": "Every base fold of main into an epic whose lines both cut contracts/bundle.json collides on the same version numbers (2.4.0/2.4.1 at this fold; 1.1.0, 1.2.0 and 1.3.0/1.4.0 before it) and dispatches a resolve-conflict job at the ceiling tier for what is no longer a judgement: the changelog's own fold convention is now deterministic with five precedents — re-cut the union at the next MINOR over the surviving line, keep its version_digests bindings for the contested numbers, record the folded line's displaced cuts in the fold entry, and let an uncontested number join whole. union_merge.go leaves bundle.json to the resolve job on purpose (\"no deterministic generator\"), but the generator now exists in prose. Main also carries an epic's own fold (its 2.4.1, the fold of main into epic/t8u), which makes the race likelier at every fork. Either pre-bump the epic's bundle past main's line at fork time, or teach the base fold the convention.",
    "evidence": "internal/reconcile/union_merge.go:26-37 — the comment that keeps bundle.json the resolve job's; contracts/CHANGELOG.md ## 1.2.0, 1.7.0, 2.1.0, 2.5.0, 2.6.0 — five fold entries naming the same collision and its resolution"
  }
]
```

STATUS: DONE
