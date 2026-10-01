package reconcile

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The RESOLVE-CONFLICT job (tick 2p6, epic gvc).
//
// WHAT WAS WRONG. cr4 and 7eq — two ticks of one wave on epic-yoh — both
// rewrote one function, and the second one to merge refused with
// merge_failed. The run STOPPED for a person with three ticks still running,
// and the only remedy was hand-merging two agents' work in a worktree.
// epic-wne stopped the same way twice on add/add conflicts. A factory that
// runs unattended cannot have a stop whose only actor is a person; the
// memory that says so (factory-runs-unattended) is the reason this path
// exists at all.
//
// WHAT THIS DOES. On a CONTENT or ADD/ADD conflict between an attempt and
// the integration branch — the two kinds two same-wave intents produce — the
// reconciler dispatches a resolve-conflict job (the role is already in the
// job contract, $defs.role) instead of stopping:
//
//   - The job's worktree is cut at the CONFLICTED MERGE ITSELF: a commit
//     whose tree is the merge of the attempt's head into the integration
//     branch, left unresolved, so the files that did not merge carry git's
//     conflict markers exactly as the merge left them. A fresh worktree of
//     the epic branch with the attempt's head merged in and the conflict
//     markers present.
//
//   - The prompt names BOTH ticks — the attempt's and the tick(s) whose
//     merged work sits on the other side, read out of the integration
//     branch's own history — because the two descriptions are the two
//     INTENTS, and a resolution that has only one of them is a resolution
//     that guessed.
//
//   - The job's RESULT is a merge commit whose parents are the epic head and
//     the attempt head. The job resolves the CONTENT; the RECONCILER mints
//     the merge commit itself (git commit-tree over the job's tree, with the
//     two parents named), because parentage is a mechanical fact and a model
//     asked to produce it correctly is a model trusted where nothing needs
//     trusting. The reconciler then treats the attempt as integrated — head
//     contained — and the gate runs as usual over the merged tree.
//
//   - The job routes at the POLICY'S CEILING TIER: the strongest worker the
//     declared ladder allows, which is the operator's stated routing
//     (locally claude; in the cloud a Workers AI model — the CloudRule guard
//     refuses anything else, so the resolve job never runs claude in the
//     cloud). It is resolved ON DEMAND rather than at construction: a role
//     that exists for the rare conflict must not make every cloud run refuse
//     at start over a cell nobody was asked to declare.
//
// WHAT STILL STOPS FOR A PERSON, as the tick's acceptance says. A resolve job
// that fails ON ITS MERITS, and a SECOND conflict on the same tick, are the
// stop, as today — and both refusals still name the files (tick ky5's whole
// point). A resolve that landed nothing leaves its branch on the remote, and
// the refusal says where it is. A resolve job that failed without answering
// at all (no report, a runner that died, a job that was lost) does not spend
// the tick's resolve: another is dispatched, from the resolution it committed
// when it committed one, up to a bound — role_allowance.go has the rule, and
// how a person's release of the tick starts the allowance afresh.

// RoleResolveConflict is the job-protocol role this file dispatches.
const RoleResolveConflict = profile.RoleResolveConflict

// mergeConflict is what git printed about a merge that did not merge: the
// paths that did not merge, HOW each one did not, and the sentence a refusal
// about them ends with (describeMergeFailure, tick ky5).
type mergeConflict struct {
	Files  []string
	Kind   map[string]string // path -> conflict kind ("content", "add/add", …)
	Detail string
}

// resolvable reports whether a resolve-conflict job is dispatched for THIS
// conflict: a content conflict (both sides edited a file that existed) or an
// add/add one (both sides created it). Two same-wave intents produce exactly
// these kinds, and these are the kinds whose resolution is a union in intent.
// Every other kind — modify/delete, rename/delete, file/directory — is one
// side's change making the other's meaningless, and deciding which one
// stands is the stop it always was.
func (c *mergeConflict) resolvable() bool {
	if c == nil || len(c.Files) == 0 {
		return false
	}
	for _, path := range c.Files {
		switch c.Kind[path] {
		case "content", "add/add":
		default:
			return false
		}
	}
	return true
}

// conflictKindsOf parses git's own CONFLICT lines and the index's unmerged
// paths into the path→kind map — the same lines describeMergeFailure reads.
func conflictKindsOf(stdout, stderr, unmerged string) map[string]string {
	kinds := map[string]string{}
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		rest, ok := strings.CutPrefix(line, "CONFLICT (")
		if !ok {
			continue
		}
		kind, text, ok := strings.Cut(rest, "): ")
		if !ok {
			continue
		}
		if path, ok := strings.CutPrefix(text, "Merge conflict in "); ok {
			kinds[path] = kind
		}
	}
	for _, path := range strings.Split(unmerged, "\n") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		if _, known := kinds[path]; !known {
			kinds[path] = "unmerged"
		}
	}
	return kinds
}

// classifyMergeFailure turns one failed merge's output into the conflict it
// was, or nil when the failure carried no conflict at all (an unrelated
// history, a missing object — an operational failure, refused as it always
// was, never handed to a resolve job).
func classifyMergeFailure(stdout, stderr, unmerged string, err error) *mergeConflict {
	kinds := conflictKindsOf(stdout, stderr, unmerged)
	if len(kinds) == 0 {
		return nil
	}
	files := make([]string, 0, len(kinds))
	for path := range kinds {
		files = append(files, path)
	}
	sort.Strings(files)
	return &mergeConflict{
		Files: files, Kind: kinds,
		Detail: describeMergeFailure(stdout, stderr, unmerged, err),
	}
}

// resolveConflict takes one resolvable conflict of one attempt to the merge
// commit a resolve-conflict job made of it. It returns the merge commit —
// parents: the epic head, the attempt head — for the caller to push and gate
// exactly as it pushes and gates a clean merge, and the FINALIZE the caller
// runs once that push has landed: the decision record and the teardown, held
// back until then because both write the run branch and a write between the
// merge's mint and its push is a lease the push loses. On every failure it
// returns a refusal that still names the files.
func (r *Reconciler) resolveConflict(ctx context.Context, marker attemptHandle, head, epicHead string,
	conflict *mergeConflict) (string, func() error, error) {

	tick := marker.TickID
	baseJobID := fmt.Sprintf("run-%s/tick-%s/resolve-%d", r.runID, tick, marker.Attempt)
	for {
		// The tick's resolve allowance (role_allowance.go): a resolve that
		// ANSWERED — merged, or refused on its merits — is the tick's one, and a
		// second conflict on the same tick is the stop, as it always was. The
		// resolves are durably recorded as decisions on the run branch, so the
		// stop survives a restart and names everything a person needs: the
		// files, and where every resolve's work is. A resolve that failed
		// without answering does not spend it, up to the bound; a person's
		// release of the tick starts it afresh.
		ledger, err := r.roleJobLedgerOf(RoleResolveConflict, tick, baseJobID)
		if err != nil {
			return "", nil, err
		}
		if len(ledger.spent) > 0 {
			prior := ledger.spent[len(ledger.spent)-1]
			branch, _ := prior.Request["resolve_branch"].(string)
			status, _ := prior.Response["status"].(string)
			return "", nil, r.refuse(RefusedMerge, tick,
				"%s does not merge onto %s (%s) and a resolve-conflict job already ran for this tick — its "+
					"recorded outcome is %q and its work is on %s (every resolve of this tick since it was last "+
					"released: %s). A second conflict on the same tick is the stop, as it always was: read the "+
					"recorded resolve and take its merge by hand, or release the attempt to try the tick again "+
					"with a fresh resolve — `ticfac settle %s %s %d --release \"<who>\" --carry-work` — and run "+
					"the epic again",
				r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, status, branch,
				describeJobs(append(append([]runstate.Decision{}, ledger.operational...), ledger.spent...),
					"resolve_branch"),
				r.opts.EpicID, tick, marker.Attempt)
		}
		if ledger.exhausted() {
			return "", nil, r.refuse(RefusedMerge, tick,
				"%s does not merge onto %s (%s) and %d resolve-conflict jobs for this tick failed without "+
					"delivering a resolution: %s. %d retries after the first is the bound, so the tick is neither "+
					"resolved nor re-dispatched: read why the jobs did not answer, then release the attempt — "+
					"`ticfac settle %s %s %d --release \"<who>\" --carry-work` — and run the epic again",
				r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, len(ledger.operational),
				describeJobs(ledger.operational, "resolve_branch"), maxOperationalRetries,
				r.opts.EpicID, tick, marker.Attempt)
		}
		merged, finalize, retry, err := r.dispatchResolve(ctx, marker, head, epicHead, conflict, baseJobID, ledger)
		if retry {
			r.record(tick, StageRedispatched,
				"the resolve-conflict job for %s failed without delivering a resolution (%s); it does not use "+
					"up the tick's resolve, and another is dispatched (%d of at most %d)",
				r.attemptName(tick, marker.Attempt), failureReason(err), len(ledger.operational)+2,
				maxOperationalRetries+1)
			continue
		}
		return merged, finalize, err
	}
}

// dispatchResolve dispatches the next resolve-conflict job of the tick's
// allowance and takes it to the merge it resolved to. `retry` reports a job
// that failed OPERATIONALLY and whose failure is recorded, so the caller may
// dispatch the next one.
func (r *Reconciler) dispatchResolve(ctx context.Context, marker attemptHandle, head, epicHead string,
	conflict *mergeConflict, baseJobID string, ledger roleJobLedger) (string, func() error, bool, error) {

	tick := marker.TickID

	// A resolve job an earlier incarnation dispatched but never integrated is
	// NOT finished from its branch merely because the branch is on origin: a
	// live job's supervisor pushes it, and so does a SIGTERM flush — at the
	// conflicted commit the job was cut at, until the job commits (epic-2jn,
	// 4mv attempt 33). The executor's Start below is what says whether it
	// settled; role_resume.go has the whole argument.
	suffix := jobOrdinalSuffix(ledger.ordinal)
	jobID := baseJobID + suffix
	writeRef := resolveWriteRef(r.runID, tick, marker.Attempt) + suffix
	branch := branchOf(writeRef)
	stateDir := resolveStateDir(r.execStateDir(tick, marker.Attempt)) + suffix

	// The ceiling tier: the strongest worker the declared policy allows. The
	// profile is resolved on demand, through the same routing every other
	// role resolves through, so the Workers AI rule refuses a cloud routing
	// of claude exactly as it does for every other role.
	tier := ""
	tierNote := "no policy is declared, so the role's own values are the ceiling"
	if r.tierPolicy != nil {
		tier = string(r.tierPolicy.CeilingOrDefault())
		tierNote = "the policy's ceiling"
	}
	resolved, err := profile.Resolve(RoleResolveConflict, profile.Options{
		Dir: r.opts.ProfileDir, RunnersConfig: r.opts.GateConfig, Tier: tier, Substrate: string(r.substrate),
	})
	if err == nil {
		err = usableProfile(r.executors, resolved)
	}
	if err != nil {
		return "", nil, false, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and its resolve-conflict job could not be routed at the ceiling: %v. "+
				"The tick is neither resolved nor re-dispatched; fix the routing and run the epic again",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, err)
	}

	// The conflicted tree the job starts from: the merge of the attempt's head
	// into the integration branch, left unresolved, committed so the
	// executor's ordinary worktree — cut at a commit — IS the conflicted
	// merge, markers present. A job an earlier incarnation dispatched under
	// this identity keeps the base it was cut from, or continues from what it
	// pushed. A job dispatched after one that failed without answering starts
	// from the resolution that job COMMITTED, when it resolved this very
	// conflict (vqc attempt 50's resolve committed a clean resolution and
	// only never reported): the next worker finishes that work rather than
	// redoing it from the markers.
	carried := r.carriedResolution(ledger, head, epicHead)
	job, err := r.roleJobBase(stateDir, branch, func() (string, error) {
		if carried != "" {
			return carried, nil
		}
		return r.conflictedTree(epicHead, head, marker)
	})
	if err != nil {
		return "", nil, false, err
	}
	wip := job.base
	if wip != carried {
		carried = ""
	}

	// Both ticks' descriptions: the attempt's own tick, and the tick(s) whose
	// merged work sits on the other side of the conflict, read out of the
	// integration branch's own history since the attempt forked from it, for
	// exactly the files that conflict.
	others := r.conflictingTickIDs(epicHead, head, tick, conflict.Files)
	r.record(tick, StageDispatched,
		"%s does not merge onto %s (%s); a resolve-conflict job is dispatched to make the union — the two "+
			"intents in conflict are %s, routed at tier %q (%s)",
		r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail,
		strings.Join(append([]string{tick}, others...), " and "), tier, tierNote)
	if carried != "" {
		r.record(tick, StageDispatched,
			"the resolve-conflict job %s starts from %s, the resolution an earlier resolve of this conflict "+
				"committed before it failed without answering", jobID, short(carried))
	}

	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: tick, Attempt: marker.Attempt,
		Try: marker.Try, JobID: jobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: writeRef, BaseSHA: wip, StateDir: stateDir,
		BaseRef:      r.opts.BaseRef,
		Title:        r.resolveTitle(tick, others),
		Profile:      resolved,
		Tier:         tier,
		Executor:     resolved.Executor,
		PriorReports: append([]subprocess.PriorReport{}, r.priorReports(tick, marker.Attempt)...),
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	resolveMarker := attemptHandle{
		Executor: resolved.Executor, JobID: jobID, Attempt: marker.Attempt, TickID: tick,
		Try: marker.Try, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: writeRef, BaseSHA: wip, StateRoot: stateDir,
		Model: resolved.Model, PromptDigest: promptDigest(resolved), Tier: tier,
	}

	executor, _, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		return "", nil, false, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and the resolve-conflict job could not be started: build its executor: %v",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, err)
	}
	handle, err := r.startWithRoom(executor, tick, r.resolveJobSpec(dispatch, marker, others))
	if capacityStop(err) {
		return "", nil, false, err
	}
	if err != nil {
		// An earlier incarnation's resolve job under this same identity may
		// already have SETTLED — its work is on the branch above, and the
		// finish from the branch is the honest answer to that, not a restart.
		// A branch still at the conflicted commit the job was cut from is not
		// that answer (roleJobAnsweredNothing).
		if refusal, ok := subprocess.AsRefusal(err); ok && refusal.Reason == subprocess.RefusedSettled {
			if remote := r.settledJobHead(branch); remote != "" && !roleJobAnsweredNothing(remote, wip, carried, ledger, "resolve_head") {
				merged, ferr := r.finishResolveFromBranch(resolveMarker, head, epicHead, conflict, remote)
				if ferr != nil {
					return "", nil, false, ferr
				}
				return merged, r.finalizeResolve(resolveMarker, head, merged, remote, conflict), false, nil
			}
		}
		startErr := r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and the resolve-conflict job %s could not be started: %v",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, jobID, err)
		if !startIsOperational(err) {
			return "", nil, false, startErr
		}
		// It never started, so it never answered: an operational failure of
		// the allowance, recorded and torn down like one, and the next job of
		// the allowance is dispatched.
		recorded := r.recordResolveOutcome(
			Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: tick, Attempt: marker.Attempt,
				JobID: jobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote},
			resolveMarker, head, "failed", "", "", conflict, nil, failureOperational, failureReason(startErr)) == nil
		r.tearDownSettled(resolveMarker, fmt.Sprintf("the resolve-conflict job %s for %s never started", jobID,
			r.attemptName(tick, marker.Attempt)), true)
		return "", nil, recorded && ctx.Err() == nil, startErr
	}
	if note := roleJobResumeNote(job, "the resolve-conflict job for "+r.attemptName(tick, marker.Attempt)); note != "" {
		r.record(tick, StageAdopted, "%s", note)
	}

	collected, rerr := r.collectResolve(ctx, handle, executor, resolveMarker, conflict, carried)
	var resolveHead string
	if collected != nil && collected.Result != nil && collected.Result.Source.HeadSHA != nil {
		resolveHead = *collected.Result.Source.HeadSHA
	}
	if resolveHead == "" && carried != "" {
		// A job cut at a committed resolution that found nothing left to
		// change: the resolution it was handed is its answer.
		resolveHead = carried
	}
	if operationalFailure(collected, rerr) {
		// The job never answered. Whatever it committed is on its branch
		// (collect preserved it) and is recorded, so the next job can start
		// from it; the failure does not spend the tick's resolve.
		if resolveHead == "" || resolveHead == carried {
			if local := r.attemptWorkHead(resolveMarker); local != "" {
				resolveHead = local
			}
		}
		recorded := r.disposeResolve(handle, executor, resolveMarker, head, resolveHead, conflict, rerr, failureOperational)
		if fault := r.roleJobBootFault(tick, "the resolve-conflict job for "+r.attemptName(tick, marker.Attempt),
			collected); fault != nil {
			return "", nil, false, fault
		}
		return "", nil, recorded && ctx.Err() == nil, rerr
	}
	if rerr == nil && roleJobAnsweredNothing(resolveHead, wip, carried, ledger, "resolve_head") {
		rerr = r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and its resolve-conflict job settled without a commit to merge: %s. "+
				"The tick is neither resolved nor re-dispatched; the job's branch is kept, with whatever it left",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, collected.Message)
	}
	var merged string
	if rerr == nil {
		merged, rerr = r.mintResolveMerge(resolveHead, head, epicHead, resolveMarker, conflict)
	}
	if rerr != nil {
		r.disposeResolve(handle, executor, resolveMarker, head, resolveHead, conflict, rerr, failureOnMerits)
		return "", nil, false, rerr
	}
	r.record(tick, StageIntegrated,
		"the resolve-conflict job resolved the conflict of %s (%s); the merge of %s it stands behind is minted "+
			"from its tree, and the gate runs as usual",
		r.attemptName(tick, marker.Attempt), strings.Join(conflict.Files, ", "), short(head))

	// The decision record and the teardown run only once the merge has been
	// PUSHED: both write the run branch, and a write between the mint and the
	// push is a lease this push loses. Until then the resolution is durable
	// where the job left it — on its branch — and an incarnation that dies in
	// between finishes from exactly that.
	finalize := func() error {
		if err := r.recordResolveDecision(dispatch, resolveMarker, head, "merged", merged, resolveHead, conflict, others); err != nil {
			return err
		}
		r.tearDown(handle, executor, resolveMarker,
			fmt.Sprintf("the resolve-conflict job's resolution of %s is integrated", tick), false)
		return nil
	}
	return merged, finalize, false, nil
}

// finalizeResolve is the finish-from-the-branch twin of the closure the
// dispatched path returns: the merge is minted from work that already sat on
// the remote, so the decision record is the only thing left to land, and the
// branch the work rode on is the one to retire once it has.
func (r *Reconciler) finalizeResolve(marker attemptHandle, head, merged, resolveHead string,
	conflict *mergeConflict) func() error {

	branch := branchOf(marker.WriteRef)
	return func() error {
		if err := r.recordResolveDecision(
			Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
				JobID: marker.JobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote},
			marker, head, "merged", merged, resolveHead, conflict, nil); err != nil {
			return err
		}
		// The work is merged: the branch that carried it is retired the way a
		// closed attempt's is.
		_, _, _ = r.git.try("", "push", r.opts.Remote, ":"+refFor(branch))
		return nil
	}
}

// collectResolve waits one resolve job out and holds its collect to the same
// rules an implementation attempt's collect is held to: it settles, it
// committed, it wrote a report that does not ask for a person, and it wrote
// under no authority but its own. Every failure is the stop, naming the files.
func (r *Reconciler) collectResolve(ctx context.Context, handle *subprocess.JobHandle, executor Executor,
	marker attemptHandle, conflict *mergeConflict, carried string) (*subprocess.Collection, error) {

	// No checkpoint of its own here: integrate has already stated the
	// integrating state, and every store write is a commit on the same branch
	// the caller's merge is about to be pushed to — a checkpoint here is a
	// lease that push loses.
	//
	// Every refusal below carries the conflict's own detail, because a
	// resolve that fails is the merge stop it replaced and the FILES are the
	// one thing that stop must never stop naming (tick ky5).
	tick := marker.TickID
	failed := func(format string, args ...any) error {
		return r.refuse(RefusedMerge, tick, "%s does not merge onto %s (%s) and its resolve-conflict job failed: %s",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, fmt.Sprintf(format, args...))
	}
	return r.collectResolveJob(ctx, handle, executor, marker, carried, failed)
}

// collectResolveJob is collectResolve's rules with the refusal left to the
// caller: an attempt's conflict and the base fold's (refresh_resolve.go) stop
// under different reasons and name different sides, and the rules a
// resolve-conflict job's collect is held to are the same for both.
//
// `carried` is the committed resolution the job was cut at, when it was cut
// at one: such a job may find nothing left to change, and its empty branch
// is then an answer — the resolution it was handed — not an undelivered one.
func (r *Reconciler) collectResolveJob(ctx context.Context, handle *subprocess.JobHandle, executor Executor,
	marker attemptHandle, carried string, failed func(format string, args ...any) error) (*subprocess.Collection, error) {

	tick := marker.TickID
	if _, err := r.awaitResolve(ctx, handle, executor, marker); err != nil {
		return nil, failed("it did not settle: %v", err)
	}
	collected, err := r.collectDetail(executor, handle, tick)
	if err != nil {
		return nil, failed("it could not be collected: %v", err)
	}
	r.record(tick, StageCollected, "%s", collectedLine("the resolve-conflict job", collected))
	// The resolve's work is durable on the remote before anything is torn
	// down, minted or dispatched from it — collect's own rule (tick 55i), for
	// the same reason it holds for any attempt. It holds for a job that
	// failed as much as for one that answered: a job that committed its
	// resolution and never reported is where the next job starts.
	r.preserveAttemptWork(marker)

	handed := carried != "" && collected.Verdict == subprocess.VerdictNoCommits
	if handed {
		r.preserveHandedWork(marker, carried)
	}
	switch {
	case collected.Verdict != subprocess.VerdictReadyToMerge && !handed:
		return collected, failed("it answered %s and the run's verdict is %s (%s): %s. The tick is neither "+
			"resolved nor re-dispatched",
			roleAnswerOf(collected), collected.Verdict, collected.Result.Outcome, collected.Message)
	case collected.Report.Status != "" && collected.Report.NeedsHuman():
		return collected, failed("it answered %s: it asks for a person, and the tick stays open for the person "+
			"it asked for: %s", collected.Report.Status, collected.Report.Detail)
	case len(collected.BoundaryViolations) > 0 || len(collected.ArtifactViolations) > 0:
		return collected, failed("it wrote under an authority that is not its own (%s)",
			strings.Join(append(append([]string{}, collected.BoundaryViolations...), collected.ArtifactViolations...), ", "))
	}
	return collected, nil
}

// awaitResolve addresses one dispatched job of the run's own — a
// resolve-conflict job or a repair job — until it settles. It is the job's
// own wait rather than the window's: neither job is an attempt of the
// plan, holds no slot, was never claimed in the tracker, and its wall clock
// is enforced by its executor — so what is left to wait on is the executor's
// own answer about whether the job has settled, at the executor's cadence.
func (r *Reconciler) awaitResolve(ctx context.Context, handle *subprocess.JobHandle, executor Executor,
	marker attemptHandle) (*subprocess.JobStatus, error) {

	cursor := ""
	interval := r.pollIntervalFor(marker.Executor)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, err := executor.Inspect(handle, cursor)
		if err != nil {
			return nil, fmt.Errorf("inspect the %s job %s: %w", marker.Role, marker.JobID, err)
		}
		if status == nil {
			return nil, fmt.Errorf("the executor reports nothing about the %s job %s", marker.Role, marker.JobID)
		}
		// A factory's `lost` is its answer for one look, and the factory
		// settles a job it can no longer run (FactoryJobs): re-asked, as the
		// window's wait re-asks it.
		if status.State == subprocess.StateLost && !jobsLiveAtFactory(executor) {
			return nil, fmt.Errorf(
				"the %s job %s can no longer be addressed and has not settled", marker.Role, marker.JobID)
		}
		r.announceNudges(marker.TickID, status)
		if status.Terminal {
			return status, nil
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
		// The window's turn, between this job's polls: the run's other
		// attempts are addressed and admitted while this one is waited out
		// (takeWindowTurn; epic hn6, run_6d88e3de).
		r.takeWindowTurn(ctx)
		if r.sleep != nil {
			r.sleep(interval)
		} else {
			time.Sleep(interval)
		}
	}
}

// conflictedTree is the commit the resolve job's worktree is cut at: the
// merge of the attempt's head into the integration branch, left UNRESOLVED
// and committed, so the tree the job starts from carries git's conflict
// markers exactly as the merge left them. A merge nobody could hand over in
// progress state is handed over as the one fact that survives a worktree:
// its content.
func (r *Reconciler) conflictedTree(epicHead, head string, marker attemptHandle) (string, error) {
	message := fmt.Sprintf("ticfac run %s: the conflicted merge of %s into %s for the resolve-conflict job",
		r.runID, branchOf(marker.WriteRef), r.branch)
	return r.conflictedMergeOf(epicHead, head, message, nil, true)
}

// conflictedMerge is conflictedTree's mechanics for any two heads: `head`
// merged into `epicHead` and left unresolved, committed with its markers.
// `config` is what goes in front of git's merge — the tracker's merge drivers
// for the base fold, so the records resolve exactly as the fold resolves them
// and only the files the fold could not merge carry markers.
func (r *Reconciler) conflictedMerge(epicHead, head, message string, config []string) (string, error) {
	return r.conflictedMergeOf(epicHead, head, message, config, false)
}

// conflictedMergeOf is conflictedMerge with the choice of keeping worker
// reports out (report_merge.go): an attempt's merge into the integration
// branch keeps them out, so the resolve job is never handed a report's
// markers to resolve; the base fold brings in what the base has, as it
// always did.
func (r *Reconciler) conflictedMergeOf(epicHead, head, message string, config []string, keepReports bool) (string, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-resolve-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the conflicted tree at %s: %w", short(epicHead), err)
	}
	defer remove()

	config = append([]string{}, config...)
	if keepReports {
		// An attempt's merge: changelogs merge as a union, as they do in the
		// integration merge itself (union_merge.go), so the job is never
		// handed a changelog's markers.
		union, removeUnion, err := unionMergeConfig()
		if err != nil {
			return "", err
		}
		defer removeUnion()
		config = append(config, union...)
	}
	args := append(config, "merge", "--no-ff", "--no-commit", "-m", message, head)
	_, _, mergeErr := r.git.try(dir, args...)
	if mergeErr != nil {
		// The markers and the unmerged index ARE the state this function
		// exists to hand over; but a merge that failed leaving NO conflicted
		// path is an operational failure — the conflict classifyMergeFailure
		// proved between these two heads is not reproducible, and nothing is
		// dispatched over a state nobody can see.
		unmerged, _ := r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		if strings.TrimSpace(unmerged) == "" {
			return "", fmt.Errorf("rebuild the conflicted merge of %s into %s: %w",
				short(head), r.branch, mergeErr)
		}
	}
	if keepReports {
		if err := r.keepReportsOut(dir, epicHead); err != nil {
			return "", err
		}
	}
	// Staging the markers as content resolves the index's unmerged entries;
	// the commit then carries the conflicted state as an ordinary tree.
	if _, _, err := r.git.try(dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage the conflicted merge of %s into %s: %w", short(head), r.branch, err)
	}
	if _, _, err := r.git.try(dir, "commit", "--quiet", "-m", message); err != nil {
		return "", fmt.Errorf("commit the conflicted merge of %s into %s: %w", short(head), r.branch, err)
	}
	return r.git.run(dir, "rev-parse", "HEAD")
}

// tickRefSpelling and the body's "tick <id> attempt" are the two places the
// run's own merge records name the tick a commit merged — the subject's
// 'ticfac/…/tick-<id>/attempt-…' branch spelling, and the body line the
// integrator writes. conflictingTickIDs reads both.
var tickRefSpelling = regexp.MustCompile(`tick-([A-Za-z0-9_-]+)/attempt-[0-9]+`)

// conflictingTickIDs reads the ticks whose MERGED work sits on the other side
// of the conflict, out of the integration branch's own history: the commits
// that touched the files that conflict, and the tick each of them merged —
// the run's own merge records say which, twice over. `self` (the conflicting
// attempt's tick) is never in the answer.
//
// The other side is what landed on the integration branch SINCE `otherHead`
// forked from it — merge-base(otherHead, epicHead)..epicHead — and nothing
// older. epic-2jn on 2026-09-27 named "4mv and 0z0 and 2qz and 35l … and
// asked and bot and closed … and it … and must" as the two intents of one
// conflict: the parse read the whole history of README.md (every tick that
// ever touched it, long merged before 4mv forked and already in 4mv's own
// base), and took any word after "tick" in a commit body for an id ("the tick
// closed", "tick it"). So a name is also kept only when it IS a tick: its
// record `.tick/issues/<id>.json` is in the tree at epicHead. The tracker's
// records are files in that tree (trackerRecordPath), so "is this a tick" is
// a fact about the commit, read with one ls-tree — not a guess from the
// word's shape.
func (r *Reconciler) conflictingTickIDs(epicHead, otherHead, self string, files []string) []string {
	span := epicHead
	if otherHead != "" {
		forkPoint, err := r.git.run("", "merge-base", otherHead, epicHead)
		if err != nil || forkPoint == "" {
			return nil
		}
		span = forkPoint + ".." + epicHead
	}
	// --full-history: without it, history simplification attributes the
	// merged side's change to the side commit and prunes the MERGE that
	// landed it — and the merge commit is exactly the record that names the
	// tick this parse is looking for.
	args := []string{"log", "-n", "200", "--full-history", "--format=%B", span, "--"}
	args = append(args, files...)
	out, _, err := r.git.try("", args...)
	if err != nil {
		return nil
	}
	known := r.trackerIDsAt(epicHead)
	seen := map[string]bool{self: true}
	var ids []string
	add := func(id string) {
		if !seen[id] && isTickIDLike(id) && known[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, match := range tickRefSpelling.FindAllStringSubmatch(out, -1) {
		add(match[1])
	}
	words := strings.Fields(out)
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "tick" {
			add(words[i+1])
		}
	}
	sort.Strings(ids)
	return ids
}

// trackerIDsAt is the set of records the tracker holds in the tree at
// `commit`: the ids of `.tick/issues/<id>.json`, the layout trackerRecordPath
// names. A tree whose listing cannot be read holds none — the other side of a
// conflict is then named by nobody rather than by prose.
func (r *Reconciler) trackerIDsAt(commit string) map[string]bool {
	issues := path.Join(trackerRoot, "issues") + "/"
	out, _, err := r.git.try("", "ls-tree", "--name-only", commit, issues)
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(line), issues)
		if id, ok := strings.CutSuffix(name, ".json"); ok && id != "" && !strings.Contains(id, "/") {
			ids[id] = true
		}
	}
	return ids
}

// isTickIDLike keeps the body-spelling parse from collecting prose: a tick id
// is the tracker's own short id — alphanumerics with - and _, nothing else.
func isTickIDLike(id string) bool {
	if id == "" || len(id) > 12 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// resolveTitle is the dispatch title the sandbox door carries: the conflict,
// named by its two ticks.
func (r *Reconciler) resolveTitle(tick string, others []string) string {
	title := fmt.Sprintf("Resolve the merge conflict of tick %s", tick)
	if len(others) > 0 {
		title += " with " + strings.Join(others, ", ")
	}
	return title
}

// resolveJobSpec is the resolve job's JobSpec: the ordinary spec of a write
// job over this tick, with the two differences the role makes — the inputs
// name BOTH ticks so the prompt carries both intents, and the artifact prefix
// is the resolve's own so its report never shadows the attempt's.
func (r *Reconciler) resolveJobSpec(d Dispatch, marker attemptHandle, others []string) *subprocess.JobSpec {
	spec := r.jobSpec(d)
	inputs := []subprocess.Input{{Kind: "tick", ID: d.TickID}}
	for _, id := range others {
		if id == d.TickID {
			continue
		}
		inputs = append(inputs, subprocess.Input{Kind: "tick", ID: id})
	}
	inputs = append(inputs, subprocess.Input{Kind: "epic", ID: d.EpicID})
	spec.Inputs = inputs
	spec.ArtifactPrefix = "runs/" + d.RunID + "/" + d.TickID + "/resolve-" + strconv.Itoa(marker.Attempt) + "/"
	return spec
}

// mintResolveMerge is the mechanical half of the resolve: the merge commit
// whose parents are the epic head and the attempt head, built over the tree
// the job resolved to. Parentage is a fact this reconciler states, not one it
// asks a model to produce — and the verification that the job actually
// resolved the files is mechanical too: no path that conflicted may still
// carry a conflict marker at the head the job claims.
func (r *Reconciler) mintResolveMerge(resolveHead, head, epicHead string, marker attemptHandle,
	conflict *mergeConflict) (string, error) {

	// The resolve's head is durable on the remote before the mint (collect
	// preserved it): a crash between here and the push of the merge finds the
	// resolution on a branch, where the finish-from-the-branch path reads it.
	// The branch is fetched only for a head this checkout does not have.
	if err := r.haveCommit(resolveHead, marker.WriteRef); err != nil {
		return "", fmt.Errorf("fetch the resolve-conflict job's branch %s: %w", branchOf(marker.WriteRef), err)
	}
	if _, err := r.git.resolve(resolveHead); err != nil {
		return "", fmt.Errorf("the resolve-conflict job's head %s is not a commit this checkout has: %w",
			short(resolveHead), err)
	}
	if path, left := r.markersLeftAt(resolveHead, conflict.Files); left {
		return "", r.refuse(RefusedMerge, marker.TickID,
			"the resolve-conflict job for the conflict of %s committed %s still carrying its conflict markers: "+
				"a resolution that did not happen is not a merge, whatever the commit says",
			r.attemptName(marker.TickID, marker.Attempt), path)
	}
	// The job's tree is the union of the attempt and the epic head its
	// worktree was cut over — NOT necessarily the head the branch has now. The
	// merge is minted over the head the tree was resolved against, because
	// that is the merge the tree is; naming a later head as its parent would
	// state that head merged while the tree silently reverts everything that
	// landed on the branch since the job was cut (another tick's merge, the
	// base fold, the run's own records).
	resolvedOver, err := r.resolvedOver(resolveHead, head)
	if err != nil {
		return "", r.refuse(RefusedMerge, marker.TickID,
			"the resolve-conflict job for the conflict of %s left %s, which %v: nothing says which head of %s "+
				"its tree was resolved against, and a merge minted over a guessed one can revert work",
			r.attemptName(marker.TickID, marker.Attempt), short(resolveHead), err, r.branch)
	}
	// The job's container commits its own report on its branch: the tree is
	// minted with every report as the head it resolved over has it
	// (report_merge.go).
	tree, err := r.treeWithoutReports(resolveHead, resolvedOver)
	if err != nil {
		return "", fmt.Errorf("read the tree the resolve-conflict job resolved to: %w", err)
	}
	message := fmt.Sprintf("Merge the resolve-conflict job's resolution of %s into %s\n\nticfac run %s: tick %s "+
		"attempt %d conflicted and was resolved by the resolve-conflict job",
		marker.TickID, r.branch, r.runID, marker.TickID, marker.Attempt)
	merged, err := r.git.run("", "commit-tree", tree, "-p", resolvedOver, "-p", head, "-m", message)
	if err != nil {
		return "", fmt.Errorf("mint the merge of the resolve-conflict job's resolution: %w", err)
	}
	if resolvedOver == epicHead {
		return merged, nil
	}
	return r.mergeResolutionOnto(merged, resolvedOver, epicHead, marker, conflict)
}

// mergeResolutionOnto carries a resolution minted over an epic head the
// branch has since moved past onto the head it has now, with a real
// three-way merge: what landed in between is kept because git merges it, not
// because anybody remembered it. A resolution that no longer merges is a
// second conflict on the same tick, and a second conflict is the stop — the
// resolve job's work stays on its branch.
func (r *Reconciler) mergeResolutionOnto(resolution, resolvedOver, epicHead string, marker attemptHandle,
	conflict *mergeConflict) (string, error) {

	dir, remove, err := r.git.tempWorktree("ticfac-merge-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the merge worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	message := fmt.Sprintf("Merge the resolve-conflict job's resolution of %s into %s\n\nticfac run %s: tick %s "+
		"attempt %d was resolved against %s; %s has moved to %s since, and the resolution is merged onto it",
		marker.TickID, r.branch, r.runID, marker.TickID, marker.Attempt, short(resolvedOver), r.branch,
		short(epicHead))
	if stdout, stderr, unmerged, err := r.mergeKeepingReportsOut(dir, epicHead, message, resolution); err != nil {
		return "", r.refuse(RefusedMerge, marker.TickID,
			"the resolve-conflict job resolved the conflict of %s (%s) against %s at %s, and %s has moved to %s "+
				"since with work that does not merge with the resolution: %s. A second conflict on the same tick "+
				"is the stop; the resolution is kept on %s",
			r.attemptName(marker.TickID, marker.Attempt), strings.Join(conflict.Files, ", "), r.branch,
			short(resolvedOver), r.branch, short(epicHead), describeMergeFailure(stdout, stderr, unmerged, err),
			branchOf(marker.WriteRef))
	}
	return r.git.run(dir, "rev-parse", "HEAD")
}

// resolvedOver answers the head a resolve-conflict job's tree was resolved
// against: the first parent of the conflicted merge its branch starts from —
// the commit on the job's first-parent chain whose second parent is `other`,
// the side that was merged in (the attempt's head, or the base head a fold
// merged). conflictedMerge commits exactly that merge, and the job's own
// commits sit on top of it.
func (r *Reconciler) resolvedOver(resolveHead, other string) (string, error) {
	lines, err := r.git.run("", "rev-list", "--first-parent", "--parents", resolveHead)
	if err != nil {
		return "", fmt.Errorf("cannot be read: %w", err)
	}
	for _, line := range strings.Split(lines, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[2] == other {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("does not start from a conflicted merge of %s", short(other))
}

// markersLeftAt is the mechanical half of "did the job resolve it": the first
// path that conflicted and still carries a conflict marker at `head`. A path
// the resolution removed carries none — the conflict is gone with the file.
func (r *Reconciler) markersLeftAt(head string, files []string) (string, bool) {
	for _, path := range files {
		blob, _, err := r.git.try("", "cat-file", "blob", head+":"+path)
		if err != nil {
			continue
		}
		if conflictMarkersIn(blob) {
			return path, true
		}
	}
	return "", false
}

// conflictMarkersIn reports whether resolved content still carries git's
// conflict markers. Only the two genuinely unambiguous prefixes count:
// `=======` alone is a markdown rule and a prose fence, so it is read only in
// the pair git leaves — and the pair is what this check is for.
func conflictMarkersIn(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, ">>>>>>>") {
			return true
		}
	}
	return false
}

// finishResolveFromBranch completes a resolve from work that is already
// durable: an earlier incarnation dispatched the job, the job settled, and
// the run went down before the merge was minted. The branch is fetched, the
// conflicted paths verified marker-free at its head, and the merge minted
// from exactly what the branch holds — the gate still decides what closes.
func (r *Reconciler) finishResolveFromBranch(marker attemptHandle, head, epicHead string,
	conflict *mergeConflict, remote string) (string, error) {

	merged, err := r.mintResolveMerge(remote, head, epicHead, marker, conflict)
	if err != nil {
		if _, ok := AsRefusal(err); ok {
			return "", err
		}
		return "", r.refuse(RefusedMerge, marker.TickID,
			"the resolve-conflict job of %s left its work on %s, and it does not resolve the conflict (%s): %v. "+
				"The tick is neither resolved nor re-dispatched; read the branch and settle the tick",
			r.attemptName(marker.TickID, marker.Attempt), branchOf(marker.WriteRef), conflict.Detail, err)
	}
	r.record(marker.TickID, StageIntegrated,
		"the resolve-conflict job of %s had already settled; its work on %s is finished into the merge %s and the "+
			"gate runs as usual",
		r.attemptName(marker.TickID, marker.Attempt), branchOf(marker.WriteRef), short(merged))
	return merged, nil
}

// recordResolveDecision lands the resolve as a decision record on the run
// branch: the request that was made (which conflict, which files, which
// branch the job writes, which ticks' intents are in it) and the response
// that came back (the merge, or the failure). Each job of the tick's resolve
// allowance (role_allowance.go) is one record; a conflict met after the
// allowance is spent stops naming them.
func (r *Reconciler) recordResolveDecision(dispatch Dispatch, marker attemptHandle, head, status,
	merged, resolveHead string, conflict *mergeConflict, others []string) error {
	return r.recordResolveOutcome(dispatch, marker, head, status, merged, resolveHead, conflict, others, "", "")
}

// recordResolveOutcome is recordResolveDecision with the failure's kind
// (role_allowance.go) and its reason, for a resolve that failed.
func (r *Reconciler) recordResolveOutcome(dispatch Dispatch, marker attemptHandle, head, status,
	merged, resolveHead string, conflict *mergeConflict, others []string, failure, reason string) error {

	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Role == RoleResolveConflict && existing.Request["tick_id"] == marker.TickID &&
			existing.Request["job_id"] == marker.JobID {
			// Already recorded, by an earlier incarnation of this run, for
			// this job: a resolve is create-if-absent, never rewritten. Each
			// job of the tick's allowance is a record of its own.
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	response := map[string]any{
		"status":          status,
		"merge":           merged,
		"resolve_head":    resolveHead,
		"conflict_files":  conflict.Files,
		"conflict_detail": conflict.Detail,
		"job_id":          marker.JobID,
	}
	if failure != "" {
		response["failure"] = failure
		response["reason"] = reason
	}
	request := map[string]any{
		"tick_id":           marker.TickID,
		"attempt":           marker.Attempt,
		"epic_id":           r.opts.EpicID,
		"job_id":            marker.JobID,
		"role":              RoleResolveConflict,
		"attempt_head":      head,
		"resolve_branch":    branchOf(marker.WriteRef),
		"conflict_files":    conflict.Files,
		"conflicting_ticks": append([]string{marker.TickID}, others...),
		"source_grade":      "write",
		"output_schema":     outputSchemaFor(RoleResolveConflict),
	}
	if dispatch.Profile != nil {
		request["profile"] = dispatch.Profile.String()
		request["profile_digest"] = dispatch.Profile.Digest
	}
	stamp := r.now().UTC().Format(time.RFC3339)
	_, err = r.store.PutDecision(runstate.Decision{
		Decision:    number,
		Role:        RoleResolveConflict,
		Request:     request,
		Response:    response,
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return fmt.Errorf("record the resolve-conflict decision for %s: %w", marker.TickID, err)
	}
	return nil
}

// disposeResolve tears a failed resolve job down the way a refused attempt is
// torn down: the worktree and the credential go, the branch is KEPT — the
// refusal is what a person reads next, and the branch is where the
// half-resolved merge is. The failure is recorded durably too, so a later
// incarnation that meets the same conflict stops naming the recorded resolve
// instead of paying for a second one.
//
// The record carries the failure's KIND (role_allowance.go): a job that
// failed without answering does not spend the tick's resolve, and the head it
// left is where the next one may start. It reports whether the failure is
// recorded — a retry is dispatched only over a recorded one, so the bound
// holds across restarts and the loop over it always ends.
func (r *Reconciler) disposeResolve(handle *subprocess.JobHandle, executor Executor, marker attemptHandle,
	head, resolveHead string, conflict *mergeConflict, err error, failure string) bool {

	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		return false
	}
	recorded := true
	if derr := r.recordResolveOutcome(
		Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
			JobID: marker.JobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote},
		marker, head, "failed", "", resolveHead, conflict, nil, failure, failureReason(err)); derr != nil {
		r.record(marker.TickID, StageRejected, "the failed resolve could not be recorded: %v", derr)
		recorded = false
	}
	r.tearDown(handle, executor, marker, fmt.Sprintf(
		"the resolve-conflict job for the conflict of %s failed", r.attemptName(marker.TickID, marker.Attempt)), true)
	return recorded
}

// carriedResolution is the committed resolution of THIS conflict that the
// latest resolve which failed without answering left behind, or "" when there
// is none: its head must be a commit this checkout can read (its branch is
// fetched for it), and it must start from the conflicted merge of exactly
// `head` over exactly `epicHead` — the same conflict, over the same base. A
// resolution of another conflict, or of this one over a base that has moved,
// is not carried: the next job starts from a fresh conflicted merge.
func (r *Reconciler) carriedResolution(ledger roleJobLedger, head, epicHead string) string {
	for i := len(ledger.operational) - 1; i >= 0; i-- {
		decision := ledger.operational[i]
		candidate, _ := decision.Response["resolve_head"].(string)
		if candidate == "" {
			continue
		}
		if branch, _ := decision.Request["resolve_branch"].(string); branch != "" {
			_ = r.git.fetch(branch)
		}
		if _, err := r.git.resolve(candidate); err != nil {
			continue
		}
		if over, err := r.resolvedOver(candidate, head); err == nil && over == epicHead {
			return candidate
		}
		return ""
	}
	return ""
}

// resolveWriteRef is the ref one tick's resolve job may write, in the same
// namespace every attempt of the run writes: deterministic per (tick,
// attempt), so the incarnation that finds a conflict already dispatched under
// this identity finds the same branch.
func resolveWriteRef(runID, tick string, attempt int) string {
	return "refs/heads/ticfac/run-" + runID + "/tick-" + tick + "/resolve-" + strconv.Itoa(attempt)
}

// resolveStateDir is the resolve job's private state directory: under the
// attempt's own, deterministic, and never the attempt's.
func resolveStateDir(attemptDir string) string {
	return filepath.Join(attemptDir, "resolve")
}
