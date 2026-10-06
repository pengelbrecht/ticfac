package reconcile

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The landed epic's own close (epics hn6 and 43y, 2026-10-06).
//
// WHAT WAS WRONG. Epic hn6's run merged its PR (#219), verified CI on main and
// finished "completed … the epic is merged into main"; epic 43y's PR (#222)
// was merged and its run completed too. Every child was closed and the
// close-out with them — and both EPIC ticks stayed open, on main and on their
// epic branches, because nothing in a run ever closes the epic. So `tk` and
// the roadmap read two landed epics as open, `tk next` could route to them,
// and the ref sweep (#233) kept their unmerged refs as an open epic's,
// indefinitely.
//
// THE CLOSE. Once the run knows the epic has landed — it merged the PR itself
// and CI on the base is verified (the opt-in), or it found the base already
// carrying the epic (a person merged the PR; the default) — it closes the epic
// tick, with a reason naming the PR and the merge commit.
//
// ON THE BASE, NOT THE EPIC BRANCH. The tracker is read from the base: that is
// where `tk`, the roadmap and the ref sweep look. The epic branch is already
// merged, so a close committed there never reaches the base. The close is a
// tracker-only commit pushed to the base under a lease on the head the tracker
// read — the paths CI starts no run for (ciIgnoredPrefixes), so it neither
// re-runs CI nor deploys anything. It is not carried in the merge itself: a
// merge commit cannot name its own sha, and the opt-in closes only after CI on
// the base has passed on the merge.
//
// A lost lease is the base moving (another PR merging, a person filing a tick):
// the write is DISCARDED and made again on the moved head — never rebuilt path
// by path onto it, because the base's `.tick/activity/` gains lines from every
// writer and a per-path rebuild would drop theirs. An epic already closed on
// the base is left as it is: a re-entry, or a person who closed it first.

// prRef is how a merge commit's subject names its PR: GitHub's "Merge pull
// request #219 from …", a squash's "… (#219)", this run's own "… (#219)".
var prRef = regexp.MustCompile(`#([0-9]+)\b`)

// prOfMerge is the PR number a merge commit's subject names, zero when it
// names none.
func (r *Reconciler) prOfMerge(sha string) int {
	subject, err := r.git.run("", "log", "-1", "--format=%s", sha)
	if err != nil {
		return 0
	}
	if m := prRef.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// landedReason is the epic's close reason: the PR, the merge, who merged it,
// and what CI said on the base.
func (r *Reconciler) landedReason(pr int, base, landed string, byRun bool, ci string) string {
	what := "the epic"
	if pr != 0 {
		what = fmt.Sprintf("the epic PR #%d", pr)
	}
	who := "found merged by ticfac run " + r.runID
	if byRun {
		who = "merged by ticfac run " + r.runID
	}
	reason := fmt.Sprintf("Landed: %s is merged into %s as %s (%s)", what, base, short(landed), who)
	if ci != "" {
		reason += "; " + ci
	}
	return reason + "."
}

// closeLandedEpic closes the epic tick on the base branch, once the epic has
// landed there as `landed`. `pr` is the open PR the run merged, nil when a
// person merged it (the forge then answers no open PR) — the merge commit's
// subject names it instead.
func (r *Reconciler) closeLandedEpic(ctx context.Context, tick, base, landed string, pr *forge.PullRequest,
	byRun bool, ci string) error {

	epic := r.opts.EpicID
	if epic == "" {
		return nil
	}
	number := 0
	if pr != nil {
		number = pr.Number
	}
	if number == 0 {
		number = r.prOfMerge(landed)
	}
	reason := r.landedReason(number, base, landed, byRun, ci)
	refuse := func(format string, args ...any) error {
		return r.refuse(RefusedLandEpicClose, tick,
			"the epic %s is merged into %s as %s, and its tick could not be closed there: %s. Run the epic again "+
				"and the run closes it, or close it on %s by hand (tk close %s --reason …)",
			epic, base, short(landed), fmt.Sprintf(format, args...), base, epic)
	}

	tree, err := openTrackerTreeAs(r.git, r.opts.Remote, base, r.runID, r.runID+"-on-"+strings.ReplaceAll(base, "/", "-"))
	if err != nil {
		return refuse("the tracker's worktree on %s could not be prepared: %v", base, err)
	}
	defer tree.close()
	tracker, ok := relocate(r.opts.Tracker, tree.dir)
	if !ok {
		return refuse("the tracker cannot be pointed at a worktree of %s", base)
	}
	for try := 0; try < maxTrackerPushes; try++ {
		if err := tree.sync(); err != nil {
			return refuse("%v", err)
		}
		current, err := tracker.Show(ctx, epic)
		if err != nil {
			return refuse("the epic's record on %s could not be read: %v", base, err)
		}
		if current.Status == "closed" {
			r.record(tick, StageEpicClosed, "the epic %s is already closed on %s: nothing is closed twice", epic, base)
			return nil
		}
		if _, err := closeWithReason(ctx, tracker, epic, reason); err != nil {
			_ = tree.discard()
			return refuse("tk refused the close: %v", err)
		}
		commit, moved, err := tree.publishOnce("close epic " + epic + ", landed as " + short(landed))
		if err != nil {
			_ = tree.discard()
			return refuse("the close could not be pushed to %s: %v", base, err)
		}
		if moved {
			if err := tree.discard(); err != nil {
				return refuse("%v", err)
			}
			continue
		}
		at := short(commit)
		if commit == "" {
			at = "(no change on disk)"
		}
		r.record(tick, StageEpicClosed, "the epic %s is closed on %s as %s: %s", epic, base, at, reason)
		return nil
	}
	return refuse("%s moved under the close %d times running", base, maxTrackerPushes)
}

// reasonCloser is a tracker that closes with a reason: tk's `close --reason`.
type reasonCloser interface {
	CloseWithReason(ctx context.Context, tickID, reason string) (tk.Tick, error)
}

// closeWithReason closes with the reason where the tracker can, and otherwise
// notes the reason and closes.
func closeWithReason(ctx context.Context, tracker Tracker, tickID, reason string) (tk.Tick, error) {
	if closer, ok := tracker.(reasonCloser); ok {
		return closer.CloseWithReason(ctx, tickID, reason)
	}
	if _, err := tracker.Note(ctx, tickID, reason); err != nil {
		return tk.Tick{}, err
	}
	return tracker.Close(ctx, tickID)
}
