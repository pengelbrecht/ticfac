package reconcile

import (
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// hn6's cloud run run_8511bc66… (2026-09-30): max_parallel let the window
// admit a third worker while the orchestrator and two workers held all three
// of the account's container slots; the start waited in the door until its
// client timed out, the supervisor read that as remote_transient, and the
// run spent its whole continuation cap of twelve on it and failed.

// slotCounter counts the jobs holding a substrate slot: from a start that
// succeeded until the first status that says the job is over — the moment the
// factory's door reclaims a settled worker's container.
type slotCounter struct {
	mu   sync.Mutex
	live map[string]bool
	peak int
}

type slotCountingExecutor struct {
	Executor
	slots *slotCounter
}

func (e *slotCountingExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	handle, err := e.Executor.Start(spec)
	if err == nil {
		e.slots.mu.Lock()
		e.slots.live[spec.JobID] = true
		if n := len(e.slots.live); n > e.slots.peak {
			e.slots.peak = n
		}
		e.slots.mu.Unlock()
	}
	return handle, err
}

func (e *slotCountingExecutor) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := e.Executor.Inspect(handle, cursor)
	if err == nil && status != nil && status.Terminal {
		e.slots.mu.Lock()
		delete(e.slots.live, handle.JobID)
		e.slots.mu.Unlock()
	}
	return status, err
}

// TestTheWindowNeverRunsMoreJobsThanTheSubstrateHasSlotsFor: a declared width
// of three over a substrate with two slots runs the wave two at a time — the
// wave runs narrower, every tick still closes, and no start is ever made for a
// slot the run could count itself out of.
func TestTheWindowNeverRunsMoreJobsThanTheSubstrateHasSlotsFor(t *testing.T) {
	t.Parallel()
	gate := slowGate(3)
	opts := fixtureOptions{gate: gate, jobSlots: 2}
	f := newFixture(t, opts)
	waveOfThree(t, f)

	slots := &slotCounter{live: map[string]bool{}}
	f.wrap = func(inner Executor) Executor {
		return &slotCountingExecutor{Executor: inner, slots: slots}
	}

	_, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}
	if slots.peak > 2 {
		t.Errorf("%d jobs were live at once on a substrate with 2 slots: the window admitted past the capacity, "+
			"which in the cloud is a start that waits in the door for a container until it times out", slots.peak)
	}
	if slots.peak < 2 {
		t.Errorf("never more than %d job was live at once: the capacity narrowed the window below itself", slots.peak)
	}
	if n := countStage(feedStages(t, f.Repo.Dir, "r-fixture"), StageWaitingForCapacity); n != 0 {
		t.Errorf("the run waited for capacity %d time(s): a slot it could count itself out of was asked for", n)
	}
}

// noRoom is a substrate's typed "no capacity" answer to a start — what the
// cloud executor's door refusal says (cloudflaresandbox's no_capacity).
type noRoom struct{}

func (noRoom) Error() string {
	return "the sandbox dispatch door refused (503 no_capacity): 3 of 3 slots held"
}
func (noRoom) NoCapacity() bool { return true }

// fullAccount answers the first `refusals` starts of one tick with noRoom,
// then lets them through: room held by something this run does not count.
type fullAccount struct {
	Executor
	mu       *sync.Mutex
	tick     string
	refusals *int
}

func (e *fullAccount) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	e.mu.Lock()
	if tickOfJob(spec.JobID) == e.tick && *e.refusals > 0 {
		*e.refusals--
		e.mu.Unlock()
		return nil, noRoom{}
	}
	e.mu.Unlock()
	return e.Executor.Start(spec)
}

// TestAStartWithNoRoomWaitsAndNeverSpendsAContinuation: the door answering
// "no capacity" is a wait. The run records it once, rests, asks again, and
// completes — no stop, no remote_transient, no automatic continuation.
func TestAStartWithNoRoomWaitsAndNeverSpendsAContinuation(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{gate: wideGate, autoResumeCap: 1}
	f := newFixture(t, opts)
	var mu sync.Mutex
	refusals := 3
	f.wrap = func(inner Executor) Executor {
		return &fullAccount{Executor: inner, mu: &mu, tick: "a1", refusals: &refusals}
	}

	_, result, err := f.supervise(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s (halt: %s)",
			result.Closed, result.State, result.Reason, result.Halt)
	}
	if len(result.Resumes) != 0 {
		t.Errorf("the run was continued %d time(s) over a start with no room (%+v): a wait for capacity is not a "+
			"stop", len(result.Resumes), result.Resumes)
	}
	events := feedStages(t, f.Repo.Dir, "r-fixture")
	if n := countStage(events, StageWaitingForCapacity); n != 1 {
		t.Errorf("the feed says %s %d time(s), want once for the one wait", StageWaitingForCapacity, n)
	}
	if n := countStage(events, StageStartFailed); n != 0 {
		t.Errorf("a start with no room was recorded as %s %d time(s): it is a wait, not a failure", StageStartFailed, n)
	}
}

// TestAStartThatOutwaitsItsBoundStopsResumablyOutsideTheCap: a substrate full
// for longer than CapacityWaitBound stops the run as no_capacity — resumable,
// and NOT counted against the continuation cap, so twice past a cap of one
// still completes rather than halting the way hn6's run did at twelve.
func TestAStartThatOutwaitsItsBoundStopsResumablyOutsideTheCap(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{gate: wideGate, autoResumeCap: 1}
	f := newFixture(t, opts)
	var mu sync.Mutex
	// Two whole waits and a little: each wait asks bound/retry+1 times.
	perWait := int(CapacityWaitBound/CapacityRetry) + 1
	refusals := 2*perWait + 2
	f.wrap = func(inner Executor) Executor {
		return &fullAccount{Executor: inner, mu: &mu, tick: "a1", refusals: &refusals}
	}

	_, result, err := f.supervise(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s (halt: %s)",
			result.Closed, result.State, result.Reason, result.Halt)
	}
	if len(result.Resumes) != 2 {
		t.Fatalf("resumes %+v, want the two no_capacity stops", result.Resumes)
	}
	for _, resume := range result.Resumes {
		if resume.Reason != RefusedNoCapacity {
			t.Errorf("a resume over %q, want %q", resume.Reason, RefusedNoCapacity)
		}
	}
}
