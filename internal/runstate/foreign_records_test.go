package runstate

import (
	"strings"
	"testing"
)

// Tick 823's seam: a run asked to wait for another run's claim reads that
// run's liveness from its records, and the records of an epic's runs share one
// integration branch — so the other runs' dispatch markers and checkpoints are
// already in this store's fetched view. What the run must NOT do is read them
// through its own run-scoped lenses (Attempts, Checkpoint), because those
// answer about THIS run by construction; the foreign reads are reads of the
// branch, deliberately named as such.
func TestAnotherRunsRecordsAreReadableThroughTheFetchedView(t *testing.T) {
	o := newOrigin(t)
	first := o.actor("first", "r-first")
	second := o.actor("second", "r-second")

	// The first run dispatched a tick and stopped on it: a checkpoint whose
	// state is terminal and whose rows account for the tick, and a dispatch
	// marker naming it — the two records a holder's liveness is read from.
	// (The fixtures' provenance names the stock run id; a record's run id is
	// its directory, and the store refuses the disagreement — so each actor's
	// records are stamped with its own.)
	stopped := testCheckpoint(StateFailed, "a1 did not pass")
	stopped.Provenance.RunID = "r-first"
	stopped.Ticks = []TickState{{TickID: "a1", State: "rejected"}}
	if _, err := first.PutCheckpoint(stopped); err != nil {
		t.Fatal(err)
	}
	firstAttempt := testAttempt(1, "a1")
	firstAttempt.Provenance.RunID = "r-first"
	if _, err := first.PutAttempt(firstAttempt); err != nil {
		t.Fatal(err)
	}

	// The second run has a dispatch of its own — a DIFFERENT tick — which the
	// foreign reads must never hand back: a store that answered "who else
	// claimed" with this run's own markers would be the run-scoped lens again.
	own := testAttempt(1, "b1")
	own.Provenance.RunID = "r-second"
	if _, err := second.PutAttempt(own); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Fetch(); err != nil {
		t.Fatal(err)
	}

	foreign, err := second.ForeignAttempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 1 {
		t.Fatalf("the second run reads %d foreign dispatch markers, want the first run's one: %+v", len(foreign), foreign)
	}
	if foreign[0].TickID != "a1" || foreign[0].Provenance.RunID != "r-first" {
		t.Errorf("the foreign marker is %s of run %s, want a1 of r-first", foreign[0].TickID, foreign[0].Provenance.RunID)
	}

	checkpoint, ok, err := second.ForeignCheckpoint("r-first")
	if err != nil || !ok {
		t.Fatalf("read the first run's checkpoint: %v %v", ok, err)
	}
	if !checkpoint.State.Terminal() {
		t.Errorf("the first run's checkpoint reads %s; the stopped run's state is the fact the claim question turns on", checkpoint.State)
	}
	if len(checkpoint.Ticks) != 1 || checkpoint.Ticks[0].TickID != "a1" || checkpoint.Ticks[0].State != "rejected" {
		t.Errorf("the first run's account of its ticks is %+v; the row is what tells an orphaned claim from a spent one", checkpoint.Ticks)
	}

	// A run with no records on the branch answers absent, not empty: the
	// difference is the whole conservative half of the claim question — a
	// holder whose records cannot be read is a holder to wait for.
	if _, ok, err := second.ForeignCheckpoint("r-never-ran"); err != nil || ok {
		t.Errorf("a run that never wrote anything reads present (%v, %v): absence is the live answer's evidence", ok, err)
	}

	// And the run id is validated the way this store's own is, because it
	// reaches a path from outside: a separator in it would read outside
	// .ticfac/runs/.
	if _, _, err := second.ForeignCheckpoint("../escape"); err == nil || !strings.Contains(err.Error(), "path separator") {
		t.Errorf("a run id with a separator reads %v, want the segment refusal", err)
	}
}
