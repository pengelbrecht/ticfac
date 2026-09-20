package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The stale-wave defect (tick g50), end to end.
//
// `tk` assigns wave numbers by layering the dependency graph at the moment it
// is asked. A run that reads the graph once carries those numbers for its whole
// life, so a tick whose blocker was open at run start stays in the wave that
// described a graph which no longer exists — and the window, which refuses to
// mix waves, refuses to dispatch it however many slots are free. Measured on
// epic ncv: three free slots, two ready ticks, zero dispatches, and a fresh
// read of the same graph putting all three in one wave.
//
// What the fixture arranges is exactly that sequence, with nothing simulated:
// a1 and a2 are dispatched together, a1 settles and CLOSES while a2 is still
// thinking, and b1 — which a1 alone blocked — must be dispatched by this same
// run, beside the live a2, with no restart.

// dispatchSignal is an executor that touches a file the moment one named tick
// is started. It is how the lingering worker learns that the dispatch the test
// is about has happened, so the test waits on the event rather than on a
// duration it guessed.
type dispatchSignal struct {
	Executor
	tick string
	path string
}

func (e *dispatchSignal) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if tickOfJob(spec.JobID) == e.tick {
		_ = os.WriteFile(e.path, []byte(e.tick+" was dispatched\n"), 0o644)
	}
	return e.Executor.Start(spec)
}

// TestABlockerClosingMidRunAdmitsItsDependentWithoutARestart is the acceptance
// for tick g50: a tick whose blockers close while the run is live is dispatched
// by that same run.
func TestABlockerClosingMidRunAdmitsItsDependentWithoutARestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate, mode: "linger-until"})

	// The dependency edges the fixture's tracker layers by. b1 is blocked by
	// a1 and by NOTHING else — so the moment a1 closes, b1 and the still-live
	// a2 are peers, which is what the run has to notice. The role jobs are
	// sequenced behind all of the work, the way an epic skeleton is.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{
		"b1": {"a1"},
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	}
	f.Tracker.write(t, state)

	// a2 keeps thinking until b1 is dispatched. If the run never dispatches
	// b1 beside it — the defect — a2 gives up on its own bound, the run
	// finishes b1 afterwards, and the assertion below is what fails.
	signal := filepath.Join(f.Root, "b1-dispatched")
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal}, f.Runner[1:]...)
	f.wrap = func(inner Executor) Executor {
		return &dispatchSignal{Executor: inner, tick: "b1", path: signal}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		// The refusal, not just the count (tick atn). This test failed once in
		// a loaded parallel suite with nothing but "closed []" to go on, which
		// is the same message whether the run was refused, starved or never
		// dispatched at all — and the reason is the whole diagnosis.
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}

	dispatched := map[string]int{}
	closed := map[string]int{}
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
	for _, tick := range []string{"a1", "a2", "b1"} {
		if _, ok := dispatched[tick]; !ok {
			t.Fatalf("%s was never dispatched: %v", tick, dispatched)
		}
	}
	if dispatched["b1"] < closed["a1"] {
		t.Fatalf("b1 was dispatched at %d, before its blocker a1 closed at %d: the run started work that "+
			"depends on unfinished work", dispatched["b1"], closed["a1"])
	}
	if dispatched["b1"] > closed["a2"] {
		t.Errorf("b1 was dispatched at %d, after a2 closed at %d: a1 — b1's only blocker — had closed while "+
			"a2 was still in flight and a slot was free, so b1 waited on a wave number describing a graph "+
			"that no longer existed. That is one whole run incarnation per dependency edge (tick g50)",
			dispatched["b1"], closed["a2"])
	}

	// And the feed says why, because an operator watching a tick from "wave 2"
	// being dispatched beside wave 1 has to be able to read the reason.
	var said bool
	for _, event := range r.Journal() {
		if event.Stage == StageReplanned && event.Tick == "b1" {
			said = true
		}
	}
	if !said {
		t.Errorf("no %s line for b1: the plan moved it up a wave and the feed never said so", StageReplanned)
	}
}

// TestTheRePlanKeepsTheBoundaryItIsAbout is the negative half, on the same
// fixture: re-deriving the plan must not turn the wave boundary off. b1 is
// blocked by BOTH a1 and a2 here, so no settlement of either one alone makes
// it admissible, and it must still be dispatched only after both have closed.
func TestTheRePlanKeepsTheBoundaryItIsAbout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})

	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{
		"b1": {"a1", "a2"},
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	}
	f.Tracker.write(t, state)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}

	dispatched := map[string]int{}
	closed := map[string]int{}
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
	for _, blocker := range []string{"a1", "a2"} {
		if dispatched["b1"] < closed[blocker] {
			t.Errorf("b1 was dispatched at %d, before %s closed at %d: the re-derived plan crossed the "+
				"boundary it exists to keep", dispatched["b1"], blocker, closed[blocker])
		}
	}
	for _, role := range []string{"rv", "co"} {
		for _, work := range []string{"a1", "a2", "b1"} {
			if dispatched[role] < closed[work] {
				t.Errorf("%s was dispatched at %d, before %s closed at %d: a role job is about a FINISHED epic",
					role, dispatched[role], work, closed[work])
			}
		}
	}
}

// TestAPlanThatHasNotMovedIsNotRePlanned: the re-derivation is promoted only
// when it actually moves a tick. A run whose graph says the same thing it said
// before keeps the plan it has, and says nothing — a feed line per settlement
// saying "nothing changed" is noise in the one place an operator reads to find
// out what did.
func TestAPlanThatHasNotMovedIsNotRePlanned(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	r, _, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, event := range r.Journal() {
		if event.Stage == StageReplanned {
			t.Errorf("a %s line on a run whose waves never moved: %q", StageReplanned, event.Detail)
		}
	}
}
