package cli

// The held class, unified (tick 4mv): a run that stopped because something
// only a person can move is ONE verdict on every surface that ends a run —
// `run-epic` exits it from its own typed refusal, and `run`/`watch` exit it
// through the watch — and the authority for which refusals are holds is the
// reconciler's own closed set (holdsForAPerson, exported as
// reconcile.HoldsForAPerson): the CLI keeps no second opinion about it,
// because a second list is exactly the disagreement this tick exists to
// remove. Before this, run-epic answered 1 for a finding_untriaged hold
// while the watch over the same run answered 3, and the SKILL's "a run that
// ends holding something for a person exits 3" was true of one command and
// false of the other.
import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

func TestARunThatStoppedHoldingForAPersonExitsHeld(t *testing.T) {
	t.Parallel()

	// Every refusal the reconciler's own set names as a hold — the same set
	// that decides StageRunHeld on the feed, so the exit code and the feed
	// line can never disagree about whether a stop was a hold. All eight of
	// holdsForAPerson's reasons are pinned here, one by name: the tick's
	// "test each path" is each reason's path through resultExitCode, and a
	// reason that joined the reconciler's set without this loop noticing
	// would be a hold the exit table answers failed for.
	for _, reason := range []string{
		reconcile.RefusedHeld,
		reconcile.RefusedFindingUntriaged,
		reconcile.RefusedNeedsHuman,
		reconcile.RefusedRoleAnswer,
		reconcile.RefusedAbsorptionDepth,
		reconcile.RefusedClaimWidth,
		reconcile.RefusedUnaddressed,
		reconcile.RefusedRejectedWork,
	} {
		stopped := &reconcile.Result{
			State: runstate.StateFailed,
			RunID: "epic-qeu", EpicID: "qeu",
			Reason:  "the close-out holds: a finding is untriaged",
			Failure: &reconcile.Refusal{Reason: reason, Message: "a person must decide"},
		}
		if code := resultExitCode(stopped); code != ExitHeld {
			t.Errorf("a run stopped over %s exits %d, want the held code %d — the same verdict the watch over this run ends by",
				reason, code, ExitHeld)
		}
		if state := runEpicStateWord(stopped); state != agentStateHeld {
			t.Errorf("a run stopped over %s answers the state word %q, want %q — the document and the exit code agree",
				reason, state, agentStateHeld)
		}
		if code := stateExitClass(runEpicStateWord(stopped)); code != resultExitCode(stopped) {
			t.Errorf("the document's state word maps to %d while the process exits %d", code, resultExitCode(stopped))
		}
	}

	// A refusal the run can repair itself is a failure, not a hold — the
	// table's failed class, as it was.
	failed := &reconcile.Result{
		State: runstate.StateFailed,
		RunID: "epic-qeu", EpicID: "qeu",
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedGate, Message: "the gate did not pass"},
	}
	if code := resultExitCode(failed); code != exitGeneric {
		t.Errorf("a gate refusal exits %d, want the failed code %d", code, exitGeneric)
	}
	if state := runEpicStateWord(failed); state != agentStateFailed {
		t.Errorf("a gate refusal answers the state word %q, want %q", state, agentStateFailed)
	}

	// The two classes that keep their own codes: completed is done, and a
	// missing epic stays not-found — the code the factory's Run Workflow
	// reads as a terminal configuration verdict.
	completed := &reconcile.Result{State: runstate.StateCompleted, RunID: "epic-qeu", EpicID: "qeu"}
	if code := resultExitCode(completed); code != exitSuccess {
		t.Errorf("a completed run exits %d, want %d", code, exitSuccess)
	}
	if state := runEpicStateWord(completed); state != agentStateDone {
		t.Errorf("a completed run answers the state word %q, want done", state)
	}
	absent := &reconcile.Result{
		State:   runstate.StateFailed,
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedEpicAbsent},
	}
	if code := resultExitCode(absent); code != exitNotFound {
		t.Errorf("an epic-absent refusal exits %d, want the missing code %d", code, exitNotFound)
	}
	if state := runEpicStateWord(absent); state != agentStateFailed {
		t.Errorf("an epic-absent refusal answers the state word %q, want failed — the code's not-found half is the documented exception, not a state word",
			state)
	}

	// Cancelled (tick rix) is decided before the refusal, in both
	// authorities alike: a deliberate stop is neither a failure nor a hold,
	// whatever refusal rode along with it, and the word and the code agree.
	cancelled := &reconcile.Result{
		State: runstate.StateCancelled, RunID: "epic-qeu", EpicID: "qeu",
		Failure: &reconcile.Refusal{Reason: reconcile.RefusedHeld},
	}
	if code := resultExitCode(cancelled); code != exitCancelled {
		t.Errorf("a cancelled run exits %d, want the cancelled code %d", code, exitCancelled)
	}
	if state := runEpicStateWord(cancelled); state != agentStateCancelled {
		t.Errorf("a cancelled run answers the state word %q, want %q", state, agentStateCancelled)
	}
}
