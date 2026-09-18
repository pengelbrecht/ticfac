package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The wall clock FIRING is a feed event (tick emk) — the incident's second
// defect, the half the split left here (the hard stop is rj0's).
//
// In the Phase 3 run the bound fired and the executor delivered its stop at
// every poll for 26+ minutes while the feed's last line stayed `dispatched`
// from 85 minutes earlier: every stop observation went to the attempt's own
// store, which no watcher reads. A watcher that stops at run_finished — and
// `ticfac status`, which shows the last event — must see the bound fire
// WHILE the attempt is unresolved, because that is the moment the run stops
// making progress on its own and the moment a person's attention is worth
// asking for.

// interruptIgnoringExecutor is the fake agent that ignores the interrupt
// key — the case that failed, so the case the acceptance names. The stop is
// delivered and the agent keeps working: inspect keeps answering `running`
// past the bound, carrying the observation the herdr executor records when
// its stop does not take.
type interruptIgnoringExecutor struct{ Executor }

func (e *interruptIgnoringExecutor) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := e.Executor.Inspect(handle, cursor)
	if err != nil {
		return nil, err
	}
	status.State, status.Terminal = subprocess.StateRunning, false
	status.Observations = append(status.Observations, subprocess.Observation{
		At: status.ObservedAt, Kind: subprocess.ObsHeartbeat,
		Detail: "the wall clock passed and the agent was interrupted through herdr, but it has not exited",
	})
	return status, nil
}

// An agent that ignores the interrupt is still BOUNDED (the settlement
// deadline refuses the attempt), and the bound's firing is on the feed: one
// line, naming the bound and the attempt, carrying the executor's own last
// word about what it saw, and landing before the run's terminal line so a
// watcher that stops at run_finished has already seen it.
func TestTheWallClockFiringIsAFeedEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	f.wrap = func(inner Executor) Executor { return &interruptIgnoringExecutor{Executor: inner} }

	opts := f.options(f.Repo, fixtureOptions{mode: "hang"})
	// A clock the wait itself advances: the bound is minutes away in the
	// run's own terms and seconds away in the test's.
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
	// The second half of this tick's acceptance is the join `ticfac status`
	// makes: the typed firing line on the feed, and the attempt's STANDING
	// worktree, present at the same moment — the two facts a probe joins by
	// tick and attempt identity to report the firing for an in-flight
	// attempt. The watcher below waits on the observable, the line appearing
	// in the feed — never on a guessed interval — and snapshots the census
	// the moment it lands, mid-run, while the attempt is still in flight.
	standingWhenFired := make(chan []runprogress.Attempt, 1)
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
			if err == nil {
				fired := false
				for _, line := range events {
					if line.Stage == StageWallClock && line.TickID != nil && *line.TickID == "a1" {
						fired = true
					}
				}
				if fired {
					if standing, err := runprogress.Standing(f.Repo.Dir, "r-fixture", time.Now()); err == nil {
						select {
						case standingWhenFired <- standing:
						default:
						}
					}
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
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
		t.Fatal("the wait for an attempt that ignores its stop did not end: it is not bounded")
	}
	if runErr != nil {
		t.Fatalf("the run should have refused, not errored: %v", runErr)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the refusal is %+v, want %s: the run stays bounded however the agent behaves",
			result.Failure, RefusedUnaddressed)
	}

	// The journal carries the firing ONCE — the re-delivery at every poll is
	// the executor's business, and a feed that repeats one fact at poll
	// cadence teaches a watcher to ignore it.
	var wall *Event
	wallAt, finished := -1, -1
	for i, event := range r.Journal() {
		switch {
		case event.Stage == StageWallClock && event.Tick == "a1":
			if wallAt >= 0 {
				t.Fatalf("the wall clock's firing was written more than once: %+v", event)
			}
			copy := event
			wall, wallAt = &copy, i
		case event.Stage == StageRunFinished:
			finished = i
		}
	}
	if wall == nil {
		t.Fatalf("no %s line on the journal: the bound fired on an attempt that would not settle, "+
			"and the feed said nothing (the last line a watcher saw was dispatched)", StageWallClock)
	}
	if !strings.Contains(wall.Detail, "wall clock of 10s") {
		t.Errorf("the line does not name the bound that fired: %q", wall.Detail)
	}
	if !strings.Contains(wall.Detail, "attempt 1 of a1") {
		t.Errorf("the line does not name the attempt it bounds: %q", wall.Detail)
	}
	// The executor's own last word rides the line, because it is the only
	// party that can see the substrate: "interrupted but it has not exited"
	// is a different first move from "the supervisor is gone", and it lived
	// only in the attempt's private observations before this.
	if !strings.Contains(wall.Detail, "has not exited") {
		t.Errorf("the line does not carry the executor's last observation: %q", wall.Detail)
	}
	if finished >= 0 && wallAt > finished {
		t.Errorf("the firing landed after run_finished: the run was over before it said the bound had fired")
	}

	// And the join the status half makes was live: when the line landed, the
	// attempt it named was standing in the repo — an in-flight attempt whose
	// firing a watcher could be told about, not a line about an attempt that
	// was already gone.
	select {
	case standing := <-standingWhenFired:
		found := false
		for _, a := range standing {
			if a.TickID == "a1" && a.Attempt == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("when the firing line landed, the standing attempts were %+v: "+
				"attempt 1 of a1 was not there for a status to report", standing)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher never saw the firing line land on the feed, though the journal " +
			"says it did: the line this test asserts must be the line a non-participant reads")
	}

	// And it reached the FEED — the surface a non-participant subscribes to
	// and `ticfac status` reads its last event from — with the identity on
	// every line, not only the in-memory journal.
	events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
	if err != nil {
		t.Fatalf("read the run's feed: %v", err)
	}
	var fed *runfeed.Event
	for i := range events {
		if events[i].Stage == StageWallClock && events[i].TickID != nil && *events[i].TickID == "a1" {
			copy := events[i]
			fed = &copy
		}
	}
	if fed == nil {
		t.Fatalf("no %s line in the run's event feed: a watcher saw nothing while the bound fired", StageWallClock)
	}
	if fed.Attempt == nil || *fed.Attempt != 1 {
		t.Errorf("the feed line names attempt %v, want 1", fed.Attempt)
	}
	if !strings.Contains(fed.Detail, "has not exited") {
		t.Errorf("the feed line does not carry the executor's last observation: %q", fed.Detail)
	}
}

// A worker that settles inside its bound owes the feed no wall-clock line:
// the line exists for the moment the run stops making progress on its own,
// and a settled attempt — however it settled — is progress.
func TestNoWallClockLineForAWorkerThatSettlesInsideItsBound(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	for _, event := range r.Journal() {
		if event.Stage == StageWallClock {
			t.Fatalf("a worker that settled inside its bound still got a %s line: %q",
				StageWallClock, event.Detail)
		}
	}
}
