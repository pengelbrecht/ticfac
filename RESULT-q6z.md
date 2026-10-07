<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-20/q6z`, base `f4945ff3ed87912f6c92c4095ae637ecf7045a8c`, harness `pi-durable` exited 0, 1 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `f4945ff3ed87912f6c92c4095ae637ecf7045a8c` is the head of the work it continued, which was cut from `ff2752098d6af41836d18bb9dafb5adb80db66d4`; its work commits are counted from the carried head._

# RESULT — tick q6z: `[roles.implement] args = ["--approve"]` is dead config after the herdr re-cut

## What changed — one commit, `0b0f5ed`

**The repository's own generator of the dead pairing no longer writes it.** `ticfac init`
was emitting `args = ["--approve"]` beside `kind = "pi"` — into the pi cells of the common
`runners.toml` it writes, and into all three role cells of the cloud file — with a comment
that explained the deleted pi CLI's trust model as if it launched. That is the same defect
one level up: every repository `ticfac init` creates was told, in its own routing, that its
implement workers run with a flag nothing reads.

- `internal/cli/init.go` — no `args` are written at all, and each generated file says so in
  one short comment placed where a reader looks for the flag; the full reasoning (the one
  consumer, the refusals, where each harness's argv really comes from) is in the Go
  comment beside the emission.
- `internal/cli/init_test.go` — `TestInitWritesNoArgsIntoTheRoutingItGenerates`, written
  test-first. It is red on the old code for all three answers (local `--runner pi`,
  `both`, cloud-only), over three assertions: the generated text names no `--approve`; no
  role cell, tier or substrate overlay in the generated files declares `args`; and no
  role resolves with `args` on any substrate the answer serves — the exact value a herdr
  dispatch's `spawnArgv` would read. Green after the fix.

Why I judged this in scope for q6z rather than another tick: the tick is *about* this dead
pairing and its misleading comment, and its own text frames it as "jhp's sweep of the
pi-CLI remnants" — the init template's `--approve` line is exactly such a remnant (the
flag went with the pi CLI in jhp/uxi), and it is the only place the pairing is
still emitted as live configuration that a dispatched attempt can write
(the herdr metering fixtures that spell the old argv are frozen history of
that executor's join, not configuration). Attempt 19 filed it as a proposal
finding; it is delivered here instead, so the reviewer can attribute the whole
diff to this tick's subject. Nothing else in the
repository carries the pairing as live instruction: `internal/runconfig/runners-config.md`
still uses `--approve` in one parenthetical example of the field-by-field config-merge rule
(line 246), but the same document already states the pi-CLI spawn rules are history and that
no `args = ["--approve"]` licenses a worker (line 298), so I left it alone.

## What this attempt could not change — the tick's named target

`.tick/runners.toml`'s `[roles.implement]` cell still carries the line and the comment. It
is a `.tick/` path, and this substrate refuses it twice over: the container's pre-commit
hook refuses any staged `.tick/` path, and the cloud collect returns `boundary-violation`
for any `.tick/` diff, discarding the attempt wholesale. The run's own boundary vocabulary
says the opposite (`internal/exec/subprocess/report.go`'s `exemptFromBoundary` names
`.tick/runners.toml` as a file a worker "may legitimately amend"), and the one worker→run
write channel — the tracker-edits block — carries tick-record prose fields only, so the
operator's edit cannot be proposed through it either. I did not write under `.tick/` and
did not bypass the hook: this attempt's guard ledger (`/work/repo.guard/attempts`) is
empty. This is the wall ticks 26g, z2x, g7p and ky7 already carry.

## The operator's paste — verified against this tree, ready to land

Verified, not composed blind: applied to a copy of this repository's own
`.tick/runners.toml`, the block below matches byte for byte, parses through the production
reader (`runconfig.LoadFor`), leaves `kind`/`model`/`effort` exactly as they stand, and
clears the args at every tier the cell declares. The Phase 2 routing paragraph above it is
untouched on purpose.

Replace this block in `.tick/runners.toml`:

```toml
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
# No `args` here, deliberately (tick q6z): the roles table's args have exactly
# one consumer — internal/cli/executor.go's spawnArgv, reached only for a herdr
# dispatch — and since 2q5 implement routes onto the local-subprocess executor,
# which launches the durable host from its own runner table and reads no argv
# from here; the cloud container is booted from the door's worker.json; and
# runconfig.Compile refuses kind "pi" for a herdr pane (uxi), so the pairing
# could not compile where it used to be read. The line that sat here was the
# deleted pi CLI's `--approve` — project-local file trust, verified live
# 2026-09-10 (tick gjk; .tick/logs/herd/av8/gjk.RESULT.md) — dead config on
# every substrate once the CLI went with its worker path (jhp, uxi): it told a
# reader of this file that implement workers somewhere run with --approve.
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"
```

### The guard that makes it stick — land it with the paste

It cannot ride this branch: it is red while the line stands, and a red guard fails the
gate. Compiled and run against this tree, it fails with the nine findings that are the
tick's claim (the cell's own line, plus every local-world tier except `frontier`, which
`runners.local.toml` already clears), and the same assertions pass against a patched copy
of the file. Land it as `internal/reconcile/implement_args_test.go` in the same edit — a
worker can commit it too, once the cell is fixed.

```go
package reconcile

import (
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// [roles.implement] in this repository's own routing carried
// args = ["--approve"] — the deleted pi CLI's project-local file-trust flag
// (verified live 2026-09-10, tick gjk) — long after nothing could read it
// (tick q6z): the roles table's args have exactly one consumer, spawnArgv
// (internal/cli/executor.go), reached only for a herdr dispatch, and
// runconfig.Compile refuses kind "pi" for a herdr pane since uxi; since 2q5
// implement routes onto the local-subprocess executor, which launches the
// durable host from its own runner table, and a cloud container is booted
// from the door's worker.json. The line stayed on as dead config whose
// comment still explained the pi CLI's trust model as if it launched: a
// reader of runners.toml would believe implement workers somewhere run with
// --approve. This guard keeps it out: the common file declares no args on
// implement, and every resolution the two local worlds can produce for the
// role, at every tier, carries none.
//
// The cloud overlay (.tick/runners.cloud.toml) is deliberately outside this
// guard: it declares its own [roles.implement] cell, a worker's boundary does
// not let this repository's ticks write that file, and the same dead pairing
// there is the operator's edit to make.
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

	cfg, err := runconfig.LoadFor(config, "")
	if err != nil {
		t.Fatal(err)
	}
	cell := cfg.Roles["implement"]
	if cell == nil {
		t.Fatal("[roles.implement] is missing from .tick/runners.toml")
	}
	if len(cell.Args) != 0 {
		t.Errorf("[roles.implement] declares args %v: dead config since 2q5 — the roles table's args are read "+
			"only by a herdr dispatch's spawnArgv, runconfig.Compile refuses kind \"pi\" for a herdr pane, and "+
			"implement routes onto the local-subprocess executor, which launches the durable host from its own "+
			"runner table (tick q6z)", cell.Args)
	}

	for _, sub := range []runconfig.Substrate{runconfig.SubstrateHarness, runconfig.SubstrateHerdr} {
		world, err := runconfig.LoadFor(config, sub)
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		for _, tier := range append([]runconfig.Tier{""}, runconfig.TierNames...) {
			w, err := world.ResolveOn(sub, "implement", tier)
			if err != nil {
				// Every tier name here is in the runconfig vocabulary; a tier
				// the merged cells do not declare is the profile layer's
				// refusal to make, never a silent fall back, so an error is a
				// routing defect in its own right.
				t.Errorf("substrate %q does not resolve implement at tier %q: %v", sub, tier, err)
				continue
			}
			if len(w.Args) != 0 {
				t.Errorf("substrate %q resolves implement at tier %q with args %v: dead config on every "+
					"substrate (tick q6z)", sub, tier, w.Args)
			}
		}
	}
}
```

## What I ran, in order

- `go test -run 'TestInitWritesNoArgsIntoTheRoutingItGenerates' ./internal/cli/` — FAIL
  on the untouched tree, all three subtests, then PASS after the fix.
- `go test -short -count=1 -timeout 45m ./internal/cli/` — ok, 54s.
- The paste's two verifications (throwaway tests, removed before the commit): the
  replacement applies to `.tick/runners.toml` as it stands, parses, and clears the args;
  the handed-over guard's assertions pass against a patched copy of the same two files.
- The handed-over guard, compiled and run in `internal/reconcile` — FAIL at base with the
  nine findings named above (removed before the commit; it cannot ride a green gate).
- `make gate GOTEST_PARALLEL=4` — exit 0, twice (gofmt, `go vet ./...`, the whole-repo
  short suite, 47 packages ok; 1m35s on the second run).
- `ticfac-exec-subprocess lint-report RESULT-q6z.md --role implement-tick --tick q6z` —
  exit 0.

## What the next attempt / the operator has to know

- **Nothing in this report needs re-deriving.** The repository-side half of the tick is
  done and committed on this branch; the named target is one paste (above), verified
  against this tree; the wall is already filed four times over (26g, z2x, g7p, ky7). If
  this tick is escalated again, the next worker should read this report, confirm the paste
  still applies, and answer the same way — there is no work left that a dispatched attempt
  can do.
- **The paste is one edit that closes more than q6z.** 2p3 asks for the same cell with the
  same words, and the same dead pairing stands in `.tick/runners.cloud.toml:24` and (as a
  clearing of nothing, once the common file is fixed) `.tick/runners.local.toml:16`, with
  `.tick/runners.cloud.toml:155-162` explaining a `--approve` it clears. One operator
  session finishes all of it; the guard above pins the common file's half.
- **The guard cannot land before the paste.** It is red while the line stands. Land it in
  the same edit, or dispatch it to the next attempt of this tick once the cell is fixed —
  then it is green and the config cannot come back unnoticed.
- The `--approve` I removed from the templates is dead by the same reasoning as the tick's
  own: `spawnArgv` (internal/cli/executor.go:335) is the only caller of `runconfig.Compile`,
  the only reader of roles-table args, and it is reached only for herdr dispatches, for
  which `runconfig.Compile` refuses `kind = "pi"` (internal/runconfig/compile.go:159);
  the local-subprocess runner's argv is compiled from `internal/exec/subprocess`'s own
  `runners` table (`pi` = the durable Node host, `--config <worker.json> --message
  <prompt>`), and the cloud container is booted from the door's `worker.json`.

```findings v2
[
  {
    "kind": "defect",
    "title": "A tick whose deliverable is .tick/ config cannot be delivered here",
    "severity": "medium",
    "body": "The run's own boundary tells a worker it may amend .tick/runners.toml (internal/exec/subprocess/report.go's exemptFromBoundary), but this substrate refuses it twice over: the container's pre-commit hook refuses any staged .tick/ path, and the cloud collect returns boundary-violation for any .tick/ diff, discarding the attempt wholesale. q6z's remaining deliverable is exactly such a cell, as are 2p3's and 96s's, so every dispatched attempt of them can only end BLOCKED. This is the wall 26g, z2x, g7p and ky7 already carry — filed from this attempt's own evidence so its BLOCKED names it, and it will dedupe against them.",
    "evidence": ".git/hooks/pre-commit; cloudflare/src/worker-collect.ts:313 and :530; internal/exec/subprocess/report.go:190-194; this attempt's guard ledger /work/repo.guard/attempts is empty (no write attempted)"
  },
  {
    "kind": "defect",
    "title": "Dead --approve remnants stand in runners.cloud.toml and runners.local.toml",
    "severity": "low",
    "body": "The common file's implement cell is q6z's named target, but the same dead pairing survives in the two routing files a worker's boundary does not let a tick write: .tick/runners.cloud.toml's [roles.implement] still carries args = [\"--approve\"], and it flows into any named config's resolution whose own cell declares no args; .tick/runners.local.toml's [roles.implement.tiers.frontier] carries args = [], a clearing of the line the common file is to drop. The cloud file's [configs.claude.roles.implement] also keeps an args = [] whose comment explains clearing the --approve. All inert, all misleading the same way; fold them into the same operator edit as q6z's paste.",
    "evidence": ".tick/runners.cloud.toml:24 and :155-162; .tick/runners.local.toml:16"
  },
  {
    "kind": "defect",
    "title": "q6z and 2p3 ask for the same one-paste edit to the same cell",
    "severity": "low",
    "body": "Two open blockers of ex6's final review carry the same deliverable: q6z (from 2q5's finding bab43662…) and 2p3 (from uxi's c1dded50…) both ask for the [roles.implement] args line and its comment in .tick/runners.toml to go, and 2p3 is already three attempts into its ladder. One operator paste closes both, but the run is running two ladders for it. A person should close one as the other's duplicate — nothing here needs a worker, and a tick dispatched to merge them would hit the same .tick/ wall as the ticks it merges.",
    "evidence": ".tick/issues/q6z.json and .tick/issues/2p3.json, both open, both blockers of 0rt"
  }
]
```

STATUS: BLOCKED — the tick's named deliverable is one cell in `.tick/runners.toml`, a path no dispatched attempt may write on this substrate (the container's pre-commit hook refuses any staged `.tick/` path and the cloud collect refuses any `.tick/` diff), so the `args = ["--approve"]` line and its comment still stand there; the exact, verified replacement and the guard that pins it are in this report for the operator (or the run's own writer, once a config-edit channel exists — ky7), while everything the repository itself could reach is done, committed and gate-green on this branch.
