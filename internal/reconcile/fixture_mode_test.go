package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The fixture's mode trap (harness_test.go, applyMode): a mode passed to a
// run after newFixture is the runner that run gets, or the test fails saying
// why it cannot be. It used to be ignored without a word, and a test asking
// for a different behaviour on a later run silently got the first run's.

// A later run's mode is the one its runner runs in, and the runs after it
// keep it.
func TestAModePassedToALaterRunIsTheRunnerThatRunGets(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "review_not_ready"})

	f.options(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready"})
	if got, _ := fakeRunnerModeOf(f.Runner); got != "review_not_ready_then_ready" {
		t.Fatalf("the run asked for mode review_not_ready_then_ready and its runner runs %q", got)
	}
	f.options(f.Repo, fixtureOptions{})
	if got, _ := fakeRunnerModeOf(f.Runner); got != "review_not_ready_then_ready" {
		t.Errorf("a run that names no mode moved the runner to %q: it keeps the last one asked for", got)
	}
}

// The trap as a run meets it: a fixture built in one mode, and a second run
// that asks for another, behaves as the second run asked. Here the first
// review's NOT READY is followed by a run in a mode whose re-review answers
// READY — and the run lands, which the ignored mode never let it.
func TestALaterRunsModeChangesWhatTheWorkerDoes(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "report", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 2 {
		t.Fatalf("%d review decisions, want 2: the run's mode (NOT READY, then READY) is what the worker did, "+
			"not the fixture's (READY once)", len(reviews))
	}
}

// A custom runner takes no mode: a run asking for one it would ignore is an
// error, not a silence. The fixture's own mode is the same options reused.
func TestAModeACustomRunnerWouldIgnoreIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "report"})
	f.Runner = askingRunner(t, "a1", "which one?", "never")

	if err := f.modeError("report"); err != nil {
		t.Errorf("the fixture's own mode over a custom runner is refused: %v", err)
	}
	err := f.modeError("empty-first")
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Errorf("a mode a custom runner would ignore is not refused: %v", err)
	}
	if !strings.Contains(strings.Join(f.Runner, " "), "FAKE_RUNNER_MODE=ask") {
		t.Errorf("the refused mode replaced the custom runner: %v", f.Runner)
	}
}
