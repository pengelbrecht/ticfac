# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the Phase 4 (ncv) close-out, 2026-09-19.

## Planning an epic

**Problem:** Phase 4 delivered every component of the Cloudflare host — the move, the Workflow
reconciler, run records out of D1, the serialized publisher, the sandbox executor — each tick green
against its own acceptance, and NOTHING drove them: no route created the Workflow, so the gate run
could not be performed, and the review was the first to notice. The epic itself ran on local herdr,
so its sixteen resumes proved the Go reconciler, not the host it was building. **Rule:** When an
epic's gate is a run, the FIRST tick wires the thinnest end-to-end path on the target host, and a
named tick PERFORMS the gate run before the review. Evidence from the old host is about the old host.

**Problem:** A reconciler that never persisted its sandbox handle, and an executor whose boot revoked
every sibling's token, were both green: FakeExecutor keyed on job_id, and the tests stubbed boot.
**Rule:** A fake must demand the identity the real thing demands. A fake more forgiving than production
certifies the defect it hides — fix the fake in the same change as the bug.

## Orchestration

**Problem:** Wave-2 agents branched from a base missing wave-1's merge and redid its work. **Rule:**
Name the prerequisite SHA and verify with `git merge-base --is-ancestor`.

**Problem:** Two additions to one file were cut by two same-wave ticks. **Rule:** Two additions to one
file are a union in INTENT, not in text — hand the resolve to a worker holding the context. A
versioned artifact has ONE owner per wave.

**Problem:** A worker committed tracker state although its prompt forbade it. **Rule:** A boundary
the substrate can enforce must not rest on instruction-following — make it impossible and REPORT
every attempt.

**Problem:** Two parallel ticks sharing a return shape were each green alone and broken together.
**Rule:** When parallel ticks share a contract, the merge gate is the only thing that tests it.

**Problem:** A state meaning "in flight" was left forever when its writer died. **Rule:** Settle it
from durable evidence (does the thing exist?) by whoever finds it next, never by trusting the
claimer to return.

**Problem:** epic-ncv stopped EIGHT times for nothing but untriaged findings, and after each triage the
resume re-dispatched the review instead of closing it — three frontier reviews of byte-identical
source, a loop that ends only when a reviewer finds nothing. **Rule:** A hold that fires when the
system does its job (finding things) makes "unattended" impossible by construction; put it where a
person already is (the PR). A resume replays a recorded decision, it never buys it again.

**Problem:** A bare `go test ./...` died at the 10-minute per-package timeout (`internal/reconcile`
is ~600s under `-short`). **Rule:** Test through the Makefile, which pins `GOTEST_TIMEOUT := 45m`.

## Git state the run does not own

**Problem:** Two runs died with `conflict_exists`: the run-state store resolved origin through
`FETCH_HEAD`, ONE file shared by every process on that checkout, and a watcher's `git fetch` rewrote
it; the same read reappeared in the tracker publisher, which MOVES the worktree to it. **Rule:** Never
resolve a ref through process-global git state. Fetch into a private per-run ref with
`--no-write-fetch-head --refmap=`. When a defect is a SHAPE (global state, unbounded retry, swallowed
error), grep for the shape, leave a guard test, and prove the fix by reproducing on the old code.

**Problem:** A run merge was refused with rerere's `Recorded preimage` chatter: the host's global
`rerere.enabled` shares `.git/rr-cache` with a person's hand resolutions, so a human's old resolve can
silently replay into a machine merge. **Rule:** The run states its own git environment
(`-c rerere.enabled=false`, like `GIT_TERMINAL_PROMPT=0`); host config must not reach a run's merge.

## Waiting and watching

**Problem:** Blind `sleep 300` loops, and three hand-rolled watchers that were each wrong (fired on a
warning line, matched a name the agent lacks, expired while work ran on). **Rule:** Wait on a
CONDITION over durable evidence, a push stream, or a held PID's exit. A watcher covers every terminal
state — reported, blocked, died-without-reporting — or its silence means nothing.

**Problem:** A watcher piped through `tail` delivered nothing for eleven minutes (block buffering);
a `pgrep -f "ticfac run-epic"` read ALIVE for ten minutes after death because it matched its own argv.
**Rule:** No `tail`/`head` in a watcher pipeline — filter in-script, flush per line. Never identify a
process by a pattern the observer matches: hold the PID and check its start time.

**Problem:** The close-out opened PR #11 and in the same second refused it as "CI unsatisfiable by
waiting"; GitHub had not created the check runs yet. **Rule:** Absence right after creation is "not
yet". Bound a wait for the thing to APPEAR before calling it "never", and state what was checked
rather than a guessed cause.

**Problem:** The 5-minute poll looks lazy; under a 20-minute wipe threshold it IS the keepalive.
**Rule:** Find what a slow interval holds open before speeding it up; it belongs to the executor.

## Where a tick lives

**Problem:** Ticks filed in ticfac's tracker to change the ticks repository were undispatchable —
`tk herd spawn` worktrees the repo whose tracker holds the tick. **Rule:** A tick goes in the tracker
of the repo whose CODE it changes. Cross-repo work is a second epic, run with `--repo <dir>`.

## Provider and model configuration

**Problem:** A 1,000,000 max-output made Cloudflare answer a bodyless 400 that pi read as context
overflow; 8,192 then truncated GLM mid-answer (it reasons in millions of tokens at thinking=high).
**Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise. Change one variable, probe both ends.

**Problem:** GLM served through `cloudflare-workers-ai` leaked `<think>` tags and looped: pi detects
reasoning providers by name or baseUrl. **Rule:** A model served off its vendor's endpoint loses that
detection. Set `compat.thinkingFormat` explicitly.

## Reviews and repairs

**Problem:** A review's "blocker" was repaired and the repair regressed (the premise was true of the
function, false of the run calling it); a regression test "failed before the fix" only on a
stage-record assertion. **Rule:** REPRODUCE a reported defect at the base, and read WHICH assertion
fails there.

**Problem:** A review said NOT READY and the run recorded `verdict: ready-to-merge` beside it
(decisions/1.json) — the schema had no field for the review's judgement. **Rule:** A role whose
answer is a judgement needs that judgement as a typed field with an effect; a status line that means
"I finished" must never be the only thing a verdict can ride on.

**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the branch
it gates, and a rekeyed key compared only to the plain key chained on every resume. **Rule:** When a
run writes to what it measures, key evidence by the SOURCE (tree minus the run's own path), and make a
derived key a function of the thing it identifies, not of the history that produced it.

**Problem:** Across 9pd and ncv the integrated gate refused innocent work six times, each through a
wall-clock test measuring the host, not the tree. **Rule:** A gate verdict is about the tree only if the host is
bounded; record the host's conditions with the verdict, and never let a gate share a host unbounded.

## Fixtures

**Problem:** Two identical fixture commits inside one second got one SHA. **Rule:** Make them differ
by something intentional and assert the property you rely on.

**Problem:** fake-runner's blocked-first modes keyed on TICFAC_ATTEMPT — the RUN's counter — and
passed only because the first tick drew 1. **Rule:** "The tick's first try" keys on `$TICFAC_TRY`;
attempt numbers are identity (branch, marker, `ticfac settle`), never an ordinal.

## Verification ticks

**Problem:** Verification workers left `RESULT-<id>.md` uncommitted, so collect saw `no-commits`, and
`tk herd wait` called agents settled that had neither. **Rule:** A tick whose output is evidence
commits its RESULT even with no source change; "settled" or `done` means "look now", not finished.

## Tracker hygiene

**Problem:** Backticks in `tk create -d "..."` were shell-substituted; `git add .tick/ && git commit`
captured foreign staged files. **Rule:** Heredoc tick text; `git commit .tick/`; check `MERGE_HEAD`.

**Problem:** `tk close` on a parked tick printed usage; the real error showed only with `--reason`.
**Rule:** A close that prints usage is a REFUSAL — re-run with `--reason done` to read it. A tick
parked with `tk ask` needs `--from human` once answered.

**Problem:** Phase 4's triage promoted ~20 findings into ticks that existed only as untracked files in
the operator's checkout, so the run branch's `promoted_as` ids resolved in no committed tracker.
**Rule:** A promotion is finished when the tick is COMMITTED where the next run reads it.

**Problem:** A spawn reported "the probe never reached the composer" while the account was at 93% of
its session limit. **Rule:** Before believing a spawn's diagnosis, send text to the pane by hand.
