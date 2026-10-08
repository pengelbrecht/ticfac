<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-6/q6z`, base `72d2f3cc5cb223fb75588e700b0c4a2377fe2d11`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `72d2f3cc5cb223fb75588e700b0c4a2377fe2d11` is the head of the work it continued, which was cut from `2364d3703e949aa3ac5e48d044222bb8325d9952`; its work commits are counted from the carried head._

# RESULT — tick q6z: `[roles.implement] args = ["--approve"]` is dead config after the herdr re-cut

The tick's named deliverable — the `args = ["--approve"]` line in `.tick/runners.toml`'s
`[roles.implement]` cell, and the comment above it that still explains the deleted pi CLI's
trust model as if it launched — lives in a `.tick/` path no dispatched worker may write on
this substrate. This attempt delivers it the way this run's contract provides: as two
`protected_change` objects in the findings block below, each the **whole replacement
content of one routing file**, which the run applies itself onto the epic branch after the
close-out, as a labelled commit. Both are byte-verified against THIS tree, not composed
blind. One commit is in the branch: the last sentence outside `.tick/` that stated the dead
pairing as a live fact.

## What is committed

`d516c37` — `internal/runconfig/schema_configs_test.go`, the comment in
`assertWellFormedNamedConfigs` that explained the claude config's `args = []` clearing by
naming "the `--approve` the cloud's own implement cell hands pi". That sentence stops being
true the moment the paste below lands, and it was the only place left in the tree — outside
`.tick/` routing, historical run records under `.ticfac/`, and the fixtures named below —
that stated the pairing as live. It now states the rule the assertion beneath it actually
tests (a cell that switches kind does not inherit the argv beneath it unless it says so),
which holds before the paste lands and after. The clearing itself stays: it is the only
mechanical guard of that rule the claude config's implement cell has.

Nothing else in the repository-side sweep moved, because it was already done:
`ticfac init` writes no `args` into any routing it generates and says so once in each file
(attempt 20, merged; `TestInitWritesNoArgsIntoTheRoutingItGenerates` is green on this
tree); `image/worker.sh`/`image/entrypoint.sh`'s `--auto-approve` is the omp CLI's own
flag, a different harness; `internal/cli/executor_gate_test.go`'s `--approve` is an example
of the *field's* semantics in a synthetic fixture; `internal/runconfig/runners-config.md`
already states that no `args = ["--approve"]` can license a pi CLI worker;
`internal/runconfig/substrate_overlay_test.go` uses the spelling as synthetic test data for
the array-replacement rule.

## The failing test first, then what makes it pass

Both halves ran on this tree through a throwaway test in `internal/reconcile` (removed
before the gate, as every throwaway in this repository is; the paste it verified is the
delivery).

**1. The tick's claim, at base.** A sweep resolving `[roles.implement]` through the
production reader (`runconfig.LoadForConfig`) on every substrate a run of this repository
executes on, through every declared named config, at base values and at every tier a pin
can reach, finds **22 findings** on the untouched tree:

- four cells declaring the pairing — the common file's, both local worlds' merged view of
  it, the cloud file's own (`.tick/runners.cloud.toml:24` redeclares it independently), and
  the `glm` named config's merged view of the cloud's (its implement cell declares no args
  of its own, so the line flows in);
- eighteen resolutions carrying `--approve`: herdr and harness at base, economy, balanced
  and strong (4 each), cloud at base, economy, balanced, strong and frontier under both
  configs (5 each).

The `claude` config's implement cell is clean only because its `args = []` clearing cancels
the line — which is exactly why the paste below keeps that clearing and rewords its comment
rather than dropping it.

**2. The paste.** Over the replacement content the same sweep returns nothing. Every
resolution the routing can produce — every role cell either file declares, at base values
and at every tier, on every substrate, through every named config, refusals included — is
identical to today's except that `args=[--approve]` becomes `args=[]`. The repository's own
routing guards hold over the replacement content: `CheckRouting` on all four
substrate/profile-set pairs, `assertTheNamedConfigsRoute` (exactly glm and claude, every job
inside its billing rule), and `sweepLocalClaude` (the local claude exception still exactly
the blessed cells). And with every comment line removed, the replacement content is the
current file minus its `args = ["--approve"]` lines and nothing else: the paste changes
words and drops one dead line per file.

**3. The paste describes this tree.** Each of the four replacement blocks below matched its
current file exactly once before it was applied, so the content in the findings block cannot
land on a tree it does not describe.

## The paste, in words

`.tick/runners.toml` — the tick's named target (two blocks):

```toml
# before
[roles.implement]
# Operator routing for Phase 2 (2026-09-10). Implementation runs entirely on
# GLM on cloudflare through pi: GLM 5.3 for complex work (the default, and the
# `strong` tier) and GLM 5.3-flash for simple work (`economy`). opus is NOT an
# implementation tier — it is the claude harness only, reserved for review and
# closeout, and it is not available in pi at all.
#
# pi needs no permission-bypass flag: verified live 2026-09-10 (tick gjk) that it
# runs tools unprompted. `--approve` covers project-local file trust, and that is
# the whole of its full-auto story. Evidence: .tick/logs/herd/av8/gjk.RESULT.md
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
args = ["--approve"]
```

```toml
# after — the trust story is replaced by the reason there are no args, and the
# one stale claim about opus (a claude-harness model that IS an implement rung
# locally, at frontier, and in the cloud's named claude config) is corrected
[roles.implement]
# Operator routing for Phase 2 (2026-09-10; the args line and the trust
# comment it carried are gone since tick q6z). Implementation runs on GLM on
# cloudflare through pi: GLM 5.3 for complex work (the default, and the
# `strong` tier) and GLM 5.3-flash for simple work (`economy`). opus is a
# claude-harness model, not a pi one: for implement it is the local frontier
# escalation and the cloud's named claude config only, never a starting tier
# — review and closeout are the roles the cells below run on it.
#
# No `args` on this cell, deliberately (tick q6z). The roles table's args
# have exactly one consumer — spawnArgv, in internal/cli/executor.go, read
# only when a herdr dispatch compiles an agent's argv — and
# runconfig.Compile refuses kind "pi" for a herdr pane (uxi), so no
# implement dispatch can read a flag from here: since 2q5 implement routes
# onto the local-subprocess executor, which launches the durable pi host
# from its own runner table (internal/exec/subprocess), and a cloud
# container is booted from the worker.json its sandbox door hands it. The
# line that sat here was the deleted pi CLI's `--approve` — project-local
# file trust, verified live 2026-09-10 (tick gjk;
# .tick/logs/herd/av8/gjk.RESULT.md) — dead config on every substrate since
# the CLI went with its worker path (jhp, uxi): it told a reader of this
# file that implement workers somewhere run with --approve. pi needs no
# permission-bypass flag in any case: it runs tools unprompted.
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
```

```toml
# before
# Complex work, named explicitly. Same model as the default: GLM 5.3 IS the
# strong tier for this epic. opus is not an implementation tier at all — it is
# reserved for review and closeout, and it is not available in the pi harness.
```

```toml
# after
# Complex work, named explicitly. Same model as the default: GLM 5.3 IS the
# strong tier for this epic. opus is not a starting tier for implement
# anywhere: locally it is the frontier escalation only (runners.local.toml)
# and in the cloud only the named claude config's strong rung — and it is a
# claude-harness model, not a pi one.
```

`.tick/runners.cloud.toml` — the same dead pairing on the cloud's own cell, and the
clearing whose comment named it (two blocks):

```toml
# before
[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
args = ["--approve"]
```

```toml
# after
# No `args` on this cell (tick q6z): the roles table's args are read only
# when a herdr dispatch compiles an agent's argv, and a cloud container is
# booted over its sandbox door from the worker.json the door hands it. The
# line that sat here was the deleted pi CLI's `--approve`, dead config on
# this substrate too.
[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
```

```toml
# before
# `args = []` clears the `--approve` the cloud's own implement cell hands pi:
# a cell that switches kind keeps the argv beneath it unless it says so, and
# claude does not take pi's flag.
[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"
effort = "high"
args = []
```

```toml
# after — the clearing STAYS (it is the only mechanical guard of the
# kind-switch rule this cell has); only its explanation changes
# `args = []` keeps this claude cell from inheriting whatever argv the cells
# beneath it may one day declare: a cell that switches kind keeps the argv
# beneath it unless it says so (the operator's 2026-09-10 rule), and the
# claude harness takes no pi flag. It cleared the `--approve` the implement
# cell above carried until tick q6z removed it, and it stays as the guard.
[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"
effort = "high"
args = []
```

`.tick/runners.local.toml` is untouched: its bare `args = []` on the frontier rung is the
same kind-switch guard, it carries no comment that names the dead flag, and removing it
would leave the rule with no guard at all.

## What I ran, in order

- `go test -short -count=1 -run 'TestQ6zProtectedChangeVerifiesAgainstThisTree' -timeout
  300s ./internal/reconcile/` — the throwaway holding both halves. **PASS**, logging the 22
  base findings (every one of them `--approve`, the tick's claim), the empty sweep over the
  replacement content, the unchanged resolutions, the three routing guards over it, and the
  comment-only diff. Removed before the gate.
- `go test -short -count=1 -run 'TestQ6zReportContentRoundTrips' -timeout 300s
  ./internal/reconcile/` — a second throwaway, deleted with the first: it read the
  `protected_change` content back out of RESULT-q6z.md exactly as the run will, wrote it to
  a temp directory, and held it to the same bar — **PASS**: the report's own content loads
  on every substrate, routes every job, keeps both named configs inside their billing rules,
  keeps the local claude exception exactly the blessed cells, and resolves implement with
  no args anywhere, while the untouched tree still reports the 22.
- `go test -short -count=1 -run 'TestInitWritesNoArgsIntoTheRoutingItGenerates' -timeout
  600s ./internal/cli/` — **PASS** (attempt 20's repository-side half, green on this tree).
- `go test -short -count=1 -run 'TestThisRepositorysRoutingRoutesEveryJobOnEverySubstrate|
  TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells|
  TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness' -timeout 900s
  ./internal/reconcile/` — **PASS** (the routing guards that hold over today's files and,
  separately, over the paste).
- `make gate GOTEST_PARALLEL=4` — **exit 0** (gofmt, `go vet ./...`, the whole-repo short
  suite; every package ok).
- The declared gate's TypeScript half, run by hand because the commit touches no
  TypeScript: `cd cloudflare && pnpm install --frozen-lockfile --prefer-offline` (exit 0),
  `pnpm lint` (exit 0, 166 files), `pnpm contracts:check` (exit 0, bundle 2.4.0),
  `pnpm exec tsc --noEmit` (exit 0).
- `ticfac-exec-subprocess lint-report RESULT-q6z.md --role implement-tick --tick q6z` —
  exit 0.

## What the operator and the next attempt have to know

- **Both protected changes are one edit.** The common file's cell and the cloud file's cell
  redeclare the pairing independently, so fixing one leaves the other standing: the sweep
  above still reports the cloud's four findings if only one lands. Each paste is
  independently *correct*, but the tick is done only when both are on the branch.
- **This closes 2p3 as well.** 2p3 ("runners.toml implement cell still describes the deleted
  pi CLI", from uxi's finding) asks for the same cell, the same comment and the same line;
  this run's attempt 9 was collected as a blocked answer with no report written, and the
  run held on it. The finding below carries a note for 2p3's record so nobody dispatches
  another attempt into it — verify the labelled commit carries both cells and close 2p3
  over the same change.
- **The guard that makes it stick cannot land in this tick.** A sweep over this
  repository's own routing files is red while the pairing stands, and a red guard fails the
  per-tick gate and CI on the epic PR — the protected change lands after the close-out, so
  no tick can carry both. It is filed below as a proposal, to land with or after the
  labelled commit: `TestThisRepositorysImplementRoutingCarriesNoArgs` in
  `internal/reconcile`, a sweep of the shape this attempt ran (resolve implement through
  `runconfig.LoadForConfig` on every substrate × every named config × every tier, plus the
  declared cell), with a synthetic bite in a temp directory that re-adds
  `args = ["--approve"]` and must be flagged. Until it lands, the two `args = []` clearings
  are the only mechanical protection the kind-switch rule has — which is why this paste
  keeps them.

```findings v2
[
 {
  "kind": "defect",
  "title": "runners.toml implement cell still carries the deleted pi CLI's --approve",
  "severity": "medium",
  "body": "The tick's named target: .tick/runners.toml [roles.implement] still declares args = [\"--approve\"] (line 22) under a comment that explains the deleted pi CLI's project-local file-trust flag as if it launched (lines 13-19). Nothing can read it: the roles table's args have exactly one consumer, spawnArgv in internal/cli/executor.go, reached only when a herdr dispatch compiles an agent's argv, and runconfig.Compile refuses kind \"pi\" for a herdr pane since uxi; since 2q5 implement routes onto the local-subprocess executor, which launches the durable pi host from its own runner table, and a cloud container is booted from the worker.json its sandbox door hands it. A sweep resolving implement through runconfig.LoadForConfig on every substrate, every named config and every tier finds 22 findings carrying this line on the untouched tree. The protected_change is the whole replacement content of the file, byte-verified against this tree: it drops the line, replaces the trust story with the reason there are no args, and corrects the one other stale claim in the same cell family (opus IS an implement rung locally, at frontier, and in the cloud's named claude config). Every value line is unchanged; with the comment lines removed the new file is the old one minus the dead line. Same cell and words as open tick 2p3.",
  "evidence": ".tick/runners.toml:13-22; internal/cli/executor.go:301 (spawnArgv, the only consumer); internal/runconfig/compile.go:159 (the pi/herdr refusal)",
  "protected_change": {
   "path": ".tick/runners.toml",
   "content": "# Worker routing for herd-substrate epic runs. Schema and semantics: the ticks\n# skill's references/runners-config.md.\nversion = 2\n\n[orchestration]\nsubstrate = \"auto\"\nmax_parallel = 4\n\n[roles.implement]\n# Operator routing for Phase 2 (2026-09-10; the args line and the trust\n# comment it carried are gone since tick q6z). Implementation runs on GLM on\n# cloudflare through pi: GLM 5.3 for complex work (the default, and the\n# `strong` tier) and GLM 5.3-flash for simple work (`economy`). opus is a\n# claude-harness model, not a pi one: for implement it is the local frontier\n# escalation and the cloud's named claude config only, never a starting tier\n# \u2014 review and closeout are the roles the cells below run on it.\n#\n# No `args` on this cell, deliberately (tick q6z). The roles table's args\n# have exactly one consumer \u2014 spawnArgv, in internal/cli/executor.go, read\n# only when a herdr dispatch compiles an agent's argv \u2014 and\n# runconfig.Compile refuses kind \"pi\" for a herdr pane (uxi), so no\n# implement dispatch can read a flag from here: since 2q5 implement routes\n# onto the local-subprocess executor, which launches the durable pi host\n# from its own runner table (internal/exec/subprocess), and a cloud\n# container is booted from the worker.json its sandbox door hands it. The\n# line that sat here was the deleted pi CLI's `--approve` \u2014 project-local\n# file trust, verified live 2026-09-10 (tick gjk;\n# .tick/logs/herd/av8/gjk.RESULT.md) \u2014 dead config on every substrate since\n# the CLI went with its worker path (jhp, uxi): it told a reader of this\n# file that implement workers somewhere run with --approve. pi needs no\n# permission-bypass flag in any case: it runs tools unprompted.\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# Simple work: mechanical, compiler-guided, low judgement.\n[roles.implement.tiers.economy]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash\"\neffort = \"medium\"\n\n# Complex work, named explicitly. Same model as the default: GLM 5.3 IS the\n# strong tier for this epic. opus is not a starting tier for implement\n# anywhere: locally it is the frontier escalation only (runners.local.toml)\n# and in the cloud only the named claude config's strong rung \u2014 and it is a\n# claude-harness model, not a pi one.\n[roles.implement.tiers.strong]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# `balanced` is NOT declared for implement, deliberately (epic 43y, tick\n# 7ml): until 2026-10-05 this cell named kind = \"codex\" (a Phase-2\n# leftover), and because runners.local.toml declares no [roles.implement]\n# role cell to overlay it away, a local run pinning --tier balanced\n# dispatched an implement worker on the codex CLI \u2014 a worker harness that is\n# neither pi-durable nor the claude-CLI frontier rung the operator's\n# 2026-10-04 decision keeps. No ladder ever derives balanced (the local\n# ladder climbs strong -> frontier, the cloud's economy -> strong), and in\n# the cloud this cell never reached a container anyway (the cloud's own role\n# cell applied last). With the cell gone, a pin of balanced is REFUSED\n# naming the tier \u2014 the profile layer's fail-closed rule (a tier that\n# silently falls back to the role is a tier an operator paid for and did not\n# get) \u2014 and internal/reconcile's routing guard resolves every tier name\n# a pin can reach on every substrate, so a cell like the old one cannot come\n# back unnoticed.\n\n# The frontier-tier review at epic completion \u2014 and, with [roles.closeout]\n# below, the deliberate LOCAL claude exception the operator blessed\n# (2026-10-04/05 decisions; tick j6o): local final reviews and close-outs\n# run on the claude CLI on purpose \u2014 the hn6 run was moved to a local run\n# for exactly that \u2014 so these cells name claude opus and\n# runners.local.toml deliberately declares no review or closeout cell to\n# overlay them away. The claude CLI is otherwise the local FRONTIER rung\n# only: implementation runs on pi-durable at base and every tier but\n# frontier, and the cloud overlays every cell here away\n# (runners.cloud.toml applies last; nothing in the cloud runs claude).\n# TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells holds this\n# table: claude exactly here and on implement's frontier rung, pi or a\n# named refusal everywhere else, on every local substrate.\n[roles.review]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# The ceiling-tier overlay of the review cell (tick 2p6): the resolve-conflict\n# job \u2014 dispatched when two same-wave attempts meet a content or add/add\n# conflict \u2014 routes at the policy's ceiling through the REVIEW cell's\n# candidates (a dedicated [roles.resolve-conflict] cell would always win),\n# and locally the ceiling is the frontier tier of .tick/runners.local.toml's\n# ladder. Without this cell the overlay the ceiling asks for is refused, and\n# a refused routing is a resolve that stops the run over config \u2014 which is\n# why a run now refuses at START when it cannot route (reconcile.CheckRouting).\n# The cloud merges this cell, but can never run it: runners.cloud.toml's\n# review cell applies last and routes the job to a Workers AI model, and the\n# cloud must declare its OWN ceiling's tier there (its ceiling is strong, not\n# frontier). Nothing in the cloud runs claude.\n[roles.review.tiers.frontier]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# Closeout shares the review cell's blessing (see [roles.review] above):\n# the operator wants local close-outs on claude opus too \u2014 the same\n# 2026-10-05 decision, recorded on tick j6o \u2014 and the cloud overlays this\n# cell away exactly as it overlays review's. A local override that moves\n# either cell off the claude CLI is a change to an operator decision, not\n# a config edit: take it to the operator.\n[roles.closeout]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# Findings a worker routes to ANOTHER repository (a `target` other than this\n# one). This run cannot fix another repository, so such a finding never gates\n# this epic; the run disposes of it with nobody triaging. A target listed here\n# with `file = true` is filed by the run as a tick in that repository's own\n# tracker, pushed to its default branch; any other target \u2014 or a filing that\n# fails terminally \u2014 becomes a backlog tick HERE that names the target. The\n# epic-2jn close-out held on five findings for pengelbrecht/ticks (2026-09-27)\n# because neither path existed. The operator owns both repositories.\n[findings.route.\"pengelbrecht/ticks\"]\nfile = true\n\n# The cloud's routing lives in .tick/runners.cloud.toml and a laptop's\n# extras (the claude ladder) in .tick/runners.local.toml (tick 5uo): each\n# merges over this file for its own world, and its role cells apply LAST, so\n# nothing written here for a tier can reach a container.\n\n[testing.commands]\n# Spelled out, not `make gate`: this file is what says what the gate runs, and a\n# reader must be able to see it here. The Makefile's `gate` target carries the\n# same line for humans and CI, and a test fails if the two ever disagree \u2014 the\n# gate had drifted to a bare `go test -short -count=1 ./...`, losing the timeout\n# and the parallelism that keep internal/reconcile inside go's per-package\n# limit, and nothing noticed.\n#\n# 2026-09-18 \u2014 the `-count=1` is dropped, deliberately, by the operator.\n#\n# It disabled Go's test cache, so every package was re-run for every tick even\n# when nothing it depends on had changed. Epic 9pd ran this gate 49 times at\n# roughly 20 minutes idle and 45-60+ under load, and finished with three ticks\n# whose code was already merged facing three sequential full runs.\n#\n# The suite still runs in full. What changes is that a package whose inputs are\n# unchanged answers from cache, which is a true statement about this tree: Go\n# keys the cache on the package's sources, its dependencies, and the files and\n# environment the test consulted.\n#\n# 2026-09-20 \u2014 and until tick 6wh it bought NOTHING, because the gate ran in a\n# fresh temp directory every time and Go's cache keys on the absolute paths a\n# test opened. The reasoning above was right and the mechanism was broken. 6wh\n# runs the gate in a stable directory per repository, so the saving described\n# here is now a thing that actually happens.\n#\n# The exposure that came with it, and the answer. A test that reads a file by an\n# ABSOLUTE path OUTSIDE the module can be served from cache after that file\n# changes. contracts.RepoRoot() builds absolute paths, so the drift guards that\n# read this very file \u2014 TestThisRepositorysGateIsReadable,\n# TestTheGateTargetMatchesTheDeclaredGate,\n# TestTheHarnessBoundOutlivesEveryDeclaredGateBound \u2014 were the ones to watch.\n# They are safe: this file is INSIDE the module those tests belong to, and go\n# invalidates on it. Demonstrated by hand (edit the command on the line below\n# and the guard fails, uncached) and on every run by\n# TestACachedPassCannotHideAnEditToAFileAGuardReads, which rebuilds the guards'\n# exact shape in a throwaway module and edits its input under it. `make suite`\n# keeps -count=1 and remains the paranoid answer; it is no longer a required\n# one. The wider audit is still tick mbv's.\n#\n# 2026-09-20 \u2014 the `-short` in this line now MEANS something (tick miu). It\n# used to mean nothing: 6 of 215 test files consulted testing.Short(), so every\n# per-tick gate ran the entire end-to-end suite \u2014 23 minutes on the e9n\n# attempt-8 run, which then failed on two tests (e4u, 7c0) that pass on a quiet\n# host and stopped the Phase 5 run for nothing.\n#\n# internal/reconcile, internal/exec/herdr and internal/runstate now skip under\n# -short at their harness constructors, and this command runs in seconds. What\n# it no longer covers is exactly what CI covers: ci.yml's go job runs\n# `make test-short` AND `make test`, so nothing this gate skips reaches main\n# unrefused. internal/shorttest holds the guard that stops a new test from\n# quietly re-entering \u2014 or quietly escaping \u2014 the short suite.\n#\n# The command itself is UNCHANGED, and must stay byte-identical to the\n# Makefile's `gate` recipe: TestTheGateTargetMatchesTheDeclaredGate compares\n# the two.\n# gofmt and go vet first, exactly as CI's go job runs them: the gate is what a\n# worker's commit must pass, and a gate weaker than CI lets a tick close on a\n# tree CI then fails. An xte worker's unformatted test file did exactly that on\n# 2026-09-23 - gate green, epic PR red - and the close-out held on it.\n# `(! grep .)` prints any unformatted file and fails if there was one; it is\n# written without $ so the Makefile's gate target can carry the same line.\ngo = { command = \"gofmt -l . | grep -v '^contracts/' | (! grep .) && go vet ./... && go test -short -timeout 45m -parallel 12 ./...\", description = \"Go: gofmt, vet and tests, as CI runs them\" }\n\n# The TypeScript half, added by tick odc. Until it, NOTHING ran cloud/factory's\n# tests \u2014 not this gate, not CI \u2014 and the cost of that silence was tick b9w: the\n# control plane sat pinned to contract bundle 3.0.0 against a 5.2.0 bundle,\n# wrong about three record shapes, for two major versions.\n#\n# What is here and what is NOT is a measurement, not a preference. On this host:\n# pnpm install from a warm store 1s, contracts:check under 1s, tsc --noEmit 2s,\n# vitest 82s. Gate commands run SERIALLY, so vitest would take the gate from\n# 3m26s to about 4m48s on every tick \u2014 and the gate's speed is what made this\n# epic survivable at all.\n#\n# So the gate takes the two cheap checks and CI takes vitest, where it runs as\n# its own job beside the 7m15s Go job and costs no wall clock at all. The split\n# is stated rather than implied: THIS GATE COVERS CONTRACTS AND TYPES, NOT\n# BEHAVIOUR. A behavioural TypeScript regression is caught by CI on the epic PR,\n# before anything reaches main, and never by this line.\n#\n# contracts:check is the one that earns its place here: it is the check that\n# would have caught b9w's drift the day it happened, and it costs a second.\n#\n# `pnpm lint` is Biome (tick ncr) \u2014 this side's gofmt AND its vet, in one Rust\n# binary. It is IN this command rather than a sibling [testing.commands] entry\n# for one measured reason: a sibling would repeat `pnpm install`, which is ~0.7s\n# on a warm store, to learn nothing the &&-chain's own output does not already\n# name. Biome itself is 0.47/0.55/0.64s over three runs on these 109 files, so\n# the gate's TypeScript half goes from ~3s to ~3.6s and the whole gate is\n# unmoved against the 3m26s Go side.\n#\n# `pnpm lint` is `biome ci --error-on-warnings`, and the flag matters: plain\n# `biome ci` exits 0 with warnings still on the floor, and most of what Biome\n# found here \u2014 the unused variable, the implicit anys \u2014 is warning severity, so\n# without it this half cannot refuse what it was added to catch.\n#\n# This is still CONTRACTS AND TYPES, not behaviour: Biome reads one file at a\n# time and answers about its shape, not about what the Worker does.\n#\n# The two-layout fallback is GONE (tick 9a8). It existed only while Phase 4's\n# l9n moved cloud/factory to cloudflare/: this command is read from the RUN'S\n# checkout while it runs against the MERGED tree (defect 19q), so across the\n# move the two disagreed and l9n could never have passed the gate whose job it\n# was to move. Phase 4 merged to main on 2026-09-19 and neither cloud/factory\n# nor cloud/sandbox exists any more, so the fallback arm named nothing.\nts = { command = \"cd cloudflare && pnpm install --frozen-lockfile --prefer-offline && pnpm lint && pnpm contracts:check && pnpm exec tsc --noEmit\", description = \"TypeScript format, lint, contracts and types\" }\n\n[environment.commands]\nwhich-go = { command = \"which go\", description = \"Go toolchain on PATH\" }\ngit-config-user-email = { command = \"git config user.email\", description = \"git identity configured\" }\n"
  }
 },
 {
  "kind": "defect",
  "title": "runners.cloud.toml redeclares the same dead args line and explains it",
  "severity": "medium",
  "body": "The same dead pairing survives independently in the cloud's own routing: .tick/runners.cloud.toml's [roles.implement] declares args = [\"--approve\"] (line 24), which flows into every cloud resolution and into the glm named config's (its implement cell declares no args of its own), and [configs.claude.roles.implement] keeps an args = [] whose comment (lines 155-157) explains clearing \"the --approve the cloud's own implement cell hands pi\" \u2014 a sentence the cleanup makes false. The protected_change is the whole replacement content of the file, byte-verified against this tree: it drops the line, says on the cell why there are no args, and rewords the clearing's comment to the rule it actually guards. The clearing itself STAYS, deliberately: it is the only mechanical guard of the operator's 2026-09-10 rule (a cell that switches kind keeps the argv beneath it unless it says so) that this cell has, and the guard test that would take over its job cannot land green until the routing change does. Every value line is unchanged; with the comment lines removed the new file is the old one minus the dead line. Lands together with the runners.toml paste, or the sweep still reports the cloud's four findings.",
  "evidence": ".tick/runners.cloud.toml:24 and :155-162; internal/runconfig/schema_configs_test.go:162 (the repo-side comment this attempt reworded in commit d516c37)",
  "protected_change": {
   "path": ".tick/runners.cloud.toml",
   "content": "# Overrides for runs on the CLOUD substrate (tick 5uo): merged over\n# .tick/runners.toml, with these role cells applied LAST \u2014 over each role's\n# own values and over any tier the common file declares. Tables merge\n# field-wise; arrays replace.\n#\n# The operator's rule: the cloud runs Workers AI models only, through the\n# factory's Workers AI gateway \u2014 so nothing in the cloud runs claude. Which\n# Workers AI model is a choice, not the rule: today it is pi on GLM 5.3 /\n# GLM 5.3 Flash, and it may change. A role this file does not declare is REFUSED at run start\n# under the cloud substrate (ErrNoCloudRouting), never a fall back to the\n# common cell, which names a harness a container cannot run at a price nobody\n# agreed to (hn0). Every role declared here must name its kind.\n#\n# The container is TOLD its substrate by whatever boots it\n# (TICKS_SUBSTRATE=cloud, set by the staged orchestrator entrypoint) \u2014 this\n# file is read, never rewritten. A tier declared only in runners.local.toml\n# does not exist here: the cloud never reads that file.\nversion = 2\n\n# No `args` on this cell (tick q6z): the roles table's args are read only\n# when a herdr dispatch compiles an agent's argv, and a cloud container is\n# booted over its sandbox door from the worker.json the door hands it. The\n# line that sat here was the deleted pi CLI's `--approve`, dead config on\n# this substrate too.\n[roles.implement]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[roles.implement.tiers.economy]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash\"\neffort = \"medium\"\n\n# The dear rung, named explicitly: GLM 5.3, the same model the role's base\n# cell runs. Both rungs are Workers AI models already proven through pi in\n# the container image.\n[roles.implement.tiers.strong]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# The cloud's two-rung Workers AI ladder (tick 7l1; operator decision\n# 2026-09-30, option B): an implementation tick starts on GLM 5.3 Flash and\n# climbs to GLM 5.3 after a failed attempt, capped there. step = 2 so one\n# failure goes straight from economy to strong: balanced is not a rung this\n# ladder declares (in the cloud it would resolve GLM 5.3 anyway, under a\n# name that says less).\n#\n# No tick starts on the classifier (operator decision 2026-10-04). Every\n# implementation tick starts on Flash and climbs to GLM 5.3 on failure. The\n# dear-mass rule this policy used to carry (dear_work_types / dear_tier /\n# mass_threshold: start at strong when more than 0.75 of Jev's probability\n# sits on design + diagnosis) is switched off because it has no predictive\n# signal: docs/classifier-eval-2026-10-04-jev-clef.md (tick r3y, PR #206)\n# measured dear mass against real outcomes at AUC 0.37-0.42 for Jev, Clef and\n# Clef-flash alike \u2014 worse than a coin. Classification still runs and is\n# recorded on the run branch (it is cheap, and it is the data a later\n# question \u2014 size/effort, or a classifier fine-tuned on outcomes \u2014 needs).\n# Re-enable only with a rule a fresh evaluation shows predicts something.\n# Role jobs (review, closeout, repair, resolve) are never classified, and\n# their cells above resolve GLM 5.3 at every tier.\n#\n# The risk the operator accepted: Phase 2 saw Flash finish 1 of 3 real\n# ticks, and every cloud implementation tick now starts there. The ladder's\n# escalation is the safety net, and `ticfac status` reports its rate per run\n# (implement ticks started, escalations, model- vs infrastructure-caused).\n# Revisit (a stronger start, or strong as the start) if that rate is high.\n[tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\nstep = 2\n\n[roles.review]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# The ceiling-tier overlay of the review cell: the resolve-conflict and\n# plan-repair jobs route at [tier_policy].ceiling THROUGH this cell (see\n# runners.toml's [roles.review.tiers.frontier]), so the cloud's ceiling,\n# \"strong\", must be a tier this cell declares. Without it the hn6 run\n# (2026-09-30) stopped at its first merge conflict; ticfac now refuses a run\n# (and doctor, and CI) whose on-demand jobs cannot route. GLM 5.3 through pi,\n# a Workers AI model: nothing in the cloud runs claude, and this cell's\n# kind/model above apply over the common file's claude frontier tier.\n[roles.review.tiers.strong]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[roles.closeout]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# Named run configs (tick tda): one repository, more than one cloud routing.\n# [configs.glm] is the Workers AI routing this file's own cells have always\n# declared \u2014 pi on GLM 5.3 / GLM 5.3 Flash, the default, so an epic that says\n# nothing runs exactly as before. [configs.claude] is the claude-sub rung\n# (spike jvj, production wiring 6fv): claude on the CLI's own versionless\n# aliases \u2014 implement starts on sonnet and climbs to opus, review and\n# close-out run on opus (operator decision 2026-10-06) \u2014 the token injected\n# by the factory's subscription wiring and never in a container, stepping\n# down to GLM 5.3 on Workers AI when no subscription is free (the rung's own\n# fallback, a live fact; a run on a claude config with NO subscription\n# configured is refused at start, at doctor, and at the submission preflight\n# \u2014 the rung being off is a defect, not a fallback). A config is a ROUTING\n# and nothing else: the gate, the evidence table, the substrate and the\n# findings routes are this repository's, the same for every run. A run\n# selects by `--config claude`, over the epic's own `config: claude` label,\n# over the declared default \u2014 one precedence, resolved once at run start.\n#\n# The claude config's concurrency caps (claude-sub 1\u20132): opus, the\n# subscription's most expensive rung, one worker at a time; sonnet two.\n# The step-down arithmetic of a busy subscription is the factory's own; what\n# this declares is how much of the subscription THIS repository asks for at\n# once, per tier, exactly the way the Workers AI ladder declares its width.\n[configs]\ndefault = \"glm\"\n\n[configs.glm.roles.implement]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[configs.glm.roles.implement.tiers.economy]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash\"\neffort = \"medium\"\n\n# The dear rung, named explicitly: GLM 5.3, the same model the role's base\n# cell runs. Both rungs are Workers AI models already proven through pi in\n# the container image.\n[configs.glm.roles.implement.tiers.strong]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[configs.glm.roles.review]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n# The ceiling-tier overlay the on-demand jobs route through (see the review\n# cell's comment below): the glm config's ceiling is strong, so this config's\n# review cell must declare it.\n[configs.glm.roles.review.tiers.strong]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[configs.glm.roles.closeout]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\neffort = \"high\"\n\n[configs.glm.tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\nstep = 2\n\n# `args = []` keeps this claude cell from inheriting whatever argv the cells\n# beneath it may one day declare: a cell that switches kind keeps the argv\n# beneath it unless it says so (the operator's 2026-09-10 rule), and the\n# claude harness takes no pi flag. It cleared the `--approve` the implement\n# cell above carried until tick q6z removed it, and it stays as the guard.\n[configs.claude.roles.implement]\nkind = \"claude\"\nmodel = \"sonnet\"\neffort = \"high\"\nargs = []\n\n# The cloud claude ladder the operator named (2026-10-06): implement starts\n# on sonnet and climbs to opus, step = 2 so one failed attempt goes straight\n# up. The aliases are the claude CLI's own versionless words, so they name\n# the latest model the IMAGE'S PINNED CLI knows \u2014 never a pinned model id,\n# which bills per token in the cloud.\n[configs.claude.roles.implement.tiers.economy]\nmodel = \"sonnet\"\neffort = \"high\"\n\n[configs.claude.roles.implement.tiers.strong]\nmodel = \"opus\"\neffort = \"high\"\n\n[configs.claude.roles.review]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n# The ceiling-tier overlay the on-demand jobs (resolve-conflict,\n# plan-repair) route through: the claude config's ceiling is strong, so its\n# review cell declares the strong tier \u2014 opus, the judgement rung.\n[configs.claude.roles.review.tiers.strong]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n[configs.claude.roles.closeout]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n\n[configs.claude.tier_policy]\ndefault = \"economy\"\nceiling = \"strong\"\nstep = 2\n\n# claude-sub 1\u20132: the subscription's own width, asked per tier \u2014 sonnet two\n# at a time, opus one. A lease the factory cannot grant steps the role down\n# to the rung's Workers AI fallback; the cap is what keeps this repository\n# from asking for the whole subscription at once.\n[configs.claude.tier_policy.concurrency]\neconomy = 2\nstrong = 1\n\n"
  }
 },
 {
  "kind": "proposal",
  "title": "Guard this repository's implement routing against the dead args pairing",
  "severity": "low",
  "body": "Nothing mechanical keeps args out of the implement role once this tick's protected changes land, and nothing did before: the pairing has stood in the routing since Phase 2 (the comment is dated 2026-09-10) and was dead from the moment the pi CLI went with its worker path (2026-10-04), because no test reads this repository's own routing for it. Land TestThisRepositorysImplementRoutingCarriesNoArgs in internal/reconcile beside the existing sweeps over the same files (routing_check_test.go): resolve implement through runconfig.LoadForConfig on every substrate (herdr, harness, cloud), through every declared named config, at base values and at every tier a pin can reach, and refuse any args on the role \u2014 plus a synthetic bite in a temp directory that re-adds args = [\"--approve\"] on the cell and on a tier and must be flagged on both. It cannot land in the tick that delivers the routing change: it is red while the pairing stands, and a red guard fails the per-tick gate and the epic PR's CI, while the protected change lands only after the close-out. Land it with or immediately after the labelled commit, where it turns green the moment it arrives; once it is green, the two manual args = [] clearings (the cloud's claude config cell and the local frontier rung) can go if you would rather have the guard than the manual protection.",
  "evidence": "internal/reconcile/routing_check_test.go:36 and :350 (the sweeps over this repository's own .tick/runners*.toml this guard joins); this attempt's throwaway found 22 findings at base and 0 against the replacement content"
 },
 {
  "kind": "defect",
  "title": "2p3 asks for the same routing edit this tick delivers",
  "severity": "low",
  "body": "Open tick 2p3 (\"runners.toml implement cell still describes the deleted pi CLI\", from uxi's finding c1dded50\u2026) names the same cell, the same comment and the same args line as q6z, and this run's attempt 9 was collected as a blocked answer with no report written and the run held on it. Nothing in 2p3 remains that a dispatched worker can write, so another dispatch is another attempt into the same refusal. The tracker_edit appends the note that closes the loop: q6z's protected changes carry the exact replacement content for both routing files, and 2p3 is done by the labelled commit that applies them.",
  "evidence": ".tick/issues/2p3.json and .tick/issues/q6z.json, both open, both parented to ex6; .ticfac/runs/run_69f8f57832d34601b040367c7207b81f/decisions/15.json (2p3 attempt 9, BLOCKED)",
  "tracker_edit": {
   "tick": "2p3",
   "field": "notes",
   "value": "2026-10-08 00:09 - tick q6z (this run's dispatch, worker-proposed note): the edit 2p3 asks for is delivered by q6z. q6z's report carries the exact replacement content for .tick/runners.toml's [roles.implement] cell \u2014 the same cell, comment and args line 2p3 names \u2014 and for the same dead pairing .tick/runners.cloud.toml redeclares, as protected_change objects the run applies itself as a labelled commit after the close-out (.tick/ is a path no dispatched worker may write). Do not dispatch another attempt into that wall: verify the labelled commit carries both cells and close 2p3 over it."
  }
 }
]
```

STATUS: DONE — the tick's named deliverable (the dead args = ["--approve"] line in .tick/runners.toml's [roles.implement] cell and the comment that explains the deleted pi CLI's trust model as if it launched, plus the same dead pairing .tick/runners.cloud.toml redeclares) is delivered as two byte-verified protected_change objects in the findings block, which the run applies itself onto the epic branch after the close-out because .tick/ is a path no dispatched worker may write; the branch carries one commit (d516c37) for the last repo-side sentence that stated the pairing as live, and the repository-side sweep was already complete and gate-green.
