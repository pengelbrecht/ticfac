package reconcile

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Findings against an epic whose acceptance is PROSE (epic-6in, 2026-09-28).
//
// An epic whose acceptance carries no [A<n>] items has a definition of done
// nothing can be pointed at, so the gating tiers (the oracle, the predictor)
// have nothing to judge a finding against. Until this file, that refusal
// (klq) left every such finding "for a person": 6in's run filed fifteen, each
// "left for a person", and the close-out's findings gate then held the whole
// run for a triage nobody was there to make. That is a step whose only actor
// is a person, and it is not a judgement at all — there is no item to judge
// against. So the run's own RULE decides, the way it decides a routed finding
// (routed.go) or one whose remedy is a live run (liverun.go):
//
//   - the reporter's claim cannot gate what has no items, so the finding
//     becomes a BACKLOG TICK with an owner, and the decision record says why;
//   - EXCEPT a finding whose reporter claims it breaks the BUILD or CI. A red
//     build fails every done, prose or not — the epic PR cannot merge over it
//     — so that claim is a gating defect, and the finding is ABSORBED into the
//     running epic, placed before the review or the close-out as any gating
//     finding is. 6in's own close-out filed one: "A re-run under a new run id
//     holds forever on the claim its stopped predecessor left (dz1
//     regression; full suite red)".
//
// The rule reads the reporter's CLAIM, in the title — what the reporter says
// the finding is — and never guesses past it: a finding that merely mentions
// CI ("forge.CI reads a skipped head as green") claims nothing about the
// build being red.

// buildBreakageClaim matches a title that claims the build or CI is broken:
// the suite, the build, CI or the gate red, failing or broken; something that
// breaks the build; code that fails to build or compile.
var buildBreakageClaim = regexp.MustCompile(
	`\b(full suite|short suite|test suite|suite|build|ci|gate|go vet|gofmt)\s+(is\s+|goes\s+|went\s+|turns\s+|stays\s+|now\s+)?` +
		`(red|broken|fail(s|ing|ed)?)\b` +
		`|\bbreaks?\s+(the\s+)?(build|ci|gate|suite)\b` +
		`|\b(fails?|failing)\s+to\s+(build|compile)\b` +
		`|\b(does\s+not|doesn't|no\s+longer)\s+(build|compile)\b` +
		`|\b(red|failing|broken)\s+(ci|build|suite|gate)\b`)

// claimsBuildBreakage reports whether a finding's reporter claims it breaks
// the build or CI.
func claimsBuildBreakage(finding runstate.Finding) bool {
	return buildBreakageClaim.MatchString(strings.ToLower(finding.Title))
}

// proseReason is the reasoning the prose rule's decision record carries.
func proseReason(finding runstate.Finding, refusal string, gating bool) string {
	if gating {
		return fmt.Sprintf("the epic's acceptance is prose (%s), so no item can be pointed at — but the reporter "+
			"claims this finding breaks the build or CI (%q), and a red build fails every definition of done, "+
			"prose or not: it is a gating defect, absorbed into the running epic", refusal, finding.Title)
	}
	return fmt.Sprintf("the epic's acceptance is prose (%s), so there is no item the reporter's claim could gate "+
		"and no judgement for a person to make: the finding is backlog work, filed as a tick with an owner. The "+
		"reporter does not claim it breaks the build or CI, the one claim that gates a prose done", refusal)
}

// decideProseFinding records the prose rule's decision about one finding and
// finishes behind it: a backlog tick, or — for a claimed build breakage — a
// child of the running epic placed before the items are asserted. The same
// create-if-absent record and finish every rule decision uses: a decision is
// made once, and a concurrent incarnation's record stands.
func (r *Reconciler) decideProseFinding(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	dispatch Dispatch, refusal string) (findingDecision, error) {

	gating := claimsBuildBreakage(standing)
	if gating {
		// A gating verdict extends the absorption chain, and the chain is
		// bounded whatever decided it (tick qjj), and past the bound it is
		// deferred to the backlog — the run never halts over it.
		links, bound, exceeded, err := r.boundedChain(standing, dispatch)
		if err != nil {
			return findingDecision{}, err
		}
<<<<<<< HEAD
		bound, err := r.absorptionDepthBound(dispatch)
		if err != nil {
			return findingDecision{}, err
		}
		if absorptionDepthExceeded(links, bound) {
			r.record(marker.TickID, StageAbsorptionBoundExceeded,
				"the absorption of finding %s would be the %s absorption of one chain and the bound is %d: %s",
				standing.Key, ordinal(len(links)+1), bound, chainNarrative(links))
			return findingDecision{}, r.refuse(RefusedAbsorptionDepth, marker.TickID,
				"absorbing the finding %s (%q), which claims to break the build or CI, would be the %s absorption "+
					"of ONE chain that already carries %d and the bound is %d (tick qjj). The chain: %s. %s. Raise "+
					"the bound with --absorption-depth and run the epic again instead",
				standing.Key, standing.Title, ordinal(len(links)+1), len(links), bound, chainNarrative(links),
				triagePointer(r.opts.EpicID, r.runID))
=======
		if exceeded {
			return r.deferPastBound(ctx, marker, standing, dispatch, links, bound,
				"breaks the build or CI, which gates any epic's done (its reporter's claim)")
>>>>>>> 90b4cd7d49ae9e892fbd3d3e3237596b79e9a51b
		}
	}
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	placement := runstate.AbsorptionBacklog
	if gating {
		if review, open := r.reviewTick(ctx); review != "" && open {
			placement = runstate.AbsorptionBeforeReview
		} else {
			placement = runstate.AbsorptionAfterReview
		}
	}
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     gating,
		Basis:      runstate.AbsorptionRule,
		Placement:  placement,
		Reason:     proseReason(standing, refusal, gating),
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	return r.recordRoutedDecision(ctx, marker, standing, record)
}

// decideUndecidedFindings takes every finding of this run still waiting for a
// triage through the decision again, before the close-out is dispatched: a
// finding an older build left "for a person" because the epic's acceptance is
// prose (epic-6in's fifteen), or one whose decision was cut short. Each is
// decided exactly as at its filing — decideFinding, idempotent behind any
// record already written — so the close-out's findings gate meets decisions,
// not a queue for a person, and a finding absorbed here is an open child the
// close-out waits behind like any other.
//
// A finding routed to another repository is left to the close-out's own
// disposal (routed.go), which already runs before it gates.
func (r *Reconciler) decideUndecidedFindings(ctx context.Context) error {
	untriaged, err := r.untriagedFindings()
	if err != nil {
		return fmt.Errorf("read the run's drafted findings: %w", err)
	}
	if len(untriaged) == 0 {
		return nil
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return err
	}
	for _, finding := range untriaged {
		if finding.Target != "" && !r.isThisRepository(finding.Target) {
			continue
		}
		marker := attemptHandle{TickID: finding.TickID, Attempt: finding.Attempt}
		for _, attempt := range attempts {
			if attempt.TickID == finding.TickID && attempt.Attempt == finding.Attempt {
				marker = handleFromMap(attempt.JobHandle)
				if marker.TickID == "" {
					marker.TickID = attempt.TickID
				}
				if marker.Attempt == 0 {
					marker.Attempt = attempt.Attempt
				}
				break
			}
		}
		dispatch, err := r.dispatchFor(marker)
		if err != nil {
			return err
		}
		if _, err := r.decideFinding(ctx, marker, finding.Key, dispatch); err != nil {
			return err
		}
	}
	return nil
}
