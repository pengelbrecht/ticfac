package reconcile

import (
	"context"
	"fmt"
	"regexp"
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
	// rejectedStepSuperseded: a close-out answered over red CI, carried onto
	// the repaired tree. It spends no rung of the bound: the attempt did not
	// fail, the CI did, and the repair job is what bounds that loop.
	rejectedStepSuperseded = "superseded"

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
// be recorded. A nil answer leaves the collect's own refusal as it was; a
// resume that meets the rejection with no decision recorded decides it then,
// from the recorded reason (disposeUndecidedRejection) — which is how a role
// job's rejected work is disposed.
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

	if !r.runDispose(ctx, entry, marker, released, head, reason, carry) {
		return nil
	}
	return r.rejectedRedispatch(tick, name, carry, reason, branch)
}

// runDispose is the run's disposal of a rejected attempt's committed work
// (head): the bound checked, the release recorded, the decision said on the
// feed. False when the bound is spent or the release could not be recorded —
// the attempt is then not disposed, and the backstop hold stands. Shared by
// the collect (disposeRejectedWork) and by a resume that finds a rejection
// with work and no decision (disposeUndecidedRejection).
func (r *Reconciler) runDispose(ctx context.Context, entry planEntry, marker attemptHandle,
	released map[string]settlement, head, reason string, carry bool) bool {

	tick := marker.TickID
	name := r.attemptName(tick, marker.Attempt)
	branch := branchOf(marker.WriteRef)
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
				return false
			}
		}
	}

	if err := r.recordRunRelease(marker, reason, step, carry, marker.WriteRef, head); err != nil {
		r.record(tick, StageRejected, "the run's release of %s could not be recorded (%v): it is not disposed", name, err)
		return false
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
	return true
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

// A rejection with work and NO decision is decided on resume (epic-6in v7z).
//
// THE STALL. On 2026-09-28 v7z's close-out (attempt 8) answered BLOCKED over
// red CI and was rejected with its retro committed — before the collect
// disposed of rejected work at all, and through a role-job path that never
// did. The build that resumed it included the disposal, but the disposal is
// made AT REJECTION TIME: a rejection recorded before it existed, or by any
// path that did not write one, has no decision, and the resume fell through to
// the backstop hold. A person typed `ticfac settle --release … --carry-work`.
//
// THE RULE. Such a rejection is decided when the resume reaches it, by the
// same classifier, from the reason the run RECORDED when it rejected the
// attempt — the checkpoint's reason at the moment the tick became rejected at
// that attempt, which is on the run branch in the checkpoint's history. The
// decision is the same run release the collect writes, attributed to the run
// with the reason, under the same bound; role jobs (close-out, review) follow
// the same rule as implement ticks. Only a reason the classifier cannot place
// keeps the hold, and the hold says so.

// rejectedOnResume marks a run release decided on resume rather than by the
// collect that rejected the attempt.
const rejectedOnResume = "decided on resume from the recorded rejection"

// disposeUndecidedRejection decides, on resume, a rejected attempt that
// carries work and has no disposition record. It returns whether the run
// disposed of it (and whether the work is carried, and the reason recorded),
// or, when it did not, why — for the backstop hold to say.
func (r *Reconciler) disposeUndecidedRejection(ctx context.Context, entry planEntry, marker attemptHandle,
	released map[string]settlement) (disposed, carry bool, reason, why string) {

	tick := marker.TickID
	if _, ok := released[attemptKey(tick, marker.Attempt)]; ok {
		return false, false, "", ""
	}
	head := r.rejectedWorkHead(marker)
	if head == "" || r.integrated(head) {
		return false, false, "", ""
	}
	recorded, found := r.recordedRejection(tick, marker.Attempt)
	if !found {
		return false, false, "", " The run found no recorded reason for this rejection in its checkpoint " +
			"history, so it cannot decide whether the work is carried."
	}
	class, carries, ok := classifyRecordedRejection(recorded)
	if !ok {
		return false, false, "", fmt.Sprintf(" The rejection's recorded reason (%q) is none the run can classify "+
			"as operational or on the merits, so it does not decide for itself whether the work is carried.",
			firstLine(recorded))
	}
	reason = class + ", " + rejectedOnResume
	if !r.runDispose(ctx, entry, marker, released, head, reason, carries) {
		return false, false, "", ""
	}
	return true, carries, reason, ""
}

// recordedRejection is the reason the run recorded when it rejected this
// attempt: the first checkpoint in which the tick is rejected at this attempt
// and whose reason names the tick (a checkpoint written for another tick while
// this one sat rejected says nothing about it).
func (r *Reconciler) recordedRejection(tick string, attempt int) (string, bool) {
	history, err := r.store.CheckpointHistory()
	if err != nil {
		return "", false
	}
	names := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(tick) + `($|[^A-Za-z0-9_-])`)
	for _, checkpoint := range history {
		for _, state := range checkpoint.Ticks {
			if state.TickID == tick && state.State == "rejected" && state.Attempt == attempt &&
				names.MatchString(checkpoint.Reason) {
				return checkpoint.Reason, true
			}
		}
	}
	return "", false
}

// recordedVerdict is the collect verdict a rejectDurably reason states.
var recordedVerdict = regexp.MustCompile(`is rejected \(([a-z-]+)\)`)

// classifyRecordedRejection is rejectionCarries over a RECORDED reason rather
// than a live verdict: the class the reason names, whether its work is
// carried, and false when the reason names no class the run can act on.
//
//   - On the merits — a boundary violation, or a collect measured from a base
//     the run did not dispatch: released without carry.
//   - no-commits: NOT classified. That rejection recorded that nothing was
//     committed; work found now arrived after it, and nothing the run recorded
//     says what it is.
//   - Operational — missing-result; a job that stopped to ask (BLOCKED,
//     NEEDS_CONTEXT), which the standing orders answer from its commits; a
//     close-out dispatched over red CI, whose answer is about the tree:
//     carried.
func classifyRecordedRejection(reason string) (string, bool, bool) {
	switch {
	case strings.Contains(reason, "authority that is not its own"),
		strings.Contains(reason, "boundary violation"),
		strings.Contains(reason, subprocess.VerdictBoundaryViolation):
		return subprocess.VerdictBoundaryViolation, false, true
	case strings.Contains(reason, "not the dispatched base"),
		strings.Contains(reason, "not the base this run dispatched"),
		strings.Contains(reason, "collected against base"):
		return "the collected base is not the dispatched base", false, true
	case strings.Contains(reason, subprocess.VerdictNoCommits):
		return "", false, false
	}
	if m := recordedVerdict.FindStringSubmatch(reason); m != nil && m[1] == subprocess.VerdictMissingResult {
		return subprocess.VerdictMissingResult, rejectionCarries(m[1]), true
	}
	switch {
	case strings.Contains(reason, "whose CI is red"), strings.Contains(reason, "over red CI"):
		return "answered over red CI", true, true
	case strings.Contains(reason, "answered "+subprocess.StatusBlocked),
		strings.Contains(reason, "answered "+subprocess.StatusNeedsContext):
		return "stopped to ask", true, true
	case strings.Contains(reason, subprocess.VerdictMissingResult):
		return subprocess.VerdictMissingResult, true, true
	}
	return "", false, false
}

// carryOntoIntegration is the base a carried close-out is cut from: the
// carried head merged onto the integration branch as origin has it (current),
// so the close-out starts from its earlier work AND the epic as it is now.
// When current is already in the carried head there is nothing to merge. The
// merge commit is pushed to the carried attempt's own branch — a fast-forward,
// its first parent is that branch's head — so every executor, local or not,
// can resolve it. A conflict, or a merge that cannot be made durable, falls
// back to the carried head alone, said on the feed.
func (r *Reconciler) carryOntoIntegration(tick string, carried attemptHandle, head, current string) string {
	if current == "" || current == head || r.git.contains(current, head) {
		return head
	}
	name := r.attemptName(carried.TickID, carried.Attempt)
	fallBack := func(why string) string {
		r.record(tick, StageCarried, "the work %s carries (%s) could not be merged onto %s at %s (%s): the next "+
			"try starts from the carried commits alone", name, short(head), r.branch, short(current), why)
		return head
	}
	out, err := r.git.run("", "merge-tree", "--write-tree", "--no-messages", head, current)
	if err != nil {
		return fallBack("they conflict")
	}
	tree := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	merged, err := r.git.run("", "commit-tree", tree, "-p", head, "-p", current, "-m",
		fmt.Sprintf("ticfac: carry the work of %s onto %s", name, r.branch))
	if err != nil {
		return fallBack(firstLine(err.Error()))
	}
	branch := branchOf(carried.WriteRef)
	if _, err := r.git.run("", "push", r.opts.Remote, merged+":"+refFor(branch)); err != nil {
		return fallBack("the merge could not be put on " + r.opts.Remote + ": " + firstLine(err.Error()))
	}
	r.record(tick, StageCarried, "the work %s carries (%s) is merged onto %s at %s (%s, on %s): the next try "+
		"starts from its commits over the epic as it is now", name, short(head), r.branch, short(current),
		short(merged), branch)
	return merged
}

// carriedOntoMerge says some try in marker's carry chain (marker included)
// was cut from its carried work merged onto the integration branch — its base
// is not the head it resumed from.
func (r *Reconciler) carriedOntoMerge(marker attemptHandle) bool {
	seen := map[int]bool{}
	for marker.ResumedFrom != nil && !seen[marker.ResumedFrom.Attempt] {
		if marker.BaseSHA != "" && marker.ResumedFrom.SHA != "" && marker.BaseSHA != marker.ResumedFrom.SHA {
			return true
		}
		seen[marker.ResumedFrom.Attempt] = true
		record, ok, err := r.store.Attempt(marker.ResumedFrom.Attempt)
		if err != nil || !ok {
			return false
		}
		marker = handleFromMap(record.JobHandle)
	}
	return false
}

// carriedPaths is every path the carried tries of marker's chain changed, each
// measured above the base that try was cut from, up to the head the next try
// resumed from — never the integration history a merged base brought in.
func (r *Reconciler) carriedPaths(marker attemptHandle) ([]string, error) {
	seen := map[int]bool{}
	set := map[string]bool{}
	var out []string
	for marker.ResumedFrom != nil {
		from := marker.ResumedFrom
		if seen[from.Attempt] {
			return nil, fmt.Errorf("the carries of %s form a cycle at attempt %d", marker.TickID, from.Attempt)
		}
		seen[from.Attempt] = true
		record, ok, err := r.store.Attempt(from.Attempt)
		if err != nil {
			return nil, err
		}
		if !ok || record.TickID != from.TickID {
			return nil, fmt.Errorf("no marker on %s for %s", r.opts.Remote, r.attemptName(from.TickID, from.Attempt))
		}
		prev := handleFromMap(record.JobHandle)
		if prev.BaseSHA == "" || from.SHA == "" {
			return nil, fmt.Errorf("the marker of %s names no base", r.attemptName(prev.TickID, prev.Attempt))
		}
		diff, err := r.git.run("", "diff", "--name-only", "--no-renames", prev.BaseSHA, from.SHA)
		if err != nil {
			return nil, fmt.Errorf("read the files %s carries between %s and %s: %w",
				r.attemptName(prev.TickID, prev.Attempt), short(prev.BaseSHA), short(from.SHA), err)
		}
		for _, path := range strings.Split(strings.TrimSpace(diff), "\n") {
			if path != "" && !set[path] {
				set[path] = true
				out = append(out, path)
			}
		}
		marker = prev
	}
	return out, nil
}
