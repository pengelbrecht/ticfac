package cli

import (
	"errors"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runsignal"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The completion signal's outcome is the factory's only account of how a
// local orchestrator's run ended (`ticfac run --cloud-workers`): hn6's
// run_6d88 failed and halted, and the factory recorded it completed because
// the signal carried no outcome at all.
func TestRunEpicOutcomeSaysHowTheRunEnded(t *testing.T) {
	t.Parallel()

	halted := runEpicOutcome(&reconcile.Result{
		State:  runstate.StateFailed,
		Reason: "ltg did not pass",
		Halt:   "the same refusal came back over an unchanged tree",
	})
	if halted.State != runsignal.OutcomeFailed || halted.ExitCode != exitGeneric ||
		halted.Reason != "ltg did not pass" || halted.Halt == "" {
		t.Errorf("a failed, halted run's outcome is %+v", halted)
	}

	done := runEpicOutcome(&reconcile.Result{State: runstate.StateCompleted, Reason: "every tick closed"})
	if done.State != runsignal.OutcomeCompleted || done.ExitCode != exitSuccess {
		t.Errorf("a completed run's outcome is %+v", done)
	}

	cancelled := runEpicOutcome(&reconcile.Result{State: runstate.StateCancelled})
	if cancelled.State != runsignal.OutcomeCancelled || cancelled.ExitCode != exitCancelled {
		t.Errorf("a cancelled run's outcome is %+v", cancelled)
	}

	// Only completed says the work is done: a run that stopped short of a
	// terminal state is not reported as finished.
	short := runEpicOutcome(&reconcile.Result{State: runstate.StateGating})
	if short.State != runsignal.OutcomeFailed {
		t.Errorf("a run that stopped while gating reports %q, want failed", short.State)
	}

	died := runEpicDiedOutcome(errors.New("reconcile: read the run state: boom"))
	if died.State != runsignal.OutcomeDied || died.Reason != "reconcile: read the run state: boom" {
		t.Errorf("a run that died reports %+v", died)
	}
}
