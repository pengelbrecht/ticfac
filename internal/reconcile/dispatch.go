package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// One tick, from the graph to the close, with the compare-and-swap that proves
// each effect has not already happened in front of it.

// attemptHandle is what the dispatch marker carries. It is the reconciler's
// half of the identity: which job, which attempt, and (in StateRoot) the
// directory this run gave that dispatch so a RESTART ON A FRESH CLONE can find
// the attempt the previous reconciler started without guessing at an
// executor's naming.
//
// StateRoot and Repo are both HOST paths — StateRoot under this run's
// ExecStateRoot, Repo the reconciler's own checkout — and neither rides into
// the durable marker asMap() writes to origin (SPEC's public-repo guard: no
// host-absolute path in a committed record). Neither needs to: StateRoot is a
// pure function of durable facts already on the marker (ExecStateRoot, the run
// id, the tick id, the attempt number), so execStateDir recomputes it
// identically on every restart, and Repo is host configuration a restarted
// reconciler is given again through Options — dispatchFor falls back to
// r.opts.Repo whenever the marker's own Repo is empty. handleFromMap leaves
// both empty; a caller that is about to adopt fills StateRoot in with
// execStateDir first, and Repo resolves itself through that fallback.
//
// The executor's own handle is not written here, and could not be: the marker
// is created BEFORE the dispatch, because a marker written afterwards guards
// nothing.
type attemptHandle struct {
	Executor  string `json:"executor"`
	JobID     string `json:"job_id"`
	Attempt   int    `json:"attempt"`
	TickID    string `json:"tick_id"`
	Role      string `json:"role"`
	Repo      string `json:"-"`
	Remote    string `json:"remote"`
	WriteRef  string `json:"write_ref"`
	BaseSHA   string `json:"base_sha"`
	StateRoot string `json:"-"`

	// Model and PromptDigest are the two halves of the profile that reach the
	// runner PROCESS — the model through the runner's own model flag, the
	// prompt as the role instruction the worker prompt opens with. They are
	// recorded here, in the marker's open handle object, because the closed
	// provenance record can say WHICH profile a job was dispatched under and
	// has nowhere to say that its prompt and model were actually applied.
	Model        string `json:"model"`
	PromptDigest string `json:"prompt_digest"`
}

// asMap is the durable form of the marker: everything BUT StateRoot and Repo,
// both host paths recovered on the read side (see attemptHandle) rather than
// ever committed.
func (a attemptHandle) asMap() map[string]any {
	return map[string]any{
		"executor": a.Executor, "job_id": a.JobID, "attempt": a.Attempt, "tick_id": a.TickID,
		"role": a.Role, "remote": a.Remote, "write_ref": a.WriteRef,
		"base_sha": a.BaseSHA,
		"model":    a.Model, "prompt_digest": a.PromptDigest,
	}
}

// handleFromMap reads a marker's durable fields back. StateRoot and Repo are
// not among them (see attemptHandle): a caller that is about to adopt this
// marker sets StateRoot from execStateDir, and Repo resolves itself through
// dispatchFor's fallback to r.opts.Repo.
func handleFromMap(raw map[string]any) attemptHandle {
	get := func(key string) string {
		if value, ok := raw[key].(string); ok {
			return value
		}
		return ""
	}
	attempt := 0
	switch value := raw["attempt"].(type) {
	case float64:
		attempt = int(value)
	case int:
		attempt = value
	}
	return attemptHandle{
		Executor: get("executor"), JobID: get("job_id"), Attempt: attempt, TickID: get("tick_id"),
		Role: get("role"), Remote: get("remote"), WriteRef: get("write_ref"),
		BaseSHA: get("base_sha"),
		Model:   get("model"), PromptDigest: get("prompt_digest"),
	}
}

// execStateDir is the directory one dispatch's executor state lives under: a
// pure function of the run's own ExecStateRoot plus the run id, tick id and
// attempt number, all of which are already durable facts elsewhere. Because it
// is deterministic, it is recomputed rather than carried in a durable record —
// which is what keeps a host-specific path out of every record this run pushes
// to origin.
func (r *Reconciler) execStateDir(tickID string, attempt int) string {
	return filepath.Join(r.opts.ExecStateRoot, r.runID, tickID, fmt.Sprintf("%d", attempt))
}

// processTick takes one tick from wherever it already is to closed.
func (r *Reconciler) processTick(ctx context.Context, entry planEntry) error {
	tick := entry.TickID

	// The tracker is the authority on whether a tick is closed. Reading it
	// before doing anything is the compare-and-swap for the close: a tick that
	// is already closed is an effect that already happened, and a restarted
	// run must not do it again.
	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s: %w", tick, err)
	}
	if current.Status == "closed" {
		r.setTick(tick, "closed")
		r.record(tick, StageSkipped, "already closed in the tracker: %s", current.ClosedReason)
		if _, err := r.checkpoint(runstate.StateRunning, "tick "+tick+" was already closed"); err != nil {
			return err
		}
		return nil
	}

	// Appendix A #11's read site. A struck-out unit is held until a PERSON
	// releases it, and nothing about this dispatch asks the clock.
	unit := r.opts.EpicID + "/" + tick
	if r.MayDispatch(unit) == Held {
		r.record(tick, StageHeld, "%s is struck out and only a person releases it", unit)
		return r.refuse(RefusedHeld, tick, "%s is struck out: a rolling window bounds the window, not the subject, "+
			"so this dispatch waits for a person and not for the clock", unit)
	}

	if isRoleJob(entry.Role) {
		// Review and closeout are jobs like any other, on the same executor —
		// what differs is that the reconciler acts on the ANSWER they return
		// rather than on a branch it merges.
		return r.processRoleJob(ctx, entry)
	}

	handle, executor, marker, err := r.claimDispatch(ctx, entry)
	if err != nil {
		return err
	}

	status, err := r.waitForSettlement(ctx, handle, executor, marker)
	if err != nil {
		return err
	}

	collected, err := r.collect(handle, executor, marker, status)
	if err != nil {
		return err
	}

	merged, err := r.integrate(marker, collected)
	if err != nil {
		return err
	}

	if err := r.gateAndClose(ctx, entry, marker, collected, merged); err != nil {
		return err
	}

	// Cleanup is LAST, and only after the close. A cleanup before the close
	// throws away the only copy of what was closed.
	r.cleanUp(handle, executor, marker)
	return nil
}

// claimDispatch is the dispatch, and the compare-and-swap in front of it.
//
// The order is the contract's: create the marker on origin, and only then
// claim the tick and start the job. A refused create means another reconciler
// already dispatched this attempt — so this one adopts it and does not start
// anything.
func (r *Reconciler) claimDispatch(ctx context.Context, entry planEntry) (*subprocess.JobHandle, Executor, attemptHandle, error) {
	tick := entry.TickID

	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, attemptHandle{}, err
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return nil, nil, attemptHandle{}, err
	}
	// The attempts a PERSON released (settle.go). Read here rather than once
	// at the start of the run: a settlement made while the run is stopped at
	// an earlier tick is one this dispatch must already see.
	released, err := r.settlements()
	if err != nil {
		return nil, nil, attemptHandle{}, err
	}
	for _, existing := range attempts {
		if existing.TickID != tick {
			continue
		}
		if was, ok := released[attemptKey(existing.TickID, existing.Attempt)]; ok {
			// A person settled it. It is not adopted — nobody could address it,
			// which is why they were asked — and whatever it left on its own
			// write ref stays there: a new attempt gets a ref of its own.
			r.record(tick, StageSettled,
				"attempt %d was released by %s at %s; a new attempt is dispatched rather than the released one adopted",
				existing.Attempt, was.by, was.at)
			continue
		}
		if !r.guarded(guardNeverRedispatchLive) {
			// The guard is off: fall through and dispatch over whatever the
			// previous incarnation started, which is the bug the guard exists
			// for — one tick, two jobs, and the run pays for both.
			break
		}
		if r.spent(existing) {
			// SETTLED, and it produced nothing. Adopting it would re-collect
			// the same refusal for as long as the run is restarted, so this is
			// a new ATTEMPT — a new number, a new marker, and a base that is
			// the integration branch as origin has it now, which is what makes
			// a worker that answered BLOCKED about an open blocker worth
			// dispatching again once that blocker is closed.
			r.record(tick, StageRedispatched,
				"attempt %d settled with nothing on %s and was rejected; a new attempt is dispatched rather than "+
					"the spent one adopted", existing.Attempt, branchOf(handleFromMap(existing.JobHandle).WriteRef))
			continue
		}
		// Appendix A #6: an attempt under this identity has already been
		// dispatched, so it is ADOPTED. Nothing is started, and a live one is
		// never redispatched.
		marker := handleFromMap(existing.JobHandle)
		marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
		handle, executor, err := r.adopt(ctx, marker)
		if err != nil {
			return nil, nil, marker, err
		}
		r.setAttempt(tick, existing.Attempt)
		r.record(tick, StageAdopted, "attempt %d was already dispatched; it is adopted by identity, never redispatched",
			existing.Attempt)
		return handle, executor, marker, nil
	}

	number := nextAttemptNumber(attempts)
	for conflicts := 0; conflicts < maxDispatchConflicts; conflicts++ {
		dispatch, marker := r.planDispatch(entry, number)

		r.setTick(tick, "ready")
		if _, err := r.checkpoint(runstate.StateDispatching, fmt.Sprintf("dispatching %s as attempt %d", tick, number)); err != nil {
			return nil, nil, marker, err
		}

		outcome, err := r.store.PutAttempt(runstate.Attempt{
			Attempt:      number,
			TickID:       tick,
			DispatchedAt: r.now().UTC().Format(time.RFC3339),
			JobHandle:    marker.asMap(),
			Provenance:   r.attemptProvenance(dispatch),
		})
		if err != nil {
			return nil, nil, marker, fmt.Errorf("record the dispatch of %s: %w", tick, err)
		}
		if !outcome.EffectPermitted() {
			// The loser of a dispatch race is refused by the repository, not by
			// a lock it might have lost — and it must NOT start a job.
			handle, executor, adopted, ok, err := r.adoptConflicted(ctx, tick, number, string(outcome))
			if err != nil {
				return nil, nil, adopted, err
			}
			if ok {
				return handle, executor, adopted, nil
			}
			// The number is somebody else's tick. Recompute it against origin
			// as it stands NOW rather than adopting another tick's job.
			refreshed, err := r.store.Attempts()
			if err != nil {
				return nil, nil, marker, err
			}
			if next := nextAttemptNumber(refreshed); next > number {
				number = next
			} else {
				number++
			}
			continue
		}

		// Appendix A #7: the marker is read back from ORIGIN before anything
		// acts on it. A write that silently did not land must not look like a
		// dispatch somebody can find — and this one is what stops the next
		// incarnation from dispatching again.
		if _, err := r.store.Fetch(); err != nil {
			return nil, nil, marker, err
		}
		if _, ok, err := r.store.Attempt(number); err != nil || !ok {
			return nil, nil, marker, fmt.Errorf(
				"the dispatch marker for %s attempt %d did not land on %s: nothing is started behind a record that "+
					"does not exist (%v)", tick, number, r.opts.Remote, err)
		}

		// The effect, now that the marker proves it has not happened.
		if _, err := r.tracker.Claim(ctx, tick, r.opts.Owner); err != nil {
			return nil, nil, marker, fmt.Errorf("claim %s: %w", tick, err)
		}
		r.record(tick, StageClaimed, "claimed for %s", r.opts.Owner)

		executor, err := r.opts.NewExecutor(dispatch)
		if err != nil {
			return nil, nil, marker, fmt.Errorf("build the executor for %s: %w", tick, err)
		}
		handle, err := executor.Start(r.jobSpec(dispatch))
		if err != nil {
			return nil, nil, marker, r.startFailure(tick, err)
		}
		r.noteAlive(dispatch.JobID)
		r.setAttempt(tick, number)
		r.setTick(tick, "dispatched")
		r.record(tick, StageDispatched, "attempt %d started as %s", number, dispatch.JobID)
		if _, err := r.checkpoint(runstate.StateRunning, fmt.Sprintf("%s is running as attempt %d", tick, number)); err != nil {
			return nil, nil, marker, err
		}
		return handle, executor, marker, nil
	}
	return nil, nil, attemptHandle{}, fmt.Errorf(
		"%d dispatch numbers running were taken by other ticks of this run while dispatching %s; that is an "+
			"operational problem, not a race to spin on", maxDispatchConflicts, tick)
}

// maxDispatchConflicts bounds the recompute-on-a-taken-number loop, for
// integrate's reason: a number moving forever under a writer is an operational
// problem to report, not a conflict to spin on.
const maxDispatchConflicts = 8

// nextAttemptNumber is the number a new dispatch takes. Attempt numbers are
// RUN-wide, not per tick: the run state store keys attempts by number alone.
func nextAttemptNumber(attempts []runstate.Attempt) int {
	number := len(attempts) + 1
	for _, existing := range attempts {
		if existing.Attempt >= number {
			number = existing.Attempt + 1
		}
	}
	return number
}

// adoptConflicted resolves the marker origin already held at the number this
// reconciler chose. It reports whether that marker was adopted.
//
// The check that makes it safe is the tick id. Attempt numbers are run-wide,
// so the record that refused this create is not necessarily ABOUT this tick —
// two reconcilers dispatching different ticks of one run race for the same
// number, and the loser reading the winner's marker would adopt another tick's
// job: its worktree, its branch, its report, collected and merged under this
// tick's name. A record for a different tick is therefore not adopted at all;
// the caller recomputes the number against origin as it stands now.
func (r *Reconciler) adoptConflicted(ctx context.Context, tick string, number int, outcome string) (
	*subprocess.JobHandle, Executor, attemptHandle, bool, error) {

	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, attemptHandle{}, false, err
	}
	recorded, ok, err := r.store.Attempt(number)
	if err != nil || !ok {
		return nil, nil, attemptHandle{}, false, fmt.Errorf(
			"attempt %d of %s is on origin and unreadable: %v", number, tick, err)
	}
	if recorded.TickID != tick {
		r.record(tick, StageRedispatched,
			"attempt %d on origin is %s's, not %s's; the number is recomputed rather than another tick's job adopted",
			number, recorded.TickID, tick)
		return nil, nil, attemptHandle{}, false, nil
	}
	marker := handleFromMap(recorded.JobHandle)
	marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
	handle, executor, err := r.adopt(ctx, marker)
	if err != nil {
		return nil, nil, marker, false, err
	}
	// The checkpoint says which attempt this tick is on however the reconciler
	// got there: a run that adopted rather than dispatched still has to say 1
	// and not 0, because the next incarnation reads that number.
	r.setAttempt(tick, recorded.Attempt)
	r.record(tick, StageAdopted,
		"the dispatch marker was already on origin (%s); this reconciler adopted rather than dispatching", outcome)
	return handle, executor, marker, true, nil
}

// spent reports whether an attempt on origin is one there is nothing left to
// adopt: it was REJECTED, and it left no commit beyond the base it was cut
// from.
//
// Both halves are durable facts, which is the point — the first is in the
// checkpoint a restart reads from origin, the second is the attempt's write ref
// on origin, and neither asks the executor whether it remembers a job. The
// second is deliberately the same question the collect vocabulary's
// `no-commits` asks: the ref usually EXISTS, because the executor pushes the
// branch it was given whether or not the worker committed anything to it, so
// "there is no ref" and "the ref carries nothing" have to be one answer.
//
// A rejected attempt that DID leave commits is a different thing entirely: its
// work exists, the refusal was about the work, and redispatching it would throw
// away the only copy of what a person has to look at.
func (r *Reconciler) spent(record runstate.Attempt) bool {
	if r.tickState(record.TickID) != "rejected" {
		return false
	}
	marker := handleFromMap(record.JobHandle)
	branch := branchOf(marker.WriteRef)
	head, err := r.git.remoteHead(branch)
	if err != nil {
		// Nobody can say what the attempt left. That is not evidence it left
		// nothing, and settling it either way from a failed read would be the
		// guess Appendix A #6 forbids.
		return false
	}
	if head == "" || head == marker.BaseSHA {
		return true
	}
	if err := r.git.fetch(branch); err != nil {
		return false
	}
	// Contained in the base it was cut from: the branch moved nowhere.
	return r.git.contains(head, marker.BaseSHA)
}

// tickState is where the run believes one tick stands, as the checkpoint has
// it: seeded from origin at Run, updated by setTick.
func (r *Reconciler) tickState(tickID string) string {
	for _, ts := range r.ticks {
		if ts.TickID == tickID {
			return ts.State
		}
	}
	return ""
}

// rejectDurably records that an attempt was rejected, ON ORIGIN, before the
// refusal is returned.
//
// A rejection that lives only in this process's memory is a rejection the next
// incarnation cannot see: it reads the checkpoint, finds the attempt marker,
// adopts the dead job and re-collects the same refusal forever. The checkpoint
// is what makes "this attempt is spent" a fact somebody else can read.
func (r *Reconciler) rejectDurably(marker attemptHandle, verdict, message string) error {
	r.setTick(marker.TickID, "rejected")
	_, err := r.checkpoint(runstate.StateRunning,
		fmt.Sprintf("attempt %d of %s is rejected (%s): %s", marker.Attempt, marker.TickID, verdict, firstLine(message)))
	return err
}

// startFailure keeps the executor's typed refusals typed. "Nobody can say
// whether it is running" is not "nothing is running", and it never becomes a
// redispatch here either.
func (r *Reconciler) startFailure(tick string, err error) error {
	if refusal, ok := subprocess.AsRefusal(err); ok {
		switch refusal.Reason {
		case subprocess.RefusedUnknown, subprocess.RefusedLive:
			return r.refuse(RefusedUnaddressed, tick,
				"the executor holds attempt of %s rather than starting it: %s", tick, refusal.Message)
		}
		return r.refuse(RefusedCollect, tick, "the executor refused to start %s: %s", tick, refusal.Message)
	}
	return fmt.Errorf("start %s: %w", tick, err)
}

// planDispatch decides everything about a dispatch before the executor is
// asked for anything — including the budget, which is clamped here so the job
// is issued the number that will govern.
func (r *Reconciler) planDispatch(entry planEntry, number int) (Dispatch, attemptHandle) {
	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", r.runID, entry.TickID, number)
	stateDir := r.execStateDir(entry.TickID, number)

	// EVERY dispatch is made at the integration branch as origin has it NOW,
	// not at the base the run was cut from. A role job because that is what it
	// is about; an implementation tick because a later wave's worker has to see
	// the waves before it — their merged code, and the tracker records this
	// reconciler pushed as it closed them. A worker that branched from the
	// run's base reads `.tick/issues/<blocker>.json` as it was before the run
	// and answers BLOCKED about a tick that is closed.
	base := r.controllerBase()
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: entry.TickID, Attempt: number,
		JobID: jobID, Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: attemptWriteRef(jobID), BaseSHA: base, StateDir: stateDir,
		Profile: r.profileFor(entry.Role),
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	marker := attemptHandle{
		Executor: subprocess.ExecutorName, JobID: jobID, Attempt: number, TickID: entry.TickID,
		Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: dispatch.WriteRef, BaseSHA: base, StateRoot: stateDir,
		Model: dispatch.Profile.Model, PromptDigest: promptDigest(dispatch.Profile),
	}
	return dispatch, marker
}

// attemptWriteRef is the ref ONE attempt of one tick may write, in SPEC
// §4.3's golden shape: refs/heads/ticfac/run-<run>/tick-<tick>/attempt-<n>.
// The job id already carries that identity, so the ref is the job id under the
// namespace the source grant bounds.
//
// Every attempt of every run gets a ref of its own, and that is the whole
// point. One ref per TICK made the git identity coarser than the dispatch
// identity the rest of this package is built on (repo key, run, tick,
// attempt): a second attempt found the branch already there and was refused
// live by the executor, its pushes collided non-fast-forward, and a merge
// could not tell one attempt's commits from another's.
func attemptWriteRef(jobID string) string {
	return "refs/heads/ticfac/" + jobID
}

// attemptRefPrefix is the namespace ONE RUN's write grade may advance —
// job-protocol.json's `write_ref_prefix`, bounded per run rather than per
// installation so a credential issued for this run cannot advance another
// run's attempt refs.
func attemptRefPrefix(runID string) string {
	return "refs/heads/ticfac/run-" + runID + "/"
}

func (r *Reconciler) jobSpec(d Dispatch) *subprocess.JobSpec {
	return &subprocess.JobSpec{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         d.JobID,
		Role:          d.Role,
		Source: subprocess.Source{
			Repository: d.Repo,
			BaseSHA:    d.BaseSHA,
			WriteRef:   d.WriteRef,
		},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: d.TickID}, {Kind: "epic", ID: d.EpicID}},
		OutputSchema:   outputSchemaFor(d.Role),
		ArtifactPrefix: "runs/" + d.RunID + "/" + d.TickID + "/",
		Credentials: subprocess.Credentials{
			Model:  subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: sourceCredentialFor(d.Role, d.RunID),
		},
		// The EFFECTIVE budget, not the requested one: a job is issued the
		// number that will govern (Appendix A #12).
		//
		// What governs it in THIS phase is worth saying plainly, because a
		// number in a record reads like an enforced limit: `max_cost_usd`
		// binds a METERED credential (subprocess.Limits), and the model
		// credential a local subprocess attempt is issued is flat-rate
		// ("issued-by-host") — nothing meters what a runner spends against it.
		// So the number here is INFORMATIONAL for this executor: it travels
		// with the job, it is what every record and the operator's own
		// submission line say, and the thing that actually stops a job on this
		// host is the wall clock beside it. A metered executor is where it
		// starts binding, and the JobSpec already carries what such an
		// executor needs.
		Limits: subprocess.Limits{WallSeconds: r.opts.WallSeconds, MaxCostUSD: d.BudgetUSD},
	}
}

// sourceCredentialFor is the source half of the job's credentials. A read-only
// grade carries NO write_ref_prefix — the contract refuses one, and the reason
// is the point: read-only means the issuer hands out no push credential, so
// there is no namespace left to bound. The executor is what keeps that: it
// issues a read-only attempt no credential and launches its runner without the
// environment or the git configuration a push needs
// (internal/exec/subprocess/grade.go).
func sourceCredentialFor(role, runID string) subprocess.SourceCredential {
	if sourceGradeFor(role) == "read-only" {
		return subprocess.SourceCredential{Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "read-only"}}
	}
	return subprocess.SourceCredential{
		Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "write", WriteRefPrefix: attemptRefPrefix(runID)},
	}
}

// adopt re-addresses an attempt somebody else dispatched — including a
// previous incarnation of this reconciler, which is what a restart is.
//
// It never dispatches. If the executor has an attempt under the dispatch's
// private state root, the handle for it is reconstructed and INSPECTED; if
// there is none, nothing was ever started and the job is started now.
func (r *Reconciler) adopt(ctx context.Context, marker attemptHandle) (*subprocess.JobHandle, Executor, error) {
	executor, err := r.opts.NewExecutor(r.dispatchFor(marker))
	if err != nil {
		return nil, nil, fmt.Errorf("build the executor for %s: %w", marker.TickID, err)
	}
	// The marker and the claim are two effects, in that order, and adopting is
	// what happens when a reconciler died between them.
	r.replayClaim(ctx, marker.TickID)
	state, found := findAttemptState(marker.StateRoot)
	if !found {
		// The marker landed and the dispatch did not: the previous reconciler
		// died in the window the marker exists to make safe. Nothing is
		// running, so this one starts it.
		handle, err := executor.Start(r.jobSpec(r.dispatchFor(marker)))
		if err != nil {
			return nil, nil, r.startFailure(marker.TickID, err)
		}
		r.noteAlive(marker.JobID)
		return handle, executor, nil
	}
	handle := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         marker.JobID,
		Attempt:       marker.Attempt,
		Executor:      subprocess.ExecutorName,
		Handle:        map[string]any{"state": state},
	}
	status, err := executor.Inspect(handle, "")
	if err != nil {
		return nil, nil, fmt.Errorf("inspect the adopted attempt of %s: %w", marker.TickID, err)
	}
	if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
		return nil, nil, r.refuse(RefusedUnaddressed, marker.TickID,
			"attempt %d of %s cannot be addressed and has not settled: it is held, never redispatched",
			marker.Attempt, marker.TickID)
	}
	r.noteAlive(marker.JobID)
	return handle, executor, nil
}

// replayClaim makes the tracker say this tick is being worked, for an attempt
// that is being ADOPTED rather than dispatched.
//
// claimDispatch's order is marker, then claim, then start, because a marker
// written after the dispatch guards nothing. The cost of that order is a
// window: a reconciler that died between the create and the claim left a
// marker on origin and a tick the tracker still reads as open. Nothing else
// closes that window — adopt never dispatches, so it never reaches
// claimDispatch's claim — and a tick worked through a whole run while the
// tracker says nobody holds it is the tracker lying about its own subject.
//
// It is settled from the tracker's OWN answer rather than from a memory of
// having claimed, so it is idempotent: a tick already in progress is left
// alone, and a tracker that cannot be read is reported rather than guessed at.
// A failure here is not fatal — the attempt exists either way, and refusing a
// tick because its claim could not be replayed would strand work that is
// already running.
func (r *Reconciler) replayClaim(ctx context.Context, tick string) {
	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		r.record(tick, StageAdopted, "the adopted attempt's tick could not be read from the tracker: %v", err)
		return
	}
	if current.Status != "open" {
		return
	}
	if _, err := r.tracker.Claim(ctx, tick, r.opts.Owner); err != nil {
		r.record(tick, StageAdopted, "the adopted attempt's tick could not be claimed: %v", err)
		return
	}
	r.record(tick, StageClaimed,
		"claimed for %s while adopting: the dispatch that made the marker never reached its claim", r.opts.Owner)
}

func (r *Reconciler) dispatchFor(marker attemptHandle) Dispatch {
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
		JobID: marker.JobID, Role: marker.Role, Repo: marker.Repo, Remote: marker.Remote,
		WriteRef: marker.WriteRef, BaseSHA: marker.BaseSHA, StateDir: marker.StateRoot,
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	if dispatch.Repo == "" {
		dispatch.Repo = r.opts.Repo
	}
	if dispatch.Role == "" {
		dispatch.Role = "implement-tick"
	}
	dispatch.Profile = r.profileFor(dispatch.Role)
	return dispatch
}

// findAttemptState locates the executor's own state directory for a dispatch.
//
// The reconciler gave this dispatch a directory of its own, so the search is
// unambiguous: exactly one attempt record can be under it. What it deliberately
// does not do is compute the executor's naming — that is the executor's, and a
// reconciler that recomputed it would be a reconciler that breaks when the
// executor renames a directory.
func findAttemptState(root string) (string, bool) {
	if root == "" {
		return "", false
	}
	found := ""
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && entry.Name() == "attempt.json" {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// ------------------------------------------------------------- the wait ---

// waitForSettlement addresses the job until it settles.
//
// Two rules meet here and neither is negotiable. Appendix A #3: no step
// outlives the host's cap, so a long wait is spread across bounded legs, and
// each leg RE-DERIVES what it knows from durable facts rather than carrying
// the previous leg's memory. Appendix A #4: the interval at which a live job is
// addressed is the keepalive, and it stays well under the substrate's wipe
// threshold.
func (r *Reconciler) waitForSettlement(ctx context.Context, handle *subprocess.JobHandle, executor Executor, marker attemptHandle) (*subprocess.JobStatus, error) {
	step := r.OpenStep(r.stepCap)
	deadline := r.settlementDeadline(marker)
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, err := executor.Inspect(handle, cursor)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", marker.TickID, err)
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
		if status.Terminal {
			r.record(marker.TickID, StageWaiting, "settled as %s", status.State)
			return status, nil
		}
		if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
			return nil, r.refuse(RefusedUnaddressed, marker.TickID,
				"attempt %d of %s cannot be addressed and has not settled: nobody can say whether it is running, "+
					"which is not the same as nothing running", marker.Attempt, marker.TickID)
		}

		// The reconciler's OWN deadline. The job's wall clock is the
		// supervisor's to enforce, and a supervisor that died without settling
		// enforces nothing: `running` then rests on a pid, and a pid is a
		// number the operating system reuses — one that belongs to somebody
		// else's process now answers signal 0 (and EPERM, "alive and not ours
		// to signal", answers it too). Nothing in the poll loop notices,
		// because the wipe threshold measures the interval between polls and
		// not the age of the job, so the run would address a dead attempt
		// forever at perfect cadence.
		//
		// Past its own wall clock plus a full wipe threshold of grace for the
		// supervisor to write its terminal record, an attempt still reading
		// `running` is one nobody can say is running. That is `unaddressed`,
		// not `wiped`: the substrate did not take it away, and a person is the
		// next actor (settle.go).
		if now := r.now(); now.After(deadline) {
			return nil, r.refuse(RefusedUnaddressed, marker.TickID,
				"attempt %d of %s still reads %s %s past the wall clock of %ds it was issued: its supervisor never "+
					"settled it, and a pid that outlives its job is a number the host reuses. Nobody can say whether "+
					"it is running; release it with `ticfac settle %s %s %d --release \"<who>\"` once you have looked",
				marker.Attempt, marker.TickID, status.State, now.Sub(deadline).Round(time.Second),
				r.opts.WallSeconds, r.opts.EpicID, marker.TickID, marker.Attempt)
		}

		// The poll IS the keepalive. Its answer is about the substrate, not
		// about the job: a job that went unaddressed past the threshold is
		// gone, whatever the last status said.
		if r.Poll(marker.JobID) == Wiped {
			return nil, r.refuse(RefusedWiped, marker.TickID,
				"attempt %d of %s went unaddressed for longer than the substrate's wipe threshold of %s",
				marker.Attempt, marker.TickID, r.wipeThreshold)
		}

		if step.Spend(r.pollInterval) == ExceededCap {
			// This leg is over. The next one is a FRESH step that re-derives
			// its state from durable facts rather than continuing this one.
			if _, err := r.store.Fetch(); err != nil {
				return nil, err
			}
			if checkpoint, ok, err := r.store.Checkpoint(); err == nil && ok {
				r.sequence, r.ticks = checkpoint.Sequence, checkpoint.Ticks
			}
			step = r.OpenStep(r.stepCap)
			continue
		}
		r.sleep(r.pollInterval)
	}
}

// settlementDeadline is the moment after which an unsettled attempt is one
// nobody can say is running.
//
// It is derived from DURABLE facts, not from when this incarnation started
// waiting: the dispatch marker on origin says when the attempt was issued, so
// a restart that adopts an attempt inherits the same deadline rather than
// giving a dead job a fresh hour every time somebody restarts the run. The
// grace on top of the job's own wall clock is one wipe threshold — long enough
// for a supervisor that is merely slow to write its terminal record, and
// bounded, which is the whole point.
//
// A marker that cannot be read leaves the deadline measured from NOW. That is
// weaker and deliberately not fatal: an unbounded wait is the failure this
// exists to remove, and refusing a tick because a timestamp would not parse
// would be a worse one.
func (r *Reconciler) settlementDeadline(marker attemptHandle) time.Time {
	issued := r.now()
	if record, ok, err := r.store.Attempt(marker.Attempt); err == nil && ok && record.TickID == marker.TickID {
		if at, parseErr := time.Parse(time.RFC3339, record.DispatchedAt); parseErr == nil {
			issued = at
		}
	}
	return issued.Add(time.Duration(r.opts.WallSeconds)*time.Second + r.wipeThreshold)
}

// ---------------------------------------------------------- the collect ---

func (r *Reconciler) collect(handle *subprocess.JobHandle, executor Executor, marker attemptHandle, status *subprocess.JobStatus) (*subprocess.Collection, error) {
	if _, err := r.checkpoint(runstate.StateCollecting, fmt.Sprintf("collecting %s attempt %d", marker.TickID, marker.Attempt)); err != nil {
		return nil, err
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, fmt.Errorf("collect %s: %w", marker.TickID, err)
	}
	r.setTick(marker.TickID, "reported")
	r.record(marker.TickID, StageCollected, "verdict %s (%s)", collected.Verdict, collected.Result.Outcome)

	// Appendix A #10's premise is that compliance is not a property of the
	// model, and a boundary measured from a base the enforced party can choose
	// is not a boundary. The executor reads the base out of the attempt record
	// beside the worker's own worktree, owned by the worker's uid; the
	// reconciler's marker is on ORIGIN, written before the dispatch and never
	// in the worker's reach. So the two are compared, and a diff measured from
	// anything but the base this run dispatched is refused rather than trusted:
	// a base moved forward to the attempt's own head makes every change
	// invisible, boundary violations included.
	if collected.Result != nil && marker.BaseSHA != "" && collected.Result.Source.BaseSHA != marker.BaseSHA {
		if err := r.rejectDurably(marker, collected.Verdict, "the collected base is not the dispatched base"); err != nil {
			return nil, err
		}
		r.record(marker.TickID, StageRejected, "the collect was measured from %s, not from the dispatched base %s",
			short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
		r.disposeRejected(handle, executor, marker, "the collected base is not the base this run dispatched")
		return nil, r.refuse(RefusedBoundary, marker.TickID,
			"attempt %d of %s was collected against base %s, but this run dispatched it at %s: the diff the boundary "+
				"check read is not the diff of this attempt, so nothing it reports about it can be believed",
			marker.Attempt, marker.TickID, short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
	}

	// Appendix A #10's reporting half: a boundary that refuses silently tells
	// nobody the model tried. The executor enforced it; the reconciler says so
	// where a person reading the run will find it, and refuses the merge.
	verdict := collected.Verdict
	if len(collected.BoundaryViolations) > 0 {
		if r.guarded(guardSubstrateEnforcesBoundary) {
			r.setTick(marker.TickID, "rejected")
			r.record(marker.TickID, StageRejected, "boundary violation: %s", strings.Join(collected.BoundaryViolations, ", "))
			r.disposeRejected(handle, executor, marker, "the attempt wrote under an authority that is not its own")
			return nil, r.refuse(RefusedBoundary, marker.TickID,
				"attempt %d of %s wrote under an authority that is not its own (%s): %s",
				marker.Attempt, marker.TickID, strings.Join(collected.BoundaryViolations, ", "), collected.Message)
		}
		// The negative control: nothing is reported and nothing is refused, so
		// the attempt's tracker writes reach the integration branch unnoticed —
		// which is the bug the guard exists for.
		verdict = subprocess.VerdictReadyToMerge
	}
	if verdict != subprocess.VerdictReadyToMerge {
		if err := r.rejectDurably(marker, collected.Verdict, collected.Message); err != nil {
			return nil, err
		}
		r.record(marker.TickID, StageRejected, "%s: %s", collected.Verdict, collected.Message)
		r.disposeRejected(handle, executor, marker, "attempt "+fmt.Sprint(marker.Attempt)+" of "+marker.TickID+
			" is "+collected.Verdict)
		return nil, r.refuse(RefusedCollect, marker.TickID, "attempt %d of %s is %s: %s",
			marker.Attempt, marker.TickID, collected.Verdict, collected.Message)
	}
	_ = status
	return collected, nil
}

// --------------------------------------------------------- the clean-up ---

// cleanUp is the last thing that happens to an attempt, and it happens only
// after the close.
//
// The order inside it is Appendix A #1's: the credential dies first, and only
// then is the attempt torn down. A container torn down before its credential is
// revoked can spend on the way out.
func (r *Reconciler) cleanUp(handle *subprocess.JobHandle, executor Executor, marker attemptHandle) {
	reason := fmt.Sprintf("attempt %d of %s is merged into %s and the tick is closed",
		marker.Attempt, marker.TickID, r.branch)
	r.tearDown(handle, executor, marker, reason, false)
}

// disposeRejected is cleanUp for an attempt that will never be merged.
//
// Nothing used to dispose of a rejection at all — Dispose was reached only
// after a close — so every refused attempt left its worktree and its branch in
// the operator's checkout forever, and the next attempt of the same tick found
// them there. That is the third thing this repair is about, and it is why the
// teardown happens HERE, in the two places a collect refuses, rather than at
// the end of a run that a refusal stops before it gets there.
//
// What it must not do is take the work with it. The refusal is what a person
// reads next, and a branch is where they read it from, so a branch that
// carries commits beyond its base is KEPT and only the worktree goes. Nothing
// about "the attempt failed" makes its commits disposable.
func (r *Reconciler) disposeRejected(handle *subprocess.JobHandle, executor Executor, marker attemptHandle, reason string) {
	if handle == nil || executor == nil {
		return
	}
	r.tearDown(handle, executor, marker, reason, r.attemptCarriesWork(marker))
}

// attemptCarriesWork asks the same question spent() asks, of the LOCAL branch:
// is there a commit on it that the base it was cut from does not already have?
func (r *Reconciler) attemptCarriesWork(marker attemptHandle) bool {
	branch := branchOf(marker.WriteRef)
	head, err := r.git.resolve(branch)
	if err != nil || head == "" {
		return false
	}
	if marker.BaseSHA == "" {
		return true
	}
	return head != marker.BaseSHA && !r.git.contains(head, marker.BaseSHA)
}

// tearDown is Appendix A #1's order, and disposal's safety, in one place.
//
// The credential dies first and the attempt second: a container torn down
// before its credential is revoked can spend on the way out. Then the executor
// refuses to delete a branch whose head no remote has, and that refusal is
// HONOURED rather than argued with — the reconciler retries keeping the
// branch, so the commits stay and the worktree still goes. A teardown that
// answered "delete it anyway" would be the reconciler taking back the one
// safety the executor has against a run that thought it was finished.
func (r *Reconciler) tearDown(handle *subprocess.JobHandle, executor Executor, marker attemptHandle,
	reason string, keepBranch bool) {

	if _, err := executor.Cancel(handle); err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt's credential could not be revoked: %v", err)
		return
	}
	err := executor.Dispose(handle, subprocess.DisposeOptions{Reason: reason, KeepBranch: keepBranch})
	if err != nil && !keepBranch && isBranchUnsafe(err) {
		// The retry is a teardown of its own, so A1's order is kept for it
		// too: revoke, then tear down. Cancel is idempotent — the credential
		// is already gone and saying so again costs a record that is already
		// there — and the alternative is a dispose with no revoke in front of
		// it, which is the shape the invariant exists to refuse.
		if _, err := executor.Cancel(handle); err != nil {
			r.record(marker.TickID, StageCleanedUp, "the attempt's credential could not be revoked: %v", err)
			return
		}
		if retry := executor.Dispose(handle, subprocess.DisposeOptions{Reason: reason, KeepBranch: true}); retry != nil {
			r.record(marker.TickID, StageCleanedUp, "the attempt was not disposed: %v", retry)
			return
		}
		r.record(marker.TickID, StageCleanedUp,
			"%s; the branch is kept because it holds commits %s does not have", reason, r.opts.Remote)
		return
	}
	if err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt was not disposed: %v", err)
		return
	}
	if keepBranch {
		r.record(marker.TickID, StageCleanedUp, "%s; the worktree is gone and the branch is kept for the commits on it", reason)
		return
	}
	r.record(marker.TickID, StageCleanedUp, "%s", reason)
}

func isBranchUnsafe(err error) bool {
	refusal, ok := subprocess.AsRefusal(err)
	return ok && refusal.Reason == subprocess.RefusedBranchUnsafe
}

// DefaultExecutor is the factory a production run uses: the local subprocess
// executor, one per dispatch, pointed at a state directory this run owns.
//
// Three of the profile's four fields reach the executor here, as HOST
// configuration: the runner it launches, the model it launches it on, and the
// role prompt the worker prompt opens with. None of them is a JobSpec field —
// the protocol's records are closed, and a field invented on this side would be
// one the reconciler's own contract does not have. `runner` is the fallback an
// operator names on the command line, for a dispatch whose profile resolved
// none.
func DefaultExecutor(runner string, runnerArgv []string, pushInterval time.Duration) func(Dispatch) (Executor, error) {
	return func(d Dispatch) (Executor, error) {
		supervisor, err := supervisorArgv()
		if err != nil {
			return nil, err
		}
		// A local, not the captured fallback: a profile that routed one
		// dispatch must not become the default for the next one.
		dispatched, model, rolePrompt := runner, "", ""
		if d.Profile != nil {
			if d.Profile.Runner != "" {
				dispatched = d.Profile.Runner
			}
			model, rolePrompt = d.Profile.Model, d.Profile.Prompt
		}
		return subprocess.New(subprocess.Options{
			Repo:           d.Repo,
			StateDir:       d.StateDir,
			Runner:         dispatched,
			Model:          model,
			RolePrompt:     rolePrompt,
			RunnerArgv:     runnerArgv,
			SupervisorArgv: supervisor,
			Remote:         d.Remote,
			Attempt:        d.Attempt,
			PushInterval:   pushInterval,
		})
	}
}

// CheckExecutor reports whether this build has an executor behind the
// four-operation protocol. It is what `ticfac run-epic` asks before it does
// anything: a build that cannot start, inspect, cancel or collect a job must
// refuse rather than report a run it did not make.
func CheckExecutor() error {
	_, err := supervisorArgv()
	return err
}

// supervisorArgv finds the executor binary that supervises an attempt. The
// reconciler's own executable is not it: `ticfac supervise` is not a command,
// and defaulting to it would spawn a supervisor that exits with a usage error
// and leaves an attempt nobody is watching.
func supervisorArgv() ([]string, error) {
	const name = "ticfac-exec-subprocess"
	if self, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(self), name)
		if info, err := os.Stat(beside); err == nil && !info.IsDir() {
			return []string{beside, "supervise"}, nil
		}
	}
	found, err := osexec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s is not beside this executable or on PATH: it is what supervises an attempt, "+
			"and a run without it would start jobs nothing is watching", name)
	}
	return []string{found, "supervise"}, nil
}
