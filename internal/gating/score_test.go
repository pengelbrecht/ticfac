package gating

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The scoring half of the self-measurement (tick jlv): a prediction made while
// an acceptance item was unrunnable becomes checkable the moment that item
// becomes runnable, and the pure half of scoring it — what the classifier said
// against what the done actually did — is a function over the two shapes this
// package already owns (Verdict, Observed), so it runs everywhere the oracle's
// own semantics do.

// testPrediction is the prediction the scoring labels: a classifier's guess
// (or the fallback standing in for one) that the finding breaks item A2.
func testPrediction() Verdict {
	return Verdict{
		FindingID:  "dc02fb31",
		Gating:     true,
		ItemID:     "A2",
		Basis:      BasisPredicted,
		Confidence: 0.62,
		Reason:     "the classifier put 0.70 of its probability mass on the items",
	}
}

// testObserved is what the done actually did: one observation of one item's
// command, keyed by the commit it ran on.
func testObserved(gating bool, result Result, exit int) *Observed {
	return &Observed{
		Verdict: Verdict{FindingID: "dc02fb31", Basis: BasisObserved},
		Commit:  "9f1c2ab37de4",
		Items: []Observation{{
			ItemID: "A2",
			Gating: gating,
			Evidence: Evidence{
				Check:      runstate.Check{ID: "done", Kind: "command"},
				StartedAt:  "2026-09-26T09:00:00Z",
				FinishedAt: "2026-09-26T09:00:01Z",
				ExitCode:   exit,
				Result:     string(result),
			},
		}},
	}
}

// THE LABELLED PAIR, both directions: the run showed the item broken and the
// prediction was right; the run demonstrated the item and the prediction was
// wrong — recorded, never hidden, because a wrong prediction is the only
// evidence anyone will ever have for where the absorb threshold belongs.
func TestAScoredPredictionIsLabelledAgainstWhatTheRunDid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  *Observed
		want Score
	}{
		{"the item was broken, the prediction was right",
			testObserved(true, ResultFail, 3), ScoreCorrect},
		{"the item was demonstrated, the prediction was wrong",
			testObserved(false, ResultPass, 0), ScoreIncorrect},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			checked, ok := ScorePrediction(testPrediction(), testCase.run)
			if !ok {
				t.Fatalf("the prediction over A2 was refused against an observation that produced evidence")
			}
			if checked.Score != testCase.want {
				t.Errorf("the score is %q, want %q", checked.Score, testCase.want)
			}
			// The reason is written for the retro a person reads: it names
			// the item, the command that answered, and the commit the answer
			// ran on, so the label can be re-read against the tree it is
			// about rather than trusted as a bare word.
			for _, want := range []string{"A2", "done", "9f1c2ab37de4"} {
				if !strings.Contains(checked.Reason, want) {
					t.Errorf("the %s reason does not name %q: %q", testCase.want, want, checked.Reason)
				}
			}
		})
	}
}

// ONLY A PREDICTION IS SCORED: an observed verdict is a measurement, and
// scoring a measurement against itself would dress an echo up as calibration
// data.
func TestAnObservedVerdictIsNotAScoredPrediction(t *testing.T) {
	t.Parallel()
	observed := testPrediction()
	observed.Basis = BasisObserved
	if _, ok := ScorePrediction(observed, testObserved(true, ResultFail, 3)); ok {
		t.Error("an observed verdict was scored: only a guess becomes a labelled pair, never a measurement")
	}
}

// A PREDICTION THAT NAMES NO ITEM IS NEVER SCORED: the fallback absorbed
// naming every unverified item at risk in its reason, and a non-gating
// prediction names nothing at all — a score needs the one command that would
// check the claim, and neither shape has one.
func TestAFallbackNamingNoItemIsNeverScored(t *testing.T) {
	t.Parallel()
	for name, warp := range map[string]func(*Verdict){
		"the fallback named no single item": func(v *Verdict) { v.ItemID = "" },
		"a non-gating prediction":           func(v *Verdict) { v.Gating, v.ItemID = false, "" },
	} {
		warp := warp
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			prediction := testPrediction()
			warp(&prediction)
			if _, ok := ScorePrediction(prediction, testObserved(true, ResultFail, 3)); ok {
				t.Errorf("a prediction that names no item was scored: nothing was run for it and nothing was checked")
			}
		})
	}
}

// NOTHING IS GUESSED PAST: no observation, or an observation that does not
// decide the predicted item — the command could not run, was skipped, or the
// item stayed unresolved — means NO score exists, and the prediction is
// reported unchecked rather than labelled either way.
func TestAnUnresolvedItemIsNeverScored(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  *Observed
	}{
		{"nothing was observed at all", nil},
		{"the item's command produced no evidence", &Observed{
			Verdict:    Verdict{FindingID: "dc02fb31", Basis: BasisObserved},
			Commit:     "9f1c2ab37de4",
			Unresolved: []Unresolved{{ItemID: "A2", Reason: "the command could not run"}},
		}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := ScorePrediction(testPrediction(), testCase.run); ok {
				t.Errorf("the prediction was scored over no evidence: a label with nothing under it is a guess wearing a score's clothes")
			}
		})
	}
}

// A score of a DIFFERENT item's observation is not a score of the prediction:
// the pair is the prediction's own item against its own command, and a
// neighbouring item's answer says nothing about it.
func TestAScoresOwnItemDecidesItAndNoOther(t *testing.T) {
	t.Parallel()
	other := testObserved(true, ResultFail, 3)
	other.Items[0].ItemID = "A3"
	if _, ok := ScorePrediction(testPrediction(), other); ok {
		t.Error("the prediction over A2 was scored against A3's observation: the labelled pair is per item, never borrowed")
	}
}
