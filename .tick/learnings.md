# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the 6in close-out, 2026-09-28.

## Planning an epic

**Problem:** FOUR epics closed without the run their acceptance names (Phase 4, xte, yoh; 2jn's A1
"init then run, claude in herdr" was traced by the review, tested with a stand-in child, and never
run — 2jn itself ran on GLM subprocesses). **Rule:** When an epic's gate is a run, the FIRST tick wires
the thinnest end-to-end path through the PRODUCTION entry point, and a named tick INSIDE the epic
performs the run before the review. If the run lives elsewhere, the acceptance says so.

**Problem:** gvc absorbed none of its 21 findings; 2jn scored none of 30 (A1-A6 bind no command); 6in's
done has no [A<n>] items to bind. **Cause:** the DATA the machinery reads was left to prose. **Rule:**
An epic that changes the orchestrator names the NEXT run on the rebuilt binary as its demonstration;
[A<n>] items AND a command per item land at planning, or the run absorbs on guesses it cannot score.

**Problem:** 2jn's review said NOT READY with ten findings; each became its own parallel tick, three
of them rewrote exit-code logic (bot, 4mv, then bkg/rix/vqc), and the post-review burst took 16 ticks,
7 conflict resolves and a silent same-function collision (7o5). **Rule:** Fold a NOT READY review's
findings by SEAM (one tick owns "exit codes", one owns "clear commands"), not one tick per finding.

**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a
tier overlay replaced the model; yoh keyed the Workers-AI rule on the substrate while the
cloudflare-sandbox executor also ran under the local one (78v). **Rule:** Check a policy on the
FINAL resolved value, keyed on what actually crosses the boundary (the executor), never on one input.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; mrn died on add/add.
**Rule:** A tick that DECLARES a vocabulary and one that CONSUMES it are different waves.

**Problem:** l6t deleted the wave path and four things only it produced (harness bound, logs, board
events, cron sweeps), each found later; after ticks' chz, four of 6in's fourteen findings were docs
still naming deleted verbs and files. **Rule:** A deletion tick first LISTS every effect the deleted
path produced and every pointer to it in BOTH repos (grep the name), then names each one's new owner
or records it as dropped. Its acceptance is that list, not "tests still pass".

**Problem:** dz1 took over the width tk 0.32.0 stopped enforcing, and a re-run under a new run id now
holds on the claim its own STOPPED predecessor left. **Rule:** An enforcement moving into this repo
brings its exemptions; test the resume and re-run paths, not only the refusal it exists for.

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

**Problem:** `tk close` usage prints, promotions pointed at uncommitted ticks, a spawn blamed "the
probe". **Rule:** Usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.

**Problem:** Ticks here that change the ticks repo were undispatchable (ha9: SEVEN dispatches); klq's
"mark xte, wne, gvc" needed a tracker write its worker's boundary forbids. **Rule:** A tick lives where
its CODE is AND where its worker can write; another repo's change is an `upstream-tick`, never a child here.

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

**Problem:** Wall-clock tests refused innocent work six times; ONE host-dependent SIGTERM temp-tree
defect (8d6) failed at base in nine yoh ticks, four gvc refiles and five 2jn ticks. **Rule:** A gate
verdict is about the tree only if the host is bounded; a guard on shared machine state attributes by a
per-process temp root (registrytest.GuardMain). Before filing "fails at base", grep `.tick/issues/`
for the test's name: a promoted finding leaves the drafts.

**Problem:** wne's per-tick gates went green while 7 of 10 relevant tests skipped under `-short`;
yoh's ts gate ran no vitest; every gvc absorption test is EndToEnd, skipped by the gate; the
review found three high defects there; dz1 broke an EndToEnd reconcile test and the epic branch's
CI sat red 25 minutes, unread until the close-out. **Rule:** A test the gate does not run is not
evidence: add it to the gate, or name the gap. A tick touching dispatch or claims runs the full
`./internal/reconcile/` suite.

**Problem:** 6in's close-out was admitted "CI green" two seconds after a red: the PR head was a
`.ticfac/`-only commit whose only check runs were the pull_request run's SKIPPED jobs, forge.CI read
all-skipped as green, and the walk back to the code commit never ran. **Rule:** A skip is not a pass:
green needs an executed success per check; a head of only skips has no verdict of its own.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put
work and report in ONE commit. **Rule:** A fake demands the identity and reproduces the real shape;
a more forgiving fake certifies the defect it hides — fix it in the same change.

**Problem:** Identical fixture commits shared a SHA; fake-runner keyed "first try" on TICFAC_ATTEMPT;
verification workers left reports uncommitted. **Rule:** Make fixture commits differ; "the tick's
first try" keys on `$TICFAC_TRY`; evidence output is committed even with no source change.

## Boundaries this repo pays to learn

**Problem:** A new `factory_*` key in ~/.ticfacrc contradicts the pinned credential-ownership contract
("an unknown factory_ key is a typo"). **Rule:** The config FILE's keys are the bundle's; the $TICFAC_*
ENVIRONMENT is this repo's operator-preference surface (TICFAC_RUNNER, $TICFAC_STATUS_PUSH): no re-cut.

**Problem:** A login-route test read the login page's own 401: `SELF.fetch` FOLLOWS a 303. **Rule:**
A redirect-asserting route test passes `redirect: "manual"`.

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated
GLM. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise. Change one variable at a time.
**Problem:** GLM via `cloudflare-workers-ai` leaked `<think>` tags. **Rule:** Off-vendor, set `compat.thinkingFormat`.
