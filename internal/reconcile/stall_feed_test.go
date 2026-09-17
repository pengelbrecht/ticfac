package reconcile

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The stall warning (tick 7zs): an attempt that is alive but making no
// progress was invisible until the wall clock spent it. In the Phase 3 run
// the feed was silent for 40 of the worker's 55 minutes precisely because
// nothing happened that the run observes — the worker was alive, ticfac
// status said so, and it was true. The gap — how long since the attempt's
// branch last moved, how long since its worktree last changed — is the
// simplest signal that is honest about what it measures, and the feed line
// that crosses the threshold is a reason to look, never a verdict.

// A worker that hangs after committing is alive and producing nothing: the
// exact shape of the Phase 3 attempt, with a worktree that stands still. The
// run says so in the feed — once, while the attempt is still unresolved, so
// a watcher reading the feed or `ticfac status` sees it with most of the
// attempt's budget left to spend.
func TestTheStallWarningIsAFeedEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	r, _, err := f.run(f.Repo, fixtureOptions{mode: "hang",
		stallWarn: 200 * time.Millisecond, stopAfter: stopAt("a1", StageStallWarned)})
	killedAfter(t, err, "a1", StageStallWarned)

	// The journal carries the warning once, and it landed while the attempt
	// was still unresolved — the line exists for the moment a person's
	// attention is worth asking for, and a warning that lands after the
	// terminal lines is a warning nobody needed.
	var stall *Event
	warnings := 0
	for _, event := range r.Journal() {
		if event.Stage == StageStallWarned && event.Tick == "a1" {
			warnings++
			stall = &event
		}
	}
	if warnings != 1 {
		t.Fatalf("the stall warning was written %d times, want exactly 1", warnings)
	}
	if stall == nil {
		t.Fatal("no stall warning for a1 on the journal")
	}
	if !strings.Contains(stall.Detail, "attempt 1 of a1") {
		t.Errorf("the line does not name the attempt it watched: %q", stall.Detail)
	}
	if !strings.Contains(stall.Detail, "branch last moved") || !strings.Contains(stall.Detail, "worktree last changed") {
		t.Errorf("the line does not carry both facts it measured: %q", stall.Detail)
	}
	if !strings.Contains(stall.Detail, "not a verdict") {
		t.Errorf("the line does not say what it is: %q", stall.Detail)
	}

	// And it reached the FEED — the surface a non-participant subscribes to
	// and `ticfac status` reads its last event from — with the identity on
	// every line.
	events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
	if err != nil {
		t.Fatalf("read the run's feed: %v", err)
	}
	var fed *runfeed.Event
	for i := range events {
		if events[i].Stage == StageStallWarned && events[i].TickID != nil && *events[i].TickID == "a1" {
			copy := events[i]
			fed = &copy
		}
	}
	if fed == nil {
		t.Fatalf("no %s line in the run's event feed: a watcher saw nothing while the attempt went nowhere", StageStallWarned)
	}
	if fed.Attempt == nil || *fed.Attempt != 1 {
		t.Errorf("the feed line names attempt %v, want 1", fed.Attempt)
	}
}

// The acceptance the whole tick turns on: NEITHER signal changes any verdict.
// A worker that produces nothing for longer than the threshold and then does
// its work and settles DONE is integrated and closed exactly as a prompt one
// is — the stall warning is a line on the feed, not a decision on the run.
func TestTheStallWarningChangesNoVerdict(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "stall-then-report"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "stall-then-report", stallWarn: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("the run refused over an attempt that was merely slow to start: %+v", result.Failure)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a stalled attempt that settled was judged anyway", result.State, result.Reason)
	}
	for _, tick := range result.Ticks {
		if tick.State != "closed" {
			t.Errorf("tick %s ended %s, want closed: the warning stopped nothing", tick.TickID, tick.State)
		}
	}

	// The warning DID fire — a test that never saw the line would prove the
	// run ignored it, not that the run was not judged by it. Once per tick,
	// and only for ticks that stalled.
	warned := map[string]int{}
	for _, event := range r.Journal() {
		if event.Stage == StageStallWarned {
			warned[event.Tick]++
		}
	}
	if warned["a1"] != 1 {
		t.Errorf("a1 was warned %d times, want exactly 1: the feed must not repeat one fact at poll cadence", warned["a1"])
	}
	for tick, n := range warned {
		if n != 1 {
			t.Errorf("%s was warned %d times, want exactly 1", tick, n)
		}
	}
}

// A worker that settles promptly owes the feed no warning: the line exists
// for the run that has no way of knowing, and a run whose attempts keep
// producing is not that run. The default threshold is an EARLY WARNING
// before the bound — well under the wall clock, not a second spelling of it.
func TestNoStallWarningForAWorkerThatSettlesPromptly(t *testing.T) {
	t.Parallel()
	if DefaultStallWarnAfter != 15*time.Minute {
		t.Errorf("the default stall threshold is %s, want 15m: early enough that a person reading the "+
			"line still has most of the attempt's budget to spend", DefaultStallWarnAfter)
	}
	if DefaultStallWarnAfter >= time.Duration(DefaultWallSeconds)*time.Second {
		t.Errorf("the default stall threshold (%s) is not an early warning before the wall clock of %ds",
			DefaultStallWarnAfter, DefaultWallSeconds)
	}

	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	for _, event := range r.Journal() {
		if event.Stage == StageStallWarned {
			t.Fatalf("a worker that settled promptly still got a %s line: %q", StageStallWarned, event.Detail)
		}
	}
}

// The stall measurement reads the same ref vocabulary the dispatch writes:
// the write ref this package mints for an attempt is the one
// runprogress.ParseAttempt reads back, and the namespace the source grant
// bounds is the one Standing enumerates. Two spellings of one vocabulary
// drift silently unless one is pinned to the other.
func TestTheProgressMeasurementReadsTheRunWriteRefs(t *testing.T) {
	t.Parallel()
	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", "epic-x", "7zs", 22)
	ref := attemptWriteRef(jobID)

	run, tick, attempt, ok := runprogress.ParseAttempt(ref)
	if !ok || run != "epic-x" || tick != "7zs" || attempt != 22 {
		t.Fatalf("the write ref %q reads back as (%q, %q, %d, %v): the measurement cannot see this run's attempts",
			ref, run, tick, attempt, ok)
	}
	prefix := attemptRefPrefix("epic-x")
	if !strings.HasPrefix(ref, prefix) {
		t.Fatalf("the write ref %q is outside the source grant's namespace %q", ref, prefix)
	}
	if prefix != runprogress.RefPrefix("epic-x") {
		t.Errorf("the source grant bounds %q while the measurement enumerates %q: one vocabulary, two spellings",
			prefix, runprogress.RefPrefix("epic-x"))
	}
}
