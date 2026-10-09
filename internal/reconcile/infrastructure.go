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
// one container boot (and one retry window) per pass. A boot that stopped on
// a service is therefore dispatched again after a BACKOFF —
// infrastructureBackoff: 1, 2, 5, then 10 minutes — and only while the
// backoff one tick has waited out in this incarnation stays inside
// infrastructureWindow (an hour, the window the cloud Workflow gives its own
// orchestrator boots: cloudflare/src/run-workflow.ts). The next one is a
// run-level refusal naming the service — the fix is the factory's (deploy it,
// check its gateway and its provider), never the tick's. The bound used to be
// a COUNT of three immediate redispatches, about a quarter of an hour of
// probe windows: epic ymf's cloud run (run_91f2952a, 2026-10-09) met a
// twenty-minute Workers AI outage (HTTP 500, AiError 4007), which three quick
// redispatches would not have outlasted. While a tick waits out its backoff
// the window does not dispatch it (mayAdmit), and the ticks queued behind it
// wait with it: the same provider is failing for them too. A service that
// gave out MID-job (a claude-sub job's quota) keeps the old count of
// maxMidJobRedispatches immediate redispatches: its next dispatch steps down
// to another route rather than waiting on the same one.
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
	// through a tick's whole backoff window (or, mid-job, for
	// maxMidJobRedispatches+1 consecutive jobs of one tick). It is
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

// infrastructureBackoff is how long a tick whose job died in its boot on a
// service waits before it is dispatched again, by how many such redispatches
// came before it in this incarnation: 1, 2, 5, then 10 minutes for every one
// after. Each job already spent the container's own retry window (minutes)
// before it gave up, and the provider an outage takes down is the one every
// other worker of the run is waiting on too.
var infrastructureBackoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// infrastructureWindow is the most backoff one tick may wait out in one
// incarnation before the run stops naming the service: an hour, matching the
// cloud Workflow's window for its orchestrator's own boots.
const infrastructureWindow = time.Hour

// maxMidJobRedispatches is how many times one incarnation dispatches a tick
// again, at once, after jobs a service gave out under MID-job (a claude-sub
// job's subscription quota). The next dispatch's lease steps down to another
// route, so there is nothing to wait out.
const maxMidJobRedispatches = 3

// infrastructureDelay is the wait before the redispatch that follows `n`
// earlier ones of the same tick.
func infrastructureDelay(n int) time.Duration {
	if n >= len(infrastructureBackoff) {
		n = len(infrastructureBackoff) - 1
	}
	return infrastructureBackoff[n]
}

// maxInfrastructureRedispatches is how many boot-time infrastructure
// redispatches of one tick fit infrastructureWindow.
func maxInfrastructureRedispatches() int {
	var waited time.Duration
	n := 0
	for waited+infrastructureDelay(n) <= infrastructureWindow {
		waited += infrastructureDelay(n)
		n++
	}
	return n
}

// infrastructureBound tracks the infrastructure redispatches of each tick in
// this incarnation: how many, how much backoff they have waited out, and
// when the next may be dispatched.
type infrastructureBound struct {
	mu     sync.Mutex
	byTick map[string]*infrastructureStreak
}

type infrastructureStreak struct {
	n         int
	waited    time.Duration
	notBefore time.Time
}

// take counts one more infrastructure failure of `tick` and says whether it
// may still be redispatched, how many it has had, and how long the
// redispatch waits (zero mid-job).
func (b *infrastructureBound) take(tick string, midJob bool, now time.Time) (int, time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byTick == nil {
		b.byTick = map[string]*infrastructureStreak{}
	}
	streak := b.byTick[tick]
	if streak == nil {
		streak = &infrastructureStreak{}
		b.byTick[tick] = streak
	}
	streak.n++
	if midJob {
		return streak.n, 0, streak.n <= maxMidJobRedispatches
	}
	delay := infrastructureDelay(streak.n - 1)
	if streak.waited+delay > infrastructureWindow {
		return streak.n, 0, false
	}
	streak.waited += delay
	streak.notBefore = now.Add(delay)
	return streak.n, delay, true
}

// deferredUntil says whether `tick` is waiting out an infrastructure backoff
// at `now`, and until when.
func (b *infrastructureBound) deferredUntil(tick string, now time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	streak := b.byTick[tick]
	if streak == nil || !now.Before(streak.notBefore) {
		return time.Time{}, false
	}
	return streak.notBefore, true
}

// waited is how much backoff `tick` has waited out in this incarnation.
func (b *infrastructureBound) waited(tick string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if streak := b.byTick[tick]; streak != nil {
		return streak.waited
	}
	return 0
}

// minutes renders a backoff for a feed line.
func minutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
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
	r.disposeRejected(handle, executor, marker, infrastructureStop(name, failure))

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
	n, delay, again := r.infrastructure.take(tick, failure.MidJob, r.now())
	if failure.MidJob {
		// The service gave out DURING the job (a claude-sub job's
		// subscription quota, cloudflaresandbox): the harness ran, so "never
		// reached its harness" would be a lie in the feed. The rule is the
		// same — same tier, no rung — and so is the bound.
		if again {
			r.record(tick, StageInfrastructureRedispatched,
				"%s. That is %s, not the tick, so it is dispatched again at the same tier (%s) and spends no "+
					"rung of the ladder (%d of at most %d such redispatches). %s",
				infrastructureStop(name, failure), failure.Service, tierOrUnchanged(marker.Tier), n,
				maxMidJobRedispatches, collected.Message)
			return r.refuse(RefusedInfrastructureRedispatch, tick,
				"%s; the tick is dispatched again at the same tier", infrastructureStop(name, failure))
		}
		return r.refuse(RefusedInfrastructure, tick,
			"%s, and that is %d jobs of %s in a row. The run stops here; no rung of the ladder was spent: %s",
			infrastructureStop(name, failure), n, tick, infrastructureRemedy(failure))
	}
	if again {
		tier := marker.Tier
		if tier == "" {
			tier = "unchanged"
		}
		at := r.now().Add(delay).UTC().Format("15:04:05Z")
		r.record(tick, StageInfrastructureRedispatched,
			"%s never reached its harness: %s did not answer through the container's retry window (exit %d) — "+
				"the provider behind it is failing or unreachable. That is infrastructure, not the tick, so it is "+
				"dispatched again at the same tier (%s) in %s, at %s, and spends no rung of the ladder (%d of at most "+
				"%d such redispatches, %s of backoff in a window of %s)",
			name, failure.Service, failure.ExitCode, tier, minutes(delay), at, n, maxInfrastructureRedispatches(),
			minutes(r.infrastructure.waited(tick)), minutes(infrastructureWindow))
		return r.refuse(RefusedInfrastructureRedispatch, tick,
			"%s never reached its harness: %s did not answer (exit %d); the tick is dispatched again at the same "+
				"tier in %s, at %s", name, failure.Service, failure.ExitCode, minutes(delay), at)
	}
	return r.refuse(RefusedInfrastructure, tick,
		"%s never reached its harness: %s did not answer through the container's retry window (exit %d), and "+
			"that is %d jobs of %s in a row over %s of backoff. The run stops here rather than paying for "+
			"containers that die in their boot. Nothing about the tick was tried and no rung of its ladder was "+
			"spent: %s",
		name, failure.Service, failure.ExitCode, n, tick, minutes(r.infrastructure.waited(tick)),
		infrastructureRemedy(failure))
}

// infrastructureRecord is one recorded infrastructure failure, as the
// redispatch walk reads it back.
type infrastructureRecord struct {
	service string
	midJob  bool
}

// infrastructureResponse is the decision record's response half. `mid_job`
// is written only when true, so every record from before it reads the same.
func infrastructureResponse(failure *subprocess.InfrastructureFailure) map[string]any {
	response := map[string]any{"service": failure.Service, "exit_code": failure.ExitCode}
	if failure.MidJob {
		response["mid_job"] = true
	}
	return response
}

// infrastructureStop says what stopped a job, in the words its kind earns: a
// boot that stopped on a service, or a service that gave out mid-job.
func infrastructureStop(name string, failure *subprocess.InfrastructureFailure) string {
	if failure.MidJob {
		return fmt.Sprintf("%s stopped mid-job: %s ran out under it (exit %d)", name, failure.Service, failure.ExitCode)
	}
	return fmt.Sprintf("%s never reached its harness: its boot stopped on %s", name, failure.Service)
}

// tierOrUnchanged names a marker's tier for a sentence.
func tierOrUnchanged(tier string) string {
	if tier == "" {
		return "unchanged"
	}
	return tier
}

// roleJobBootFault is the stop for a job the run dispatches for itself — a
// resolve-conflict job (a tick's or a base fold's) or a repair job — whose boot
// stopped on a deterministic environment fault (Persistent). Such a job's
// missing-result is otherwise an operational failure the allowance retries
// (role_allowance.go); retrying one would boot the same image on the same
// repository and stop the same way, twice more, before a stop that says only
// that the allowance ran out. It returns nil for anything else, which keeps
// its operational retry: a transient boot fault (the gateway, origin) is
// exactly what that retry is for, and a role job has no tier ladder to spend.
func (r *Reconciler) roleJobBootFault(tick, job string, collected *subprocess.Collection) *Refusal {
	if collected == nil || collected.Infrastructure == nil || !collected.Infrastructure.Persistent {
		return nil
	}
	failure := collected.Infrastructure
	return r.refuse(RefusedWorkerBootFault, tick,
		"%s never reached its harness: its boot stopped on %s (exit %d). Every job of this run boots the same "+
			"image on the same repository and would stop the same way, so it is not dispatched again and the run "+
			"stops here. What the boot said: %s. To fix: %s",
		job, failure.Service, failure.ExitCode, collected.Message, infrastructureRemedy(failure))
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
		Response:    infrastructureResponse(failure),
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
func (r *Reconciler) infrastructureFailures(tick string) map[int]infrastructureRecord {
	out := map[int]infrastructureRecord{}
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
		midJob, _ := d.Response["mid_job"].(bool)
		if attempt := decisionAttemptOf(d); attempt >= 1 {
			out[attempt] = infrastructureRecord{service: service, midJob: midJob}
		}
	}
	return out
}
