package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The recursion's bound (tick qjj), pinned against the same harness every
// other run-level guarantee here is pinned with.
//
// THE CASES, one per acceptance clause:
//
//   - the recursion is BOUNDED: a chain that already carries the bound's
//     worth of absorptions refuses to grow one more, and the refusal is a
//     STOP for a person — the run ends held, not looping;
//   - the stop carries the FULL ABSORPTION CHAIN that produced it — which
//     tick reported which finding, what decided each absorption, which tick
//     each became — never only the count;
//   - the bound is checked only where the recursion actually extends: a
//     non-gating finding goes to the backlog and a chain at depth zero
//     absorbs, so the stop is rare and meaningful rather than the human
//     gate this epic exists to remove, rebuilt under a different name;
//   - the finding that tripped the bound is left a person's, drafted and
//     untriaged, so the stop is a decision point rather than a discovery
//     that vanished.

// Exceeding the bound, end to end: every tick's work discovers a NEW gating
// finding (the fake runner's finding_chain mode — an absorbed tick's own
// attempt reports the next link), the done's command answers broken while any
// finding stands (the observed gate), and the bound is set to ONE absorption
// per chain. The plan's own findings absorb with nobody triaging — depth
// zero, within the bound — and the absorbed tick's finding would be the
// SECOND link of one chain, past the bound: the run stops for a person, and
// the stop carries the chain that produced it.
func TestExceedingTheAbsorptionDepthBoundStopsTheRunForAPersonWithTheChain(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_chain"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_chain",
		absorptionDepth: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want failed holding for a person: an unbounded recursion is "+
			"an epic that never closes, and exceeding the bound is the stop: %+v", result.State, result.Failure)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedAbsorptionDepth {
		t.Fatalf("the failure is %+v, want the absorption depth bound's hold: exceeding the bound is "+
			"THE thing that stops for a person", result.Failure)
	}

	// THE STOP CARRIES THE CHAIN, not only the count: the tick the refusal
	// names is an absorbed tick (a link of a chain, never plan work), and the
	// record of the absorption that created it names the finding and the
	// discovering plan tick — both of which the message must say, because a
	// person judging whether the run was right to keep going reads WHICH
	// finding led to which, not a number.
	tripped := result.Failure.TickID
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	var link *runstate.Absorption
	for i := range records {
		if records[i].TickID == tripped {
			link = &records[i]
		}
	}
	if link == nil {
		t.Fatalf("the refused tick %s was not created by an absorption: the bound governs the "+
			"recursion, and a stop over a tick no chain produced is the wrong stop (records: %+v)", tripped, records)
	}
	finding, ok, err := store.Finding(link.Key)
	if err != nil || !ok {
		t.Fatalf("read the finding the absorbed tick %s came from: %v %v", tripped, ok, err)
	}
	for _, named := range []string{
		"the bound is 1",
		finding.TickID, // the plan tick that reported the first link
		link.Key,       // the finding that became the refused tick
		link.TickID,    // the tick the first absorption created
	} {
		if !strings.Contains(result.Failure.Message, named) {
			t.Errorf("the stop's message does not carry %q, a link of the chain that produced it: %s",
				named, result.Failure.Message)
		}
	}
	if !strings.Contains(result.Failure.Message, tripped) {
		t.Errorf("the stop's message does not name the tick whose finding tripped the bound: %s",
			result.Failure.Message)
	}

	// THE CHAIN IS ABSORPTIONS THAT HAPPENED, not a summary of them: the
	// bound's worth of links absorbed with nobody triaging before the stop —
	// the refusal is the run's own arithmetic, not a person's intervention.
	if len(records) < 1 {
		t.Fatalf("the run recorded %d absorption(s) before the stop, want at least the plan's own "+
			"finding absorbed: a bound that stops the FIRST absorption is the human gate rebuilt",
			len(records))
	}

	// The finding that tripped the bound is left a PERSON'S: drafted, still
	// proposed, with no absorption record deciding it — the stop is a
	// decision point, and a discovery that vanished is the 604 shape.
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	var trippedDraft *runstate.Finding
	for i := range findings {
		if findings[i].TickID == tripped && findings[i].Status == runstate.FindingProposed {
			trippedDraft = &findings[i]
		}
	}
	if trippedDraft == nil {
		t.Fatalf("no finding of the refused tick %s is still proposed: the stop must hand the "+
			"discovery to a person rather than absorb it or drop it (findings: %+v)", tripped, findings)
	}
	if _, decided, err := store.Absorption(trippedDraft.Key); err != nil {
		t.Fatal(err)
	} else if decided {
		t.Fatalf("the tripped finding %s carries an absorption record: the run recorded the very "+
			"decision the bound forbids", trippedDraft.Key)
	}

	// AND THE FEED SAID SO, at the moment it happened: the bound's own stage
	// on the tick that reported, and the run's hold naming the refusal — a
	// stop nobody can see is indistinguishable from a run that died.
	events := feedStages(t, f.Repo.Dir, "r-fixture")
	if line := detailOfStage(events, StageAbsorptionBoundExceeded); line == "" {
		t.Errorf("no %s line in the feed: exceeding the bound is the thing that stops, and it must be "+
			"announced where the tick's own story is (stages: %v)", StageAbsorptionBoundExceeded, feedStagesOf(events))
	}
	if line := detailOfStage(events, StageRunHeld); !strings.Contains(line, RefusedAbsorptionDepth) {
		t.Errorf("the feed's %s line does not name the bound's hold: %q", StageRunHeld, line)
	}
}

// The bound's arithmetic, so every edge is pinned without a fixture: a chain
// may carry up to the bound's worth of links, and only a chain already at the
// bound refuses the next absorption. Depth counts absorption links, never
// findings — a redelivered finding extends nothing — and the bound is never
// "off": zero or below is a misconfiguration, and the default owns the
// decision rather than an unbounded recursion nobody configured.
//
// short: pure arithmetic over links already in memory
func TestAbsorptionDepthIsExceededOnlyByAChainTheBoundCannotCarry(t *testing.T) {
	t.Parallel()
	one := []chainLink{{}} // one absorption, whatever it decided

	if absorptionDepthExceeded(nil, 1) {
		t.Error("a chain with no absorptions exceeds the bound of 1: the FIRST link is the absorption " +
			"this epic exists to make, and a bound that stops it is the human gate rebuilt")
	}
	if absorptionDepthExceeded(nil, 0) {
		t.Error("a misconfigured bound of 0 stops the first absorption: zero must be the default, " +
			"never an unbounded recursion quietly unbound")
	}
	if !absorptionDepthExceeded(one, 1) {
		t.Error("a chain of one absorption does not exceed the bound of 1: the second link is the stop")
	}
	if absorptionDepthExceeded(one, 3) {
		t.Error("a chain of one absorption exceeds the bound of 3: the stop must be rare, and this " +
			"chain is not yet at the bound")
	}
}

// The chain as a person reads it, so the stop's most important property is
// pinned without a repository: EVERY link names the tick that reported, the
// finding, what decided it, and the tick it became — because a person judging
// whether the run was right to keep going reads WHICH finding led to which,
// and a count sends nobody anywhere.
//
// short: formatting over records already in memory
func TestTheChainNarrativeNamesEveryLink(t *testing.T) {
	t.Parallel()
	links := []chainLink{
		{Finding: runstate.Finding{Key: "k1", TickID: "a1", Title: "The gate is red at base"},
			Record: runstate.Absorption{Key: "k1", TickID: "m01", Gating: true, ItemID: "A1",
				Basis: runstate.AbsorptionObserved}},
		{Finding: runstate.Finding{Key: "k2", TickID: "m01", Title: "The absorbed fix broke the fixture"},
			Record: runstate.Absorption{Key: "k2", TickID: "m02", Gating: true, ItemID: "A2",
				Basis: runstate.AbsorptionPredicted}},
	}
	narrative := chainNarrative(links)
	for _, named := range []string{"tick a1 reported finding k1", "\"The gate is red at base\"",
		"gates done item A1", "became tick m01", "tick m01 reported finding k2", "became tick m02"} {
		if !strings.Contains(narrative, named) {
			t.Errorf("the chain narrative omits %q, a link a person judges the run by: %s", named, narrative)
		}
	}

	// The empty chain says so in words rather than saying nothing: the refusal
	// reads the same whether the walk found links or not.
	if got := chainNarrative(nil); !strings.Contains(got, "no earlier absorption") {
		t.Errorf("an empty chain narrates %q, want it to say no absorption produced the tick", got)
	}
}
