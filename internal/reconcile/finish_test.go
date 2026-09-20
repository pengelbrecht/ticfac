package reconcile

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// gateOf is the declared gate for the heartbeat tests: one command, whatever
// the test needs it to do, at an undeclared width so exactly one worker is ever
// held and the gate runs with nothing live beside it.
func gateOf(command string) string {
	return `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "` + command + `", description = "the check under test" }
`
}

// gateLines is the feed's account of one tick's gate: the stages, in order,
// between its integration and its verdict.
func gateLines(r *Reconciler, tick string) (started, running, stalled int, firstRunning, released int) {
	for i, event := range r.Journal() {
		if event.Tick != tick {
			continue
		}
		switch event.Stage {
		case StageGateStarted:
			started++
		case StageGateRunning:
			running++
			if firstRunning == 0 {
				firstRunning = i + 1
			}
		case StageGateStalled:
			stalled++
		case StageCleanedUp:
			if released == 0 && strings.Contains(event.Detail, "is collected") {
				released = i + 1
			}
		}
	}
	return started, running, stalled, firstRunning, released
}

// TestARunningGateSaysSoWithNoLiveWorkerBeside is tick 9pz's second acceptance.
//
// Both things a run emitted — feed lines and liveness probes — were driven off
// polling LIVE attempts, and the gate itself said nothing at all between
// `integrated` and `gate_passed`. So a run whose last live worker had settled,
// sitting inside a multi-minute gate, produced exactly the same feed as a
// process that had died: observed live at 32 minutes of total silence.
//
// The fixture is the worst case on purpose. At an undeclared width the run
// holds ONE worker, and since this tick that worker is released at its collect
// — so while a1's gate runs there is provably nothing live to poll, and every
// line the feed carries has to come from the gate itself.
func TestARunningGateSaysSoWithNoLiveWorkerBeside(t *testing.T) {
	t.Parallel()
	gate := gateOf("sleep 3; test -f README.md")
	f := newFixture(t, fixtureOptions{gate: gate})
	opts := fixtureOptions{gate: gate, gateHeartbeat: 200 * time.Millisecond}

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}

	started, running, _, firstRunning, released := gateLines(r, "a1")
	if started != 1 {
		t.Errorf("a1's gate announced its start %d times, want once: %v", started, r.Stages("a1"))
	}
	if running < 2 {
		t.Errorf("a1's three-second gate wrote %d heartbeats at a 200ms cadence: a run inside a gate is "+
			"indistinguishable from a run that has died, which is what 9pz's second half is about", running)
	}
	// And the heartbeats really did come with nothing live beside them: a1's
	// own worker was released before the first of them, and at this width it
	// was the only worker there was.
	if released == 0 {
		t.Fatalf("a1's worker was never released: %v", r.Stages("a1"))
	}
	if firstRunning < released {
		t.Errorf("a1's first gate heartbeat is at %d, before its worker was released at %d: the fixture is not "+
			"exercising the case this test is for", firstRunning, released)
	}
}

// TestAQuietGateIsWarnedAboutAndATalkativeOneIsNot.
//
// dh1's rule, pointed at a check: alive was never the question, and what a
// person needs to know is whether it is getting anywhere. The warning must fire
// on a gate that has produced nothing for longer than the threshold — and must
// NOT fire on one that is printing as it goes, however long it takes, because a
// warning that cries wolf on every healthy long check is one nobody reads.
//
// Neither case refuses anything: both runs close every tick.
func TestAQuietGateIsWarnedAboutAndATalkativeOneIsNot(t *testing.T) {
	t.Parallel()

	quiet := gateOf("sleep 3; test -f README.md")
	f := newFixture(t, fixtureOptions{gate: quiet})
	r, result, err := f.run(f.Repo, fixtureOptions{
		gate: quiet, gateHeartbeat: 200 * time.Millisecond, stallWarn: 900 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("the quiet run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("the quiet gate closed %v: a warning refused something, which it must never do", result.Closed)
	}
	_, _, stalled, _, _ := gateLines(r, "a1")
	if stalled != 1 {
		t.Errorf("a1's silent three-second gate was warned about %d times under a 900ms threshold, want exactly "+
			"one: %v", stalled, r.Stages("a1"))
	}

	talkative := gateOf("i=0; while [ $i -lt 30 ]; do echo working; sleep 0.1; i=$((i+1)); done; test -f README.md")
	g := newFixture(t, fixtureOptions{gate: talkative})
	gr, gResult, err := g.run(g.Repo, fixtureOptions{
		gate: talkative, gateHeartbeat: 200 * time.Millisecond, stallWarn: 900 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("the talkative run: %v", err)
	}
	if len(gResult.Closed) != 5 {
		t.Fatalf("the talkative gate closed %v", gResult.Closed)
	}
	_, running, stalled, _, _ := gateLines(gr, "a1")
	if running < 2 {
		t.Fatalf("the talkative gate wrote %d heartbeats, so it did not run long enough to be a test of the "+
			"warning at all", running)
	}
	if stalled != 0 {
		t.Errorf("a gate that printed every 100ms for three seconds was warned about %d times: a stall warning "+
			"that fires on a check which is plainly working is noise, and noise is how a real one gets ignored",
			stalled)
	}
}
