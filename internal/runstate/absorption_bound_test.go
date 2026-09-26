package runstate

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The recorded absorption depth bound (tick wz0, finding 95f5ee1a): the
// bound a run applies is a record on the run branch, so a cold restart
// applies the same bound the warm run did. Held to the standard every other
// record here is: durable on origin, made once, overridable only by the
// person's explicit raise under a guard.

func testAbsorptionBound(bound int) AbsorptionBound {
	p := testProvenance(PhaseWorker)
	p.TickID, p.Attempt = Ptr("a1"), Ptr(1)
	return AbsorptionBound{
		SchemaVersion: SchemaVersion,
		RunID:         testRun,
		Bound:         bound,
		RecordedAt:    "2026-09-26T11:00:00Z",
		Provenance:    p,
	}
}

// THE VALIDATE CASES: a bound that is at least a bound, a run it belongs to,
// and a time it was recorded — because a cold restart reads this record alone.
//
// short: Validate over a record already in memory
func TestAnAbsorptionBoundRecordValidates(t *testing.T) {
	t.Parallel()
	if err := testAbsorptionBound(5).Validate(); err != nil {
		t.Fatalf("a recorded bound does not validate: %v", err)
	}
}

// short: Validate over records already in memory
func TestAnAbsorptionBoundRecordRefusesWhatItCannotSay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		warp func(*AbsorptionBound)
		want string
	}{
		{"no run", func(b *AbsorptionBound) { b.RunID = "" }, "names no run"},
		{"zero bound", func(b *AbsorptionBound) { b.Bound = 0 }, "at least one"},
		{"negative bound", func(b *AbsorptionBound) { b.Bound = -3 }, "at least one"},
		{"no recorded_at", func(b *AbsorptionBound) { b.RecordedAt = "" }, "no recorded_at"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := testAbsorptionBound(5)
			testCase.warp(&record)
			err := record.Validate()
			if err == nil {
				t.Fatalf("%s validated; want the refusal %q", testCase.name, testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("%s refused with %q, want it to name %q", testCase.name, err, testCase.want)
			}
		})
	}
}

// THE DURABLE CASE: the first incarnation's bound is the run's — recorded
// once, read back cold from origin — and the person's explicit raise is a
// GUARDED update: a stale writer loses to whoever moved the record first.
func TestTheAbsorptionBoundIsRecordedOnceAndRaisedOnlyUnderGuard(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	outcome, err := s.PutAbsorptionBound(testAbsorptionBound(5))
	if err != nil {
		t.Fatalf("record the bound: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s", outcome, Created)
	}

	// A second record of the SAME bound — the racing first incarnation, or
	// the resume that re-derives it — proposes nothing new.
	again, err := s.PutAbsorptionBound(testAbsorptionBound(5))
	if err != nil {
		t.Fatalf("re-record the bound: %v", err)
	}
	if again.EffectPermitted() {
		t.Fatalf("outcome %s, want the repository to refuse a second recorded bound", again)
	}

	// A cold reader — a fresh actor that fetched nothing of this writer's
	// view — reads the bound from ORIGIN alone.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	read, ok, err := reader.AbsorptionBound()
	if err != nil || !ok {
		t.Fatalf("the recorded bound is not on origin: %v %v", ok, err)
	}
	if read.Bound != 5 {
		t.Fatalf("the bound read back as %d, want the 5 the warm run recorded", read.Bound)
	}

	// THE PERSON'S RAISE, under the sha guard: a reader that fetched the
	// standing record may rewrite it; a writer whose view went stale under it
	// loses to whoever moved the record first, and re-reads rather than
	// overwriting.
	raised := testAbsorptionBound(7)
	outcome, err = reader.UpdateAbsorptionBound(raised)
	if err != nil {
		t.Fatalf("raise the recorded bound: %v", err)
	}
	if outcome != Updated {
		t.Fatalf("outcome %s, want %s", outcome, Updated)
	}
	stale, err := s.UpdateAbsorptionBound(testAbsorptionBound(9))
	if err != nil {
		t.Fatalf("the stale raise: %v", err)
	}
	if stale != ConflictStaleSHA {
		t.Fatalf("the stale writer's outcome is %s, want %s — a guarded update is refused, never blind", stale, ConflictStaleSHA)
	}
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	standing, ok, err := s.AbsorptionBound()
	if err != nil || !ok {
		t.Fatalf("re-read the raised bound: %v %v", ok, err)
	}
	if standing.Bound != 7 {
		t.Fatalf("the standing bound is %d, want the raise's 7: a stale writer must not overwrite it", standing.Bound)
	}
}
