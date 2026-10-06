# Epic umq closed out: containers on the durable_object policy (SDK 1.0)

The retro of epic `umq` as its close-out (tick `rlx`, attempt 3, run
`run_7445005ff17447c3ae351f5ba99ea01d`) leaves it, written from the records on the integration
branch `epic/umq` and the ticks' own reports (`RESULT-dax.md`, `RESULT-l76.md`). The attempt report
beside this record carries the full close-out; this document is the durable part — what the epic
delivered, what the run absorbed, what is left open, and the learnings the retro compacts.

**Why this is a document and not a `.tick/learnings.md` edit.** The close-out's learnings
compaction is produced in full below (the appendix is the whole compacted file, ready to apply),
but this container's own boundary guard refuses it at the substrate: worker.sh's pre-commit hook
rejects any commit that stages a path under `.tick/` — `.tick/learnings.md` included, which the
write boundary itself exempts (tick 54n) — and the finish phase's `sweep_boundary_state` restores
the directory before the salvage, so an uncommitted edit is discarded with the container. A cloud
close-out therefore cannot write the one file its role names, and a close-out that committed
nothing but its report would be rejected as `no-commits` (tick 19l). The compaction rides here
until a tick unblocks the file; the defect is filed from this close-out's report.

## What the epic delivered

The plan moved every factory container — workers, then the orchestrator — off Sandbox SDK 0.12.x's
`Sandbox` class and the app-wide image rollout, onto our own Durable Object class under
`scheduling_policy = "durable_object"`. Eight ticks were planned; five (fsy, x9d, nmd, 1hq, v1d)
landed on main on 2026-10-01 as PRs #187/#188/#189/#192/#193, closed by the operator the day the
epic was planned; the run dispatched the remaining cutover (dax), the review (l76) and this
close-out (rlx).

- **[A1] met** — new runs default to `do_v1` with the image pin recorded at submit and a per-job
  instance size, routed per run under the seam; `wrangler.toml` declares exactly one container
  application, `ticks-factory-sandbox` on the `durable_object` policy, with no `max_instances`.
  Proven live in this run's own containers: two factory deploys ran mid-run (main `4cec450a`,
  `782ccd5c`), and every container of the run — the worker, the review, the close-out — started on
  the run's submit-time pin `TICFAC_VERSION=ce124e8ca47a`, with PID 1 `sleep infinity`, the new
  image's boot entrypoint.
- **[A2] met** — staging Worker rehearses a deploy over a live run (nmd, PR #189); the
  constructor re-arms the inactivity timeout on the DO restart a deploy causes; corroborated live
  by the two mid-run deploys not taking the worker's container.
- **[A3] met, the deletion by construction post-merge** — the grace period, `rollout_held.go`,
  the deploy's rollout wait and the `max_instances` mirror are deleted (only ban-list tests and
  comments name them now); the 0.x application `ticks-orchestrator` is deleted by the deploy
  behind a live-run guard, so the physical deletion fires at the first post-merge deploy — its
  summary line is the evidence to read.
- **[A4] met** — prune protects every digest a live run pins (`livePinsSQL` + selection tests),
  and an unreadable D1 answer stops the prune rather than guessing.
- **[A5] met** — the integrated gate passed both halves at `41553f3957fe`; CI is green on the
  epic PR #242; the cloudflare suite ran green in CI and twice by hand; and the hn6-style cloud
  run the item names is this one, a `do_v1` run whose worker tick completed end to end.

## What the run absorbed

Five findings, every one decided `backlog-default`, none gating, none entering the running epic;
each promoted to a backlog tick with an owner and listed on the epic PR. From dax: the three
subprocess tests failing at base in Linux containers (**8ct** — whose subject the run's own gate
repair `4f742e41` then fixed) and the declared-image check still naming the deleted 0.x
application (**oog**). From l76: the `run_image` stamp naming the deployment's image rather than
the run's pin (**xtd**), the deploy's Go SQL restating the Worker's run states (**oyn**), and the
still-open 8ct (**2b2**). The decision records are under
`.ticfac/runs/run_7445005ff17447c3ae351f5ba99ea01d/absorptions/`.

## What is left open

1. The two post-merge reads on the first deploy this run's merge triggers: [A3]'s deletion line,
   and whether real wrangler's output carries the digest-pinned FactorySandbox image line.
2. The five backlog ticks: 8ct (fixed in this epic; 2b2 records how to close it once the merge is
   on main), oog, xtd, oyn, 2b2.
3. The 0.x residue — the `sdk0` opt-out and the residual `Sandbox` class and `SANDBOXES` binding —
   filed from this close-out as a proposal.
4. The stall in this run's own dispatch, owned by epic ex6 (created on main during this run): the
   WorkerAgent host kept dying and resuming without progress, and no stall detection fired for a
   pi-durable worker.
5. The learnings destination a cloud close-out cannot reach — filed from this close-out as a
   defect; until it is fixed, the appendix below is the apply.

## The learnings this retro compacts

Three entries. Two extend rules the file already carries; one is new. Every rule below is already
worded to drop into `.tick/learnings.md`; the appendix carries the whole compacted file, with the
older entries' examples tightened to hold the 150-line cap — no rule was dropped.

1. **Planning — post-merge clauses name their observable.** umq's [A3] could only fire after the
   merge, was never marked, and three workers re-derived it. A clause needing the deployed factory
   is marked post-merge at planning *naming the observable its evidence is* — the line the first
   deploy prints — so the close-out reads it instead of re-deriving it.
2. **Orchestration — a repair that lands a backlog tick's subject.** umq's repair `4f742e41`
   fixed what backlog tick 8ct describes; the link lived only in its commit message, so the
   review re-derived it as a new tick (2b2). A repair landing a backlog tick's subject writes the
   merge on that tick's notes in the same change — a fixed tick that reads open is re-derived by
   hand.
3. **Reviews and repairs — the gate's host is the epic's own product.** umq's integrated gate went
   red on three tests green on CI and laptops: the epic's own new image runs PID 1 as
   `sleep infinity`, which reaps nobody, so killed tools stayed zombies and `kill(pid, 0)` read
   them as alive. A red that shows only on an epic's new image is the epic's (the image IS the
   change): fix the oracle, never skip the test.

## Appendix: the compacted `.tick/learnings.md` as this close-out produced it

150 lines, at the file's own cap, ready to apply verbatim (the three entries above are integrated,
and these older entries' examples were tightened to make room: the SIX-epics clause list, the hn6
state matrix, the wne/mrn vocabulary entry, the l6t deletion entry, the 6in close-out entry, the
ticks-repo boundary entry, the SHAPE-repair entry, the wne `-short`/vitest entry, the
lost-sandbox-handle fixture entry, and the dm2 gateway entry).

```
# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the hn6 (2026-10-05), 43y and umq (2026-10-06) close-outs.

## Planning an epic

**Problem:** SIX epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6,
43y); umq's [A3] fired only post-merge, unmarked, and three workers re-derived it. **Rule:** When
an epic's gate is a run or a view, the FIRST tick wires the thinnest end-to-end path through the
PRODUCTION entry point and a named tick INSIDE the epic performs it before the review. A clause
needing the DEPLOYED factory to carry the epic's code is marked post-merge at planning, naming the
OBSERVABLE its evidence is (the line the first deploy prints), so the close-out READS it.

**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads
was prose. **Rule:** An orchestrator epic names the NEXT run on the rebuilt binary as its demo;
[A<n>] items AND a command per item land at planning, or the run absorbs on guesses.

**Problem:** hn6 closed 65 of a 9-tick plan; ten repairs each fixed one word-pairing (a stopped run,
the tracker's close). **Rule:** An epic rendering run state lists the state matrix (running, held,
stopped, failed, cloud, local …) × every surface at planning, ONE precedence table tested over it.

**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick; three rewrote exit-code
logic (bot, 4mv, bkg/rix/vqc): 16 ticks, 7 conflict resolves, a silent same-function collision (7o5).
**Rule:** Fold a NOT READY review's findings by SEAM (one tick owns "exit codes"), not one per finding.

**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a tier
overlay replaced the model; yoh keyed the Workers-AI rule on the substrate, not the executor (78v).
**Rule:** Check a policy on the FINAL resolved value, keyed on what actually crosses the boundary.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; mrn died on add/add.
hn6's wave 1 hand-typed the status contract's goldens, and seven repairs removed values wave 2's
derivation could never produce. **Rule:** A tick that DECLARES a vocabulary and one that CONSUMES it
are different waves, and the declaring wave's fixtures are checked against the builder from day one.

**Problem:** l6t deleted the wave path and four things only it produced; after jhp deleted the pi
CLI, six ticks chased kind "pi" in cells and fixtures (twa's would have killed every cloud
container). **Rule:** A deletion tick first LISTS every effect and pointer of the deleted path in
BOTH repos (grep the NAME), naming each one's owner or "dropped".

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

**Problem:** 6in's close-out took four attempts: a BLOCKED left work no rule disposed of (#117),
and a re-verify that committed nothing was rejected "no-commits". **Rule:** Every honest answer
needs a terminal verdict, "nothing new" included; a verifier commits its evidence.

**Problem:** `tk close` usage prints; promotions pointed at uncommitted ticks. **Rule:** Usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.

**Problem:** Ticks changing the ticks repo were undispatchable (ha9); 43y's twa fix was cells
outside the worker boundary. **Rule:** A tick lives where its CODE is AND its worker can write;
another repo's change is an `upstream-tick`; a fix that is a decision goes to the operator.

**Problem:** umq's repair fixed what backlog tick 8ct describes; the link lived only in its commit
message, and the review re-derived it (2b2). **Rule:** A repair landing a backlog tick's subject
writes the merge on that tick's notes — a fixed tick that reads open is re-derived by hand.

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

**Problem:** A defect that is a SHAPE was repaired site by site; "clear with" cost hn6 nine repairs.
**Rule:** A repair names EVERY implementation of the seam, leaves a guard test, reproduces on the
old code; a command printed for a person comes from ONE builder, parsed with the real CLI.

**Problem:** 06t deleted the stray RESULT-*.md reports and passed its gate; the integration merge
(keepReportsOut) restored all seven, and only round two's `git ls-tree epic/hn6` saw it (obk).
**Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.

**Problem:** A boot step past its retries escaped supervisePass. **Rule:** Every ending is
finalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.

**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the
branch it gates. **Rule:** Key evidence by the SOURCE (tree minus the run's own path).

**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed
at base in 18 ticks; one 43y 120s bound was filed five times; umq's gate went red on tests green on
CI because its OWN new image runs PID 1 as `sleep infinity`, which reaps nobody — killed tools
stayed zombies, and `kill(pid,0)` read them as alive. **Rule:** A gate verdict is about the tree
only if the host is bounded (registrytest.GuardMain's per-process root); before filing "fails at
base", grep `.tick/issues/` — the run must dedupe on the test id. A red that shows only on an
epic's new image is the epic's (the image IS the change): fix the oracle, never skip the test.

**Problem:** wne's gates went green with 7 of 10 skipped under `-short`; yoh's ts gate ran no
vitest. **Rule:** A test the gate does not run is not evidence: add it or name the gap. A NEW package's
suite joins `[testing.commands]`; a tick touching dispatch, claims or profiles runs `./internal/reconcile/`.

**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI
green (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; identical fixture
commits shared a SHA. **Rule:** A fake demands the identity and reproduces the real shape — a more
forgiving fake certifies the defect it hides; fix it in the same change, and make fixture commits differ.

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

**Problem:** dm2 blamed two row-less pi runs on pi abandoning the stream; 648 found pi DOES drain
it (4 live calls, 4 tagged rows). The gateway passes pi's own `Authorization` upstream, and the route
needs `/workers-ai/v1`. **Rule:** a gateway route is verified by reading the LOG ROW back, not by
the call answering 200; a cause is verified by reproducing it, not by the symptom it would explain.
```
