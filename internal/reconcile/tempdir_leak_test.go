package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// Tick w9j: the host's temp directory held ~1,370 ticfac-* dirs, tracker,
// merge and gate trees among them, some still registered as worktrees of the
// checkout they came from. These tests run those paths — a run that closes, a
// run whose gate refuses, a gate that finds every slot held, and the signal
// handler's exit — each against a temp directory of its own, and ask the two
// questions a leak answers wrong: is anything named ticfac-* still there, and
// is `git worktree list` back to the length it started at.

// isolateTemp points os.TempDir() at a directory only this test uses.
func isolateTemp(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertNoTicfacTemp(t *testing.T, tmp, after string) {
	t.Helper()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempdir.Prefix) {
			t.Errorf("%s left %s in the temp directory", after, e.Name())
		}
	}
}

// worktreeCount is the length of the worktree list, less the gate's slots:
// those are kept on purpose (gatedir.go), bounded per repository, and are not
// temp trees.
func worktreeCount(t *testing.T, dir string) int {
	t.Helper()
	return len(registeredWorktrees(t, dir)) + 1
}

// serial: it points TMPDIR (process-wide) at a directory of its own, which
// t.Setenv refuses in a parallel test — and which is the only way to ask "did
// this run leave anything" of a temp directory other tests are writing into.
func TestARunLeavesNoTempTreesBehind(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, tc := range []struct {
		name  string
		gate  string
		state runstate.State
	}{
		{"a run that closes every tick", passingGate, runstate.StateCompleted},
		{"a run whose gate refuses", failingGate, runstate.StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := isolateTemp(t)
			f := newFixture(t, fixtureOptions{gate: tc.gate})
			before := worktreeCount(t, f.Repo.Dir)

			_, result, err := f.run(f.Repo, fixtureOptions{gate: tc.gate})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != tc.state {
				t.Fatalf("the run ended %s, want %s: %s", result.State, tc.state, result.Reason)
			}
			f.stopEverything()
			assertNoTicfacTemp(t, tmp, tc.name)
			if after := worktreeCount(t, f.Repo.Dir); after != before {
				t.Errorf("git worktree list went from %d to %d:\n%s", before, after,
					mustRun(t, f.Repo.Dir, "git", "worktree", "list"))
			}
		})
	}
}

// serial: TMPDIR, as above, and tempdir.ReleaseAll is process-wide — it would
// remove a tree a parallel test still has open.
func TestTempTreesGoOnEveryPathIncludingTheSignalExit(t *testing.T) {
	tmp := isolateTemp(t)
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	before := worktreeCount(t, repo.Dir)

	// A merge that conflicts and one that does not: the error return and the
	// success return of mergeInWorktree both go through its remove.
	r := &Reconciler{git: g, branch: "main", runID: "r-w9j"}
	head := commitOnBranch(t, repo.Dir, "side", "README.md", "side\n")
	if _, conflict, err := r.mergeInWorktree("a1", 1, "side", head, repo.Base); err != nil || conflict != nil {
		t.Fatalf("a clean merge: conflict %v, err %v", conflict, err)
	}
	other := commitOnBranch(t, repo.Dir, "other", "README.md", "other\n")
	if _, conflict, _ := r.mergeInWorktree("a1", 2, "other", other, head); conflict == nil {
		t.Fatal("the fixture's second merge was meant to conflict")
	}
	assertNoTicfacTemp(t, tmp, "a merge")

	// A gate that finds every slot held runs in a throwaway tree, with its
	// output in a scratch directory of its own.
	var releases []func()
	for i := 0; i < gateSlots; i++ {
		_, _, release, err := g.gateWorktree("ticfac-gate-", repo.Base)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	dir, lock, remove, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil || !strings.HasPrefix(dir, tmp) {
		t.Fatalf("with every slot held the gate was handed %s, not a throwaway tree", dir)
	}
	shell, err := startShell(dir, "true", gateWaitDelay, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for !shell.settled() {
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, code, err := shell.wait(); code != 0 || err != nil {
		t.Fatalf("the gate command: code %d, err %v", code, err)
	}
	remove()
	for _, release := range releases {
		release()
	}
	assertNoTicfacTemp(t, tmp, "a gate on a throwaway tree")

	// The signal handler leaves by os.Exit, which runs no defer: whatever it
	// had open goes through tempdir.ReleaseAll instead — the tracker's tree,
	// a merge in flight, an index being built. The tracker's tree is never
	// closed here: its close runs a prune, which would tidy up after a
	// ReleaseAll that had removed only the directories.
	if _, err := openTrackerTree(g, "origin", "main", "r-w9j"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.tempWorktree("ticfac-merge-", repo.Base); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tempIndex(); err != nil {
		t.Fatal(err)
	}
	if worktreeCount(t, repo.Dir) != before+2 {
		t.Fatalf("the fixture did not open two worktrees:\n%s", mustRun(t, repo.Dir, "git", "worktree", "list"))
	}
	tempdir.ReleaseAll()
	assertNoTicfacTemp(t, tmp, "the signal exit")
	if after := worktreeCount(t, repo.Dir); after != before {
		t.Errorf("git worktree list went from %d to %d:\n%s", before, after,
			mustRun(t, repo.Dir, "git", "worktree", "list"))
	}
}

// commitOnBranch commits one file on a new branch cut from main and answers
// with the commit.
func commitOnBranch(t *testing.T, dir, branch, path, content string) string {
	t.Helper()
	mustRun(t, dir, "git", "checkout", "--quiet", "-b", branch, "main")
	write(t, filepath.Join(dir, path), content)
	mustRun(t, dir, "git", "commit", "--quiet", "-am", branch)
	head := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, dir, "git", "checkout", "--quiet", "main")
	return head
}
