package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
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
	// The absorption policy (2026-10-06): the reviewer's blocking verdict is
	// the basis the finding entered the epic on, and the record says so.
	if record.Basis != runstate.AbsorptionReviewer || !record.Gating || record.Placement != runstate.AbsorptionBeforeReview {
		t.Errorf("the blocking finding's decision is %+v, want gating on basis %s, placed before the re-review",
			record, runstate.AbsorptionReviewer)
	}

	low := findingByTitle(t, store, "A polish the review noticed on the way")
	lowRecord, ok, err := store.Absorption(low.Key)
	if err != nil || !ok {
		t.Fatalf("the low finding has no decision record: %v %v", ok, err)
	}
	if lowRecord.Basis != runstate.AbsorptionBacklogDefault || lowRecord.Gating {
		t.Errorf("the low finding's decision is %+v, want backlog-default: the review did not name it blocking", lowRecord)
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

// A spent bound is not the end when the tree moved (epic-hn6, 2026-10-06).
// hn6's run held land_review_not_ready after its second NOT READY, whose only
// blocking finding was stray RESULT-*.md files at the branch root; a person
// committed the fix to epic/hn6, and a re-run held again on the same verdict,
// because the bound alone decided — a verdict on a tree that no longer exists
// standing over the one that does. These are the acceptance:
//
//  5. a spent bound with a tree CHANGED since the final review (a person's
//     commit) reviews once more instead of holding, and lands on its READY —
//     with every tick, the close-out included, already closed;
//  6. a spent bound with an UNCHANGED tree — only the run's own bookkeeping
//     since — still holds, with the message it always had, and makes no
//     review;
//  7. the extra round is gated the same way: its NOT READY over a tree
//     nothing changed since holds, and so does every re-run after it.

// pushOntoTheEpicBranch lands a commit on origin's epic/qeu the way a person
// fixing what a review named would: through a clone of their own, never the
// reconciler's checkout.
func pushOntoTheEpicBranch(t *testing.T, f *fixture, path, content, message string) string {
	t.Helper()
	dir := filepath.Join(f.Root, "a-person")
	if _, err := os.Stat(dir); err != nil {
		cloneRepo(t, f.Repo.Origin, dir)
	}
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", "epic/qeu")
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "epic/qeu", "FETCH_HEAD")
	writeUnder(t, dir, path, content)
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "epic/qeu")
	return strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
}

// heldAfterTheBound runs the review_not_ready fixture to its hold: two NOT
// READY rounds, the bound spent, every tick closed.
func heldAfterTheBound(t *testing.T) (*fixture, *landingForge) {
	t.Helper()
	pr := &landingForge{}
	f := newFixture(t, fixtureOptions{mode: "review_not_ready", pullRequests: pr})
	pr.origin = f.Repo.Origin
	declareRule(t, f.Repo, landingRule)
	_, held, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the run ended %s (%+v), want a %s hold after the bound", held.State, held.Failure,
			RefusedLandReviewNotReady)
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != maxReviewRounds {
		t.Fatalf("%d review decisions before the re-run, want %d", n, maxReviewRounds)
	}
	return f, pr
}

// 5. THE CHANGED TREE: a person's fix after the bound is reviewed, and lands.
func TestASpentReviewBoundReviewsAgainWhenTheTreeChangedSinceTheFinalReview(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)
	fix := pushOntoTheEpicBranch(t, f, "fixed-by-a-person.txt", "the stray files are gone\n",
		"a person fixes what the final review named")

	// The next review answers READY: the fix is what it would judge.
	f.Runner = fakeRunnerArgv(t, "review_not_ready_then_ready")
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("the re-run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the re-run ended %s (%+v): a tree changed since the final review is reviewed again, not held "+
			"on a verdict about a tree that no longer exists", result.State, result.Failure)
	}
	reviews := reviewDecisions(t, draftsStore(t, f.Repo))
	if len(reviews) != maxReviewRounds+1 {
		t.Fatalf("%d review decisions, want %d: one more round over the changed tree", len(reviews),
			maxReviewRounds+1)
	}
	extra := reviews[len(reviews)-1]
	if got := reviewVerdictOf(extra.Response); got != subprocess.ReviewVerdictReady {
		t.Errorf("the extra round's verdict is %q, want READY", got)
	}
	if judged, _ := extra.Request["source_sha"].(string); judged == "" ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", fix, judged) {
		t.Errorf("the extra round judged %q, which does not carry the person's fix %s", judged, fix)
	}
	tick, _ := extra.Request["tick_id"].(string)
	current, err := f.Tracker.Show(context.Background(), tick)
	if err != nil {
		t.Fatal(err)
	}
	if current.Parent != "qeu" || current.Role != "review" || current.Status != "closed" {
		t.Errorf("the extra review %s is parent %q, role %q, %s", tick, current.Parent, current.Role, current.Status)
	}
	if !strings.Contains(result.Reason, "merged into main") {
		t.Errorf("the terminal reason does not say the epic is merged: %q", result.Reason)
	}
	if !onOrigin(f, fix, "main") {
		t.Error("main does not carry the person's fix: the run landed something other than the reviewed tree")
	}
}

// 6. THE UNCHANGED TREE: only the run's own bookkeeping since the final review
// is not a new tree, and the hold stands as it was.
func TestASpentReviewBoundOverAnUnchangedTreeStillHolds(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)

	// A review made now would answer READY and land: the hold below is the
	// rule's, not a second NOT READY's.
	f.Runner = fakeRunnerArgv(t, "review_not_ready_then_ready")
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr})
	if err != nil {
		t.Fatalf("the re-run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the re-run ended %s (%+v), want the %s hold: nothing but run state changed since the final "+
			"review", result.State, result.Failure, RefusedLandReviewNotReady)
	}
	for _, want := range []string{"after 2 review round(s), the bound being 2", "the Phase 4 gate still never ran",
		"The Phase 4 gate still never ran after the fix"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not name %q: %s", want, result.Failure.Message)
		}
	}
	if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != maxReviewRounds {
		t.Errorf("%d review decisions, want %d: an unchanged tree is not reviewed again", n, maxReviewRounds)
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review still rejects")
	}
}

// 7. THE EXTRA ROUND IS GATED TOO: its NOT READY over a tree nothing changed
// since holds — and a re-run after that holds without reviewing again.
func TestAnExtraReviewRoundStillNotReadyOverAnUnchangedTreeHolds(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)
	pushOntoTheEpicBranch(t, f, "an-attempted-fix.txt", "not enough\n", "a person tries a fix")

	for i := 1; i <= 2; i++ {
		_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_not_ready", pullRequests: pr})
		if err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
		if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
			t.Fatalf("re-run %d ended %s (%+v), want the %s hold", i, result.State, result.Failure,
				RefusedLandReviewNotReady)
		}
		if !strings.Contains(result.Failure.Message, "after 3 review round(s), the bound being 2") {
			t.Errorf("re-run %d's hold does not count the extra round: %s", i, result.Failure.Message)
		}
		if n := len(reviewDecisions(t, draftsStore(t, f.Repo))); n != maxReviewRounds+1 {
			t.Errorf("re-run %d: %d review decisions, want %d: one extra round for the one change, and none "+
				"for a tree nothing changed since", i, n, maxReviewRounds+1)
		}
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review still rejects")
	}
}

// A held run resumed under a NEW run id (epic ilz, 2026-10-07). A cloud
// resume is a new submission, so it runs under a new run id; ilz's resume read
// only its own (empty) decisions, found no review, no close-out of its own and
// nothing open, and halted over "epic ilz has no dispatchable tick" — an
// unclassified stop. A person hand-filed a review tick and reopened the
// close-out. These are the acceptance:
//
//  8. a resume under a new run id over a tree CHANGED since the earlier run's
//     NOT READY final review reviews the tree again, runs the close-out after
//     it (reopened: the one that closed was the earlier run's) and lands on
//     the READY — with no person;
//  9. over an UNCHANGED tree the hold stands with the land hold's own message,
//     a classified stop, never "no classification".

// storeOfRun opens the run state of one run of the fixture's epic.
func storeOfRun(t *testing.T, f *fixture, runID string) *runstate.Store {
	t.Helper()
	s, err := runstate.Open(runstate.Options{Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	return s
}

// 8. THE NEW RUN ID OVER A CHANGED TREE: reviewed again, closed out, landed.
func TestAResumeUnderANewRunIDReviewsAChangedTreeAgainAndLands(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)
	fix := pushOntoTheEpicBranch(t, f, "fixed-by-a-person.txt", "the doc says what the code does\n",
		"a person fixes what the final review named")

	f.Runner = fakeRunnerArgv(t, "review_not_ready_then_ready")
	resume := fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr, runID: "r-resume"}
	_, result, err := f.run(f.Repo, resume)
	if err != nil {
		t.Fatalf("the resume under a new run id: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resume ended %s (%+v): a tree changed since an earlier run's NOT READY is reviewed again "+
			"and landed on its READY, never a stop over nothing to dispatch", result.State, result.Failure)
	}
	reviews := reviewDecisions(t, storeOfRun(t, f, "r-resume"))
	if len(reviews) != 1 {
		t.Fatalf("the resume recorded %d review decisions, want the one review of the changed tree", len(reviews))
	}
	if got := reviewVerdictOf(reviews[0].Response); got != subprocess.ReviewVerdictReady {
		t.Errorf("the resume's review verdict is %q, want READY", got)
	}
	if judged, _ := reviews[0].Request["source_sha"].(string); judged == "" ||
		!mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", fix, judged) {
		t.Errorf("the resume's review judged %q, which does not carry the person's fix %s", judged, fix)
	}
	if f.Tracker.count("reopen:co") == 0 {
		t.Error("the close-out co was never reopened: the resume has no close-out of its own to land with")
	}
	co, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if co.Status != "closed" {
		t.Errorf("the reopened close-out is %s, want closed again by the resume", co.Status)
	}
	if !strings.Contains(result.Reason, "merged into main") {
		t.Errorf("the terminal reason does not say the epic is merged: %q", result.Reason)
	}
	if !onOrigin(f, fix, "main") {
		t.Error("main does not carry the person's fix")
	}
}

// 9. THE NEW RUN ID OVER AN UNCHANGED TREE: the hold stands, classified.
func TestAResumeUnderANewRunIDOverAnUnchangedTreeHoldsClassified(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f, pr := heldAfterTheBound(t)

	// A review made now would answer READY and land: the hold below is the
	// rule's, not a third NOT READY's.
	f.Runner = fakeRunnerArgv(t, "review_not_ready_then_ready")
	resume := fixtureOptions{mode: "review_not_ready_then_ready", pullRequests: pr, runID: "r-resume"}
	_, result, err := f.supervise(f.Repo, resume)
	if err != nil {
		t.Fatalf("the resume under a new run id ended in an error, not a classified stop: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedLandReviewNotReady {
		t.Fatalf("the resume ended %s (%+v), want the %s hold: nothing but run state changed since the earlier "+
			"run's final review", result.State, result.Failure, RefusedLandReviewNotReady)
	}
	for _, want := range []string{"recorded by r-fixture", "after 2 review round(s), the bound being 2",
		"The Phase 4 gate still never ran after the fix"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the hold does not name %q: %s", want, result.Failure.Message)
		}
	}
	if n := len(reviewDecisions(t, storeOfRun(t, f, "r-resume"))); n != 0 {
		t.Errorf("the resume recorded %d review decisions: an unchanged tree is not reviewed again", n)
	}
	for _, event := range feedStages(t, f.Repo.Dir, "r-resume") {
		if strings.Contains(event.Detail, "no classification") || strings.Contains(event.Detail, "no dispatchable tick") {
			t.Errorf("the resume's feed carries an unclassified stop: %s: %s", event.Stage, event.Detail)
		}
	}
	if onOrigin(f, originHead(t, f, "epic/qeu"), "main") {
		t.Error("main carries the epic: the run merged work its own review still rejects")
	}
}
