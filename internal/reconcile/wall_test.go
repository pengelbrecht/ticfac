package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The wall clock as a runaway backstop (tick wv2): set per role and tier in
// [tier_policy.wall_seconds], overridden per tick by a `wall_minutes:` label,
// and recorded on the marker so an adopting run measures the attempt against
// the bound it was issued.

const wallGate = tierGate + `
[tier_policy.wall_seconds]
balanced = 7200
"implement.balanced" = 10800
`

func markerWall(t *testing.T, r *Reconciler, tickID string) int {
	t.Helper()
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID == tickID {
			switch v := record.JobHandle["wall_seconds"].(type) {
			case float64:
				return int(v)
			case int:
				return v
			}
		}
	}
	return 0
}

// [A4]+[A5] The most specific declaration wins over the tier's, the label wins
// over both, and each is what the JobSpec is issued and the marker records.
func TestTheBackstopIsSetPerRoleAndTierAndATickLabelOverridesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wallGate})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"wall_minutes:90"} })

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if got := f.spec("a1").Limits.WallSeconds; got != 90*60 {
		t.Errorf("a1 was issued a wall of %ds, want 5400 from its wall_minutes:90 label", got)
	}
	if got := markerWall(t, r, "a1"); got != 90*60 {
		t.Errorf("a1's marker records a wall of %ds, want 5400", got)
	}
	if got := f.spec("b1").Limits.WallSeconds; got != 10800 {
		t.Errorf("b1 was issued a wall of %ds, want 10800 from tier_policy.wall_seconds.\"implement.balanced\"", got)
	}
	if detail, ok := journalLine(r, "a1", StageTierDerived); !ok || !strings.Contains(detail, "wall_minutes:90") {
		t.Errorf("the dispatch line does not say where the backstop came from: %q", detail)
	}
}

// With nothing declared, the backstop is the generous default — never the
// hour that stopped epic-6in's working attempts.
//
// short: a constant comparison; no fixture, no process, no git.
func TestTheDefaultBackstopIsGenerous(t *testing.T) {
	t.Parallel()
	if DefaultWallSeconds < 4*3600 {
		t.Fatalf("DefaultWallSeconds = %d: the wall clock is a runaway backstop now, and a bound this short "+
			"stops workers that are working (epic-6in)", DefaultWallSeconds)
	}
}

// A malformed label is refused loudly, naming the tick and the label, before
// anything is claimed.
func TestAMalformedWallLabelIsRefusedBeforeAnythingIsClaimed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wallGate})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"wall_minutes:soon"} })

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedWallLabel {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedWallLabel)
	}
	if !strings.Contains(result.Failure.Message, "a1") || !strings.Contains(result.Failure.Message, "wall_minutes:soon") {
		t.Errorf("the refusal does not name the tick and the label: %q", result.Failure.Message)
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID == "a1" {
			t.Fatalf("a1 was dispatched (attempt %d) over a label that should have refused it", record.Attempt)
		}
	}
}

// [A6] A restarted run polls the live workers a previous incarnation left
// BEFORE its slow startup work — the base fold and the sweep — so a worker's
// stuck watch and backstop do not wait out a long startup (epic-6in: dz1's
// restart spent 31 minutes there without once polling it).
func TestARestartedRunPollsItsLiveWorkersBeforeItsStartupWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)

	firstFold := func(e Event) bool { return e.Stage == StageRefreshed }
	r, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: firstFold})
	if _, ok := err.(*killedAt); !ok {
		t.Fatalf("the second incarnation ended with %v, not at its first base fold", err)
	}
	polled, folded := -1, -1
	for i, event := range r.Journal() {
		switch {
		case event.Tick == "a1" && event.Stage == StagePolledAtResume && polled < 0:
			polled = i
		case event.Stage == StageRefreshed && folded < 0:
			folded = i
		}
	}
	if polled < 0 {
		t.Fatalf("the restarted run never polled a1's live worker before its startup work:\n%s", journalText(r))
	}
	if folded >= 0 && polled > folded {
		t.Fatalf("a1 was polled at line %d, after the base fold at line %d", polled, folded)
	}
}

// The stuck watch on the feed (tick wv2): a worker that writes nothing, uses
// no CPU and never finishes is nudged — a stuck_nudged line naming the
// evidence — and, still quiet a window later, stopped — a stuck_stopped line
// — well inside its backstop, with the retry and the tier ladder taking it
// from there.
func TestAStuckWorkerIsNudgedThenStoppedOnTheFeed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "wedged"})
	r, _, err := f.run(f.Repo, fixtureOptions{mode: "wedged", stuckAfter: 1500 * time.Millisecond,
		stopAfter: stopAt("a1", StageStuckStopped)})
	killedAfter(t, err, "a1", StageStuckStopped)
	nudged, ok := journalLine(r, "a1", StageStuckNudged)
	if !ok {
		t.Fatalf("the stuck worker was stopped with no nudge on the feed:\n%s", journalText(r))
	}
	for _, want := range []string{"appears stuck", "worktree last changed", "tool process(es)"} {
		if !strings.Contains(nudged, want) {
			t.Errorf("the nudge line %q does not carry %q", nudged, want)
		}
	}
	if stopped, _ := journalLine(r, "a1", StageStuckStopped); !strings.Contains(stopped, "stopped as stuck") {
		t.Errorf("the stop line reads %q", stopped)
	}
	if _, fired := journalLine(r, "a1", StageWallClock); fired {
		t.Error("the wall clock fired: the stuck watch, not the backstop, is what stops a stuck worker")
	}
}

func journalText(r *Reconciler) string {
	var b strings.Builder
	for _, e := range r.Journal() {
		b.WriteString("  " + e.Tick + " " + e.Stage + ": " + e.Detail + "\n")
	}
	return b.String()
}
