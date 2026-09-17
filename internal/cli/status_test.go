package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// `ticfac status` reports, for each in-flight attempt, how long since its
// branch last moved and its worktree last changed (tick 7zs) — the Phase 3
// run was alive and true for 55 minutes while its worker produced nothing
// for 40 of them, and a person caught it by reading the pane.

// statusFixture is a repo with one standing attempt: branch tip committed
// three hours ago, worktree's newest file written two hours ago. The run
// named by the branch is whatever claims it — these tests claim the test
// process, which is alive.
func statusFixture(t *testing.T, now time.Time) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_DATE="+now.Add(-3*time.Hour).UTC().Format(time.RFC3339),
			"GIT_COMMITTER_DATE="+now.Add(-3*time.Hour).UTC().Format(time.RFC3339))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "status@example.com")
	git("config", "user.name", "status test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")
	git("branch", "ticfac/run-r-status/tick-a1/attempt-1")
	worktree := filepath.Join(t.TempDir(), "wt-a1")
	cmd := exec.Command("git", "worktree", "add", "--quiet", worktree, "ticfac/run-r-status/tick-a1/attempt-1")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktree, "work-a1.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The checked-out base file carries the commit's age, as it does in a
	// real worktree — it was written by whoever made the base commit.
	if err := os.Chtimes(filepath.Join(worktree, "base.txt"), now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(worktree, "work-a1.txt"), now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return repo
}

// Both gaps are on the text surface, both on the JSON surface, and they are
// the same numbers: a watcher reading either surface sees what was measured,
// not two stories about it.
func TestStatusReportsTheGapOfEveryInFlightAttempt(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())
	life, err := runlife.Claim(repo, "r-status")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var out bytes.Buffer
	if code := statusCommand([]string{"--repo", repo, "r-status"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "attempt 1 of a1") {
		t.Errorf("the in-flight attempt is not named:\n%s", text)
	}
	if !strings.Contains(text, "last moved 3h0m") {
		t.Errorf("the branch gap is not the measured one:\n%s", text)
	}
	if !strings.Contains(text, "last changed 2h0m") {
		t.Errorf("the worktree gap is not the measured one:\n%s", text)
	}

	out.Reset()
	if code := statusCommand([]string{"--repo", repo, "--json", "r-status"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("a live run exited %d in --json: %s", code, out.String())
	}
	var status runlife.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("the JSON status does not decode: %v\n%s", err, out.String())
	}
	if len(status.Attempts) != 1 {
		t.Fatalf("the JSON reports %d in-flight attempts, want 1:\n%s", len(status.Attempts), out.String())
	}
	a := status.Attempts[0]
	if a.TickID != "a1" || a.Attempt != 1 {
		t.Errorf("the attempt reads as %s#%d, want a1#1", a.TickID, a.Attempt)
	}
	if a.BranchIdle == nil || !strings.Contains(a.BranchIdle.String(), "3h0m") {
		t.Errorf("the JSON branch gap is %+v, want ~3h", a.BranchIdle)
	}
	if a.WorktreeIdle == nil || !strings.Contains(a.WorktreeIdle.String(), "2h0m") {
		t.Errorf("the JSON worktree gap is %+v, want ~2h", a.WorktreeIdle)
	}
}

// The gap never changes the verdict: the exit code stays liveness's answer
// alone, so a watcher looping over it behaves exactly as before — a dead run
// with a stalled attempt still exits 1, and its gaps are still reported,
// which is precisely when a person needs them.
func TestTheGapChangesNoVerdict(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())

	var out bytes.Buffer
	code := statusCommand([]string{"--repo", repo, "r-status"}, &out, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("a dead run exited %d: the gap must not change the verdict, only report beside it", code)
	}
	if !strings.Contains(out.String(), "not_running") {
		t.Errorf("the liveness answer is not the headline:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "attempt 1 of a1") {
		t.Errorf("a dead run's standing attempt is not reported — the leftover worktree is exactly the case the gaps exist for:\n%s", out.String())
	}
}
