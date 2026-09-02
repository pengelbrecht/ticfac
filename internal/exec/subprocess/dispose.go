package subprocess

import (
	"fmt"
	"os"
)

// dispose: the operation the four-verb protocol deliberately does NOT have.
//
// Disposal is a host concern, not a protocol one, so it is a Go method and a
// local subcommand rather than a fifth operation on the seam. What it is not
// free to do is dispose whenever it is asked: job-protocol.json's
// `rules.disposal` allows it only after the required source or evidence has
// been persisted, or after an explicit recovery outcome has been recorded —
// and Appendix A #1 puts the credential's death before the executor's.
//
// Cleanup that leaves no run-created worktree and no run-created branch is the
// point of the operation. Cleanup that also leaves no commits anybody kept is
// the reason it can refuse.

// DisposeOptions says what disposal is allowed to take with it.
type DisposeOptions struct {
	// Reason is the explicit recovery or escalation this disposal is part of.
	// It is what permits disposal of an attempt whose facts were never
	// persisted — recorded, so that "we threw it away" has an author.
	Reason string

	// KeepBranch leaves the branch in place and removes only the worktree.
	KeepBranch bool
}

// Dispose removes the worktree and, unless asked not to, the branch this
// attempt created.
func (e *Executor) Dispose(h *JobHandle, opts DisposeOptions) error {
	local, err := h.Local()
	if err != nil {
		return err
	}
	st := e.storeAt(local.State)
	record, err := st.readAttempt()
	if err != nil {
		return fmt.Errorf("dispose %s attempt %d: no attempt record at %s: %w",
			h.JobID, h.Attempt, local.State, err)
	}

	persisted := st.exists(fileResult)
	if !persisted && opts.Reason == "" {
		return refuse(RefusedNotPersisted,
			"attempt %d of %s has not been collected: disposal before its facts are persisted is how a run "+
				"loses the only record of what it did. Collect it, or dispose with an explicit reason.",
			record.Attempt, record.JobID)
	}

	// Appendix A #1's second half: the money dies first, then the work is
	// rescued. A container torn down before its credential is revoked can
	// spend on the way out.
	if e.guarded("revoke_before_teardown") && st.credentialLive() {
		return refuse(RefusedCredential,
			"attempt %d of %s still holds a live credential: revoke it (cancel) before tearing the attempt down",
			record.Attempt, record.JobID)
	}

	head := headOf(record.Repo, record.Branch)
	if !opts.KeepBranch && head != "" && opts.Reason == "" && !e.onRemote(record, head) {
		return refuse(RefusedBranchUnsafe,
			"branch %s holds commits no remote has: deleting it would discard the only copy. "+
				"Push it, keep the branch, or dispose with an explicit reason.", record.Branch)
	}

	if err := worktreeRemove(record.Repo, record.Worktree); err != nil {
		return fmt.Errorf("remove the attempt worktree: %w", err)
	}
	if !opts.KeepBranch && branchExists(record.Repo, record.Branch) {
		if err := branchDelete(record.Repo, record.Branch); err != nil {
			return fmt.Errorf("delete the attempt branch: %w", err)
		}
	}
	_ = st.observe(Observation{At: e.stamp(), Kind: ObsExited,
		Detail: disposalNote(record, opts, persisted)})
	return nil
}

// onRemote answers whether the attempt's commits already exist somewhere this
// disposal is not about to delete.
func (e *Executor) onRemote(record *attemptRecord, head string) bool {
	if record.Remote == "" {
		return false
	}
	return isAncestor(record.Repo, head, "refs/remotes/"+record.Remote+"/"+record.Branch)
}

func disposalNote(record *attemptRecord, opts DisposeOptions, persisted bool) string {
	what := "worktree and branch"
	if opts.KeepBranch {
		what = "worktree"
	}
	if opts.Reason != "" {
		return fmt.Sprintf("disposed the %s of attempt %d: %s", what, record.Attempt, opts.Reason)
	}
	_ = persisted
	return fmt.Sprintf("disposed the %s of attempt %d after its result was persisted", what, record.Attempt)
}

// PurgeState removes the attempt's state directory. It is separate from
// disposal on purpose: the state directory holds the collected result, the
// observation log and the transcript, which are the run's record of the
// attempt and outlive the git objects.
func (e *Executor) PurgeState(h *JobHandle) error {
	local, err := h.Local()
	if err != nil {
		return err
	}
	return os.RemoveAll(local.State)
}
