package runstate

import (
	"fmt"
	"strings"
)

// The epic-amendment confirmation record (tick 7sn, epic 43y).
//
// THE DEFECT THIS RECORD EXISTS FOR was observed on epic 43y, 2026-10-05:
// tick 8em closed a gap in acceptance item A1 ("every worker, local and
// cloud, runs on pi-durable") by proposing a NOTE on the epic's own record
// that declared the cloud PR-review's omp CLI boot "excepted, on this
// record". The run applied the note through its durable tracker writer — the
// one channel a worker has to the epic's record — and the note read, from
// then on, as part of the record the close-out scores A1 from. But the
// operator's recorded exceptions covered only the LOCAL claude frontier
// rung. A WORKER had amended the epic's definition of done, and nothing
// anywhere required the operator to confirm it: the exception was
// self-ratifying the moment the run applied it.
//
// THE RULE: a note on the epic that a worker proposed is the WORKER'S claim,
// never the operator's word. The run still applies it — a worker must be able
// to record context where the close-out reads it, and the note's visibility
// is the point (the alternative, refusing worker-proposed epic notes
// outright, would push the recording back into code comments, where 8em
// found it) — but the run also files THIS record, and the close-out does not
// hand over while one is undecided: the operator confirms the amendment
// (it stands as theirs) or rejects it (the close-out names that the record
// must be repaired or the rejection withdrawn), at the one decision point
// where a person is already being asked to look.
//
// The record lives at `.ticfac/runs/<run-id>/amendments/<key>.json`, keyed
// by a hash over the amended record, field and value — the SUBJECT, not the
// delivery — so the same amendment re-proposed by a later attempt is the one
// confirmation, and the proposal is create-if-absent like every other
// decision record here: a resumed run re-files what a killed one already
// filed and proposes nothing twice. The operator's decision is a
// sha-guarded update, attributed, in the same shape a finding's triage is.
//
// The vocabulary covers the ONE field the machinery files today — a note on
// the epic, the 8em channel — and is closed on purpose: an
// acceptance_criteria or description edit is a different amendment with
// different guards (the drop rule at the apply), and widening this record
// to cover it is a change this package will name, never one it drifts into.

// AmendmentFieldNotes is the one record field an amendment covers: the
// epic's notes — the free prose the close-out reads the acceptance against.
const AmendmentFieldNotes = "notes"

// AmendmentFields is the closed field vocabulary.
var AmendmentFields = []string{AmendmentFieldNotes}

// The decision states of an amendment.
const (
	// AmendmentPending: applied to the epic's record, awaiting the operator.
	// Holds the close-out.
	AmendmentPending = "pending"
	// AmendmentConfirmed: the operator looked and let the amendment stand —
	// it reads as theirs from the decision on. Clears the close-out's hold.
	AmendmentConfirmed = "confirmed"
	// AmendmentRejected: the operator disowned the amendment. Holds the
	// close-out — the epic does not hand over behind an amendment to its
	// own definition of done that the operator rejected and the record still
	// carries. The repair the hold names: remove or rewrite the amendment
	// through the tracker's own writer (a person's, or a tick's), or confirm
	// the repaired record's state — the one reopening DecideAmendment
	// performs, an operator revisiting after the record was repaired.
	AmendmentRejected = "rejected"
)

// AmendmentStatuses is the closed state vocabulary.
var AmendmentStatuses = []string{AmendmentPending, AmendmentConfirmed, AmendmentRejected}

// Amendment is one worker-proposed amendment to the epic's own record, at
// `.ticfac/runs/<run-id>/amendments/<key>.json`, awaiting — or carrying —
// the operator's decision on it.
type Amendment struct {
	SchemaVersion int `json:"schema_version"`
	// Key is the record's file name and its dedup identity: a hash over the
	// amended record, field and value, so the same amendment from a later
	// attempt is one confirmation.
	Key string `json:"key"`
	// Source names the channel the amendment arrived through — a worker's
	// tracker-edit proposal, applied by the run's own writer.
	Source string `json:"source"`
	// EpicID is the record amended: the epic whose acceptance the close-out
	// scores. Only the epic's own record is here — a note on another tick is
	// that tick's context, not an amendment to the definition of done.
	EpicID string `json:"epic_id"`
	// Field is the record field amended, from the closed vocabulary above.
	Field string `json:"field"`
	// Value is the amended text, verbatim as the run applied it — the
	// operator confirming or rejecting a summary would be confirming prose
	// nobody scored, so the decision is made against the worker's own words.
	Value string `json:"value"`
	// ProposedBy is the tick whose attempt proposed the amendment, and
	// Attempt its number — the provenance of the claim, in the record.
	ProposedBy string `json:"proposed_by"`
	Attempt    int    `json:"attempt"`
	// ProposedAt is when the run applied the amendment and filed this
	// record, RFC3339.
	ProposedAt string `json:"proposed_at"`

	// Status is the decision state; the fields below name the decision.
	Status    string `json:"status"`
	DecidedAt string `json:"decided_at,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// FirstLine is the amendment's own headline for a hold, a listing or a PR
// body: the value's first non-empty line, so a decision point names what the
// amendment says without pasting its whole text into a sentence.
func (a Amendment) FirstLine() string {
	for _, line := range strings.Split(a.Value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Validate applies the record's own rules: the closed field and state
// vocabularies, the decision's completeness (a decided record names who
// decided and when; a pending one names neither), and the same discipline
// every decision record here is held to — a decision nobody can attribute is
// one nobody can audit.
func (a Amendment) Validate() error {
	if a.SchemaVersion != SchemaVersion {
		return fmt.Errorf("amendment schema_version is %d, want %d", a.SchemaVersion, SchemaVersion)
	}
	if err := checkSegment("amendment key", a.Key); err != nil {
		return err
	}
	if a.Source == "" {
		return fmt.Errorf("amendment names no source: a record that cannot say what channel it arrived through " +
			"cannot be deduplicated against its next delivery")
	}
	if a.EpicID == "" {
		return fmt.Errorf("amendment names no epic: the record amended is the one the close-out scores, and a " +
			"record of nothing amends nothing")
	}
	if !oneOf(a.Field, AmendmentFields) {
		return fmt.Errorf("amendment field %q is not one of %v", a.Field, AmendmentFields)
	}
	if strings.TrimSpace(a.Value) == "" {
		return fmt.Errorf("amendment carries no value: the operator decides against the worker's own words, and " +
			"there are none")
	}
	if a.ProposedBy == "" || a.Attempt < 1 {
		return fmt.Errorf("amendment names no proposing tick or attempt: a claim nobody can attribute is one " +
			"nobody can audit")
	}
	if a.ProposedAt == "" {
		return fmt.Errorf("amendment has no proposed_at")
	}
	if !oneOf(a.Status, AmendmentStatuses) {
		return fmt.Errorf("amendment status %q is not one of %s", a.Status, strings.Join(AmendmentStatuses, ", "))
	}
	decided := a.Status != AmendmentPending
	if decided != (a.DecidedBy != "") || decided != (a.DecidedAt != "") {
		return fmt.Errorf("amendment %s names its decision fields half in and half out (decided_by %q, "+
			"decided_at %q): a decision is attributed in full or not made", a.Status, a.DecidedBy, a.DecidedAt)
	}
	return a.Provenance.validate()
}

// isDecisionOf reports whether the decided record differs from the pending
// one only in the decision: the amendment itself — the worker's words, the
// proposing attempt, the provenance — is the claim under judgement and is
// never editable from the decision.
func (a Amendment) isDecisionOf(decided Amendment) bool {
	pending := a
	pending.Status, pending.DecidedAt, pending.DecidedBy = AmendmentPending, "", ""
	moved := decided
	moved.Status, moved.DecidedAt, moved.DecidedBy = AmendmentPending, "", ""
	return pending == moved
}
