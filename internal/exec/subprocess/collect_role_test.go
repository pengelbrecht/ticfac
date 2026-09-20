package subprocess

import (
	"strings"
	"testing"
)

// The recorded no-commits rule (tick 19l), enforced in collect: the same
// attempt — a report over an empty branch — collects by the ROLE's rule, not by
// one rule for every job. A review's deliverable is its answer, so its empty
// branch is what a correct attempt looks like; a close-out's deliverable is
// the record it leaves in the repository, so its empty branch is an
// undelivered deliverable whatever its answer says.
//
// This is the fixture the pwp incident was: a close-out answered
// DONE_WITH_CONCERNS over an empty branch and the feed read "answered failed
// (no-commits)" — a verdict attributed to a worker that had declared no such
// thing.
func TestCollectAppliesTheRolesRecordedNoCommitsRule(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "nocommit"})

	review := f.spec("run-30/tick-rre/attempt-1", "rre")
	review.Role = "review-epic"
	review.OutputSchema = "ticfac.job-result.review-epic.v1"
	reviewHandle := f.Start(review)
	f.waitSettled(reviewHandle)

	reviewCollected := f.collect(reviewHandle)
	if reviewCollected.Verdict != VerdictReadyToMerge {
		t.Errorf("the review collected %s, want ready-to-merge: for this role the empty branch is what a "+
			"correct attempt looks like", reviewCollected.Verdict)
	}
	if reviewCollected.Result.Outcome != OutcomeSucceeded {
		t.Errorf("the review collected as %s: a role whose deliverable is its answer is not failed by "+
			"committing nothing", reviewCollected.Result.Outcome)
	}
	if reviewCollected.Result.Source.Commits != 0 {
		t.Errorf("the review collected %d commits; the rule releases the ROLE, not the fact", reviewCollected.Result.Source.Commits)
	}
	if reviewCollected.Result.RoleResult == nil || reviewCollected.Result.RoleResult.Status != StatusDone {
		t.Fatalf("the review's answer did not reach the envelope: %+v", reviewCollected.Result.RoleResult)
	}

	closeout := f.spec("run-30/tick-rco/attempt-1", "rco")
	closeout.Role = "closeout-epic"
	closeout.OutputSchema = "ticfac.job-result.closeout-epic.v1"
	closeoutHandle := f.Start(closeout)
	f.waitSettled(closeoutHandle)

	closeoutCollected := f.collect(closeoutHandle)
	if closeoutCollected.Verdict != VerdictNoCommits {
		t.Errorf("the close-out collected %s, want no-commits: its deliverable is the record it leaves in "+
			"the repository, and an empty branch did not leave one", closeoutCollected.Verdict)
	}
	if closeoutCollected.Result.Outcome != OutcomeFailed {
		t.Errorf("the close-out collected as %s, want failed", closeoutCollected.Result.Outcome)
	}
	if closeoutCollected.Result.RoleResult == nil || closeoutCollected.Result.RoleResult.Status != StatusDone {
		t.Fatalf("the close-out's answer did not reach the envelope: the failure is the run's verdict, "+
			"not the worker's, so the answer is carried anyway: %+v", closeoutCollected.Result.RoleResult)
	}
}

// The review's payload carries the review's OWN verdict, not the collect
// vocabulary's (tick b50). A review answering NOT READY was recorded in
// epic-ncv decisions/1.json as `result.verdict: ready-to-merge` beside a
// summary saying the opposite — the collect verdict about the branch read as
// an approval of the epic — so the review-epic payload states the review's
// judgement as its own field and does not carry the collect verdict at all:
// for a review it is a constant (an empty branch is what a correct attempt
// looks like), the branch facts are in the envelope's Source, and a word that
// read as approval was the whole defect.
func TestTheReviewsPayloadCarriesItsOwnVerdictNotTheCollects(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "review_not_ready"})

	review := f.spec("run-31/tick-rnv/attempt-1", "rnv")
	review.Role = "review-epic"
	review.OutputSchema = "ticfac.job-result.review-epic.v1"
	reviewHandle := f.Start(review)
	f.waitSettled(reviewHandle)
	reviewCollected := f.collect(reviewHandle)
	payload := reviewCollected.Result.RoleResult.Result
	if got := payload["review_verdict"]; got != ReviewVerdictNotReady {
		t.Errorf("the review's payload says review_verdict %v, want %s: the review's judgement is the "+
			"one field its contract exists to carry", got, ReviewVerdictNotReady)
	}
	if _, ok := payload["verdict"]; ok {
		t.Errorf("the review's payload still carries the collect verdict %v: a word that read as approval "+
			"was the whole defect", payload["verdict"])
	}
	// The summary states the review's verdict, never the collect's: for a
	// review the parenthetical is its own judgement, which is what a reader
	// of the decision record reads the summary as.
	if strings.Contains(reviewCollected.Result.RoleResult.Summary, VerdictReadyToMerge) {
		t.Errorf("the review's summary %q spells the collect verdict, which a reader takes for the "+
			"review's own judgement", reviewCollected.Result.RoleResult.Summary)
	}
	if !strings.Contains(reviewCollected.Result.RoleResult.Summary, ReviewVerdictNotReady) {
		t.Errorf("the review's summary %q does not state the review's own verdict", reviewCollected.Result.RoleResult.Summary)
	}

	// Every OTHER role keeps the collect verdict where it belongs: for an
	// implement or closeout job the merge verdict about the branch is the
	// fact the run acts on, and its tests read it here.
	worker := f.spec("run-31/tick-w1/attempt-1", "w1")
	workerHandle := f.Start(worker)
	f.waitSettled(workerHandle)
	workerCollected := f.collect(workerHandle)
	if got := workerCollected.Result.RoleResult.Result["verdict"]; got != VerdictReadyToMerge {
		t.Errorf("the implement job's payload says verdict %v, want %s", got, VerdictReadyToMerge)
	}
	if _, ok := workerCollected.Result.RoleResult.Result["review_verdict"]; ok {
		t.Errorf("an implement job's payload carries a review verdict nobody asked it for")
	}
}
