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
//     worth of absorptions refuses to grow one more;
//   - and the bound NEVER HALTS FOR A PERSON (run_5c7c16d1, epic hn6,
//     2026-10-04: the 4th link of one chain stopped the run, and the
//     operator filed it to the backlog by hand — the manual step that is a
//     defect here). Past the bound the run files the finding as a BACKLOG
//     TICK with an owner, exactly what `ticfac triage … =file` does, records
//     it (the feed's finding_backlogged_past_bound), names it in the epic PR
//     body as deferred past the absorption bound, and carries on — even when
//     the finding claims to gate a done item: the bound wins, and the
//     close-out and the final review see the deferral and judge it;
//   - the deferral carries the FULL ABSORPTION CHAIN that produced it —
//     which tick reported which finding, what decided each absorption, which
//     tick each became — never only the count;
//   - the bound is checked only where the recursion actually extends: a
//     non-gating finding goes to the backlog and a chain at depth zero
//     absorbs.

// Past the bound, end to end: every tick's work discovers a NEW gating
// finding (the fake runner's finding_chain mode — an absorbed tick's own
// attempt reports the next link), the done's command answers broken (the
// observed gate), and the bound is ONE absorption per chain. The plan's own
// findings absorb with nobody triaging — depth zero, within the bound — and
// the absorbed tick's finding would be the SECOND link of one chain, past the
// bound: the run files it to the backlog and carries on to the end.
func TestPastTheAbsorptionDepthBoundTheFindingIsBackloggedAndTheRunCarriesOn(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_chain"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_chain",
		absorptionDepth: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want completed: the bound defers a finding, it never stops the run",
			result.State, result.Failure)
	}

	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	byTick := map[string]runstate.Absorption{}
	var deferred []runstate.Absorption
	for _, record := range records {
		byTick[record.TickID] = record
		if record.Placement == runstate.AbsorptionPastBound {
			deferred = append(deferred, record)
		}
	}
	if len(deferred) == 0 {
		t.Fatalf("no finding was deferred past the bound (records: %+v): the chain fixture grows past any bound", records)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	children := epicChildren(t, f)
	for _, record := range deferred {
		// THE RECORD: decided by the bound's rule, gating nothing, and saying
		// why — the bound, the chain, and the verdict it overrode.
		if record.Gating || record.Basis != runstate.AbsorptionRule || record.ItemID != "" {
			t.Errorf("the past-bound decision is %+v, want the rule's non-gating backlog", record)
		}
		finding, ok, err := store.Finding(record.Key)
		if err != nil || !ok {
			t.Fatalf("read the deferred finding %s: %v %v", record.Key, ok, err)
		}
		// The finding was reported by an ABSORBED tick — a link of a chain,
		// never plan work: the bound governs the recursion only.
		link, ok := byTick[finding.TickID]
		if !ok || link.Placement == runstate.AbsorptionPastBound {
			t.Errorf("the deferred finding %s was reported by %s, which no absorption created: the bound "+
				"governs the recursion, and a deferral over a tick no chain produced is the wrong one",
				record.Key, finding.TickID)
			continue
		}
		for _, named := range []string{
			"the bound is 1",
			"absorption bound",
			"gates done item A1", // the verdict the bound overrode
			link.Key,             // the finding that became the reporting tick
			link.TickID,          // the tick the first absorption created
		} {
			if !strings.Contains(record.Reason, named) {
				t.Errorf("the deferral's reason does not carry %q: %s", named, record.Reason)
			}
		}
		if linkFinding, ok, _ := store.Finding(link.Key); ok && !strings.Contains(record.Reason, linkFinding.TickID) {
			t.Errorf("the deferral's reason does not name the plan tick %s the chain started from: %s",
				linkFinding.TickID, record.Reason)
		}
		// THE FINDING IS TRIAGED, by the run, as `=file` would have: promoted
		// to the backlog tick, never left proposed for a person.
		if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
			t.Errorf("the deferred finding %s is %s (promoted as %q), want promoted to its backlog tick %s",
				record.Key, finding.Status, finding.PromotedAs, record.TickID)
		}
		// THE BACKLOG TICK: open, with an owner, outside the epic, never
		// dispatched — the recursion stops here, and the run goes on.
		tick, ok := state.Ticks[record.TickID]
		if !ok {
			t.Fatalf("the backlog tick %s does not exist", record.TickID)
		}
		if tick.Parent != "" || tick.Status != "open" || tick.Owner == "" {
			t.Errorf("the deferred tick is parent %q, status %s, owner %q: want an open backlog tick with an "+
				"owner, outside the epic", tick.Parent, tick.Status, tick.Owner)
		}
		if !strings.Contains(tick.Description, "absorption bound") {
			t.Errorf("the deferred tick does not say it was deferred past the absorption bound: %s", tick.Description)
		}
		if children[record.TickID] != "" {
			t.Errorf("the deferred tick %s is a child of the epic", record.TickID)
		}
		if d := f.dispatch(record.TickID); d.TickID != "" {
			t.Errorf("a worker was dispatched at the deferred tick %s: %+v", record.TickID, d)
		}
	}

	// THE FEED SAYS SO, at the moment it happened, on its own stage — and the
	// run never held.
	events := feedStages(t, f.Repo.Dir, "r-fixture")
	line := detailOfStage(events, StageBackloggedPastBound)
	if line == "" {
		t.Fatalf("no %s line in the feed (stages: %v)", StageBackloggedPastBound, feedStagesOf(events))
	}
	namesADeferral := false
	for _, record := range deferred {
		namesADeferral = namesADeferral || strings.Contains(line, "backlog tick "+record.TickID)
	}
	if !strings.Contains(line, "the bound is 1") || !namesADeferral {
		t.Errorf("the feed's deferral line does not name the bound and the backlog tick: %q", line)
	}
	if countStage(events, StageRunHeld) != 0 {
		t.Errorf("the run held (%q): the bound never halts for a person", detailOfStage(events, StageRunHeld))
	}
}

// The recorded bound, end to end (tick wz0, finding 95f5ee1a): the warm run
// is started with a RAISED bound and killed the moment its first absorption
// is durable, and the cold restart — invoked without the flag, carrying
// nothing but what is on origin — must apply the bound the warm run ran
// with: the chain that is deferred carries FOUR links and the deferral names
// the RECORDED bound, where a restart applying the bare default of 3 defers
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
	// chain grows past the depth a bare default would have deferred it at.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold, result, err := f.run(clone, fixtureOptions{gate: observedGate, mode: "finding_chain"})
	if err != nil {
		t.Fatalf("the cold run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want completed: past the bound the finding is deferred and the "+
			"run carries on", result.State, result.Failure)
	}

	// THE DEFERRAL NAMES THE RECORDED BOUND AND A CHAIN THE DEFAULT COULD
	// NEVER HAVE — the one observable that separates "the restart honoured
	// the record" from "the restart silently dropped back to 3".
	store := openRunStore(t, clone.Dir, cold.IntegrationBranch(), cold.RunID())
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	var deferred *runstate.Absorption
	for i := range records {
		if records[i].Placement == runstate.AbsorptionPastBound {
			deferred = &records[i]
			break
		}
	}
	if deferred == nil {
		t.Fatalf("no finding was deferred past the bound: the chain fixture grows past any bound (%+v)", records)
	}
	for _, named := range []string{"the bound is 4", "already carries 4"} {
		if !strings.Contains(deferred.Reason, named) {
			t.Errorf("the deferral does not say %q, the recorded bound the restart was bound by: %s",
				named, deferred.Reason)
		}
	}

	// And the record says the same thing the decision applied: untouched by
	// the restart, because a cold restart adopts the record, it does not
	// rewrite it.
	standing, ok, err := store.AbsorptionBound()
	if err != nil || !ok {
		t.Fatalf("read the recorded bound after the restart: %v %v", ok, err)
	}
	if standing.Bound != 4 {
		t.Errorf("the recorded bound is %d after the restart, want the 4 untouched: a cold restart adopts the "+
			"record, it does not rewrite it", standing.Bound)
	}
}

// The deferral as the epic PR's reviewer reads it: the "where to look first"
// list names every tick deferred past the absorption bound, and the record's
// own lines say what it is — never the prose rule's "the acceptance is prose".
//
// short: formatting over records already in memory
func TestAFindingDeferredPastTheBoundIsNamedForTheReviewer(t *testing.T) {
	t.Parallel()
	record := runstate.Absorption{Key: "k4", TickID: "b04", Basis: runstate.AbsorptionRule,
		Placement: runstate.AbsorptionPastBound, Reason: "past the absorption bound"}
	look := (&Reconciler{}).lookFirst(nil, -1, nil, []runstate.Absorption{record})
	if !strings.Contains(look, "deferred past the absorption bound") || !strings.Contains(look, "b04") {
		t.Errorf("the reviewer's first list does not name the deferral: %s", look)
	}
	if got := verdictLine(record); !strings.Contains(got, "absorption bound") || strings.Contains(got, "prose") {
		t.Errorf("the deferral's verdict line reads %q", got)
	}
	if got := placementLine(record); !strings.Contains(got, "deferred past the absorption bound") {
		t.Errorf("the deferral's placement line reads %q", got)
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
