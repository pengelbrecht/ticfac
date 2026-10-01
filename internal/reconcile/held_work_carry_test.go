package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A held tick that LEFT WORK is carried forward in the same run when the
// cause is operational (the follow-up to #168 and #177).
//
// The collect already disposes of a rejected attempt's work by its
// rejection's class (rejected_work.go, epic-6in 823): an operational
// rejection — missing-result: a worker stopped as stuck, a runner that died
// after committing — is released by the run CARRYING its work, and the tick
// is requeued in-run. When that disposal itself fails for an operational
// reason (origin did not take the release record), the collect's refusal is
// held, with work nothing merged; the next incarnation then decides it from
// the recorded rejection (disposeUndecidedRejection) and carries it. Waiting
// for the restart only kept the tick and its dependents idle, so the window
// now does what the resume would, in-run, bounded by maxOperationalRetries.
// A rejection on the merits (a boundary violation) is never carried, and its
// hold stands.

// failingReleases makes the run's first `n` release records of a tick fail,
// as an origin that did not take the write.
type failingReleases struct {
	mu   sync.Mutex
	tick string
	n    int
}

func (f *failingReleases) fault(marker attemptHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if marker.TickID != f.tick || f.n == 0 {
		return nil
	}
	f.n--
	return errors.New("origin did not take the write (injected)")
}

func runWithFailingReleases(t *testing.T, f *fixture, opts fixtureOptions, fail *failingReleases) (*Reconciler, *Result) {
	t.Helper()
	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	r.releaseFault = fail.fault
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	return r, result
}

// The stuck worker that committed (823's shape), whose disposal could not be
// recorded the first time: the run carries its work in-run and completes.
func TestAHeldTickThatLeftWorkIsCarriedForwardInTheSameRun(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "stuck-first", gate: tierGate})
	r, result := runWithFailingReleases(t, f,
		fixtureOptions{mode: "stuck-first", stuckAfter: 1500 * time.Millisecond}, &failingReleases{tick: "a1", n: 1})

	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want completed: a1's committed work is carried in-run (a1 stages %v)",
			result.State, result.Failure, r.Stages("a1"))
	}
	if _, held := journalLine(r, "a1", StageTickHeld); held {
		t.Errorf("a1 was held although its work could be carried in-run: %v", r.Stages("a1"))
	}
	if _, ok := journalLine(r, "a1", StageRejectedWorkCarried); !ok {
		t.Errorf("where a1's work went is not on the feed:\n%s", journalText(r))
	}
	first, second := markerOfTry(t, r, "a1", 1), markerOfTry(t, r, "a1", 2)
	if second.ResumedFrom == nil || second.ResumedFrom.Attempt != first.Attempt {
		t.Fatalf("a1's second try resumed from %+v, want the first try's work", second.ResumedFrom)
	}
	issue, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Status != "closed" {
		t.Errorf("a1 is %s, want closed behind the carried work", issue.Status)
	}
	found := false
	for _, e := range r.Journal() {
		if e.Tick == "a1" && e.Stage == StageRedispatched && strings.Contains(e.Detail, "in this run") {
			found = true
		}
	}
	if !found {
		t.Errorf("a1's in-run carry is not said on the feed as an in-run redispatch:\n%s", journalText(r))
	}
}

// A rejection ON THE MERITS whose release could not be recorded: its work
// is never carried, in-run or otherwise — the hold stands.
func TestAHeldTickWithWorkRejectedOnTheMeritsStaysHeld(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "boundary-first", gate: tierGate})
	r, result := runWithFailingReleases(t, f, fixtureOptions{mode: "boundary-first"},
		&failingReleases{tick: "a1", n: 99})

	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.TickID != "a1" {
		t.Fatalf("the run ended %s (%+v), want failed holding a1", result.State, result.Failure)
	}
	if _, held := journalLine(r, "a1", StageTickHeld); !held {
		t.Errorf("a1 is not held: %v", r.Stages("a1"))
	}
	if _, carried := journalLine(r, "a1", StageRejectedWorkCarried); carried {
		t.Errorf("work rejected on the merits was carried")
	}
	if contains(r.Stages("a1"), StageRedispatched) {
		t.Errorf("a1 was dispatched again in-run after a rejection on the merits: %v", r.Stages("a1"))
	}
}
