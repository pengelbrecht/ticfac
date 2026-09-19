package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The findings channel end to end (tick 7vn, reshaped by tick aqm), against
// a real repository, a real origin, the real run-state store and the real
// tracker-tree publishing path — the same harness every other run-level
// guarantee here is pinned with.
//
// THE FIVE CASES, and the routing and fail-closed halves:
//
//  1. a finding becomes a DRAFT — durably, on origin, stamped with the
//     attempt that discovered it — the tick that reported it CLOSES and the
//     run continues, and the CLOSE-OUT holds while the draft waits for a
//     person: the per-tick hold moved to the close-out (tick aqm), one
//     decision point at the end instead of one per tick mid-run;
//  2. a finding repeated on a later attempt of the same tick is deduplicated
//     against the original proposal and proposes nothing new, including when
//     the original was discarded — and a discarded finding holds nothing,
//     so the run completes over it;
//  3. a malformed findings block refuses the attempt rather than closing the
//     tick behind findings nobody could read;
//  4. a finding triaged as FIXED — repaired inside this epic, the commit named
//     — unblocks the tick, and a later report of the same finding is NOT
//     suppressed: the fix did not hold, the run hears it again, and what the
//     re-report reaches is the close-out's hold;
//  5. a role job's (the final review's) findings ride the same way: the review
//     tick closes behind its validated answer and the close-out holds.

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

// 1. A finding becomes a draft; the tick CLOSES, the run continues, and the
// close-out holds. This is tick aqm's first acceptance, in full: a tick with
// untriaged findings closes, the run continues past it, and the one decision
// point is the close-out's — naming the findings and where to triage them.
func TestAFindingRidesToTheCloseOutAndHoldsThere(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding"})
	repo := f.Repo
	reconciler, result, err := f.run(repo, fixtureOptions{mode: "finding"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("run state %s, want failed: the close-out must hold the run over untriaged findings", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want %s", result.Failure, RefusedFindingUntriaged)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("failure tick %s, want co: the hold moved to the close-out (tick aqm), not the tick that reported",
			result.Failure.TickID)
	}
	for _, want := range []string{
		"A finding the fake runner proposes",
		"An upstream finding routed to another repository",
		"tick a1",
		"close-out does not hand over",
	} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not name %q — a hold a person cannot act on is a stall by definition: %s",
				want, result.Failure.Message)
		}
	}

	// THE RUN CONTINUED: the tick that reported the findings closed, and so did
	// every tick after it — the run reached the close-out, which is where it
	// stopped. a1's close SAID what it was carrying rather than closing
	// silently.
	for _, id := range []string{"a1", "a2", "b1", "rv"} {
		if got := f.Tracker.count("close:" + id); got != 1 {
			t.Fatalf("%s was closed %d times, want 1: a tick with untriaged findings closes and the run continues",
				id, got)
		}
	}
	if got := f.Tracker.count("close:co"); got != 0 {
		t.Fatalf("co was closed %d times, want 0: the close-out is the one tick the hold stops", got)
	}
	if !contains(reconciler.Stages("a1"), StageClosedCarrying) {
		t.Errorf("a1's close did not say it was carrying untriaged findings: %v — a close behind findings "+
			"nobody triaged must not read as nothing found", reconciler.Stages("a1"))
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
			if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{Status: runstate.FindingPromoted, By: "the operator", PromotedAs: "zz9"}); err != nil {
				t.Fatalf("promote the local finding: %v", err)
			}
			promoted = finding.Key
		} else {
			if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{Status: runstate.FindingPromoted, By: "the operator", PromotedAs: "pengelbrecht/ticks:of9"}); err != nil {
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

	// The next run of the same run id resumes at the close-out's close: the
	// gate has already passed, the drafts are triaged, and co CLOSES — the
	// hold lifted without re-dispatching a single work tick or the review,
	// which is the loop the old per-tick hold bought (epic-ncv's three
	// byte-identical reviews).
	_, result, err = f.run(repo, fixtureOptions{mode: "finding"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("run state %s (%+v), want completed after triage", result.State, result.Failure)
	}
	for _, id := range []string{"a1", "a2", "b1", "rv", "co"} {
		if got := f.Tracker.count("close:" + id); got != 1 {
			t.Fatalf("%s was closed %d times after triage, want 1", id, got)
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
		if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{Status: runstate.FindingDiscarded, By: "the operator"}); err != nil {
			t.Fatalf("discard %s: %v", finding.Key, err)
		}
	}

	// The next attempt of the same tick commits, answers DONE, and reports the
	// SAME finding — the shape a recurring discovery has until it is fixed.
	// A DISCARDED finding holds nothing (tick aqm): a1 closes behind its
	// discarded originals, every later tick's re-report deduplicates against
	// them, and the run completes.
	reconciler, result, err := f.run(repo, opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("run state %s (%+v), want completed: a discarded finding does not hold the close-out", result.State, result.Failure)
	}

	// The dedup, as the run's own record: duplicates naming the original and
	// proposing nothing new — on a1 for its own re-report, and on every later
	// tick this fixture mode re-reports from.
	var duplicates int
	for _, event := range reconciler.Journal() {
		if event.Stage == StageFindingDuplicate {
			duplicates++
			if !strings.Contains(event.Detail, "discarded") {
				t.Errorf("duplicate event %q does not name the human's decision", event.Detail)
			}
		}
		if event.Stage == StageFindingFiled {
			t.Errorf("a finding was FILED on the resume run — the dedup key is not deduplicating: %+v", event)
		}
	}
	if duplicates < 2 {
		t.Fatalf("%d duplicate events recorded, want at least 2: the findings a1's second attempt re-reported",
			duplicates)
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

// 4. THE FIXED VERDICT (tick her): a finding repaired inside the epic is
// triaged as fixed, naming the commit — the verdict unblocks the tick the way
// promote and discard do, and a later report of the same finding is NOT
// suppressed: the re-report re-opens the draft, discovered by the attempt that
// re-found it, and the close-out holds over it again. The proof the verdict
// unblocked anything is the run getting as far as dispatching a1's second
// attempt — a gate that had not lifted would have failed the run before the
// attempt, with the draft still discovered by attempt 1.
func TestAFixedFindingThatIsReportedAgainIsHeardAgain(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_blocked"})
	repo := f.Repo
	opts := fixtureOptions{mode: "finding_blocked"}

	// Attempt 1 of a1 answers BLOCKED with nothing committed, and its report
	// carries the two findings: the attempt is refused, the discoveries are
	// drafted.
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

	// The person triages both as FIXED, naming the commit that repaired them —
	// a real commit of the run's own integration branch, so the claim is
	// checkable rather than asserted.
	fixedAs := s.Head()
	if fixedAs == "" {
		t.Fatal("the fixture's integration branch has no head to name as the repair")
	}
	for _, finding := range findings {
		outcome, decided, err := s.TriageFinding(finding.Key, runstate.Triage{
			Status: runstate.FindingFixed, By: "the operator", FixedAs: fixedAs,
		})
		if err != nil || outcome != runstate.Updated {
			t.Fatalf("triage %s as fixed: outcome %s err %v", finding.Key, outcome, err)
		}
		if decided.FixedAs != fixedAs {
			t.Fatalf("finding %s records fixed_as %q, want %q", finding.Key, decided.FixedAs, fixedAs)
		}
	}
	if proposedCount(t, s) != 0 {
		t.Fatal("a fixed verdict must lift the close gate the way promote and discard do")
	}

	// The resume run: attempt 2 of a1 commits, answers DONE, and reports the
	// SAME two findings — and because the standing verdict was FIXED, the
	// re-report is not a duplicate: the drafts are re-proposed, a1 CLOSES
	// carrying them, and the run continues to the close-out, which holds —
	// the re-report surfaces at the one decision point (tick aqm), not as a
	// per-tick refusal. THE ACCEPTANCE CASE of tick her, moved by aqm.
	reconciler, result, err := f.run(repo, opts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("run state %s, want failed: the re-reported finding must hold the close-out", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want %s: the re-report must surface, not dedup", result.Failure, RefusedFindingUntriaged)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("failure tick %s, want co: the hold over a re-opened finding is the close-out's", result.Failure.TickID)
	}
	if !strings.Contains(result.Failure.Message, "tick a1") {
		t.Errorf("the hold does not name a1, the tick whose finding re-opened: %s", result.Failure.Message)
	}
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Fatalf("a1 was closed %d times, want 1: a tick carrying a finding whose fix did not hold "+
			"closes, and the finding rides to the close-out", got)
	}
	if got := f.Tracker.count("close:co"); got != 0 {
		t.Fatalf("co was closed %d times, want 0: the close-out is what the re-opened finding holds", got)
	}
	// The run got as far as dispatching attempt 2 — the fixed verdict had
	// unblocked the tick — and the draft is re-proposed with attempt 2 as the
	// discoverer.
	if got := f.startCount("run-r-fixture/tick-a1/attempt-2"); got != 1 {
		t.Fatalf("a1's second attempt started %d times, want 1: the fixed verdict must unblock "+
			"the tick the way promote and discard do", got)
	}
	s2 := draftsStore(t, repo)
	reopened, err := s2.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened) != 2 {
		t.Fatalf("findings %v, want the two re-opened drafts", reopened)
	}
	for _, finding := range reopened {
		if finding.Status != runstate.FindingProposed {
			t.Errorf("finding %s is %s, want proposed: the re-report re-opens the draft", finding.Key, finding.Status)
		}
		if finding.DiscoveredFrom != "run-r-fixture/tick-a1/attempt-2" {
			t.Errorf("discovered_from %q, want attempt 2: the re-opened draft must name the "+
				"attempt that re-found it — the evidence the fix did not hold", finding.DiscoveredFrom)
		}
	}

	// The run's own record says the findings were FILED again — never that they
	// were duplicates — and the tick's tracker record names the re-opened
	// draft, so a person reading the tracker hears it too.
	var filed int
	for _, event := range reconciler.Journal() {
		if event.Stage == StageFindingDuplicate && event.Tick == "a1" {
			t.Errorf("the re-report of a fixed finding was recorded as a duplicate: %q — a fix that "+
				"did not hold is exactly what the dedup must not swallow", event.Detail)
		}
		if event.Stage == StageFindingFiled && event.Tick == "a1" {
			filed++
		}
	}
	if filed != 2 {
		t.Fatalf("%d findings filed on the resume run, want 2: the re-report must surface as a "+
			"draft waiting for a person", filed)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Ticks["a1"].Notes, "drafted for triage") {
		t.Errorf("a1's notes do not name the re-opened finding: %q", state.Ticks["a1"].Notes)
	}
}

// The role-job path: a review's findings are drafted from its validated
// answer's report the same way — the 604 shape, upstream findings that reached
// the tracker only because an orchestrator read prose that far. Under tick
// aqm the review tick CLOSES behind its validated answer (a review re-held
// for its own findings was the loop that re-dispatched byte-identical
// reviews until one found nothing), and the close-out holds over the
// findings, naming the review as the tick that reported them.
func TestAReviewJobsFindingRidesToTheCloseOut(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "review_finding"})
	// One work tick that reports nothing unusual, so the run reaches the
	// review wave; the review job itself answers DONE with a findings block.
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_finding"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the review's untriaged finding holding the close-out", result.Failure)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("failure tick %s, want co: the review closed and its finding rides to the close-out",
			result.Failure.TickID)
	}
	if !strings.Contains(result.Failure.Message, "tick rv") {
		t.Errorf("the hold does not name rv, the tick whose review reported the finding: %s", result.Failure.Message)
	}
	if got := f.Tracker.count("close:rv"); got != 1 {
		t.Fatalf("rv was closed %d times, want 1: a review whose findings wait for a person closes; "+
			"the hold is the close-out's (tick aqm), and re-holding the review is the loop that "+
			"punishes a thorough reviewer with another round", got)
	}
	if got := f.Tracker.count("close:co"); got != 0 {
		t.Fatalf("co was closed %d times, want 0: the close-out is what the review's finding holds", got)
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
