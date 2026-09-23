package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The fixture response below is the wire shape the epic's measurement paid to
// find: the answer nests ONE DEEPER than the model page shows —
// result -> {state, result: {model, answers, usage}} — and a read at
// result.answers finds zero answers and no error. Every parse test pins that
// nesting, because it is the one detail of this call that cost real time.

func answer(t *testing.T, tickID, choice string, confidence float64, probabilities map[string]float64) map[string]any {
	t.Helper()
	return map[string]any{
		"id":            tickID,
		"choice":        choice,
		"confidence":    confidence,
		"probabilities": probabilities,
	}
}

func fullProbabilities(choice string) map[string]float64 {
	return map[string]float64{
		"mechanical":   0.02,
		"translation":  0.01,
		"construction": 0.04,
		"diagnosis":    0.03,
		"design":       0.03,
		choice:         0.87, // overwrites its own slot: the chosen work type carries the mass
	}
}

// jevBody wraps answers in the response's full nesting, exactly as measured.
func jevBody(answers any) map[string]any {
	return map[string]any{
		"result": map[string]any{
			"state": "<the state echo>",
			"result": map[string]any{
				"model":   "jev-2026-09",
				"answers": answers,
				"usage":   map[string]any{"input_tokens": 52945, "output_tokens": 0, "cost_usd": 0.0022},
			},
		},
	}
}

type countingServer struct {
	mu       sync.Mutex
	requests []json.RawMessage
	handler  func(body []byte) (int, any)
}

func serve(t *testing.T, handler func(body []byte) (int, any)) (*countingServer, *Client) {
	t.Helper()
	server := &countingServer{handler: handler}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the classifier request: %v", err)
		}
		server.mu.Lock()
		server.requests = append(server.requests, json.RawMessage(body))
		server.mu.Unlock()
		status, payload := handler(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(httpServer.Close)
	client := New(Config{APIBase: httpServer.URL, APIKey: "test-key"}, httpServer.Client())
	return server, client
}

func ticks(n int) []Tick {
	all := make([]Tick, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, Tick{
			ID:                 fmt.Sprintf("t%02d", i),
			Title:              fmt.Sprintf("tick %d title", i),
			Description:        fmt.Sprintf("tick %d description", i),
			AcceptanceCriteria: fmt.Sprintf("tick %d acceptance criteria", i),
		})
	}
	return all
}

// The precondition, which is not an optimisation: a tick carrying a role is
// never classified. The request must not contain it anywhere — not as a
// question, not in the shared state.
func TestRoleCarryingTickIsNeverInTheRequest(t *testing.T) {
	mixed := append(ticks(3),
		Tick{ID: "rev1", Title: "Final review of the diff", Role: "review"},
		Tick{ID: "clo1", Title: "Close out the epic", Role: "closeout"},
	)
	request, asked, err := buildRequest(mixed)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(asked) != 3 {
		t.Fatalf("%d ticks were asked, want the 3 role-less ones", len(asked))
	}
	if len(request.Questions) != 3 {
		t.Fatalf("%d questions in the request, want one per role-less tick", len(request.Questions))
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal the request: %v", err)
	}
	for _, forbidden := range []string{"rev1", "clo1", "Final review", "Close out"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("the request body contains %q: a role-carrying tick is never sent to the classifier, anywhere in the request", forbidden)
		}
	}
	for _, question := range request.Questions {
		if question.ID == "rev1" || question.ID == "clo1" {
			t.Errorf("question %q is for a role-carrying tick", question.ID)
		}
	}
}

// A batch is ONE call: one Choice question per role-less tick, keyed by tick
// id, against one shared state — the isolation the API documents is what makes
// one round trip for a whole epic safe rather than merely cheap.
func TestAnEpicsTicksBatchIntoOneCall(t *testing.T) {
	epic := ticks(5)
	answers := make([]any, 0, len(epic))
	for _, one := range epic {
		answers = append(answers, answer(t, one.ID, "construction", 0.85, fullProbabilities("construction")))
	}
	server, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody(answers)
	})
	result, err := client.Classify(context.Background(), epic)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 1 {
		t.Fatalf("the classifier saw %d requests for %d ticks, want exactly one call", calls, len(epic))
	}
	if result.Unavailable != "" {
		t.Fatalf("a successful call reported itself unavailable: %s", result.Unavailable)
	}
	if len(result.Classifications) != len(epic) {
		t.Fatalf("%d classifications for %d ticks", len(result.Classifications), len(epic))
	}
	if result.Model != "jev-2026-09" {
		t.Errorf("the answering model was %q, want the identity from result.result.model", result.Model)
	}
	if result.Usage.InputTokens != 52945 || result.Usage.CostUSD != 0.0022 {
		t.Errorf("usage decoded as %+v, want the measured 52945 input tokens at $0.0022", result.Usage)
	}
}

// The full distribution is the payload, not decoration: routing is on
// probability mass, so a classification that threw away everything but the
// argmax would starve the next tick of the number it routes on.
func TestAClassificationCarriesTheFullDistribution(t *testing.T) {
	probabilities := map[string]float64{
		"mechanical":   0.02,
		"translation":  0.01,
		"construction": 0.42,
		"diagnosis":    0.04,
		"design":       0.51,
	}
	// A marginal split the measurement actually produced: sz0 was design 0.41
	// / construction 0.34. The argmax is design, but the mass rule needs both.
	server, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{
			answer(t, "sz0", "design", 0.41, probabilities),
		})
	})
	result, err := client.Classify(context.Background(), []Tick{{ID: "sz0", Title: "A marginal split"}})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 1 {
		t.Fatalf("the classifier saw %d requests, want one", calls)
	}
	classification, ok := result.Classifications["sz0"]
	if !ok {
		t.Fatalf("sz0 was not classified (result %+v)", result)
	}
	if len(classification.Probabilities) != len(runconfig.WorkTypeNames) {
		t.Fatalf("%d probabilities, want one per work type on the enum", len(classification.Probabilities))
	}
	if got := classification.Probabilities[runconfig.WorkDesign]; got != 0.51 {
		t.Errorf("P(design) = %v, want 0.51", got)
	}
	if got := classification.Probabilities[runconfig.WorkConstruction]; got != 0.42 {
		t.Errorf("P(construction) = %v, want 0.42: the distribution must carry the mass, not just the argmax", got)
	}
	if classification.Choice != runconfig.WorkDesign {
		t.Errorf("choice = %q, want design", classification.Choice)
	}
	if classification.Confidence != 0.41 {
		t.Errorf("confidence = %v, want 0.41 as answered", classification.Confidence)
	}
}

// A batch where every tick carries a role asks nothing at all: no HTTP call,
// no unavailable, no answer. "Never classified" includes "never billed".
func TestAnAllRoleBatchMakesNoCall(t *testing.T) {
	server, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{})
	})
	result, err := client.Classify(context.Background(), []Tick{
		{ID: "rev1", Title: "Final review", Role: "review"},
		{ID: "clo1", Title: "Close out", Role: "closeout"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 0 {
		t.Fatalf("a batch of only role ticks made %d HTTP calls, want none", calls)
	}
	if result.Unavailable != "" {
		t.Errorf("a batch that asked nothing reported itself unavailable: %s", result.Unavailable)
	}
	if len(result.Classifications) != 0 {
		t.Errorf("a batch that asked nothing classified %d ticks", len(result.Classifications))
	}
}

// The unreachable classifier is a NO-ANSWER the run can read, not an error it
// must survive: routing falls back to the start policy, and the run continues.
func TestAnUnreachableClassifierIsANoAnswerNotAnError(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := httpServer.URL
	httpServer.Close() // the address now refuses connections

	client := New(Config{APIBase: base, APIKey: "test-key"}, httpServer.Client())
	result, err := client.Classify(context.Background(), ticks(3))
	if err != nil {
		t.Fatalf("an unreachable classifier returned an error (%v), which stops a run; it must be a no-answer instead", err)
	}
	if result.Unavailable == "" {
		t.Fatalf("an unreachable classifier produced %+v: the no-answer must say it could not be reached", result)
	}
	if !strings.Contains(result.Unavailable, "reach") {
		t.Errorf("the no-answer reason %q does not say the classifier could not be reached", result.Unavailable)
	}
	if len(result.Classifications) != 0 {
		t.Errorf("an unreachable classifier nonetheless classified %d ticks", len(result.Classifications))
	}
}

// A refused call (5xx, and by the same path a 4xx) is the same no-answer, with
// the status in the reason: distinguishable from both an answer and silence.
func TestARefusedCallIsANoAnswerNamingTheStatus(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return http.StatusInternalServerError, map[string]any{"error": "quota exceeded"}
	})
	result, err := client.Classify(context.Background(), ticks(2))
	if err != nil {
		t.Fatalf("a refused call returned an error (%v), which stops a run; it must be a no-answer instead", err)
	}
	if result.Unavailable == "" || !strings.Contains(result.Unavailable, "500") {
		t.Fatalf("a refused call produced %+v: the no-answer must name the status", result)
	}
}

// A response that is not the shape the notes pinned — including the exact
// wrong nesting a naive reader produces — is a no-answer too, never a silent
// zero-classifications success.
func TestTheNestingTrapIsANoAnswer(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   any
	}{
		{"the body has answers at the wrong level", http.StatusOK,
			map[string]any{"result": map[string]any{"state": "…", "answers": []any{}}}},
		{"the body has no inner result", http.StatusOK,
			map[string]any{"result": map[string]any{"state": "…"}}},
		{"the body is an empty object", http.StatusOK, map[string]any{}},
		{"the body is not JSON at all", http.StatusOK, "not json"},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			_, client := serve(t, func(body []byte) (int, any) {
				return one.status, one.body
			})
			result, err := client.Classify(context.Background(), ticks(1))
			if err != nil {
				t.Fatalf("a malformed response returned an error (%v), which stops a run; it must be a no-answer instead", err)
			}
			if result.Unavailable == "" {
				t.Fatalf("a malformed response produced %+v: reading zero answers must be a no-answer, not a success", result)
			}
			if len(result.Classifications) != 0 {
				t.Errorf("a malformed response nonetheless classified %d ticks", len(result.Classifications))
			}
		})
	}
}

// The answers list may come back keyed by question id instead of as a list
// carrying ids; both are the same answers, so both decode.
func TestAnswersKeyedByIdAlsoDecode(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody(map[string]any{
			"abc": map[string]any{"choice": "diagnosis", "confidence": 0.61,
				"probabilities": fullProbabilities("diagnosis")},
		})
	})
	result, err := client.Classify(context.Background(), []Tick{{ID: "abc", Title: "A cause unknown"}})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	classification, ok := result.Classifications["abc"]
	if !ok {
		t.Fatalf("a keyed-answers response classified nothing (result %+v)", result)
	}
	if classification.Choice != runconfig.WorkDiagnosis {
		t.Errorf("choice = %q, want diagnosis", classification.Choice)
	}
}

// A work type off the enum in an answer is a protocol change, not a
// judgement call: that tick gets no answer, says why, and the rest of the
// batch survives it.
func TestAnAnswerOffTheEnumIsNoAnswerForThatTickOnly(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{
			answer(t, "good", "mechanical", 0.9, fullProbabilities("mechanical")),
			answer(t, "odd", "polish", 0.9, map[string]float64{"polish": 0.9}),
		})
	})
	result, err := client.Classify(context.Background(), []Tick{
		{ID: "good", Title: "A stated change"},
		{ID: "odd", Title: "A made-up kind of work"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if _, ok := result.Classifications["good"]; !ok {
		t.Errorf("a good answer in the batch was lost to a bad one beside it: %+v", result)
	}
	reason, ok := result.Unanswered["odd"]
	if !ok {
		t.Fatalf("the off-enum answer was dropped silently: %+v", result)
	}
	if !strings.Contains(reason, "polish") {
		t.Errorf("the no-answer reason %q does not name the offending work type", reason)
	}
}

// A missing answer for an asked tick is per-tick too: the batch survives, the
// gap is named.
func TestAMissingAnswerForOneTickIsNamed(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{
			answer(t, "present", "design", 0.98, fullProbabilities("design")),
		})
	})
	result, err := client.Classify(context.Background(), []Tick{
		{ID: "present", Title: "The shape is in question"},
		{ID: "absent", Title: "No answer came back"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if _, ok := result.Classifications["present"]; !ok {
		t.Errorf("the answered tick was not classified: %+v", result)
	}
	if _, ok := result.Unanswered["absent"]; !ok {
		t.Errorf("the unanswered tick is not named as unanswered: %+v", result)
	}
	if len(result.Unanswered) != 1 {
		t.Errorf("unanswered = %+v, want exactly the one absent tick", result.Unanswered)
	}
}

// A choice absent from an otherwise-complete answer is derived from the
// distribution, because the distribution — not the argmax — is the payload.
func TestAChoiceAbsentIsDerivedFromTheDistribution(t *testing.T) {
	_, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{
			map[string]any{"id": "abc",
				"probabilities": map[string]float64{
					"mechanical": 0.05, "translation": 0.05, "construction": 0.10,
					"diagnosis": 0.30, "design": 0.50}},
		})
	})
	result, err := client.Classify(context.Background(), []Tick{{ID: "abc", Title: "A split tick"}})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	classification, ok := result.Classifications["abc"]
	if !ok {
		t.Fatalf("abc was not classified (result %+v)", result)
	}
	if classification.Choice != runconfig.WorkDesign {
		t.Errorf("derived choice = %q, want the distribution's own maximum, design", classification.Choice)
	}
}

// Caller defects are hard errors, not no-answers: they never reach the wire.
func TestCallerDefectsAreRefusedBeforeTheWire(t *testing.T) {
	server, client := serve(t, func(body []byte) (int, any) {
		return http.StatusOK, jevBody([]any{})
	})
	if _, err := client.Classify(context.Background(), []Tick{{Title: "no id"}}); err == nil {
		t.Errorf("a tick with no id was sent to the wire rather than refused")
	}
	if _, err := client.Classify(context.Background(), []Tick{
		{ID: "dupe", Title: "once"}, {ID: "dupe", Title: "twice"},
	}); err == nil {
		t.Errorf("two ticks with one id were sent to the wire rather than refused")
	}
	server.mu.Lock()
	calls := len(server.requests)
	server.mu.Unlock()
	if calls != 0 {
		t.Errorf("a caller defect made %d HTTP calls; defects are refused before the wire", calls)
	}
}

// No API key is the absent classifier: the no-answer says so, and the run
// degrades to the start policy rather than dialling anything.
func TestAnAbsentKeyIsANoAnswerWithoutDialling(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the classifier was dialled with no API key configured")
	}))
	defer httpServer.Close()
	client := New(Config{APIBase: httpServer.URL}, httpServer.Client())
	result, err := client.Classify(context.Background(), ticks(1))
	if err != nil {
		t.Fatalf("an absent key returned an error (%v); it must be a no-answer so the run degrades", err)
	}
	if result.Unavailable == "" || !strings.Contains(result.Unavailable, "key") {
		t.Fatalf("an absent key produced %+v: the no-answer must say no key is configured", result)
	}
}

// The criteria are the thing that was measured: every work type on the enum
// carries a what, a not_for (which pins the contentious boundaries) and
// examples from this repository. Paraphrasing them is how a measured
// distribution quietly becomes an unmeasured one.
func TestCriteriaCoverTheEnumWithWhatNotForAndExamples(t *testing.T) {
	if len(workTypeCriteria) != len(runconfig.WorkTypeNames) {
		t.Fatalf("%d criteria entries for %d work types", len(workTypeCriteria), len(runconfig.WorkTypeNames))
	}
	for _, one := range runconfig.WorkTypeNames {
		criterion, ok := workTypeCriteria[one]
		if !ok {
			t.Fatalf("work type %q has no criteria entry", one)
		}
		if criterion.What == "" {
			t.Errorf("%q has no what: the criteria are the classifier's question", one)
		}
		if criterion.NotFor == "" {
			t.Errorf("%q has no not_for: a negative example pins a boundary better than another positive one", one)
		}
		if len(criterion.Examples) == 0 {
			t.Errorf("%q has no examples: the measured criteria were grounded in this repository's ticks", one)
		}
	}
}

// The request each tick's question carries: the state holds every asked
// tick's title, description and acceptance criteria — the input the
// measurement used — and the question names its tick by id.
func TestTheRequestCarriesTitleDescriptionAndAcceptanceCriteria(t *testing.T) {
	request, _, err := buildRequest([]Tick{{
		ID:                 "8xd",
		Title:              "Read the whole tracker from one repo tarball",
		Description:        "A streaming tar reader to a stated spec.",
		AcceptanceCriteria: "The reader streams; acceptance criteria named here.",
	}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	for _, text := range []string{
		"Read the whole tracker from one repo tarball",
		"A streaming tar reader to a stated spec.",
		"The reader streams; acceptance criteria named here.",
	} {
		if !strings.Contains(request.State, text) {
			t.Errorf("the shared state does not carry %q", text)
		}
	}
	if len(request.Questions) != 1 || request.Questions[0].ID != "8xd" {
		t.Fatalf("the request is not one question keyed by tick id: %+v", request.Questions)
	}
	if request.Questions[0].Type != "choice" {
		t.Errorf("question type = %q, want the Choice primitive", request.Questions[0].Type)
	}
}
