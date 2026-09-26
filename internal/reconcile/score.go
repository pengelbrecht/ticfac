package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/gating"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The self-measurement (tick jlv): the scoring pass the close-out runs before
// anything is dispatched or handed over.
//
// A prediction made while an acceptance item was unrunnable becomes CHECKABLE
// the moment that item becomes runnable, and the epic finishing is what makes
// the item runnable — the epic built the thing the item's command needs. What
// the classifier said, and what the done actually did, is a labelled pair: the
// calibration set the factory generates BY OPERATING, and the only evidence
// anyone will ever have for where gating's absorb threshold belongs, which gvc
// set by argument rather than measurement. A prediction that was wrong is not
// a failure to hide.
//
// SO THE PASS IS: at the close-out — after the open-children gate has every
// child closed, before the close-out job is claimed or dispatched — every
// PREDICTED absorption whose named item is NOW runnable has that item's
// command run, through the same oracle and the same evidence table the
// absorption decision used, and the prediction is scored against the run:
//
//   - the item's command answered FAIL — the done does not demonstrate the
//     item on the tree the close-out hands over — the prediction was RIGHT;
//
//   - the item's command PASSED — the done demonstrates the item as the epic
//     hands it over, which carries the absorbed fix — the prediction was
//     WRONG, recorded as wrong, because the wrongness is the measurement.
//
// What stays unchecked stays unchecked, visibly: an observed absorption is a
// measurement and is never scored against itself; a non-gating prediction and
// the absorb fallback named no item, so no one command checks them; and an
// item whose command produced no evidence (still not runnable, could not run,
// error, skipped) leaves the prediction unscored rather than guessed either
// way — the oracle's own rule, one pass further out. The retro reports the
// difference: what was absorbed, against which item, observed or predicted,
// and the score of each CHECKED prediction.
//
// The record is on the run branch beside the absorption it scores —
// `.ticfac/runs/<run-id>/predictions/<key>.json`, keyed by the same finding
// key — and carries BOTH HALVES of the pair, so a later measurement across
// epics reads one record per prediction. Create-if-absent makes the pass
// idempotent however many incarnations the close-out takes: a prediction is
// scored once, and the incarnation killed between the command and the record
// is resumed by the record's absence.

// scorePredictions runs the self-measurement at the close-out. It is called
// from the close-out's settle, before the job is claimed: the retro the
// close-out writes — and the epic PR's body — report the scores, so the
// scores have to exist before the worker that reports them starts.
func (r *Reconciler) scorePredictions(ctx context.Context, tick string) error {
	if r.store == nil {
		return nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return fmt.Errorf("read the run state on %s to score its checked predictions: %w", r.branch, err)
	}
	records, err := r.store.Absorptions()
	if err != nil {
		return fmt.Errorf("read the run's absorption decisions to score their predictions: %w", err)
	}
	if len(records) == 0 {
		// Nothing was absorbed, so nothing was predicted: the epic's shape
		// never changed, and the retro has nothing to score.
		return nil
	}

	// The epic's own definition of done, resolved against the evidence table
	// as it stands NOW — the same table, read the same way, as the absorption
	// decision read it. An item that was unverified then and runnable now is
	// exactly the pair this pass exists to score.
	epic, err := r.tracker.Show(ctx, r.opts.EpicID)
	if err != nil {
		return fmt.Errorf("read the epic %s to score its checked predictions: %w", r.opts.EpicID, err)
	}
	evidence, commands, err := r.evidenceTable()
	if err != nil {
		return err
	}
	done, refusal, err := acceptance.Decide(epic.AcceptanceCriteria, evidence)
	if err != nil {
		// The acceptance the absorption itself once decided against is now
		// unreadable — a person edited the epic's own text between the two.
		// The scoring refuses to guess past it, and says so in the feed: the
		// predictions stay unchecked, which the retro reports, rather than
		// being scored against a done this run cannot read.
		r.record(tick, StagePredictionScored,
			"the run cannot score its predictions: the epic %s carries an acceptance this run cannot read, so each "+
				"checked prediction would be scored against a done nobody can parse: %v", r.opts.EpicID, err)
		return nil
	}
	if refusal != nil {
		// The acceptance was enumerated when the absorption decided against
		// it and is prose now: nothing is runnable, nothing can be scored,
		// and the retro says so from the refusal rather than from silence.
		r.record(tick, StagePredictionScored,
			"the run cannot score its predictions: %s", refusal.Reason)
		return nil
	}

	oracle := gating.NewOracle(evidenceRunner{r: r, commands: commands})
	for _, record := range records {
		// Only a PREDICTION is scored. An observed absorption is what a
		// command said; a non-gating prediction and the fallback named no
		// item, so no one command checks them — all three are reported by
		// the retro as what they are, never half-scored here.
		if record.Basis != runstate.AbsorptionPredicted || !record.Gating || record.ItemID == "" {
			continue
		}
		// Scored once, however many incarnations the close-out takes: the
		// standing record IS the label, and a resume reads it rather than
		// re-running the item's command.
		if _, ok, err := r.store.PredictionScore(record.Key); err != nil {
			return err
		} else if ok {
			continue
		}
		// The item the prediction named, against the table as it stands now.
		// An item that never became runnable is never scored — the retro
		// reports the prediction as unchecked, which absence says and a
		// label never would.
		item, runnable := scoredItem(done, record.ItemID)
		if !runnable {
			continue
		}

		// The item's OWN command, through the oracle: one item, one run,
		// keyed by the commit it ran on — the branch head as origin has it,
		// the tree the close-out hands over.
		observed, _, err := oracle.Observe(ctx, gating.Finding{ID: record.Key},
			acceptance.Done{Items: []acceptance.Resolved{item}})
		if err != nil {
			return fmt.Errorf("run the done's command for %s to score the prediction of finding %s: %w",
				record.ItemID, record.Key, err)
		}
		prediction := gating.Verdict{
			FindingID:  record.Key,
			Gating:     record.Gating,
			ItemID:     record.ItemID,
			Basis:      gating.BasisPredicted,
			Confidence: record.Confidence,
			Fallback:   record.Fallback,
			Reason:     record.Reason,
		}
		checked, ok := gating.ScorePrediction(prediction, observed)
		if !ok {
			// No evidence about the item: unchecked, never guessed either way.
			continue
		}
		observation, ok := observationOf(observed, record.ItemID)
		if !ok {
			return fmt.Errorf("the scored prediction of finding %s has no observation of the item %s it named",
				record.Key, record.ItemID)
		}

		score := runstate.PredictionScore{
			Key:             record.Key,
			ItemID:          record.ItemID,
			PredictedGating: true,
			Confidence:      record.Confidence,
			Fallback:        record.Fallback,
			Score:           string(checked.Score),
			Reason:          checked.Reason,
			Check:           observation.Evidence.Check,
			Commit:          observed.Commit,
			Result:          observation.Evidence.Result,
			ExitCode:        observation.Evidence.ExitCode,
			Output:          observation.Evidence.Output,
			StartedAt:       observation.Evidence.StartedAt,
			FinishedAt:      observation.Evidence.FinishedAt,
			ScoredAt:        r.now().UTC().Format(time.RFC3339),
		}
		tickID := tick
		score.Provenance = r.provenance(&tickID, nil, runstate.PhaseCloseout, "")
		outcome, err := r.store.PutPredictionScore(score)
		if err != nil {
			return err
		}
		if !outcome.EffectPermitted() {
			// Another incarnation of this run scored this prediction between
			// the read and the write. Theirs is the label: a prediction is
			// scored once, and the record a race leaves is the record the
			// retro reads.
			continue
		}
		r.record(tick, StagePredictionScored,
			"the prediction that finding %s gates item %s — %s, basis %s%s — was scored %s against the run: %s",
			record.Key, record.ItemID, verdictLine(record), record.Basis, confidenceLine(record), checked.Score,
			checked.Reason)
	}
	return nil
}

// scoredItem is the item the prediction named as the done holds it now:
// resolved by id, runnable only when the evidence table binds it to a command
// — the same state the absorption decision resolved, one incarnation later.
func scoredItem(done acceptance.Done, id string) (acceptance.Resolved, bool) {
	for _, item := range done.Items {
		if item.ID == id {
			return item, item.State == acceptance.Runnable
		}
	}
	return acceptance.Resolved{}, false
}

// observationOf is the item's own observation in the observed verdict it was
// scored against.
func observationOf(observed *gating.Observed, itemID string) (gating.Observation, bool) {
	if observed == nil {
		return gating.Observation{}, false
	}
	for _, observation := range observed.Items {
		if observation.ItemID == itemID {
			return observation, true
		}
	}
	return gating.Observation{}, false
}
