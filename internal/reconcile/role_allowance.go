package reconcile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The allowance of the jobs a run dispatches for itself over one tick — the
// resolve-conflict job (resolve.go) and the repair job (gate_repair.go).
//
// WHAT WAS WRONG (epic-2jn, 2026-09-27, vqc). Attempt 50 conflicted and its
// resolve job committed a clean resolution — and then its runner exited 0
// without writing a report. The verdict was missing-result, the resolve was
// recorded "failed", and the run stopped. A person released the tick with
// --carry-work; attempt 51 met the same conflict and was refused at once: "a
// resolve-conflict job already ran for this tick". The one-resolve-per-tick
// bound exists so a run does not loop paying for resolutions that do not
// converge, but it counted a job that never DELIVERED a resolution — a runner
// that died, left no report, was killed or lost — exactly like one that
// delivered a resolution which then did not hold. And a person's release,
// the decision to try the tick again, did not reset it.
//
// THE RULE NOW.
//
//   - A job that failed OPERATIONALLY does not use up the tick's job: it
//     never answered, so nothing about the conflict (or the gate) was learnt.
//     Operational means precisely: the executor could not say the job settled
//     or could not collect it, or the collect's verdict is missing-result —
//     no readable report, whatever the failure class (runner_error,
//     wall_clock_exceeded, quota_exhausted, infrastructure_error, or a
//     cancelled job). Everything else — a report asking for a person, a
//     boundary violation, no commits, markers left in the resolution, a
//     resolution that conflicts again, a repair whose gate fails — is an
//     answer on its merits, and spends the tick's job as it always did.
//   - Operational failures are bounded too: at most maxOperationalRetries
//     further jobs after the first, and then the stop, naming every job.
//   - A person's release of an attempt of the tick (`ticfac settle
//     --release`, with or without --carry-work) is a decision to try again:
//     the jobs recorded before it do not count against the allowance after it.
//
// Each job the allowance dispatches has an identity of its own — the first
// keeps the one it always had, the n-th adds "-r<n>" — so a restart finds
// the job it dispatched by identity (role_resume.go) and never mistakes an
// earlier job's settled state for the next one's.

// maxOperationalRetries is how many more jobs a tick is given after jobs that
// failed without delivering an answer.
const maxOperationalRetries = 2

// The failure kinds a failed run-dispatched job's decision records.
const (
	failureOperational = "operational"
	failureOnMerits    = "merits"
)

// operationalFailure reports whether a job whose collect ended in `err` never
// delivered an answer at all: `collected` is nil when the executor could not
// say it settled or could not collect it, and a missing-result verdict is a
// job that left no readable report.
func operationalFailure(collected *subprocess.Collection, err error) bool {
	if err == nil {
		return false
	}
	return collected == nil || collected.Verdict == subprocess.VerdictMissingResult
}

// roleJobLedger is what the run branch records about one role's jobs over one
// tick.
type roleJobLedger struct {
	// spent are the recorded jobs of the current allowance that answered: a
	// merge, or a failure on its merits. A record written before failures
	// carried a kind is read as spent — the bound it was written under.
	spent []runstate.Decision
	// operational are the recorded jobs of the current allowance that failed
	// without answering, oldest first.
	operational []runstate.Decision
	// ordinal is the number the next job under this attempt's identity takes:
	// one past every job recorded under it, whatever allowance it was in.
	ordinal int
}

// exhausted says the operational bound is reached.
func (l roleJobLedger) exhausted() bool { return len(l.operational) > maxOperationalRetries }

// roleJobLedgerOf reads the ledger of `role`'s jobs over `tick`. baseJobID is
// the first job's identity for the attempt at hand; later jobs of the same
// attempt are baseJobID+"-r<n>".
func (r *Reconciler) roleJobLedgerOf(role, tick, baseJobID string) (roleJobLedger, error) {
	ledger := roleJobLedger{ordinal: 1}
	if r.store == nil {
		return ledger, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return ledger, err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return ledger, err
	}
	released := latestReleaseOf(decisions, tick)
	for _, decision := range decisions {
		if decision.Role != role || decision.Request["kind"] == baseFoldKind {
			continue
		}
		if id, _ := decision.Request["tick_id"].(string); id != tick {
			continue
		}
		if job, _ := decision.Request["job_id"].(string); job == baseJobID || strings.HasPrefix(job, baseJobID+"-r") {
			ledger.ordinal++
		}
		if decision.Decision < released {
			continue
		}
		if kind, _ := decision.Response["failure"].(string); kind == failureOperational {
			ledger.operational = append(ledger.operational, decision)
		} else {
			ledger.spent = append(ledger.spent, decision)
		}
	}
	return ledger, nil
}

// latestReleaseOf is the number of the latest settlement of any attempt of
// `tick`, or 0 when no person has released one.
func latestReleaseOf(decisions []runstate.Decision, tick string) int {
	latest := 0
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op != SettleOp {
			continue
		}
		if id, _ := decision.Request["tick_id"].(string); id == tick && decision.Decision > latest {
			latest = decision.Decision
		}
	}
	return latest
}

// jobOrdinalSuffix is the identity suffix of the n-th job of one attempt: the
// first keeps the identity it always had.
func jobOrdinalSuffix(ordinal int) string {
	if ordinal <= 1 {
		return ""
	}
	return fmt.Sprintf("-r%d", ordinal)
}

// describeJobs names every recorded job of a ledger for a stop: its branch,
// its outcome and, for a failure, why.
func describeJobs(decisions []runstate.Decision, branchKey string) string {
	parts := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		branch, _ := decision.Request[branchKey].(string)
		status, _ := decision.Response["status"].(string)
		part := fmt.Sprintf("%s (%s", branch, status)
		if kind, _ := decision.Response["failure"].(string); kind != "" {
			part += ", " + kind
		}
		if reason, _ := decision.Response["reason"].(string); reason != "" {
			part += ": " + reason
		}
		parts = append(parts, part+")")
	}
	return strings.Join(parts, "; ")
}

// failureReason is the short account of a failed job a decision keeps.
func failureReason(err error) string {
	reason := ""
	if refusal, ok := AsRefusal(err); ok {
		reason = refusal.Message
	} else if err != nil {
		reason = err.Error()
	}
	if len(reason) > 600 {
		reason = strings.ToValidUTF8(reason[:600], "") + "…"
	}
	return reason
}

// haveCommit makes `head` a commit this checkout has: it already does, or it
// is fetched from `ref`. A job handed a committed resolution (or fix) that
// found nothing left to change answers with that very commit — which this
// checkout already has, and which is durable on the FAILED job's branch —
// while its own branch reaches origin only if its supervisor's best-effort
// final push landed. Fetching that branch unconditionally made a push that
// failed under load a stop (#82's retry test, CI run 36349904786).
func (r *Reconciler) haveCommit(head, ref string) error {
	if head != "" {
		if _, err := r.git.resolve(head); err == nil {
			return nil
		}
	}
	return r.git.fetch(ref)
}

// preserveHandedWork puts the commit a job was handed and answered with on
// the job's own branch too, so a restart that finds the job settled finishes
// it from its branch like any other. It is best-effort: the commit is already
// durable on the branch of the job that committed it, and a push that fails
// here is recorded, never a stop.
func (r *Reconciler) preserveHandedWork(marker attemptHandle, handed string) {
	branch := branchOf(marker.WriteRef)
	if remote, err := r.git.remoteHead(branch); err == nil && remote == handed {
		return
	}
	if _, stderr, err := r.git.try("", "push", r.opts.Remote, handed+":"+refFor(branch)); err != nil {
		r.record(marker.TickID, StageCollected,
			"%s answers with %s, the commit it was handed, which could not be put on its own branch %s (%s): "+
				"it stays durable on the branch of the job that committed it",
			marker.JobID, short(handed), branch, firstLine(stderr))
	}
}

// settledJobHead is the head a settled run-dispatched job left on `branch`,
// for the finish-from-the-branch path: origin's, when the branch reached it,
// and otherwise the job's local branch in this checkout. A job's branch
// reaches origin only through collect's preservation or its supervisor's
// best-effort push, and a job that answered with the commit it was HANDED has
// nothing for collect to preserve — so the incarnation (or the lease-lost
// pass of the same one) that finds it settled reads it here rather than
// refusing a job that finished.
func (r *Reconciler) settledJobHead(branch string) string {
	if remote, err := r.git.remoteHead(branch); err == nil && remote != "" {
		return remote
	}
	local, err := r.git.resolve(refFor(branch))
	if err != nil {
		return ""
	}
	return local
}

// roleJobAnsweredNothing says a run-dispatched job's head is no answer at all:
// its branch still sits at the commit the job was cut from. The one exception
// is a job cut at work HANDED to it — the committed fix or resolution an
// earlier job of the allowance left when it failed without answering — and
// answering with that very commit is its answer. The hand-off is read from
// the LEDGER (the operational decisions' recorded heads, under headKey) as
// well as from `carried`, because a later pass recomputes `carried` against
// an integration branch that may have moved while the job's base did not.
// Anything else at its base did nothing, and finishing it "from its branch"
// recorded a merge over nothing: the gate failed again over the same tree and
// the tick's one job was spent on a job that never ran (the hazard epic-6in's
// resume of repair-3 walked into).
func roleJobAnsweredNothing(head, base, carried string, ledger roleJobLedger, headKey string) bool {
	if head == "" {
		return true
	}
	if head != base {
		return false
	}
	if base == carried {
		return false
	}
	for _, decision := range ledger.operational {
		if handed, _ := decision.Response[headKey].(string); handed != "" && handed == base {
			return false
		}
	}
	return true
}

// startIsOperational says a run-dispatched job's failed Start is a job that
// never answered — an operational failure the allowance retries — rather than
// a stop. A plain error is the substrate failing to launch it (epic-6in:
// herdr's agent_name_taken), and so is a SETTLED refusal the caller could not
// finish from the branch: the identity is spent with nothing to show for it.
// Every other refusal stays the stop it was: live or unaddressable work under
// the identity is never dispatched over, and a cancelled or unenforceable
// dispatch is a person's to read.
//
// A start the substrate itself calls PERMANENT (permanentStart — the cloud
// door's invalid_sandbox_name) is a stop on its merits, once: the same
// identity is refused the same way on every ask, and counting it a job that
// never answered spent hn6 run_ee8e's resolve allowance in six seconds.
func startIsOperational(err error) bool {
	var permanent permanentStartAnswer
	if errors.As(err, &permanent) && permanent.PermanentStart() {
		return false
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		return err != nil
	}
	return refusal.Reason == subprocess.RefusedSettled
}

// permanentStartAnswer is a substrate's typed "this start can never succeed
// under this identity" (the cloud door's invalid_sandbox_name), recognised
// by its method so this package imports no executor.
type permanentStartAnswer interface{ PermanentStart() bool }
