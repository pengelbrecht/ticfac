package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The epic PR's BODY (tick 4sb): the write half of the PR + CI close-out rule.
//
// Until this tick the forge could find, open and read the PR but never write
// to one, so a person merging an epic saw CI status and nothing else — not
// the review's verdict, not a single finding. On an epic whose review said
// NOT READY, the merge button would have been pressed behind a green check.
//
// The write is an EDIT of the PR's body, never a comment, and the choice is
// the thing under test: the durable record is the run's own state (the
// decisions and the findings drafts), and the body is a VIEW of it — so the
// body is RECOMPOSED from the record and overwritten on every admission and
// again at the close. A comment would APPEND, and a resumed close-out that
// wrote twice would post the findings twice; an overwrite writes the same
// body twice and the PR carries each fact exactly once however many resumes
// the run survived. That is what "idempotent" means here and what these
// tests count.

// The PR the run OPENS carries the final review's verdict and every finding
// the run drafted — each exactly once, whatever triage state it is in, with
// the finding's own text.
func TestTheEpicPRCarriesTheReviewsVerdictAndEveryFinding(t *testing.T) {
	t.Parallel()

	// The findings run: a1 reports two findings, nobody has triaged them, and
	// the run stops at a1's close — before the close-out, so no PR exists
	// behind findings a person has not seen.
	pulls := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "finding", pullRequests: pulls})
	declareCloseoutRule(t, f.Repo)
	repo := f.Repo
	_, result, err := f.run(repo, fixtureOptions{mode: "finding", pullRequests: pulls})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the untriaged finding", result.Failure)
	}

	// A person triages both drafts, promoting each into the repository it
	// targets — the same triage the findings tests perform.
	s := draftsStore(t, repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings %v, want the two the report carried", findings)
	}
	for _, finding := range findings {
		promotedAs := "zz9"
		if finding.Target != "" {
			promotedAs = finding.Target + ":of9"
		}
		if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{
			Status: runstate.FindingPromoted, By: "the operator", PromotedAs: promotedAs,
		}); err != nil {
			t.Fatalf("promote %s: %v", finding.Key, err)
		}
	}

	// The resumed run reaches the close-out and OPENS the epic PR, whose
	// body must now carry the review's verdict and both findings.
	forge := &fakeForge{}
	r, result, err := f.run(repo, fixtureOptions{mode: "finding", pullRequests: forge})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	body := forge.body()
	if body == "" {
		t.Fatal("the epic PR was opened with no body at all")
	}
	// The rule's own sentence stays the opening: the PR says WHY it exists
	// before it says what the run found.
	if !strings.Contains(body, "PR + CI close-out rule") {
		t.Errorf("the body does not state the rule the PR exists under:\n%s", body)
	}
	// The final review's verdict: the review ran in THIS resume (the first
	// run never reached it), answered DONE, and its answer is on the PR.
	if !strings.Contains(body, "The final review") || !strings.Contains(body, "DONE") {
		t.Errorf("the body does not carry the final review's verdict:\n%s", body)
	}
	// Every finding's identity and text — each exactly ONCE: the count is
	// the idempotence property, stated as a number — and the triage state
	// carried with each one.
	for _, want := range []string{
		"A finding the fake runner proposes",
		"Discovered beside the work, reported mechanically.",
		"An upstream finding routed to another repository",
		"pengelbrecht/ticks",
	} {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("the body carries %q %d times, want exactly 1:\n%s", want, got, body)
		}
	}
	if got := strings.Count(body, "triaged promoted"); got != 2 {
		t.Errorf("the body carries the triage state %d times, want once per finding:\n%s", got, body)
	}
	if !contains(r.Stages("co"), StagePRBodyWritten) {
		t.Errorf("stages %v do not record the body the PR carries", r.Stages("co"))
	}
}

// A resumed close-out REWRITES the body rather than appending to it: cut
// right after the PR is opened, the next incarnation finds the PR, writes the
// body again — the same body, byte for byte — and the PR carries each
// finding exactly once across both incarnations.
func TestAResumedCloseOutRewritesTheBodyNotAppendsToIt(t *testing.T) {
	t.Parallel()

	pulls := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "finding", pullRequests: pulls})
	declareCloseoutRule(t, f.Repo)
	repo := f.Repo
	_, result, err := f.run(repo, fixtureOptions{mode: "finding", pullRequests: pulls})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the untriaged finding", result.Failure)
	}
	s := draftsStore(t, repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{
			Status: runstate.FindingDiscarded, By: "the operator",
		}); err != nil {
			t.Fatalf("discard %s: %v", finding.Key, err)
		}
	}

	// The resumed run opens the PR, and is cut the moment the PR exists.
	forge := &fakeForge{}
	_, _, err = f.run(repo, fixtureOptions{
		mode: "finding", pullRequests: forge, stopAfter: stopAt("co", StagePROpened),
	})
	killedAfter(t, err, "co", StagePROpened)

	// The restarted run reads everything from origin, on a fresh clone, and
	// finds the PR the killed incarnation opened.
	restart := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restart"))
	_, result, err = f.run(restart, fixtureOptions{mode: "finding", pullRequests: forge})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restarted run ended %s (%+v)", result.State, result.Failure)
	}
	if got := forge.count("open"); got != 1 {
		t.Errorf("the PR was opened %d times across two incarnations, want 1", got)
	}
	if got := forge.count("update_body"); got < 1 {
		t.Errorf("the resumed close-out rewrote the PR body %d times, want at least 1", got)
	}
	// The rewrite is the same body: the records it is composed from did not
	// change between the incarnations, and a VIEW recomposed from unchanged
	// records is unchanged — that is what makes writing twice safe.
	bodies := forge.allBodies()
	if len(bodies) < 2 {
		t.Fatalf("the PR was written %d times across two incarnations, want at least 2", len(bodies))
	}
	if bodies[0] != bodies[len(bodies)-1] {
		t.Errorf("the resumed close-out wrote a different body:\nfirst:  %s\nsecond: %s",
			bodies[0], bodies[len(bodies)-1])
	}
	// And each finding still appears exactly once: no incarnation appended.
	body := bodies[len(bodies)-1]
	for _, want := range []string{
		"A finding the fake runner proposes",
		"An upstream finding routed to another repository",
	} {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("the body carries %q %d times across two incarnations, want exactly 1", want, got)
		}
	}
}

// The close-out's OWN attempt can draft a finding that did not exist when
// the admission composed the body. The close gate — the last moment the run
// owns the PR — recomposes the body from the FINAL records, so the finding
// reaches the PR even though the admission's body predated it. This is the
// prerequisite the operator's 2026-09-19 decision (the aqm tick) needs:
// carrying anything onto a PR is a write, and until this tick the forge had
// none.
func TestTheCloseOutsOwnFindingReachesThePRBody(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "closeout_finding"})
	declareCloseoutRule(t, f.Repo)
	repo := f.Repo
	forge := &fakeForge{}
	r, result, err := f.run(repo, fixtureOptions{mode: "closeout_finding", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The close-out reported a finding nobody triaged, so the run refuses its
	// close — but the PR the close gate just rewrote already CARRIES it.
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the close-out's own untriaged finding", result.Failure)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("the failure is about %s, want co", result.Failure.TickID)
	}
	bodies := forge.allBodies()
	if len(bodies) < 2 {
		t.Fatalf("the PR was written %d times, want the admission's body and the close gate's rewrite", len(bodies))
	}
	if strings.Contains(bodies[0], "A finding the close-out itself proposes") {
		t.Errorf("the admission's body already carried a finding the close-out had not reported yet:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[len(bodies)-1], "A finding the close-out itself proposes") ||
		!strings.Contains(bodies[len(bodies)-1], "Found by the close-out, after the admission wrote the PR body.") {
		t.Errorf("the close gate's body does not carry the close-out's own finding:\n%s", bodies[len(bodies)-1])
	}
	if !contains(r.Stages("co"), StagePRBodyWritten) {
		t.Errorf("stages %v do not record the close gate's rewrite of the body", r.Stages("co"))
	}

	// A person triages it, and the resumed run closes the close-out behind
	// the same PR — whose body still carries the finding, exactly once.
	s := draftsStore(t, repo)
	findings, err := s.Findings()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, finding := range findings {
		if finding.TickID == "co" {
			found = true
			if _, _, err := s.TriageFinding(finding.Key, runstate.Triage{
				Status: runstate.FindingDiscarded, By: "the operator",
			}); err != nil {
				t.Fatalf("discard %s: %v", finding.Key, err)
			}
		}
	}
	if !found {
		t.Fatal("the close-out's own finding was never drafted")
	}
	_, result, err = f.run(repo, fixtureOptions{mode: "closeout_finding", pullRequests: forge})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v)", result.State, result.Failure)
	}
	body := forge.body()
	if got := strings.Count(body, "A finding the close-out itself proposes"); got != 1 {
		t.Errorf("the body carries the close-out's finding %d times, want exactly 1:\n%s", got, body)
	}
	if !strings.Contains(body, "discarded") {
		t.Errorf("the body does not carry the finding's triage state:\n%s", body)
	}
}

// A body the forge refuses to write is a typed refusal, not a silent merge
// behind a PR that carries nothing: the person the rule exists for is the one
// who reads the body, so the close-out is not admitted while the PR cannot
// carry the record.
func TestACloseOutIsNotAdmittedWhenThePRBodyCannotBeWritten(t *testing.T) {
	t.Parallel()
	forge := &fakeForge{exists: true, pr: openPR(),
		updateErr: fmt.Errorf("GitHub answered 403: permission denied")}
	f := newFixture(t, fixtureOptions{pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: forge})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCloseoutPRBody {
		t.Fatalf("the failure is %+v, want a %s refusal", result.Failure, RefusedCloseoutPRBody)
	}
	for _, want := range []string{"#7", "403", "permission denied"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the refusal does not say %q: %q", want, result.Failure.Message)
		}
	}
	// The close-out was never dispatched behind a PR that carries nothing,
	// and everything before it closed as usual.
	if d := f.dispatch("co"); d.TickID != "" {
		t.Fatal("the close-out was dispatched behind a body the forge could not write")
	}
	for _, tick := range []string{"a1", "a2", "b1", "rv"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s: the body is a close-out gate, not a work gate", tick, current.Status)
		}
	}
}
