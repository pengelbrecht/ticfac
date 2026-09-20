package shorttest_test

// The short-suite discipline, enforced rather than remembered.
//
// Modelled on internal/reconcile's TestEveryTestRunsInParallelOrSaysWhyItCannot,
// which keeps that package's `t.Parallel()` annotation from rotting. This keeps
// the same promise one level up: a test added to an end-to-end package next
// month is out of the per-tick gate by construction, not because its author
// remembered.
//
// Every top-level test in a declared package must do one of four things:
//
//   - build the package's end-to-end harness in its own body — the constructor
//     calls shorttest.EndToEnd for it, so the skip is free;
//   - call shorttest.EndToEnd(t) itself — which is what a test that builds the
//     harness only inside a subtest closure has to do, because a t.Skip inside
//     a closure skips the subtest and leaves the parent's assertions running
//     against nothing;
//   - carry a doc-comment line beginning `short:` saying why it is cheap
//     enough to pay for on every tick;
//   - call shorttest.LoadBearing(t) AND carry a `gate:` doc-comment line with
//     its measured cost — an end-to-end test that stays in the gate because
//     it is the only proof a critical path works.
//
// A test that does none of the four fails here. The default is therefore
// "runs on CI, not in the gate", and the only way past this guard is to say in
// one line which side the test is on and why.
//
// The fourth is the one worth watching, because it is the one that can walk
// the gate back to 23 minutes a reasonable-looking test at a time. So it is
// bounded rather than argued: the `gate:` costs are summed and must fit in
// shorttest.Budget, and the guard prints the spend either way. Admitting a new
// load-bearing test therefore means measuring it against what is left, or
// raising a number in a diff a reviewer can see.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
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

	// The gate's end-to-end spend, and what it was spent on.
	var spend time.Duration
	var admitted []string

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
				where := pkg + "/" + entry.Name() + ": " + fn.Name.Name

				// The load-bearing exception, checked from both ends. A claim
				// without a cost is an exception nobody priced; a cost without
				// a claim is a comment that does nothing, and the test it
				// decorates is silently skipped while its doc says otherwise —
				// which is worse than either alone.
				claims := callsOwnBody(fn, []string{"LoadBearing"})
				cost, stated := gateCost(fn)
				switch {
				case claims && !stated:
					t.Errorf("%s calls shorttest.LoadBearing(t) without a `gate:` doc-comment line: "+
						"a place in the per-tick gate is spent against shorttest.Budget, so it is taken "+
						"with a measured cost or not at all", where)
				case stated && !claims:
					t.Errorf("%s carries a `gate:` doc-comment line but never calls shorttest.LoadBearing(t): "+
						"the harness skips it under -short and the comment says it does not", where)
				case claims && stated:
					if shortReason(fn) != "" {
						t.Errorf("%s carries both `gate:` and `short:`: it is either an end-to-end test "+
							"the gate pays for or a cheap one, and this file cannot tell which", where)
					}
					spend += cost
					admitted = append(admitted, where+" ("+cost.String()+")")
					continue
				}

				if callsOwnBody(fn, append([]string{"EndToEnd"}, constructors...)) {
					continue
				}
				if shortReason(fn) != "" {
					continue
				}
				t.Errorf("%s neither builds the end-to-end harness in its own body, "+
					"nor calls shorttest.EndToEnd(t), nor carries a `short:` doc-comment line "+
					"saying why the per-tick gate should pay for it, nor claims a place in it with "+
					"shorttest.LoadBearing(t) and a `gate:` cost", where)
			}
		}
	}

	// The spend, printed whether or not it fits. A budget nobody sees the
	// balance of is a budget that is discovered only when it is already gone.
	sort.Strings(admitted)
	t.Logf("end-to-end tests admitted to the per-tick gate: %s of %s\n  %s",
		spend, shorttest.Budget, strings.Join(admitted, "\n  "))
	if spend > shorttest.Budget {
		t.Errorf("the admitted end-to-end tests declare %s against a budget of %s. Either drop one to "+
			"EndToEnd, or raise shorttest.Budget deliberately and say in its comment what the gate bought",
			spend, shorttest.Budget)
	}
}

// The negative control for the load-bearing exception. The two halves of the
// claim — the call and the priced `gate:` line — must be refused apart and
// accepted only together, because each half alone is a lie of a different
// shape: a call without a cost is an exception nobody priced, and a line
// without a call is a doc comment saying the gate runs a test the harness
// skips.
func TestTheDisciplineRefusesHalfAClaimOnTheGate(t *testing.T) {
	t.Parallel()

	const src = `package p

// gate: 2s — priced, and claimed below
func TestBoth(t *testing.T) { shorttest.LoadBearing(t); newFixture(t, fixtureOptions{}) }

func TestClaimWithNoCost(t *testing.T) { shorttest.LoadBearing(t); newFixture(t, fixtureOptions{}) }

// gate: 2s — priced, but nothing claims it
func TestCostWithNoClaim(t *testing.T) { newFixture(t, fixtureOptions{}) }

// gate: soon — a cost that is not a duration
func TestUnpriceable(t *testing.T) { shorttest.LoadBearing(t); newFixture(t, fixtureOptions{}) }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		claims bool
		cost   time.Duration
		stated bool
	}{
		"TestBoth":            {claims: true, cost: 2 * time.Second, stated: true},
		"TestClaimWithNoCost": {claims: true},
		"TestCostWithNoClaim": {cost: 2 * time.Second, stated: true},
		"TestUnpriceable":     {claims: true},
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		w := want[fn.Name.Name]
		claims := callsOwnBody(fn, []string{"LoadBearing"})
		cost, stated := gateCost(fn)
		if claims != w.claims || stated != w.stated || cost != w.cost {
			t.Errorf("%s: claims=%v cost=%v stated=%v; want claims=%v cost=%v stated=%v",
				fn.Name.Name, claims, cost, stated, w.claims, w.cost, w.stated)
		}
	}
}

// gateCost reads the measured cost off a `gate:` doc-comment line, whose shape
// is `gate: <duration> — <why the coverage is load-bearing>`. The duration is
// first because it is the part this file has to be able to add up; the reason
// is for the person deciding whether the next one fits.
func gateCost(fn *ast.FuncDecl) (time.Duration, bool) {
	if fn.Doc == nil {
		return 0, false
	}
	for _, comment := range fn.Doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		rest, ok := strings.CutPrefix(text, "gate:")
		if !ok {
			continue
		}
		field := strings.TrimSpace(rest)
		if cut := strings.IndexAny(field, " \t"); cut >= 0 {
			field = field[:cut]
		}
		d, err := time.ParseDuration(field)
		if err != nil {
			return 0, false
		}
		return d, true
	}
	return 0, false
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
