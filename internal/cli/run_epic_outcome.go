package cli

import (
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runsignal"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// runEpicOutcome is the run's own account of its end, as the completion
// signal carries it to the factory. A LOCAL orchestrator (`ticfac run
// --cloud-workers`) has no process the factory can ask, so this is the
// factory's only source for how the run ended: without it a run that failed
// and halted (hn6's run_6d88 and run_09eb) was recorded as completed, and the
// next `ticfac run` read that record and called the run finished.
//
// The state is the run's terminal word: completed and cancelled as they are,
// and anything else — failed, or a supervised run that stopped short of a
// terminal state — is failed, because only completed says the work is done.
func runEpicOutcome(result *reconcile.Result) *runsignal.Outcome {
	if result == nil {
		return &runsignal.Outcome{State: runsignal.OutcomeDied, ExitCode: exitGeneric,
			Reason: "run-epic ended with no result"}
	}
	state := runsignal.OutcomeFailed
	switch result.State {
	case runstate.StateCompleted:
		state = runsignal.OutcomeCompleted
	case runstate.StateCancelled:
		state = runsignal.OutcomeCancelled
	}
	return &runsignal.Outcome{
		State:    state,
		ExitCode: resultExitCode(result),
		Reason:   result.Reason,
		Halt:     result.Halt,
	}
}

// runEpicDiedOutcome is the outcome of a run-epic that ended in an error
// rather than a result: the run died, and the error is its reason.
func runEpicDiedOutcome(err error) *runsignal.Outcome {
	reason := "run-epic ended with no result"
	if err != nil {
		reason = err.Error()
	}
	return &runsignal.Outcome{State: runsignal.OutcomeDied, ExitCode: exitGeneric, Reason: reason}
}
