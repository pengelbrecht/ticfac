package runstate

import (
	"fmt"
	"sort"
	"strings"
)

// The amendment record, written and read through the same compare-and-swap
// every other decision record here is: the proposal is create-if-absent on
// origin (a repeat is refused by the repository and read back as the
// original), and the operator's decision is a sha-guarded update —
// attributed, and never made twice.

// PutAmendment files one worker-proposed amendment to the epic's record.
// Create-if-absent: the same amendment — the same record, field and value —
// re-proposed by a later attempt, or re-filed by a resumed run, is ONE
// confirmation, and the original stands with the attempt that first proposed
// it. The run files this record only when the tracker write it records has
// succeeded, so a resume that re-applies the amendment as a no-op re-files
// this as the same no-op.
func (s *Store) PutAmendment(a Amendment) (Outcome, error) {
	a.SchemaVersion = SchemaVersion
	if err := a.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(a.Provenance); err != nil {
		return "", err
	}
	if a.Status != AmendmentPending {
		return "", fmt.Errorf("runstate: a new amendment is %s, not %q; the decision is the operator's",
			AmendmentPending, a.Status)
	}
	content, err := encodeRecord(a)
	if err != nil {
		return "", err
	}
	outcome, err := s.CreateIfAbsent(AmendmentPath(s.runID, a.Key), content)
	if err != nil {
		return "", err
	}
	if outcome.EffectPermitted() {
		return outcome, nil
	}
	// The record already exists under this key: an earlier incarnation filed
	// this amendment, or a later attempt re-proposed it. The ORIGINAL stands —
	// including the attempt that first proposed it and any decision the
	// operator already made on it — and nothing new is filed.
	_, ok, err := s.Amendment(a.Key)
	if err != nil || !ok {
		return outcome, fmt.Errorf("runstate: run %s has an amendment record %s that cannot be read: %v %v",
			s.runID, a.Key, ok, err)
	}
	return outcome, nil
}

// Amendment returns one amendment record by its key.
func (s *Store) Amendment(key string) (*Amendment, bool, error) {
	if err := checkSegment("amendment key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var a Amendment
	ok, err := s.load(AmendmentPath(s.runID, key), &a)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &a, true, nil
}

// Amendments returns every amendment record of the run, ordered by key — the
// close-out's gate and the operator's listing both read the same set.
func (s *Store) Amendments() ([]Amendment, error) {
	out := []Amendment{}
	for _, key := range s.amendmentKeys() {
		a, ok, err := s.Amendment(key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *a)
		}
	}
	return out, nil
}

// AmendmentDecision is the operator's word on one amendment: confirm (it
// stands as theirs) or reject (the close-out names the record must be
// repaired or the rejection withdrawn).
type AmendmentDecision struct {
	Status string
	By     string
}

// DecideAmendment records the operator's decision on one amendment.
//
// The rules the decision keeps, held here in the same shape a finding's
// triage keeps them (TriageFinding):
//   - a PENDING amendment is decidable, once. A confirmed amendment is final
//     — the operator's confirmation is the word the close-out hands over
//     behind, and it is never undone by a second decision;
//   - a REJECTED amendment may be CONFIRMED later, and only confirmed: the
//     one reopening, mirroring the one a fixed finding's re-report performs.
//     A rejection says the record must be repaired; the repair is a person's
//     through the tracker's own writer or a tick's, and once it has landed
//     the operator revisits the amendment as confirmed — the rejection
//     already said no twice, so there is no deciding back into it;
//   - the decision names WHO made it. A decision nobody can attribute is one
//     nobody can audit;
//   - nothing but the decision moves. The amendment is the worker's words;
//     a decision that could rewrite them is a channel for scope after the
//     fact.
func (s *Store) DecideAmendment(key string, d AmendmentDecision) (Outcome, Amendment, error) {
	status, by := d.Status, d.By
	if _, err := s.Fetch(); err != nil {
		return "", Amendment{}, err
	}
	if by == "" {
		return "", Amendment{}, fmt.Errorf("runstate: a decision on amendment %s names no author: a decision "+
			"nobody can attribute is one nobody can audit", key)
	}
	if status != AmendmentConfirmed && status != AmendmentRejected {
		return "", Amendment{}, fmt.Errorf("runstate: a decision on amendment %s is %s or %s, not %q",
			key, AmendmentConfirmed, AmendmentRejected, status)
	}
	current, ok, err := s.Amendment(key)
	if err != nil {
		return "", Amendment{}, err
	}
	if !ok {
		return "", Amendment{}, fmt.Errorf("runstate: run %s has no amendment record %s", s.runID, key)
	}
	if current.Status == AmendmentConfirmed {
		// Already confirmed, by this operator or another. The confirmation is
		// the word the close-out hands over behind; it is never made twice
		// and never undone.
		return NoChange, *current, nil
	}
	if current.Status == AmendmentRejected && status != AmendmentConfirmed {
		return "", Amendment{}, fmt.Errorf("runstate: amendment %s is rejected; the one decision left to it is "+
			"to confirm a record repaired since — reject it again is no decision at all", key)
	}

	decided := *current
	decided.Status = status
	decided.DecidedAt = s.stamp()
	decided.DecidedBy = by
	if err := decided.Validate(); err != nil {
		return "", Amendment{}, fmt.Errorf("runstate: %w", err)
	}
	if !current.isDecisionOf(decided) {
		return "", Amendment{}, fmt.Errorf("runstate: deciding amendment %s would change more than the "+
			"decision: the amendment is the worker's words and is not editable from here", key)
	}
	content, err := encodeRecord(decided)
	if err != nil {
		return "", Amendment{}, err
	}
	outcome, err := s.UpdateIfSHA(AmendmentPath(s.runID, key), content)
	if err != nil {
		return "", Amendment{}, err
	}
	switch outcome {
	case Updated:
		return outcome, decided, nil
	case ConflictStaleSHA:
		// Another decision landed between the fetch and the write — the
		// operator's other agent, or a second terminal. Theirs is the
		// decision: re-read it and report it as the outcome rather than
		// retrying, the same rule a finding's triage keeps.
		if _, err := s.Fetch(); err != nil {
			return "", Amendment{}, err
		}
		standing, ok, err := s.Amendment(key)
		if err != nil || !ok {
			return outcome, Amendment{}, err
		}
		return NoChange, *standing, nil
	default:
		return outcome, *current, nil
	}
}

func (s *Store) amendmentKeys() []string {
	prefix := RunDir(s.runID) + "/amendments/"
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
