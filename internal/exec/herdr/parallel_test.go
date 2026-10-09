package herdr

// The parallel discipline, enforced rather than remembered.
//
// The same guard internal/reconcile carries, mirrored here because this
// package is one of the suite's slow five (tick x73): 143 top-level tests,
// each over a real git repository and origin, a real fake herdr on its own
// unix socket and a real agent process — measured 38.3s serial on this host
// before tick x73's parallel pass and 12.3s at -parallel 12 after it.
// -parallel does nothing for a package whose tests never call t.Parallel(),
// so the annotation is the whole mechanism and the one thing that can rot.
//
// The isolation the parallelism needs is structural — every test builds its
// own harness, and the harness builds its own repository and origin under
// its own t.TempDir() and serves its own herdtest socket; two tests share
// nothing but read-only values (the fake agent's script, the repository
// root). The discipline that actually has to be kept is the annotation
// itself.
//
// This test keeps it: every top-level test in this package either calls
// t.Parallel() directly or carries a doc-comment line beginning `serial:`
// saying why it cannot — and this package knows exactly one reason, the
// transcript home, which the stuck tests point at a temp dir through
// t.Setenv because the watch under test reads it from the process
// environment. A test added without either is caught here rather than
// discovered in a gate that has quietly gone serial again.
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
	dir := filepath.Join(root, "internal", "exec", "herdr")
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
