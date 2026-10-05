package reconcile

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The absorption itself (tick npq), end to end against the same harness every
// other run-level guarantee here is pinned with: a real repository, a real
// origin, the real run-state store, the real tracker-tree publishing path and
// the real local subprocess executor.
//
// THE CASES, one per acceptance clause, under the absorption policy of
// 2026-10-06 (absorb_policy.go — no classifier is asked):
//
//  1. a HIGH-severity finding whose reporter names the done item it breaks,
//     reported while the epic's work is under way, is promoted into the
//     running epic as a tick with no person triaging, placed before the
//     final review (basis worker-asserted-high); reported once the work is
//     done, it is DEFERRED to the reviewer instead — a backlog tick, listed
//     in the epic PR;
//  2. the absorption decision — item id, verdict, basis — is a decision
//     record on the run branch;
//  3. a cold re-derivation from git reaches the same epic graph as the warm
//     run, proven by killing the run the moment the absorption is durable
//     and restarting from a fresh clone;
//  4. every other finding — low severity, or naming no item of the done —
//     becomes a backlog tick with an owner (basis backlog-default), and is
//     still reported, early or late;
//  5. an epic whose done is prose decides by the same policy (epic-6in): a
//     backlog tick, unless the reporter rates it high and claims the build
//     or CI is broken, which is absorbed — never a finding left for a
//     person.
//
// The reviewer's own basis — a finding the NOT READY review names blocking
// is absorbed — is pinned with the review rounds (review_rounds_test.go).

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

// 1a. THE WORKER-ASSERTED CASE, early: a1's report carries a HIGH-severity
// finding naming done item A1 while the epic's other ticks are still open, and
// the run absorbs it on the reporter's assertion — no classifier, no person —
// placing the absorbed tick before the final review and closing the whole epic
// with nobody having triaged anything.
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
	if record.Basis != runstate.AbsorptionWorkerAssertedHigh {
		t.Errorf("the decision's basis is %q, want %s: the reporter rated it high and named the item it breaks",
			record.Basis, runstate.AbsorptionWorkerAssertedHigh)
	}
	if record.Confidence != 0 || record.Model != "" || record.Fallback != "" {
		t.Errorf("the decision carries a classifier's answer (%+v): the policy asks no classifier", record)
	}
	for _, named := range []string{"high severity", "A1"} {
		if !strings.Contains(record.Reason, named) {
			t.Errorf("the recorded reason does not say what the absorption rests on (%q missing): %q", named, record.Reason)
		}
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

// 1b. THE WORKER-ASSERTED CASE, late: the close-out reports a HIGH-severity
// finding naming done item A1 after every implementation tick closed and the
// final review ran. It is NOT absorbed on the reporter's word — hn6 kept
// re-opening after its work was done exactly that way — but deferred to the
// reviewer: a backlog tick with an owner, outside the epic, listed in the
// epic PR under "Deferred findings", and the run completes.
func TestAHighSeverityFindingAfterTheReviewIsDeferredToTheReviewer(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "closeout_finding_high", pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "closeout_finding_high", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s — a deferred finding is listed, never held for: %+v",
			result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Basis != runstate.AbsorptionWorkerAssertedHigh ||
		record.Placement != runstate.AbsorptionDeferredToReview {
		t.Fatalf("the decision is %+v, want a worker-asserted high finding deferred to the reviewer, not absorbed", record)
	}
	if !strings.Contains(record.Reason, "review") {
		t.Errorf("the recorded reason does not say the work was done and the finding is the reviewer's: %q", record.Reason)
	}
	assertBacklogTick(t, f, r, record)

	body := forge.body()
	if !strings.Contains(body, "## Deferred findings") || !strings.Contains(body, record.TickID) {
		t.Errorf("the epic PR does not list the deferred finding %s under Deferred findings:\n%s", record.TickID, body)
	}
}

// 4. THE DEFAULT, early: a1's finding names done item A1 but is LOW severity.
// A reporter's low-severity claim is backlog work whatever it names — hn6
// absorbed such findings at confidence 0.44 and grew from 13 ticks to 60+.
func TestANonGatingFindingBecomesABacklogTickWithAnOwner(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local_low"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local_low"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Placement != runstate.AbsorptionBacklog || record.Basis != runstate.AbsorptionBacklogDefault {
		t.Fatalf("the decision is %+v, want a backlog-default backlog tick: a low-severity finding is not absorbed", record)
	}
	if !strings.Contains(record.Reason, "low") {
		t.Errorf("the recorded reason does not name the severity it rests on: %q", record.Reason)
	}
	assertBacklogTick(t, f, r, record)
}

// 4b. THE DEFAULT, late: the close-out's LOW-severity finding naming A1,
// after the review — backlog, as early, and still reported.
func TestALowSeverityFindingLateInAnEpicIsBacklogged(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "closeout_finding_low"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "closeout_finding_low"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Placement != runstate.AbsorptionBacklog || record.Basis != runstate.AbsorptionBacklogDefault {
		t.Fatalf("the decision is %+v, want a backlog-default backlog tick", record)
	}
	assertBacklogTick(t, f, r, record)
}

// 4c. A HIGH-severity finding naming an item the epic's done does not carry
// names no done item it breaks: backlog-default, not absorbed.
func TestAHighFindingNamingNoItemOfTheDoneIsBacklogged(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Basis != runstate.AbsorptionBacklogDefault {
		t.Fatalf("the decision is %+v, want backlog-default: A1 is not an item of this epic's done", record)
	}
	if !strings.Contains(record.Reason, "A1") {
		t.Errorf("the recorded reason does not name the item that is not the done's: %q", record.Reason)
	}
}

// assertBacklogTick pins what a backlog decision promised: a backlog tick
// with the epic's owner, outside the epic, never worked by the run, and the
// finding promoted to it — still reported, never dropped.
func assertBacklogTick(t *testing.T, f *fixture, r *Reconciler, record runstate.Absorption) {
	t.Helper()
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
		t.Errorf("the backlog tick %s is inside the epic graph", record.TickID)
	}
	if _, dispatched := orderOf(r).dispatched[record.TickID]; dispatched {
		t.Errorf("the backlog tick was dispatched by the run: work the epic does not need is a person's")
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	finding, ok, err := store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the backlog finding's draft: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
		t.Fatalf("the finding is %s promoted as %s, want promoted as the backlog tick %s", finding.Status,
			finding.PromotedAs, record.TickID)
	}
}

// 5. PROSE IS NOT A PERSON'S DECISION (epic-6in): an epic whose acceptance
// carries no [A<n>] items has a done nothing can be pointed at, so there is
// no item a finding could gate — and no judgement for a person to make. The
// run's rule decides: a BACKLOG tick with an owner, the reason recorded, and
// the run completes. (It used to leave the finding "for a person", and 6in's
// close-out held the whole run on fifteen of them.)
func TestAProseDoneTurnsAFindingIntoABacklogTickNotAHold(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	// The fixture's default epic acceptance: no [A<n>] marks at all.

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a finding against a prose done is backlog work, not a hold for a person",
			result.State, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Gating || record.Placement != runstate.AbsorptionBacklog || record.Basis != runstate.AbsorptionBacklogDefault {
		t.Fatalf("the decision is %+v, want a non-gating backlog-default decision", record)
	}
	if !strings.Contains(record.Reason, "prose") {
		t.Errorf("the recorded reason does not say the acceptance is prose: %q", record.Reason)
	}
	tick, err := f.Tracker.Show(context.Background(), record.TickID)
	if err != nil {
		t.Fatalf("the backlog tick %s does not exist: %v", record.TickID, err)
	}
	if tick.Parent != "" {
		t.Errorf("the backlog tick's parent is %q, want none: it is not the running epic's work", tick.Parent)
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	finding, ok, err := store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the finding: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
		t.Fatalf("the finding is %s promoted as %s, want promoted as the backlog tick %s", finding.Status,
			finding.PromotedAs, record.TickID)
	}
	events := feedStages(t, f.Repo.Dir, "r-fixture")
	if line := detailOfStage(events, StageAbsorptionRefused); line != "" {
		t.Errorf("the run still left the finding for a person: %q", line)
	}
	if line := detailOfStage(events, StageBacklogged); line == "" {
		t.Errorf("no %s line: a decision nobody can see reads as a finding that vanished; stages: %v",
			StageBacklogged, feedStagesOf(events))
	}
}

// 5b. THE EXCEPTION: a HIGH-severity finding whose reporter claims it breaks
// the build or CI gates ANY done, prose or not — a red build is an epic PR nobody can
// merge — so it is absorbed into the running epic as a child the run works
// before the hand-over. 6in's own close-out filed one ("… dz1 regression; full
// suite red").
func TestAProseDoneAbsorbsAFindingThatClaimsTheBuildIsRed(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_build_red"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_build_red"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if !record.Gating || record.Basis != runstate.AbsorptionWorkerAssertedHigh || record.Placement == runstate.AbsorptionBacklog {
		t.Fatalf("the decision is %+v, want a worker-asserted gating absorption, placed before the hand-over", record)
	}
	if _, inEpic := epicChildren(t, f)[record.TickID]; !inEpic {
		t.Errorf("the absorbed tick %s is not a child of the running epic", record.TickID)
	}
	order := orderOf(r)
	if _, dispatched := order.dispatched[record.TickID]; !dispatched {
		t.Errorf("the absorbed tick %s was never worked: a claimed red build is fixed before the hand-over",
			record.TickID)
	}
}

// short: the claim reader over titles in memory; no repository, no run
func TestTheBuildBreakageClaimReadsTheReportersClaimOnly(t *testing.T) {
	t.Parallel()
	for title, want := range map[string]bool{
		"A re-run under a new run id holds forever on the claim its stopped predecessor left (dz1 regression; full suite red)": true,
		"The build is broken on linux after the parser change":                                                                 true,
		"This change breaks the build under -race":                                                                             true,
		"internal/cli fails to compile without the tk package":                                                                 true,
		"CI is red on the epic branch":                                                                                         true,
		"forge.CI reads a head whose every check run is SKIPPED as green, and admitted 6in's close-out over a red go job":      false,
		"No mechanical guard that ticfac never imports or requires a ticks Go package":                                         false,
		"runners-config.md still documents tk cloud spawn/wait/collect, which no longer exist":                                 false,
		"Worker boundary shim guards tk but not ticfac on the harness PATH":                                                    false,
	} {
		if got := claimsBuildBreakage(runstate.Finding{Title: title}); got != want {
			t.Errorf("claimsBuildBreakage(%q) = %v, want %v", title, got, want)
		}
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

// 3c. THE RECORDED DECISION OUTRANKS THE GATES (finding d6356432, tick
// wz0): the kill lands between the decision record and the triage, and the
// epic's acceptance becomes PROSE before the restart — the very gate a
// fresh decision consults, moved between the two incarnations. A recorded
// absorption is finished BEFORE the target, evidence-table and acceptance
// gates are re-evaluated: those gates are inputs to a decision this run has
// already made, and a restart that re-consulted them would re-decide the
// finding differently — refuse over a done that is now prose, leave the
// record standing with no tick and the draft untriaged — the re-derivation
// hole the record exists to close.
func TestARecordedAbsorptionIsFinishedBeforeTheGatesAreReEvaluated(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	// The cut: the moment the created tick's publish lands — after the
	// record and the tick, before the placement edge and the triage.
	atTheCreate := func(e Event) bool {
		return e.Stage == StagePublished && strings.Contains(e.Detail, "create tick ")
	}
	if _, _, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_local", stopAfter: atTheCreate}); err != nil {
		if _, ok := err.(*killedAt); !ok {
			t.Fatalf("the warm run ended with %v, not the kill at the created tick's publish", err)
		}
	}
	reader := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	records, err := reader.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("origin carries %d absorption record(s) at the cut, want the one the warm run made", len(records))
	}
	warmRecord := records[0]
	if finding, ok, err := reader.Finding(records[0].Key); err != nil || !ok {
		t.Fatalf("read the finding at the cut: %v %v", ok, err)
	} else if finding.Status != runstate.FindingProposed {
		t.Fatalf("the finding is %s at the cut, want still proposed: the kill landed before the triage", finding.Status)
	}

	// THE GATE THAT MOVED: the epic's acceptance becomes prose between the
	// incarnations — the input a fresh decision consults, changed under a
	// decision already recorded.
	setEpicAcceptance(t, f, "The epic is done when a person says it is: prose, with no items marked.")

	// The restart, from a fresh clone: the recorded decision is FINISHED —
	// not re-made and not refused — behind the record the kill left, and the
	// run completes with nothing left holding the close-out.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	cold, result, err := f.run(clone, fixtureOptions{gate: observedGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("the cold run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the cold run ended %s: %s — a recorded decision stranded behind a gate that moved is the "+
			"re-derivation hole the record exists to close: %+v", result.State, result.Reason, result.Failure)
	}
	store := openRunStore(t, clone.Dir, cold.IntegrationBranch(), cold.RunID())
	records, err = store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TickID != warmRecord.TickID {
		t.Fatalf("the finished run carries %+v, want the killed run's one decision finished — never a second one", records)
	}
	finding, ok, err := store.Finding(records[0].Key)
	if err != nil || !ok {
		t.Fatalf("read the finding after the restart: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != records[0].TickID {
		t.Fatalf("the finding is %s promoted as %s, want promoted as the record's tick %s: the recorded decision was "+
			"finished behind the record, not re-decided against the prose acceptance",
			finding.Status, finding.PromotedAs, records[0].TickID)
	}
	// The refusal never spoke: a restart that re-consulted the acceptance
	// gate would have refused over the prose done and held the close-out on an
	// untriaged finding — the failure the reorder exists to make impossible.
	events := feedStages(t, clone.Dir, "r-fixture")
	if line := detailOfStage(events, StageAbsorptionRefused); line != "" {
		t.Errorf("the restart refused to absorb (%s) although the decision was already recorded and standing: %q",
			StageAbsorptionRefused, line)
	}
	if line := detailOfStage(events, StageRunHeld); strings.Contains(line, RefusedFindingUntriaged) {
		t.Errorf("the restart held the close-out on the finding as untriaged although the recorded decision finished it: %q", line)
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
		Basis: runstate.AbsorptionWorkerAssertedHigh, Reason: "asserted broken", Placement: runstate.AbsorptionBeforeReview,
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
		runstate.Absorption{Key: "k", TickID: "b01", Gating: false, Basis: runstate.AbsorptionBacklogDefault,
			Reason: "low severity", Placement: runstate.AbsorptionBacklog}, "operator@example.com", "")
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
