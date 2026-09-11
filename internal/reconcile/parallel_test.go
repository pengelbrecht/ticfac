package reconcile

// The parallel discipline, enforced rather than remembered.
//
// This package is the whole cost of the gate: every one of its tests builds a
// real repository, a real bare origin and real worker processes, and before
// they ran in parallel the suite measured 604-1063s per invocation — paid by
// every worker and every gate, repeatedly. The isolation that parallelism
// needs is structural (each fixture owns a t.TempDir() root, its own repo and
// origin, its own executor state root; the only shared value, executorBin, is
// built once in TestMain and only read afterwards), so the discipline that
// actually has to be kept is the annotation itself.
//
// This test keeps it: every top-level test in this package either calls
// t.Parallel() directly or carries a doc-comment line beginning `serial:`
// saying why it cannot. A test added without either is caught here rather
// than discovered in a gate that has quietly gone serial again.
//
// Sequential subtests inside a parallel test are fine and are not policed:
// subtests that are phases of one story are SUPPOSED to run in order, and
// the fixture-heavy subtests that can run apart carry their own
// t.Parallel() where it is safe.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

func TestEveryTestRunsInParallelOrSaysWhyItCannot(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "internal", "reconcile")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			if fn.Recv != nil || fn.Body == nil {
				continue
			}
			if callsParallelDirectly(fn) {
				continue
			}
			if serialReason(fn) != "" {
				continue
			}
			t.Errorf("%s: %s neither calls t.Parallel() nor carries a `serial:` doc-comment line "+
				"naming why it cannot — the suite's wall-clock is what rots when this is forgotten",
				entry.Name(), fn.Name.Name)
		}
	}
}

// callsParallelDirectly reports whether t.Parallel() is a statement of the
// test's own body. A call buried inside a subtest closure does not count:
// it parallelises the subtest, not this test, and this package has tests
// whose subtests run in parallel while the test itself still has to signal.
func callsParallelDirectly(fn *ast.FuncDecl) bool {
	for _, stmt := range fn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "t" || sel.Sel.Name != "Parallel" {
			continue
		}
		return true
	}
	return false
}

// serialReason is the one-line reason a test that cannot run in parallel
// carries, as a doc-comment line beginning `serial:`. A test that cannot be
// parallel is a fact about the design worth writing down where the next
// reader of the suite will find it.
func serialReason(fn *ast.FuncDecl) string {
	if fn.Doc == nil {
		return ""
	}
	for _, comment := range fn.Doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		if strings.HasPrefix(text, "serial:") {
			return text
		}
	}
	return ""
}
