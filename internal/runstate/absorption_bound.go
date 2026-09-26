package runstate

import (
	"fmt"
)

// The absorption recursion's recorded bound (tick qjj set the bound; tick wz0
// records it). --absorption-depth is a flag on the invocation, and an
// invocation is not a run: a warm run started with a raised bound and
// cold-restarted without the flag would stop at the default over the same git
// state it had already absorbed past — the bound a decision was already made
// under, silently rewritten by a restart. So the bound the run applies is a
// record on the run branch, written at the first decision that needs it,
// create-if-absent: the FIRST incarnation's bound is the run's, and every
// later incarnation — warm or cold, flag or no flag — reads it back before
// deciding anything under it (finding 95f5ee1a).
//
// The ONE exception is a person's explicit raise: the depth refusal's own
// escape hatch is "raise the bound with --absorption-depth and run the epic
// again", so an invocation that names the flag explicitly OVERRIDES the
// record, updating it — a record that out-ranked the person would turn the
// documented escape hatch into a no-op. The override is a guarded update
// under the same sha rule every update here is: a racing writer that moved
// the record first wins, and the loser reads back what stands.

// AbsorptionBound is the recorded absorption recursion bound of the RUN, at
// `.ticfac/runs/<run-id>/absorption-bound.json` — exactly one per run,
// written when the bound first governs a decision.
type AbsorptionBound struct {
	SchemaVersion int `json:"schema_version"`
	// RunID is the run whose bound this is — the record's own identity,
	// restated beside the provenance the envelope already carries.
	RunID string `json:"run_id"`
	// Bound is how many absorptions ONE chain may carry before the run
	// stops for a person. Always at least one: the effective bound, never
	// the raw flag — zero or below was already replaced by the default
	// before the run recorded anything.
	Bound int `json:"bound"`
	// RecordedAt is when the run recorded the bound, RFC3339.
	RecordedAt string `json:"recorded_at"`

	Provenance Provenance `json:"provenance"`
}

// Validate applies the record's own rules: a bound that is at least a bound,
// a run it belongs to, and a time it was recorded — the same discipline every
// decision record here is held to, so a cold restart reading the record can
// trust what it says without the writer beside it.
func (b AbsorptionBound) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("absorption bound schema_version is %d, want %d", b.SchemaVersion, SchemaVersion)
	}
	if b.RunID == "" {
		return fmt.Errorf("absorption bound names no run: a bound belongs to one run, and a record that cannot say " +
			"which is a bound nobody can apply")
	}
	if b.Bound < 1 {
		return fmt.Errorf("absorption bound of %d carries %d: the effective bound is at least one, and zero or below "+
			"is the default wearing a record's shape — never an unbounded recursion quietly recorded", b.Bound, b.Bound)
	}
	if b.RecordedAt == "" {
		return fmt.Errorf("absorption bound has no recorded_at")
	}
	return b.Provenance.validate()
}

// PutAbsorptionBound records the run's bound, create-if-absent: the first
// incarnation's bound is the run's, and a second writer racing the first is
// refused by the repository and reads back the standing record.
func (s *Store) PutAbsorptionBound(b AbsorptionBound) (Outcome, error) {
	b.SchemaVersion = SchemaVersion
	if err := b.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(b.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(b)
	if err != nil {
		return "", err
	}
	return s.CreateIfAbsent(AbsorptionBoundPath(s.runID), content)
}

// UpdateAbsorptionBound rewrites the recorded bound — the person's explicit
// raise, under the same sha-guarded update the checkpoint is: a racing writer
// that moved the record first wins, and the loser re-reads what stands rather
// than overwriting it.
func (s *Store) UpdateAbsorptionBound(b AbsorptionBound) (Outcome, error) {
	b.SchemaVersion = SchemaVersion
	if err := b.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(b.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(b)
	if err != nil {
		return "", err
	}
	return s.UpdateIfSHA(AbsorptionBoundPath(s.runID), content)
}

// AbsorptionBound reads the run's recorded bound, if there is one.
func (s *Store) AbsorptionBound() (*AbsorptionBound, bool, error) {
	var b AbsorptionBound
	ok, err := s.load(AbsorptionBoundPath(s.runID), &b)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &b, true, nil
}
