package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The merge into the EpicRun integration branch.
//
// It happens in a DETACHED worktree of its own, outside the repository, for two
// reasons that are the same reason: the integration branch must not be checked
// out anywhere the run-state store might move it, and a worktree inside the
// tree being merged would appear in the diff the boundary check reads.
//
// It is idempotent because it asks a question about the world rather than
// remembering what it did: an attempt whose head is already contained in the
// integration branch is already integrated, whoever integrated it.

const maxMergePushes = 8

type merge struct {
	AttemptHead string
	EpicHead    string
	GateSHA     string
	Merged      bool
}

func (r *Reconciler) integrate(ctx context.Context, marker attemptHandle, collected *subprocess.Collection) (merge, error) {
	tick := marker.TickID
	branch := branchOf(marker.WriteRef)

	if _, err := r.checkpoint(runstate.StateIntegrating,
		fmt.Sprintf("integrating %s attempt %d into %s", tick, marker.Attempt, r.branch)); err != nil {
		return merge{}, err
	}

	// Nothing was collected, because nothing needed to be: a resumed run whose
	// attempt is already merged does not collect it a second time (processTick).
	// The merge that happened is proven here rather than remembered, and if it
	// cannot be proven nothing is integrated.
	if collected == nil {
		return r.integratedAlready(marker, branch)
	}

	head, err := r.durableAttemptHead(branch, collected)
	if err != nil {
		return merge{}, r.refuse(RefusedMerge, tick, "%v", err)
	}
	if err := r.git.fetch(branch); err != nil {
		return merge{}, r.refuse(RefusedMerge, tick, "fetch %s from %s: %v", branch, r.opts.Remote, err)
	}
	if _, err := r.git.resolve(head); err != nil {
		return merge{}, r.refuse(RefusedMerge, tick, "the attempt's head %s is not a commit this checkout has: %v", head, err)
	}

	// The declaration's other half (tick 01u), on the diff this merge is
	// about to act on and before anything merges it: an attempt that touched
	// a file its tick's touch: declaration does not name is refused here,
	// recorded as a rejection, never merged silently. It runs before the
	// merge loop because the merge is the one place the full diff is already
	// proven durable — the collected head, pushed to origin — and after the
	// collect's own refusals because a verdict that never reached here (no
	// commits, a BLOCKED answer) is the more specific thing to tell a person.
	if err := r.checkDeclaredTouch(marker, head); err != nil {
		return merge{}, err
	}

	for try := 0; try < maxMergePushes; try++ {
		epicHead, err := r.git.remoteHead(r.branch)
		if err != nil {
			return merge{}, err
		}
		if err := r.git.fetch(r.branch); err != nil {
			return merge{}, err
		}
		if r.git.contains(head, epicHead) {
			// Already integrated — by an earlier incarnation of this run, or by
			// somebody else. Nothing is merged twice.
			r.setTick(tick, "integrated")
			r.record(tick, StageIntegrated, "%s is already contained in %s", short(head), r.branch)
			return merge{AttemptHead: head, EpicHead: epicHead, GateSHA: epicHead, Merged: false}, nil
		}

		// A resolve in flight: set once the conflict is handed to the
		// resolve-conflict job, run once the push below has landed it. The
		// decision record and the teardown are the resolve's writes to the run
		// branch, and a write between the mint and the push is a lease the
		// push loses — so they wait for it.
		var finalize func() error
		merged, conflict, err := r.mergeInWorktree(tick, marker.Attempt, branch, head, epicHead)
		if err != nil {
			return merge{}, markConflict(err)
		}
		if conflict != nil {
			// A content or add/add conflict between an attempt and the branch
			// is two same-wave intents, and two same-wave intents are a union a
			// worker holding BOTH descriptions can make (tick 2p6): the run
			// dispatches a resolve-conflict job instead of stopping for a
			// person. Every other conflict, and every failure of the resolve,
			// is marked as the conflict it is, and the finish hands it to the
			// standing ladder (finishIntegrate) — naming the files, as ky5
			// made it.
			merged, finalize, err = r.resolveConflict(ctx, marker, head, epicHead, conflict)
			if err != nil {
				return merge{}, markConflict(err)
			}
		}
		_, stderr, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
			if finalize != nil {
				// The merge is on the branch: land the resolve's own records and
				// retire the branch its resolution rode in on. A failure here is
				// returned, never swallowed — the next incarnation would finish
				// the same resolve from the branch and re-record what it owes.
				if err := finalize(); err != nil {
					return merge{}, err
				}
			}
			r.setTick(tick, "integrated")
			r.record(tick, StageIntegrated, "merged %s into %s as %s", short(head), r.branch, short(merged))
			return merge{AttemptHead: head, EpicHead: merged, GateSHA: merged, Merged: true}, nil
		}
		if !leaseRefused(stderr) {
			// Not a lease race: an authentication failure, an unreachable
			// remote, a hook that declined the push. Retrying it eight times
			// and then reporting "the branch moved under this reconciler"
			// sends the next repair at a conflict nobody had — the error is
			// this one, and it is returned as it arrived.
			return merge{}, fmt.Errorf("push the merge of %s into %s to %s: %w: %s",
				tick, r.branch, r.opts.Remote, pushErr, firstLine(stderr))
		}
		// The branch moved under this writer — the run-state store's own
		// records land on it. Rebuild the merge on the new head rather than
		// forcing over whatever arrived.
	}
	return merge{}, r.refuse(RefusedMerge, tick,
		"%s moved under this reconciler %d times running while merging %s; that is an operational problem, "+
			"not a conflict to spin on", r.branch, maxMergePushes, tick)
}

// markConflict marks a merge_failed raised by the merge of the attempt's work
// or by the resolve of its conflict as the conflict it is. Anything else —
// an operational error, another refusal — passes through untouched.
func markConflict(err error) error {
	var refusal *Refusal
	if asRefusal(err, &refusal) && refusal.Reason == RefusedMerge {
		refusal.conflict = true
	}
	return err
}

// integratedAlready is the merge of an attempt this run has already merged.
//
// It merges nothing and can merge nothing: the only thing it does is read
// origin for the attempt's head and the integration branch's, and report the
// pair when the second already CONTAINS the first. A head that is not
// contained is refused — there is then no merge to stand on and no collect to
// make one from, and integrating on the strength of a head nobody collected is
// the false completion durableAttemptHead exists to refuse.
//
// An attempt whose branch origin no longer has (epic-2jn: a repair's finish
// retired the attempt's branch rather than its own) is read from the head
// this checkout's branch carries or the run's records state — and held to
// the same containment: the integration branch carrying it is the proof.
func (r *Reconciler) integratedAlready(marker attemptHandle, branch string) (merge, error) {
	tick := marker.TickID
	head, err := r.git.remoteHead(branch)
	if err != nil {
		return merge{}, err
	}
	if head == "" {
		head = r.offOriginAttemptHead(marker)
	}
	if head == "" && marker.CollectedFrom != nil {
		// Another run's work this attempt ruled on, or finished from the
		// integration branch (takeover.go): its commit is the head.
		head = marker.CollectedFrom.SHA
	}
	epicHead, err := r.git.remoteHead(r.branch)
	if err != nil {
		return merge{}, err
	}
	if err := r.git.fetch(r.branch); err != nil {
		return merge{}, err
	}
	if head == "" || !r.git.contains(head, epicHead) {
		return merge{}, r.refuse(RefusedMerge, tick,
			"%s was not collected in this incarnation and %s does not carry its head either: nothing says what "+
				"this attempt produced, and nothing is merged on the strength of a head nobody checked",
			branch, r.branch)
	}
	r.setTick(tick, "integrated")
	r.record(tick, StageIntegrated, "%s is already contained in %s", short(head), r.branch)
	return merge{AttemptHead: head, EpicHead: epicHead, GateSHA: epicHead, Merged: false}, nil
}

// AttemptMergeNeedle is the words every merge commit that carries one
// attempt into the integration branch spells: "ticfac run <run>: tick
// <tick> attempt <n>" — whole (the plain merge below) or as the opening of
// the resolution's story (the resolve-conflict job's two spellings in
// resolve.go). The status model's report drill-in searches a repository's
// merge commits for exactly these words to read a merged attempt's diff
// after the close swept the attempt's branch (tick ihw): the wording lives
// here, where the message is minted, so the writer and the reader are one
// spelling and cannot drift apart.
func AttemptMergeNeedle(runID, tickID string, attempt int) string {
	return fmt.Sprintf("ticfac run %s: tick %s attempt %d", runID, tickID, attempt)
}

// mergeInWorktree performs the merge itself. A conflict is returned as a
// conflict rather than resolved here: resolving one is the resolve-conflict
// job's (tick 2p6), and a reconciler that resolved it silently would be a
// reconciler inventing code. The RESOLVABLE kinds are handed to that job by
// integrate; every other failure of this merge is refused as it always was.
func (r *Reconciler) mergeInWorktree(tick string, attempt int, branch, head, epicHead string) (string, *mergeConflict, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-merge-", epicHead)
	if err != nil {
		return "", nil, fmt.Errorf("prepare the merge worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	message := fmt.Sprintf("Merge branch '%s' into %s\n\n%s",
		branch, r.branch, AttemptMergeNeedle(r.runID, tick, attempt))
	// Worker reports are kept out of the merge (report_merge.go): a report
	// never lands on the integration branch and never conflicts. The
	// unmerged paths come back read BEFORE the abort, which is what erases
	// them. They are the index's answer to "which files", and they back up
	// git's printed CONFLICT lines rather than replace them (see
	// describeMergeFailure).
	if stdout, stderr, unmerged, err := r.mergeKeepingReportsOut(dir, message, head); err != nil {
		if conflict := classifyMergeFailure(stdout, stderr, unmerged, err); conflict != nil && conflict.resolvable() {
			// Two same-wave intents (content, add/add): the resolve-conflict
			// job's kinds, decided by integrate — not here, where the refusal
			// is the only other thing this merge can produce.
			return "", conflict, nil
		}
		return "", nil, r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s: %s", r.attemptName(tick, attempt), r.branch,
			describeMergeFailure(stdout, stderr, unmerged, err))
	}
	merged, err := r.git.run(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	return merged, nil, nil
}

// describeMergeFailure is the sentence a merge_failed refusal ends with: which
// files did not merge, and HOW each one did not (tick ky5).
//
// It used to be firstLine(stderr), and that was empty every time it mattered.
// git merge prints its CONFLICT lines on STDOUT — "CONFLICT (add/add): Merge
// conflict in x.go" is ordinary progress output as far as git is concerned —
// and with rerere switched off for the run (gitbin.NoRerere) nothing at all
// reaches stderr on a plain conflict. So the refusal read "does not merge onto
// epic/wne: " and stopped, and the operator of epic-wne rebuilt the merge by
// hand in a worktree of their own to learn what git had already printed and
// the reconciler had thrown away.
//
// The KIND is carried, not just the path, because the kind is the diagnosis:
//
//   - add/add: both sides CREATED the file. Inside one epic that is two ticks
//     of one wave writing the same new path — a planning defect, and the fix is
//     the wave, not the merge.
//   - content: both sides edited a file that already existed. Ordinary
//     overlap, or an attempt built on a base the branch has since moved past.
//   - modify/delete, rename/delete, file/directory, …: one side's change
//     makes the other's meaningless; a person decides which one stands.
//
// git's own wording is kept for every kind whose line is not the plain "Merge
// conflict in <path>" — it names the sides and says what was left in the
// tree, and a paraphrase of it could only lose something. The unmerged paths
// from the index are appended when no CONFLICT line mentions them, so a git
// whose output this parse does not recognise still names its files; and when
// there is neither, the merge's whole output is the detail, because an empty
// reason is the one answer this refusal may never give again.
func describeMergeFailure(stdout, stderr, unmerged string, err error) string {
	var conflicts []string
	named := map[string]bool{}
	addAdd := false
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		rest, ok := strings.CutPrefix(line, "CONFLICT (")
		if !ok {
			continue
		}
		kind, text, ok := strings.Cut(rest, "): ")
		if !ok {
			conflicts = append(conflicts, line)
			continue
		}
		if kind == "add/add" {
			addAdd = true
		}
		if path, ok := strings.CutPrefix(text, "Merge conflict in "); ok {
			named[path] = true
			conflicts = append(conflicts, kind+" in "+path)
			continue
		}
		conflicts = append(conflicts, kind+": "+text)
	}
	for _, path := range strings.Split(unmerged, "\n") {
		path = strings.TrimSpace(path)
		if path == "" || named[path] {
			continue
		}
		mentioned := false
		for _, c := range conflicts {
			if strings.Contains(c, path) {
				mentioned = true
				break
			}
		}
		if !mentioned {
			conflicts = append(conflicts, "unmerged "+path)
		}
	}

	if len(conflicts) == 0 {
		output := strings.Join(strings.Fields(strings.TrimSpace(stdout+"\n"+stderr)), " ")
		if output == "" && err != nil {
			output = err.Error()
		}
		if output == "" {
			output = "git merge failed and printed nothing"
		}
		return output
	}
	noun := "conflicts"
	if len(conflicts) == 1 {
		noun = "conflict"
	}
	detail := fmt.Sprintf("%d %s: %s", len(conflicts), noun, strings.Join(conflicts, "; "))
	if addAdd {
		detail += ". An add/add conflict means both sides created the same file: two ticks planned into one " +
			"wave that each write one new path is a planning defect, and the fix is the wave, not the merge"
	}
	return detail
}

// durableAttemptHead is the commit the merge is of. It is the head that was
// COLLECTED — the one the boundary check read and the one the verdict is
// about — made durable on origin before anything merges it.
//
// Origin's own head for the ref is not that commit and is never assumed to be.
// The two diverge for reasons that are ordinary rather than exotic: the
// supervisor's final push can fail and be ignored, and a ref that a previous
// run or attempt also wrote carries commits this attempt never produced. A
// reconciler that merged origin's head regardless would merge a stale subset,
// gate it, close the tick — and then dispose of the branch holding the commits
// nobody merged. That is a false completion and a lost write in one step, so
// the rule here is narrow:
//
//   - nothing was collected: refuse. A head that was not collected is a head
//     nothing checked, and it is not integrated on the strength of existing.
//   - collected and origin agree: merge it.
//   - collected and origin differ: push the collected head. git refuses a
//     non-fast-forward, which is exactly the case where origin holds work this
//     attempt did not produce — and that refusal is the merge's refusal.
//
// Durable still means pushed: the head is on origin before it is merged, so a
// restart on a fresh clone reads the same commit.
func (r *Reconciler) durableAttemptHead(branch string, collected *subprocess.Collection) (string, error) {
	remote, err := r.git.remoteHead(branch)
	if err != nil {
		return "", err
	}
	local := ""
	if collected != nil && collected.Result != nil && collected.Result.Source.HeadSHA != nil {
		local = *collected.Result.Source.HeadSHA
	}
	switch {
	case local == "" && remote == "":
		return "", fmt.Errorf("%s carries no commit on %s and none was collected: there is nothing to integrate",
			branch, r.opts.Remote)
	case local == "":
		return "", fmt.Errorf(
			"%s on %s is at %s but the collect of this attempt read no commit at all: that head was never checked "+
				"against the attempt's base and is not what this run verified, so it is not merged",
			branch, r.opts.Remote, short(remote))
	case local == remote:
		return local, nil
	}

	// The collected head is not what origin has. Before pushing it, ask whether
	// this checkout HAS it: a restart on a fresh clone reads the collected head
	// out of a record the previous clone wrote, and the objects behind it stayed
	// in that clone. The push then fails for a reason that has nothing to do
	// with origin, and blaming origin for holding something else sends the next
	// repair at the wrong problem.
	if _, err := r.git.resolve(local); err != nil {
		return "", fmt.Errorf(
			"the collected head %s of %s is not a commit this checkout has, so it cannot be put on %s (which "+
				"holds %s): the attempt was collected in another clone and its objects are still there. Nothing "+
				"is merged; run this from the checkout that holds the attempt, or push %s from it first",
			short(local), branch, r.opts.Remote, short(remote), branch)
	}

	// Push it: a fast-forward makes origin agree with what was collected, and
	// anything else is origin holding commits this attempt did not produce.
	if _, stderr, err := r.git.try("", "push", r.opts.Remote, local+":"+refFor(branch)); err != nil {
		if r.supersedeEvacuationSnapshot(branch, remote, local) || r.replaceOwnEarlierHead(branch, local) {
			return local, nil
		}
		return "", fmt.Errorf(
			"the collected head %s of %s could not be put on %s, which holds %s instead: %s. Nothing is merged: "+
				"that head is not the commit this run collected, and merging it would close the tick over work "+
				"the attempt did not produce while the commits it did produce stay behind",
			short(local), branch, r.opts.Remote, short(remote), firstLine(stderr))
	}
	return local, nil
}

// replaceOwnEarlierHead replaces origin's head of an attempt branch with the
// collected head when origin's head is a state the attempt's own branch held
// and the worker has since rewritten (epic-2jn, rix attempt 45), and reports
// whether origin holds the collected head afterwards.
//
// A worker that amends, rebases or resets commits its supervisor already
// pushed leaves origin on a commit its branch moved off, and the collected
// head can never fast-forward it. That is the attempt's own history, not
// somebody else's write — and the branch's reflog, in this repository whose
// worktree the worker committed in, is the proof: both origin's head and the
// collected head must be states the local branch pointed at. The replacement
// leases on origin's head (subprocess.ReplaceOwnEarlierHead). Origin holding
// anything the branch never held is still the refusal.
func (r *Reconciler) replaceOwnEarlierHead(branch, head string) bool {
	run := func(args ...string) (string, error) { return r.git.run("", args...) }
	replaced, _ := subprocess.ReplaceOwnEarlierHead(run, r.opts.Remote, branch, head)
	return replaced
}

// evacuationSnapshotSubject is the subject prefix every SIGTERM flush's
// snapshot commit has carried, the older on-branch spelling and the wip-ref
// one alike.
const evacuationSnapshotSubject = "ticfac: evacuation snapshot of uncommitted work"

// supersedeEvacuationSnapshot replaces origin's head of an attempt branch with
// the collected head when origin's head is nothing but a SIGTERM flush's
// snapshot the worker's own result supersedes (epic-2jn), and reports whether
// it did.
//
// Older builds' flush committed the in-flight worktree ON the attempt branch
// and pushed it. A worker that outlived the flush — the ordinary local case —
// then committed its real result on the head it knew, and that result could
// never fast-forward the snapshot: the run was held for a person over two
// identical trees. The worker's final commit supersedes its own snapshot, so
// the replacement is exact rather than a force:
//
//   - origin's head is a snapshot commit — the flush's subject, one parent;
//   - the snapshot's parent is in the collected head's history, so the
//     collected head carries everything origin had except the snapshot
//     itself, which was never the worker's own commit;
//   - the push is --force-with-lease on the snapshot's sha, so a ref that
//     moved again since it was read is refused, not overwritten.
//
// Anything else stays the refusal it was: origin holding commits this
// attempt did not produce is exactly what the non-fast-forward exists to say.
func (r *Reconciler) supersedeEvacuationSnapshot(branch, remote, local string) bool {
	if remote == "" || local == "" {
		return false
	}
	if err := r.git.fetch(branch); err != nil {
		return false
	}
	info, err := r.git.run("", "log", "-1", "--format=%P%x00%s", remote)
	if err != nil {
		return false
	}
	parents, subject, ok := strings.Cut(info, "\x00")
	if !ok || !strings.HasPrefix(subject, evacuationSnapshotSubject) {
		return false
	}
	parent := strings.Fields(parents)
	if len(parent) != 1 || !r.git.contains(parent[0], local) {
		return false
	}
	_, _, err = r.git.try("", "push", "--force-with-lease="+refFor(branch)+":"+remote,
		r.opts.Remote, local+":"+refFor(branch))
	return err == nil
}

func branchOf(writeRef string) string {
	if len(writeRef) > len("refs/heads/") && writeRef[:len("refs/heads/")] == "refs/heads/" {
		return writeRef[len("refs/heads/"):]
	}
	return writeRef
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstLine(text string) string {
	for i, r := range text {
		if r == '\n' {
			return text[:i]
		}
	}
	return text
}
