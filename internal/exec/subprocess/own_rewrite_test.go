package subprocess

import (
	"os"
	"path/filepath"
	"testing"
)

// epic-2jn, rix attempt 45 (2026-09-27): the worker committed, its
// supervisor's timer pushed the commit, and two minutes later the worker ran
// `git commit --amend`. Every later timed push was a non-fast-forward and
// failed quietly ("the next tick tries again", forever), so origin kept the
// commit the worker had rewritten away and the run's collect refused the
// worker's real head as somebody else's work.
//
// A worker rewriting its OWN pushed history is ordinary agent behaviour. The
// attempt branch has two writers, the worker and its supervisor, so origin's
// head being a state the local branch itself held (its reflog says so) is
// proof the rewrite is the attempt's own, and the push replaces it under a
// lease. Anything origin holds that the branch never held stays refused.

// ownRewriteFixture is an attempt worktree on its own branch, with one commit
// the supervisor's push has already put on origin.
func ownRewriteFixture(t *testing.T, branch string) (repo *testRepo, worktree, pushed string) {
	t.Helper()
	repo = newRepo(t, "own-rewrite")
	worktree = filepath.Join(repo.Root, "attempt")
	runGit(t, repo.Dir, "worktree", "add", "--quiet", "-b", branch, worktree, repo.Base)
	writeAndCommit(t, worktree, "work.txt", "the first draft\n", "the worker's commit")
	if err := pushBranch(worktree, "origin", branch); err != nil {
		t.Fatalf("the first push of %s failed: %v", branch, err)
	}
	return repo, worktree, runGit(t, worktree, "rev-parse", "HEAD")
}

func writeAndCommit(t *testing.T, dir, file, body, subject string, extra ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, append([]string{"commit", "--quiet", "-m", subject}, extra...)...)
}

func originHead(t *testing.T, repo *testRepo, branch string) string {
	t.Helper()
	out := runGit(t, repo.Dir, "ls-remote", repo.Origin, "refs/heads/"+branch)
	if len(out) < 40 {
		return ""
	}
	return out[:40]
}

func TestTheTimedPushFollowsAWorkerThatAmendedWhatItAlreadyPushed(t *testing.T) {
	const branch = "ticfac/run-epic-2jn/tick-rix/attempt-45"
	repo, worktree, pushed := ownRewriteFixture(t, branch)

	// The amend: same parent, one line changed, a new commit.
	writeAndCommit(t, worktree, "work.txt", "the first draft, amended\n", "the worker's commit", "--amend")
	amended := runGit(t, worktree, "rev-parse", "HEAD")
	if amended == pushed {
		t.Fatal("the amend did not make a new commit")
	}

	if err := pushBranch(worktree, "origin", branch); err != nil {
		t.Fatalf("the timed push after the worker amended its own pushed commit failed: %v", err)
	}
	if got := originHead(t, repo, branch); got != amended {
		t.Fatalf("origin holds %s, want the amended head %s", short(got), short(amended))
	}
}

func TestTheTimedPushStillRefusesAnOriginHeadTheBranchNeverHeld(t *testing.T) {
	const branch = "ticfac/run-x/tick-a/attempt-1"
	repo, worktree, _ := ownRewriteFixture(t, branch)

	// Somebody else moves the attempt's ref on origin, from another clone:
	// a state this branch never held.
	foreign := pushDurableWork(t, repo, "elsewhere", "foreign.txt", "not this attempt's\n")
	runGit(t, repo.Origin, "update-ref", "refs/heads/"+branch, foreign)

	writeAndCommit(t, worktree, "work.txt", "the first draft, amended\n", "the worker's commit", "--amend")
	if err := pushBranch(worktree, "origin", branch); err == nil {
		t.Fatal("the timed push overwrote an origin head the attempt's branch never held")
	}
	if got := originHead(t, repo, branch); got != foreign {
		t.Fatalf("origin moved to %s; the refusal wrote something", short(got))
	}
}
