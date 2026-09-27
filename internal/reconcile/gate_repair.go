package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The REPAIR-GATE job (tick wj6, epic gvc).
//
// WHAT WAS WRONG. Twice on epic-yoh, a tick's merge landed on the epic branch
// and the integrated gate failed over it: l6t deleted the wave path while the
// lifecycle contract still pointed at the deleted symbols, and mn7 deleted
// the TypeScript reconciler while a test harness still re-exported the
// deleted class. Both times the run STOPPED for a person, who fixed the tree
// by hand, pushed and resumed. Both fixes were small and mechanical, and the
// gate's own output named the failing test or the exact compiler error. A
// factory that runs unattended cannot have a stop whose only actor is a
// person reading a gate log — the same design defect 2p6 closed for merge
// conflicts, and this file is its sibling, built on the same job mechanism
// the resolve-conflict job already is.
//
// WHAT THIS DOES. On gate_failed over a merge that is already ON the
// integration branch — whoever produced the commit, since the tree is what
// is being repaired — the reconciler dispatches a plan-repair job instead
// of stopping:
//
//   - The job's worktree is cut at the EPIC HEAD, the very tree the gate
//     failed on — which carries the evidence records of the failing checks
//     under the run's own `.ticfac/` path, so the job's prompt names them
//     as inputs and the gate's own output is in the tree the job starts
//     from. The prompt carries the TICK too (its record under
//     `.tick/issues/` is the intent the repair must preserve), and the
//     merge that landed the failing work is in the worktree's own history —
//     the diff a person would have read by hand.
//
//   - The job routes at the POLICY'S CEILING TIER, resolved on demand
//     exactly as the resolve-conflict job's is (locally claude; in the cloud
//     a Workers AI model — the CloudRule guard refuses anything else, so
//     the repair never runs claude in the cloud).
//
//   - Its commits are MERGED AND GATED AS USUAL: an ordinary merge onto the
//     integration branch under the same lease the attempt's merge pushed
//     under, then the full gate again over the repaired tree, then the same
//     close behind a passing gate. The gate is the verdict, not the repair.
//
// WHAT STILL STOPS FOR A PERSON, as the tick's acceptance says. ONE repair
// per tick — durably recorded as a decision on the run branch, so a second
// gate failure over the same tick stops naming the recorded repair rather
// than paying for another. A repair that failed without answering at all
// does not spend it (role_allowance.go): another is dispatched, from the fix
// it committed when it committed one, up to a bound. And a repair whose gate ALSO fails is the stop,
// as today, naming BOTH failures: the one the repair was dispatched over
// and the one the repaired tree produced.

// RoleRepairGate is the job-protocol role this file dispatches: the closed
// vocabulary's repair slot.
const RoleRepairGate = profile.RoleRepairGate

// repairFailedGate answers a failed gate with the repair job, and returns
// nil once the repair's merge has been gated and the tick closed behind it.
// Every failure is a refusal — the gate stop the repair replaced, carrying
// everything the repair found out on the way.
//
// It is called from closeAfterGate with the finished gate's failures, and it
// ends by re-entering the same machinery it came from (gateAndClose), so a
// repair's merge is held to exactly the order the attempt's was:
// merge -> gate -> evidence -> freshness -> close.
func (r *Reconciler) repairFailedGate(ctx context.Context, entry planEntry, marker attemptHandle,
	merged merge, g *gateProgress) error {

	tick := marker.TickID
	failures := strings.Join(g.failures, ", ")
	baseJobID := fmt.Sprintf("run-%s/tick-%s/repair-%d", r.runID, tick, marker.Attempt)
	for {
		// ONE REPAIR PER TICK — the durable half of the bound. A recorded
		// repair that ANSWERED (merged, or failed on its merits) means a gate
		// failure this tick already had its one job: a second failure over the
		// same tick stops, naming both, exactly as a second conflict on the
		// same tick does for the resolve job (2p6). A repair that failed
		// without answering does not spend it, up to the bound, and a person's
		// release of the tick starts it afresh (role_allowance.go).
		ledger, err := r.roleJobLedgerOf(RoleRepairGate, tick, baseJobID)
		if err != nil {
			return err
		}
		if len(ledger.spent) > 0 {
			prior := ledger.spent[len(ledger.spent)-1]
			branch, _ := prior.Request["repair_branch"].(string)
			status, _ := prior.Response["status"].(string)
			earlier, _ := prior.Response["gate_failures"].(string)
			if earlier == "" {
				earlier = "the failures its decision records"
			}
			return r.refuse(RefusedGate, tick,
				"the integrated gate did not pass for %s twice: first %s, and now %s over the tree its repair job "+
					"left behind. One repair per tick is all a run dispatches: the recorded repair's outcome is %q and "+
					"its work is on %s (every repair of this tick since it was last released: %s). The tick is NOT "+
					"closed: read both failures, take the repair's tree or settle the tick by hand, and run the epic "+
					"again under a new run id",
				r.attemptName(tick, marker.Attempt), earlier, failures, status, branch,
				describeJobs(append(append([]runstate.Decision{}, ledger.operational...), ledger.spent...),
					"repair_branch"))
		}
		if ledger.exhausted() {
			return r.refuse(RefusedGate, tick,
				"the integrated gate did not pass for %s (%s) and %d repair jobs for this tick failed without "+
					"delivering a repair: %s. %d retries after the first is the bound, so the tick is neither repaired "+
					"nor closed: the merge is already on %s, so fix the check or the tree, push it to %s, and run the "+
					"epic again under this run id",
				tick, failures, len(ledger.operational), describeJobs(ledger.operational, "repair_branch"),
				maxOperationalRetries, r.branch, r.branch)
		}
		retry, err := r.dispatchRepair(ctx, entry, marker, merged, g, ledger)
		if retry {
			r.record(tick, StageRedispatched,
				"the repair job for %s failed without delivering a repair (%s); it does not use up the tick's "+
					"repair, and another is dispatched (%d of at most %d)",
				r.attemptName(tick, marker.Attempt), failureReason(err), len(ledger.operational)+2,
				maxOperationalRetries+1)
			continue
		}
		return err
	}
}

// dispatchRepair dispatches the next repair job of the tick's allowance and
// takes it through its merge to the re-gate. `retry` reports a job that failed
// OPERATIONALLY and whose failure is recorded, so the caller may dispatch the
// next one.
func (r *Reconciler) dispatchRepair(ctx context.Context, entry planEntry, marker attemptHandle,
	merged merge, g *gateProgress, ledger roleJobLedger) (bool, error) {

	tick := marker.TickID
	failures := strings.Join(g.failures, ", ")

	// A repair an earlier incarnation dispatched but never integrated is NOT
	// finished from its branch merely because the branch is on origin: a
	// live job's supervisor pushes it, and so does a SIGTERM flush, before
	// the job has repaired anything (epic-2jn, 4mv's resolve). The executor's
	// Start below is what says whether it settled; role_resume.go has the
	// whole argument.

	// The ceiling tier: the strongest worker the declared policy allows,
	// resolved on demand through the same routing every other role resolves
	// through (the resolve-conflict job's argument, in full there).
	tier := ""
	tierNote := "no policy is declared, so the role's own values are the ceiling"
	if r.tierPolicy != nil {
		tier = string(r.tierPolicy.CeilingOrDefault())
		tierNote = "the policy's ceiling"
	}
	resolved, err := profile.Resolve(RoleRepairGate, profile.Options{
		Dir: r.opts.ProfileDir, RunnersConfig: r.opts.GateConfig, Tier: tier, Substrate: string(r.substrate),
	})
	if err == nil {
		err = usableProfile(r.executors, resolved)
	}
	if err != nil {
		return false, r.refuse(RefusedGate, tick,
			"the integrated gate on %s did not pass for %s (%s) and its repair job could not be routed at the "+
				"ceiling: %v. The tick is neither repaired nor closed; fix the routing and run the epic again",
			short(merged.GateSHA), tick, failures, err)
	}

	// The tree the job starts from: the epic head as it stands NOW — the tree
	// the gate failed on, together with the evidence records the failure wrote
	// onto the same branch, so the job's worktree carries the gate's own
	// output under `.ticfac/` exactly where its inputs name it.
	// A repair an earlier incarnation dispatched under this identity keeps the
	// base it was cut from, or continues from what it pushed.
	// A job dispatched after one that failed without answering starts from
	// the fix that job COMMITTED, when it left one: the next worker finishes
	// that work rather than redoing it, and its merge is an ordinary merge
	// onto the head the branch has by then.
	identity := r.repairMarkerOf(marker, ledger.ordinal)
	jobID, writeRef, stateDir := identity.JobID, identity.WriteRef, identity.StateRoot
	branch := branchOf(writeRef)
	carried := r.carriedRepair(ledger)
	job, err := r.roleJobBase(stateDir, branch, func() (string, error) {
		if carried != "" {
			return carried, nil
		}
		if err := r.git.fetch(r.branch); err != nil {
			return "", err
		}
		base, err := r.git.remoteHead(r.branch)
		if err != nil {
			return "", err
		}
		if base == "" {
			return "", r.refuse(RefusedGate, tick,
				"the integrated gate did not pass for %s (%s) and the integration branch %s could not be read to "+
					"cut its repair job a worktree", tick, failures, r.branch)
		}
		return base, nil
	})
	if err != nil {
		return false, err
	}
	base := job.base
	if base != carried {
		carried = ""
	}

	r.record(tick, StageRepairDispatched,
		"the integrated gate did not pass over %s (%s); a repair job is dispatched to fix the tree the failing "+
			"checks named — routed at tier %q (%s)",
		short(merged.GateSHA), failures, tier, tierNote)

	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: tick, Attempt: marker.Attempt,
		Try: marker.Try, JobID: jobID, Role: RoleRepairGate, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: writeRef, BaseSHA: base, StateDir: stateDir,
		BaseRef:      r.opts.BaseRef,
		Title:        r.repairTitle(tick),
		Profile:      resolved,
		Tier:         tier,
		Executor:     resolved.Executor,
		PriorReports: append([]subprocess.PriorReport{}, r.priorReports(tick, marker.Attempt)...),
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	repairMarker := attemptHandle{
		Executor: resolved.Executor, JobID: jobID, Attempt: marker.Attempt, TickID: tick,
		Try: marker.Try, Role: RoleRepairGate, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: writeRef, BaseSHA: base, StateRoot: stateDir,
		Model: resolved.Model, PromptDigest: promptDigest(resolved), Tier: tier,
	}

	executor, _, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		return false, r.refuse(RefusedGate, tick,
			"the integrated gate on %s did not pass for %s (%s) and the repair job could not be started: build its "+
				"executor: %v", short(merged.GateSHA), tick, failures, err)
	}
	handle, err := executor.Start(r.repairJobSpec(dispatch, marker, g.failed))
	if err != nil {
		// An earlier incarnation's repair under this same identity may have
		// SETTLED — its work is on the branch above, and finishing from the
		// branch is the honest answer to that, not a restart.
		if refusal, ok := subprocess.AsRefusal(err); ok && refusal.Reason == subprocess.RefusedSettled {
			if remote, headErr := r.git.remoteHead(branch); headErr == nil && remote != "" {
				return false, r.finishRepairFromBranch(ctx, entry, marker, identity, merged.AttemptHead, remote, g)
			}
		}
		return false, r.refuse(RefusedGate, tick,
			"the integrated gate on %s did not pass for %s (%s) and the repair job could not be started: %v",
			short(merged.GateSHA), tick, failures, err)
	}
	if note := roleJobResumeNote(job, "the repair job for "+r.attemptName(tick, marker.Attempt)); note != "" {
		r.record(tick, StageAdopted, "%s", note)
	}

	collected, rerr := r.collectRepair(ctx, handle, executor, repairMarker, g, carried)
	var repairHead string
	if collected != nil && collected.Result != nil && collected.Result.Source.HeadSHA != nil {
		repairHead = *collected.Result.Source.HeadSHA
	}
	if repairHead == "" && carried != "" {
		// A job cut at a committed fix that found nothing left to change: the
		// fix it was handed is its answer.
		repairHead = carried
	}
	if operationalFailure(collected, rerr) {
		// The job never answered. Whatever it committed is on its branch
		// (collect preserved it) and is recorded, so the next job can start
		// from it; the failure does not spend the tick's repair.
		if repairHead == "" || repairHead == carried {
			if local := r.attemptWorkHead(repairMarker); local != "" {
				repairHead = local
			}
		}
		recorded := r.disposeRepair(handle, executor, repairMarker, g, rerr, failureOperational, repairHead)
		return recorded && ctx.Err() == nil, rerr
	}
	if rerr == nil && repairHead == "" {
		rerr = r.refuse(RefusedGate, tick,
			"the integrated gate did not pass for %s (%s) and its repair job settled without a commit to merge: %s. "+
				"The tick is neither repaired nor closed; the job's branch is kept, with whatever it left",
			tick, failures, collected.Message)
	}
	var repairMerge string
	if rerr == nil {
		repairMerge, rerr = r.mergeRepair(repairMarker, repairHead, g)
	}
	if rerr != nil {
		r.disposeRepair(handle, executor, repairMarker, g, rerr, failureOnMerits, repairHead)
		return false, rerr
	}
	// The re-gate's fingerprint keeps the TICK'S attempt head as its
	// attempt_head — the head the collect and the boundary check read — while
	// its source is the repaired tree: the repair changed what the gate is
	// ABOUT, not whose work the gate is over, and the freshness check still
	// watches the attempt branch for exactly the moves it exists to catch.
	repaired := merge{AttemptHead: merged.AttemptHead, EpicHead: repairMerge, GateSHA: repairMerge, Merged: true}

	// The repair's work is merged and its decision is durable BEFORE the gate
	// re-runs, so a re-gate that fails again re-enters this function and
	// meets the recorded repair: one repair per tick, named by both failures.
	if err := r.recordRepairDecision(dispatch, repairMarker, "merged", repaired, repairHead, g); err != nil {
		return false, err
	}
	r.record(tick, StageIntegrated,
		"the repair job fixed the tree behind the failed gate on %s; its merge is %s and the gate runs as usual",
		short(merged.GateSHA), short(repaired.GateSHA))
	r.tearDown(handle, executor, repairMarker,
		fmt.Sprintf("the repair job's fix of %s is integrated", tick), false)

	return false, r.gateAndClose(ctx, entry, marker, nil, repaired)
}

// collectRepair waits one repair job out and holds it to the same rules the
// resolve-conflict job is held to (and an implementation attempt's collect,
// before that): it settles, it committed, it wrote a report that does not
// ask for a person, and it wrote under no authority but its own. Every
// failure is the gate stop the repair replaced, naming the gate's failures
// beside the repair's.
//
// `carried` is the committed fix the job was cut at, when it was cut at one:
// such a job may find nothing left to change, and its empty branch is then an
// answer — the fix it was handed — not an undelivered one.
func (r *Reconciler) collectRepair(ctx context.Context, handle *subprocess.JobHandle, executor Executor,
	marker attemptHandle, g *gateProgress, carried string) (*subprocess.Collection, error) {

	// No checkpoint of its own here, for the resolve job's reason: every
	// store write is a commit on the same branch the repair's merge is about
	// to be pushed to — a checkpoint here is a lease that push loses.
	tick := marker.TickID
	failures := strings.Join(g.failures, ", ")
	failed := func(format string, args ...any) error {
		return r.refuse(RefusedGate, tick,
			"the integrated gate did not pass for %s (%s) and its repair job failed: %s. The tick is neither "+
				"repaired nor closed: the merge is already on %s, so the repair is to fix the check or the tree, push it to "+
				"%s, and run the epic again under this run id",
			tick, failures, fmt.Sprintf(format, args...), r.branch, r.branch)
	}
	if _, err := r.awaitResolve(ctx, handle, executor, marker); err != nil {
		return nil, failed("it did not settle: %v", err)
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, failed("it could not be collected: %v", err)
	}
	r.record(tick, StageCollected, "%s", collectedLine("the repair job", collected))
	// The repair's work is durable on the remote before anything is merged or
	// dispatched from it — collect's own rule (tick 55i). It holds for a job
	// that failed as much as for one that answered: a job that committed its
	// fix and never reported is where the next job starts.
	r.preserveAttemptWork(marker)

	handed := carried != "" && collected.Verdict == subprocess.VerdictNoCommits
	switch {
	case collected.Verdict != subprocess.VerdictReadyToMerge && !handed:
		return collected, failed("it answered %s and the run's verdict is %s (%s): %s. The tick is neither "+
			"repaired nor closed", roleAnswerOf(collected), collected.Verdict, collected.Result.Outcome, collected.Message)
	case collected.Report.Status != "" && collected.Report.NeedsHuman():
		return collected, failed("it answered %s: it asks for a person, and the tick stays open for the person "+
			"it asked for: %s", collected.Report.Status, collected.Report.Detail)
	case len(collected.BoundaryViolations) > 0 || len(collected.ArtifactViolations) > 0:
		return collected, failed("it wrote under an authority that is not its own (%s)",
			strings.Join(append(append([]string{}, collected.BoundaryViolations...), collected.ArtifactViolations...), ", "))
	}
	return collected, nil
}

// mergeRepair merges the repair job's head onto the integration branch under
// the same lease the attempt's own merge pushed under: the branch moves with
// the run's own records while the repair runs, and a push that loses its
// lease rebuilds on the new head rather than forcing over it. It returns the
// commit the integration branch is at once the repair is on it — the merge
// itself, or the head it already found the repair contained in.
func (r *Reconciler) mergeRepair(marker attemptHandle, head string, g *gateProgress) (string, error) {
	tick := marker.TickID
	failures := strings.Join(g.failures, ", ")
	if err := r.git.fetch(marker.WriteRef); err != nil {
		return "", fmt.Errorf("fetch the repair job's branch %s: %w", branchOf(marker.WriteRef), err)
	}
	if _, err := r.git.resolve(head); err != nil {
		return "", r.refuse(RefusedGate, tick,
			"the integrated gate did not pass for %s (%s) and its repair job's head %s is not a commit this "+
				"checkout has: %v", tick, failures, short(head), err)
	}
	for try := 0; try < maxMergePushes; try++ {
		epicHead, err := r.git.remoteHead(r.branch)
		if err != nil {
			return "", err
		}
		if err := r.git.fetch(r.branch); err != nil {
			return "", err
		}
		if r.git.contains(head, epicHead) {
			// Already integrated — by an earlier incarnation that pushed and
			// died before the decision landed. Nothing is merged twice; the
			// gate still decides what closes.
			return epicHead, nil
		}
		merged, conflict, err := r.mergeInWorktree(tick, marker.Attempt, branchOf(marker.WriteRef), head, epicHead)
		if err != nil {
			return "", err
		}
		if conflict != nil {
			// The repair was cut at the epic head, so its merge conflicting
			// means somebody else moved the branch with work of their own
			// while the repair ran. That is the merge stop it always was
			// (2p6's resolve is for ATTEMPTS' conflicts), and it is refused
			// here naming the gate's failures, the files and the repair's
			// branch, which is where the half-merged fix sits.
			return "", r.refuse(RefusedGate, tick,
				"the integrated gate did not pass for %s (%s) and its repair job's merge onto %s conflicts (%s): "+
					"the repair's work is kept on %s. The tick is neither repaired nor closed",
				tick, failures, r.branch, conflict.Detail, branchOf(marker.WriteRef))
		}
		_, stderr, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
			return merged, nil
		}
		if !leaseRefused(stderr) {
			return "", fmt.Errorf("push the repair of the failed gate for %s into %s to %s: %w: %s",
				tick, r.branch, r.opts.Remote, pushErr, firstLine(stderr))
		}
		// The branch moved under this writer — the run-state store's own
		// records land on it. Rebuild the merge on the new head.
	}
	return "", r.refuse(RefusedGate, tick,
		"%s moved under this reconciler %d times running while merging the repair of %s; that is an operational "+
			"problem, not a conflict to spin on", r.branch, maxMergePushes, tick)
}

// finishRepairFromBranch completes a repair from work that is already
// durable: an earlier incarnation dispatched the job, the job settled, and
// the run went down before the merge was pushed. The merge is made from
// exactly what the branch holds, the decision lands, and the gate re-runs
// over it — the same ending the dispatched path reaches. `attemptHead` is
// the tick's own attempt head, the one the re-gate's fingerprint keeps as
// its attempt_head (see repairFailedGate).
//
// `marker` is the TICK'S attempt marker — the one the gate and the close are
// about. Everything done to the REPAIR (fetch its branch, name it in the
// merge and the decision, retire it) is done with the repair's own marker,
// derived from the same deterministic identity the dispatch uses. Working
// the repair with the attempt's marker was the epic-2jn stall: the
// retirement below deleted the ATTEMPT's branch on origin, the freshness
// check then read that absent branch as an attempt head of "" and refused
// the passing gate over the repaired tree as stale, and the next pass held
// the tick as rejected work nobody had merged — while its head was on the
// integration branch all along.
func (r *Reconciler) finishRepairFromBranch(ctx context.Context, entry planEntry, marker, repairMarker attemptHandle,
	attemptHead, remote string, g *gateProgress) error {

	repairMerge, err := r.mergeRepair(repairMarker, remote, g)
	if err != nil {
		return err
	}
	repaired := merge{AttemptHead: attemptHead, EpicHead: repairMerge, GateSHA: repairMerge, Merged: true}
	if err := r.recordRepairDecision(
		Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
			JobID: repairMarker.JobID, Role: RoleRepairGate, Repo: r.opts.Repo, Remote: r.opts.Remote},
		repairMarker, "merged", repaired, remote, g); err != nil {
		return err
	}
	r.record(marker.TickID, StageIntegrated,
		"the repair job of %s had already settled; its work on %s is finished into the merge %s and the gate runs "+
			"as usual",
		r.attemptName(marker.TickID, marker.Attempt), branchOf(repairMarker.WriteRef), short(repaired.GateSHA))
	// The branch that carried the REPAIR's work is retired the way a finished
	// resolve's is, now that its commits are on the integration branch. The
	// attempt's own branch is not this function's to retire: the close does.
	_, _, _ = r.git.try("", "push", r.opts.Remote, ":"+refFor(branchOf(repairMarker.WriteRef)))
	return r.gateAndClose(ctx, entry, marker, nil, repaired)
}

// repairMarkerOf is the repair job's identity for one attempt's failed gate —
// the job id, write ref and state directory repairFailedGate dispatches it
// under — derived from the attempt's marker, so an incarnation finishing a
// repair it did not dispatch names the same job and the same branch.
//
// `ordinal` is the job's place in the tick's repair allowance
// (role_allowance.go): the first repair keeps the identity it always had.
func (r *Reconciler) repairMarkerOf(marker attemptHandle, ordinal int) attemptHandle {
	suffix := jobOrdinalSuffix(ordinal)
	repair := marker
	repair.Role = RoleRepairGate
	repair.JobID = fmt.Sprintf("run-%s/tick-%s/repair-%d", r.runID, marker.TickID, marker.Attempt) + suffix
	repair.WriteRef = repairWriteRef(r.runID, marker.TickID, marker.Attempt) + suffix
	repair.StateRoot = repairStateDir(r.execStateDir(marker.TickID, marker.Attempt)) + suffix
	return repair
}

// carriedRepair is the fix the latest repair that failed without answering
// committed, or "" when it left none this checkout can read (its branch is
// fetched for it). A repair's merge is an ordinary merge onto whatever head
// the branch has, so a committed fix is carried whatever has landed since.
func (r *Reconciler) carriedRepair(ledger roleJobLedger) string {
	if len(ledger.operational) == 0 {
		return ""
	}
	decision := ledger.operational[len(ledger.operational)-1]
	candidate, _ := decision.Response["repair_head"].(string)
	if candidate == "" {
		return ""
	}
	if branch, _ := decision.Request["repair_branch"].(string); branch != "" {
		_ = r.git.fetch(branch)
	}
	if _, err := r.git.resolve(candidate); err != nil {
		return ""
	}
	return candidate
}

// repairTitle is the dispatch title the sandbox door carries.
func (r *Reconciler) repairTitle(tick string) string {
	return fmt.Sprintf("Repair the tree the integrated gate failed over for tick %s", tick)
}

// repairJobSpec is the repair job's JobSpec: the ordinary spec of a write job
// over this tick, with the two differences the role makes — the inputs name
// the EVIDENCE records of the failing checks (the gate's own output, which
// the worktree carries under the run's `.ticfac/` path) beside the tick and
// the epic, and the artifact prefix is the repair's own so its report never
// shadows the attempt's.
func (r *Reconciler) repairJobSpec(d Dispatch, marker attemptHandle, failing []gateCheck) *subprocess.JobSpec {
	spec := r.jobSpec(d)
	spec.Inputs = []subprocess.Input{{Kind: "tick", ID: d.TickID}, {Kind: "epic", ID: d.EpicID}}
	for _, check := range failing {
		spec.Inputs = append(spec.Inputs, subprocess.Input{Kind: "evidence", ID: check.Key})
	}
	spec.ArtifactPrefix = "runs/" + d.RunID + "/" + d.TickID + "/repair-" + strconv.Itoa(marker.Attempt) + "/"
	return spec
}

// repairDecisionOf is the recorded repair decision of one tick, whichever
// attempt's gate failure produced it — the durable half of "one repair per
// tick".
func (r *Reconciler) repairDecisionOf(tick string) (*runstate.Decision, bool, error) {
	if r.store == nil {
		return nil, false, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return nil, false, err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return nil, false, err
	}
	for i := range decisions {
		if decisions[i].Role != RoleRepairGate {
			continue
		}
		if id, _ := decisions[i].Request["tick_id"].(string); id == tick {
			return &decisions[i], true, nil
		}
	}
	return nil, false, nil
}

// recordRepairDecision lands the repair as a decision record on the run
// branch: the request that was made (which gate failed, which checks, which
// branch the job writes) and the response that came back (the merge, or the
// failure). Each job of the tick's repair allowance (role_allowance.go) is
// one record; a gate failure met after the allowance is spent stops naming
// them.
func (r *Reconciler) recordRepairDecision(dispatch Dispatch, marker attemptHandle, status string,
	repaired merge, repairHead string, g *gateProgress) error {
	return r.recordRepairOutcome(dispatch, marker, status, repaired, repairHead, g, "", "")
}

// recordRepairOutcome is recordRepairDecision with the failure's kind
// (role_allowance.go) and its reason, for a repair that failed.
func (r *Reconciler) recordRepairOutcome(dispatch Dispatch, marker attemptHandle, status string,
	repaired merge, repairHead string, g *gateProgress, failure, reason string) error {

	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Role == RoleRepairGate && existing.Request["tick_id"] == marker.TickID &&
			existing.Request["job_id"] == marker.JobID {
			// Already recorded, by an earlier incarnation of this run, for
			// this job: a repair is create-if-absent, never rewritten. Each
			// job of the tick's allowance is a record of its own.
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	failures := append([]string{}, g.failures...)
	sort.Strings(failures)
	failingChecks := make([]string, 0, len(g.failed))
	for _, check := range g.failed {
		failingChecks = append(failingChecks, check.Name)
	}
	response := map[string]any{
		"status":        status,
		"gate_failures": strings.Join(failures, ", "),
		"merge":         repaired.GateSHA,
		"repair_head":   repairHead,
		"job_id":        marker.JobID,
	}
	if failure != "" {
		response["failure"] = failure
		response["reason"] = reason
	}
	request := map[string]any{
		"tick_id": marker.TickID,
		"attempt": marker.Attempt,
		// The head of the tick's attempt the failing gate was over: the
		// durable record of what is integrated, for a resume that finds the
		// attempt's own branch gone from origin (recordedAttemptHead).
		"attempt_head":   g.fingerprint["attempt_head"],
		"epic_id":        r.opts.EpicID,
		"job_id":         marker.JobID,
		"role":           RoleRepairGate,
		"repair_branch":  branchOf(marker.WriteRef),
		"gate_sha":       g.fingerprint["source_sha"],
		"gate_failures":  strings.Join(failures, ", "),
		"failing_checks": failingChecks,
		"source_grade":   "write",
		"output_schema":  outputSchemaFor(RoleRepairGate),
	}
	if dispatch.Profile != nil {
		request["profile"] = dispatch.Profile.String()
		request["profile_digest"] = dispatch.Profile.Digest
	}
	stamp := r.now().UTC().Format(time.RFC3339)
	_, err = r.store.PutDecision(runstate.Decision{
		Decision:    number,
		Role:        RoleRepairGate,
		Request:     request,
		Response:    response,
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return fmt.Errorf("record the repair decision for %s: %w", marker.TickID, err)
	}
	return nil
}

// disposeRepair tears a failed repair job down the way a refused attempt is
// torn down: the worktree and the credential go, the branch is KEPT — the
// refusal is what a person reads next, and the branch is where the
// half-made fix is. The failure is recorded durably too, so a later
// incarnation that meets the same gate failure stops naming the recorded
// repair instead of paying for a second one.
//
// The record carries the failure's KIND (role_allowance.go) and the head the
// job left, and the return says whether it landed: a retry is dispatched only
// over a recorded failure.
func (r *Reconciler) disposeRepair(handle *subprocess.JobHandle, executor Executor, marker attemptHandle,
	g *gateProgress, err error, failure, repairHead string) bool {

	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		return false
	}
	recorded := true
	if derr := r.recordRepairOutcome(
		Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
			JobID: marker.JobID, Role: RoleRepairGate, Repo: r.opts.Repo, Remote: r.opts.Remote},
		marker, "failed", merge{}, repairHead, g, failure, failureReason(err)); derr != nil {
		r.record(marker.TickID, StageRejected, "the failed repair could not be recorded: %v", derr)
		recorded = false
	}
	r.tearDown(handle, executor, marker,
		fmt.Sprintf("the repair job for the failed gate of %s failed", r.attemptName(marker.TickID, marker.Attempt)), true)
	return recorded
}

// repairWriteRef is the ref one tick's repair job may write, in the same
// namespace every attempt of the run writes: deterministic per (tick,
// attempt), so the incarnation that finds a gate failure already dispatched
// under this identity finds the same branch.
func repairWriteRef(runID, tick string, attempt int) string {
	return "refs/heads/ticfac/run-" + runID + "/tick-" + tick + "/repair-" + strconv.Itoa(attempt)
}

// repairStateDir is the repair job's private state directory: under the
// attempt's own, deterministic, and never the attempt's.
func repairStateDir(attemptDir string) string {
	return filepath.Join(attemptDir, "repair")
}
