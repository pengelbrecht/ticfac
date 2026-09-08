package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The fold of the epic's BASE branch into its integration branch, at run start
// and at every restart.
//
// An EpicRun integration branch is cut from the base branch once and then
// diverges the moment either side writes — and BOTH sides write tracker
// records. The run writes claims, notes and closes onto the integration branch
// (tracker.go); people and other runs file ticks on the base branch. Nothing
// folded the base back in, and the Phase 1 gate run paid for it: after epic cia
// finished, two follow-up ticks were created on ticks' main, the next run
// (gate-cia-3) read its tracker from `epic/cia` where neither exists, and
// refused with "epic cia has no dispatchable tick" while `tk graph` on main
// listed both as ready.
//
// So before anything is planned, the reconciler merges origin's base branch
// into origin's integration branch. Three things make it a fold rather than a
// hope:
//
//   - IT IS MERGED WITH TK'S OWN DRIVERS. `.tick/issues/*.json` and
//     `.tick/activity/activity.jsonl` are formats tk owns, and a repository
//     declares them in its `.gitattributes` as `merge=tick` and
//     `merge=tick-activity`. Git reaches those drivers by NAME, so the
//     commands go in front of git as `-c merge.<name>.driver=...`, built by
//     internal/tk from the manifest's own argv (tk.MergeDrivers).
//
//   - IT LANDS UNDER THE STORE'S COMPARE-AND-SWAP. The integration branch is
//     the ref the run-state store moves constantly; the fold is pushed with
//     `--force-with-lease` on the head it merged onto, and a lost lease
//     rebuilds the merge rather than forcing over what arrived.
//
//   - A CONFLICT IS A TYPED REFUSAL. Resolving one is a role-job with its own
//     contract (resolve-conflict, Phase 2). A reconciler that skipped the fold
//     on a conflict would be back to planning from a tracker that is missing
//     ticks — silently, which is the failure this whole file exists to remove.

// refreshFromBase folds the epic's base branch into the integration branch as
// origin has both, and answers with a typed refusal when they do not merge.
func (r *Reconciler) refreshFromBase(ctx context.Context) error {
	base := branchName(r.baseBranch(ctx), r.opts.Remote)
	if base == "" || base == r.branch {
		return nil
	}
	baseHead, err := r.git.remoteHead(base)
	if err != nil {
		return err
	}
	if baseHead == "" {
		// The epic names no base branch and the run was cut from a commit —
		// `--base HEAD` is the default. There is no branch to fold, and
		// inventing one is not this reconciler's decision to make.
		r.record("", StageRefreshed, "%s has no branch %s: there is nothing to refresh %s from",
			r.opts.Remote, base, r.branch)
		return nil
	}
	if err := r.git.fetch(base); err != nil {
		return fmt.Errorf("fetch %s from %s: %w", base, r.opts.Remote, err)
	}
	drivers, err := r.mergeDrivers()
	if err != nil {
		return err
	}

	for try := 0; try < maxMergePushes; try++ {
		epicHead, err := r.git.remoteHead(r.branch)
		if err != nil {
			return err
		}
		if err := r.git.fetch(r.branch); err != nil {
			return err
		}
		if r.git.contains(baseHead, epicHead) {
			// The branch already carries the base — an earlier incarnation of
			// this run folded it, or the branch was cut a moment ago. Nothing
			// is merged twice.
			r.base = epicHead
			r.record("", StageRefreshed, "%s already carries %s at %s", r.branch, base, short(baseHead))
			return nil
		}

		merged, err := r.foldBase(base, baseHead, epicHead, drivers)
		if err != nil {
			return err
		}
		_, _, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
			r.base = merged
			r.record("", StageRefreshed, "%s of %s is folded into %s as %s",
				short(baseHead), base, r.branch, short(merged))
			return nil
		}
		// The branch moved under this writer — the run-state store's own
		// records land on it. Rebuild the fold on the new head rather than
		// forcing over whatever arrived.
	}
	return fmt.Errorf("%s moved under this reconciler %d times running while folding %s into it; "+
		"that is an operational problem, not a conflict to spin on", r.branch, maxMergePushes, base)
}

// foldBase performs the merge itself, in a DETACHED worktree of its own — for
// integrate.go's reason: the integration branch must not be checked out
// anywhere the run-state store might move it.
func (r *Reconciler) foldBase(base, baseHead, epicHead string, drivers map[string]string) (string, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-refresh-", epicHead)
	if err != nil {
		return "", fmt.Errorf("prepare the refresh worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	args := driverConfig(drivers)
	message := fmt.Sprintf("Merge branch '%s' into %s\n\nticfac run %s: refresh the integration branch "+
		"from the epic's base branch", base, r.branch, r.runID)
	args = append(args, "merge", "--no-ff", "--no-edit", "-m", message, baseHead)

	if _, stderr, err := r.git.try(dir, args...); err != nil {
		conflicts, _ := r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		_, _, _ = r.git.try(dir, "merge", "--abort")
		if conflicts == "" {
			// Not a conflict: git could not run the merge at all. That is an
			// operational problem, and calling it a conflict would send the
			// next repair at a file nobody touched.
			return "", fmt.Errorf("merge %s into %s: %w: %s", base, r.branch, err, firstLine(stderr))
		}
		return "", r.refuse(RefusedBaseRefresh, "",
			"%s of %s does not fold into %s: %s. Every tick filed on %s since this branch forked is invisible to "+
				"this run until somebody resolves that — which is a person's decision, or a resolve-conflict job's, "+
				"and not a merge this reconciler may perform",
			short(baseHead), base, r.branch, strings.Join(strings.Fields(conflicts), ", "), base)
	}
	return r.git.run(dir, "rev-parse", "HEAD")
}

// driverConfig is the `-c merge.<name>.driver=...` git needs to resolve the
// tracker's records the way tk does. The names are sorted so that one fold and
// the next put the same command line in front of git.
func driverConfig(drivers map[string]string) []string {
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)

	var args []string
	for _, name := range names {
		args = append(args,
			"-c", "merge."+name+".name=the tracker's own resolution for its records",
			"-c", "merge."+name+".driver="+drivers[name])
	}
	return args
}

// mergeDrivers is how git must resolve `.tick/` during the fold: the tracker's
// own commands, taken from the tracker this run was given, because the tk that
// merges a record has to be the tk that wrote it.
func (r *Reconciler) mergeDrivers() (map[string]string, error) {
	if source, ok := r.opts.Tracker.(interface {
		MergeDrivers() (map[string]string, error)
	}); ok {
		return source.MergeDrivers()
	}
	return tk.DefaultMergeDrivers()
}

// baseBranch is the branch this epic is cut from: the epic tick's own
// `base_branch`, and the run's `--base` when the tracker carries none.
//
// The tracker is read rather than assumed because an epic that declares a base
// declares it there — a run told `--base HEAD` on a machine whose checkout is
// on some other branch would otherwise fold nothing, which is the silence this
// file exists to remove. An epic the tracker cannot show is not an error here:
// the graph is read a moment later, and refusing twice for one reason tells an
// operator less, not more.
func (r *Reconciler) baseBranch(ctx context.Context) string {
	if epic, err := r.tracker.Show(ctx, r.opts.EpicID); err == nil {
		if declared := strings.TrimSpace(epic.BaseBranch); declared != "" {
			return declared
		}
	}
	return r.opts.BaseRef
}

// branchName is a base as a BRANCH: `refs/heads/main`, `origin/main` and
// `main` are one branch named three ways, and only the last is what a remote
// answers about.
func branchName(ref, remote string) string {
	name := strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	if remote != "" {
		name = strings.TrimPrefix(name, remote+"/")
	}
	return name
}
