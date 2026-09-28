package reconcile

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Tick 823, folding the finding 08e5bcc0: a claim the run did not make is a
// boundary the window never crosses — but a claim a STOPPED run left is not a
// live party's, and telling the two apart is what this file owns.
//
// Since dz1 a claimed tick the run never dispatched (a "foreign" claim) was
// counted against the width but otherwise admitted: with room under the width
// the run claimed the tick AGAIN and started a worker over another run's — or
// a person's — live claim (08e5bcc0). And with no room, the same foreign
// claim held the run at claim_width FOREVER when its holder was a run that had
// already stopped and would never close the tick (the 823 regression), because
// a hold that waits for a dead holder is a hold with no event to wait for.
//
// The distinction is made from DURABLE EVIDENCE, never from the claimer's word
// (the rule the parallel-claims learning states): the runs of an epic share one
// integration branch, so the records a run leaves on it — its dispatch
// markers, its checkpoint — are the only witness of who claimed a tick and
// whether that run is still going. A claim whose most recent dispatch marker
// belongs to a run whose checkpoint reads terminal, and whose account of the
// tick does not say it closed it, is a stopped run's: the holder is gone, its
// claim is orphaned, and taking it over claims nothing the width has not
// already counted. Every other claim — a live run's, a person's, one no
// record witnesses — is LIVE: the run holds on it and never dispatches over
// it, whatever the width says.

// TestTheRunHoldsOnATickALiveForeignPartyClaimsEvenWhenTheWidthHasRoom is
// 08e5bcc0's acceptance: the width has room — one claim under a declared
// width of two — and the run still does not claim a1 again and start a worker
// over a claim it did not make. It HOLDS, in the resumable shape every other
// hold has: the feed says run_held in the vocabulary a watcher matches on,
// the refusal names the tick and what ends the claim, and nothing was
// dispatched for the next incarnation to adopt or duplicate.
func TestTheRunHoldsOnATickALiveForeignPartyClaimsEvenWhenTheWidthHasRoom(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	// A claim this run did not make, held by a party its records cannot
	// witness as stopped: no run-state on the integration branch names a
	// dispatch of a1, so as far as durable evidence goes the holder is LIVE —
	// another run from a checkout this branch does not see, or a person with
	// tk. The width (2) has room for the claim; the hold must come from the
	// claim itself.
	f.Tracker.holdClaimsAs(t, "another-run", "a1")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the run should have HELD, not returned an operational error: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedForeignClaim {
		t.Fatalf("the run ended %s with failure %+v, want a %s refusal: room under the width is not "+
			"permission to claim a tick another party is working on", result.State, result.Failure, RefusedForeignClaim)
	}
	if result.State != runstate.StateFailed {
		t.Errorf("the run ended %s; a held run is checkpointed so it can be resumed", result.State)
	}
	if result.Failure.TickID != "a1" {
		t.Errorf("the refusal names tick %+v, want a1 — the claim is what held the run", result.Failure)
	}

	// It never ASKED. Claiming a1 again would have been granted — the tracker
	// enforces nothing — and would have been the over-claim over a live
	// holder that 08e5bcc0 exists to stop.
	if got := f.Tracker.count("claim:a1"); got != 0 {
		t.Errorf("the run asked the tracker for a1's claim %d time(s) under a claim another party holds: "+
			"the tracker would have granted it, and the grant would have been a second worker on one tick", got)
	}

	// The hold is VISIBLE in the vocabulary a watcher matches on, and the run
	// dispatched nothing: there is no work in flight to walk away from.
	held, dispatched := false, false
	for _, event := range r.Journal() {
		switch {
		case event.Stage == StageRunHeld:
			held = true
		case event.Stage == StageDispatched:
			dispatched = true
		}
	}
	if !held {
		t.Errorf("no %s line in the feed: a hold nobody can see is a stall by definition", StageRunHeld)
	}
	if dispatched {
		t.Error("the run dispatched a worker over a foreign claim: the holder's worker may be thinking right now")
	}
	if !strings.Contains(result.Failure.Message, "does not dispatch over a claim") {
		t.Errorf("the refusal does not say the rule it is holding on: %q", result.Failure.Message)
	}

	// The other party finished and CLOSED a1 — a claim lives until its tick
	// closes — and the resumed run re-derives from the graph, works what is
	// left, and never touches the other party's work.
	ctx := context.Background()
	if _, err := f.Tracker.Close(ctx, "a1"); err != nil {
		t.Fatalf("close the other party's a1: %v", err)
	}
	second, resumed, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s (failure %+v)", resumed.State, resumed.Reason, resumed.Failure)
	}
	if !slices.Contains(resumed.Closed, "a2") || !slices.Contains(resumed.Closed, "b1") {
		t.Errorf("the resumed run closed %v, want the epic's remaining work: the other party's claim must cost "+
			"nothing but the wait", resumed.Closed)
	}
	// And it never touched the other party's tick: no claim asked for, no
	// worker started, across both incarnations. (a1 does appear in the
	// resumed run's own account of closed ticks, but that is its row
	// reconciled against the tracker the other party closed the tick in; the
	// claim count and the feed are what prove the run kept its hands off the
	// work itself.)
	if got := f.Tracker.count("claim:a1"); got != 0 {
		t.Errorf("the run asked the tracker for a1's claim %d time(s) across both incarnations: the other "+
			"party's work was never this run's to dispatch", got)
	}
	for _, incarnation := range [][]Event{r.Journal(), second.Journal()} {
		for _, event := range incarnation {
			if event.Tick == "a1" && (event.Stage == StageDispatched || event.Stage == StageRedispatched) {
				t.Errorf("a1 was dispatched (%s) over the other party's claim: the hold this test pins exists so that "+
					"this never happens", event.Stage)
			}
		}
	}
}
