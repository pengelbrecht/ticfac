# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the 43y (2026-10-06) and v5t (2026-10-07) close-outs.

## Planning an epic

**Problem:** SEVEN epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6's
`ticfac watch`; 43y's [A4]/[A2]; v5t's [A1]); 43y's and v5t's COULD not: the factory deploys main only.
v5t's 6fv tested its door with WORKER_AGENTS unbound; production binds it, so the claude rung was dead
on the real door until the review (yhe). **Rule:** When an epic's gate is a run or a view, the FIRST
tick wires the thinnest path through the PRODUCTION entry point, its tests binding what production
binds (wrangler.toml), and a named tick INSIDE the epic performs it; a DEPLOYED-factory clause is post-merge.

**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads
was prose. **Rule:** An orchestrator epic names the NEXT run on the rebuilt binary as its demo;
[A<n>] items AND a command per item land at planning, or the run absorbs on guesses.

**Problem:** hn6 planned 9 ticks and closed 65: ten repairs each fixed ONE pairing of which word wins
across nine runs. **Rule:** An epic that renders run state lists the state matrix (running, held,
stopped, failed, prior, cloud, local …) × every surface at planning, ONE precedence table tested over it.

**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick (16 ticks, 7 resolves, a
silent same-function collision, 7o5); v5t's two became y38 (Go) and yhe (TS) on ONE step-down seam: a
resolve and a duplicate high finding (bw7). **Rule:** A reviewer reports ONE blocking finding per SEAM,
naming every side of it: the run makes one tick per finding, so the report is the only fold there is.

**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a tier
overlay replaced the model; yoh keyed the Workers-AI rule on the substrate, not the executor (78v).
**Rule:** Check a policy on the FINAL resolved value, keyed on what actually crosses the boundary.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; hn6's wave 1 HAND-TYPED its
status goldens and seven repairs removed values wave 2 could never produce. **Rule:** DECLARING and
CONSUMING a vocabulary are different waves, and the declaring wave's fixtures are checked against the
builder from day one (TestEveryGoldenAgreesWithThePipelineDerivation's shape).

**Problem:** l6t deleted the wave path and four things only it produced; 6in findings named deleted
verbs; after 43y's jhp deleted the pi CLI, six ticks chased kind "pi" in cells and fixtures (twa's
would have killed every cloud container at boot). **Rule:** A deletion tick first LISTS every effect
and pointer of the deleted path in BOTH repos (grep the NAME), naming each one's owner or "dropped".

**Problem:** dz1's re-run held on the claim its STOPPED predecessor left. **Rule:** An enforcement
moving into this repo brings its exemptions; test resume and re-run, not only the refusal.

## Orchestration

**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one
file. **Rule:** Name the SHA; check `--is-ancestor`. Two additions to one file are a union in
INTENT, not in text — hand the resolve to a worker holding the context. ONE owner per artifact.

**Problem:** A worker committed tracker state its prompt forbade. **Rule:** A boundary the substrate
can enforce must not rest on instruction-following — make it impossible and REPORT every attempt.

**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an "in flight"
state outlived its writer. **Rule:** The merge gate is the only test of a shared contract; settle
in-flight state from durable evidence, never by trusting the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**
A hold that fires when the system does its job (finding things) makes "unattended" impossible; put
it where a person already is (the PR). A resume replays a recorded decision, never re-buys one.

**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify
rejected "no-commits". **Rule:** Every honest answer needs a terminal verdict; a verifier commits evidence.

**Problem:** `tk close` usage prints; promotions pointed at uncommitted ticks. **Rule:** Usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.

**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches); twa's (43y) and
tda's (v5t) deliverables were runners.cloud.toml cells outside the boundary (7vp, lxo): yck burnt two
BLOCKED dispatches and a hold before an operator appended it; 8em wrote an exception onto the epic.
**Rule:** A tick lives where its CODE is AND its worker can write: check every acceptance artifact
against exemptFromBoundary at PLANNING; another repo's change is an `upstream-tick`; a decision (an
exception, a routing cell) is a named operator step in the epic, never a dispatched tick.

**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`);
host `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and
host git state never reach the run: fetch into a private per-run ref (`--no-write-fetch-head
--refmap=`) and state the run's own git environment (`-c rerere.enabled=false`).

**Problem:** epic-hn6, restarted on its id after a SIGINT, read `phase: cancelled`: a restart wrote no
`resumed` line, so the old `run_died` stayed its last word. **Rule:** Every incarnation states itself
in the feed; a reader of "the last terminal line" is only as true as the writer's resume line.

## Waiting and watching

**Problem:** Blind `sleep 300` loops; three hand-rolled watchers, each wrong; `pgrep -f` read ALIVE by
matching its own argv; PR #11's CI called "unsatisfiable" before its checks existed. **Rule:** Wait on a
CONDITION over durable evidence, a push stream, or a held PID's exit, covering every terminal state; no
`tail`/`head` in a watcher pipeline; absence right after creation is "not yet" — bound it to APPEAR.

## Reviews and repairs

**Problem:** A regression test "failed before the fix" only on an unrelated assertion. **Rule:**
REPRODUCE a reported defect at the base, and read WHICH assertion fails there.

**Problem:** A defect that is a SHAPE was repaired one site at a time (dyo Go, 94u TS); nine hn6
repairs fixed a printed "clear with" command that could not clear its hold. **Rule:** A repair
names EVERY implementation of the seam, leaves a guard test, and reproduces on the old code; a
command printed for a person comes from ONE builder and a test PARSES it with the real CLI.

**Problem:** 06t deleted the stray RESULT-*.md reports and passed its gate; the integration merge
(keepReportsOut) restored all seven, and only round two's `git ls-tree epic/hn6` saw it (obk).
**Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.

**Problem:** A boot step past its retries escaped supervisePass. **Rule:** Every ending is
finalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.

**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the
branch it gates. **Rule:** Key evidence by the SOURCE (tree minus the run's own path).

**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed
at base in 18 ticks; one 43y 120s harness bound was filed five times (h3c 7oy 4ao omq 30e). **Rule:** A
gate verdict is about the tree only if the host is bounded (registrytest.GuardMain's per-process root);
before filing "fails at base", grep `.tick/issues/` — the run must dedupe on the test id.

**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; yoh's ts gate
ran no vitest; dz1's and 43y's (enb, 4w7) red EndToEnd epic CI sat unread until a review. **Rule:** A
test the gate does not run is not evidence: add it or name the gap. A NEW package's suite joins
`[testing.commands]` in its tick; a tick touching dispatch, claims or a profile runs `./internal/reconcile/`.

**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI
green (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put
work and report in ONE commit; v5t's Go executor said `cloudflare-workers-ai/…` where the door said
`workers-ai/…`, each green on its own fake (y38). **Rule:** A fake demands the identity and reproduces
the real shape; fix a forgiving fake in the same change. A TS↔Go seam gets a parity guard over its
spellings and one Go test driving the REAL door (claude_sub_e2e_test.go's shape). Fixture commits
differ; "the tick's first try" keys on `$TICFAC_TRY`.

**Problem:** hn6's activity fixture spelled a claude worker's model `claude-opus-5`; real attempts say
`opus`, so a live claude worker's activity is never read. **Rule:** Copy a fixture's identity
strings from a real record (`.ticfac/runs/*/attempts/*.json`), never type them.

## Boundaries this repo pays to learn

**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract. **Rule:** The config
FILE's keys are the bundle's; $TICFAC_* is the operator-preference surface.

**Problem:** A login-route test read the login page's own 401: `SELF.fetch` FOLLOWS a 303. **Rule:**
A redirect-asserting route test passes `redirect: "manual"`.

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated
GLM; GLM via `cloudflare-workers-ai` leaked `<think>` tags. **Rule:** A bodyless 4xx is REQUEST SHAPE
until proven otherwise; change one variable at a time. Off-vendor, set `compat.thinkingFormat`.

**Problem:** dm2 blamed row-less pi runs on pi abandoning the stream; 648 showed pi drains it. The
gateway passes pi's own `Authorization` upstream (a bad key fails 10000) and needs `/workers-ai/v1`.
**Rule:** Verify a gateway route by reading its LOG ROW back; verify a cause by reproducing it.
