package subprocess

import (
	"os"
	"path/filepath"
	"testing"
)

// The two reads the SIGTERM evacuation flush (tick ppt) makes of an attempt's
// state directory: whether the attempt settled, and where its work is. They
// are exported because the flush lives in the reconciler while the names they
// read — attempt.json, runner.exit — are this package's own.

// short: file reads and writes only: no repository, no subprocess
func TestAttemptSettledReadsTheSettleMarker(t *testing.T) {
	dir := t.TempDir()
	if AttemptSettled(dir) {
		t.Fatalf("a state directory with no runner.exit read as settled: an unsettled " +
			"attempt is the one the flush spends its bounded budget on")
	}
	if err := os.WriteFile(filepath.Join(dir, fileRunnerExit), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !AttemptSettled(dir) {
		t.Fatalf("a state directory holding runner.exit did not read as settled")
	}
}

// short: file reads and writes only: no repository, no subprocess
func TestReadAttemptWorkNamesWhereTheWorkIs(t *testing.T) {
	dir := t.TempDir()

	// A record that names neither worktree nor branch is a refusal, not a
	// zero answer: the flush that got one would be pushing a claim about
	// nothing.
	if _, err := ReadAttemptWork(dir); err == nil {
		t.Fatalf("a state directory with no attempt record read as work")
	}
	empty, err := os.Create(filepath.Join(dir, fileAttempt))
	if err != nil {
		t.Fatal(err)
	}
	empty.Close()
	if _, err := ReadAttemptWork(dir); err == nil {
		t.Fatalf("an empty attempt record read as work")
	}

	raw := `{"schema_version": 1, "tick_id": "a1", "attempt": 1, "job_id": "run-r/tick-a1/attempt-1",
		"branch": "ticfac/run-r/tick-a1/attempt-1", "worktree": "/tmp/state/a1/1/worktree", "remote": "origin"}`
	if err := os.WriteFile(filepath.Join(dir, fileAttempt), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	work, err := ReadAttemptWork(dir)
	if err != nil {
		t.Fatal(err)
	}
	if work.TickID != "a1" || work.Attempt != 1 || work.Branch != "ticfac/run-r/tick-a1/attempt-1" ||
		work.Worktree != "/tmp/state/a1/1/worktree" || work.Remote != "origin" {
		t.Errorf("the read work is %+v, not what the record states", work)
	}
}
