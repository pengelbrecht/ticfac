package herdr

import (
	"context"
	"fmt"
	"sort"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The run's sweep, herdr's half (epic-6in: 4i8's repair-3).
//
// A disposal addresses ONE job through its handle, and a job nothing ever
// comes back for is never disposed: the repair start #88's name clash
// refused left a workspace with no agent in it, a worktree and a branch, the
// tick's gate then passed on a re-run, and nothing owned the leftovers — an
// operator removed them by hand. The sweep needs no handle. Every job's
// branch is "ticfac/" + its job id, under the run's namespace, so herdr's own
// answer to "what exists" — each workspace's worktree and the branch checked
// out there — names the run and the tick of everything this run made, and
// the run says which of those ticks nothing will come back for.
//
// What it removes is exactly what disposal would: the workspace, its panes
// and the worktree herdr opened, in one worktree.remove, after the same
// liveness rule — a workspace whose agent is working or blocked is HELD, and
// so is one herdr will not remove, and the git half of the sweep leaves a
// held worktree alone. Uncommitted work is put on the job's wip ref before
// the removal may force past it. A workspace attributed by its label alone
// is never swept: a label carries no run.

// SweepLeftovers removes the herdr workspaces of this run's jobs that the
// scope says nothing will come back for.
func (e *Executor) SweepLeftovers(ctx context.Context, scope subprocess.SweepScope) (subprocess.SweepReport, error) {
	var report subprocess.SweepReport
	sv, err := e.survey(ctx)
	if err != nil {
		return report, fmt.Errorf("sweep: %w", err)
	}
	workspaces := append([]client.WorkspaceInfo(nil), sv.snapshot.Workspaces...)
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].WorkspaceID < workspaces[j].WorkspaceID })
	for _, ws := range workspaces {
		path, branch := sv.checkoutOf(ws)
		if branch == "" || !scope.Covers(branch) {
			continue
		}
		switch ws.AgentStatus {
		case client.StatusWorking, client.StatusBlocked:
			report.Held = append(report.Held, path)
			report.Failed = append(report.Failed, fmt.Sprintf(
				"the herdr workspace %s (%s) is left: its agent is %s — a working agent may be mid-turn and a "+
					"blocked one is waiting on a person; the next sweep asks again", ws.WorkspaceID, branch, ws.AgentStatus))
			continue
		}
		preserved := false
		if path != "" {
			snap, ok, err := subprocess.PreserveUncommitted(path, subprocess.JobOfBranch(branch), "")
			if err != nil {
				report.Held = append(report.Held, path)
				report.Failed = append(report.Failed, fmt.Sprintf(
					"the herdr workspace %s (%s) is left: its uncommitted work could not be preserved first (%v)",
					ws.WorkspaceID, branch, err))
				continue
			}
			if ok {
				preserved = true
				report.Removed = append(report.Removed, fmt.Sprintf(
					"preserved the uncommitted work in %s on %s (commit %s) before its workspace was removed",
					path, snap.Ref, short(snap.Commit)))
			}
		}
		_, err := e.client.WorktreeRemove(ctx, client.WorktreeRemoveParams{WorkspaceID: ws.WorkspaceID, Force: preserved})
		if err != nil && !client.IsCode(err, client.CodeWorkspaceNotFound) {
			report.Held = append(report.Held, path)
			report.Failed = append(report.Failed, fmt.Sprintf(
				"herdr would not remove the workspace %s (%s): %v; the next sweep retries it", ws.WorkspaceID, branch, err))
			continue
		}
		report.Removed = append(report.Removed, fmt.Sprintf(
			"removed the herdr workspace %s of %s, with its panes and its worktree %s", ws.WorkspaceID, branch, path))
	}
	return report, nil
}

// checkoutOf is the worktree a workspace is open on and the branch checked
// out there, from herdr's own two answers: the snapshot's worktree block, and
// the listing's branch for that path or for that open workspace.
func (sv *workspaceSurvey) checkoutOf(ws client.WorkspaceInfo) (path, branch string) {
	if ws.Worktree != nil {
		path = ws.Worktree.CheckoutPath
	}
	if sv.listing == nil {
		return path, ""
	}
	for _, wt := range sv.listing.Worktrees {
		open := wt.OpenWorkspaceID != nil && *wt.OpenWorkspaceID == ws.WorkspaceID
		if (path != "" && samePath(wt.Path, path)) || (path == "" && open) {
			if path == "" {
				path = wt.Path
			}
			if wt.Branch != nil {
				branch = *wt.Branch
			}
			return path, branch
		}
	}
	return path, ""
}
