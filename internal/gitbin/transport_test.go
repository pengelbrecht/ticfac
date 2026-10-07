package gitbin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The transport is bounded on both of git's network transports unless the
// operator bounded it themselves — and the respect is per variable.
//
// ssh (tick pul): a `git fetch` held its ssh open for two and a half hours,
// because ssh's defaults have no connect timeout and no keepalive. https
// (epic-6in): a checkpoint push sat thirty minutes inside a silent `git
// remote-https`, because curl's defaults have no low-speed abort.
// short: TransportEnv() over three environment variables
func TestTheTransportIsBoundedUnlessTheOperatorBoundItThemselves(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_HTTP_LOW_SPEED_LIMIT", "")
	t.Setenv("GIT_HTTP_LOW_SPEED_TIME", "")
	env := TransportEnv()
	get := func(env []string, name string) (string, bool) {
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, name+"="); ok {
				return value, true
			}
		}
		return "", false
	}

	ssh, ok := get(env, "GIT_SSH_COMMAND")
	if !ok {
		t.Fatalf("TransportEnv() = %v sets no GIT_SSH_COMMAND", env)
	}
	for _, want := range []string{"ConnectTimeout=", "ServerAliveInterval=", "ServerAliveCountMax="} {
		if !strings.Contains(ssh, want) {
			t.Errorf("%q does not set %s: an ssh that can wait forever is a run that can stall forever", ssh, want)
		}
	}
	for _, name := range []string{"GIT_HTTP_LOW_SPEED_LIMIT", "GIT_HTTP_LOW_SPEED_TIME"} {
		value, ok := get(env, name)
		if n, err := strconv.Atoi(value); !ok || err != nil || n <= 0 {
			t.Errorf("TransportEnv() = %v: %s is %q, want a positive number — curl has no low-speed abort "+
				"unless both are set, and an https remote that goes silent is then waited on forever (epic-6in)",
				env, name, value)
		}
	}

	// An operator who has said how to reach their remote, or how patient to
	// be with it, is not overruled — and saying one thing does not switch the
	// other bounds off.
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /custom/key")
	t.Setenv("GIT_HTTP_LOW_SPEED_TIME", "600")
	env = TransportEnv()
	if _, ok := get(env, "GIT_SSH_COMMAND"); ok {
		t.Errorf("TransportEnv() = %v overrides an operator's own GIT_SSH_COMMAND", env)
	}
	if _, ok := get(env, "GIT_HTTP_LOW_SPEED_TIME"); ok {
		t.Errorf("TransportEnv() = %v overrides an operator's own GIT_HTTP_LOW_SPEED_TIME", env)
	}
	if _, ok := get(env, "GIT_HTTP_LOW_SPEED_LIMIT"); !ok {
		t.Errorf("TransportEnv() = %v dropped the low-speed limit because the operator set only the time", env)
	}
}

// Every git this repository starts that can reach a remote carries
// TransportEnv. epic-6in's stall was not a missing bound so much as a bound
// that lived in one runner: runstate's and reconcile's gits had the ssh half,
// and the executors' gits, the completion signal's ls-remote and the cloud
// command's push had nothing at all. A new runner written next month is the
// same gap again unless something reads it.
//
// So this reads every production Go file for the calls that start git —
// os/exec's Command or CommandContext naming "git" or gitbin.Path() — and
// requires the function that makes the call to mention TransportEnv. A call
// is let off only when its own argv names a subcommand that never leaves the
// machine (rev-parse, config, cat-file, ...) in literals the reader can see.
// A runner that takes its argv from its caller can be handed a push, so it
// carries the bound whatever its callers do today.
//
// short: parses the repository's Go files; no git, no processes
func TestEveryGitThatCanReachARemoteIsBounded(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	unused := map[string]bool{}
	for key := range readsOnlyTheLocalRepository {
		unused[key] = true
	}
	var offenders []string
	for _, path := range trackedGoFiles(t, root) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		found, err := unboundedGits(rel, src)
		if err != nil {
			t.Fatalf("scan %s: %v", rel, err)
		}
		offenders = append(offenders, found...)
	}
	offenders = slices.DeleteFunc(offenders, func(offender string) bool {
		file, rest, _ := strings.Cut(offender, ":")
		_, fn, _ := strings.Cut(rest, " in ")
		key := filepath.ToSlash(file) + " " + fn
		if _, ok := readsOnlyTheLocalRepository[key]; ok {
			delete(unused, key)
			return true
		}
		return false
	})
	for key := range unused {
		t.Errorf("readsOnlyTheLocalRepository lets off %q, which no longer starts git: drop the entry", key)
	}
	if len(offenders) > 0 {
		t.Fatalf("git started where a remote can be reached, with no transport bound:\n  %s\n"+
			"Add gitbin.TransportEnv() to the command's environment, in the function that starts it: "+
			"a push or fetch to a remote that goes silent otherwise holds the run forever (tick pul over ssh, "+
			"epic-6in over https).", strings.Join(offenders, "\n  "))
	}
}

// The guard's own negative control: it must see an unbounded push, an
// unbounded runner, and a bounded one, or its silence above means nothing.
//
// short: parses three small sources in memory
func TestTheTransportGuardSeesAnUnboundedGit(t *testing.T) {
	t.Parallel()
	const src = `package p

import (
	osexec "os/exec"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

func push() { osexec.Command("git", "-C", "/r", "push", "origin", "main").Run() }

func runner(args ...string) { osexec.Command(gitbin.Path(), append([]string{"-c", "a=b"}, args...)...).Run() }

func bounded(args ...string) {
	cmd := osexec.Command(gitbin.Path(), args...)
	cmd.Env = gitbin.TransportEnv()
}

func local() { osexec.Command("git", "-C", "/r", "rev-parse", "HEAD").Run() }

func notGit() { osexec.Command("sh", "-c", "git push").Run() }
`
	got, err := unboundedGits("p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p.go:8 in push", "p.go:10 in runner"}
	if !slices.Equal(got, want) {
		t.Fatalf("the guard reported %q, want %q", got, want)
	}
}

// readsOnlyTheLocalRepository names the spawns ("file func") whose argv this
// reader cannot see through but which are local by construction, each with
// why. It is short on purpose: an entry is a claim a reviewer checks, and the
// guard fails on one that no longer matches anything.
var readsOnlyTheLocalRepository = map[string]string{
	// Both spell their subcommand as a literal, but after runstate's safeArgs
	// spread, a package variable of -c flags the reader cannot resolve.
	"internal/runstate/batch.go start":          "`cat-file --batch`: the store's held-open object reader",
	"internal/runstate/batch.go catFileProcess": "`cat-file blob <sha>`: one object read",
	// internal/gittest is the test fixtures' one hermetic helper (tick pqs).
	// Command and Control ARE bounded — Env() and ControlEnv() append
	// gitbin.TransportEnv — but the mention is one call away, past this
	// guard's single-function view. Under is deliberately unbounded: the
	// environment it runs under is the fixture's own subject (the sandbox
	// grades), and a bound the helper added would erase the thing under test.
	"internal/gittest/gittest.go Command": "bounded through Env(), which appends gitbin.TransportEnv",
	"internal/gittest/gittest.go Control": "bounded through ControlEnv(), which appends gitbin.TransportEnv",
	"internal/gittest/gittest.go Under":   "the caller's environment is the fixture's subject; a bound would erase it",
}

// localSubcommands never leave the machine. A literal one of these, named in
// the call's own argv, is the only thing that lets a git spawn go unbounded.
var localSubcommands = map[string]bool{}

func init() {
	for _, name := range strings.Fields("rev-parse rev-list config cat-file show log diff status " +
		"merge-base worktree branch show-ref update-ref symbolic-ref for-each-ref ls-files ls-tree " +
		"read-tree write-tree commit-tree hash-object mktree update-index var version init " +
		"check-ref-format check-ignore") {
		localSubcommands[name] = true
	}
	// `git remote get-url` reads config; `git remote` alone lists names.
	localSubcommands["remote"] = true
}

// unboundedGits is the guard over one file: every git spawn whose argv may
// reach a remote, in a function that never mentions TransportEnv.
func unboundedGits(name string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	execName := ""
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			execName = "exec"
			if imp.Name != nil {
				execName = imp.Name.Name
			}
		}
	}
	if execName == "" {
		return nil, nil
	}
	var offenders []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		bounded := mentions(fn, "TransportEnv")
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != execName {
				return true
			}
			var args []ast.Expr
			switch sel.Sel.Name {
			case "Command":
				args = call.Args
			case "CommandContext":
				if len(call.Args) == 0 {
					return true
				}
				args = call.Args[1:]
			default:
				return true
			}
			if len(args) == 0 || !isGit(args[0]) {
				return true
			}
			if !bounded && mayReachARemote(args[1:], call.Ellipsis.IsValid()) {
				offenders = append(offenders, name+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" in "+fn.Name.Name)
			}
			return true
		})
	}
	return offenders, nil
}

func mentions(fn *ast.FuncDecl, ident string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == ident {
			found = true
		}
		return !found
	})
	return found
}

// isGit is the program argument naming git: the literal, or gitbin.Path().
func isGit(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		value, _ := strconv.Unquote(e.Value)
		return value == "git"
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			pkg, ok := sel.X.(*ast.Ident)
			return ok && pkg.Name == "gitbin" && sel.Sel.Name == "Path"
		}
		if id, ok := e.Fun.(*ast.Ident); ok {
			return id.Name == "Path" // gitbin's own
		}
	}
	return false
}

// mayReachARemote reads an argv as far as its literals go. The first word that
// is not a global flag is the subcommand; a local one lets the call off. Any
// word the reader cannot see — a variable, or a spread slice — where the
// subcommand would be means the caller chooses, and a caller can choose push.
func mayReachARemote(args []ast.Expr, spread bool) bool {
	words, known := flatten(args, spread)
	for i := 0; i < len(words); i++ {
		if !known[i] {
			return true
		}
		word := words[i]
		if strings.HasPrefix(word, "-") {
			switch word {
			case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
				i++
			}
			continue
		}
		return !localSubcommands[word]
	}
	return true
}

// flatten turns an argv into words, with known[i] false for any word the
// source does not spell as a string literal. append(...) and []string{...}
// spreads are opened; any other spread is one unknown word.
func flatten(args []ast.Expr, spread bool) (words []string, known []bool) {
	add := func(word string, ok bool) {
		words = append(words, word)
		known = append(known, ok)
	}
	var visit func(expr ast.Expr, spread bool)
	visit = func(expr ast.Expr, spread bool) {
		if !spread {
			if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				value, _ := strconv.Unquote(lit.Value)
				add(value, true)
				return
			}
			add("", false)
			return
		}
		switch e := expr.(type) {
		case *ast.CompositeLit:
			for _, elt := range e.Elts {
				visit(elt, false)
			}
			return
		case *ast.CallExpr:
			if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "append" && len(e.Args) > 0 {
				visit(e.Args[0], true)
				for i, arg := range e.Args[1:] {
					visit(arg, e.Ellipsis.IsValid() && i == len(e.Args)-2)
				}
				return
			}
		}
		add("", false)
	}
	for i, arg := range args {
		visit(arg, spread && i == len(args)-1)
	}
	return words, known
}

// trackedGoFiles answers the .go files git knows about under root, in the
// order git lists them.
//
// Both repository-wide guards in this package (this transport guard, and the
// push-queue guard beside it) read the tree's TRACKED files rather than
// walking the directory (tick pqs): a checkout is not a clean room — an
// agent worktree or a scratch clone under it is no party to this tree, and
// the old walks had to name every stray directory after the fact.
//
// It runs git itself, through this package's own binary and pins — this is
// the package internal/gittest is built on, so it cannot import the helper,
// and this package's tests are the one place the hermeticity guard exempts.
func trackedGoFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command(Path(), "ls-files", "*.go")
	cmd.Dir = root
	cmd.Env = append(WithoutPinnedConfig(os.Environ()),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", root, err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		files = append(files, filepath.Join(root, filepath.FromSlash(line)))
	}
	return files
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
