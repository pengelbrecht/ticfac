<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-21/q6z`, base `2364d3703e949aa3ac5e48d044222bb8325d9952`, harness `pi-durable` exited 0, 0 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `2364d3703e949aa3ac5e48d044222bb8325d9952` is the head of the work it continued, which was cut from `ff2752098d6af41836d18bb9dafb5adb80db66d4`; its work commits are counted from the carried head._

# RESULT — tick q6z, attempt 21: `[roles.implement] args = ["--approve"]` is dead config after the herdr re-cut

Attempt 20's report (committed in this branch's base) did the repository-side half of
this tick — `ticfac init` no longer writes the dead pairing — and that work is merged
and green on this tree. What the tick NAMES, though, is one cell of
`.tick/runners.toml`, and that line and its comment still stand there, because a
dispatched attempt may not write under `.tick/` on this substrate. This attempt
re-verified everything from scratch against the current tree, widened the operator
paste to every file the pairing survives in (verified byte for byte), and hardened the
guard that pins it (22 findings at base, all of them the tick's claim). There is no
commit on this branch: nothing in a worker's write boundary remained to change.

## What this attempt re-verified, itself

- **The repository-side half is green.** `TestInitWritesNoArgsIntoTheRoutingItGenerates`
  (internal/cli/init_test.go, attempt 20's test) passes on this tree, and `make gate`
  exits 0 over it. `internal/cli/init.go` writes no `args` into any routing it
  generates, and says so once in each file, where a reader looks for the flag.
- **The pairing still stands where the tick names it.** `.tick/runners.toml:17-22`
  carries `args = ["--approve"]` under the comment that still explains the deleted pi
  CLI's trust model as if it launched. The same dead line survives independently in
  `.tick/runners.cloud.toml:24` (the cloud's own `[roles.implement]` cell, which
  redeclares it, so it flows into every cloud resolution and into the `glm` named
  config's, whose implement cell declares no args of its own), and two `args = []`
  clearings exist only to cancel it: `.tick/runners.cloud.toml:155-162` (with a comment
  that explains clearing the `--approve`) and `.tick/runners.local.toml:16`.
- **The wall is real and unchanged.** Three layers refuse a worker's write to the
  cell: the container's pre-commit hook (`.git/hooks/pre-commit`, installed by
  `image/worker.sh:529`, refuses any staged `.tick/` path), the harness's `tk` shim
  (`harness/src/env/boundary-guard.ts`), and the cloud collect
  (`cloudflare/src/worker-collect.ts:313`, `:530`: any `.tick/` path in a branch diff
  is a `boundary-violation` that refuses the whole branch). The local Go collect would
  in fact honour the exemption (`internal/exec/subprocess/report.go`'s
  `exemptFromBoundary` names `.tick/runners.toml`), but the hook makes the commit
  impossible before the collect ever runs, and `internal/exec/subprocess/prompt.go`'s
  own "you MAY write these" clause is a fourth disagreement. This is the wall open
  ticks **26g, z2x, g7p and ky7** already carry; I did not attempt the write, did not
  bypass the hook, and did not file it again — this attempt's guard ledger
  (`/work/repo.guard/attempts`) is empty, and a fifth draft of a four-times-filed
  finding is noise, not evidence. Everything below is delivered through the report,
  which is the one channel a worker owns.

## The one operator paste — every file the pairing survives in

Verified against THIS tree, not composed blind: each old block below matches its
current file exactly once (byte for byte), the patched copies parse through the
production reader (`runconfig.LoadForConfig`) on every substrate × every declared
named config, the sweep below comes back empty against them, and every kind, model and
effort resolution is exactly what it is today — the paste may only take the args away.
A throwaway test did this and was removed before the gate; the four replacement pairs
are the whole edit, and one operator session closes this tick, 2p3, and the two
remnant files in it.

### 1. `.tick/runners.toml` — the tick's named target (and 2p3's)

Replace this block:

```toml
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

with:

```toml
[roles.implement]
# Operator routing for Phase 2 (2026-09-10; comment reworded tick q6z).
# Implementation runs on GLM on cloudflare through pi: GLM 5.3 for complex
# work (the default, and the `strong` tier) and GLM 5.3-flash for simple work
# (`economy`). opus is the claude harness and not a pi model at all; for
# implement it is the local `frontier` escalation (runners.local.toml) and the
# cloud's named `claude` config, never a starting tier — review and closeout
# run on it by the operator's own cells below.
#
# No `args` on this cell, deliberately (tick q6z): the roles table's args
# have exactly one consumer — spawnArgv (internal/cli/executor.go), read only
# when a dispatch spawns a herdr pane — and runconfig.Compile refuses
# kind "pi" for a herdr pane (uxi), so nothing can read a flag from here:
# the local-subprocess executor launches the durable pi host from its own
# runner table, and a cloud container is booted from the worker.json its
# sandbox door hands it. The line that sat here was the deleted pi CLI's
# `--approve` — project-local file trust, verified live 2026-09-10 (tick gjk;
# .tick/logs/herd/av8/gjk.RESULT.md) — dead config on every substrate since
# the CLI went with its worker path (jhp, uxi): it told a reader of this
# file that implement workers somewhere run with --approve. pi needs no
# permission-bypass flag in any case: it runs tools unprompted.
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
```

The first paragraph keeps the operator's routing rationale (dated 2026-09-10) rather
than dropping it — attempt 20's paste deleted it wholesale — but corrects its one
stale claim: since 2026-09-23 `opus` *is* an implement rung locally (the `frontier`
escalation in runners.local.toml) and in the cloud's named `claude` config, so "opus
is NOT an implementation tier" had stopped being true of the whole routing. The
second paragraph replaces the trust story: the reader of the cell now learns why
there are no args, not what `--approve` used to mean to a CLI that no longer exists.

### 2. `.tick/runners.cloud.toml` — the same dead pairing on the cloud's own cell

Replace this block:

```toml
[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
args = ["--approve"]
```

with:

```toml
# No `args` on this cell (tick q6z): the roles table's args are read
# only when a herdr dispatch compiles an agent's argv, and a cloud container
# is booted from the worker.json its sandbox door hands it. The line that sat
# here was the deleted pi CLI's `--approve`, dead on this substrate too.
[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
```

### 3. `.tick/runners.cloud.toml` — the clearing that explained a `--approve` it cancels

Replace this block:

```toml
# `args = []` clears the `--approve` the cloud's own implement cell hands pi:
# a cell that switches kind keeps the argv beneath it unless it says so, and
# claude does not take pi's flag.
[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"
effort = "high"
args = []
```

with:

```toml
# This cell switches implement onto the claude-sub rung. No `args` here
# (tick q6z): the cells beneath it declare none, and a cell that switches
# kind keeps the argv beneath it unless it says so — the `args = []` that sat
# here cleared a `--approve` the pi cell no longer carries. internal/reconcile's
# implement-args guard now refuses any args on this role, which is the
# protection the manual clearing used to give.
[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"
effort = "high"
```

### 4. `.tick/runners.local.toml` — the bare clearing on the frontier rung

Replace this block:

```toml
[roles.implement.tiers.frontier]
kind = "claude"
model = "opus"
effort = "high"
args = []
```

with:

```toml
[roles.implement.tiers.frontier]
kind = "claude"
model = "opus"
effort = "high"
```

Dropping the two `args = []` clearings is safe **only together with the guard below**,
which takes over their job: the clearings were manual protection against
args-inheritance-across-a-kind-change (the runners.toml defect the operator recorded
on 2026-09-10), and the guard refuses args on this role everywhere, at every tier,
world and config — strictly more than the two clearings covered. Land the guard in
the same edit, and see the third finding below for the residual this leaves on the
other roles.

## The guard that makes it stick — land it in the same edit

This is the guard attempt 20 handed over, hardened on the two things its run at base
exposed: it now covers the **cloud world and both named configs** (attempt 20's
covered the common file and the two local worlds only — nine findings), and its
"bites" section is a synthetic routing rather than a patched copy of this
repository's files, so it says the same thing whatever state the real routing is in.
Compiled and run against this tree it fails with **22 findings** — four cells
declaring the pairing (the common cell, both local worlds' merged cell, the cloud's,
and the `glm` config's inherited one) and eighteen resolutions resolving implement
with `--approve` (local base/economy/balanced/strong ×2, cloud
base/economy/balanced/strong/frontier ×2 configs); the local `frontier` rung and the
`claude` config are clean only because their `args = []` clearings cancel the line —
which is exactly why the paste drops the line *and* the clearings together. Against
the patched copies of the same files the same sweep returns nothing.

Land it as `internal/reconcile/implement_args_test.go` — a worker can commit it too,
once the cell is fixed; until then it is red, and a red guard fails the gate.

```go
package reconcile

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The implement cell of this repository's routing carried
// args = ["--approve"] — the deleted pi CLI's project-local file-trust flag,
// verified live 2026-09-10 (tick gjk; .tick/logs/herd/av8/gjk.RESULT.md) —
// long after nothing could read it (tick q6z). The roles table's args have
// exactly one consumer, spawnArgv (internal/cli/executor.go), reached only
// when a dispatch spawns a herdr pane, and runconfig.Compile refuses
// kind "pi" for a herdr pane since uxi; since 2q5 implement routes onto the
// local-subprocess executor, which launches the durable pi host from its own
// runner table, and a cloud container is booted over its sandbox door from
// the worker.json the door hands it. A flag no executor reads is not inert
// config: it tells a reader of runners.toml that implement workers somewhere
// run with --approve. This guard keeps the pairing out of every file this
// repository's routing is read from — the common cell, the two substrate
// overlays (the cloud cell redeclared the pairing independently), and every
// named config's cells — and off every resolution every world can produce,
// at base values and at every tier a pin can reach: the exact value a herdr
// dispatch's spawnArgv would compile into an agent's argv.
//
// short: reads this repository's .tick/runners*.toml and resolves roles in
// memory; no harness, no git, milliseconds.
func TestThisRepositorysImplementRoutingCarriesNoArgs(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".tick", "runners.toml")
	for _, problem := range sweepImplementArgs(config) {
		t.Error(problem)
	}

	// The sweep bites: a synthetic routing that re-adds the pairing — the
	// historic line on the implement cell, and a tier of its own — must be
	// flagged on the cell AND on the resolution, or the sweep certifies
	// nothing. It is a synthetic file, not a copy of this repository's, so
	// the bite says the same thing whatever state the real routing is in.
	dir := t.TempDir()
	writeTOML(t, filepath.Join(dir, "runners.toml"), `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
args = ["--approve"]

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"
`)
	problems := sweepImplementArgs(filepath.Join(dir, "runners.toml"))
	if len(problems) == 0 {
		t.Fatal("the re-added args = [\"--approve\"] passed the sweep")
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"--approve", "declares args", `tier "economy"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep's report does not name %s:\n%s", want, joined)
		}
	}
}

// sweepImplementArgs resolves implement against config on every substrate a
// run of this repository executes on — through the file's own cells and every
// named config they declare — and returns every place the role carries args,
// as strings rather than t.Errorf calls so the guard's own test can also
// point it at a config it must flag. ResolveOn is the seam spawnArgv reads
// (internal/cli/executor.go), and it is fail-open about tiers: a tier no cell
// declares resolves at the role's base values, so every tier a pin can reach
// resolves to something here, and none of those answers may carry args.
func sweepImplementArgs(config string) []string {
	var problems []string
	tiers := []string{""}
	for _, tier := range runconfig.TierNames {
		tiers = append(tiers, string(tier))
	}
	for _, sub := range []runconfig.Substrate{
		runconfig.SubstrateHerdr, runconfig.SubstrateHarness, runconfig.SubstrateCloud,
	} {
		world, err := runconfig.LoadFor(config, sub)
		if err != nil {
			problems = append(problems, fmt.Sprintf("the %s world does not read %s: %v", sub, config, err))
			continue
		}
		names := append([]string{""}, world.NamedConfigNames()...)
		for _, name := range names {
			selected, err := runconfig.LoadForConfig(config, sub, name)
			if err != nil {
				problems = append(problems, fmt.Sprintf("the %s world refuses its own config %q: %v", sub, name, err))
				continue
			}
			what := fmt.Sprintf("the %s world (config %q)", sub, name)
			if cell := selected.Roles["implement"]; cell == nil {
				problems = append(problems, what+" declares no [roles.implement] cell")
			} else if len(cell.Args) != 0 {
				problems = append(problems, fmt.Sprintf("%s [roles.implement] declares args %v: dead config since 2q5 — the roles table's args are read only by a herdr dispatch's spawnArgv, runconfig.Compile refuses kind %q for a herdr pane, and implement routes onto the local-subprocess executor (tick q6z)", what, cell.Args, cell.Kind))
			}
			for _, tier := range tiers {
				rung := "base values"
				if tier != "" {
					rung = fmt.Sprintf("tier %q", tier)
				}
				w, err := selected.ResolveOn(sub, "implement", runconfig.Tier(tier))
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s does not resolve implement at %s: %v", what, rung, err))
					continue
				}
				if len(w.Args) != 0 {
					problems = append(problems, fmt.Sprintf("%s resolves implement at %s with args %v: dead config on every substrate (tick q6z)", what, rung, w.Args))
				}
			}
		}
	}
	return problems
}
```

## What I ran, in order

- `go test -short -count=1 -run 'TestThisRepositorysImplementRoutingCarriesNoArgs' -timeout 300s ./internal/reconcile/`
  — **FAIL** on the untouched tree, 22 findings, every one of them the tick's claim
  (the throwaway file carrying it was removed before the gate; its text is above).
- `go test -short -count=1 -run 'TestTheQ6zPasteVerifiesAgainstThisTree' -timeout 300s ./internal/reconcile/`
  — **PASS**: each of the four old blocks matches its current file exactly once, the
  patched copies parse on every world and config, the sweep returns nothing against
  them, and every kind/model/effort resolution is unchanged. Throwaway, removed.
- `go test -short -count=1 -run 'TestInitWritesNoArgsIntoTheRoutingItGenerates' -timeout 300s ./internal/cli/`
  — **PASS** (attempt 20's repository-side half, green on this tree).
- `make gate GOTEST_PARALLEL=4` — **exit 0**, twice (gofmt, `go vet ./...`, the
  whole-repo short suite, 47 packages ok). The tree this branch leaves is exactly its
  gate-green base: this attempt commits nothing.
- `ticfac-exec-subprocess lint-report RESULT-q6z.md --role implement-tick --tick q6z`
  — exit 0.

## What the operator and the next attempt have to know

- **One session closes this tick.** The paste above (four replacement pairs) is the
  whole edit: it closes q6z and 2p3 (same cell, same words) and removes the pairing
  from the two files no dispatched actor may write. Land the guard with it — it is red
  until all four pairs land and green the moment they do, and it is the enforcement
  that lets the manual `args = []` clearings go.
- **Do not attach a tracker edit to a BLOCKED answer on this tick.** The l89
  disposition (`internal/reconcile/tracker_edits.go`) applies a BLOCKED answer's
  tracker edits itself and closes the tick over them — the honest shape only when the
  fix *is* the record write. q6z's fix is a `.tick/runners.toml` cell, not a record
  field, so a note would close this tick on the strength of a comment while the dead
  line still stands in the routing. This attempt carries no tracker-edits block for
  exactly that reason; the next attempt should not add one either. The channel that
  would carry this edit is ky7's `config-edits` proposal, still open.
- **There is no work left that a dispatched attempt can do.** The repository-side
  sweep is complete and merged (the only live `--approve` spellings outside `.tick/`
  routing are `image/entrypoint.sh` and `image/worker.sh`'s `omp --auto-approve` —
  the omp CLI's own flag, a different harness — and a synthetic fixture's prose in
  `internal/cli/executor_gate_test.go` that uses `--approve` as an example of the
  *field's* semantics, which says nothing about this repository's cells).
  `internal/runconfig/runners-config.md`'s parenthetical example of the config-merge
  rule keeps `--approve` as a hypothetical, and the same document already states no
  `args = ["--approve"]` can license a pi CLI worker; it is not a live remnant. If
  this tick is escalated again, the next worker should confirm the paste still
  applies, hand the same report, and answer the same way.

```findings v2
[
  {
    "kind": "defect",
    "title": "Dead --approve remnants stand in runners.cloud.toml and runners.local.toml",
    "severity": "low",
    "body": "q6z's named target is the common file's implement cell, but the same dead pairing survives in the two routing files a worker's boundary does not let a tick write: .tick/runners.cloud.toml's [roles.implement] carries its own args = [\"--approve\"], which flows into every cloud resolution and into the glm named config's (its implement cell declares no args of its own); .tick/runners.cloud.toml's [configs.claude.roles.implement] keeps an args = [] whose comment explains clearing the --approve; .tick/runners.local.toml's frontier tier carries a bare args = [] clearing of the same line. All inert, all misleading the same way as the tick's own target. The four replacement pairs in this report's paste fold all of them into the one operator session that closes q6z and 2p3.",
    "evidence": ".tick/runners.cloud.toml:24 and :155-162; .tick/runners.local.toml:16; this report's paste section, pair 2-4"
  },
  {
    "kind": "defect",
    "title": "q6z and 2p3 ask for the same one-paste edit to the same cell",
    "severity": "low",
    "body": "Two open blockers of ex6's final review carry the same deliverable: q6z (from 2q5's finding bab43662…) and 2p3 (from uxi's finding c1dded50…) both ask for the [roles.implement] args line and its comment in .tick/runners.toml to go, and both have been dispatched into the same .tick/ wall (2p3's ladder is spent; it is rejected in the run's checkpoint and still open in the tracker). One operator paste closes both, and this report's paste is it. A person should close one as the other's duplicate — nothing here needs a worker, and a tick dispatched to merge them would hit the same wall as the ticks it merges.",
    "evidence": ".tick/issues/q6z.json and .tick/issues/2p3.json, both open, both parented to ex6"
  },
  {
    "kind": "proposal",
    "title": "runconfig still inherits args across a kind change; only implement is guarded",
    "severity": "low",
    "body": "The operator recorded on 2026-09-10 (av8's decision log) that a tier or config cell that switches kind inherits args the new kind cannot parse, fixed then by manually clearing args = [] on every kind-switching cell, and that the real fix — a refusal in runconfig — was worth carrying into wgi. Nothing ever carried it: resolve.go still applies args by presence with no kind check. This tick's paste drops the two manual clearings because internal/reconcile's new implement-args guard refuses the pairing on that one role everywhere; review, closeout, resolve-conflict and plan-repair have no such guard, so an args line re-added to one of their cells under a switching kind would flow through unchecked. A runconfig refusal at the kind switch (or the same guard sweep over every role) is the residual fix.",
    "evidence": "internal/runconfig/resolve.go (overlay: args replace when present, no kind check); .tick/issues/av8.json notes 2026-09-10 12:49"
  }
]
```

STATUS: BLOCKED — the tick's named deliverable is one cell in `.tick/runners.toml` (plus the two remnant files folded into the same edit), a path no dispatched attempt may write on this substrate (the container's pre-commit hook refuses any staged `.tick/` path and the cloud collect refuses any `.tick/` diff — the wall open ticks 26g, z2x, g7p and ky7 already carry), so the `args = ["--approve"]` line and its comment still stand there; the complete, byte-verified four-pair operator paste and the 22-finding guard that pins it are in this report for the operator (or the run's own writer, once ky7's config-edits channel exists), while the repository-side half is done, merged and gate-green, and this branch commits nothing because nothing a worker may write remained to change.
