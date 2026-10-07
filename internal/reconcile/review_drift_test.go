package reconcile

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A NOT READY caused by the base moving is not a round (review_rounds.go,
// epic ilz 2026-10-07). ilz documented cloudflare/src/claude-sub.ts while four
// PRs rewrote it on main; each fold of main made the reviewer find the guide
// stale against the code at the integration head, every such round counted
// toward the bound, and the run held for a person over a defect the epic never
// made. These are the acceptance:
//
//  1. the incident: round 1 NOT READY over the epic's own defect, rounds 2 and
//     3 each preceded by a fold of main that rewrote the file their blocking
//     finding names, round 4 READY: the run LANDS, it does not halt;
//  2. the backstop: a base that moves under every round is excused
//     maxDriftReviewRounds times, and after that the bound holds;
//  3. the mechanical check: a fold that the blocking finding does not name
//     excuses nothing, so the bound holds after 2 rounds as it always did;
//  4. the close-out's own merge is not unreviewed work once the close-out has
//     closed, though `tk graph` no longer lists it (ilz's round 3).

// driftRun runs the review_base_drift fixture to its end. While each fix the
// run absorbs is worked — the moment it is claimed — another PR rewrites
// shared/api.txt on main and main is folded into epic/qeu, so the next review
// judges a tree the base moved under. readyAt is the review that answers
// READY (0: none); body, when set, is what the drift reviews' blocking finding
// says instead of naming shared/api.txt.
func driftRun(t *testing.T, readyAt int, body string) (*fixture, *Result, int) {
	t.Helper()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "review_base_drift", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	argv := fakeRunnerArgv(t, "review_base_drift")
	env := []string{"DRIFT_SYNC=" + t.TempDir(), fmt.Sprintf("DRIFT_READY_AT=%d", readyAt)}
	if body != "" {
		env = append(env, "DRIFT_BODY="+body)
	}
	f.Runner = append(append(append([]string{}, argv[:2]...), env...), argv[2:]...)

	skeleton := map[string]bool{"a1": true, "a2": true, "b1": true, "rv": true, "co": true}
	folded := map[string]bool{}
	hook := func(e Event) bool {
		if e.Stage != StageClaimed || skeleton[e.Tick] || folded[e.Tick] {
			return false
		}
		tick, err := f.Tracker.Show(context.Background(), e.Tick)
		if err != nil || tick.Role != "" {
			return false
		}
		folded[e.Tick] = true
		mergeMain(t, f, "shared/api.txt", fmt.Sprintf("main as of fold %d\n", len(folded)),
			"another PR rewrites shared/api.txt")
		foldMainIntoTheEpicBranch(t, f)
		return false
	}
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_base_drift", pullRequests: pr, stopAfter: hook})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return f, result, len(folded)
}

// assertVerdicts checks the run's review decisions, in order.
func assertVerdicts(t *testing.T, f *fixture, want ...string) {
	t.Helper()
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	got := make([]string, 0, len(reviews))
	for _, d := range reviews {
		got = append(got, reviewVerdictOf(d.Response))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the review verdicts are %v, want %v", got, want)
	}
}

// 1. THE INCIDENT: two drift rounds are excused, and the run lands on round 4.
func TestNotReadyRoundsCausedByTheBaseMovingDoNotSpendTheReviewBound(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, result, folds := driftRun(t, 4, "")
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the run ended %s (%q, %+v), want it landed: a NOT READY about the base moving under the epic "+
			"is not a round of the bound", result.State, result.Reason, result.Failure)
	}
	nr, ready := subprocess.ReviewVerdictNotReady, subprocess.ReviewVerdictReady
	assertVerdicts(t, f, nr, nr, nr, ready)
	if folds < 2 {
		t.Errorf("%d fold(s) of main were made under the epic, want one before each drift round", folds)
	}
	if got := showOnOrigin(t, f, "main", "shared/api.txt"); got == "" {
		t.Error("main does not carry shared/api.txt")
	}
}

// 2. THE BACKSTOP: a base that moves under every round is excused only so
// often; then the bound holds, naming the excused rounds.
func TestABaseThatMovesUnderEveryReviewRoundStillHoldsPastTheDriftBound(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, result, _ := driftRun(t, 0, "")
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the run ended %s (%+v), want the %s hold past the drift bound", result.State, result.Failure,
			RefusedLandReviewNotReady)
	}
	rounds := maxReviewRounds + maxDriftReviewRounds
	for _, want := range []string{fmt.Sprintf("after %d review round(s), the bound being %d", rounds, maxReviewRounds),
		fmt.Sprintf("%d of them excused as the base moving under the epic", maxDriftReviewRounds)} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not say %q: %s", want, result.Failure.Message)
		}
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != rounds {
		t.Errorf("%d review decisions, want %d: the bound plus the excused drift rounds, and no more", n, rounds)
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review still rejects")
	}
}

// 3. THE MECHANICAL CHECK: the base moved, but the blocking finding names
// nothing it brought in — the epic's own defect, called drift. It counts.
func TestANotReadyThatNamesNothingTheFoldBroughtInSpendsTheBound(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, result, folds := driftRun(t, 4, "The epic's own claims are wrong, whatever main did.")
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the run ended %s (%+v), want the %s hold after the bound", result.State, result.Failure,
			RefusedLandReviewNotReady)
	}
	if !strings.Contains(result.Failure.Message, "after 2 review round(s), the bound being 2") ||
		strings.Contains(result.Failure.Message, "excused") {
		t.Errorf("the hold is not the plain bound's: %s", result.Failure.Message)
	}
	if folds == 0 {
		t.Error("no fold was made under the epic: the check had nothing to refuse")
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != maxReviewRounds {
		t.Errorf("%d review decisions, want %d", n, maxReviewRounds)
	}
}

// 4. THE CLOSED CLOSE-OUT: a READY review, then the close-out lands its retro
// and closes, with the tracker's graph listing open tasks alone as `tk graph`
// does. The close-out's merge is not work a review is owed: one review, landed.
func TestAClosedCloseoutsMergeIsNotUnreviewedWorkWhenTheGraphListsOpenTasksAlone(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "report", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.OpenOnlyGraph = true
	f.Tracker.write(t, state)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "report", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted || !strings.Contains(result.Reason, "merged into main") {
		t.Fatalf("the run ended %s (%q, %+v), want it landed", result.State, result.Reason, result.Failure)
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != 1 {
		t.Errorf("%d review decisions, want 1: the close-out's own merge is not a change a review is owed, "+
			"closed or not", n)
	}
}

// TestNamesPath pins what counts as a finding naming a folded path: the path,
// or its file name with an extension standing as a word of its own.
//
// short: string matching over literals, no repository or harness
func TestNamesPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text, path string
		want       bool
	}{
		{"main rewrote cloudflare/src/claude-sub.ts by 503 lines", "cloudflare/src/claude-sub.ts", true},
		{"see claude-sub.ts:208 for the refresh", "cloudflare/src/claude-sub.ts", true},
		{"(claude-sub.ts)", "cloudflare/src/claude-sub.ts", true},
		{"it ends in claude-sub.ts.", "cloudflare/src/claude-sub.ts", true},
		{"claude-sub.tsx is another file", "cloudflare/src/claude-sub.ts", false},
		{"my-claude-sub.ts is another file", "cloudflare/src/claude-sub.ts", false},
		{"the Makefile changed", "Makefile", true},
		{"the Makefile changed", "sub/Makefile", false},
		{"the guide is wrong", "cloudflare/src/claude-sub.ts", false},
		{".gitattributes", ".gitattributes", true},
		{"a .gitattributes rule", "sub/.gitattributes", false},
	} {
		if got := namesPath(c.text, c.path); got != c.want {
			t.Errorf("namesPath(%q, %q) = %v, want %v", c.text, c.path, got, c.want)
		}
	}
}
