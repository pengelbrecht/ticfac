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

	// A resumed run does not collect a job it has already MERGED — a closeout
	// whose write reached the integration branch and whose gate then refused.
	// Its decision is already recorded (recordDecision is created if absent),
	// its worktree went with the teardown that followed the refusal, and what
	// is left to do is the gate. processTick's reason, in full there.
	integrated, err := r.integratedHead(marker)
	if err != nil {
		return err
	}
	if integrated != "" {
		r.record(entry.TickID, StageCollected,
			"the %s job's attempt %d is already merged into %s at %s; it is not collected a second time",
			entry.Role, marker.Attempt, r.branch, short(integrated))
		merged, err := r.integrate(marker, nil)
		if err != nil {
			r.disposeRefused(handle, executor, marker, err)
			return err
		}
		if err := r.gateAndClose(ctx, entry, marker, nil, merged); err != nil {
			r.disposeRefused(handle, executor, marker, err)
			return err
		}
		r.cleanUp(handle, executor, marker)
		return nil
	}

	collected, answer, err := r.collectRole(ctx, entry, handle, executor, marker, status)
	if err != nil {
		r.disposeRefused(handle, executor, marker, err)
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
			r.disposeRefused(handle, executor, marker, err)
			return err
		}
		if err := r.gateAndClose(ctx, entry, marker, collected, merged); err != nil {
			r.disposeRefused(handle, executor, marker, err)
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
// What it does not do is apply the merge verdict a plain tick's collect would:
// the recorded no-commits rule (tick 19l, subprocess.NoCommitsIsFailure) is
// already carried by the verdict the executor minted — a review's empty
// branch collects as ready-to-merge — so the one verdict this path still
// refuses is the one the role's own recorded rule says IS a failure. What it
// keeps regardless is A10's boundary — a job that wrote under an authority
// that is not its own is refused whatever it answered — and then the envelope
// itself.
func (r *Reconciler) collectRole(ctx context.Context, entry planEntry, handle *subprocess.JobHandle, executor Executor,
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
	// Tick 19l: what the role answered and what the run concluded are two
	// claims by two parties, stated separately — never one sentence that reads
	// as the worker declaring the run's verdict.
	r.record(tick, StageCollected, "%s", collectedLine("the "+entry.Role+" job", collected))

	// collect's own durability rule (ticfac tick 55i), for the same reason:
	// a role job's branch is kept when it carries commits, and a refusal here
	// tears the worktree the branch lived in — the commits must be on origin
	// before the answer they were is refused.
	r.preserveAttemptWork(marker)

	// collect's rule, for the same reason: a boundary measured from a base the
	// enforced party can rewrite is not a boundary, and the base this run
	// dispatched is on the marker rather than beside the worker's worktree.
	if collected.Result != nil && marker.BaseSHA != "" && collected.Result.Source.BaseSHA != marker.BaseSHA {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "the collect was measured from %s, not from the dispatched base %s",
			short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
		return nil, nil, r.refuse(RefusedBoundary, tick,
			"the %s job for %s was collected against base %s, but this run dispatched it at %s: the diff the "+
				"boundary check read is not the diff of this attempt",
			entry.Role, tick, short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
	}

	// The findings channel (tick 7vn), before the envelope is even validated:
	// a review's whole deliverable is an answer, and its findings are the
	// discoveries the answer made — the 604 shape, an upstream finding that
	// reached the tracker only because an orchestrator read that far.
	if err := r.fileFindings(ctx, marker, collected); err != nil {
		return nil, nil, err
	}

	if len(collected.BoundaryViolations) > 0 && r.guarded(guardSubstrateEnforcesBoundary) {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "boundary violation: %s", strings.Join(collected.BoundaryViolations, ", "))
		return nil, nil, r.refuse(RefusedBoundary, tick,
			"the %s job for %s wrote under an authority that is not its own (%s): %s",
			entry.Role, tick, strings.Join(collected.BoundaryViolations, ", "), collected.Message)
	}

	// The recorded no-commits rule (tick 19l), enforced. The executor's
	// classify mints `no-commits` only for a role whose recorded rule says an
	// empty branch IS a failure — a review's empty branch collects as
	// ready-to-merge, because its deliverable is the answer the validation
	// below reads — so a role job arriving here with that verdict is one whose
	// rule was decided against it and whose attempt did not meet it: the
	// close-out, whose retro and learnings are its write deliverable. The tick
	// is NOT closed behind the answer: for such a role an empty branch is an
	// undelivered deliverable, and closing over it would make the recorded
	// rule a word nobody acts on. The findings channel ran already, so what a
	// refused attempt discovered is still drafted.
	if collected.Verdict == subprocess.VerdictNoCommits {
		r.setTick(tick, "rejected")
		r.record(tick, StageRejected, "the %s job made no commits: the role answered %s, and the run's verdict is %s",
			entry.Role, roleAnswerOf(collected), collected.Verdict)
		return nil, nil, r.refuse(RefusedCollect, tick,
			"the %s job for %s answered %s, and the run's verdict is %s (%s): %s. The tick is NOT closed: for this role an "+
				"empty branch is an undelivered deliverable, whatever the answer says",
			entry.Role, tick, roleAnswerOf(collected), collected.Verdict, collected.Result.Outcome, collected.Message)
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
	if needsHuman(answer.Status) {
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
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return err
	}
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

	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s before closing it: %w", tick, err)
	}
	if current.Status != "closed" {
		// The close's other gate (tick 7vn), same rule as the integrated
		// close: a role job's findings are discoveries its answer made, and a
		// tick whose findings are untriaged is not closed — behind a validated
		// envelope or anything else.
		if refusal, err := r.gateOnFindings(tick); err != nil {
			return err
		} else if refusal != nil {
			r.setTick(tick, "rejected")
			r.record(tick, StageRejected, "%s: %s", refusal.Reason, firstLine(refusal.Message))
			return refusal
		}
		note := fmt.Sprintf("ticfac run %s: the %s job (attempt %d) returned a validated %s envelope at %s — %s: %s",
			r.runID, answer.Role, marker.Attempt, answer.SchemaID, short(marker.BaseSHA), answer.Status, answer.Summary)
		if _, err := r.tracker.Note(ctx, tick, note); err != nil {
			return fmt.Errorf("note the %s answer on %s: %w", answer.Role, tick, err)
		}
		if _, err := r.tracker.Close(ctx, tick); err != nil {
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

// roleAnswerOf is the role's OWN answer, as its report stated it — for a feed
// line that must never attribute the run's verdict to the worker (tick 19l).
// The pwp run's close-out answered DONE_WITH_CONCERNS, the run concluded
// failed (no-commits), and one line that read "closeout-epic answered
// failed (no-commits)" sent the diagnosis looking for a worker that had
// declared failure — it had not. When no report was readable the answer is
// stated as that fact, not invented: this run does not put a status in a
// mouth that never said one.
func roleAnswerOf(collected *subprocess.Collection) string {
	if collected.Result != nil && collected.Result.RoleResult != nil && collected.Result.RoleResult.Status != "" {
		return collected.Result.RoleResult.Status
	}
	return "with no report the run could read"
}

// collectedLine is the feed line for a collected attempt (tick 19l): what the
// role answered, what the run concluded, and why — stated separately, so the
// verdict is never attributed to the worker that never said it. `who` names
// the job ("the review-epic job"); the two answers are the report's own
// STATUS line and the run's verdict/outcome pair, and the why is the
// collected message the refusal or the merge would carry anyway.
func collectedLine(who string, collected *subprocess.Collection) string {
	line := fmt.Sprintf("%s answered %s; the run's verdict is %s (%s)",
		who, roleAnswerOf(collected), collected.Verdict, collected.Result.Outcome)
	if collected.Message != "" {
		line += ": " + collected.Message
	}
	return line
}
