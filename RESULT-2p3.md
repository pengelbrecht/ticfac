<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-1/2p3-run_af0e77b99c1540b7b8005cf04fd2795d`, base `c2a26e8fb23f326259396cbf87cd538157660590`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `c2a26e8fb23f326259396cbf87cd538157660590` is the head of the work it continued, which was cut from `88c0ba451b814aab34bd6f31da38788fbe1e18f7`; its work commits are counted from the carried head._

# 2p3

The tick: `.tick/runners.toml`'s `[roles.implement]` still explained
`kind = "pi"` as the deleted pi CLI (tick gjk's `--approve` trust story) and
passed `args = ["--approve"]`. After 2q5, uxi and jhp, `pi` is the
pi-durable harness — headless through the local-subprocess executor or
hosted in a cloud container — and neither launch reads roles-table `args`.
The comment and the dead args mislead a reader into thinking a pi-CLI path
still exists.

## What changed

This is attempt 6 on the branch; two earlier attempts died mid-flight and
their salvage is the branch's history:

- **4349dad8 / d34a16e2** — the substrate's own `worker report` commits:
  the fallback reports of the attempts whose harness exited before writing
  one. Nothing else on them.
- **c2a26e8f** (prior attempt, committed but never reported — the pi-durable
  harness exited 1 after committing): the tick's CODE half.
  `ticfac init`'s two writers — `runnersTOML` (base file) and `cloudTOML`
  (cloud file) — carried the same story this tick removes from the
  repository's own cell: the gjk trust story comment and
  `args = ["--approve"]` in every pi cell. They now emit no args for a pi
  cell and say why in one line ("No `args`: no launch of this harness reads
  them"), so every fresh repository `ticfac init` writes is free of the
  story, not just this one.
- **d64d6bc4** (this attempt): the missing guard test,
  `TestAPiCellIsWrittenWithoutArgs` in `internal/cli/init_test.go` — it pins
  both writers emitting no `args` beside a pi cell, none of the trust story
  ("permission-bypass", "file trust", "full-auto story"), the base writer's
  one-line why, and all three cloud cells still naming pi. The existing
  end-to-end `TestInitOnAPiRepositoryWritesCellsEverySubstrateCanRun` now
  also refuses a parsed implement cell that declares args.

Reproduced at the base (88c0ba451, the code before the fix): the new test
fails there exactly on the defect — the emitted `args = ["--approve"]` in
the base cell and in all three cloud cells, and the parsed fixture cell
carrying `[--approve]` — and passes on this branch.

## What could not be committed, and where it is

The tick's other artifact — the same fix in THIS repository's own
`.tick/runners.toml` `[roles.implement]` — cannot be committed from a tick:
the worker substrate's pre-commit hook refuses any staged `.tick/` path, and
its sweep restores the directory before the salvage. It is carried verbatim
as a `protected_change` in the findings block below: the whole new file,
identical to the current one except the `[roles.implement]` cell — the three
story lines replaced by the pi-durable explanation (with the pointer to the
init writers and their guard test), and `args = ["--approve"]` dropped. The
TOML parses, the cell keeps `kind = "pi"` and its model, and the
blessed-cells sweep (`sweepLocalClaude`) stays green against the new content
when it sits beside the real `runners.local.toml`. The cell is
byte-identical on `epic/ex6`, this attempt's base and this HEAD, so the
content applies cleanly.

A second finding proposes the follow-up guard that can only exist after that
change lands: a repo-file test refusing any pi cell that declares args
(green only once the protected change is applied; red before, so it must not
be added by this tick).

## What I ran

- `go test ./internal/cli/ -run 'TestAPiCellIsWrittenWithoutArgs|
  TestInitOnAPiRepositoryWritesCellsEverySubstrateCanRun' -count=1` — PASS
  here; reproduced FAILING at the base worktree (88c0ba451) with the exact
  defect in the output.
- `go test ./internal/cli/ -count=1 -short -timeout 45m` — ok, 27.1s.
- `make gate` (gofmt, go vet, `go test -short -timeout 45m -parallel 12
  ./...`) — all ok.
- `make ts-gate` (pnpm install, biome lint, contracts check, tsc) — ok,
  contracts at bundle 2.3.0.
- `sweepLocalClaude` against the proposed `.tick/runners.toml` content beside
  the real `runners.local.toml` (throwaway test, since removed) — clean.

## What the next tick has to know

- The protected change in the findings block is the tick's remaining
  artifact; until it is applied to `.tick/runners.toml`, this repository's
  own cell still carries the story and the dead args. Nothing at runtime
  reads them, so nothing breaks meanwhile.
- After it lands, the proposal finding's repo-file drift guard becomes
  addable — that is the point at which a red-until-now test turns green.
- `ticfac init`'s writers and the repository's own cell now intend the same
  shape (no args on a pi cell); they are two artifacts with one rule, and
  only the writers are guarded today.

```findings v2
[
  {
    "kind": "defect",
    "title": ".tick/runners.toml [roles.implement] still carries the dead --approve arg",
    "severity": "low",
    "body": "The [roles.implement] cell still explains kind = \"pi\" as the deleted pi CLI (tick gjk's --approve trust story) and passes args = [\"--approve\"]. After 2q5, uxi and jhp, pi is the pi-durable harness — headless through the local-subprocess executor or hosted in a cloud container — and neither launch reads roles-table args: the key reaches an argv only through the herdr pane path (spawnArgv), and herdr refuses kind pi. The comment and the dead args mislead a reader into thinking a pi-CLI path still exists. The exact replacement — comment rewritten to the pi-durable explanation, args line dropped, everything else byte-identical — is carried as protected_change: the worker substrate refuses staging any .tick/ path, so a tick cannot commit it. The cell is byte-identical on epic/ex6, this attempt's base (88c0ba451) and HEAD (d64d6bc4a), so the content applies cleanly, and the blessed-cells sweep (sweepLocalClaude) stays green against it beside the real runners.local.toml.",
    "evidence": ".tick/runners.toml:16-18 (the gjk story comment), :22 (args = [\"--approve\"]); profiles-herdr/implement-tick.json executor local-subprocess",
    "protected_change": {
      "path": ".tick/runners.toml",
      "content": "# Worker routing for herd-substrate epic runs. Schema and semantics: the ticks\n# skill's references/runners-config.md.\nversion = 2\n\n[orchestration]\nsubstrate = \"auto\"\nmax_parallel = 4\n\n[roles.implement]\n# Operator routing for Phase 2 (2026-09-10). Implementation runs entirely on\n# GLM on cloudflare through pi: GLM 5.3 for complex work (the default, and the\n# `strong` tier) and GLM 5.3-flash for simple work (`economy`). opus is NOT an\n# implementation tier — it is the claude harness only, reserved for review and\n# closeout, and it is not available in pi at all.\n#\n# `pi` is the pi-durable harness (epic 43y, tick hpk; the pi CLI is deleted,\n# tick jhp): headless through the local-subprocess executor or hosted in a\n# cloud container. No `args`: neither launch reads roles-table args — that\n# key reaches an argv only through the herdr pane path (spawnArgv), and herdr\n# refuses kind pi — so tick 2p3 dropped the `--approve` the deleted pi CLI\n# used to take for project-local file trust (tick gjk). ticfac init's writers\n# emit the same shape, and internal/cli's TestAPiCellIsWrittenWithoutArgs\n# pins them.\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# Simple work: mechanical, compiler-guided, low judgement.\n[roles.implement.tiers.economy]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash\"\neffort = \"medium\"\n\n# Complex work, named explicitly. Same model as the default: GLM 5.3 IS the\n# strong tier for this epic. opus is not an implementation tier at all — it is\n# reserved for review and closeout, and it is not available in the pi harness.\n[roles.implement.tiers.strong]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# `balanced` is NOT declared for implement, deliberately (epic 43y, tick\n# 7ml): until 2026-10-05 this cell named kind = \"codex\" (a Phase-2\n# leftover), and because runners.local.toml declares no [roles.implement]\n# role cell to overlay it away, a local run pinning --tier balanced\n# dispatched an implement worker on the codex CLI — a worker harness that is\n# neither pi-durable nor the claude-CLI frontier rung the operator's\n# 2026-10-04 decision keeps. No ladder ever derives balanced (the local\n# ladder climbs strong -> frontier, the cloud's economy -> strong), and in\n# the cloud this cell never reached a container anyway (the cloud's own role\n# cell applied last). With the cell gone, a pin of balanced is REFUSED\n# naming the tier — the profile layer's fail-closed rule (a tier that\n# silently falls back to the role is a tier an operator paid for and did not\n# get) — and internal/reconcile's routing guard resolves every tier name\n# a pin can reach on every substrate, so a cell like the old one cannot come\n# back unnoticed.\n\n# The frontier-tier review at epic completion — and, with [roles.closeout]\n# below, the deliberate LOCAL claude exception the operator blessed\n# (2026-10-04/05 decisions; tick j6o): local final reviews and close-outs\n# run on the claude CLI on purpose — the hn6 run was moved to a local run\n# for exactly that — so these cells name claude opus and\n# runners.local.toml deliberately declares no review or closeout cell to\n# overlay them away. The claude CLI is otherwise the local FRONTIER rung\n# only: implementation runs on pi-durable at base and every tier but\n# frontier, and the cloud overlays every cell here away\n# (runners.cloud.toml applies last; nothing in the cloud runs claude).\n# TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells holds this\n# table: claude exactly here and on implement's frontier rung, pi or a\n# named refusal everywhere else, on every local substrate.\n[roles.review]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# The ceiling-tier overlay of the review cell (tick 2p6): the resolve-conflict\n# job — dispatched when two same-wave attempts meet a content or add/add\n# conflict — routes at the policy's ceiling through the REVIEW cell's\n# candidates (a dedicated [roles.resolve-conflict] cell would always win),\n# and locally the ceiling is the frontier tier of .tick/runners.local.toml's\n# ladder. Without this cell the overlay the ceiling asks for is refused, and\n# a refused routing is a resolve that stops the run over config — which is\n# why a run now refuses at START when it cannot route (reconcile.CheckRouting).\n# The cloud merges this cell, but can never run it: runners.cloud.toml's\n# review cell applies last and routes the job to a Workers AI model, and the\n# cloud must declare its OWN ceiling's tier there (its ceiling is strong, not\n# frontier). Nothing in the cloud runs claude.\n[roles.review.tiers.frontier]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# Closeout shares the review cell's blessing (see [roles.review] above):\n# the operator wants local close-outs on claude opus too — the same\n# 2026-10-05 decision, recorded on tick j6o — and the cloud overlays this\n# cell away exactly as it overlays review's. A local override that moves\n# either cell off the claude CLI is a change to an operator decision, not\n# a config edit: take it to the operator.\n[roles.closeout]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# Findings a worker routes to ANOTHER repository (a `target` other than this\n# one). This run cannot fix another repository, so such a finding never gates\n# this epic; the run disposes of it with nobody triaging. A target listed here\n# with `file = true` is filed by the run as a tick in that repository's own\n# tracker, pushed to its default branch; any other target — or a filing that\n# fails terminally — becomes a backlog tick HERE that names the target. The\n# epic-2jn close-out held on five findings for pengelbrecht/ticks (2026-09-27)\n# because neither path existed. The operator owns both repositories.\n[findings.route.\"pengelbrecht/ticks\"]\nfile = true\n\n# The cloud's routing lives in .tick/runners.cloud.toml and a laptop's\n# extras (the claude ladder) in .tick/runners.local.toml (tick 5uo): each\n# merges over this file for its own world, and its role cells apply LAST, so\n# nothing written here for a tier can reach a container.\n\n[testing.commands]\n# Spelled out, not `make gate`: this file is what says what the gate runs, and a\n# reader must be able to see it here. The Makefile's `gate` target carries the\n# same line for humans and CI, and a test fails if the two ever disagree — the\n# gate had drifted to a bare `go test -short -count=1 ./...`, losing the timeout\n# and the parallelism that keep internal/reconcile inside go's per-package\n# limit, and nothing noticed.\n#\n# 2026-09-18 — the `-count=1` is dropped, deliberately, by the operator.\n#\n# It disabled Go's test cache, so every package was re-run for every tick even\n# when nothing it depends on had changed. Epic 9pd ran this gate 49 times at\n# roughly 20 minutes idle and 45-60+ under load, and finished with three ticks\n# whose code was already merged facing three sequential full runs.\n#\n# The suite still runs in full. What changes is that a package whose inputs are\n# unchanged answers from cache, which is a true statement about this tree: Go\n# keys the cache on the package's sources, its dependencies, and the files and\n# environment the test consulted.\n#\n# 2026-09-20 — and until tick 6wh it bought NOTHING, because the gate ran in a\n# fresh temp directory every time and Go's cache keys on the absolute paths a\n# test opened. The reasoning above was right and the mechanism was broken. 6wh\n# runs the gate in a stable directory per repository, so the saving described\n# here is now a thing that actually happens.\n#\n# The exposure that came with it, and the answer. A test that reads a file by an\n# ABSOLUTE path OUTSIDE the module can be served from cache after that file\n# changes. contracts.RepoRoot() builds absolute paths, so the drift guards that\n# read this very file — TestThisRepositorysGateIsReadable,\n# TestTheGateTargetMatchesTheDeclaredGate,\n# TestTheHarnessBoundOutlivesEveryDeclaredGateBound — were the ones to watch.\n# They are safe: this file is INSIDE the module those tests belong to, and go\n# invalidates on it. Demonstrated by hand (edit the command on the line below\n# and the guard fails, uncached) and on every run by\n# TestACachedPassCannotHideAnEditToAFileAGuardReads, which rebuilds the guards'\n# exact shape in a throwaway module and edits its input under it. `make suite`\n# keeps -count=1 and remains the paranoid answer; it is no longer a required\n# one. The wider audit is still tick mbv's.\n#\n# 2026-09-20 — the `-short` in this line now MEANS something (tick miu). It\n# used to mean nothing: 6 of 215 test files consulted testing.Short(), so every\n# per-tick gate ran the entire end-to-end suite — 23 minutes on the e9n\n# attempt-8 run, which then failed on two tests (e4u, 7c0) that pass on a quiet\n# host and stopped the Phase 5 run for nothing.\n#\n# internal/reconcile, internal/exec/herdr and internal/runstate now skip under\n# -short at their harness constructors, and this command runs in seconds. What\n# it no longer covers is exactly what CI covers: ci.yml's go job runs\n# `make test-short` AND `make test`, so nothing this gate skips reaches main\n# unrefused. internal/shorttest holds the guard that stops a new test from\n# quietly re-entering — or quietly escaping — the short suite.\n#\n# The command itself is UNCHANGED, and must stay byte-identical to the\n# Makefile's `gate` recipe: TestTheGateTargetMatchesTheDeclaredGate compares\n# the two.\n# gofmt and go vet first, exactly as CI's go job runs them: the gate is what a\n# worker's commit must pass, and a gate weaker than CI lets a tick close on a\n# tree CI then fails. An xte worker's unformatted test file did exactly that on\n# 2026-09-23 - gate green, epic PR red - and the close-out held on it.\n# `(! grep .)` prints any unformatted file and fails if there was one; it is\n# written without $ so the Makefile's gate target can carry the same line.\ngo = { command = \"gofmt -l . | grep -v '^contracts/' | (! grep .) && go vet ./... && go test -short -timeout 45m -parallel 12 ./...\", description = \"Go: gofmt, vet and tests, as CI runs them\" }\n\n# The TypeScript half, added by tick odc. Until it, NOTHING ran cloud/factory's\n# tests — not this gate, not CI — and the cost of that silence was tick b9w: the\n# control plane sat pinned to contract bundle 3.0.0 against a 5.2.0 bundle,\n# wrong about three record shapes, for two major versions.\n#\n# What is here and what is NOT is a measurement, not a preference. On this host:\n# pnpm install from a warm store 1s, contracts:check under 1s, tsc --noEmit 2s,\n# vitest 82s. Gate commands run SERIALLY, so vitest would take the gate from\n# 3m26s to about 4m48s on every tick — and the gate's speed is what made this\n# epic survivable at all.\n#\n# So the gate takes the two cheap checks and CI takes vitest, where it runs as\n# its own job beside the 7m15s Go job and costs no wall clock at all. The split\n# is stated rather than implied: THIS GATE COVERS CONTRACTS AND TYPES, NOT\n# BEHAVIOUR. A behavioural TypeScript regression is caught by CI on the epic PR,\n# before anything reaches main, and never by this line.\n#\n# contracts:check is the one that earns its place here: it is the check that\n# would have caught b9w's drift the day it happened, and it costs a second.\n#\n# `pnpm lint` is Biome (tick ncr) — this side's gofmt AND its vet, in one Rust\n# binary. It is IN this command rather than a sibling [testing.commands] entry\n# for one measured reason: a sibling would repeat `pnpm install`, which is ~0.7s\n# on a warm store, to learn nothing the &&-chain's own output does not already\n# name. Biome itself is 0.47/0.55/0.64s over three runs on these 109 files, so\n# the gate's TypeScript half goes from ~3s to ~3.6s and the whole gate is\n# unmoved against the 3m26s Go side.\n#\n# `pnpm lint` is `biome ci --error-on-warnings`, and the flag matters: plain\n# `biome ci` exits 0 with warnings still on the floor, and most of what Biome\n# found here — the unused variable, the implicit anys — is warning severity, so\n# without it this half cannot refuse what it was added to catch.\n#\n# This is still CONTRACTS AND TYPES, not behaviour: Biome reads one file at a\n# time and answers about its shape, not about what the Worker does.\n#\n# The two-layout fallback is GONE (tick 9a8). It existed only while Phase 4's\n# l9n moved cloud/factory to cloudflare/: this command is read from the RUN'S\n# checkout while it runs against the MERGED tree (defect 19q), so across the\n# move the two disagreed and l9n could never have passed the gate whose job it\n# was to move. Phase 4 merged to main on 2026-09-19 and neither cloud/factory\n# nor cloud/sandbox exists any more, so the fallback arm named nothing.\nts = { command = \"cd cloudflare && pnpm install --frozen-lockfile --prefer-offline && pnpm lint && pnpm contracts:check && pnpm exec tsc --noEmit\", description = \"TypeScript format, lint, contracts and types\" }\n\n[environment.commands]\nwhich-go = { command = \"which go\", description = \"Go toolchain on PATH\" }\ngit-config-user-email = { command = \"git config user.email\", description = \"git identity configured\" }\n"
    }
  },
  {
    "kind": "proposal",
    "title": "Guard the repo's own runners.toml: no pi cell declares args",
    "severity": "low",
    "body": "Once the protected [roles.implement] change lands, a guard test can read this repository's .tick/runners.toml and refuse any cell pairing kind = \"pi\" with args — the same invariant TestAPiCellIsWrittenWithoutArgs (internal/cli) now pins on ticfac init's writers. It cannot be added by this tick: until the protected change is applied the cell still carries args, so the test would be red and would block the close-out's gate. The natural home is internal/reconcile beside the other TestThisRepositorys* guards.",
    "evidence": "internal/cli/init_test.go TestAPiCellIsWrittenWithoutArgs (writer half, green); internal/reconcile/routing_check_test.go (the repo-file guards' home)"
  }
]
```

STATUS: DONE — the code half is committed with its guard test, the gate is green, and the repository's own `.tick/runners.toml` cell fix is carried as a protected_change finding for the run to apply to the epic branch.
