package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A fold of the base is not a change a review is owed (treeChangedSinceReview,
// after #223 and #225). While other PRs keep landing on main, every land-time
// fold of main into the epic branch read as a changed tree: another review,
// meanwhile main moved again, another fold, another review — a loop that keeps
// an epic from ever landing. A merge whose second parent is on the base is the
// base's code coming in, already reviewed where it landed, and it is not
// counted; only the epic's own work is. These are the acceptance:
//
//  1. a fold of main after a READY final review lands with no other review;
//  2. a fold of main after a NOT READY at the bound holds, with no other
//     review;
//  3. an epic-side change AND a fold still make exactly one more review.

// foldMainIntoTheEpicBranch merges origin's main into epic/qeu the way the
// land's fold does — a merge commit whose second parent is main's head — from
// a clone of its own, and pushes it.
func foldMainIntoTheEpicBranch(t *testing.T, f *fixture) string {
	t.Helper()
	dir := filepath.Join(f.Root, "a-fold")
	if _, err := os.Stat(dir); err != nil {
		cloneRepo(t, f.Repo.Origin, dir)
	}
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", "main", "epic/qeu")
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "epic/qeu", "origin/epic/qeu")
	mustRun(t, dir, "git", "merge", "--quiet", "--no-ff", "--no-edit", "-m", "Merge branch 'main' into epic/qeu",
		"origin/main")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "epic/qeu")
	return strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
}

// 1. A READY review, then a fold of main: the run lands on the READY.
func TestAFoldOfTheBaseAfterAReadyReviewLandsWithNoOtherReview(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	mergeMain(t, f, "landed-on-main-meanwhile.txt", "another PR\n", "another PR lands on main")
	fold := foldMainIntoTheEpicBranch(t, f)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the resume ended %s (%q, %+v), want it landed", result.State, result.Reason, result.Failure)
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != 1 {
		t.Errorf("%d review decisions, want 1: a fold of main is the base's code, not the epic's work", n)
	}
	if !onOrigin(f, fold, "main") {
		t.Error("main does not carry the epic branch as folded")
	}
}

// 2. A NOT READY at the bound, then a fold of main: the hold stands.
func TestAFoldOfTheBaseAfterANotReadyAtTheBoundStillHolds(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)
	mergeMain(t, f, "landed-on-main-meanwhile.txt", "another PR\n", "another PR lands on main")
	foldMainIntoTheEpicBranch(t, f)

	// A review made now would answer READY and land: the hold below is the
	// rule's.
	f.Runner = fakeRunnerArgv(t, "review_not_ready_then_ready")
	_, result, err := f.run(f.Repo, fixtureOptions{pullRequests: pr})
	if err != nil {
		t.Fatalf("the re-run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the re-run ended %s (%+v), want the %s hold: a fold of main is no answer to the review",
			result.State, result.Failure, RefusedLandReviewNotReady)
	}
	if !strings.Contains(result.Failure.Message, "after 2 review round(s), the bound being 2") {
		t.Errorf("the hold is not the bound's: %s", result.Failure.Message)
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != maxReviewRounds {
		t.Errorf("%d review decisions, want %d: a fold is not reviewed", n, maxReviewRounds)
	}
}

// 3. The epic's own change still counts beside a fold: exactly one more review.
func TestAnEpicSideChangeBesideAFoldOfTheBaseIsReviewedOnce(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	change := pushOntoTheEpicBranch(t, f, "changed-after-the-review.txt", "work no review saw\n",
		"a person changes the epic after its READY review")
	mergeMain(t, f, "landed-on-main-meanwhile.txt", "another PR\n", "another PR lands on main")
	foldMainIntoTheEpicBranch(t, f)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the resume ended %s (%q, %+v), want it landed on the second review's READY", result.State,
			result.Reason, result.Failure)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 2 {
		t.Fatalf("%d review decisions, want 2: the epic's own change is reviewed, once", len(reviews))
	}
	if judged, _ := reviews[1].Request["source_sha"].(string); judged == "" ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", change, judged) {
		t.Errorf("the second review judged %q, which does not carry the change %s", judged, change)
	}
}
