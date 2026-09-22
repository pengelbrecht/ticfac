package jev

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The wire contract, as the epic's measurement proved it on 2026-09-22. Two
// details cost real time to find, and both are pinned by the tests:
//
//   - The response nests ONE DEEPER than the model page shows:
//     result -> {state, result: {model, answers, usage}}. Reading
//     result.answers gets zero answers and no error.
//   - The response carries the FULL PROBABILITY MAP, not just a winner:
//     {choice, confidence, probabilities: {…}}. Confidence is computed from
//     the shape of that distribution, so a marginal tick is VISIBLY marginal
//     rather than silently decided.
//
// One request is a STATE (every asked tick's own text) plus one Choice
// question per tick, keyed by tick id. The API documents that questions in
// one call are "evaluated in parallel and in isolation against the same
// state" — the isolation is what makes one round trip for a whole epic safe
// rather than merely cheap, because tick A's classification cannot drag
// tick B's.

// criterion is one work type's entry in a Choice question's criteria: the
// object shape the docs allow — what, not_for, examples. 'not_for' earns its
// place: construction-vs-diagnosis is the contentious boundary, and a
// negative example pins a boundary better than another positive one.
type criterion struct {
	What     string   `json:"what"`
	NotFor   string   `json:"not_for"`
	Examples []string `json:"examples"`
}

// workTypeCriteria is the criteria sent with every Choice question, in the
// measured shape — an object per work type with what, not_for and examples
// — carrying the enum's recorded definitions and this repository's own
// boundary cases as examples (8xd construction against qsn diagnosis, and
// so on). The exact prose of the 49-tick measurement is not recorded in the
// tracker, so this is the reconstruction from the recorded shape and
// definitions, not a copy; a test refuses a criteria table that does not
// cover exactly [runconfig.WorkTypeNames].
var workTypeCriteria = map[runconfig.WorkType]criterion{
	runconfig.WorkMechanical: {
		What: "The change is stated, not decided. Done is knowable by construction: the tick already names the exact edit, and carrying it out is applying it.",
		NotFor: "Work where anything must be worked out first, however small the edit. A one-line change whose shape is not given is not mechanical; " +
			"size is not the axis, and neither is risk.",
		Examples: []string{
			"Delete hello.txt and goodbye.txt (measured: nine tool calls, no decisions).",
		},
	},
	runconfig.WorkTranslation: {
		What:   "A known pattern applied to new material. Done is knowable by comparison with the pattern's existing instances.",
		NotFor: "Work that establishes the pattern rather than applies it, or whose pattern the repository does not already contain.",
		Examples: []string{
			"Add a guard test in the shape of the existing `serial:` guard.",
			"Port a component to a new host once the first port exists (measured at 0.95/0.92 confidence).",
		},
	},
	runconfig.WorkConstruction: {
		What:   "Build to a stated spec; decisions are local. Done is the acceptance criteria.",
		NotFor: "Work where the cause is unknown (that is diagnosis) or the shape itself is in question (that is design). A stated spec is the line: the tick says what to build, not merely what is wrong.",
		Examples: []string{
			"A streaming tar reader to a stated spec (8xd): the shape was stated, the work was building it.",
			"Put the ticfac binaries in the orchestrator image (prs).",
		},
	},
	runconfig.WorkDiagnosis: {
		What: "Cause unknown. Form and discard hypotheses against evidence. Done is knowable only once the cause is found.",
		NotFor: "A defect filed after its cause was found: its text names the fix, so the remaining work is mechanical or construction. " +
			"Real diagnosis work is filed from a symptom, not from a fix.",
		Examples: []string{
			"A detached `git maintenance` child corrupting the NEXT test's fixture (qsn): two wrong hypotheses before the cause, found with GIT_TRACE2_EVENT.",
			"The tracker and the gate both naming their worktree `tree` (m78).",
		},
	},
	runconfig.WorkDesign: {
		What:   "The shape itself is in question, and the tick's own framing may be wrong. Done is knowable only by argument.",
		NotFor: "Work that builds to a settled shape, however large or consequential. The mark of design is that the question is what to build, not how.",
		Examples: []string{
			"Decide where the orchestrator runs (yoh).",
			"Decide how a tick's first-attempt model is chosen (wne).",
		},
	},
}

// question is one Choice question in a request: the tick it asks about, keyed
// by tick id, against the shared state the request carries.
type question struct {
	ID           string                           `json:"id"`
	Type         string                           `json:"type"`
	Instructions string                           `json:"instructions"`
	Criteria     map[runconfig.WorkType]criterion `json:"criteria"`
}

// request is one classifier round trip: the state is every asked tick's own
// text, and the questions are one per tick.
type request struct {
	State     string     `json:"state"`
	Questions []question `json:"questions"`
}

// stateText is the shared state: each asked tick's title, description and
// acceptance criteria — the input the measurement used — under its id.
func stateText(ticks []Tick) string {
	var builder strings.Builder
	for i, one := range ticks {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		fmt.Fprintf(&builder, "tick %s\n", one.ID)
		if strings.TrimSpace(one.Title) != "" {
			fmt.Fprintf(&builder, "title: %s\n", strings.TrimSpace(one.Title))
		}
		if strings.TrimSpace(one.Description) != "" {
			fmt.Fprintf(&builder, "description: %s\n", strings.TrimSpace(one.Description))
		}
		if strings.TrimSpace(one.AcceptanceCriteria) != "" {
			fmt.Fprintf(&builder, "acceptance criteria: %s\n", strings.TrimSpace(one.AcceptanceCriteria))
		}
	}
	return builder.String()
}

// buildRequest assembles the one round trip for a batch. It returns the
// request and the ids of the ticks actually asked, which is every tick that
// does NOT carry a role: a role-carrying tick is never classified — the enum
// has no right answer for "read a diff and judge it", and [roles.*] already
// maps review and closeout to a model, so asking as well buys a worse answer
// from a menu that has no right option on it. This is a precondition, not an
// optimisation.
func buildRequest(ticks []Tick) (request, []string, error) {
	asked := make([]Tick, 0, len(ticks))
	seen := make(map[string]bool, len(ticks))
	for _, one := range ticks {
		if strings.TrimSpace(one.Role) != "" {
			continue
		}
		if strings.TrimSpace(one.ID) == "" {
			return request{}, nil, fmt.Errorf(
				"a role-less tick with no id cannot be keyed into the batch, so the classifier cannot ask about it")
		}
		if seen[one.ID] {
			return request{}, nil, fmt.Errorf(
				"tick %s appears twice in the batch: one Choice question is keyed by tick id, so a duplicate would answer as one tick",
				one.ID)
		}
		seen[one.ID] = true
		asked = append(asked, one)
	}

	built := request{State: stateText(asked), Questions: make([]question, 0, len(asked))}
	ids := make([]string, 0, len(asked))
	for _, one := range asked {
		built.Questions = append(built.Questions, question{
			ID:   one.ID,
			Type: "choice",
			Instructions: fmt.Sprintf(
				"What kind of work is tick %s? Judge only that tick's own text above, on the axis of how much of the work is deciding what to do rather than doing it.",
				one.ID),
			Criteria: workTypeCriteria,
		})
		ids = append(ids, one.ID)
	}
	return built, ids, nil
}

// wireAnswer is one answer as the API returns it. The choice is the argmax
// and confidence is computed from the shape of the distribution; the
// distribution is the payload the next stage routes on, and an argmax that
// throws it away cannot be recovered.
type wireAnswer struct {
	ID            string             `json:"id"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// answerSet tolerates both shapes the answers field arrives in: a list
// carrying its question ids, and an object keyed by question id. Which one
// the wire uses is not worth pinning a caller to.
type answerSet map[string]wireAnswer

func (set *answerSet) UnmarshalJSON(body []byte) error {
	var list []wireAnswer
	if err := json.Unmarshal(body, &list); err == nil {
		*set = make(answerSet, len(list))
		for _, one := range list {
			(*set)[one.ID] = one
		}
		return nil
	}
	var keyed map[string]wireAnswer
	if err := json.Unmarshal(body, &keyed); err != nil {
		return fmt.Errorf("the answers are neither a list of keyed questions nor an object keyed by question id")
	}
	*set = make(answerSet, len(keyed))
	for id, one := range keyed {
		one.ID = id
		(*set)[id] = one
	}
	return nil
}

// responseEnvelope is the response, one level deeper than the model page
// shows: result -> {state, result: {model, answers, usage}}. A reader at
// result.answers finds zero answers and no error, which is exactly the trap
// this shape exists to make loud.
type responseEnvelope struct {
	Result *struct {
		State  json.RawMessage `json:"state"`
		Result *struct {
			Model   string    `json:"model"`
			Answers answerSet `json:"answers"`
			Usage   Usage     `json:"usage"`
		} `json:"result"`
	} `json:"result"`
}

// parseResponse decodes one response's payload. Every failure is a
// no-answer reason rather than an error, because a malformed, empty-nested
// or missing response is a fact about the classifier that the run degrades
// around, not a reason to stop the run.
func parseResponse(body []byte) (answers answerSet, model string, usage Usage, unavailable string) {
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, "", Usage{}, fmt.Sprintf("the classifier's response did not decode as JSON: %v", err)
	}
	if envelope.Result == nil || envelope.Result.Result == nil {
		return nil, "", Usage{}, "the response carries no result.result.answers: Jev answers nest one level deeper than the model page shows " +
			"(result -> {state, result: {model, answers, usage}}), and a read at result.answers finds zero answers and no error"
	}
	return envelope.Result.Result.Answers, envelope.Result.Result.Model, envelope.Result.Result.Usage, ""
}

// assemble turns one response's answers into the batch's result: one
// classification per answered tick, one named reason per asked tick that
// got no valid answer. A work type off the enum in an answer is a protocol
// change, not a judgement call, so that tick gets no answer and says why —
// and the rest of the batch survives it, because questions were asked in
// isolation.
func assemble(asked []string, answers answerSet, model string, usage Usage) Result {
	result := Result{
		Classifications: map[string]Classification{},
		Unanswered:      map[string]string{},
		Model:           model,
		Usage:           usage,
	}
	for _, id := range asked {
		one, ok := answers[id]
		if !ok {
			result.Unanswered[id] = "no answer came back for this tick"
			continue
		}
		probabilities := make(map[runconfig.WorkType]float64, len(one.Probabilities))
		offEnum := make([]string, 0)
		for name, mass := range one.Probabilities {
			if !runconfig.IsKnownWorkType(name) {
				offEnum = append(offEnum, name)
				continue
			}
			probabilities[runconfig.WorkType(name)] = mass
		}
		if len(offEnum) > 0 {
			result.Unanswered[id] = fmt.Sprintf(
				"the answer put probability on %s, which is not a work type on the closed enum: the enum is the question, so this is a protocol change, not a judgement call",
				quoteList(offEnum))
			continue
		}
		if len(probabilities) == 0 {
			result.Unanswered[id] = "the answer carries no probability distribution, and the distribution is what the next stage routes on"
			continue
		}
		choice := runconfig.WorkType(one.Choice)
		if one.Choice == "" {
			choice = argmax(probabilities)
		} else if !runconfig.IsKnownWorkType(one.Choice) {
			result.Unanswered[id] = fmt.Sprintf(
				"the answer chose %q, which is not a work type on the closed enum: the enum is the question, so this is a protocol change, not a judgement call",
				one.Choice)
			continue
		}
		result.Classifications[id] = Classification{
			TickID:        id,
			Choice:        choice,
			Confidence:    one.Confidence,
			Probabilities: probabilities,
		}
	}
	return result
}

// argmax is the distribution's own maximum, used only when the answer carried
// no choice. The distribution, not the argmax, is the payload — routing spends
// the mass — so the choice here is a label for reading, never a decision.
func argmax(probabilities map[runconfig.WorkType]float64) runconfig.WorkType {
	var best runconfig.WorkType
	var bestMass = -1.0
	// Walk the enum in order, so a tie lands on the cheaper work type
	// deterministically rather than on whatever the map handed over first.
	for _, one := range runconfig.WorkTypeNames {
		mass, ok := probabilities[one]
		if !ok {
			continue
		}
		if mass > bestMass {
			best, bestMass = one, mass
		}
	}
	return best
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, one := range values {
		quoted = append(quoted, fmt.Sprintf("%q", one))
	}
	return strings.Join(quoted, ", ")
}
