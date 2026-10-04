package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The base fold's resolve-conflict job (stall class base_refresh_conflict).
//
// WHAT WAS WRONG. Every incarnation of a run folds the epic's base branch into
// its integration branch before it plans (refresh.go), and a fold that
// conflicted was a typed refusal and nothing more: "<sha> of main does not
// fold into epic/<id>: go.mod, go.sum … which is a person's decision, or a
// resolve-conflict job's". On 2026-09-27 epic-2jn stopped that way three
// times (go.mod, go.sum, internal/forge/forge.go), and a person resolved each
// one by hand. A step whose only actor is a person is a design defect
// (factory-runs-unattended); the refusal itself already named the actor that
// should have been dispatched.
//
// WHAT THIS DOES. It reuses tick 2p6's resolve-conflict job for the fold:
//
//   - The job's worktree is the CONFLICTED FOLD: the epic head with the base
//     head merged in — through the tracker's own merge drivers, exactly as the
//     fold merges — left unresolved and committed, markers present.
//
//   - Its prompt is the role's own, followed by a brief that says this is a
//     fold of the base branch into the epic and names both INTENTS: the base
//     branch's commits since the fork, and the epic's (its record, and the
//     commits and ticks its branch merged), each narrowed to the files that
//     conflict.
//
//   - Its RESULT is minted by the reconciler, as 2p6's is: a merge commit over
//     the job's tree whose parents are the epic head and the base head. It is
//     pushed to the integration branch under the same lease as a clean fold,
//     and the run continues — the integrated gate runs on the next merge, as
//     it does after every fold.
//
//   - It routes at the policy's CEILING tier through the same routing every
//     role resolves through, so the cloud rule refuses claude there exactly
//     as it does for 2p6.
//
// THE BOUND. One resolve per fold: the resolve is a decision record on the run
// branch keyed by the BASE HEAD it folded, so an incarnation that meets the
// same base head conflicting again — after a resolve that failed on its
// merits — stops naming the recorded resolve instead of paying for a second
// one. A resolve that fails on its merits (it asked for a person, left
// conflict markers, committed nothing) is the stop, and the stop names both
// sides and the files. A resolve that failed without answering at all (no
// report, a runner that died, a job that was lost) does not spend the fold's
// resolve: another is dispatched, from the resolution it committed when it
// committed one, up to the bound role_allowance.go sets for every
// run-dispatched job. A base head that moved on is a new fold, and a new fold
// may be resolved.

// baseFoldKind marks a resolve-conflict decision as the base fold's rather
// than an attempt's: the request carries no tick, and a reader must be able to
// tell the two apart without guessing from what is missing.
const baseFoldKind = "base-fold"

// baseFoldListLimit bounds each side's commit list in the brief: enough to
// state the intents, never so many that the prompt is the log.
const baseFoldListLimit = 30

// resolveBaseFold takes one resolvable conflict of the base fold to the merge
// commit a resolve-conflict job made of it — parents: the epic head it was
// resolved against, the base head — with the finalize the caller runs once
// that merge is on the branch. Every failure is a RefusedBaseRefresh refusal
// naming both sides and the files.
func (r *Reconciler) resolveBaseFold(ctx context.Context, base, baseHead, epicHead string,
	conflict *mergeConflict, drivers map[string]string) (string, func() error, error) {

	sides := r.baseFoldSides(base, baseHead, epicHead)
	forgiven := -1
	for {
		// THE BOUND: one resolve per fold, durably. A fold of this base head
		// that a resolve already ANSWERED for — merged, or failed on its
		// merits — is the stop, naming the recorded outcome. A resolve that
		// failed without answering does not spend it, up to the bound the
		// tick's resolve has too (role_allowance.go).
		ledger, err := r.baseFoldLedgerOf(baseHead)
		if err != nil {
			return "", nil, err
		}
		if r.foldRetrying {
			// The retry of a fold the run start deferred (refresh_defer.go)
			// gets a fresh operational allowance: the jobs that never answered
			// before it are recorded and were paid for, and what failed them
			// then need not fail now. What was spent on its merits stays spent.
			if forgiven < 0 {
				forgiven = len(ledger.operational)
			}
			ledger.operational = ledger.operational[forgiven:]
		}
		if len(ledger.spent) > 0 {
			prior := ledger.spent[len(ledger.spent)-1]
			branch, _ := prior.Request["resolve_branch"].(string)
			status, _ := prior.Response["status"].(string)
			return "", nil, r.refuse(RefusedBaseRefresh, "",
				"%s do not fold together (%s) and a resolve-conflict job already ran for this fold — its recorded "+
					"outcome is %q and its work is on %s (every resolve of this fold: %s). One resolve per fold is "+
					"the bound: read the recorded resolve, merge %s into %s by hand, and run the epic again",
				sides, conflict.Detail, status, branch,
				describeJobs(append(append([]runstate.Decision{}, ledger.operational...), ledger.spent...),
					"resolve_branch"), base, r.branch)
		}
		if ledger.exhausted() {
			return "", nil, r.refuse(RefusedBaseRefresh, "",
				"%s do not fold together (%s) and %d resolve-conflict jobs for this fold failed without delivering "+
					"a resolution: %s. %d retries after the first is the bound: read why the jobs did not answer, "+
					"merge %s into %s by hand, and run the epic again",
				sides, conflict.Detail, len(ledger.operational), describeJobs(ledger.operational, "resolve_branch"),
				maxOperationalRetries, base, r.branch)
		}
		merged, finalize, retry, err := r.dispatchBaseFold(ctx, base, baseHead, epicHead, conflict, drivers, sides,
			ledger)
		if retry {
			r.record("", StageRedispatched,
				"the resolve-conflict job for the fold of %s into %s failed without delivering a resolution (%s); "+
					"it does not use up the fold's resolve, and another is dispatched (%d of at most %d)",
				base, r.branch, failureReason(err), len(ledger.operational)+2, maxOperationalRetries+1)
			continue
		}
		return merged, finalize, err
	}
}

// dispatchBaseFold dispatches the next resolve-conflict job of the fold's
// allowance and takes it to the fold it resolved to. `retry` reports a job
// that failed OPERATIONALLY and whose failure is recorded, so the caller may
// dispatch the next one.
func (r *Reconciler) dispatchBaseFold(ctx context.Context, base, baseHead, epicHead string,
	conflict *mergeConflict, drivers map[string]string, sides string, ledger roleJobLedger) (string, func() error, bool, error) {

	attempt := ledger.ordinal

	writeRef := baseFoldWriteRef(r.runID, attempt, baseHead)
	branch := branchOf(writeRef)
	jobID := fmt.Sprintf("run-%s/base-fold-%d", r.runID, attempt)
	stateDir := filepath.Join(r.opts.ExecStateRoot, r.runID, baseFoldKind, strconv.Itoa(attempt))
	marker := attemptHandle{
		JobID: jobID, Attempt: attempt, TickID: r.opts.EpicID, Try: 1, Role: RoleResolveConflict,
		Repo: r.opts.Repo, Remote: r.opts.Remote, WriteRef: writeRef, StateRoot: stateDir,
	}

	// A resolve an earlier incarnation dispatched and never folded in is NOT
	// finished from its branch merely because the branch is on origin: a live
	// job's supervisor pushes it, and so does a SIGTERM flush — at the
	// conflicted fold the job was cut at, until the job commits (epic-2jn,
	// 4mv's resolve). The executor's Start below is what says whether it
	// settled; role_resume.go has the whole argument.

	// The ceiling tier, resolved on demand through the same routing every
	// role resolves through (resolve.go says why).
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
		return "", nil, false, r.refuse(RefusedBaseRefresh, "",
			"%s do not fold together (%s) and the resolve-conflict job could not be routed at the ceiling: %v. "+
				"Fix the routing and run the epic again",
			sides, conflict.Detail, err)
	}

	// The worktree the job starts from: the fold itself, unresolved, through
	// the tracker's drivers so only what the fold could not merge has markers.
	message := fmt.Sprintf("ticfac run %s: the conflicted fold of %s into %s for the resolve-conflict job",
		r.runID, base, r.branch)
	// A job an earlier incarnation dispatched under this identity keeps the
	// base it was cut from, or continues from what it pushed.
	// A job dispatched after one that failed without answering starts from
	// the resolution that job COMMITTED, when it resolved this very fold over
	// this very epic head.
	carried := r.carriedResolution(ledger, baseHead, epicHead)
	job, err := r.roleJobBase(stateDir, branch, func() (string, error) {
		if carried != "" {
			return carried, nil
		}
		return r.conflictedMerge(epicHead, baseHead, message, driverConfig(drivers))
	})
	if err != nil {
		return "", nil, false, err
	}
	wip := job.base
	if wip != carried {
		carried = ""
	}
	marker.BaseSHA = wip

	// The prompt: the role's own, and the brief that says what THIS conflict
	// is. A copy — the profile is the role's, and the brief is this fold's.
	withBrief := *resolved
	withBrief.Prompt = strings.TrimRight(resolved.Prompt, "\n") + "\n\n" +
		r.baseFoldBrief(base, baseHead, epicHead, conflict)
	marker.Executor, marker.Model, marker.PromptDigest, marker.Tier =
		withBrief.Executor, withBrief.Model, promptDigest(&withBrief), tier

	r.record("", StageDispatched,
		"%s do not fold together (%s); a resolve-conflict job is dispatched to fold %s into %s with both intents, "+
			"routed at tier %q (%s)",
		sides, conflict.Detail, base, r.branch, tier, tierNote)

	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: r.opts.EpicID, Attempt: attempt, Try: 1,
		JobID: jobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: writeRef, BaseSHA: wip, StateDir: stateDir, BaseRef: r.opts.BaseRef,
		Title:    doorLine(fmt.Sprintf("Fold %s into %s: resolve the merge conflict", base, r.branch)),
		Profile:  &withBrief,
		Tier:     tier,
		Executor: withBrief.Executor,
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}

	failed := func(format string, args ...any) error {
		return r.refuse(RefusedBaseRefresh, "",
			"%s do not fold together (%s) and the resolve-conflict job dispatched for the fold failed: %s. "+
				"Its branch %s is kept with whatever it left; merge %s into %s by hand and run the epic again",
			sides, conflict.Detail, fmt.Sprintf(format, args...), branch, base, r.branch)
	}

	executor, _, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		return "", nil, false, failed("its executor could not be built: %v", err)
	}
	handle, err := r.startWithRoom(executor, "", r.baseFoldJobSpec(dispatch))
	if capacityStop(err) {
		return "", nil, false, err
	}
	if err != nil {
		if refusal, ok := subprocess.AsRefusal(err); ok && refusal.Reason == subprocess.RefusedSettled {
			if remote := r.settledJobHead(branch); remote != "" {
				merged, ferr := r.finishBaseFoldFromBranch(base, baseHead, remote, marker, conflict, sides)
				if ferr != nil {
					return "", nil, false, ferr
				}
				return merged, r.finalizeBaseFold(&dispatch, nil, marker, base, baseHead, epicHead, "merged",
					merged, remote, conflict, true), false, nil
			}
		}
		return "", nil, false, failed("it could not be started: %v", err)
	}
	if note := roleJobResumeNote(job, "the resolve-conflict job for the fold of "+base); note != "" {
		r.record("", StageAdopted, "%s", note)
	}

	collected, rerr := r.collectResolveJob(ctx, handle, executor, marker, carried, failed)
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
		// from it; the failure does not spend the fold's resolve.
		if resolveHead == "" || resolveHead == carried {
			if local := r.attemptWorkHead(marker); local != "" {
				resolveHead = local
			}
		}
		recorded := true
		if derr := r.recordBaseFoldOutcome(&dispatch, marker, base, baseHead, epicHead, "failed", "", resolveHead,
			conflict, failureOperational, failureReason(rerr)); derr != nil {
			r.record("", StageRejected, "the failed resolve of the fold could not be recorded: %v", derr)
			recorded = false
		}
		r.tearDown(handle, executor, marker, fmt.Sprintf(
			"the resolve-conflict job for the fold of %s into %s failed", base, r.branch), true)
		if fault := r.roleJobBootFault("", "the resolve-conflict job for the fold of "+base, collected); fault != nil {
			return "", nil, false, fault
		}
		return "", nil, recorded && ctx.Err() == nil, rerr
	}
	if rerr == nil && (resolveHead == "" || (resolveHead == wip && carried == "")) {
		rerr = failed("it settled without a commit to fold: %s", collected.Message)
	}
	var merged string
	if rerr == nil {
		merged, rerr = r.mintBaseFold(resolveHead, baseHead, marker, conflict, failed)
	}
	if rerr != nil {
		if _, ok := AsRefusal(rerr); ok {
			if derr := r.recordBaseFoldOutcome(&dispatch, marker, base, baseHead, epicHead, "failed", "", resolveHead,
				conflict, failureOnMerits, failureReason(rerr)); derr != nil {
				r.record("", StageRejected, "the failed resolve of the fold could not be recorded: %v", derr)
			}
		}
		r.tearDown(handle, executor, marker, fmt.Sprintf(
			"the resolve-conflict job for the fold of %s into %s failed", base, r.branch), true)
		return "", nil, false, rerr
	}
	r.record("", StageRefreshed,
		"the resolve-conflict job resolved the fold of %s into %s (%s); the merge %s is minted from its tree with "+
			"parents %s and %s",
		base, r.branch, strings.Join(conflict.Files, ", "), short(merged), short(epicHead), short(baseHead))
	return merged, r.finalizeBaseFold(&dispatch, &jobInFlight{handle, executor}, marker, base, baseHead, epicHead,
		"merged", merged, resolveHead, conflict, false), false, nil
}

// jobInFlight is the dispatched job a finalize tears down.
type jobInFlight struct {
	handle   *subprocess.JobHandle
	executor Executor
}

// finalizeBaseFold is what runs once the fold is on the branch: the decision
// record, then the job's teardown — or, for a resolve finished from its
// branch, the branch's retirement.
func (r *Reconciler) finalizeBaseFold(dispatch *Dispatch, job *jobInFlight, marker attemptHandle,
	base, baseHead, epicHead, status, merged, resolveHead string, conflict *mergeConflict, fromBranch bool) func() error {

	return func() error {
		if err := r.recordBaseFoldDecision(dispatch, marker, base, baseHead, epicHead, status, merged, resolveHead,
			conflict); err != nil {
			return err
		}
		if job != nil {
			r.tearDown(job.handle, job.executor, marker,
				fmt.Sprintf("the resolve-conflict job's fold of %s into %s is integrated", base, r.branch), false)
		}
		if fromBranch {
			_, _, _ = r.git.try("", "push", r.opts.Remote, ":"+refFor(branchOf(marker.WriteRef)))
		}
		return nil
	}
}

// mintBaseFold is the mechanical half: the conflicted files verified free of
// markers at the job's head, and the merge commit — parents: the epic head the
// fold was RESOLVED AGAINST, the base head — built over the job's tree.
//
// The first parent is read off the conflicted fold the job's branch starts
// from — the merge commit whose second parent is the base head — never taken
// from the branch as it stands now: a job an earlier incarnation cut over an
// older epic head (adopted while live, or finished from its branch) resolved
// THAT head's tree, and a merge naming a later head as its parent over that
// tree silently reverts whatever landed in between. The refresh loop folds a
// resolution whose first parent is not the branch's head onto the head it has
// now, with a real merge.
func (r *Reconciler) mintBaseFold(resolveHead, baseHead string, marker attemptHandle,
	conflict *mergeConflict, failed func(format string, args ...any) error) (string, error) {

	if err := r.haveCommit(resolveHead, marker.WriteRef); err != nil {
		return "", fmt.Errorf("fetch the resolve-conflict job's branch %s: %w", branchOf(marker.WriteRef), err)
	}
	if _, err := r.git.resolve(resolveHead); err != nil {
		return "", fmt.Errorf("the resolve-conflict job's head %s is not a commit this checkout has: %w",
			short(resolveHead), err)
	}
	epicHead, err := r.resolvedOver(resolveHead, baseHead)
	if err != nil {
		return "", failed("its branch %v", err)
	}
	if path, left := r.markersLeftAt(resolveHead, conflict.Files); left {
		return "", failed("it committed %s still carrying its conflict markers — a resolution that did not happen "+
			"is not a fold, whatever the commit says", path)
	}
	// The job's container commits its own report on its branch, and the
	// conflicted fold its worktree starts from still carries the integration
	// branch's reports (the fold brings in what the BASE has; it is the epic
	// side that has them): the tree is minted with every report as the epic
	// head the job resolved against has it (report_merge.go). ab0cdab4
	// rewrote RESULT-hn6.md on epic/hn6 through exactly this path, after the
	// reports guard was already on the branch.
	tree, err := r.treeWithoutReports(resolveHead, epicHead)
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("Merge the base branch into %s through the resolve-conflict job\n\nticfac run %s: "+
		"the fold of %s into %s conflicted (%s) and was resolved by the resolve-conflict job %s",
		r.branch, r.runID, short(baseHead), r.branch, strings.Join(conflict.Files, ", "), marker.JobID)
	merged, err := r.git.run("", "commit-tree", tree, "-p", epicHead, "-p", baseHead, "-m", message)
	if err != nil {
		return "", fmt.Errorf("mint the fold from the resolve-conflict job's resolution: %w", err)
	}
	return merged, nil
}

// finishBaseFoldFromBranch completes a fold from a resolve that is already
// durable on its branch. The mint reads the epic head it was resolved against
// off the conflicted fold the branch starts from, so the minted merge's
// parents are the two heads the job actually resolved, whatever the branch has
// done since.
func (r *Reconciler) finishBaseFoldFromBranch(base, baseHead, remote string, marker attemptHandle,
	conflict *mergeConflict, sides string) (string, error) {

	failed := func(format string, args ...any) error {
		return r.refuse(RefusedBaseRefresh, "",
			"%s do not fold together (%s) and the resolve-conflict job an earlier incarnation dispatched left "+
				"its work on %s, which does not finish the fold: %s. Read the branch, merge %s into %s by hand, "+
				"and run the epic again",
			sides, conflict.Detail, branchOf(marker.WriteRef), fmt.Sprintf(format, args...), base, r.branch)
	}
	merged, err := r.mintBaseFold(remote, baseHead, marker, conflict, failed)
	if err != nil {
		return "", err
	}
	r.record("", StageRefreshed,
		"the resolve-conflict job for the fold of %s into %s had already settled; its work on %s is finished into "+
			"the merge %s", base, r.branch, branchOf(marker.WriteRef), short(merged))
	return merged, nil
}

// baseFoldLedgerOf is the fold's resolve allowance (role_allowance.go): the
// recorded resolves of the fold of one base head, split into those that
// answered (spent) and those that failed without answering (operational),
// and the attempt number the NEXT fold resolve takes — one past every
// base-fold resolve the run has recorded, whichever base head it folded.
func (r *Reconciler) baseFoldLedgerOf(baseHead string) (roleJobLedger, error) {
	ledger := roleJobLedger{ordinal: 1}
	if r.store == nil {
		return ledger, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return ledger, err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return ledger, err
	}
	for _, decision := range decisions {
		if decision.Role != RoleResolveConflict || decision.Request["kind"] != baseFoldKind {
			continue
		}
		ledger.ordinal++
		if head, _ := decision.Request["base_head"].(string); head != baseHead {
			continue
		}
		if kind, _ := decision.Response["failure"].(string); kind == failureOperational {
			ledger.operational = append(ledger.operational, decision)
		} else {
			ledger.spent = append(ledger.spent, decision)
		}
	}
	return ledger, nil
}

// recordBaseFoldDecision lands the fold's resolve on the run branch —
// create-if-absent per job, so a restart that finishes the same resolve never
// records it twice, and each job of the fold's allowance is a record of its
// own.
func (r *Reconciler) recordBaseFoldDecision(dispatch *Dispatch, marker attemptHandle, base, baseHead, epicHead,
	status, merged, resolveHead string, conflict *mergeConflict) error {
	return r.recordBaseFoldOutcome(dispatch, marker, base, baseHead, epicHead, status, merged, resolveHead, conflict,
		"", "")
}

// recordBaseFoldOutcome is recordBaseFoldDecision with the failure's kind
// (role_allowance.go) and its reason, for a resolve that failed.
func (r *Reconciler) recordBaseFoldOutcome(dispatch *Dispatch, marker attemptHandle, base, baseHead, epicHead,
	status, merged, resolveHead string, conflict *mergeConflict, failure, reason string) error {

	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Role == RoleResolveConflict && existing.Request["kind"] == baseFoldKind &&
			existing.Request["base_head"] == baseHead && existing.Request["job_id"] == marker.JobID {
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	d := Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: r.opts.EpicID, Attempt: marker.Attempt,
		JobID: marker.JobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote,
		BaseSHA: marker.BaseSHA}
	if dispatch != nil {
		d = *dispatch
	}
	request := map[string]any{
		"kind":           baseFoldKind,
		"epic_id":        r.opts.EpicID,
		"job_id":         marker.JobID,
		"role":           RoleResolveConflict,
		"base_branch":    base,
		"base_head":      baseHead,
		"epic_head":      epicHead,
		"resolve_branch": branchOf(marker.WriteRef),
		"conflict_files": conflict.Files,
		"source_grade":   "write",
		"output_schema":  outputSchemaFor(RoleResolveConflict),
	}
	if d.Profile != nil {
		request["profile"] = d.Profile.String()
		request["profile_digest"] = d.Profile.Digest
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
	stamp := r.now().UTC().Format(time.RFC3339)
	if _, err := r.store.PutDecision(runstate.Decision{
		Decision: number, Role: RoleResolveConflict, Request: request, Response: response, Validated: true,
		RequestedAt: stamp, AnsweredAt: stamp, Provenance: r.attemptProvenance(d),
	}); err != nil {
		return fmt.Errorf("record the resolve-conflict decision for the fold of %s: %w", short(baseHead), err)
	}
	return nil
}

// baseFoldJobSpec is the ordinary write job's spec with the fold's inputs —
// the epic, whose record is the epic side's intent, and the run — and an
// artifact prefix of its own so its report shadows nothing.
func (r *Reconciler) baseFoldJobSpec(d Dispatch) *subprocess.JobSpec {
	spec := r.jobSpec(d)
	spec.Inputs = []subprocess.Input{{Kind: "epic", ID: d.EpicID}, {Kind: "run", ID: d.RunID}}
	spec.ArtifactPrefix = "runs/" + d.RunID + "/" + baseFoldKind + "-" + strconv.Itoa(d.Attempt) + "/"
	return spec
}

// baseFoldWriteRef is the ref one fold resolve may write: in the run's own
// namespace, and naming the base head it folds, so a restart finishes a
// resolve of THIS fold from its branch and never one of a base since moved.
func baseFoldWriteRef(runID string, attempt int, baseHead string) string {
	head := baseHead
	if len(head) > 12 {
		head = head[:12]
	}
	return attemptRefPrefix(runID) + baseFoldKind + "-" + strconv.Itoa(attempt) + "-" + head
}

// baseFoldSides names both sides of a fold, as every refusal about it must.
func (r *Reconciler) baseFoldSides(base, baseHead, epicHead string) string {
	return fmt.Sprintf("%s at %s and %s at %s", base, short(baseHead), r.branch, short(epicHead))
}

// baseFoldBrief is the part of the prompt that says what THIS conflict is: a
// fold of the base branch into the epic, with both sides' intents — the
// commits each side made since the fork, narrowed to the files that conflict,
// and the epic's own record. Plain ASCII, because the prompt rides a door that
// takes nothing else.
func (r *Reconciler) baseFoldBrief(base, baseHead, epicHead string, conflict *mergeConflict) string {
	forkPoint, _ := r.git.run("", "merge-base", epicHead, baseHead)
	commits := func(from, to string) []string {
		if from == "" {
			return nil
		}
		args := []string{"log", "--no-merges", "-n", strconv.Itoa(baseFoldListLimit), "--format=%h %s",
			from + ".." + to, "--"}
		out, _, err := r.git.try("", append(args, conflict.Files...)...)
		if err != nil || strings.TrimSpace(out) == "" {
			return nil
		}
		return strings.Split(strings.TrimSpace(out), "\n")
	}
	ticks := r.conflictingTickIDs(epicHead, baseHead, "", conflict.Files)

	var b strings.Builder
	fmt.Fprintf(&b, "## This conflict: folding %s into the epic\n\n", base)
	fmt.Fprintf(&b, "The paragraphs above speak of an attempt and two ticks. THIS conflict is not between two\n")
	fmt.Fprintf(&b, "ticks: it is the fold of the epic's BASE branch %s into its integration branch %s,\n", base, r.branch)
	fmt.Fprintf(&b, "which the run makes at the start of every incarnation so the epic builds on what landed on\n")
	fmt.Fprintf(&b, "%s since it forked. Your worktree is %s at %s with %s at %s merged in, left\n",
		base, r.branch, short(epicHead), base, short(baseHead))
	fmt.Fprintf(&b, "unresolved: the files that did not merge carry the markers. They are:\n\n")
	for _, path := range conflict.Files {
		fmt.Fprintf(&b, "- %s (%s)\n", path, conflict.Kind[path])
	}
	fmt.Fprintf(&b, "\nThe two intents:\n\n")
	fmt.Fprintf(&b, "1. %s's: the commits that landed on %s since the epic forked from it", base, base)
	if forkPoint != "" {
		fmt.Fprintf(&b, " (git log %s..%s)", short(forkPoint), short(baseHead))
	}
	fmt.Fprintf(&b, ". Those that touched the conflicted files:\n")
	writeList(&b, commits(forkPoint, baseHead))
	fmt.Fprintf(&b, "2. The epic's: epic %s (its record is .tick/issues/%s.json) and the work its ticks merged\n",
		r.opts.EpicID, r.opts.EpicID)
	fmt.Fprintf(&b, "   onto %s", r.branch)
	if forkPoint != "" {
		fmt.Fprintf(&b, " (git log %s..%s)", short(forkPoint), short(epicHead))
	}
	fmt.Fprintf(&b, ". Those that touched the conflicted files:\n")
	writeList(&b, commits(forkPoint, epicHead))
	if len(ticks) > 0 {
		fmt.Fprintf(&b, "   The ticks whose merged work is in them: %s. Read their records under .tick/issues/.\n",
			strings.Join(ticks, ", "))
	}
	fmt.Fprintf(&b, "\nResolve each file so that BOTH hold: the epic keeps building on everything %s now has,\n", base)
	fmt.Fprintf(&b, "and keeps every change its own ticks made. For dependency manifests and lock files (go.mod,\n")
	fmt.Fprintf(&b, "go.sum and the like) keep every requirement either side added, at the higher of the two\n")
	fmt.Fprintf(&b, "versions, and make the lock file agree with the manifest. Remove every conflict marker and\n")
	fmt.Fprintf(&b, "commit the resolution on your branch; the reconciler makes the merge commit itself, with\n")
	fmt.Fprintf(&b, "the epic head and the %s head as its parents, and the integrated gate judges the tree.\n", base)
	return doorText(b.String())
}

func writeList(b *strings.Builder, lines []string) {
	if len(lines) == 0 {
		fmt.Fprintf(b, "   (none that git can attribute to those files)\n")
		return
	}
	for _, line := range lines {
		fmt.Fprintf(b, "   - %s\n", line)
	}
}

// doorText keeps text to what the sandbox door reads in a prose field — UTF-8
// with no control character but tab and line feed: a commit subject can
// carry anything, and the brief must not be the reason a dispatch is
// refused. A control character, or a byte that is not UTF-8, becomes `?`;
// every other rune (an em-dash in a subject, say) is kept as written.
func doorText(s string) string {
	var b strings.Builder
	for i, c := range s {
		switch {
		case c == '\n' || c == '\t':
			b.WriteRune(c)
		case unicode.IsControl(c):
			b.WriteByte('?')
		case c == utf8.RuneError && !strings.HasPrefix(s[i:], "�"):
			b.WriteByte('?')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// doorLine is doorText for one line: the title, which carries no tab or
// line break at all.
func doorLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(doorText(s))
}
