package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The findings channel end to end (tick 7vn), against a real repository, a
// real origin, the real run-state store and the real tracker-tree publishing
// path — the same harness every other run-level guarantee here is pinned with.
//
// THE THREE ACCEPTANCE CASES, and the routing and fail-closed halves:
//
//  1. a finding becomes a DRAFT — durably, on origin, stamped with the
//     attempt that discovered it — and the tick that reported it is refused
//     its close while the draft waits for a person;
//  2. a finding repeated on a later attempt of the same tick is deduplicated
//     against the original proposal and proposes nothing new, including when
//     the original was discarded;
//  3. a malformed findings block refuses the attempt rather than closing the
//     tick behind findings nobody could read.

// draftsStore is the run's finding drafts, read and triaged the way the CLI
// does it: a store over the same repo, remote, branch and run the reconciler
// used.
func draftsStore(t *testing.T, repo *testRepo) *runstate.Store {
	t.Helper()
	s, err := runstate.Open(runstate.Options{
		Repo:   repo.Dir,
		Remote: "origin",
		Branch: "epic/qeu",
		RunID:  "r-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	return s
}

// proposedCount is how many of the run's drafts are still waiting for a
// person.
func proposedCount(t *testing.T, s *runstate.Store) int {
	t.Helper()
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range findings {
		if f.Status == runstate.FindingProposed {
			n++
		}
	}
	return n
}

// 1. A finding becomes a draft and blocks the close.
func TestAFindingBecomesADraftAndRefusesTheClose(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding"})
	repo := f.Repo
	_, result, err := f.run(repo, fixtureOptions{mode: "finding"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("run state %s, want failed: the untriaged finding must stop the run before the close", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want %s", result.Failure, RefusedFindingUntriaged)
	}
	if got := f.Tracker.count("close:a1"); got != 0 {
		t.Fatalf("a1 was closed %d times: a tick whose findings are untriaged cannot be closed", got)
	}

	// The drafts are on origin: two findings, both proposed, both discovered
	// by a1's first attempt — the routing target kept, the upstream one named.
	s := draftsStore(t, repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings %v, want the two the report carried", findings)
	}
	var routed bool
	for _, finding := range findings {
		if finding.TickID != "a1" {
			t.Errorf("finding %s belongs to %s, want a1", finding.Key, finding.TickID)
		}
		if finding.DiscoveredFrom != "run-r-fixture/tick-a1/attempt-1" {
			t.Errorf("discovered_from %q: the draft must name the attempt that discovered it, so the "+
				"provenance is never lost", finding.DiscoveredFrom)
		}
		if finding.Status != runstate.FindingProposed {
			t.Errorf("status %q, want proposed", finding.Status)
		}
		if finding.Target == "pengelbrecht/ticks" {
			routed = true
		}
	}
	if !routed {
		t.Error("the upstream finding lost its target repository: a finding routed elsewhere was dropped into this one")
	}

	// The tick's own record names the drafts, so a person reading the tracker
	// — not only the run state — sees that findings wait for them.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Ticks["a1"].Notes, "drafted for triage") {
		t.Errorf("a1's notes do not name the drafted finding: %q", state.Ticks["a1"].Notes)
	}

	// A person triages: the local finding is promoted as a tick carrying the
	// discovery's provenance, the upstream one is promoted INTO the repository
	// it targets — routed there, not dropped here.
	var promoted, routedKey string
	for _, finding := range findings {
		if finding.Target == "" {
			if _, _, err := s.TriageFinding(finding.Key, runstate.FindingPromoted, "the operator", "zz9"); err != nil {
				t.Fatalf("promote the local finding: %v", err)
			}
			promoted = finding.Key
		} else {
			if _, _, err := s.TriageFinding(finding.Key, runstate.FindingPromoted, "the operator", "pengelbrecht/ticks:of9"); err != nil {
				t.Fatalf("promote the routed finding: %v", err)
			}
			routedKey = finding.Key
		}
	}
	if promoted == "" || routedKey == "" {
		t.Fatal("the two findings did not come back distinct")
	}
	if proposedCount(t, s) != 0 {
		t.Fatal("both drafts should be triaged")
	}

	// The next run of the same run id resumes at the close: the gate has
	// already passed, the drafts are triaged, and a1 CLOSES. Every later tick
	// reports the SAME two findings in this fixture mode, so what the rest of
	// the run proves is the dedup across ticks: the subjects were decided, the
	// duplicates propose nothing new, and nothing is blocked by them.
	_, result, err = f.run(repo, fixtureOptions{mode: "finding"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("run state %s (%+v), want completed after triage", result.State, result.Failure)
	}
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Fatalf("a1 was closed %d times after triage, want 1", got)
	}
	for _, id := range []string{"a2", "b1", "rv", "co"} {
		if got := f.Tracker.count("close:" + id); got != 1 {
			t.Fatalf("%s was closed %d times, want 1", id, got)
		}
	}
}

// 2. A finding repeated on a later attempt of the same tick is deduplicated
// against the original proposal and proposes nothing new — including when the
// original was discarded.
func TestARepeatedFindingOnALaterAttemptProposesNothingNew(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_blocked"})
	repo := f.Repo
	opts := fixtureOptions{mode: "finding_blocked"}

	// Attempt 1 of a1 answers BLOCKED with nothing committed, and its report
	// carries the finding: the attempt is refused, the DISCOVERY is drafted.
	_, result, err := f.run(repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil {
		t.Fatalf("run state %s, want the refused attempt", result.State)
	}
	s := draftsStore(t, repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings %v, want the two the blocked report carried", findings)
	}
	for _, finding := range findings {
		if finding.DiscoveredFrom != "run-r-fixture/tick-a1/attempt-1" {
			t.Errorf("discovered_from %q, want the first attempt", finding.DiscoveredFrom)
		}
	}

	// The person DISCARDS them both — the hard case: whatever the human did
	// with the original, a redelivery proposes nothing new.
	for _, finding := range findings {
		if _, _, err := s.TriageFinding(finding.Key, runstate.FindingDiscarded, "the operator", ""); err != nil {
			t.Fatalf("discard %s: %v", finding.Key, err)
		}
	}

	// The next attempt of the same tick commits, answers DONE, and reports the
	// SAME finding — the shape a recurring discovery has until it is fixed.
	reconciler, result, err := f.run(repo, opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("run state %s (%+v), want completed: a discarded finding does not block the close", result.State, result.Failure)
	}

	// The dedup, as the run's own record: duplicates naming the original and
	// proposing nothing new.
	var duplicates int
	for _, event := range reconciler.Journal() {
		if event.Stage == StageFindingDuplicate {
			duplicates++
			if !strings.Contains(event.Detail, "discarded") {
				t.Errorf("duplicate event %q does not name the human's decision", event.Detail)
			}
		}
	}
	if duplicates != 2 {
		t.Fatalf("%d duplicate events recorded, want 2: the two findings the later attempt re-reported", duplicates)
	}
	for _, event := range reconciler.Journal() {
		if event.Stage == StageFindingFiled && event.Tick == "a1" {
			t.Errorf("a finding was FILED on the resume run — the dedup key is not deduplicating: %+v", event)
		}
	}

	// One record per finding, still carrying the FIRST attempt as the
	// discoverer, and the run closed a1.
	s2 := draftsStore(t, repo)
	findings, err = s2.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings %v, want exactly the originals", findings)
	}
	for _, finding := range findings {
		if finding.Status != runstate.FindingDiscarded {
			t.Errorf("status %q, want discarded", finding.Status)
		}
		if finding.DiscoveredFrom != "run-r-fixture/tick-a1/attempt-1" {
			t.Errorf("discovered_from %q: the original proposal must keep the attempt that first reported it",
				finding.DiscoveredFrom)
		}
	}
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Fatalf("a1 closed %d times, want 1", got)
	}
	if got := f.startCount("run-r-fixture/tick-a1/attempt-2"); got != 1 {
		t.Fatalf("a1's second attempt started %d times, want 1", got)
	}
}

// 3. A findings block that does not parse refuses the attempt — never a
// silent drop, and never a close behind findings nobody could read.
func TestAnUnreadableFindingsBlockRefusesTheAttempt(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_bad"})
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_bad"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingInvalid {
		t.Fatalf("failure %+v, want %s", result.Failure, RefusedFindingInvalid)
	}
	if got := f.Tracker.count("close:a1"); got != 0 {
		t.Fatalf("a1 was closed %d times behind a findings block nobody could read", got)
	}
	if findings, err := draftsStore(t, f.Repo).Findings(); err != nil || len(findings) != 0 {
		t.Fatalf("findings %v (err %v): an unreadable block drafts nothing", findings, err)
	}
}

// The role-job path: a review's findings are drafted from its validated
// answer's report the same way, and its tick is refused the close while one
// waits — the 604 shape, upstream findings that reached the tracker only
// because an orchestrator read prose that far.
func TestAReviewJobsFindingBlocksItsOwnClose(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "review_finding"})
	// One work tick that reports nothing unusual, so the run reaches the
	// review wave; the review job itself answers DONE with a findings block.
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_finding"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the review tick's own untriaged finding", result.Failure)
	}
	if result.Failure != nil && result.Failure.TickID != "rv" {
		t.Fatalf("failure tick %s, want rv", result.Failure.TickID)
	}
	s := draftsStore(t, f.Repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("the review's findings were never drafted")
	}
	for _, finding := range findings {
		if finding.TickID != "rv" {
			t.Errorf("finding %s belongs to %s, want rv: a finding is drafted under the tick whose job reported it",
				finding.Key, finding.TickID)
		}
		if !strings.HasPrefix(finding.DiscoveredFrom, "run-r-fixture/tick-rv/attempt-") {
			t.Errorf("discovered_from %q, want the review job's own attempt named", finding.DiscoveredFrom)
		}
	}
}
