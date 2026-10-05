# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the hn6 close-out, 2026-10-05.

## Planning an epic

**Problem:** FOUR epics closed without the run their acceptance names (Phase 4, xte, yoh; 2jn's A1
was traced with a stand-in child); hn6's A1 names `ticfac watch epic-<id>` and nothing watched the
live run until its close-out, which found three defects only a live run shows. **Rule:** When an
epic's gate is a run or a view, the FIRST tick wires the thinnest end-to-end path through the
PRODUCTION entry point, and a named tick INSIDE the epic runs it live before the review.

**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads
was prose. **Rule:** An orchestrator epic names the NEXT run on the rebuilt binary as its demo;
[A<n>] items AND a command per item land at planning, or the run absorbs on guesses.

**Problem:** hn6 planned 9 ticks and closed 65. Ten repairs (gmo z3p d23 eli c65 cno c9n 3yx 5d4
3qg) each fixed ONE pairing of which word wins: an earlier run's hold, a stopped cloud run, an older
subject run, the tracker's close. **Cause:** the epic ran across nine runs, cloud and local, and the
model merging them was specified for one live run. **Rule:** An epic that renders run state lists
the state matrix at planning (running, held, stopped, failed, cancelled, awaiting merge, prior run,
cloud, local) × every surface, with ONE precedence table (chronology, then authority) tested over it.

**Problem:** 2jn's NOT READY became one tick per finding; three rewrote exit codes, one collision went
silent (7o5). **Rule:** Fold a NOT READY review's findings by SEAM, not one tick per finding.

**Problem:** A policy held per layer and failed in the whole (xte's tier overlay replaced the model;
yoh keyed Workers AI on the substrate, 78v). **Rule:** Check a policy on the FINAL resolved value,
keyed on what crosses the boundary (the executor), never on one input.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; mrn died on add/add.
hn6's wave 1 declared the status contract with HAND-TYPED goldens, and seven repairs (378 oro log dvv
fq0 lh4 lkq) removed values wave 2's derivation could never produce. **Rule:** A tick that DECLARES a
vocabulary and one that CONSUMES it are different waves, and the declaring wave's fixtures are
checked against the builder from day one (TestEveryGoldenAgreesWithThePipelineDerivation's shape).

**Problem:** l6t deleted the wave path and four things only it produced; four of 6in's findings were
docs naming deleted verbs. **Rule:** A deletion tick first LISTS every effect and pointer of the
deleted path in BOTH repos, naming each one's new owner or "dropped"; that list is its acceptance.

**Problem:** dz1's re-run held on the claim its STOPPED predecessor left. **Rule:** An enforcement
moving into this repo brings its exemptions; test the resume and re-run paths, not only the refusal.

## Orchestration

**Problem:** Wave-2 branched from a base missing wave-1. **Rule:** Name the SHA; check `--is-ancestor`.

**Problem:** Two additions to one file were cut by two same-wave ticks. **Rule:** Two additions to one
file are a union in INTENT, not in text — hand the resolve to a worker holding the context. A
versioned artifact has ONE owner per wave.

**Problem:** A worker committed tracker state its prompt forbade. **Rule:** A boundary the substrate
can enforce must not rest on instruction-following — make it impossible and REPORT every attempt.

**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an "in flight"
state outlived its writer. **Rule:** The merge gate is the only test of a shared contract; settle
in-flight state from durable evidence, never by trusting the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**
A hold that fires when the system does its job (finding things) makes "unattended" impossible; put
it where a person already is (the PR). A resume replays a recorded decision, never buys it again.

**Problem:** 6in's close-out took four attempts past its committed retro: a BLOCKED left work no
rule disposed of (#117), and a re-verify that committed nothing was rejected "no-commits". **Rule:**
Every honest answer needs a terminal verdict, "nothing new" included; a verifier commits its evidence.

**Problem:** `tk close` usage prints; promotions pointed at uncommitted ticks. **Rule:** Usage is a
REFUSAL; a promotion is finished when the tick is COMMITTED.

**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches). **Rule:** A
tick lives where its CODE is AND its worker can write; another repo's change is an `upstream-tick`.

**Problem:** Two runs died `conflict_exists`: the store resolved origin through `FETCH_HEAD`, shared by
every process on the checkout. Host `rerere.enabled` replayed a person's resolve into a machine merge.
**Rule:** Never resolve a ref through process-global git state: fetch into a private per-run ref
(`--no-write-fetch-head --refmap=`), and state the run's own git environment (`-c rerere.enabled=false`).

**Problem:** epic-hn6 died by SIGINT, was started again on the same id, and its live status read
`phase: cancelled` with no ETA: a restart over a non-terminal checkpoint writes no `resumed` line,
so the old `run_died` stayed the run's last word. **Rule:** Every incarnation states itself in the feed;
a reader keyed on "the last terminal line" is only as true as the writer's resume line.

## Waiting and watching

**Problem:** Blind `sleep 300` loops; three hand-rolled watchers, each wrong; a watcher piped through
`tail` delivered nothing; `pgrep -f` read ALIVE by matching its own argv. **Rule:** Wait on a
CONDITION over durable evidence, a push stream, or a held PID's exit (check its start time), covering
every terminal state; no `tail`/`head` in a watcher pipeline.

**Problem:** The close-out called PR #11's CI "unsatisfiable" before GitHub created its checks.
**Rule:** Absence right after creation is "not yet": bound a wait to APPEAR before calling it "never".

## Reviews and repairs

**Problem:** A regression test "failed before the fix" only on an unrelated assertion. **Rule:**
REPRODUCE a reported defect at the base, and read WHICH assertion fails there.

**Problem:** A defect that is a SHAPE was repaired one site at a time (dyo Go, 94u TS, herdr open; 4lv
then 0ye); nine hn6 repairs (ulw qxj q8m v2f gf0 fub quz bd5 l1t) fixed a printed "clear with"
command that could not clear its hold. **Rule:** A repair names EVERY implementation of the seam (grep
Go, TS, every executor), leaves a guard test, and reproduces on the old code. A command printed for
a person comes from ONE builder and a test PARSES it with the real CLI against the hold it names.

**Problem:** 06t deleted the stray RESULT-*.md reports and passed its gate; the integration merge
(keepReportsOut) restored all seven, and only round two's `git ls-tree epic/hn6` saw it (obk).
**Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.

**Problem:** A boot step past its retries escaped supervisePass. **Rule:** In the Run Workflow every
ending is finalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.

**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the
branch it gates. **Rule:** Key evidence by the SOURCE (tree minus the run's own path).

**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed
at base in 18 ticks. **Rule:** A gate verdict is about the tree only if the host is bounded (guard
shared state by a per-process temp root); before filing "fails at base", grep `.tick/issues/`.

**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; dz1's red
EndToEnd sat unread until the close-out. **Rule:** A test the gate does not run is not evidence; a
tick touching dispatch or claims runs the full `./internal/reconcile/` suite.

**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI
green (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put
work and report in ONE commit. **Rule:** A fake demands the identity and reproduces the real shape;
a more forgiving fake certifies the defect it hides — fix it in the same change.

**Problem:** Identical fixture commits shared a SHA. **Rule:** Make them differ; "first try" keys on
`$TICFAC_TRY`, never TICFAC_ATTEMPT.

**Problem:** hn6's activity fixture spelled a claude worker's model `claude-opus-5`; real attempts say
`opus`, so a live claude worker's activity is never read. **Rule:** Copy a fixture's identity
strings from a real record (`.ticfac/runs/*/attempts/*.json`), never type them.

## Boundaries this repo pays to learn

**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract. **Rule:** The config
FILE's keys are the bundle's; the $TICFAC_* ENVIRONMENT is this repo's operator-preference surface.

**Problem:** A login-route test read the login page's own 401: `SELF.fetch` FOLLOWS a 303. **Rule:**
A redirect-asserting route test passes `redirect: "manual"`.

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated
GLM; GLM via `cloudflare-workers-ai` leaked `<think>` tags. **Rule:** A bodyless 4xx is REQUEST SHAPE
until proven otherwise; change one variable at a time. Off-vendor, set `compat.thinkingFormat`.
