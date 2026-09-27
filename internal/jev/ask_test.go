package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// The generalised core (tick bse, absorbing wne's finding dc02fb31): the wire
// is one SHAPE — a state plus Choice questions over closed enums — and the
// work-type classification was only ever its first user. These tests ask with
// labels that are NOT work types, so a core that still secretly knows the
// work-type enum cannot pass them.

// answerOver builds a handler that answers each question over its own
// labels — whatever closed enum the caller asked, never the work-type
// vocabulary. The label order is deliberately NOT on the wire (the criteria
// object is unordered JSON), so a reader of the request body answers over the
// labels it can see — the criteria keys — deterministically.
func answerOver(t *testing.T) func(body []byte) (int, any) {
	t.Helper()
	return func(body []byte) (int, any) {
		var sent request
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Errorf("the request did not decode: %v", err)
			return 500, map[string]any{"error": "unreadable request"}
		}
		answers := make([]any, 0, len(sent.Questions))
		for _, question := range sent.Questions {
			labels := make([]string, 0, len(question.Criteria))
			for label := range question.Criteria {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			probabilities := make(map[string]float64, len(labels))
			for i, label := range labels {
				if i == 0 {
					probabilities[label] = 0.7
					continue
				}
				probabilities[label] = 0.3 / float64(len(labels)-1)
			}
			answers = append(answers, map[string]any{
				"id":            question.ID,
				"choice":        labels[0],
				"confidence":    0.7,
				"probabilities": probabilities,
			})
		}
		return 200, jevBody(answers)
	}
}

// One Ask round trip sends the caller's state and one Choice question per
// question, each carrying ITS OWN closed enum as its criteria — the labels are
// the caller's, not the work-type vocabulary's.
func TestAskSendsTheStateAndEachQuestionsOwnEnum(t *testing.T) {
	server, client := serve(t, answerOver(t))
	questions := []Question{{
		ID:           "f1",
		Instructions: "Does the finding break an item?",
		Choices: []Choice{
			{Label: "A1", Criterion: Criterion{What: "the finding breaks A1", NotFor: "it does not", Examples: []string{"one"}}},
			{Label: "none", Criterion: Criterion{What: "no item depends on it", NotFor: "nothing has run red yet", Examples: []string{"one"}}},
		},
	}, {
		ID:           "f2",
		Instructions: "Does the second finding break an item?",
		Choices: []Choice{
			{Label: "alpha", Criterion: Criterion{What: "first", NotFor: "no", Examples: []string{"x"}}},
			{Label: "beta", Criterion: Criterion{What: "second", NotFor: "no", Examples: []string{"x"}}},
		},
	}}
	result, err := client.Ask(context.Background(), "a state", questions)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Unavailable != "" {
		t.Fatalf("a successful call reported itself unavailable: %s", result.Unavailable)
	}
	server.mu.Lock()
	calls, bodies := len(server.requests), server.requests
	server.mu.Unlock()
	if calls != 1 {
		t.Fatalf("the classifier saw %d requests for %d questions, want one call", calls, len(questions))
	}
	var sent request
	if err := json.Unmarshal(bodies[0], &sent); err != nil {
		t.Fatalf("the request did not decode: %v", err)
	}
	if sent.State != "a state" {
		t.Errorf("the request state = %q, want the caller's own state", sent.State)
	}
	if len(sent.Questions) != 2 {
		t.Fatalf("%d questions were sent, want %d", len(sent.Questions), 2)
	}
	if sent.Questions[0].ID != "f1" || sent.Questions[1].ID != "f2" {
		t.Errorf("the questions were keyed %q and %q, want f1 and f2", sent.Questions[0].ID, sent.Questions[1].ID)
	}
	for _, one := range sent.Questions {
		if one.Type != "choice" {
			t.Errorf("question %s has type %q, want the Choice primitive", one.ID, one.Type)
		}
		if one.Instructions == "" {
			t.Errorf("question %s carries no instructions", one.ID)
		}
	}
	if _, ok := sent.Questions[0].Criteria["A1"]; !ok {
		t.Errorf("the first question's criteria do not carry A1: %+v", sent.Questions[0].Criteria)
	}
	if _, ok := sent.Questions[0].Criteria["none"]; !ok {
		t.Errorf("the first question's criteria do not carry none: %+v", sent.Questions[0].Criteria)
	}
	if _, ok := sent.Questions[1].Criteria["alpha"]; !ok {
		t.Errorf("the second question's criteria do not carry alpha: %+v", sent.Questions[1].Criteria)
	}
}

// The answers come back as answers: the choice, the confidence, the
// distribution over THAT question's labels, the model identity and the usage.
func TestAskReturnsAnswersOverTheCallersOwnLabels(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return 200, jevBody([]any{answer(t, "f1", "alpha", 0.7,
			map[string]float64{"alpha": 0.7, "beta": 0.2, "gamma": 0.1})})
	})
	questions := []Question{{
		ID: "f1", Instructions: "one question",
		Choices: []Choice{
			{Label: "alpha", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
			{Label: "beta", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
			{Label: "gamma", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
		},
	}}
	result, err := client.Ask(context.Background(), "state", questions)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result.Unavailable != "" {
		t.Fatalf("a successful call reported itself unavailable: %s", result.Unavailable)
	}
	got, ok := result.Answers["f1"]
	if !ok {
		t.Fatalf("f1 was not answered (result %+v)", result)
	}
	if got.Choice != "alpha" {
		t.Errorf("choice = %q, want alpha", got.Choice)
	}
	if got.Confidence != 0.7 {
		t.Errorf("confidence = %v, want 0.7 as answered", got.Confidence)
	}
	if got.Probabilities["beta"] != 0.2 {
		t.Errorf("P(beta) = %v, want 0.2: the distribution over the caller's labels is the payload", got.Probabilities["beta"])
	}
	if result.Model != "jev-2026-09" {
		t.Errorf("the answering model was %q, want the identity from result.result.model", result.Model)
	}
	if result.Usage.InputTokens != 52945 || result.Usage.CostUSD != 0.0022 {
		t.Errorf("usage decoded as %+v, want the measured usage", result.Usage)
	}
}

// A label off the QUESTION'S OWN closed set is a protocol change, not a
// judgement call: that question gets no answer, says why, and the rest of the
// batch survives it — the same isolation the work-type path was measured on.
func TestAskValidatesLabelsAgainstTheQuestionsOwnEnum(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return 200, jevBody([]any{
			map[string]any{"id": "good", "choice": "alpha", "confidence": 0.7,
				"probabilities": map[string]float64{"alpha": 0.7, "beta": 0.3}},
			map[string]any{"id": "odd", "choice": "delta", "confidence": 0.9,
				"probabilities": map[string]float64{"delta": 0.9}},
		})
	})
	criterion := Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}
	questions := []Question{
		{ID: "good", Instructions: "q", Choices: []Choice{
			{Label: "alpha", Criterion: criterion}, {Label: "beta", Criterion: criterion}}},
		{ID: "odd", Instructions: "q", Choices: []Choice{
			{Label: "alpha", Criterion: criterion}, {Label: "beta", Criterion: criterion}}},
	}
	result, err := client.Ask(context.Background(), "state", questions)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, ok := result.Answers["good"]; !ok {
		t.Errorf("a good answer in the batch was lost to a bad one beside it: %+v", result)
	}
	reason, ok := result.Unanswered["odd"]
	if !ok {
		t.Fatalf("the off-enum answer was dropped silently: %+v", result)
	}
	if !strings.Contains(reason, "delta") {
		t.Errorf("the no-answer reason %q does not name the offending label", reason)
	}
}

// A choice absent from an otherwise-complete answer is derived from the
// distribution, walking the question's OWN choice order so a tie is broken
// deterministically — not the work-type enum's order, which knows nothing of
// this question's labels.
func TestAskDerivesAnAbsentChoiceFromTheQuestionsOwnOrder(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return 200, jevBody([]any{map[string]any{
			"id":            "q1",
			"probabilities": map[string]float64{"gamma": 0.4, "beta": 0.4, "alpha": 0.2},
		}})
	})
	result, err := client.Ask(context.Background(), "state", []Question{{
		ID: "q1", Instructions: "q",
		// The order is the caller's: gamma before beta, so the 0.4 tie is
		// broken by the caller's order, not by whatever the map handed over.
		Choices: []Choice{
			{Label: "gamma", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
			{Label: "beta", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
			{Label: "alpha", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
		},
	}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got, ok := result.Answers["q1"]
	if !ok {
		t.Fatalf("q1 was not answered (result %+v)", result)
	}
	if got.Choice != "gamma" {
		t.Errorf("derived choice = %q, want gamma: the caller's own order breaks the tie", got.Choice)
	}
}

// Caller defects are hard errors, not no-answers: they never reach the wire.
func TestAskRefusesCallerDefectsBeforeTheWire(t *testing.T) {
	server, client := serve(t, func(body []byte) (int, any) {
		return 200, jevBody([]any{})
	})
	criterion := Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}
	for name, questions := range map[string][]Question{
		"a question with no id": {{Instructions: "q", Choices: []Choice{{Label: "a", Criterion: criterion}}}},
		"two questions with one id": {
			{ID: "dupe", Instructions: "q", Choices: []Choice{{Label: "a", Criterion: criterion}}},
			{ID: "dupe", Instructions: "q", Choices: []Choice{{Label: "a", Criterion: criterion}}},
		},
		"a question with no choices": {{ID: "q1", Instructions: "q"}},
		"a choice with no label":     {{ID: "q1", Instructions: "q", Choices: []Choice{{Criterion: criterion}}}},
		"two choices with one label": {{ID: "q1", Instructions: "q", Choices: []Choice{
			{Label: "a", Criterion: criterion}, {Label: "a", Criterion: criterion}}}},
	} {
		if _, err := client.Ask(context.Background(), "state", questions); err == nil {
			t.Errorf("%s was sent to the wire rather than refused", name)
		}
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 0 {
		t.Errorf("caller defects made %d HTTP calls; defects are refused before the wire", calls)
	}
}

// The unreachable classifier is a no-answer for the general core too, so every
// caller of Ask gets the degradation the work-type path was measured on.
func TestAskReportsAnUnreachableClassifierAsANoAnswer(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := httpServer.URL
	httpServer.Close() // the address now refuses connections

	client := New(Config{APIBase: base, APIKey: "test-key"}, httpServer.Client())
	result, err := client.Ask(context.Background(), "state", []Question{{
		ID: "q1", Instructions: "q", Choices: []Choice{
			{Label: "a", Criterion: Criterion{What: "w", NotFor: "n", Examples: []string{"e"}}},
		},
	}})
	if err != nil {
		t.Fatalf("an unreachable classifier returned an error (%v); it must be a no-answer instead", err)
	}
	if result.Unavailable == "" || !strings.Contains(result.Unavailable, "reach") {
		t.Fatalf("an unreachable classifier produced %+v: the no-answer must say it could not be reached", result)
	}
}

// Asking nothing is not failing to reach: no call, no unavailable, no answers.
func TestAskWithNoQuestionsMakesNoCall(t *testing.T) {
	server, client := serve(t, func(body []byte) (int, any) {
		return 200, jevBody([]any{})
	})
	result, err := client.Ask(context.Background(), "state", nil)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 0 {
		t.Errorf("an empty batch made %d HTTP calls, want none", calls)
	}
	if result.Unavailable != "" {
		t.Errorf("an empty batch reported itself unavailable: %s", result.Unavailable)
	}
	if len(result.Answers) != 0 {
		t.Errorf("an empty batch answered %d questions", len(result.Answers))
	}
}
