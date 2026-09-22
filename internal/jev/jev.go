// Package jev classifies a tick's KIND OF WORK with Jev, the TypeSafe AI
// "System One" model — text in, typed probabilistic decisions out.
//
// What Jev is asked, and what it is never asked. It is asked which WORK TYPE
// a tick is, from a closed enum ([runconfig.WorkTypeNames]: mechanical,
// translation, construction, diagnosis, design), plus the full probability
// distribution. It is never asked for a model or a tier: the classifier
// judges the tick, the operator's table judges the cost, and keeping those
// apart is what lets a model-lineup change happen without invalidating a
// single recorded classification.
//
// The precondition, which is not an optimisation: A TICK CARRYING A ROLE IS
// NEVER CLASSIFIED. [roles.*] already maps review and closeout to a model;
// the measurement put three role ticks through the classifier and got
// mechanical 0.32, diagnosis 0.61 and design 0.99 for the same kind of work,
// because the enum has no right answer for "read a diff and judge it". The
// classifier is for implementation ticks.
//
// ONE CALL PER BATCH. Questions in a single API call are evaluated in
// parallel and in isolation against the same state, so an epic's role-less
// ticks batch into one round trip — one Choice question per tick, keyed by
// tick id, against a state carrying each tick's title, description and
// acceptance criteria. The isolation is what makes this safe rather than
// merely cheap: tick A's classification cannot drag tick B's.
//
// A NO-ANSWER IS NOT AN ERROR. An unreachable, refused or malformed
// classifier — and a classification that was never configured at all —
// yields [Result.Unavailable] with the reason in it and no error, so the run
// degrades to the start policy rather than stopping. Only caller defects
// (a batch that cannot be keyed: empty or duplicate tick ids) are errors,
// because they never reach the wire.
//
// What this package is in the wne epic: the CALL itself. The vocabulary is
// [internal/runconfig]'s; the work-type-to-model table is factory config;
// routing on probability mass and the decision record on the run branch are
// later ticks. Nothing here writes anything anywhere — the caller records
// what [Result] carries, and it carries the full distribution on purpose,
// because routing spends mass that an argmax would have thrown away.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// DefaultAPIBase is the TypeSafe AI root.
const DefaultAPIBase = "https://api.typesafe.ai"

// answersPath is the endpoint one classification round trip posts to. The
// endpoint path is the one part of the wire the epic's notes do not pin; it
// lives here alone so a correction is a one-line change.
const answersPath = "/v1/answers"

// Config is everything needed to reach the classifier.
type Config struct {
	// APIBase is the API root; empty means DefaultAPIBase.
	APIBase string
	// APIKey is the bearer token. Empty is the ABSENT classifier: a
	// no-answer without dialling, so a run without classification configured
	// degrades rather than fails.
	APIKey string
}

// Tick is the classifier's input: the tick's title, description and
// acceptance criteria — the input the measurement used — plus the fields that
// decide whether it is asked at all. It is deliberately its own shape rather
// than the tracker's, so this package reads no tracker contract.
type Tick struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria string
	// Role is the tick's process role in tracker spelling ("review",
	// "closeout"). A non-empty Role means the tick is never classified.
	Role string
}

// Classification is one tick's answer: the full probability distribution over
// the closed work-type enum, the choice, and the confidence — which is
// computed by the API from the shape of the distribution, so a marginal tick
// is visibly marginal rather than silently decided.
type Classification struct {
	TickID string
	// Choice is the argmax as answered (or derived from the distribution
	// when the answer carried none). It is a label for reading; the
	// distribution is the payload.
	Choice     runconfig.WorkType
	Confidence float64
	// Probabilities is the full distribution: one entry per work type on
	// the enum. Routing is on mass over the dear work types, and mass
	// cannot be recovered from an argmax, so nothing downstream may
	// summarise this map away.
	Probabilities map[runconfig.WorkType]float64
}

// Usage is what the call cost, for the decision record.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Result is what one batch's single round trip produced.
//
// Unavailable, when non-empty, is why no classification exists: the
// classifier could not be reached, refused the call, answered a shape this
// reader does not recognise, or was never configured. It is a NO-ANSWER, not
// an error — the run degrades to the start policy — and Classifications and
// Unanswered are empty when it is set, because an unavailable classifier
// answered nothing.
//
// Unanswered names, per tick, why an asked tick got no valid answer when the
// call itself succeeded: no answer came back, or an answer named a work type
// off the closed enum. A batch survives its unanswered ticks — the questions
// were asked in isolation — and each gap is named rather than defaulted.
type Result struct {
	Classifications map[string]Classification
	Unanswered      map[string]string
	// Model is the answering model's identity, for the decision record: a
	// restart re-reads what it says rather than re-asking a model it already
	// paid.
	Model string
	Usage Usage
	// Unavailable is the distinguishable no-answer; see above.
	Unavailable string
}

// Client classifies tick batches.
type Client struct {
	config Config
	http   *http.Client
}

// New builds a Client. A nil http client gets one with a bounded timeout —
// Jev is 40-200x faster than a frontier model, so a minute is generous for a
// whole epic's batch.
func New(config Config, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if strings.TrimSpace(config.APIBase) == "" {
		config.APIBase = DefaultAPIBase
	}
	config.APIBase = strings.TrimRight(config.APIBase, "/")
	return &Client{config: config, http: client}
}

// Classify asks Jev, in ONE round trip, which work type each role-less tick
// in the batch is. Role-carrying ticks are never sent — not as questions and
// not in the shared state — and a batch of only role ticks makes no call at
// all.
//
// The only errors are caller defects the wire would mis-key: an empty tick
// id, a duplicate id. Everything about the classifier's own reachability and
// answers is in the [Result]: read [Result.Unavailable] before
// [Result.Classifications].
func (c *Client) Classify(ctx context.Context, ticks []Tick) (Result, error) {
	empty := Result{Classifications: map[string]Classification{}, Unanswered: map[string]string{}}
	built, askedIDs, err := buildRequest(ticks)
	if err != nil {
		return empty, err
	}
	if len(askedIDs) == 0 {
		// Nothing was asked — a batch of only role ticks, or an empty batch.
		// No call, no unavailable: asking nothing is not failing to reach,
		// and needs no key either.
		return empty, nil
	}
	if strings.TrimSpace(c.config.APIKey) == "" {
		empty.Unavailable = "no API key is configured for the classifier: this run has no classification, and routing falls back to the start policy"
		return empty, nil
	}

	body, unavailable := c.post(ctx, built)
	if unavailable != "" {
		empty.Unavailable = unavailable
		return empty, nil
	}

	answers, model, usage, unavailable := parseResponse(body)
	if unavailable != "" {
		empty.Unavailable = unavailable
		return empty, nil
	}
	return assemble(askedIDs, answers, model, usage), nil
}

// post sends the one round trip. Transport failures are no-answer reasons,
// not errors: an unreachable classifier is a fact the run degrades around.
func (c *Client) post(ctx context.Context, built request) ([]byte, string) {
	encoded, err := json.Marshal(built)
	if err != nil {
		return nil, fmt.Sprintf("the classifier request could not be encoded: %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.config.APIBase+answersPath, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Sprintf("the classifier request could not be built: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Sprintf("the classifier could not be reached: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if readErr != nil {
		return nil, fmt.Sprintf("the classifier's response could not be read: %v", readErr)
	}
	if response.StatusCode >= 300 {
		// The status alone hides why: a refused key and a quota both read
		// as "no answer" otherwise.
		return nil, fmt.Sprintf("the classifier answered %s: %s",
			response.Status, strings.TrimSpace(string(body)))
	}
	return body, ""
}
