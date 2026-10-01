package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// One tick's spent ladder holds that tick, not the run (epic hn6, run_ee8e).
//
// 378's conflict outlived every resolve and its ladder was at the ceiling, so
// merge_failed was terminal for 378 — and the run stopped with it: zl1 and
// 0rx "still running and the run is stopping for another tick's refusal", yjq
// settled and never finished. None of them depended on 378, and nothing 378
// did had reached the integration branch: a refusal raised before an
// attempt's work merges says nothing about the tree every other tick gates
// on. So the refused tick is held, every tick that does not wait behind it is
// worked to the end, and the run ends — failed, naming the held tick and its
// refusal — only once nothing else can progress.

// signalOnSecondTry touches signal when a tick's SECOND try is torn down:
// the first try's rejection redispatches it (the ladder's one further try at
// the ceiling), and the second one's is the terminal refusal.
type signalOnSecondTry struct {
	Executor
	tick   string
	signal string
	state  *tryCount
}

type tryCount struct {
	mu     sync.Mutex
	starts int
}

func (e *signalOnSecondTry) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if strings.Contains(spec.JobID, "/tick-"+e.tick+"/attempt-") {
		e.state.mu.Lock()
		e.state.starts++
		e.state.mu.Unlock()
	}
	return e.Executor.Start(spec)
}

func (e *signalOnSecondTry) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	err := e.Executor.Dispose(handle, opts)
	e.state.mu.Lock()
	second := e.state.starts >= 2
	e.state.mu.Unlock()
	if second && strings.Contains(handle.JobID, "/tick-"+e.tick+"/attempt-") {
		_ = os.WriteFile(e.signal, nil, 0o644)
	}
	return err
}

func TestATicksTerminalRefusalHoldsOnlyThatTick(t *testing.T) {
	t.Parallel()
	// a1 writes under the tracker's authority on every try: rejected on its
	// merits, redispatched once at the ceiling, and refused for good. a2 is
	// still thinking when that happens — it lingers until a1's second try is
	// torn down — and b1 waits behind a1.
	signal := filepath.Join(t.TempDir(), "a1-refused")
	f := newFixture(t, fixtureOptions{gate: ceilingGate, mode: "linger-until"})
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal, "BOUNDARY_TICK=a1"},
		f.Runner[1:]...)
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{"b1": {"a1"}}
	f.Tracker.write(t, state)
	count := &tryCount{}
	f.wrap = func(inner Executor) Executor {
		return &signalOnSecondTry{Executor: inner, tick: "a1", signal: signal, state: count}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: ceilingGate, mode: "linger-until"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}

	// The run ends failed, naming the held tick and its refusal.
	if result.State != runstate.StateFailed || result.Failure == nil {
		t.Fatalf("the run ended %s (%+v), want failed on a1's refusal", result.State, result.Failure)
	}
	if result.Failure.TickID != "a1" || result.Failure.Reason != RefusedBoundary {
		t.Errorf("the run's refusal is %s on %s, want %s on a1", result.Failure.Reason, result.Failure.TickID,
			RefusedBoundary)
	}
	if !strings.Contains(result.Reason, "a1") {
		t.Errorf("the run's reason does not name the held tick: %s", result.Reason)
	}

	// a2 did not depend on a1: it is worked to the end, not abandoned.
	a2, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Status != "closed" {
		t.Errorf("a2 is %s: a1's refusal stopped a tick that does not wait behind it (stages %v)",
			a2.Status, r.Stages("a2"))
	}
	for _, event := range r.Journal() {
		if event.Tick == "a2" && event.Stage == StageWaiting && strings.Contains(event.Detail, "another tick's refusal") {
			t.Errorf("a2 was abandoned for a1's refusal: %s", event.Detail)
		}
	}

	// b1 waits behind a1, and is never dispatched over it.
	if _, ok := journalLine(r, "b1", StageWaitsBehindHeld); !ok {
		t.Errorf("b1 is not recorded as waiting behind the held a1: %v", r.Stages("b1"))
	}
	if contains(r.Stages("b1"), StageDispatched) {
		t.Errorf("b1 was dispatched behind a held blocker")
	}
}

// Which refusals hold only their tick: the ones raised before an attempt's
// work reached the integration branch. A failing gate — whose merge IS on the
// branch — and a refusal about the run itself still stop the run.
//
// short: a table over a pure function; nothing is dispatched.
func TestOnlyARefusalBeforeTheMergeHoldsJustItsTick(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]bool{
		RefusedMerge: true, RefusedCollect: true, RefusedBoundary: true, RefusedUndeclaredTouch: true,
		RefusedWiped: true, RefusedUnaddressed: true, RefusedRejectedWork: true, RefusedFindingInvalid: true,
		RefusedGate: false, RefusedStale: false, RefusedBaseRefresh: false, RefusedEpicAbsent: false,
		RefusedIntegratedHeadMissing: false, RefusedClaimWidth: false, RefusedRoleResult: false,
	} {
		if got := holdsOnlyItsTick(&Refusal{Reason: reason}); got != want {
			t.Errorf("holdsOnlyItsTick(%s) = %v, want %v", reason, got, want)
		}
	}
	if holdsOnlyItsTick(nil) {
		t.Error("no refusal holds a tick")
	}
}
