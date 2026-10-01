package reconcile

import (
	"context"
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// An attempt the run can no longer address, on an executor whose jobs live
// at the factory, is asked of the factory before anybody is (the operator's
// decision after #177).
//
// attempt_unaddressed and wiped both say "nobody can say whether this attempt
// is running": its bounds are spent and it has not settled, or the run went
// without addressing it past the substrate's wipe threshold (a host that
// slept). For a LOCAL executor that is the truth — the run is the only party
// that could know — and the attempt is held for a person to look at, as it
// always was. For an executor whose jobs live at the factory (FactoryJobs,
// #160) it is not: the factory keeps the job's records and answers for it by
// identity, and "a refusal whose only actor is a person is a defect unless it
// really needs judgment". So the run asks the factory again:
//
//   - the factory says the job settled: the run collects it, as it would have
//     on the poll before;
//   - the factory says the job is running: the run waits on it, a bounded
//     number of times (maxOperationalRetries), each said on the feed;
//   - the factory cannot answer (lost, unreachable) or the waits are spent:
//     the refusal is marked factoryUnanswered. The window then RELEASES the
//     attempt — durably, the record `ticfac settle` writes, attributed to the
//     run and carrying whatever the attempt committed — and dispatches the
//     tick again in-run, at most maxOperationalRetries times; after that it
//     is held exactly as it always was. The released attempt earns the tier
//     ladder no rung: nothing about the tick was decided, only the substrate
//     was.

// rejectedStepUnanswered is the step a run release of an attempt the factory
// could not answer for records. It spends no rung of the ladder and none of
// the rejected-work bound.
const rejectedStepUnanswered = "factory_unanswered"

// askFactoryAgain asks the factory about an attempt the run is about to
// refuse as unaddressed or wiped. answered says the factory's answer stands
// instead of the refusal: a settled status to collect, or nil — the job is
// running and the run waits on it. Otherwise the refusal is marked
// factoryUnanswered for the window, and the attempt's committed work is put
// where the run's release can carry it from. A local executor is never asked:
// it answers false and leaves the refusal untouched.
func (r *Reconciler) askFactoryAgain(ctx context.Context, fl *inflightAttempt, refusal *Refusal) (*subprocess.JobStatus, bool) {
	if refusal == nil || fl == nil || fl.executor == nil || !jobsLiveAtFactory(fl.executor) || ctx.Err() != nil {
		return nil, false
	}
	marker := fl.marker
	name := r.attemptName(marker.TickID, marker.Attempt)
	status, err := fl.executor.Inspect(fl.handle, fl.cursor)
	switch {
	case err == nil && status.Terminal:
		r.record(marker.TickID, StageWaiting,
			"%s could no longer be addressed (%s), so the run asked the factory again before holding it: the "+
				"factory answers it settled as %s, and it is collected", name, refusal.Reason, status.State)
		return status, true
	case err == nil && status.State != subprocess.StateLost && fl.factoryWaits < maxOperationalRetries:
		fl.factoryWaits++
		r.noteAlive(marker.JobID)
		fl.overdueAt = r.now()
		r.record(marker.TickID, StageWaiting,
			"%s could no longer be addressed (%s), so the run asked the factory again before holding it: the "+
				"factory answers it %s, and the run keeps waiting on it (%d of at most %d)",
			name, refusal.Reason, status.State, fl.factoryWaits, maxOperationalRetries)
		return nil, true
	}
	said := "could not be reached"
	switch {
	case err != nil:
		said = "could not be reached: " + firstLine(err.Error())
	case status.State == subprocess.StateLost:
		said = "answers lost"
	default:
		said = fmt.Sprintf("still answers %s after %d waits", status.State, fl.factoryWaits)
	}
	refusal.Message += fmt.Sprintf(" — asked the factory again: it %s", said)
	refusal.factoryUnanswered = true
	unanswered := marker
	refusal.unanswered = &unanswered
	// What the attempt committed goes where the release reads it: the local
	// branch's head pushed to the write ref, or — for a container that
	// pushed to a landing branch of its own — what the executor collects.
	r.preserveAttemptWork(marker)
	if r.rejectedWorkHead(marker) == "" {
		if collected, cerr := fl.executor.CollectDetail(fl.handle); cerr == nil {
			r.preserveCollectedWork(marker, collected)
		}
	}
	return nil, false
}

// releaseUnanswered is the window's release of an attempt the factory could
// not answer for, recorded before the tick is dispatched again: a run
// release, carrying the attempt's committed work when there is any, so the
// next dispatch of the tick (claimDispatch) neither adopts it nor holds it.
// False when the release could not be recorded.
func (r *Reconciler) releaseUnanswered(refusal *Refusal) bool {
	if refusal == nil || refusal.unanswered == nil {
		return false
	}
	marker := *refusal.unanswered
	if _, err := r.store.Fetch(); err != nil {
		r.record(marker.TickID, StageRejected, "the run's release of %s could not be recorded (%v): it is held",
			r.attemptName(marker.TickID, marker.Attempt), err)
		return false
	}
	head := r.rejectedWorkHead(marker)
	carry := head != "" && !r.integrated(head)
	ref, sha := "", ""
	if carry {
		ref, sha = marker.WriteRef, head
	}
	if err := r.recordRunRelease(marker, refusal.Reason, rejectedStepUnanswered, carry, ref, sha); err != nil {
		r.record(marker.TickID, StageRejected, "the run's release of %s could not be recorded (%v): it is held",
			r.attemptName(marker.TickID, marker.Attempt), err)
		return false
	}
	return true
}
