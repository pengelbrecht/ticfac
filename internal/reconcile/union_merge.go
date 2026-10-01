package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// A changelog conflict is the union of both sides, made by git (the hn6
// follow-up to run_ee8e).
//
// Two ticks of one wave that each add an entry under the same CHANGELOG
// heading edit the same lines, and git calls that a content conflict. The
// run then dispatched a resolve-conflict job at the ceiling tier to do what
// is not a judgement at all: keep both entries. git's own `union` merge
// driver does exactly that, so every merge of an attempt INTO the
// integration branch — and the conflicted tree a resolve job is cut from —
// runs with the changelog files marked `merge=union`.
//
// The marking rides `core.attributesFile` on the merge's own command line,
// pointed at a file this reconciler writes outside the repository: the
// repository's .gitattributes is never edited, and a repository that states
// a merge driver for one of these paths itself still wins (git reads the
// tree's .gitattributes over core.attributesFile). The base fold (main into
// the epic) is not marked: it merges what the base has, as it always did.
//
// contracts/bundle.json is NOT given a mechanical path, on purpose. It is not
// generated: it is CUT by hand — a version a person bumps, a digests map, and
// an append-only version_digests ledger that refuses a re-cut at an unchanged
// version (internal/contracts/bundle.go) — and a version is pinned by exact
// value in cloudflare/contracts.pin.json. Two ticks that each re-cut it chose
// a version each; picking a third is a decision about the bundle's identity
// that this repository has no deterministic generator for, so that conflict
// stays the resolve job's.

// unionMergedNames are the basenames merged as a union, at any depth. Exact
// names, not prefixes: CHANGELOG_test.go is code.
var unionMergedNames = []string{
	"CHANGELOG", "CHANGELOG.md", "CHANGELOG.txt",
	"CHANGES", "CHANGES.md", "CHANGES.txt",
	"HISTORY", "HISTORY.md", "HISTORY.txt",
}

// unionMergeConfig is the `-c` pair that marks the changelog files
// `merge=union` for one git invocation, and the cleanup of the attributes
// file it points at.
func unionMergeConfig() ([]string, func(), error) {
	root, remove, err := tempdir.Make("ticfac-merge-attributes-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("prepare the merge's attributes: %w", err)
	}
	var lines strings.Builder
	for _, name := range unionMergedNames {
		lines.WriteString(name + " merge=union\n")
	}
	path := filepath.Join(root, "attributes")
	if err := os.WriteFile(path, []byte(lines.String()), 0o644); err != nil {
		remove()
		return nil, func() {}, fmt.Errorf("write the merge's attributes: %w", err)
	}
	return []string{"-c", "core.attributesFile=" + path}, remove, nil
}
