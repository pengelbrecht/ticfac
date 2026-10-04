package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// THE RECURSION'S BOUND (tick qjj). The criterion in gvc bounds most of the
// absorption already — only goal-gating findings absorb, and a goal is
// finite — but what no criterion can bound is the RECURSION: a defect
// discovered while fixing an absorbed defect, which may itself gate the done,
// and so on. Unbounded, that is an epic that never closes, which in an
// unattended factory is worse than a stop because nothing announces it.
//
// The bound is DEPTH, not wall clock, because depth is the honest measure of
// the failure mode: it counts how far the run has travelled from the epic
// anyone asked for — one link per absorbed defect discovered fixing the
// previous one — while wall clock bounds the resource, and the resource is
// already bounded by the wall clock every attempt carries. Depth is what a
// person must judge, so depth is what the stop counts.
//
// PAST THE BOUND THE RUN DEFERS, IT NEVER HALTS (run_5c7c16d1, epic hn6,
// 2026-10-04). The bound used to be a refusal that held the run for a person:
// hn6's chain gmo → z3p → ulw → qxj reached its 4th link at 06:14 and the run
// stopped, and the operator's whole answer was to file the finding to the
// backlog by hand (km5) — a step whose only actor is a person, which in an
// unattended factory is a defect. So the run takes that step itself: past the
// bound the finding becomes a BACKLOG TICK with an owner, outside the epic —
// what `ticfac triage … =file` does — recorded (placement past-bound, basis
// rule), announced on the feed (finding_backlogged_past_bound) and named in
// the epic PR body as "deferred past the absorption bound", and the run
// carries on. Even a finding that claims to gate a done item is deferred:
// the bound wins, and the close-out and the final review see the deferral
// and can judge it — a NOT READY review that names it blocking still adopts
// it into the epic (review_rounds.go), which is a review's judgement, not a
// recursion.
//
// The deferral carries the FULL ABSORPTION CHAIN that produced it, never only
// the count, so a reviewer can see which finding led to which. If the bound
// trips often in practice, the criterion is wrong and the bound is hiding it —
// the chain is the evidence a person needs to say so.

// chainLink is one link of an absorption chain: a finding an earlier
// absorption decided on one side, the tick that decision created on the other.
// A chain is walked from the tick whose attempt reported the finding at hand:
// was THAT tick created by an absorption? Then this link is the record of the
// finding it absorbed, and the walk continues from the tick that discovered
// THAT finding, until it reaches a tick no absorption created — the epic's
// own plan, the ground the recursion started from.
type chainLink struct {
	// Finding is the finding the link's record decided, as the durable draft
	// reads it — the title and the discovering tick are the chain a person
	// reads. A draft that cannot be read back still counts as a link, with
	// its key alone: the bound governs the recursion's shape, not its prose.
	Finding runstate.Finding
	// Record is the absorption decision that promoted that finding.
	Record runstate.Absorption
}

// absorptionChain walks the chain that produced the tick named, from the
// durable records on the run branch: the absorptions this run recorded and
// the finding drafts beside them. The links come back EARLIEST FIRST — the
// order a person reads the story in — and the walk is bounded by its own
// shape: each link consumes one absorption record, and a visited set over the
// ticks stops a store that somehow describes a cycle from walking it forever.
//
// A decision already recorded is NOT re-judged here: the bound that stood
// when a record was made owns that record, and the chain is the context of
// the NEXT decision, not a second opinion about the old ones.
func (r *Reconciler) absorptionChain(discoveredBy string) ([]chainLink, error) {
	records, err := r.store.Absorptions()
	if err != nil {
		return nil, fmt.Errorf("read the run's absorption records to bound the recursion: %w", err)
	}
	findings, err := r.store.Findings()
	if err != nil {
		return nil, fmt.Errorf("read the run's finding drafts to bound the recursion: %w", err)
	}
	byTick := make(map[string]runstate.Absorption, len(records))
	for _, record := range records {
		byTick[record.TickID] = record
	}
	byKey := make(map[string]runstate.Finding, len(findings))
	for _, finding := range findings {
		byKey[finding.Key] = finding
	}

	var links []chainLink
	visited := map[string]bool{}
	tick := discoveredBy
	for tick != "" && !visited[tick] {
		visited[tick] = true
		record, ok := byTick[tick]
		if !ok {
			// The tick no absorption created: the epic's own plan, where the
			// recursion started — the walk ends here.
			break
		}
		link := chainLink{Finding: runstate.Finding{Key: record.Key}, Record: record}
		if finding, ok := byKey[record.Key]; ok {
			link.Finding = finding
		}
		links = append(links, link)
		tick = link.Finding.TickID
	}
	// Earliest first: the walk started at the finding at hand and walked
	// backwards, so the story a person reads is the reverse of the walk.
	for i, j := 0, len(links)-1; i < j; i, j = i+1, j-1 {
		links[i], links[j] = links[j], links[i]
	}
	return links, nil
}

// absorptionDepthExceeded is the bound's own arithmetic: a chain that already
// carries the bound's worth of links may not carry one more. The depth counts
// absorption links, never findings — a redelivered finding deduplicates and
// extends nothing — and a bound of zero or below never exceeds anything
// because the option's own defaulting replaces it with the real number: a
// misconfigured bound must not silently UNBOUND the recursion.
func absorptionDepthExceeded(links []chainLink, bound int) bool {
	return bound > 0 && len(links) >= bound
}

// isPastBound reports whether a decision record is the bound's deferral.
func isPastBound(record runstate.Absorption) bool {
	return record.Placement == runstate.AbsorptionPastBound
}

// pastBoundReason is the reasoning the deferral's record carries: the bound,
// the chain that reached it, and the verdict the bound overrode.
func pastBoundReason(links []chainLink, bound int, overridden string) string {
	return fmt.Sprintf("absorbing it would be the %s absorption of ONE chain that already carries %d and the "+
		"bound is %d (tick qjj), so it is deferred past the absorption bound: a backlog tick with an owner, "+
		"outside the epic, and the run carries on rather than recurse past the bound or halt for a person. "+
		"Its verdict was that it %s — the bound wins, and the close-out and the final review see the deferral "+
		"and can judge it. The chain: %s",
		ordinal(len(links)+1), len(links), bound, overridden, chainNarrative(links))
}

// boundedChain is the recursion's check for a GATING verdict about to make a
// finding the next link of a chain: the chain that produced the reporting
// tick, the bound this decision applies, and whether the chain is already at
// it.
func (r *Reconciler) boundedChain(standing runstate.Finding, dispatch Dispatch) ([]chainLink, int, bool, error) {
	links, err := r.absorptionChain(standing.TickID)
	if err != nil {
		return nil, 0, false, err
	}
	// The bound THIS decision applies, resolved against the run branch and
	// recorded there (tick wz0, finding 95f5ee1a): a cold restart applies the
	// same bound the warm run did, and only a person's explicit raise changes
	// it.
	bound, err := r.absorptionDepthBound(dispatch)
	if err != nil {
		return nil, 0, false, err
	}
	return links, bound, absorptionDepthExceeded(links, bound), nil
}

// deferPastBound records the bound's deferral and finishes behind it: a
// backlog tick with an owner, promoted from the finding with nobody
// triaging — the same create-if-absent record and finish every rule decision
// uses, so a decision is made once and a concurrent incarnation's stands.
func (r *Reconciler) deferPastBound(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	dispatch Dispatch, links []chainLink, bound int, overridden string) (findingDecision, error) {
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     false,
		Basis:      runstate.AbsorptionRule,
		Placement:  runstate.AbsorptionPastBound,
		Reason:     pastBoundReason(links, bound, overridden),
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	return r.recordRoutedDecision(ctx, marker, standing, record)
}

// ------------------------------------------------- the recorded bound ---

// resolvedAbsorptionDepth is the precedence of the four facts that name the
// bound a decision applies (tick wz0, finding 95f5ee1a), pure so the order is
// pinned without a fixture:
//
//  1. an EXPLICIT flag is the person's raise (--absorption-depth) and it
//     WINS over the record, because a record that out-ranked the person
//     would turn the flag into a no-op;
//  2. otherwise the RECORDED bound is the run's: the first incarnation's
//     bound outlives it, and a cold restart without the flag applies the same
//     bound the warm run did rather than silently dropping back to the
//     default over git state it had already absorbed past;
//  3. otherwise the flag's own value — which the options' defaulting has
//     already made the real number, never a zero that would unbound the
//     recursion.
func resolvedAbsorptionDepth(recorded int, recordedStands bool, flagBound int, explicit bool) int {
	if explicit {
		return flagBound
	}
	if recordedStands {
		return recorded
	}
	return flagBound
}

// absorptionDepthBound resolves the bound THIS decision applies, and makes
// it durable: the resolution above decides, and the outcome is recorded on
// the run branch — create-if-absent where no bound stands, so the first
// incarnation's bound is the run's, and a guarded update where an explicit
// raise overrides a standing one, so the person's escape hatch works after a
// restart too. Both races resolve the same way: the repository's answer
// stands, and this decision reads it back rather than re-deciding.
func (r *Reconciler) absorptionDepthBound(dispatch Dispatch) (int, error) {
	// Origin's view, fetched: a cold restart reads the record the warm run
	// left, and a warm run reads whatever a racing incarnation wrote.
	if _, err := r.store.Fetch(); err != nil {
		return 0, fmt.Errorf("read the run state to resolve the absorption depth bound: %w", err)
	}
	recorded, stands, err := r.store.AbsorptionBound()
	if err != nil {
		return 0, fmt.Errorf("read the recorded absorption depth bound: %w", err)
	}
	bound := resolvedAbsorptionDepth(0, false, r.opts.AbsorptionDepthBound, r.opts.AbsorptionDepthExplicit)
	if stands {
		bound = resolvedAbsorptionDepth(recorded.Bound, true, r.opts.AbsorptionDepthBound, r.opts.AbsorptionDepthExplicit)
		if bound == recorded.Bound {
			// The precedence above adopted the standing record — write
			// nothing: a cold restart honours the recorded bound here, and a
			// warm incarnation without the flag does the same.
			return recorded.Bound, nil
		}
	}

	// The record to write: the person's explicit raise over a standing
	// record (a guarded update, because the record is the run's and the
	// raise must not overwrite a racing writer silently), or the first
	// decision's own bound where none stands yet (create-if-absent, so the
	// first incarnation's bound is the run's).
	boundRecord := runstate.AbsorptionBound{
		RunID:      r.runID,
		Bound:      bound,
		RecordedAt: r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	var outcome runstate.Outcome
	if stands {
		outcome, err = r.store.UpdateAbsorptionBound(boundRecord)
	} else {
		outcome, err = r.store.PutAbsorptionBound(boundRecord)
	}
	if err != nil {
		return 0, fmt.Errorf("record the absorption depth bound %d on the run branch: %w", bound, err)
	}
	if outcome.EffectPermitted() {
		return bound, nil
	}
	// A racing incarnation moved the record between this decision's fetch and
	// its write: theirs stands, and this decision adopts it — never a blind
	// retry over a guarded write.
	if _, err := r.store.Fetch(); err != nil {
		return 0, fmt.Errorf("re-read the run state a racing incarnation of the absorption depth bound: %w", err)
	}
	standing, ok, err := r.store.AbsorptionBound()
	if err != nil || !ok {
		return 0, fmt.Errorf("read the absorption depth bound a concurrent incarnation left: %v %v", ok, err)
	}
	return standing.Bound, nil
}

// chainNarrative is the chain as a person reads it: which tick reported which
// finding, what decided each absorption, and which tick each became — every
// link named, never only their number, because "depth 3" sends nobody
// anywhere while "tick a1's finding became m1, m1's became m2" is the thing
// an operator judges the run by.
func chainNarrative(links []chainLink) string {
	if len(links) == 0 {
		return "no earlier absorption produced the tick that reported it"
	}
	parts := make([]string, 0, len(links))
	for _, link := range links {
		parts = append(parts, fmt.Sprintf(
			"tick %s reported finding %s (%q), %s and became tick %s",
			link.Finding.TickID, link.Finding.Key, link.Finding.Title,
			verdictLine(link.Record), link.Record.TickID))
	}
	return strings.Join(parts, "; ")
}

// ordinal is the bare count as a person reads it: 1st, 2nd, 3rd, 4th…
func ordinal(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return fmt.Sprintf("%dth", n)
	}
	switch n % 10 {
	case 1:
		return fmt.Sprintf("%dst", n)
	case 2:
		return fmt.Sprintf("%dnd", n)
	case 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}
