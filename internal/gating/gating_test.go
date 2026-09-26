package gating

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/jev"
)

// The fixture is gvc's own shape: an epic whose done enumerates into items,
// some bound to a command the oracle can run (A2) and some not yet runnable
// (A1, A3) — the unverified ones are the classifier's to predict, and the
// runnable one must not even be offered to it, because running it is the
// authoritative verdict and no classifier overrides it.
const criteria = "[A1] A cloud run dispatches on the model the gateway names.\n" +
	"[A2] Every tick closes behind a green gate.\n" +
	"[A3] The close-out retro reports what was absorbed, against which item, and whether predicted or observed."

func doneOf(t *testing.T, criteria string, evidence map[string]string) acceptance.Done {
	t.Helper()
	done, refusal, err := acceptance.Decide(criteria, evidence)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if refusal != nil {
		t.Fatalf("Decide refused a marked acceptance: %s", refusal)
	}
	return done
}

// fakeClassifier stands in for *jev.Client at the seam: one Ask, captured, and
// the answer handed back.
type fakeClassifier struct {
	result    jev.AnswerResult
	err       error
	calls     int
	state     string
	questions []jev.Question
}

func (f *fakeClassifier) Ask(_ context.Context, state string, questions []jev.Question) (jev.AnswerResult, error) {
	f.calls++
	f.state = state
	f.questions = questions
	return f.result, f.err
}

func answerOver(probabilities map[string]float64, choice string) jev.AnswerResult {
	return jev.AnswerResult{
		Answers: map[string]jev.Answer{findingID: {
			Choice:        choice,
			Confidence:    0.62,
			Probabilities: probabilities,
		}},
		Model: "jev-2026-09",
		Usage: jev.Usage{InputTokens: 900, OutputTokens: 40, CostUSD: 0.0001},
	}
}

const findingID = "dc02fb31"

var theFinding = Finding{
	ID:    findingID,
	Title: "A model name spelled two ways",
	Body: "When the table's value is wired into a cloud dispatch, a value routed into a container boot will be refused; " +
		"no test boots a container, so nothing is red, but the run the done names would not start.",
}

// The question is a Choice over THIS EPIC'S OWN not-yet-runnable items plus
// 'none', in document order with none last — and a runnable item is never
// offered, because running it is the authoritative verdict and no classifier
// overrides it.
func TestTheQuestionIsAChoiceOverTheUnverifiedItemsPlusNone(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"A1": 0.7, "A3": 0.1, "none": 0.2}, "A1")}
	predictor := NewPredictor(classifier)
	done := doneOf(t, criteria, map[string]string{"A2": "go"})
	verdict, _, err := predictor.Predict(context.Background(), theFinding, done)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict == nil {
		t.Fatal("Predict returned no verdict")
	}
	if classifier.calls != 1 {
		t.Fatalf("the classifier was asked %d times, want one call", classifier.calls)
	}
	if len(classifier.questions) != 1 {
		t.Fatalf("%d questions were sent, want exactly one", len(classifier.questions))
	}
	question := classifier.questions[0]
	if question.ID != findingID {
		t.Errorf("the question is keyed %q, want the finding id %q", question.ID, findingID)
	}
	if question.Instructions == "" {
		t.Error("the question carries no instructions")
	}
	labels := make([]string, 0, len(question.Choices))
	for _, choice := range question.Choices {
		labels = append(labels, choice.Label)
	}
	want := []string{"A1", "A3", NoneLabel}
	if len(labels) != len(want) {
		t.Fatalf("the enum is %v, want %v: the runnable A2 is the oracle's, not the classifier's", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("the enum is %v, want %v in this order (document order, none last)", labels, want)
		}
	}
	for _, choice := range question.Choices {
		if choice.Criterion.What == "" || choice.Criterion.NotFor == "" || len(choice.Criterion.Examples) == 0 {
			t.Errorf("choice %q carries an incomplete criterion: %+v", choice.Label, choice.Criterion)
		}
	}
	for _, text := range []string{theFinding.Title, theFinding.Body} {
		if !strings.Contains(classifier.state, text) {
			t.Errorf("the state does not carry %q: the classifier judges the finding's own prose", text)
		}
	}
}

// THE BIAS, stated as a test: the argmax is 'none' at 0.6, and the finding
// still gates, because the decision spends MASS and the threshold is
// deliberately cheap. A classifier that says "probably fine" must still
// absorb when the mass says otherwise — that is what erring toward absorbing
// means in code.
func TestThePredictionSpendsMassNotTheArgmax(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 0.6, "A1": 0.4}, NoneLabel)}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !verdict.Gating {
		t.Fatalf("a finding carrying %.2f of the probability mass was predicted non-gating: %+v", 0.4, verdict)
	}
	if verdict.ItemID != "A1" {
		t.Errorf("the item is %q, want A1 — the distribution's own maximum among the items", verdict.ItemID)
	}
	if verdict.GatingMass != 0.4 {
		t.Errorf("the gating mass is %v, want 0.4", verdict.GatingMass)
	}
}

// The threshold's placement is load-bearing, so it is guarded rather than
// remembered: it must sit on the CHEAP side of a coin flip, and a marginal
// distribution that reaches it must gate.
func TestTheThresholdSitsOnTheCheapSide(t *testing.T) {
	t.Parallel()

	if AbsorbThreshold >= 0.5 {
		t.Fatalf("AbsorbThreshold = %v: the threshold belongs well below a coin flip, or a false negative closes an "+
			"epic whose goal is unmet in a factory where nobody is watching", AbsorbThreshold)
	}
	classifier := &fakeClassifier{result: answerOver(map[string]float64{
		"none": 1 - AbsorbThreshold - 0.01, "A1": AbsorbThreshold + 0.01}, NoneLabel)}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !verdict.Gating {
		t.Errorf("a distribution clearing the threshold by a hair did not gate: %+v", verdict)
	}
	if !strings.Contains(verdict.Reason, "false negative") || !strings.Contains(verdict.Reason, "false positive") {
		t.Errorf("the reason %q does not state the cost asymmetry the threshold sits on", verdict.Reason)
	}
}

// The other side of the bias: a distribution carried by 'none' does not gate,
// the finding is a backlog tick with an owner, and the reason says so rather
// than leaving the reader to infer what "not gating" means for the finding.
func TestADistributionCarriedByNoneDoesNotGate(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 0.95, "A1": 0.05}, NoneLabel)}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict.Gating {
		t.Fatalf("a finding carrying 0.05 of the mass was predicted gating: %+v", verdict)
	}
	if verdict.ItemID != "" {
		t.Errorf("the non-gating verdict names item %q, want none", verdict.ItemID)
	}
	if !strings.Contains(verdict.Reason, "backlog") {
		t.Errorf("the reason %q does not say the finding belongs to a backlog tick", verdict.Reason)
	}
}

// The answer's own choice is the item absorption has to name, even when the
// mass alone would have pointed elsewhere — the answer doubles as the item id.
func TestTheChosenItemIsTheItemAbsorptionNames(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 0.2, "A1": 0.3, "A3": 0.5}, "A3")}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !verdict.Gating || verdict.ItemID != "A3" {
		t.Errorf("the verdict is gating=%v item=%q, want the classifier's own choice A3", verdict.Gating, verdict.ItemID)
	}
}

// THE RECORD: a prediction is never an observation. Two verdicts differing
// only in basis must be distinguishable in the record — a retro that cannot
// tell a guess from a measurement cannot report honestly, and the next tick
// scores predictions against what the done later did, which is impossible if
// the two read the same.
func TestAPredictedVerdictIsDistinguishableFromAnObservedOne(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 0.2, "A1": 0.8}, "A1")}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict.Basis != BasisPredicted {
		t.Fatalf("a classified prediction recorded basis %q, want %q", verdict.Basis, BasisPredicted)
	}
	predicted, err := json.Marshal(*verdict)
	if err != nil {
		t.Fatalf("marshal the predicted verdict: %v", err)
	}
	observed, err := json.Marshal(Verdict{FindingID: verdict.FindingID, Gating: true, ItemID: "A1", Basis: BasisObserved})
	if err != nil {
		t.Fatalf("marshal the observed verdict: %v", err)
	}
	var predictedBasis, observedBasis Basis
	if err := json.Unmarshal(predicted, &struct{ Basis *Basis }{&predictedBasis}); err != nil {
		t.Fatalf("re-read the predicted verdict: %v", err)
	}
	if err := json.Unmarshal(observed, &struct{ Basis *Basis }{&observedBasis}); err != nil {
		t.Fatalf("re-read the observed verdict: %v", err)
	}
	if predictedBasis != BasisPredicted || observedBasis != BasisObserved {
		t.Errorf("the record reads bases %q and %q, want %q and %q: a guess and a measurement must survive the round trip as themselves",
			predictedBasis, observedBasis, BasisPredicted, BasisObserved)
	}
	if predictedBasis == observedBasis {
		t.Error("a predicted and an observed verdict are indistinguishable in the record")
	}
	if strings.Contains(string(predicted), string(BasisObserved)) {
		t.Errorf("the predicted verdict's record carries %q: a prediction is never recorded as observed\n%s", BasisObserved, predicted)
	}
}

// THE FALLBACK, and it is the one the acceptance names: an unreachable
// classifier ABSORBS rather than deferring. Deferring would stop an unattended
// run on the only actor a person plays; absorbing risks work the epic did not
// need, which is the cheaper failure by the stated asymmetry.
func TestAnUnavailableClassifierFallsBackToAbsorbing(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: jev.AnswerResult{
		Unavailable: "the classifier could not be reached: connection refused",
	}}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("an unreachable classifier returned an error (%v); absorbing is the documented fallback, not a stop", err)
	}
	if verdict == nil || !verdict.Gating {
		t.Fatalf("an unreachable classifier did not absorb: %+v", verdict)
	}
	if verdict.Basis != BasisPredicted {
		t.Errorf("the fallback recorded basis %q: it is still a guess, never an observation", verdict.Basis)
	}
	if !strings.Contains(verdict.Fallback, "could not be reached") {
		t.Errorf("the fallback %q does not name why no prediction was made", verdict.Fallback)
	}
	if !strings.Contains(verdict.Reason, "false negative") {
		t.Errorf("the reason %q does not state the asymmetry the fallback rests on", verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "A1") || !strings.Contains(verdict.Reason, "A3") {
		t.Errorf("the reason %q does not name the unverified items at risk", verdict.Reason)
	}
}

// A predictor with no classifier wired at all is the same no-prediction as an
// unreachable one — the documented degradation absorbs, it never panics and it
// never stops.
func TestANilClassifierAbsorbsLikeAnUnavailableOne(t *testing.T) {
	t.Parallel()

	predictor := NewPredictor(nil)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("a nil classifier returned an error (%v); absorbing is the documented fallback", err)
	}
	if verdict == nil || !verdict.Gating {
		t.Fatalf("a nil classifier did not absorb: %+v", verdict)
	}
	if verdict.Basis != BasisPredicted {
		t.Errorf("the fallback recorded basis %q: it is a guess, never an observation", verdict.Basis)
	}
	if !strings.Contains(verdict.Fallback, "no classifier is configured") {
		t.Errorf("the fallback %q does not say no classifier is configured", verdict.Fallback)
	}
}

// A question the call answered with a named gap is the same epistemic state as
// an unreachable classifier: no prediction exists, and the decision absorbs
// rather than defers — for the same stated reason.
func TestAnUnansweredQuestionFallsBackToAbsorbingToo(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: jev.AnswerResult{
		Unanswered: map[string]string{findingID: "the answer put probability on \"later\", which is not a choice on the closed enum"},
	}}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("an unanswered question returned an error (%v); absorbing is the documented fallback", err)
	}
	if verdict == nil || !verdict.Gating {
		t.Fatalf("an unanswered question did not absorb: %+v", verdict)
	}
	if !strings.Contains(verdict.Fallback, "closed enum") {
		t.Errorf("the fallback %q does not carry the gap's own reason", verdict.Fallback)
	}
}

// A classifier answering neither an answer nor a named gap — a violated seam,
// not a judgement — is treated as no prediction and absorbs too: the fallback
// is about the EPISTEMIC state, not about which way the classifier failed.
func TestAClassifierAnsweringNeitherAnswerNorGapAbsorbs(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: jev.AnswerResult{
		Answers: map[string]jev.Answer{}, Unanswered: map[string]string{},
	}}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict == nil || !verdict.Gating {
		t.Fatalf("a classifier answering nothing did not absorb: %+v", verdict)
	}
}

// Where every item is the oracle's, there is nothing to predict — and that is
// a named outcome, not a verdict guessed anyway: running the done is the
// authoritative verdict, and this package does not predict over what can be
// run.
func TestNothingToPredictWhenEveryItemIsTheOracles(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"A1": 1}, "A1")}
	predictor := NewPredictor(classifier)
	done := doneOf(t, criteria, map[string]string{"A1": "go", "A2": "go", "A3": "go"})
	verdict, nothing, err := predictor.Predict(context.Background(), theFinding, done)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict != nil {
		t.Fatalf("the classifier predicted over a fully runnable done: %+v", verdict)
	}
	if nothing == "" {
		t.Fatal("a fully runnable done produced no named reason for predicting nothing")
	}
	for _, want := range []string{"authoritative", "runnable"} {
		if !strings.Contains(nothing, want) {
			t.Errorf("the reason %q does not name %q", nothing, want)
		}
	}
	if classifier.calls != 0 {
		t.Errorf("the classifier was asked %d times over the oracle's items, want never", classifier.calls)
	}
}

// A done with no items never reaches a question: enumerating it is klq's
// refusal, and this package does not silently stand in for the refusal with a
// guess of its own.
func TestNothingToPredictWhenTheDoneCarriesNoItems(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 1}, "none")}
	predictor := NewPredictor(classifier)
	verdict, nothing, err := predictor.Predict(context.Background(), theFinding, acceptance.Done{})
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict != nil {
		t.Fatalf("an empty done was predicted against: %+v", verdict)
	}
	if nothing == "" {
		t.Fatal("an empty done produced no named reason")
	}
	if !strings.Contains(nothing, "refus") {
		t.Errorf("the reason %q does not name that the refusal owns this case, not a prediction", nothing)
	}
	if classifier.calls != 0 {
		t.Errorf("the classifier was asked %d times over no items, want never", classifier.calls)
	}
}

// Caller defects are hard errors, not verdicts: a finding with no id cannot be
// keyed, and guessing for it would be a decision wearing a defect's costume.
func TestCallerDefectsAreRefused(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 1}, "none")}
	predictor := NewPredictor(classifier)
	if _, _, err := predictor.Predict(context.Background(), Finding{Title: "no id"}, doneOf(t, criteria, nil)); err == nil {
		t.Error("a finding with no id was predicted rather than refused")
	}
	if classifier.calls != 0 {
		t.Errorf("a caller defect made %d calls; defects are refused before the wire", classifier.calls)
	}
}

// The verdict carries what a later pass needs to re-derive the decision without
// re-paying the classifier: the confidence as answered, the model identity, and
// the full distribution over the enum actually asked — item ids and 'none'.
func TestTheVerdictCarriesConfidenceModelAndDistribution(t *testing.T) {
	t.Parallel()

	classifier := &fakeClassifier{result: answerOver(map[string]float64{"none": 0.2, "A1": 0.6, "A3": 0.2}, "A1")}
	predictor := NewPredictor(classifier)
	verdict, _, err := predictor.Predict(context.Background(), theFinding, doneOf(t, criteria, map[string]string{"A2": "go"}))
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if verdict.Confidence != 0.62 {
		t.Errorf("confidence = %v, want 0.62 as answered", verdict.Confidence)
	}
	if verdict.Model != "jev-2026-09" {
		t.Errorf("model = %q, want the answering model's identity", verdict.Model)
	}
	for _, label := range []string{"A1", "A3", NoneLabel} {
		if _, ok := verdict.Probabilities[label]; !ok {
			t.Errorf("the recorded distribution is missing %q: the enum as asked is the record's own shape", label)
		}
	}
	if _, ok := verdict.Probabilities["A2"]; ok {
		t.Errorf("the recorded distribution carries A2, which was never offered: the oracle owns it")
	}
}
