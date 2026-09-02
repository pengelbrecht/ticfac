package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Review and closeout: role jobs, on the SAME executor as every implementation
// tick, and different in exactly one way — what the reconciler acts on.
//
// An implementation tick's verdict is its branch and the integrated gate over
// it. A role job's verdict is its ANSWER: the role-result envelope of
// contracts/job-protocol.json, validated against the contract the JobSpec asked
// for before anything is decided on it. So the order here is the same order the
// gate keeps, with validation where the gate would be:
//
//	dispatch  ->  collect  ->  VALIDATE  ->  record the decision  ->  close
//
// A malformed envelope FAILS CLOSED. The process tick stays open, nothing is
// noted on it, and the run stops — because a tick closed behind an answer
// nobody could parse is a close nothing stands behind, and an unvalidated model
// response acted on as authority is how a hallucinated verdict closes an epic.

// processRoleJob takes one review or closeout tick from the graph to closed.
func (r *Reconciler) processRoleJob(ctx context.Context, entry planEntry) error {
	handle, executor, marker, err := r.claimDispatch(ctx, entry)
	if err != nil {
		return err
	}

	status, err := r.waitForSettlement(ctx, handle, executor, marker)
	if err != nil {
		return err
	}

	collected, answer, err := r.collectRole(entry, handle, executor, marker, status)
	if err != nil {
		return err
	}

	// The VALIDATED answer lands before anything is decided on it, so that a
	// restart re-reads what the model said instead of paying for it twice.
	if err := r.recordDecision(entry, marker, answer); err != nil {
		return err
	}

	// A closeout writes — a retro, and the learnings it compacts — so work it
	// actually produced is integrated and gated like any other change before
	// its tick closes. A review is dispatched read-only and has none.
	if sourceGradeFor(entry.Role) == "write" && collected.Result.Source.Commits > 0 {
		merged, err := r.integrate(marker, collected)
		if err != nil {
			return err
		}
		if err := r.gateAndClose(ctx, entry, marker, collected, merged); err != nil {
			return err
		}
	} else if err := r.closeRoleTick(ctx, marker, answer); err != nil {
		return err
	}

	r.cleanUp(handle, executor, marker)
	return nil
}

// collectRole collects a role job and holds its answer to the contract.
//
// It does not apply the collect vocabulary's merge verdict: `no-commits` is
// what a correct read-only review looks like, and refusing it would refuse
// every review this phase runs. What it does keep is A10's boundary — a job
// that wrote under an authority that is not its own is refused whatever it
// answered — and then the envelope itself.
func (r *Reconciler) collectRole(entry planEntry, handle *subprocess.JobHandle, executor Executor,
	marker attemptHandle, status *subprocess.JobStatus) (*subprocess.Collection, *subprocess.RoleResult, error) {

	tick := marker.TickID
	if _, err := r.checkpoint(runstate.StateCollecting,
		fmt.Sprintf("collecting the %s job for %s", entry.Role, tick)); err != nil {
		return nil, nil, err
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, nil, fmt.Errorf("collect %s: %w", tick, err)
	}
	r.setTick(tick, "reported")
	r.record(tick, StageCollected, "%s answered %s (%s)", entry.Role, collected.Result.Outcome, collected.Verdict)

	if len(collected.BoundaryViolations) > 0 && r.guarded(guardSubstrateEnforcesBoundary) {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "boundary violation: %s", strings.Join(collected.BoundaryViolations, ", "))
		return nil, nil, r.refuse(RefusedBoundary, tick,
			"the %s job for %s wrote under an authority that is not its own (%s): %s",
			entry.Role, tick, strings.Join(collected.BoundaryViolations, ", "), collected.Message)
	}

	answer := collected.Result.RoleResult
	if err := ValidateRoleResult(answer, outputSchemaFor(entry.Role), entry.Role); err != nil {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "the role-result envelope did not validate: %v", err)
		return nil, nil, r.refuse(RefusedRoleResult, tick,
			"the %s job for %s did not return a role-result this reconciler can act on: %v. The tick is NOT closed: "+
				"acting on an answer nobody could validate is how an unchecked model response becomes a verdict",
			entry.Role, tick, err)
	}

	// The envelope validated, and it says a person is needed. For a role job
	// that IS the verdict — its only deliverable is the answer — so the tick
	// stays open for the person it asked for.
	if answer.Status == subprocess.StatusBlocked || answer.Status == subprocess.StatusNeedsContext {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "%s answered %s", entry.Role, answer.Status)
		return nil, nil, r.refuse(RefusedRoleAnswer, tick,
			"the %s job for %s answered %s: %s. The tick stays open, because a role job's answer IS its verdict and "+
				"this one asks for a person", entry.Role, tick, answer.Status, answer.Summary)
	}

	_ = status
	return collected, answer, nil
}

// recordDecision lands the validated answer in the run state, as
// contracts/ticfac-run-state.json's decision record: the request that was made,
// the response that came back, and the fact that it was validated BEFORE
// anything acted on it. A validated decision is a thing a model was paid for
// once, so a restart re-reads it rather than re-asking.
func (r *Reconciler) recordDecision(entry planEntry, marker attemptHandle, answer *subprocess.RoleResult) error {
	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if existing.Role == entry.Role && existing.Request["tick_id"] == marker.TickID {
			// Already recorded, by an earlier incarnation of this run. A
			// decision is created if absent and never rewritten.
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}

	response, err := asRecordMap(answer)
	if err != nil {
		return fmt.Errorf("record the %s decision for %s: %w", entry.Role, marker.TickID, err)
	}
	dispatch := r.dispatchFor(marker)
	profile := dispatch.Profile
	request := map[string]any{
		"tick_id":       marker.TickID,
		"epic_id":       r.opts.EpicID,
		"job_id":        marker.JobID,
		"role":          entry.Role,
		"output_schema": outputSchemaFor(entry.Role),
		"source_sha":    marker.BaseSHA,
		"source_grade":  sourceGradeFor(entry.Role),
	}
	if profile != nil {
		// The profile is named as well as digested: a person reading the record
		// months later should not have to resolve a digest to know what ran.
		request["profile"] = profile.String()
		request["profile_digest"] = profile.Digest
	}

	stamp := r.now().UTC().Format(time.RFC3339)
	outcome, err := r.store.PutDecision(runstate.Decision{
		Decision:    number,
		Role:        entry.Role,
		Request:     request,
		Response:    response,
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return fmt.Errorf("record the %s decision for %s: %w", entry.Role, marker.TickID, err)
	}
	if !outcome.EffectPermitted() {
		// Somebody else recorded it between the read and the write. Theirs is
		// the record: a decision is never overwritten.
		return nil
	}
	return nil
}

// closeRoleTick closes a role tick behind its validated answer.
//
// There is no integrated gate here because there is nothing integrated to gate:
// the job produced no commit, and the thing that stands behind this close is
// the validated envelope recorded as a decision immediately before it.
func (r *Reconciler) closeRoleTick(ctx context.Context, marker attemptHandle, answer *subprocess.RoleResult) error {
	tick := marker.TickID
	if _, err := r.checkpoint(runstate.StatePublishing,
		fmt.Sprintf("closing %s behind its validated %s answer", tick, answer.Role)); err != nil {
		return err
	}

	current, err := r.opts.Tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s before closing it: %w", tick, err)
	}
	if current.Status != "closed" {
		note := fmt.Sprintf("ticfac run %s: the %s job (attempt %d) returned a validated %s envelope at %s — %s: %s",
			r.runID, answer.Role, marker.Attempt, answer.SchemaID, short(marker.BaseSHA), answer.Status, answer.Summary)
		if _, err := r.opts.Tracker.Note(ctx, tick, note); err != nil {
			return fmt.Errorf("note the %s answer on %s: %w", answer.Role, tick, err)
		}
		if _, err := r.opts.Tracker.Close(ctx, tick); err != nil {
			return fmt.Errorf("close %s: %w", tick, err)
		}
	}

	r.setTick(tick, "closed")
	r.record(tick, StageClosed, "closed behind a validated %s answer (%s)", answer.Role, answer.Status)
	if _, err := r.checkpoint(runstate.StateRunning, fmt.Sprintf("%s is closed", tick)); err != nil {
		return err
	}
	return nil
}

// asRecordMap turns a protocol record into the open map a run-state record
// carries it in. It round-trips through the record's own JSON so that what is
// stored is what the contract says the record is, field for field.
func asRecordMap(record any) (map[string]any, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
