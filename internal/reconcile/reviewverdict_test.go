package reconcile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The review's verdict (tick b50): a review's judgement is a typed field with
// an effect, never prose. These tests are the acceptance:
//
//   1. a review answering NOT READY produces a durable decision record that
//      cannot be read as approval — its own verdict as review_verdict, and
//      the collect vocabulary's ready-to-merge nowhere in it at all;
//   2. the NOT READY verdict is carried to the epic PR by a STATED rule (the
//      close-out is not held on it; the person merging reads it), and the
//      carry has teeth: the PR body states the verdict;
//   3. a review whose report never stated its judgement is refused — the tick
//      is not closed behind an answer nobody can read.

// validateReviewVerdict: the field is required, the vocabulary is closed, and
// a review that said neither word is refused rather than guessed at.
func TestValidateReviewVerdict(t *testing.T) {
	t.Parallel()
	reviewPayload := func(verdict any) *subprocess.RoleResult {
		payload := map[string]any{}
		if verdict != nil {
			payload["review_verdict"] = verdict
		}
		return &subprocess.RoleResult{
			SchemaVersion: subprocess.SchemaVersionRoleResult,
			SchemaID:      "ticfac.job-result.review-epic.v1",
			Role:          "review-epic",
			Status:        subprocess.StatusDoneWithConcerns,
			Summary:       "the review is complete and its verdict is NOT READY",
			Result:        payload,
		}
	}
	if err := validateReviewVerdict(reviewPayload(subprocess.ReviewVerdictNotReady)); err != nil {
		t.Errorf("a NOT READY verdict was refused: %v", err)
	}
	if err := validateReviewVerdict(reviewPayload(subprocess.ReviewVerdictReady)); err != nil {
		t.Errorf("a READY verdict was refused: %v", err)
	}
	if err := validateReviewVerdict(reviewPayload(nil)); err == nil {
		t.Error("a review that stated no verdict was accepted: prose is where NOT READY went to be recorded as its opposite")
	} else if !strings.Contains(err.Error(), "REVIEW-VERDICT") {
		t.Errorf("the refusal does not tell the review what to state: %v", err)
	}
	if err := validateReviewVerdict(reviewPayload("ready-to-merge")); err == nil {
		t.Error("the collect vocabulary's word was accepted as the review's verdict: they answer different questions")
	} else if !strings.Contains(err.Error(), "closed vocabulary") {
		t.Errorf("the refusal does not name the closed vocabulary: %v", err)
	}
	if err := validateReviewVerdict(reviewPayload("MAYBE")); err == nil {
		t.Error("a word outside the vocabulary was accepted")
	}
}

// The PR body's verdict paragraph, from a RECORDED decision: the typed verdict
// first, the stated carry rule on a NOT READY, and a legacy record — one that
// predates the typed field — stating its status and summary rather than
// nothing, which a person merging would read as "no review found anything".
func TestReviewVerdictParagraph(t *testing.T) {
	t.Parallel()
	typed := runstate.Decision{
		Decision: 1,
		Role:     "review-epic",
		Response: map[string]any{
			"status":  subprocess.StatusDoneWithConcerns,
			"summary": "the review is complete and its verdict is NOT READY",
			"result":  map[string]any{"review_verdict": subprocess.ReviewVerdictNotReady},
		},
	}
	paragraph := reviewVerdictParagraph(typed)
	if !strings.Contains(paragraph, "judged the epic NOT READY") {
		t.Errorf("the paragraph does not state the review's own verdict:\n%s", paragraph)
	}
	if !strings.Contains(paragraph, "The close-out is not held on this verdict") {
		t.Errorf("the paragraph does not state the rule a NOT READY follows:\n%s", paragraph)
	}
	if strings.Contains(paragraph, subprocess.VerdictReadyToMerge) {
		t.Errorf("the paragraph spells the collect vocabulary's word, which a reader takes for approval:\n%s", paragraph)
	}

	ready := typed
	ready.Response = map[string]any{
		"status":  subprocess.StatusDone,
		"summary": "the epic does what it said it would",
		"result":  map[string]any{"review_verdict": subprocess.ReviewVerdictReady},
	}
	if got := reviewVerdictParagraph(ready); !strings.Contains(got, "judged the epic READY") {
		t.Errorf("the paragraph does not state the READY verdict:\n%s", got)
	}

	// The epic-ncv decision 1 record itself: a summary that says NOT READY
	// and no typed field, because the judgement had nowhere to live yet. The
	// paragraph states what the record has, and stays honest about the gap.
	legacy := typed
	legacy.Response = map[string]any{
		"status":  subprocess.StatusDoneWithConcerns,
		"summary": "the review is complete and its verdict is NOT READY",
		"result":  map[string]any{"verdict": subprocess.VerdictReadyToMerge},
	}
	got := reviewVerdictParagraph(legacy)
	if !strings.Contains(got, "answered DONE_WITH_CONCERNS") {
		t.Errorf("a legacy record's paragraph does not state the review's answer:\n%s", got)
	}
	if strings.Contains(got, "judged the epic") {
		t.Errorf("a record that never stated a typed verdict is spelled as one:\n%s", got)
	}
}

// THE ACCEPTANCE. The pxc shape, end to end: a review answering NOT READY is
// collected, its judgement validates, the decision record carries its OWN
// verdict — and the word ready-to-merge appears nowhere in it, not even in
// the summary — the review tick closes behind an answer that can be read,
// the run reaches the close-out, and the epic PR carries the verdict and the
// stated rule, because the merge is a person's and the PR is where that
// judgement is made.
func TestAReviewAnsweringNotReadyIsRecordedAsItsOwnVerdictAndCarriedToThePR(t *testing.T) {
	t.Parallel()

	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "review_not_ready", pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	repo := f.Repo
	_, result, err := f.run(repo, fixtureOptions{mode: "review_not_ready", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a NOT READY review is carried to the PR, not held against the close-out",
			result.State, result.Failure)
	}

	// The review tick closed behind an answer that could be read.
	current, err := f.Tracker.Show(context.Background(), "rv")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("rv is %s: the review closed behind its validated answer whatever the verdict said", current.Status)
	}

	// The durable record: the review's OWN verdict, spelled in its own
	// vocabulary, and the collect vocabulary's word nowhere in the response.
	decisions, err := draftsStore(t, repo).Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var review *runstate.Decision
	for i := range decisions {
		if decisions[i].Role == "review-epic" {
			review = &decisions[i]
		}
	}
	if review == nil {
		t.Fatal("no review decision was recorded")
	}
	if got := reviewVerdictOf(review.Response); got != subprocess.ReviewVerdictNotReady {
		t.Errorf("the recorded decision's review_verdict is %q, want %s", got, subprocess.ReviewVerdictNotReady)
	}
	raw, err := json.Marshal(review.Response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), subprocess.VerdictReadyToMerge) {
		t.Errorf("the recorded decision spells the collect vocabulary's word, which a reader takes for an "+
			"approval the review never gave:\n%s", raw)
	}

	// The PR body: the verdict a person merging reads, the stated rule, and
	// the word that read as approval — nowhere.
	body := forge.body()
	if body == "" {
		t.Fatal("the epic PR was opened with no body at all")
	}
	if !strings.Contains(body, "judged the epic NOT READY") {
		t.Errorf("the PR body does not state the review's verdict:\n%s", body)
	}
	if !strings.Contains(body, "The close-out is not held on this verdict") {
		t.Errorf("the PR body does not state the rule a NOT READY follows:\n%s", body)
	}
	if strings.Contains(body, subprocess.VerdictReadyToMerge) {
		t.Errorf("the PR body spells the collect vocabulary's word beside a NOT READY review:\n%s", body)
	}
}

// A review whose report never stated its judgement — the prose-only answer,
// which is where NOT READY went to be recorded as its opposite — is refused:
// the run fails naming the line the report lacked, and the review tick is NOT
// closed behind an answer nobody can read.
func TestAReviewThatNeverStatedItsVerdictIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "review_no_verdict"})
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_no_verdict"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedRoleResult {
		t.Fatalf("failure %+v, want a %s refusal", result.Failure, RefusedRoleResult)
	}
	if result.Failure.TickID != "rv" {
		t.Fatalf("the failure is about %s, want rv", result.Failure.TickID)
	}
	for _, want := range []string{"REVIEW-VERDICT", "prose"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the refusal does not say %q: %q", want, result.Failure.Message)
		}
	}
	current, err := f.Tracker.Show(context.Background(), "rv")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("rv was closed behind an answer whose judgement nobody can read")
	}
	decisions, err := draftsStore(t, f.Repo).Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range decisions {
		if d.Role == "review-epic" {
			t.Errorf("a decision %d was recorded for an answer that never stated its judgement", d.Decision)
		}
	}
}
