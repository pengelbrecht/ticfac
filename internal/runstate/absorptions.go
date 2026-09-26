package runstate

import (
	"fmt"
)

// The absorption decision record (tick npq): what the run itself decided
// about a finding it drafted, written on the run branch beside the tick the
// decision created.
//
// Absorption changes the epic's SHAPE mid-run — a new child, a new edge, a
// graph the run did not start with — and Axiom 1 says a cold reconstruction
// from git must reach the SAME epic, which means the reasoning that absorbed
// the finding is a record on the run branch, not merely a tick in the
// tracker. Without this record, the tick exists but WHY it exists lives only
// in whatever warm process created it: a re-derivation that reaches a smaller
// epic than the warm run is the failure this record exists to prevent.
//
// The record is the decision the two-tier verdict drove (ticks pzp, bse):
// which acceptance item the finding was judged to break (the item id, empty
// when the verdict named none — the fallback that absorbs without a model's
// answer), whether the finding gates the done, whether that verdict was
// OBSERVED (a command ran and said so) or PREDICTED (a classifier's guess, or
// the documented fallback standing in for one), and the confidence when a
// classifier answered. A retro that cannot tell a guess from a measurement
// cannot report honestly, so Basis is a typed field here as it is on the
// verdict.
//
// The record is also the idempotency marker for the promotion itself: it is
// created-if-absent keyed by the finding's dedup key BEFORE the tick is
// created, so an incarnation killed between the two is resumed by the record
// — the next one re-reads the tick id, creates the tick if it does not exist
// yet, and finishes the triage behind the decision the record already
// carries. Existence is the marker, the attempts' own rule.

// The basis of the recorded verdict. These are gating's Basis values (tick
// bse declares them; the oracle writes BasisObserved), spelled here because
// runstate is the layer BELOW gating and cannot import it — the same
// one-spelling-per-layer rule the finding record keeps for its own
// vocabularies.
const (
	// AbsorptionObserved: a command bound to the item ran and answered. The
	// oracle's verdict (tick pzp) — authoritative, and never overridden by a
	// prediction.
	AbsorptionObserved = "observed"
	// AbsorptionPredicted: a guess, however confident — a classifier's answer
	// or the fallback that stands in when none could be had. The predictor's
	// verdict (tick bse), and the absorption's own fallback (tick npq).
	AbsorptionPredicted = "predicted"
)

// The placement of the tick the promotion created, as the run arranged it.
// The absorbed tick is placed so it is fixed BEFORE the items it gates are
// asserted — before the final review when the review has not run yet, and
// otherwise before the close-out (whose own gate refuses to start while a
// child of the epic is open). A fix that lands after the review it
// invalidates has not been absorbed, it has been appended — so the placement
// is a stated fact of the record, never something a reader infers from where
// the tick happened to land in a wave.
const (
	// AbsorptionBeforeReview: the review tick was still open, and the
	// promotion made it blocked-by the absorbed tick — the review that
	// asserts the items runs after the fix.
	AbsorptionBeforeReview = "before-review"
	// AbsorptionAfterReview: the review had already run (the finding is the
	// review's own discovery, or a late one), so the placement is before the
	// close-out, which does not start while any child of the epic is open and
	// is where the done is asserted at hand-over.
	AbsorptionAfterReview = "after-review-before-closeout"
	// AbsorptionBacklog: the finding was judged NOT GATING — the done is
	// reachable with it standing — and the promotion created a backlog tick
	// with an owner rather than a child of the running epic.
	AbsorptionBacklog = "backlog"
)

// AbsorptionPlacements is the closed placement vocabulary.
var AbsorptionPlacements = []string{
	AbsorptionBeforeReview, AbsorptionAfterReview, AbsorptionBacklog,
}

// Absorption is one decision, at `.ticfac/runs/<run-id>/absorptions/<key>.json`,
// where <key> is the finding's dedup key.
type Absorption struct {
	SchemaVersion int `json:"schema_version"`
	// Key is the finding the decision is about — the draft's dedup key, and
	// the record's file name.
	Key string `json:"key"`
	// TickID is the tick the promotion created. For a gating finding it is a
	// child of the running epic, sequenced before the review; for a non-gating
	// one it is a backlog tick with an owner.
	TickID string `json:"tick_id"`
	// Gating is the verdict: the finding breaks the epic's definition of done
	// (true — absorbed into the epic) or the done is reachable with it
	// standing (false — a backlog tick, still reported).
	Gating bool `json:"gating"`
	// ItemID is the acceptance item the verdict named — the item whose
	// demonstration the finding makes unreachable. Empty when the verdict
	// named none (a fallback that absorbed without a model's answer, naming
	// every unverified item in its reason instead) and always empty for a
	// non-gating verdict.
	ItemID string `json:"item_id,omitempty"`
	// Basis is how the verdict was reached: observed (a command answered) or
	// predicted (a classifier's guess or the fallback). The record's most
	// important field, for the retro the close-out owes.
	Basis string `json:"basis"`
	// Confidence is what the classifier answered for its own choice — zero
	// for the fallback and for an observation, which no confidence qualifies.
	Confidence float64 `json:"confidence,omitempty"`
	// Fallback, when non-empty, is why NO MODEL answered — the classifier
	// unreachable, or neither tier able to reach the item at all — and the
	// verdict is the documented absorb-anyway fallback standing in for one.
	Fallback string `json:"fallback,omitempty"`
	// Reason is what the verdict rests on, in the deciding tier's own words:
	// the observation's evidence, the mass against the threshold, or the cost
	// asymmetry for a fallback. Written for the person the retro reports to.
	Reason string `json:"reason"`
	// Placement says where the promotion put the tick: before the review,
	// before the close-out, or the backlog. A stated fact, never an
	// inference from a wave number.
	Placement string `json:"placement"`
	// DecidedAt is when the run decided, RFC3339.
	DecidedAt string `json:"decided_at"`

	Provenance Provenance `json:"provenance"`
}

// Validate applies the record's own rules: the closed basis and placement
// vocabularies, the decision's agreement with itself (a non-gating verdict
// names no item and lands in the backlog; a backlog record is not a gating
// one), and a reason a reader can act on — the same discipline every other
// decision record here is held to: a decision nobody can attribute is one
// nobody can audit.
func (a Absorption) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("absorption schema_version is %d, want %d", a.SchemaVersion, SchemaVersion)
	}
	if err := checkSegment("absorption key", a.Key); err != nil {
		return err
	}
	if a.TickID == "" {
		return fmt.Errorf("absorption of %s names no tick: the promotion is the tick the decision created, "+
			"and the record must say which — an orphaned reasoning is the re-derivation hole the record exists to close", a.Key)
	}
	if !oneOf(a.Basis, []string{AbsorptionObserved, AbsorptionPredicted}) {
		return fmt.Errorf("absorption basis %q is neither %s nor %s: a retro that cannot tell a guess from a "+
			"measurement cannot report honestly", a.Basis, AbsorptionObserved, AbsorptionPredicted)
	}
	if !oneOf(a.Placement, AbsorptionPlacements) {
		return fmt.Errorf("absorption placement %q is not one of %v", a.Placement, AbsorptionPlacements)
	}
	if a.Reason == "" {
		return fmt.Errorf("absorption of %s carries no reason: a decision that cannot say what it rests on is "+
			"one nobody can audit or replay", a.Key)
	}
	if a.DecidedAt == "" {
		return fmt.Errorf("absorption of %s has no decided_at", a.Key)
	}
	if !a.Gating {
		if a.ItemID != "" {
			return fmt.Errorf("absorption of %s is not gating and names item %s: a verdict that says the done is "+
				"reachable names no item it breaks", a.Key, a.ItemID)
		}
		if a.Placement != AbsorptionBacklog {
			return fmt.Errorf("absorption of %s is not gating and placed %q: a finding the done is reachable "+
				"without becomes a backlog tick, not a child of the running epic", a.Key, a.Placement)
		}
	}
	if a.Gating && a.Placement == AbsorptionBacklog {
		return fmt.Errorf("absorption of %s is gating and placed in the backlog: a finding that gates the done is "+
			"absorbed into the epic, and a backlog tick for it would close an epic whose goal is unmet", a.Key)
	}
	if a.Basis == AbsorptionObserved && a.Confidence != 0 {
		return fmt.Errorf("absorption of %s is observed and carries confidence %.2f: an observation is what a "+
			"command said, and no confidence qualifies it", a.Key, a.Confidence)
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		return fmt.Errorf("absorption of %s carries confidence %.2f, which is not a probability", a.Key, a.Confidence)
	}
	return a.Provenance.validate()
}
