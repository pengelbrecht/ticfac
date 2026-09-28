package subprocess

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Leftovers: the git half of a teardown, shared by every executor and by the
// reconciler's sweep (epic-6in, 46x attempt 2 and 4i8's repair-3).
//
// A job's resources come down in ONE order, wherever the teardown runs: the
// substrate's workspace and pane first (the executor's own), then the git
// worktree and its registration, then — only when it is meant to go — the
// branch. The branch step cannot come first: git refuses to delete a branch a
// worktree still has checked out, and that refusal is exactly what stranded
// 46x attempt 2 when herdr had dropped the workspace (the pane close took it)
// and the teardown declined to remove a worktree it "could not attribute".
// The worktree IS attributable: the attempt record names its path, and git's
// own census says which branch is checked out there.
//
// The helpers below are the mechanics of the middle step, exported because
// the local executor, the herdr executor and the reconciler's sweep all take
// it, and three copies are how one of them came to skip it.

// SweepScope is what a run's leftover sweep may take: the resources NAMED for
// a job of one run — every job's branch is "ticfac/" + its job id, under the
// run's namespace, since #88 names every workspace and agent by job too —
// whose tick the run says is no longer needed.
type SweepScope struct {
	// Namespace is the branch prefix every job of the run writes under,
	// "ticfac/run-<run>/".
	Namespace string
	// Sweepable answers, for the tick a job's branch names ("" for a job
	// that names none, such as a base fold), whether nothing will come back
	// for that job's resources.
	Sweepable func(tickID string) bool
}

// TickOfBranch is the tick a job branch under the namespace names — the
// "tick-<id>" segment right after the namespace — and whether the branch is
// in the namespace at all.
func (s SweepScope) TickOfBranch(branch string) (string, bool) {
	if s.Namespace == "" || !strings.HasPrefix(branch, s.Namespace) {
		return "", false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(branch, s.Namespace), "/")
	tick, _ := strings.CutPrefix(first, "tick-")
	if tick == first {
		tick = ""
	}
	return tick, true
}

// Covers answers whether a job branch is this sweep's to take.
func (s SweepScope) Covers(branch string) bool {
	tick, ok := s.TickOfBranch(branch)
	return ok && s.Sweepable != nil && s.Sweepable(tick)
}

// JobOfBranch is the job id a job branch carries: the branch without the
// "ticfac/" every write ref is minted under.
func JobOfBranch(branch string) string {
	return strings.TrimPrefix(branch, "ticfac/")
}

// SweepReport is what one executor's half of a sweep did. Every line is a
// sentence for the run's feed; Held is the worktree paths the substrate still
// holds for something that may need them (a working or blocked agent, or a
// removal the substrate refused), which the git half must not remove from
// under it. A failed step is reported, never dropped: the next sweep retries
// it.
type SweepReport struct {
	Removed []string
	Held    []string
	Failed  []string
}

// WorktreeRegistration is one entry of git's worktree census: the directory
// and the branch it has checked out ("" for a detached or bare worktree).
type WorktreeRegistration struct {
	Path   string
	Branch string // the short branch name, without refs/heads/
}

// ListWorktrees is the repository's worktree census, from `git worktree list
// --porcelain`. The main checkout is included; callers match by path or by
// branch.
func ListWorktrees(repo string) ([]WorktreeRegistration, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var regs []WorktreeRegistration
	var reg *WorktreeRegistration
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
			reg = nil
		case strings.HasPrefix(line, "worktree "):
			regs = append(regs, WorktreeRegistration{Path: strings.TrimPrefix(line, "worktree ")})
			reg = &regs[len(regs)-1]
		case strings.HasPrefix(line, "branch ") && reg != nil:
			reg.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	return regs, nil
}

// SamePath compares two worktree paths as the directories they name: cleaned,
// and with symlinks resolved where the path still exists — git reports a
// worktree by its real path, and a record written before a symlinked temp
// root was resolved (macOS's /var → /private/var) must still match it.
func SamePath(a, b string) bool {
	return canonical(a) == canonical(b)
}

func canonical(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	// A path that no longer exists: resolve its parent, which usually does.
	dir, base := filepath.Split(filepath.Clean(path))
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, base)
	}
	return filepath.Clean(path)
}

// FindWorktree is the registration at path, if git holds one.
func FindWorktree(regs []WorktreeRegistration, path string) (WorktreeRegistration, bool) {
	for _, reg := range regs {
		if SamePath(reg.Path, path) {
			return reg, true
		}
	}
	return WorktreeRegistration{}, false
}

// RemoveWorktree removes one worktree and its registration: `git worktree
// remove --force`, then `git worktree prune` for a registration whose
// directory is already gone. --force is safe only behind PreserveUncommitted
// — every caller takes the snapshot first.
func RemoveWorktree(repo, dir string) error {
	return worktreeRemove(repo, dir)
}

// PruneWorktrees drops the registrations whose directories are gone.
func PruneWorktrees(repo string) error {
	_, err := git(repo, "worktree", "prune")
	return err
}

// DeleteBranch deletes one local branch. The caller has already decided the
// branch is meant to go — its commits are merged, or it carries none.
func DeleteBranch(repo, branch string) error {
	return branchDelete(repo, branch)
}

// PreserveUncommitted snapshots a worktree's uncommitted work onto a wip ref
// BEFORE a forced removal destroys it — the pbb rule, wherever this run
// destroys a worktree that may hold work. A clean worktree takes no snapshot
// (ok false). The ref is the job's own (WipRefFor); when an earlier snapshot
// already holds that ref — the wall-clock close took one — the new one goes
// beside it rather than over it, so no preserved work is ever replaced.
func PreserveUncommitted(worktree, jobID, artifactPrefix string) (WIPSnapshot, bool, error) {
	if _, err := os.Stat(worktree); err != nil {
		if os.IsNotExist(err) {
			return WIPSnapshot{}, false, nil
		}
		return WIPSnapshot{}, false, err
	}
	dirty, err := UncommittedWork(worktree, artifactPrefix)
	if err != nil {
		return WIPSnapshot{}, false, fmt.Errorf("read the worktree's uncommitted work: %w", err)
	}
	if !dirty {
		return WIPSnapshot{}, false, nil
	}
	ref := WipRefFor(jobID)
	if _, err := git(worktree, "rev-parse", "--verify", "--quiet", ref); err == nil {
		ref = WipRefFor(jobID + "-swept")
	}
	commit, err := snapshotWorktree(worktree, ref, artifactPrefix,
		"ticfac: work-in-progress snapshot before the worktree was removed — not evidence, never merged")
	if err != nil {
		return WIPSnapshot{}, false, err
	}
	return WIPSnapshot{SchemaVersion: WIPSnapshotSchemaVersion, Ref: ref, Commit: commit}, true, nil
}
