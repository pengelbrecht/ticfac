package cli

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The half of ticfac tick rf3 that is the exit code: a run that stopped because
// the epic does not exist on the submitted tree is the ONE stop a replacement
// container cannot reach a different answer on — the tracker's tree is cut from
// the submitted commit, so the epic is missing on every boot. The first
// per-tick Cloudflare smoke run re-booted into that identical failure until a
// person stopped it.
//
// So the run exits NOT-FOUND — tk's code 4, the code this package already
// keeps for "a lookup that honestly came back empty: a missing epic" — which
// the factory's Run Workflow reads as a terminal configuration verdict
// (TERMINAL_EXIT_CODES, cloudflare/src/sandbox.ts) and answers by refusing to
// reboot: the run stops, with the reconciler's refusal in its durable records
// and the exit code telling the supervisor why.
func TestARunStoppedForAnEpicAbsentFromTheSubmittedTreeExitsNotFound(t *testing.T) {
	t.Parallel()

	stopped := &reconcile.Result{
		State:   runstate.StateFailed,
		Reason:  "the epic qeu does not exist on the submitted tree",
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedEpicAbsent},
	}
	if code := resultExitCode(stopped); code != exitNotFound {
		t.Errorf("exit code %d, want %d — the Workflow re-boots anything else, and a missing epic is "+
			"missing on every boot", code, exitNotFound)
	}

	// Every other failure is what it was: generic. A merge conflict, a gate
	// that did not pass and a worker that answered BLOCKED are all stops a
	// person repairs and a reboot may legitimately adopt the repair of.
	failed := &reconcile.Result{
		State:   runstate.StateFailed,
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedBaseRefresh},
	}
	if code := resultExitCode(failed); code != exitGeneric {
		t.Errorf("a base-refresh refusal exits %d, want %d", code, exitGeneric)
	}
	noFailure := &reconcile.Result{State: runstate.StateFailed, Reason: "the gate did not pass"}
	if code := resultExitCode(noFailure); code != exitGeneric {
		t.Errorf("a run that failed without a typed refusal exits %d, want %d", code, exitGeneric)
	}
	if code := resultExitCode(&reconcile.Result{State: runstate.StateCompleted}); code != exitSuccess {
		t.Errorf("a completed run exits %d, want %d", code, exitSuccess)
	}
}
