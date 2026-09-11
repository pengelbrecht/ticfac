package herdr

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// dispose: tear the workspace down, and nothing the run did not create.
//
// The worktree AND the workspace AND the agent on its pane go in one herdr
// call — worktree.remove tears the agent down with the workspace, which is
// why the liveness question is answered again, IMMEDIATELY before the
// removal, and why "herdr did not answer" refuses: silence is not evidence
// that killing an agent is safe (the cleanup lesson this executor carries).
// A WORKING or BLOCKED worker is refused too — the pane is the handoff state
// a human answers, and a working worker may be mid-turn about to commit.
// Liveness by itself is NOT a refusal: an interactive agent CLI does not
// exit when it finishes a turn, and on a collected attempt a settled, idle
// agent is the normal end-of-run state that worktree.remove is FOR.
//
// Never Force. Uncommitted work in the worktree means the collect step has
// not been believed yet; the one narrow exception, carried from ticks'
// cleanup, is the attempt's OWN untracked report, which is archived beside
// the attempt record first so the report survives the worktree and the
// remove can proceed without Force. The branch, when deletion is permitted,
// is this executor's to delete — against the safety check that refuses a
// branch whose commits no remote has, exactly as the local executor's does,
// in the same refusal vocabulary the reconciler already retries with
// KeepBranch on.

// Dispose removes the herdr workspace, the worktree it opened and — unless
// asked not to — the branch the attempt created.
func (e *Executor) Dispose(h *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	local, err := local(h)
	if err != nil {
		return err
	}
	full, record, err := local.resolved()
	if err != nil {
		return fmt.Errorf("dispose %s attempt %d: %w", h.JobID, h.Attempt, err)
	}
	st := e.storeAt(full.State)

	persisted := st.exists(fileResult)
	if !persisted && opts.Reason == "" {
		return refuse(subprocess.RefusedNotPersisted,
			"attempt %d of %s has not been collected: disposal before its facts are persisted is how a run "+
				"loses the only record of what it did. Collect it, or dispose with an explicit reason.",
			record.Attempt, record.JobID)
	}

	// The liveness re-check, taken here rather than at any earlier point: a
	// worker that was idle at planning time can be prompted or wake itself
	// between then and the removal. The question is asked once more, and
	// its FAILURE refuses as loudly as a bad answer.
	state, detail, err := e.livenessForTeardown(record)
	if err != nil {
		return refuse(subprocess.RefusedUnknown, "%s: attempt %d of %s is not torn down (%v)",
			detail, record.Attempt, record.JobID, err)
	}
	switch state {
	case teardownWorking, teardownBlocked:
		return refuse(subprocess.RefusedLive, "%s: attempt %d of %s is not torn down",
			detail, record.Attempt, record.JobID)
	}

	// The exclude line comes out FIRST, while the worktree it was resolved
	// from still exists — "no run-created worktree and no run-created branch"
	// has to include the one edit Start made to a file this executor does
	// not own, or every attempt this host ever ran leaves a line in the
	// operator's info/exclude that nothing will ever remove. It also comes
	// before the report archive below for a mechanical reason: the exclude
	// is what keeps the attempt's own report OUT of `git status`, so with
	// the line still in place the archive would see a clean worktree and
	// leave the report for worktree.remove to refuse on.
	if err := unexcludeFromGit(excludeDirFor(record), record.Spec.ArtifactPrefix); err != nil {
		return fmt.Errorf("remove the artifact prefix from this repository's git exclude file: %w", err)
	}

	// The report archive: the one narrow exception to never-Force. A
	// collected attempt's only dirt is typically its own untracked report —
	// collect read it, but worktree.remove would refuse over it — so it is
	// moved beside the attempt record first, where it survives the
	// worktree. Any other dirt is left completely alone: the removal refuses
	// on it exactly as herdr is documented to.
	if err := e.archiveOwnReport(st, record); err != nil {
		return err
	}

	_, removeErr := e.client.WorktreeRemove(context.Background(), client.WorktreeRemoveParams{
		WorkspaceID: full.WorkspaceID,
		Force:       false,
	})
	if removeErr != nil && !client.IsCode(removeErr, client.CodeWorkspaceNotFound) {
		// A workspace herdr no longer has is the state this step exists to
		// reach, not a failure to reach it — read as an error it stranded
		// the attempt forever, and every later dispose refused it again.
		return fmt.Errorf("herdr worktree.remove for workspace %s: %w", full.WorkspaceID, removeErr)
	}
	if removeErr == nil {
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("removed the herdr workspace %s and its worktree %s", full.WorkspaceID, full.Worktree)}); err != nil {
			return fmt.Errorf("record the disposal: %w", err)
		}
	}

	// The branch, with the one refusal a Reason does NOT lift: a caller
	// holding a reason it believes in is exactly the caller most likely to
	// be wrong about whether the work reached a remote, and this deletes
	// the only copy when it is. KeepBranch is the way to dispose of such an
	// attempt: the workspace goes, the commits stay.
	head := headOf(record.Repo, record.Branch)
	if !opts.KeepBranch && head != "" && !e.onRemote(record, head) {
		return refuse(subprocess.RefusedBranchUnsafe,
			"branch %s holds commits no remote has: deleting it would discard the only copy. "+
				"Push it, or dispose with KeepBranch.", record.Branch)
	}
	if !opts.KeepBranch && branchExists(record.Repo, record.Branch) {
		if err := branchDelete(record.Repo, record.Branch); err != nil {
			return fmt.Errorf("delete the attempt branch: %w", err)
		}
	}
	if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
		Detail: disposalNote(record, opts, persisted, removeErr == nil)}); err != nil {
		return fmt.Errorf("record the disposal: %w", err)
	}
	return nil
}

// The teardown liveness classes. Herdr's positive answers are the ones that
// permit removal; its failures are the refusal.
type teardownState int

const (
	teardownGone    teardownState = iota // the agent is no longer there
	teardownSettled                      // idle, done or unknown — not spending
	teardownWorking                      // mid-turn, may be about to commit
	teardownBlocked                      // waiting on a human's answer
)

// livenessForTeardown asks the ONE question removal needs, next to the
// removal. A launch that was never confirmed settled without an agent ever
// existing, so it is removal's normal case too.
func (e *Executor) livenessForTeardown(record *attemptRecord) (teardownState, string, error) {
	if !record.LaunchConfirmed {
		return teardownGone, "the agent was never confirmed launched", nil
	}
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	if err != nil {
		// "herdr did not answer is not evidence that tearing an agent down
		// is safe" — the refusal as the failure, not a guess.
		return 0, "herdr could not be asked whether the agent is still there", err
	}
	switch agent.AgentStatus {
	case client.StatusWorking:
		return teardownWorking, fmt.Sprintf("the agent %s is working and may be mid-turn about to commit", record.AgentName), nil
	case client.StatusBlocked:
		return teardownBlocked, fmt.Sprintf("the agent %s is blocked: the pane is the handoff state a human answers", record.AgentName), nil
	case client.StatusIdle, client.StatusDone, client.StatusUnknown:
		return teardownSettled, fmt.Sprintf("the agent %s is live and settled (%s); worktree.remove tears it down with the workspace",
			record.AgentName, agent.AgentStatus), nil
	default:
		// agent_gone answers arrive as errors; a live-but-unclassifiable
		// agent is settled by elimination here.
		return teardownSettled, fmt.Sprintf("the agent %s answers %q", record.AgentName, agent.AgentStatus), nil
	}
}

// archiveOwnReport moves the attempt's own untracked report out of the
// worktree — and ONLY that, only when it is the ONLY dirt there. The line
// ticks' cleanup draws is kept exactly: any other modification or untracked
// file is left completely alone, worktree.remove refuses on it, and this is
// never widened to Force.
func (e *Executor) archiveOwnReport(st *store, record *attemptRecord) error {
	if _, err := os.Stat(record.Worktree); err != nil {
		return nil
	}
	entries, err := statusPorcelain(record.Worktree)
	if err != nil {
		// A worktree git cannot inspect is left alone: silence here is not
		// evidence that removing the workspace is safe either, but neither
		// is it this step's to decide — the removal itself refuses.
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	if len(entries) != 1 {
		return nil
	}
	entry := strings.Fields(entries[0])
	if len(entry) != 2 || entry[0] != "??" || entry[1] != record.ResultRel {
		return nil
	}
	raw, err := os.ReadFile(record.ResultPath)
	if err != nil {
		return nil
	}
	if err := st.writeFile(st.path(fileReportArchive), raw, 0o644); err != nil {
		return fmt.Errorf("archive the attempt's own report before the worktree goes: %w", err)
	}
	if err := os.Remove(record.ResultPath); err != nil {
		return fmt.Errorf("remove the archived report from the worktree: %w", err)
	}
	return nil
}

// excludeDirFor is the directory the exclude file is resolved from: the
// attempt's worktree while it is still there, because that is where Start's
// append resolved it, and the repository once it is gone.
func excludeDirFor(record *attemptRecord) string {
	if _, err := os.Stat(record.Worktree); err == nil {
		return record.Worktree
	}
	return record.Repo
}

// onRemote answers whether the attempt's commits already exist somewhere this
// disposal is not about to delete. The base is one such place: a branch still
// at the commit it was cut from carries no commit of its own.
func (e *Executor) onRemote(record *attemptRecord, head string) bool {
	if record.BaseSHA != "" && isAncestor(record.Repo, head, record.BaseSHA) {
		return true
	}
	if record.Remote == "" {
		return false
	}
	return isAncestor(record.Repo, head, "refs/remotes/"+record.Remote+"/"+record.Branch)
}

func disposalNote(record *attemptRecord, opts subprocess.DisposeOptions, persisted, removedWorkspace bool) string {
	what := "workspace, worktree and branch"
	if opts.KeepBranch {
		what = "workspace and worktree"
	}
	if !removedWorkspace {
		what = "branch (the herdr workspace " + record.WorkspaceID + " was already gone)"
	}
	if opts.Reason != "" {
		return fmt.Sprintf("disposed the %s of attempt %d: %s", what, record.Attempt, opts.Reason)
	}
	_ = persisted
	return fmt.Sprintf("disposed the %s of attempt %d after its result was persisted", what, record.Attempt)
}

// PurgeState removes the attempt's state directory. It is separate from
// disposal on purpose: the state directory holds the collected result, the
// observation log and the archived report, which are the run's record of the
// attempt and outlive the git objects.
func (e *Executor) PurgeState(h *subprocess.JobHandle) error {
	local, err := local(h)
	if err != nil {
		return err
	}
	return os.RemoveAll(local.State)
}
