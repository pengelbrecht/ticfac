package reconcile

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/gating"
	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The absorption itself (tick npq), end to end against the same harness every
// other run-level guarantee here is pinned with: a real repository, a real
// origin, the real run-state store, the real tracker-tree publishing path and
// the real local subprocess executor.
//
// THE CASES, one per acceptance clause:
//
//  1. a finding judged GATING is promoted into the running epic as a tick
//     with no person triaging, placed before the final review — driven here
//     by an OBSERVED verdict (the done's own command answers) and by a
//     PREDICTED one (the classifier's, and the documented fallback's);
//  2. the absorption decision — item id, verdict, observed or predicted,
//     confidence — is a decision record on the run branch;
//  3. a cold re-derivation from git reaches the same epic graph as the warm
//     run, proven by killing the run the moment the absorption is durable
//     and restarting from a fresh clone;
//  4. a finding the done is reachable without still becomes a backlog tick
//     with an owner, and is still reported;
//  5. an epic whose done is prose refuses to absorb and says so, leaving the
//     finding a person's at the close-out hold.

// setEpicAcceptance writes the epic's own definition of done: the [A<n>]-marked
// items the absorption decision is driven against.
func setEpicAcceptance(t *testing.T, f *fixture, criteria string) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	epic := state.Ticks["qeu"]
	epic.AcceptanceCriteria = criteria
	state.Ticks["qeu"] = epic
	f.Tracker.write(t, state)
}

// localFindingReport is the worker's report of the one finding the
// finding_local mode reports, in the shape findingKey hashes — the finding
// every absorption here is driven on.
func localFindingReport() subprocess.Finding {
	return subprocess.Finding{
		Kind: "proposed-tick", Title: "A finding the fake runner proposes",
		Body: "Discovered beside the work, reported mechanically.", Severity: "high", Target: "",
	}
}

// fakeGatingClassifier stands in for *jev.Client at the prediction seam: one
// answer, handed back for whatever question the predictor asks.
type fakeGatingClassifier struct {
	result jev.AnswerResult
	calls  int
}

func (f *fakeGatingClassifier) Ask(_ context.Context, _ string, _ []jev.Question) (jev.AnswerResult, error) {
	f.calls++
	return f.result, nil
}

// answerOver builds the classifier's answer over the finding the test's
// fixture reports, with the given distribution and choice.
func answerOver(t *testing.T, probabilities map[string]float64, choice string) jev.AnswerResult {
	t.Helper()
	return jev.AnswerResult{
		Answers: map[string]jev.Answer{findingKey(localFindingReport()): {
			Choice:        choice,
			Confidence:    0.62,
			Probabilities: probabilities,
		}},
		Model: "jev-2026-09",
	}
}

// absorbingAbsorption reads the run's one absorption decision from ORIGIN, and
// refuses the test rather than passing quietly when there is not exactly one.
func absorbingAbsorption(t *testing.T, repo *testRepo, reconciler *Reconciler) runstate.Absorption {
	t.Helper()
	store := openRunStore(t, repo.Dir, reconciler.IntegrationBranch(), reconciler.RunID())
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("the run recorded %d absorption decision(s), want exactly one: %+v", len(records), records)
	}
	return records[0]
}

// epicChildren is the set of the epic's children in the tracker's graph — the
// epic graph a warm run and a cold re-derivation must both reach.
func epicChildren(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	graph, err := f.Tracker.Graph(context.Background(), "qeu")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			out[task.ID] = task.Status
		}
	}
	return out
}

// firstSeen reads the journal for when each tick was first dispatched and
// closed, so "before the final review" is an assertion about order, not hope.
type firstSeen struct {
	dispatched map[string]int
	closed     map[string]int
}

func orderOf(r *Reconciler) firstSeen {
	dispatched, closed := map[string]int{}, map[string]int{}
	for i, event := range r.Journal() {
		switch event.Stage {
		case StageDispatched, StageAdopted, StageRedispatched:
			if _, seen := dispatched[event.Tick]; !seen {
				dispatched[event.Tick] = i
			}
		case StageClosed:
			closed[event.Tick] = i
		}
	}
	return firstSeen{dispatched: dispatched, closed: closed}
}

// 1a. THE OBSERVED CASE, and the acceptance's own worked example: the done's
// command is bound to the item, the command answers non-zero while the finding
// stands, and the run absorbs — observed, no judgement, no person — placing the
// absorbed tick before the final review and closing the whole epic with nobody
// having triaged anything.
func TestAGatingFindingIsAbsorbedIntoTheRunningEpicWithNoPersonTriaging(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — an absorbed finding is worked by the run, and nothing here needs a person: %+v",
			result.State, result.Reason, result.Failure)
	}

	// THE DECISION RECORD, on the run branch: item, verdict, basis.
	record := absorbingAbsorption(t, f.Repo, r)
	if !record.Gating {
		t.Fatalf("the recorded decision is not gating: %+v", record)
	}
	if record.ItemID != "A1" {
		t.Errorf("the decision names item %q, want A1 — the absorption names which acceptance item was unreachable", record.ItemID)
	}
	if record.Basis != runstate.AbsorptionObserved {
		t.Errorf("the decision's basis is %q, want %s: the done's command ran and answered, so the verdict is an observation",
			record.Basis, runstate.AbsorptionObserved)
	}
	if record.Placement != runstate.AbsorptionBeforeReview {
		t.Errorf("the decision's placement is %q, want %s: the fix must land before the final review, not after it",
			record.Placement, runstate.AbsorptionBeforeReview)
	}

	// The tick the promotion created: a child of the RUNNING epic, sequenced
	// before the review — and the run worked it to a close with no person.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick, ok := state.Ticks[record.TickID]
	if !ok {
		t.Fatalf("the promoted tick %s does not exist in the tracker", record.TickID)
	}
	if tick.Parent != "qeu" {
		t.Errorf("the absorbed tick's parent is %q, want qeu: it is part of the running epic, not appended after it", tick.Parent)
	}
	if tick.DiscoveredFrom == "" {
		t.Error("the absorbed tick carries no discovered_from: a tick filed with no provenance is the 9t0 shape")
	}
	if tick.Status != "closed" {
		t.Errorf("the absorbed tick is %s, want closed: the run works what it absorbs", tick.Status)
	}
	if !has(state.BlockedBy["rv"], record.TickID) {
		t.Errorf("the review rv is not blocked-by the absorbed tick %s: the review asserts the items, so the fix must land before it (edges: %v)",
			record.TickID, state.BlockedBy["rv"])
	}

	// BEFORE THE FINAL REVIEW, as an order of events, not a wave number.
	order := orderOf(r)
	if _, seen := order.dispatched[record.TickID]; !seen {
		t.Fatalf("the absorbed tick %s was never dispatched: %v", record.TickID, order.dispatched)
	}
	if order.dispatched["rv"] < order.closed[record.TickID] {
		t.Errorf("the review was dispatched at %d, before the absorbed tick closed at %d: the fix landed after the items were asserted",
			order.dispatched["rv"], order.closed[record.TickID])
	}

	// The finding is TRIAGED as the run's promotion, and nobody typed anything.
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	finding, ok, err := store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("the absorbed finding's draft cannot be read: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted {
		t.Fatalf("the finding is %s, want promoted: the absorption IS the triage", finding.Status)
	}
	if finding.PromotedAs != record.TickID {
		t.Errorf("the finding is promoted as %s, want the tick the absorption created (%s)", finding.PromotedAs, record.TickID)
	}
	if !strings.Contains(finding.TriagedBy, "ticfac run") {
		t.Errorf("the triage is attributed to %q, want the run: a decision nobody can attribute is one nobody can audit",
			finding.TriagedBy)
	}

	// And the feed said so, at the moment it happened.
	var said bool
	for _, event := range r.Journal() {
		if event.Stage == StageAbsorbed && event.Tick == "a1" {
			said = true
		}
	}
	if !said {
		t.Errorf("no %s line for the finding a1 reported: an absorption nobody can see is indistinguishable from a finding that vanished",
			StageAbsorbed)
	}
}

// has is the membership a placement assertion reads, named apart from the
// test-helper vocabulary other files here already own.
func has(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// 1b. THE PREDICTED CASE, through the documented fallback: the item is not yet
// runnable — nothing is bound — and no classifier is configured, so the
// predictor falls back to absorbing, recorded as a PREDICTION with the fallback
// saying why no model answered. Erring toward absorbing is the stated bias: a
// false negative closes an epic whose goal is unmet in a factory where nobody
// is watching.
func TestAnUnverifiedFindingIsAbsorbedByThePredictedFallback(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	// [A2] is bound to no command: nothing can run, and the item is the
	// classifier's — which this run does not have.
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — the fallback absorbs rather than stopping for a person: %+v",
			result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if !record.Gating {
		t.Fatalf("the recorded decision is not gating: %+v", record)
	}
	if record.Basis != runstate.AbsorptionPredicted {
		t.Errorf("the decision's basis is %q, want %s: a fallback is a guess, never a measurement", record.Basis,
			runstate.AbsorptionPredicted)
	}
	if record.Fallback == "" {
		t.Error("the decision records no fallback: a record that cannot say why no model answered reads as a prediction that was made")
	}
	if !strings.Contains(record.Reason, "A2") {
		t.Errorf("the decision's reason does not name the item at risk: %q", record.Reason)
	}
	if record.Placement != runstate.AbsorptionBeforeReview {
		t.Errorf("the placement is %q, want before the review", record.Placement)
	}
}

// 1c. THE CLASSIFIER'S PREDICTION, with the confidence recorded beside it:
// the item is not yet runnable, the classifier puts its mass on the item above
// the threshold, and the absorption is PREDICTED gating naming the item and the
// confidence. The other direction — mass below the threshold — is the backlog
// case below.
func TestAClassifiedPredictionAbsorbsWithItsConfidenceRecorded(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.7, "none": 0.3}, "A2")}

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	if classifier.calls == 0 {
		t.Fatal("the classifier was never asked: a configured classifier is the prediction tier's answer, not a decoration")
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if !record.Gating || record.ItemID != "A2" {
		t.Fatalf("the recorded decision is %+v, want gating naming item A2", record)
	}
	if record.Basis != runstate.AbsorptionPredicted {
		t.Errorf("the basis is %q, want predicted", record.Basis)
	}
	if record.Fallback != "" {
		t.Errorf("the decision records a fallback (%q) although a model answered", record.Fallback)
	}
	if record.Confidence == 0 {
		t.Error("the decision records no confidence: the acceptance names the confidence of a prediction, and a retro reads it")
	}
}

// 4. THE NON-GATING CASE, observed: every item's command passed while the
// finding stands, so the done is reachable and the finding becomes a backlog
// tick with an owner — still promoted by the run, still reported, never part of
// the running epic.
func TestANonGatingFindingBecomesABacklogTickWithAnOwner(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: absorbingGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: absorbingGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}

	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating {
		t.Fatalf("the recorded decision is gating: %+v — the done's command passed while the finding stood", record)
	}
	if record.Placement != runstate.AbsorptionBacklog {
		t.Errorf("the placement is %q, want the backlog", record.Placement)
	}
	if record.Basis != runstate.AbsorptionObserved {
		t.Errorf("the basis is %q, want observed: the command ran", record.Basis)
	}

	// A BACKLOG TICK WITH AN OWNER: the epic's own owner, a person — and not a
	// child of the running epic, which is what makes it backlog rather than
	// the epic's to absorb.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick, ok := state.Ticks[record.TickID]
	if !ok {
		t.Fatalf("the backlog tick %s does not exist", record.TickID)
	}
	if tick.Parent != "" {
		t.Errorf("the backlog tick's parent is %q, want none: it is not part of the running epic", tick.Parent)
	}
	if tick.Owner != "operator@example.com" {
		t.Errorf("the backlog tick's owner is %q, want the epic's owner: a backlog tick waits for a person", tick.Owner)
	}
	if tick.Status != "open" {
		t.Errorf("the backlog tick is %s, want open: the run does not work what it does not absorb", tick.Status)
	}
	if _, inEpic := epicChildren(t, f)[record.TickID]; inEpic {
		t.Errorf("the backlog tick %s is inside the epic graph: absorbing it would extend the epic rather than serve it", record.TickID)
	}

	// The run did not dispatch it, and the finding is promoted to it.
	order := orderOf(r)
	if _, dispatched := order.dispatched[record.TickID]; dispatched {
		t.Errorf("the backlog tick was dispatched by the run: work the epic does not need is a person's, not the run's")
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	finding, ok, err := store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the backlog finding's draft: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
		t.Fatalf("the finding is %s promoted as %s, want promoted as the backlog tick %s — still reported, never dropped",
			finding.Status, finding.PromotedAs, record.TickID)
	}
}

// 4b. THE NON-GATING CASE, predicted: the classifier puts its mass on 'none'
// and the finding becomes a backlog tick with the prediction's basis and
// confidence recorded — a guess the retro can score, never a measurement it
// would read as one.
func TestAClassifiedNoneBecomesABacklogTickAsAPrediction(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.05, "none": 0.95}, "none")}

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating {
		t.Fatalf("the recorded decision is gating: %+v — the mass sat below the threshold", record)
	}
	if record.Basis != runstate.AbsorptionPredicted {
		t.Errorf("the basis is %q, want predicted", record.Basis)
	}
	if record.Placement != runstate.AbsorptionBacklog {
		t.Errorf("the placement is %q, want the backlog", record.Placement)
	}
	if record.ItemID != "" {
		t.Errorf("a non-gating verdict names item %q: the done is reachable, nothing is unreachable", record.ItemID)
	}
}

// 5. THE REFUSAL: an epic whose acceptance carries no [A<n>] items has a done
// that is prose, nothing can be pointed at, and the run refuses to absorb
// rather than guessing — says so in the feed, and leaves the finding a
// person's at the close-out hold.
func TestAnEpicWithAProseDoneRefusesToAbsorbAndSaysSo(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	// The fixture's default epic acceptance: no [A<n>] marks at all.

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want failed on the close-out's untriaged hold: the refusal leaves the finding a person's",
			result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("the failure is %+v, want the close-out's untriaged-findings hold", result.Failure)
	}

	// THE RUN SAID SO: the feed names the refusal, so an operator reading why
	// the close-out is held sees that the run refused to guess at a done that
	// is prose, not that a finding fell on the floor.
	events := feedStages(t, f.Repo.Dir, "r-fixture")
	line := detailOfStage(events, StageAbsorptionRefused)
	if line == "" {
		t.Fatalf("no %s line in the feed: a refusal nobody can see reads as a finding that vanished; stages: %v",
			StageAbsorptionRefused, feedStagesOf(events))
	}
	if !strings.Contains(line, "refuses to absorb") && !strings.Contains(line, "refusing to absorb") {
		t.Errorf("the refusal line does not say the run refused to absorb: %q", line)
	}

	// And the decision was NOT made: no absorption record, no tick created, the
	// draft still proposed and still holding the close-out for a person.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("the run recorded %d absorption decision(s) over a done that is prose: it must refuse to absorb rather than guess", len(records))
	}
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Status != runstate.FindingProposed {
		t.Fatalf("the finding is %+v, want one still proposed and waiting for a person", findings)
	}
}

// stagesOf is the feed's stages, for a failure message that names what did
// happen rather than only what did not.
func feedStagesOf(events []runfeed.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Stage)
	}
	return out
}

// 3. THE COLD RE-DERIVATION: the run is killed the moment the absorption is
// durable — record on origin, tick created and placed, finding triaged — and a
// fresh clone, holding nothing but what is on origin, re-derives the SAME epic
// graph: the absorbed tick is admitted, worked before the review, and the epic
// closes with no person and no second absorption.
func TestAKilledRunsAbsorptionIsReDerivedColdFromGit(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	// The warm run: killed at the absorption itself, after every durable write
	// it made and before anything else — the state a crash would leave.
	killed := fixtureOptions{gate: observedGate, mode: "finding_local", stopAfter: stopAt("a1", StageAbsorbed)}
	warm, _, err := f.run(f.Repo, killed)
	if err != nil {
		// A stopAfter cut surfaces as the simulated kill; a run that ended any
		// other way here is a different failure and the assertions below will
		// name it.
		if _, ok := err.(*killedAt); !ok {
			t.Fatalf("the warm run ended with %v, not the kill after %s", err, StageAbsorbed)
		}
	}

	// What the warm run left on ORIGIN — read from git, not from any warm
	// memory: the absorption record, the promoted draft, the created tick and
	// the review's placement edge, all on the branch.
	reader := openRunStore(t, f.Repo.Dir, "epic/qeu", warm.RunID())
	records, err := reader.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("origin carries %d absorption record(s) at the kill, want the one the warm run made", len(records))
	}
	record := records[0]
	if !record.Gating || record.Placement != runstate.AbsorptionBeforeReview {
		t.Fatalf("the warm record is %+v, want gating placed before the review", record)
	}
	finding, ok, err := reader.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("the finding draft is not on origin: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted {
		t.Fatalf("the finding is %s at the kill, want promoted: the absorption was durable", finding.Status)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Ticks[record.TickID]; !exists {
		t.Fatalf("the promoted tick %s does not exist at the kill", record.TickID)
	}
	if !has(state.BlockedBy["rv"], record.TickID) {
		t.Fatalf("the review is not blocked-by the absorbed tick at the kill: the placement edge did not reach the tracker")
	}

	// The epic graph the WARM run reached: the original five children plus the
	// absorbed one. The cold re-derivation below must reach exactly this.
	warmGraph := epicChildren(t, f)
	if len(warmGraph) != 6 {
		t.Fatalf("the warm epic graph has %d children (%v), want 5 plus the absorbed tick", len(warmGraph), warmGraph)
	}

	// The COLD re-derivation: a fresh clone, a fresh run state, nothing but
	// what is on origin.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold, result, err := f.run(clone, fixtureOptions{gate: observedGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("the cold run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the cold run ended %s: %s — a re-derivation that cannot finish over its own absorbed tick reaches a smaller epic: %+v",
			result.State, result.Reason, result.Failure)
	}

	// THE SAME EPIC GRAPH: every child of the warm graph, closed.
	coldGraph := epicChildren(t, f)
	if len(coldGraph) != len(warmGraph) {
		t.Fatalf("the cold graph reached %d children (%v), want the warm run's %d (%v): a smaller epic than the warm run is the failure this tick exists to prevent",
			len(coldGraph), coldGraph, len(warmGraph), warmGraph)
	}
	for id := range warmGraph {
		if status, ok := coldGraph[id]; !ok {
			t.Fatalf("the cold graph lost %s, a child the warm run had", id)
		} else if status != "closed" {
			t.Errorf("the cold run left %s %s, want closed", id, status)
		}
	}

	// No SECOND absorption: the re-reported finding deduplicated against the
	// promoted draft, and the standing decision was never made twice.
	coldRecords, err := openRunStore(t, clone.Dir, cold.IntegrationBranch(), cold.RunID()).Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(coldRecords) != 1 || coldRecords[0].TickID != record.TickID {
		t.Fatalf("the cold run carries %d absorption record(s) (%+v), want the warm one untouched: a decision is never made twice",
			len(coldRecords), coldRecords)
	}
	created := f.Tracker.count("create:" + record.TickID)
	if created != 1 {
		t.Errorf("the promoted tick was created %d times, want once", created)
	}

	// And the cold run worked the absorbed tick BEFORE the review, from the
	// graph it re-derived rather than anything it was told.
	order := orderOf(cold)
	if _, seen := order.dispatched[record.TickID]; !seen {
		t.Fatalf("the cold run never dispatched the absorbed tick %s: the re-derivation did not reach it", record.TickID)
	}
	if order.dispatched["rv"] < order.closed[record.TickID] {
		t.Errorf("the cold review was dispatched at %d, before the absorbed tick closed at %d",
			order.dispatched["rv"], order.closed[record.TickID])
	}
}

// 3b. THE HALF-MADE PROMOTION: the kill lands BETWEEN the decision record
// and the triage — after the tick's creation publish, before anything else —
// and the restart must finish behind the record the kill left: the tick is
// created-if-absent (never clobbered), the placement edge is idempotent, the
// triage the kill skipped is completed, and the run then reaches the same
// epic the warm one was heading for. This is the record-as-resume-marker
// claim with its window actually cut open.
func TestAKilledRunsHalfMadeAbsorptionIsFinishedBehindItsRecord(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	// The cut: the moment the created tick's publish lands — after the record
	// and the tick, before the placement edge and the triage.
	atTheCreate := func(e Event) bool {
		return e.Stage == StagePublished && strings.Contains(e.Detail, "create tick ")
	}
	_, _, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_local", stopAfter: atTheCreate})
	if _, ok := err.(*killedAt); !ok {
		t.Fatalf("the warm run ended with %v, not the kill at the created tick's publish", err)
	}

	// What the kill left: the decision record and the tick on origin, the
	// finding still PROPOSED — the triage is the half that was not made.
	reader := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	records, err := reader.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("origin carries %d absorption record(s) at the cut, want the one the kill made", len(records))
	}
	record := records[0]
	finding, ok, err := reader.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the finding at the cut: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingProposed {
		t.Fatalf("the finding is %s at the cut, want still proposed: the kill landed before the triage", finding.Status)
	}

	// The restart, from a fresh clone: finishes behind the record — the tick
	// already there is left standing, the triage is completed — and reaches
	// the same completed epic.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold, result, err := f.run(clone, fixtureOptions{gate: observedGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("the cold run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the cold run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	store := openRunStore(t, clone.Dir, cold.IntegrationBranch(), cold.RunID())
	records, err = store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TickID != record.TickID {
		t.Fatalf("the finished run carries %+v, want the killed run's record finished — never a second decision", records)
	}
	finding, ok, err = store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the finding after the restart: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
		t.Fatalf("the finding is %s promoted as %s, want promoted as the record's tick %s: the triage the kill skipped is the resume's to finish",
			finding.Status, finding.PromotedAs, record.TickID)
	}
	// The tick exists exactly once — the second create was create-if-absent,
	// and the standing record was left alone.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick, exists := state.Ticks[record.TickID]
	if !exists || tick.Parent != "qeu" || tick.Status != "closed" {
		t.Fatalf("the promoted tick is %+v, want a closed child of the epic worked by the restart", tick)
	}
	created := f.Tracker.count("create:" + record.TickID)
	if created != 2 {
		t.Errorf("the tick's create was called %d times, want twice (the killed create and the idempotent resume)", created)
	}
}

// The pure half of the two-tier meeting point, so every branch of the
// combination is pinned without a fixture: an observation wins, an unresolved
// runnable item is never read as demonstrated, and where neither tier can
// answer the decision errs toward absorbing — recorded as a prediction.
//
// short: combineGating over verdicts already in memory
func TestCombineGatingAnswersEveryTierState(t *testing.T) {
	t.Parallel()

	done := acceptance.Done{Items: []acceptance.Resolved{
		{Item: acceptance.Item{ID: "A1"}, State: acceptance.Runnable, Command: "done"},
		{Item: acceptance.Item{ID: "A2"}, State: acceptance.Unverified},
	}}
	observedGating := &gating.Observed{Verdict: gating.Verdict{
		FindingID: "k", Gating: true, ItemID: "A1", Basis: gating.BasisObserved}, Commit: "abc123"}
	observedFine := &gating.Observed{Verdict: gating.Verdict{
		FindingID: "k", Gating: false, Basis: gating.BasisObserved}, Commit: "abc123"}
	predictedGating := &gating.Verdict{FindingID: "k", Gating: true, ItemID: "A2", Basis: gating.BasisPredicted}
	predictedFine := &gating.Verdict{FindingID: "k", Gating: false, Basis: gating.BasisPredicted}

	// An observation wins, and nothing is asked beside it.
	verdict, _ := combineGating("k", done, observedGating, "", nil, "")
	if !verdict.Gating || verdict.Basis != gating.BasisObserved {
		t.Errorf("observed gating combined to %+v, want the observation untouched", verdict)
	}

	// Observed fine + a predicted break: the prediction decides the half the
	// observation does not reach.
	verdict, _ = combineGating("k", done, observedFine, "", predictedGating, "")
	if !verdict.Gating || verdict.ItemID != "A2" || verdict.Basis != gating.BasisPredicted {
		t.Errorf("observed fine with a predicted break combined to %+v, want the predicted break of A2", verdict)
	}

	// Observed fine + nothing left to decide: the observation stands.
	verdict, _ = combineGating("k", done, observedFine, "", predictedFine, "")
	if verdict.Gating {
		t.Errorf("observed fine with a predicted none combined to gating %+v", verdict)
	}

	// Observed fine but a RUNNABLE item produced no evidence: never read as
	// demonstrated — the fallback, naming the item.
	observedFine.Unresolved = []gating.Unresolved{{
		ItemID: "A1", Reason: "the command answered error, which produced no evidence about the item"}}
	verdict, _ = combineGating("k", done, observedFine, "", nil, "every acceptance item is bound to a command")
	if !verdict.Gating || verdict.Basis != gating.BasisPredicted || verdict.Fallback == "" {
		t.Errorf("a runnable item with no evidence combined to %+v, want the absorb fallback saying why no verdict was reached", verdict)
	}
	if !strings.Contains(verdict.Reason, "A1") {
		t.Errorf("the fallback reason does not name the item with no evidence: %q", verdict.Reason)
	}

	// Nothing observed, nothing predicted: the combined fallback.
	verdict, _ = combineGating("k", done, nil, "no runner is configured", nil, "every item is the oracle's")
	if !verdict.Gating || verdict.Fallback == "" {
		t.Errorf("neither tier answering combined to %+v, want the absorb fallback — deferring would stop an unattended run", verdict)
	}

	// Nothing observed, a prediction: the prediction is the answer.
	verdict, _ = combineGating("k", done, nil, "no runnable item", predictedFine, "")
	if verdict.Gating {
		t.Errorf("a predicted none combined to gating %+v", verdict)
	}
}

// A tracker record this layer writes must be the record the pinned layout
// describes, because the file is what tk reads back: the required fields set,
// two-space indented, no invented shape. (The pure half of the write path;
// the durable half is pinned by the tests above through the real tree.)
//
// short: encoding over a record already in memory
func TestAWrittenTrackerRecordCarriesThePinnedLayout(t *testing.T) {
	t.Parallel()
	tick := absorbedTickRecord("r-1", runstate.Finding{
		Key: "dc02fb31", Title: "A finding the fake runner proposes",
		Body: "Discovered beside the work.", DiscoveredFrom: "run-r-1/tick-a1/attempt-1",
	}, runstate.Absorption{
		Key: "dc02fb31", TickID: "n9x", Gating: true, ItemID: "A1",
		Basis: runstate.AbsorptionObserved, Reason: "observed broken", Placement: runstate.AbsorptionBeforeReview,
	}, "ticfac-test", "qeu")

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustMarshal(tick)), &fields); err != nil {
		t.Fatalf("the written record does not read back as a tick record: %v", err)
	}
	for _, required := range []string{"id", "title", "status", "priority", "type", "owner", "created_by", "created_at", "updated_at"} {
		if _, ok := fields[required]; !ok {
			t.Errorf("the written record omits %s, which contracts/tracker-layout.json requires", required)
		}
	}
	if string(fields["status"]) != `"open"` || string(fields["type"]) != `"task"` {
		t.Errorf("the written record's defaults are %s/%s, want open/task (the layout's own)", fields["status"], fields["type"])
	}
	if string(fields["parent"]) != `"qeu"` {
		t.Errorf("the written absorbed record's parent is %s, want the running epic", fields["parent"])
	}
	// A backlog record omits parent entirely — omitted rather than nulled.
	backlog := absorbedTickRecord("r-1", runstate.Finding{Key: "k", Title: "t", DiscoveredFrom: "d"},
		runstate.Absorption{Key: "k", TickID: "b01", Gating: false, Basis: runstate.AbsorptionObserved,
			Reason: "fine", Placement: runstate.AbsorptionBacklog}, "operator@example.com", "")
	fields = map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(mustMarshal(backlog)), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["parent"]; ok {
		t.Errorf("the backlog record carries parent %s, want it omitted — a backlog tick has no parent", fields["parent"])
	}
}

func mustMarshal(record any) []byte {
	raw, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	return raw
}
