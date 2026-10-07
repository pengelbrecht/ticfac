package gatescope_test

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gatescope"
)

// The selection: which packages' full (non-short) suites a tick's gate must
// run, from the diff the gate hands the check to the module's package graph.
//
// The three pure halves — reading `go list`'s record, mapping a changed file
// to the package that owns it, and walking the reverse of the import graph —
// are tested here against fixed inputs, so a broken parse fails for the
// reason it broke rather than somewhere inside a real `go list` invocation.
// The whole pipeline against a real module on disk is the harness test in
// scope_harness_test.go.

// goListRecord is one `go list -f` line: the template gatescope runs spells
// the five fields with tabs, and the lists inside are space-separated module
// import paths (every other import is dropped by the parse).
const goListRecord = "example.com/probe/alpha\tinternal/reconcile/alpha\texample.com/probe/root beta\t\t\n" +
	"example.com/probe/beta\tinternal/reconcile/beta\troot\talpha\texample.com/probe/root\n" +
	"example.com/probe/root\t.\t\t\t\n"

// short: pure functions over fixed inputs
func TestLoadPackagesReadsTheGraphGoListPrints(t *testing.T) {
	t.Parallel()
	pkgs, err := gatescope.LoadPackages(func(name string, args ...string) (string, error) {
		if name != "go" || !strings.Contains(strings.Join(args, " "), "-f") {
			t.Fatalf("the graph is read with `go list -f`, not %s %v", name, args)
		}
		return goListRecord, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("%d packages, want 3: %+v", len(pkgs), pkgs)
	}
	byPath := map[string]gatescope.Package{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	if got := byPath["example.com/probe/alpha"].Dir; got != "internal/reconcile/alpha" {
		t.Errorf("alpha's dir is %q", got)
	}
	// beta's TEST imports alpha and its external test imports root: both are
	// edges the reverse closure has to walk, or a tick that changes a
	// package only a test suite depends on would pass the gate untested.
	if got := byPath["example.com/probe/beta"].TestImports; strings.Join(got, " ") != "alpha" {
		t.Errorf("beta's test imports are %v, want [alpha]", got)
	}
	if got := byPath["example.com/probe/beta"].XTestImports; strings.Join(got, " ") != "example.com/probe/root" {
		t.Errorf("beta's external test imports are %v, want [example.com/probe/root]", got)
	}
}

// short: pure functions over fixed inputs
func TestTouchPackagesMapsAFileToItsDeepestAncestorPackage(t *testing.T) {
	t.Parallel()
	pkgs := []gatescope.Package{
		{ImportPath: "example.com/probe", Dir: "."},
		{ImportPath: "example.com/probe/internal/reconcile", Dir: "internal/reconcile"},
	}
	for _, tc := range []struct {
		file string
		want string
	}{
		{"internal/reconcile/gate.go", "example.com/probe/internal/reconcile"},
		// testdata lives INSIDE the package directory but outside its Go
		// files: a changed fixture script is a change to the package whose
		// tests read it. (`go list` never lists a testdata directory itself;
		// the deepest LISTED package ancestor is the package above it.)
		{"internal/reconcile/testdata/fake-runner.sh", "example.com/probe/internal/reconcile"},
		// The root package owns only the Go files of the root directory: the
		// repository's own files (Makefile, README, go.mod) are not a change
		// to any package, or a docs-only tick would bill the whole import
		// closure of the root package for full suites it never touched.
		{"embed.go", "example.com/probe"},
		{"go.mod", ""},
		{"Makefile", ""},
		// A file under no package at all maps to nothing.
		{"docs/adr-001.md", ""},
	} {
		got := gatescope.TouchPackages([]string{tc.file}, pkgs)
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s mapped to %q, want %q", tc.file, strings.Join(got, " "), tc.want)
		}
	}
}

// short: pure functions over fixed inputs
func TestTouchPackagesTakesEveryPackageAChangeCanReach(t *testing.T) {
	t.Parallel()
	pkgs := []gatescope.Package{
		{ImportPath: "a", Dir: "a"},
		{ImportPath: "b", Dir: "b"},
	}
	got := gatescope.TouchPackages([]string{"a/a.go", "b/b.go"}, pkgs)
	if strings.Join(got, " ") != "a b" {
		t.Fatalf("two files in two packages selected %q, want %q", strings.Join(got, " "), "a b")
	}
}

// short: pure functions over fixed inputs
func TestReverseClosureWalksTestImportsToo(t *testing.T) {
	t.Parallel()
	pkgs := []gatescope.Package{
		{ImportPath: "root", Dir: "."},
		{ImportPath: "alpha", Dir: "alpha"},
		// beta imports nothing at build time but its TESTS import alpha: a
		// tick that breaks alpha must run beta's suite, or the regression
		// rides to CI on a package the gate decided was untouched.
		{ImportPath: "beta", Dir: "beta", TestImports: []string{"alpha"}},
		// gamma is reached only THROUGH beta's tests: the closure is
		// transitive, not one hop.
		{ImportPath: "gamma", Dir: "gamma", XTestImports: []string{"beta"}},
		{ImportPath: "delta", Dir: "delta", Imports: []string{"root"}},
	}
	got := gatescope.ReverseClosure([]string{"alpha"}, pkgs)
	if strings.Join(got, " ") != "alpha beta gamma" {
		t.Fatalf("alpha's closure is %q, want %q", strings.Join(got, " "), "alpha beta gamma")
	}
}

// short: pure functions over fixed inputs
func TestSelectRunsNothingWhenNoPackageOwnsTheChange(t *testing.T) {
	t.Parallel()
	shell := func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "git":
			return "docs/adr-001.md\x00work.txt\x00", nil
		case name == "go" && strings.Contains(joined, "-f"):
			return "example.com/probe/alpha\talpha\t\t\t\n", nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return "", nil
	}
	got, err := gatescope.Select(shell, "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a change under no Go package selected %v", got)
	}
}

// The per-package budget (tick r1f): the packages the gate declares too
// expensive to pay for per tick are taken out of the run and named in the
// output — never skipped silently — and a declaration that names no package
// of the module is refused, or a typo would run the suite it meant to leave.
//
// short: pure functions over fixed inputs
func TestTheBudgetTakesAPackageOutOfTheRunAndNamesIt(t *testing.T) {
	t.Parallel()
	shell := func(name string, args ...string) (string, error) {
		if name == "go" && strings.Contains(strings.Join(args, " "), "-f") {
			return "example.com/probe/alpha\talpha\t\t\t\n" +
				"example.com/probe/beta\tbeta\texample.com/probe/alpha\t\t\n" +
				"example.com/probe/hero\thero\texample.com/probe/beta\t\t\n", nil
		}
		return "alpha/alpha.go\x00", nil
	}
	getenv := func(name string) (string, bool) {
		switch name {
		case gatescope.EnvBase:
			return "base-sha", true
		case gatescope.EnvHead:
			return "head-sha", true
		}
		return "", false
	}

	out := &strings.Builder{}
	code := gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4",
		LeaveToCI: []string{"example.com/probe/beta"}}, getenv, shell, out)
	if code == 0 {
		t.Fatalf("a run that shells out to go test cannot pass in this fixture; it answered 0:\n%s", out)
	}
	if !strings.Contains(out.String(), "example.com/probe/beta is left to CI") {
		t.Errorf("the budgeted package was skipped without being named:\n%s", out)
	}
	if strings.Contains(out.String(), " example.com/probe/beta\n") ||
		strings.Contains(out.String(), " example.com/probe/beta\n\n") {
		t.Errorf("the budgeted package still ran:\n%s", out)
	}
	if !strings.Contains(out.String(), "example.com/probe/alpha") ||
		!strings.Contains(out.String(), "example.com/probe/hero") {
		t.Errorf("the run lost a package the budget did not name:\n%s", out)
	}

	// A name no package of the module carries is a typo, and the suite it
	// meant to leave would run: refused.
	typo := &strings.Builder{}
	code = gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4",
		LeaveToCI: []string{"example.com/probe/recncile"}}, getenv, shell, typo)
	if code == 0 {
		t.Fatalf("a typo in the budget list was accepted:\n%s", typo)
	}
	if !strings.Contains(typo.String(), "example.com/probe/recncile") {
		t.Errorf("the refusal does not name the typo:\n%s", typo)
	}

	// The whole selection budgeted away is a pass that says so, not a silent
	// green.
	all := &strings.Builder{}
	shellAll := func(name string, args ...string) (string, error) {
		if name == "go" && strings.Contains(strings.Join(args, " "), "-f") {
			return "example.com/probe/alpha\talpha\t\t\t\n", nil
		}
		return "alpha/alpha.go\x00", nil
	}
	if code := gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4",
		LeaveToCI: []string{"example.com/probe/alpha"}}, getenv, shellAll, all); code != 0 {
		t.Fatalf("a selection left wholly to CI refused the tick:\n%s", all)
	}
	if !strings.Contains(all.String(), "every package the diff") {
		t.Errorf("a wholly-budgeted selection ran without saying so:\n%s", all)
	}
}

// short: pure functions over fixed inputs
func TestPairReadsTheDiffTheGateExportsAndRefusesHalfOfOne(t *testing.T) {
	t.Parallel()
	getenv := func(name string) (string, bool) {
		switch name {
		case gatescope.EnvBase:
			return "base-sha", true
		case gatescope.EnvHead:
			return "head-sha", true
		}
		return "", false
	}
	base, head, err := gatescope.Pair(getenv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if base != "base-sha" || head != "head-sha" {
		t.Fatalf("the gate's pair read back as %s...%s", base, head)
	}

	// Half a pair is a contract break, not an empty diff: the gate exports
	// both names or neither, so one of two is a caller to refuse loudly.
	half := func(name string) (string, bool) {
		if name == gatescope.EnvBase {
			return "base-sha", true
		}
		return "", false
	}
	if _, _, err := gatescope.Pair(half, nil); err == nil {
		t.Error("half an exported pair was accepted")
	}

	// An exported EMPTY pair is the gate's own "nothing to diff": a check
	// that runs nothing and says so, never one that guesses a default.
	emptyBoth := func(name string) (string, bool) {
		switch name {
		case gatescope.EnvBase, gatescope.EnvHead:
			return "", true
		}
		return "", false
	}
	if base, head, err := gatescope.Pair(emptyBoth, nil); err != nil || base != "" || head != "" {
		t.Fatalf("an empty exported pair read back as %s...%s (%v), want nothing to run", base, head, err)
	}
}

// short: pure functions over fixed inputs
func TestPairDefaultsToThePersonsBranchWhenNoGateExported(t *testing.T) {
	t.Parallel()
	var ran []string
	shell := func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return "the-merge-base\n", nil
	}
	base, head, err := gatescope.Pair(func(string) (string, bool) { return "", false }, shell)
	if err != nil {
		t.Fatal(err)
	}
	if base != "the-merge-base" || head != "HEAD" {
		t.Fatalf("a person's bare invocation diffed %s...%s, want the merge-base with origin/main...HEAD",
			base, head)
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "merge-base HEAD origin/main") {
		t.Fatalf("the default diff is against origin/main, computed with %v", ran)
	}
}
