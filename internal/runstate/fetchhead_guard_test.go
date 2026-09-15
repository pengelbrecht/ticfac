package runstate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoProductionCodeReadsFetchHead pins the fix for tick wdb.
//
// FETCH_HEAD is ONE FILE in .git, shared by every process using the checkout.
// Reading a ref through it means any concurrent `git fetch` — an operator
// watching the run, a second ticfac command, an editor — silently substitutes
// another branch's head. In the run-state store that emptied the run's view of
// its own state and ended the run; in the tracker it would have moved the
// worktree onto the wrong base and published there.
//
// It looks for the Go string literal, not the word: the fix's own comments name
// FETCH_HEAD to explain why it is banned.
//
// This is a grep, deliberately: the defect is a SHAPE, it already occurred
// twice in two packages, and a reviewer cannot be relied on to catch the third.
func TestNoProductionCodeReadsFetchHead(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), "\"FETCH_HEAD\"") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("production code reads FETCH_HEAD in %v.\n"+
			"FETCH_HEAD is process-global state in the checkout: a concurrent `git fetch` by anyone "+
			"replaces it between the fetch and the read. Fetch into a private per-run ref and resolve "+
			"that instead:\n"+
			"    git fetch --no-write-fetch-head <remote> +<branch>:refs/ticfac/peek/<run-id>\n"+
			"    git rev-parse refs/ticfac/peek/<run-id>\n"+
			"See tick wdb.", offenders)
	}
}

// repoRoot walks up from this package to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// TestEveryProductionFetchStaysOffSharedRefs extends the guard to the second
// shared ref, found only after the FETCH_HEAD fix shipped.
//
// Fetching a branch from a NAMED remote makes git also update
// refs/remotes/<remote>/<branch> opportunistically. An operator's
// `git fetch origin` in the same checkout updates the same ref, the two race on
// its lock, and the loser exits 1 — which ended the run in
// TestAnOperatorFetchingInTheRunsCheckoutEndsNothing on the code that had
// already stopped reading FETCH_HEAD. A fetch ticfac issues must update only
// the refspec on its own command line and write nothing else.
func TestEveryProductionFetchStaysOffSharedRefs(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, `"fetch"`) {
				continue
			}
			if !strings.Contains(line, `"--no-write-fetch-head"`) || !strings.Contains(line, `"--refmap="`) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("production fetches that can race another git process on shared refs: %v\n"+
			"Pass both flags on the same line as \"fetch\":\n"+
			"    \"fetch\", \"--no-write-fetch-head\", \"--refmap=\", <remote>, +<ref>:<private ref>\n"+
			"See tick wdb.", offenders)
	}
}
