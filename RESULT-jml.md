<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ymf/attempt-7/jml`, base `92cac3b93bb21b7fc907e60fc7ac6014db7daa48`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# Epic ymf closed out: the watch answers the operator's three questions

The close-out of epic `ymf` (tick `jml`, attempt 7, run
`run_6a569ee0f05740ec88858323a007661f`, which took over the failed
`run_91f2952af63f42f08a0ad243b4488842`, which took over `run_1a8cce01b53348ef83b341f36f1499bf`).
It is written from this checkout of the epic's integrated tree, the three runs' records under
`.ticfac/runs/`, and the ticks' own records and decision envelopes — never from memory of the plan.
The full retro is committed at `docs/ymf-closeout-retro-2026-10-09.md`; this report carries its
essentials and the typed findings the run acts on.

## What the epic set out to do, and what it delivered

The operator's remark of 2026-10-08 — "frankly i don't think the tui is very intuitive/clear" —
became the dashboard redesign of `docs/design/watch-redesign-2026-10.md`: needs-you first, one
health + N-of-M + ETA line, one epic phase track with a you-are-here marker, ticks grouped
NOW/DONE/UP NEXT/HELD with plain status words, a live one-line activity excerpt per running worker,
exceptions inline only when they happen, mechanics behind keys, readable feed sentences, cost only
when metered and subscription window use on a claude-sub run, the drill-down (enter) and events (e)
views kept working, and the four open watch defects t0y, dfb, 4dn, zrl folded in.

Re-verified against the tree at this close-out, item by item:

- **[A1] MET in the tree, with one live contradiction the final review judged blocking (kv3).**
  All ten golden frames are committed
  (`internal/cli/testdata/watch_scenario_{fresh,busy,held,landed,failed}_{80,120}.txt`),
  byte-compared by `TestDashboardScenarioGolden` in the short gate, which this close-out ran green;
  `TestDashboardScenariosAnswerTheThreeQuestions` holds each frame to the three questions in order.
  The exception: over `tui/`'s fixture world the honest frame reads `● stopped: … · 2 of 4 done`
  above `UP NEXT (4) t01 … t04` with t01 and t02 closed — reproduced here with the real binary
  (`ticfac watch --json epic-hld`: t01 state `closed`, status `up next`, `groups.up_next` all four,
  `progress.ticks.closed` 2). A tick whose only closure evidence is the tracker gets the wrong
  word and the wrong group while the health line counts it done. That is kv3, open, and it is why
  the final verdict is NOT READY.
- **[A2] MET.** 93n's per-worker activity derivation with its age; the busy golden renders
  `"adding the /feed handler"` at 19m and `"running pytest"` at 8m.
- **[A3] MET.** No TIER/ATTEMPTS column anywhere; the one retry reads inline as
  `writing code (attempt 2, model escalated)`, and every other row carries nothing.
- **[A4] MET.** The `─ latest ─` sections read `13:08 v1g merged into the epic, now testing`; a
  test fails when a new feed stage has no sentence; raw events stay on [e].
- **[A5] MET.** The landed golden carries `config claude · cost Workers AI $0.41`; the busy
  claude-sub golden carries `config claude · MAX1 · 34% of 5h`; the fresh, held and failed goldens
  carry no cost line.
- **[A6] Fixed in the tree, with tests, but the tracker still shows all four open.** Re-verified:
  `TestTheDashboardCountsDuplicatesTheWayTheHealthLineDoes` (t0y),
  `TestTheHoldAlertWordingsReadANilCommandAsNoCommand` (dfb and the stream-path echo ugt), the 4dn
  clamp (`TestAnEndedRunLeavesItsDispatchedRowUnmeasured`, with the pinned contract re-cut at
  bundle 2.7.0 to state the rule), `TestNarrowChildRowsAlignWithTheirParent` proving zrl's
  manifestation gone in the grouped layout. All four records still read status "open" with only
  the 2026-10-08 planning note — pzi's finding, an open tick; against the tracker A6 reads false.
- **[A7] MET as the branch carries them.** All five 120x40 screenshots are committed under
  `docs/design/watch-redesign-2026-10/` — fresh, busy, held, landed, failed — so PR #272 shows
  them with the branch; the retro links them for the operator's eye.
- **[A8] MET at this head, re-run here.** `make gate` exit 0 (gofmt, vet, the short suite over 50
  packages); `make bombadil` exit 0 (honest 2/2 over the real binary on a real pty);
  `make bombadil-seeded` exit 0 (8 of 8 broken programs fail as they must). Every merged tick's
  integrated gate (go, go-touched, ts) passed on its own merge; the round-2 review verified CI
  green on `epic/ymf@35a5caa9` (run 37930297384) and this run's checkpoint admits the close-out on
  "CI is green on the epic PR #272".

The epic's record states its eight [A<n>] marks on one line, so the machinery parses exactly one
item of record — A1 — and A2–A8 are references nothing can key on (the round-1 review hit this
live; tick baq, open). The re-flow travels as a `tracker_edit` finding below, applied by the run.

## How the run went

Three incarnations, one integration branch. `run_1a8cce01…` stopped after dispatching 47j and 93n;
`run_91f2952…` took over both and closed the status-model wave — 47j (feed sentences, `1e6ca05c`),
93n (worker excerpts, `a69efe51`), b13 (the cost line, `39a19d87`, three attempts and six failed
resolve-conflict jobs), lck (the status words and groups, `76af4578`) — then failed on ugm with
nothing collected. This run took over ugm's claim, folded the base itself (decision 1:
`daffbf60a` → `ac181a94`, the contracts conflicts resolved, the bundle re-cut to 2.6.0), and closed
the rest: ugm (the dashboard and goldens, `52d7cced` after one plan-repair), jym (the four defects,
`6e95c4c2`), review 8t5 round 1 — **NOT READY**, the bombadil suite still pinning the old
dashboard — whose blocking finding was absorbed as **vii** (re-pointed `tui/`, `35a5caa9`, try 1 a
missing-result whose work was carried), and review 4oj round 2 — **NOT READY** on kv3. Every
dispatch was a hosted pi-durable conversation on Workers AI GLM; the reviews and the strong-tier
retry on `glm-5.3`, the rest on `glm-5.3-flash`.

## What the run absorbed

Twelve decisions under `.ticfac/runs/run_6a569ee0f05740ec88858323a007661f/absorptions/`, all named:
one `reviewer` (the round-1 bombadil finding → vii, placed before the re-review), one
`worker-asserted-high` deferred to the reviewer (kv3 — round 2 reproduced it and named it
blocking, but that round spent the review bound, so nothing was absorbed past it), ten
`backlog-default` (h4u, ugt, c3i, zvj from ugm; 5u1 from jym; ijh, pzi, baq from round 1; xbf
from vii; k6z from round 2) — each an open backlog tick with an owner, listed on the epic PR. The
failed predecessor run's seven promoted findings (pl5, pta, 0by, r58, rpc, bt4, lgs) ride the same
PR. No worker-proposed amendment is filed with this run, and `ymf.json` carries no notes at all:
nothing needs confirming or marking unconfirmed.

## What is left open

1. **kv3, and the person's three options.** The run does not merge an epic its final review judged
   NOT READY: its landing hold names decision 8 after 2 rounds, the bound being 2. Fix kv3 (the
   review's stated rule: a tick whose state is closed with no readable records reads done/merged
   and groups DONE) and run the epic again; merge PR #272 by hand to accept it as it stands; or
   close the epic with the defect standing. k6z (open) names the property the suite should gain
   so this class cannot stand green again.
2. **A blocking finding adopted at the bound's last round.** kv3 was reported inside the epic's
   window, deferred to the reviewer by the 2026-10-06 policy, and named blocking on the bound's
   final round — where nothing is absorbed. The run ends held over a defect it could have fixed
   in the same shape vii fixed round 1's. Whether such a finding should buy the round its fix
   needs is an operator question on the machinery; filed below.
3. **A6 against the tracker.** t0y, dfb, 4dn, zrl — and the echoes filed while fixing them (ugt,
   zvj) — are open records whose content the tree already delivers. pzi names the four; closing
   them is a person's move.
4. **The acceptance criteria parse as one item** — baq (open); the re-flow below is the run's to
   apply.
5. **The backlog.** Eighteen open backlog ticks across the two runs that reported findings,
   listed on the PR.
6. **The learnings destination** (tick 3hw, open): a cloud close-out still cannot commit
   `.tick/learnings.md` — the container's pre-commit hook refuses every `.tick/` path — so the
   compaction rides the same channel bo9's did, a `protected_change` the run applies as its own
   labelled commit for the merger to review.

## The learnings

One new Problem → Rule pair (a screen that renders two facts from one model — a count and a
grouping — gets a property pinning their agreement, with a seeded program that breaks only that
claim; kv3 stood green on the watch suite's own honest frame), two rules extended (the prose-data
rule gains ONE ITEM PER LINE — ymf's eight marks on one line parsed as item A1 alone all epic, and
ex6's four had done the same; the render-state rule gains the suite half — ymf shipped its goldens
and left `tui/`'s pty suite pinning the old screen, so CI ran red from the first merge while the
per-tick gate stayed green and the final review was CI's first reader), four same-family merges
out, at the file's 150-line hard cap. The whole new file is the `protected_change` below.

## The next feasible epic

**1lq, "Halve the internal/reconcile test suite"** — planned 2026-10-08 from t8u's final review
and the operator's re-scope, its items already one per line, its two structural ticks (9xx, kf1)
unblocked with the review (yet) and close-out (vkh) behind them, and its before recorded in the
record itself (t8u's baseline: 293 serial minutes). dm6 (priority 1) needs the operator's own
GitHub App clicks before any run can prove its items; dha's premise predates umq's cutover and
still wants the re-read ex6's retro asked for.

This close-out leaves the retro document in the tree and nothing else: the record is the delivery.
The tracker edit and the learnings compaction travel as findings, applied by the run; the
implementation is untouched here.

```findings v2
[
  {
    "kind": "defect",
    "title": "Re-flow ymf's acceptance criteria so [A1]..[A8] parse as eight items",
    "severity": "low",
    "body": "The epic's definition of done states its eight [A<n>] marks on one line, and internal/acceptance's parser takes a bracketed [A<n>] only at a line's start (a mid-line bracket is a reference, never a definition), so the record carries exactly one item of record — A1 — and [A2]..[A8] are references nothing can key on: no command can bind evidence to them and a finding cannot name the item it breaks. The round-1 review hit this live (its blocking finding could claim A8 only in prose) and tick baq, still open, records it. The tracker edit this finding carries re-marks the same eight items one per line — the same words, nothing dropped — so the machinery the epic's close-out feeds (the epic PR's done-evidence section, the [evidence.acceptance] table, a future finding's breaks claim) can address all eight.",
    "evidence": ".tick/issues/ymf.json acceptance_criteria (one line, [A1]..[A8]); internal/acceptance/acceptance.go attemptedMark ('^[ \\t]*\\[A([^\\]]*)\\]', line-anchored)",
    "tracker_edit": {
      "tick": "ymf",
      "field": "acceptance_criteria",
      "value": "[A1] For the five scenarios in docs/design/watch-redesign-2026-10.md (fresh run, busy wave, held tick, ended landed run, ended failed run) the dashboard renders through a terminal emulator at 80x24 and 120x40 with: the needs-you line first; health + N of M done + ETA on one line; the phase track; ticks grouped by state with status words (no unlabelled symbol); golden frames committed and checked by tests in the gate.\n[A2] Each running worker row shows a one-line excerpt of its latest activity, local and cloud.\n[A3] No TIER/ATTEMPTS columns; a retry or escalation shows inline only when it happens.\n[A4] The recent-activity section shows readable sentences (no shas); raw events remain on [e].\n[A5] Cost shows only when metered; a claude-sub run shows subscription window use.\n[A6] t0y, dfb, 4dn and zrl are fixed.\n[A7] Screenshots of the five scenarios at 120x40 are attached to the epic PR.\n[A8] make gate passes and CI is green."
    }
  },
  {
    "kind": "proposal",
    "title": "Apply the ymf close-out retro: the compacted .tick/learnings.md",
    "severity": "medium",
    "body": "The ymf close-out's retro, as the whole new .tick/learnings.md. It is a compaction, not an append: the file is at its 150-line hard cap, so one new Problem → Rule pair goes in (a screen that renders two facts from one model — a count and a grouping, a word and a state — gets a property pinning their agreement, with a seeded program that breaks only that claim; kv3 stood green on the watch suite's own honest frame), two rules gain the halves ymf paid for (the prose-data rule gains ONE ITEM PER LINE — ymf's eight marks on one line parsed as item A1 alone all epic, ex6's four had done the same; the render-state rule gains the suite half — ymf shipped its goldens and left tui/'s pty suite pinning the old screen, so CI ran red from the first merge while the per-tick gate stayed green), and four same-family merges come out (the reproduce/shape pair, the 6in/promotion pair, the two fixture-identity entries, the two small boundaries entries), at exactly 150 lines. A cloud close-out still cannot commit the file itself (tick 3hw, open: the container's pre-commit hook refuses every .tick/ path), so it rides this protected change as bo9's did.",
    "protected_change": {
      "path": ".tick/learnings.md",
      "content": "# Learnings\n\nRepo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.\nSeeded from ticks' learnings 2026-09-02; last compacted at the bo9, t8u (2026-10-08) and ymf (2026-10-09) close-outs.\n\n## Planning an epic\n\n**Problem:** EIGHT epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6's `ticfac watch`, 43y's [A4]/[A2], v5t's\n[A1], bo9's [A1] — all verified by READING the hops, no factory reachable; 43y's and v5t's COULD not: the factory deploys main only). v5t's\n6fv tested its door with WORKER_AGENTS unbound, which production binds (yhe). **Rule:** When an epic's gate is a run or a view, the FIRST\ntick wires the thinnest path through the PRODUCTION entry point, its tests binding what production binds, and a named tick INSIDE performs\nit; a hop-by-hop READ is not that run.\n\n**Problem:** t8u's [A1] asked for the full suite halved \"on a quiet host\" with no BEFORE recorded and no tick on its floor\n(internal/reconcile, 41m serial): its ticks cut the per-tick gate 452s → 162s uncached while reconcile moved ~13%, and no review could time\na quiet host (load 8-17). **Rule:** A speed epic records its before at PLANNING — the command, the figure, a measure this shared host can\ngive (CI step durations, a serial test-elapsed sum) — and puts a tick on the measured floor first.\n\n**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads was prose. 0ek's four decisions are prose\ntoo — one NARROWED its own tick — because the envelope's `decisions` field has no report block filling it; ymf's eight [A<n>] marks sat on\nONE line, so the done parsed as item A1 and [A2]..[A8] stayed unaddressable all epic (ex6's four marks did the same). **Rule:** An\norchestrator epic names the NEXT run on the rebuilt binary as its demo; [A<n>] items AND a command per item at planning, ONE PER LINE. A\nNARROWED scope is typed, or unseen.\n\n**Problem:** hn6 planned 9 ticks and closed 65: ten repairs each fixed ONE pairing of which word wins; ymf redesigned the dashboard and left\ntui/'s pty suite pinning the old screen — every CI run on the branch red from the first merge while the per-tick gate stayed green, and the\nfinal review was CI's first reader. **Rule:** An epic that renders state lists the state matrix (running, held, stopped, failed, prior,\ncloud, local …) × every surface, AND every suite that asserts each surface — gate or CI-only, its re-pointing a tick in scope — at planning,\nONE precedence table tested over it.\n\n**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick (16 ticks, 7 resolves, a silent same-function collision, 7o5);\nv5t's two became y38 (Go) and yhe (TS) on ONE step-down seam, plus a duplicate high finding (bw7). **Rule:** A reviewer reports ONE blocking\nfinding per SEAM, naming every side of it: the run makes one tick per finding, so the report is the only fold there is.\n\n**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a tier overlay replaced the model; yoh keyed the\nWorkers-AI rule on the substrate, not the executor (78v). **Rule:** Check a policy on the FINAL resolved value, keyed on what crosses the boundary.\n\n**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; hn6's wave 1 HAND-TYPED goldens for values wave 2 could never\nproduce; bo9's rkk made `skills install|get`'s name optional while g5f, same wave, ADDED `skills diff` still demanding it. **Rule:**\nDECLARING and CONSUMING a vocabulary — or CHANGING a family's convention and ADDING to it — are different waves; fixtures check the builder.\n\n**Problem:** l6t deleted the wave path and four things only it produced; after 43y's jhp deleted the pi\nCLI, six ticks chased kind \"pi\" (twa's would have killed every cloud container at boot). **Rule:** A\ndeletion tick first LISTS every effect and pointer of the path in BOTH repos (grep the NAME), each owned.\n\n**Problem:** dz1's re-run held on the claim its STOPPED predecessor left. **Rule:** An enforcement\nmoving into this repo brings its exemptions; test resume and re-run, not only the refusal.\n\n## Orchestration\n\n**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one file. **Rule:** Name the SHA; check\n`--is-ancestor`. Two additions to one file are a union in INTENT, not text: a worker holding the context resolves it. ONE owner per artifact.\n\n**Problem:** A worker committed tracker state its prompt forbade. **Rule:** A boundary the substrate\ncan enforce must not rest on instruction-following — make it impossible and REPORT every attempt.\n\n**Problem:** FOUR of bo9's six ticks carried \"BOUNDARY VIOLATION ATTEMPTED — a model ignored an explicit instruction\"; only 0ek's `tk\ndecide` was a write: the guard matches `$1` alone, so the read-only `tk skills diff ticks` read as a violation. **Rule:** A refused READ is\nnot a violation: track tk's read vocabulary, match the SUBCOMMAND past global flags, banner WRITES only — or the one real attempt is lost\nin the noise.\n\n**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an \"in flight\" state outlived its writer. **Rule:**\nThe merge gate is the only test of a shared contract; settle in-flight state from durable evidence, never by trusting the claimer.\n\n**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**\nA hold that fires when the system does its job (finding things) makes \"unattended\" impossible; put\nit where a person already is (the PR). A resume replays a recorded decision, never re-buys one.\n\n**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify rejected \"no-commits\"; `tk close` usage\nprinted where promotions pointed at uncommitted ticks. **Rule:** Every honest answer needs a terminal verdict; a verifier commits evidence;\nusage is a REFUSAL; a promotion is finished when the tick is COMMITTED.\n\n**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches); twa's (43y) and\ntda's (v5t) runners.cloud.toml cells sat outside the boundary (7vp, lxo). **Rule:** A tick lives where its\nCODE is AND its worker can write: check every acceptance artifact against exemptFromBoundary at PLANNING;\nanother repo's change is an `upstream-tick`; an exception or routing cell is a named operator step.\n\n**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`);\nhost `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and host\ngit state never reach the run: fetch into a per-run ref (`--no-write-fetch-head --refmap=`), and state the run's own git environment (`-c rerere.enabled=false`).\n\n**Problem:** epic-hn6, restarted on its id after a SIGINT, read `phase: cancelled`: a restart wrote no\n`resumed` line, so the old `run_died` stayed its last word. **Rule:** Every incarnation states itself\nin the feed; a reader of \"the last terminal line\" is only as true as the writer's resume line.\n\n## Waiting and watching\n\n**Problem:** Blind `sleep 300` loops; hand-rolled watchers; `pgrep -f` read ALIVE by matching its own argv; PR #11's CI called\n\"unsatisfiable\" before its checks existed. **Rule:** Wait on a CONDITION over durable evidence, a push stream or a held PID's exit,\ncovering every terminal state; no `tail`/`head` in a watcher pipeline; absence right after creation is \"not yet\" — bound it to APPEAR.\n\n## Reviews and repairs\n\n**Problem:** A regression test \"failed before the fix\" only on an unrelated assertion; a defect that is a SHAPE was repaired one site at a\ntime (dyo Go, 94u TS); nine hn6 repairs fixed a printed \"clear with\" command that could not clear its hold. **Rule:** REPRODUCE a reported\ndefect at the base and read WHICH assertion fails there; a repair names EVERY implementation of the seam, leaves a guard test and reproduces\non the old code; a command printed for a person comes from ONE builder and a test PARSES it with the real CLI.\n\n**Problem:** 06t deleted stray RESULT-*.md reports and passed its gate; the integration merge (keepReportsOut) restored all seven; only round two's\n`git ls-tree epic/hn6` saw it (obk). **Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.\n\n**Problem:** A boot step past its retries escaped supervisePass. **Rule:** Every ending is\nfinalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.\n\n**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the branch it gates — and bo9's epic head sat\n60 bookkeeping commits past the newest CI run. **Rule:** Key evidence by the SOURCE (tree minus the run's own path); a close-out reads CI on\nthe last CODE-BEARING commit and PROVES the remainder code-free (`git diff --stat <sha>..HEAD -- . ':(exclude).tick' ':(exclude).ticfac'`).\n\n**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed at base in 18 ticks; one 43y 120s\nharness bound was filed five times (h3c 7oy 4ao omq 30e). **Rule:** A gate verdict is about the tree only if the host is bounded\n(registrytest.GuardMain); before filing \"fails at base\", grep `.tick/issues/` — the run must dedupe on the test id.\n\n**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; yoh's ts gate ran no vitest; dz1's, 43y's (enb, 4w7)\nand t8u's (p4c: Apple git 2.50 vs CI's 2.55) red epic CI sat unread until a review; z7w's -short properties doubled internal/cli's wall,\nunseen. **Rule:** A test the gate does not run is not evidence: add it or name the gap. A NEW package's suite joins `[testing.commands]` in\nits tick; a tick touching dispatch, claims or a profile runs `./internal/reconcile/`. A test of git's OWN behaviour (hooks, ref\ntransactions) is green only on CI's git; a tick adding tests reports its package's -short wall.\n\n**Problem:** The re-pointed watch suite pinned the dashboard's wording, order and pane fit, and held green over a frame whose health line\nread \"2 of 4 done\" while UP NEXT held all four ticks — kv3, ymf's round-2 blocker, stood green on the suite's own honest frame. **Rule:** A\nscreen that renders two facts from one model — a count and a grouping, a word and a state — gets a property pinning their AGREEMENT, with\na seeded program that breaks only that claim.\n\n**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI\ngreen (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.\n\n## Fixtures\n\n**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put work and report in ONE commit; v5t's\nGo executor said `cloudflare-workers-ai/…` where the door said `workers-ai/…`, each green on its own fake (y38); hn6's activity fixture\nspelled a claude worker's model `claude-opus-5` where real attempts say `opus`, so a live worker's activity was never read. **Rule:** A fake\ndemands the identity and reproduces the real shape; fix a forgiving fake in the same change; copy identity strings from a real record\n(`.ticfac/runs/*/attempts/*.json`), never type them. A TS↔Go seam gets a parity guard over its spellings and one Go test driving the REAL\ndoor; \"first try\" is `$TICFAC_TRY`.\n\n## Boundaries this repo pays to learn\n\n**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract; a login-route test read the login page's own 401 because\n`SELF.fetch` FOLLOWS a 303. **Rule:** The config FILE's keys are the bundle's ($TICFAC_* is the operator-preference surface); a\nredirect-asserting route test passes `redirect: \"manual\"`.\n\n**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated GLM; GLM via `cloudflare-workers-ai`\nleaked `<think>` tags. dm2 blamed row-less pi runs on pi abandoning the stream; 648 showed pi drains it — the gateway passes pi's own\n`Authorization` upstream (a bad key fails 10000) and needs `/workers-ai/v1`. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven\notherwise; change one variable at a time; off-vendor, set `compat.thinkingFormat`. Verify a gateway route by reading its LOG ROW back, and a\ncause by REPRODUCING it.\n\n**Problem:** Three gate guards ran a repo script through a shell — a broken watchdog answered `ok (cached)`: go's test cache sees\nonly the test binary's own opens; a subprocess's never, and outside-module files not at all. **Rule:** A drift guard reads its\ninput in-process, inside the module, or it is not guarding (mbv); a host-tool guard (wirevocab × herdr) needs -count=1 somewhere.\n"
    }
  },
  {
    "kind": "proposal",
    "title": "A blocking finding adopted at the review bound's last round ends the run held",
    "severity": "low",
    "body": "ymf's round-2 review named kv3 blocking — a deferred finding, first named blocking there — and because that round also spent the bound of 2, answerNotReadyReview absorbed nothing and the run ends at a landing hold for a person, over a fix the run could have made in the same shape vii fixed round 1's (one status-word rule, one test). The bound's design reads a second NOT READY as \"a disagreement the run cannot settle by itself — another round is the same judgement asked again\"; round 2's was a NEW finding, not a re-asked question. Whether a review that adopts a deferred finding on its last round should buy the round the fix needs, or whether the hold is the intended cost of the deferral, is an operator decision on the machinery, made visible by this epic's outcome.",
    "evidence": "internal/reconcile/review_rounds.go:66 (maxReviewRounds) with answerNotReadyReview's spent() branch (review_rounds.go:527-532); .ticfac/runs/run_6a569ee0f05740ec88858323a007661f/absorptions/e740036384f66facdd6df48f826a9d5ce79abbd8f3978be771e23f438f99bbdf.json (kv3, basis worker-asserted-high, placement deferred-to-review)"
  }
]
```

STATUS: DONE_WITH_CONCERNS — the epic's final review (4oj, decision 8) is NOT READY on kv3, so the run will hold the landing for a person; a person must choose between fixing kv3 and re-running, merging PR #272 by hand, or closing the epic — and the four fixed defect ticks (t0y, dfb, 4dn, zrl) are still open in the tracker (pzi)
