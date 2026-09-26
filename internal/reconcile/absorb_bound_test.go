package reconcile

import (
	"path/filepath"
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

// The recorded bound, end to end (tick wz0, finding 95f5ee1a): the warm run
// is started with a RAISED bound and killed the moment its first absorption
// is durable, and the cold restart — invoked without the flag, carrying
// nothing but what is on origin — must apply the bound the warm run ran
// with: the chain that stops the run carries FOUR links and the stop names
// the RECORDED bound, where a restart applying the bare default of 3 stops
// a chain at three links it would never have let reach four.
func TestAColdRestartHonoursTheRecordedAbsorptionDepthBound(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_chain"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	// The warm run: the bound is RAISED to 4 explicitly, and the kill lands
	// at the first absorption — after the decision record, the bound record
	// and the created tick, all durable on origin.
	killed := fixtureOptions{gate: observedGate, mode: "finding_chain", absorptionDepth: 4,
		stopAfter: stopAt("a1", StageAbsorbed)}
	warm, _, err := f.run(f.Repo, killed)
	if err != nil {
		if _, ok := err.(*killedAt); !ok {
			t.Fatalf("the warm run ended with %v, not the kill at the first absorption", err)
		}
	}

	// THE BOUND IS ON THE RUN BRANCH, not only in the invocation that named
	// it: read from origin, by a fresh reader holding nothing warm.
	reader := openRunStore(t, f.Repo.Dir, "epic/qeu", warm.RunID())
	recorded, ok, err := reader.AbsorptionBound()
	if err != nil || !ok {
		t.Fatalf("the warm run recorded no depth bound on the run branch: %v %v — a bound that lives only in "+
			"the invocation dies with it", ok, err)
	}
	if recorded.Bound != 4 {
		t.Fatalf("the recorded bound is %d, want the 4 the warm run was raised to", recorded.Bound)
	}

	// The cold restart, WITHOUT the flag: adopts the recorded bound, and the
	// chain grows past the depth a bare default would have stopped it at.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold, result, err := f.run(clone, fixtureOptions{gate: observedGate, mode: "finding_chain"})
	if err != nil {
		t.Fatalf("the cold run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want failed on the depth bound's hold: the chain fixture grows past any bound: %+v",
			result.State, result.Failure)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedAbsorptionDepth {
		t.Fatalf("the failure is %+v, want the absorption depth bound's hold", result.Failure)
	}

	// THE STOP NAMES THE RECORDED BOUND AND A CHAIN THE DEFAULT COULD NEVER
	// HAVE — the one observable that separates "the restart honoured the
	// record" from "the restart silently dropped back to 3": a run applying
	// the default stops a chain at THREE links, and no chain of it ever
	// reaches four.
	for _, named := range []string{"the bound is 4", "already carries 4"} {
		if !strings.Contains(result.Failure.Message, named) {
			t.Errorf("the stop's message does not say %q, the recorded bound the restart was bound by: %s",
				named, result.Failure.Message)
		}
	}

	// And the record says the same thing the decision applied: untouched by
	// the restart, because a cold restart adopts the record, it does not
	// rewrite it.
	store := openRunStore(t, clone.Dir, cold.IntegrationBranch(), cold.RunID())
	standing, ok, err := store.AbsorptionBound()
	if err != nil || !ok {
		t.Fatalf("read the recorded bound after the restart: %v %v", ok, err)
	}
	if standing.Bound != 4 {
		t.Errorf("the recorded bound is %d after the restart, want the 4 untouched: a cold restart adopts the "+
			"record, it does not rewrite it", standing.Bound)
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

// The bound's precedence (tick wz0, finding 95f5ee1a), pinned without a
// fixture so the ORDER is the tested thing, not a side effect of a run: an
// explicit flag is the person's raise and wins over the record — the depth
// refusal's own escape hatch must not be dead — while an unnamed bound
// adopts whatever the run branch records, so a cold restart honours the
// bound the warm run ran with instead of dropping to the default over git
// state it had already absorbed past.
//
// short: pure precedence over numbers already in memory
func TestTheRecordedBoundOutranksAnUnnamedFlagAndYieldsToAnExplicitOne(t *testing.T) {
	t.Parallel()

	// An unnamed invocation over a standing record: the RECORD wins — the
	// cold-restart case the record exists for.
	if got := resolvedAbsorptionDepth(5, true, 3, false); got != 5 {
		t.Errorf("an unnamed bound of 3 over a recorded 5 resolved to %d, want the recorded 5: "+
			"a cold restart applies the bound the warm run ran with", got)
	}
	// An explicit raise over a standing record: the FLAG wins — the escape
	// hatch the refusal names, and a record that out-ranked it would be a
	// no-op.
	if got := resolvedAbsorptionDepth(5, true, 7, true); got != 7 {
		t.Errorf("an explicit raise to 7 over a recorded 5 resolved to %d, want the person's 7", got)
	}
	// An explicit LOWERING is still explicit: the person named a number, and
	// the run applies it — never a record that out-ranks the person.
	if got := resolvedAbsorptionDepth(5, true, 2, true); got != 2 {
		t.Errorf("an explicit lowering to 2 over a recorded 5 resolved to %d, want the person's 2", got)
	}
	// No record, no explicit flag: the flag's own (already-defaulted) value.
	if got := resolvedAbsorptionDepth(0, false, 3, false); got != 3 {
		t.Errorf("no record and an unnamed flag resolved to %d, want the default 3", got)
	}
	// No record, explicit: the first decision records what the person named.
	if got := resolvedAbsorptionDepth(0, false, 5, true); got != 5 {
		t.Errorf("no record and an explicit 5 resolved to %d, want 5 recorded", got)
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
