package runlife

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// startSleeper starts a real process and records it as the run's driver, the
// way Claim would have from inside it.
func startSleeper(t *testing.T, repo, runID string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a process to observe: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	start, ok := processStart(cmd.Process.Pid)
	if !ok {
		t.Skip("ps cannot report a start time on this host")
	}
	dir := Dir(repo, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(dir, Record{SchemaVersion: SchemaVersion, RunID: runID, PID: cmd.Process.Pid,
		ProcessStart: start, StartedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// Acceptance for tick udp: a run process that is killed reads dead, without
// anyone having to notice. SIGKILL is the case that matters — the process gets
// no chance to release, which is exactly the shape of the pwp deaths.
func TestAKilledRunReadsDead(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	cmd := startSleeper(t, repo, "r-killed")

	if got := Probe(repo, "r-killed", time.Now()); got.State != Alive {
		t.Fatalf("a running process reads %s (%s), want alive", got.State, got.Reason)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()

	got := Probe(repo, "r-killed", time.Now())
	if got.State != Dead {
		t.Fatalf("a killed process reads %s (%s), want dead", got.State, got.Reason)
	}
	if !strings.Contains(got.Reason, LogName) {
		t.Errorf("a death should point at the run log, where its last words are: %q", got.Reason)
	}
}

// Acceptance for tick udp: a pid the OS has reused must not read as the run.
// A pidfile that recorded only the pid would answer "alive" for any process
// that happened to be given the same number.
func TestARecycledPidDoesNotReadAlive(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	dir := Dir(repo, "r-recycled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// This test process is certainly alive, under a start time that is not this.
	if err := writeRecord(dir, Record{SchemaVersion: SchemaVersion, RunID: "r-recycled", PID: os.Getpid(),
		ProcessStart: "Thu Jan  1 00:00:00 1970", StartedAt: "1970-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	got := Probe(repo, "r-recycled", time.Now())
	if got.State != Dead {
		t.Fatalf("a live pid with a different start time reads %s (%s), want dead", got.State, got.Reason)
	}
	if !strings.Contains(got.Reason, "different process") {
		t.Errorf("the reason should say the pid was reused: %q", got.Reason)
	}
}

func TestReleaseLeavesNothingToMistakeForARun(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	life, err := Claim(repo, "r-clean")
	if err != nil {
		t.Fatal(err)
	}
	if got := Probe(repo, "r-clean", time.Now()); got.State != Alive {
		t.Fatalf("a claimed run reads %s (%s), want alive", got.State, got.Reason)
	}
	life.Release("completed")
	if got := Probe(repo, "r-clean", time.Now()); got.State != NotRunning {
		t.Fatalf("a released run reads %s (%s), want not_running", got.State, got.Reason)
	}
	log, err := os.ReadFile(filepath.Join(Dir(repo, "r-clean"), LogName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "started as pid") || !strings.Contains(string(log), "ended: completed") {
		t.Errorf("the run log should say when the run started and how it ended:\n%s", log)
	}
}

// A second run-epic for a run that already has a live driver is a second
// reconciler. It is refused at the door rather than left to the run-state CAS.
func TestASecondProcessIsRefusedWhileTheFirstLives(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	startSleeper(t, repo, "r-twice")

	_, err := Claim(repo, "r-twice")
	if !errors.Is(err, ErrAlreadyLive) {
		t.Fatalf("claiming a run with a live driver: %v, want ErrAlreadyLive", err)
	}
}

// A driver that died without releasing must not lock the run forever: the
// next run-epic resumes it.
func TestADeadDriverDoesNotBlockTheResume(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	cmd := startSleeper(t, repo, "r-resume")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	life, err := Claim(repo, "r-resume")
	if err != nil {
		t.Fatalf("resuming after the driver died: %v", err)
	}
	defer life.Release("test")
	if got := Probe(repo, "r-resume", time.Now()); got.State != Alive {
		t.Fatalf("the resumed run reads %s (%s)", got.State, got.Reason)
	}
}

// TestLivenessDoesNotDependOnTheEnvironment is the Phase 3 review's finding 3.
//
// `ps -o lstart=` renders a human date: the same process prints 14:19:19 in a
// local shell and 12:19:19 under TZ=UTC, and a different month name under
// another locale. Comparing that text across environments made a live run read
// dead — and a dead-looking run is one a second run-epic claims.
func TestLivenessDoesNotDependOnTheEnvironment(t *testing.T) {
	// serial: t.Setenv cannot be used with t.Parallel.
	repo := t.TempDir()
	cmd := startSleeper(t, repo, "r-tz")

	if got := Probe(repo, "r-tz", time.Now()); got.State != Alive {
		t.Fatalf("baseline reads %s (%s)", got.State, got.Reason)
	}

	// The probe under a different timezone and locale must reach the same
	// answer about the same process.
	t.Setenv("TZ", "Asia/Tokyo")
	t.Setenv("LC_ALL", "C")
	if got := Probe(repo, "r-tz", time.Now()); got.State != Alive {
		t.Errorf("under TZ=Asia/Tokyo the same live process reads %s (%s)", got.State, got.Reason)
	}
	t.Setenv("TZ", "UTC")
	if got := Probe(repo, "r-tz", time.Now()); got.State != Alive {
		t.Errorf("under TZ=UTC the same live process reads %s (%s)", got.State, got.Reason)
	}

	// And a claim must still be refused while it is alive, in any environment.
	if _, err := Claim(repo, "r-tz"); !errors.Is(err, ErrAlreadyLive) {
		t.Errorf("under TZ=UTC a second claim on a live run returned %v, want ErrAlreadyLive", err)
	}
	_ = cmd
}

// Probe answers the question liveness never did (tick 7zs): for every
// attempt whose worktree still stands in the repo, how long since its branch
// last moved and its worktree last changed. The gaps are a measurement beside
// the liveness answer — never part of it — and a repo the census cannot read
// leaves them null rather than guessed at.
func TestProbeReportsTheStandingAttemptsGaps(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo := t.TempDir()
	startSleeper(t, repo, "r-gaps")

	// A repository the census CAN read, with no attempt standing in it.
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "runlife@example.com")
	git("config", "user.name", "runlife test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")

	// No attempt stands: an empty census, which is a claim ("nothing is in
	// flight here"), not a failure to look.
	status := Probe(repo, "r-gaps", now)
	if status.State != Alive {
		t.Fatalf("a live process reads %s (%s)", status.State, status.Reason)
	}
	if status.Attempts == nil || len(status.Attempts) != 0 {
		t.Fatalf("a run with no standing attempts reports %+v, want an empty census", status.Attempts)
	}
}

// A repo the census cannot read — not a repository at all — leaves the gaps
// null and the liveness answer untouched: the measurement is a hint, and a
// hint that cannot be made must not turn into either a guess or a failure.
func TestProbeLeavesTheGapsNullWhenTheCensusCannotBeRead(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	startSleeper(t, repo, "r-norepo")

	status := Probe(filepath.Join(repo, "not-a-repo"), "r-norepo", time.Now())
	if status.Attempts != nil {
		t.Fatalf("a census that could not be read reported %+v, want null", status.Attempts)
	}
}

// wallRepo is a repository with ONE standing attempt — the in-flight shape —
// whose run feed already carries lines beside the liveness facts: the fixture
// for the wall clock's firing reaching `ticfac status` (tick q1e).
func wallRepo(t *testing.T, runID string, now time.Time) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "runlife@example.com")
	git("config", "user.name", "runlife test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")
	branch := "ticfac/run-" + runID + "/tick-a1/attempt-1"
	git("branch", branch)
	worktree := filepath.Join(t.TempDir(), "wt-a1")
	cmd := exec.Command("git", "worktree", "add", "--quiet", worktree, branch)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return repo
}

// wallDetail is the firing line's own detail — recognisable so a test can tell
// the line it asked for from a prose mention of the words "wall clock".
const wallDetail = "the wall clock of 3600s fired 4m0s ago and attempt 1 of a1 has not settled: " +
	"the executor is stopping it — the interrupt was delivered but the agent has not exited"

// appendWallLine writes the run's own wall-clock line for one attempt: the
// same writer the reconciler feeds through, so what the test reads is what a
// run writes.
func appendWallLine(t *testing.T, repo, runID, tick string, attempt int, at time.Time, detail string) {
	t.Helper()
	feed := runfeed.Open(repo, runID)
	n := attempt
	if err := feed.Append(runfeed.NewEvent(at, runID, tick, &n, reconcile.StageWallClock, detail)); err != nil {
		t.Fatalf("append the wall clock line: %v", err)
	}
}

// The wall clock FIRING reaches `ticfac status` for an in-flight attempt (tick
// q1e): the run's typed feed line — not the LAST line, and not prose — is
// what status reports beside the attempt's measured gaps.
//
// The fixture's traps, because the acceptance says "neither is derived by
// matching observation prose":
//
//   - a stall_warned line whose DETAIL mentions the wall clock — prose that
//     says the words must not count as the fact;
//   - the firing line for another tick, and one for another attempt of this
//     tick — identity is (tick, attempt), and a line for attempt 2 of a1 is
//     not a fact about attempt 1;
//   - a later run-level line, so the firing is not the last event: a watcher
//     reading `ticfac status` while the run polls on must not depend on
//     nothing having been said since.
func TestProbeReportsTheWallClockFiringOfAStandingAttempt(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo := wallRepo(t, "r-wall", now)

	// A line whose prose names the wall clock but whose STAGE is the early
	// warning (7zs) — the words are on it, and the fact is not.
	one := 1
	if err := runfeed.Open(repo, "r-wall").Append(runfeed.NewEvent(
		now.Add(-6*time.Minute), "r-wall", "a1", &one,
		reconcile.StageStallWarned,
		"attempt 1 of a1 is alive but has produced nothing durable for 15m0s: ... — the wall clock of 3600s is still the bound")); err != nil {
		t.Fatalf("append the stall line: %v", err)
	}
	// The firing for a different tick, and one for a different attempt of
	// this tick: neither is a fact about a1#1.
	appendWallLine(t, repo, "r-wall", "b1", 1, now.Add(-5*time.Minute), "the wall clock of 3600s fired for b1")
	appendWallLine(t, repo, "r-wall", "a1", 2, now.Add(-4*time.Minute), "the wall clock of 3600s fired for attempt 2")
	// The line that IS about a1#1, four minutes ago, with the executor's word.
	appendWallLine(t, repo, "r-wall", "a1", 1, now.Add(-4*time.Minute), wallDetail)
	// And a later run-level line, so the firing is not the last event.
	if err := runfeed.Open(repo, "r-wall").Append(runfeed.NewEvent(
		now.Add(-1*time.Minute), "r-wall", "", nil, reconcile.StageBudgetSet,
		"the effective budget for this run is $1.00")); err != nil {
		t.Fatalf("append the later line: %v", err)
	}

	status := Probe(repo, "r-wall", now)
	if len(status.Attempts) != 1 {
		t.Fatalf("the probe reports %d in-flight attempts, want the one that stands", len(status.Attempts))
	}
	if len(status.WallClocks) != 1 {
		t.Fatalf("the probe reports %d wall clock firings, want the one that fired:\n%+v",
			len(status.WallClocks), status.WallClocks)
	}
	w := status.WallClocks[0]
	if w.TickID != "a1" || w.Attempt != 1 {
		t.Fatalf("the firing reads as %s#%d, want a1#1: the other tick's and the other attempt's "+
			"lines must not attach to this attempt", w.TickID, w.Attempt)
	}
	if w.FiredAt == nil {
		t.Fatal("the attempt whose wall clock fired reports no firing: a watcher reading status " +
			"while the run polls on sees only the gaps")
	}
	if !w.FiredAt.Equal(now.Add(-4 * time.Minute)) {
		t.Errorf("the firing reads as %s, want %s", w.FiredAt, now.Add(-4*time.Minute))
	}
	if w.FiredAgo == nil || time.Duration(*w.FiredAgo) != (4*time.Minute) {
		t.Errorf("the firing's age is %+v, want 4m0s", w.FiredAgo)
	}
	if w.Detail != wallDetail {
		t.Errorf("the report does not carry the run's own line verbatim:\n got %q\nwant %q",
			w.Detail, wallDetail)
	}
	// And the firing is not the last event — the per-attempt fact is the
	// only place a watcher could have read it from.
	if status.LastEvent == nil || status.LastEvent.Stage != reconcile.StageBudgetSet {
		t.Fatalf("the fixture's last event reads as %+v, want the later budget line — "+
			"the firing must be reportable without being last", status.LastEvent)
	}
}

// Prose alone reports nothing: a feed that only MENTIONS the wall clock in a
// line's detail — the stall warning, whose whole point is that the bound has
// NOT fired — leaves the attempt's wall-clock facts null. Matching the typed
// stage is the machine's half; matching the sentence would be prose.
func TestProseAloneReportsNoWallClockFiring(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	repo := wallRepo(t, "r-prose", now)
	one := 1
	if err := runfeed.Open(repo, "r-prose").Append(runfeed.NewEvent(
		now.Add(-6*time.Minute), "r-prose", "a1", &one,
		reconcile.StageStallWarned,
		"attempt 1 of a1 is alive but has produced nothing durable for 15m0s: ... — the wall clock of 3600s is still the bound")); err != nil {
		t.Fatalf("append the stall line: %v", err)
	}

	status := Probe(repo, "r-prose", now)
	if len(status.Attempts) != 1 {
		t.Fatalf("the probe reports %d in-flight attempts, want the one that stands", len(status.Attempts))
	}
	if len(status.WallClocks) != 0 {
		t.Fatalf("a line whose prose mentions the wall clock reported a firing (%+v): "+
			"the fact is the typed stage, never the sentence", status.WallClocks)
	}
}
