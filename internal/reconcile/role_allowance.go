package reconcile

import (
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
		if op, _ := decision.Request["op"].(string); op != settleOp {
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
