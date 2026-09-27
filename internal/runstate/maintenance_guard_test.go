package runstate

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// No test in this package leaves a git process running when it returns
// (tick 35l).
//
// The incident this guards against is reconcile's, and cost that run two
// days (tick qsn): CI failed a test that PASSED. Nothing asserted; what
// failed was Go's own teardown of the directory the test had built —
//
//	testing.go:1267: TempDir RemoveAll cleanup: unlinkat
//	/tmp/.../002/.git/objects: directory not empty
//
// RemoveAll had walked that repository's `.git/objects` and something wrote
// into it between the walk and the unlink. Every subcommand a fixture runs
// in a t.TempDir repository — init, commit, push, clone, fetch — ends by
// starting `git maintenance run --auto --detach`: git's own daemonize, fork
// and setsid, so the git the test waited for has exited while a git it never
// had a handle on is still running in the object store it is about to
// delete. reconcile pinned its own harness and added the two guards this
// file mirrors (internal/reconcile/harness_maintenance_test.go); runstate's
// gitRun was left unpinned — its environment came straight from
// os.Environ() — so every fixture repository this package builds could start
// the same child.
//
// On this host the read-only grade's GIT_CONFIG_COUNT pins mask it
// (maintenance.auto=false is pinned into the worker's own environment, and
// git reads those above every config file), which is why the full suite
// looks green here; CI's unpinned git 2.55 is the shape the incident was,
// and it is a one-in-forty flake there. The final review this tick gates
// runs the full suite on exactly that.
//
// Two guards, because the leak has two halves.
//
// The first is the RULE: the fixture's git starts no such process. It is
// checked by watching, not by reasoning — git's trace2 stream names every
// child a command starts, so "started a background maintenance" is a fact on
// disk rather than a race to observe in `ps`. That makes this deterministic
// and version-independent, which matters: whether the detached maintenance
// then WRITES anything is the 2.55-versus-2.50 split gitbin.NoAutoMaintenance
// documents, and it is why the incident was one CI run in many and never a
// laptop's.
//
// The second is the REACH: every git this package's tests start goes through
// the pinned builder. A rule pinned at one helper is worth nothing if the
// next test reaches for exec.Command directly, which is exactly how the leak
// got here.
//
// And the rule is stated in TWO places, because a push has two ends. The git
// the fixture starts directly takes its pins from the environment
// (gitbin.WithNoAutoMaintenance); but a push to a local path ends in
// git-receive-pack, and git's local transport starts that process with
// GIT_CONFIG_COUNT stripped from its environment (connect.c removes the
// repo-local environment, and the env-config mechanism is in it), so no
// environment pin — the fixture's or the store's own safeArgs — can reach the
// process that ends a push by starting maintenance IN THE ORIGIN. That half
// is pinned in the origin's own config, at creation (newOrigin); the guard
// asserts both, because either alone leaves the leak half-closed.
//
// gate: 1.1s — a leaked child is not caught by the test that leaks it. It is
// caught by the NEXT one, whose fixture it corrupts, and the failure surfaces
// wherever the RemoveAll happened to lose the race — which is how the incident
// cost two days of chasing "flaky reconcile" before anyone read a cleanup line
// as a process. CI-only is the wrong side for that: a tick that introduces the
// leak should be the tick that hears about it, not the tick after next.
func TestTheFixturesGitStartsNoGitItDoesNotWaitFor(t *testing.T) {
	shorttest.LoadBearing(t)
	t.Parallel()

	// The CONTROL: the same subcommands the way gitRun ran them before this
	// tick — no maintenance pins. Without this, a git that started no
	// maintenance for reasons of its own would pass the assertion below for
	// free, and the fixture would prove nothing about the tree.
	control := tracedGit(t, "control", func(trace string) {
		run := func(dir string, args ...string) *exec.Cmd {
			return withTrace(unpinnedGit(dir, args...), trace)
		}
		buildOriginFixture(t, filepath.Join(t.TempDir(), "control"), run, false)
	})
	if len(control) == 0 {
		t.Fatal("a plain commit and fetch started no background maintenance at all on this git; " +
			"this fixture proves nothing about gitRun")
	}

	// The fixture's builder, doing the same work: gitRun's own sequence —
	// bare origin, seed clone, commit, push, clone, move origin, fetch —
	// with the origin pinned the way newOrigin pins it, because the
	// environment half of the rule cannot reach a push's receive-pack.
	leaked := tracedGit(t, "fixture", func(trace string) {
		run := func(dir string, args ...string) *exec.Cmd {
			return gitCommand(args...).withEnv(trace).command(dir)
		}
		buildOriginFixture(t, filepath.Join(t.TempDir(), "fixture"), run, true)
	})
	if len(leaked) != 0 {
		t.Errorf("the fixture's git started %d background process(es) no test waits for: %s\n"+
			"Each is `git maintenance run --auto --detach` — forked, setsid'd, and still writing in "+
			"that repository's .git/objects after the command returned. A test that returns with one "+
			"alive is the CI failure this pins: t.TempDir's RemoveAll walks the directory while the "+
			"maintenance writes into it, and a passing test fails on \"directory not empty\" (tick qsn).",
			len(leaked), strings.Join(leaked, "; "))
	}
}

// buildOriginFixture is newOrigin's own sequence, run through whichever
// builder it is handed: bare origin, seed repository, commit, push, clone,
// then one more commit pushed so the fetch transfers objects — a fetch with
// nothing to bring home has nothing to maintain either.
//
// pinOrigin is whether the bare origin gets the maintenance pins the fixture
// applies to a repository that will receive pushes — newOrigin's own three
// git config writes, which is the push half of the rule, because the
// environment half (gitbin.WithNoAutoMaintenance) cannot reach a push's
// receive-pack: git's local transport strips GIT_CONFIG_COUNT from its
// environment. The control passes false: the origin gitRun built before
// this tick was unpinned there too, and the control must prove that an
// unpinned origin's push side leaks as much as its commit side does.
func buildOriginFixture(t *testing.T, root string, run func(dir string, args ...string) *exec.Cmd, pinOrigin bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	clone := filepath.Join(root, "clone")

	mustSucceed(t, run(root, "init", "--quiet", "--bare", bare))
	if pinOrigin {
		mustSucceed(t, run(bare, "config", "maintenance.auto", "false"))
		mustSucceed(t, run(bare, "config", "receive.autogc", "false"))
		mustSucceed(t, run(bare, "config", "gc.auto", "0"))
	}
	// HEAD on the branch, the way a real origin's is: a bare repository whose
	// HEAD still points at the init default fetches nothing, and a fetch that
	// brings nothing home has nothing to maintain either.
	mustSucceed(t, run(bare, "symbolic-ref", "HEAD", "refs/heads/"+testBranch))
	mustSucceed(t, run(root, "init", "--quiet", "-b", testBranch, seed))
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("the target repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, run(seed, "add", "-A"))
	mustSucceed(t, run(seed, "commit", "--quiet", "-m", "seed"))
	mustSucceed(t, run(seed, "push", "--quiet", bare, testBranch))
	mustSucceed(t, run(root, "clone", "--quiet", "--no-checkout", bare, clone))

	if err := os.WriteFile(filepath.Join(seed, "move.txt"), []byte("origin moves\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, run(seed, "add", "-A"))
	mustSucceed(t, run(seed, "commit", "--quiet", "-m", "origin moves"))
	mustSucceed(t, run(seed, "push", "--quiet", bare, testBranch))
	mustSucceed(t, run(clone, "fetch", "--quiet", bare))
}

// Every git this package's tests start goes through the pinned builder
// (tick 35l). The rule above reaches only as far as this holds.
//
// short: an AST scan of this package's own test files
func TestEveryGitTheTestsStartGoesThroughThePinnedRunner(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// The two sanctioned builders of a git command line, exempted BY FUNCTION
	// and not by file: gitCmd.command states the rule, and unpinnedGit is the
	// control that proves the rule bites. A file-wide exemption would let a
	// third git in beside them without anybody noticing.
	sanctioned := map[string]bool{"command": true, "unpinnedGit": true}

	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || sanctioned[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if named, ok := execCommandName(call); ok && namesGit(named) {
					offenders = append(offenders, fset.Position(call.Pos()).String())
				}
				return true
			})
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Errorf("these tests start git without going through gitCommand:\n  %s\n"+
			"gitCommand is where gitbin.WithNoAutoMaintenance is stated, and a git without it ends "+
			"by forking `git maintenance run --auto --detach` into the repository the test is about to "+
			"delete — a background process that outlives the test and breaks the NEXT one's fixture "+
			"(tick qsn, reconcile's two days; tick 35l is this package's half). Route it through "+
			"gitRun, gitCommand, or — if it is a control that needs an unpinned git — unpinnedGit.",
			strings.Join(offenders, "\n  "))
	}
}

// execCommandName is the NAME argument of an exec.Command or
// exec.CommandContext call — the second one for CommandContext, whose first
// is the context.
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
	if sel.Sel.Name == "CommandContext" {
		index = 1
	} else if sel.Sel.Name != "Command" {
		return nil, false
	}
	if len(call.Args) <= index {
		return nil, false
	}
	return call.Args[index], true
}

// namesGit is whether an exec.Command's name argument is a git: the literal
// "git" (or a path ending in it), or gitbin.Path().
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

// tracedGit runs `body` with a trace2 event directory of its own and answers
// with the command line of every DETACHED maintenance one of those git
// processes started.
//
// git writes one event file per process into the directory GIT_TRACE2_EVENT
// names, and a process records its own argv on the way in. So a maintenance
// nobody waited for is not something to catch in `ps` before it exits — it is
// a file that is either there or is not, whatever the scheduler did.
func tracedGit(t *testing.T, name string, body func(trace string)) []string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trace-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body("GIT_TRACE2_EVENT=" + dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var event struct {
				Event string   `json:"event"`
				Argv  []string `json:"argv"`
			}
			if json.Unmarshal([]byte(line), &event) != nil || event.Event != "start" {
				continue
			}
			if isBackgroundMaintenance(event.Argv) {
				started = append(started, strings.Join(event.Argv[1:], " "))
			}
		}
	}
	sort.Strings(started)
	return started
}

// isBackgroundMaintenance is whether an argv is one of the processes git
// starts and does not wait for: `maintenance run --auto --detach`, and the
// `gc --auto` an older git ran in its place.
func isBackgroundMaintenance(argv []string) bool {
	// Walk past git's global options to the SUBCOMMAND, so that a repository
	// path or a config value spelled "gc" cannot read as one. The options
	// that take a separate value are skipped with it.
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-c" || arg == "-C" || arg == "--git-dir" || arg == "--work-tree" ||
			arg == "--namespace" || arg == "--exec-path" || arg == "--config-env":
			i++
		case strings.HasPrefix(arg, "-"):
			// A long option carrying its own value, or a plain flag.
		default:
			return arg == "maintenance" || arg == "gc"
		}
	}
	return false
}

func mustSucceed(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out)
	}
}

// withTrace turns trace2 on for a control's git: the event directory's
// entries are what tracedGit reads.
func withTrace(cmd *exec.Cmd, trace string) *exec.Cmd {
	cmd.Env = append(cmd.Env, trace)
	return cmd
}
