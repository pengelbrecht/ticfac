# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the wne and xte close-outs, 2026-09-23.

## Planning an epic

**Problem:** TWICE now (Phase 4, then xte) every component tick closed green and NOTHING drove them.
Phase 4: no route created the Workflow. xte: the door, the Go executor, the cloud profiles and
adoption all landed, but internal/cli never registered the executor and the container's
`run-epic` never named the cloud profiles — so a cloud run still dispatched local subprocesses.
Both epics ran on the old host, proving the old host. **Rule:** When an epic's gate is a run, the
FIRST tick wires the thinnest end-to-end path through the PRODUCTION entry point (tested through
the staged entrypoint, not a package), and a named tick PERFORMS the gate run before the review.
A partition of components with no wiring tick is refused at planning.
wne repeated the shape: an injectable classifier nothing constructs, a factory table nothing reads. A
seam is not delivery — a tick wires it, or the done says in words that production is out of scope.

**Problem:** xte's "a cloud run never routes claude" held per layer and failed in the whole: the
cloud overlay was checked, then a tier overlay applied after it (`balanced` → codex). **Rule:**
Check a policy on the FINAL resolved value, after every overlay, never on one input layer.

**Problem:** wne put the work-type enum (mrn) and the classifier that uses it (0ju) in one wave; 0ju
had to create the enum too, and mrn died on an add/add conflict. **Rule:** A tick that DECLARES a
vocabulary and one that CONSUMES it are different waves, even when neither lists the other's file.

**Problem:** A lost sandbox handle and a boot revoking every sibling's token were green: the fake keyed
on job_id and the tests stubbed boot. **Rule:** A fake must demand the identity the real thing
demands; a more forgiving fake certifies the defect it hides — fix it in the same change.

## Orchestration

**Problem:** Wave-2 agents branched from a base missing wave-1's merge. **Rule:** Name the prerequisite
SHA and verify with `git merge-base --is-ancestor`.

**Problem:** Two additions to one file were cut by two same-wave ticks. **Rule:** Two additions to one
file are a union in INTENT, not in text — hand the resolve to a worker holding the context. A
versioned artifact has ONE owner per wave.

**Problem:** A worker committed tracker state although its prompt forbade it. **Rule:** A boundary
the substrate can enforce must not rest on instruction-following — make it impossible and REPORT
every attempt.

**Problem:** Parallel ticks sharing a return shape were each green alone, broken together; an "in
flight" state outlived its dead writer. **Rule:** The merge gate is the only test of a shared contract.
Settle in-flight state from durable evidence by whoever finds it, never by trusting the claimer.

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

**Problem:** Host `rerere.enabled` replayed a person's old resolve into a machine merge. **Rule:** The
run states its own git environment (`-c rerere.enabled=false`); host config must not reach a run's merge.

## Waiting and watching

**Problem:** Blind `sleep 300` loops, and three hand-rolled watchers that were each wrong. **Rule:** Wait
on a CONDITION over durable evidence, a push stream, or a held PID's exit. A watcher covers every
terminal state — reported, blocked, died-without-reporting — or its silence means nothing.

**Problem:** A watcher piped through `tail` delivered nothing (block buffering); `pgrep -f` read ALIVE
after death by matching its own argv. **Rule:** No `tail`/`head` in a watcher pipeline; flush per line.
Never identify a process by a pattern the observer matches: hold the PID and check its start time.

**Problem:** The close-out opened PR #11 and in the same second refused it as "CI unsatisfiable by
waiting"; GitHub had not created the check runs yet. **Rule:** Absence right after creation is "not
yet". Bound a wait for the thing to APPEAR before calling it "never", and state what was checked
rather than a guessed cause.

## Where a tick lives

**Problem:** Ticks filed here to change the ticks repository were undispatchable, and xte's ha9
(pi in the ticks sandbox image) was dispatched SEVEN times: no ticfac worker could make it. **Rule:**
A tick goes in the tracker of the repo whose CODE it changes; another repo's change is an
`upstream-tick`, never a child of this epic. Cross-repo work is a second epic, `--repo <dir>`.

## Provider and model configuration

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated
GLM. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise. Change one variable, probe both ends.

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
wall-clock test measuring the host, not the tree. **Rule:** A gate verdict is about the tree only if
the host is bounded; record the host's conditions with the verdict.

**Problem:** wne's per-tick gates went green while 7 of the 10 tests exercising the change skipped
under `-short`; the review was the first to run them. **Rule:** A tick's evidence runs under the gate's
own flags. A test that skips there is not evidence — make it cheap, or name it in the acceptance.

## Fixtures

**Problem:** Two identical fixture commits in one second got one SHA. **Rule:** Make them differ on
purpose and assert the property you rely on.

**Problem:** fake-runner's blocked-first modes keyed on TICFAC_ATTEMPT — the RUN's counter — and
passed only because the first tick drew 1. **Rule:** "The tick's first try" keys on `$TICFAC_TRY`;
attempt numbers are identity (branch, marker, `ticfac settle`), never an ordinal.

## Verification ticks

**Problem:** Verification workers left `RESULT-<id>.md` uncommitted, so collect saw `no-commits`.
**Rule:** Evidence output is committed even with no source change; "settled" means "look now", not done.

## Tracker hygiene

**Problem:** `tk close` on a parked tick printed usage; the real error showed only with `--reason`.
**Rule:** A close that prints usage is a REFUSAL — re-run with `--reason done` to read it. A tick
parked with `tk ask` needs `--from human` once answered. Heredoc tick text; `git commit .tick/`.

**Problem:** Phase 4's triage promoted ~20 findings into ticks that existed only as untracked files in
the operator's checkout, so the run branch's `promoted_as` ids resolved in no committed tracker.
**Rule:** A promotion is finished when the tick is COMMITTED where the next run reads it.

**Problem:** A spawn blamed "the probe" while the account sat at 93% of its session limit. **Rule:**
Before believing a spawn's diagnosis, send text to the pane by hand.
