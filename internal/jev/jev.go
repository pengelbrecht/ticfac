// Package jev asks Jev, the TypeSafe AI "System One" model closed-enum
// questions — text in, typed probabilistic decisions out.
//
// WHAT THE CORE IS, since tick bse generalised it (absorbing wne's finding
// dc02fb31: "internal/jev only knows the work-type Choice"): a Choice question
// over ANY closed enum — the caller's labels, the caller's criteria, one
// question per thing asked, one call per batch, and every answer validated
// against its own question's enum. The package was born with one question —
// which WORK TYPE a tick is (tick 0ju, epic wne), over
// [runconfig.WorkTypeNames] — and that remains its first user; the second is
// the gating prediction (internal/gating, tick bse, epic gvc), which asks a
// Choice over a run's acceptance items plus 'none'. The work-type call is
// [Client.Classify]; any other closed enum goes through [Client.Ask]. Neither
// user's vocabulary lives here: this package knows the WIRE, and each question
// carries its own enum in.
//
// What Jev is asked, and what it is never asked. The work-type question is
// asked which kind of WORK a tick is, from its closed enum plus the full
// probability distribution, and it is never asked for a model or a tier: the
// classifier judges the tick, the operator's table judges the cost, and
// keeping those apart is what lets a model-lineup change happen without
// invalidating a single recorded classification.
//
// The precondition, which is not an optimisation: A TICK CARRYING A ROLE IS
// NEVER CLASSIFIED. [roles.*] already maps review and closeout to a model; the
// measurement put three role ticks through the classifier and got mechanical
// 0.32, diagnosis 0.61 and design 0.99 for the same kind of work, because the
// enum has no right answer for "read a diff and judge it". The classifier is
// for implementation ticks.
//
// ONE CALL PER BATCH. Questions in a single API call are evaluated in
// parallel and in isolation against the same state, so a batch of questions
// costs one round trip — one Choice question per thing asked, keyed by its id,
// against a state carrying the material every question is judged against. The
// isolation is what makes this safe rather than merely cheap: question A's
// answer cannot drag question B's.
//
// A NO-ANSWER IS NOT AN ERROR. An unreachable, refused or malformed
// classifier — and one that was never configured at all — yields
// [AnswerResult.Unavailable] or [Result.Unavailable] with the reason in it and
// no error, so the run degrades to its documented fallback rather than
// stopping. Only caller defects (a batch that cannot be keyed: empty or
// duplicate ids, a question with no choices) are errors, because they never
// reach the wire.
//
// What this package is in the two epics it serves: the CALL itself. The
// work-type vocabulary is [internal/runconfig]'s; the work-type-to-model table
// is factory config; the acceptance items are [internal/acceptance]'s; routing
// on probability mass and the decision record on the run branch belong to the
// callers. Nothing here writes anything anywhere — the caller records what the
// result carries, and it carries the full distribution on purpose, because
// decisions spend mass that an argmax would have thrown away.
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

// Tick is the work-type question's input: the tick's title, description and
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

// Answer is one question's answer as the general core reads it: the choice on
// the question's own closed enum (as answered, or derived from the
// distribution when the answer carried none), the confidence — computed by the
// API from the shape of the distribution, so a marginal answer is visibly
// marginal rather than silently decided — and the full distribution, because
// decisions downstream spend mass that an argmax would have thrown away.
type Answer struct {
	// Choice is a label from the question's own closed enum.
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
}

// AnswerResult is what one general round trip's single call produced.
//
// Unavailable, when non-empty, is why no answer exists: the classifier could
// not be reached, refused the call, answered a shape this reader does not
// recognise, or was never configured. It is a NO-ANSWER, not an error — every
// caller degrades to its own documented fallback — and Answers and Unanswered
// are empty when it is set, because an unavailable classifier answered
// nothing.
//
// Unanswered names, per question, why an asked question got no valid answer
// when the call itself succeeded: no answer came back, or an answer named a
// label off that question's own closed enum. A batch survives its unanswered
// questions — the questions were asked in isolation — and each gap is named
// rather than defaulted.
type AnswerResult struct {
	Answers    map[string]Answer
	Unanswered map[string]string
	// Model is the answering model's identity, for the decision record: a
	// restart re-reads what it says rather than re-asking a model it already
	// paid.
	Model string
	Usage Usage
	// Unavailable is the distinguishable no-answer; see above.
	Unavailable string
}

// Classification is one tick's work-type answer: the full probability
// distribution over the closed work-type enum, the choice, and the confidence
// — which is computed by the API from the shape of the distribution, so a
// marginal tick is visibly marginal rather than silently decided.
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

// Result is what one batch's work-type classification round trip produced.
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

// Client asks closed-enum questions.
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

// Ask sends one round trip: the caller's state, and one Choice question per
// question — each over its OWN closed enum, which is the question's [Choices].
//
// The only errors are caller defects the wire would mis-key: a question with
// no id, two questions sharing one id, a question offering no choices, or
// choices with no label or a label used twice. Everything about the
// classifier's own reachability and answers is in the [AnswerResult]: read
// [AnswerResult.Unavailable] before [AnswerResult.Answers].
func (c *Client) Ask(ctx context.Context, state string, questions []Question) (AnswerResult, error) {
	empty := AnswerResult{Answers: map[string]Answer{}, Unanswered: map[string]string{}}
	built, askedIDs, err := buildAsk(state, questions)
	if err != nil {
		return empty, err
	}
	return c.roundTrip(ctx, built, askedIDs)
}

// Classify asks Jev, in ONE round trip, which work type each role-less tick in
// the batch is — the work-type question as it was measured, over
// [runconfig.WorkTypeNames]. Role-carrying ticks are never sent — not as
// questions and not in the shared state — and a batch of only role ticks makes
// no call at all.
//
// The only errors are caller defects the wire would mis-key: an empty tick
// id, a duplicate id. Everything about the classifier's own reachability and
// answers is in the [Result]: read [Result.Unavailable] before
// [Result.Classifications].
func (c *Client) Classify(ctx context.Context, ticks []Tick) (Result, error) {
	built, askedIDs, err := buildRequest(ticks)
	if err != nil {
		return Result{Classifications: map[string]Classification{}, Unanswered: map[string]string{}}, err
	}
	answers, err := c.roundTrip(ctx, built, askedIDs)
	if err != nil {
		return Result{Classifications: map[string]Classification{}, Unanswered: map[string]string{}}, err
	}
	if answers.Unavailable != "" {
		return Result{Unavailable: answers.Unavailable}, nil
	}

	result := Result{
		Classifications: map[string]Classification{},
		Unanswered:      map[string]string{},
		Model:           answers.Model,
		Usage:           answers.Usage,
	}
	for _, id := range askedIDs {
		if reason, gap := answers.Unanswered[id]; gap {
			result.Unanswered[id] = reason
			continue
		}
		one, ok := answers.Answers[id]
		if !ok {
			// The core answers every asked question or names its gap, so
			// reaching here is a core violation rather than an answer; it is
			// named like a gap rather than defaulted, and the rest of the
			// batch survives it.
			result.Unanswered[id] = "the classifier answered neither an answer nor a named gap for this question"
			continue
		}
		probabilities := make(map[runconfig.WorkType]float64, len(one.Probabilities))
		for label, mass := range one.Probabilities {
			// The answer's labels were validated against this question's
			// criteria, which ARE the work-type enum, so the cast is safe.
			probabilities[runconfig.WorkType(label)] = mass
		}
		result.Classifications[id] = Classification{
			TickID:        id,
			Choice:        runconfig.WorkType(one.Choice),
			Confidence:    one.Confidence,
			Probabilities: probabilities,
		}
	}
	return result, nil
}

// roundTrip is the one shared exchange: send the built request, read the
// response, and assemble the answers against each question's own enum. Asking
// nothing is not failing to reach, and an absent key is the documented
// degradation — both are no-answers, never errors.
func (c *Client) roundTrip(ctx context.Context, built request, askedIDs []string) (AnswerResult, error) {
	empty := AnswerResult{Answers: map[string]Answer{}, Unanswered: map[string]string{}}
	if len(askedIDs) == 0 {
		// Nothing was asked — an empty batch. No call, no unavailable:
		// asking nothing is not failing to reach, and needs no key either.
		return empty, nil
	}
	if strings.TrimSpace(c.config.APIKey) == "" {
		empty.Unavailable = "no API key is configured for the classifier: no question is answered, and every decision that would have consumed an answer falls back to its documented degradation"
		return empty, nil
	}

	body, unavailable := c.post(ctx, built)
	if unavailable != "" {
		empty.Unavailable = unavailable
		return empty, nil
	}

	rawAnswers, model, usage, unavailable := parseResponse(body)
	if unavailable != "" {
		empty.Unavailable = unavailable
		return empty, nil
	}
	return assemble(built.Questions, askedIDs, rawAnswers, model, usage), nil
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
