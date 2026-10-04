package reconcile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The release command a hold for a person names is addressed by the run whose
// store carries the attempt: settle without --run-id opens epic-<epic-id>,
// which is the right store only for a run that wrote its records under that
// spelling — a cloud run's records live under the factory's run_<hex>, so the
// command the prose and the alert spell must name the run itself (tick ulw
// fixed the model surface, tick qxj the reconciler's prose and the watch
// alert, and this test pins the one spelling all three read).
// short: string matching over a table; no repository, no run
func TestTheSettleReleaseCommandNamesTheRunItsStoreIsUnder(t *testing.T) {
	t.Parallel()
	const plain = `ticfac settle qeu a1 2 --release "<who>"`
	for _, tc := range []struct {
		name  string
		runID string
		want  string
	}{
		{"no run id spells the plain form every local run's prose has always carried", "", plain},
		{"the epic spelling is the default a settle without the flag opens, so the flag stays off", "epic-qeu", plain},
		{"a cloud run's store is the factory's run id, so the command names it", "run_5c7c16d199414da2a8a40d97b524e987",
			`ticfac settle qeu a1 2 --run-id run_5c7c16d199414da2a8a40d97b524e987 --release "<who>"`},
		{"any other run id is named too: the default cannot address its store", "r-fixture",
			`ticfac settle qeu a1 2 --run-id r-fixture --release "<who>"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SettleReleaseCommand("qeu", "a1", 2, tc.runID); got != tc.want {
				t.Errorf("SettleReleaseCommand with run id %q = %q, want %q", tc.runID, got, tc.want)
			}
			r := &Reconciler{opts: Options{EpicID: "qeu", RunID: tc.runID}}
			if got := r.settleCommand("a1", 2); got != tc.want {
				t.Errorf("the run's own release command with run id %q = %q, want %q", tc.runID, got, tc.want)
			}
		})
	}
}

// No prose in this package spells the settle command's own format any more:
// every site takes the command from SettleReleaseCommand, so a site that
// starts spelling it again — without the run's id, the defect tick qxj fixed
// — fails here rather than in front of a person holding a run. The scan reads
// string literals, not bytes: comments may name the command's shape (the
// narratives that do), and only the helper may spell the format.
// short: a source scan of this package; no repository, no run
func TestNoReconcileProseSpellsTheSettleCommand(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "internal", "reconcile")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the package's sources: %v", err)
	}
	const helper = "settle_command.go"
	spelled := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			if !strings.Contains(value, "settle %s") {
				return true
			}
			if name != helper {
				t.Errorf("%s spells the settle command itself at %s: take it from SettleReleaseCommand, so the "+
					"release command the prose names carries the run's id (tick qxj)",
					name, fset.Position(literal.Pos()))
			}
			spelled++
			return true
		})
	}
	if spelled != 2 {
		t.Errorf("the helper spells the settle command %d times, want exactly 2 (the plain and the run-id form): "+
			"the scan is reading the wrong thing if this is not one spelling in one place", spelled)
	}
}
