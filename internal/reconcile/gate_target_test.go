package reconcile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// gateTargets names the Makefile target that is each declared gate command's
// twin: the same line said once in .tick/runners.toml (what the integrated
// gate runs) and once in the Makefile (what a human and CI run).
//
// The mapping is spelled out rather than derived, so adding a check without a
// target fails TestTheGateTargetMatchesTheDeclaredGate instead of being
// discovered when one of the two silently stops running, which is exactly how
// cloud/factory's suite came to be unrun for two major bundle versions (ticks
// odc and b9w).
//
// `harness` is in this map while the cell that declares it is not in
// .tick/runners.toml yet — deliberately, and that is the one entry the map
// tolerates (see the test for why): the cell is a change to a protected file a
// worker may not commit, so the run itself applies it at the close-out, and a
// check that had to be added to this map at that same moment would hold the
// gate red from either side. TestTheDeclaredHarnessGatePairsWithItsMakefileTwin
// is what keeps the not-yet-declared entry honest in the meantime: it pairs the
// cell the run is to apply with this map's `harness-gate` target, byte for byte.
var gateTargets = map[string]string{
	"go":         "gate",
	"go-touched": "gate-touched",
	"ts":         "ts-gate",
	"harness":    "harness-gate",
}

// TestTheGateTargetMatchesTheDeclaredGate pins the two spellings of "run this
// repository's gate" to each other.
//
// .tick/runners.toml is what the integrated gate runs, and the Makefile's
// `gate` target is what a human and CI run. They were allowed to differ, and
// they did: runners.toml carried a bare `go test -short -count=1 ./...` while
// the Makefile's targets carried a measured -timeout and -parallel. So the one
// runner that can block every tick ran internal/reconcile serially against go's
// default 10-minute per-package timeout — the drift the Makefile's own header
// says pointing CI and the docs at it exists to prevent.
//
// The command stays spelled out in runners.toml rather than delegating to make:
// that file is what says what the gate runs, and a reader must see it there.
// This test is what keeps the copy honest.
//
// One direction of the old count check is gone, and the reason is tick 2pn. The
// map and the declaration it names can no longer land in one commit: the
// `harness` cell lives in .tick/runners.toml, a protected file a worker may not
// commit, so the run applies it at the close-out — AFTER this test has gated
// every attempt of the tick that asks for it. A check that failed while a map
// entry had no declaration would be red on every attempt while the cell was
// still on its way, and a check that failed the other way (a declaration with no
// map entry) is kept, because that half still lands in one commit. So the
// missing direction is this test's one documented window, not a hole: the
// undeclared entry is held to its target by
// TestTheDeclaredHarnessGatePairsWithItsMakefileTwin, which fails the moment the
// cell the run is to apply and the `harness-gate` recipe disagree.
// short: two files of this checkout compared — and it is the drift guard the gate exists to keep honest, so it belongs in every tick
func TestTheGateTargetMatchesTheDeclaredGate(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	declared, err := ReadGateCommands(filepath.Join(root, ".tick", "runners.toml"))
	if err != nil {
		t.Fatalf("read this repository's gate: %v", err)
	}
	byName := map[string]GateCommand{}
	for _, command := range declared {
		byName[command.Name] = command
	}

	// Say which map entries nobody declares yet, rather than failing on them:
	// the window between a protected change and the run applying it.
	for _, name := range sortedGateNames(gateTargets) {
		if _, ok := byName[name]; !ok {
			t.Logf("the %q check has its Makefile target (%s) and is not declared in .tick/runners.toml yet: "+
				"tick 2pn's cell is a protected change the run applies at the close-out, and "+
				"TestTheDeclaredHarnessGatePairsWithItsMakefileTwin holds the cell to that target until it does",
				name, gateTargets[name])
		}
	}

	for _, problem := range gateTwinProblems(filepath.Join(root, ".tick", "runners.toml"), filepath.Join(root, "Makefile"), gateTargets) {
		t.Error(problem)
	}
}

// The `harness` gate cell, spelled exactly as the change this tick's report
// carries for .tick/runners.toml — a protected file a worker may not commit, so
// the declaration is applied by the run itself at the close-out. Until that day
// this literal is the only copy of the cell in the tree, which is the point of
// pinning it: the Makefile twin cannot drift from the cell the run was told to
// apply without a test failing here first, and once the declaration lands,
// TestTheGateTargetMatchesTheDeclaredGate holds the real file to the same line.
//
// It is the `[testing.commands.harness]` spelling rather than the inline one —
// toml.go accepts both as the same document — because the change is APPENDED to
// the end of the file, and a key appended there lands in whatever table is open
// unless it opens one of its own.
const harnessGateCell = `
# The pi-durable harness package's own suites (tick 2pn, epic ex6). Until this
# cell the harness package that epic 43y created ran lint, typecheck and BOTH
# its vitest suites in CI (ci.yml's "harness conformance and replay") and in
# no gate at all, so a tick that changed harness/ merged on a gate that had
# said nothing about it: one of its node-half tests sat red at base and was
# found, and absorbed, five times by five workers (h3c, 7oy, 4ao, omq, 30e)
# because no gate ever told a run the suite was already red.
#
# The whole suite, not a fast half. Measured on one host (4 cores, load under 1):
# pnpm install 0.4s from a warm store (11.3s the first ever run, a store fill,
# not a gate cost), pnpm lint 1.5s, pnpm typecheck 15.3s (two tsc projects),
# pnpm test 56-65s (the workerd half 30-37s, the node half 26-28s). The whole
# command 1m24s warm and 1m29s cold against the 60m bound under one gate
# command and a Go half the same host measures at 1m45s — the same order as the
# cloudflare vitest the ts cell deliberately leaves to CI. The FAST HALF would
# not have been enough: the node half — real bash, real git, the real local
# door — is where the red-at-base test lived, so a gate that ran only the
# workerd half would have covered the package and missed it a sixth time.
#
# pnpm test is harness/package.json's own script, which is BOTH vitest
# configs, so CI's step and this check cannot drift; pnpm install comes first
# because a gate worktree carries no node_modules.
[testing.commands.harness]
command = "cd harness && pnpm install --frozen-lockfile --prefer-offline && pnpm lint && pnpm typecheck && pnpm test"
description = "Harness package: lint, types and both vitest suites"
`

// TestTheDeclaredHarnessGatePairsWithItsMakefileTwin proves the parity guard
// covers the `harness` half of the gate on the day the run applies the cell —
// and that the cell it applies is the Makefile's line, byte for byte.
//
// This is the half of tick 2pn a worker cannot commit: the declaration itself
// (see TestTheHarnessGateTargetRunsTheHarnessSuites for why). A guard that only
// learned about the `harness` command when it appeared in this tree would be a
// guard whose first run against the real cell is the run that gates the epic —
// and a parity failure found there is a closed tick reopened by the thing that
// closed it. So the cell is dry-run here against the real files, and the
// negative control beside it proves the comparison bites rather than accepts.
//
// The dry run is the real gate file with the cell appended — the change as the
// run applies it, not a hand-built stand-in — so it proves three things at once:
// the reader accepts the cell's spelling, the whole document is still a config
// the run's own reader can load (a cell that broke Parse would break the run at
// start, over routing, long before any gate ran), and all three declared
// commands, `go`, `ts` and `harness`, pair with their Makefile twins.
// short: the checkout's own gate file and Makefile with one synthetic copy, no subprocess — and it is what proves the gate's newest half is covered before it lands
func TestTheDeclaredHarnessGatePairsWithItsMakefileTwin(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	gateFile := filepath.Join(root, ".tick", "runners.toml")
	raw, err := os.ReadFile(gateFile)
	if err != nil {
		t.Skipf("no %s in this checkout", gateFile)
	}
	makefile := filepath.Join(root, "Makefile")

	patched := filepath.Join(t.TempDir(), "runners.toml")
	if err := os.WriteFile(patched, append(raw, []byte(harnessGateCell)...), 0o644); err != nil {
		t.Fatalf("write the gate with the harness cell appended: %v", err)
	}
	if problems := gateTwinProblems(patched, makefile, gateTargets); len(problems) != 0 {
		for _, problem := range problems {
			t.Errorf("the gate with the `harness` cell this tick declares does not pair with this tree's Makefile: %s", problem)
		}
	}
	if _, err := runconfig.Parse(raw); err != nil {
		t.Fatalf("this repository's gate file does not load as a config: %v", err)
	}
	if _, err := runconfig.Parse(append(raw, []byte(harnessGateCell)...)); err != nil {
		t.Errorf("the gate with the `harness` cell appended is not a config the run can load at start: %v", err)
	}

	// The negative control, because a comparison nobody has seen fail is a
	// comparison nobody has seen. Drop `pnpm test` — the package's own script,
	// and the only place the node half runs — and the pairing must refuse the
	// cell rather than accept it.
	drifted := strings.Replace(harnessGateCell, " && pnpm test", "", 1)
	if err := os.WriteFile(patched, append(raw, []byte(drifted)...), 0o644); err != nil {
		t.Fatalf("write the drifted declaration: %v", err)
	}
	problems := gateTwinProblems(patched, makefile, gateTargets)
	if len(problems) == 0 {
		t.Fatal("a `harness` cell that stops before `pnpm test` paired with the Makefile's twin: the parity check does " +
			"not bite on the harness half, and a harness gate that quietly covers half the package's suites is exactly " +
			"the fast half tick 2pn refuses")
	}
	for _, problem := range problems {
		if !strings.Contains(problem, `"harness"`) || !strings.Contains(problem, "drifted") {
			t.Errorf("the drifted `harness` cell was refused for the wrong reason: %s", problem)
		}
	}
}

// gateTwinProblems is the parity check itself, as a function over two paths and
// a target map rather than a test body, so a declaration that is not in this
// tree yet can be checked against this tree's Makefile: the `harness` cell is
// applied by the run at the close-out, and
// TestTheDeclaredHarnessGatePairsWithItsMakefileTwin proves the guard covers
// that half before the day it lands rather than discovering it after.
func gateTwinProblems(runnersToml, makefile string, targets map[string]string) []string {
	declared, err := ReadGateCommands(runnersToml)
	if err != nil {
		return []string{fmt.Sprintf("read the declared gate %s: %v", runnersToml, err)}
	}
	var problems []string
	for _, command := range declared {
		target, known := targets[command.Name]
		if !known {
			problems = append(problems, fmt.Sprintf("the declared gate %q has no Makefile target in this test's map: the "+
				"gate is the one runner that can block every tick, and a check only runners.toml knows about is one `make` "+
				"cannot run — add it to `gateTargets` with its target, or drop the declaration", command.Name))
			continue
		}
		recipe, ok, err := findMakeRecipe(makefile, target)
		if err != nil {
			problems = append(problems, fmt.Sprintf("read the Makefile's `%s` target: %v", target, err))
			continue
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("the Makefile has no `%s` target for declared gate %q: either restore "+
				"it, or drop the declaration", target, command.Name))
			continue
		}
		if got, want := recipe, command.Command; got != want {
			problems = append(problems, fmt.Sprintf("the Makefile's %s target and the declared gate %q have drifted:\n  Makefile:      %s\n  runners.toml:  %s\n"+
				"They are the same thing said twice.", target, command.Name, got, want))
		}
	}
	return problems
}

func sortedGateNames(targets map[string]string) []string {
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestTheHarnessGateTargetRunsTheHarnessSuites pins the pi-durable harness
// package's suites into this repository's Makefile — the half of tick 2pn that
// lives outside .tick/.
//
// The tick's whole point is that the harness package epic 43y created runs
// lint, typecheck and BOTH its vitest suites in CI (ci.yml's `harness
// conformance and replay`) and in no gate: the `ts` check is cloudflare's and
// the `go` check is Go, so a tick that changed harness/ merged on a gate that
// had said nothing about it. The cost of that silence was one harness test red
// at base, found by five different workers and absorbed five times (h3c, 7oy,
// 4ao, omq, 30e), because no gate ever told a run it was already red. The
// cloudflare half of the same gap is tick tc9's; this is the harness half.
//
// WHAT is here and what is NOT. The declared gate — the `[testing.commands]`
// entry the run's integrated gate reads — is the other half of the tick, and
// it lives in .tick/runners.toml, which a worker may not commit: the container's
// pre-commit hook and the cloud collect refuse any .tick/ path wholesale, where
// internal/exec/subprocess/report.go's own boundary exempts the runner table
// (tick 9sy). So the cell is carried as the tick's protected change and the run
// applies it at the close-out, and until that day this target is what a human
// and CI run, with TestTheDeclaredHarnessGatePairsWithItsMakefileTwin pinning
// the cell to it.
//
// The pieces, not the whole line, for the same reason the parity test reads a
// recipe: each piece is a way the target could quietly become weaker than the
// suite it is supposed to run. `pnpm test` matters most, because it is the
// package's OWN script and the node half is inside it — the half that runs real
// bash, real git and the real local door, and the half the red-at-base test
// lived in. A `test` script that ran only the workerd half would make
// `make harness-gate` cover the package and still miss dumb-git-origin, so the
// script is pinned to BOTH vitest configs here rather than trusted.
// short: three files of this checkout compared, no subprocess — and a suite the gate does not run is one a tick can silently break, so the guard belongs in every tick
func TestTheHarnessGateTargetRunsTheHarnessSuites(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	// The target is the gate command's twin: what a human runs where the run
	// runs the declared cell. A missing target is not a skip — this test is
	// the only thing that says the harness suites have an entry point at all.
	recipe, ok := makeRecipe(t, filepath.Join(root, "Makefile"), "harness-gate")
	if !ok {
		t.Fatal("the Makefile has no `harness-gate` target: the harness package's suites have no entry point a " +
			"human or CI can run, and the declared `harness` gate has no twin (tick 2pn)")
	}
	for _, piece := range []struct{ want, why string }{
		{"cd harness", "the target must run inside the harness package"},
		{"pnpm install --frozen-lockfile", "a fresh checkout or gate worktree carries no node_modules"},
		{"pnpm lint", "Biome is this side's gofmt and its vet"},
		{"pnpm typecheck", "a behavioural suite that cannot typecheck has failed for a reason the log should name"},
		{"pnpm test", "the package's own script — both vitest suites, not one"},
	} {
		if !strings.Contains(recipe, piece.want) {
			t.Errorf("the `harness-gate` target does not run %q (%s):\n  %s", piece.want, piece.why, recipe)
		}
	}

	// The `test` script is what makes `pnpm test` both halves, so it is pinned
	// rather than assumed: the workerd suite (vitest.config.ts) and the node
	// suite (vitest.node.config.ts) are two configs because the pool-workers
	// plugin owns every file it is handed and the node tests exec — and a gate
	// that reaches only one of them is the fast half tick 2pn refuses.
	raw, err := os.ReadFile(filepath.Join(root, "harness", "package.json"))
	if err != nil {
		t.Fatalf("read the harness package's own scripts: %v", err)
	}
	var pkg struct {
		Scripts struct {
			Test string `json:"test"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("harness/package.json is not the JSON a reader expects: %v", err)
	}
	for _, half := range []struct{ want, why string }{
		{"vitest run", "the workerd suite — storage conformance, transcript replay, the execution environments"},
		{"vitest.node.config.ts", "the node suite — real bash, real git, the real local door"},
	} {
		if !strings.Contains(pkg.Scripts.Test, half.want) {
			t.Errorf("harness/package.json's `test` script does not run %q (%s): %q — so `pnpm test`, and the "+
				"harness gate with it, covers only half the package's suites", half.want, half.why, pkg.Scripts.Test)
		}
	}
}

// makeRecipe returns a target's single-line recipe with $(VAR) expanded from
// the same Makefile's simple assignments. It is deliberately small: the point
// is to compare one command, not to implement make.
func makeRecipe(t *testing.T, path, target string) (string, bool) {
	t.Helper()

	recipe, ok, err := findMakeRecipe(path, target)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return recipe, ok
}

// findMakeRecipe is makeRecipe for a caller that reports its own problems
// rather than failing a test, so the parity check can run over a path the test
// does not own.
func findMakeRecipe(path, target string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(string(raw), "\n")

	vars := map[string]string{}
	assign := regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)\s*[:?]?=\s*(.*?)\s*$`)
	for _, line := range lines {
		if m := assign.FindStringSubmatch(line); m != nil {
			vars[m[1]] = m[2]
		}
	}

	for i, line := range lines {
		if !strings.HasPrefix(line, target+":") {
			continue
		}
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" || strings.HasPrefix(next, "#") {
				continue
			}
			if !strings.HasPrefix(next, "\t") {
				break
			}
			recipe := strings.TrimSpace(next)
			for name, value := range vars {
				recipe = strings.ReplaceAll(recipe, "$("+name+")", value)
			}
			return recipe, true, nil
		}
	}
	return "", false, nil
}
