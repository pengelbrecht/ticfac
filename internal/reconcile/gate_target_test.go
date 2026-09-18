package reconcile

import (
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
