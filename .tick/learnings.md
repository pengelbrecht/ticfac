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

## Waiting

**Problem:** A worker ran `sleep 300` in a loop — fifteen minutes of blind waiting for runs that had
finished in seconds — and the orchestrator looked stalled repeatedly for the same reason. **Rule:**
Never wait on a blind sleep. Wait on a CONDITION: an `until` loop over the durable evidence, a push
stream where the substrate has one (`tk herd wait`), or a held PID's exit status.

**Problem:** Three hand-rolled watchers were each wrong — one keyed on a log file being non-empty and
fired on a warning line, one matched a process name the agent does not carry, one expired at its own
timeout while the work ran on. **Rule:** A watcher must cover every terminal state, not just success:
reported, blocked, and died-without-reporting are three different answers, and silence from a
success-only watcher is indistinguishable from still working.

**Problem:** Two runs died with `conflict_exists` on a checkpoint write, and the story that a competing
writer had advanced the run did not survive the commit timeline. The real cause: the run-state store
resolves origin's head through `git rev-parse FETCH_HEAD`, and `FETCH_HEAD` is ONE FILE in `.git`
shared by every process using that checkout. A watcher running `git fetch` in the run's own repository
overwrote it with main's head; `ls-tree` of main has no `.ticfac` tree, so the store's view of its own
state came back EMPTY, it took the create path instead of update, and the push was refused. Reproduced
in a scratch repo in four commands. **Rule:** Never resolve a ref through `FETCH_HEAD` or any other
process-global git state. Fetch into a private per-run ref, read that, and pass BOTH
`--no-write-fetch-head` and `--refmap=` — fetching from a named remote also rewrites
`refs/remotes/<remote>/<branch>`, and an operator's `git fetch origin` racing it on that ref's lock
ended a run on the first fix. A watcher must not be able to corrupt the thing it watches.

**Problem:** The same `FETCH_HEAD` read appears a second time in the tracker publisher, where `sync()`
MOVES THE WORKTREE to the resolved commit. **Rule:** When a defect comes from a shape rather than a
line — process-global state, an unbounded retry, a swallowed error — grep for the shape before calling
it fixed, and leave a guard test that fails on the next occurrence. Then prove the fix with a test
that REPRODUCES the original failure on the old code: the concurrency test written to confirm the
FETCH_HEAD fix is what found the second shared ref, and it only reproduced once it matched
production (checkout on main, a second writer moving the ref). The crash is the benign symptom;
the same read one layer over lands writes on the wrong base.

**Problem:** A hand-rolled watcher ran for eleven minutes and delivered nothing, because it was piped
through `tail` to suppress a replay of history. `tail` and `head` block-buffer when stdout is a pipe,
so the whole stream accumulated and never arrived; the run meanwhile collected, filed a finding and
merged, all unreported. **Rule:** Never put `tail`, `head`, or any block-buffering stage in a watcher's
pipeline. Filter inside the script and flush per line. If a watcher must not replay history, seed its
cursor from the feed at startup rather than trimming its own output.

**Problem:** A liveness check of `pgrep -f "ticfac run-epic"` reported ALIVE for ten minutes after the
run had died, because the monitoring command's own argv contained that string. **Rule:** Never identify
a process by a pattern the observer itself matches. Hold the PID, write it down, and check that PID —
and verify start time too, so a recycled PID cannot read as alive.

**Problem:** `tk herd wait` reported three agents settled while two had no commits and no report.
**Rule:** A settled agent means "worth looking now", never "the work is finished". Completion is the
commits on `tick/<id>` plus `RESULT-<id>.md`, whatever any signal says.

**Problem:** The reconciler's 5-minute poll looks lazy and is not — it sits under a 20-minute wipe
threshold so the poll IS the keepalive. **Rule:** Before speeding up a slow interval, find out what it
is holding open. A cadence right for a cloud substrate is wrong for a local run, so the interval
belongs to the executor, not to one constant.

## Where a tick lives

**Problem:** Two ticks were filed in ticfac's tracker to delete code from the ticks repository, because
they belonged to the extraction epic. Neither could be dispatched — `tk herd spawn` always creates a
worktree of the repo whose tracker holds the tick, so their branches were uncollectable and ungated —
and a worker burned a dollar diagnosing it. **Rule:** A tick goes in the tracker of the repo whose
CODE it changes, never the tracker of the epic it belongs to. Cross-repo work is a second epic, planned
there, and `ticfac run-epic --repo <dir>` runs it against that checkout.

## Provider and model configuration

**Problem:** A max-output override of 1,000,000 made Cloudflare answer a bodyless 400, which pi read as
"context overflow" and tried to recover from by summarising — which hit the same ceiling and killed the
worker. **Rule:** A bodyless 4xx from a model provider is a REQUEST-SHAPE problem until proven
otherwise. Change one variable at a time and probe the ceiling; the error text will not tell you.

**Problem:** pi detects non-standard reasoning providers by provider name or baseUrl, and a GLM model
served through `cloudflare-workers-ai` matches none of them, so its `<think>` tags leaked into content
and the model looped. **Rule:** A model served through a provider other than its vendor's own endpoint
loses that detection. Set `compat.thinkingFormat` explicitly for it.

**Problem:** Correcting the 1,000,000 output cap to 8,192 then truncated a worker mid-answer, because
GLM at thinking=high generates millions of reasoning tokens. **Rule:** Probe both ends. A cap that is
too low fails as silently as one that is too high.

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

**Problem:** fake-runner's blocked-first modes gated on TICFAC_ATTEMPT = 1 — the RUN's dispatch
counter, not the tick's — so every test passed only because the first tick always drew number 1;
the false defect report from the nvn worker about its own feature was the same numbering read
wrong (tick vw0). **Rule:** A fixture that means "the tick's first try" keys on $TICFAC_TRY, the
per-tick number the harness derives and passes — attempt numbers are identity (branch, marker,
`ticfac settle`), never an ordinal.

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
