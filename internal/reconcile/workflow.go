package reconcile

import (
	"fmt"
	"regexp"
	"strings"
)

// The workflow half of the integrated gate (tick cwa).
//
// The declared commands prove the TREE: `go test ./...` compiles only what
// exists, so it cannot fail over a path that does not — and a CI workflow
// naming a package an epic deleted keeps the repository's own PRs red on every
// push and pull_request while every declared gate command stays green. That is
// how an epic's close-out rule ("CI green on the PR") became unsatisfiable
// behind FIVE passing integrated gates (the pwp run, 2026-09-15).
//
// The general shape: the gate proves the tree and says nothing about whether
// the repo's own CI configuration still DESCRIBES that tree. Any config
// naming paths — a workflow, a codeowners file, a lint include list, a
// release manifest — can rot invisibly behind a green gate.
//
// The options weighed for closing it, and why the others were rejected:
//
//   - Parse the workflow files for package patterns and assert each resolves
//     (CHOSEN). Cheap, no new dependency (this module is stdlib-only), and it
//     refuses BEFORE the close with the file and the path named, so the repair
//     starts at the workflow instead of at a CI log a person reads.
//
//   - Run the repo's workflow steps directly (act, or a parsed runner).
//     Rejected: it duplicates the repo's CI inside the gate — minutes per
//     gate, per tick — needs a third-party runner the stdlib-first rule
//     forbids, and is environment-dependent (a workflow may need the forger's
//     own context to run at all).
//
//   - Declare it out of scope and have the close-out check CI status on the
//     PR instead. Rejected: a red PR is exactly the state the incident left
//     the run in, so the close-out rule never fires and the run can neither
//     complete nor name its repair — it is a human gate, and the human starts
//     from a CI log with no tick and no workflow named. The close-out's CI
//     check stays as the outer ring it already is; this check is the earlier,
//     mechanical one that can say what rotted.
//
// The check is deliberately narrow, because a gate that refuses wrongly blocks
// the close of every tick in the repository:
//
//   - only `./x/...` package WILDCARDS are asserted. A wildcard is always an
//     input reference — go's `...` matches packages that must exist — while a
//     bare `./x` can be a build OUTPUT (`go build -o ./bin/tool ./cmd/tool`)
//     and refusing those would refuse honest workflows. The incident's shape
//     is a wildcard.
//
//   - only `.github/workflows/*.{yml,yaml}` are read: the repository's own CI
//     configuration. CODEOWNERS, lint includes and release manifests rot too,
//     and each of those is a check of its own, not a widening of this one.
//
//   - comments are excluded: `#` starts a comment in YAML and in the shell
//     both, and CI does not fail over one.
//
// The check reads the tree through git plumbing only — no worktree, no host
// paths — and re-runs on every gate because it costs nothing and cannot go
// stale.

// workflowDir is where a repository's own CI configuration lives.
const workflowDir = ".github/workflows/"

// workflowPackagePattern matches a `./x/...` package wildcard: `./`, at least
// one path character, then `/...`. The root wildcard `./...` deliberately does
// not match — it names the whole tree, and resolves as long as the tree has
// a root. Module paths (`github.com/x/y/...`) do not start with `./` and are
// not this repository's paths to assert.
var workflowPackagePattern = regexp.MustCompile(`\./[A-Za-z0-9_./-]+/\.\.\.`)

// workflowPackagePatterns reads one workflow document and returns each
// `./x/...` package wildcard it names outside comments, deduplicated, in
// first-seen order.
func workflowPackagePatterns(document string) []string {
	seen := map[string]bool{}
	var patterns []string
	for _, line := range strings.Split(document, "\n") {
		for _, pattern := range workflowPackagePattern.FindAllString(stripWorkflowComment(line), -1) {
			if seen[pattern] {
				continue
			}
			seen[pattern] = true
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

// stripWorkflowComment removes a `#` comment from one workflow line: `#` starts
// a comment in YAML and in the shell both, and a workflow's commands are
// written in each. A `#` inside a quoted string is not a comment, so both
// quote kinds are tracked.
func stripWorkflowComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(line) {
				i++ // skip the escaped character, it cannot close the string
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// staleWorkflowPatterns reads every workflow file in the gated tree and
// returns, as sentences a refusal can carry, the package wildcards they name
// that the tree cannot resolve. The empty set is a repository whose CI
// configuration still describes its tree.
func (r *Reconciler) staleWorkflowPatterns(gateSHA string) ([]string, error) {
	out, err := r.git.run("", "ls-tree", "-r", "--name-only", gateSHA)
	if err != nil {
		return nil, fmt.Errorf("read the tree of %s for workflow files: %w", short(gateSHA), err)
	}
	var stale []string
	for _, file := range strings.Split(out, "\n") {
		if !isWorkflowFile(file) {
			continue
		}
		document, err := r.git.run("", "show", gateSHA+":"+file)
		if err != nil {
			return nil, fmt.Errorf("read the gated tree's %s: %w", file, err)
		}
		for _, pattern := range workflowPackagePatterns(document) {
			// The wildcard's base, as a tree path: `./internal/gather/...`
			// asserts the directory `internal/gather`.
			dir := strings.TrimSuffix(pattern, "...")
			dir = strings.TrimSuffix(dir, "/")
			dir = strings.TrimPrefix(dir, "./")
			if dir == "" {
				continue
			}
			resolved, err := r.git.run("", "ls-tree", gateSHA, "--", dir)
			if err != nil {
				return nil, fmt.Errorf("read %s in the tree of %s: %w", dir, short(gateSHA), err)
			}
			kind := ""
			if fields := strings.Fields(resolved); len(fields) >= 2 {
				kind = fields[1]
			}
			switch kind {
			case "tree": // the package directory is there; CI can match it
			case "blob":
				stale = append(stale, fmt.Sprintf(
					"%s names %s, but %s in the gated tree is a file, not a package directory",
					file, pattern, dir))
			default:
				stale = append(stale, fmt.Sprintf(
					"%s names %s, but the gated tree carries no %s directory",
					file, pattern, dir))
			}
		}
	}
	return stale, nil
}

// isWorkflowFile reports whether a tree path is one of the repository's own CI
// workflow files — the one config this check reads (see the options weighed
// above for why nothing else is in scope).
func isWorkflowFile(path string) bool {
	if !strings.HasPrefix(path, workflowDir) {
		return false
	}
	name := strings.TrimPrefix(path, workflowDir)
	if strings.Contains(name, "/") {
		return false
	}
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}
