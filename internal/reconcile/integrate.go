package reconcile

import (
	"fmt"

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
		_, _, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
			r.setTick(tick, "integrated")
			r.record(tick, StageIntegrated, "merged %s into %s as %s", short(head), r.branch, short(merged))
			return merge{AttemptHead: head, EpicHead: merged, GateSHA: merged, Merged: true}, nil
		}
		// The branch moved under this writer — the run-state store's own
		// records land on it. Rebuild the merge on the new head rather than
		// forcing over whatever arrived.
	}
	return merge{}, r.refuse(RefusedMerge, tick,
		"%s moved under this reconciler %d times running while merging %s; that is an operational problem, "+
			"not a conflict to spin on", r.branch, maxMergePushes, tick)
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
	if _, stderr, err := r.git.try(dir, "merge", "--no-ff", "--no-edit", "-m", message, head); err != nil {
		_, _, _ = r.git.try(dir, "merge", "--abort")
		return "", r.refuse(RefusedMerge, tick,
			"attempt %d of %s does not merge onto %s: %s", attempt, tick, r.branch, firstLine(stderr))
	}
	return r.git.run(dir, "rev-parse", "HEAD")
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

	// The collected head is not what origin has. Push it: a fast-forward makes
	// origin agree with what was collected, and anything else is origin
	// holding commits this attempt did not produce.
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
