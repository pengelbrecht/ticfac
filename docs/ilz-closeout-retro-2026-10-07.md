# Epic ilz closed out: the claude-sub operator guide, and the first cloud run on the claude config

The retro of epic `ilz` from its close-out (tick `kzs`, the third close-out attempt, dispatched by run
`run_18eafdaa047544be9a196d913fb08a30`). Written from the integration branch `epic/ilz` at `781ff16`,
the records of both runs that drove this epic under `.ticfac/runs/`, and the ticks' own reports. Plans
and memory are not sources here. The attempt report beside this record carries the close-out's findings;
this document is the durable part.

**This record replaces, rather than amends, the one the previous close-out committed.** That version
marked [A1] "MET AS WRITTEN AND REVIEWED, BROKEN ON THE HEAD THAT SHIPS" and named three stale failover
claims. `555253b` corrected all three *before* the head this is written on, so the earlier text
contradicted the tree carrying it — review round 4 (`lpl`) reported that as a finding, tracked as `3gw`,
and this rewrite is its answer. Every number below is re-measured against `781ff16`.

**Why the learnings compaction is an appendix here and not a `.tick/learnings.md` commit.** The same
wall `umq`'s cloud close-out hit (`docs/umq-closeout-retro-2026-10-06.md`), unmoved since and re-checked
here: the container's pre-commit hook refuses any commit staging a path under `.tick/`
(`image/worker.sh`, installed at `.git/hooks/pre-commit`) including the `.tick/learnings.md` that the Go
write boundary itself exempts (`internal/exec/subprocess/report.go:193`); `worker-collect.ts:313` makes
any `.tick/` path a boundary violation that refuses the whole branch; and `sweep_boundary_state`
(`image/worker.sh:596`) restores the directory before the salvage, so an uncommitted edit dies with the
container. It is filed as tick `9sy`, still open. The compaction is Appendix A, as the whole file, ready
to apply.

## What the epic set out to do

From its tracker record: the smoke run of the `claude` config in the cloud — a deliberately small epic
whose own workers ride the subscription rung (sonnet implement, opus review and close-out). The
deliverable was the operator documentation `v5t` shipped the rung without: how to add, list, rotate and
remove subscriptions, how the pool fails over, how an epic selects the config, and how to see the pool's
state.

## How the run went

Nine dispatches across **two runs**, 06:35 → 10:04 UTC on 2026-10-07, with one operator intervention
between them. Four review rounds and three close-out attempts.

`run_4d92bab25a7341d3bba01691779168a0`:

| # | tick | role | model | outcome |
|---|---|---|---|---|
| 1 | `56z` | implement-tick | sonnet | the guide and the README link; merged `f05bb24b`, integrated gate green. Taken over from run `run_d2359e14`, whose checkpoint read `failed` |
| 2 | `yaz` | review-epic | opus | **NOT READY** (round 1) — four claims in the failover section contradict `claude-sub.ts`. Four findings (`5gm`, `hbw`, `gv3`, `55q`) |
| 3 | `5gm` | implement-tick | sonnet | the four claims corrected; merged `e834273c`, integrated gate green |
| 4 | `0n3` | review-epic | opus | **READY** (round 2), two non-blocking findings (`z38`, `k40`) |
| 5 | `kzs` | closeout-epic | opus | **rejected, `no-commits`**: the branch carried only `RESULT-kzs.md`. Four findings (`9sy`, `2x8`, `am6`, `hyw`) |
| 6 | `kzs` | closeout-epic | opus | the retro committed and merged `1f23a308`, integrated gate green. Three findings (`xe9`, `n2u`, `adn`) |
| 7 | `vnp` | review-epic | opus | **NOT READY** (round 3) — placed because the tree had changed since round 2's READY: `bfedc21` had folded `main` in, and three failover claims went stale. Two findings (`6a6`, `3za`) |

Run 1 then halted on `land_review_not_ready`: three NOT READY rounds against a bound of two. The
operator intervened, and that intervention is three separate things, worth separating because only the
first is on this branch:

- `555253b`, pushed straight to `epic/ilz` at 09:08 UTC — the failover section rewritten against
  `claude-sub.ts` as PRs #250–#253 left it. This is the epic's own fix, and it is not a tick.
- `a83e4bf` at 09:21 UTC — a second fold of `main` into `epic/ilz`, which is why the merge base is now
  `e684dfec` and not the `4cf4d59` the previous retro measured against.
- the halt could not be cleared by the command the halt message named: `ticfac run ilz --cloud` found
  the parked Workflow and merely re-attached, replaying the halt. Clearing it took a stop and a fresh
  submission under a **new run id**, which is why this epic has two runs. Filed as `kk7`.

`run_18eafdaa047544be9a196d913fb08a30`:

| # | tick | role | model | outcome |
|---|---|---|---|---|
| 1 | `lpl` | review-epic | opus | **READY** (round 4) — re-read claim by claim against the cited sources at `1ff1dde`, `make gate` exit 0. Two non-blocking findings (`3gw`, `7o4`) |
| 2 | `kzs` | closeout-epic | opus | this record |

Every implement dispatch resolved `sonnet` on the economy tier and every review and close-out `opus`,
as the `claude` config declares. All nine ran the `claude` harness.

## Delivered against the definition of done

The deliverable, measured against the merge base of `main` and `epic/ilz` (`e684dfec`):

```
README.md                   |   5 ++
docs/claude-sub-operator.md | 159 ++++++++++++++++++++++++++++++++++++++++++
```

Plus this record. Nothing else: no Go, no TypeScript, no stray `RESULT-*.md` on the integrated tree
(`git ls-tree --name-only -r origin/epic/ilz | grep -i result` matches only `internal/reconcile/
roleresult.go` and its test). The guide names no URL, no account id and no token value. Everything else
on the branch is the two runs' own records and the tracker's.

- **[A1] the guide, every claim checked against `claude-sub.ts`, `sandbox-executor.ts` and
  `runconfig_select.go` — MET.** Four review rounds read the document claim by claim. The three claims
  that round 3 found stale are corrected on this head, re-derived here against the files rather than
  taken on `555253b`'s or round 4's word:
  - the quota arm now carries `overageInUse`: the guide says **any** answer, a 200 included, marked
    overage-in-use is benched until the unified reset, else the overage reset, else 5 hours
    (`docs/claude-sub-operator.md:95-100` against `claude-sub.ts:361-369`, `OVERAGE_BENCH_FALLBACK_MS`
    at `:221`);
  - the guide now says that answer is **withheld** and the job handed a synthesized 429 naming the
    reset, which is what `claude-sub.ts:948-966` does — it cancels the upstream body and returns a
    `rate_limit_error` refusal with `retry-after` set from the bench;
  - the stickiness sentence now reads `LEASE_TTL_MS` (2 hours) as measuring *silence*, kept alive by the
    proxy's once-a-minute refresh (`docs/…:72-81` against `LEASE_REFRESH_MS` at `claude-sub.ts:208`,
    the refresh at `:553` and `:906`, and the TTL's own comment at `:196-197`: it "measures SILENCE, not
    the job's length").

  The rest re-reads as the guide says, also re-derived: the `LABEL` regex (`claude-sub.ts:229`) and
  `subscriptionLabels` reading the env at every lease, `normalizeToken` stripping whitespace,
  `DEFAULT_MAX_CONCURRENT` = 2 (`:191`), `AUTH_BENCH_MS` 24h (`:227`), the `/overage/` window exclusion
  and the `unified === "" && reset !== null` quota case (`:382-393`), the non-quota 429 falling to
  `retry-after ?? THROTTLE_COOLDOWN_MS` (`:401-404`), the lease's fewest-live choice and its
  `exhausted`/`busy`/`none` outcomes, the step-down overriding the boot pair
  (`sandbox-executor.ts:1888-1921`), both observation surfaces, and the config precedence in
  `runconfig_select.go`, which neither fold of `main` touched.

  What the guide still omits is in the open list below (`6a6`, `7o4`, `3za`, `z38`, `hbw`): each is one
  or two sentences of coverage, none contradicts the code, and none was named blocking by any round.
- **[A2] README links it — MET.** `README.md:278`, in the factory/cloud section.
- **[A3] `make gate` passes — MET, on the head that ships.** Re-run in this container at `781ff16`
  rather than cited: `make gate` (gofmt, `go vet ./...`, `go test -short ./...`) exit 0, and `make
  ts-gate` (install, lint, `contracts:check`, `tsc --noEmit`) exit 0 — biome reports one info-severity
  suggestion in `cloudflare/src/`, which is not a failure. The run's own integrated gate passed four
  times in run 1 (`.ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/evidence/gate-*.json`, all
  `exit_code: 0`), the last pair keyed to `1f23a308`, which predates `555253b`; round 4 ran `make gate`
  at `1ff1dde` to exit 0. Note what [A3] does not cover: **no gate reads prose**, which is how three
  verified claims rotted under four green gates.
- **[A4] the run's workers ran on claude via the subscription rung — MET, and directly readable for
  this attempt.** This close-out's own container environment carries `TICKS_CLAUDE_SUB=1`. That variable
  exists only in `claudeSubProcessEnv()` (`claude-sub.ts:458-464`), which `worker-boot.ts:668` spreads
  **only** when the boot holds a lease (`input.claude_sub !== undefined`); a step-down returns no
  `claude_sub` and overrides the pair with the deployment's standing Workers AI rung
  (`sandbox-executor.ts:1907-1921`), so the variable cannot be present on a stepped-down job. For the
  other eight dispatches the evidence is one step weaker and unchanged from the previous retro: each
  resolved `claude` on a versionless alias and each report's container-facts line reads `harness claude
  exited 0`. The gap the acceptance item fell into — it named "the run's events", a Worker
  `console.log` — is closed on `main` by this epic's own fold: `claudeSubNote`
  (`sandbox-executor.ts:1933-1938`) now puts `claude_sub: {state, label, reason, retry_at}` on the job
  handle and into the run feed. What it still does not reach is the per-attempt record (`hyw`).

## What the epic bought outside its own branch

Worth recording, because none of it is in the diff above and all of it exists because this epic ran.
Two orchestrator defects this epic's incidents found are already fixed on `main`:

- **PR #255** (merged 09:37 UTC) — a NOT READY round caused by the base moving under the epic no longer
  spends the review bound, and a *closed* close-out's merge is no longer read as unreviewed work.
  `treeChangedSinceReview` read close-out ids from `tk graph`, which lists open tasks only, so once
  `kzs` closed its retro merge looked like new work: that is what placed round 3 and pushed this epic
  past its bound. Backstopped at two excused rounds.
- **PR #256** (merged 09:57 UTC) — `kk7`: a held cloud run's rerun now supersedes the parked Workflow
  instead of re-attaching to it.

One remains open on `main`: **`1y4`** — after `supervision_halted` the in-container orchestrator lingered
~10 minutes and no completion signal reached the Workflow, which stayed `running`. #256 works around it;
the root cause is unowned.

Note for whoever lands this: the orchestrator that dispatched run 2 is `TICFAC_VERSION=4cf4d590a987`,
which predates both #255 and #256, so neither fix was in force for this epic — the manual stop and
resubmission stand as the record of how run 1 was cleared.

## Note on the epic PR

PR #254 reads `mergeable_state: dirty` on GitHub. This is the known, documented shape rather than a new
problem: the sole conflicting path is `.tick/activity/activity.jsonl`, which `.gitattributes` resolves
with `tk`'s `merge=tick-activity` union driver — a driver GitHub does not have. `b6u` records exactly
this ("the PR still LOOKS conflicting to a person on GitHub, and GitHub's merge button will refuse it")
and the remedy already shipped: CI also triggers on `epic/**` pushes, so the code gets a verdict
independent of mergeability. A test merge in this container confirmed `activity.jsonl` is the only
conflict; the driver itself could not be run here, because the worker guard refuses `tk`.

## What is left open

Sixteen backlog ticks the two runs promoted, re-read against `781ff16` and **not re-filed**. Five have
moved since they were written and should be re-read before they are worked:

| tick | sev | what |
|---|---|---|
| `9sy` | high | the container hook and `worker-collect.ts` refuse the `.tick/learnings.md` the Go boundary exempts, so a cloud close-out cannot write the file its role names. Re-checked here: still true, and this record is still the workaround |
| `xe9` | high | **answered.** It named the three stale failover claims; `555253b` corrected all three and round 4 re-read them. Close it |
| `3gw` | medium | **answered by this record.** The previous retro declared [A1] broken on a head where it is fixed. Close it |
| `6a6` | medium | **half answered.** Its second half — that the guide said a 401/403 benches for 24h without saying only inference answers are classified — is fixed (`docs/…:82-86,104`). Its first half stands: `REFUSED_BETAS`, a non-claude model and a leased label with no secret value still go unmentioned |
| `55q` | medium | **obsolete.** It asked for a lease TTL above a worker's 8h wall clock; `LEASE_REFRESH_MS` is that fix. Close it rather than work it |
| `k40` | low | **premise moved.** It asked for a test pinning stale-lease reassignment; with the refresh a live job's lease no longer goes stale, and the refresh itself is pinned twice (`cloudflare/test/claude-sub.test.ts:290`, `:566`). Rescope or close |
| `adn` | low | the tracker hygiene for `55q` and `k40` above. Still the right call |
| `7o4` | low | the guide says a quota-stopped claude-sub job "is redispatched at the same tier" with no bound; `maxInfrastructureRedispatches` is 3 (`internal/reconcile/infrastructure.go:86`) and the fourth refuses the run (`:155-157`). Verified still true |
| `3za` | medium | the Observing section names the two HTTP surfaces but not the per-run answer — `claude_sub` on the handle and in the run feed. Verified still true |
| `z38` | medium | the guide names `CLAUDE_SUB_MAX_CONCURRENT` but not `[configs.claude.tier_policy.concurrency]` (`.tick/runners.cloud.toml:204-206`), which is what bounds how many claude workers run at once. Verified still true |
| `hbw` | medium | the guide says "this repository has one Worker, one config"; `cloudflare/staging/wrangler.toml:20` also binds `CLAUDE_SUB_POOL`. The command it justifies is right. Verified still true |
| `gv3` | medium | `claude-sub.ts:30-32` still says "Production does none of these"; `cloudflare/wrangler.toml:166` binds the pool and `index.ts:2006` exports the proxy. Verified still wrong at this head |
| `n2u` | medium | nothing re-opens a prose claim when a fold rewrites the file it was checked against. This epic is the whole case for it: two folds, four review rounds, three close-outs |
| `2x8` | medium | review and close-out containers get a depth-1 checkout, unannounced. Hit again here: `git log` saw one commit until `git fetch --unshallow origin` |
| `am6` | low | the boundary banner calls any refused `tk` invocation an attempted write. Fired again in this container for `tk tree ilz` and for git's own `tk merge-activity` driver during the test merge, neither of which could write anything |
| `hyw` | low | no run-level attempt record names the subscription a lease billed. The remedy site is not where the tick points: `.ticfac/runs/*/attempts/*.json` is written before the dispatch |

Nothing from this close-out is unanswered that is not either in that table or filed as a finding on the
attempt report beside this record.

## What the retro changes in `.tick/learnings.md`

Four lessons, each folded into an existing entry rather than added beside it — the file is at its
150-line hard cap, so this compaction removes as much as it adds. It merges four pairs of related
entries (the wave-base and shared-contract pair, the close-out-verdict and `tk close` pair, the
`supervisePass` and gate-evidence pair, and the config-key and gateway pair), landing at 149 lines.

- **Planning an epic.** [A4] named the run's *events* — a Worker log nobody can read afterwards. And
  [A1] staked the epic on "every claim checked against" three source files while another stream of work
  was rewriting the first of them. An acceptance item about a RUN names the PERSISTED record that
  answers it; an item of the form "checked against `<file>`" names who else is editing that file.
- **Reviews and repairs.** The guide was verified claim by claim, then had `main` folded under it twice,
  and three claims went stale on the shipping head with every gate green, because prose has no test.
  A CLAIM, like a tree state, is accepted on the INTEGRATED head — and **a close-out's own record is
  such a claim**, so a close-out that runs again re-reads the record its predecessor committed. That
  this one did not is `3gw`, and it cost a dispatch.
- **Orchestration / boundaries.** Attempt 5 burnt a dispatch on `no-commits` because the container hook
  refuses the one path the collect's Go half allows. Every enforcer of a boundary — hook, sweep, banner,
  `worker-collect.ts` — reads `ExemptFromBoundary()`; and a close-out COMMITS its learnings.
- **Orchestration.** The halt message named a command that could not clear the halt, and clearing it by
  hand cost ~40 minutes and a new run id. A message that tells a person what to do is tested by doing
  it (`kk7`, fixed in #256).

## Appendix A — `.tick/learnings.md`, compacted, ready to apply

Replace the file with the following, verbatim. It is 149 lines, inside the 150-line hard cap.

```markdown
# Learnings

Repo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.
Seeded from ticks' learnings 2026-09-02; last compacted at the v5t and ilz (2026-10-07) close-outs.

## Planning an epic

**Problem:** SEVEN epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6's
`ticfac watch`; 43y's [A4]/[A2]; v5t's [A1]); 43y's and v5t's COULD not: the factory deploys main only.
v5t's 6fv tested its door with WORKER_AGENTS unbound; production binds it, so the claude rung was dead
on the real door until the review (yhe); ilz's [A4] named the run's EVENTS — a Worker `console.log` its
own review called unverifiable. **Rule:** When an epic's gate is a run or a view, the FIRST tick wires
the thinnest path through the PRODUCTION entry point, its tests binding what production binds
(wrangler.toml), a named tick INSIDE the epic performs it, and the item names the PERSISTED record that
answers it — a handle field, an attempt record — never a log line; DEPLOYED-factory clauses are post-merge.

**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads was
prose; ilz's [A1] cited three source files and #250-#253 rewrote the first one mid-epic. **Rule:** An
orchestrator epic names the NEXT run on the rebuilt binary as its demo; [A<n>] items AND a command per
item land at planning; an item of the form "checked against <file>" names who else is editing <file>.

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

**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one file;
parallel ticks sharing a return shape were green alone, broken together; an "in flight" state outlived
its writer. **Rule:** Name the SHA and check `--is-ancestor`; two additions to one file are a union in
INTENT, not in text — hand the resolve to a worker holding the context, ONE owner per artifact; the merge
gate is the only test of a shared contract; settle in-flight state from durable evidence, never the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**
A hold that fires when the system does its job (finding things) makes "unattended" impossible; put
it where a person already is (the PR). A resume replays a recorded decision, never re-buys one.

**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify
rejected "no-commits"; ilz's kzs lost one reporting without committing, then needed a third after the
epic's own fix landed under its retro; `tk close` usage prints; promotions pointed at uncommitted ticks.
**Rule:** Every honest answer needs a terminal verdict; a verifier commits evidence and a close-out
COMMITS its learnings; usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.

**Problem:** A worker committed tracker state its prompt forbade; ticks changing the ticks repo were
undispatchable (ha9: SEVEN dispatches); twa's (43y) and tda's (v5t) deliverables were runners.cloud.toml
cells outside the boundary (7vp, lxo); ilz's close-out burnt an attempt on `no-commits` because the
container hook refuses the `.tick/learnings.md` `OutsideBoundary` exempts. **Rule:** A boundary the
substrate enforces never rests on instruction-following, and EVERY enforcer — hook, sweep, banner,
worker-collect.ts — reads `ExemptFromBoundary()`; a tick lives where its CODE is AND its worker can
write, so check every acceptance artifact against it at PLANNING; another repo's change is an
`upstream-tick`; a decision (an exception, a routing cell) is a named operator step, never a tick.

**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`);
host `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and
host git state never reach the run: fetch into a private per-run ref (`--no-write-fetch-head
--refmap=`) and state the run's own git environment (`-c rerere.enabled=false`).

**Problem:** epic-hn6, restarted on its id after a SIGINT, read `phase: cancelled`: a restart wrote no
`resumed` line, so the old `run_died` stayed its last word. **Rule:** Every incarnation states itself
in the feed; a reader of "the last terminal line" is only as true as the writer's resume line.

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
(keepReportsOut) restored all seven, and only round two's `git ls-tree epic/hn6` saw it (obk). ilz's
guide misread a `??` chain's fallback as its `Math.max` floor (1s for 60s); corrected and verified claim
by claim, it then had `main` merged under it (+503 lines of claude-sub.ts) and three failover claims went
stale — every gate green, because prose has no test — costing two review rounds, an operator fix and two
close-outs, the first of which COMMITTED "[A1] broken on the head that ships" onto the tree that fixed
it (3gw). **Rule:** A repair or a CLAIM whose effect is a TREE state is accepted on the INTEGRATED head,
never the branch — a close-out's own RECORD is such a claim, so a re-run close-out re-reads it; a doc
QUOTES the expression behind each number; a fold of main re-opens every claim about a file it touched.

**Problem:** A boot step past its retries escaped supervisePass; gate evidence keyed by commit was wrong
both ways, because the run writes `.ticfac/` to the branch it gates. **Rule:** Every ending is finalize's
— catch a throwing step AT ITS STEP, a test failing each step past its retries; and key evidence by the
SOURCE (the tree minus the run's own path), never the commit.

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

**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract. A 1,000,000 max-output drew
a bodyless 400 read as context overflow; 8,192 truncated GLM; GLM via `cloudflare-workers-ai` leaked
`<think>` tags; 648 showed the gateway needs `/workers-ai/v1` and passes the caller's own
`Authorization` upstream; a login-route test read the login page's own 401, because `SELF.fetch` FOLLOWS
a 303. **Rule:** The config FILE's keys are the bundle's and $TICFAC_* is the operator-preference
surface; a bodyless 4xx is REQUEST SHAPE until proven; change one variable at a time; off-vendor set
`compat.thinkingFormat`; verify a route by its LOG ROW; a redirect-asserting test sets `redirect:
"manual"`.
```
