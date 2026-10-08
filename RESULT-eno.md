# eno — closing out ex6: pi-durable in the cloud, proved and hardened

Attempt 12 of `eno`, the close-out tick of epic `ex6`, run
`run_af0e77b99c1540b7b8005cf04fd2795d` (which took over the failed
`run_69f8f57832d34601b040367c7207b81f`). Everything below is read from the
integration branch `epic/ex6` (head `3f5bf861c`, source-identical to this
checkout), the ticks' own reports on their branches, and the two runs' records
under `.ticfac/runs/` — never from memory of the plan. The full retro is this
attempt's work commit, `docs/ex6-closeout-retro-2026-10-08.md`; this report
carries what the record reads.

## What the epic delivered, against its own definition of done

The epic set out to prove pi-durable in the cloud for real and harden it
(43y's first cloud run stalled for hours with no stall detection firing). The
ten implement ticks are merged on `epic/ex6` (3sd `aafb2538c`, 8wa `97ab0f651`,
8gd `9edd748fc`, gzw `c15200e79`, m1w `d6d0069bb`, qzg `fab7fe786`, 96s
`b52d34d29`, 2p3 `2f136b4c3`, q6z `6f55eb0d6` through resolve-conflict 6, 2pn
`6294aacb5`) and the NOT READY round's two absorbed fixes with them (tgx
`587236914`, lrd `73f92acbf`); two reviews returned validated envelopes (0rt
NOT READY, ynd READY). Against the marks:

- **[A1] — met by this run.** Every dispatch of the run — twelve attempts,
  the resolve-conflict job, both reviews and this close-out — is a hosted
  pi-durable conversation on Workers AI GLM in a cloud container (this
  close-out runs in one: the harness's `TICFAC_BASH_NONCE` is in its
  environment, PID 1 is the FactorySandbox image's `sleep infinity`). 8gd
  removed the last CLI floor, so the PR-review job hosts too. The stall
  detection the item leans on is in the epic's base (#236/#238); this run's
  own failures were harness/collect faults that the folded #260 turned into
  redispatches, not stalls and not questions for a person. The run's
  completion is this close-out's own event; the checkpoint ("CI is green on
  the epic PR #265; the close-out of ex6 is admitted") is the durable witness.
- **[A2] — machinery met and pinned; the live injection is the operator's.**
  The host-kill resume is exercised by a harness node-half test I ran green
  here ("resumes from the storage after the harness is killed mid-tool,
  without re-running the tool"); the three restore lines are pinned on both
  the host and the runbook (`runbook_fault_evidence_test.go`, the mid-command
  line added by qzg). Fault 1's only door is still a factory deploy, which
  "lands between tool calls and proves nothing" (the runbook says so), so the
  injection (`9if`) waits on the door tick (`0mz`) and the post-merge cloud
  run (`lox`) — all three open, the operator's; the runbook honestly scores
  the real-run half PREDICTED, and the repo's constitution routes a
  deployed-factory clause to post-merge.
- **[A3] — met, after the review caught it half-delivered.** The cloud half
  holds (the factory's gateway stamps the attribution on every hosted
  conversation). The local half is lrd's: `gateway-metering.ts` reads m1w's
  join from `worker.json` and composes the provider override the pi CLI
  extension composes, proven by a wire-level acceptance suite over the real
  entry (every call tagged: route, both bearers, metadata, affinity) with
  negatives that bite, and pinned to the Go writer by a parity guard.
- **[A4] — met, and re-run at this head by the close-out.** `make gate` exit 0
  in 1m57s; `make ts-gate` exit 0 in 30s (biome, `contracts:check` at bundle
  2.4.0, tsc); `make harness-gate` exit 0 in 58s (workerd 10 files, node 11
  files / 66 tests); the cloudflare vitest suite CI runs — 80 files, 1874
  tests — green in 7m01s. CI is green on the epic PR #265 per the run's
  checkpoint. The per-tick gate's harness half is 2pn's cell, one of the five
  pending protected changes.

## What the run absorbed, from its own records

Seven decision records under this run's `absorptions/`, none silent: two on
the basis **`reviewer`** (the NOT READY review named them blocking, so they
became the epic's work — m1w's metering finding → **lrd**, the
protected-change ordering finding → **tgx**, both first decided
`backlog-default` and re-placed before the re-review), and five on the basis
**`backlog-default`** (**flm**, **ydw**, **r0t**, **sob**, **y9g** — low
severity, no done item named, backlog ticks with owners on the epic PR). No
`worker-asserted-high` absorption: the review's own acceptance-criteria
finding was fixed by the run as a tracker edit (`e11b1642e`), so ex6's items
parse as four. The failed first run's eighteen promoted findings are all
`backlog-default` and all still open. The five pending protected changes
(2pn's harness cell `2911b0ce…`, q6z's two routing files `4fe8b808…`,
`6d07a495…`, 2p3's runners.toml `b916a378…`, 96s's local header note
`492d466f…`) took no tick: the run applies each as its own labelled commit
after these reads, and the PR lists them for the merger. I composed the five
by hand in the apply order (contents by key, then appends): the result parses,
declares `go`/`ts`/`harness`, the harness command is byte-identical to the
Makefile's `harness-gate` recipe, the implement cell carries no `args`, and
the cloud/local payloads differ from the branch's files by exactly their
intended edits.

`ticfac amendments ex6` answers that no worker-proposed amendments exist, and
no `amendments/` records exist for either incarnation: nothing to confirm or
mark unconfirmed.

## What is left open

1. The operator's three: `0mz` (the fault-1 door), `9if` (the injections,
   blocked on 0mz), `lox` (the post-merge cloud run) — [A2]'s live half and
   43y's [A4] cloud read.
2. The backlog: five ticks from this run (flm, ydw, r0t, sob, y9g) and
   eighteen from the failed first run. When the labelled commits land, r0t,
   5k6, u5q and ktd are done by them — the umq lesson says the merge gets
   written on the tick's notes in the same change.
3. 96s's unattributed `make gate` FAIL (one FAIL it could not attribute, never
   reproduced, deliberately filed as nothing): a worker asked for a human eye
   and nothing has answered.
4. The learnings destination (tick 3hw, open): this substrate cannot commit a
   `.tick/` path — re-verified from this container — so the compaction is
   durable in the retro doc and the apply is a paste.
5. The review bound is spent (2 of 2: a NOT READY and a READY); a third review
   is a person's, by the run's own rule.

## Learnings

Compacted into the retro doc's appendix, ready to apply verbatim into
`.tick/learnings.md` (148 lines, under the 150 cap): the
production-entry-point rule gains the DOOR clause (an acceptance item that
fires a fault names its door tick at planning — a deploy is not a door); the
planning-data rule gains ONE ITEM PER LINE (ex6's four marks on one line
parsed as one mega-item, blinding the done-evidence, worker-asserted-high
absorption and a review's `breaks` claim to A2–A4); a new rule for a finish
line spanning a writer→reader seam (done only when the CONSUMING side exists
and is exercised — m1w cited a file on no branch; a salvage with no agent
account is reviewed as a branch, never closed as done); the boundary rule
records the channel this run proved (a deliverable no worker can commit rides
as a `protected_change`; the run applies it as a labelled commit the PR
lists); and a new rule for the compose channel (contents before appends per
path, the composition tested end to end with the real payloads — tgx).
Every existing rule is carried over, with same-family entries merged to make
room; exactly one rule is dropped — the `redirect: "manual"` test gotcha, the
one entry that restates platform behaviour — named in the retro so the
operator can restore it if they disagree.

## The next feasible epic

Already on the record: **bo9** ("Operator surface follow-ups": ba4, g5f, u2g,
0ek, close-out 52h) — four planned ticks with `[A<n>]` items, labelled
`config:claude`, whose run would also be the still-owed live proof of v5t's
[A1]: a real `config: claude` cloud run on the subscription rung, which ex6's
config:glm run did not cover. After it, dha (Phase 5 — its premise predates
umq's cutover and needs a re-read) and t8u (test suite performance). ex6's own
leftovers are operator actions and single ticks, not epic material.

```findings v2
[
  {
    "kind": "defect",
    "title": "The run promotes one finding twice when two attempts report it",
    "severity": "low",
    "body": "q6z's attempts 19 and 20 of the failed first run reported the same defect — the dead --approve pairing surviving in runners.cloud.toml and runners.local.toml — and the run promoted both drafts, leaving two open backlog ticks (u5q, ktd) with one subject; 5k6 and r0t similarly record the 2p3/q6z duplication from both sides. The absorption path keys a promotion on the finding's key, and two attempts of one tick mint two keys for one subject, so the backlog grows duplicates nothing dedupes. The learnings already make a run grep .tick/issues/ before filing 'fails at base'; the same grep belongs at promotion, over open ticks' titles and evidence.",
    "evidence": ".tick/issues/u5q.json and .tick/issues/ktd.json (same subject, reporters run dispatches #19 and #20 of run_69f8f57832d34601b040367c7207b81f); .tick/issues/5k6.json beside .tick/issues/r0t.json; the two promoted drafts .ticfac/runs/run_69f8f57832d34601b040367c7207b81f/findings/bee7105c…json and …/4a590f4d…json"
  }
]
```

STATUS: DONE_WITH_CONCERNS — the epic's four items hold on the integration branch, both review rounds validated and every suite green at the head by this close-out's own runs; the concerns are [A2]'s live fault injection, which waits on the operator's 0mz/9if/lox; the five pending protected changes the run applies after these reads, for the merger to review on PR #265; and the learnings compaction, durable in docs/ex6-closeout-retro-2026-10-08.md and awaiting its paste (3hw)
