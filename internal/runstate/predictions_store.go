package runstate

import (
	"fmt"
	"sort"
	"strings"
)

// The prediction score record, written and read through the same
// compare-and-swap every other decision record here is: create-if-absent on
// origin, keyed by the finding's dedup key, so the prediction is scored once
// however many incarnations the run takes to score it.

// PutPredictionScore records one checked prediction. Create-if-absent: a
// prediction is scored once — the close-out that reaches this a second time
// (a resumed run, a requeue over a blocked tick) reads the standing record and
// proposes nothing new. A second writer racing the first is refused by the
// repository and reads back the original, exactly as an absorption decision is.
func (s *Store) PutPredictionScore(p PredictionScore) (Outcome, error) {
	p.SchemaVersion = SchemaVersion
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(p.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(p)
	if err != nil {
		return "", err
	}
	outcome, err := s.CreateIfAbsent(PredictionScorePath(s.runID, p.Key), content)
	if err != nil {
		return "", err
	}
	if outcome.EffectPermitted() {
		return outcome, nil
	}
	// The record already exists under this key: an earlier incarnation of
	// this run scored this prediction. The original stands — the label is
	// never made twice — and the caller reads it back rather than re-scoring.
	current, ok, err := s.PredictionScore(p.Key)
	if err != nil || !ok {
		return outcome, fmt.Errorf("runstate: run %s has a prediction score record %s that cannot be read: %v %v",
			s.runID, p.Key, ok, err)
	}
	_ = current
	return outcome, nil
}

// PredictionScore returns one checked prediction by the finding's key.
func (s *Store) PredictionScore(key string) (*PredictionScore, bool, error) {
	if err := checkSegment("prediction score key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var p PredictionScore
	ok, err := s.load(PredictionScorePath(s.runID, key), &p)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &p, true, nil
}

// PredictionScores returns every checked prediction of the run, ordered by
// key — the close-out's retro reports each beside the absorption whose
// prediction it scores.
func (s *Store) PredictionScores() ([]PredictionScore, error) {
	out := []PredictionScore{}
	for _, key := range s.predictionScoreKeys() {
		p, ok, err := s.PredictionScore(key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *Store) predictionScoreKeys() []string {
	prefix := RunDir(s.runID) + "/predictions/"
	keys := []string{}
	for path := range s.view {
		name, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		if key, ok := strings.CutSuffix(name, ".json"); ok && !strings.Contains(key, "/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
