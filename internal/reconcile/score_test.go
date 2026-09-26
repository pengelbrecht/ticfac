package reconcile

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The self-measurement (tick jlv), end to end against the same harness every
// other absorption guarantee is pinned with: a prediction made while an
// acceptance item was unrunnable becomes checkable when the item becomes
// runnable, the close-out runs the item's command, and the prediction is
// scored against what the done actually did — both halves recorded on the run
// branch, for the retro to report and for a later measurement across epics.
//
// THE CASES, one per acceptance clause:
//
//  1. a prediction whose item became runnable is SCORED against the run —
//     both directions, the run agreeing (correct) and the run disagreeing
//     (incorrect, the evidence the threshold's next reader needs);
//  2. both halves are recorded, and the score survives on the run branch —
//     scored once, however many incarnations the close-out takes;
//  3. a prediction whose item never became runnable, and an OBSERVED
//     absorption, are never scored: the first is reported unchecked, the
//     second was never a guess.
//
// The item "becomes runnable" here the way it does in a real epic: the
// runners.toml the run reads gains the [evidence.acceptance] binding — the
// epic's own work binds the command that proves the item, and the cut between
// the legs is the moment the binding lands.

// scoringGate is passingGate with the item the fixture's predicted absorption
// names NOW BOUND: [A2] was unverified when the classifier predicted over it,
// and the done's command — passing here, deliberately — is what the close-out
// runs to score the prediction against.
const scoringGate = passingGate + `
[evidence.commands]
done = { command = "test -f README.md", description = "the done's check, bound to the predicted item" }

[evidence.acceptance]
A2 = "done"
`

// scoringGateBroken is scoringGate with the done's command FAILING: the
// close-out runs it, it answers non-zero, and the prediction was right — the
// done does not demonstrate the item on the tree the finding was made on.
const scoringGateBroken = passingGate + `
[evidence.commands]
done = { command = "exit 3", description = "the done's check, broken at the close-out" }

[evidence.acceptance]
A2 = "done"
`

// scoringGateFor is scoringGate with the done's command answered ONLY by the
// absorbed tick's own work: the file its fake worker commits is the fix the
// prediction bought, so the command passes on the tree the close-out hands
// over and fails on the tree the finding was made on — the pair that proves a
// correct gating prediction is scored against the finding's own tree rather
// than against the repair (tick ce4, finding cfd74936).
func scoringGateFor(tickID string) string {
	return passingGate + fmt.Sprintf(`
[evidence.commands]
done = { command = "test -f work-%s.txt", description = "the done's check, satisfied only by the absorbed tick's fix" }

[evidence.acceptance]
A2 = "done"
`, tickID)
}

// scoredPrediction drives the fixture's one finding to a PREDICTED absorption
// naming item [A2], cut the moment the decision is durable — the classifier
// answered while nothing was bound — and returns the standing record read
// from origin. The warm leg's options are the caller's, so a test that
// declares the PR rule or supplies a forge runs the same cut.
func scoredPrediction(t *testing.T, f *fixture, classifier *fakeGatingClassifier, warm fixtureOptions) runstate.Absorption {
	t.Helper()
	warm.mode = "finding_local"
	warm.gatingClassifier = classifier
	if warm.stopAfter == nil {
		warm.stopAfter = stopAt("a1", StageAbsorbed)
	}
	_, _, err := f.run(f.Repo, warm)
	if _, ok := err.(*killedAt); !ok {
		t.Fatalf("the warm run ended with %v, not the kill after %s", err, StageAbsorbed)
	}
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("the warm run recorded %d absorption decision(s), want exactly one: %+v", len(records), records)
	}
	record := records[0]
	if !record.Gating || record.ItemID != "A2" || record.Basis != runstate.AbsorptionPredicted {
		t.Fatalf("the warm run's decision is %+v, want predicted gating over item A2 — nothing was bound, so the "+
			"item was the classifier's to predict", record)
	}
	// The decision names the model that answered it (tick ce4, finding
	// b8137057): the scores the close-out grades against this record are per
	// model, and a record that cannot say which model guessed is a label the
	// later measurement cannot calibrate with.
	if record.Model != "jev-2026-09" {
		t.Errorf("the decision names the answering model %q, want the classifier's own jev-2026-09", record.Model)
	}
	return record
}

// 1a. THE ITEM BECAME RUNNABLE, and the done demonstrated it: the prediction
// was WRONG, recorded as wrong — the only evidence anyone will ever have for
// where the absorb threshold belongs. The score is made exactly once: the
// close-out is cut the moment the label lands, the resumed run reads the
// standing record and scores nothing again, and the pair survives on the run
// branch.
func TestAPredictionIsScoredAgainstTheRunWhenItsItemBecomesRunnable(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.7, "none": 0.3}, "A2")}
	record := scoredPrediction(t, f, classifier, fixtureOptions{})

	// The item BECOMES RUNNABLE: the binding the epic's own work would write
	// lands between the legs, and the classifier's question becomes a
	// command the oracle can run.
	write(t, filepath.Join(f.Repo.Dir, ".tick", "runners.toml"), scoringGate)

	// The close-out is cut the moment the score is durable — before the
	// close-out job itself is dispatched, which is where the retro that
	// reports the score is written.
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier,
		stopAfter: stopAt("co", StagePredictionScored)})
	if _, ok := err.(*killedAt); !ok {
		t.Fatalf("the resumed run ended with %v, not the kill after %s", err, StagePredictionScored)
	}

	// The resumed close-out completes, and scores nothing twice.
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("the final run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}

	// THE LABELLED PAIR, both halves, on the run branch: the prediction
	// (gating, item, confidence) and the outcome (the command, the commit it
	// ran on, what it answered), joined by the finding's key to the
	// absorption whose prediction this grades.
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	scores, err := store.PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 1 {
		t.Fatalf("the run scored %d prediction(s), want exactly one — the one whose item became runnable: %+v",
			len(scores), scores)
	}
	score := scores[0]
	if score.Key != record.Key {
		t.Errorf("the score is keyed %q, want the absorption's finding key %q: the pair is joined on it",
			score.Key, record.Key)
	}
	if score.ItemID != "A2" {
		t.Errorf("the score names item %q, want A2: the check is of the item the prediction named", score.ItemID)
	}
	if !score.PredictedGating {
		t.Error("the score is of a prediction that broke no item: it said the finding gates the done")
	}
	if score.Confidence != record.Confidence || score.Confidence == 0 {
		t.Errorf("the score carries confidence %.2f, want the prediction's own %.2f: calibrating the threshold is "+
			"calibrating against this number", score.Confidence, record.Confidence)
	}
	if score.Score != runstate.PredictionScoreIncorrect {
		t.Errorf("the score is %q, want %s: the done demonstrated the item, so the prediction was wrong",
			score.Score, runstate.PredictionScoreIncorrect)
	}
	if score.Result != "pass" || score.Check.ID != "done" {
		t.Errorf("the outcome is %s via %s, want the done's command passing", score.Result, score.Check.ID)
	}
	if score.Commit == "" {
		t.Error("the score is keyed by no commit: a label with nothing under it is a timestamped opinion")
	}
	// THE SCORE IS KEYED BY THE TREE THE FINDING WAS MADE ON (tick ce4,
	// finding cfd74936) — the discovering dispatch's base — never the tree
	// the close-out hands over, which carries the absorbed fix the
	// prediction drove: scoring against that tree measures the repair, not
	// the classifier.
	if score.Commit != record.Provenance.SourceSHA {
		t.Errorf("the score is keyed by commit %s, want %s — the tree the finding was made on, not the tree the close-out hands over",
			score.Commit, record.Provenance.SourceSHA)
	}
	// The score names the model whose prediction it grades, carried from the
	// absorption record so the labelled pairs are per model.
	if score.Model != record.Model || score.Model == "" {
		t.Errorf("the score names the answering model %q, want the absorption's own %q: a score nobody can attribute to the model that guessed is a label the later measurement cannot calibrate with",
			score.Model, record.Model)
	}
	if score.ScoredAt == "" {
		t.Error("the score carries no scored_at")
	}

	// The feed said so, at the close-out, exactly once across every
	// incarnation — the resumed close-out read the standing record and
	// scored nothing again.
	events := feedStages(t, f.Repo.Dir, r.RunID())
	if got := countStage(events, StagePredictionScored); got != 1 {
		t.Errorf("the feed carries %d %s lines, want the one the scoring pass left", got, StagePredictionScored)
	}
	line := detailOfStage(events, StagePredictionScored)
	if !strings.Contains(line, "incorrect") || !strings.Contains(line, record.Key) {
		t.Errorf("the scoring line does not name the label and the finding: %q", line)
	}
}

// 1b. THE RUN AGREED: the item's command answered fail on the tree the
// finding was made on, so the done does not demonstrate the item while the
// finding stands, and the prediction was RIGHT.
func TestAPredictionTheRunAgreesWithIsScoredCorrect(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.7, "none": 0.3}, "A2")}
	scoredPrediction(t, f, classifier, fixtureOptions{})

	write(t, filepath.Join(f.Repo.Dir, ".tick", "runners.toml"), scoringGateBroken)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("the resumed run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}

	scores, err := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID()).PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 1 {
		t.Fatalf("the run scored %d prediction(s), want one: %+v", len(scores), scores)
	}
	if scores[0].Score != runstate.PredictionScoreCorrect {
		t.Errorf("the score is %q, want %s: the item's command failed, so the done does not demonstrate the item",
			scores[0].Score, runstate.PredictionScoreCorrect)
	}
	if scores[0].Result != "fail" || scores[0].ExitCode == 0 {
		t.Errorf("the outcome is %+v, want the done's command failing", scores[0])
	}
}

// 1c. THE FIX THE PREDICTION BOUGHT IS ON THE TREE THE CLOSE-OUT HANDS OVER,
// and the prediction is still scored on the tree the finding was made on
// (tick ce4, finding cfd74936): a CORRECT gating prediction scores CORRECT.
// The item's command — bound between the legs, the way the epic's own work
// binds it — is satisfied only by the absorbed tick's own work, so it fails
// on the finding's tree and passes on the close-out's. The old scoring ran it
// on the branch head, read the pass as the prediction being wrong, and a
// correct "this gates" answer scored INCORRECT — the label measured the
// repair, not the classifier.
func TestACorrectGatingPredictionIsScoredOnTheTreeTheFindingWasMadeAbout(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.7, "none": 0.3}, "A2")}
	record := scoredPrediction(t, f, classifier, fixtureOptions{})

	// The item becomes runnable against a command only the ABSORBED TICK's
	// fix satisfies: the prediction bought exactly that fix.
	write(t, filepath.Join(f.Repo.Dir, ".tick", "runners.toml"), scoringGateFor(record.TickID))

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("the resumed run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}

	// THE FIX IS ON THE TREE THE CLOSE-OUT HANDS OVER: the pass the old
	// scoring would have read as INCORRECT is one command away on the branch
	// head — the premise the label below is meaningless without.
	branch := r.IntegrationBranch()
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "cat-file", "-e",
		"refs/heads/"+branch+":work-"+record.TickID+".txt") {
		t.Fatalf("the absorbed tick's fix is not on the tree the close-out hands over: the fixture's premise — a command that passes on %s because the fix landed — does not hold", branch)
	}

	scores, err := openRunStore(t, f.Repo.Dir, branch, r.RunID()).PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 1 {
		t.Fatalf("the run scored %d prediction(s), want one: %+v", len(scores), scores)
	}
	score := scores[0]
	if score.Score != runstate.PredictionScoreCorrect {
		t.Errorf("the score is %q, want %s: the item's command failed on the tree the finding was made on, so the done does not demonstrate the item while the finding stands and the finding was gating as predicted",
			score.Score, runstate.PredictionScoreCorrect)
	}
	if score.Result != "fail" {
		t.Errorf("the outcome is %q on commit %s, want the done's command failing: the command is satisfied only by the fix the branch carries, so a fail names the tree the finding was made on",
			score.Result, score.Commit)
	}
	if score.Commit != record.Provenance.SourceSHA {
		t.Errorf("the score is keyed by commit %s, want %s — the tree the finding was made on (the discovering dispatch's base), never the tree carrying the absorbed fix",
			score.Commit, record.Provenance.SourceSHA)
	}
	if score.Model != record.Model || score.Model == "" {
		t.Errorf("the score names the answering model %q, want the absorption's own %q: the labelled pairs are per model",
			score.Model, record.Model)
	}
}

// 3a. THE ITEM NEVER BECAME RUNNABLE: the prediction stays unchecked — no
// score record exists, because nothing was run for the item and nothing is
// guessed either way. The retro reports the absence, which absence says and a
// label never would.
func TestAPredictionWhoseItemNeverBecomesRunnableIsNotScored(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	classifier := &fakeGatingClassifier{result: answerOver(t, map[string]float64{"A2": 0.7, "none": 0.3}, "A2")}

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", gatingClassifier: classifier})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Basis != runstate.AbsorptionPredicted {
		t.Fatalf("the decision is %+v, want predicted: the item was never bound", record)
	}
	scores, err := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID()).PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 0 {
		t.Fatalf("the run scored %d prediction(s) over an item that never became runnable: %+v", len(scores), scores)
	}
}

// 3b. AN OBSERVED ABSORPTION IS NOT A PREDICTION: the oracle answered when the
// finding was decided, and scoring a measurement against itself would dress an
// echo up as calibration data.
func TestAnObservedAbsorptionIsNotAScoredPrediction(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{gate: observedGate, mode: "finding_local"})
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, fixtureOptions{gate: observedGate, mode: "finding_local"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	record := absorbingAbsorption(t, f.Repo, r)
	if record.Basis != runstate.AbsorptionObserved {
		t.Fatalf("the decision is %+v, want observed", record)
	}
	scores, err := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID()).PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 0 {
		t.Fatalf("the run scored %d prediction(s) over an observed absorption: %+v", len(scores), scores)
	}
}

// The tree a prediction is scored on is the tree the finding was made on —
// the discovering dispatch's base, recorded on the absorption decision's own
// provenance (tick ce4, finding cfd74936) — never the tree the close-out hands
// over, which carries the absorbed fix the prediction drove. The wiring is one
// field of one record already in memory; the end-to-end cases above prove the
// close-out runs the item's command on it.
//
// short: the tree is a field of a record already in memory
func TestTheTreeAPredictionIsScoredOnIsTheTreeTheFindingWasMadeAbout(t *testing.T) {
	t.Parallel()
	about := "9f1c2ab37de49f1c2d5e"
	record := runstate.Absorption{Key: "dc02fb31", Provenance: runstate.Provenance{SourceSHA: about}}
	if got := predictionTree(record); got != about {
		t.Errorf("the prediction is scored on %q, want %s — the tree the finding was made on, never the tree "+
			"carrying the absorbed fix", got, about)
	}
}
