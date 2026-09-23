package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge_failed refusal names the files that did not merge and the kind of
// conflict each one is (tick ky5).
//
// The refusal used to end at a colon: git prints its CONFLICT lines on
// stdout, the reconciler read stderr, and stderr is empty on a plain conflict.
// These tests drive REAL conflicts through the same git the reconciler runs —
// its identity, its rerere switch, its environment — because the defect was
// precisely a disagreement between what this code assumed git prints and
// where git actually prints it. A table of made-up merge output would have
// passed the broken version too.

// mergeConflictRepo builds a repository whose `side` branch conflicts with
// `main` in the ways the table asks for, and returns it and the reconciler's
// git pointed at it.
func mergeConflictRepo(t *testing.T, onBase map[string]string, onMain, onSide map[string]string, deleteOnSide []string) (string, *repoGit) {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--quiet", "-b", "main")
	configure(t, dir)
	commitFiles := func(files map[string]string, remove []string, message string) {
		for name, content := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, name), content)
		}
		for _, name := range remove {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		mustRun(t, dir, "git", "add", "--all")
		mustRun(t, dir, "git", "commit", "--quiet", "--allow-empty", "-m", message)
	}
	commitFiles(onBase, nil, "base")
	mustRun(t, dir, "git", "branch", "side")
	commitFiles(onMain, nil, "main's side")
	mustRun(t, dir, "git", "checkout", "--quiet", "side")
	commitFiles(onSide, deleteOnSide, "the attempt's side")
	mustRun(t, dir, "git", "checkout", "--quiet", "main")
	return dir, &repoGit{dir: dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
}

// mergeDetail merges `side` into `main` the way mergeInWorktree does and
// returns the detail its refusal would carry.
func mergeDetail(t *testing.T, dir string, g *repoGit) string {
	t.Helper()
	stdout, stderr, err := g.try(dir, "merge", "--no-ff", "--no-edit", "-m", "merge", "side")
	if err == nil {
		t.Fatalf("the fixture's branches merged cleanly; there is no conflict to describe")
	}
	unmerged, _ := g.run(dir, "diff", "--name-only", "--diff-filter=U")
	_, _, _ = g.try(dir, "merge", "--abort")
	return describeMergeFailure(stdout, stderr, unmerged, err)
}

// Two ticks of one wave that each CREATE the same file: the add/add conflict
// that stopped epic-wne twice, and the one whose kind is a planning verdict.
func TestAnAddAddConflictIsNamedWithItsPathAndKind(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"README.md": "base\n"},
		map[string]string{"internal/runconfig/worktype.go": "package runconfig // main\n"},
		map[string]string{"internal/runconfig/worktype.go": "package runconfig // side\n"},
		nil)

	detail := mergeDetail(t, dir, g)
	if strings.TrimSpace(detail) == "" {
		t.Fatal("a real add/add conflict produced an empty merge_failed detail")
	}
	if !strings.Contains(detail, "add/add in internal/runconfig/worktype.go") {
		t.Errorf("the detail does not name the add/add conflict and its path: %q", detail)
	}
	if !strings.Contains(detail, "planning defect") {
		t.Errorf("the detail does not say what an add/add conflict means: %q", detail)
	}
	if strings.Contains(detail, "content in") {
		t.Errorf("an add/add conflict is reported as a content conflict: %q", detail)
	}
}

// Both sides edit a file that already existed: a content conflict, and it has
// to read as one — distinguishable from add/add without reproducing the merge.
func TestAContentConflictIsDistinguishableFromAnAddAddConflict(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"shared.txt": "base\n"},
		map[string]string{"shared.txt": "main\n"},
		map[string]string{"shared.txt": "side\n"},
		nil)

	detail := mergeDetail(t, dir, g)
	if !strings.Contains(detail, "content in shared.txt") {
		t.Errorf("the detail does not name the content conflict and its path: %q", detail)
	}
	if strings.Contains(detail, "add/add") || strings.Contains(detail, "planning defect") {
		t.Errorf("a content conflict is described as an add/add one: %q", detail)
	}
}

// A kind whose git line is not "Merge conflict in <path>" keeps git's own
// wording, and every conflict in one merge is named — not the first alone.
func TestEveryConflictOfOneMergeIsNamedWithGitsOwnWording(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"shared.txt": "base\n", "doomed.txt": "base\n"},
		map[string]string{"shared.txt": "main\n", "doomed.txt": "main\n", "new.txt": "main\n"},
		map[string]string{"shared.txt": "side\n", "new.txt": "side\n"},
		[]string{"doomed.txt"})

	detail := mergeDetail(t, dir, g)
	for _, want := range []string{
		"3 conflicts",
		"add/add in new.txt",
		"content in shared.txt",
		"modify/delete: doomed.txt deleted in",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not carry %q: %q", want, detail)
		}
	}
}

// Output this parse does not recognise still names its files, from the index,
// and an empty merge output never becomes an empty reason.
func TestAMergeFailureWithoutConflictLinesIsNeverEmpty(t *testing.T) {
	t.Parallel()
	if got := describeMergeFailure("", "", "a.go\nb.go", nil); !strings.Contains(got, "unmerged a.go") ||
		!strings.Contains(got, "unmerged b.go") {
		t.Errorf("the index's unmerged paths are not named: %q", got)
	}
	if got := describeMergeFailure("", "fatal: refusing to merge unrelated histories", "", nil); !strings.Contains(got, "unrelated histories") {
		t.Errorf("git's own message is not carried: %q", got)
	}
	if got := describeMergeFailure("", "", "", nil); strings.TrimSpace(got) == "" {
		t.Error("a merge that printed nothing produced an empty detail")
	}
}
