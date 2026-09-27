package gating

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/jev"
)

// NoneLabel is the extra choice on every question this package asks: the
// finding gates no item, the done is demonstrable in full while it stands, and
// the finding belongs to a backlog tick with an owner. It is the enum's last
// label and never an item id, so it cannot collide with the [A<n>] namespace.
const NoneLabel = "none"

// AbsorbThreshold is the bar of probability mass on the items (rather than on
// 'none') at and above which a prediction declares the finding GATING.
//
// THE THRESHOLD IS DELIBERATELY CHEAP, and this comment is the place the
// asymmetry that sets it is stated, because this is where someone will later
// be tempted to tune it the other way: a FALSE POSITIVE costs the epic some
// work it did not need — an absorbed tick that turns out unnecessary. A FALSE
// NEGATIVE closes an epic whose goal is unmet, in a factory where nobody is
// watching — the exact failure the gvc epic exists to prevent. Those are not
// the same cost: the second is the disaster; the first is waste. So the bar
// sits well below a coin flip, at 0.35, and the decision spends the MASS on
// the items rather than the argmax, so a classifier that says "probably fine"
// still absorbs when the mass says otherwise.
//
// Raising this number is allowed. Raising it without re-reading this comment
// is not: move it toward a coin flip and each step of the move quietly trades
// "unmet goals, silently closed" for "work the epic did not need", which is
// the wrong direction of trade in a factory nobody is watching.
const AbsorbThreshold = 0.35

// Basis distinguishes a MEASUREMENT from a GUESS in the record: a verdict the
// oracle reached by running the done's command is OBSERVED; a verdict this
// package reached by asking a classifier — or by falling back — is PREDICTED.
// A retro that cannot tell the two apart cannot report honestly, and the
// self-measurement the epic designs (jlv scores predictions against what the
// done later did) is impossible if they read the same, so it is a typed field
// on every verdict, never a convention.
type Basis string

const (
	// BasisObserved is the oracle's verdict: the acceptance item's command
	// ran, and the verdict says what it showed. This package never writes it;
	// it is declared here so the oracle's record is the same shape as this
	// package's and the two cannot drift into indistinguishable prose.
	BasisObserved Basis = "observed"
	// BasisPredicted is this package's verdict: a guess, however confident —
	// from a classifier's answer, or from the absorb fallback when no answer
	// existed. Recorded as a guess on purpose, whatever its confidence.
	BasisPredicted Basis = "predicted"
)

// Finding is the defect a worker drafted, as the record the worker wrote it:
// the finding's own title and body are the prose the classifier judges — the
// prediction is reasoning about prose against prose, and it needs the finding's
// own words, not a summary of them. It is deliberately this package's own
// shape rather than the findings contract's, so this package reads no contract.
type Finding struct {
	// ID keys the finding's question and its verdict, and names the finding
	// in every record: the absorption (npq) and the later scoring (jlv) both
	// point at the prediction through it.
	ID    string
	Title string
	Body  string

	// The reporter's DONE EVIDENCE (tick nfo), wired in as INPUTS and never
	// as verdicts (tick wz0, finding c244ce2c): the worker prompt promises
	// that the claim is evidence, never the verdict — the run runs the named
	// check where it can, predicts where it cannot yet, and scores the claim
	// against what the done actually did. Both tiers read these fields:
	// the predictor carries them into the classifier's state as evidence the
	// answer weighs, and the oracle scores the claim against what ran in the
	// reason a person reads. Neither lets the claim BE the verdict — a
	// confirmed claim proves nothing the command did not show, and a refuted
	// one un-breaks nothing.
	//
	// DoneItem is the [A<n>] acceptance item the reporter believes the
	// finding breaks, or 'none' when the reporter believes it breaks none.
	// Empty is the visible third state — unlinked, no claim — never a claim
	// of non-gating.
	DoneItem string
	// DemonstratingCheck is the command or test the reporter names as what
	// would demonstrate the breakage. Where the claimed item is runnable the
	// run runs the command the evidence table binds to it — the table is the
	// authorisation, and the oracle's verdict on it is authoritative — and
	// where it is not runnable the check is named to the classifier as part
	// of the claim.
	DemonstratingCheck string
}

// Verdict is the decision record one prediction produces. It is the shape both
// halves of the two-tier decision write — this package writes it with
// [BasisPredicted], the oracle (pzp) with [BasisObserved] — so a reader of the
// run branch can always tell a guess from a measurement by a field, never by
// inference.
type Verdict struct {
	// FindingID is the finding the verdict is about.
	FindingID string `json:"finding_id"`
	// Gating says whether the finding is decided to gate the done. For this
	// package's verdicts it is a PREDICTION, per Basis below — never an
	// observation, whatever the confidence.
	Gating bool `json:"gating"`
	// ItemID is the acceptance item the finding is predicted to break — the
	// answer doubles as the item id that absorption has to name. Empty when
	// the finding is not gating, and when the fallback absorbed without a
	// model's answer to name one (the fallback's Reason names every
	// unverified item at risk instead).
	ItemID string `json:"item_id,omitempty"`
	// Basis is how the verdict was reached: predicted (this package) or
	// observed (the oracle). The record's most important field.
	Basis Basis `json:"basis"`
	// Confidence is what the classifier answered for its own choice —
	// computed by the API from the shape of the distribution. Zero for the
	// fallback, which no model answered.
	Confidence float64 `json:"confidence,omitempty"`
	// GatingMass is the probability mass the answer put on the items rather
	// than on 'none' — the number the threshold is spent on.
	GatingMass float64 `json:"gating_mass,omitempty"`
	// Probabilities is the full distribution over the enum actually asked:
	// the unverified item ids and 'none'. The mass is the payload, and
	// recording only an argmax would starve the later scoring (jlv) of the
	// number it re-evaluates.
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Model is the answering model's identity, for the record: a later pass
	// re-reads what it says rather than re-asking a model the run already
	// paid. Empty for the fallback.
	Model string `json:"model,omitempty"`
	// Fallback, when non-empty, is why NO PREDICTION was made — the
	// classifier was unreachable, refused, answered a shape the core does not
	// recognise, or named a gap instead of an answer — and the verdict is the
	// documented absorb fallback standing in for it. A fallback verdict is
	// still a prediction, never an observation, and never a stop.
	Fallback string `json:"fallback,omitempty"`
	// Reason says what the verdict rests on: the mass and the threshold for a
	// prediction, the asymmetry for a fallback, and the backlog for a
	// non-gating finding. Written for the retro a person reads.
	Reason string `json:"reason"`
}

// Classifier is the seam to the generalised Jev core: *jev.Client satisfies
// it, and a test fakes it — the seam exists so this decision is testable
// without dialling anything, and so the caller that owns credentials can hand
// the predictor a client it built (jev.ResolveCredential, jev.New) without this
// package learning where keys live.
type Classifier interface {
	Ask(ctx context.Context, state string, questions []jev.Question) (jev.AnswerResult, error)
}

// Predictor decides gating where the done cannot yet be run.
type Predictor struct {
	classifier Classifier
}

// NewPredictor builds a Predictor on the given classifier. A nil classifier is
// the same no-answer as an unreachable one: every prediction falls back to
// absorbing, which is the documented degradation — never a stop, and never a
// defer, because deferring would stop an unattended run on the one actor only a
// person can play.
func NewPredictor(classifier Classifier) *Predictor {
	return &Predictor{classifier: classifier}
}

// Predict decides whether the finding gates the epic's done, over the done's
// NOT-YET-RUNNABLE items.
//
// The three returns, and every caller takes all three:
//
//   - (verdict, "", nil): a verdict exists — a classifier's prediction, or the
//     absorb fallback standing in for an answer that could not be had. The
//     verdict's Basis is always BasisPredicted.
//   - (nil, reason, nil): nothing is this package's to predict, and the reason
//     says whose it is — every item is the oracle's (running it is the
//     authoritative verdict), or the done carries no items at all (the refusal
//     owns it, and this package does not guess where the refusal refused).
//   - (nil, "", err): a caller defect — a finding with no id cannot be keyed.
//
// RUNNABLE ITEMS ARE NOT OFFERED: the question is a Choice over the unverified
// items plus 'none', in document order with 'none' last, and an item bound to
// a command in [evidence.acceptance] is the oracle's alone. That is enforced
// here, not left to the caller: no classifier may predict over what can be
// run, because the oracle's verdict on it is authoritative and a prediction
// beside it would be noise that outranks nothing and confuses everything.
func (p *Predictor) Predict(ctx context.Context, finding Finding, done acceptance.Done) (*Verdict, string, error) {
	if strings.TrimSpace(finding.ID) == "" {
		return nil, "", fmt.Errorf(
			"a finding with no id cannot be keyed into the classifier's question, so its gating cannot be predicted")
	}

	// The classifier's items are the UNVERIFIED ones; the runnable ones are
	// the oracle's, and are not offered at all.
	var unverified []acceptance.Resolved
	for _, item := range done.Items {
		if item.State == acceptance.Unverified {
			unverified = append(unverified, item)
		}
	}
	if len(done.Items) == 0 {
		// Never a question: an acceptance with no items is the refusal's to
		// answer for (klq), and standing in for a refusal with a guess would
		// be the exact failure the refusal exists to prevent.
		return nil, "the done carries no acceptance items to predict against: an unenumerated acceptance is a " +
			"refusal's to answer for, not a prediction's to guess at — mark the acceptance into [A<n>] items " +
			"first, then decide absorption against them", nil
	}
	if len(unverified) == 0 {
		return nil, "every acceptance item is bound to a command in [evidence.acceptance], so each is the " +
			"oracle's to run and none is the classifier's to predict: running the done is the authoritative verdict " +
			"on a runnable item and no classifier overrides it", nil
	}

	state, question := ask(finding, unverified)
	if p.classifier == nil {
		// A predictor built with no classifier is the same no-prediction as
		// an unreachable one, and the same decision stands in for it: absorb,
		// recorded as a guess, never as a stop.
		return absorbFallback(finding, unverified,
			"no classifier is configured for gating predictions"), "", nil
	}
	result, err := p.classifier.Ask(ctx, state, []jev.Question{question})
	if err != nil {
		// Caller defects only (a batch that cannot be keyed): this package
		// built the question, so an error here is this package's defect, and
		// guessing past it would be a decision wearing a defect's costume.
		return nil, "", err
	}
	if result.Unavailable != "" {
		return absorbFallback(finding, unverified, result.Unavailable), "", nil
	}
	if reason, gap := result.Unanswered[finding.ID]; gap {
		return absorbFallback(finding, unverified, reason), "", nil
	}
	answer, ok := result.Answers[finding.ID]
	if !ok {
		// The core answers every asked question or names its gap, so this is
		// a violated seam rather than a judgement — the same epistemic state
		// as an unavailable classifier: no prediction exists.
		return absorbFallback(finding, unverified,
			"the classifier answered neither an answer nor a named gap for the question"), "", nil
	}
	return predict(finding, unverified, answer, result), "", nil
}

// ask builds the one question and the state it is judged against: a Choice
// over the unverified items plus 'none', each item's criterion carrying the
// item's OWN fact, and the state carrying the finding's own prose — the
// prediction is prose against prose, so the classifier needs both texts
// whole.
func ask(finding Finding, items []acceptance.Resolved) (string, jev.Question) {
	var state strings.Builder
	fmt.Fprintf(&state, "finding %s\n", finding.ID)
	if strings.TrimSpace(finding.Title) != "" {
		fmt.Fprintf(&state, "title: %s\n", strings.TrimSpace(finding.Title))
	}
	if strings.TrimSpace(finding.Body) != "" {
		fmt.Fprintf(&state, "body: %s\n", strings.TrimSpace(finding.Body))
	}
	// The reporter's claim, as evidence in front of the classifier (tick
	// wz0, finding c244ce2c): the reporter already read the epic's acceptance
	// and answered the question this prediction is about, so the answer is
	// weighed with the claim in front of it — and still never believed,
	// which is why the claim rides the state and never widens the enum: a
	// reporter can be wrong in either direction, and the mass still decides.
	if claim := strings.TrimSpace(finding.DoneItem); claim != "" {
		if claim == NoneLabel {
			fmt.Fprintf(&state, "the reporter claims the finding breaks no acceptance item\n")
		} else {
			fmt.Fprintf(&state, "the reporter claims the finding breaks done item %s", claim)
			if check := strings.TrimSpace(finding.DemonstratingCheck); check != "" {
				fmt.Fprintf(&state, ", and names %q as what would demonstrate the breakage", check)
			}
			fmt.Fprintf(&state, "\n")
		}
		fmt.Fprintf(&state, "the claim is evidence, never the verdict\n")
	}

	choices := make([]jev.Choice, 0, len(items)+1)
	for _, item := range items {
		choices = append(choices, jev.Choice{
			Label: item.ID,
			Criterion: jev.Criterion{
				What: fmt.Sprintf(
					"The finding breaks this item: %s While the finding stands, the item cannot be demonstrated — "+
						"including when nothing can run yet to show it, and including when the finding is itself a "+
						"prediction that nothing has proved red.", item.Text),
				NotFor: "A finding this item does not depend on, however real or however well-evidenced: the done is " +
					"demonstrable as far as this item is concerned with the finding standing, and a real defect the " +
					"done does not need is a backlog tick with an owner, not the epic's to absorb.",
				Examples: []string{
					"A model name spelled two ways (qu3): the done said a run dispatches on the gateway model and a " +
						"container boot would refuse it — nothing was red, because no test boots a container, and " +
						"still the item could not be demonstrated while the finding stood.",
				},
			},
		})
	}
	choices = append(choices, jev.Choice{
		Label: NoneLabel,
		Criterion: jev.Criterion{
			What: "No item of this epic's definition of done depends on the finding: the done is demonstrable in " +
				"full while the finding stands, and the finding belongs to a backlog tick with an owner, however " +
				"interesting it is.",
			NotFor: "Choosing none because nothing has run red yet, or because the item's check cannot run today: " +
				"the question is whether the DONE depends on the finding, not whether it can be demonstrated " +
				"right now. A finding that is itself a prediction — nothing red, nothing runnable — is exactly " +
				"what this question exists for.",
			Examples: []string{
				"A merge failure that named no file (ky5): it was real and it cost an hour, but the done was " +
					"reachable with it standing — backlog, and it stayed backlog.",
			},
		},
	})

	return state.String(), jev.Question{
		ID: finding.ID,
		Instructions: fmt.Sprintf(
			"Does finding %s break an acceptance item of this epic's definition of done? Judge the finding's own "+
				"text above against each item's fact, on the axis of whether the item can be demonstrated while the "+
				"finding stands — including when nothing can run yet to show it.", finding.ID),
		Choices: choices,
	}
}

// predict turns a classifier's answer into the verdict: the decision spends
// the MASS on the items rather than the argmax, against the deliberately
// cheap threshold, and the answer's own choice is the item absorption names.
func predict(finding Finding, items []acceptance.Resolved, answer jev.Answer, result jev.AnswerResult) *Verdict {
	mass := 0.0
	heaviest, heaviestMass := "", -1.0
	for _, item := range items {
		itemMass := answer.Probabilities[item.ID]
		mass += itemMass
		// Walk the items in document order, so a tie lands on the first item
		// deterministically rather than on whatever the map handed over.
		if itemMass > heaviestMass {
			heaviest, heaviestMass = item.ID, itemMass
		}
	}

	verdict := Verdict{
		FindingID:     finding.ID,
		Basis:         BasisPredicted,
		Confidence:    answer.Confidence,
		GatingMass:    mass,
		Probabilities: answer.Probabilities,
		Model:         result.Model,
	}
	if mass < AbsorbThreshold {
		verdict.Reason = fmt.Sprintf(
			"the classifier put %.2f of its probability mass on the items rather than on none, below the deliberately "+
				"cheap absorb threshold of %.2f: the done is predicted reachable with the finding standing, and a real "+
				"defect the done does not need is a backlog tick with an owner rather than the epic's to absorb",
			mass, AbsorbThreshold)
		return &verdict
	}

	// The answer doubles as the item id that absorption has to name; when the
	// choice is 'none' and the mass cleared the bar anyway, the distribution's
	// own maximum among the items is the named item.
	verdict.Gating = true
	verdict.ItemID = answer.Choice
	if verdict.ItemID == "" || verdict.ItemID == NoneLabel {
		verdict.ItemID = heaviest
	}
	verdict.Reason = fmt.Sprintf(
		"the classifier put %.2f of its probability mass on the items rather than on none, at or above the "+
			"deliberately cheap absorb threshold of %.2f, and named %s: a false positive costs the epic work it did "+
			"not need, a false negative closes an epic whose goal is unmet in a factory where nobody is watching, "+
			"and those are not the same cost — the threshold sits on the cheap side",
		mass, AbsorbThreshold, verdict.ItemID)
	return &verdict
}

// absorbFallback is the verdict written when NO PREDICTION could be made — the
// classifier unreachable, refused, malformed, or naming a gap — and the
// documented decision is to ABSORB rather than defer. It is still recorded as a
// prediction (a guess, never a measurement), with Fallback carrying why no model
// made it and the reason naming every unverified item at risk, because the
// absorption has to name which item was unreachable and with no answer all of
// them were.
func absorbFallback(finding Finding, items []acceptance.Resolved, why string) *Verdict {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return &Verdict{
		FindingID: finding.ID,
		Gating:    true,
		Basis:     BasisPredicted,
		Fallback:  why,
		Reason: fmt.Sprintf(
			"no prediction could be made — %s — and the decision is to absorb anyway: a false negative would close an "+
				"epic whose goal is unmet in a factory where nobody is watching, which costs more than the work a "+
				"false positive wastes, and deferring would stop an unattended run on the one actor only a person "+
				"can play. The unverified items at risk are %s",
			strings.TrimSpace(why), strings.Join(ids, ", ")),
	}
}
