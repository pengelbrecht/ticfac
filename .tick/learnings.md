# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' `.tick/learnings.md` (Orchestration and Verification ticks) on 2026-09-02.

## Orchestration

**Problem:** Wave-2 agents branched from a base missing wave-1's merged commit and redid its work.
**Rule:** Name the prerequisite SHA, instruct `git merge <integration-branch>`, then verify with
`git merge-base --is-ancestor`.

**Problem:** Two additions to one file (a versioned bundle, a changelog) were cut by two same-wave
ticks. **Rule:** Two additions to one file are a union in INTENT, not in text — hand the resolve to a
worker holding the context. A versioned artifact has ONE owner per wave.

**Problem:** A worker committed tracker state although its prompt forbade it. **Rule:** A boundary
the substrate can enforce must not rest on instruction-following — make it impossible and REPORT
every attempt.

**Problem:** One parallel tick changed a shared return shape and broke another's file; both green
alone. **Rule:** When parallel ticks share a contract, the merge gate is the only thing that tests it.

**Problem:** A state meaning "in flight" was left forever when its writer died. **Rule:** Settle it
from durable evidence (does the thing exist?) by whoever finds it next, never by trusting the
claimer to return.

**Problem:** A bare `go test ./...` died at the default 10-minute PER-PACKAGE timeout because
`internal/reconcile` alone takes ~600-620s even under `-short`. **Rule:** Run tests through the
Makefile (`make test-short` / `make test`), which pins `GOTEST_TIMEOUT := 45m` exactly for this.

## Reviews and repairs

**Problem:** A review called something a blocker, a repair was built on it, and the repair introduced
a real regression — the premise ("integrate never marks the tick rejected") was true of the function
and false of the run that calls it. **Rule:** Before repairing a reported defect, REPRODUCE it at the
base commit. A worktree at the base plus the new test costs minutes and settles it.

**Problem:** A regression test "failed before the fix", so the fix looked proven — but it failed only
on a stage-record assertion while every behavioural assertion passed unfixed. **Rule:** Read WHICH
assertion fails at the base. One failing line is not evidence the behaviour changed.

**Problem:** A reviewer reported "no RESULT file was written" for three ticks that all wrote one;
cleanup had archived them to `.tick/logs/herd/<epic>/<id>.RESULT.md` and the worktrees were gone.
**Rule:** Point a reviewer at the ARCHIVE, not the worktree, for any tick already cleaned up.

**Problem:** A fix keyed gate evidence by commit; the run writes its own `.ticfac/` records to the
branch it gates, so the key was wrong in both directions at once — reused across a real change, and
re-minted on every resume. **Rule:** When a run writes to the artifact it measures, key evidence by
the SOURCE (tree minus the run's own path), never by the commit.

**Problem:** A rekeyed evidence key was compared only against the record at the plain key, so each
resume minted another key. **Rule:** A derived key must be a function of the thing it identifies, not
of the history that produced it — or it chains.

## Fixtures

**Problem:** A test built two commits with the same parent, content, message and author and asserted
they differed; inside one second git gave them one SHA. **Rule:** Make fixture commits differ by
something intentional (the message) and assert the property you rely on (same tree, different commit).

## Verification ticks

**Problem:** Workers finishing a verification tick left a clean tree and did NOT commit
`RESULT-<id>.md`; collect reads the branch, so it reported `no-commits`. **Rule:** A tick whose
output is evidence must say "commit `RESULT-<id>.md` even when no source changed". A pane saying
`done` means idle at a prompt, not finished.

## Naming and tracker hygiene

**Problem:** Backticks in double-quoted `tk create -d "..."` were shell-substituted. **Rule:**
Single-quote or heredoc tick text containing backticks, `$`, `()` or `<>`.

**Problem:** `git add .tick/ && git commit` captured foreign staged files. **Rule:** `git add
.tick/ && git commit .tick/`. Confirm `MERGE_HEAD` is empty after any merge.

**Problem:** `tk close` on a tick left `awaiting: input` printed usage text and exited non-zero; the
real error ("a verdict is a human's decision") appeared only with a short `--reason`. **Rule:** A
close that prints usage is a REFUSAL, not a bad flag. Re-run with `--reason done` to read it. A tick
parked with `tk ask` needs `--from human` once the human has answered.

**Problem:** `tk herd spawn` reported "the probe never reached the composer" while the composer was
fine; the account was at 93% of its session limit. **Rule:** Before believing a spawn's diagnosis,
send text to the pane by hand. A capacity failure and a broken pane read identically.
