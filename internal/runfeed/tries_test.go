package runfeed

import (
	"testing"
	"time"
)

// TestTriesCountTheTicksOwnDispatches pins the number a person reads first
// (tick h58). The operator's run: 0ju got run dispatch 1, mrn 2, and w9b 3, 4
// and 5 — so the line carrying attempt 5 is w9b's THIRD try, and a prefix
// that said "w9b#5" read as its fifth.
// short: counting over events already in memory
func TestTriesCountTheTicksOwnDispatches(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	line := func(tick string, attempt int) Event {
		n := attempt
		return NewEvent(at, "epic-wne", tick, &n, "dispatched", "")
	}
	var tries Tries
	for _, e := range []Event{
		line("0ju", 1), line("mrn", 2), line("w9b", 3), line("w9b", 3), line("w9b", 4), line("w9b", 5),
		NewEvent(at, "epic-wne", "", nil, "run_finished", ""),
		NewEvent(at, "epic-wne", "keh", nil, "held", ""),
	} {
		tries.Observe(e)
	}

	for _, c := range []struct {
		tick    string
		attempt int
		want    int
	}{
		{"0ju", 1, 1}, {"mrn", 2, 1}, {"w9b", 3, 1}, {"w9b", 4, 2}, {"w9b", 5, 3},
	} {
		got, ok := tries.Of(c.tick, c.attempt)
		if !ok || got != c.want {
			t.Errorf("Of(%s, dispatch %d) = %d, %v; want try %d", c.tick, c.attempt, got, ok, c.want)
		}
	}

	// A number the feed never showed for the tick is not guessed at — not
	// even one another tick owns.
	if got, ok := tries.Of("w9b", 2); ok {
		t.Errorf("Of(w9b, dispatch 2) = %d, want no answer: dispatch 2 is mrn's", got)
	}
	if got, ok := tries.Of("keh", 1); ok {
		t.Errorf("Of(keh, 1) = %d, want no answer: keh's lines carried no attempt", got)
	}
}
