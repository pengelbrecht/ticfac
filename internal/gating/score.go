package gating

import (
	"fmt"
	"strings"
)

// The scoring half of the self-measurement (tick jlv). A prediction made
// while an acceptance item was unrunnable becomes CHECKABLE the moment that
// item becomes runnable: what the classifier said, and what the done actually
// did, is a labelled pair — the calibration set the factory generates by
// operating, which wne's 49-tick measurement could not have, because that
// sample could only test face validity (every tick ran at its role's base
// model and no capability boundary was ever exercised). gvc set the absorb
// threshold by argument rather than measurement; this is the only measurement
// anyone will ever have of where it belongs.
//
// The rules, and nothing here may weaken them:
//
//   - ONLY A PREDICTION IS SCORED. An observed verdict is what a command
//     said, and scoring a measurement against itself is an echo recorded as
//     calibration data.
//   - THE SCORE IS PER ITEM: the prediction's own named item, against its
//     own bound command. The fallback absorbed naming every unverified item
//     at risk in its reason and names no single item, so no one command
//     checks it — it is reported unchecked, never half-scored.
//   - NOTHING IS GUESSED PAST: a run that produced no evidence about the item
//     leaves the prediction unchecked, never labelled either way. The oracle's
//     own rule, one pass further out.

// Score is the label a checked prediction is scored with: CORRECT — the run
// agreed with the prediction, the done did not demonstrate the item; or
// INCORRECT — the run disagreed, the done demonstrated the item as the epic
// hands it over. A wrong prediction is not a failure to hide: it is the
// evidence the threshold's next reader needs, which is why it is a typed
// field on the record and never prose a retro paraphrases.
type Score string

const (
	// ScoreCorrect: the item's command answered fail on the tree the close-out
	// scores, so the done does not demonstrate the item and the finding was
	// gating as predicted.
	ScoreCorrect Score = "correct"
	// ScoreIncorrect: the item's command passed on the tree the close-out
	// scores, so the done demonstrates the item as the epic hands it over.
	ScoreIncorrect Score = "incorrect"
)

// Checked is one prediction scored against what the done actually did: the
// label, and the reason written for the retro a person reads — naming the
// item, the command that answered, and the commit the answer ran on, so the
// label can be re-read against the tree it is about rather than trusted as a
// bare word.
type Checked struct {
	// Score is the label: correct or incorrect.
	Score Score
	// Reason says what the run showed and what that does to the prediction,
	// in the terms the record's reader can audit.
	Reason string
}

// ScorePrediction labels one prediction against one observation of the item
// it named. It answers (Checked, true) when the pair exists — the prediction
// was a guess about a named item, and that item's command produced evidence —
// and (Checked{}, false) when it does not, and every false is a state the
// CALLER reports rather than papers over:
//
//   - the verdict was observed, not predicted: nothing to score — the
//     observation is its own answer;
//   - the prediction broke no item or named none (a non-gating prediction,
//     or the absorb fallback): no one command checks it;
//   - nothing was observed, or the observation does not decide the predicted
//     item: the prediction is reported unchecked, never labelled either way.
func ScorePrediction(prediction Verdict, observed *Observed) (Checked, bool) {
	if prediction.Basis != BasisPredicted {
		// An observation is what a command said. Scoring it against itself
		// would dress an echo up as calibration data.
		return Checked{}, false
	}
	if !prediction.Gating || strings.TrimSpace(prediction.ItemID) == "" {
		// A prediction that named no item named no command that could check
		// it. The fallback's reason names every unverified item at risk, but
		// a score is of ONE item's OWN answer, never a borrowed one.
		return Checked{}, false
	}
	if observed == nil {
		return Checked{}, false
	}
	for _, observation := range observed.Items {
		if observation.ItemID != prediction.ItemID {
			continue
		}
		if observation.Gating {
			return Checked{
				Score: ScoreCorrect,
				Reason: fmt.Sprintf(
					"the prediction was RIGHT: at the close-out the command %s for %s answered fail (exit %d) on %s — "+
						"the tree the epic hands over, which carries the absorbed fix — so the done does not demonstrate "+
						"the item and the finding was gating as predicted",
					observation.Evidence.Check.ID, prediction.ItemID, observation.Evidence.ExitCode,
					shortCommit(observed.Commit)),
			}, true
		}
		return Checked{
			Score: ScoreIncorrect,
			Reason: fmt.Sprintf(
				"the prediction was WRONG: at the close-out the command %s for %s passed on %s — the tree the epic "+
					"hands over, which carries the absorbed fix — so the done demonstrates the item as handed over. "+
					"A wrong prediction is not a failure to hide: it is the only evidence anyone will ever have for "+
					"where the absorb threshold belongs",
				observation.Evidence.Check.ID, prediction.ItemID, shortCommit(observed.Commit)),
		}, true
	}
	// The item's command produced no evidence (error, skipped, or could not
	// run): the prediction stays unchecked, never guessed either way.
	return Checked{}, false
}
