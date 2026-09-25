package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
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

// trackerMutator is the executor wrapper a mid-run test uses (tick 3h0): the
// moment one named tick is started, one mutation is applied to the tracker's
// state — the shape of a person changing a RUNNING epic, which is what the
// live-epic tests are about. It runs on the run's own goroutine (the fixture
// runs the reconciler in the foreground), and it fires exactly once however
// many attempts the tick gets.
type trackerMutator struct {
	Executor
	tracker *fakeTracker
	on      string
	mutate  func(*trackerState)
	done    bool
}

func (e *trackerMutator) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if !e.done && tickOfJob(spec.JobID) == e.on {
		e.done = true
		state, err := e.tracker.load()
		if err != nil {
			// The run is mid-dispatch on this goroutine; a Fatal here would
			// goexit out of the reconciler's own stack. The error is said, and
			// the assertions below find the missing mutation anyway.
			os.Stderr.WriteString("the mid-run tracker mutation could not read the state: " + err.Error() + "\n")
		} else {
			e.mutate(&state)
			if err := e.tracker.save(state); err != nil {
				os.Stderr.WriteString("the mid-run tracker mutation could not be written: " + err.Error() + "\n")
			}
		}
	}
	return e.Executor.Start(spec)
}

// addTick is the mutation a person absorbing a finding into the running epic
// makes: a new child of the epic, open, with no edges of its own. The tracker
// layers it into a wave the moment it appears.
func addTick(state *trackerState, id string) {
	state.Ticks[id] = tk.Tick{ID: id, Title: "tick " + id, Status: "open", Type: "task", Parent: "qeu", Priority: 2}
	state.Order = append(state.Order, id)
}

// layerDynamically replaces the fixture's declared waves with the tracker's
// own layering (edges in state.BlockedBy), so a tick the test adds while the
// run is going is layered the moment it appears — exactly as tk layers a tick
// a person creates — rather than being absent from a wave list cut before it
// existed.
func layerDynamically(t *testing.T, f *fixture, edges map[string][]string) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = edges
	f.Tracker.write(t, state)
}

// TestATickCreatedAfterTheRunStartedIsDispatchedByThatRun is the first
// acceptance of tick 3h0, in the production incident's own shape: a tick
// created under the epic WHILE the run is going — a person absorbing a
// finding into it — is sequenced, dispatched and closed by that same run,
// with no restart. Before 3h0 the re-derivation refreshed only the ticks the
// plan already carried, so a new child never entered the plan at all: the
// run finished over work nobody dispatched, and a person had to kill it so a
// resume could replan from scratch.
func TestATickCreatedAfterTheRunStartedIsDispatchedByThatRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	layerDynamically(t, f, map[string][]string{
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	})

	// The moment a1 is started — wave 1 in flight, the run live — a new child
	// n9 appears under the epic.
	f.wrap = func(inner Executor) Executor {
		return &trackerMutator{Executor: inner, tracker: f.Tracker, on: "a1",
			mutate: func(state *trackerState) { addTick(state, "n9") }}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 6 {
		t.Fatalf("closed %v, want every tick of the epic including n9, created mid-run; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}

	dispatched := map[string]int{}
	for i, event := range r.Journal() {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			if _, seen := dispatched[event.Tick]; !seen {
				dispatched[event.Tick] = i
			}
		}
	}
	if _, ok := dispatched["n9"]; !ok {
		t.Fatalf("n9 was never dispatched by the running run: %v", dispatched)
	}
	var said bool
	for _, event := range r.Journal() {
		if event.Stage == StageReplanned && event.Tick == "n9" {
			said = true
		}
	}
	if !said {
		t.Errorf("no %s line for n9: the plan admitted a tick created after the run started and the feed "+
			"never said so, so to an operator it reads as work nobody dispatched", StageReplanned)
	}
}

// TestABlockedByEdgeAddedMidRunIsHonouredBeforeTheBlockedTickIsDispatched is
// the second acceptance of tick 3h0: a blocked_by edge added while the run is
// going — the exact epic-yoh shape, where the close-out was made blocked-by
// four freshly absorbed ticks and the live run dispatched it over all four —
// holds the blocked tick until the blocker closes. Here the edge lands on a
// work tick and its blocker is another new child, so the run has to admit the
// blocker AND sequence the blocked tick behind it.
func TestABlockedByEdgeAddedMidRunIsHonouredBeforeTheBlockedTickIsDispatched(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	layerDynamically(t, f, map[string][]string{
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	})

	// While a1 is being dispatched — b1 still queued behind the width — a new
	// child n9 appears AND b1 is made blocked-by it.
	f.wrap = func(inner Executor) Executor {
		return &trackerMutator{Executor: inner, tracker: f.Tracker, on: "a1",
			mutate: func(state *trackerState) {
				addTick(state, "n9")
				state.BlockedBy["b1"] = []string{"n9"}
			}}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 6 {
		t.Fatalf("closed %v, want every tick of the epic including n9, the mid-run blocker; the run ended %s: %+v",
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
	if _, ok := dispatched["n9"]; !ok {
		t.Fatalf("n9 was never dispatched: %v", dispatched)
	}
	if dispatched["b1"] < closed["n9"] {
		t.Errorf("b1 was dispatched at %d, before n9 — the tick it was made blocked-by mid-run — closed at %d: "+
			"the edge added while the run was going was not honoured before the blocked tick was dispatched",
			dispatched["b1"], closed["n9"])
	}
}

// TestTheIncidentsOwnShapeACloseoutMadeBlockedByAbsorbedTicksMidRun is
// tick 3h0's production incident composed, end to end — the two behaviours
// the individual tests drive, landed on the incident's own subject. On
// epic-yoh (2026-09-24), while the review ran, four ticks were absorbed into
// the epic AND the close-out was made blocked-by each of them; the live run
// — whose plan carried none of it — went straight from the review into the
// close-out and opened the epic PR over four open children. The first two
// tests drive each behaviour alone (the admission on a work queue, the edge
// on a work tick); this one drives them together on the close-out, because
// that is the composition a person actually performed and the one whose
// failure is an epic closed over an unmet definition of done: a close-out
// made blocked-by a tick absorbed mid-run is worked AFTER that tick, by the
// same run, with no restart.
func TestTheIncidentsOwnShapeACloseoutMadeBlockedByAbsorbedTicksMidRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	layerDynamically(t, f, map[string][]string{
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	})

	// While the review runs — the last thing before the close-out, exactly
	// where the incident happened — a person absorbs a finding into the epic:
	// a new child n9 appears AND the close-out is made blocked-by it.
	f.wrap = func(inner Executor) Executor {
		return &trackerMutator{Executor: inner, tracker: f.Tracker, on: "rv",
			mutate: func(state *trackerState) {
				addTick(state, "n9")
				state.BlockedBy["co"] = []string{"a1", "a2", "b1", "rv", "n9"}
			}}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 6 {
		t.Fatalf("closed %v, want every tick of the epic — n9 was absorbed while the review ran and the close-out was made "+
			"blocked-by it, so the run must work n9 and only then close the epic; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s, want completed: absorbing a gating finding into a running epic must not need a person "+
			"to kill the run so a resume can replan from scratch: %+v", result.State, result.Failure)
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
	if _, ok := dispatched["n9"]; !ok {
		t.Fatalf("n9 was never dispatched by the running run: %v", dispatched)
	}
	if dispatched["co"] < closed["n9"] {
		t.Errorf("the close-out was dispatched at %d, before n9 — the tick it was made blocked-by while the review ran — "+
			"closed at %d: the incident's own shape, a close-out dispatched over its open blocker", dispatched["co"], closed["n9"])
	}
	if dispatched["co"] < dispatched["n9"] {
		t.Errorf("the close-out was dispatched at %d, before n9 was even dispatched at %d", dispatched["co"], dispatched["n9"])
	}
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
