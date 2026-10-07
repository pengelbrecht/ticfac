package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

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

	// Each declared check has a Makefile target carrying the SAME line. The
	// mapping is spelled out rather than derived, so adding a check without a
	// target — or a target without a check — fails here instead of being
	// discovered when one of the two silently stops running, which is exactly
	// how cloud/factory's suite came to be unrun for two major bundle versions
	// (ticks odc and b9w).
	//
	// `harness` is deliberately NOT in this map yet. The `harness-gate` Makefile
	// target exists (tick 2pn's half that lives outside .tick/), but the
	// [testing.commands] declaration beside it does not: this attempt's
	// substrate refuses a worker commit under .tick/ wholesale
	// (tick 9sy), so the one line that would complete this map could not be
	// written from a container. Naming a check nobody declares fails this test,
	// so the map entry is the LAST step of that landing, never a thing to add
	// ahead of the declaration it names.
	targets := map[string]string{
		"go": "gate",
		"ts": "ts-gate",
	}
	if len(declared) != len(targets) {
		t.Fatalf("this repository declares %d gate command(s) and this test knows %d: add the new one to "+
			"`targets` with its Makefile target, or say why it has none", len(declared), len(targets))
	}

	for _, command := range declared {
		target, known := targets[command.Name]
		if !known {
			t.Errorf("the declared gate %q has no Makefile target in this test: the gate is the one runner that "+
				"can block every tick, and a check only this file knows about is one `make` cannot run", command.Name)
			continue
		}
		recipe, ok := makeRecipe(t, filepath.Join(root, "Makefile"), target)
		if !ok {
			t.Errorf("the Makefile has no `%s` target for declared gate %q: either restore it, or drop the "+
				"declaration", target, command.Name)
			continue
		}
		if got, want := recipe, command.Command; got != want {
			t.Errorf("the Makefile's %s target and the declared gate %q have drifted:\n  Makefile:      %s\n  runners.toml:  %s\n"+
				"They are the same thing said twice.", target, command.Name, got, want)
		}
	}
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
// it lives in .tick/runners.toml, which this attempt's substrate refuses to
// let a worker commit: image/worker.sh's pre-commit hook and
// cloudflare/src/worker-collect.ts refuse any .tick/ path wholesale, where
// internal/exec/subprocess/report.go's own boundary exempts the runner table
// (tick 9sy). So this target is the Makefile twin — the thing a human and CI
// run — and TestTheGateTargetMatchesTheDeclaredGate's `targets` map is where
// the mapping lands when the declaration does.
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
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
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
			return recipe, true
		}
	}
	return "", false
}
