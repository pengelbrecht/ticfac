// Package gatescope says which packages' full — non-short — test suites a
// tick's gate must run, and runs them.
//
// The per-tick gate runs `go test -short ./...` across the whole repository,
// which is cheap and covers the readers, the parsers and the drift guards.
// What it cannot see is the half of the suite that skips under -short: the
// end-to-end tests, the ones that build real repositories and spawn real
// worker processes. A tick that broke one of those passed every gate and was
// first refused by CI's full suite at the epic's close-out (tick dz1: a
// pinned-version change broke four internal/reconcile end-to-end tests, and
// the break cost a repair and two ~30-minute CI cycles to surface). This
// package is the other half of the answer: the packages a tick TOUCHED — and
// the packages whose code or tests import them — get their full suites run
// in the gate that gates that tick, and no other package does.
//
// # The inputs

// The gate hands the check its subject. The integrated gate (internal/
// reconcile) exports, into every declared gate command's environment, the two
// commits whose three-dot diff is the files the tick being gated changed:
//
//	TICFAC_GATE_TOUCHED_BASE   the integration branch's head at the merge
//	TICFAC_GATE_TOUCHED_HEAD   the attempt's head the merge carried in
//
// `git diff --name-only $BASE...$HEAD` is the tick's own delta — the same
// three-dot shape the status model reads a merged attempt's diff with — and
// never the branch's other ticks or the run's own records. Both names are
// ALWAYS exported, an empty value meaning "this gate has no touched diff to
// offer" (the fold at an epic's close-out; an adopted attempt whose merge
// this run cannot name), so a command can tell the gate's "nothing to diff"
// from a bare invocation by a person, which falls back to the person's own
// branch against origin/main.
//
// # The rule

// A changed file belongs to the deepest package whose directory is an
// ancestor of the file's path, so a fixture script under testdata/ counts as
// a change to the package whose tests read it, and a file under no package
// (docs, the repo's own scripts) counts as nothing. The one exception is
// the ROOT package: a repository's root directory holds the repository's own
// files — the Makefile, README, go.mod — and the drift guards that read
// those run in the gate's SHORT suite, which is already paid for; mapping
// them to the root package would bill every root-file tick for the full
// suites of everything that transitively imports it, untouched. The root
// package owns only the Go files of its own directory.
//
// The selection is that set plus every package that reaches it over the
// module's import graph — code imports and TEST imports alike, transitively,
// all read from one `go list` invocation — because dz1's shape is exactly a
// package whose build imports nothing of the tick's change but whose tests
// break on it.
//
// The run is one `go test` over the selection, non-short, with the timeout
// and parallelism the repository declares in its gate, and WITHOUT -count=1:
// a package whose inputs are unchanged answers from Go's own test cache,
// which is a true statement about this tree (the audit on tick mbv), and is
// what makes a re-gate over a repair that touched the other half cheap.
package gatescope

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The gate's contract with this check, in environment variable names. The
// authoritative description of what the reconciler exports is in
// internal/reconcile/gate_touched.go; these names are the one thing both
// sides spell, so they live here once and the reconciler imports them.
const (
	EnvBase = "TICFAC_GATE_TOUCHED_BASE"
	EnvHead = "TICFAC_GATE_TOUCHED_HEAD"
)

// Shell runs one command in the repository root and answers its combined
// output. It is a field rather than a call so the selection can be tested
// over fixed inputs, and so the check's own `go test` can be run by the same
// seam with output inherited (Run writes, the selection reads).
type Shell func(name string, args ...string) (string, error)

// NewShell is the Shell that really runs things, in the working directory
// the gate's command was started in.
func NewShell() Shell {
	return func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
}

// Package is one package of the module the check can run: its import path,
// its directory relative to the repository root, and the module-internal
// packages its code, its tests and its external tests import directly.
type Package struct {
	ImportPath   string
	Dir          string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

// graphTemplate is the one `go list` invocation the whole selection reads:
// the package's import path, its directory, and its three direct import lists.
// The lists are joined with spaces because the fields are split on tabs, and
// every import outside the module is dropped when the record is parsed.
const graphTemplate = `{{.ImportPath}}	{{.Dir}}	{{join .Imports " "}}	{{join .TestImports " "}}	{{join .XTestImports " "}}`

// LoadPackages reads the module's package graph. One invocation, one record
// per package; the direct edges a package's tests add are in the same record,
// so the reverse closure over code and test imports costs nothing extra.
func LoadPackages(shell Shell) ([]Package, error) {
	out, err := shell("go", "list", "-f", graphTemplate, "./...")
	if err != nil {
		return nil, fmt.Errorf("read the module's packages with go list: %v\n%s", err, strings.TrimSpace(out))
	}
	var pkgs []Package
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("go list printed a record this check cannot read (%d fields): %q", len(fields), line)
		}
		dir, err := moduleDir(fields[1])
		if err != nil {
			return nil, fmt.Errorf("read the directory of package %s: %w", fields[0], err)
		}
		pkgs = append(pkgs, Package{
			ImportPath: fields[0], Dir: dir,
			Imports: strings.Fields(fields[2]), TestImports: strings.Fields(fields[3]),
			XTestImports: strings.Fields(fields[4]),
		})
	}
	return pkgs, nil
}

// moduleDir is a package directory relative to the repository root, in the
// slash-separated form git's diff paths use. `go list` prints it absolute;
// the root prints as the empty-relative ".", which the mapping below treats
// as the ancestor of everything.
func moduleDir(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("no directory")
	}
	if !filepath.IsAbs(dir) {
		return filepath.ToSlash(filepath.Clean(dir)), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("package directory %s is outside this repository", dir)
	}
	return filepath.ToSlash(rel), nil
}

// ChangedFiles is the tick's own delta: `git diff --name-only BASE...HEAD`,
// read with -z so a path with a newline or a quote in it is not mangled by
// git's quoting. The three-dot form measures from the merge base of the two
// sides, so files only the integration branch changed since the fork are
// not the tick's.
func ChangedFiles(shell Shell, base, head string) ([]string, error) {
	out, err := shell("git", "diff", "-z", "--name-only", base+"..."+head)
	if err != nil {
		return nil, fmt.Errorf("read the diff %s...%s: %v\n%s", base, head, err, strings.TrimSpace(out))
	}
	var files []string
	for _, file := range strings.Split(out, "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	return files, nil
}

// TouchPackages maps the changed files onto the packages that own them: for
// each file, the package whose directory is the deepest ancestor of the
// file's path. The answer is sorted and deduplicated.
func TouchPackages(files []string, pkgs []Package) []string {
	owners := map[string]bool{}
	for _, file := range files {
		if best := owningPackage(file, pkgs); best != "" {
			owners[best] = true
		}
	}
	return sortedKeys(owners)
}

// owningPackage is the deepest package directory that contains file: an
// ancestor, the file's own directory, or nothing when the change lives under
// no package at all. Paths are the slash-separated, root-relative form both
// git and LoadPackages answer in.
//
// The root package is the exception the doc comment above argues for: it
// owns only the Go files of the repository's root directory, never the
// repository's own non-Go files and never the tree below it.
func owningPackage(file string, pkgs []Package) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file)))
	inRoot, isGo := dir == ".", strings.HasSuffix(file, ".go")
	best, bestDepth := "", -1
	for _, pkg := range pkgs {
		if pkg.Dir == "." {
			if !inRoot || !isGo {
				continue
			}
		} else if pkg.Dir != dir && !strings.HasPrefix(dir, pkg.Dir+"/") {
			continue
		}
		// The root package's Dir is "."; its depth is 0, so a deeper package
		// always wins where one exists.
		depth := 0
		if pkg.Dir != "." {
			depth = strings.Count(pkg.Dir, "/") + 1
		}
		if depth > bestDepth {
			best, bestDepth = pkg.ImportPath, depth
		}
	}
	return best
}

// ReverseClosure is the transitive closure of the seed set over the module's
// import graph, walked backwards: every package whose code, tests or
// external tests — directly or through another package's tests — reach a
// seed package. The answer is sorted and includes the seeds.
func ReverseClosure(seeds []string, pkgs []Package) []string {
	listed := map[string]bool{}
	for _, pkg := range pkgs {
		listed[pkg.ImportPath] = true
	}
	reverse := map[string][]string{}
	for _, pkg := range pkgs {
		for _, imported := range append(append(append([]string{}, pkg.Imports...),
			pkg.TestImports...), pkg.XTestImports...) {
			if listed[imported] && imported != pkg.ImportPath {
				reverse[imported] = append(reverse[imported], pkg.ImportPath)
			}
		}
	}
	seen := map[string]bool{}
	queue := append([]string{}, seeds...)
	for _, seed := range seeds {
		seen[seed] = true
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, importer := range reverse[current] {
			if seen[importer] {
				continue
			}
			seen[importer] = true
			queue = append(queue, importer)
		}
	}
	return sortedKeys(seen)
}

// Select is the whole selection: the tick's diff, the packages that own its
// files, and the packages whose tests or code import those.
func Select(shell Shell, base, head string) ([]string, error) {
	files, err := ChangedFiles(shell, base, head)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	pkgs, err := LoadPackages(shell)
	if err != nil {
		return nil, err
	}
	touched := TouchPackages(files, pkgs)
	if len(touched) == 0 {
		return nil, nil
	}
	return ReverseClosure(touched, pkgs), nil
}

// Pair resolves the diff the check is about: the gate's exported pair when
// the gate exported one — empty means nothing to run — or, for a person
// running the check by hand with no gate above it, the working branch
// against its merge base with origin/main, which is the same question said
// for the tree a person is about to push.
//
// The two names are read as a pair: one without the other is a contract
// break the gate never produces, and it is refused rather than guessed at.
func Pair(getenv func(string) (string, bool), shell Shell) (base, head string, err error) {
	base, baseOK := getenv(EnvBase)
	head, headOK := getenv(EnvHead)
	switch {
	case baseOK && headOK:
		return base, head, nil
	case baseOK || headOK:
		return "", "", fmt.Errorf("the gate exported %s but not %s: the pair is exported whole or not at all",
			EnvBase, EnvHead)
	}
	out, err := shell("git", "merge-base", "HEAD", "origin/main")
	if err != nil {
		return "", "", fmt.Errorf("no gate exported a touched diff, and the working branch has no merge base "+
			"with origin/main to diff instead: %v\n%s", err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), "HEAD", nil
}

// Options is the `go test` the check runs: the timeout and the parallelism
// the repository's gate declares, passed to go test verbatim, and the
// packages — by import path — this repository declares too expensive for a
// per-tick gate: their full suites are CI's (and the close-out's), not the
// gate's, and the check names each one it leaves rather than running it
// silently.
type Options struct {
	Timeout   string
	Parallel  string
	LeaveToCI []string
}

// Run is the check: resolve the diff, select the packages, run their full
// suites. Everything — the narration and the go test output alike — goes to
// out, which is the gate's captured stdout when the gate runs it: the one
// stream a failing check's evidence is read from.
//
// The exit code is the check's: zero when every selected package passed (or
// nothing was selected, which is a pass that says so), non-zero when the
// suites or the inputs failed.
func Run(opts Options, getenv func(string) (string, bool), shell Shell, out io.Writer) int {
	base, head, err := Pair(getenv, shell)
	if err != nil {
		fmt.Fprintf(out, "gate-touched: %v\n", err)
		return 1
	}
	if base == "" || head == "" {
		fmt.Fprintf(out, "gate-touched: this gate names no touched diff, so no package's full suite runs\n")
		return 0
	}
	pkgs, err := Select(shell, base, head)
	if err != nil {
		fmt.Fprintf(out, "gate-touched: %v\n", err)
		return 1
	}
	if len(pkgs) == 0 {
		fmt.Fprintf(out, "gate-touched: the diff %s...%s changes no Go package, so no full suite runs\n",
			shortSHA(base), shortSHA(head))
		return 0
	}
	// The per-package budget (tick r1f): the packages this repository refuses
	// to pay for per tick are taken out of the run and named in the output,
	// each with the reason, so a gate that skips one says so in its own
	// evidence. A name that is no package of this module is a typo that would
	// otherwise silently pay for the suite it meant to leave: it is refused.
	if len(opts.LeaveToCI) > 0 {
		graph, err := LoadPackages(shell)
		if err != nil {
			fmt.Fprintf(out, "gate-touched: %v\n", err)
			return 1
		}
		if err := validateLeaveToCI(opts.LeaveToCI, graph); err != nil {
			fmt.Fprintf(out, "gate-touched: %v\n", err)
			return 1
		}
		pkgs = leaveToCI(pkgs, opts.LeaveToCI, out)
		if len(pkgs) == 0 {
			fmt.Fprintf(out, "gate-touched: every package the diff %s...%s reaches is left to CI, so no full suite runs\n",
				shortSHA(base), shortSHA(head))
			return 0
		}
	}
	fmt.Fprintf(out, "gate-touched: the diff %s...%s; full (non-short) suites of %d package(s):\n  %s\n",
		shortSHA(base), shortSHA(head), len(pkgs), strings.Join(pkgs, "\n  "))
	args := []string{"test", "-timeout", opts.Timeout, "-parallel", opts.Parallel}
	args = append(args, pkgs...)
	fmt.Fprintf(out, "$ go %s\n", strings.Join(args, " "))
	cmd := exec.Command("go", args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(out, "gate-touched: go test could not run: %v\n", err)
		return 1
	}
	return 0
}

// shortSHA is a sha as a person reads it in a gate's output. A non-sha
// (HEAD, a ref name) is kept whole: the line is for reading, not parsing.
func shortSHA(sha string) string {
	if len(sha) > 12 && !strings.ContainsAny(sha[:12], "/:") {
		return sha[:12]
	}
	return sha
}

// validateLeaveToCI refuses a declaration that names no package of this
// module: a typo in the budget list would silently run the very suite it
// was meant to leave to CI, and a gate that quietly pays 40 minutes for a
// misspelled name is a budget nobody can read.
func validateLeaveToCI(leave []string, graph []Package) error {
	known := map[string]bool{}
	for _, pkg := range graph {
		known[pkg.ImportPath] = true
	}
	for _, name := range leave {
		if !known[name] {
			return fmt.Errorf("-leave-to-ci names %q, which is not a package of this module", name)
		}
	}
	return nil
}

// leaveToCI takes the declared packages out of the run, naming each one it
// leaves with the reason, and answers what is left to run.
func leaveToCI(pkgs []string, leave []string, out io.Writer) []string {
	skipped := map[string]bool{}
	for _, name := range leave {
		skipped[name] = true
	}
	var run []string
	for _, pkg := range pkgs {
		if !skipped[pkg] {
			run = append(run, pkg)
			continue
		}
		fmt.Fprintf(out, "gate-touched: %s is left to CI: this gate declares its full suite too expensive to pay "+
			"per tick, so the short suite and CI's are its cover\n", pkg)
	}
	return run
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
