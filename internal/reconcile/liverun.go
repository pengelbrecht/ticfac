package reconcile

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A finding whose remedy is a LIVE RUN never becomes a worker's tick (epic
// 2jn, 2026-09-27).
//
// 2jn's review reported tm5: "Demonstrate 2jn's A1 and A5 live: ticfac init +
// ticfac run in herdr, and one epic via run --cloud". It claimed done item A1,
// the absorption judged it gating, and it became a child of the running epic —
// a tick the run then dispatched to a pi worker, who cannot run a whole epic,
// in herdr, on claude, from inside a tick. A person stopped the worker by pid
// and moved the tick out by hand. The absorption was right that the done was
// unproven, and wrong about who can prove it: the remedy is the NEXT run,
// which is not this epic's work at all.
//
// So the run's own RULE decides such a finding, before either gating tier is
// asked — the same shape as a finding routed to another repository
// (routed.go): it gates nothing here whatever done item it claims, and the
// record says so (Basis rule). It becomes a BACKLOG TICK outside the epic,
// labelled liveRunLabel and titled for the next epic run, carrying the finding
// verbatim — so the next run of an epic in the way it names is where it is
// satisfied, and the epic PR lists it for its reviewer (pr_reviewer.go).

// liveRunLabel marks a backlog tick whose remedy is a live run of an epic —
// the label the next epic run's operator (or agent) finds them by:
// `tk list --label live-run`.
const liveRunLabel = "live-run"

// liveRunDemonstration is the REMEDY half of the rule, read in the finding's
// TITLE — the title is what the finding asks for: to demonstrate, or run,
// something live.
var liveRunDemonstration = regexp.MustCompile(
	`\b(demonstrat\w*|live (run|demo\w*)|run \w+ live|(prove|show|exercise)\w* \S+(\s\S+){0,6} live)\b`)

// liveRunSubject is the SUBJECT half, read in the title and body together:
// what is to be run is an epic, or the product on a real substrate — never a
// function, a test or a command a worker can run in its worktree.
var liveRunSubject = regexp.MustCompile(
	`\b(ticfac (init|run)\b|run-epic\b|run --cloud\b|(an|one|the next|another|a whole|a real) epic\b|` +
		`real substrate|live substrate|end to end on a real)`)

// needsLiveRun reports whether a finding's remedy is a live run of an epic:
// its title asks for a demonstration or a live run, and what is to be run is
// an epic or the product on a real substrate. Both halves, because either
// alone is ordinary work — "demonstrate the parser handles X" is a test a
// worker writes, and "ticfac run crashes on Y" is a bug a worker fixes.
func needsLiveRun(finding runstate.Finding) bool {
	title := strings.ToLower(finding.Title)
	if !liveRunDemonstration.MatchString(title) {
		return false
	}
	return liveRunSubject.MatchString(title + "\n" + strings.ToLower(finding.Body))
}

// liveRunReason is the reasoning the decision record carries.
func liveRunReason(finding runstate.Finding) string {
	claim := "it claims no done item of this epic"
	switch item := strings.TrimSpace(finding.DoneItem); {
	case item == "":
	case strings.EqualFold(item, "none"):
		claim = "its reporter claims it breaks no done item"
	default:
		claim = fmt.Sprintf("its reporter claims done item %s is unproven without it", item)
	}
	return fmt.Sprintf("the finding's remedy is a live run — running an epic, or the product on a real substrate — "+
		"which no worker inside this epic can do: %s, and it gates none of this epic's done items here, because "+
		"absorbing it would dispatch a worker at work only the next epic run can do. It is a backlog tick outside "+
		"the epic, labelled %s, for the next epic run to satisfy", claim, liveRunLabel)
}

// isLiveRun reports whether a decision record is this rule's.
func isLiveRun(record runstate.Absorption) bool {
	return record.Placement == runstate.AbsorptionNextRun
}

// decideLiveRunFinding records the rule's decision and finishes behind it: a
// backlog tick outside the epic, promoted from the finding with nobody
// triaging.
func (r *Reconciler) decideLiveRunFinding(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	dispatch Dispatch) (findingDecision, error) {
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     false,
		Basis:      runstate.AbsorptionRule,
		Placement:  runstate.AbsorptionNextRun,
		Reason:     liveRunReason(standing),
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	// The same create-if-absent record and finish the routed rule uses: a
	// decision is made once, and a concurrent incarnation's record stands.
	return r.recordRoutedDecision(ctx, marker, standing, record)
}
