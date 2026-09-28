package reconcile

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
)

// The leftover sweep (epic-6in).
//
// A teardown addresses one job through its handle, and three kinds of job
// were never addressed again once their one teardown did not happen:
//
//   - a START that never ran — 4i8's repair-3, refused by herdr before #88
//     over an agent name, left a workspace with no agent, a worktree and a
//     branch; the tick's gate then passed on a re-run, so the repair was never
//     needed and nothing owned its leftovers;
//   - a teardown that FAILED — 46x attempt 2's disposal after its wall-clock
//     stop, refused by git over the order of its own steps;
//   - everything a KILLED orchestrator had in flight — a SIGKILL runs no
//     defer and no close.
//
// An operator cleaned each of them by hand. The sweep makes that nobody's
// job: every resource a run makes is named for its job — the job's branch is
// "ticfac/" + its job id, under the run's namespace, and since #88 its herdr
// workspace and agent carry the job's name too — so the run can find, by
// name, everything it made for a tick nothing will come back for, and take it
// down in the one order a teardown uses: the substrate's workspace and panes
// (each executor's half), then the git worktree and its registration, then
// the branch — only when its commits are merged or it carries none. A branch
// holding commits nothing merged is KEPT, as disposal keeps it; a wip ref is
// never swept (tick pbb: it retires only with the record naming it), and a
// worktree holding uncommitted work has it put on a wip ref of its own first.
//
// The sweep runs where the run knows a tick needs nothing more: when the tick
// closes (after its cleanUp), at run start and resume (for every tick the
// tracker says is closed — the crashed or killed run's leftovers), and at run
// end. A step that fails is recorded and left for the next sweep; a sweep
// never refuses the run.

// LeftoverSweeper is an executor that can find and remove, without a handle,
// the substrate resources it made for a run's jobs. The herdr executor is
// one; an executor that makes nothing outside git needs none, because the git
// half of the sweep is the run's own.
type LeftoverSweeper interface {
	SweepLeftovers(ctx context.Context, scope subprocess.SweepScope) (subprocess.SweepReport, error)
}

// sweepNamespace is the branch prefix every job of this run writes under.
func (r *Reconciler) sweepNamespace() string {
	return strings.TrimPrefix(attemptRefPrefix(r.runID), "refs/heads/")
}

// sweepTick sweeps one tick's leftovers — the close's half.
func (r *Reconciler) sweepTick(tick string) {
	if tick == "" {
		return
	}
	r.sweepLeftovers(context.Background(), tick, func(t string) bool { return t == tick })
}

// sweepClosed sweeps every job of this run whose tick the tracker says is
// closed — run start, resume and run end. everything also takes the jobs
// that name no tick (a base fold's): only at the end of a run that completed,
// when nothing of the run will be asked for again.
func (r *Reconciler) sweepClosed(ctx context.Context, everything bool) {
	closed := map[string]bool{}
	r.sweepLeftovers(ctx, "", func(tick string) bool {
		if tick == "" {
			return everything
		}
		if answer, ok := closed[tick]; ok {
			return answer
		}
		current, err := r.tracker.Show(ctx, tick)
		answer := err == nil && current.Status == "closed"
		closed[tick] = answer
		return answer
	})
}

// sweepLeftovers is one sweep over the scope. tick names the tick the feed
// lines are recorded under ("" for a run-level sweep).
func (r *Reconciler) sweepLeftovers(ctx context.Context, tick string, sweepable func(string) bool) {
	if r.tracker == nil || r.opts.Repo == "" || r.git == nil {
		return
	}
	r.sweepMu.Lock()
	defer r.sweepMu.Unlock()
	scope := subprocess.SweepScope{Namespace: r.sweepNamespace(), Sweepable: sweepable}
	say := func(format string, args ...any) { r.record(tick, StageCleanedUp, format, args...) }

	// The substrate's half first: a workspace removed after its worktree is
	// a workspace open on a directory that no longer exists.
	held := map[string]bool{}
	for _, s := range r.leftoverSweepers() {
		report, err := s.sweeper.SweepLeftovers(ctx, scope)
		if err != nil {
			say("the %s executor's leftovers were not swept: %v; the next sweep retries", s.name, err)
			// Nothing it holds is known to be free: its worktrees are left
			// for the next sweep rather than removed from under it.
			return
		}
		for _, line := range report.Removed {
			say("swept: %s", line)
		}
		for _, line := range report.Failed {
			say("not swept: %s", line)
		}
		for _, path := range report.Held {
			held[canonicalPath(path)] = true
		}
	}

	// The git worktrees and their registrations.
	regs, err := subprocess.ListWorktrees(r.opts.Repo)
	if err != nil {
		say("the run's leftover worktrees were not swept: the worktree census failed: %v", err)
		return
	}
	checkedOut := map[string]bool{}
	for _, reg := range regs {
		if reg.Branch == "" || !scope.Covers(reg.Branch) || subprocess.SamePath(reg.Path, r.opts.Repo) {
			if reg.Branch != "" {
				checkedOut[reg.Branch] = true
			}
			continue
		}
		if held[canonicalPath(reg.Path)] {
			checkedOut[reg.Branch] = true
			continue
		}
		snap, preserved, err := subprocess.PreserveUncommitted(reg.Path, subprocess.JobOfBranch(reg.Branch), "")
		if err != nil {
			checkedOut[reg.Branch] = true
			say("not swept: the worktree %s of %s: its uncommitted work could not be preserved first (%v); the next "+
				"sweep retries", reg.Path, reg.Branch, err)
			continue
		}
		if preserved {
			say("swept: preserved the uncommitted work in %s on %s (commit %s) before its worktree was removed",
				reg.Path, snap.Ref, short(snap.Commit))
		}
		if err := subprocess.RemoveWorktree(r.opts.Repo, reg.Path); err != nil {
			checkedOut[reg.Branch] = true
			say("not swept: the worktree %s of %s could not be removed (%v); the next sweep retries", reg.Path, reg.Branch, err)
			continue
		}
		say("swept: removed the worktree %s of %s and its registration", reg.Path, reg.Branch)
	}
	if err := subprocess.PruneWorktrees(r.opts.Repo); err != nil {
		say("the worktree registrations were not pruned: %v", err)
	}

	// The branches: only those whose commits are already on the integration
	// branch — merged, or never carrying any — and never one still checked
	// out.
	branches, err := r.git.run("", "for-each-ref", "--format=%(refname)", "refs/heads/"+scope.Namespace)
	if err != nil {
		say("the run's leftover branches were not swept: %v", err)
		return
	}
	var integration []string
	var names []string
	for _, line := range strings.Split(branches, "\n") {
		if name := strings.TrimPrefix(strings.TrimSpace(line), "refs/heads/"); name != "" && name != r.branch {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, branch := range names {
		if !scope.Covers(branch) || checkedOut[branch] {
			continue
		}
		head, err := r.git.resolve(refFor(branch))
		if err != nil || head == "" {
			continue
		}
		if integration == nil {
			// Asked once, and only when there is a branch to judge: it
			// reads origin.
			integration = append(r.integrationHeads(), "")
		}
		merged := false
		for _, into := range integration {
			if into != "" && (head == into || r.git.contains(head, into)) {
				merged = true
				break
			}
		}
		if !merged {
			if !r.sweepKept[branch] {
				if r.sweepKept == nil {
					r.sweepKept = map[string]bool{}
				}
				r.sweepKept[branch] = true
				say("kept: the branch %s holds commits %s does not have", branch, r.branch)
			}
			continue
		}
		if err := subprocess.DeleteBranch(r.opts.Repo, branch); err != nil {
			say("not swept: the branch %s could not be deleted (%v); the next sweep retries", branch, err)
			continue
		}
		say("swept: deleted the branch %s — its commits are on %s", branch, r.branch)
	}
}

// integrationHeads is every head of the integration branch this checkout can
// name: origin's, and the local ref's. A branch contained in either carries
// nothing the integration branch does not already have.
func (r *Reconciler) integrationHeads() []string {
	var heads []string
	if remote, err := r.git.remoteHead(r.branch); err == nil && remote != "" {
		heads = append(heads, remote)
	}
	if local, err := r.git.resolve(refFor(r.branch)); err == nil && local != "" {
		heads = append(heads, local)
	}
	if r.base != "" {
		heads = append(heads, r.base)
	}
	return heads
}

// namedSweeper is one executor's sweep, under the executor's name.
type namedSweeper struct {
	name    string
	sweeper LeftoverSweeper
}

// leftoverSweepers builds, once per run, one executor for each executor the
// run's profiles route to, and keeps those that can sweep.
func (r *Reconciler) leftoverSweepers() []namedSweeper {
	r.sweepOnce.Do(func() {
		if r.opts.NewExecutor == nil {
			return
		}
		byExecutor := map[string]Dispatch{}
		consider := func(d Dispatch) {
			if d.Profile == nil {
				return
			}
			if _, ok := byExecutor[d.Profile.Executor]; !ok {
				byExecutor[d.Profile.Executor] = d
			}
		}
		roles := make([]string, 0, len(r.profiles))
		for role := range r.profiles {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			consider(r.sweepDispatch(role, r.profiles[role]))
			tiers := make([]string, 0, len(r.tierProfiles[role]))
			for tier := range r.tierProfiles[role] {
				tiers = append(tiers, tier)
			}
			sort.Strings(tiers)
			for _, tier := range tiers {
				consider(r.sweepDispatch(role, r.tierProfiles[role][tier]))
			}
		}
		names := make([]string, 0, len(byExecutor))
		for name := range byExecutor {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			executor, _, err := r.opts.NewExecutor(byExecutor[name])
			if err != nil {
				r.record("", StageCleanedUp, "the %s executor could not be built to sweep this run's leftovers: %v",
					name, err)
				continue
			}
			if s, ok := executor.(LeftoverSweeper); ok {
				r.sweepers = append(r.sweepers, namedSweeper{name: name, sweeper: s})
			}
		}
	})
	return r.sweepers
}

// sweepDispatch is the dispatch a sweeping executor is built from: the run's
// identity and one profile that routes to it, and no job — a sweep addresses
// none.
func (r *Reconciler) sweepDispatch(role string, p *profile.Profile) Dispatch {
	return Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, Role: role, Attempt: 1,
		Repo: r.opts.Repo, Remote: r.opts.Remote, BaseRef: r.opts.BaseRef,
		StateDir: filepath.Join(r.opts.ExecStateRoot, r.runID, ".sweep"),
		Profile:  p,
	}
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
