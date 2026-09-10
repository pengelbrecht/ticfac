package parity

import (
	"strings"
	"testing"
)

// contracts/sweep-selection-contract.json — STRUCTURAL.
//
// Which ticks a sweep selects, the fields that decide it, and the total order
// they are selected in. ticfac's reconciler computes waves over a tick list
// (SPEC §12 Phase 1 step 2) and will follow this order — a wave that is a
// different set on two runs is a wave nobody can reproduce. It is structural
// today because the selection runs over records ticfac reads through
// `tk --json`, and there is no tk client here yet.
//
// What is asserted is the part that makes the contract usable when that client
// lands: the field names it decides on, the closure of the declaration's key
// set, and — the one thing worth catching early — that the ORDER is total.
// A sort that is not total falls back to map iteration for its ties, and then
// two runs of one sweep select different ticks with nothing failing.

const sweepSelectionFile = "sweep-selection-contract.json"

type sweepSelection struct {
	Fields        map[string]string `json:"fields"`
	ClosedStatus  string            `json:"closed_status"`
	EpicType      string            `json:"epic_type"`
	AwaitingHuman struct {
		Fields                  []string `json:"fields"`
		ManualTrueMeansAwaiting bool     `json:"manual_true_means_awaiting"`
	} `json:"awaiting_human"`
	Order struct {
		Rule             string `json:"rule"`
		WaveComputeRule  string `json:"wave_compute_rule"`
		AgeIsOldestFirst bool   `json:"age_is_oldest_first"`
	} `json:"order"`
	Declaration struct {
		Path         string         `json:"path"`
		Table        string         `json:"table"`
		Keys         []string       `json:"keys"`
		RequiredKeys []string       `json:"required_keys"`
		Tiers        []string       `json:"tiers"`
		Gates        []string       `json:"gates"`
		Example      map[string]any `json:"example"`
	} `json:"declaration"`
}

func TestSweepSelectionFieldsAndVocabulary(t *testing.T) {
	var c sweepSelection
	readContract(t, sweepSelectionFile, &c)

	if len(c.Fields) == 0 {
		t.Fatal("the contract names no fields, so nothing decides selection")
	}
	for key, name := range c.Fields {
		if name == "" {
			t.Errorf("field %q maps to an empty record field", key)
		}
	}
	for _, needed := range []string{"id", "type", "status", "priority", "created_at"} {
		if c.Fields[needed] == "" {
			t.Errorf("the contract does not name the %q field; the order below is computed from it", needed)
		}
	}
	if c.ClosedStatus == "" || c.EpicType == "" {
		t.Error("the contract must name the closed status it excludes and the epic type it treats apart")
	}

	// The two ways a tick says a human owns it. Dropping one silently sweeps a
	// tick a person is holding.
	if len(c.AwaitingHuman.Fields) == 0 {
		t.Fatal("no field marks a tick as awaiting a human")
	}
	for _, name := range c.AwaitingHuman.Fields {
		if c.Fields[name] == "" {
			t.Errorf("awaiting_human names %q, which is not one of the contract's fields", name)
		}
	}
	if !c.AwaitingHuman.ManualTrueMeansAwaiting {
		t.Error("a manual tick that is not awaiting a human is a tick a sweep may take from someone")
	}
}

// The order has to be TOTAL, and it has to end in the id: priority and
// created_at can tie, and a tie broken by map iteration is a different wave on
// every run.
func TestTheSelectionOrderIsTotalAndEndsInTheID(t *testing.T) {
	var c sweepSelection
	readContract(t, sweepSelectionFile, &c)

	for label, rule := range map[string]string{
		"order.rule":              c.Order.Rule,
		"order.wave_compute_rule": c.Order.WaveComputeRule,
	} {
		if rule == "" {
			t.Errorf("%s is empty", label)
			continue
		}
		keys := strings.Split(rule, ",")
		last := strings.TrimSpace(keys[len(keys)-1])
		if !strings.HasPrefix(last, c.Fields["id"]) {
			t.Errorf("%s is %q; the last key must be the id, or ties are broken by whatever the runtime does",
				label, rule)
		}
		if !strings.Contains(rule, c.Fields["priority"]) {
			t.Errorf("%s does not order by priority", label)
		}
	}
	if !strings.Contains(c.Order.Rule, c.Fields["created_at"]) {
		t.Errorf("order.rule %q does not order by age", c.Order.Rule)
	}
	if !c.Order.AgeIsOldestFirst {
		t.Error("age is newest-first; a sweep would then start with what it has least evidence about")
	}
}

// The declaration's key set is closed, its required keys are a subset of it,
// and its example uses only keys it declares. A sweep runs with nobody
// watching, so a key silently ignored is a budget nobody set.
func TestSweepDeclarationKeysAreClosed(t *testing.T) {
	var c sweepSelection
	readContract(t, sweepSelectionFile, &c)

	d := c.Declaration
	if d.Path == "" || d.Table == "" {
		t.Fatal("the contract does not say where a sweep is declared")
	}
	if len(d.Keys) == 0 {
		t.Fatal("the declaration has no keys")
	}
	declared := map[string]bool{}
	for _, key := range d.Keys {
		if declared[key] {
			t.Errorf("declaration key %q appears twice", key)
		}
		declared[key] = true
	}
	if len(d.RequiredKeys) == 0 {
		t.Error("no key is required; a sweep with no budget and no bound is an unattended run")
	}
	for _, key := range d.RequiredKeys {
		if !declared[key] {
			t.Errorf("required key %q is not one of the declared keys", key)
		}
	}
	for key := range d.Example {
		if !declared[key] {
			t.Errorf("the example uses key %q, which the declaration does not allow", key)
		}
	}
	for _, key := range d.RequiredKeys {
		if _, ok := d.Example[key]; !ok {
			t.Errorf("the example omits the required key %q", key)
		}
	}

	// The two closed vocabularies. A tier or gate the example uses and the
	// list does not is a value nothing would accept.
	for label, pair := range map[string][2]any{
		"tier":             {d.Tiers, d.Example["tier"]},
		"gate_on_complete": {d.Gates, d.Example["gate_on_complete"]},
	} {
		allowed, _ := pair[0].([]string)
		if len(allowed) == 0 {
			t.Errorf("%s has no closed set of values", label)
			continue
		}
		value, ok := pair[1].(string)
		if !ok {
			continue // the key is optional and the example may omit it
		}
		if !contains(allowed, value) {
			t.Errorf("the example's %s is %q, which is not one of %v", label, value, allowed)
		}
	}
}
