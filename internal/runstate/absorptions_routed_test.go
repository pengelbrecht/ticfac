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
		"a rule with no target that names an item": func() Absorption {
			a := routed("k2p", AbsorptionBacklog)
			a.Target, a.Gating, a.ItemID = "", true, "A1"
			return a
		}(),
		"a rule with no target filed in a tracker": func() Absorption {
			a := routed("example/upstream:k2p", AbsorptionRouted)
			a.Target = ""
			return a
		}(),
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: a routed decision that contradicts itself validated: %+v", name, a)
		}
	}
	// The PROSE rule (epic-6in) is a rule decision about THIS repository: a
	// backlog tick, or a gating absorption naming no item for a claimed red
	// build. Both validate.
	for name, a := range map[string]Absorption{
		"backlog": func() Absorption {
			a := routed("k2p", AbsorptionBacklog)
			a.Target = ""
			return a
		}(),
		"a claimed red build": func() Absorption {
			a := routed("k2p", AbsorptionBeforeReview)
			a.Target, a.Gating = "", true
			return a
		}(),
	} {
		if err := a.Validate(); err != nil {
			t.Errorf("the prose rule's %s decision does not validate: %v", name, err)
		}
	}
}

// A finding DEFERRED PAST THE ABSORPTION BOUND (run_5c7c16d1, epic hn6) is the
// bound's rule decision about THIS repository: a backlog tick with an owner,
// gating nothing whatever the verdict said — and it cannot be crossed with a
// gating, routed or predicted record.
//
// short: Validate over records already in memory
func TestAPastBoundAbsorptionRecordAgreesWithItself(t *testing.T) {
	t.Parallel()
	pastBound := func() Absorption {
		a := testAbsorption("5e1ec7ed")
		a.Gating, a.ItemID, a.Basis = false, "", AbsorptionRule
		a.TickID, a.Placement = "k2p", AbsorptionPastBound
		a.Reason = "deferred past the absorption bound: the chain already carries 3 and the bound is 3"
		return a
	}
	if err := pastBound().Validate(); err != nil {
		t.Fatalf("a finding deferred past the bound does not validate: %v", err)
	}
	for name, mutate := range map[string]func(*Absorption){
		"gating":      func(a *Absorption) { a.Gating = true },
		"an item":     func(a *Absorption) { a.ItemID = "A1" },
		"predicted":   func(a *Absorption) { a.Basis, a.Confidence = AbsorptionPredicted, 0.7 },
		"routed":      func(a *Absorption) { a.Target = "example/upstream" },
		"a repo tick": func(a *Absorption) { a.TickID = "example/upstream:k2p" },
	} {
		a := pastBound()
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: a past-bound decision that contradicts itself validated: %+v", name, a)
		}
	}
}
