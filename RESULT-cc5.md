# ticfac Phase 1 gate run (tick cc5)

This report is the evidence that closes ticks-tracker tick **421** (final review of ticks epic 4ik, "ticfac Phase 1"): one real epic in the ticks repository was run through `ticfac run-epic` with a claude runner, killed and restarted from a fresh clone after dispatch, after collection and before closure, with no duplicate attempt and no false close. Operator approval for the target epic and the live pushes: 2026-09-08 (ticfac tick cc5, note and answer).

## What was run

| | |
|---|---|
| Target | ticks epic `cia` "Guard and frontier hygiene": uhw, uqe (wave 1), t62 (wave 2), brp (review job), glo (close-out job) |
| Runner | claude, routed by ticks' `.tick/runners.toml` `[roles.implement]` (sonnet); review-epic profile resolved to opus |
| Reconciler binary | `ticfac` + `ticfac-exec-subprocess` built from `epic/qeu` at 2ea1c22 (with the cgx fix, see below); the first attempt used 19995af |
| Run record | `.ticfac/runs/gate-cia-2/` on `origin/epic/cia` in ticks: checkpoint (43 sequences), attempts 1–5, evidence gate-{uhw-1,uqe-2,t62-3}-{go,pi-runner}, decisions 1–2 (validated role-result envelopes) |
| Tag | `ticfac/run-gate-cia-2` → 69160c256fb4212fc1dd263f9774c167df718fa4 on the ticks origin |
| PR | https://github.com/pengelbrecht/ticks/pull/80 (epic/cia → main), opened by the run operator — the Phase 1 reconciler does not open PRs |
| Cost | not metered (subscription seat); wall clock per attempt below |

## The three cuts (run gate-cia-2, fixed binary)

Each restart was a **fresh `git clone`** of the ticks origin; the executor's state root and the attempt worktrees (linked worktrees of the clone the attempt was dispatched from) stayed on the host, as the design says they must.

| Cut | When | Evidence | Restart adopted |
|---|---|---|---|
| after dispatch | 09:56:29Z, checkpoint seq 3: uhw `dispatched`, attempt 1 | `attempts/1.json` on origin; `.tick/issues/uhw.json` on `origin/epic/cia` already `in_progress` (the claim is durable before the job starts) | run 6 adopted attempt 1 by identity; no attempt 2 for uhw |
| after collection | 10:02:32Z, seq 7: uhw `integrated`, state `gating` | still exactly one attempt marker | run 7 re-ran the integrated gate (`go`, `pi-runner`) and recorded evidence |
| before closure | 10:09:02Z, inside `tk close uhw` (SIGKILL from a tk wrapper), seq 12 state `publishing` | evidence on origin, tracker still `in_progress` — no false close | run 8 closed uhw once, then ran uqe, t62, review, close-out to completion (11:04:33Z, seq 43, `completed`) |

Attempts on origin: 1 uhw, 2 uqe, 3 t62, 4 brp (review-epic, read-only source grade, no push credential issued), 5 glo (closeout-epic). Five ticks, five attempts, zero duplicates. Runner wall clock: uhw 1m48s, uqe 9m05s, t62 14m17s, brp 8m58s, glo 2m38s. The integration branch carries 72 `ticfac run` commits (run-state records and tracker writes) plus the ticks' own source commits.

Review job verdict (decision 1): DONE_WITH_CONCERNS with two findings on the guard scoping — filed in the ticks tracker as m9j (F1) and l5w (F2). Close-out job verdict (decision 2): DONE_WITH_CONCERNS, asking that the PR be opened and the findings filed; both done by the operator side of this run.

## The first attempt (run gate-cia) and what it found

The first run, on binaries from 19995af, hit the same three cuts on uhw cleanly (08:45–08:54Z) and then **failed at t62** in the no-cut pass: the t62 worker read its blockers uhw and uqe as `status: open` and answered BLOCKED. Root cause: the reconciler's tracker writes (claim, note, close through `tk`) were left as uncommitted edits in its checkout on `main` and never reached origin, so a fresh clone and a wave-2 worktree could not see them. This is exactly the class of defect the gate exists to catch. Fixed as ticfac tick **cgx** (tracker writes run in a detached worktree of the integration branch and are committed and pushed with force-with-lease after every write; wave N+1 branches from the integrated tree; a rejected attempt that left no commits is redispatched). The failed run's branches are archived as tags `ticfac/archive/gate-cia-failed/*` on the ticks origin; the epic was reset and rerun as `gate-cia-2` above.

Four more defects came out of the run and were fixed in this epic before the final review: the shipped binary could not locate the tk JSON manifest outside the ticfac checkout (u65, embedded manifest); a worker committed its RESULT artifact into the attempt branch (wtd, the artifact prefix is now excluded in the attempt worktree; `runs/gate-cia-2/uqe/RESULT-uqe.md` remains on the ticks PR from before the fix); durable records carried host-absolute paths (0x1, `report_path` is worktree-relative and the dispatch marker no longer round-trips the state root); and a new run read its tracker from the integration branch, where ticks created on main after the fork did not exist (k0d, the reconciler now folds the base branch into the integration branch at run start with tk's merge drivers — the follow-up run `gate-cia-3` that fixed the ticks CI failures ran with that fold done by hand).

## Timeline (UTC, from the operator's harness log)

```
2026-09-08T08:44:27Z run1: start (fresh clone1 @ 1da981d5)
2026-09-08T08:44:30Z cut1-after-dispatch: reconciler 80464 exited before the cut condition
2026-09-08T08:44:46Z cut1-after-dispatch: reconciler 80914 exited before the cut condition
2026-09-08T08:45:21Z run1: relaunch with cwd=ticfac checkout (workaround: tk client locates the manifest from the repo root instead of the embedded copy)
2026-09-08T08:45:44Z cut1-after-dispatch: checkpoint seq 3 shows uhw=dispatched — SIGKILL reconciler 82516
2026-09-08T08:48:32Z run2: start from fresh clone2 @ 1da981d5 (expect: adopt uhw attempt 1)
2026-09-08T08:49:00Z cut2-after-collection: checkpoint seq 7 shows uhw=integrated — SIGKILL reconciler 90587
2026-09-08T08:49:20Z run3: start from fresh clone3 @ 1da981d5 with cut 3 armed (kill before 'tk close')
2026-09-08T08:54:09Z cut3: killing reconciler pid 95410 before 'tk close uhw'
2026-09-08T08:54:34Z run4: start from fresh clone4 @ 1da981d5, no cuts — run to completion
2026-09-08T09:55:12Z reset: run gate-cia (failed at t62 — tracker writes not durable, fixed by ticfac tick cgx) archived as tags ticfac/archive/gate-cia-failed/*; branches epic/cia, tick/{uhw,uqe,t62} deleted on origin for a clean rerun
2026-09-08T09:55:53Z run5: fresh clone5 @ 1da981d5, binaries from ticfac 2ea1c22 (with cgx tracker-durability fix), NEW run-id gate-cia-2, cut 1 armed (after dispatch)
2026-09-08T09:56:29Z cut1-after-dispatch: checkpoint seq 3 shows uhw=dispatched — SIGKILL reconciler 26764
2026-09-08T09:56:56Z run6: fresh clone6, cut 2 armed (after collection)
2026-09-08T10:02:32Z cut2-after-collection: checkpoint seq 7 shows uhw=integrated — SIGKILL reconciler 38879
2026-09-08T10:02:51Z run7: fresh clone7, cut 3 armed (kill before 'tk close')
2026-09-08T10:09:02Z cut3: killing reconciler pid 97015 before 'tk close uhw'
2026-09-08T10:09:17Z run8: fresh clone8, no cuts — run to completion
```

## What the run needs from an operator today

- `cwd` was the ticfac checkout for the first run (manifest lookup; fixed by u65).
- The PR from the integration branch to main is opened by the operator, not the reconciler.
- The epic tick itself (`cia`) stays open: the close-out job closes its own tick; closing the epic belongs to the PR merge / operator.
