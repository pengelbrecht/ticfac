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
// the attempt that first reported it. The one exception is a draft whose
// standing triage is FIXED: its repeat is the proof the fix did not hold, and
// it re-opens the draft instead of deduplicating (tick her).
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
	path := FindingPath(s.runID, f.Key)
	outcome, err := s.CreateIfAbsent(path, content)
	if err != nil {
		return "", err
	}
	if outcome.EffectPermitted() {
		return outcome, nil
	}
	// The draft already exists under this key: the funnel's dedup, with ONE
	// exception (tick her). A standing triage of FIXED means a person
	// recorded that the finding was repaired, naming the commit — and a
	// finding reported again after that is the proof the fix did not hold
	// (reverted, lost in a merge, or never landed). That is exactly when the
	// run must hear it: the re-report re-opens the draft as a fresh proposal,
	// discovered by the attempt that re-found it, instead of being swallowed
	// by the dedup that rightly holds for promoted and discarded originals.
	current, ok, err := s.Finding(f.Key)
	if err != nil || !ok {
		return outcome, fmt.Errorf("runstate: run %s has a findings draft %s that cannot be read: %v %v",
			s.runID, f.Key, ok, err)
	}
	if current.Status != FindingFixed {
		return outcome, nil
	}
	return s.UpdateIfSHA(path, content)
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

// Triage is a person's decision on one draft: one of the three verdicts,
// attributed, with the name the verdict requires. A promotion names the tick
// it created (and the repository it was routed to, when the finding targeted
// another one); a fixed verdict names the commit that repaired it; a discard
// names neither.
type Triage struct {
	Status     string
	By         string
	PromotedAs string
	FixedAs    string
}

// TriageFinding records a person's decision on one draft: promote (with the
// tick the promotion created, and the repository it was routed to when the
// finding targeted another one), discard, or FIXED — repaired inside this
// epic, the commit named (tick her).
//
// The rules the funnel's own human gate keeps, held here:
//   - only a PROPOSED draft is triaged. A decision is never made twice — a
//     second attempt to decide an already-decided draft is NoChange. The one
//     reopening a redelivery performs is PutFinding's, on a FIXED draft whose
//     fix did not hold — never a person's decision undone by a second triage;
//   - a triage names WHO made it. A decision nobody can attribute is one
//     nobody can audit — the same discipline a settlement's release answers;
//   - nothing but the triage moves. The discovery is the worker's report; a
//     triage that could rewrite it is a channel for scope after the fact.
func (s *Store) TriageFinding(key string, t Triage) (Outcome, Finding, error) {
	status, by, promotedAs, fixedAs := t.Status, t.By, t.PromotedAs, t.FixedAs
	if _, err := s.Fetch(); err != nil {
		return "", Finding{}, err
	}
	if by == "" {
		return "", Finding{}, fmt.Errorf("runstate: a triage of finding %s names no author: a decision nobody can "+
			"attribute is one nobody can audit", key)
	}
	if !oneOf(status, FindingStatuses) || status == FindingProposed {
		return "", Finding{}, fmt.Errorf("runstate: a triage is %s, %s or %s, not %q",
			FindingPromoted, FindingDiscarded, FindingFixed, status)
	}
	if status == FindingPromoted && promotedAs == "" {
		return "", Finding{}, fmt.Errorf("runstate: promoting finding %s names no tick: the promotion is the tick "+
			"a person created, and the draft must say which", key)
	}
	if status == FindingPromoted && fixedAs != "" {
		return "", Finding{}, fmt.Errorf("runstate: promoting finding %s names a fixed commit: a triage is one "+
			"decision, not two", key)
	}
	if status == FindingDiscarded && promotedAs != "" {
		return "", Finding{}, fmt.Errorf("runstate: discarding finding %s names a promoted tick: a triage is one "+
			"decision, not two", key)
	}
	if status == FindingDiscarded && fixedAs != "" {
		return "", Finding{}, fmt.Errorf("runstate: discarding finding %s names a fixed commit: a triage is one "+
			"decision, not two", key)
	}
	if status == FindingFixed && promotedAs != "" {
		return "", Finding{}, fmt.Errorf("runstate: fixing finding %s names a promoted tick: a triage is one "+
			"decision, not two", key)
	}
	if status == FindingFixed && fixedAs == "" {
		return "", Finding{}, fmt.Errorf("runstate: fixing finding %s names no commit: the fixed verdict records "+
			"the commit that repaired it, so the claim is checkable rather than asserted", key)
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
		// (The exception a redelivery CAN reopen — a fixed draft re-proposed
		// by PutFinding — reads here as a PROPOSED draft again, decidable
		// once more with the fix's failure in front of the person.)
		return NoChange, *current, nil
	}

	triaged := *current
	triaged.Status = status
	triaged.TriagedAt = s.stamp()
	triaged.TriagedBy = by
	triaged.PromotedAs = promotedAs
	triaged.FixedAs = fixedAs
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
