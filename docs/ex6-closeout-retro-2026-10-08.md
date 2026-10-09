# Epic ex6 closed out: pi-durable in the cloud, proved and hardened

The retro of epic `ex6` from its close-out (tick `eno`, attempt 12, run
`run_af0e77b99c1540b7b8005cf04fd2795d`, which took over the failed
`run_69f8f57832d34601b040367c7207b81f`). It is written from the integration
branch `epic/ex6` (head `3f5bf861c` when this was written, source-identical to
this checkout), the ticks' own reports on their branches, and the two runs'
records under `.ticfac/runs/` — never from memory of the plan. The learnings it
compacts belong in `.tick/learnings.md`; the compacted file itself is this
document's appendix, ready to apply verbatim, because this substrate cannot
commit a `.tick/` path (tick 3hw, open — verified again from this container:
the pre-commit hook refuses it).

## What the epic set out to do

From `.tick/issues/ex6.json` (created 2026-10-06, follow-on to 43y): prove
pi-durable in the cloud for real and harden it. 43y shipped pi-durable as the
single worker harness; its first real cloud run (umq's `run_7445005f…`) stalled
for hours — the WorkerAgent DO's host kept dying and resuming without progress,
and no stall detection fired for a pi-durable worker. ex6's scope: the
resume-loop root cause and pi-durable-aware stall detection (landed in the
epic's base as #236/#238), the cloud PR-review boot on pi-durable, fault
injection of 43y's A2 into a real cloud run, the post-merge cloud run for
43y's A4, harness suites in the per-tick gate, local pi-durable metering (m1w),
and the small 43y follow-ups. The operator restructured sck's proposal into
this epic (b8b23ce8d): its seven 43y follow-up ticks became ex6's children,
and ex6's own cloud run on config:glm is the A4/A2 run.

## How the run went

Two incarnations, one integration branch. The first (`run_69f8f578…`) closed
six ticks (3sd, 8gd, 8wa, gzw, m1w, qzg) and then FAILED: 2p3, 2pn, 96s and
q6z — all four ticks whose whole deliverable is a `.tick/` file — were held on
`attempt_needs_human`, because a harness fault exited 1 with no report and the
old ladder turned that into a question for a person. PR #260 ("worker: a
harness fault is never a question for a person (ex6 2p3)") fixed the ladder on
main; the fold `549bd8a94` carried it in, and the second run
(`run_af0e77b99c…`) took over the claim and closed all four through the
protected-change channel.

| tick | role | model, tries | outcome |
|---|---|---|---|
| 3sd | implement | GLM flash → 5.3, 3 | merged `aafb2538c`: the classification pin's profile digest gets a SHORT guard, so a profile edit fails `make gate` at the editing tick |
| 8wa | implement | GLM flash → 5.3, 2 | merged `97ab0f651`: PR-body condensation level 3 bounds an amendment's headline by rune; the base already carried the value-omission |
| 8gd | implement | GLM 5.3, 2 | merged `9edd748fc`: the cloud PR-review boots hosted (`--boot`/`--finish` halves, a review-spec host kind, `RUN_HARNESS` unset); contract bundle re-cut 2.3.0 → 2.4.0 |
| gzw | implement | GLM 5.3, 1 | merged `c15200e79`: the timeout-discipline guard recognises `promisify(<entry point>)` under any local name |
| m1w | implement | GLM 5.3, 1 | merged `d6d0069bb`: the Go writer of the local gateway metering join — **half delivered**, no agent report (a salvage); the review named the missing reader blocking → **lrd** |
| qzg | implement | GLM 5.3, 1 | merged `fab7fe786`: the mid-command container loss fires the `onRestore` ear, its line pinned on both the host and the runbook |
| 2p3 | implement | GLM flash, 1 (this run) | merged `2f136b4c3`: `ticfac init` writes no args in any pi cell, guarded; this repository's own cell rides as a protected change |
| 96s | implement | GLM flash, 1 (this run) | merged `b52d34d29`: no code — the whole deliverable is the `runners.local.toml` header note, carried as a protected change |
| q6z | implement | GLM flash → 5.3, 2 (this run) | merged `6f55eb0d6` through resolve-conflict 6 (`internal/cli/init.go`, against 2p3): the repo-side comment reworded; both routing files' fixes ride as protected changes |
| 2pn | implement | GLM flash → 5.3, 2 (this run) | merged `6294aacb5`: `make harness-gate` and the parity guard that holds from whichever side lands first; the `[testing.commands.harness]` cell is the protected change |
| 0rt | review, round 1 | GLM 5.3 | **NOT READY** — two blocking findings (m1w's missing reader; the protected-change ordering that erases 2pn's cell), one medium (ex6's one-line acceptance criteria) |
| tgx | implement (absorbed from 0rt) | GLM flash, 1 | merged `587236914`: `orderForApplication` — contents before appends per path — with the incident reproduced end to end |
| lrd | implement (absorbed from 0rt) | GLM flash → 5.3, 2 | merged `73f92acbf`: `harness/src/local/gateway-metering.ts` — the reader m1w's writer cited — plus the wire-level acceptance suite and the Go↔TS parity guard |
| ynd | review, round 2 | GLM 5.3 | **READY** — one low finding (the harness test's hand-copied credential pipeline, y9g) |

Every dispatch of the second run — twelve attempts plus the resolve-conflict
job and both reviews — was a hosted pi-durable conversation on Workers AI GLM
in a cloud container; so was the first run's. The two review rounds spent the
run's bound of 2 exactly.

## Delivered against the definition of done

- **[A1] A real cloud epic run completes with every worker on pi-durable, no
  stall left undetected — MET by this run.** The run this retro belongs to IS
  the run: every one of its dispatches (implement ticks, the resolve-conflict
  job, both reviews and this close-out) is a hosted pi-durable conversation in
  a cloud container — this close-out runs in one, with the harness's
  `TICFAC_BASH_NONCE` in its environment and PID 1 the FactorySandbox image's
  `sleep infinity`, on the run's submit-time image pin. 8gd removed the last
  CLI floor, so the PR-review job hosts too. The stall detection the item
  leans on (the commit-stream watch, quiet-resume counting, the guarded tk
  resolution) landed in the epic's base as #236/#238; this run's own failures
  were harness and collect faults, which the folded #260 turned into
  redispatches, not stalls — and not questions for a person. The run's
  completion is this close-out's own event, so the close-out cannot observe
  it; the run's checkpoint is the durable witness (state `running`, reason
  "CI is green on the epic PR #265; the close-out of ex6 is admitted").
- **[A2] The two faults resume without redoing work — machinery MET and
  pinned; the live injection is the operator's, honestly scored PREDICTED.**
  In the tree: the host-kill resume is exercised by a harness node-half test
  this close-out ran green ("resumes from the storage after the harness is
  killed mid-tool, without re-running the tool"); the three restore lines
  (between rounds, nonce, mid-command) are pinned by
  `internal/cli/runbook_fault_evidence_test.go` against both the host's
  TypeScript and the runbook, which the mid-command line joined in qzg; and
  the supervisor settles a hosted worker on every ending. What no diff can
  deliver: fault 1's only door today is a factory deploy, which "lands
  between tool calls and proves nothing" — the runbook says so itself — and
  the operator door that would fire it reliably (`0mz`) is open and
  unimplemented, with the injection tick (`9if`) blocked on it and the
  post-merge cloud run (`lox`) beside it. The runbook scores the real-run half
  *predicted* until then, and the repo's constitution routes a
  deployed-factory clause to post-merge. A concern the PR carries, not a
  defect the branch can act on.
- **[A3] pi-durable workers metered locally and in the cloud — MET.** The
  cloud half holds (every hosted conversation presents the run token to the
  factory's gateway, which stamps the attribution). The local half is the
  round-1 blocker, delivered end to end: m1w's Go writer puts the join in
  `worker.json` (`workerconfig.go`), lrd's reader (`gateway-metering.ts`, the
  very file the writer's comment cited) composes the provider override the pi
  CLI extension composes, and the acceptance suite drives the real entry as a
  child process against a recording fake AI Gateway, asserting every model
  call tagged — route, both bearers, metadata, affinity — with negatives that
  bite (the faux rung unmetered, a no-route URL refusing the boot, an empty
  credential sending nothing). `workerconfig_parity_test.go` pins every field
  the writer marshals to a declaration in the harness reader, so the two
  sides cannot drift apart silently again.
- **[A4] make gate + cloudflare tests + harness suites pass in CI — MET, and
  re-run at this head by the close-out.** On this tree (source-identical to
  the epic branch head): `make gate` exit 0 in 1m57s (gofmt, vet, the short
  suite over 47 packages); `make ts-gate` exit 0 in 30s (biome,
  `contracts:check` — 16 contracts at bundle 2.4.0 — and tsc); `make
  harness-gate` exit 0 in 58s (workerd 10 files, node 11 files / 66 tests);
  and the cloudflare vitest suite the per-tick gate deliberately leaves to
  CI — 80 files, 1874 tests — green in 7m01s. CI is green on the epic PR
  #265 per the run's own checkpoint, which admits this close-out. The
  per-tick gate's harness half (2pn's cell) is one of the five pending
  protected changes below.

## What the run absorbed

From `.ticfac/runs/run_af0e77b99c1540b7b8005cf04fd2795d/absorptions/` — seven
decision records, all named, none silent:

- **Two on the basis `reviewer`** — the final review judged the epic NOT
  READY and named them blocking, so they became the epic's work, fixed before
  the re-review: the m1w metering finding → **lrd** (merged `73f92acbf`);
  the protected-change ordering finding → **tgx** (merged `587236914`).
  Both had first been decided `backlog-default` and were re-placed
  `before-review` when the review spoke — each record says so.
- **Five on the basis `backlog-default`** (low severity, no done item named):
  the repo-side args guard proposal → **flm**; "make gate covers a third of
  the run's gate" → **ydw**; "2p3 asks for the same routing edit q6z
  delivers" → **r0t**; the implement-routing guard proposal → **sob**; the
  round-2 review's "harness metering test's credential pipeline is unpinned
  from Go's" → **y9g**. All are backlog ticks with owners, listed on the
  epic PR.
- **No `worker-asserted-high` absorption** happened in this run: the one
  high-severity claim against the machinery's own record (0rt's
  acceptance-criteria finding, `bd613fca…`) was fixed by the run itself as a
  tracker edit (`e11b1642e`), so ex6's items parse as four.
- **The five pending protected changes** are the other half of the absorption
  story: 2pn's harness gate cell (`2911b0ce…`), q6z's two routing files
  (`4fe8b808…`, `6d07a495…`), 2p3's runners.toml (`b916a378…`), 96s's
  runners.local.toml note (`492d466f…`). No tick was created for any of
  them — the run applies each as its own labelled commit after this
  close-out's reads, and the PR lists every one for the merger.
- The failed first run's eighteen promoted findings (all `backlog-default`,
  all still open) ride the same PR: the boundary-wall family (26g, z2x, o5k,
  aii, ky7, g7p), the dead-args family (a9c, u5q, ktd, yfx, 5k6), 96s's
  guard-disarm defect (knm), 8wa's PR-body pair (cyr, wa8), 8gd's hosted
  review nudge (2ha), qzg's restore pair (mr0, 0j1), and 2pn's gate-third
  note (z30).

`ticfac amendments ex6` answers "run epic-ex6 has no worker-proposed
amendments to the epic's record filed with it", and no
`.ticfac/runs/<run>/amendments/` records exist for either incarnation: nothing
here needs confirming or marking unconfirmed.

## What is left open

1. **The five pending protected changes** — applied by the run as labelled
   commits after this report, listed on PR #265. This close-out composed them
   by hand the way `orderForApplication` now orders them (q6z's content, then
   2p3's, then 2pn's append; one content each for cloud and local): the
   result parses as TOML, declares `go`, `ts` and `harness`, the harness
   cell is byte-identical to the Makefile's `harness-gate` recipe, the
   implement cell carries no `args`, and the cloud and local payloads differ
   from the branch's files by exactly their intended edits. Nothing is left
   to compose.
2. **[A2]'s live half and 43y's [A4] cloud read: the operator's three ticks**
   — `0mz` (the door that ends one attempt's WorkerAgent host on demand),
   `9if` (the two injections, blocked on 0mz), `lox` (the post-merge cloud
   run). The runbook is the procedure and scores itself PREDICTED.
3. **The backlog.** Five ticks from this run (flm, ydw, r0t, sob, y9g) and
   eighteen from the failed first run, all open. When the labelled commits
   land, r0t, 5k6, u5q and ktd are done by them (the umq lesson: write the
   merge on the tick's notes in the same change), and the boundary-wall
   family (26g, z2x, o5k, aii, ky7, g7p) is largely answered by the channel
   this run proved five times over — but nothing closes a backlog tick on
   its own: the PR is where a person already is.
4. **96s's unattributed gate FAIL.** Its report recorded one `make gate` FAIL
   it could not attribute (output lost, never reproduced, two clean follow-ups
   including a full `-count=1` pass), and per the repo's dedupe rule it filed
   nothing. Flagged here because a worker asked for a human eye and nothing
   has answered.
5. **The learnings destination (tick 3hw, open).** The write boundary exempts
   `.tick/learnings.md`, but this substrate's pre-commit hook refuses every
   `.tick/` path and the finish sweep would discard an uncommitted edit —
   re-verified from this container. The compaction is durable here, in this
   document's appendix, and the apply is a paste.
6. **The review bound is spent**: two rounds, a NOT READY and a READY. A third
   review is a person's, by the run's own rule.

## Learnings

The retro compacts five additions into `.tick/learnings.md` (the full
compacted file is the appendix below, ready to apply verbatim):

- The production-entry-point rule gains the DOOR clause: an acceptance item
  that FIRES a fault names the tick that makes the injection reliable as a
  precondition — a deploy is not a door (ex6's [A2], `0mz`/`9if`).
- The planning-data rule gains ONE ITEM PER LINE: ex6's four `[A<n>]` marks on
  one line parsed as one mega-item, blinding the done-evidence,
  worker-asserted-high absorption and a review's typed `breaks` claim to
  A2–A4 until the review repaired it (hn6's yjq had the same shape).
- A NEW rule for a finish line that spans a writer→reader seam: done only when
  the CONSUMING side exists and is exercised — m1w cited a harness file on no
  branch and its gate stayed green because only the writer was tested; a
  salvage with no agent account is reviewed as a branch, never closed as done.
- The boundary rule records what ex6 proved: a deliverable no worker can
  commit rides as a `protected_change` finding, which the run applies as a
  labelled commit the PR lists (ex6 carried five).
- A NEW rule for the compose channel: several protected changes onto one file
  are a seam of their own — contents before appends per path, the composition
  tested end to end with the REAL payloads (tgx reproduced the incident's
  exact key order; round 1 had only exercised each piece).

To stay under the file's 150-line cap beside those additions, the compaction
merges same-family entries (terminal verdict + usage refusal; boot step +
evidence keying; the two fixture entries; the boundary-wall entry now carries
the channel facts) and drops exactly one rule, the `redirect: "manual"` test
gotcha — the one entry that restates platform behaviour rather than a lesson
the next epic acts on. Restore it from the previous file if you disagree;
every other rule is carried over.

## The next feasible epic

Already on the record: **bo9, "Operator surface follow-ups"** — four planned
ticks with `[A<n>]` items and a close-out tick (52h) waiting: ba4 (`ticfac run
--cloud --config`), g5f (a stale installed skill reported), u2g
(`skills get --full`), 0ek (the local pi harness inside the binary — a
prerequisite for the first release, y9v). It is labelled `config:claude`, so
its run would also be the live v5t [A1] proof that is still owed: a real
`config: claude` cloud run on the subscription rung, which ex6's [A1] did not
cover (ex6 ran config:glm). After it, the planned-but-unrun epics are dha
(Phase 5: drive a run from the Cloudflare host — its premise predates umq's
cutover and needs a re-read before planning) and t8u (test suite performance).
ex6's own leftovers are operator actions and single ticks, not epic material.

## Appendix: the compacted .tick/learnings.md (apply verbatim)

148 lines, at the file's hard cap of 150.

```markdown
# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro. Seeded from ticks' learnings
2026-09-02; last compacted at the 43y, v5t and ex6 (2026-10-08) close-outs.

## Planning an epic

**Problem:** SEVEN epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6, 43y, v5t); 6fv
tested its door with WORKER_AGENTS unbound while production binds it; ex6's [A2] fault 1 had no door but a factory
deploy, which lands between tool calls and proves nothing — its injection (9if) waits on the door tick (0mz). **Rule:**
When an item's evidence is a run, a view or a FIRED fault, the FIRST tick wires the thinnest path through the PRODUCTION
entry point, its tests binding what production binds; the DOOR that makes it reliable is named at planning — a deploy is
not a door — and a DEPLOYED-factory clause is post-merge.

**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the machinery's data was prose; ex6's four [A<n>]
marks sat on ONE line, so acceptance parsed one mega-item and the done-evidence, worker-asserted-high absorption and a
review's breaks claim never saw A2–A4 (hn6's yjq: same shape). **Rule:** An orchestrator epic names the NEXT run on the
rebuilt binary as its demo; [A<n>] items AND a command per item land at planning, ONE ITEM PER LINE, or the run absorbs
on guesses.

**Problem:** m1w closed on a salvage with no agent report: the Go writer cited a harness reader that existed on no
branch, and the gate stayed green because only the writer was tested — the review named the missing reader blocking
(lrd). **Rule:** A finish line spanning a writer→reader seam is done only when the CONSUMING side exists and is
exercised; a delivery citing a file on no branch is the tell, and a salvage with no agent account is reviewed as a
branch, never closed as done.

**Problem:** hn6 planned 9 ticks and closed 65: ten repairs each fixed ONE wording pairing across nine runs. **Rule:**
An epic that renders run state lists the state matrix (running, held, stopped, failed, prior, cloud, local …) × every
surface at planning, ONE precedence table tested over it.

**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick (16 ticks, 7 resolves); v5t's two became
one tick per side of ONE seam, plus a duplicate high finding (bw7). **Rule:** A reviewer reports ONE blocking finding
per SEAM, naming every side of it: the run makes one tick per finding, so the report is the only fold there is.

**Problem:** A policy held per layer and failed in the whole: xte's cloud overlay was replaced by a tier overlay; yoh
keyed the Workers-AI rule on the substrate, not the executor (78v). **Rule:** Check a policy on the FINAL resolved
value, keyed on what actually crosses the boundary.

**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; hn6's wave 1 HAND-TYPED its status goldens
and seven repairs removed values wave 2 could never produce. **Rule:** DECLARING and CONSUMING a vocabulary are
different waves, and the declaring wave's fixtures are checked against the builder from day one.

**Problem:** l6t deleted the wave path and four things only it produced; after 43y's jhp deleted the pi CLI, six ticks
chased kind "pi" in cells and fixtures (twa's would have killed every cloud container at boot). **Rule:** A deletion
tick first LISTS every effect and pointer of the deleted path in BOTH repos (grep the NAME), naming each one's owner or
"dropped".

**Problem:** dz1's re-run held on the claim its STOPPED predecessor left. **Rule:** An enforcement moving into this repo
brings its exemptions; test resume and re-run, not only the refusal.

## Orchestration

**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one file. **Rule:** Name
the SHA; check `--is-ancestor`. Two additions to one file are a union in INTENT, not in text — hand the resolve to a
worker holding the context. ONE owner per artifact.

**Problem:** A worker committed tracker state its prompt forbade. **Rule:** A boundary the substrate can enforce must
not rest on instruction-following — make it impossible and REPORT every attempt.

**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an "in flight" state outlived its
writer. **Rule:** The merge gate is the only test of a shared contract; settle in-flight state from durable evidence,
never by trusting the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:** A hold that fires
when the system does its job (finding things) makes "unattended" impossible; put it where a person already is (the PR).
A resume replays a recorded decision, never re-buys one.

**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of, a re-verify rejected "no-commits"; `tk
close` usage printed where promotions pointed at uncommitted ticks. **Rule:** Every honest answer needs a terminal
verdict (a verifier commits evidence); usage is a REFUSAL, and a promotion is finished when the tick is COMMITTED.

**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches); two deliverables (twa 43y, tda
v5t) were runner cells outside the boundary, costing holds until an operator appended them; ex6's five `.tick/`
proposals then composed badly: applied in findings-key order, an append landed first and two whole-file contents
replaced the file, erasing the cell (tgx). **Rule:** A tick lives where its CODE is AND its worker can write: check
every acceptance artifact against exemptFromBoundary at PLANNING; another repo's change is an `upstream-tick`; a
decision (an exception, a routing cell) is a named operator step in the epic, never a dispatched tick. A deliverable no
worker can commit rides as a `protected_change` finding the run applies as a labelled commit the PR lists — and that
compose channel is a SEAM: contents before appends per path, the composition tested end to end with the real payloads.

**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`); host
`rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and host git state never
reach the run: fetch into a private per-run ref (`--no-write-fetch-head --refmap=`) and state the run's own git
environment (`-c rerere.enabled=false`).

**Problem:** epic-hn6, restarted on its id after a SIGINT, read `phase: cancelled`: no `resumed` line was written, so
the old `run_died` stayed its last word. **Rule:** Every incarnation states itself in the feed; a reader of "the last
terminal line" is only as true as the writer's resume line.

## Waiting and watching

**Problem:** Blind `sleep 300` loops; hand-rolled watchers; `pgrep -f` read ALIVE by matching its own argv. **Rule:**
Wait on a CONDITION over durable evidence, a push stream, or a held PID's exit, covering every terminal state; no
`tail`/`head` in a watcher pipeline; absence right after creation is "not yet" — bound it to APPEAR.

## Reviews and repairs

**Problem:** A regression test "failed before the fix" only on an unrelated assertion. **Rule:** REPRODUCE a reported
defect at the base, and read WHICH assertion fails there.

**Problem:** A defect that is a SHAPE was repaired one site at a time (dyo Go, 94u TS); nine hn6 repairs fixed a printed
"clear with" command that could not clear its hold. **Rule:** A repair names EVERY implementation of the seam, leaves a
guard test, and reproduces on the old code; a command printed for a person comes from ONE builder and a test PARSES it
with the real CLI.

**Problem:** 06t deleted the stray RESULT-*.md reports and passed its gate; ilz's close-out committed "[A1] broken on
the head that ships" onto the tree that fixed it — every gate green, because prose has no test. **Rule:** A repair or a
CLAIM whose effect is a TREE state is accepted on the INTEGRATED head, never the branch; a close-out's own RECORD is
such a claim, so a re-run close-out re-reads it; a fold of main re-opens every claim about a file it touched.

**Problem:** A boot step past its retries escaped supervisePass; gate evidence keyed by commit was wrong both ways,
because the run writes `.ticfac/` to the branch it gates. **Rule:** Every ending is finalize's — catch a throwing step
AT ITS STEP, a test failing each step past its retries — and key evidence by the SOURCE (tree minus the run's own path).

**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed at base in 18 ticks;
one 43y harness bound was filed five times (h3c 7oy 4ao omq 30e). **Rule:** A gate verdict is about the tree only if the
host is bounded (registrytest.GuardMain); before filing "fails at base", grep `.tick/issues/` — the run must dedupe on
the test id.

**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; yoh's ts gate ran no vitest.
**Rule:** A test the gate does not run is not evidence: add it or name the gap. A NEW package's suite joins
`[testing.commands]` in its tick; a tick touching dispatch, claims or a profile runs `./internal/reconcile/`.

**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI green (#100).
**Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.

## Fixtures

**Problem:** A lost sandbox handle was green because the fake keyed on job_id; v5t's Go executor said `cloudflare-
workers-ai/…` where the door said `workers-ai/…`, each green on its own fake (y38); hn6's activity fixture spelled a
claude worker's model `claude-opus-5`, which no real attempt says. **Rule:** A fake demands the identity and reproduces
the real shape; fix a forgiving fake in the same change; copy identity strings from a real record
(`.ticfac/runs/*/attempts/*.json`), never type them. A TS↔Go seam gets a parity guard over its spellings and one Go test
driving the REAL door (claude_sub_e2e_test.go's shape). Fixture commits differ; "the tick's first try" keys on
`$TICFAC_TRY`.

## Boundaries this repo pays to learn

**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract. **Rule:** The config FILE's keys are the
bundle's; $TICFAC_* is the operator-preference surface.

**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated GLM; GLM via
`cloudflare-workers-ai` leaked <think>` tags. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise; change
one variable at a time. Off-vendor, set `compat.thinkingFormat`.

**Problem:** dm2 blamed row-less pi runs on pi abandoning the stream; 648 showed pi drains it. The gateway passes pi's
own `Authorization` upstream, and the route needs `/workers-ai/v1`. **Rule:** Verify a gateway route by reading the LOG
ROW back; verify a cause by reproducing it.
```
