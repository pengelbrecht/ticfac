package runstate

import "strings"

// Outcome is what a run-state operation did, in the vocabulary
// contracts/ticfac-run-state.json's `cas.fake.ops` declares. It is a value, not
// an error: a compare-and-swap that refuses has answered a question about the
// world correctly.
type Outcome string

const (
	// Fetched: the store refreshed its view of origin.
	Fetched Outcome = "fetched"
	// NoChange: an observation. A poll that learns nothing writes nothing —
	// checkpoint on state change, not on observation.
	NoChange Outcome = "no_change"
	// LocalOnly: written into the local repository and durable to nobody.
	LocalOnly Outcome = "local_only"

	// Created: the path was absent from origin and now holds this content.
	Created Outcome = "created"
	// Updated: origin's blob sha matched the one the writer fetched, and the
	// path now holds this content.
	Updated Outcome = "updated"

	// ConflictExists: the path is already on origin. Another reconciler got
	// there first — the effect this guard protects must NOT happen.
	ConflictExists Outcome = "conflict_exists"
	// ConflictStaleSHA: origin moved under this writer. Its view of the run is
	// stale; re-fetch and reconcile from what is actually there, never retry
	// blindly.
	ConflictStaleSHA Outcome = "conflict_stale_sha"
	// ConflictMissingBase: the writer holds no fetched sha for the path. A
	// writer that never read origin cannot compare against it.
	ConflictMissingBase Outcome = "conflict_missing_base"
)

// IsConflict reports whether the compare-and-swap refused.
func (o Outcome) IsConflict() bool { return strings.HasPrefix(string(o), "conflict_") }

// EffectPermitted reports whether the caller may now perform the effect the
// write guards — start the job, pay for the model call, trust the record.
//
// This is the load-bearing half of the contract: the loser of a dispatch race
// is refused by the repository, and must not dispatch anyway.
func (o Outcome) EffectPermitted() bool { return !o.IsConflict() }
