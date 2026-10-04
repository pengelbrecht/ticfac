package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// factoryAttemptAnswer is the production reconcile.Options.FactoryAttempt: what
// the factory can still say about one of ITS OWN runs' attempts, asked by a
// settle running on a host that holds none of the attempt's state — the
// operator's machine the hold's printed command (`ticfac settle … --run-id
// run_…`) runs on, which never ran the worker and cannot build its executor
// (reconcile/settle.go, tick bd5).
//
// One read answers it: GET /api/runs/<id>, the same route the claim holder's
// aliveness and the settled-attempt takeover's record read, on the same
// credential surfaces (cloudHolderClient) — the run's own token inside a cloud
// container, the configured operator credential on a laptop. A run the factory
// has never heard of is answered "not asked" (a local run's settle keeps its
// own refusal), and so is an unreachable factory: a release never rules on a
// guess.
//
// The answer is the factory's own two records, in the order that keeps A6
// intact:
//
//   - a settlement it holds for the attempt's worker container: TERMINAL, with
//     the container's own last word — exit 0 is succeeded, anything else
//     failed. A settlement stands even while the run works on, exactly as a
//     local terminal attempt does.
//   - no settlement: the run itself decides. ALIVE means the attempt is still
//     the run's to address — cancelled and collected by its orchestrator,
//     never released behind its back. DEAD means nothing will ever answer for
//     the worker: that is `lost`, the one state a person may release. Anything
//     the factory cannot vouch for either way is not asked at all — the
//     release refuses rather than guessing.
func factoryAttemptAnswer(ctx context.Context, runID, tickID string, attempt int) reconcile.FactoryAttemptAnswer {
	client := cloudHolderClient()
	if client == nil {
		return reconcile.FactoryAttemptAnswer{
			Evidence: "no factory credential is configured on this host",
		}
	}
	if strings.TrimSpace(runID) == "" {
		return reconcile.FactoryAttemptAnswer{
			Evidence: "the run names no id to ask the factory by",
		}
	}
	askCtx, cancel := context.WithTimeout(ctx, claimHolderHTTPTimeout)
	defer cancel()
	data, err := client.request(askCtx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return reconcile.FactoryAttemptAnswer{
			Evidence: fmt.Sprintf("the factory could not be asked about run %s: %v", runID, err),
		}
	}
	var status struct {
		claimHolderRunStatus
		claimHolderSettled
	}
	if err := decodeCloudJSON(data, &status); err != nil {
		return reconcile.FactoryAttemptAnswer{
			Evidence: fmt.Sprintf("the factory's answer about run %s is unreadable: %v", runID, err),
		}
	}
	for _, settled := range status.SettledAttempts {
		if settled.TickID != tickID || settled.Attempt != attempt {
			continue
		}
		if settled.State != "completed" && settled.State != "failed" {
			continue
		}
		state := subprocess.StateFailed
		exit := "no exit code"
		if settled.ExitCode != nil {
			exit = fmt.Sprintf("exit %d", *settled.ExitCode)
			if settled.State == "completed" && *settled.ExitCode == 0 {
				state = subprocess.StateSucceeded
			}
		}
		return reconcile.FactoryAttemptAnswer{
			Asked:    true,
			Terminal: true,
			State:    state,
			Evidence: fmt.Sprintf("the factory recorded its worker container %s with %s at %s",
				settled.State, exit, settled.At),
		}
	}
	workflow := ""
	if status.Phase.Workflow.ID != "" {
		workflow = status.Phase.Workflow.Status
	}
	verdict := cloudHolderVerdict(status.Run.State, workflow)
	switch verdict.Verdict {
	case reconcile.HolderAlive:
		return reconcile.FactoryAttemptAnswer{
			Asked: true, Live: true,
			Evidence: fmt.Sprintf("the factory says run %s is live: %s", runID, verdict.Evidence),
		}
	case reconcile.HolderDead:
		return reconcile.FactoryAttemptAnswer{
			Asked: true, Lost: true,
			Evidence: fmt.Sprintf("the factory answers for no worker of attempt %d: run %s is over (%s)",
				attempt, runID, verdict.Evidence),
		}
	}
	return reconcile.FactoryAttemptAnswer{
		Evidence: fmt.Sprintf("the factory cannot say what became of run %s (%s)", runID, verdict.Evidence),
	}
}
