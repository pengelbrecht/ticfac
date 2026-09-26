package reconcile

import (
	"context"
	"fmt"
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
// that fails, and a SECOND conflict on the same tick, are the stop, as today
// — and both refusals still name the files (tick ky5's whole point). A
// resolve that landed nothing leaves its branch on the remote, and the
// refusal says where it is.

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

	// A SECOND conflict on the same tick is the stop, as today — and the
	// first resolve is durably recorded as a decision on the run branch, so
	// the stop survives a restart and names everything a person needs: the
	// files, and where the first resolve's work is.
	prior, ok, err := r.resolveDecisionOf(tick)
	if err != nil {
		return "", nil, err
	}
	if ok {
		branch, _ := prior.Request["resolve_branch"].(string)
		status, _ := prior.Response["status"].(string)
		return "", nil, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and a resolve-conflict job already ran for this tick — its "+
				"recorded outcome is %q and its work is on %s. A second conflict on the same tick is the stop, "+
				"as it always was: read the recorded resolve, take its merge or settle the tick, and run the epic again",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, status, branch)
	}

	// A resolve job an earlier incarnation dispatched but never integrated:
	// its branch is durable on the remote, and the work is finished from the
	// evidence rather than paid for twice (the learnings' rule: settle
	// in-flight state from durable evidence by whoever finds it). A branch
	// that does not verify is the stop, naming the files and the branch.
	branch := branchOf(resolveWriteRef(r.runID, tick, marker.Attempt))
	if remote, headErr := r.git.remoteHead(branch); headErr == nil && remote != "" {
		merged, err := r.finishResolveFromBranch(marker, head, epicHead, conflict, remote)
		if err != nil {
			return "", nil, err
		}
		return merged, r.finalizeResolve(marker, head, branch, merged, remote, conflict), nil
	}

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
		return "", nil, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and its resolve-conflict job could not be routed at the ceiling: %v. "+
				"The tick is neither resolved nor re-dispatched; fix the routing and run the epic again",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, err)
	}

	// The conflicted tree the job starts from: the merge of the attempt's head
	// into the integration branch, left unresolved, committed so the
	// executor's ordinary worktree — cut at a commit — IS the conflicted
	// merge, markers present.
	wip, err := r.conflictedTree(epicHead, head, marker)
	if err != nil {
		return "", nil, err
	}

	// Both ticks' descriptions: the attempt's own tick, and the tick(s) whose
	// merged work sits on the other side of the conflict, read out of the
	// integration branch's own history for exactly the files that conflict.
	others := r.conflictingTickIDs(epicHead, tick, conflict.Files)
	r.record(tick, StageDispatched,
		"%s does not merge onto %s (%s); a resolve-conflict job is dispatched to make the union — the two "+
			"intents in conflict are %s, routed at tier %q (%s)",
		r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail,
		strings.Join(append([]string{tick}, others...), " and "), tier, tierNote)

	jobID := fmt.Sprintf("run-%s/tick-%s/resolve-%d", r.runID, tick, marker.Attempt)
	writeRef := resolveWriteRef(r.runID, tick, marker.Attempt)
	stateDir := resolveStateDir(r.execStateDir(tick, marker.Attempt))
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
		return "", nil, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and the resolve-conflict job could not be started: build its executor: %v",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, err)
	}
	handle, err := executor.Start(r.resolveJobSpec(dispatch, marker, others))
	if err != nil {
		// An earlier incarnation's resolve job under this same identity may
		// already have SETTLED — its work is on the branch above, and the
		// finish from the branch is the honest answer to that, not a restart.
		if refusal, ok := subprocess.AsRefusal(err); ok && refusal.Reason == subprocess.RefusedSettled {
			if remote, headErr := r.git.remoteHead(branch); headErr == nil && remote != "" {
				merged, ferr := r.finishResolveFromBranch(marker, head, epicHead, conflict, remote)
				if ferr != nil {
					return "", nil, ferr
				}
				return merged, r.finalizeResolve(marker, head, branch, merged, remote, conflict), nil
			}
		}
		return "", nil, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s (%s) and the resolve-conflict job could not be started: %v",
			r.attemptName(tick, marker.Attempt), r.branch, conflict.Detail, err)
	}

	collected, rerr := r.collectResolve(ctx, handle, executor, resolveMarker, conflict)
	var resolveHead string
	if collected != nil && collected.Result != nil && collected.Result.Source.HeadSHA != nil {
		resolveHead = *collected.Result.Source.HeadSHA
	}
	if rerr == nil && resolveHead == "" {
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
		r.disposeResolve(handle, executor, resolveMarker, head, conflict, rerr)
		return "", nil, rerr
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
	return merged, finalize, nil
}

// finalizeResolve is the finish-from-the-branch twin of the closure the
// dispatched path returns: the merge is minted from work that already sat on
// the remote, so the decision record is the only thing left to land, and the
// branch the work rode on is the one to retire once it has.
func (r *Reconciler) finalizeResolve(marker attemptHandle, head, branch, merged, resolveHead string,
	conflict *mergeConflict) func() error {

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
	marker attemptHandle, conflict *mergeConflict) (*subprocess.Collection, error) {

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
	if _, err := r.awaitResolve(ctx, handle, executor, marker); err != nil {
		return nil, failed("it did not settle: %v", err)
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, failed("it could not be collected: %v", err)
	}
	r.record(tick, StageCollected, "%s", collectedLine("the resolve-conflict job", collected))

	switch {
	case collected.Verdict != subprocess.VerdictReadyToMerge:
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
	// The resolve's work is durable on the remote before anything is torn
	// down or minted from it — collect's own rule (tick 55i), for the same
	// reason it holds for any attempt.
	r.preserveAttemptWork(marker)
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
		if status.State == subprocess.StateLost {
			return nil, fmt.Errorf(
				"the %s job %s can no longer be addressed and has not settled", marker.Role, marker.JobID)
		}
		if status.Terminal {
			return status, nil
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
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
	dir, remove, err := r.git.tempWorktree("ticfac-resolve-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the conflicted tree at %s: %w", short(epicHead), err)
	}
	defer remove()

	message := fmt.Sprintf("ticfac run %s: the conflicted merge of %s into %s for the resolve-conflict job",
		r.runID, branchOf(marker.WriteRef), r.branch)
	_, _, mergeErr := r.git.try(dir, "merge", "--no-ff", "--no-commit", "-m", message, head)
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
func (r *Reconciler) conflictingTickIDs(epicHead, self string, files []string) []string {
	// --full-history: without it, history simplification attributes the
	// merged side's change to the side commit and prunes the MERGE that
	// landed it — and the merge commit is exactly the record that names the
	// tick this parse is looking for.
	args := []string{"log", "-n", "200", "--full-history", "--format=%B", epicHead, "--"}
	args = append(args, files...)
	out, _, err := r.git.try("", args...)
	if err != nil {
		return nil
	}
	seen := map[string]bool{self: true}
	var ids []string
	add := func(id string) {
		if !seen[id] && isTickIDLike(id) {
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

	// The resolve's head must be durable on the remote before the mint: a
	// crash between here and the push of the merge must find the resolution
	// on the branch, where the finish-from-the-branch path reads it.
	if err := r.git.fetch(marker.WriteRef); err != nil {
		return "", fmt.Errorf("fetch the resolve-conflict job's branch %s: %w", branchOf(marker.WriteRef), err)
	}
	if _, err := r.git.resolve(resolveHead); err != nil {
		return "", fmt.Errorf("the resolve-conflict job's head %s is not a commit this checkout has: %w",
			short(resolveHead), err)
	}
	for _, path := range conflict.Files {
		blob, _, err := r.git.try("", "cat-file", "blob", resolveHead+":"+path)
		if err != nil {
			// The resolution removed the file: the conflict is gone with it.
			continue
		}
		if conflictMarkersIn(blob) {
			return "", r.refuse(RefusedMerge, marker.TickID,
				"the resolve-conflict job for the conflict of %s committed %s still carrying its conflict markers: "+
					"a resolution that did not happen is not a merge, whatever the commit says",
				r.attemptName(marker.TickID, marker.Attempt), path)
		}
	}
	tree, err := r.git.run("", "rev-parse", resolveHead+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("read the tree the resolve-conflict job resolved to: %w", err)
	}
	message := fmt.Sprintf("Merge the resolve-conflict job's resolution of %s into %s\n\nticfac run %s: tick %s "+
		"attempt %d conflicted and was resolved by the resolve-conflict job",
		marker.TickID, r.branch, r.runID, marker.TickID, marker.Attempt)
	merged, err := r.git.run("", "commit-tree", tree, "-p", epicHead, "-p", head, "-m", message)
	if err != nil {
		return "", fmt.Errorf("mint the merge of the resolve-conflict job's resolution: %w", err)
	}
	return merged, nil
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

// resolveDecisionOf is the recorded resolve-conflict decision of one tick,
// whichever attempt's conflict produced it — the durable half of "a second
// conflict on the same tick is the stop".
func (r *Reconciler) resolveDecisionOf(tick string) (*runstate.Decision, bool, error) {
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
		if decisions[i].Role != RoleResolveConflict {
			continue
		}
		if id, _ := decisions[i].Request["tick_id"].(string); id == tick {
			return &decisions[i], true, nil
		}
	}
	return nil, false, nil
}

// recordResolveDecision lands the resolve as a decision record on the run
// branch: the request that was made (which conflict, which files, which
// branch the job writes, which ticks' intents are in it) and the response
// that came back (the merge, or the failure). One resolve per tick is
// recordable; the second conflict a run ever meets stops naming this record.
func (r *Reconciler) recordResolveDecision(dispatch Dispatch, marker attemptHandle, head, status,
	merged, resolveHead string, conflict *mergeConflict, others []string) error {

	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Role == RoleResolveConflict && existing.Request["tick_id"] == marker.TickID {
			// Already recorded, by an earlier incarnation of this run, for
			// this tick: the resolve is create-if-absent, never rewritten.
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
	request := map[string]any{
		"tick_id":           marker.TickID,
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
func (r *Reconciler) disposeResolve(handle *subprocess.JobHandle, executor Executor, marker attemptHandle,
	head string, conflict *mergeConflict, err error) {

	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		return
	}
	if derr := r.recordResolveDecision(
		Dispatch{RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
			JobID: marker.JobID, Role: RoleResolveConflict, Repo: r.opts.Repo, Remote: r.opts.Remote},
		marker, head, "failed", "", "", conflict, nil); derr != nil {
		r.record(marker.TickID, StageRejected, "the failed resolve could not be recorded: %v", derr)
	}
	r.tearDown(handle, executor, marker, fmt.Sprintf(
		"the resolve-conflict job for the conflict of %s failed", r.attemptName(marker.TickID, marker.Attempt)), true)
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
