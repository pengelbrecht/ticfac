package runstate

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The absorption decision record (tick npq), held to the same standard the
// finding draft is: the record is the reasoning that changed the epic's
// shape mid-run, so it must be durable on origin, made exactly once, and
// honest about what it rests on — observed or predicted, which item, what
// confidence.
//
// The pure half (Validate) runs everywhere; the durable half (create-if-absent
// on a real origin) is end-to-end, like every other record here.

func testAbsorption(key string) Absorption {
	p := testProvenance(PhaseWorker)
	p.TickID, p.Attempt = Ptr("a1"), Ptr(1)
	p.Executor, p.Role = Ptr("local-subprocess"), Ptr("implement-tick")
	return Absorption{
		SchemaVersion: SchemaVersion,
		Key:           key,
		TickID:        "n9x",
		Gating:        true,
		ItemID:        "A1",
		Basis:         AbsorptionObserved,
		Reason:        "the command for A1 is observed broken while the finding stands",
		Placement:     AbsorptionBeforeReview,
		DecidedAt:     "2026-09-25T10:00:00Z",
		Provenance:    p,
	}
}

// THE VALIDATE CASES: the closed vocabularies, and the decision's agreement
// with itself. A record a cold re-derivation reads has to be able to say what
// it is without the warm run beside it.
//
// short: Validate over a record already in memory
func TestAnAbsorptionRecordValidates(t *testing.T) {
	t.Parallel()
	if err := testAbsorption("dc02fb31").Validate(); err != nil {
		t.Fatalf("a decided absorption does not validate: %v", err)
	}
}

// short: Validate over records already in memory
func TestAnAbsorptionRecordRefusesWhatItCannotSay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		warp func(*Absorption)
		want string
	}{
		{"no tick", func(a *Absorption) { a.TickID = "" }, "names no tick"},
		{"basis outside the vocabulary", func(a *Absorption) { a.Basis = "felt" }, "neither observed nor predicted"},
		{"placement outside the vocabulary", func(a *Absorption) { a.Placement = "appended" }, "not one of"},
		{"no reason", func(a *Absorption) { a.Reason = "" }, "carries no reason"},
		{"no decided_at", func(a *Absorption) { a.DecidedAt = "" }, "no decided_at"},
		{"not gating but names an item", func(a *Absorption) {
			a.Gating, a.ItemID, a.Placement = false, "A1", AbsorptionBacklog
		}, "reachable names no item"},
		{"not gating but placed in the epic", func(a *Absorption) {
			a.Gating, a.ItemID = false, ""
		}, "becomes a backlog tick"},
		{"gating but placed in the backlog", func(a *Absorption) {
			a.Placement = AbsorptionBacklog
		}, "gates the done is absorbed into the epic"},
		{"observed with a confidence", func(a *Absorption) {
			a.Confidence = 0.62
		}, "an observation is what a command said"},
		{"confidence outside a probability", func(a *Absorption) {
			a.Basis, a.Confidence = AbsorptionPredicted, 1.5
		}, "not a probability"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := testAbsorption("dc02fb31")
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

// THE DURABLE CASE: the decision is create-if-absent keyed by the finding, so
// an incarnation killed between the record and the tick it names is resumed by
// the record — and a second writer racing the first reads the original back.
func TestAnAbsorptionIsDecidedOnceAndDurableOnOrigin(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	record := testAbsorption("dc02fb31")
	outcome, err := s.PutAbsorption(record)
	if err != nil {
		t.Fatalf("record the absorption: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s", outcome, Created)
	}

	// Read back from ORIGIN, not from this writer's view: durable means
	// pushed, and the reasoning that changed the epic's shape is exactly the
	// record a cold re-derivation must be able to reach from git alone.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	read, ok, err := reader.Absorption("dc02fb31")
	if err != nil || !ok {
		t.Fatalf("the absorption record is not on origin: %v %v", ok, err)
	}
	if read.TickID != "n9x" || read.Basis != AbsorptionObserved || read.ItemID != "A1" ||
		read.Placement != AbsorptionBeforeReview {
		t.Fatalf("the record read back as %+v, not the decision that was made", read)
	}

	// A second write of the SAME decision — the killed incarnation's resume,
	// or a racing writer — proposes nothing new: the original stands, and the
	// caller reads it back to finish what it records.
	again, err := s.PutAbsorption(record)
	if err != nil {
		t.Fatalf("re-record the absorption: %v", err)
	}
	if again.EffectPermitted() {
		t.Fatalf("outcome %s, want the repository to refuse a second decision under one key", again)
	}
	standing, ok, err := s.Absorption("dc02fb31")
	if err != nil || !ok {
		t.Fatalf("read the standing absorption: %v %v", ok, err)
	}
	if standing.TickID != "n9x" {
		t.Fatalf("the standing decision names tick %s, want n9x: a decision is never made twice", standing.TickID)
	}

	// And the listing sees it, ordered by key, so the close-out's retro can
	// say what was absorbed and why without walking the store by hand.
	records, err := reader.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Key != "dc02fb31" {
		t.Fatalf("the run's absorptions are %+v, want exactly the one decision", records)
	}
}
