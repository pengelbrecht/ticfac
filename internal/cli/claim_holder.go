package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// The run's answer to "is the run holding this claim still running?"
// (reconcile.Options.ClaimHolder), asked when the holder's checkpoint on the
// integration branch does not read terminal — which is exactly what a run
// that DIED leaves: hn6's second cloud run was killed with its checkpoint at
// "dispatching", the factory recorded it failed with its Workflow complete,
// and the next submission held on its claim forever.
//
// Two hosts can run a holder, and each is asked what only it knows:
//
//   - a LOCAL run: its run.pid and the process table (runlife), in the
//     checkout the machine's registry names for it — or this checkout. A pid
//     that is gone, or reused, is a run that died; a run directory with no
//     run.pid is a run whose process released it on the way out;
//   - a CLOUD run: the factory's record and its Workflow instance
//     (GET /api/runs/<id>). A finished record (completed, stopped, failed)
//     or an ended instance (complete, errored, terminated) is a run nothing
//     will ever advance; a record with no instance behind it is dead by
//     #87's orphan rule when Cloudflare itself says there is none.
//
// Inside a cloud run's container the factory is asked on the run's own
// credential (TICKS_FACTORY_URL / TICKS_FACTORY_TOKEN), which the factory
// answers for runs of the same project only; on an operator's machine, on the
// configured factory credential. Anything neither host can vouch for either
// way is HolderUnknown: the claim is held, never taken over on a guess.

// claimHolderHTTPTimeout bounds one question to the factory.
const claimHolderHTTPTimeout = 15 * time.Second

// claimHolderLiveness is the production ClaimHolder for a run working in repo.
func claimHolderLiveness(repo string) func(context.Context, string) reconcile.HolderState {
	return func(ctx context.Context, runID string) reconcile.HolderState {
		var said []string
		if answer, decided := localHolder(repo, runID); decided {
			return answer
		} else if answer.Evidence != "" {
			said = append(said, answer.Evidence)
		}
		answer, decided := askFactoryAboutHolder(ctx, runID)
		if decided {
			return answer
		}
		if answer.Evidence != "" {
			said = append(said, answer.Evidence)
		}
		if len(said) == 0 {
			said = append(said, "no host this run can reach knows run "+runID)
		}
		return reconcile.HolderState{Verdict: reconcile.HolderUnknown, Evidence: strings.Join(said, "; ")}
	}
}

// localHolder asks the process table about a local run. Decided only when the
// run is known on this machine: registered here, or with a run directory in
// this checkout.
func localHolder(repo, runID string) (reconcile.HolderState, bool) {
	checkout := localRunCheckout(repo, runID)
	if checkout == "" {
		return reconcile.HolderState{}, false
	}
	status := runlife.Liveness(checkout, runID)
	switch status.State {
	case runlife.Alive:
		return reconcile.HolderState{Verdict: reconcile.HolderAlive,
			Evidence: "the local run is alive: " + status.Reason}, true
	case runlife.Dead:
		return reconcile.HolderState{Verdict: reconcile.HolderDead,
			Evidence: "the local run died: " + status.Reason}, true
	case runlife.NotRunning:
		return reconcile.HolderState{Verdict: reconcile.HolderDead,
			Evidence: "no process holds the local run in " + checkout + ": its last process released it"}, true
	}
	return reconcile.HolderState{Evidence: "the local run's process could not be asked: " + status.Reason}, false
}

// localRunCheckout is the checkout on THIS machine whose process ran runID,
// or "" when no process here ever did: the one the machine's registry names
// for it, else repo when the run's log stands in it. It is also how a cloud
// run whose orchestrator is this machine (`ticfac run --cloud-workers`, #151)
// is told from one the factory's container drives: its id is the factory's,
// but its process, pidfile and feed are here.
func localRunCheckout(repo, runID string) string {
	if reg, ok, err := runregistry.Lookup(runID); err == nil && ok && reg.Repo != "" {
		host, _ := os.Hostname()
		if reg.Host == "" || reg.Host == host {
			return reg.Repo
		}
	}
	if repo != "" {
		// run.log, not the directory: the directory is also the feed's, and a
		// cloud run watched from this checkout can have one. Only a local
		// process that claimed the run writes its log.
		if _, err := os.Stat(filepath.Join(runlife.Dir(repo, runID), runlife.LogName)); err == nil {
			return repo
		}
	}
	return ""
}

// claimHolderRunStatus is the part of GET /api/runs/<id> a holder verdict reads.
type claimHolderRunStatus struct {
	Run   cloudRunRecord `json:"run"`
	Phase cloudPhase     `json:"phase"`
}

// cloudHolderClient is the factory a holder is asked about: the run's own
// credential inside a cloud run's container, the configured operator
// credential elsewhere. Nil when neither is configured. A seam, so the tests
// answer with a fake factory.
var cloudHolderClient = func() *cloudClient {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("TICKS_FACTORY_URL")), "/")
	token := strings.TrimSpace(os.Getenv("TICKS_FACTORY_TOKEN"))
	if base != "" && token != "" {
		return &cloudClient{baseURL: base, token: token, http: httpnet.Client(claimHolderHTTPTimeout)}
	}
	client, err := newCloudClient()
	if err != nil {
		return nil
	}
	return client
}

// cloudHolderOrphaned is #87's orphan rule, asked only of a record that
// still claims life with no Workflow instance the factory could read: true
// when Cloudflare itself answered that the instance does not exist. A seam
// for the same reason as cloudHolderClient.
var cloudHolderOrphaned = func(ctx context.Context, runID, state string) (bool, string) {
	liveness := cloudRunLiveness(ctx, runID, state)
	return liveness.State == cloudLivenessOrphaned, liveness.Reason
}

// askFactoryAboutHolder asks the factory about a cloud run.
func askFactoryAboutHolder(ctx context.Context, runID string) (reconcile.HolderState, bool) {
	client := cloudHolderClient()
	if client == nil {
		return reconcile.HolderState{}, false
	}
	askCtx, cancel := context.WithTimeout(ctx, claimHolderHTTPTimeout)
	defer cancel()
	data, err := client.request(askCtx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		var api cloudAPIError
		if errors.As(err, &api) && api.status == http.StatusNotFound {
			return reconcile.HolderState{Evidence: "the factory has no run " + runID}, false
		}
		return reconcile.HolderState{Evidence: fmt.Sprintf("the factory could not be asked about run %s: %v",
			runID, err)}, false
	}
	var status claimHolderRunStatus
	if err := decodeCloudJSON(data, &status); err != nil {
		return reconcile.HolderState{Evidence: fmt.Sprintf("the factory's answer about run %s is unreadable: %v",
			runID, err)}, false
	}
	workflow := ""
	if status.Phase.Workflow.ID != "" {
		workflow = status.Phase.Workflow.Status
	}
	answer := cloudHolderVerdict(status.Run.State, workflow)
	if answer.Verdict == reconcile.HolderUnknown && !isFinishedCloudRun(status.Run.State) && workflow == "" {
		if orphaned, reason := cloudHolderOrphaned(ctx, runID, status.Run.State); orphaned {
			return reconcile.HolderState{Verdict: reconcile.HolderDead,
				Evidence: "the factory's record is an orphan: " + reason}, true
		}
	}
	return answer, answer.Verdict != reconcile.HolderUnknown
}

// cloudHolderVerdict is the verdict the factory's record and its Workflow
// instance's status make of a cloud run, before #87's orphan rule: workflow
// is "" when the factory could read no instance.
func cloudHolderVerdict(state, workflow string) reconcile.HolderState {
	state, workflow = strings.TrimSpace(state), strings.TrimSpace(workflow)
	switch {
	case isFinishedCloudRun(state):
		evidence := fmt.Sprintf("the factory's record says %s", state)
		if workflow != "" {
			evidence += fmt.Sprintf(" and its Workflow instance is %s", workflow)
		}
		return reconcile.HolderState{Verdict: reconcile.HolderDead, Evidence: evidence}
	case workflow == "complete" || workflow == "errored" || workflow == "terminated":
		return reconcile.HolderState{Verdict: reconcile.HolderDead, Evidence: fmt.Sprintf(
			"the factory's record says %s, but its Workflow instance is %s: nothing will ever advance the run",
			stateOrUnknown(state), workflow)}
	case workflow != "" && state != "":
		return reconcile.HolderState{Verdict: reconcile.HolderAlive, Evidence: fmt.Sprintf(
			"the factory's record says %s and its Workflow instance is %s", state, workflow)}
	}
	return reconcile.HolderState{Verdict: reconcile.HolderUnknown, Evidence: fmt.Sprintf(
		"the factory's record says %s and it could read no Workflow instance for the run", stateOrUnknown(state))}
}

// claimHolderSettled is the part of GET /api/runs/<id> a settled-attempt
// answer reads: the attempts whose own worker container the factory recorded
// settled (migration 0019).
type claimHolderSettled struct {
	SettledAttempts []struct {
		TickID   string `json:"tick_id"`
		Attempt  int    `json:"attempt"`
		State    string `json:"state"`
		ExitCode *int   `json:"exit_code"`
		At       string `json:"at"`
	} `json:"settled_attempts"`
}

// settledAttemptOnFactory is the production reconcile.Options.SettledAttempt:
// how another run's cloud attempt settled, as the factory recorded its worker
// container (hn6's ltg). A container that completed with exit 0 settled
// succeeded; one the factory has no settlement for — still running, never
// observed, a factory that predates the record — is not Known, and the
// takeover carries its work into a fresh worker as before.
func settledAttemptOnFactory(ctx context.Context, runID, tickID string, attempt int) reconcile.SettledState {
	client := cloudHolderClient()
	if client == nil {
		return reconcile.SettledState{}
	}
	askCtx, cancel := context.WithTimeout(ctx, claimHolderHTTPTimeout)
	defer cancel()
	data, err := client.request(askCtx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return reconcile.SettledState{}
	}
	var status claimHolderSettled
	if err := decodeCloudJSON(data, &status); err != nil {
		return reconcile.SettledState{}
	}
	for _, settled := range status.SettledAttempts {
		if settled.TickID != tickID || settled.Attempt != attempt {
			continue
		}
		exit := "no exit code"
		if settled.ExitCode != nil {
			exit = fmt.Sprintf("exit %d", *settled.ExitCode)
		}
		return reconcile.SettledState{
			Known:     true,
			Succeeded: settled.State == "completed" && settled.ExitCode != nil && *settled.ExitCode == 0,
			Evidence: fmt.Sprintf("the factory recorded its worker container %s with %s at %s",
				settled.State, exit, settled.At),
		}
	}
	return reconcile.SettledState{}
}
