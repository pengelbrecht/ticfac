package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// wideGate is passingGate with a host width the run may actually use. The
// width lives in the repo's own runners.toml because that is where the run
// reads it from — a test that set it any other way would prove the field, not
// the declaration.
const wideGate = `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// dispatchOrder is what the feed says about which attempts were live at once:
// the stages that open and close an attempt's window, in the order they
// happened.
func dispatchOrder(journal []Event) []Event {
	var out []Event
	for _, event := range journal {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched, StageWaiting, StageIntegrated, StageGatePassed, StageClosed:
			out = append(out, event)
		}
	}
	return out
}

// TestTheWindowDispatchesAWaveTogether is the acceptance: with a declared
// width of two, both ticks of wave 1 are dispatched before either is waited
// out. Under the sequential loop a1 was dispatched, waited for, collected,
// merged, gated and CLOSED before a2 was dispatched at all.
func TestTheWindowDispatchesAWaveTogether(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic", result.Closed)
	}

	var a1Dispatched, a2Dispatched, a1Closed int
	for i, event := range dispatchOrder(r.Journal()) {
		switch {
		case event.Tick == "a1" && event.Stage == StageDispatched:
			a1Dispatched = i + 1
		case event.Tick == "a2" && event.Stage == StageDispatched:
			a2Dispatched = i + 1
		case event.Tick == "a1" && event.Stage == StageClosed:
			a1Closed = i + 1
		}
	}
	if a1Dispatched == 0 || a2Dispatched == 0 || a1Closed == 0 {
		t.Fatalf("the feed is missing a dispatch or a close: a1 %d/%d, a2 %d", a1Dispatched, a1Closed, a2Dispatched)
	}
	if a2Dispatched > a1Closed {
		t.Errorf("a2 was dispatched after a1 closed (positions %d and %d): the wave ran one at a time, "+
			"which is the sequential run the width was supposed to widen", a2Dispatched, a1Closed)
	}
}

// TestTheWindowIntegratesOneAtATime is the other half, and the half that keeps
// the run honest: there is ONE integration branch, and a gate that ran on a
// tree other than the one being closed proves nothing about it. However wide
// the dispatch, a tick's integrate/gate/close runs to completion before the
// next tick's begins.
func TestTheWindowIntegratesOneAtATime(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	r, _, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Walk the closing half of each tick's life. Once a tick integrates,
	// nothing else may integrate, gate or close until that tick has closed.
	owner := ""
	for _, event := range r.Journal() {
		switch event.Stage {
		case StageIntegrated, StageGatePassed:
			if owner != "" && owner != event.Tick {
				t.Fatalf("%s reached %s while %s still held the integration branch: "+
					"two ticks were being integrated at once", event.Tick, event.Stage, owner)
			}
			owner = event.Tick
		case StageClosed:
			if owner != "" && owner != event.Tick {
				t.Fatalf("%s closed while %s held the integration branch", event.Tick, owner)
			}
			owner = ""
		}
	}
}

// TestAnUndeclaredWidthRunsExactlyAsItAlwaysDid pins the default. A repo that
// never declared max_parallel gets the sequential run, tick by tick, with no
// overlap at all — the width is opt-in, and a run that was not told how wide it
// may be does not widen itself.
func TestAnUndeclaredWidthRunsExactlyAsItAlwaysDid(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic", result.Closed)
	}

	// Sequential means: between one tick's dispatch and its close, no other
	// tick is dispatched.
	open := ""
	for _, event := range dispatchOrder(r.Journal()) {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			if open != "" && open != event.Tick {
				t.Fatalf("%s was dispatched while %s was still open: an undeclared width widened itself",
					event.Tick, open)
			}
			open = event.Tick
		case StageClosed:
			open = ""
		}
	}
}

// TestTheWindowNeverSpansAWaveBoundary: wave 2 exists because something in it
// is blocked by something in wave 1. Dispatching across the boundary would
// start b1 against a base that is missing the very work it was sequenced
// behind, so the window stops at the boundary however much room it has.
func TestTheWindowNeverSpansAWaveBoundary(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	r, _, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	closed := map[string]int{}
	dispatched := map[string]int{}
	for i, event := range r.Journal() {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			if _, seen := dispatched[event.Tick]; !seen {
				dispatched[event.Tick] = i
			}
		case StageClosed:
			closed[event.Tick] = i
		}
	}
	for _, earlier := range []string{"a1", "a2"} {
		if dispatched["b1"] < closed[earlier] {
			t.Errorf("b1 (wave 2) was dispatched at %d, before %s (wave 1) closed at %d",
				dispatched["b1"], earlier, closed[earlier])
		}
	}
	// And the role jobs run alone, after everything they are about.
	for _, role := range []string{"rv", "co"} {
		for _, work := range []string{"a1", "a2", "b1"} {
			if dispatched[role] < closed[work] {
				t.Errorf("%s was dispatched at %d, before %s closed at %d: a role job is about a FINISHED epic",
					role, dispatched[role], work, closed[work])
			}
		}
	}
}

// TestAWidthOfOneIsTheSequentialRun: the width is a bound, not a target, and
// one is the bound the old loop had. This is the regression guard for every
// test in this package that was written against the sequential shape.
func TestAWidthOfOneIsTheSequentialRun(t *testing.T) {
	t.Parallel()
	narrow := `version = 2

[orchestration]
max_parallel = 1

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`
	f := newFixture(t, fixtureOptions{gate: narrow})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: narrow})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic", result.Closed)
	}
	open := ""
	for _, event := range dispatchOrder(r.Journal()) {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			if open != "" && open != event.Tick {
				t.Fatalf("%s was dispatched while %s was still open, at a declared width of one",
					event.Tick, open)
			}
			open = event.Tick
		case StageClosed:
			open = ""
		}
	}
}

// TestARefusalStopsTheWindowAndNamesWhatIsStillRunning: a width greater than
// one makes it possible for the first time that the run stops while other
// workers are still thinking. Those workers do not stop because the run did,
// so the feed has to say they are out there — silence would look exactly like
// the run having finished with them.
func TestARefusalStopsTheWindowAndNamesWhatIsStillRunning(t *testing.T) {
	t.Parallel()
	refusing := `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "exit 3", description = "always refuses" }
`
	f := newFixture(t, fixtureOptions{gate: refusing})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: refusing})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 0 {
		t.Fatalf("closed %v behind a gate that refuses everything", result.Closed)
	}

	// The second tick of the wave was dispatched — that is the point of the
	// width — and it must NOT have been integrated or closed after the first
	// tick's gate refused.
	var dispatched, abandoned bool
	for _, event := range r.Journal() {
		if event.Tick == "a2" {
			switch event.Stage {
			case StageDispatched:
				dispatched = true
			case StageIntegrated, StageGatePassed, StageClosed:
				t.Errorf("a2 reached %s after a1's gate refused: the run integrated over a tree "+
					"it had just been told nothing stands behind", event.Stage)
			}
			if event.Stage == StageWaiting && strings.Contains(event.Detail, "still running") {
				abandoned = true
			}
		}
	}
	if !dispatched {
		t.Fatal("a2 was never dispatched: the window did not widen at all")
	}
	if !abandoned {
		t.Error("a2 was left running and the feed never said so: an operator reading the feed " +
			"cannot tell a worker still out there from one the run finished with")
	}
}

// TestARefreshDoesNotRevertAnAttemptTheWindowIsHolding.
//
// The step-cap rollover re-derives the run's tick state from origin. An
// attempt whose number is not checkpointed yet — an ADOPTED one, which sets it
// at dispatch.go:523 and returns with no checkpoint behind it — would have its
// row reverted to origin's, and every later feed line for that tick would name
// the wrong attempt or none at all.
//
// This is about identity, which this run treats as load-bearing: a feed line
// that cannot say which attempt it is about is a line nobody can act on.
func TestARefreshDoesNotRevertAnAttemptTheWindowIsHolding(t *testing.T) {
	t.Parallel()
	r := &Reconciler{ticks: []runstate.TickState{
		{TickID: "a1", State: "dispatched", Attempt: 7},
		{TickID: "a2", State: "dispatched", Attempt: 8},
		{TickID: "b1", State: "closed", Attempt: 3},
	}}
	live := []*inflightAttempt{
		{marker: attemptHandle{TickID: "a2", Attempt: 8}},
	}

	// What origin knows is older: it never heard a2's attempt number, and it
	// still thinks a1 was merely claimed.
	r.adoptTicks([]runstate.TickState{
		{TickID: "a1", State: "closed", Attempt: 7},
		{TickID: "a2", State: "ready", Attempt: 0},
		{TickID: "b1", State: "closed", Attempt: 3},
	}, live)

	got := map[string]runstate.TickState{}
	for _, ts := range r.ticks {
		got[ts.TickID] = ts
	}
	if len(r.ticks) != 3 {
		t.Fatalf("ticks = %v, want one row per tick", r.ticks)
	}
	if got["a2"].Attempt != 8 || got["a2"].State != "dispatched" {
		t.Errorf("a2 = %+v, want the window's own row (attempt 8, dispatched): a refresh reverted an "+
			"attempt the run is still holding, so its feed lines can no longer name it", got["a2"])
	}
	if got["a1"].State != "closed" {
		t.Errorf("a1 = %+v, want origin's row: durable state wins for a tick the window is NOT holding", got["a1"])
	}
}

// And a tick the window admitted that origin has never heard of survives the
// refresh rather than vanishing from the run's own state.
func TestARefreshKeepsATickOriginHasNotHeardOf(t *testing.T) {
	t.Parallel()
	r := &Reconciler{ticks: []runstate.TickState{{TickID: "a2", State: "dispatched", Attempt: 8}}}
	live := []*inflightAttempt{{marker: attemptHandle{TickID: "a2", Attempt: 8}}}
	r.adoptTicks([]runstate.TickState{{TickID: "a1", State: "closed", Attempt: 7}}, live)
	if len(r.ticks) != 2 {
		t.Fatalf("ticks = %v, want a1 from origin AND a2 from the window", r.ticks)
	}
	if r.ticks[1].TickID != "a2" || r.ticks[1].Attempt != 8 {
		t.Errorf("ticks = %v, want a2 appended with its attempt intact", r.ticks)
	}
}

// TestABusyRunDoesNotReadItsOwnGateAsAWipe.
//
// Poll concludes WIPED from the interval between two polls, which is sound
// only while that interval measures the substrate. With a window it does not:
// finishTick integrates and gates one tick while the rest of the window waits,
// the default wipe threshold is 20 minutes and a gate may run to 45. Without
// excuseWindow the very next poll of a healthy attempt reads as wiped, and the
// run refuses work that was never in trouble.
func TestABusyRunDoesNotReadItsOwnGateAsAWipe(t *testing.T) {
	t.Parallel()
	clock := time.Now()
	r := &Reconciler{
		lastPolled:    map[string]time.Time{},
		liveness:      map[string]string{},
		wipeThreshold: 20 * time.Minute,
		now:           func() time.Time { return clock },
	}
	live := []*inflightAttempt{{
		entry:  planEntry{TickID: "a2"},
		marker: attemptHandle{TickID: "a2", Attempt: 8, JobID: "job-a2"},
	}}
	r.noteAlive("job-a2")

	// A gate that ran for well over the threshold, exactly as a real one may.
	gate := 45 * time.Minute
	clock = clock.Add(gate)

	// Without the excuse, this is what the next poll would have concluded.
	unexcused := &Reconciler{
		lastPolled:    map[string]time.Time{"job-a2": clock.Add(-gate)},
		liveness:      map[string]string{},
		wipeThreshold: 20 * time.Minute,
		now:           func() time.Time { return clock },
	}
	if unexcused.Poll("job-a2") != Wiped {
		t.Fatal("the test is not exercising the hazard: a 45-minute gap under a 20-minute threshold " +
			"must otherwise read as a wipe")
	}

	r.excuseWindow(live, gate)
	if got := r.Poll("job-a2"); got != Polled {
		t.Errorf("Poll = %v, want %v: the run read its own gate as the substrate wiping a healthy attempt", got, Polled)
	}

	// And it said so, because on a substrate that really does reclaim, a gap
	// that long is something an operator needs to know happened.
	var said bool
	for _, event := range r.Journal() {
		if event.Tick == "a2" && strings.Contains(event.Detail, "went unpolled") {
			said = true
		}
	}
	if !said {
		t.Error("the gap exceeded the wipe threshold and the feed never mentioned it")
	}
}

// A gap that stayed under the threshold is unremarkable and says nothing.
func TestAShortGateSaysNothing(t *testing.T) {
	t.Parallel()
	clock := time.Now()
	r := &Reconciler{
		lastPolled:    map[string]time.Time{},
		liveness:      map[string]string{},
		wipeThreshold: 20 * time.Minute,
		now:           func() time.Time { return clock },
	}
	live := []*inflightAttempt{{
		entry:  planEntry{TickID: "a2"},
		marker: attemptHandle{TickID: "a2", Attempt: 8, JobID: "job-a2"},
	}}
	r.excuseWindow(live, 6*time.Minute)
	if len(r.Journal()) != 0 {
		t.Errorf("journal = %v, want silence: an ordinary gate is not news", r.Journal())
	}
}
