package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A READY review is about the tree it judged (review_rounds.go, after
// epic-hn6). hn6's branch closed three ticks hours after its final review
// answered, and nothing reviewed them: a READY verdict stood over work it
// never saw, and with the opt-in the run would have merged it. These are the
// acceptance:
//
//  1. a tree changed after a READY final review — here a person's commit
//     landing after the close-out, in the same run — is reviewed once more
//     before the run merges, and the run lands on that review's READY;
//  2. a resumed run whose tree changed after its READY review reviews it
//     again, and that review's NOT READY holds the land as any final NOT
//     READY does;
//  3. a tree unchanged since the READY review — only the run's bookkeeping
//     and the close-out's own merge after it, the normal path — lands with no
//     other review.

// readyThenKilledAtTheCloseout runs a fixture whose review answers READY up
// to the close-out's close, and kills the run there: before the land.
func readyThenKilledAtTheCloseout(t *testing.T, mode string) (*fixture, *landingForge) {
	t.Helper()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: mode, pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)
	_, _, err := f.run(f.Repo, fixtureOptions{mode: mode, pullRequests: pr, stopAfter: stopAt("co", StageClosed)})
	killedAfter(t, err, "co", StageClosed)
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 1 || reviewVerdictOf(reviews[0].Response) != subprocess.ReviewVerdictReady {
		t.Fatalf("before the land the run holds %d review decision(s), want one READY", len(reviews))
	}
	return f, pr
}

// 1. A change after the READY review, in the same run: reviewed, then landed.
func TestAChangeAfterAReadyReviewIsReviewedAgainBeforeTheRunLands(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "report", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	// A person pushes to the epic branch the moment the close-out closes:
	// after the READY review, before the land.
	fix := ""
	pushed := func(e Event) bool {
		if fix == "" && e.Tick == "co" && e.Stage == StageClosed {
			fix = pushOntoTheEpicBranch(t, f, "changed-after-the-review.txt", "work no review saw\n",
				"a person changes the epic after its READY review")
		}
		return false
	}
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr, stopAfter: pushed})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fix == "" {
		t.Fatal("the close-out never closed, so nothing was pushed after the review")
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v), want it landed on the second review's READY", result.State, result.Failure)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 2 {
		t.Fatalf("%d review decisions, want 2: the READY review, and one over the tree changed after it", len(reviews))
	}
	again := reviews[1]
	if got := reviewVerdictOf(again.Response); got != subprocess.ReviewVerdictReady {
		t.Errorf("the second review's verdict is %q, want READY", got)
	}
	if judged, _ := again.Request["source_sha"].(string); judged == "" ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", fix, judged) {
		t.Errorf("the second review judged %q, which does not carry the change %s", judged, fix)
	}
	if !strings.Contains(result.Reason, "merged into main") {
		t.Errorf("the terminal reason does not say the epic is merged: %q", result.Reason)
	}
	if !onOrigin(f, fix, "main") {
		t.Error("main does not carry the change the second review judged")
	}
}

// 2. A resume over a tree changed after the READY review: reviewed again, and
// that review's NOT READY holds.
func TestAResumeOverATreeChangedAfterAReadyReviewHoldsOnTheNewReviewsNotReady(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "review_ready_then_not_ready")
	pushOntoTheEpicBranch(t, f, "changed-after-the-review.txt", "work no review saw\n",
		"a person changes the epic after its READY review")

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_ready_then_not_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the resume ended %s (%+v), want the %s hold: the review of the changed tree said NOT READY",
			result.State, result.Failure, RefusedLandReviewNotReady)
	}
	if !strings.Contains(result.Failure.Message, "The change after the READY review broke it") {
		t.Errorf("the hold does not name the new review's reasons: %s", result.Failure.Message)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 2 {
		t.Errorf("%d review decisions, want 2: the READY review and one over the changed tree", len(reviews))
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review rejects")
	}
}

// 3. The normal path: nothing but bookkeeping and the close-out after the
// READY review, so the resume lands with no other review.
func TestAnUnchangedTreeAfterAReadyReviewLandsWithoutAnotherReview(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the resume ended %s (%q, %+v), want it landed", result.State, result.Reason, result.Failure)
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != 1 {
		t.Errorf("%d review decisions, want 1: the close-out's merge is not a change a review is owed", n)
	}
}
