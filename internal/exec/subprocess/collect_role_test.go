package subprocess

import "testing"

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
