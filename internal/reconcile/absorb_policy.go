package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// THE ABSORPTION POLICY (operator decision 2026-10-06).
//
// WHAT WAS WRONG. Absorbing a worker's finding into the running epic was
// decided by a classifier's prediction: "gates done item A4 (basis predicted,
// confidence 0.44, answered by jev-1.13.0)". Jev has no predictive signal for
// us (docs/classifier-eval-2026-10-04-jev-clef.md: AUC ~0.4), and in epic hn6
// it absorbed low-severity findings at confidence 0.44-0.47 while it
// backlogged a HIGH-severity one ("breaks no item of the done", 0.87). hn6
// grew from 13 planned ticks to 60+ and kept re-opening after its work was
// done.
//
// THE RULE. A finding enters the running epic only on an explicit basis:
//
//   - reviewer: the final reviewer (the review-epic job) judged the epic NOT
//     READY and named the finding blocking — review_rounds.go adopts its tick
//     into the epic and rewrites the decision record with this basis;
//   - worker-asserted-high: the reporter rated the finding HIGH severity and
//     itself named the [A<n>] item of the epic's done it breaks (or, for an
//     epic whose done is prose, claimed it breaks the build or CI, which
//     breaks any done). Asserted by the worker, never predicted by a
//     classifier. And only while the epic's work is under way: once it is
//     done — every implementation tick closed, or the final review already
//     run — the finding is DEFERRED to the reviewer: a backlog tick with an
//     owner, noted on the open review, absorbed only if the reviewer names
//     it blocking on its next round, and otherwise listed in the epic PR
//     under "Deferred findings".
//
// Everything else is backlog-default: a backlog tick with an owner, listed in
// the epic PR. The absorption bound (tick qjj, #204) still bounds every
// absorption the policy makes.
//
// No classifier is asked for the placement. The two-tier verdict this
// replaced (an oracle that ran the done's commands, a predictor that asked
// Jev, and an absorb-anyway fallback) is gone from the decision, and so is
// the close-out's scoring of those predictions (tick jlv), which existed only
// to calibrate the classifier's absorb threshold. Earlier runs' records that
// carry it (basis observed or predicted) still read and are still reported.

// policyDecision is what the policy decided about one finding.
type policyDecision struct {
	Gating    bool
	ItemID    string
	Basis     string
	Placement string
	Reason    string
}

// policyVerdict decides one finding under the policy. prose is the refusal's
// reason when the epic's acceptance carries no [A<n>] items, and empty when
// done enumerates them.
func (r *Reconciler) policyVerdict(ctx context.Context, finding runstate.Finding,
	done acceptance.Done, prose string) (policyDecision, error) {

	asserted, claim := workerAssertedHigh(finding, done, prose)
	if !asserted {
		return policyDecision{
			Basis:     runstate.AbsorptionBacklogDefault,
			Placement: runstate.AbsorptionBacklog,
			Reason:    backlogDefaultReason(finding, done, prose),
		}, nil
	}

	workDone, why, err := r.epicWorkDone(ctx)
	if err != nil {
		return policyDecision{}, err
	}
	if workDone {
		return policyDecision{
			Basis:     runstate.AbsorptionWorkerAssertedHigh,
			Placement: runstate.AbsorptionDeferredToReview,
			Reason: fmt.Sprintf("the reporter rated it high severity and %s, but %s: a finding the epic's own "+
				"work did not need is not absorbed on the reporter's word once that work is done. It is deferred "+
				"to the final reviewer, who absorbs it only by naming it blocking, and is otherwise listed in the "+
				"epic PR under deferred findings", claim, why),
		}, nil
	}

	placement := runstate.AbsorptionAfterReview
	if review, open := r.reviewTick(ctx); review != "" && open {
		placement = runstate.AbsorptionBeforeReview
	}
	item := ""
	if prose == "" {
		item = finding.DoneItem
	}
	return policyDecision{
		Gating:    true,
		ItemID:    item,
		Basis:     runstate.AbsorptionWorkerAssertedHigh,
		Placement: placement,
		Reason: fmt.Sprintf("the reporter rated it high severity and %s, while the epic's work is still under "+
			"way: it is absorbed and fixed before the done is asserted", claim),
	}, nil
}

// workerAssertedHigh is whether the reporter itself asserted the finding
// breaks the done, at high severity: it named an [A<n>] item the epic's done
// carries, or — for a prose done, which has no items — claimed the build or
// CI is broken. claim says which, for the record's reason.
func workerAssertedHigh(finding runstate.Finding, done acceptance.Done, prose string) (bool, string) {
	if finding.Severity != subprocess.FindingSeverityHigh {
		return false, ""
	}
	if prose != "" {
		if claimsBuildBreakage(finding) {
			return true, fmt.Sprintf("claims it breaks the build or CI (%q), which breaks any done, prose or not",
				finding.Title)
		}
		return false, ""
	}
	if !subprocess.ValidFindingDoneItem(finding.DoneItem) {
		return false, ""
	}
	for _, item := range done.Items {
		if item.ID == finding.DoneItem {
			return true, fmt.Sprintf("names done item %s as the one it breaks", finding.DoneItem)
		}
	}
	return false, ""
}

// backlogDefaultReason says why a finding is backlog work, in terms of the
// two explicit bases it lacks.
func backlogDefaultReason(finding runstate.Finding, done acceptance.Done, prose string) string {
	var lacks []string
	if finding.Severity != subprocess.FindingSeverityHigh {
		lacks = append(lacks, fmt.Sprintf("its severity is %s, not high", finding.Severity))
	}
	switch {
	case prose != "":
		if !claimsBuildBreakage(finding) {
			lacks = append(lacks, fmt.Sprintf("the epic's acceptance is prose (%s) and the reporter does not "+
				"claim it breaks the build or CI", prose))
		}
	case !subprocess.ValidFindingDoneItem(finding.DoneItem):
		lacks = append(lacks, "the reporter names no done item it breaks")
	default:
		known := false
		for _, item := range done.Items {
			known = known || item.ID == finding.DoneItem
		}
		if !known {
			lacks = append(lacks, fmt.Sprintf("the done item it names, %s, is not an item of the epic's done",
				finding.DoneItem))
		}
	}
	return fmt.Sprintf("%s. A finding enters the running epic only when the final reviewer names it blocking, "+
		"or when its reporter rates it high severity and names the done item it breaks while the epic's work is "+
		"under way; this one is a backlog tick with an owner, listed in the epic PR", strings.Join(lacks, "; "))
}

// epicWorkDone is whether the epic's work is done, for the policy's late
// rule: the final review has already run (a review decision is recorded, or
// a review tick of the epic is closed), or every implementation tick — every
// child with no role — is closed. why says which, for the record's reason.
func (r *Reconciler) epicWorkDone(ctx context.Context) (bool, string, error) {
	if r.store != nil {
		rounds, err := r.readReviewRounds()
		if err != nil {
			return false, "", err
		}
		if rounds.final != nil {
			tick, _ := rounds.final.Request["tick_id"].(string)
			return true, fmt.Sprintf("the final review has already run (%s, decision %d)", tick,
				rounds.final.Decision), nil
		}
	}
	// The graph with its CLOSED tasks (graphWithClosed): the closed review
	// this looks for is exactly what `tk graph` alone leaves out.
	graph, err := r.graphWithClosed(ctx)
	if err != nil {
		return false, "", fmt.Errorf("read the epic graph of %s to see whether its work is done: %w",
			r.opts.EpicID, err)
	}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role == "review" && task.Status == "closed" {
				return true, fmt.Sprintf("the final review has already run (%s)", task.ID), nil
			}
		}
	}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role == "" && task.Status != "closed" {
				return false, "", nil
			}
		}
	}
	return true, "every implementation tick of the epic is closed", nil
}

// noteDeferralOnReview puts a deferred finding in front of the reviewer that
// judges it: a note on the epic's open review tick — read by the review job
// from its own tracker record — naming the finding by exactly the kind, title
// and target that make the reviewer's re-report the same finding, so a
// reviewer that names it blocking brings this very tick into the epic
// (review_rounds.go). No open review, or the review is the reporter itself:
// nothing to note, and the epic PR lists the finding.
func (r *Reconciler) noteDeferralOnReview(ctx context.Context, marker attemptHandle, finding runstate.Finding,
	record runstate.Absorption) error {
	review, open := r.reviewTick(ctx)
	if review == "" || !open || review == marker.TickID {
		return nil
	}
	current, err := r.tracker.Show(ctx, review)
	if err != nil {
		return fmt.Errorf("read the review %s to note the deferred finding %s: %w", review, record.Key, err)
	}
	if strings.Contains(current.Notes, record.Key) {
		return nil
	}
	note := fmt.Sprintf("ticfac run %s: DEFERRED FINDING for this review's judgement (key %s, backlog tick %s). "+
		"A worker reported it after the epic's work was done: kind %q, title %q, severity high, claiming %s. "+
		"It enters the epic only if you name it blocking: re-report it in your findings block with exactly this "+
		"kind and title, severity high, as a reason for REVIEW-VERDICT: NOT READY. Otherwise it stays backlog "+
		"and is listed in the epic PR.",
		r.runID, record.Key, record.TickID, finding.Kind, finding.Title, finding.LinkageText())
	if _, err := r.tracker.Note(ctx, review, note); err != nil {
		return fmt.Errorf("note the deferred finding %s on the review %s: %w", record.Key, review, err)
	}
	return nil
}

// policyVerdictLine is the verdict of a policy decision as a person reads it;
// ok is false for a record an earlier run decided otherwise.
func policyVerdictLine(record runstate.Absorption) (string, bool) {
	switch record.Basis {
	case runstate.AbsorptionReviewer:
		return "the final review judged the epic NOT READY and named it blocking", true
	case runstate.AbsorptionWorkerAssertedHigh:
		switch {
		case !record.Gating:
			return "its reporter rated it high severity and named the done it breaks, but the epic's work was " +
				"done when it was reported, so it is deferred to the final reviewer", true
		case record.ItemID != "":
			return fmt.Sprintf("gates done item %s, as its reporter asserts at high severity", record.ItemID), true
		default:
			return "breaks the build or CI, which breaks any done, as its reporter asserts at high severity", true
		}
	case runstate.AbsorptionBacklogDefault:
		return "it is not absorbed: only a finding the final reviewer names blocking, or a high-severity one " +
			"whose reporter names the done item it breaks while the work is under way, enters the running epic", true
	}
	return "", false
}

// policyBasis is whether a record was decided under the 2026-10-06 policy.
func policyBasis(basis string) bool {
	return basis == runstate.AbsorptionReviewer || basis == runstate.AbsorptionWorkerAssertedHigh ||
		basis == runstate.AbsorptionBacklogDefault
}

// isDeferredToReview is whether a decision deferred its finding to the
// final reviewer.
func isDeferredToReview(record runstate.Absorption) bool {
	return record.Placement == runstate.AbsorptionDeferredToReview
}
