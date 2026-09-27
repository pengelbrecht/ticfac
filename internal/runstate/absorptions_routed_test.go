package runstate

import "testing"

// A finding routed to ANOTHER repository is decided by the run's rule: it
// never gates this epic, it is filed in the target's tracker (routed) or as a
// local backlog tick naming the target, and the record says which — and the
// two spellings of the tick cannot be crossed.
//
// short: Validate over records already in memory
func TestARoutedAbsorptionRecordAgreesWithItself(t *testing.T) {
	t.Parallel()
	routed := func(tick, placement string) Absorption {
		a := testAbsorption("5e1ec7ed")
		a.Gating, a.ItemID, a.Basis = false, "", AbsorptionRule
		a.Target, a.TickID, a.Placement = "example/upstream", tick, placement
		a.Reason = "routed to example/upstream, which this run cannot fix"
		return a
	}
	if err := routed("example/upstream:k2p", AbsorptionRouted).Validate(); err != nil {
		t.Fatalf("a finding filed in its target's tracker does not validate: %v", err)
	}
	if err := routed("k2p", AbsorptionBacklog).Validate(); err != nil {
		t.Fatalf("a routed finding backlogged here does not validate: %v", err)
	}
	for name, a := range map[string]Absorption{
		"filed under a bare id":              routed("k2p", AbsorptionRouted),
		"backlogged under another repo's id": routed("example/upstream:k2p", AbsorptionBacklog),
		"filed under another target":         routed("example/other:k2p", AbsorptionRouted),
		"gating": func() Absorption {
			a := routed("k2p", AbsorptionBeforeReview)
			a.Gating, a.ItemID = true, "A1"
			return a
		}(),
		"predicted": func() Absorption {
			a := routed("k2p", AbsorptionBacklog)
			a.Basis = AbsorptionPredicted
			return a
		}(),
		"a rule with no target": func() Absorption {
			a := routed("k2p", AbsorptionBacklog)
			a.Target = ""
			return a
		}(),
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: a routed decision that contradicts itself validated: %+v", name, a)
		}
	}
}
