package reconcile

import (
	"errors"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
)

// Substrate capacity: how many jobs can be live at once, which is not the
// epic's width (hn6's cloud run run_8511bc66…, 2026-09-30).
//
// THE OBSERVATION. The run's window was as wide as max_parallel, and in the
// cloud every live job is a container on an account whose ceiling is
// FACTORY_MAX_INSTANCES = 3 — the orchestrator's own container among them.
// The orchestrator plus two workers filled the account; the third worker's
// start waited inside the factory's door for a slot that could not free,
// until the door client's timeout, and the supervisor read the timeout as
// remote_transient. Twelve continuations later the cap was spent and the run
// failed, with its two workers still up holding the slots.
//
// Two rules close it, one per side of the door:
//
//   - The window does not admit a NEW job past the substrate's capacity
//     (mayAdmit, via roomForAJob). No door call is made for a slot the run
//     can count itself out of: the wave simply runs narrower, and the next
//     tick is admitted when a live one settles. The capacity is the
//     executor's statement (KnownExecutor.MaxLiveJobs), never a number this
//     package invents: a local run states none and is exactly as it was.
//   - A start the substrate answers with "no capacity" — room the run could
//     not count, held by another project's run or by a container nothing has
//     reclaimed yet — is a WAIT (startWithRoom). The door answers it at once
//     instead of blocking; the run records it, rests, and asks again. It is
//     never remote_transient and never spends the continuation cap: a full
//     account is a fact about the world, and the hourly reclaim frees what a
//     dead run left behind.

// capacityAnswer is how an executor says a start found no room on its
// substrate: typed, so the reconciler recognises it without importing the
// executor that said it (the cloud executor's no_capacity door refusal
// implements it) and without matching on prose.
type capacityAnswer interface{ NoCapacity() bool }

// isNoCapacity reports whether err is a start the substrate refused for want
// of room — retry later, not a verdict on anything.
func isNoCapacity(err error) bool {
	var answer capacityAnswer
	return err != nil && errors.As(err, &answer) && answer.NoCapacity()
}

// RefusedNoCapacity is the stop a start that found no room for longer than
// CapacityWaitBound becomes. Resumable by construction and waiting on the
// world, and it never counts against the continuation cap (supervise.go).
const RefusedNoCapacity = "no_capacity"

// StageWaitingForCapacity is the feed line a start that found no room leaves,
// once per wait: what is being waited for, and that it is a wait.
const StageWaitingForCapacity = "waiting_for_capacity"

// CapacityRetry is how long a start that found no room rests before it asks
// again, and CapacityWaitBound how long it keeps asking before the run stops
// over it (resumably, uncapped). The bound is longer than the factory's
// hourly reclaim, so a slot a dead run left is freed inside one wait.
const (
	CapacityRetry     = 30 * time.Second
	CapacityWaitBound = 90 * time.Minute
)

// jobSlotsFor is the run's capacity for live jobs: the smallest MaxLiveJobs
// any executor its profiles route to states, or 0 when none states one.
func jobSlotsFor(executors []KnownExecutor, profiles map[string]*profile.Profile,
	tiers map[string]map[string]*profile.Profile) int {
	slots := 0
	consider := func(p *profile.Profile) {
		if p == nil {
			return
		}
		known, ok := honoured(executors, p.Executor)
		if !ok || known.MaxLiveJobs <= 0 {
			return
		}
		if slots == 0 || known.MaxLiveJobs < slots {
			slots = known.MaxLiveJobs
		}
	}
	for _, p := range profiles {
		consider(p)
	}
	for _, perRole := range tiers {
		for _, p := range perRole {
			consider(p)
		}
	}
	return slots
}

// roomForAJob says whether the substrate has room for one more live job
// beside the ones the window is holding live. Only a live attempt holds a
// slot: a settled one's container is reclaimed at its first terminal
// observation (the factory's door), so it is capacity no longer held.
func (r *Reconciler) roomForAJob(window *held) bool {
	return r.jobSlots <= 0 || len(window.live) < r.jobSlots
}

// startWithRoom starts a job, waiting through the substrate's "no capacity"
// answers: each one rests CapacityRetry and asks again, and the first is
// recorded so the wait is visible. A start still refused for want of room
// after CapacityWaitBound is the typed RefusedNoCapacity stop, never an
// operational error the supervisor would read as a transient remote.
//
// It is also the ONE place every job is started, so it is where a job bound
// for an executor that checks out from origin has its start commit published
// first (start_publish.go) — whatever the job's kind.
func (r *Reconciler) startWithRoom(executor Executor, tick string, spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if err := r.publishStart(executor, tick, spec); err != nil {
		return nil, err
	}
	var waited time.Duration
	for {
		handle, err := executor.Start(spec)
		if !isNoCapacity(err) {
			return handle, err
		}
		if waited == 0 {
			r.record(tick, StageWaitingForCapacity,
				"%s waits for a free slot: %s. This is a wait, not a failure — nothing was started, the run "+
					"asks again every %s, and a slot frees when a live job settles or the factory reclaims a "+
					"container a finished run left", spec.JobID, firstLine(err.Error()), CapacityRetry)
		}
		if waited >= CapacityWaitBound {
			return nil, r.refuse(RefusedNoCapacity, tick,
				"%s found no free slot on its substrate for %s (%v). The run stops RESUMABLY: nothing was "+
					"started, the attempt's marker is on the remote, and the next incarnation starts it the "+
					"moment there is room. Look at what holds the account's containers (`ticfac factory status`)",
				spec.JobID, waited, err)
		}
		r.sleep(CapacityRetry)
		waited += CapacityRetry
		r.lookAtLiveJobs()
	}
}

// lookAtLiveJobs asks once after each of the window's live jobs while a start
// waits for room. The answers are not acted on here — the window's own poll
// does that — but the asking is what gives a slot back: the cloud door
// records a finished worker and reclaims its container at its first terminal
// observation, and a wait that asked nothing would hold the slots of workers
// that had already finished, waiting on itself.
func (r *Reconciler) lookAtLiveJobs() {
	if r.window == nil {
		return
	}
	for _, fl := range r.window.live {
		if fl.executor == nil || fl.handle == nil {
			continue
		}
		_, _ = fl.executor.Inspect(fl.handle, "")
	}
}

// capacityStop reports whether err is the RefusedNoCapacity stop, so a start
// site with its own failure vocabulary passes it on as what it is.
func capacityStop(err error) bool {
	var refusal *Refusal
	return asRefusal(err, &refusal) && refusal.Reason == RefusedNoCapacity
}
