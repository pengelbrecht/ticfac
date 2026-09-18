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

	// The one refusal a Reason does NOT lift. A Reason permits disposal of an
	// attempt whose FACTS were never persisted, because "we threw it away" then
	// has an author; it has never had anything to say about commits. A caller
	// holding a reason it believes in — "the tick is closed", "the attempt was
	// rejected" — is exactly the caller most likely to be wrong about whether
	// the work reached a remote, and this deletes the only copy when it is.
	// KeepBranch is the way to dispose of such an attempt: the worktree goes,
	// the commits stay.
	head := headOf(record.Repo, record.Branch)
	if !opts.KeepBranch && head != "" && !e.onRemote(record, head) {
		return refuse(RefusedBranchUnsafe,
			"branch %s holds commits no remote has: deleting it would discard the only copy. "+
				"Push it, or dispose with KeepBranch.", record.Branch)
	}

	// The exclude line comes out FIRST, while the worktree it was resolved
	// from still exists. "No run-created worktree and no run-created branch"
	// has to include the one edit this executor made to a file it does not
	// own, or every attempt this host ever ran leaves a line in the operator's
	// info/exclude that nothing will ever remove.
	if err := unexcludeFromGit(excludeDirFor(record), record.Spec.ArtifactPrefix); err != nil {
		return fmt.Errorf("remove the artifact prefix from this repository's git exclude file: %w", err)
	}

	// The report is copied out BEFORE the worktree goes, because the worktree
	// is the only place it lives.
	//
	// collect archives it (tick 35h), but dispose is reached without a collect:
	// settle.go cancels and disposes a RELEASED attempt, so a report a person
	// released was deleted unread — the analysis 35h exists to preserve, lost
	// on the one path a person takes when they most need to read it. herdr's
	// dispose has always done this; the local executor did not. Found by the
	// Phase 3 review, with a reproduction.
	if _, archived := archiveReport(st, record); !archived {
		_ = st.observe(Observation{At: e.stamp(), Kind: ObsExited,
			Detail: "the attempt had no readable report to archive before its worktree was removed"})
	}

	// The uncommitted work is preserved BEFORE the worktree goes (tick pbb).
	// This executor's removal is --force: the one unconditional destruction
	// of a worktree this run owns besides the herdr wall-clock pane close, and
	// the path a rejected attempt and a person's release both take. A
	// snapshot that cannot land fails the disposal — the caller retries it,
	// and the alternative is a teardown that throws work away to tidy up.
	// A worktree with nothing uncommitted to preserve takes no snapshot: one
	// wip ref per ordinarily-disposed attempt would bury the stopped ones in
	// noise.
	if err := e.preserveUncommittedWork(st, record); err != nil {
		return fmt.Errorf("preserve the uncommitted work before the worktree goes: %w", err)
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

// preserveUncommittedWork snapshots the worktree's uncommitted state onto a
// wip ref of its own, ONCE, before the force-remove destroys it (tick pbb;
// the mechanics and the ref naming are the seam's — the same ones the herdr
// wall-clock close gates its pane close on). The ref sits outside refs/heads,
// so the work is recoverable after teardown without ever presenting itself
// as a branch to merge, and where it landed is recorded beside the attempt
// record — the name a later attempt's dispatch walks, exactly as it walks
// the archived report.
func (e *Executor) preserveUncommittedWork(st *store, record *attemptRecord) error {
	if _, ok := st.wipSnapshot(); ok {
		return nil // already preserved; the removal may proceed
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		if os.IsNotExist(err) {
			return nil // the worktree is already gone: nothing left to preserve
		}
		return fmt.Errorf("stat the worktree at %s: %w", record.Worktree, err)
	}
	dirty, err := UncommittedWork(record.Worktree, record.Spec.ArtifactPrefix)
	if err != nil {
		return fmt.Errorf("read the worktree's uncommitted work: %w", err)
	}
	if !dirty {
		return nil
	}
	ref := WipRefFor(record.JobID)
	commit, err := SnapshotWorktree(record.Worktree, ref, record.Spec.ArtifactPrefix)
	if err != nil {
		return err
	}
	if err := st.markWIPSnapshot(WIPSnapshot{Ref: ref, Commit: commit, TakenAt: e.stamp()}); err != nil {
		return err
	}
	_ = st.observe(Observation{At: e.stamp(), Kind: ObsExited,
		Detail: fmt.Sprintf("preserved the uncommitted work of attempt %d of %s on %s before the worktree was removed: "+
			"the snapshot is material a later attempt can be pointed at, never evidence of completion",
			record.Attempt, record.JobID, ref)})
	return nil
}

// excludeDirFor is the directory the exclude file is resolved from: the
// attempt's worktree while it is still there, because that is where the append
// resolved it, and the repository once it is gone.
func excludeDirFor(record *attemptRecord) string {
	if _, err := os.Stat(record.Worktree); err == nil {
		return record.Worktree
	}
	return record.Repo
}

// onRemote answers whether the attempt's commits already exist somewhere this
// disposal is not about to delete.
//
// The base is one such place. A branch still at (or behind) the commit it was
// cut from carries no commit of its own, so there is nothing on it to lose —
// which is what a read-only attempt and an attempt that committed nothing both
// look like, and neither should be kept forever for the sake of commits that
// are not there.
func (e *Executor) onRemote(record *attemptRecord, head string) bool {
	if record.BaseSHA != "" && isAncestor(record.Repo, head, record.BaseSHA) {
		return true
	}
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

// PurgeState removes the attempt's state directory — and the wip ref its
// record names, if the attempt's work was preserved (tick pbb). The ref and
// the record retire TOGETHER: the record beside the attempt is the only
// thing that says where the preserved work lives, and a ref with no record
// is litter nobody can place, so outliving each other helps nobody. The ref
// goes FIRST, so a purge interrupted between the two is completed by the
// next one from the record that is still there; an unreadable record or a
// missing ref is nothing to prune rather than a failure, exactly as a state
// directory that is already gone is.
func (e *Executor) PurgeState(h *JobHandle) error {
	local, err := h.Local()
	if err != nil {
		return err
	}
	st := e.storeAt(local.State)
	if record, rerr := st.readAttempt(); rerr == nil {
		if snap, ok := st.wipSnapshot(); ok && snap.Ref != "" {
			if _, derr := git(record.Repo, "update-ref", "-d", snap.Ref); derr != nil {
				return fmt.Errorf("delete the preserved-work ref %s: %w", snap.Ref, derr)
			}
		}
	}
	return os.RemoveAll(local.State)
}
