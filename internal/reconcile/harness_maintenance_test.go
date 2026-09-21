package reconcile

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

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// No test in this package leaves a git process running when it returns
// (tick qsn).
//
// The incident: CI failed a test that PASSED. Nothing asserted; what failed
// was Go's own teardown of the directory the test had built —
//
//	--- FAIL: TestAChangedGateCommandIsNotEvidenceForTheGateDeclaredNow (24.37s)
//	    testing.go:1267: TempDir RemoveAll cleanup: unlinkat
//	    /tmp/TestAChangedGateCommand.../002/.git/objects: directory not empty
//
// RemoveAll had walked that repository's `.git/objects` and something wrote
// into it between the walk and the unlink. `002` is the third directory
// t.TempDir handed that test: commitOnto's, a clone the test made, committed
// in and fetched back. Every one of those three subcommands ends by starting
// `git maintenance run --auto --detach` — git's own daemonize, fork and
// setsid — so the git the test waited for had exited while a git it never had
// a handle on was still running in the object store it was about to delete.
//
// Two guards, because the leak has two halves.
//
// The first is the RULE: the harness's runner starts no such process. It is
// checked by watching, not by reasoning — git's trace2 stream names every
// child a command starts, so "started a background maintenance" is a fact on
// disk rather than a race to observe in `ps`. That makes this deterministic
// and version-independent, which matters: whether the detached maintenance
// then WRITES anything is the 2.55-versus-2.50 split gitbin.NoAutoMaintenance
// documents, and it is why the incident was one CI run in many and never a
// laptop's.
//
// The second is the REACH: every git this package's tests start goes through
// that runner. A rule pinned at one helper is worth nothing if the next test
// reaches for exec.Command directly, which is exactly how this one was
// introduced.
//
// gate: 0.5s — a leaked child is not caught by the test that leaks it. It is
// caught by the NEXT one, whose fixture it corrupts, and the failure surfaces
// wherever the RemoveAll happened to lose the race — which is how this cost
// two days of chasing "flaky reconcile" before anyone read a cleanup line as
// a process. CI-only is the wrong side for that: a tick that introduces the
// leak should be the tick that hears about it, not the tick after next. The
// price is one repository, two clones and a dozen git processes, measured at
// 0.31s in the full -short suite and 0.20s alone.
func TestTheHarnessStartsNoGitItDoesNotWaitFor(t *testing.T) {
	shorttest.LoadBearing(t)
	t.Parallel()

	// A repository of its own rather than newRepo's: this guard is about the
	// harness's git and nothing else, so it takes none of the end-to-end
	// fixture's cost and none of its -short skip. It runs in every gate.
	root := t.TempDir()
	source := filepath.Join(root, "source")
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", source)
	configure(t, source)
	write(t, filepath.Join(source, "README.md"), "# source\n")
	mustRun(t, source, "git", "add", "-A")
	mustRun(t, source, "git", "commit", "--quiet", "-m", "base")

	// The CONTROL: the same subcommands run the way the harness ran them
	// before this tick. Without this, a git that started no maintenance for
	// reasons of its own would pass the assertion below for free.
	control := tracedGit(t, "control", func(trace string) {
		clone := filepath.Join(t.TempDir(), "plain")
		mustSucceed(t, withTrace(unpinnedGit(root, "clone", "--quiet", source, clone), trace))
		configure(t, clone)
		write(t, filepath.Join(clone, "control.txt"), "control\n")
		mustSucceed(t, withTrace(unpinnedGit(clone, "add", "-A"), trace))
		mustSucceed(t, withTrace(unpinnedGit(clone, "commit", "--quiet", "-m", "control"), trace))
		// Something to transfer, so the fetch is a fetch and not a no-op.
		moveSource(t, source, "control")
		mustSucceed(t, withTrace(unpinnedGit(clone, "fetch", "--quiet", "origin"), trace))
	})
	if len(control) == 0 {
		t.Fatal("a plain commit and fetch started no background maintenance at all on this git; " +
			"this fixture proves nothing about the harness")
	}

	// The harness's runner, doing the same work: commitOnto's shape exactly —
	// clone, configure, commit, fetch the commit back.
	leaked := tracedGit(t, "harness", func(trace string) {
		clone := filepath.Join(t.TempDir(), "harness")
		mustSucceed(t, harnessCommand("git", "clone", "--quiet", source, clone).withEnv(trace).command(root))
		configure(t, clone)
		write(t, filepath.Join(clone, "work.txt"), "work\n")
		mustSucceed(t, harnessCommand("git", "add", "-A").withEnv(trace).command(clone))
		mustSucceed(t, harnessCommand("git", "commit", "--quiet", "-m", "work").withEnv(trace).command(clone))
		moveSource(t, source, "harness")
		mustSucceed(t, harnessCommand("git", "fetch", "--quiet", "origin").withEnv(trace).command(clone))
	})
	if len(leaked) != 0 {
		t.Errorf("the harness's git started %d background process(es) no test waits for: %s\n"+
			"Each is `git maintenance run --auto --detach` — forked, setsid'd, and still writing in "+
			"that repository's .git/objects after the command returned. A test that returns with one "+
			"alive is the CI failure this pins: t.TempDir's RemoveAll walks the directory while the "+
			"repack writes into it, and a passing test fails on \"directory not empty\" (tick qsn).",
			len(leaked), strings.Join(leaked, "; "))
	}
}

// Every git this package's tests start goes through the harness's runner
// (tick qsn). The rule above reaches only as far as this holds.
//
// short: an AST scan of this package's own test files
func TestEveryGitTheTestsStartGoesThroughTheHarnessRunner(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// The two sanctioned builders of a git command line, exempted BY FUNCTION
	// and not by file: one states the rule, the other is the control that
	// proves the rule bites. A file-wide exemption would let a third git in
	// beside them without anybody noticing.
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
		t.Errorf("these tests start git without going through harnessCommand:\n  %s\n"+
			"harnessCommand is where gitbin.WithNoAutoMaintenance is stated, and a git without it ends "+
			"by forking `git maintenance run --auto --detach` into the repository the test is about to "+
			"delete — a background process that outlives the test and breaks the NEXT one's fixture "+
			"(tick qsn). Route it through mustRun or harnessCommand.",
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
func tracedGit(t *testing.T, name string, body func(traceEnv string)) []string {
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

// moveSource puts one new commit on the repository the clones fetch from, so
// that the fetch under trace transfers objects rather than answering "up to
// date" — a fetch with nothing to bring home has nothing to maintain either.
func moveSource(t *testing.T, source, name string) {
	t.Helper()
	write(t, filepath.Join(source, name+".txt"), name+"\n")
	mustRun(t, source, "git", "add", "-A")
	mustRun(t, source, "git", "commit", "--quiet", "-m", "upstream moves for "+name)
}

// unpinnedGit is a git run WITHOUT the harness's pins: an operator's own
// command, which is a git that DOES end by starting background maintenance.
//
// It exists for the two CONTROLS that need one — this file's, and the one
// maintenance_test.go states before it asserts anything about the
// reconciler's own git (tick mel). Both are the same argument: an assertion
// that a particular git starts no maintenance is worth nothing on a machine
// where no git would have. Nothing else in this package may build a git
// command; TestEveryGitTheTestsStartGoesThroughTheHarnessRunner is what says
// so, and this file is exempt from it precisely because of these two lines.
func unpinnedGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

func mustSucceed(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out)
	}
}

func withTrace(cmd *exec.Cmd, trace string) *exec.Cmd {
	cmd.Env = append(cmd.Env, trace)
	return cmd
}
