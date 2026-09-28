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
//   - A CONFLICT IS A RESOLVE-CONFLICT JOB, THEN A TYPED REFUSAL. A content
//     or add/add conflict is handed to the resolve-conflict job the attempt
//     merge already uses (refresh_resolve.go), bounded to one resolve per
//     fold; every other kind, and a resolve that fails, is the typed refusal
//     it always was. A reconciler that skipped the fold on a conflict would
//     be back to planning from a tracker that is missing ticks — silently,
//     which is the failure this whole file exists to remove.

// refreshFromBase folds the epic's base branch into the integration branch as
// origin has both, and answers with a typed refusal when they do not merge.
func (r *Reconciler) refreshFromBase(ctx context.Context) error {
	return r.refreshFrom(ctx, branchName(r.baseBranch(ctx), r.opts.Remote))
}

// refreshFrom is refreshFromBase against a named branch: the fold at run
// start, and the landing's fold of the branch the epic PR merges into
// (land.go) — one fold, with one resolve job and one lease, whoever asks.
func (r *Reconciler) refreshFrom(ctx context.Context, base string) error {
	r.folded = ""
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

	// A resolved fold, once a resolve-conflict job has made one: the merge
	// commit whose parents are the epic head it was resolved against and the
	// base head. Held across the loop so a lost lease folds THAT onto the
	// moved branch rather than paying for a second resolve; finalize is the
	// resolve's own records, landed once the fold is on the branch.
	var resolved string
	var finalize func() error

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
			if finalize != nil {
				if err := finalize(); err != nil {
					return err
				}
			}
			r.base = epicHead
			r.record("", StageRefreshed, "%s already carries %s at %s", r.branch, base, short(baseHead))
			return nil
		}

		target := baseHead
		if resolved != "" {
			target = resolved
		}
		merged, conflict, err := r.foldBase(base, target, epicHead, drivers)
		if err != nil {
			return err
		}
		if conflict != nil {
			if resolved != "" {
				// The resolved fold was made against an epic head the branch
				// has since moved past, and what moved it conflicts with the
				// resolution. A second resolve is not what the bound allows.
				return r.refuse(RefusedBaseRefresh, "",
					"the resolve-conflict job's fold of %s at %s into %s (%s) no longer merges onto %s at %s: %s. "+
						"A second resolve of one fold is not this run's to pay for; merge %s into %s by hand and run "+
						"the epic again",
					base, short(baseHead), r.branch, short(resolved), r.branch, short(epicHead),
					strings.Join(conflict.Files, ", "), short(resolved), r.branch)
			}
			if !conflict.resolvable() {
				return r.refuse(RefusedBaseRefresh, "",
					"%s of %s does not fold into %s: %s (%s). Every tick filed on %s since this branch forked is "+
						"invisible to this run until somebody resolves that — and a conflict of this kind is one "+
						"side's change making the other's meaningless, which is a person's decision, not a "+
						"resolve-conflict job's",
					short(baseHead), base, r.branch, strings.Join(conflict.Files, ", "), conflict.Detail, base)
			}
			// A content or add/add conflict between the base and the epic is
			// two intents a worker holding both can union (refresh_resolve.go):
			// the run dispatches a resolve-conflict job instead of stopping.
			resolved, finalize, err = r.resolveBaseFold(ctx, base, baseHead, epicHead, conflict, drivers)
			if err != nil {
				return err
			}
			if first, _ := r.git.run("", "rev-parse", resolved+"^1"); first != epicHead {
				// Resolved against an epic head the branch has moved past (a
				// job an earlier incarnation cut, adopted live or finished from
				// its branch): the next pass folds the resolution onto the head
				// the branch has now with a real merge — the mint's first
				// parent is the head the job resolved, never this one.
				continue
			}
			merged = resolved
		}
		_, stderr, pushErr := r.git.try("", "push",
			"--force-with-lease="+refFor(r.branch)+":"+epicHead,
			r.opts.Remote, merged+":"+refFor(r.branch))
		if pushErr == nil {
			if finalize != nil {
				// The fold is on the branch: land the resolve's decision record
				// and retire its branch — after the push, because both write
				// the run branch and a write before it is a lease it loses.
				if err := finalize(); err != nil {
					return err
				}
			}
			r.base = merged
			r.folded = merged
			r.record("", StageRefreshed, "%s of %s is folded into %s as %s",
				short(baseHead), base, r.branch, short(merged))
			return nil
		}
		if !leaseRefused(stderr) {
			// integrate.go's reason: an auth failure, an unreachable remote or
			// a declined push is not a lease race, and reporting it as one
			// sends the next repair at a conflict nobody had.
			return fmt.Errorf("push the refresh of %s from %s to %s: %w: %s",
				r.branch, base, r.opts.Remote, pushErr, firstLine(stderr))
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
//
// `target` is the base head, or — once a resolve-conflict job has resolved
// the fold — the resolved merge, folded onto a branch that moved under it. A
// merge that did not merge answers the conflict it was, for the caller to
// resolve or refuse; nothing here decides which.
func (r *Reconciler) foldBase(base, target, epicHead string, drivers map[string]string) (string, *mergeConflict, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-refresh-", epicHead)
	if err != nil {
		return "", nil, fmt.Errorf("prepare the refresh worktree at %s: %w", short(epicHead), err)
	}
	defer remove()

	args := driverConfig(drivers)
	message := fmt.Sprintf("Merge branch '%s' into %s\n\nticfac run %s: refresh the integration branch "+
		"from the epic's base branch", base, r.branch, r.runID)
	args = append(args, "merge", "--no-ff", "--no-edit", "-m", message, target)

	if stdout, stderr, err := r.git.try(dir, args...); err != nil {
		unmerged, _ := r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		_, _, _ = r.git.try(dir, "merge", "--abort")
		if strings.TrimSpace(unmerged) == "" {
			// Not a conflict: git could not run the merge at all. That is an
			// operational problem, and calling it a conflict would send the
			// next repair at a file nobody touched.
			return "", nil, fmt.Errorf("merge %s into %s: %w: %s", base, r.branch, err, firstLine(stderr))
		}
		return "", classifyMergeFailure(stdout, stderr, unmerged, err), nil
	}
	merged, err := r.git.run(dir, "rev-parse", "HEAD")
	return merged, nil, err
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
	if isBranchRef(r.opts.BaseRef) {
		return r.opts.BaseRef
	}
	// --base named a COMMIT — HEAD by default, or the submitted SHA a cloud
	// container passes — which says where the integration branch was cut
	// from, not what flows into it afterwards. What flows in is the remote's
	// default branch (ticks wvd and rf3). Treating "cut from a commit" as "no
	// branch to fold" is what left a running epic blind to every tick filed on
	// main after it started, and made operator re-gating silently inert.
	return r.remoteDefaultBranch()
}

// isBranchRef reports whether ref names a branch rather than a commit: not
// empty, not HEAD, and not a bare hex object id.
func isBranchRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		return false
	}
	if len(ref) >= 7 && len(ref) <= 64 && strings.Trim(strings.ToLower(ref), "0123456789abcdef") == "" {
		return false
	}
	return true
}

// remoteDefaultBranch is the remote's default branch as a branch NAME: the
// remote's HEAD as this checkout holds it, else as the remote itself answers
// (a cloud container's checkout is a fetch of one commit and holds no
// refs/remotes/<remote>/HEAD), else "" — never guessed.
func (r *Reconciler) remoteDefaultBranch() string {
	if out, err := r.git.run("", "symbolic-ref", "--short", "refs/remotes/"+r.opts.Remote+"/HEAD"); err == nil && out != "" {
		return strings.TrimPrefix(out, r.opts.Remote+"/")
	}
	if out, err := r.git.run("", "ls-remote", "--symref", r.opts.Remote, "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "ref:" && strings.HasPrefix(fields[1], "refs/heads/") {
				return strings.TrimPrefix(fields[1], "refs/heads/")
			}
		}
	}
	return ""
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

// gateRunStartFold runs the integrated gate over the fold this run start just
// pushed, when the epic branch already carries work this run integrated — and
// a failing check is the repair job's, before any worker is cut from the tree
// (epic-6in, 2026-09-28).
//
// The fold is a merge like any other, and a merge can be clean as text and
// broken as code. 6in's run-start fold of main (28385b75) merged main's edit
// of contracts/status-model.json beside the epic's re-cut contract bundle:
// no conflict, and a digest the bundle no longer matched. Nothing gated the
// fold, so it went to origin, CI went red on it (go and go race, every
// internal/contracts check), and the next worker, dz1, was dispatched onto
// the broken tree and paid to fix a break that was not its tick's. Every
// other merge onto the integration branch is gated before anything builds on
// it; the readying's fold of the base already is (land.go's gateLanding).
// This is that gate, at the fold every run start makes.
//
// It is skipped when the run has closed nothing yet: the epic branch is then
// the base plus nothing this run merged, the base's own CI speaks for it, and
// there is no work a repair could be dispatched under.
func (r *Reconciler) gateRunStartFold(ctx context.Context) error {
	folded := r.folded
	if folded == "" || r.store == nil {
		return nil
	}
	owner, err := r.ciRepairOwner(ctx)
	if err != nil || owner == nil {
		return err
	}
	base := branchName(r.baseBranch(ctx), r.opts.Remote)
	if _, err := r.gateLanding(ctx, owner, base, folded); err != nil {
		return err
	}
	// The gate, and a repair's merge when it ran one, wrote the run branch:
	// what the run plans from is what origin now holds.
	_, err = r.store.Fetch()
	return err
}
