package runprogress

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The gap is the Phase 3 incident's question (tick 7zs): the run knew the
// worker was alive for all 55 minutes and had no way to know it had produced
// nothing durable for 40 of them. These tests hold the measurement to the
// honesty the tick demands — what it says is what it measures, and nothing it
// cannot measure is guessed at.

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// repoWithCommitAt builds a repository with one commit whose committer date is
// fixed, so a branch tip's "last moved" is an exact, assertable fact rather
// than a race with the test's own clock.
func repoWithCommitAt(t *testing.T, at time.Time) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "-b", "main")
	gitIn(t, dir, "config", "user.email", "gap@example.com")
	gitIn(t, dir, "config", "user.name", "gap test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	stamp := at.UTC().Format("2006-01-02T15:04:05Z")
	cmd := exec.Command("git", "commit", "--quiet", "-m", "base")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit (in %s): %v\n%s", dir, err, out)
	}
	return dir
}

// attemptWorktree registers a worktree on one attempt's branch against the
// repo — the shape both local executors leave behind — and returns its path.
func attemptWorktree(t *testing.T, repo, branch, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	gitIn(t, repo, "worktree", "add", "--quiet", dir, branch)
	return dir
}

func setMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// How old the fixture's facts are, made constants so the assertions are about
// the MEASUREMENT and not about clock arithmetic.
const (
	branchAge = 3 * time.Hour // how long ago the branch tip was committed
	fileAge   = 2 * time.Hour // how long ago the worktree's newest file was written
)

// aFixture is one repo with one attempt standing: branch tip committed
// branchAge before now, worktree's newest file written fileAge before now.
// The checked-out base file carries the commit's own age, as it does in a
// real worktree — it was written by whoever made the base commit.
func aFixture(t *testing.T, now time.Time) (repo, branch, worktree string) {
	t.Helper()
	repo = repoWithCommitAt(t, now.Add(-branchAge))
	branch = "refs/heads/ticfac/run-r1/tick-a1/attempt-1"
	gitIn(t, repo, "branch", "ticfac/run-r1/tick-a1/attempt-1")
	worktree = attemptWorktree(t, repo, "ticfac/run-r1/tick-a1/attempt-1", "wt-a1")
	if err := os.WriteFile(filepath.Join(worktree, "work-a1.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, filepath.Join(worktree, "work-a1.txt"), now.Add(-fileAge))
	setMtime(t, filepath.Join(worktree, "base.txt"), now.Add(-branchAge))
	return repo, branch, worktree
}

func TestParseAttempt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		ref     string
		run     string
		tick    string
		attempt int
		ok      bool
	}{
		{"refs/heads/ticfac/run-epic-x/tick-7zs/attempt-22", "epic-x", "7zs", 22, true},
		{"refs/heads/ticfac/run-epic-x/tick-a1/attempt-1", "epic-x", "a1", 1, true},
		// The job-protocol golden's own shape.
		{"refs/heads/ticfac/run-42/tick-t1/attempt-3", "42", "t1", 3, true},
		// Not this vocabulary at all.
		{"refs/heads/main", "", "", 0, false},
		{"refs/heads/ticfac/run-42/tick-t1", "", "", 0, false},
		{"refs/heads/ticfac/run-42/tick-t1/attempt-0", "", "", 0, false},
		{"refs/heads/ticfac/run-42/tick-t1/attempt-x", "", "", 0, false},
		{"refs/heads/ticfac/tick-t1/attempt-1", "", "", 0, false},
	} {
		run, tick, attempt, ok := ParseAttempt(c.ref)
		if ok != c.ok || run != c.run || tick != c.tick || attempt != c.attempt {
			t.Errorf("ParseAttempt(%q) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
				c.ref, run, tick, attempt, ok, c.run, c.tick, c.attempt, c.ok)
		}
	}
}

// Both facts are measured, and both are reported: the branch moved 3h ago,
// the worktree changed 2h ago, and the GAP — how long since the attempt last
// produced anything durable, whichever kind — is the newer fact, 2h.
func TestAttemptOfMeasuresBothFacts(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo, branch, _ := aFixture(t, now)

	got, ok, err := AttemptOf(repo, branch, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the attempt stands in the repo: its gap is measurable and the answer must say so")
	}
	if got.TickID != "a1" || got.Attempt != 1 {
		t.Errorf("the attempt reads as %s#%d, want a1#1", got.TickID, got.Attempt)
	}
	if got.BranchIdle == nil || time.Duration(*got.BranchIdle) != branchAge {
		t.Errorf("the branch gap is %+v, want %s: the tip was committed exactly then", got.BranchIdle, branchAge)
	}
	if got.WorktreeIdle == nil || time.Duration(*got.WorktreeIdle) != fileAge {
		t.Errorf("the worktree gap is %+v, want %s: the newest file was written exactly then", got.WorktreeIdle, fileAge)
	}
	idle, ok := got.Idle()
	if !ok || idle != fileAge {
		t.Errorf("the gap is (%s, %v), want (%s, true): the most recent durable thing the attempt did was write a file", idle, ok, fileAge)
	}
}

// A branch tip's age is NOT the attempt's gap: before the attempt's first
// commit the tip is the BASE commit, somebody else's work, and a reader told
// "nothing durable for three hours" about an attempt twenty minutes old is
// sent at a number that measures the wrong thing. The branch number is still
// reported; it is just not claimed as the attempt's gap without the worktree
// to bound it.
func TestTheGapNeedsTheWorktreeFact(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo, branch, worktree := aFixture(t, now)

	// The teardown shape: the registration remains, the directory is gone.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	got, ok, err := AttemptOf(repo, branch, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the attempt's branch still stands and is measurable")
	}
	if got.WorktreeIdle != nil {
		t.Errorf("a removed worktree still reports a change age: %+v", got.WorktreeIdle)
	}
	if idle, ok := got.Idle(); ok {
		t.Errorf("a branch-only measurement claims a gap of %s: the tip is the base commit, not this attempt's work", idle)
	}
}

// The measurement reads the files, not the checkout's plumbing: a newer file
// under .git is bookkeeping, not work, and the git dir of a linked worktree
// is not inside the worktree at all — but the walk must skip the name
// wherever it finds it, so the git plumbing's mtime never reads as progress.
func TestTheWalkSkipsGit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, filepath.Join(dir, "work.txt"), now.Add(-fileAge))
	if err := os.WriteFile(filepath.Join(dir, ".git", "objects", "pack"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, filepath.Join(dir, ".git", "objects", "pack"), now)

	at, ok := worktreeChangedAt(dir)
	if !ok || now.Sub(at) < fileAge {
		t.Fatalf("the newest file counted is under .git: the gap measured %s, want the work file at %s before now",
			now.Sub(at), fileAge)
	}
}

// Standing enumerates exactly one run's attempts from the registrations git
// keeps — the shape both local executors leave — and nothing else: another
// run's attempt and the checkout itself are not this run's business.
func TestStandingListsOnlyOneRun(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo, _, _ := aFixture(t, now)

	gitIn(t, repo, "branch", "ticfac/run-r2/tick-b1/attempt-1")
	other := attemptWorktree(t, repo, "ticfac/run-r2/tick-b1/attempt-1", "wt-b1")

	got, err := Standing(repo, "r1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Standing found %d attempts for run r1, want 1: %+v", len(got), got)
	}
	if got[0].TickID != "a1" || got[0].Attempt != 1 {
		t.Errorf("the one attempt reads as %s#%d, want a1#1", got[0].TickID, got[0].Attempt)
	}
	if got[0].Worktree == "" || got[0].Worktree == other {
		t.Errorf("the attempt's worktree is %q, want the r1 worktree, not %q", got[0].Worktree, other)
	}
	if idle, ok := got[0].Idle(); !ok || idle != fileAge {
		t.Errorf("the standing attempt's gap is (%s, %v), want (%s, true)", idle, ok, fileAge)
	}

	otherRuns, err := Standing(repo, "r2", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherRuns) != 1 || otherRuns[0].TickID != "b1" {
		t.Fatalf("Standing for r2 found %+v, want exactly b1#1", otherRuns)
	}
}

// The run id in the ref and the run id asked for must agree as a whole: a
// worktree of run r11 does not answer a question about run r1 just because
// one is a prefix of the other.
func TestStandingRequiresTheWholeRunID(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo, _, _ := aFixture(t, now)

	got, err := Standing(repo, "r11", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Standing found %+v for run r11, want none: r1 is a different run", got)
	}
}
