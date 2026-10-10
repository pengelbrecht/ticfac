<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-9/72z`, base `b31fb02f38f4410c8becc44a0406bbd96eabb5fb`, harness `pi-durable` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# Epic ozw closed out: the watch polish the operator accepted from ymf's merge

The close-out of epic `ozw` (tick `72z`, attempt 9, run
`run_1fac91062aa546f29910c5796a468976`). It is written from this checkout of the epic's integrated
tree — unshallowed against origin, so the branch's history, the five merge commits and the attempt
branches are readable — the run's records under `.ticfac/runs/`, the ticks' own reports
(`RESULT-az1.md`, `RESULT-cl7.md`, `RESULT-etl.md`, `RESULT-h3s.md`, `RESULT-7px.md`,
`RESULT-c1d.md`, `RESULT-z0b.md`), the tracker records and the activity feed: never from memory of
the plan. The full retro is committed at `docs/ozw-closeout-retro-2026-10-10.md`; this report
carries its essentials and the typed findings the run acts on.

## What the epic set out to do, and what it delivered

The four follow-ups the operator accepted when merging ymf (PR #272, 2026-10-10): the health line
contradicted the needs-you box; tick titles were truncated at 80 columns; the committed
screenshots showed no colour; the NOW/DONE/UP NEXT/HELD groups and the phase track ran together.
Five items, [A1]–[A5] — with the caveat that recurs from ymf: the record carries them on ONE line,
so the machinery parses exactly one item of record (A1) and [A2]–[A5] are mid-line references
nothing can key on (tick 1ip, open; the re-flow travels as a `tracker_edit` finding below). The
items are scored by the epic's own words, re-verified against the tree at this close-out:

- **[A1] MET.** The verdict is derived in the status model, so the terminal and the phone page
  render one word: `buildVerdict` (`internal/statusmodel/verdict.go:111`) returns `paused` with
  summary "needs you" whenever the attention list carries anything, stopped and degraded keeping
  their precedence. The held goldens read `● paused · needs you · 1 of 5 done · ~1h left` at both
  sizes; the contract bundle is re-cut at 2.8.0 with the word in `health_verdict.state`; the phone
  page agrees through `verdictWord`'s new arm. Pinned by three verdict tests, the "health line
  agrees with needs-you" property with its seeded breaker, and a cross-renderer phone-page test.
- **[A2] MET.** `dashRowColumns` gives the name the room to its natural width before the excerpt
  claims any of it (`internal/cli/watch_view.go:1034-1098`), and the row's status note renders in
  its compact spelling (`statusmodel.CompactException`: "attempt 2 · escalated"). The busy 80
  golden shows `order-feed service skeleton` whole; measured here, no line of any 80-column golden
  exceeds 80 cells. Pinned by the tick's own test, the re-pointed narrow-width ladder, and the
  `propTitleBeforeExcerpt` property with its seeded breaker.
- **[A3] MET as integrated — and the committed-evidence half was the round-1 blocker.** cl7's
  diagnosis, reproduced on the old files: the view never dropped its accents, the ymf capture ran
  without colour. It built the capture path that cannot silently lose them (`capture.sh`, the
  source writer asserted equal to the pinned plain goldens, the committed-PNG guard with its own
  anti-monochrome oracle) and re-captured the five. Three later merges then changed the pinned
  frames and nothing re-captured — the round-1 review found the committed set a frame behind this
  epic's own headline fix (the held PNG still reading `● healthy` in green under its needs-you
  box). The run absorbed that finding as 7px, which re-captured in place; round 2 verified the fix
  the strong way — a fresh capture from the integrated renderer is byte-identical to the committed
  set (sha256, per scenario) — and this close-out re-ran the committed-PNG guard green. The
  accent set is asserted cell by cell on the styled output (`TestWatchColourGridPerState`, with
  cl7's held-row red and etl's paused amber added).
- **[A4] MET.** One blank after the phase track, one between each adjacent group pair, UP NEXT the
  first thing that collapses before the spacing yields, the fold ladder renumbered with no stale
  caller. Measured here: the five 80x24 goldens run 24, 24, 23, 18 and 13 lines — all inside 24
  rows with the spacing standing. Pinned by three of h3s's tests.
- **[A5] MET at this head.** All ten scenario goldens plus the epic-state golden re-cut from the
  merged renderer (the az1/h3s conflict in three busy/held frames resolved by the resolve-conflict
  job as a regeneration, not a hand edit). Every merged tick's integrated gate (go, go-touched,
  ts) passed on its own merge — five evidence records in the run's state. This close-out ran the
  whole-repo gate fresh — `make gate` exit 0, gofmt, vet and the short suite over 50 packages —
  plus the committed-PNG guard and the scenario goldens. CI: the round-1 review read the forge
  green at every merge; round 2 read it green at `29d4f5e31390`, the head carrying the regenerated
  screenshots; the run's own checkpoint admits the close-out on "CI is green on the epic PR #273".

## How the run went

One incarnation, one integration branch, nine attempt dispatches plus one resolve-conflict job.
`epic/ozw` was cut from `f17d12208`, and its diff against that base is 36 files, +1232/−123. The
four implement ticks ran in parallel over the same renderer and the same goldens (the shared-
artifact hazard every one of their reports named), merged in sequence — cl7 `36834236b8c1`
(18:09), az1 `1aaa20c8e59e` (18:35, try 1 having settled rejected missing-result with its head
carried to the strong tier), h3s `f2472bf0140e` (19:26, through the resolve-conflict job), etl
`323f813550e4` (20:02) — then review c1d round 1 **NOT READY**, 7px (the absorbed blocker)
`29d4f5e31390` (21:25), and review z0b round 2 **READY**. Both review rounds are spent; the bound
is 2; the second verdict is READY, so nothing is held over the bound.

## What the run absorbed

Six findings, every one named in the run's `absorptions/`, none silent:

- **One on the basis `reviewer`** — the round-1 NOT READY named the stale-screenshots finding
  blocking, so it became the epic's work, fixed before the re-review: the finding → **7px**,
  re-placed `before-review` (it had first been decided `backlog-default`, the reporter naming no
  done item — and being unable to, which is 1ip's defect). The epic's shape changed once, from
  its own review, and the record says so.
- **Five on the basis `backlog-default`**, each a backlog tick with an owner, listed on the epic
  PR: cl7's freeze rendering finding → **2yn** (routed upstream, `charmbracelet/freeze` — a
  person's carry); az1's width-0 cap → **3j0**; the round-1 review's gate gap → **m8b**; the
  round-1 review's criteria parse → **1ip**; 7px's freeze pin → **s7a**.
- **No amendments**: no worker-proposed note is filed on the epic's record, and `ozw.json` carries
  no notes at all — nothing needs confirming or marking unconfirmed.

## What is left open

1. **Five backlog ticks, all open, all on the epic PR.** m8b — the gate gap: nothing ties the
   committed PNGs, or their sources, to the frames the goldens pin, so a stale capture with good
   colour passes `make gate`; the staleness class this epic paid a review round for can recur
   until the capture sources are committed and asserted. s7a — `capture.sh` recommends
   `freeze@latest`, unpinned (today's `freeze@latest` still reproduces the committed set byte for
   byte, per round 2). 3j0 — the width-0 branch caps the title column at 32 against its own
   contract comment, pre-existing at the base. 1ip — the criteria re-flow, which this report
   carries as its tracker edit. 2yn — the upstream carry to `charmbracelet/freeze`, a person's
   move.
2. **The criteria-parse defect recurred from ymf, and nothing enforces it** — the rule was written
   at ymf's close-out and applied to ymf's record the same day; ozw went one line again the next
   morning and spent the epic with [A2]–[A5] unkeyable, its round-1 review unable to name the item
   its blocking finding broke. The run's only reader of the criteria is the PR's done-evidence
   section, after the fact. Filed as a proposal below: the run that admits an epic whose marks
   parse as fewer items than it marks should re-flow or refuse.
3. **The known-failure in `internal/reconcile`, pre-existing and owned**: hai/e1k's claim-width
   hold, filed 2026-10-04 — reproduced identically at the base by the round-1 review and at its own
   base by etl's report.
   CI's go jobs are green at every merge of this epic. Not this epic's regression.
4. **The review bound is spent** — two rounds, ending READY. Nothing is held for a person by the
   bound; the epic lands on its own verdict.
5. **`72z.json` carries no description** — the close-out tick was created bare (the round-1 review
   noted it in passing: "After etl only the review (this tick) and a bare close-out remain, and
   72z carries no description naming the screenshots."). The stale-screenshots work still found
   an owner — the review's blocking finding, absorbed as 7px — but a bare close-out record is why
   the review had to be the one to say it: a close-out dispatched with no description inherits only
   the generic role prompt. Cosmetic, named for the record, not filed.

## The learnings

The compaction is the `protected_change` below — a cloud close-out still cannot commit
`.tick/learnings.md` (tick 3hw, open), so it rides the same channel bo9's and ymf's did. Two rules
gain the halves this epic paid for (the prose-data rule gains the enforcement point; the
gate-coverage rule gains committed rendered evidence as the same claim), and two same-family
merges come out, at exactly the file's 150-line hard cap.

This close-out leaves the retro document (`docs/ozw-closeout-retro-2026-10-10.md`) in the tree
and this report on the branch: the record is the delivery. The tracker re-flow and the learnings
compaction travel as findings, applied by the run; the implementation is untouched here.

```findings v2
[
  {
    "kind": "defect",
    "title": "Re-flow ozw's acceptance criteria so [A1]..[A5] parse as five items",
    "severity": "low",
    "body": "The epic's definition of done states its five [A<n>] marks on one line, and internal/acceptance's parser takes a bracketed [A<n>] only at a line's start (a mid-line bracket is a reference, never a definition), so the record carries exactly one item of record — A1 — and [A2]..[A5] are references nothing can key on: no command can bind evidence to them and a finding cannot name the item it breaks. This epic paid for it twice — its round-1 review's blocking finding (the committed screenshots, [A3]/[A5] material) carried no breaks claim, so the run's absorption had to fall back on the reviewer basis rather than a done-item claim — and it is the same defect the ymf review filed low, recurring one day after ymf's own record was re-flowed. Tick 1ip, open, records it. The tracker edit this finding carries re-marks the same five items one per line — the same words, nothing dropped — so the machinery the epic's close-out feeds (the epic PR's done-evidence section, the [evidence.acceptance] table, a future finding's breaks claim) can address all five.",
    "evidence": ".tick/issues/ozw.json acceptance_criteria (one line, [A1]..[A5]); internal/acceptance/acceptance.go:18 (attemptedMark is line-anchored)",
    "tracker_edit": {
      "tick": "ozw",
      "field": "acceptance_criteria",
      "value": "[A1] With anything in needs-you, the health line no longer reads healthy (e.g. 'paused · needs you').\n[A2] At 80x24 a tick title gets the width before the status: the busy scenario shows 'order-feed service skeleton' in full or nearly, the status shortens or wraps instead.\n[A3] The dashboard uses its colour accents on a colour terminal (green done, amber in progress, red failed/held, dim pending), and the committed screenshots show them.\n[A4] A blank line separates the groups and the phase track, within 80x24 for the five scenarios.\n[A5] Golden frames and screenshots updated; make gate passes and CI is green."
    }
  },
  {
    "kind": "proposal",
    "title": "A run admits an epic whose acceptance marks parse as fewer items than it marks",
    "severity": "medium",
    "body": "The one-item-per-line rule was written into the learnings at ymf's close-out and applied to ymf's own record the same day; ozw was planned one line again the next morning, spent the whole epic with [A2]..[A5] unkeyable, and its round-1 review could not name the item its blocking finding broke. The run's only reader of an epic's criteria is the PR's done-evidence section, after the fact (internal/reconcile/pr_reviewer.go:220); nothing checks the record when the epic is claimed or admitted, so the rule rests on the planner reading a learnings file at the moment of writing the record. The check is cheap — acceptance.Parse against the [A<n>] marks the text carries — and the repair channel already exists (the tracker edit): a run that admits an epic whose marks parse as fewer items than it marks should re-flow it or refuse, not blind its own evidence binding for the epic's whole life and repair the record at close-out.",
    "evidence": ".tick/issues/ozw.json acceptance_criteria (one line, [A1]..[A5]) against .tick/issues/ymf.json (re-flowed 2026-10-09, the day before ozw was created); internal/reconcile/pr_reviewer.go:220 (the only acceptance.Parse reader, post hoc)"
  },
  {
    "kind": "proposal",
    "title": "Apply the ozw close-out retro: the compacted .tick/learnings.md",
    "severity": "medium",
    "body": "The ozw close-out's retro, as the whole new .tick/learnings.md. It is a compaction, not an append: the file is at its 150-line hard cap, so two rules gain the halves this epic paid for — the prose-data rule gains the enforcement point (ozw's five marks went one line again the day after ymf's re-flow, and its round-1 review could key no item onto its blocking finding: the rule is enforced where the record is first read, not repaired at close-out), and the gate-coverage rule gains committed rendered evidence as the same claim (ozw's five watch PNGs aged under three green merges — the gate pins the frames they render, nothing tied the images to them — so an epic that names committed screenshots puts the re-capture in the tick that LAST changes a pinned frame, or asserts the capture sources in the gate) — and two same-family merges come out (the two boundary pairs into one; the dz1 resume pair into the hn6 restart pair it shares a family with), at exactly 150 lines. A cloud close-out still cannot commit the file itself (tick 3hw, open: the container's pre-commit hook refuses every .tick/ path), so it rides this protected change as bo9's and ymf's did.",
    "protected_change": {
      "path": ".tick/learnings.md",
      "content": "# Learnings\n\nRepo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.\nSeeded from ticks' learnings 2026-09-02; last compacted at the bo9, t8u (2026-10-08), ymf (2026-10-09) and ozw (2026-10-10) close-outs.\n\n## Planning an epic\n\n**Problem:** EIGHT epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6's `ticfac watch`, 43y's [A4]/[A2], v5t's\n[A1], bo9's [A1] — all verified by READING the hops, no factory reachable; 43y's and v5t's COULD not: the factory deploys main only). v5t's\n6fv tested its door with WORKER_AGENTS unbound, which production binds (yhe). **Rule:** When an epic's gate is a run or a view, the FIRST\ntick wires the thinnest path through the PRODUCTION entry point, its tests binding what production binds, and a named tick INSIDE performs\nit; a hop-by-hop READ is not that run.\n\n**Problem:** t8u's [A1] asked for the full suite halved \"on a quiet host\" with no BEFORE recorded and no tick on its floor\n(internal/reconcile, 41m serial): its ticks cut the per-tick gate 452s → 162s uncached while reconcile moved ~13%, and no review could time\na quiet host (load 8-17). **Rule:** A speed epic records its before at PLANNING — the command, the figure, a measure this shared host can\ngive (CI step durations, a serial test-elapsed sum) — and puts a tick on the measured floor first.\n\n**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads was prose. 0ek's four decisions are prose\ntoo — one NARROWED its own tick — because the envelope's `decisions` field has no report block filling it; ymf's eight [A<n>] marks sat on\nONE line, so the done parsed as item A1 and [A2]..[A8] stayed unaddressable all epic (ex6's four marks did the same; ozw's five went one\nline again the day after ymf's re-flow, and ozw's round-1 review could key no item onto its blocking finding). **Rule:** An orchestrator\nepic names the NEXT run on the rebuilt binary as its demo; [A<n>] items AND a command per item at planning, ONE PER LINE — enforced where\nthe record is first read, not repaired at close-out: a run that admits an epic whose marks parse as fewer items than it marks re-flows it\nor refuses. A NARROWED scope is typed, or unseen.\n\n**Problem:** hn6 planned 9 ticks and closed 65: ten repairs each fixed ONE pairing of which word wins; ymf redesigned the dashboard and left\ntui/'s pty suite pinning the old screen — every CI run on the branch red from the first merge while the per-tick gate stayed green, and the\nfinal review was CI's first reader. **Rule:** An epic that renders state lists the state matrix (running, held, stopped, failed, prior,\ncloud, local …) × every surface, AND every suite that asserts each surface — gate or CI-only, its re-pointing a tick in scope — at planning,\nONE precedence table tested over it.\n\n**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick (16 ticks, 7 resolves, a silent same-function collision, 7o5);\nv5t's two became y38 (Go) and yhe (TS) on ONE step-down seam, plus a duplicate high finding (bw7). **Rule:** A reviewer reports ONE blocking\nfinding per SEAM, naming every side of it: the run makes one tick per finding, so the report is the only fold there is.\n\n**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a tier overlay replaced the model; yoh keyed the\nWorkers-AI rule on the substrate, not the executor (78v). **Rule:** Check a policy on the FINAL resolved value, keyed on what crosses the boundary.\n\n**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; hn6's wave 1 HAND-TYPED goldens for values wave 2 could never\nproduce; bo9's rkk made `skills install|get`'s name optional while g5f, same wave, ADDED `skills diff` still demanding it. **Rule:**\nDECLARING and CONSUMING a vocabulary — or CHANGING a family's convention and ADDING to it — are different waves; fixtures check the builder.\n\n**Problem:** l6t deleted the wave path and four things only it produced; after 43y's jhp deleted the pi\nCLI, six ticks chased kind \"pi\" (twa's would have killed every cloud container at boot). **Rule:** A\ndeletion tick first LISTS every effect and pointer of the path in BOTH repos (grep the NAME), each owned.\n\n## Orchestration\n\n**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one file. **Rule:** Name the SHA; check\n`--is-ancestor`. Two additions to one file are a union in INTENT, not text: a worker holding the context resolves it. ONE owner per artifact.\n\n**Problem:** A worker committed tracker state its prompt forbade; bo9's guard matched `$1` alone, so FOUR of six ticks bannered \"BOUNDARY\nVIOLATION ATTEMPTED\" over the read-only `tk skills diff ticks`, leaving 0ek's one real write (`tk decide`) lost in the noise. **Rule:** A\nboundary the substrate can enforce must not rest on instruction-following — make it impossible and REPORT every attempt; a refused READ is\nnot a violation: match the SUBCOMMAND past global flags, banner WRITES only.\n\n**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an \"in flight\" state outlived its writer. **Rule:**\nThe merge gate is the only test of a shared contract; settle in-flight state from durable evidence, never by trusting the claimer.\n\n**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**\nA hold that fires when the system does its job (finding things) makes \"unattended\" impossible; put\nit where a person already is (the PR). A resume replays a recorded decision, never re-buys one.\n\n**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify rejected \"no-commits\"; `tk close` usage\nprinted where promotions pointed at uncommitted ticks. **Rule:** Every honest answer needs a terminal verdict; a verifier commits evidence;\nusage is a REFUSAL; a promotion is finished when the tick is COMMITTED.\n\n**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches); twa's (43y) and\ntda's (v5t) runners.cloud.toml cells sat outside the boundary (7vp, lxo). **Rule:** A tick lives where its\nCODE is AND its worker can write: check every acceptance artifact against exemptFromBoundary at PLANNING;\nanother repo's change is an `upstream-tick`; an exception or routing cell is a named operator step.\n\n**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`);\nhost `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and host\ngit state never reach the run: fetch into a per-run ref (`--no-write-fetch-head --refmap=`), and state the run's own git environment (`-c rerere.enabled=false`).\n\n**Problem:** dz1's re-run held on the claim its STOPPED predecessor left; epic-hn6, restarted on its id after a SIGINT, read `phase:\ncancelled` — a restart wrote no `resumed` line, so the old `run_died` stayed its last word. **Rule:** An enforcement moving into this repo\nbrings its exemptions; test resume and re-run, not only the refusal; every incarnation states itself in the feed — a reader of \"the last\nterminal line\" is only as true as the writer's resume line.\n\n## Waiting and watching\n\n**Problem:** Blind `sleep 300` loops; hand-rolled watchers; `pgrep -f` read ALIVE by matching its own argv; PR #11's CI called\n\"unsatisfiable\" before its checks existed. **Rule:** Wait on a CONDITION over durable evidence, a push stream or a held PID's exit,\ncovering every terminal state; no `tail`/`head` in a watcher pipeline; absence right after creation is \"not yet\" — bound it to APPEAR.\n\n## Reviews and repairs\n\n**Problem:** A regression test \"failed before the fix\" only on an unrelated assertion; a defect that is a SHAPE was repaired one site at a\ntime (dyo Go, 94u TS); nine hn6 repairs fixed a printed \"clear with\" command that could not clear its hold. **Rule:** REPRODUCE a reported\ndefect at the base and read WHICH assertion fails there; a repair names EVERY implementation of the seam, leaves a guard test and reproduces\non the old code; a command printed for a person comes from ONE builder and a test PARSES it with the real CLI.\n\n**Problem:** 06t deleted stray RESULT-*.md reports and passed its gate; the integration merge (keepReportsOut) restored all seven; only round two's\n`git ls-tree epic/hn6` saw it (obk). **Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.\n\n**Problem:** A boot step past its retries escaped supervisePass. **Rule:** Every ending is\nfinalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.\n\n**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the branch it gates — and bo9's epic head sat\n60 bookkeeping commits past the newest CI run. **Rule:** Key evidence by the SOURCE (tree minus the run's own path); a close-out reads CI on\nthe last CODE-BEARING commit and PROVES the remainder code-free (`git diff --stat <sha>..HEAD -- . ':(exclude).tick' ':(exclude).ticfac'`).\n\n**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed at base in 18 ticks; one 43y 120s\nharness bound was filed five times (h3c 7oy 4ao omq 30e). **Rule:** A gate verdict is about the tree only if the host is bounded\n(registrytest.GuardMain); before filing \"fails at base\", grep `.tick/issues/` — the run must dedupe on the test id.\n\n**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; yoh's ts gate ran no vitest; dz1's, 43y's (enb, 4w7)\nand t8u's (p4c: Apple git 2.50 vs CI's 2.55) red epic CI sat unread until a review; z7w's -short properties doubled internal/cli's wall,\nunseen. **Rule:** A test the gate does not run is not evidence: add it or name the gap. A NEW package's suite joins `[testing.commands]` in\nits tick; a tick touching dispatch, claims or a profile runs `./internal/reconcile/`. A test of git's OWN behaviour (hooks, ref\ntransactions) is green only on CI's git; a tick adding tests reports its package's -short wall. Committed RENDERED evidence is the same\nclaim: ozw's five watch PNGs aged under three green merges — the gate pins the frames they render, nothing tied the images to them — so an\nepic that names committed screenshots puts the re-capture in the tick that LAST changes a pinned frame, or asserts the capture sources in\nthe gate.\n\n**Problem:** The re-pointed watch suite pinned the dashboard's wording, order and pane fit, and held green over a frame whose health line\nread \"2 of 4 done\" while UP NEXT held all four ticks — kv3, ymf's round-2 blocker, stood green on the suite's own honest frame. **Rule:** A\nscreen that renders two facts from one model — a count and a grouping, a word and a state — gets a property pinning their AGREEMENT, with\na seeded program that breaks only that claim.\n\n**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI\ngreen (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.\n\n## Fixtures\n\n**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put work and report in ONE commit; v5t's\nGo executor said `cloudflare-workers-ai/…` where the door said `workers-ai/…`, each green on its own fake (y38); hn6's activity fixture\nspelled a claude worker's model `claude-opus-5` where real attempts say `opus`, so a live worker's activity was never read. **Rule:** A fake\ndemands the identity and reproduces the real shape; fix a forgiving fake in the same change; copy identity strings from a real record\n(`.ticfac/runs/*/attempts/*.json`), never type them. A TS↔Go seam gets a parity guard over its spellings and one Go test driving the REAL\ndoor; \"first try\" is `$TICFAC_TRY`.\n\n## Boundaries this repo pays to learn\n\n**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract; a login-route test read the login page's own 401 because\n`SELF.fetch` FOLLOWS a 303. **Rule:** The config FILE's keys are the bundle's ($TICFAC_* is the operator-preference surface); a\nredirect-asserting route test passes `redirect: \"manual\"`.\n\n**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated GLM; GLM via `cloudflare-workers-ai`\nleaked `<think>` tags. dm2 blamed row-less pi runs on pi abandoning the stream; 648 showed pi drains it — the gateway passes pi's own\n`Authorization` upstream (a bad key fails 10000) and needs `/workers-ai/v1`. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven\notherwise; change one variable at a time; off-vendor, set `compat.thinkingFormat`. Verify a gateway route by reading its LOG ROW back, and a\ncause by REPRODUCING it.\n\n**Problem:** Three gate guards ran a repo script through a shell — a broken watchdog answered `ok (cached)`: go's test cache sees\nonly the test binary's own opens; a subprocess's never, and outside-module files not at all. **Rule:** A drift guard reads its\ninput in-process, inside the module, or it is not guarding (mbv); a host-tool guard (wirevocab × herdr) needs -count=1 somewhere.\n"
    }
  }
]
```

STATUS: DONE
