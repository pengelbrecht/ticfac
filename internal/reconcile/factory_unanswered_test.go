package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// attempt_unaddressed and wiped on a factory-backed attempt are asked of the
// factory, then answered in-run — not handed to a person (the operator's
// decision after #177: "a refusal whose only actor is a person is a defect
// unless it really needs judgment").
//
// Both refusals say "nobody can say whether this attempt is running". For a
// local executor that is true: the run is the only party that could know, and
// it does not. For an executor whose jobs live at the factory (#160) the
// factory keeps the job's records and answers for it by identity, so the
// run asks it again before deciding anything: a settled answer is collected,
// a running one is waited on (a bounded number of times), and only an
// attempt the factory still cannot answer for is released by the run and
// dispatched again in-run, at most maxOperationalRetries times, before it is
// held as it always was.

// factoryFlavoured is a local executor restated as a factory's: its jobs
// "live at the factory" (FactoryJobs), and its answers are scripted per
// tick. Every answer it does not script is the inner executor's TERMINAL
// answer, waited for, so a test's timing is the clock's and never a race
// with a real worker process.
type factoryFlavoured struct {
	Executor
	factory bool
	script  *factoryScript
}

// factoryScript is what the "factory" answers for a tick's jobs: `hidden`
// answers of `state` for the tick's first try, then the real one — or, with
// forever, `state` for every try.
type factoryScript struct {
	mu      sync.Mutex
	tick    string
	state   string
	hidden  int
	forever bool
	asks    int
	starts  int
	// clock is the run's own clock: every scripted answer jumps it past
	// the wipe threshold, a host that slept between two looks, so the next
	// keepalive poll of a job that has not settled reads Wiped.
	clock *lockedClock
}

func (e *factoryFlavoured) JobsLiveAtFactory() bool { return e.factory }

func (e *factoryFlavoured) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if strings.Contains(spec.JobID, "/tick-"+e.script.tick+"/attempt-") {
		e.script.mu.Lock()
		e.script.starts++
		e.script.mu.Unlock()
	}
	return e.Executor.Start(spec)
}

func (e *factoryFlavoured) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	s := e.script
	if strings.Contains(handle.JobID, "/tick-"+s.tick+"/attempt-") {
		s.mu.Lock()
		s.asks++
		scripted := s.forever || (s.starts <= 1 && s.hidden > 0)
		if scripted && !s.forever {
			s.hidden--
		}
		state := s.state
		s.mu.Unlock()
		if scripted {
			if s.clock != nil {
				s.clock.advance(25 * time.Second)
			}
			status, err := e.Executor.Inspect(handle, cursor)
			if err != nil {
				return nil, err
			}
			status.State, status.Terminal = state, false
			return status, nil
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, err := e.Executor.Inspect(handle, cursor)
		if err != nil || status.Terminal || time.Now().After(deadline) {
			return status, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *factoryScript) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// runOnAWipingClock runs the fixture on the run's own clock, which every
// scripted answer jumps past the wipe threshold, so the second look at a
// scripted attempt reads Wiped: the keepalive half of "nobody can say
// whether it is running", produced on demand.
func runOnAWipingClock(t *testing.T, f *fixture, factory bool, script *factoryScript) (*Reconciler, *Result) {
	t.Helper()
	f.wrap = func(inner Executor) Executor {
		return &factoryFlavoured{Executor: inner, factory: factory, script: script}
	}
	opts := f.options(f.Repo, fixtureOptions{})
	clock := &lockedClock{at: time.Now()}
	script.clock = clock
	opts.Now = clock.now
	opts.Sleep = clock.advance
	opts.PollInterval = 2 * time.Second
	opts.WipeThreshold = 20 * time.Second
	opts.StepCap = time.Hour
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
	case <-time.After(5 * time.Minute):
		t.Fatal("the run did not end")
	}
	if runErr != nil {
		t.Fatalf("the run did not finish: %v", runErr)
	}
	return r, result
}

// lockedClock is testClock safe to read from the run's goroutines.
type lockedClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *lockedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *lockedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// The factory answers when asked again: a1's first try reads running long
// enough to be Wiped, the run asks the factory, the factory says it settled,
// and the run collects it — no hold, no redispatch, nobody asked.
func TestAWipedFactoryAttemptIsAskedOfTheFactoryAndCollected(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	script := &factoryScript{tick: "a1", state: subprocess.StateRunning, hidden: 2}
	r, result := runOnAWipingClock(t, f, true, script)

	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want completed: the factory answered for a1 when asked (a1 stages %v)",
			result.State, result.Failure, r.Stages("a1"))
	}
	line, ok := journalLine(r, "a1", StageWaiting)
	found := false
	for _, e := range r.Journal() {
		if e.Tick == "a1" && strings.Contains(e.Detail, "asked the factory again") {
			found, line = true, e.Detail
			break
		}
	}
	if !found {
		t.Errorf("the run's question to the factory is not on the feed (last waiting line %q, ok %v)", line, ok)
	}
	if n := script.startCount(); n != 1 {
		t.Errorf("a1 was dispatched %d times, want 1: the factory's answer is collected, not dispatched over", n)
	}
	for _, stage := range []string{StageTickHeld, StageRunHeld, StageRedispatched} {
		if _, bad := journalLine(r, "a1", stage); bad {
			t.Errorf("a1 reached %s although the factory answered for it: %v", stage, r.Stages("a1"))
		}
	}
}

// The factory cannot answer, again and again: a1 reads lost for every try.
// Each unanswered try is released by the run and dispatched again in-run,
// maxOperationalRetries times, and then a1 is held as before — while every
// tick that does not wait behind it is worked to the end.
func TestAFactoryAttemptNobodyCanAnswerForIsRetriedInRunThenHeld(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	script := &factoryScript{tick: "a1", state: subprocess.StateLost, forever: true}
	r, result := runOnAWipingClock(t, f, true, script)

	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.TickID != "a1" ||
		(result.Failure.Reason != RefusedWiped && result.Failure.Reason != RefusedUnaddressed) {
		t.Fatalf("the run ended %s (%+v), want failed holding a1 on wiped/unaddressed", result.State, result.Failure)
	}
	if n := script.startCount(); n != 1+maxOperationalRetries {
		t.Errorf("a1 was dispatched %d times, want %d (one try and %d in-run retries): %v", n,
			1+maxOperationalRetries, maxOperationalRetries, r.Stages("a1"))
	}
	retries := 0
	for _, e := range r.Journal() {
		if e.Tick == "a1" && e.Stage == StageRedispatched && strings.Contains(e.Detail, "the factory") {
			retries++
		}
	}
	if retries != maxOperationalRetries {
		t.Errorf("%d in-run redispatches of a1 name the factory, want %d", retries, maxOperationalRetries)
	}
	if _, held := journalLine(r, "a1", StageTickHeld); !held {
		t.Errorf("a1 is not held once its retries are spent: %v", r.Stages("a1"))
	}
	a2, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Status != "closed" {
		t.Errorf("a2 is %s: a1's hold stopped a tick that does not wait behind it", a2.Status)
	}
}

// A LOCAL executor has no factory to ask: a Wiped attempt is held exactly as
// it always was.
func TestAWipedLocalAttemptIsHeldAsBefore(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	script := &factoryScript{tick: "a1", state: subprocess.StateRunning, forever: true}
	r, result := runOnAWipingClock(t, f, false, script)

	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.TickID != "a1" ||
		result.Failure.Reason != RefusedWiped {
		t.Fatalf("the run ended %s (%+v), want failed holding a1 on wiped", result.State, result.Failure)
	}
	if n := script.startCount(); n != 1 {
		t.Errorf("a1 was dispatched %d times, want 1: a local attempt nobody can address is never dispatched over", n)
	}
	for _, e := range r.Journal() {
		if e.Tick == "a1" && strings.Contains(e.Detail, "asked the factory again") {
			t.Errorf("a local attempt was 'asked of the factory': %s", e.Detail)
		}
	}
}

// Which held refusals are retried in-run because the factory could not
// answer for them: wiped and unaddressed, factory-backed, marked so.
//
// short: a table over a pure function; nothing is dispatched.
func TestOnlyAFactoryAttemptTheFactoryCouldNotAnswerForIsRetriedInRun(t *testing.T) {
	t.Parallel()
	for _, refusal := range []*Refusal{
		{Reason: RefusedWiped, factoryUnanswered: true},
		{Reason: RefusedUnaddressed, factoryUnanswered: true},
	} {
		if !redispatchesInRun(refusal) {
			t.Errorf("%+v is held; the factory could not answer for it, so it is retried in-run", refusal)
		}
	}
	for _, refusal := range []*Refusal{
		{Reason: RefusedWiped},
		{Reason: RefusedUnaddressed},
		{Reason: RefusedBoundary, factoryUnanswered: true},
		{Reason: RefusedRejectedWork, factoryUnanswered: true},
	} {
		if redispatchesInRun(refusal) {
			t.Errorf("%+v is retried in-run; it must stay held", refusal)
		}
	}
}
