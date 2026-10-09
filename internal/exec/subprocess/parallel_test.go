package subprocess

// The parallel discipline, enforced rather than remembered.
//
// The same guard internal/reconcile carries, mirrored here because this
// package is one of the suite's slow five (tick x73): its 222 top-level
// tests are the per-tick gate's single heaviest package (measured 77.8s
// under -short and 90.0s in the full suite, serial, on this host before
// tick x73's parallel pass — 32.9s short and 32.3s full after it — and
// -parallel 12 does nothing for a package whose tests never call
// t.Parallel()), and the isolation the parallelism needs is
// structural: every fixture builds its own repository and origin under its
// own t.TempDir(), its own state root and its own runner processes, and the
// one value two tests share — the executor binary — is built once in
// TestMain and only read afterwards. The discipline that actually has to be
// kept is the annotation itself.
//
// This test keeps it: every top-level test in this package either calls
// t.Parallel() directly or carries a doc-comment line beginning `serial:`
// saying why it cannot. This package knows three reasons, and each is a
// fact about the seam the test drives rather than about parallelism:
//
//   - t.Setenv — the environment is process-wide, and Go refuses a test
//     that has called it from running in parallel (the transcript home, the
//     harness dir and the steer socket dir all arrive that way);
//   - the package-level test seams — groupSignal, statusBetweenReads,
//     observeBeforeLiveness — whose replacement is visible to every other
//     test in this process, not just to the fixture that swapped them;
//   - nothing else. The runnerStarted seam is NOT one: the tests that use it
//     swap it inside a helper MODE of this binary running as its own
//     process, which is exactly why those tests can be parallel.
//
// A test added without either the call or the reason is caught here rather
// than discovered in a gate that has quietly gone serial again.
//
// Sequential subtests inside a parallel test are fine and are not policed:
// subtests that are phases of one story are SUPPOSED to run in order.

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

// short: an AST scan of this package's own test files
func TestEveryTestRunsInParallelOrSaysWhyItCannot(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "internal", "exec", "subprocess")
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
// it parallelises the subtest, not this test.
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
