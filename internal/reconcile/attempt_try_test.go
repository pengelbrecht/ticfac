package reconcile

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// TestTryOfCountsThisTicksDispatches pins the number an operator actually wants.
//
// Attempt numbers count dispatches across the run, because the number is the
// attempt's identity: its branch, its durable marker, and the argument to
// `ticfac settle`. In a sentence that reads wrong — "attempt 12 of nvn" sounds
// like eleven failures at nvn when it is nvn's first try and the run's twelfth
// dispatch. This is the number that makes the line honest.
// short: tryOf over attempt records already in memory
func TestTryOfCountsThisTicksDispatches(t *testing.T) {
	t.Parallel()

	// The Phase 3 run: eleven dispatches across seven ticks, cwa twice.
	attempts := []runstate.Attempt{
		{Attempt: 1, TickID: "0z0"}, {Attempt: 2, TickID: "55i"}, {Attempt: 3, TickID: "0iz"},
		{Attempt: 4, TickID: "48q"}, {Attempt: 5, TickID: "4ys"}, {Attempt: 6, TickID: "54n"},
		{Attempt: 7, TickID: "cwa"}, {Attempt: 8, TickID: "cwa"}, {Attempt: 9, TickID: "e08"},
		{Attempt: 10, TickID: "glb"}, {Attempt: 11, TickID: "her"},
	}

	for _, c := range []struct {
		tick   string
		number int
		want   int
	}{
		{"nvn", 12, 1}, // the run's twelfth dispatch, nvn's first try
		{"cwa", 7, 1},  // the stalled one
		{"cwa", 8, 2},  // the fresh one after the release
		{"0z0", 1, 1},
		{"her", 11, 1},
	} {
		if got := tryOf(attempts, c.tick, c.number); got != c.want {
			t.Errorf("tryOf(%s, attempt %d) = %d, want %d", c.tick, c.number, got, c.want)
		}
	}
}

// An attempt numbered below one already recorded for the same tick — a replay,
// or a marker read out of order — must not inflate the count.
// short: tryOf over attempt records already in memory
func TestTryOfIgnoresLaterAttemptsOfTheSameTick(t *testing.T) {
	t.Parallel()

	attempts := []runstate.Attempt{{Attempt: 7, TickID: "cwa"}, {Attempt: 8, TickID: "cwa"}}
	if got := tryOf(attempts, "cwa", 7); got != 1 {
		t.Errorf("tryOf(cwa, attempt 7) = %d with a later attempt recorded, want 1", got)
	}
}

// A line written for a person leads with the tick's own try and labels the
// run-wide number as the dispatch number it is (tick h58). The line an
// operator misread — "attempt 5 started as … (w9b try 3; attempt numbers
// count this run's dispatches)" — needed a footnote for its own headline.
// short: formatting over values already in memory
func TestAttemptLabelLeadsWithTheTry(t *testing.T) {
	t.Parallel()

	if got, want := AttemptLabel("w9b", 3, 5), "w9b try 3 (run dispatch #5)"; got != want {
		t.Errorf("AttemptLabel(w9b, try 3, dispatch 5) = %q, want %q", got, want)
	}
	// A try nobody could count is not guessed at: the dispatch number still
	// names the attempt, and is still labelled as one.
	if got, want := AttemptLabel("w9b", 0, 5), "w9b (run dispatch #5)"; got != want {
		t.Errorf("AttemptLabel(w9b, no try, dispatch 5) = %q, want %q", got, want)
	}
}
