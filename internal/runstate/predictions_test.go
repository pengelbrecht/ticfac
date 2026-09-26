package runstate

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The prediction score record (tick jlv): what the classifier said against
// what the done actually did, one labelled pair per checked prediction, held
// to the same standard the absorption decision record is: durable on origin,
// made exactly once, and honest about what it rests on — the prediction's own
// essentials carried in the record so a later measurement across epics reads
// the pair without walking back to the absorption that made it.
//
// The pure half (Validate) runs everywhere; the durable half (create-if-absent
// on a real origin) is end-to-end, like every other record here.

func testPredictionScore(key string) PredictionScore {
	p := testProvenance(PhaseCloseout)
	p.TickID = Ptr("co")
	return PredictionScore{
		SchemaVersion:   SchemaVersion,
		Key:             key,
		ItemID:          "A2",
		PredictedGating: true,
		Confidence:      0.62,
		Score:           PredictionScoreIncorrect,
		Reason:          "the prediction was WRONG: at the close-out the command done for A2 passed, so the done demonstrates the item as handed over",
		Check:           Check{ID: "done", Kind: "command"},
		Commit:          "9f1c2ab37de4",
		Result:          "pass",
		ExitCode:        0,
		Output:          InlineOutput{Mode: "inline", MaxBytes: 16 << 10},
		StartedAt:       "2026-09-26T09:00:00Z",
		FinishedAt:      "2026-09-26T09:00:01Z",
		ScoredAt:        "2026-09-26T09:00:02Z",
		Provenance:      p,
	}
}

// THE VALIDATE CASES: the closed score vocabulary, the outcome's agreement
// with the score it carries, and the pair's own completeness — a record a
// later measurement reads has to be able to say what was predicted, what ran,
// and what it added up to, without the warm run beside it.
//
// short: Validate over a record already in memory
func TestAPredictionScoreValidates(t *testing.T) {
	t.Parallel()
	// Both labels, each with the outcome that earns it: an incorrect
	// prediction is one the run DISAGREED with (the item was demonstrated),
	// a correct one is one the run agreed with (the item's command failed).
	correct := testPredictionScore("dc02fb31")
	correct.Score, correct.Result = PredictionScoreCorrect, "fail"
	if err := correct.Validate(); err != nil {
		t.Fatalf("a checked prediction scored correct does not validate: %v", err)
	}
	incorrect := testPredictionScore("dc02fb31")
	if err := incorrect.Validate(); err != nil {
		t.Fatalf("a checked prediction scored incorrect does not validate: %v", err)
	}
}

// short: Validate over records already in memory
func TestAPredictionScoreRefusesWhatItCannotSay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		warp func(*PredictionScore)
		want string
	}{
		{"no item", func(s *PredictionScore) { s.ItemID = "" }, "names no item"},
		{"score outside the vocabulary", func(s *PredictionScore) { s.Score = "maybe" }, "not one of"},
		{"no reason", func(s *PredictionScore) { s.Reason = "" }, "carries no reason"},
		{"no check", func(s *PredictionScore) { s.Check = Check{} }, "names no check"},
		{"no commit", func(s *PredictionScore) { s.Commit = "" }, "keyed by nothing"},
		{"no scored_at", func(s *PredictionScore) { s.ScoredAt = "" }, "no scored_at"},
		{"confidence outside a probability", func(s *PredictionScore) { s.Confidence = 1.5 }, "not a probability"},
		{"a score of a prediction that broke no item", func(s *PredictionScore) {
			s.PredictedGating = false
		}, "broke no item"},
		{"an outcome that produced no evidence", func(s *PredictionScore) {
			s.Score, s.Result = PredictionScoreCorrect, "error"
		}, "a command that produced evidence"},
		{"a score the outcome disagrees with", func(s *PredictionScore) {
			s.Score, s.Result = PredictionScoreCorrect, "pass"
		}, "the item was demonstrated"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			record := testPredictionScore("dc02fb31")
			testCase.warp(&record)
			err := record.Validate()
			if err == nil {
				t.Fatalf("%s validated; want the refusal %q", testCase.name, testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("%s refused with %q, want it to name %q", testCase.name, err, testCase.want)
			}
		})
	}
}

// THE DURABLE CASE: the score is create-if-absent keyed by the finding, so a
// close-out cut between the run and the record is resumed by the record's
// absence — the command runs again, the label is written once, and a second
// writer racing the first reads the original back. And the listing sees it,
// ordered by key, so the close-out's retro can report every checked
// prediction without walking the store by hand.
func TestAPredictionScoreIsDurableOnOriginAndScoredOnce(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)

	record := testPredictionScore("dc02fb31")
	outcome, err := s.PutPredictionScore(record)
	if err != nil {
		t.Fatalf("record the prediction score: %v", err)
	}
	if outcome != Created {
		t.Fatalf("outcome %s, want %s", outcome, Created)
	}

	// Read back from ORIGIN, not from this writer's view: the pair is the one
	// record a later measurement across epics reads, and durable means pushed.
	reader := o.actor("reader", testRun)
	if _, err := reader.Fetch(); err != nil {
		t.Fatal(err)
	}
	read, ok, err := reader.PredictionScore("dc02fb31")
	if err != nil || !ok {
		t.Fatalf("the prediction score is not on origin: %v %v", ok, err)
	}
	if read.ItemID != "A2" || read.Score != PredictionScoreIncorrect || read.Result != "pass" ||
		read.PredictedGating != true || read.Confidence != 0.62 {
		t.Fatalf("the record read back as %+v, not the labelled pair that was made", read)
	}

	// A second write of the SAME pair — the killed close-out's resume, or a
	// racing writer — proposes nothing new: the original stands, because a
	// prediction is scored once however many incarnations the run takes.
	again, err := s.PutPredictionScore(record)
	if err != nil {
		t.Fatalf("re-record the prediction score: %v", err)
	}
	if again.EffectPermitted() {
		t.Fatalf("outcome %s, want the repository to refuse a second score under one key", again)
	}
	standing, ok, err := reader.PredictionScore("dc02fb31")
	if err != nil || !ok {
		t.Fatalf("read the standing prediction score: %v %v", ok, err)
	}
	if standing.Key != "dc02fb31" || standing.Score != PredictionScoreIncorrect {
		t.Fatalf("the standing score is %+v, want the original label: a prediction is scored once", standing)
	}

	// And the listing sees it, ordered by key, beside the absorption records
	// whose predictions it scores.
	records, err := reader.PredictionScores()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Key != "dc02fb31" {
		t.Fatalf("the run's prediction scores are %+v, want exactly the one labelled pair", records)
	}
}
