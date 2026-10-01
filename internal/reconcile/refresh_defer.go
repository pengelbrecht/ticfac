package reconcile

import (
	"context"
	"strings"
)

// A run-start fold of the base branch that does not land is DEFERRED, not a
// halt (epic hn6, run_09ebaf29).
//
// WHAT WAS WRONG. Every incarnation folds the epic's base branch into its
// integration branch before it plans (refresh.go), and a fold that did not
// land — a resolve job that failed on its merits, resolve jobs that never
// answered up to the bound, a conflict of a kind no job resolves — stopped
// the run for a person before a single tick was worked. hn6's run halted so
// with every tick of the epic still open and workable on the branch it had:
// a step whose only actor is a person, where the run could still make
// progress.
//
// THE RULE NOW.
//
//   - At run start, a base_refresh_conflict refusal is recorded
//     (base_refresh_deferred) and the run plans and works its ticks on the
//     unfolded epic branch. What that costs is stated, not hidden: ticks
//     filed on the base branch since the fork are not in the plan yet.
//   - Once every tick is closed — before the close-out's PR is readied, and
//     before anything else is built from the branch — the fold is retried.
//     Its resolve jobs get a FRESH allowance of operational retries: the
//     failures that exhausted the first one are recorded and named, but they
//     were paid at run start, and what failed then (hn6: a start commit the
//     container could not see) need not fail now. A resolve that failed on
//     its MERITS is still the fold's one resolve, and a conflict of a kind
//     no job resolves is still a person's — those are the stop, now with
//     every tick's work done.
//   - A retried fold that lands and brings open ticks the run never planned
//     stops RESUMABLY (base_fold_replan): the next incarnation plans them
//     from the folded branch, with no person.
//
// The fold at landing (land.go) is unchanged: it is the same fold, asked for
// again when the base moves under a ready PR.

// RefusedFoldReplan is the stop a deferred fold makes when, retried after
// every tick closed, it landed and brought ticks the run never planned. It
// needs nobody: the next incarnation re-plans from the folded branch.
const RefusedFoldReplan = "base_fold_replan"

// deferRunStartFold reports whether the run-start fold's error is a refusal
// the run defers, and records the deferral when it is.
func (r *Reconciler) deferRunStartFold(err error) bool {
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedBaseRefresh {
		return false
	}
	r.foldDeferred = refusal
	r.folded = ""
	r.record("", StageRefreshDeferred,
		"the fold of the epic's base branch into %s did not land, and the run does not stop for it: it works its "+
			"ticks on the unfolded branch (ticks filed on the base since the fork are not planned until the fold "+
			"lands) and retries the fold once every tick is closed. Why it did not land: %s",
		r.branch, refusal.Message)
	return true
}

// retryDeferredFold retries a fold the run start deferred, once every tick is
// closed: nil when there was none or it landed with nothing new to plan, the
// base_refresh_conflict stop when it still does not land, and the resumable
// base_fold_replan stop when it landed and brought open ticks.
func (r *Reconciler) retryDeferredFold(ctx context.Context) error {
	deferred := r.foldDeferred
	if deferred == nil {
		return nil
	}
	r.foldRetrying = true
	err := r.refreshFromBase(ctx)
	r.foldRetrying = false
	if err == nil {
		err = r.gateRunStartFold(ctx)
	}
	if err != nil {
		if refusal, ok := AsRefusal(err); ok && refusal.Reason == RefusedBaseRefresh {
			return r.refuse(RefusedBaseRefresh, "",
				"every tick of %s is closed on the unfolded branch, and the fold of the base branch the run start "+
					"deferred still does not land when retried: %s", r.opts.EpicID, refusal.Message)
		}
		return err
	}
	r.foldDeferred = nil
	if r.store != nil {
		if _, err := r.store.Fetch(); err != nil {
			return err
		}
	}
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return err
	}
	if plan := planFrom(graph); len(plan) > 0 {
		ids := make([]string, 0, len(plan))
		for _, entry := range plan {
			ids = append(ids, entry.TickID)
		}
		return r.refuse(RefusedFoldReplan, "",
			"the fold of the base branch the run start deferred has landed, and %s now carries open ticks this run "+
				"never planned (%s): the run stops to plan them from the folded branch, and the next incarnation "+
				"continues without a person", r.branch, strings.Join(ids, ", "))
	}
	return nil
}
