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
	if len(declared) != 1 {
		t.Fatalf("this repository declares %d gate commands; this test assumes the single Go gate", len(declared))
	}

	recipe, ok := makeRecipe(t, filepath.Join(root, "Makefile"), "gate")
	if !ok {
		t.Fatal("the Makefile has no `gate` target: either restore it, or delete this test with the reason")
	}
	if got, want := recipe, declared[0].Command; got != want {
		t.Errorf("the Makefile's gate target and the declared gate have drifted:\n  Makefile:      %s\n  runners.toml:  %s\n"+
			"They are the same thing said twice; the gate is the one runner that can block every tick.", got, want)
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
