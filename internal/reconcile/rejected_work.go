package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A rejected attempt that carries work is disposed by the RUN (epic-6in, 823).
//
// THE STALL. On 2026-09-28 attempt 9 of 823 (a pi worker) was stopped by the
// stuck watch during a network outage, collected as missing-result and
// rejected. It had committed work. The rejection stopped the incarnation, the
// supervisor resumed it, and the resume halted on rejected_attempt_carries_work:
// "Read the branch; then take the work, or release the attempt". A person read
// nothing — they typed `ticfac settle --release … --carry-work` — which is a
// mechanical act performed on the run's behalf, and every such act is a defect.
//
// THE RULE. What happens to the commits of a rejected attempt is a function of
// WHY it was rejected, and the collect is the one place that knows why. So the
// collect decides, there, and writes the decision durably BEFORE the rejection
// is recorded — the same settlement record `ticfac settle` writes, attributed
// to the run and the reason, so a resume reads it back through the same path
// a person's release takes and there is no window in which a rejection with
// work is durable and its disposition is not:
//
//   - OPERATIONAL rejections — missing-result: the worker was stopped as stuck,
//     hit its wall clock, its runner died, or it never wrote a report — say
//     nothing about the commits. They are CARRIED: the next attempt is cut
//     from them, exactly as --carry-work does, and the gate still decides what
//     merges.
//   - Rejections ON THE MERITS — a boundary violation, or a collect measured
//     from a base the run did not dispatch — say the commits are not to be
//     trusted. They are RELEASED WITHOUT CARRY: the branch is kept on origin
//     and named on the feed, and the next attempt starts fresh from the
//     integration branch. Carrying "minus the violating paths" would mean the
//     run rewriting commits it did not author, interleaved with ones it
//     cannot vouch for; a carried attempt's commits are held to the boundary
//     again anyway (checkCarriedWork), so carrying would only move the same
//     refusal one attempt later. The next worker still sees the earlier
//     attempt's report (priorReports).
//   - no-commits never reaches here: there is nothing to dispose.
//
// THE BOUND. Each disposal earns the tier ladder its rung, so a tick whose
// attempts keep failing climbs to the ceiling; at the ceiling it gets ONE
// more attempt (the ceiling step, the analogue of tick tyd's decide step), and
// a rejection after that is not disposed — it falls through to the backstop
// hold, which says the ladder is spent. It never loops.
//
// In-run the disposal is a REQUEUE (the window's `again`), not a stop, so the
// supervisor's anti-spin rule — which would read two consecutive collect
// failures of one tick over an unchanged tree as a spin — never sees it.

const (
	// RefusedRejectedRedispatch is a collect that rejected an attempt with
	// work and disposed of that work by itself — carried or released by the
	// rejection's class. The window requeues the tick in-run; should it
	// escape the window, the next incarnation reads the recorded release and
	// dispatches the same way, so it is resumable without a person.
	RefusedRejectedRedispatch = "rejected_attempt_redispatched"

	// StageRejectedWorkCarried: the run released a rejected attempt carrying
	// its work; the next try starts from its commits.
	StageRejectedWorkCarried = "rejected_work_carried"
	// StageRejectedWorkReleased: the run released a rejected attempt WITHOUT
	// its work; the branch is kept and named, the next try starts fresh.
	StageRejectedWorkReleased = "rejected_work_released"

	// The steps of the bound a run release spends.
	rejectedStepEscalate = "escalate"
	rejectedStepCeiling  = "ceiling"

	// runReleaser is who a run release names. It is not a person, so it is
	// recorded as it is rather than as a pseudonymous handle.
	runReleaser = "ticfac"
)

// rejectedWorkHead is the head of the work a rejected attempt committed of
// its OWN — on origin, or only in this checkout — and "" when it committed
// nothing beyond the base it was dispatched at.
func (r *Reconciler) rejectedWorkHead(marker attemptHandle) string {
	if head, err := r.remoteWork(branchOf(marker.WriteRef), marker.BaseSHA); err == nil && head != "" {
		return head
	}
	head := r.attemptWorkHead(marker)
	if head == "" || head == marker.BaseSHA || r.git.contains(head, marker.BaseSHA) {
		return ""
	}
	return head
}

// runReleasesOf is every release the RUN made of an attempt of this tick.
func runReleasesOf(released map[string]settlement, tick string) []settlement {
	var out []settlement
	prefix := tick + "#"
	for key, s := range released {
		if s.byRun && strings.HasPrefix(key, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// disposeRejectedWork is the collect's disposal of a rejected attempt's work.
// It is called BEFORE the rejection is made durable. It returns the refusal
// the collect raises instead of its own — RefusedRejectedRedispatch, which the
// window requeues — or nil when the attempt is not the run's to dispose: it
// committed nothing, its work is already integrated, it is a role job, a
// person already released it, the bound is spent, or the release could not
// be recorded. A nil answer leaves the collect's own refusal, and a resume's
// backstop hold, exactly as they were.
func (r *Reconciler) disposeRejectedWork(ctx context.Context, entry planEntry, marker attemptHandle,
	reason string, carry bool) *Refusal {

	tick := marker.TickID
	if isRoleJob(entry.Role) || isRoleJob(marker.Role) {
		return nil
	}
	head := r.rejectedWorkHead(marker)
	if head == "" || r.integrated(head) {
		return nil
	}
	name := r.attemptName(tick, marker.Attempt)
	branch := branchOf(marker.WriteRef)

	if _, err := r.store.Fetch(); err != nil {
		return nil
	}
	released, err := r.settlements()
	if err != nil {
		return nil
	}
	if was, ok := released[attemptKey(tick, marker.Attempt)]; ok {
		if !was.byRun {
			return nil
		}
		// Already disposed (a collect raised twice): the decision stands.
		return r.rejectedRedispatch(tick, name, was.carry, was.reason, branch)
	}

	step := rejectedStepCeiling
	next := ""
	if tier, up := r.escalatesAbove(ctx, entry, marker); up {
		step, next = rejectedStepEscalate, tier
	} else {
		for _, prior := range runReleasesOf(released, tick) {
			if prior.step == rejectedStepCeiling {
				// The one retry at the ceiling was spent and failed too.
				r.record(tick, StageRejected,
					"%s was rejected (%s) with work on %s (%s), and the run does NOT dispose of it: the tier "+
						"ladder is at its ceiling and the one further try at the ceiling was rejected already. "+
						"It is the bound that keeps a failing tick from looping", name, reason, branch, short(head))
				return nil
			}
		}
	}

	if err := r.recordRunRelease(marker, reason, step, carry, marker.WriteRef, head); err != nil {
		r.record(tick, StageRejected, "the run's release of %s could not be recorded (%v): it is not disposed", name, err)
		return nil
	}

	where := "at the same tier: the ladder is at its ceiling, and this is the one further try it gets"
	if step == rejectedStepEscalate {
		where = fmt.Sprintf("one tier up (%s → %s)", marker.Tier, next)
	}
	if carry {
		r.record(tick, StageRejectedWorkCarried,
			"%s was rejected (%s), an operational failure that says nothing about its commits: the run released "+
				"it carrying its work (%s on %s), and the next try starts from those commits, %s",
			name, reason, short(head), branch, where)
	} else {
		r.record(tick, StageRejectedWorkReleased,
			"%s was rejected on the merits (%s): the run released it WITHOUT its work — the commits stay on %s "+
				"(%s) for anyone to read, and are not carried — and the next try starts fresh from %s, %s",
			name, reason, branch, short(head), r.branch, where)
	}
	return r.rejectedRedispatch(tick, name, carry, reason, branch)
}

func (r *Reconciler) rejectedRedispatch(tick, name string, carry bool, reason, branch string) *Refusal {
	how := "from its commits"
	if !carry {
		how = "fresh, its commits kept on " + branch
	}
	return r.refuse(RefusedRejectedRedispatch, tick,
		"%s was rejected (%s) and released by the run; the tick is dispatched again %s", name, reason, how)
}

// recordRunRelease lands the run's release of a rejected attempt: the record
// `ticfac settle` writes, with the run as the releaser and the rejection as
// the reason, as FIELDS. Create-if-absent per attempt.
func (r *Reconciler) recordRunRelease(marker attemptHandle, reason, step string, carry bool, carryRef, carrySHA string) error {
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if op, _ := existing.Request["op"].(string); op == settleOp &&
			existing.Request["tick_id"] == marker.TickID && decisionAttemptOf(existing) == marker.Attempt {
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return err
	}
	response := map[string]any{
		"settled":     true,
		"released_by": runReleaser,
		"by_run":      true,
		"reason":      reason,
		"step":        step,
		"disposition": dispositionUnaddressable,
		"state":       "rejected",
	}
	if carry {
		response["disposition"] = dispositionCarryWork
		response["carry_ref"] = carryRef
		response["carry_sha"] = carrySHA
	}
	stamp := r.now().UTC().Format(time.RFC3339)
	outcome, err := r.store.PutDecision(runstate.Decision{
		Decision: number,
		Role:     settleRole,
		Request: map[string]any{
			"op":      settleOp,
			"run_id":  r.runID,
			"epic_id": r.opts.EpicID,
			"tick_id": marker.TickID,
			"attempt": marker.Attempt,
			"job_id":  marker.JobID,
			"state":   "rejected",
		},
		Response:    response,
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return err
	}
	if !outcome.EffectPermitted() {
		return fmt.Errorf("decision %d was taken on %s while this release was being recorded", number, r.opts.Remote)
	}
	return nil
}

// rejectionCarries says whether a collect verdict's rejection is OPERATIONAL —
// its commits are carried — or on the merits.
func rejectionCarries(verdict string) bool {
	return verdict != subprocess.VerdictBoundaryViolation
}
