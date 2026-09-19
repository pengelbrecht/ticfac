package subprocess

import (
	"os"
	"path/filepath"
	"testing"
)

// An attempt that settles WHILE it is being observed is never reported lost.
//
// observe used to read the settlement marker at the top, then check liveness.
// The supervisor writes runner.exit atomically and then exits, so there was a
// window: observe reads 'not settled', the supervisor settles and exits,
// alive() re-reads the marker and correctly answers 'not alive' BECAUSE the
// attempt is settled — and observe, still holding its stale 'not settled',
// reported the attempt LOST. Lost makes the run refuse and stop.
//
// On a fast machine the window is microseconds. On the 2-vCPU CI runner it
// failed TestAStoppedAttemptSPreservedWorkReachesTheNextAttempt three times in
// one day, as 'nobody can say whether it is running'.
//
// The seam settles the attempt at exactly the instant that used to be misread,
// so this fails every time against the old order rather than on a bad day.
func TestAnAttemptThatSettlesMidObservationIsNotLost(t *testing.T) {
	dir := t.TempDir()
	st := newStore(dir)
	record := &attemptRecord{
		JobID:       "run-1/tick-a1/attempt-1",
		Attempt:     1,
		ResultPath:  filepath.Join(dir, "no-report-here.md"),
		WallSeconds: 10,
	}

	observeBeforeLiveness = func() {
		// The supervisor's last act before exiting: settle.
		if err := os.WriteFile(st.path(fileRunnerExit), []byte("137\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { observeBeforeLiveness = nil })

	e := &Executor{}
	state, detail := e.observe(st, record)
	if state == StateLost {
		t.Fatalf("an attempt that settled during observation was reported LOST (%q): the settlement marker was "+
			"read before the liveness check and was stale by the time it was used", detail)
	}
	if state != StateFailed {
		t.Errorf("state = %s (%q), want %s: settled with no report is a failure, not running and not lost",
			state, detail, StateFailed)
	}
}
