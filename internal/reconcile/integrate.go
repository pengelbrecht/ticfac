package reconcile

import (
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

func (r *Reconciler) integrate(marker attemptHandle, collected *subprocess.Collection) (merge, error) {
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
		return r.integratedAlready(tick, branch)
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

		merged, err := r.mergeInWorktree(tick, marker.Attempt, branch, head, epicHead)
		if err != nil {
			return merge{}, err
		}
		_, stderr, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
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

// integratedAlready is the merge of an attempt this run has already merged.
//
// It merges nothing and can merge nothing: the only thing it does is read
// origin for the attempt's head and the integration branch's, and report the
// pair when the second already CONTAINS the first. A head that is not
// contained is refused — there is then no merge to stand on and no collect to
// make one from, and integrating on the strength of a head nobody collected is
// the false completion durableAttemptHead exists to refuse.
func (r *Reconciler) integratedAlready(tick, branch string) (merge, error) {
	head, err := r.git.remoteHead(branch)
	if err != nil {
		return merge{}, err
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

// mergeInWorktree performs the merge itself. A conflict is refused rather than
// resolved: resolving one is a role-job with its own contract, and a
// reconciler that resolved it silently would be a reconciler inventing code.
func (r *Reconciler) mergeInWorktree(tick string, attempt int, branch, head, epicHead string) (string, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-merge-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the merge worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	message := fmt.Sprintf("Merge branch '%s' into %s\n\nticfac run %s: tick %s attempt %d",
		branch, r.branch, r.runID, tick, attempt)
	if stdout, stderr, err := r.git.try(dir, "merge", "--no-ff", "--no-edit", "-m", message, head); err != nil {
		// The unmerged paths are read BEFORE the abort, which is what erases
		// them. They are the index's answer to "which files", and they back up
		// git's printed CONFLICT lines rather than replace them (see
		// describeMergeFailure).
		unmerged, _ := r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		_, _, _ = r.git.try(dir, "merge", "--abort")
		return "", r.refuse(RefusedMerge, tick,
			"%s does not merge onto %s: %s", r.attemptName(tick, attempt), r.branch,
			describeMergeFailure(stdout, stderr, unmerged, err))
	}
	return r.git.run(dir, "rev-parse", "HEAD")
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
		return "", fmt.Errorf(
			"the collected head %s of %s could not be put on %s, which holds %s instead: %s. Nothing is merged: "+
				"that head is not the commit this run collected, and merging it would close the tick over work "+
				"the attempt did not produce while the commits it did produce stay behind",
			short(local), branch, r.opts.Remote, short(remote), firstLine(stderr))
	}
	return local, nil
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
