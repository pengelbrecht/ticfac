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

		merged, err := r.mergeInWorktree(tick, branch, head, epicHead)
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
func (r *Reconciler) mergeInWorktree(tick, branch, head, epicHead string) (string, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-merge-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the merge worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	message := fmt.Sprintf("Merge branch '%s' into %s\n\nticfac run %s: tick %s", branch, r.branch, r.runID, tick)
	if _, stderr, err := r.git.try(dir, "merge", "--no-ff", "--no-edit", "-m", message, head); err != nil {
		_, _, _ = r.git.try(dir, "merge", "--abort")
		return "", r.refuse(RefusedMerge, tick,
			"attempt %d of %s does not merge onto %s: %s", 1, tick, r.branch, firstLine(stderr))
	}
	return r.git.run(dir, "rev-parse", "HEAD")
}

// durableAttemptHead is the commit the merge is of, taken from ORIGIN — the
// only place a restarted run on a fresh clone can read it. When the attempt's
// work is not there, it is pushed rather than merged from a checkout that may
// not survive: durable means pushed, and a merge of something no remote has is
// a merge of the only copy.
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
	case remote != "":
		return remote, nil
	case local == "":
		return "", fmt.Errorf("%s carries no commit on %s and none was collected: there is nothing to integrate",
			branch, r.opts.Remote)
	default:
		if _, err := r.git.run("", "push", r.opts.Remote, local+":"+refFor(branch)); err != nil {
			return "", fmt.Errorf("the attempt's work on %s is not on %s and could not be pushed there: %w",
				branch, r.opts.Remote, err)
		}
		return local, nil
	}
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
