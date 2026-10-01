package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A finish step that waits on a job keeps the window turning (epic hn6,
// run_6d88e3de).
//
// 378's resolve-conflict job ran 25 minutes, and for all of it the run
// addressed nothing else: the finish step sat in the resolve's own wait.
// 7uv and zl1 were never asked about, the factory let both containers idle
// out with their workers in them, and the next look found 7uv gone. A wait
// inside a finish step now hands the window its turn between its polls.

// resolveWindowGate is resolveGate three wide, so a third tick of the wave
// is in flight beside the conflicting pair.
var resolveWindowGate = strings.Replace(resolveGate, "max_parallel = 2", "max_parallel = 3", 1)

// TestALiveAttemptIsAddressedWhileAResolveJobRuns drives the shape: a1 and
// a2 conflict, a3 is still working when a2's resolve-conflict job starts,
// and a3 finishes only after it has. The resolve answers a moment after a3
// is done. The run must SEE a3 settle while the resolve is still running —
// a run that addressed nothing during the resolve sees it only afterwards.
//
// short: one full fixture run, the resolve acceptance driver's own shape;
// skipped under -short with the rest of the end-to-end suite.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS, LIVE_TICK) for the fake runner's workers to synchronise
// through, and t.Setenv forbids a parallel test.
func TestALiveAttemptIsAddressedWhileAResolveJobRuns(t *testing.T) {
	conflictSync(t)
	t.Setenv("LIVE_TICK", "a3")
	mode := "conflict_resolve_while_live"
	f := newFixture(t, fixtureOptions{mode: mode, gate: resolveWindowGate})
	seedSharedFile(t, f)
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Waves = [][]string{{"a1", "a2", "a3"}, {"b1"}, {"rv", "co"}}
	state.Order = []string{"a1", "a2", "a3", "b1", "rv", "co"}
	state.Ticks["a3"] = tk.Tick{ID: "a3", Title: "tick a3", Status: "open", Type: "task", Parent: "qeu", Priority: 2}
	f.Tracker.write(t, state)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: mode, gate: resolveWindowGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s)", result.State, result.Reason)
	}

	resolveCollected, liveSettled := -1, -1
	for i, e := range r.journal {
		if e.Tick == "a2" && e.Stage == StageCollected && strings.Contains(e.Detail, "resolve-conflict job") &&
			resolveCollected < 0 {
			resolveCollected = i
		}
		if e.Tick == "a3" && e.Stage == StageWaiting && strings.HasPrefix(e.Detail, "settled as") && liveSettled < 0 {
			liveSettled = i
		}
	}
	if resolveCollected < 0 || liveSettled < 0 {
		t.Fatalf("the journal holds no resolve collect for a2 (%d) or no settle for a3 (%d)", resolveCollected, liveSettled)
	}
	if liveSettled > resolveCollected {
		t.Errorf("a3 was seen to settle only AFTER a2's resolve-conflict job was collected: the run addressed " +
			"nothing while the resolve ran, which is how run_6d88e3de's workers idled out unwatched")
	}
	for _, tick := range []string{"a1", "a2", "a3", "b1"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s, not closed", tick, current.Status)
		}
	}
}
