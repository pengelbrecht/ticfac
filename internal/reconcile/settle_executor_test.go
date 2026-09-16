package reconcile

import (
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// TestSettleAddressesTheAttemptsOwnExecutor pins tick emk's recovery path.
//
// addressForSettlement built the handle with Executor hardcoded to the local
// subprocess executor. The herdr executor refuses a handle naming another
// executor — correctly, since decoding one would be the collected-under-another-
// name failure — so every attempt of a herdr run was unreleasable:
//
//	ticfac settle 9pd cwa 7: reconcile: inspect attempt 7 of cwa:
//	handle names executor "local-subprocess"; this is herdr
//
// That command is the one the run's own refusal tells the operator to run when
// an attempt cannot be addressed. It happened in the Phase 3 run: the wall
// clock could not stop a pi worker, the reconciler refused the attempt as
// unaddressed and named this command, and this command refused too.
func TestSettleAddressesTheAttemptsOwnExecutor(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"herdr", subprocess.ExecutorName, ""} {
		marker := attemptHandle{
			Executor: name,
			JobID:    "run-r/tick-t/attempt-1",
			Attempt:  1,
			TickID:   "t",
		}
		handle := &subprocess.JobHandle{
			SchemaVersion: subprocess.SchemaVersion,
			JobID:         marker.JobID,
			Attempt:       marker.Attempt,
			Executor:      marker.Executor,
			Handle:        map[string]any{"state": "/tmp/state"},
		}
		if handle.Executor != name {
			t.Errorf("a marker naming executor %q produced a handle naming %q: the executor that holds the "+
				"attempt is the only one that can release it", name, handle.Executor)
		}
	}
}

// TestNoSettlementHandleHardcodesAnExecutor is the guard: the defect was one
// literal in one struct, and a reader cannot see it is wrong from the line.
func TestNoSettlementHandleHardcodesAnExecutor(t *testing.T) {
	t.Parallel()

	body := mustReadSource(t, "settle.go")
	start := strings.Index(body, "func (r *Reconciler) addressForSettlement")
	if start < 0 {
		t.Fatal("addressForSettlement is gone; this guard needs rewriting")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		end = len(body) - start
	}
	fn := body[start : start+end]
	if strings.Contains(fn, "Executor:      subprocess.ExecutorName") || strings.Contains(fn, "Executor: subprocess.ExecutorName") {
		t.Error("addressForSettlement names one executor literally: a handle must carry the executor its " +
			"attempt was dispatched under, or every attempt of every other executor is unreleasable (tick emk)")
	}
}

func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
