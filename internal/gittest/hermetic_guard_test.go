package gittest

// The reach of the hermetic helper, enforced rather than remembered.
//
// internal/reconcile and internal/runstate each hold a guard like this one
// over their OWN package (tick qsn, tick 35l), and both were filed over the
// same incident: a fixture reached past the pinned builder to exec.Command,
// and the leak was found only when CI failed the NEXT test. This is the same
// guard one level up — every test file in the repository, not one package —
// because the class was never one package's: twelve findings across three
// epics were all "a fixture measured its host instead of the tree", and a
// per-package guard only ever sees the instance that lands inside it (tick
// pqs).
//
// Four rules, one per half of the class:
//
//   - START: a test file starts git by exec.Command or exec.CommandContext,
//     anywhere except the package this helper is built on. Every other git a
//     test starts goes through this package's doors, so the environment it
//     runs in is stated in one place instead of at ninety call sites.
//
//   - HAND-OFF: a test passes the literal "git" to a runner that is not one
//     of the sanctioned generic runners. A runner that takes its program
//     name from its caller is a git start the START rule cannot see, so the
//     runners that may receive one are named here — each routing the git
//     case through this package — and adding one is a visible diff to this
//     table, not a silent second way to start git.
//
//   - WORKING DIRECTORY: a git is started with neither a directory of its own
//     (a `.Dir =` assignment) nor a `-C` argument, on a subcommand that
//     reads the repository from the working directory — in a linked
//     worktree, the checkout the gate happens to run in. gittest refuses
//     this at runtime; this rule holds the same line for the starts that
//     remain outside its doors.
//
//   - LIVE TOOLS: a test reaches wrangler, gh, or the factory's live deploy
//     path without an opt-in. TestFactoryDeployAndSetupAreWired once ran
//     the REAL factory deploy — the host PATH, the operator's Cloudflare
//     account, a live deploy from a gate run (fixed in #52 by faking the
//     tools the deploy shells out to). The opt-in for wrangler and gh is
//     this file's liveTools table, entered with a reason; the factory's
//     live entry points must go through the harness constructors that fake
//     those tools, in the test's own body.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// gitLiteral is the one spelling of the program's name in this file. It is a
// constant and not an argument because the HAND-OFF rule reads arguments,
// and this file is scanned by its own rule like any other.
const gitLiteral = "git"

// exemptPackages are the test files the START rule does not reach, each with
// the reason it cannot. Adding one is a visible diff a reviewer checks.
var exemptPackages = map[string]string{
	// The helper is BUILT on this package: WithoutPinnedConfig,
	// WithNoAutoMaintenance and the transport bounds are its own subject, and
	// the package's tests construct environments as their fixture — the
	// controls that prove the helper's entries do what they say.
	"internal/gitbin": "the environment builders' own package; its fixtures ARE the environments",
}

// sanctionedRunners are the generic runners ("package dir" -> callee names)
// that may receive the literal "git" from a caller: each one routes the git
// case through this package, so the hand-off inherits hermeticity rather than
// bypassing it. A runner not in this table that is handed "git" fails the
// HAND-OFF rule — which is how a copy-pasted mustRun from another package gets
// caught before it becomes a second, unpinned way to start git.
var sanctionedRunners = map[string]map[string]bool{
	"internal/reconcile":       {"mustRun": true, "mustRunAllowingFailure": true, "harnessCommand": true},
	"internal/exec/herdr":      {"mustRun": true},
	"internal/exec/subprocess": {"mustRun": true},
	"internal/cli":             {"execTestCmd": true, "execTestOutput": true},
	"internal/sandbox":         {"runOK": true},
}

// universalCallees may receive the literal "git" in any test file: the
// exec.Command family (whose git-literal name argument is the START rule's,
// not a hand-off), a presence check (LookPath starts nothing), and the path
// builders (a fixture writing a fake "git" onto a PATH of its own constructs
// a condition; it does not measure one).
var universalCallees = map[string]bool{
	"exec.Command":        true,
	"exec.CommandContext": true,
	"exec.LookPath":       true,
	"filepath.Join":       true,
	"filepath.Base":       true,
}

// liveTools names the test functions that may reach wrangler or gh, each
// entered with the reason the reach is deliberate. It is EMPTY: no test in
// this repository may start either tool, and the one test that shells to a
// live binary at all (the claude CLI smoke test, to DOCKER) opts in at
// runtime with an environment variable of its own. A new entry is a
// decision, made in a diff a reviewer can see.
var liveTools = map[string]string{}

// factoryHarnessConstructors are the constructors that fake the tools a real
// deploy shells out to (wrangler, the container engine, the package manager,
// the GitHub and Gateway endpoints), and the wrappers that build one and
// hand it back. A factory test that calls one of factoryLiveEntrypoints must
// build one of these in its own body — the test's own, or the same closure
// the entry point is called from — because that is where t.Setenv is legal
// and where every subtest inherits the fake PATH.
var factoryHarnessConstructors = []string{"newHarness", "newSetupHarness", "newDeviceHarness"}

// factoryLiveEntrypoints are the entry points that shell out to the live
// operator tools. Anything else a test can call is either pure or already
// pointed at a fake by the options the harness builds.
var factoryLiveEntrypoints = []string{"Deploy", "Setup"}

// TestEveryGitATestStartsGoesThroughTheHermeticHelper holds the START, the
// HAND-OFF and the WORKING DIRECTORY rules over every tracked test file.
//
// short: an AST scan of the tree's tracked test files; no git, no processes
func TestEveryGitATestStartsGoesThroughTheHermeticHelper(t *testing.T) {
	t.Parallel()
	offenders := scanTestFiles(t, scanGitStarts)
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Errorf("these tests start git the hermetic helper does not build:\n  %s\n"+
			"Route the git through internal/gittest — Command or Run for a fixture, Control for a\n"+
			"maintenance control, Under when the environment is the fixture's own subject — or,\n"+
			"for a generic runner that must also run other programs, register it in this file's\n"+
			"sanctionedRunners with the git case routed through the helper. A git started any\n"+
			"other way inherits the host's config, its maintenance, or its working directory,\n"+
			"and fails the gate only on the machine that has the wrong one (tick pqs).",
			strings.Join(offenders, "\n  "))
	}
}

// TestNoTestReachesWranglerGhOrTheFactorysLivePathUnoptedIn holds the LIVE
// TOOLS rule: the same scan, the other half of the class — a fixture that
// measures a live system rather than the tree.
//
// short: an AST scan of the tree's tracked test files; no git, no processes
func TestNoTestReachesWranglerGhOrTheFactorysLivePathUnoptedIn(t *testing.T) {
	t.Parallel()
	offenders := scanTestFiles(t, scanLiveTools)
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Errorf("these tests reach a live operator tool without an opt-in:\n  %s\n"+
			"wrangler and gh act on the operator's real accounts, so a test that runs them\n"+
			"is a test that can spend money and touch live systems from a gate. Fake the tool\n"+
			"(internal/factory's harness constructors do exactly that) or register the test\n"+
			"in this file's liveTools table with the reason the reach is deliberate. A deploy\n"+
			"or setup must go through newHarness or newSetupHarness in the test's own body:\n"+
			"that is what fakes the tools the deploy shells out to (#52).",
			strings.Join(offenders, "\n  "))
	}
}

// The negative controls. A guard nothing has ever seen refuse is not known to
// be a guard, so each rule is run over source written to be refused, and over
// the shapes that are supposed to satisfy it — including the ones that are
// deliberately NOT enough: a git handed to an unregistered runner, a git
// run under a different package's table, and a deploy whose harness is built
// inside a subtest closure instead of the test's own body.
//
// short: parses synthetic sources in memory; no git, no processes
func TestTheGuardsRefuseWhatTheyMustRefuse(t *testing.T) {
	t.Parallel()

	// The START rule: a direct start is refused — the literal "git" and
	// gitbin.Path() alike — and the same starts in the package the helper is
	// built on are that package's own subject. A start through the helper is
	// not a start at all.
	const starts = `package reconcile

import ("os/exec"; "github.com/pengelbrecht/ticfac/internal/gitbin"; "github.com/pengelbrecht/ticfac/internal/gittest")

func direct() { exec.Command("git", "status").Run() }

func resolved() { exec.Command(gitbin.Path(), "status").Run() }

func throughTheHelper(dir string) { gittest.Command(dir, "status").Run() }
`
	if got := scanTestFile("internal/reconcile/synthetic_test.go", starts, scanGitStarts); countTagged(got, tagStart) != 2 {
		t.Errorf("the START rule saw %d direct gits, want 2 (the literal and gitbin.Path()): %q",
			countTagged(got, tagStart), got)
	}
	if got := scanTestFile("internal/gitbin/synthetic_test.go", starts, scanGitStarts); len(got) != 0 {
		t.Errorf("the START rule refused the environment builders' own package: %q", got)
	}

	// The HAND-OFF rule: "git" handed to a runner not in the table is
	// refused; the table's own runners and the universal callees pass; and a
	// runner whose program argument is a variable is not a hand-off at all —
	// the start, if there is one, is the START rule's to find.
	const handoffs = `package cli

import ("os/exec"; "path/filepath")

func unregistered(t *testing.T, dir string, args ...string) { helper(t, dir, "git", "add") }

func sanctioned(t *testing.T, dir string, args ...string) { execTestCmd(t, dir, "git", "add", "-A") }

func presence() { exec.LookPath("git") }

func fakeOnAPath(bin string) { exec.Command(filepath.Join(bin, "git"), "status").Run() }

func helper(t *testing.T, dir string, name string, args ...string) { exec.Command(name, args...).Run() }
`
	if got := scanTestFile("internal/cli/synthetic_test.go", handoffs, scanGitStarts); len(got) != 1 {
		t.Errorf("the HAND-OFF rule found %d offenders under cli, want 1 (helper is not execTestCmd): %q",
			len(got), got)
	}
	if got := scanTestFile("internal/reconcile/synthetic_test.go", handoffs, scanGitStarts); len(got) != 2 {
		t.Errorf("the HAND-OFF rule found %d offenders under reconcile, want 2 (helper and execTestCmd "+
			"are not sanctioned there): %q", len(got), got)
	}
	// And under a package whose table sanctions only mustRun — the table
	// sanctions callees, never files.
	if got := scanTestFile("internal/exec/herdr/synthetic_test.go", handoffs, scanGitStarts); len(got) != 2 {
		t.Errorf("the HAND-OFF rule found %d offenders in herdr, want 2 (helper and execTestCmd: "+
			"herdr sanctions only mustRun): %q", len(got), got)
	}

	// The WORKING DIRECTORY rule: a git with neither a directory of its own
	// nor a -C argument resolves its repository from the caller's cwd, which
	// in a linked worktree is the checkout under test. A stated directory
	// and a -C argument are both enough; init and clone are not reads of the
	// caller's repository at all.
	const dirs = `package runstate

import "os/exec"

func inherited() { exec.Command("git", "fetch", "origin").Run() }

func stated(dir string) {
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	_ = cmd
}

func withC(repo string) { exec.Command("git", "-C", repo, "fetch", "origin").Run() }

func creates(repo string) { exec.Command("git", "init", repo).Run() }
`
	if got := scanTestFile("internal/runstate/synthetic_test.go", dirs, scanGitStarts); countTagged(got, tagCwd) != 1 {
		t.Errorf("the WORKING DIRECTORY rule found %d offenders, want 1 (the inherited one): %q",
			countTagged(got, tagCwd), got)
	}
	// The START rule still sees all four starts in that source.
	if got := scanTestFile("internal/runstate/synthetic_test.go", dirs, scanGitStarts); countTagged(got, tagStart) != 4 {
		t.Errorf("the START rule found %d starts in the cwd source, want 4: %q", countTagged(got, tagStart), got)
	}

	// The LIVE TOOLS rule: wrangler and gh are refused by name — through
	// exec.Command, exec.CommandContext and exec.LookPath alike — and the
	// factory's live entry points are refused without a harness constructor
	// in the test's own body, including when the only harness is inside a
	// subtest closure.
	const live = `package factory

import "os/exec"

func TestADeployThroughTheHarness(t *testing.T) { h := newHarness(t); _ = h; Deploy(nil, h.options()) }

func TestADeployWithNoHarness(t *testing.T) { Deploy(nil, nil) }

func TestADeployWhoseHarnessIsOnlyInsideAClosure(t *testing.T) {
	Deploy(nil, nil)
	t.Run("a", func(t *testing.T) { _ = newHarness(t) })
}

func TestWranglerDirect(t *testing.T) { exec.Command("wrangler", "deploy").Run() }

func TestGhByContext(t *testing.T) { exec.CommandContext(nil, "gh", "pr", "list").Run() }

func TestGhPresence(t *testing.T) { exec.LookPath("gh") }
`
	wantLive := []string{
		"TestADeployWithNoHarness",
		"TestADeployWhoseHarnessIsOnlyInsideAClosure",
		"TestWranglerDirect",
		"TestGhByContext",
		"TestGhPresence",
	}
	got := scanTestFile("internal/factory/synthetic_test.go", live, scanLiveTools)
	if len(got) != len(wantLive) {
		t.Fatalf("the LIVE TOOLS rule found %d offenders, want %d: %q", len(got), len(wantLive), got)
	}
	for i, complaint := range got {
		if !strings.Contains(complaint, wantLive[i]) {
			t.Errorf("offender %d is %q, which does not name %q", i, complaint, wantLive[i])
		}
	}
	// And the same deploy source outside internal/factory says nothing
	// about the factory's own rule: the LIVE TOOLS half that is scoped is
	// scoped to the package whose entry points they are.
	if got := scanTestFile("internal/cli/synthetic_test.go", live, scanLiveTools); len(got) != 3 {
		t.Errorf("the LIVE TOOLS rule found %d offenders in cli, want 3 (wrangler and gh only, no deploy rule): %q",
			len(got), got)
	}
}

// ------------------------------------------------------------- the scan ---

// The complaint tags, one per rule, so a reader of a failure knows which
// rule is speaking and the negative controls can count them.
const (
	tagStart   = "[start]"
	tagHandoff = "[hand-off]"
	tagCwd     = "[cwd]"
	tagLive    = "[live]"
)

// scanTestFiles enumerates the tree's TRACKED test files — Tracked, not a
// directory walk, for the same reason Tracked exists: an agent worktree or a
// scratch clone under the checkout is no party to this tree, and a guard
// that reads it fails the gate on a file nobody here wrote (the 2026-09-24
// parity incident) — and runs one classifier over each.
func scanTestFiles(t *testing.T, classify scanFunc) []string {
	t.Helper()
	root := moduleRoot(t)
	var complaints []string
	for _, path := range Tracked(t, root) {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		complaints = append(complaints, scanTestFile(filepath.ToSlash(rel), string(src), classify)...)
	}
	return complaints
}

// scanFunc is one rule over one test file, answering the complaints it has.
type scanFunc func(path, src string) []string

// scanTestFile runs one classifier over source text, synthetic or real.
func scanTestFile(path, src string, classify scanFunc) []string {
	return classify(path, src)
}

// scanGitStarts is the START, HAND-OFF and WORKING DIRECTORY rules over one
// test file.
func scanGitStarts(path, src string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return []string{path + ": does not parse: " + err.Error()}
	}
	pkg := filepath.ToSlash(filepath.Dir(path))
	if _, ok := exemptPackages[pkg]; ok {
		return nil
	}
	runners := sanctionedRunners[pkg]

	var complaints []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		where := func(node ast.Node) string {
			return path + ":" + strconv.Itoa(fset.Position(node.Pos()).Line) + " in " + fn.Name.Name
		}
		states := dirAssigned(fn)
		receivers := assignedTo(fn)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// The HAND-OFF rule: the literal "git" handed to a callee that
			// is not one of this package's sanctioned runners.
			if callee := calleeName(call); callee != "" && !universalCallees[callee] && !(runners != nil && runners[callee]) {
				for _, arg := range call.Args {
					if literalIs(arg, gitLiteral) {
						complaints = append(complaints, tagHandoff+" "+where(call)+
							" hands \"git\" to "+callee+", which is not one of the sanctioned runners: a runner that"+
							" takes its program name from its caller is a git start the START rule cannot see")
						break
					}
				}
			}
			// The START rule, and the WORKING DIRECTORY rule under it.
			name, ok := execCommandName(call)
			if !ok || !namesGit(name) {
				return true
			}
			complaints = append(complaints, tagStart+" "+where(call)+
				" starts git directly: route it through internal/gittest (tick pqs)")
			if statedADirectory(call, receivers, states) {
				return true
			}
			complaints = append(complaints, tagCwd+" "+where(call)+
				" starts git with neither a directory of its own nor a -C argument, so it resolves its"+
				" repository from the caller's working directory — in a linked worktree, the checkout the"+
				" gate runs in")
			return true
		})
	}
	return complaints
}

// scanLiveTools is the LIVE TOOLS rule over one test file: wrangler and gh by
// name, and the factory's live entry points without a harness constructor in
// the test's own body.
func scanLiveTools(path, src string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return []string{path + ": does not parse: " + err.Error()}
	}
	pkg := filepath.ToSlash(filepath.Dir(path))

	var complaints []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		where := func(node ast.Node) string {
			return path + ":" + strconv.Itoa(fset.Position(node.Pos()).Line) + " in " + fn.Name.Name
		}
		optedIn := strings.HasPrefix(fn.Name.Name, "Test") && liveTools[fn.Name.Name] != ""
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !optedIn {
				// A live tool by name, through either starter or a presence
				// check: reaching it decides the test on the host.
				for _, tool := range []string{"wrangler", "gh"} {
					if name, ok := execCommandName(call); ok && literalIs(name, tool) {
						complaints = append(complaints, tagLive+" "+where(call)+
							" runs "+tool+", a live operator tool: fake it, or register the test in the guard's"+
							" liveTools table with the reason (tick pqs)")
						return true
					}
					if calleeName(call) == "exec.LookPath" && len(call.Args) > 0 && literalIs(call.Args[0], tool) {
						complaints = append(complaints, tagLive+" "+where(call)+
							" looks for "+tool+": a presence check decides the test on what the host has installed,"+
							" and the reach it gates is a live one")
						return true
					}
				}
			}
			return true
		})
		if !optedIn && pkg == "internal/factory" && strings.HasPrefix(fn.Name.Name, "Test") {
			if scopesCallingLive(fn) != nil && !harnessBuiltFor(fn, scopesCallingLive(fn)) {
				complaints = append(complaints, tagLive+" "+where(fn)+
					" calls the factory's live deploy path without building the harness that fakes the tools it"+
					" shells out to: without a harness constructor in the same body, a deploy reaches the host's"+
					" wrangler, its container engine and its accounts (#52, tick pqs)")
			}
		}
	}
	return complaints
}

// execCommandName is the NAME argument of an exec.Command or
// exec.CommandContext call — the second one for CommandContext, whose first
// is the context. It answers false for everything else, including
// exec.LookPath, which starts nothing.
func execCommandName(call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return nil, false
	}
	index := 0
	switch sel.Sel.Name {
	case "Command":
	case "CommandContext":
		index = 1
	default:
		return nil, false
	}
	if len(call.Args) <= index {
		return nil, false
	}
	return call.Args[index], true
}

// namesGit is whether an exec.Command's name argument is a git: the literal
// "git" (or a path ending in it), or gitbin.Path() — the same reading as the
// reconcile and runstate guards this one generalises.
func namesGit(arg ast.Expr) bool {
	switch e := arg.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(e.Value)
		return err == nil && filepath.Base(value) == "git"
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "gitbin" && sel.Sel.Name == "Path"
	}
	return false
}

// literalIs is whether an expression is the exact string literal want.
func literalIs(expr ast.Expr, want string) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	return err == nil && value == want
}

// calleeName is a call's callee as a reader can sanction it: the identifier,
// or package.function for a selector.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok {
			return pkg.Name + "." + fun.Sel.Name
		}
	}
	return ""
}

// dirAssigned answers the identifiers a `.Dir =` is assigned to anywhere in
// fn, so the WORKING DIRECTORY rule can ask whether a command's directory
// was stated in the function that starts it.
func dirAssigned(fn *ast.FuncDecl) map[string]bool {
	states := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) {
				continue
			}
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Dir" {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				states[id.Name] = true
			}
		}
		return true
	})
	return states
}

// assignedTo answers, for every call that is the right-hand side of an
// assignment, the name of the variable it is assigned to — so a command
// built at `x := exec.Command(...)` can be asked whether x's directory was
// ever stated.
func assignedTo(fn *ast.FuncDecl) map[ast.Expr]string {
	receivers := map[ast.Expr]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			if i < len(assign.Lhs) {
				if id, ok := assign.Lhs[i].(*ast.Ident); ok {
					receivers[rhs] = id.Name
				}
			}
		}
		return true
	})
	return receivers
}

// statedADirectory is whether the command this call builds receives a
// directory of its own: through the variable it is assigned to, or through
// -C or --git-dir in its own argv — assembled through append and []string
// literals as much as spelled out literally, which is how nearly every
// runner here builds it. init and clone are exempt on their own: they take
// the repository as an argument rather than reading the caller's, and the
// argument's absoluteness is gittest's runtime rule for the helper's users.
func statedADirectory(call *ast.CallExpr, receivers map[ast.Expr]string, states map[string]bool) bool {
	for _, arg := range call.Args {
		for _, lit := range argvLiterals(arg) {
			if lit == "-C" || lit == "--git-dir" || lit == "init" || lit == "clone" {
				return true
			}
		}
	}
	if receiver, ok := receivers[call]; ok && states[receiver] {
		return true
	}
	return false
}

// argvLiterals is every string literal an expression contributes to a
// command's argv, reading through the append and composite-literal shapes a
// runner assembles it with.
func argvLiterals(expr ast.Expr) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if value, err := strconv.Unquote(e.Value); err == nil {
				return []string{value}
			}
		}
	case *ast.CallExpr:
		if calleeName(e) == "append" {
			var out []string
			for _, arg := range e.Args {
				out = append(out, argvLiterals(arg)...)
			}
			return out
		}
	case *ast.CompositeLit:
		var out []string
		for _, elt := range e.Elts {
			out = append(out, argvLiterals(elt)...)
		}
		return out
	}
	return nil
}

// callsAnyBody is whether a body calls any of the named functions,
// nested function literals excluded — a closure's calls belong to the
// closure's own scope, not to the body that holds it.
func callsAnyBody(body *ast.BlockStmt, names []string) bool {
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
	for _, stmt := range body.List {
		ast.Inspect(stmt, visit)
	}
	return found
}

// scopesCallingLive answers the bodies that call a live factory entry point
// and so must build a harness: the test's own body, and every nested function
// literal's — a subtest that deploys needs the fake PATH its own t.Setenv
// sets, wherever the deploy is written.
func scopesCallingLive(fn *ast.FuncDecl) []*ast.BlockStmt {
	var scopes []*ast.BlockStmt
	if callsAnyBody(fn.Body, factoryLiveEntrypoints) {
		scopes = append(scopes, fn.Body)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if callsAnyBody(lit.Body, factoryLiveEntrypoints) {
			scopes = append(scopes, lit.Body)
		}
		return false // nested closures are their own scopes
	})
	return scopes
}

// harnessBuiltFor is whether every scope that calls a live entry point builds
// a harness constructor in its own body — or the test's own body does, which
// covers every subtest the test runs, because t.Setenv there reaches them
// all.
func harnessBuiltFor(fn *ast.FuncDecl, scopes []*ast.BlockStmt) bool {
	for _, scope := range scopes {
		if callsAnyBody(fn.Body, factoryHarnessConstructors) {
			continue
		}
		if !callsAnyBody(scope, factoryHarnessConstructors) {
			return false
		}
	}
	return true
}

// countTagged is how many of the complaints carry one rule's tag.
func countTagged(complaints []string, tag string) int {
	n := 0
	for _, complaint := range complaints {
		if strings.HasPrefix(complaint, tag) {
			n++
		}
	}
	return n
}
