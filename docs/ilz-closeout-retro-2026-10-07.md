# Epic ilz closed out: the claude-sub operator guide, and the first cloud run on the claude config

The retro of epic `ilz` from its close-out (tick `kzs`, attempt 6, run
`run_4d92bab25a7341d3bba01691779168a0`). Written from the integration branch `epic/ilz`, the run's
records under `.ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/`, and the ticks' own reports.
Plans and memory are not sources here. The attempt report beside this record carries the full
close-out and its findings; this document is the durable part.

**Why the learnings compaction is an appendix here and not a `.tick/learnings.md` commit.** The same
wall `umq`'s cloud close-out hit (`docs/umq-closeout-retro-2026-10-06.md`), unmoved since: the
container's pre-commit hook refuses any commit staging a path under `.tick/`, including the
`.tick/learnings.md` that the write boundary itself exempts
(`internal/exec/subprocess/report.go:193`), and `sweep_boundary_state` restores the directory before
the salvage, so an uncommitted edit dies with the container. It is filed as tick `9sy`. The
compaction is Appendix A, as the whole file, ready to apply.

## What the epic set out to do

From its tracker record: the smoke run of the `claude` config in the cloud — a deliberately small
epic whose own workers ride the subscription rung (sonnet implement, opus review and close-out).
The deliverable was the operator documentation `v5t` shipped the rung without: how to add, list,
rotate and remove subscriptions, how the pool fails over, how an epic selects the config, and how
to see the pool's state.

## How the run went

Six dispatches, 06:35 → 08:18 UTC on 2026-10-07. Two review rounds, and a close-out twice.

| # | tick | role | model | outcome |
|---|---|---|---|---|
| 1 | `56z` | implement-tick | sonnet | the guide and the README link; merged, integrated gate green. Taken over from run `run_d2359e14`, whose checkpoint read `failed` |
| 2 | `yaz` | review-epic | opus | **NOT READY** — four claims in the failover section contradict `claude-sub.ts` |
| 3 | `5gm` | implement-tick | sonnet | the four claims corrected; merged, integrated gate green |
| 4 | `0n3` | review-epic | opus | **READY**, with two non-blocking findings |
| 5 | `kzs` | closeout-epic | opus | **rejected, `no-commits`**: the branch carried only `RESULT-kzs.md`. The retro was right and undelivered |
| 6 | `kzs` | closeout-epic | opus | this record |

Attempt 5 is worth stating plainly, because the role's rule is recorded and was simply not met: for
`closeout-epic`, `NoCommitsIsFailure` is true — "its deliverable is the record it leaves in the
repository — the retro, and the learnings the boundary permits it to write"
(`internal/exec/subprocess/rolecommits.go:32-35`). It analysed the `.tick/` wall correctly, put the
compaction in its report, and committed nothing. A report is not a deliverable; this epic already
had two precedents for the document that is (`umq`, `v5t`), and it used neither.

Between attempt 5 and attempt 6 the run also merged `main` into `epic/ilz` (`bfedc21`), re-ran the
integrated gate on the result, opened PR #254 and waited for CI. That merge matters more than its
position in the log suggests — see [A1] below.

## Delivered against the definition of done

The epic's whole substantive contribution, measured against the merge base of `main` and
`epic/ilz` (`4cf4d59`):

```
README.md                   |   5 ++
docs/claude-sub-operator.md | 143 ++++++++++++++++++++++++++++++++++++++++++++
2 files changed, 148 insertions(+)
```

Nothing else. No Go, no TypeScript, no stray `RESULT-*.md` on the integrated tree
(`git ls-tree --name-only origin/epic/ilz | grep -i result` is empty). The guide names no URL, no
account id and no token value.

- **[A1] the guide, every claim checked against `claude-sub.ts`, `sandbox-executor.ts` and
  `runconfig_select.go` — MET AS WRITTEN AND REVIEWED, BROKEN ON THE HEAD THAT SHIPS.** Two review
  rounds read the document claim by claim and round 2 passed it at `e834273`. Then `bfedc21` merged
  `main` into `epic/ilz`, carrying PRs #250–#253 and **+503 lines of
  `cloudflare/src/claude-sub.ts`** — the file [A1] names first — and nothing re-read the guide
  against it. Three claims in its failover section are now wrong on the integration head:
  - the quota arm omits `overageInUse`. Any answer, **a 200 included**, carrying
    `anthropic-ratelimit-unified-overage-in-use: true` is now a quota bench until the unified or
    overage reset, falling back to `OVERAGE_BENCH_FALLBACK_MS` = 5 hours
    (`claude-sub.ts:180-181,220-221,361-369`). The guide's quota test (lines 81-93) is 429-shaped
    only, so an operator seeing a five-hour bench with no 429 anywhere has nothing to read;
  - "Benching never blocks the job that triggered it: that job's own answer … is returned to the
    container" (lines 94-96) is false for exactly that case. The proxy cancels the upstream body and
    returns a synthesized 429 instead (`claude-sub.ts:947-971`); `ClaudeSubProxy`'s own docstring now
    reads "unchanged (but for an overage answer, which is withheld)";
  - the stickiness sentence (lines 72-80) measures `LEASE_TTL_MS` from "when it was first taken",
    and promises that a lease stale past two hours is treated as new. The proxy now refreshes a live
    job's lease once a minute (`LEASE_REFRESH_MS`, `claude-sub.ts:208,550-555,906`) and the TTL's own
    comment says it "measures SILENCE, not the job's length".

  Everything else re-reads as the guide says, re-derived here rather than taken on the review's
  word: the `LABEL` regex and `subscriptionLabels` reading the env at every lease
  (`claude-sub.ts:229,300-308`), `normalizeToken` stripping whitespace (`:318-322`),
  `DEFAULT_MAX_CONCURRENT` = 2 (`:191`), `AUTH_BENCH_MS` 24h (`:227`), the `/overage/` window
  exclusion and the `unified === "" && reset !== null` quota case (`:382-393`), the lease's
  fewest-live choice and its `exhausted`/`busy`/`none` outcomes (`:505-535`), the step-down
  overriding the boot pair (`sandbox-executor.ts:1884-1923`), both observation surfaces
  (`index.ts:263,1813-1814` and `claude-sub.ts:749-788`), and the config precedence
  (`runconfig_select.go`, which the merge does not touch at all). The commands an operator types are
  all correct; it is the explanatory half of the most load-bearing section that rotted.
- **[A2] README links it — MET.** `README.md:277-280`, in the factory/cloud section, after the
  deploy-secrets paragraph.
- **[A3] `make gate` passes — MET, on the post-merge head.** The integrated gate ran four times and
  passed every time; the last pair is keyed to `bfedc21`, the head carrying the `main` merge:
  `evidence/gate-0n3-4-go.json` and `gate-0n3-4-ts.json`, both `exit_code: 0`, `result: pass`. CI on
  PR #254 is green per the run's own checkpoint at sequence 37. Note what that does not cover: the
  gate's TypeScript half is contracts and types, not behaviour, and **no gate reads prose**, which
  is why [A1] rotted under a green gate.
- **[A4] the run's workers ran on claude via the subscription rung — MET, and now directly
  readable.** The acceptance named "the run's events", which is a Worker `console.log` — the final
  review called it unverifiable and handed it here. It is verifiable twice over:
  - this container's own environment carries `TICKS_CLAUDE_SUB=1`, and that variable exists only in
    `claudeSubProcessEnv()`, which `worker-boot.ts:668` spreads **only** when the boot holds a
    lease (`input.claude_sub !== undefined`). A step-down returns no `claude_sub` and overrides the
    pair with the deployment's standing Workers AI rung
    (`sandbox-executor.ts:1903-1922`), so the variable cannot be present on a stepped-down job;
  - all four worker dispatches resolved `claude` on a versionless alias and every report's
    container-facts line reads `harness claude exited 0` (`RESULT-56z.md`, `RESULT-yaz.md`,
    `RESULT-5gm.md`, `RESULT-0n3.md` — read the annotated commit, not the agent's own).

  The gap the acceptance item fell into is also already closed on `main` by this epic's own merge:
  `claudeSubNote` ("finding: the step-down's reason was a console line only") now puts
  `claude_sub: {state, label, reason, retry_at}` on the handle and into the run feed as
  `StageClaudeSubSteppedDown`.

## What is left open

Nine backlog ticks the run already promoted, re-read here and not re-filed:

| tick | sev | what |
|---|---|---|
| `9sy` | high | the container hook and `worker-collect.ts` refuse the `.tick/learnings.md` the Go boundary exempts, so a cloud close-out cannot write the file its role names. Still true; this record is the workaround |
| `hbw` | medium | the guide says "this repository has one Worker, one config"; `cloudflare/staging/wrangler.toml` also binds `CLAUDE_SUB_POOL`. The command it justifies is right |
| `gv3` | medium | `claude-sub.ts:30-32` still says "Production does none of these"; `wrangler.toml:166` binds the pool and `index.ts:2006` exports the proxy. Verified still wrong at the integration head |
| `z38` | medium | the guide names `CLAUDE_SUB_MAX_CONCURRENT` but not `[configs.claude.tier_policy.concurrency]`, which is what bounds how many claude workers run at once |
| `2x8` | medium | review and close-out containers get a depth-1 checkout, unannounced; `git fetch --unshallow origin` fixes it in one command (it did, here) |
| `am6` | low | the boundary banner calls any refused `tk` invocation an attempted write; `56z`'s fired for `tk tree ilz`, which is not a subcommand `tk 0.32.0` has |
| `hyw` | low | no run-level attempt record names the subscription a lease billed. The merge added `claude_sub` to the executor's own record (`cloudflaresandbox/record.go:210,214-221`), but `.ticfac/runs/*/attempts/*.json` is written **before** the dispatch (`reconcile/dispatch.go:49-57`), so it structurally cannot carry it — the remedy site is not where the tick points |
| `55q` | medium | **obsolete.** It asked for a lease TTL above a worker's 8h wall clock; `LEASE_REFRESH_MS` on `main` is that fix, and the TTL now measures silence. Close it rather than work it |
| `k40` | low | **premise moved.** It asked for a test pinning stale-lease reassignment; with the refresh, a live job's lease no longer goes stale, so what wants pinning is the refresh |

Two things nothing has answered, reported as findings from this close-out:

- the three stale guide claims above, which break [A1] on the head that merges to `main`;
- that nothing re-opens a prose claim when a merge rewrites the file it was checked against. The
  integrated gate ran on `bfedc21` and passed, because no check reads a document.

## What the retro changes in `.tick/learnings.md`

Three lessons, each folded into an existing entry rather than added beside it — the file is at its
150-line hard cap, so this compaction removes as much as it adds (it merges the two pi/GLM gateway
entries, folds the redirect-test gotcha in beside them, moves the lone "Waiting and watching" entry
into Orchestration, and merges the tracker-state entry into the boundary entry).

- **Planning an epic.** ilz's [A4] named the run's *events* — a Worker log nobody can read
  afterwards. An acceptance item about a RUN names the PERSISTED record that answers it: a handle
  field, an attempt record. Never a log line.
- **Reviews and repairs.** The guide was verified claim by claim and then had `main` merged under
  it, and three claims went stale on the shipping head with every gate green, because prose has no
  test. A CLAIM, like a tree state, is accepted on the INTEGRATED head; a doc quotes the expression
  behind each number it states (round 1's worst defect was a `??` chain's fallback read as its
  `Math.max` floor — 1s where the pool means 60s); and a merge of `main` re-opens every claim about
  a file it touched.
- **Orchestration / boundaries.** Attempt 5 burnt a dispatch on `no-commits` because the container
  hook refuses the one path the collect allows. Every enforcer of a boundary — hook, sweep, banner,
  `worker-collect.ts` — reads `ExemptFromBoundary()`; and a close-out COMMITS its learnings.

## Appendix A — `.tick/learnings.md`, compacted, ready to apply

Replace the file with the following, verbatim. It is 150 lines, the hard cap exactly.

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

**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an "in flight"
state outlived its writer. **Rule:** The merge gate is the only test of a shared contract; settle
in-flight state from durable evidence, never by trusting the claimer.

**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**
A hold that fires when the system does its job (finding things) makes "unattended" impossible; put
it where a person already is (the PR). A resume replays a recorded decision, never re-buys one.

**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify
rejected "no-commits"; ilz's kzs lost one reporting without committing. **Rule:** Every honest answer
needs a terminal verdict; a verifier commits evidence, and a close-out COMMITS its learnings.

**Problem:** `tk close` usage prints; promotions pointed at uncommitted ticks. **Rule:** Usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.

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
by claim, it then had `main` merged under it (+503 lines of claude-sub.ts) and three failover claims
went stale on the head that ships — every gate green, because prose has no test. **Rule:** A repair or a
CLAIM whose effect is a TREE state is accepted on the INTEGRATED head, never the branch; a doc QUOTES
the expression behind each number it states; a merge of main re-opens every claim about a file it touched.

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

**Problem:** A 1,000,000 max-output drew a bodyless 400 read as context overflow; 8,192 truncated GLM;
GLM via `cloudflare-workers-ai` leaked `<think>` tags; 648 showed the gateway needs `/workers-ai/v1` and
passes the caller's own `Authorization` upstream; a login-route test read the login page's own 401,
because `SELF.fetch` FOLLOWS a 303. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven; change one
variable at a time; off-vendor set `compat.thinkingFormat`; verify a route by its LOG ROW; a
redirect-asserting route test passes `redirect: "manual"`.
```
