package runstate

import (
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The findings draft (tick 7vn): a worker's discovery outside its tick, held
// as a DRAFT a person triages, on the model of the signal funnel ticks already
// runs for its declared sources.
//
// THE SHAPE IS THE FUNNEL'S, ON PURPOSE. The funnel turns a source's delivery
// into a draft tick, deduplicated on `(source, external_ref)`, where a
// redelivery comes back as a duplicate naming the original proposal and
// proposes nothing new — whatever the human did with it, including discarding
// it. A worker is exactly such a source: the same finding WILL recur on every
// attempt of the same tick until it is fixed, and a chatty worker must not
// re-propose something already declined. So the draft is:
//
//   - create-if-absent on origin, keyed by the finding's own external_ref —
//     a repeat is refused by the repository and read back as the original,
//     which is what keeps `discovered_from` naming the attempt that FIRST
//     reported it;
//   - TRIAGED by a person — or, since tick npq, by the run's own absorption
//     decision against the epic's definition of done, recorded on the run
//     branch and attributed to the run — and a triage is the one rewrite:
//     promote records the tick that was created (and where, when the finding
//     was routed to another repository), discard records that a person
//     looked and said no, and FIXED records the commit that repaired it
//     inside the epic (tick her) — the verdict a repaired finding needs,
//     because there is no tick to promote it to and a discard would mean the
//     opposite of what happened. A repeat after promote or discard proposes
//     nothing new; a repeat after FIXED is the proof the fix did not hold, and
//     it re-opens the draft — exactly when the run must hear it, never
//     suppress it;
//   - a BLOCKER on the close of the tick whose attempts reported it while it
//     is still `proposed` — a finding nobody triaged is the 604 shape: real,
//     filed only because an orchestrator read that far, and lost the moment
//     one did not.
//
// A draft is NOT a tick record, and this store does not write the tracker:
// `.tick/` has a protected prefix, and a tick marked `draft` would be a tick
// in front of every reader that forgot to filter it. The promotion — the
// human pressing create — is the only thing that makes a tick, and the draft
// carries the `discovered_from` that names the attempt, so a promoted tick is
// never the 9t0 shape: filed with no provenance and no review.

// The triage states of a findings draft.
const (
	// FindingProposed: filed, awaiting a person. Blocks the closing tick.
	FindingProposed = "proposed"
	// FindingPromoted: a person created a tick from it, and `PromotedAs`
	// names it (and the repository it was routed to, when the finding
	// targeted another one).
	FindingPromoted = "promoted"
	// FindingDiscarded: a person looked and said no. A repeat proposes
	// nothing new — the funnel's rule, which is what stops a chatty worker
	// re-proposing something already declined.
	FindingDiscarded = "discarded"
	// FindingFixed: a person recorded that the finding was repaired inside
	// this epic, and `FixedAs` names the commit that repaired it, so the
	// claim is checkable rather than asserted (tick her). Unlike promote and
	// discard, a repeat is NOT suppressed: a finding that comes back after a
	// fixed verdict is the proof the fix did not hold — reverted, lost in a
	// merge, or never landed — and that is exactly when the run must hear it
	// again.
	FindingFixed = "fixed"
)

// FindingStatuses is the closed draft vocabulary.
var FindingStatuses = []string{FindingProposed, FindingPromoted, FindingDiscarded, FindingFixed}

// Finding is one draft, at `.ticfac/runs/<run-id>/findings/<key>.json`, where
// <key> is the finding's own dedup key.
type Finding struct {
	SchemaVersion int `json:"schema_version"`
	// Key is the draft's file name and its dedup identity: the external_ref
	// half of (source, external_ref).
	Key string `json:"key"`
	// Source names the channel the finding arrived through — a worker's
	// report, collected by this run.
	Source string `json:"source"`
	// DiscoveredFrom names the ATTEMPT that first reported the finding, in
	// the reconciler's job-id shape (`run-<run>/tick-<tick>/attempt-<n>`), so
	// the attempt that found a promoted tick is always recoverable.
	DiscoveredFrom string `json:"discovered_from"`

	// The typed finding itself, field for field as the worker reported it.
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	// Target names the repository the finding belongs on, owner/name. Empty
	// means the repository this run works on; a finding targeting another
	// repository is ROUTED there at promotion rather than dropped.
	Target string `json:"target"`

	// TickID is the tick whose attempt reported the finding, and Attempt its
	// number — together with DiscoveredFrom, the provenance of the discovery.
	TickID  string `json:"tick_id"`
	Attempt int    `json:"attempt"`

	// The finding's DONE EVIDENCE (tick nfo): the claim the reporting worker
	// made against the EPIC's definition of done, carried field for field as
	// reported. The claim is never the verdict on whether the finding gates
	// the done — the named check is what the run runs where the item is
	// runnable, one input to a prediction where it is not yet, and the claim
	// is what the reporter is later scored against.
	//
	// DoneItem is the [A<n>] acceptance item the reporter believes the
	// finding breaks, or "none" when the reporter believes it breaks none.
	// DemonstratingCheck is the command or test the reporter says would
	// demonstrate the breakage — one of the repository's declared testing
	// commands where one fits, else the test's name. BOTH are optional at
	// the report: a draft that carries neither is UNLINKED, and is accepted —
	// Linkage states it rather than leaving a reader to infer it from
	// silence, the same visible-third-state rule an unbound acceptance item
	// gets (UNVERIFIED, not false).
	DoneItem           string `json:"done_item,omitempty"`
	DemonstratingCheck string `json:"demonstrating_check,omitempty"`

	// Status is the triage state; the fields below name the triage.
	Status     string `json:"status"`
	ProposedAt string `json:"proposed_at"`
	TriagedAt  string `json:"triaged_at,omitempty"`
	TriagedBy  string `json:"triaged_by,omitempty"`
	// PromotedAs names the tick a promotion created: `<tick-id>` in this
	// repository, `<owner/name>:<tick-id>` in the repository a routed
	// finding targeted.
	PromotedAs string `json:"promoted_as,omitempty"`
	// FixedAs names the commit that repaired a fixed finding: the checkable
	// half of the fixed verdict. Empty for every other status.
	FixedAs string `json:"fixed_as,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// The linkage states of a draft's done-item claim (tick nfo): what the
// reporter said about the epic's definition of done, as a stated fact rather
// than something a reader infers from absent keys.
const (
	// FindingLinkedToAnItem: done_item names the acceptance item the
	// reporter believes the finding breaks — the claim pzp runs the named
	// check against, and bse's classifier takes as one input.
	FindingLinkedToAnItem = "linked"
	// FindingClaimsNoItem: done_item is the reporter's answer that the
	// finding breaks no item. A claim, scored like any other — never the
	// verdict, because the reporter's own claim is never the verdict.
	FindingClaimsNoItem = "none"
	// FindingUnlinked: neither evidence field was reported. Accepted, and
	//	marked — the state an absorption decision treats as "no evidence from
	// the reporter", not as a claim of non-gating.
	FindingUnlinked = "unlinked"
)

// Linkage states what the draft's done_item claim is: linked to an item, the
// reporter's none, or unlinked. Derived from the record rather than stored
// beside it, so the mark cannot drift from the fields it marks.
func (f Finding) Linkage() string {
	switch {
	case subprocess.ValidFindingDoneItem(f.DoneItem):
		return FindingLinkedToAnItem
	case f.DoneItem == subprocess.FindingDoneItemNone:
		return FindingClaimsNoItem
	default:
		return FindingUnlinked
	}
}

// LinkageText is the mark as the surfaces spell it — one wording, shared by
// the event feed, the triage listing and the close-out's PR body, because a
// mark three surfaces spell three ways is three marks.
func (f Finding) LinkageText() string {
	switch f.Linkage() {
	case FindingLinkedToAnItem:
		if f.DemonstratingCheck == "" {
			return "breaks done item " + f.DoneItem
		}
		return fmt.Sprintf("breaks done item %s (demonstrated by %q)", f.DoneItem, f.DemonstratingCheck)
	case FindingClaimsNoItem:
		return "claims to break no done item"
	default:
		return "unlinked: names no done item"
	}
}

// Validate applies the draft's own rules: the closed triage vocabulary, the
// finding's closed kind and severity vocabularies (which are the channel's,
// owned by the executor record), and a triage that names who made it — the
// same discipline a settlement's release is held to, for the same reason: a
// decision nobody can attribute is one nobody can audit.
func (f Finding) Validate() error {
	if f.SchemaVersion != SchemaVersion {
		return fmt.Errorf("finding schema_version is %d, want %d", f.SchemaVersion, SchemaVersion)
	}
	if err := checkSegment("finding key", f.Key); err != nil {
		return err
	}
	if f.Source == "" {
		return fmt.Errorf("finding names no source: a draft that cannot say what channel it arrived through " +
			"cannot be deduplicated against its next delivery")
	}
	if f.DiscoveredFrom == "" {
		return fmt.Errorf("finding names no discovered_from: a draft that cannot say which attempt found it is " +
			"the provenance-less filing this record exists to prevent")
	}
	if !oneOf(f.Kind, subprocess.FindingKinds) {
		return fmt.Errorf("finding.kind %q is not one of %s", f.Kind, strings.Join(subprocess.FindingKinds, ", "))
	}
	if f.Title == "" {
		return fmt.Errorf("finding.title is empty")
	}
	if !oneOf(f.Severity, subprocess.FindingSeverities) {
		return fmt.Errorf("finding.severity %q is not one of %s", f.Severity, strings.Join(subprocess.FindingSeverities, ", "))
	}
	if err := subprocess.ValidateDoneItem(f.DoneItem); err != nil {
		return err
	}
	if f.TickID == "" || f.Attempt < 1 {
		return fmt.Errorf("finding names no tick or attempt: the tick whose worker found it is where a " +
			"finding blocks the close")
	}
	if !oneOf(f.Status, FindingStatuses) {
		return fmt.Errorf("finding status %q is not one of %s", f.Status, strings.Join(FindingStatuses, ", "))
	}
	if f.ProposedAt == "" {
		return fmt.Errorf("finding has no proposed_at")
	}
	switch f.Status {
	case FindingProposed:
		if f.TriagedAt != "" || f.TriagedBy != "" || f.PromotedAs != "" || f.FixedAs != "" {
			return fmt.Errorf("finding is proposed and names a triage: a draft nobody triaged cannot say who did")
		}
	case FindingPromoted, FindingDiscarded, FindingFixed:
		if f.TriagedAt == "" || f.TriagedBy == "" {
			return fmt.Errorf("finding is %s and names neither who triaged it nor when: a decision nobody can "+
				"attribute is one nobody can audit", f.Status)
		}
	}
	if f.Status == FindingPromoted && f.PromotedAs == "" {
		return fmt.Errorf("finding is promoted and names no tick: the promotion is the tick a person created, " +
			"and the draft must say which")
	}
	if f.Status == FindingPromoted && f.FixedAs != "" {
		return fmt.Errorf("finding is promoted and names a fixed commit: a triage is one decision, not two")
	}
	if f.Status == FindingDiscarded && f.PromotedAs != "" {
		return fmt.Errorf("finding is discarded and names a promoted tick: a triage is one decision, not two")
	}
	if f.Status == FindingDiscarded && f.FixedAs != "" {
		return fmt.Errorf("finding is discarded and names a fixed commit: a triage is one decision, not two")
	}
	if f.Status == FindingFixed && f.PromotedAs != "" {
		return fmt.Errorf("finding is fixed and names a promoted tick: a triage is one decision, not two")
	}
	if f.Status == FindingFixed {
		if f.FixedAs == "" {
			return fmt.Errorf("finding is fixed and names no commit: the fixed verdict is the commit that " +
				"repaired it, so the claim is checkable rather than asserted")
		}
		if !isCommitID(f.FixedAs) {
			return fmt.Errorf("finding.fixed_as %q is not a commit id: the fix must be named so it can be "+
				"checked, not asserted", f.FixedAs)
		}
	}
	return f.Provenance.validate()
}

// isCommitID reports whether s is a commit id a person or a run can resolve:
// hexadecimal, abbreviated or full length. The check is shape, not existence
// - the commit may live on any of the repositories a finding can target.
func isCommitID(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// isTriageOf reports whether next is this record with exactly its TRIAGE
// changed: a person decided the draft, and nothing else about it moved. The
// discovery — kind, title, body, severity, target, discovered_from — is what
// the worker reported and is not editable from the triage surface; a triage
// that could rewrite the finding is a channel a worker could smuggle scope
// through after the fact.
func (f Finding) isTriageOf(next Finding) bool {
	before, after := f, next
	before.Status, before.TriagedAt, before.TriagedBy, before.PromotedAs, before.FixedAs = "", "", "", "", ""
	after.Status, after.TriagedAt, after.TriagedBy, after.PromotedAs, after.FixedAs = "", "", "", "", ""
	return before == after
}
