package reconcile

import (
	"errors"
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// One push per step (tick f61).
//
// Every record this run writes — a checkpoint, a claim, a note, a close —
// used to be its own push to the integration branch, about a dozen per tick.
// On GitHub each push is a round trip, a CI run and a share of a rate the
// forge asks a repository to keep under six a minute, and two live runs on
// one repository went past it (docs/analysis/github-failures.md).
//
// heldStep runs one sequence of writes HELD: each record is still its own commit,
// with its own message and guard, but the chain reaches origin as one push
// when the step ends (runstate/held.go says what is held and what lands at
// once, and why a held step adds no state a crash could leave behind).
//
// The rule for choosing a step: between its first write and its end, NOTHING
// acts outside the run on the strength of a held record — no job is started,
// no branch is pushed, no gate is run, no worktree is torn down, and nothing
// waits long enough for a reader of origin's checkpoint (`ticfac status`) to
// be misled. A write whose answer something DOES act on (a create, a claim)
// lands at once inside the step, carrying what was held before it.
//
// A step that panics — the simulated kill in tests, a crash anywhere — lands
// nothing: what it held is dropped, exactly as a process that died before
// its push would drop it.
func (r *Reconciler) heldStep(fn func() error) (err error) {
	if r.store == nil || r.store.Holding() {
		// Not yet open, or already inside a step: the outer step lands it.
		return fn()
	}
	r.store.Hold()
	returned := false
	defer func() {
		if !returned {
			r.store.Abandon()
		}
	}()
	err = fn()
	returned = true

	releaseErr := r.store.Release()
	if releaseErr == nil {
		return err
	}
	var conflict *runstate.HeldConflict
	if errors.As(releaseErr, &conflict) {
		// Someone else advanced the run under this step. Re-read what is
		// actually there, as a refused checkpoint always has — never retry
		// blindly.
		if _, fetchErr := r.store.Fetch(); fetchErr == nil {
			if previous, ok, readErr := r.store.Checkpoint(); readErr == nil && ok {
				r.sequence, r.ticks = previous.Sequence, previous.Ticks
			}
		}
		releaseErr = fmt.Errorf("reconcile: the run state moved under this reconciler: %w", releaseErr)
	} else {
		releaseErr = fmt.Errorf("reconcile: land the step's records on %s: %w", r.opts.Remote, releaseErr)
	}
	if err != nil {
		return errors.Join(err, releaseErr)
	}
	return releaseErr
}
