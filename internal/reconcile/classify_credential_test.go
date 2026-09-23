package reconcile

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

	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// THE CREDENTIAL SOURCES, end to end through the real client (tick x0k, epic
// wne): the exchange (w9b) and the mass routing (s45) were proved against a
// faked seam, so what this file proves is the half neither of them could — the
// REAL *jev.Client, over a real HTTP round trip, built the way run-epic builds
// it from the credential source the process found:
//
//   - a CLOUD run — the sandbox's AI_GATEWAY_BASE_URL/AI_GATEWAY_TOKEN —
//     classifies through the factory's gateway route, presenting the run token
//     the route exchanges for the deployment's key;
//   - a LOCAL run — the operator's $TICFAC_JEV_API_KEY — classifies directly,
//     on the operator's own credential;
//   - no credential, or an unreachable classifier, is the documented
//     degradation: nothing classified (or a recorded no-answer), every
//     dispatch at [tier_policy.start].
//
// These tests skip in the short suite with the rest of the harness; the
// cheap halves of the wiring — the source resolution and the route parity —
// live in internal/jev and internal/cli and run in the gate.

// jevStub is the classifier's wire, answered by a real HTTP server in the
// measured response shape: the envelope nested one level deeper than the model
// page shows, the full probability map in every answer. It records the path,
// the bearer and the request body it was handed, because those are the
// credential the caller rode, which is the thing under test.
type jevStub struct {
	mu     sync.Mutex
	server *httptest.Server
	paths  []string
	bearer []string
	bodies []string
}

// dearWireAnswer is one answered tick: a distribution whose mass on the dear
// work types (diagnosis 0.10 + design 0.45 = 0.55) clears massGate's
// provisional 0.50 threshold, so the dispatch that reads it starts dear — the
// property that tells a routed record from an unrouted one.
func dearWireAnswer(tick string) map[string]any {
	return map[string]any{
		"id":            tick,
		"choice":        "design",
		"confidence":    0.9,
		"probabilities": map[string]float64{"mechanical": 0.02, "translation": 0.03, "construction": 0.40, "diagnosis": 0.10, "design": 0.45},
	}
}

// serve stands the classifier's wire up and returns it. Every question the
// request carried is answered dear, so a whole epic's role-less ticks route
// the same way and the assertions can be about the credential, not the answer.
func serveJev(t *testing.T) *jevStub {
	t.Helper()
	stub := &jevStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the classifier request: %v", err)
			return
		}
		stub.mu.Lock()
		stub.paths = append(stub.paths, r.URL.Path)
		stub.bearer = append(stub.bearer, r.Header.Get("Authorization"))
		stub.bodies = append(stub.bodies, string(body))
		stub.mu.Unlock()

		var request struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("the classifier request is not the wire shape: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		answers := make([]map[string]any, 0, len(request.Questions))
		for _, question := range request.Questions {
			answers = append(answers, dearWireAnswer(question.ID))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"state": "ok",
				"result": map[string]any{
					"model":   "jev-1",
					"answers": answers,
					"usage":   map[string]any{"input_tokens": 1200, "output_tokens": 40, "cost_usd": 0.00005},
				},
			},
		})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *jevStub) asks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func (s *jevStub) path(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[i]
}

func (s *jevStub) bearerOf(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bearer[i]
}

// questionIDs is every tick the classifier was asked about, across every ask —
// the set that must be the role-less ticks and never a role-carrying one.
func (s *jevStub) questionIDs(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	bodies := append([]string{}, s.bodies...)
	s.mu.Unlock()
	ids := make([]string, 0, len(bodies))
	for _, raw := range bodies {
		var request struct {
			Questions []struct {
				ID string `json:"id"`
			} `json:"questions"`
		}
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Fatalf("a recorded ask is not the wire shape: %v", err)
		}
		for _, question := range request.Questions {
			ids = append(ids, question.ID)
		}
	}
	return ids
}

// classifierFrom builds the classifier run-epic would build for one
// environment: the same resolution (jev.ResolveCredential), the same client
// (jev.New). A configured source hands back a real client; an unconfigured one
// hands back nil, which is the run's documented "classifies nothing".
func classifierFrom(t *testing.T, environment map[string]string) (Classifier, string) {
	t.Helper()
	source := jev.ResolveCredential(func(name string) string { return environment[name] })
	if !source.Configured {
		return nil, source.Note
	}
	return jev.New(source.Config, nil), source.Note
}

// A LOCAL run — the operator's own key in $TICFAC_JEV_API_KEY, no gateway route
// in the environment — classifies each role-less tick through Jev over a real
// HTTP round trip, presents the OPERATOR'S key (never anything run-scoped),
// records the full distribution and the model identity on the run branch, and
// the dispatch the record routed starts at the dear tier.
func TestALocalRunClassifiesOnTheOperatorsCredential(t *testing.T) {
	t.Parallel()
	stub := serveJev(t)
	classifier, _ := classifierFrom(t, map[string]string{
		"TICFAC_JEV_API_KEY":  "operator-key",
		"TICFAC_JEV_API_BASE": stub.server.URL,
	})
	if classifier == nil {
		t.Fatal("the operator's key resolved no classifier")
	}

	f := newFixture(t, fixtureOptions{gate: massGate})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the local run ended %s: %s", result.State, result.Reason)
	}

	// Every role-less tick was asked, over the direct API root, presenting the
	// operator's key; the role ticks were never sent.
	if stub.asks() != 3 {
		t.Fatalf("the classifier was asked %d times, want once for each of a1, a2, b1", stub.asks())
	}
	asked := stub.questionIDs(t)
	sort.Strings(asked)
	if strings.Join(asked, ",") != "a1,a2,b1" {
		t.Errorf("the classifier was asked about %v, want a1, a2 and b1 once each and never the role ticks", asked)
	}
	for i := 0; i < stub.asks(); i++ {
		if stub.path(i) != "/v1/answers" {
			t.Errorf("ask %d went to %q, want the classifier's own /v1/answers", i, stub.path(i))
		}
		if stub.bearerOf(i) != "Bearer operator-key" {
			t.Errorf("ask %d presented %q, want the operator's key", i, stub.bearerOf(i))
		}
	}

	// The record is on the run branch, and routing read it: every role-less
	// dispatch started at the dear tier its 0.55 of mass bought.
	decisions := classificationRecordsOf(t, r, f)
	classified := 0
	for i := range decisions {
		if decisions[i].Role != runstate.RoleClassifyTick {
			continue
		}
		classified++
		read, err := classificationOfDecision(&decisions[i])
		if err != nil {
			t.Errorf("the classification record does not read back: %v", err)
			continue
		}
		if read.Model != "jev-1" || read.Choice != runconfig.WorkDesign {
			t.Errorf("the record says %+v", read)
		}
		if read.Probabilities[runconfig.WorkDesign] != 0.45 || read.Probabilities[runconfig.WorkDiagnosis] != 0.10 {
			t.Errorf("the record lost its distribution: %v", read.Probabilities)
		}
	}
	if classified != 3 {
		t.Fatalf("%d classification records on the run branch, want one per role-less tick", classified)
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, r, tick, 1); got != "balanced" {
			t.Errorf("%s's marker records tier %q, want balanced: the recorded 0.55 of mass clears 0.50", tick, got)
		}
	}
}

// A CLOUD run — the sandbox's AI_GATEWAY_BASE_URL pointing at the factory's
// /api/gateway prefix and AI_GATEWAY_TOKEN holding the run token — classifies
// through the GATEWAY ROUTE, at /jev, presenting the run-scoped token: the
// credential the Worker exchanges for the deployment's key, and the one a
// revocation kills classification with. The record lands and routes the same
// way the local one does.
func TestACloudRunClassifiesThroughTheGatewayRouteWithTheRunToken(t *testing.T) {
	t.Parallel()
	stub := serveJev(t)
	classifier, _ := classifierFrom(t, map[string]string{
		"AI_GATEWAY_BASE_URL": stub.server.URL + "/api/gateway",
		"AI_GATEWAY_TOKEN":    "tkr_run-scoped",
		"TICFAC_JEV_API_KEY":  "operator-key-must-not-be-used",
	})
	if classifier == nil {
		t.Fatal("the sandbox's gateway route resolved no classifier")
	}

	f := newFixture(t, fixtureOptions{gate: massGate})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the cloud run ended %s: %s", result.State, result.Reason)
	}

	// The classifier rode the gateway route at the jev slug, presenting the
	// run token — never the operator key the environment also carried.
	if stub.asks() != 3 {
		t.Fatalf("the classifier was asked %d times, want once for each role-less tick", stub.asks())
	}
	asked := stub.questionIDs(t)
	sort.Strings(asked)
	if strings.Join(asked, ",") != "a1,a2,b1" {
		t.Errorf("the classifier was asked about %v, want the role-less ticks only", asked)
	}
	for i := 0; i < stub.asks(); i++ {
		if stub.path(i) != "/api/gateway/jev/v1/answers" {
			t.Errorf("ask %d went to %q, want the gateway route at /api/gateway/jev/v1/answers", i, stub.path(i))
		}
		if stub.bearerOf(i) != "Bearer tkr_run-scoped" {
			t.Errorf("ask %d presented %q, want the run's gateway token", i, stub.bearerOf(i))
		}
	}

	// The record lands on the run branch like a local run's, and routes the
	// dispatch the same way.
	if decisions := classificationRecordsOf(t, r, f); len(decisions) != 3 {
		t.Fatalf("%d classification records on the run branch, want one per role-less tick", len(decisions))
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, r, tick, 1); got != "balanced" {
			t.Errorf("%s's marker records tier %q, want balanced: the recorded 0.55 of mass clears 0.50", tick, got)
		}
	}
}

// No credential — the documented fallback, and the SAY that has to come with
// it: the source resolution answers "not configured", the run classifies
// nothing (no record is written, and no role-less tick was ever sent), and
// every dispatch starts at [tier_policy.start]'s default.
func TestARunWithNoCredentialClassifiesNothingAndStartsAtThePolicy(t *testing.T) {
	t.Parallel()
	stub := serveJev(t)
	classifier, note := classifierFrom(t, map[string]string{})
	if classifier != nil {
		t.Fatal("an empty environment resolved a classifier")
	}
	for _, want := range []string{"no classifier credential", "[tier_policy.start]"} {
		if !strings.Contains(note, want) {
			t.Errorf("the source's note does not say %q: %q", want, note)
		}
	}

	f := newFixture(t, fixtureOptions{gate: massGate})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("a run with no credential ended %s: %s", result.State, result.Reason)
	}
	if stub.asks() != 0 {
		t.Errorf("a run with no credential asked a classifier %d times", stub.asks())
	}
	if decisions := classificationRecordsOf(t, r, f); len(decisions) != 0 {
		t.Fatalf("%d classification records on the run branch, want none", len(decisions))
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, r, tick, 1); got != "economy" {
			t.Errorf("%s's marker records tier %q, want economy: [tier_policy.start] is the fallback", tick, got)
		}
	}
}

// An UNREACHABLE classifier — the operator's key configured against a base
// that refuses connections — is a recorded no-answer, not a stop: the run
// completes, the no-answer is on the run branch where a cold re-derivation
// reads it, and every dispatch falls back to [tier_policy.start].
func TestAnUnreachableClassifierThroughTheWiredClientDegradesToTheStartPolicy(t *testing.T) {
	t.Parallel()
	// An address that now refuses connections, made the same way internal/jev's
	// own unreachable test makes one.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closed := server.URL
	server.Close() // the address now refuses connections

	classifier, _ := classifierFrom(t, map[string]string{
		"TICFAC_JEV_API_KEY":  "operator-key",
		"TICFAC_JEV_API_BASE": closed,
	})
	if classifier == nil {
		t.Fatal("the operator's key resolved no classifier")
	}

	f := newFixture(t, fixtureOptions{gate: massGate})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Classifier = classifier
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("a run with an unreachable classifier ended %s, want a completed degrade: %s", result.State, result.Reason)
	}

	// Three no-answer records — one per role-less tick — naming reachability,
	// read back through the same reader a cold re-derivation uses.
	decisions := classificationRecordsOf(t, r, f)
	if len(decisions) != 3 {
		t.Fatalf("%d classification records on the run branch, want one no-answer per role-less tick", len(decisions))
	}
	for i := range decisions {
		read, err := classificationOfDecision(&decisions[i])
		if err != nil {
			t.Fatalf("the no-answer record does not read back: %v", err)
		}
		if !strings.Contains(read.NoAnswer, "could not be reached") {
			t.Errorf("the no-answer is %q, want the unreachable classifier named", read.NoAnswer)
		}
	}
	for _, tick := range []string{"a1", "a2", "b1"} {
		if got := markerTierOfTry(t, r, tick, 1); got != "economy" {
			t.Errorf("%s's marker records tier %q, want economy: the recorded no-answer falls back to [tier_policy.start]", tick, got)
		}
	}
}

// classificationRecordsOf reads the run branch as a cold reader does — fetch,
// then the classification decision records alone — and returns them.
func classificationRecordsOf(t *testing.T, r *Reconciler, f *fixture) []runstate.Decision {
	t.Helper()
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	classifications := make([]runstate.Decision, 0, len(decisions))
	for i := range decisions {
		if decisions[i].Role == runstate.RoleClassifyTick {
			classifications = append(classifications, decisions[i])
		}
	}
	return classifications
}
