package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// TestARefusalReachesTheFeedInItsOwnWords pins half of tick emk.
//
// A refusal decides the run's fate, and in the Phase 3 run one reached no
// surface an operator reads. The feed went from `dispatched cwa` straight to a
// generic run_finished ("cwa did not pass: the run stopped rather than
// integrating over an unproven change"), and the actual reason — the attempt
// was unaddressed past its wall clock, release it with `ticfac settle` — was
// only recoverable by reading dispatch.go.
func TestARefusalReachesTheFeedInItsOwnWords(t *testing.T) {
	t.Parallel()

	r := &Reconciler{now: time.Now}
	refusal := r.refuse(RefusedUnaddressed, "cwa",
		"attempt %d of %s still reads running past the wall clock", 7, "cwa")
	r.recordRefusal("cwa", refusal)

	journal := r.Journal()
	if len(journal) != 1 {
		t.Fatalf("a refusal wrote %d journal lines, want 1: %+v", len(journal), journal)
	}
	if journal[0].Stage != StageRejected {
		t.Errorf("the refusal was recorded as %q, want %q", journal[0].Stage, StageRejected)
	}
	if !strings.Contains(journal[0].Detail, RefusedUnaddressed) {
		t.Errorf("the line does not name the refusal class: %q", journal[0].Detail)
	}
	if !strings.Contains(journal[0].Detail, "past the wall clock") {
		t.Errorf("the line does not carry the refusal's own words: %q", journal[0].Detail)
	}
}

// A path that already recorded its own rejection keeps it: that line carries
// detail this one cannot, and a terminal event said twice is not terminal.
func TestARefusalIsNotRecordedTwice(t *testing.T) {
	t.Parallel()

	r := &Reconciler{now: time.Now}
	r.record("cwa", StageRejected, "boundary violation: .tick/issues/cwa.json")
	r.recordRefusal("cwa", r.refuse(RefusedBoundary, "cwa", "the attempt wrote under the tracker's authority"))

	if journal := r.Journal(); len(journal) != 1 {
		t.Fatalf("the refusal was recorded twice: %+v", journal)
	}
}

// A rejection recorded for ANOTHER tick does not suppress this one's.
func TestARefusalIsRecordedPerTick(t *testing.T) {
	t.Parallel()

	r := &Reconciler{now: time.Now}
	r.record("aaa", StageRejected, "boundary violation in another tick")
	r.recordRefusal("bbb", r.refuse(RefusedUnaddressed, "bbb", "nobody can say whether it is running"))

	journal := r.Journal()
	if len(journal) != 2 {
		t.Fatalf("want both rejections, got %+v", journal)
	}
	if journal[1].Tick != "bbb" {
		t.Errorf("the second line is for %q, want bbb", journal[1].Tick)
	}
}

// The unaddressed refusal must carry the executor's own last observation, not a
// guess. A local subprocess whose supervisor died and a herdr agent that will
// not stop are different problems with different first moves, and the refusal
// used to describe only the first (tick emk).
func TestTheUnaddressedRefusalCarriesTheExecutorsLastWord(t *testing.T) {
	t.Parallel()

	seen := lastObservation(&subprocess.JobStatus{Observations: []subprocess.Observation{
		{At: "2026-09-15T14:50:03Z", Kind: subprocess.ObsHeartbeat, Detail: "first"},
		{At: "2026-09-15T14:50:08Z", Kind: subprocess.ObsExited,
			Detail: "stopped the agent tick-cwa-a7 at its wall clock of 3600s: the interrupt was delivered through herdr"},
	}})
	if !strings.Contains(seen, "the interrupt was delivered through herdr") {
		t.Errorf("the refusal would not carry what the executor saw: %q", seen)
	}

	if got := lastObservation(nil); !strings.Contains(got, "no observation") {
		t.Errorf("an executor that said nothing should be reported as such: %q", got)
	}
	if got := lastObservation(&subprocess.JobStatus{Observations: []subprocess.Observation{{Detail: "   "}}}); !strings.Contains(got, "no detail") {
		t.Errorf("an empty observation should be reported as such: %q", got)
	}
}
