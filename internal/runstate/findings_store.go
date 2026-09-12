package runstate

import (
	"fmt"
	"sort"
	"strings"
)

// The findings draft, written and read through the same compare-and-swap every
// other record here is: the proposal is create-if-absent on origin (a repeat
// is refused by the repository and read back as the original), and the triage
// is a sha-guarded update (a person's decision, attributed and never rewritten
// twice).

// PutFinding proposes one findings draft. Create-if-absent: the finding's key
// is its dedup identity, so a repeat of the same finding — from a later
// attempt of the same tick, which is the normal shape, since the finding
// recurs until it is fixed — is ConflictExists and the ORIGINAL stands, with
// the attempt that first reported it.
func (s *Store) PutFinding(f Finding) (Outcome, error) {
	f.SchemaVersion = SchemaVersion
	if err := f.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(f.Provenance); err != nil {
		return "", err
	}
	if f.Status != FindingProposed {
		return "", fmt.Errorf("runstate: a new finding is proposed, not %q; the triage is a person's decision", f.Status)
	}
	content, err := encodeRecord(f)
	if err != nil {
		return "", err
	}
	return s.CreateIfAbsent(FindingPath(s.runID, f.Key), content)
}

// Finding returns one draft by its key.
func (s *Store) Finding(key string) (*Finding, bool, error) {
	if err := checkSegment("finding key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var f Finding
	ok, err := s.load(FindingPath(s.runID, key), &f)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &f, true, nil
}

// Findings returns every draft in the run, ordered by key.
func (s *Store) Findings() ([]Finding, error) {
	out := []Finding{}
	for _, key := range s.findingKeys() {
		f, ok, err := s.Finding(key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *f)
		}
	}
	return out, nil
}

// TriageFinding records a person's decision on one draft: promote (with the
// tick the promotion created, and the repository it was routed to when the
// finding targeted another one) or discard.
//
// The rules the funnel's own human gate keeps, held here:
//   - only a PROPOSED draft is triaged. A decision is never made twice — a
//     second attempt to decide an already-decided draft is NoChange, because
//     "what the human did with it" is the one thing a redelivery must not
//     reopen;
//   - a triage names WHO made it. A decision nobody can attribute is one
//     nobody can audit — the same discipline a settlement's release answers;
//   - nothing but the triage moves. The discovery is the worker's report; a
//     triage that could rewrite it is a channel for scope after the fact.
func (s *Store) TriageFinding(key string, status, by, promotedAs string) (Outcome, Finding, error) {
	if _, err := s.Fetch(); err != nil {
		return "", Finding{}, err
	}
	if by == "" {
		return "", Finding{}, fmt.Errorf("runstate: a triage of finding %s names no author: a decision nobody can "+
			"attribute is one nobody can audit", key)
	}
	if !oneOf(status, FindingStatuses) || status == FindingProposed {
		return "", Finding{}, fmt.Errorf("runstate: a triage is %s or %s, not %q",
			FindingPromoted, FindingDiscarded, status)
	}
	if status == FindingPromoted && promotedAs == "" {
		return "", Finding{}, fmt.Errorf("runstate: promoting finding %s names no tick: the promotion is the tick "+
			"a person created, and the draft must say which", key)
	}
	if status == FindingDiscarded && promotedAs != "" {
		return "", Finding{}, fmt.Errorf("runstate: discarding finding %s names a promoted tick: a triage is one "+
			"decision, not two", key)
	}

	current, ok, err := s.Finding(key)
	if err != nil {
		return "", Finding{}, err
	}
	if !ok {
		return "", Finding{}, fmt.Errorf("runstate: run %s has no findings draft %s", s.runID, key)
	}
	if current.Status != FindingProposed {
		// Already decided, by this person or another incarnation. The
		// decision stands: the funnel's rule, which is what stops a
		// redelivery reopening whatever the human did with the original.
		return NoChange, *current, nil
	}

	triaged := *current
	triaged.Status = status
	triaged.TriagedAt = s.stamp()
	triaged.TriagedBy = by
	triaged.PromotedAs = promotedAs
	if err := triaged.Validate(); err != nil {
		return "", Finding{}, fmt.Errorf("runstate: %w", err)
	}
	if !current.isTriageOf(triaged) {
		return "", Finding{}, fmt.Errorf("runstate: triaging finding %s would change more than the decision: the "+
			"discovery is the worker's report and is not editable from here", key)
	}
	content, err := encodeRecord(triaged)
	if err != nil {
		return "", Finding{}, err
	}
	outcome, err := s.UpdateIfSHA(FindingPath(s.runID, key), content)
	if err != nil {
		return "", Finding{}, err
	}
	switch outcome {
	case Updated:
		return outcome, triaged, nil
	case ConflictStaleSHA:
		// Another triager decided between the fetch and the write. Theirs is
		// the decision: re-read it, and report it as the outcome rather than
		// retrying — a triage is a person's decision, never a race to win.
		if _, err := s.Fetch(); err != nil {
			return "", Finding{}, err
		}
		decided, ok, err := s.Finding(key)
		if err != nil || !ok {
			return outcome, Finding{}, err
		}
		return NoChange, *decided, nil
	default:
		return outcome, *current, nil
	}
}

func (s *Store) findingKeys() []string {
	prefix := RunDir(s.runID) + "/findings/"
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
