package runlife

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
