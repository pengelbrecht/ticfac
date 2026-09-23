package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// THE CLASSIFICATION EXCHANGE (tick w9b, epic wne).
//
// Jev is asked ONCE per role-less tick, and the answer is written to the run
// branch as a DECISION RECORD — the same shape, the same
// `.ticfac/runs/<run-id>/decisions/<n>.json` location, the same
// create-if-absent rule the review and closeout exchanges already use — so
// that a later pass, a restart, and a cold reconstruction from git all READ
// the record instead of re-asking a model the run already paid.
//
// This is axiom 1, not thrift, and the distinction decides the shape:
// cold reconstruction from git must reach the SAME dispatch as the warm
// process. A recorded classification does. A re-ask only probably does — Jev
// is probabilistic, and two calls on the same tick may differ, which would
// make a cold re-derivation dispatch a different model than the run it is
// reconstructing. That is an axiom 1 violation wearing the costume of a cache
// miss, so the exchange records its OUTCOME — answer or no-answer — and not
// merely its success: an unanswered tick that is silently re-asked later is
// the same violation with better manners.
//
// What is recorded is the FULL DISTRIBUTION and the model identity, never
// just the chosen work type. The distribution is what the routing rule
// consumes (tick s45 routes on probability mass), and recording only the
// argmax would mean a threshold change could not be re-evaluated against runs
// that already happened.
//
// THE PRECONDITION, which is not an optimisation: A TICK CARRYING A ROLE IS
// NEVER CLASSIFIED. [roles.*] already maps review and closeout to a model, and
// the enum has no right answer for "read a diff and judge it" — the
// measurement put three role ticks through and got mechanical 0.32,
// diagnosis 0.61 and design 0.99 for the same kind of work. The gate here is
// the plan's own role; the tracker's own role field is consulted before the
// call as well, so the precondition holds on the tracker's truth, not on
// this package's normalisation of it.
//
// DEGRADATION, not failure: a run whose classifier was never configured — or
// one that could not be reached, refused, or answered a shape this reader does
// not recognise — routes at the start policy exactly as it did before this
// exchange existed. The no-answer is recorded like the answer, because the
// question this record answers is "what did the run know when it dispatched",
// and "nothing, for this reason" is knowledge a cold re-derivation needs to
// reproduce the same dispatch.

// Classifier is the classifier seam: one call, one batch of role-less ticks,
// the full probability distribution back. *jev.Client satisfies it, and a
// test fakes it — the seam exists so the exchange is testable without dialling
// anything, and so the surface that owns credentials can hand the reconciler a
// client without the reconciler learning where keys live.
type Classifier interface {
	Classify(ctx context.Context, ticks []jev.Tick) (jev.Result, error)
}

// classificationResponse is what the decision record's `response` carries for
// the classification exchange. The distribution is one entry per work type
// the answer put probability on, keyed by work-type name; `no_answer` carries
// the distinguishable no-answer's reason (the classifier unreachable, refused,
// malformed, never configured — or asked and given no valid answer for this
// tick), and exactly one of the two shapes is present: a response that is
// both, or neither, is refused by [classificationOfDecision].
type classificationResponse struct {
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Model         string             `json:"model"`
	NoAnswer      string             `json:"no_answer,omitempty"`
	Usage         jev.Usage          `json:"usage"`
}

// RecordedClassification is a classification decision record, read back: what
// the record says, re-validated the way the role exchange re-reads its
// envelope. Choice and Probabilities are empty when the record is a
// no-answer — the reader that routes (tick s45) falls back to the start policy
// on exactly that shape, the same degradation the warm process made.
type RecordedClassification struct {
	TickID        string
	Choice        runconfig.WorkType
	Confidence    float64
	Probabilities map[runconfig.WorkType]float64
	Model         string
	NoAnswer      string
}

// classificationOfDecision turns one decision record back into the
// classification it says it is — the same round trip the role exchange makes
// through roleResultOf, held to the same standard: a record that cannot be
// read back as a coherent answer is refused rather than re-paid for, because
// an unreadable record is not a cache miss.
func classificationOfDecision(d *runstate.Decision) (RecordedClassification, error) {
	if d.Role != runstate.RoleClassifyTick {
		return RecordedClassification{}, fmt.Errorf("the decision is a %s exchange, not a classification", d.Role)
	}
	raw, err := json.Marshal(d.Response)
	if err != nil {
		return RecordedClassification{}, err
	}
	var response classificationResponse
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&response); err != nil {
		return RecordedClassification{}, fmt.Errorf("the recorded response does not re-read as a classification: %w", err)
	}
	out := RecordedClassification{Model: response.Model, NoAnswer: strings.TrimSpace(response.NoAnswer)}
	if out.NoAnswer != "" {
		if len(response.Probabilities) > 0 || response.Choice != "" {
			return RecordedClassification{}, fmt.Errorf(
				"the recorded response is both a no-answer and a distribution: a record two readers disagree about")
		}
		return out, nil
	}
	if len(response.Probabilities) == 0 {
		return RecordedClassification{}, fmt.Errorf(
			"the recorded response carries no probability distribution, and the distribution is what routing consumes")
	}
	probabilities := make(map[runconfig.WorkType]float64, len(response.Probabilities))
	for name, mass := range response.Probabilities {
		if !runconfig.IsKnownWorkType(name) {
			return RecordedClassification{}, fmt.Errorf(
				"the recorded distribution puts probability on %q, which is not a work type on the closed enum", name)
		}
		probabilities[runconfig.WorkType(name)] = mass
	}
	if !runconfig.IsKnownWorkType(response.Choice) {
		return RecordedClassification{}, fmt.Errorf(
			"the recorded choice %q is not a work type on the closed enum", response.Choice)
	}
	if strings.TrimSpace(response.Model) == "" {
		return RecordedClassification{}, fmt.Errorf(
			"the recorded classification names no model identity: a re-derivation could not say which model it is re-reading")
	}
	out.Choice = runconfig.WorkType(response.Choice)
	out.Confidence = response.Confidence
	out.Probabilities = probabilities
	return out, nil
}

// classificationTickOf is the tick a decision record is about — the request's
// own `tick_id`, the same field the role exchange's lookup keys on.
func classificationTickOf(d *runstate.Decision) string {
	tick, _ := d.Request["tick_id"].(string)
	return tick
}

// recordedClassification is the tick's classification as the run branch
// already records it, or its absence. Errors are REFUSED by the caller, never
// walked around: a record that exists but cannot be read back is not re-asked
// — that is the cache-miss costume the exchange exists to prevent — it is a
// refusal for a person, exactly like a role decision that cannot be re-read.
func (r *Reconciler) recordedClassification(tickID string) (*RecordedClassification, bool, error) {
	decisions, err := r.store.Decisions()
	if err != nil {
		return nil, false, err
	}
	for i := range decisions {
		decision := &decisions[i]
		if decision.Role != runstate.RoleClassifyTick {
			continue
		}
		if classificationTickOf(decision) != tickID {
			continue
		}
		classification, err := classificationOfDecision(decision)
		if err != nil {
			return nil, false, err
		}
		return &classification, true, nil
	}
	return nil, false, nil
}

// classificationFor is the whole exchange for one tick: read the record if
// the run branch carries one, and otherwise — only at the tick's FIRST
// dispatch (tick sj2) — ask the classifier once, and record the outcome
// before anything is dispatched on top of it.
//
// A role-carrying tick answers (nil, nil): never classified, per the
// precondition above. An unconfigured classifier answers (nil, nil) the same
// way when no record exists — the run degrades to the start policy, which is
// today's behaviour, and a record an earlier incarnation DID write is still
// read: the record is the answer, whatever this incarnation could ask. And a
// LATER attempt that finds no record answers (nil, nil) too, for the same
// axiom 1 reason the record exists: the first attempt was planned under
// whatever its incarnation knew — nothing — and asking NOW would make the
// re-derivation of this very run dispatch the later attempt on an answer the
// first attempt never had, reaching a different dispatch than the run it
// reconstructs. So the later attempt routes at the policy exactly as the
// first one did, and the classifier is never asked past a tick's first
// dispatch.
func (r *Reconciler) classificationFor(ctx context.Context, entry planEntry, firstDispatch bool) (*RecordedClassification, error) {
	if entry.Role != "implement-tick" {
		return nil, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	if existing, ok, err := r.recordedClassification(entry.TickID); err != nil {
		return nil, r.refuse(RefusedClassification, entry.TickID,
			"tick %s has a recorded classification that cannot be read back: %v. The tick is neither dispatched nor "+
				"re-asked: an unreadable record is not a cache miss — re-asking a model the run already paid would make a "+
				"cold re-derivation reach a different answer than the warm process, so the record is the authority and a "+
				"person decides what happens to it",
			entry.TickID, err)
	} else if ok {
		return existing, nil
	}
	if !firstDispatch {
		// NOT this tick's first dispatch, and no record exists: the first
		// attempt was planned under nothing, and it is too late to ask
		// (tick sj2). An ask here would answer a question the run's own
		// history says was already settled by the first attempt's fallback —
		// and a cold re-derivation of this run, reading the record such an
		// ask would write, would reach a different dispatch than the run it
		// reconstructs. Nothing is asked and nothing is recorded: routing
		// falls back to the start policy, exactly as it does when no
		// classifier is configured at all.
		return nil, nil
	}
	if r.opts.Classifier == nil {
		// No classifier is configured: this run has no classification, and
		// routing falls back to the start policy. A record an earlier
		// incarnation wrote was already returned above.
		return nil, nil
	}

	// THE PRECONDITION, on the tracker's truth: the tick's own role field
	// decides, because a role this package normalises away is still a role
	// [roles.*] can map to a model.
	tick, err := r.tracker.Show(ctx, entry.TickID)
	if err != nil {
		return nil, fmt.Errorf("read tick %s to classify it: %w", entry.TickID, err)
	}
	if strings.TrimSpace(tick.Role) != "" {
		return nil, nil
	}

	// ASK ONCE. The only errors the classifier can return here are caller
	// defects that never reached the wire (an unkeyable batch); everything
	// about reachability and answers is in the result, and the outcome below
	// records whichever it was.
	result, err := r.opts.Classifier.Classify(ctx, []jev.Tick{{
		ID: entry.TickID, Title: tick.Title, Description: tick.Description,
		AcceptanceCriteria: tick.AcceptanceCriteria, Role: tick.Role,
	}})
	if err != nil {
		return nil, fmt.Errorf("classify %s: %w", entry.TickID, err)
	}

	// The outcome, whichever it was — answer, per-tick gap, or an unavailable
	// classifier — is what the record says, so the cold re-derivation falls
	// back for the same reason the warm process did.
	response := classificationResponse{Model: result.Model, Usage: result.Usage}
	classification, classified := result.Classifications[entry.TickID]
	switch {
	case result.Unavailable != "":
		response.NoAnswer = result.Unavailable
	case classified:
		response.Choice = string(classification.Choice)
		response.Confidence = classification.Confidence
		probabilities := make(map[string]float64, len(classification.Probabilities))
		for workType, mass := range classification.Probabilities {
			probabilities[string(workType)] = mass
		}
		response.Probabilities = probabilities
	default:
		if reason, unanswered := result.Unanswered[entry.TickID]; unanswered {
			response.NoAnswer = reason
		} else {
			response.NoAnswer = "the classifier answered neither a classification nor a named gap for this tick"
		}
	}
	if err := r.recordClassification(entry, tick, response); err != nil {
		return nil, err
	}

	// What the record says is the answer, read back through the same reader a
	// cold re-derivation uses — the write is not believed until it reads.
	existing, ok, err := r.recordedClassification(entry.TickID)
	if err != nil || !ok {
		return nil, r.refuse(RefusedClassification, entry.TickID,
			"tick %s was classified but the record does not read back as a classification: %v. The tick is neither "+
				"dispatched nor re-asked: what is on the run branch is the authority a cold re-derivation would read, "+
				"and a record that cannot be read is a person's to fix",
			entry.TickID, err)
	}
	if response.NoAnswer == "" {
		r.record(entry.TickID, StageClassified,
			"classified as %s (confidence %.2f) by %s; the full distribution is recorded on the run branch and later "+
				"passes read the record rather than re-asking",
			existing.Choice, existing.Confidence, existing.Model)
	} else {
		r.record(entry.TickID, StageClassified,
			"the classifier gave no answer (%s); the no-answer is recorded on the run branch and later passes read it "+
				"rather than re-asking, so routing falls back to the start policy",
			firstLine(existing.NoAnswer))
	}
	return existing, nil
}

// recordClassification lands the exchange's outcome as a decision record,
// created if absent, at the next free decision number. A classification is a
// thing a model was paid for once, so the record is never rewritten; a lost
// race on the NUMBER (another tick's classification, or a role decision)
// re-fetches and retries, while a lost race on THIS TICK's record means
// somebody else already recorded it and theirs stands.
func (r *Reconciler) recordClassification(entry planEntry, tick tk.Tick, response classificationResponse) error {
	request := map[string]any{
		"tick_id":             entry.TickID,
		"epic_id":             r.opts.EpicID,
		"role":                runstate.RoleClassifyTick,
		"title":               tick.Title,
		"description":         tick.Description,
		"acceptance_criteria": tick.AcceptanceCriteria,
	}
	responseMap, err := asRecordMap(response)
	if err != nil {
		return fmt.Errorf("record the classification of %s: %w", entry.TickID, err)
	}

	// The provenance of a reconciler-side exchange: no dispatch produced it,
	// so role, attempt, executor and tier stay null — the contract's reading of
	// "no dispatch produced this" — and what identifies the answer is the
	// model field: the classifier that gave it, from the answer itself.
	tickID := entry.TickID
	provenance := r.provenance(&tickID, nil, runstate.PhaseWorker, r.classificationSource())
	if strings.TrimSpace(response.Model) != "" {
		model := response.Model
		provenance.Model = &model
	}

	stamp := r.now().UTC().Format(time.RFC3339)
	for conflicts := 0; conflicts < maxDispatchConflicts; conflicts++ {
		if _, err := r.store.Fetch(); err != nil {
			return err
		}
		decisions, err := r.store.Decisions()
		if err != nil {
			return err
		}
		number := len(decisions) + 1
		for _, existing := range decisions {
			if existing.Decision >= number {
				number = existing.Decision + 1
			}
		}

		outcome, err := r.store.PutDecision(runstate.Decision{
			Decision: number, Role: runstate.RoleClassifyTick,
			Request: request, Response: responseMap,
			Validated: true, RequestedAt: stamp, AnsweredAt: stamp,
			Provenance: provenance,
		})
		if err != nil {
			return fmt.Errorf("record the classification of %s: %w", entry.TickID, err)
		}
		if outcome.EffectPermitted() {
			return nil
		}
		// Somebody else took the number. If it was this tick's classification —
		// an identical racing incarnation — theirs is the record and the ask
		// still happened once as far as the run branch is concerned; otherwise
		// the slot was another exchange's and the next number is tried.
		if _, err := r.store.Fetch(); err != nil {
			return err
		}
		if _, ok, err := r.recordedClassification(entry.TickID); err != nil {
			return err
		} else if ok {
			return nil
		}
	}
	return fmt.Errorf("record the classification of %s: the run's decisions moved under this writer %d times running; "+
		"that is an operational problem, not a race to spin on", entry.TickID, maxDispatchConflicts)
}

// classificationSource is the ref the classification was produced against:
// the integration branch as origin has it NOW, the branch the tick's own
// text was read from — the same base a dispatch of this moment would be cut
// from. The run's original base is the fallback, exactly as a dispatch's own
// provenance falls back to it.
func (r *Reconciler) classificationSource() string {
	if head, err := r.git.remoteHead(r.branch); err == nil && head != "" {
		return head
	}
	return ""
}
