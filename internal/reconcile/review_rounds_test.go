package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A final review's NOT READY is the run's to act on (review_rounds.go,
// epic-6in 2026-09-28). 6in's review named a HIGH finding its own work
// introduced; the prose rule sent it to the backlog, nothing in the run could
// address it, and landing held for a person on a verdict formed before later
// fixes. These are the acceptance:
//
//  1. a NOT READY review's blocking finding is absorbed into the epic and
//     fixed, the epic is reviewed again, the re-review answers READY and the
//     run LANDS (the opt-in merge) with no person — the low finding stays
//     backlog;
//  2. the rounds are bounded: a re-review that is still NOT READY holds for a
//     person, naming the remaining reasons, and no third review is made;
//  3. a run an older build left held land_review_not_ready (epic-6in itself)
//     resumes on the new build by absorbing the backlogged blocking finding
//     and reviewing again — and lands, with no person.

// findingByTitle is the drafted finding a report carried under a title.
func findingByTitle(t *testing.T, store *runstate.Store, title string) runstate.Finding {
	t.Helper()
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("no finding titled %q was drafted", title)
	return runstate.Finding{}
}

// reviewDecisions is every review-epic decision the run recorded, in order.
func reviewDecisions(t *testing.T, store *runstate.Store) []runstate.Decision {
	t.Helper()
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var out []runstate.Decision
	for _, d := range decisions {
		if d.Role == "review-epic" {
			out = append(out, d)
		}
	}
	return out
}

// assertAbsorbedAndReReviewed checks the shape the fix leaves: the blocking
// finding's tick a closed child of the epic, the low one's still backlog, and
// two review rounds — the first NOT READY, the second READY.
func assertAbsorbedAndReReviewed(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	store := draftsStore(t, f.Repo)

	blocking := findingByTitle(t, store, "The Phase 4 gate run never happened")
	record, ok, err := store.Absorption(blocking.Key)
	if err != nil || !ok {
		t.Fatalf("the blocking finding has no decision record: %v %v", ok, err)
	}
	tick, err := f.Tracker.Show(ctx, record.TickID)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Parent != "qeu" {
		t.Errorf("the blocking finding's tick %s has parent %q: a NOT READY review's blocking finding is the "+
			"epic's work, never backlog", tick.ID, tick.Parent)
	}
	if tick.Status != "closed" {
		t.Errorf("the blocking finding's tick %s is %s: the run works it before the re-review", tick.ID, tick.Status)
	}

	low := findingByTitle(t, store, "A polish the review noticed on the way")
	lowRecord, ok, err := store.Absorption(low.Key)
	if err != nil || !ok {
		t.Fatalf("the low finding has no decision record: %v %v", ok, err)
	}
	lowTick, err := f.Tracker.Show(ctx, lowRecord.TickID)
	if err != nil {
		t.Fatal(err)
	}
	if lowTick.Parent != "" || lowTick.Status == "closed" {
		t.Errorf("the low finding's tick %s is parent %q, %s: a finding that is not a reason stays backlog",
			lowTick.ID, lowTick.Parent, lowTick.Status)
	}

	reviews := reviewDecisions(t, store)
	if len(reviews) != 2 {
		t.Fatalf("%d review decisions, want 2: the first review and one re-review", len(reviews))
	}
	if got := reviewVerdictOf(reviews[0].Response); got != subprocess.ReviewVerdictNotReady {
		t.Errorf("the first review's verdict is %q, want NOT READY", got)
	}
	if got := reviewVerdictOf(reviews[1].Response); got != subprocess.ReviewVerdictReady {
		t.Errorf("the re-review's verdict is %q, want READY", got)
	}
	rereview, _ := reviews[1].Request["tick_id"].(string)
	if rereview == "rv" || rereview == "" {
		t.Fatalf("the re-review ran as %q: it is a review tick of its own", rereview)
	}
	current, err := f.Tracker.Show(ctx, rereview)
	if err != nil {
		t.Fatal(err)
	}
	if current.Parent != "qeu" || current.Role != "review" || current.Status != "closed" {
		t.Errorf("the re-review %s is parent %q, role %q, %s", rereview, current.Parent, current.Role, current.Status)
	}
	if !strings.Contains(current.Description, "the Phase 4 gate run never happened") {
		t.Errorf("the re-review does not carry the previous review's reasons:\n%s", current.Description)
	}
}

// assertLanded checks the run merged the epic — the absorbed fix included —
// into main: the opt-in merge, with no person.
func assertLanded(t *testing.T, f *fixture, result *Result) {
	t.Helper()
	if !strings.Contains(result.Reason, "merged into main") {
		t.Errorf("the terminal reason does not say the epic is merged: %q", result.Reason)
	}
	store := draftsStore(t, f.Repo)
	record, ok, err := store.Absorption(findingByTitle(t, store, "The Phase 4 gate run never happened").Key)
	if err != nil || !ok {
		t.Fatalf("the blocking finding has no decision record: %v %v", ok, err)
	}
	if got := showOnOrigin(t, f, "main", "work-"+record.TickID+".txt"); got == "" {
		t.Errorf("main does not carry the absorbed fix %s", record.TickID)
	}
}

// 1. THE ACCEPTANCE: NOT READY → absorbed, fixed, re-reviewed READY → landed.
func TestANotReadyReviewsBlockingFindingIsAbsorbedFixedReReviewedAndTheRunLands(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a NOT READY whose blocking finding the run can fix is the run's, "+
			"not a person's", result.State, result.Failure)
	}
	assertAbsorbedAndReReviewed(t, f)
	assertLanded(t, f, result)
	if !contains(r.Stages("rv"), StageReviewRound) {
		t.Errorf("the review's stages %v do not record the review round", r.Stages("rv"))
	}
	if body := pr.body(); !strings.Contains(body, "judged the epic READY") {
		t.Errorf("the PR body does not carry the re-review's READY:\n%s", body)
	}
}

// 2. THE BOUND: two NOT READY rounds hold for a person, naming the reasons.
func TestAReviewStillNotReadyAfterTheBoundHoldsNamingItsReasons(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "review_not_ready", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the run ended %s (%+v), want a %s hold after the bound", result.State, result.Failure,
			RefusedLandReviewNotReady)
	}
	for _, want := range []string{"after 2 review round(s)", "the Phase 4 gate still never ran",
		"The Phase 4 gate still never ran after the fix"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not name %q: %s", want, result.Failure.Message)
		}
	}
	if strings.Contains(result.Failure.Message, "DONE (NOT READY)") {
		t.Errorf("the hold carries a bare verdict: %s", result.Failure.Message)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != maxReviewRounds {
		t.Errorf("%d review decisions, want %d: the bound makes no third review", len(reviews), maxReviewRounds)
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review still rejects")
	}
}

// 4. THE RESUME (epic-6in itself): an older build held the run at the land on
// a NOT READY whose blocking finding it had sent to the backlog. The new build,
// resuming, absorbs it, reviews again and lands — no person.
func TestAResumeHeldOnANotReadyReviewAbsorbsItsBackloggedBlockingFindingAndLands(t *testing.T) {
	t.Parallel()
	pr := &landingForge{}
	old := fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr, notReadyForAPerson: true}
	f := newFixture(t, old)
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)

	_, held, err := f.run(f.Repo, old)
	if err != nil {
		t.Fatalf("the older build's run: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the older build's run ended %s (%+v), want the 6in hold", held.State, held.Failure)
	}
	store := draftsStore(t, f.Repo)
	blocking := findingByTitle(t, store, "The Phase 4 gate run never happened")
	record, ok, err := store.Absorption(blocking.Key)
	if err != nil || !ok {
		t.Fatalf("the blocking finding has no decision record: %v %v", ok, err)
	}
	if tick, err := f.Tracker.Show(context.Background(), record.TickID); err != nil || tick.Parent != "" {
		t.Fatalf("the older build left the blocking finding's tick %+v (%v): want it backlog, as 6in's bib", tick, err)
	}

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("the resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resume ended %s (%+v): a held NOT READY is the run's to act on when resumed", result.State,
			result.Failure)
	}
	assertAbsorbedAndReReviewed(t, f)
	assertLanded(t, f, result)
}
