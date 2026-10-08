<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-9/52h`, base `0687b0c41f0a2da7a216795b182ffaef83104364`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# 52h — close out bo9: operator surface follow-ups

The retro record is committed at `docs/bo9-closeout-retro-2026-10-08.md`, following the
convention the v5t, umq and ilz close-outs set. This report is the summary and the findings
channel; the document is the record.

Sources: the integration branch `epic/bo9`, the run's records under
`.ticfac/runs/run_03aa9c8a71574ab3891b47df3dbd5ec9/` (8 attempts, 9 decisions, 15 absorptions, 12
gate evidence records), and the ticks' own `RESULT-*.md` reports on their attempt branches. Every
acceptance item was re-checked against the tree at this close-out rather than taken from a tick's
account.

## Delivered against the definition of done

| item | verdict | how it was checked here |
|---|---|---|
| A1 `run --cloud --config` | **built and tested hop by hop; never run** | every hop present (`run_cloud.go:214` → `runs.ts` → `sandbox.ts` → `common.sh:162` → `ticfacentrypoint.go:343`); `TestRunCloudForwardsTheNamedConfig`, `TestOverrideForwardsTheNamedRunConfig`, `TestOverrideLeavesOffTheConfigFlagWhenUnset` pass. No tick ran the command — **ihr** |
| A2 `skills diff` | **met** | built the binary, installed into a scratch repo, set the stamp to `0.0.1`: `skills diff ticfac` → "installed=0.0.1 bundle=dev", "upgrade with `ticfac skills install ticfac`", exit 1 |
| A3 `skills get --full` | **met** | printed SKILL.md then `references/cloud.md`, `commands.md`, `holds.md`, each under a `--- <path> ---` header |
| A4 embedded harness | **met** | this container has no `harness/node_modules`, yet `TestTheDurableRunnerRunsOnTheEmbeddedBundleWithNoHarnessCheckout` passes (real supervisor, real Node, argv `node <cache>/harness/…`). Breaking `harness/src/local/main.ts` made `TestLocalHarnessBundleMatchesItsSources` fail naming `make harness-bundle` |
| A5 gate + CI | **met** | `make gate` exits 0 here. CI push run `37771454557` on `7287ad5` — the last code-bearing commit — is `success` on every job. The remainder of the branch is code-free |

A4's end-to-end half is `shorttest.EndToEnd`, so `make gate` does not run it; CI's `go-test` job
does, because it installs Node 22 (`ci.yml:209-213`). The staleness test is pure Go and does run
in the gate.

No exception to any acceptance item was claimed on bo9's record: the epic carries no notes and
the run has no `amendments/` directory, so there is nothing unconfirmed.

## What the run absorbed

Fifteen findings, **none into the epic**. Fourteen `backlog-default` (all `low` or `medium`; only
ihr named a done item, A1), and **ifw** by the live-run rule (`basis: rule`, placement
`next-run`). All fifteen are parentless, open backlog ticks: w75, 50j, avo, jtj, rgx, 359 (g2s);
ihr, 5p5, hdb, dd4, eus (52h attempt 7); 656, ifw, k2n, 8qr (two). Nothing was deferred to a
person; nothing was discarded. Neither reviewer named a blocking finding, so the epic's shape
never changed — the acceptance above is scored against the original five items.

## The learnings

The bo9 compaction of `.tick/learnings.md` is already filed and still pending: finding
`18505c55` from attempt 7 carries the whole new 150-line file as a `protected_change`, and
`applyProtectedChanges` reads every `proposed` finding regardless of which attempt proposed it,
so attempt 7's rejection did not orphan it. It is deliberately not re-proposed — `PutFinding`
dedupes on (kind, title, target) and the original stands, so a re-proposal is a no-op, and one
under a different title would be a second whole-file replacement of the same path resolved by
sha256 key order. The two lessons that post-date it (fold the base before the final review; a
close-out commits its retro document) are recorded in the retro's "The learnings" section for the
next compaction, and the second is filed below.

## Why attempt 7 was rejected, and what changed

Attempt 7 reported its retro in prose and proposed the learnings as a `protected_change`, and
committed nothing. The run rejected it `no-commits`: for `closeout-epic`,
`NoCommitsIsFailure` is true, and the waiver that would have covered a protected-change-only
delivery is gated to `implement-tick`. This attempt commits the retro document, which is the
committable deliverable the role has always had and its prompt never names.

## What is left open

The highest-consequence item is **656**: `deploy-factory.yml`'s `paths` list does not include
`harness/embed`, the embed root 0ek added, so a bundle-only commit on main is decided "nothing
that ships changed" and skipped, leaving orchestrator containers on the old bundle — and the
staleness gate hashes `harness/src`, not the bundle, so it cannot catch it either. Then the live
proof of A1 (**ihr**, with **w75** and **50j**, and **ifw** already routed to the next run); the
skills family's remaining seams (**5p5**, **avo**, **k2n**, **8qr**, **jtj**, **hdb**); **rgx**;
and two findings about the factory's own machinery (**dd4**, **eus**).

```findings v2
[
  {
    "kind": "defect",
    "title": "A close-out that follows its prompt commits nothing and is rejected",
    "severity": "medium",
    "body": "closeout-epic's recorded commit rule says an empty branch is an undelivered deliverable because its deliverable is 'the retro, and the learnings the boundary permits it to write'. But on the cloud substrate the boundary refuses every .tick/ path, so the learnings can only travel as a finding's protected_change — and acceptProtectedDelivery, the waiver that makes a protected-change-only delivery count, returns early for every role that is not implement-tick. The role's prompt compounds it: it names no committable artefact and says 'Your report is the only channel that is read'. bo9's attempt 7 did exactly that and was rejected no-commits, costing a full opus close-out and a redispatch. The committable deliverable the role actually has is docs/<epic>-closeout-retro-<date>.md, the convention v5t, umq and ilz already follow; either the prompt should name it or the waiver should cover closeout-epic.",
    "evidence": "internal/reconcile/protected_changes.go:414 (the implement-tick gate) against internal/exec/subprocess/rolecommits.go:33-35; profiles/closeout-epic.md's closing line; checkpoint 47 of run_03aa9c8a71574ab3891b47df3dbd5ec9 records the rejection"
  },
  {
    "kind": "defect",
    "title": "Two protected changes to one path resolve by sha256 key order",
    "severity": "low",
    "body": "applyProtectedChanges walks the run's findings in findingKeys() order, which is sort.Strings over sha256 hex, and applies each proposal in turn. Two findings proposing `content` for the same path are therefore both committed and the lexicographically later key silently wins — the final text of a protected file decided by a hash. The dedup closes the other door rather than this one: findingKey hashes (kind, title, target) and not the body, so a later attempt re-proposing the same subject is refused with 'the ORIGINAL stands and nothing new is proposed', and cannot correct an earlier attempt's text. Hit while closing bo9: attempt 7's learnings proposal could neither be superseded nor safely competed with, so the compaction stands as attempt 7 wrote it and two lessons had to be left for the next one. Applying at most one proposal per path, newest-wins, and recording the ones it supersedes would fix both halves.",
    "evidence": "internal/reconcile/protected_changes.go:153-203 with internal/runstate/findings_store.go:214-227 (sort.Strings) and internal/reconcile/findings.go:60-63 (the key) and :185-210 (the dedup)"
  },
  {
    "kind": "proposal",
    "title": "Fold the epic's base before the final review, not after it",
    "severity": "low",
    "body": "g2s judged bo9 READY at ed3f1d2f; the run then folded main into epic/bo9 as 7287ad5, which changed the tree the verdict was about and brought in a file the epic's own diff touches. That correctly invalidated the verdict, so the run minted tick `two` and paid for a second full opus epic review to reach the same READY. Folding the base before dispatching the final review — or re-checking whether a fold is pending before taking a verdict — would have saved a whole review. The conservatism is right; the ordering is what costs.",
    "evidence": "decision 8 (source_sha ed3f1d2fc915) then the merge 7287ad5fe6f3 'Merge branch main into epic/bo9', then .tick/issues/two.json's own description, then decision 9"
  }
]
```

STATUS: DONE
