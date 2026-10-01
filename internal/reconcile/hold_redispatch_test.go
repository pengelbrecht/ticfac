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
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A held tick that left nothing is dispatched again in the same run (the
// follow-up to #168).
//
// #168 made a refusal raised before an attempt's work merged hold only its
// tick. For an attempt whose worker never answered and left NOTHING
// (collect_failed on missing-result: a lost container, a dead runner) that
// hold lasted until the run
// ended and its supervisor resumed it, and the resume did the one thing the
// run already knew to do — dispatch the tick again, a rung up. A run that
// waits for its own restart to redispatch a tick is a run whose dependents
// sit idle for the length of every other tick. So an operational rejection
// that left nothing is requeued in-run, bounded by the run's operational
// retry bound; a refusal on the merits (a boundary violation, an undeclared
// touch, a conflict no resolve delivered) stays held, as #168 made it.

// signalOnFirstRejection touches signal once the tick's first attempt is torn
// down: its rejection has been answered, whichever way the run answered it.
type signalOnFirstRejection struct {
	Executor
	tick   string
	signal string
	once   sync.Once
	starts *tryCount
}

func (e *signalOnFirstRejection) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if strings.Contains(spec.JobID, "/tick-"+e.tick+"/attempt-") {
		e.starts.mu.Lock()
		e.starts.starts++
		e.starts.mu.Unlock()
	}
	return e.Executor.Start(spec)
}

func (e *signalOnFirstRejection) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	err := e.Executor.Dispose(handle, opts)
	if strings.Contains(handle.JobID, "/tick-"+e.tick+"/attempt-") {
		e.once.Do(func() { _ = os.WriteFile(e.signal, nil, 0o644) })
	}
	return err
}

// nothingOnce is a fixture whose a1 never answers on its first `tries` tries
// while a2 lingers until a1's first rejection, and b1 waits behind a1.
func nothingOnce(t *testing.T, tries string) (*fixture, *tryCount) {
	t.Helper()
	dir := t.TempDir()
	signal := filepath.Join(dir, "a1-rejected")
	f := newFixture(t, fixtureOptions{mode: "linger-until"})
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal,
		"VANISH_TICK=a1", "VANISH_TRIES=" + tries, "VANISH_COUNT=" + filepath.Join(dir, "a1-tries")},
		f.Runner[1:]...)
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{"b1": {"a1"}}
	f.Tracker.write(t, state)
	count := &tryCount{}
	f.wrap = func(inner Executor) Executor {
		return &signalOnFirstRejection{Executor: inner, tick: "a1", signal: signal, starts: count}
	}
	return f, count
}

func TestAHeldTickThatLeftNothingIsDispatchedAgainInTheSameRun(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f, starts := nothingOnce(t, "1")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "linger-until"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want completed: a1's unanswered first try is answered in-run "+
			"(a1 stages %v)", result.State, result.Failure, r.Stages("a1"))
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		issue, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if issue.Status != "closed" {
			t.Errorf("%s is %s, want closed (stages %v)", tick, issue.Status, r.Stages(tick))
		}
	}
	starts.mu.Lock()
	n := starts.starts
	starts.mu.Unlock()
	if n != 2 {
		t.Errorf("a1 was dispatched %d times, want 2 (one unanswered try, one dispatched again in-run)", n)
	}
	line, ok := journalLine(r, "a1", StageRedispatched)
	if !ok || !strings.Contains(line, "in this run") {
		t.Errorf("a1's in-run redispatch is not in the feed: %q (stages %v)", line, r.Stages("a1"))
	}
	if _, held := journalLine(r, "a1", StageTickHeld); held {
		t.Errorf("a1 was held although its try left nothing a person has to look at")
	}
}

// The bound: a tick whose worker never answers is dispatched again at
// most maxOperationalRetries times in one run, and then held as #168 holds
// it — the run works every other tick and ends naming it.
func TestAHeldTickThatKeepsLeavingNothingIsHeldOnceItsRetriesAreSpent(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f, starts := nothingOnce(t, "99")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "linger-until"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil ||
		result.Failure.TickID != "a1" || result.Failure.Reason != RefusedCollect {
		t.Fatalf("the run ended %s (%+v), want failed on a1's collect_failed", result.State, result.Failure)
	}
	starts.mu.Lock()
	n := starts.starts
	starts.mu.Unlock()
	if n != 1+maxOperationalRetries {
		t.Errorf("a1 was dispatched %d times, want %d (one try and %d in-run retries)", n,
			1+maxOperationalRetries, maxOperationalRetries)
	}
	if _, held := journalLine(r, "a1", StageTickHeld); !held {
		t.Errorf("a1 is not held once its retries are spent: %v", r.Stages("a1"))
	}
	a2, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Status != "closed" {
		t.Errorf("a2 is %s: a1's spent retries stopped a tick that does not wait behind it", a2.Status)
	}
	if contains(r.Stages("b1"), StageDispatched) {
		t.Errorf("b1 was dispatched behind a held blocker")
	}
}

// Which held refusals are dispatched again in-run: a collect that left
// nothing for an operational reason. A boundary violation that left nothing
// is a collect_failed on the merits and stays held, as does every other
// reason holdsOnlyItsTick names.
//
// short: a table over a pure function; nothing is dispatched.
func TestOnlyAnOperationalCollectThatLeftNothingIsDispatchedAgainInRun(t *testing.T) {
	t.Parallel()
	if !redispatchesInRun(&Refusal{Reason: RefusedCollect, neverAnswered: true}) {
		t.Error("an operational collect that left nothing is not dispatched again in-run")
	}
	for _, refusal := range []*Refusal{
		{Reason: RefusedCollect},
		{Reason: RefusedBoundary, neverAnswered: true},
		{Reason: RefusedUndeclaredTouch},
		{Reason: RefusedRejectedWork},
		{Reason: RefusedFindingInvalid},
		{Reason: RefusedUnaddressed},
		{Reason: RefusedWiped},
		{Reason: RefusedMerge, conflict: true},
		nil,
	} {
		if redispatchesInRun(refusal) {
			t.Errorf("%+v is dispatched again in-run; it must stay held", refusal)
		}
	}

	// Which collects are a worker that never answered: missing-result from a
	// runner that died or infrastructure, not one stopped by its own bound
	// (which the next dispatch would meet again) and not an empty answer.
	collect := func(verdict, outcome, class string) *subprocess.Collection {
		return &subprocess.Collection{Verdict: verdict,
			Result: &subprocess.JobResult{Outcome: outcome, FailureClass: class}}
	}
	for _, c := range []struct {
		collected *subprocess.Collection
		want      bool
	}{
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureRunnerError), true},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureInfrastructure), true},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureWallClockExceeded), false},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureCostBudgetExceeded), false},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureQuotaExhausted), false},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeFailed, subprocess.FailureCredentialRefused), false},
		{collect(subprocess.VerdictMissingResult, subprocess.OutcomeCancelled, ""), false},
		{collect(subprocess.VerdictNoCommits, subprocess.OutcomeFailed, subprocess.FailureRunnerError), false},
		{&subprocess.Collection{Verdict: subprocess.VerdictMissingResult}, false},
		{nil, false},
	} {
		if got := neverAnswered(c.collected); got != c.want {
			t.Errorf("neverAnswered(%+v) = %v, want %v", c.collected, got, c.want)
		}
	}
}
