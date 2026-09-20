package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Tick 8jl: a host suspend must not refuse a live attempt.
//
// The reconciler's own settlement deadline used to be one comparison,
// `now.After(fl.deadline)`, against a deadline parsed out of the durable
// marker. A parsed RFC3339 carries no monotonic reading, so Go compared it in
// WALL time — and a host that suspended aged every live attempt past its bound
// while it slept. The first poll after the lid opened refused all of them at
// once, as RefusedUnaddressed, and stopped the run.
//
// Epic dha woke with four attempts 5 to 7 hours past their wall clocks. Every
// one of them settled SUCCESSFULLY within a minute of the machine waking.

// frozenClock is a host asleep: wall time moves on — the durable dispatch stamp
// recedes into the past — while the run's own clock does not advance at all,
// because the run is not running. It is the cheapest honest model of a suspend
// that a test can build, and it is what the acceptance asks for: advance wall
// time without advancing the run's own clock.
type frozenClock struct{ at time.Time }

func (c *frozenClock) now() time.Time     { return c.at }
func (c *frozenClock) hold(time.Duration) {}

// TestASuspendedHostDoesNotRefuseALiveAttempt is 8jl's acceptance.
//
// The attempt's marker is stamped far enough in the past that its issued budget
// is long spent in CALENDAR terms — which is exactly what waking from a
// suspend looks like — while the run's own clock has not moved, so this run has
// watched the attempt for no time at all. Nothing may be refused on that.
func TestASuspendedHostDoesNotRefuseALiveAttempt(t *testing.T) {
	t.Parallel()
	r := &Reconciler{
		wipeThreshold: 20 * time.Minute,
		opts:          Options{WallSeconds: 3600},
	}
	woke := time.Now()
	clock := &frozenClock{at: woke}
	r.now = clock.now

	// Dispatched seven hours ago in calendar terms, adopted the moment this
	// run woke up. dha's numbers.
	fl := &inflightAttempt{
		deadline:   woke.Add(-6 * time.Hour).Round(0),
		addressing: woke,
	}

	if over, watched, unaddressable := r.unaddressable(fl); unaddressable {
		t.Fatalf("an attempt was declared unaddressable %s past its budget after this run had watched it for %s: "+
			"a host that slept is not an executor that died, and refusing on the calendar alone kills healthy "+
			"runs every time a lid is closed", over, watched)
	}
}

// And the bound still bounds: a run whose OWN clock advances past the grace,
// with the attempt still unsettled, does refuse. This is the half that must
// not be lost in making the other half safe.
func TestAnAttemptNobodyCanAddressIsStillRefused(t *testing.T) {
	t.Parallel()
	r := &Reconciler{
		wipeThreshold: 20 * time.Minute,
		opts:          Options{WallSeconds: 3600},
	}
	woke := time.Now()
	clock := &frozenClock{at: woke}
	r.now = clock.now
	fl := &inflightAttempt{
		deadline:   woke.Add(-6 * time.Hour).Round(0),
		addressing: woke,
	}

	// This run has now been awake, watching, for longer than the grace.
	clock.at = woke.Add(21 * time.Minute)

	over, watched, unaddressable := r.unaddressable(fl)
	if !unaddressable {
		t.Fatal("an attempt past its budget that this run has watched, awake, for longer than the wipe " +
			"threshold was not refused: the bound that catches a dead supervisor is gone")
	}
	if watched < 20*time.Minute {
		t.Errorf("the refusal says this run watched for %s, which is under the grace it is supposed to have spent", watched)
	}
	if over < 6*time.Hour {
		t.Errorf("the refusal says %s past the budget, want the calendar gap", over)
	}
}

// TestTheSettlementDeadlineIsSpentInTheRunsOwnClock is the same statement from
// the other side, end to end: the wait is bounded by the clock the run spends,
// so the existing bound test's clock — advanced only when the run sleeps —
// still reaches the refusal, and reaches it through both halves.
func TestTheSettlementDeadlineIsSpentInTheRunsOwnClock(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	f.wrap = func(inner Executor) Executor { return &alwaysRunningExecutor{Executor: inner} }

	opts := f.options(f.Repo, fixtureOptions{mode: "hang"})
	clock := &testClock{at: time.Now()}
	opts.Now = clock.now
	opts.Sleep = clock.advance
	opts.PollInterval = 2 * time.Second
	opts.WipeThreshold = 20 * time.Second
	opts.StepCap = time.Hour
	opts.WallSeconds = 10

	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var result *Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = r.RunProtected(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the wait for an attempt that never settles did not end: the bound is gone")
	}
	if runErr != nil {
		t.Fatalf("the run should have refused, not errored: %v", runErr)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the refusal is %+v, want %s", result.Failure, RefusedUnaddressed)
	}
	// And it says both halves, so a person reading it can tell a dead
	// supervisor from a host that was merely asleep.
	if !strings.Contains(result.Failure.Message, "watched it for") {
		t.Errorf("the refusal does not say how long this run watched the attempt: %q", result.Failure.Message)
	}
}
