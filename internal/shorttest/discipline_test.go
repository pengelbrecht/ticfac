package shorttest_test

// The short-suite discipline, enforced rather than remembered.
//
// Modelled on internal/reconcile's TestEveryTestRunsInParallelOrSaysWhyItCannot,
// which keeps that package's `t.Parallel()` annotation from rotting. This keeps
// the same promise one level up: a test added to an end-to-end package next
// month is out of the per-tick gate by construction, not because its author
// remembered.
//
// Every top-level test in a declared package must do one of three things:
//
//   - build the package's end-to-end harness in its own body — the constructor
//     calls shorttest.EndToEnd for it, so the skip is free;
//   - call shorttest.EndToEnd(t) itself — which is what a test that builds the
//     harness only inside a subtest closure has to do, because a t.Skip inside
//     a closure skips the subtest and leaves the parent's assertions running
//     against nothing;
//   - carry a doc-comment line beginning `short:` saying why it is cheap
//     enough to pay for on every tick.
//
// A test that does none of the three fails here. The default is therefore
// "runs on CI, not in the gate", and the only way past this guard is to say in
// one line why the gate should carry it.

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

// endToEndPackages are the packages whose tests build real git repositories
// and spawn real worker processes, with the constructors that do it. These
// three are the whole of the gate's old cost: measured on a quiet host they
// ran 180s, 176s and 113s while every other package in the repository came to
// ~270s between them.
//
// A new package that grows a harness of its own belongs in this table. Nothing
// can force that entry to be added — but a package outside the table cannot
// quietly lose coverage either, because everything in it still runs.
var endToEndPackages = map[string][]string{
	filepath.Join("internal", "reconcile"):     {"newFixture", "newRepo", "cloneRepo"},
	filepath.Join("internal", "exec", "herdr"): {"newHarness", "newRepo"},
	filepath.Join("internal", "runstate"):      {"newOrigin"},
}

func TestEveryEndToEndTestSkipsItselfOrSaysWhyItIsShort(t *testing.T) {
	t.Parallel()

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	for pkg, constructors := range endToEndPackages {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
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
				if !ok || fn.Recv != nil || fn.Body == nil {
					continue
				}
				if !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
					continue
				}
				if callsOwnBody(fn, append([]string{"EndToEnd"}, constructors...)) {
					continue
				}
				if shortReason(fn) != "" {
					continue
				}
				t.Errorf("%s/%s: %s neither builds the end-to-end harness in its own body, "+
					"nor calls shorttest.EndToEnd(t), nor carries a `short:` doc-comment line "+
					"saying why the per-tick gate should pay for it",
					pkg, entry.Name(), fn.Name.Name)
			}
		}
	}
}

// The negative control. A guard nothing has ever seen refuse is not known to
// be a guard, so the classifier is run over source written to be refused — and
// over each of the three things that are supposed to satisfy it, including the
// one that is deliberately NOT enough: a harness built inside a subtest
// closure, which skips the subtest and leaves the parent measuring nothing.
func TestTheDisciplineRefusesATestThatSaysNothing(t *testing.T) {
	t.Parallel()

	const src = `package p

// TestSilent says nothing at all.
func TestSilent(t *testing.T) { doSomethingExpensive() }

func TestHarness(t *testing.T) { f := newFixture(t, fixtureOptions{}); _ = f }

func TestExplicit(t *testing.T) { shorttest.EndToEnd(t); more() }

// short: one digest
func TestAnnotated(t *testing.T) { digest() }

func TestOnlyInsideAClosure(t *testing.T) {
	t.Run("a", func(t *testing.T) { newFixture(t, fixtureOptions{}) })
	assertSomethingTheSubtestWasSupposedToDo()
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"TestSilent":             false,
		"TestHarness":            true,
		"TestExplicit":           true,
		"TestAnnotated":          true,
		"TestOnlyInsideAClosure": false,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		accepted := callsOwnBody(fn, []string{"EndToEnd", "newFixture"}) || shortReason(fn) != ""
		if accepted != want[fn.Name.Name] {
			t.Errorf("%s: the guard %s it; want %s",
				fn.Name.Name,
				map[bool]string{true: "accepts", false: "refuses"}[accepted],
				map[bool]string{true: "accepted", false: "refused"}[want[fn.Name.Name]])
		}
	}
}

// callsOwnBody reports whether the test's own body calls one of the named
// functions. Calls inside a nested function literal do not count: a
// constructor built inside a subtest closure skips the SUBTEST, and the
// parent's own assertions then run against whatever the skipped subtests did
// not do — which is exactly how a suite goes green while measuring nothing.
func callsOwnBody(fn *ast.FuncDecl, names []string) bool {
	found := false
	var visit func(node ast.Node) bool
	visit = func(node ast.Node) bool {
		if found {
			return false
		}
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		for _, want := range names {
			if name == want {
				found = true
				return false
			}
		}
		return true
	}
	for _, stmt := range fn.Body.List {
		ast.Inspect(stmt, visit)
	}
	return found
}

// shortReason is the one-line reason a test gives for running in the per-tick
// gate, as a doc-comment line beginning `short:`. It is the same shape as the
// `serial:` line internal/reconcile's parallel guard asks for, and it is there
// for the same reason: the fact is worth writing down where the next reader of
// the suite will find it.
func shortReason(fn *ast.FuncDecl) string {
	if fn.Doc == nil {
		return ""
	}
	for _, comment := range fn.Doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		if strings.HasPrefix(text, "short:") {
			return text
		}
	}
	return ""
}
