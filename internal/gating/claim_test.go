package gating

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
)

// The reporter's done evidence (tick nfo), wired into the decision as INPUTS
// and never as verdicts (tick wz0, finding c244ce2c): the worker prompt
// promises that the run runs the named check where it can, predicts where it
// cannot yet, and scores the claim against what the done actually did. These
// tests pin that the claim REACHES both tiers — the classifier sees it as
// evidence, the oracle scores it against what ran — while neither tier lets
// the claim BE the verdict: the observation still overrides, the prediction
// still spends mass, and an unlinked finding (no claim) is judged exactly as
// it was.

// The CLAIMED finding: the shape a worker's findings block carries when it
// reports done evidence — done_item naming the item it believes broken, and
// demonstrating_check naming the command it says would show the breakage.
var theClaimedFinding = Finding{
	ID:    findingID,
	Title: theFinding.Title,
	Body:  theFinding.Body,
	// The reporter's claim (tick nfo): A3 is the item the finding is said to
	// break, and "retro" the command said to demonstrate it.
	DoneItem:           "A3",
	DemonstratingCheck: "retro",
}

// THE CLASSIFIER'S HALF: the claim reaches the question's state as evidence,
// so the prediction is made WITH the reporter's answer in front of it —
// while the enum stays the unverified items plus 'none': the claim adds
// evidence, never a choice, because the claim is never the verdict.
//
// short: ask over a state already in memory
func TestTheReportersClaimReachesTheClassifierAsEvidence(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"A3": 0.8, "none": 0.2}, "A3")}
	predictor := NewPredictor(classifier)
	// A2 is runnable, so the oracle owns it and the claim's A3 is one of the
	// classifier's own items — the claim points INSIDE the question's enum.
	done := doneOf(t, criteria, map[string]string{"A2": "go"})
	verdict, _, err := predictor.Predict(context.Background(), theClaimedFinding, done)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict == nil || !verdict.Gating {
		t.Fatalf("Predict answered %+v, want the gating prediction the fixture's mass makes", verdict)
	}
	for _, named := range []string{"A3", "retro", findingID, theFinding.Title} {
		if !strings.Contains(classifier.state, named) {
			t.Errorf("the classifier's state does not carry %q, a claim the decision takes as evidence: %q",
				named, classifier.state)
		}
	}
	// The claim never widens the enum: the choices stay the unverified items
	// plus none, in document order, and no choice is "the reporter's claim".
	question := classifier.questions[0]
	labels := make([]string, 0, len(question.Choices))
	for _, choice := range question.Choices {
		labels = append(labels, choice.Label)
	}
	want := []string{"A1", "A3", NoneLabel}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("the enum over a claimed finding is %v, want %v: the claim is evidence, never a choice", labels, want)
	}
}

// The NONE claim reaches the classifier too — the reporter's answer that the
// finding breaks no item is a claim like any other, scored rather than
// believed, so the classifier sees what the reporter said before it answers.
//
// short: ask over a state already in memory
func TestANoneClaimReachesTheClassifierAsEvidence(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"A1": 0.1, "A3": 0.1, "none": 0.8}, "none")}
	predictor := NewPredictor(classifier)
	done := doneOf(t, criteria, nil)
	claimed := theClaimedFinding
	claimed.DoneItem, claimed.DemonstratingCheck = NoneLabel, ""
	if _, _, err := predictor.Predict(context.Background(), claimed, done); err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !strings.Contains(classifier.state, "no acceptance item") &&
		!strings.Contains(classifier.state, NoneLabel) {
		t.Errorf("the classifier's state does not carry the reporter's none claim: %q", classifier.state)
	}
}

// An UNLINKED finding — no done evidence reported — asks exactly as it did
// before the claim existed: no claim line in the state, and the same
// question. A missing claim must be the visible third state it is on the
// draft, never a claim of non-gating.
//
// short: ask over a state already in memory
func TestAnUnlinkedFindingAsksWithNoClaim(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"A3": 0.8, "none": 0.2}, "A3")}
	predictor := NewPredictor(classifier)
	done := doneOf(t, criteria, nil)
	if _, _, err := predictor.Predict(context.Background(), theFinding, done); err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if strings.Contains(classifier.state, "the reporter claims") {
		t.Errorf("an unlinked finding's state carries a claim line: %q", classifier.state)
	}
}

// THE ORACLE'S HALF: the claim is SCORED against what the done actually did.
// Where the observation answers the claimed item the reason says which way
// the claim came out — confirmed by a run that showed the item broken,
// refuted by a run that demonstrated it — and where it could not, the reason
// says the claim went unscored rather than letting silence read as
// agreement. The verdict itself is still the run's: a refuted claim does not
// un-break a broken item, and a confirmed claim proves nothing the command
// did not already show.
//
// short: observe over fake runs already in memory
func TestTheOracleScoresTheClaimAgainstWhatRan(t *testing.T) {
	t.Parallel()

	done := acceptance.Done{Items: []acceptance.Resolved{
		{Item: acceptance.Item{ID: "A1", Text: "runnable"}, State: acceptance.Runnable, Command: "go"},
	}}
	claimed := theClaimedFinding
	claimed.DoneItem = "A1"

	// CONFIRMED: the reporter said A1, and A1's command is observed broken
	// while the finding stands.
	oracle := NewOracle(&fakeRunner{answers: map[string]Run{"go": {
		Commit: "abc123def456", Result: ResultFail, ExitCode: 1, Stderr: "red",
		StartedAt: "2026-09-26T10:00:00Z", FinishedAt: "2026-09-26T10:01:00Z",
	}}})
	observed, _, err := oracle.Observe(context.Background(), claimed, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observed.Gating || observed.ItemID != "A1" {
		t.Fatalf("the observation is %+v, want the run's own gating verdict on A1", observed.Verdict)
	}
	if !strings.Contains(observed.Reason, "confirmed") {
		t.Errorf("the reason does not score the reporter's claim against the run: %q", observed.Reason)
	}

	// REFUTED: the reporter said A1, and A1's command passed while the
	// finding stands — the claim is scored, and the verdict is still the
	// run's not-gating observation.
	oracle = NewOracle(&fakeRunner{answers: map[string]Run{"go": passOn("abc123def456")}})
	observed, _, err = oracle.Observe(context.Background(), claimed, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Gating {
		t.Fatalf("a passing command produced a gating verdict: %+v", observed.Verdict)
	}
	if !strings.Contains(observed.Reason, "refuted") {
		t.Errorf("the reason does not score the reporter's claim against the run: %q", observed.Reason)
	}
}

// A NONE claim against a run that showed an item broken: the claim is
// scored, and the score is against the reporter — the observation stands
// whatever the reporter believed.
//
// short: observe over fake runs already in memory
func TestANoneClaimIsScoredAgainstABreakingRun(t *testing.T) {
	t.Parallel()

	done := acceptance.Done{Items: []acceptance.Resolved{
		{Item: acceptance.Item{ID: "A1", Text: "runnable"}, State: acceptance.Runnable, Command: "go"},
	}}
	claimed := theClaimedFinding
	claimed.DoneItem, claimed.DemonstratingCheck = NoneLabel, ""
	oracle := NewOracle(&fakeRunner{answers: map[string]Run{"go": {
		Commit: "abc123def456", Result: ResultFail, ExitCode: 1, Stderr: "red",
		StartedAt: "2026-09-26T10:00:00Z", FinishedAt: "2026-09-26T10:01:00Z",
	}}})
	observed, _, err := oracle.Observe(context.Background(), claimed, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observed.Gating {
		t.Fatalf("the reporter's none claim overrode a breaking run: %+v", observed.Verdict)
	}
	if !strings.Contains(observed.Reason, "refuted") {
		t.Errorf("the reason does not score the reporter's none claim against the breaking run: %q", observed.Reason)
	}
}

// An UNSCOREABLE claim — the claimed item produced no evidence, or names an
// item the done does not carry — is said to be just that, never silently
// agreed with: the visible third state, on the record a person reads.
//
// short: observe over fake runs already in memory
func TestAnUnscoreableClaimIsSaidToBeUnscoreable(t *testing.T) {
	t.Parallel()

	done := acceptance.Done{Items: []acceptance.Resolved{
		{Item: acceptance.Item{ID: "A1", Text: "runnable"}, State: acceptance.Runnable, Command: "go"},
		{Item: acceptance.Item{ID: "A3", Text: "unverified"}, State: acceptance.Unverified},
	}}

	// The claimed item produced no evidence of its own: A1's command cannot
	// run, A3 is the classifier's — the claim is scored against nothing.
	claimed := theClaimedFinding
	oracle := NewOracle(&fakeRunner{errs: map[string]error{"go": errRunnerDead}, answers: map[string]Run{}})
	observed, reason, err := oracle.Observe(context.Background(), claimed, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed != nil {
		t.Fatalf("a runner that could run nothing observed %+v, want the nil answer", observed.Verdict)
	}
	if !strings.Contains(reason, "A1") {
		t.Errorf("the no-observation reason does not carry the claimed item's unresolved state: %q", reason)
	}

	// A claim naming an item the done does not carry: scored against nothing,
	// and said so on the verdict the rest of the run did reach.
	claimed.DoneItem = "A9"
	oracle = NewOracle(&fakeRunner{answers: map[string]Run{"go": {
		Commit: "abc123def456", Result: ResultFail, ExitCode: 1,
		StartedAt: "2026-09-26T10:00:00Z", FinishedAt: "2026-09-26T10:01:00Z",
	}}})
	observed, _, err = oracle.Observe(context.Background(), claimed, done)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observed.Gating {
		t.Fatalf("the run's own verdict was lost behind the malformed claim: %+v", observed.Verdict)
	}
	if !strings.Contains(observed.Reason, "A9") || !strings.Contains(observed.Reason, "no item") {
		t.Errorf("the reason does not say the claim names an item the done does not carry: %q", observed.Reason)
	}
}
