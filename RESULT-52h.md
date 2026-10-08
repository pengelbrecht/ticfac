# 52h — close-out: operator surface follow-ups (epic bo9)

Read from `epic/bo9` against `990bb6eb` (the merge base with main, and the run's
recorded `source_sha`) and from the six ticks' own RESULT files on their
branches. 40 non-bookkeeping files, 55,286 insertions — 53,432 of them the one
generated file, `harness/embed/local-main.bundle.mjs`.

## What the epic set out to deliver, and what it did

Four deliverables, all four shipped, each by one tick merged into `epic/bo9`
with the integrated gate (go, ts) green:

| tick | delivered | commit |
| --- | --- | --- |
| 0ek | the local pi-durable harness as an esbuild bundle, `go:embed`ed, run with plain `node` | `c8e6310b0939` |
| ba4 | `ticfac run --cloud --config <name>`, carried through the factory submission | `3a69386864b3` |
| g5f | `ticfac skills diff` — an installed skill copy's drift from the binary | `868d88b524c0` |
| rkk | `skills install`/`get` default to the ticfac skill when no name is given | `e28a6094b699` |
| u2g | `skills get --full` prints SKILL.md and every `references/` file | `06d41537071b` |
| g2s | final review — verdict READY, six findings, all backlog | (report on its branch) |

rkk was not in the original plan: the operator added it on 2026-10-07 ("`ticfac
skills install ticfac` repeats itself — the binary embeds one skill"). It is the
epic's one scope addition.

## The acceptance criteria

The epic's tracker record carries its criteria as prose with no per-item marks,
so there is nothing to read back; this is my own assessment.

**[A1] `ticfac run <epic> --cloud --config claude` runs the epic on the claude
config — shipped, never run.** The chain is complete and tested at seven of its
eight hops (CLI refusal removed → submission field → `parseSubmission` →
Workflow params → `orchestratorEnv` → `TICKS_CONFIG` → `image/common.sh` →
`--config` on the `run-epic` argv), and g2s verified every hop by reading it,
stating plainly that it could not do otherwise because no factory was reachable
from the review launch. Nobody has performed the command the criterion names.
That is the eighth epic to close this way, so I am filing it rather than letting
the eighth instance pass unrecorded.

**[A2] `ticfac skills diff` reports a stale installed copy with the upgrade
command — holds.** Verified on a binary I built from this tree, in a throwaway
repo:

    $ ticfac skills diff ticfac          # fresh install
    no drift: matches the embedded bundle (version dev)                              exit 0
    $ ticfac skills diff ticfac          # stamp rewritten to v0.0.1
    drift against the embedded bundle: installed=v0.0.1 bundle=dev
    upgrade with `ticfac skills install ticfac`
    ticfac skills diff: drift detected — fix with `ticfac skills install ticfac`     exit 1

**[A3] `ticfac skills get ticfac --full` prints SKILL.md and every references
file — holds.** Same binary: SKILL.md leads, then `references/cloud.md`,
`references/commands.md`, `references/holds.md`, each under a `--- <path> ---`
header.

**[A4] a local pi-durable run outside ticfac with only the binaries and node;
a stale bundle fails the gate — holds.** 0ek's two tests are the criterion at
the only level a Go test reaches it, and g2s checked both halves by hand rather
than trusting them, including regenerating the bundle from the committed script
and getting a byte-identical file.

**[A5] `make gate` passes and CI is green on the epic PR — holds.** I ran
`GOTEST_PARALLEL=4 GOFLAGS=-p=2 nice -n 19 make gate` on this tree: **exit 0**
(gofmt, `go vet ./...`, the whole-repo short suite, `internal/reconcile` 13.5s).

CI, stated precisely because the literal reading is false: the newest CI run on
`epic/bo9` is green on `06d41537` — u2g's integration merge, and the **last
code-bearing commit on the branch**. The 60 commits between it and the epic head
(`0ceb60b`) are entirely `.ticfac/runs/` and `.tick/` records, which I checked
rather than assumed:

    git diff --stat 06d41537071b origin/epic/bo9 -- . ':(exclude).ticfac' ':(exclude).tick'
    # empty

So no CI run exists for the head sha, and the complete code diff is covered by a
green run. Five green CI runs cover the five code merges. The diff touches no
`internal/reconcile/`, so AGENTS.md's wait-for-PR-CI exception does not apply.
PR #267 is open, not merged, against `main` at `79405e74`.

## What is left open

**Already filed by the run.** g2s's six findings were all triaged `promoted` and
exist as committed, open backlog ticks: `w75` (no test covers `common.sh`
reading `TICKS_CONFIG`), `50j` (a queued cloud submission drops the named
config), `jtj` (`references/commands.md` never mentions `--full`), `avo`
(`--full`'s file order is lexicographic luck), `rgx` (`make build` now needs
node and pnpm), `359` (`main.ts`'s header documents the pre-bundle argv). None
blocking; nothing is waiting on a decision.

**Raised by a worker and unanswered until now.** ba4's report flagged the queued
`--config` path as deliberately out of scope — `50j` now covers it. 0ek recorded
four design decisions in its RESULT prose because it had no other channel (it
tried `tk decide` and the container refused it); one of them *narrowed its own
tick*, leaving `.goreleaser.yaml` and the image build untouched where the tick's
description named them as candidates. That narrowing is correct on its merits and
nothing downstream depends on it, but it is discoverable only by reading prose —
which is what finding 5 below is about.

**Found during this close-out** (findings 1–4): the A1 demonstration gap;
`skills diff` requiring the name its two siblings just stopped requiring;
`skills diff` missing from README's command table; and the boundary guard
reporting refused *reads* as attempted violations.

## Learnings

`.tick/learnings.md` is at its 150-line hard cap, so the retro is a compaction,
not an append: **32 Problem → Rule pairs in, 32 out**, 50 lines rewritten to make
room. I cannot commit it — the container's pre-commit hook refuses every staged
`.tick/` path, and `CloudBoundaryRefuses` (protected_change.go:119) says that is
deliberate on this substrate, learnings.md included. So the whole 150-line file
rides finding 6 below as a `protected_change` `content`, which the run applies
after this close-out's reads and lists on the epic PR. I verified it against the
cap (`wc -l` → 150) and the pair count (32 in, 32 out) before handing it over.

What bo9 changed in it:

- **The epic-gate rule gained its eighth instance and a new clause.** bo9's [A1]
  joins the list, with `a hop-by-hop READ is not that run` — the failure mode
  here was not a missing test, it was a complete verification that still wasn't
  the thing the criterion asked for.
- **A new Orchestration entry:** four of bo9's six ticks carried "BOUNDARY
  VIOLATION ATTEMPTED — a model ignored an explicit instruction"; only one was a
  write. A refused read is not a violation, and a banner that cries wolf three
  times out of four buries the one real attempt.
- **The evidence-keying rule now covers the close-out's CI gate:** read CI on the
  last code-bearing commit and *prove* the remainder code-free.
- **Two existing entries absorbed bo9 clauses:** prose-not-data now names a
  worker's narrowed scope; the vocabulary-waves rule now covers changing a
  command family's convention while a sibling adds to that family.
- **Merged** the two pi/GLM/gateway entries, which had split one cause.

Not recorded: that 0ek's bundle regenerates byte-identically, and that the three
same-file conflict resolutions lost nothing. Both are the machinery working.

## What I ran

| check | result |
| --- | --- |
| `GOTEST_PARALLEL=4 GOFLAGS=-p=2 nice -n 19 make gate` | **exit 0** |
| `go build -o /tmp/tfchk ./cmd/ticfac` | clean |
| `ticfac skills diff ticfac` — fresh, stale-stamp, not-installed | exit 0 / 1 / 1, upgrade command on each |
| `ticfac skills get --full` | SKILL.md + 3 references under path headers |
| `ticfac skills diff` (no name) | **exit 2**, "Exactly one skill name is required" — finding 2 |
| `git diff 06d41537..epic/bo9` minus `.tick`/`.ticfac` | empty (A5's CI reasoning) |
| GitHub API: `actions/runs?branch=epic/bo9` | 5 runs, all `success` |
| `wc -l` on the compacted learnings | 150 (the cap) |

No `gh` on this launch either, so CI came from the REST API directly. I did not
rerun the harness `pnpm` suites or the TS gate — no code changed in this tick.

## Findings

```findings v2
[
  {
    "kind": "proposal",
    "title": "A1 shipped without the run it names; no tick performed it",
    "severity": "medium",
    "body": "[A1] names a command - `ticfac run <epic> --cloud --config claude` - and nothing in the epic ran it. The review verified all eight hops by reading, saying so plainly because no factory was reachable from its launch, and seven of the eight hops carry tests. This is the EIGHTH epic to close without the run its acceptance names, and the first where a hop-by-hop read stood in for it, so the gap is a demonstration gap rather than a correctness one. Worth one tick that performs the command against the deployed factory once this merges, before --config on cloud runs is treated as exercised.",
    "breaks": {
      "item": "A1",
      "check": "ticfac run <epic> --cloud --config claude"
    },
    "evidence": "tick/bo9/attempt-6/g2s:RESULT-g2s.md - \"because no factory is reachable from here\"; .tick/learnings.md:8-13 records the seven prior instances"
  },
  {
    "kind": "defect",
    "title": "skills diff still demands the name install and get just dropped",
    "severity": "medium",
    "body": "rkk made `skills install` and `skills get` default to the ticfac skill because the operator said naming it repeats itself - the binary embeds one skill. g5f, merged in the same wave, ADDED `skills diff`, which still refuses the short form: `ticfac skills diff` exits 2 with \"Exactly one skill name is required.\" So the epic shipped a skills family where two of three commands take an optional name and the newest one does not. The docs are at least self-consistent (they teach `ticfac skills diff ticfac`), so this is a surface wart rather than a contradiction, and the fix is the argument-count change rkk already made next door.",
    "evidence": "`ticfac skills diff` on a binary built from epic/bo9 -> exit 2, \"Exactly one skill name is required.\"; internal/cli/skills.go - rkk's `[name]` shape on get/install vs diff's cmd.Args requiring exactly one"
  },
  {
    "kind": "defect",
    "title": "README's command table never mentions ticfac skills diff",
    "severity": "low",
    "body": "g5f added `skills diff` to SKILL.md and references/commands.md - which TestTheSkillAndTheCommandTreeNameEachOther forces - but not to README.md, whose command table still reads `ticfac skills list|get|install`. rkk edited that exact table row in the same wave for the short form, so the line was touched and `diff` was not added to it. `grep -c 'skills diff' README.md` is 0. The reciprocity test covers the skill bundle and not the README, which is why the gate stayed green; extending it would make this class of omission impossible rather than noticed.",
    "evidence": "README.md:124 (the table row, rewritten by rkk) and README.md:49; `grep -c 'skills diff' README.md` -> 0"
  },
  {
    "kind": "defect",
    "title": "The tk boundary guard reports refused READS as violations",
    "severity": "medium",
    "body": "Four of bo9's six ticks carried the container's \"BOUNDARY VIOLATION ATTEMPTED ... a model that ignored an explicit instruction is something a human has to see\" banner. Only 0ek's `tk decide` was a write attempt. g5f's was `tk skills diff ticks`, a read-only command the guard's pass list simply omits; rkk's `tk --json show rkk` and g2s's `tk note list g2s` were reads whose form the $1-only match cannot see past. The pass list omits every read-only subcommand group tk has grown (skills, decisions, frontier, board, roadmap). A banner that is wrong three times out of four stops being read, and the one real attempt - a worker burying four design decisions in prose because `tk decide` was refused - is what gets lost. Note the fix spans the bundle: boundary-guard.ts is under harness/src, so it needs `make harness-bundle`.",
    "evidence": "harness/src/env/boundary-guard.ts:51-56 and image/worker.sh:488-493 (the pass list, matched against ${1:-} only); the banners in tick/bo9/attempt-{3,4,6} RESULT files against `tk --help`'s command list"
  },
  {
    "kind": "proposal",
    "title": "The envelope's decisions field has no report block that fills it",
    "severity": "low",
    "body": "RoleResult carries `Decisions []Decision` and the contract bundle goldens an implement-tick envelope with a populated `decisions` array, but the report reader knows only a `findings` block and a `tracker-edits` block - so nothing can ever populate it from a worker's RESULT file. 0ek is the cost: it reached for `tk decide`, was refused, and wrote four decisions as prose under its own heading, one of which narrowed its tick's declared scope (goreleaser and the image build left untouched). A declared-but-unreachable field reads to the next worker as a channel that exists. Either give the report a `decisions` block beside `findings`, or drop the field.",
    "evidence": "internal/exec/subprocess/record.go:430 declares Decisions; contracts/job-protocol.json examples/golden/10 populates it; internal/exec/subprocess/lint.go:149-154 knows only the findings and tracker-edits blocks; record.go:475 is the only use, a pass-through"
  },
  {
    "kind": "proposal",
    "title": "Apply the bo9 close-out retro: the compacted .tick/learnings.md",
    "severity": "medium",
    "body": "The close-out's retro, as the whole new .tick/learnings.md. It is a compaction, not an append: the file is at its 150-line hard cap, so 32 Problem -> Rule pairs go in and 32 come out, at exactly 150 lines. bo9's [A1] becomes the eighth instance of the epic-gate rule, which gains \"a hop-by-hop READ is not that run\"; a new Orchestration entry records that four of six ticks' boundary-violation banners were refused READS rather than writes; the evidence-keying rule now covers a close-out reading CI on the last code-bearing commit; the prose-not-data and vocabulary-waves entries absorb a bo9 clause each; and the two pi/GLM/gateway entries merge, since they split one cause. I could not commit it: the pre-commit hook refuses every staged .tick/ path and protected_change.go's CloudBoundaryRefuses says that is deliberate here, learnings.md included.",
    "protected_change": {
      "path": ".tick/learnings.md",
      "content": "# Learnings\n\nRepo-specific gotchas, Problem → Cause → Rule. Hard cap 150 lines — compact every retro.\nSeeded from ticks' learnings 2026-09-02; last compacted at the v5t (2026-10-07) and bo9 (2026-10-08) close-outs.\n\n## Planning an epic\n\n**Problem:** EIGHT epics closed without the run their acceptance names (Phase 4, xte, yoh, 2jn, hn6's\n`ticfac watch`; 43y's [A4]/[A2]; v5t's [A1]; bo9's [A1], verified by READING all eight hops — no factory\nwas reachable). 43y's and v5t's COULD not: the factory deploys main only. v5t's 6fv tested its door with\nWORKER_AGENTS unbound, which production binds (yhe). **Rule:** When an epic's gate is a run or a view, the\nFIRST tick wires the thinnest path through the PRODUCTION entry point, its tests binding what production\nbinds (wrangler.toml), and a named tick INSIDE performs it; a hop-by-hop READ is not that run.\n\n**Problem:** gvc absorbed none of 21 findings; 2jn scored none of 30: the data the machinery reads was\nprose. 0ek's four decisions are prose too — one NARROWED its own tick — because the envelope's `decisions`\nfield has no report block filling it. **Rule:** An orchestrator epic names the NEXT run on the rebuilt\nbinary as its demo; [A<n>] items AND a command per item at planning. A NARROWED scope is typed, or unseen.\n\n**Problem:** hn6 planned 9 ticks and closed 65: ten repairs each fixed ONE pairing of which word wins\nacross nine runs. **Rule:** An epic that renders run state lists the state matrix (running, held,\nstopped, failed, prior, cloud, local …) × every surface at planning, ONE precedence table tested over it.\n\n**Problem:** 2jn's NOT READY review's ten findings each became a parallel tick (16 ticks, 7 resolves, a\nsilent same-function collision, 7o5); v5t's two became y38 (Go) and yhe (TS) on ONE step-down seam: a\nresolve and a duplicate high finding (bw7). **Rule:** A reviewer reports ONE blocking finding per SEAM,\nnaming every side of it: the run makes one tick per finding, so the report is the only fold there is.\n\n**Problem:** A policy held per layer and failed in the whole: xte checked the cloud overlay, then a tier\noverlay replaced the model; yoh keyed the Workers-AI rule on the substrate, not the executor (78v).\n**Rule:** Check a policy on the FINAL resolved value, keyed on what actually crosses the boundary.\n\n**Problem:** wne put a vocabulary (mrn) and its consumer (0ju) in one wave; hn6's wave 1 HAND-TYPED\ngoldens for values wave 2 could never produce; bo9's rkk made `skills install|get`'s name optional while\ng5f, same wave, ADDED `skills diff` still demanding it. **Rule:** DECLARING and CONSUMING a vocabulary —\nor CHANGING a family's convention and ADDING to it — are different waves; fixtures check the builder.\n\n**Problem:** l6t deleted the wave path and four things only it produced; after 43y's jhp deleted the pi\nCLI, six ticks chased kind \"pi\" (twa's would have killed every cloud container at boot). **Rule:** A\ndeletion tick first LISTS every effect and pointer of the path in BOTH repos (grep the NAME), each owned.\n\n**Problem:** dz1's re-run held on the claim its STOPPED predecessor left. **Rule:** An enforcement\nmoving into this repo brings its exemptions; test resume and re-run, not only the refusal.\n\n## Orchestration\n\n**Problem:** Wave-2 branched from a base missing wave-1; two same-wave ticks cut additions to one\nfile. **Rule:** Name the SHA; check `--is-ancestor`. Two additions to one file are a union in\nINTENT, not in text — hand the resolve to a worker holding the context. ONE owner per artifact.\n\n**Problem:** A worker committed tracker state its prompt forbade. **Rule:** A boundary the substrate\ncan enforce must not rest on instruction-following — make it impossible and REPORT every attempt.\n\n**Problem:** FOUR of bo9's six ticks carried \"BOUNDARY VIOLATION ATTEMPTED — a model ignored an explicit\ninstruction\"; only 0ek's `tk decide` was a write. The guard matches `$1` alone and omits tk's read-only\n`skills`/`decisions`/`frontier`, so `tk skills diff ticks` reads as a violation. **Rule:** A refused READ\nis not a violation: track tk's read vocabulary, match the SUBCOMMAND past global flags, banner WRITES only\n— or the one real attempt is lost in the noise.\n\n**Problem:** Parallel ticks sharing a return shape were green alone, broken together; an \"in flight\"\nstate outlived its writer. **Rule:** The merge gate is the only test of a shared contract; settle\nin-flight state from durable evidence, never by trusting the claimer.\n\n**Problem:** ncv stopped EIGHT times for untriaged findings; yoh needed a person ~20 times. **Rule:**\nA hold that fires when the system does its job (finding things) makes \"unattended\" impossible; put\nit where a person already is (the PR). A resume replays a recorded decision, never re-buys one.\n\n**Problem:** 6in's close-out took four attempts: a BLOCKED no rule disposed of (#117), a re-verify\nrejected \"no-commits\". **Rule:** Every honest answer needs a terminal verdict; a verifier commits evidence.\n\n**Problem:** `tk close` usage prints; promotions pointed at uncommitted ticks. **Rule:** Usage is a REFUSAL; a promotion is finished when the tick is COMMITTED.\n\n**Problem:** Ticks changing the ticks repo were undispatchable (ha9: SEVEN dispatches); twa's (43y) and\ntda's (v5t) runners.cloud.toml cells sat outside the boundary (7vp, lxo). **Rule:** A tick lives where its\nCODE is AND its worker can write: check every acceptance artifact against exemptFromBoundary at PLANNING;\nanother repo's change is an `upstream-tick`; an exception or routing cell is a named operator step.\n\n**Problem:** Two runs died with `conflict_exists` (a watcher's fetch rewrote the shared `FETCH_HEAD`);\nhost `rerere.enabled` replayed a person's resolve into a machine merge. **Rule:** Process-global and host\ngit state never reach the run: fetch into a per-run ref (`--no-write-fetch-head --refmap=`), and state the run's own git environment (`-c rerere.enabled=false`).\n\n**Problem:** epic-hn6, restarted on its id after a SIGINT, read `phase: cancelled`: a restart wrote no\n`resumed` line, so the old `run_died` stayed its last word. **Rule:** Every incarnation states itself\nin the feed; a reader of \"the last terminal line\" is only as true as the writer's resume line.\n\n## Waiting and watching\n\n**Problem:** Blind `sleep 300` loops; three hand-rolled watchers, each wrong; `pgrep -f` read ALIVE by\nmatching its own argv; PR #11's CI called \"unsatisfiable\" before its checks existed. **Rule:** Wait on a\nCONDITION over durable evidence, a push stream or a held PID's exit, covering every terminal state; no `tail`/`head` in a watcher pipeline; absence right after creation is \"not yet\" — bound it to APPEAR.\n\n## Reviews and repairs\n\n**Problem:** A regression test \"failed before the fix\" only on an unrelated assertion. **Rule:**\nREPRODUCE a reported defect at the base, and read WHICH assertion fails there.\n\n**Problem:** A defect that is a SHAPE was repaired one site at a time (dyo Go, 94u TS); nine hn6 repairs\nfixed a printed \"clear with\" command that could not clear its hold. **Rule:** A repair names EVERY\nimplementation of the seam, leaves a guard test and reproduces on the old code; a command printed for a person comes from ONE builder and a test PARSES it with the real CLI.\n\n**Problem:** 06t deleted the stray RESULT-*.md reports and passed its gate; the integration merge\n(keepReportsOut) restored all seven, and only round two's `git ls-tree epic/hn6` saw it (obk).\n**Rule:** A repair whose effect is a TREE state is accepted on the INTEGRATED head, never the branch.\n\n**Problem:** A boot step past its retries escaped supervisePass. **Rule:** Every ending is\nfinalize's: catch a throwing step AT ITS STEP; a test fails each step past its retries.\n\n**Problem:** Gate evidence keyed by commit was wrong both ways: the run writes `.ticfac/` to the branch it\ngates — and bo9's epic head sat 60 bookkeeping commits past the newest CI run. **Rule:** Key evidence by\nthe SOURCE (tree minus the run's own path); a close-out reads CI on the last CODE-BEARING commit and PROVES\nthe remainder code-free (`git diff --stat <sha>..HEAD -- . ':(exclude).tick' ':(exclude).ticfac'`).\n\n**Problem:** Wall-clock tests refused innocent work six times; ONE host SIGTERM defect (8d6) failed at\nbase in 18 ticks; one 43y 120s harness bound was filed five times (h3c 7oy 4ao omq 30e). **Rule:** A gate\nverdict is about the tree only if the host is bounded (registrytest.GuardMain); before filing \"fails at base\", grep `.tick/issues/` — the run must dedupe on the test id.\n\n**Problem:** wne's gates went green with 7 of 10 relevant tests skipped under `-short`; yoh's ts gate ran\nno vitest; dz1's and 43y's (enb, 4w7) red EndToEnd epic CI sat unread until a review. **Rule:** A test the\ngate does not run is not evidence: add it or name the gap. A NEW package's suite joins\n`[testing.commands]` in its tick; a tick touching dispatch, claims or a profile runs `./internal/reconcile/`.\n\n**Problem:** A NOT READY was recorded ready-to-merge; 6in's close-out read an all-SKIPPED head as CI\ngreen (#100). **Rule:** A verdict is a typed field, and a skip is not a pass: green needs a success.\n\n## Fixtures\n\n**Problem:** A lost sandbox handle was green because the fake keyed on job_id; yoh's collect fake put\nwork and report in ONE commit; v5t's Go executor said `cloudflare-workers-ai/…` where the door said\n`workers-ai/…`, each green on its own fake (y38). **Rule:** A fake demands the identity and reproduces\nthe real shape; fix a forgiving fake in the same change. A TS↔Go seam gets a parity guard over its\nspellings and one Go test driving the REAL door (claude_sub_e2e_test.go's shape); \"first try\" is `$TICFAC_TRY`.\n\n**Problem:** hn6's activity fixture spelled a claude worker's model `claude-opus-5`; real attempts say\n`opus`, so a live claude worker's activity is never read. **Rule:** Copy a fixture's identity\nstrings from a real record (`.ticfac/runs/*/attempts/*.json`), never type them.\n\n## Boundaries this repo pays to learn\n\n**Problem:** A new `factory_*` key in ~/.ticfacrc broke the pinned contract. **Rule:** The config\nFILE's keys are the bundle's; $TICFAC_* is the operator-preference surface.\n\n**Problem:** A login-route test read the login page's own 401: `SELF.fetch` FOLLOWS a 303. **Rule:**\nA redirect-asserting route test passes `redirect: \"manual\"`.\n\n**Problem:** A 1,000,000 max-output drew a bodyless 400 pi read as context overflow; 8,192 truncated GLM;\nGLM via `cloudflare-workers-ai` leaked `<think>` tags. dm2 blamed row-less pi runs on pi abandoning the\nstream; 648 showed pi drains it — the gateway passes pi's own `Authorization` upstream (a bad key fails\n10000) and needs `/workers-ai/v1`. **Rule:** A bodyless 4xx is REQUEST SHAPE until proven otherwise;\nchange one variable at a time; off-vendor, set `compat.thinkingFormat`. Verify a gateway route by reading\nits LOG ROW back, and a cause by REPRODUCING it.\n"
    },
    "evidence": "wc -l -> 150 (the cap stated in the file's own header); grep -c '\\*\\*Problem:\\*\\*' -> 32, grep -c '\\*\\*Rule:\\*\\*' -> 32"
  }
]
```

## Judgement

bo9 delivered what it set out to deliver, plus one operator-added tick. Nothing
is blocking: the gate is green on the integrated tree, the review's six findings
are committed backlog ticks, and mine are backlog too — one demonstration gap on
[A1], two small surface/doc inconsistencies in the skills family the epic was
tidying, two about the machinery that carried the epic rather than the epic
itself, and the retro compaction the substrate will not let me commit. The epic
is ready to land.

STATUS: DONE
