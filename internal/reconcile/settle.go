package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The operator's settlement of an attempt nobody can address.
//
// Appendix A #6 says a live attempt is never redispatched, and this package
// keeps that: an attempt whose supervisor died without writing its terminal
// record inspects as `lost`, and every restart HOLDS it rather than starting a
// second job over the same identity. What was missing is the other half of
// "an in-flight state is settled by whoever finds it next": for this one state
// there was no next actor at all. The reconciler cannot settle it — nothing
// durable says whether that pid was the job or a reused number — so the run
// refused the same tick forever, and the only way forward was a new run id,
// which orphans the run's attempt numbering and its evidence.
//
// So the next actor is a PERSON, and this is the surface they act through:
//
//	ticfac settle <epic> <tick> <attempt> --release "<who>"
//
// Three things make it a settlement rather than an override:
//
//   - it refuses a LIVE attempt. A6 is not an operator's to waive: an attempt
//     the executor can still address is cancelled, not released.
//   - the release is RECORDED, durably, on origin, as a decision naming who
//     made it — the same shape a role job's answer lands in, because "a person
//     decided this run may go on" is exactly a decision the run was made under.
//     A release nobody can attribute is the clock release A11 refuses.
//   - the reconciler READS it. The next run does not adopt a released attempt;
//     it dispatches a new one, at a new number, with a write ref of its own, so
//     whatever the dead attempt left on its branch stays there for a person.
//
// The second attempt a person has to be able to release is the one this run
// REJECTED while it was holding commits: nothing merged them, so the next run
// neither collects it (its worktree went with the teardown the refusal ran) nor
// dispatches over it (that would orphan the only copy). dispatch.go's
// holdAttemptWork refusal names this command; this is the other end of it.
//
// The credential goes first and the attempt second (Appendix A #1), for the
// reason a teardown always does: a container torn down before its credential is
// revoked can spend on the way out.

// settleOp is the request marker a settlement decision carries. It is read as
// a FIELD rather than matched in prose: a run that recovers a decision by
// matching on a sentence is the failure Appendix A #9 is about.
const settleOp = "settle_attempt"

// settleRole is the role a settlement is recorded under, from the contract's
// closed role vocabulary ($defs.role). A failure somebody looked at and ruled
// on is a triage, and it is the only name in that vocabulary this record could
// honestly take.
const settleRole = "triage-failure"

// Settlement is what a settlement did, for the operator who asked for it.
type Settlement struct {
	RunID      string
	TickID     string
	Attempt    int
	ReleasedBy string

	// State is the executor's last word about the attempt at the moment it was
	// released — what the person is settling, in the executor's vocabulary.
	State string

	// Decision is the decision number the release landed as, and Recorded is
	// false when a settlement for this attempt was already on origin.
	Decision int
	Recorded bool

	// Disposed says whether the executor's own state for the attempt was torn
	// down. A settlement stands whether or not it was: the record on origin is
	// what the next run reads.
	Disposed bool
}

// settlement is one released attempt, as the run reads it back.
type settlement struct {
	by string
	at string
}

func attemptKey(tick string, attempt int) string { return fmt.Sprintf("%s#%d", tick, attempt) }

// Settle releases one attempt this host cannot address, on a person's word.
//
// It is the same reconciler a run is made with — the same repository, remote,
// integration branch, run id and executor factory — because settling an
// attempt is an act inside a run and not a tool beside it.
func (r *Reconciler) Settle(ctx context.Context, tickID string, attempt int, by string) (*Settlement, error) {
	if by == "" {
		return nil, fmt.Errorf("reconcile: a settlement names who made it: a release with no author is the clock " +
			"release Appendix A #11 refuses (pass --release \"<who>\")")
	}
	if tickID == "" || attempt < 1 {
		return nil, fmt.Errorf("reconcile: settle names a tick and an attempt number")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// The run's base, as every record this reconciler writes states it: the
	// integration branch as origin has it. Run establishes it by ensuring the
	// branch; a settlement never creates one — a run whose branch is not there
	// has no attempt to release.
	head, err := r.git.remoteHead(r.branch)
	if err != nil {
		return nil, fmt.Errorf("reconcile: read %s from %s: %w", r.branch, r.opts.Remote, err)
	}
	if head == "" {
		return nil, fmt.Errorf("reconcile: %s has no branch %s: there is no run there to settle anything of",
			r.opts.Remote, r.branch)
	}
	r.base, r.baseRef = head, refFor(r.branch)

	store, err := runstate.Open(runstate.Options{
		Repo: r.opts.Repo, Remote: r.opts.Remote, Branch: r.branch, RunID: r.runID, Now: r.now,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	r.store = store
	if _, err := store.Fetch(); err != nil {
		return nil, fmt.Errorf("reconcile: read the run state: %w", err)
	}

	record, ok, err := store.Attempt(attempt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("reconcile: run %s has no attempt %d on %s: there is nothing under that number to "+
			"settle", r.runID, attempt, r.opts.Remote)
	}
	if record.TickID != tickID {
		return nil, fmt.Errorf("reconcile: attempt %d of run %s is %s's, not %s's: attempt numbers are run-wide, "+
			"and settling one under another tick's name would release a job nobody looked at",
			attempt, r.runID, record.TickID, tickID)
	}

	marker := handleFromMap(record.JobHandle)
	marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)

	// Already settled: a settlement is a decision, and a decision is created if
	// absent and never rewritten.
	if existing, err := r.settlements(); err != nil {
		return nil, err
	} else if was, ok := existing[attemptKey(tickID, attempt)]; ok {
		return &Settlement{RunID: r.runID, TickID: tickID, Attempt: attempt, ReleasedBy: was.by,
			State: subprocess.StateLost, Recorded: false}, nil
	}

	handle, executor, state, err := r.addressForSettlement(marker)
	if err != nil {
		return nil, err
	}

	number, err := r.recordSettlement(marker, by, state)
	if err != nil {
		return nil, err
	}

	// The record is on origin; only now is anything torn down. A1's order
	// inside the teardown: revoke, then dispose, and the branch is KEPT —
	// whatever the dead attempt committed is the only copy of it.
	disposed := false
	if handle != nil && executor != nil {
		if _, err := executor.Cancel(handle); err == nil {
			disposed = executor.Dispose(handle, subprocess.DisposeOptions{
				Reason:     fmt.Sprintf("attempt %d of %s was released by %s", attempt, tickID, by),
				KeepBranch: true,
			}) == nil
		}
	}

	return &Settlement{RunID: r.runID, TickID: tickID, Attempt: attempt, ReleasedBy: by,
		State: state, Decision: number, Recorded: true, Disposed: disposed}, nil
}

// addressForSettlement asks the executor what it can still say about the
// attempt, and refuses every answer but the one a person may release.
//
// A live attempt is not settled here (A6): it is addressable, so it is
// cancelled through the executor and not released behind its back. An attempt
// this host has no state for was never started here, so the next run starts it
// rather than holding it. That leaves two a person may release: `lost` —
// started, and nobody can say whether it is running — and an attempt that
// settled itself and THIS RUN REJECTED, whose commits no run will merge and no
// run will dispatch over until somebody says what happens to them. A settled
// attempt the run has not rejected is still refused: the next run collects it,
// and releasing it would throw away the report it left.
func (r *Reconciler) addressForSettlement(marker attemptHandle) (*subprocess.JobHandle, Executor, string, error) {
	executor, err := r.opts.NewExecutor(r.dispatchFor(marker))
	if err != nil {
		return nil, nil, "", fmt.Errorf("reconcile: build the executor for %s: %w", marker.TickID, err)
	}
	state, found := findAttemptState(marker.StateRoot)
	if !found {
		return nil, nil, "", fmt.Errorf(
			"reconcile: this host holds no state for attempt %d of %s: nothing was started here, so the next run "+
				"starts it rather than holding it — there is nothing to release",
			marker.Attempt, marker.TickID)
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
		return nil, nil, "", fmt.Errorf("reconcile: inspect attempt %d of %s: %w", marker.Attempt, marker.TickID, err)
	}
	switch {
	case status.Terminal && r.rejectedDurably(marker):
		// A settled attempt this run already REJECTED is the other thing a
		// person has to be able to release. The next run does not collect it —
		// the teardown that followed the refusal removed the worktree the
		// collect reads — and it does not dispatch over it either, because its
		// commits are the only copy of what the refusal was about. Somebody has
		// to look at them and say the run may go on, and this is where they say
		// it. Whatever the attempt left on its branch stays there: the release
		// keeps the branch, and the next run's attempt gets a ref of its own.
		//
		// Unless the work is ALREADY MERGED, which is the gate's refusal and
		// not the merge's. Everything the sentence above says about "the only
		// copy" is then false: the commits are on the integration branch, the
		// next run recognises the attempt as integrated and re-runs the gate on
		// its own, and a release would instead send it to dispatch a fresh
		// attempt from a base that already contains the work — a worker with
		// nothing to do, whose empty branch is refused, forever. The repair for
		// a failing gate is the tree, not the tracker.
		if merged, err := r.attemptIsMerged(marker); err != nil {
			return nil, nil, status.State, err
		} else if merged {
			return nil, nil, status.State, fmt.Errorf(
				"reconcile: attempt %d of %s was rejected, but its work is already merged into %s: releasing it "+
					"would dispatch a fresh attempt from a base that already carries the work, and there would be "+
					"nothing for it to do. Nothing is released. The gate is what refused, and the gate is keyed by "+
					"the commit it ran on: fix the check or the tree, push it to %s, and run the epic again under "+
					"this run id",
				marker.Attempt, marker.TickID, r.branch, r.branch)
		}
		return handle, executor, status.State, nil
	case status.Terminal:
		return nil, nil, status.State, fmt.Errorf(
			"reconcile: attempt %d of %s settled itself as %s and this run has not rejected it: the next run "+
				"collects it, and releasing a settled attempt would throw away the report it left",
			marker.Attempt, marker.TickID, status.State)
	case status.State != subprocess.StateLost:
		return nil, nil, status.State, fmt.Errorf(
			"reconcile: attempt %d of %s is %s — the executor can still address it. A live attempt is cancelled, "+
				"never released: Appendix A #6 is not an operator's to waive",
			marker.Attempt, marker.TickID, status.State)
	}
	return handle, executor, status.State, nil
}

// rejectedDurably reports whether the run's own checkpoint ON ORIGIN records
// this attempt of this tick as rejected. It is read as a FIELD of a durable
// record rather than inferred from the executor's state or from prose: an
// operator's release is permitted by what the run wrote down, not by what this
// process remembers.
func (r *Reconciler) rejectedDurably(marker attemptHandle) bool {
	checkpoint, ok, err := r.store.Checkpoint()
	if err != nil || !ok {
		return false
	}
	for _, ts := range checkpoint.Ticks {
		if ts.TickID == marker.TickID && ts.State == "rejected" && ts.Attempt == marker.Attempt {
			return true
		}
	}
	return false
}

// attemptIsMerged asks ORIGIN whether the integration branch already carries
// what this attempt produced. A read that fails is an error and never a "no":
// answering "not merged" for an unreachable remote is what would let the
// release this function guards go through on a guess.
func (r *Reconciler) attemptIsMerged(marker attemptHandle) (bool, error) {
	head, err := r.remoteWork(branchOf(marker.WriteRef), marker.BaseSHA)
	if err != nil {
		return false, fmt.Errorf("reconcile: read %s on %s to see whether attempt %d of %s is already merged: %w",
			branchOf(marker.WriteRef), r.opts.Remote, marker.Attempt, marker.TickID, err)
	}
	if head == "" {
		return false, nil
	}
	return r.integratedOn(head)
}

// recordSettlement writes the release to origin, as a decision.
func (r *Reconciler) recordSettlement(marker attemptHandle, by, state string) (int, error) {
	decisions, err := r.store.Decisions()
	if err != nil {
		return 0, err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}

	stamp := r.now().UTC().Format(time.RFC3339)
	dispatch := r.dispatchFor(marker)
	outcome, err := r.store.PutDecision(runstate.Decision{
		Decision: number,
		Role:     settleRole,
		Request: map[string]any{
			"op":      settleOp,
			"run_id":  r.runID,
			"epic_id": r.opts.EpicID,
			"tick_id": marker.TickID,
			"attempt": marker.Attempt,
			"job_id":  marker.JobID,
			"state":   state,
		},
		Response: map[string]any{
			"settled":     true,
			"released_by": by,
			"disposition": "unaddressable",
			"state":       state,
		},
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return 0, fmt.Errorf("reconcile: record the release of attempt %d of %s: %w",
			marker.Attempt, marker.TickID, err)
	}
	if !outcome.EffectPermitted() {
		return 0, fmt.Errorf("reconcile: decision %d was taken on %s while this release was being recorded: "+
			"nothing was written, and a decision is never overwritten", number, r.opts.Remote)
	}
	return number, nil
}

// settlements are the attempts a person has released, read from the run's
// decisions on origin. The fields are read as FIELDS: an operator's release is
// recovered from the record's own shape and never from its prose.
func (r *Reconciler) settlements() (map[string]settlement, error) {
	decisions, err := r.store.Decisions()
	if err != nil {
		return nil, err
	}
	out := map[string]settlement{}
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op != settleOp {
			continue
		}
		tick, _ := decision.Request["tick_id"].(string)
		attempt := 0
		switch value := decision.Request["attempt"].(type) {
		case float64:
			attempt = int(value)
		case int:
			attempt = value
		}
		if tick == "" || attempt < 1 {
			continue
		}
		by, _ := decision.Response["released_by"].(string)
		out[attemptKey(tick, attempt)] = settlement{by: by, at: decision.AnsweredAt}
	}
	return out, nil
}
