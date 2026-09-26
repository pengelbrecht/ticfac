package runstate

import (
	"fmt"
	"sort"
	"strings"
)

// The absorption decision record, written and read through the same
// compare-and-swap every other record here is: create-if-absent on origin,
// keyed by the finding's dedup key, so the decision is made once however
// many incarnations the run takes to make it.

// PutAbsorption records one absorption decision. Create-if-absent: the
// record is the promotion's idempotency marker, written BEFORE the tick is
// created, so an incarnation killed between the two is resumed by the record
// — the next one re-reads the tick id and finishes behind the decision the
// record carries. A second writer racing the first is refused by the
// repository and reads back the original, exactly as an attempt marker is.
func (s *Store) PutAbsorption(a Absorption) (Outcome, error) {
	a.SchemaVersion = SchemaVersion
	if err := a.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(a.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(a)
	if err != nil {
		return "", err
	}
	outcome, err := s.CreateIfAbsent(AbsorptionPath(s.runID, a.Key), content)
	if err != nil {
		return "", err
	}
	if outcome.EffectPermitted() {
		return outcome, nil
	}
	// The record already exists under this key: a previous incarnation of
	// this run decided this finding. The original stands — the decision is
	// never made twice — and the caller re-reads it to finish what it
	// records (create the tick if the kill came first, complete the triage).
	current, ok, err := s.Absorption(a.Key)
	if err != nil || !ok {
		return outcome, fmt.Errorf("runstate: run %s has an absorption record %s that cannot be read: %v %v",
			s.runID, a.Key, ok, err)
	}
	_ = current
	return outcome, nil
}

// Absorption returns one decision record by the finding's key.
func (s *Store) Absorption(key string) (*Absorption, bool, error) {
	if err := checkSegment("absorption key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var a Absorption
	ok, err := s.load(AbsorptionPath(s.runID, key), &a)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &a, true, nil
}

// Absorptions returns every absorption decision of the run, ordered by key.
func (s *Store) Absorptions() ([]Absorption, error) {
	out := []Absorption{}
	for _, key := range s.absorptionKeys() {
		a, ok, err := s.Absorption(key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (s *Store) absorptionKeys() []string {
	prefix := RunDir(s.runID) + "/absorptions/"
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
