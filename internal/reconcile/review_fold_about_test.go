package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A fold of the base that changes what the epic is ABOUT is reviewed before
// the land (review_rounds.go, epic ilz 2026-10-07). ilz documented
// cloudflare/src/claude-sub.ts; main changed that file after ilz's READY
// review, and the fold that brought it in was not reviewed, so the run would
// have landed a stale guide. These are the acceptance:
//
//  1. a READY review, then a fold changing a file the epic's own diff touches:
//     one more review runs before the land;
//  2. a fold changing a file the epic's ACCEPTANCE names, made by the land's
//     own fold: the land stops, resumably, and the next incarnation reviews
//     the folded tree and lands;
//  3. a fold changing an unrelated file, made by the land's own fold: it lands
//     on the READY with no other review (#231's rule);
//  4. the backstop: a base that changes the epic's files under every round
//     buys maxDriftReviewRounds rounds, and then the run lands;
//  5. the absorption policy reads a CLOSED review off a tracker whose graph
//     lists open tasks alone.

// foldMainTakingTheBase merges origin's main into epic/qeu as a fold does, a
// conflict resolved to main's side, from a clone of its own, and pushes it.
func foldMainTakingTheBase(t *testing.T, f *fixture) string {
	t.Helper()
	dir := filepath.Join(f.Root, "a-fold")
	if _, err := os.Stat(dir); err != nil {
		cloneRepo(t, f.Repo.Origin, dir)
	}
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", "main", "epic/qeu")
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "epic/qeu", "origin/epic/qeu")
	mustRun(t, dir, "git", "merge", "--quiet", "--no-ff", "--no-edit", "-X", "theirs", "-m",
		"Merge branch 'main' into epic/qeu", "origin/main")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "epic/qeu")
	return strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
}

// acceptanceNaming gives the fixture epic acceptance criteria that name path.
func acceptanceNaming(t *testing.T, f *fixture, path string) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	epic := state.Ticks["qeu"]
	epic.AcceptanceCriteria = "- [A1] the guide describes " + path + " as it is"
	state.Ticks["qeu"] = epic
	f.Tracker.write(t, state)
}

// landed fails the test unless the run merged the epic into main.
func landed(t *testing.T, result *Result) {
	t.Helper()
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the run ended %s (%q, %+v), want it landed", result.State, result.Reason, result.Failure)
	}
}

// 1. A fold changing a file the epic's own diff touches: reviewed, then landed.
func TestAFoldChangingAFileTheEpicTouchesIsReviewedBeforeTheLand(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	mergeMain(t, f, "work-a1.txt", "main's own work-a1\n", "another PR writes the file a1 wrote")
	fold := foldMainTakingTheBase(t, f)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	landed(t, result)
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != 2 {
		t.Fatalf("%d review decisions, want 2: a fold that changed work-a1.txt, which the epic wrote, is reviewed",
			len(reviews))
	}
	if judged, _ := reviews[1].Request["source_sha"].(string); judged == "" ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", fold, judged) {
		t.Errorf("the second review judged %q, which does not carry the fold %s", judged, fold)
	}
}

// 2. The land's own fold changes a file the acceptance names: the land stops
// for a review, needing nobody, and the next incarnation reviews and lands.
func TestTheLandsFoldOfAPathTheAcceptanceNamesIsReviewedBeforeTheMerge(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	acceptanceNaming(t, f, "shared/api.txt")
	// Main moves as the readying starts: after the run-start fold and every
	// ask before the land, so only the land's own fold brings it in.
	moved := false
	hook := func(e Event) bool {
		if !moved && e.Stage == StageLanding {
			moved = true
			mergeMain(t, f, "shared/api.txt", "the api as main now has it\n", "another PR rewrites shared/api.txt")
		}
		return false
	}
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr, stopAfter: hook})
	if err != nil {
		t.Fatalf("the first resume: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandFoldReview {
		t.Fatalf("the first resume ended %s (%+v), want the %s stop", result.State, result.Failure,
			RefusedLandFoldReview)
	}
	if !resumesWithoutAPerson(result.Failure.Reason) || holdsForAPerson(result.Failure.Reason) {
		t.Errorf("%s is not continued without a person", RefusedLandFoldReview)
	}
	if !strings.Contains(result.Failure.Message, "shared/api.txt") {
		t.Errorf("the stop does not name the folded path: %s", result.Failure.Message)
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Fatal("main carries the epic: the run merged a fold no review judged")
	}

	_, result, err = f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("the second resume: %v", err)
	}
	landed(t, result)
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != 2 {
		t.Errorf("%d review decisions, want 2: the READY review, and one over the fold the acceptance is about", n)
	}
}

// 3. The land's own fold changes nothing of the epic: landed on the READY.
func TestTheLandsFoldOfAnUnrelatedPathLandsWithNoOtherReview(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	acceptanceNaming(t, f, "shared/api.txt")
	moved := ""
	hook := func(e Event) bool {
		if moved == "" && e.Stage == StageLanding {
			moved = mergeMain(t, f, "landed-on-main-meanwhile.txt", "another PR\n", "another PR lands on main")
		}
		return false
	}
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr, stopAfter: hook})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	landed(t, result)
	if moved == "" || !onOrigin(f, moved, "epic/qeu") {
		t.Error("the land's fold did not bring in main's move")
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != 1 {
		t.Errorf("%d review decisions, want 1: a fold touching nothing of the epic is not reviewed", n)
	}
}

// 4. THE BACKSTOP: main rewrites the file the acceptance names while every
// review runs. Folds buy maxDriftReviewRounds more rounds, then the run lands.
func TestABaseThatKeepsChangingTheEpicsFilesBuysOnlyTheDriftBoundOfReviews(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := readyThenKilledAtTheCloseout(t, "report")
	acceptanceNaming(t, f, "shared/api.txt")
	mergeMain(t, f, "shared/api.txt", "main as of the first fold\n", "another PR rewrites shared/api.txt")

	moves := 0
	skeleton := map[string]bool{"a1": true, "a2": true, "b1": true, "rv": true, "co": true}
	hook := func(e Event) bool {
		if e.Stage == StageClaimed && !skeleton[e.Tick] {
			moves++
			mergeMain(t, f, "shared/api.txt", "main moved again under the review\n"+strings.Repeat("!", moves)+"\n",
				"another PR rewrites shared/api.txt")
		}
		return false
	}
	var result *Result
	for i := 0; i < 6; i++ {
		var err error
		if _, result, err = f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr, stopAfter: hook}); err != nil {
			t.Fatalf("resume %d: %v", i+1, err)
		}
		if result.Failure == nil || result.Failure.Reason != RefusedLandFoldReview {
			break
		}
	}
	landed(t, result)
	if n, want := len(reviewDecisions(t, draftsStore(t, f.Repo))), 1+maxDriftReviewRounds; n != want {
		t.Errorf("%d review decisions, want %d: the READY review and the folds' bound of rounds", n, want)
	}
	if moves == 0 {
		t.Error("main never moved under a review: the backstop had nothing to bound")
	}
}

// 5. The policy's "is the epic's work done?" reads the CLOSED review: `tk
// graph` lists open tasks alone, so the open graph never shows it.
//
// short: one fake tracker file and a graph read, no repository or harness run
func TestEpicWorkDoneReadsAClosedReviewOffAnOpenOnlyGraph(t *testing.T) {
	t.Parallel()
	tracker := newTracker(t, t.TempDir())
	state, err := tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.OpenOnlyGraph = true
	rv := state.Ticks["rv"]
	rv.Status = "closed"
	state.Ticks["rv"] = rv
	tracker.write(t, state)

	r := &Reconciler{tracker: tracker, opts: Options{EpicID: "qeu"}}
	done, why, err := r.epicWorkDone(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !done || !strings.Contains(why, "the final review has already run (rv)") {
		t.Errorf("epicWorkDone = %v, %q; want done by the closed review rv, which only `graph --all` lists",
			done, why)
	}
}
