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
