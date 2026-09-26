# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the gvc close-out, 2026-09-26.

## Planning an epic

**Problem:** THREE epics closed without the run their acceptance names. Phase 4: no route created the
Workflow. xte: internal/cli never registered the new executor. yoh: every tick green against fakes,
the live run deferred to dha's u9h, and the gap only a live run shows (worker.sh never reads
TICKS_ROLE_PROMPT) surfaced as an upstream finding. **Rule:** When an epic's gate is a run, the FIRST
tick wires the thinnest end-to-end path through the PRODUCTION entry point, and a named tick INSIDE
the epic performs the run before the review. If the run lives elsewhere, the acceptance says so.

**Problem:** gvc, the epic about absorbing findings, absorbed none of its 21. Its run was driven by a
ticfac binary built before any of its absorption code merged, and its own acceptance was never
marked into the [A<n>] items that code reads (klq's marking was a tracker write no worker may make).
**Cause:** an epic that changes the orchestrator is run BY the old orchestrator, and the DATA its
machinery reads was left to a tick's acceptance. **Rule:** Such an epic's done names the NEXT run on
the rebuilt binary as its demonstration; data the machinery reads (marked acceptance, bindings)
lands as a planning step before wave 1, never as a tick's acceptance.

**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a
tier overlay replaced the model; yoh keyed the Workers-AI rule on the substrate while the
cloudflare-sandbox executor also ran under the local one (78v). **Rule:** Check a policy on the
FINAL resolved value, keyed on what actually crosses the boundary (the executor), never on one input.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; mrn died on add/add.
**Rule:** A tick that DECLARES a vocabulary and one that CONSUMES it are different waves.

**Problem:** l6t deleted the wave path, and with it four things it alone produced: the worker's
harness bound, streamed logs, per-tick board events, and cron sweeps. Each was found later, one
finding at a time (9iz, 925). **Rule:** A deletion tick first LISTS every effect the deleted path
produced (events, bounds, logs, schedules), then names each one's new owner or records it as
dropped. A deletion's acceptance is that list, not "tests still pass".

## Orchestration

**Problem:** Wave-2 branched from a base missing wave-1. **Rule:** Name the SHA; check `--is-ancestor`.

**Problem:** Two additions to one file were cut by two same-wave ticks. **Rule:** Two additions to one
file are a union in INTENT, not in text — hand the resolve to a worker holding the context. A
versioned artifact has ONE owner per wave.

**Problem:** A worker committed tracker state although its prompt forbade it. **Rule:** A boundary
the substrate can enforce must not rest on instruction-following — make it impossible and REPORT
every attempt.

**Problem:** Parallel ticks sharing a return shape were each green alone, broken together; an "in
flight" state outlived its dead writer. **Rule:** The merge gate is the only test of a shared contract.
Settle in-flight state from durable evidence by whoever finds it, never by trusting the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**
A hold that fires when the system does its job (finding things) makes "unattended" impossible; put
it where a person already is (the PR). A resume replays a recorded decision, never buys it again.

**Problem:** A bare `go test ./...` died at the 10-minute package timeout. **Rule:** Use the Makefile.

## Git state the run does not own

**Problem:** Two runs died with `conflict_exists`: the run-state store resolved origin through
`FETCH_HEAD`, one file shared by every process on the checkout, and a watcher's fetch rewrote it.
**Rule:** Never resolve a ref through process-global git state. Fetch into a private per-run ref with
`--no-write-fetch-head --refmap=`.

**Problem:** Host `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** The run
states its own git environment (`-c rerere.enabled=false`); host config never reaches its merge.

## Waiting and watching

**Problem:** Blind `sleep 300` loops; three hand-rolled watchers, each wrong. **Rule:** Wait on a
CONDITION over durable evidence, a push stream, or a held PID's exit, covering every terminal state
(reported, blocked, died-without-reporting) — or the watcher's silence means nothing.

**Problem:** A watcher piped through `tail` delivered nothing; `pgrep -f` read ALIVE by matching its
own argv. **Rule:** No `tail`/`head` in a watcher pipeline. Hold the PID and check its start time.

**Problem:** The close-out refused PR #11 as "CI unsatisfiable" the second it opened it; GitHub had
not created the check runs yet. **Rule:** Absence right after creation is "not yet". Bound a wait for
the thing to APPEAR before calling it "never".

## Where a tick lives

**Problem:** Ticks here that change the ticks repo were undispatchable (ha9: SEVEN dispatches); klq's
"mark xte, wne, gvc" needed a tracker write its worker's boundary forbids. **Rule:** A tick lives where
its CODE is AND where its worker can write; another repo's change is an `upstream-tick`, never a child here.

## Provider and model configuration

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated
GLM. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise. Change one variable at a time.

**Problem:** GLM via `cloudflare-workers-ai` leaked `<think>` tags. **Rule:** Off-vendor, set `compat.thinkingFormat`.

## Reviews and repairs

**Problem:** A review's "blocker" was repaired and the repair regressed; a regression test "failed
before the fix" only on an unrelated assertion. **Rule:** REPRODUCE a reported defect at the base,
and read WHICH assertion fails there.

**Problem:** A defect that is a SHAPE was repaired one site at a time ("collect counts commits": dyo
Go, 94u TS, herdr still open; "a throw escapes finalize": 4lv, then 0ye). **Rule:** A repair names
EVERY implementation of the seam (grep Go, TS, every executor), leaves a guard test, and reproduces
on the old code.

**Problem:** A boot step past its retries escaped supervisePass: no revocation, release or destroy.
**Rule:** In the Run Workflow every ending is finalize's: a step that can throw is caught AT ITS STEP
into a finalize-reaching outcome, and a test fails each step past its retries.

**Problem:** A review said NOT READY; the run recorded ready-to-merge. **Rule:** A verdict is a typed field.

**Problem:** Gate evidence keyed by commit was wrong both ways, because the run writes `.ticfac/` to
the branch it gates. **Rule:** Key evidence by the SOURCE (tree minus the run's own path), and make
a derived key a function of the thing it identifies, not of its history.

**Problem:** The integrated gate refused innocent work six times through wall-clock tests measuring
the host; a host-dependent git fixture failed at base in NINE yoh ticks, and the runstate
maintenance fixture was filed FOUR more times in gvc. **Rule:** A gate verdict is about the tree only
if the host is bounded. Telling workers to look a failure up does not work; show them the drafts.

**Problem:** wne's per-tick gates went green while 7 of 10 relevant tests skipped under `-short`;
yoh's ts gate ran no vitest; every gvc absorption test is EndToEnd, skipped by the gate, and the
review found three high defects there.
**Rule:** A tick's evidence runs under the gate's own flags. A test the gate does not run is not
evidence — add it to the gate, or name the gap in the acceptance.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put
work and report in ONE commit. **Rule:** A fake demands the identity and reproduces the real shape;
a more forgiving fake certifies the defect it hides — fix it in the same change.

**Problem:** Two identical fixture commits in one second got one SHA. **Rule:** Make them differ.

**Problem:** fake-runner keyed "first try" on TICFAC_ATTEMPT, the RUN's counter. **Rule:** "The
tick's first try" keys on `$TICFAC_TRY`; attempt numbers are identity, never an ordinal.

**Problem:** Verification workers left `RESULT-<id>.md` uncommitted; collect saw `no-commits`.
**Rule:** Evidence output is committed even with no source change.

## Tracker hygiene

**Problem:** `tk close` on a parked tick printed usage. **Rule:** Usage is a REFUSAL — re-run with
`--reason done` to read it; a `tk ask`-parked tick needs `--from human`; `git commit .tick/`.

**Problem:** Promotions pointed at ticks that existed only as untracked files. **Rule:** A promotion
is finished when the tick is COMMITTED where the next run reads it; re-read its text against what
the epic deleted (u9h still names the deleted EpicReconcilerWorkflow).

**Problem:** A spawn blamed "the probe" at 93% of its session limit. **Rule:** Test the pane by hand.
