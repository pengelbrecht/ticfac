package reconcile

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Tick 9pz: a finish must not stop the window admitting.
//
// The window admits as soon as a slot frees — it does not wait for a wave. But
// the finish used to run INLINE in the run loop, and the loop admits at the
// top, so for the whole of a collect, an integrate, a gate and a close the run
// admitted nothing however many ticks were ready. Measured on this
// repository's own epic feeds, that cost a median of 7m19s per settled tick, of
// which the gate was 89%.
//
// These tests are about the two halves of the repair, and they are written so
// that the OLD code fails them for the right reason rather than by timing out
// on something unrelated.

// waveOfThree puts three ticks in one wave, so a width of two leaves a third
// waiting for a slot. The role jobs stay behind all of them.
func waveOfThree(t *testing.T, f *fixture) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{
		"rv": {"a1", "a2", "b1"},
		"co": {"a1", "a2", "b1", "rv"},
	}
	f.Tracker.write(t, state)
}

// admissionGate is a declared gate whose command WAITS for a third tick to be
// dispatched, and fails if it never is.
//
// The gate is the right place to put the question because the gate is where the
// time goes: a run that cannot admit through its gate cannot admit at all. The
// bound is the test's own, well under the fixture's GateTimeout, so the old code
// fails with this command's own message rather than with a timeout somewhere
// else.
func admissionGate(signal string) string {
	return `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
admitted = { command = "i=0; while [ $i -lt 400 ] && [ ! -f '` + signal + `' ]; do sleep 0.05; i=$((i+1)); done; test -f '` + signal + `' || { echo 'no third tick was admitted while this gate ran'; exit 9; }", description = "the window admits while this gate runs" }
`
}

// TestTheWindowAdmitsWhileASettledAttemptIsBeingFinished is tick 9pz's
// acceptance: with a width of two and three ready ticks, the third is dispatched
// into the slot the first settled attempt freed — WHILE that attempt is being
// integrated and gated, not after.
//
// On the code before this tick it fails with the gate's own words:
//
//	the integrated gate on <sha> did not pass for a1: admitted (fail). ...
//
// because the run is inside finishTick(a1) for the whole of that gate and
// cannot reach the admission at the top of its loop.
func TestTheWindowAdmitsWhileASettledAttemptIsBeingFinished(t *testing.T) {
	t.Parallel()
	signal := filepath.Join(t.TempDir(), "third-dispatched")
	gate := admissionGate(signal)

	f := newFixture(t, fixtureOptions{gate: gate, mode: "linger-until"})
	waveOfThree(t, f)

	// a2 keeps thinking until the third tick is dispatched, so the run really
	// does have to admit from a free slot rather than from an empty window.
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal}, f.Runner[1:]...)
	f.wrap = func(inner Executor) Executor {
		return &dispatchSignal{Executor: inner, tick: "b1", path: signal}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}

	// The feed's own account of it: b1 was dispatched after a1 settled and
	// BEFORE a1's gate passed.
	var settled, dispatched, gated int
	for i, event := range r.Journal() {
		switch {
		case event.Tick == "a1" && event.Stage == StageWaiting && strings.Contains(event.Detail, "settled as"):
			if settled == 0 {
				settled = i + 1
			}
		case event.Tick == "b1" && event.Stage == StageDispatched:
			dispatched = i + 1
		case event.Tick == "a1" && event.Stage == StageGatePassed:
			gated = i + 1
		}
	}
	if settled == 0 || dispatched == 0 || gated == 0 {
		t.Fatalf("the feed is missing one of the three events: a1 settled %d, b1 dispatched %d, a1 gated %d",
			settled, dispatched, gated)
	}
	if dispatched > gated {
		t.Errorf("b1 was dispatched at %d, after a1's gate passed at %d: the run admitted nothing for the whole "+
			"of the finish, which is the defect 9pz is about", dispatched, gated)
	}
	if dispatched < settled {
		t.Errorf("b1 was dispatched at %d, before a1 even settled at %d: the width was exceeded", dispatched, settled)
	}
}

// countingExecutor counts the attempts whose worker exists right now: started
// and not yet disposed of. It is how "no N+1 live workers" is a number rather
// than an impression.
//
// The count moves on the executor's own verbs, not on the reconciler's
// bookkeeping, and a second disposal of the same attempt — the close retiring a
// branch whose worktree already went — is not a second worker leaving.
type countingExecutor struct {
	Executor
	state *workerCount
}

type workerCount struct {
	open int
	peak int
	gone map[string]bool
}

func (e *countingExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	handle, err := e.Executor.Start(spec)
	if err == nil {
		e.state.open++
		if e.state.open > e.state.peak {
			e.state.peak = e.state.open
		}
	}
	return handle, err
}

func (e *countingExecutor) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	err := e.Executor.Dispose(handle, opts)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%s#%d", handle.JobID, handle.Attempt)
	if !e.state.gone[id] {
		e.state.gone[id] = true
		e.state.open--
	}
	return nil
}

// TestTheDeclaredWidthIsStillHonouredThroughAFinish: the slot a finish frees is
// a real slot, and freeing it must not let the run run wider than it said. With
// a declared width of two, no more than two workers exist at any moment.
func TestTheDeclaredWidthIsStillHonouredThroughAFinish(t *testing.T) {
	t.Parallel()
	signal := filepath.Join(t.TempDir(), "third-dispatched")
	gate := admissionGate(signal)

	f := newFixture(t, fixtureOptions{gate: gate, mode: "linger-until"})
	waveOfThree(t, f)
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal}, f.Runner[1:]...)

	count := &workerCount{gone: map[string]bool{}}
	f.wrap = func(inner Executor) Executor {
		return &countingExecutor{
			Executor: &dispatchSignal{Executor: inner, tick: "b1", path: signal},
			state:    count,
		}
	}

	_, result, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}
	if count.peak > 2 {
		t.Errorf("%d workers existed at once under a declared width of 2: freeing the finished tick's slot "+
			"widened the run past what it said", count.peak)
	}
	if count.peak < 2 {
		t.Errorf("never more than %d worker existed at once: the fixture is not exercising a window at all", count.peak)
	}
}

// TestARefusalDuringAFinishStopsTheRunAndAdmitsNothingAfterIt.
//
// The finish being a state machine the loop advances is exactly the change that
// could weaken this, so it is asserted rather than assumed: the moment a tick's
// gate refuses, the run stops — no further tick is admitted, and nothing else
// integrates over a tree the refusal just said nothing stands behind.
func TestARefusalDuringAFinishStopsTheRunAndAdmitsNothingAfterIt(t *testing.T) {
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
	waveOfThree(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: refusing})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 0 {
		t.Fatalf("closed %v behind a gate that refuses everything", result.Closed)
	}

	refused := -1
	for i, event := range r.Journal() {
		if event.Stage == StageGateFailed {
			refused = i
			break
		}
	}
	if refused < 0 {
		t.Fatal("no gate refused: the fixture is not exercising the refusal at all")
	}
	for _, event := range r.Journal()[refused+1:] {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			t.Errorf("%s was %s after a gate refused at %d: the run admitted work past a refusal",
				event.Tick, event.Stage, refused)
		case StageIntegrated, StageGatePassed, StageClosed:
			t.Errorf("%s reached %s after a gate refused at %d: the run integrated over a tree it had just been "+
				"told nothing stands behind", event.Tick, event.Stage, refused)
		}
	}
}

// TestACollectedWorkerIsReleasedBeforeItsGateRuns is tick 9pz's first half: a
// worker is FINISHED once its evidence is collected, so its credential and
// worktree go at the collect and not after a close it is no part of.
//
// What it must NOT take with it is the branch — that is what every refusal path
// reads next, and what the close retires afterwards — so both facts are
// asserted together.
func TestACollectedWorkerIsReleasedBeforeItsGateRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic", result.Closed)
	}

	released, gated, retired := 0, 0, 0
	for i, event := range r.Journal() {
		if event.Tick != "a1" {
			continue
		}
		switch {
		case event.Stage == StageCleanedUp && strings.Contains(event.Detail, "is collected"):
			if released == 0 {
				released = i + 1
			}
		case event.Stage == StageGatePassed:
			gated = i + 1
		case event.Stage == StageCleanedUp && strings.Contains(event.Detail, "is closed"):
			retired = i + 1
		}
	}
	if released == 0 {
		t.Fatalf("a1's worker was never released at the collect: stages %v", r.Stages("a1"))
	}
	if gated == 0 || retired == 0 {
		t.Fatalf("the feed is missing a1's gate (%d) or its retirement (%d): stages %v", gated, retired, r.Stages("a1"))
	}
	if released > gated {
		t.Errorf("a1's worker was released at %d, after its gate passed at %d: the run held a worker that had "+
			"already gone home for the whole of the gate", released, gated)
	}
	if retired < gated {
		t.Errorf("a1's attempt was retired at %d, before its gate passed at %d: a clean-up before the close "+
			"throws away the only copy of what was closed", retired, gated)
	}
}
