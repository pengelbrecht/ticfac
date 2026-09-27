package reconcile

import (
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Re-addressing a job the run dispatched for itself — a resolve-conflict job
// (resolve.go, refresh_resolve.go) or a gate repair (gate_repair.go) — after
// the incarnation that dispatched it is gone (epic-2jn, 4mv attempt 33).
//
// Such a job is not an attempt of the plan: no marker names it, and the
// incarnation that meets the same conflict or the same failed gate again
// finds it only by its deterministic identity — its job id, its state
// directory, its branch. The branch being on origin is NOT evidence that the
// job settled: the supervisor's timer pushes a live job's head, and a SIGTERM
// flush pushes it too — for a resolve job, the conflicted commit it was cut
// at, markers and all. Reading that head as the job's result is how 4mv's
// resume judged a resolution that had not happened yet, and refused it, while
// the worker was still making it.
//
// Whether the job settled is the executor's answer. Its Start is the one
// operation that asks it: a live job under the identity is ADOPTED (the same
// handle comes back, and the caller waits for it exactly as it waits for one
// it just started); a settled one is refused as settled (and only THEN is its
// branch the finished work the caller finishes from); and a job nobody is
// running — its disk gone with its container — is started, continuing from
// what it pushed.

// roleJob is what the run knows about an earlier incarnation's job under one
// identity before it asks the executor.
type roleJob struct {
	// base is the commit the job is (re)started from.
	base string
	// known says this host still holds the job's state: the executor adopts
	// it or refuses it as settled, and never starts it again.
	known bool
	// pushed says the job's branch is on origin while its state is gone: a
	// start continues from that head.
	pushed bool
}

// roleJobBase answers the base a run-dispatched job is started from:
//
//   - the base the job was cut from, when this host still holds its state —
//     the executor adopts the job or refuses it as settled, and the base is
//     what it states, not one minted afresh that the job never saw;
//   - its branch's pushed head, when the state is gone and origin carries the
//     branch — the work continues from what it pushed, and the executor
//     refuses to cut a job at a base its own pushed branch does not descend
//     from (a fresh mint is such a base: a conflicted merge committed again
//     is a different commit);
//   - otherwise fresh(): nothing was ever dispatched under the identity.
func (r *Reconciler) roleJobBase(stateRoot, branch string, fresh func() (string, error)) (roleJob, error) {
	if state, found := findAttemptState(stateRoot); found {
		if work, err := subprocess.ReadAttemptWork(state); err == nil && work.BaseSHA != "" {
			return roleJob{base: work.BaseSHA, known: true}, nil
		}
		base, err := fresh()
		return roleJob{base: base, known: true}, err
	}
	remote, err := r.git.remoteHead(branch)
	if err == nil && remote != "" {
		if err := r.git.fetch(branch); err != nil {
			return roleJob{}, fmt.Errorf("fetch %s, the pushed work of a job an earlier incarnation dispatched: %w",
				branch, err)
		}
		return roleJob{base: remote, pushed: true}, nil
	}
	base, err := fresh()
	return roleJob{base: base}, err
}

// roleJobResumeNote is the run's account of a job it re-addressed rather
// than dispatched, recorded once the executor has answered: empty for a job
// dispatched for the first time.
func roleJobResumeNote(job roleJob, what string) string {
	switch {
	case job.known:
		return fmt.Sprintf("%s an earlier incarnation of this run dispatched is still running; it is adopted by "+
			"identity and waited on — its branch on origin is where it pushes, never its result", what)
	case job.pushed:
		return fmt.Sprintf("%s an earlier incarnation of this run dispatched is gone with its state; it is "+
			"started again from the head it pushed (%s), and what it never pushed is redone", what, short(job.base))
	}
	return ""
}
