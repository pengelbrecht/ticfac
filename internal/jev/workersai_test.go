package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// JEV ON WORKERS AI (tick tum). The operator has no TypeSafe key; Jev is
// served by Cloudflare Workers AI as typesafe/jev and billed to the operator's
// Cloudflare account. These tests stand a fake Workers AI up and pin the
// whole call as it was verified live on 2026-09-28:
//
//	POST <REST root>/accounts/<account>/ai/run
//	Authorization: Bearer <Cloudflare API token>
//	{"model": "typesafe/jev", "input": {"state": "…", "questions": {
//	    "<id>": {"type": "choice", "instructions": "…", "criteria": {…}}}}}
//
// answered by
//
//	{"result": {"state": "Completed", "result": {"model": "jev-1.13.0",
//	  "answers": {"<id>": {"type": "choice", "choice": "…", "probabilities":
//	  {…}, "confidence": 1}}, "usage": {…}}, "gatewayMetadata": {…}},
//	 "success": true}

// fakeWorkersAI is Workers AI's run endpoint: it records the path, the bearer
// and the raw body, and answers every question in the run body over its own
// criteria, in the verified envelope.
type fakeWorkersAI struct {
	mu      sync.Mutex
	server  *httptest.Server
	paths   []string
	bearers []string
	bodies  [][]byte
	// refuse, when set, is the whole response instead of an answer.
	refuse func(w http.ResponseWriter)
}

func newFakeWorkersAI(t *testing.T) *fakeWorkersAI {
	t.Helper()
	fake := &fakeWorkersAI{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.paths = append(fake.paths, r.URL.Path)
		fake.bearers = append(fake.bearers, r.Header.Get("Authorization"))
		fake.bodies = append(fake.bodies, body)
		refuse := fake.refuse
		fake.mu.Unlock()
		if refuse != nil {
			refuse(w)
			return
		}
		var run struct {
			Model string `json:"model"`
			Input struct {
				Questions map[string]struct {
					Criteria map[string]json.RawMessage `json:"criteria"`
				} `json:"questions"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &run); err != nil || run.Model != "typesafe/jev" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false,
				"errors": []any{map[string]any{"code": 5006, "message": "bad input"}}})
			return
		}
		answers := map[string]any{}
		for id, question := range run.Input.Questions {
			labels := make([]string, 0, len(question.Criteria))
			for label := range question.Criteria {
				labels = append(labels, label)
			}
			sort.Strings(labels)
			probabilities := map[string]float64{}
			for i, label := range labels {
				probabilities[label] = 0
				if i == len(labels)-1 {
					probabilities[label] = 1
				}
			}
			answers[id] = map[string]any{"type": "choice", "choice": labels[len(labels)-1],
				"probabilities": probabilities, "confidence": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"state": "Completed",
				"result": map[string]any{
					"model":   "jev-1.13.0",
					"answers": answers,
					"usage":   map[string]any{"input_tokens": 212, "output_tokens": 0},
				},
				"gatewayMetadata": map[string]any{"keySource": "Unified"},
			},
			"success": true,
		})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeWorkersAI) calls() ([]string, []string, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.paths...), append([]string{}, f.bearers...), append([][]byte{}, f.bodies...)
}

// A LOCAL classification is one Workers AI run on the operator's account,
// presenting the stored Cloudflare API token, with the body in the model's
// input schema: the model named, the questions an object keyed by tick id, and
// every question EXACTLY {type, instructions, criteria} — the schema refuses
// any other property, so an id inside a question is a 400, not an answer.
func TestALocalClassificationIsAWorkersAIRunOnTheOperatorsAccount(t *testing.T) {
	t.Parallel()
	fake := newFakeWorkersAI(t)
	source := ResolveCredential(lookupOf(map[string]string{OperatorBaseEnv: fake.server.URL}), storedCredential)
	if !source.Configured {
		t.Fatalf("the stored credential resolved no classifier: %s", source.Note)
	}
	result, err := New(source.Config, fake.server.Client()).Classify(context.Background(), ticks(2))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if result.Unavailable != "" {
		t.Fatalf("the Workers AI answer was a no-answer: %s", result.Unavailable)
	}

	paths, bearers, bodies := fake.calls()
	if len(paths) != 1 {
		t.Fatalf("%d calls for one batch, want one run", len(paths))
	}
	if paths[0] != "/accounts/account-placeholder/ai/run" {
		t.Errorf("the run went to %q, want Workers AI's /accounts/<account>/ai/run", paths[0])
	}
	if bearers[0] != "Bearer cf-token-placeholder" {
		t.Errorf("the run presented %q, want the operator's Cloudflare API token", bearers[0])
	}
	var run map[string]json.RawMessage
	if err := json.Unmarshal(bodies[0], &run); err != nil {
		t.Fatalf("the run body is not JSON: %v", err)
	}
	if string(run["model"]) != `"typesafe/jev"` {
		t.Errorf("the run names model %s, want typesafe/jev", run["model"])
	}
	var input struct {
		State     string                                `json:"state"`
		Questions map[string]map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(run["input"], &input); err != nil {
		t.Fatalf("the run's input is not {state, questions-object}: %v\n%s", err, run["input"])
	}
	if !strings.Contains(input.State, "tick t00") {
		t.Errorf("the state does not carry the ticks: %q", input.State)
	}
	if len(input.Questions) != 2 || input.Questions["t00"] == nil || input.Questions["t01"] == nil {
		t.Fatalf("the questions are %v, want an object keyed t00 and t01", input.Questions)
	}
	for id, question := range input.Questions {
		keys := make([]string, 0, len(question))
		for key := range question {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "criteria,instructions,type" {
			t.Errorf("question %s carries %v, want exactly criteria, instructions and type", id, keys)
		}
		if string(question["type"]) != `"choice"` {
			t.Errorf("question %s is type %s, want choice", id, question["type"])
		}
	}

	// And the verified envelope decodes: result.result.answers, keyed by id.
	if result.Model != "jev-1.13.0" || result.Usage.InputTokens != 212 {
		t.Errorf("the answer's model and usage read %q and %+v", result.Model, result.Usage)
	}
	for _, id := range []string{"t00", "t01"} {
		one, ok := result.Classifications[id]
		if !ok {
			t.Errorf("%s was not classified: %+v", id, result)
			continue
		}
		if one.Choice == "" || one.Confidence != 1 || len(one.Probabilities) != 5 {
			t.Errorf("%s classified as %+v, want a choice at confidence 1 over the five work types", id, one)
		}
	}
}

// A CLOUD classification rides the factory's gateway route at /jev/ai/run with
// the run token: the Worker knows its own account, so the client names none,
// and the body is the same Workers AI run body.
func TestACloudClassificationRidesTheGatewayRouteToWorkersAI(t *testing.T) {
	t.Parallel()
	fake := newFakeWorkersAI(t)
	source := ResolveCredential(lookupOf(map[string]string{
		GatewayBaseEnv:  fake.server.URL + "/api/gateway",
		GatewayTokenEnv: "tkr_run-scoped",
	}), storedCredential)
	result, err := New(source.Config, fake.server.Client()).Classify(context.Background(), ticks(1))
	if err != nil || result.Unavailable != "" {
		t.Fatalf("the gateway route's answer: %v %s", err, result.Unavailable)
	}
	paths, bearers, bodies := fake.calls()
	if len(paths) != 1 || paths[0] != "/api/gateway/jev/ai/run" {
		t.Fatalf("the classifier went to %v, want the gateway route at /api/gateway/jev/ai/run", paths)
	}
	if bearers[0] != "Bearer tkr_run-scoped" {
		t.Errorf("the route was presented %q, want the run token", bearers[0])
	}
	if !strings.Contains(string(bodies[0]), `"model":"typesafe/jev"`) {
		t.Errorf("the route's body is not a Workers AI run body: %s", bodies[0])
	}
	if _, ok := result.Classifications["t00"]; !ok {
		t.Errorf("t00 was not classified through the route: %+v", result)
	}
}

// Cloudflare's own refusal — the {"success": false, "errors": […]} envelope,
// on a 4xx or even a 200 — is a no-answer naming Cloudflare's reason, never a
// silent zero-classification success.
func TestAWorkersAIRefusalIsANoAnswerInCloudflaresWords(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]int{"a 403": http.StatusForbidden, "a 200": http.StatusOK} {
		fake := newFakeWorkersAI(t)
		fake.refuse = func(w http.ResponseWriter) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "result": nil,
				"errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
		}
		client := New(Config{APIBase: fake.server.URL, AccountID: "account-placeholder", APIKey: "cf-token-placeholder"}, fake.server.Client())
		result, err := client.Classify(context.Background(), ticks(1))
		if err != nil {
			t.Fatalf("%s: a refusal returned an error (%v); it must be a no-answer", name, err)
		}
		if !strings.Contains(result.Unavailable, "Authentication error") || len(result.Classifications) != 0 {
			t.Errorf("%s: the refusal produced %+v, want a no-answer naming Cloudflare's reason", name, result)
		}
	}
}

// The default REST root with no account is not a call to make: Workers AI
// runs under an account, and a URL without one is a 404 dressed as an outage.
func TestNoAccountOnTheDefaultRootIsANoAnswerWithoutDialling(t *testing.T) {
	t.Parallel()
	client := New(Config{APIKey: "cf-token-placeholder"}, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("the classifier was dialled with no account")
		return nil, io.EOF
	})})
	result, err := client.Classify(context.Background(), ticks(1))
	if err != nil || !strings.Contains(result.Unavailable, "account") {
		t.Fatalf("no account produced %v %+v, want a no-answer naming the account", err, result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The probe `ticfac doctor` and `ticfac factory status` run: one tiny
// question, and a sentence naming the model when Jev answers — or the run's
// own no-answer reason as an error when it does not.
func TestTheProbeSaysWhetherJevAnswers(t *testing.T) {
	t.Parallel()
	fake := newFakeWorkersAI(t)
	client := New(Config{APIBase: fake.server.URL, AccountID: "account-placeholder", APIKey: "cf-token-placeholder"}, fake.server.Client())
	detail, err := client.Probe(context.Background())
	if err != nil {
		t.Fatalf("the probe against an answering Workers AI failed: %v", err)
	}
	if !strings.Contains(detail, "jev-1.13.0") || !strings.Contains(detail, "Workers AI") {
		t.Errorf("the probe's detail does not name the answering model: %q", detail)
	}
	if paths, _, _ := fake.calls(); len(paths) != 1 {
		t.Errorf("the probe made %d calls, want one", len(paths))
	}

	fake.refuse = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false,
			"errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
	}
	if _, err := client.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("the probe against a refusing Workers AI answered %v, want the refusal", err)
	}
}
