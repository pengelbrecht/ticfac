package reconcile

import (
	"fmt"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A job whose container died in its BOOT on a service outside it is not a
// failed attempt at its tick (epic hn6, run_37b36bfe).
//
// WHAT WAS WRONG. At ~04:49Z the factory's Worker was being redeployed. 0rx's
// worker container booted, probed the model gateway with one 30s request, got
// no answer and exited. Its landing branch was empty, so the collect read
// "missing-result … carries no report", the run rejected the try, and the
// redispatch walk counted it as a failed attempt — earning the tier ladder its
// rung, which took 0rx two rungs up to the ceiling. Nothing about the tick had
// been tried: the harness never started.
//
// THE RULE NOW. The container retries the gateway's probe and origin's fetch
// over a bounded window and, when the service still does not answer, exits
// with its own code (sandboximage.ExitGatewayUnavailable,
// ExitOriginUnavailable). The executor carries that as a typed fact beside the
// missing-result verdict (subprocess.Collection.Infrastructure), and the run:
//
//   - records the attempt as an infrastructure failure (a decision record, so
//     a resume reads it back the same way),
//   - rejects it durably and tears it down as any rejection,
//   - and dispatches the tick again IN-RUN at the same tier: the redispatch
//     walk does not count the attempt toward the ladder, because the ladder's
//     "failed" means a worker had its chance and did not pass.
//
// THE BOUND. A service that is down for good would otherwise loop forever at
// one container boot (and one retry window) per pass, so at most
// maxInfrastructureRedispatches such redispatches of one tick are made per
// incarnation. The next one is a run-level refusal naming the service — the
// fix is the factory's (deploy it, check its gateway), never the tick's.
//
// THE OTHER BOOT FAULTS. Every other stop before the harness — the inputs the
// factory sent (2), the image's tk (4), the repository's pre-flight (5) or
// setup (6), a model route the gateway refused (7), a harness that cannot use
// the route (8) — failed the same way: missing-result, a rung spent. But they
// are DETERMINISTIC environment faults: a retry as-is reaches the same answer,
// and a higher tier boots the same image on the same repository. They are
// collected as PERSISTENT: no rung, no redispatch, and the run stops at once
// on RefusedWorkerBootFault, naming the cause, the boot's own reason (the
// worker's boot-stopped marker, #176) and what to fix.

const (
	// RefusedInfrastructureRedispatch is a collect whose job died in its boot
	// on a service outside it: the window requeues the tick in-run at the same
	// tier. Should it escape the window, the next incarnation reads the
	// recorded infrastructure decision and redispatches the same way.
	RefusedInfrastructureRedispatch = "infrastructure_redispatched"

	// RefusedInfrastructure is that bound spent: the service did not answer
	// for maxInfrastructureRedispatches+1 consecutive jobs of one tick. It is
	// a stop, not a hold: nothing about the tick waits on a person, the
	// factory does, and running the epic again once it answers is the repair.
	RefusedInfrastructure = "worker_infrastructure_unavailable"

	// RefusedWorkerBootFault is a job whose boot stopped on a deterministic
	// environment fault (subprocess.InfrastructureFailure.Persistent): every
	// container of the run would stop the same way at every tier, so the run
	// stops at once — no redispatch, no rung — naming the cause and its fix.
	// Not resumable: nothing changes between incarnations until a person (or
	// a deploy) fixes the environment.
	RefusedWorkerBootFault = "worker_boot_fault"

	// StageInfrastructureRedispatched: a job never reached its harness
	// because a service outside it did not answer; it is dispatched again at
	// the same tier.
	StageInfrastructureRedispatched = "infrastructure_redispatched"

	// infrastructureKind is the decision record of one such job.
	infrastructureKind = "infrastructure_failure"
)

// maxInfrastructureRedispatches is how many times one incarnation dispatches a
// tick again after jobs that died in their boot on infrastructure. Each one
// already spent the container's own retry window (minutes), so three is a
// quarter of an hour of a service not answering before the run says so.
const maxInfrastructureRedispatches = 3

// infrastructureBound counts the infrastructure redispatches of each tick in
// this incarnation.
type infrastructureBound struct {
	mu     sync.Mutex
	byTick map[string]int
}

// take counts one more infrastructure failure of `tick` and says whether it
// may still be redispatched, and how many it has had.
func (b *infrastructureBound) take(tick string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byTick == nil {
		b.byTick = map[string]int{}
	}
	b.byTick[tick]++
	n := b.byTick[tick]
	return n, n <= maxInfrastructureRedispatches
}

// answerInfrastructure is the collect's answer to a job that never reached its
// harness because a service outside it did not answer. It returns nil when the
// infrastructure decision could not be recorded: the collect then goes the
// ordinary way, which is the conservative answer (a rung spent), never a
// redispatch nothing on origin explains.
func (r *Reconciler) answerInfrastructure(marker attemptHandle, handle *subprocess.JobHandle, executor Executor,
	collected *subprocess.Collection) error {

	failure := collected.Infrastructure
	tick := marker.TickID
	name := r.attemptName(tick, marker.Attempt)
	if err := r.recordInfrastructure(marker, failure); err != nil {
		r.record(tick, StageRejected, "%s never reached its harness (its boot stopped on %s), and that could not be "+
			"recorded (%v): it is collected as the failed attempt it reads as", name, failure.Service, err)
		return nil
	}
	if err := r.rejectDurably(marker, collected.Verdict, collected.Message); err != nil {
		return err
	}
	r.disposeRejected(handle, executor, marker, fmt.Sprintf("%s never reached its harness: its boot stopped on %s",
		name, failure.Service))

	if failure.Persistent {
		// The environment itself is wrong, so every container of the run
		// would stop the same way at every tier: no redispatch, and no rung
		// spent.
		return r.refuse(RefusedWorkerBootFault, tick,
			"%s never reached its harness: its boot stopped on %s (exit %d). Every worker of this run boots the "+
				"same image on the same repository and would stop the same way at any tier, so the run stops here; "+
				"nothing about the tick was tried and no rung of its ladder was spent. What the boot said: %s. To "+
				"fix: %s",
			name, failure.Service, failure.ExitCode, collected.Message, infrastructureRemedy(failure))
	}
	n, again := r.infrastructure.take(tick)
	if again {
		tier := marker.Tier
		if tier == "" {
			tier = "unchanged"
		}
		r.record(tick, StageInfrastructureRedispatched,
			"%s never reached its harness: %s did not answer through the container's retry window (exit %d). "+
				"That is infrastructure, not the tick, so it is dispatched again at the same tier (%s) and spends "+
				"no rung of the ladder (%d of at most %d such redispatches)",
			name, failure.Service, failure.ExitCode, tier, n, maxInfrastructureRedispatches)
		return r.refuse(RefusedInfrastructureRedispatch, tick,
			"%s never reached its harness: %s did not answer (exit %d); the tick is dispatched again at the same tier",
			name, failure.Service, failure.ExitCode)
	}
	return r.refuse(RefusedInfrastructure, tick,
		"%s never reached its harness: %s did not answer through the container's retry window (exit %d), and "+
			"that is %d jobs of %s in a row. The run stops here rather than paying for containers that die in "+
			"their boot. Nothing about the tick was tried and no rung of its ladder was spent: %s",
		name, failure.Service, failure.ExitCode, n, tick, infrastructureRemedy(failure))
}

// infrastructureRemedy is where a person looks: the fix the executor named,
// or a general one for a service that stays down.
func infrastructureRemedy(failure *subprocess.InfrastructureFailure) string {
	if failure.Fix != "" {
		return failure.Fix
	}
	return "check that " + failure.Service + " is reachable from the factory's containers, then run the epic again"
}

// recordInfrastructure lands one infrastructure failure as a decision record.
// Create-if-absent per job.
func (r *Reconciler) recordInfrastructure(marker attemptHandle, failure *subprocess.InfrastructureFailure) error {
	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if kind, _ := existing.Request["kind"].(string); kind == infrastructureKind &&
			existing.Request["tick_id"] == marker.TickID && existing.Request["job_id"] == marker.JobID {
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return err
	}
	stamp := r.now().UTC().Format(time.RFC3339)
	_, err = r.store.PutDecision(runstate.Decision{
		Decision: number,
		// The implement role whatever the job was, as a blocked answer's:
		// a role job's own decisions are what its resume closes behind, and
		// this record is not anybody's answer.
		Role: "implement-tick",
		Request: map[string]any{
			"kind": infrastructureKind, "tick_id": marker.TickID, "attempt": marker.Attempt,
			"job_id": marker.JobID, "tier": marker.Tier,
		},
		Response:    map[string]any{"service": failure.Service, "exit_code": failure.ExitCode},
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return fmt.Errorf("record the infrastructure failure of %s: %w", r.attemptName(marker.TickID, marker.Attempt), err)
	}
	return nil
}

// infrastructureFailures is every attempt of `tick` the run recorded as an
// infrastructure failure, by attempt number, naming the service. A record that
// cannot be read answers nothing: the walk then counts the attempt toward the
// ladder, the conservative reading.
func (r *Reconciler) infrastructureFailures(tick string) map[int]string {
	out := map[int]string{}
	if r.store == nil {
		return out
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return out
	}
	for _, d := range decisions {
		if kind, _ := d.Request["kind"].(string); kind != infrastructureKind {
			continue
		}
		if id, _ := d.Request["tick_id"].(string); id != tick {
			continue
		}
		service, _ := d.Response["service"].(string)
		if attempt := decisionAttemptOf(d); attempt >= 1 {
			out[attempt] = service
		}
	}
	return out
}
