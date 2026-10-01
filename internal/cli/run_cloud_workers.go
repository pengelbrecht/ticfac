package cli

// `ticfac run <epic> --cloud-workers`: local orchestrator, cloud workers.
//
// A cloud run's orchestrator lives in a factory container, and every time the
// platform replaces that container (a rollout, an eviction) the run pays a
// state-recovery bill. This placement moves ONLY the orchestrator: the Go
// reconciler runs on this machine, and every implement and role job still
// boots a worker container through the factory's per-tick sandbox door — the
// same door, on the same kind of credential, a container orchestrator uses.
//
// What the command does, so that nothing is exported by hand:
//
//   - RECORDS the run in the factory (POST /api/runs with orchestrator
//     "local"): the run gets a factory run id, the project's lease, the
//     budgets, the stop, and a Run Workflow that boots NO orchestrator
//     container — it supervises this machine's heartbeat instead, and at the
//     run's end revokes its credentials and reclaims its workers (#142/#143).
//
//   - COLLECTS the run's own credential for this machine
//     (POST /api/runs/<id>/orchestrator, on the operator's factory token): the
//     run token every in-run door takes, and the account's container ceiling,
//     all of which is the WORKERS' — this orchestrator holds no container.
//
//   - STARTS `ticfac run-epic` in the background, the same detached start a
//     local run gets, with the run's identity (--run-id, the factory's) and
//     the CLOUD profile set embedded in this binary (--profiles
//     cloudflare-sandbox), and an environment that says the rest:
//     TICKS_FACTORY_URL/TOKEN/PROJECT and TICKS_RUN_ID (the executor's door,
//     the feed relay, the done signal), TICKS_SUBSTRATE=cloud (the Workers AI
//     cells of .tick/runners.cloud.toml — no claude in the cloud), and
//     TICKS_ORCHESTRATOR=local (the heartbeat, and a worker ceiling that
//     keeps no slot for an orchestrator container).
//
//   - ATTACHES the live view, exactly as a local run does: the run's feed is
//     on this disk (and relayed to the factory, so `ticfac watch <run-id>`
//     works from anywhere).
//
// What runs HERE: the reconciler, its git work (claims, merges, base folds,
// the integration branch's pushes, the close-out) and the integrated gate,
// with this machine's own GitHub credential and Jev credential — the same
// ones a local run uses. What runs THERE: every worker, as a cloud run's
// would.
//
// A second invocation: a live run whose process is alive here is attached
// to; a live run whose local process died gets a fresh credential and a new
// local process under the SAME factory run (its workers keep running and are
// adopted); anything else — no run, a finished one — is a new submission,
// which resumes the epic from its integration branch.
//
// A FINISHED run is not resumed under its own run id, as a local run's is,
// because the factory model has no reopening: a run is one Workflow instance
// whose id is the run id, and its finalize is terminal — credentials
// revoked, worker containers reclaimed, lease released, the terminal feed
// line and board event published. The run's end is recorded truthfully (the
// done signal carries run-epic's outcome, so a failed, halted run is failed),
// and the next run picks the epic up from the integration branch, the
// tracker and the commit-keyed gate evidence. What a new id does not carry is
// the previous run's attempt numbering. The window in which the SAME id is
// kept is while the factory still holds the run alive: a local process that
// died without finishing is re-credentialled above.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runsignal"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// runStartCloudWorkers starts the detached run-epic with its extra
// environment. A seam for the reason runStartDetached is one; the production
// value is the same start, with the factory credentials in the child's
// environment rather than its argv.
var runStartCloudWorkers = startDetached

// localOrchestratorCredential is the factory's answer to
// POST /api/runs/<id>/orchestrator.
type localOrchestratorCredential struct {
	RunID               string `json:"run_id"`
	Project             string `json:"project"`
	Epic                string `json:"epic"`
	State               string `json:"state"`
	Token               string `json:"token"`
	FactoryMaxInstances int    `json:"factory_max_instances"`
}

// errNotLocalOrchestrator is the factory saying a run's orchestrator is its
// own container: it hands that run's credential to nobody.
var errNotLocalOrchestrator = errors.New("the run is orchestrated by a factory container")

// collectLocalCredential asks the factory for run runID's credential for
// this machine.
func collectLocalCredential(ctx context.Context, client *cloudClient, runID string) (*localOrchestratorCredential, error) {
	data, err := client.request(ctx, http.MethodPost, "/api/runs/"+url.PathEscape(runID)+"/orchestrator", map[string]any{})
	if err != nil {
		var api cloudAPIError
		if errors.As(err, &api) && api.status == http.StatusConflict && strings.Contains(string(api.body), "not_local_orchestrator") {
			return nil, errNotLocalOrchestrator
		}
		if errors.As(err, &api) && api.status == http.StatusNotFound && !strings.Contains(string(api.body), "unknown_run") {
			return nil, fmt.Errorf("the factory does not serve local orchestrators yet (its deploy predates "+
				"`--cloud-workers`; update it with `ticfac factory deploy` or merge to main): %w", err)
		}
		return nil, err
	}
	var credential localOrchestratorCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		return nil, fmt.Errorf("the factory's credential answer is unreadable: %w", err)
	}
	if credential.Token == "" || credential.RunID != runID {
		return nil, fmt.Errorf("the factory answered no credential for run %s", runID)
	}
	return &credential, nil
}

// cloudWorkersEnv is the environment the local orchestrator is started with:
// everything a container orchestrator's boot exports that this placement
// needs, and nothing it does not (no gateway variables: the classifier and
// the forge use this machine's own credentials, exactly as a local run).
func cloudWorkersEnv(factoryURL, project string, credential *localOrchestratorCredential) []string {
	env := []string{
		sandboximage.EnvFactoryURL + "=" + factoryURL,
		sandboximage.EnvFactoryToken + "=" + credential.Token,
		sandboximage.EnvFactoryProject + "=" + project,
		"TICKS_RUN_ID=" + credential.RunID,
		"TICKS_SUBSTRATE=cloud",
		runsignal.EnvOrchestrator + "=" + runsignal.OrchestratorLocal,
	}
	if credential.FactoryMaxInstances > 0 {
		env = append(env, sandboximage.EnvFactoryMaxInstances+"="+strconv.Itoa(credential.FactoryMaxInstances))
	}
	return env
}

// runCloudWorkersCommand is `run --cloud-workers`' body.
func runCloudWorkersCommand(ctx context.Context, epicID, repo string, fl *runFlags, stdout, stderr io.Writer) int {
	prose := stdout
	if *fl.asJSON {
		prose = stderr
	}
	finishFor := func(runID string) func(action, state, note string) int {
		return func(action, state, note string) int {
			if *fl.asJSON {
				if err := emitRunJSON(action, state, note, epicID, runID, fl, stdout); err != nil {
					fmt.Fprintf(stderr, "ticfac run %s --cloud-workers: %v\n", epicID, err)
					return exitGeneric
				}
			}
			return stateExitClass(state)
		}
	}
	fail := func(action string, err error) int {
		fmt.Fprintf(stderr, "ticfac run %s --cloud-workers: %v\n", epicID, err)
		return finishFor("")(action, agentStateFailed, err.Error())
	}

	client, err := newCloudClient()
	if err != nil {
		return fail("starting", err)
	}
	project, err := cloudProjectOf(repo)
	if err != nil {
		return fail("starting", err)
	}
	existing, err := cloudRunsForEpic(ctx, client, project, epicID)
	if err != nil {
		return fail("starting", err)
	}

	action := "starting"
	var credential *localOrchestratorCredential
	if existing != nil {
		action = "resuming"
		if liveness := cloudRunLiveness(ctx, existing.RunID, existing.State); liveness.Alive {
			// Alive in the factory. Attached to when its orchestrator is
			// alive HERE; re-credentialled when it is a local run whose
			// process died; refused when its orchestrator is a container.
			if probe := runlife.Probe(repo, existing.RunID, time.Now()); probe.State == runlife.Alive {
				fmt.Fprintf(prose, "run %s is alive (%s; its orchestrator on this machine: %s) — attaching; "+
					"Ctrl-C detaches without stopping it\n", existing.RunID, liveness.Reason, probe.Reason)
				code := attachCloudWorkersRun(ctx, epicID, repo, existing.RunID, prose, stderr)
				return finishFor(existing.RunID)("attached", runAttachState(repo, existing.RunID, code), "")
			}
			credential, err = collectLocalCredential(ctx, client, existing.RunID)
			if errors.Is(err, errNotLocalOrchestrator) {
				return fail(action, fmt.Errorf("cloud run %s of epic %s is alive in the factory with its "+
					"orchestrator in a container — `ticfac run %s --cloud` attaches to it, `ticfac status %s` "+
					"asks whether it is alive; one arbiter per project", existing.RunID, epicID, epicID, existing.RunID))
			}
			if err != nil {
				return fail(action, err)
			}
			fmt.Fprintf(prose, "run %s is alive in the factory (%s) but its orchestrator is not running here — "+
				"restarting it on this machine; its workers keep running and are adopted\n",
				existing.RunID, liveness.Reason)
		} else {
			// Not the same run id, unlike a local run's resume, and on purpose:
			// the factory FINALIZED this run (see the header) — its Workflow
			// instance, whose id is the run id, is complete; its credentials
			// are revoked, its workers reclaimed and its lease released. A
			// finished factory run is never reopened. What carries over is
			// what a resume needs: the integration branch, the tracker's
			// closed ticks, and gate evidence keyed by commit.
			fmt.Fprintf(prose, "run %s is not running — %s — a finished factory run is not reopened "+
				"(its credentials, workers and lease ended with it), so the epic resumes from its "+
				"integration branch as a new run\n",
				existing.RunID, liveness.Reason)
		}
	} else {
		fmt.Fprintf(prose, "no run for epic %s in the factory — starting one\n", epicID)
	}

	if credential == nil {
		runID, _, err := submitCloudRun(ctx, client, repo, epicID, "local", prose)
		if err != nil {
			return fail(action, err)
		}
		credential, err = collectLocalCredential(ctx, client, runID)
		if err != nil {
			return fail(action, fmt.Errorf("run %s was recorded in the factory but its credential could not "+
				"be collected (it ends on its own once its heartbeat bound passes): %w", runID, err))
		}
	}
	runID := credential.RunID
	finish := finishFor(runID)
	fmt.Fprintf(prose, "run %s: the orchestrator runs on this machine; every worker runs in the factory "+
		"(the cloud profile set, the Workers AI cells of .tick/runners.cloud.toml)", runID)
	if credential.FactoryMaxInstances > 0 {
		fmt.Fprintf(prose, ", at most %d worker container(s) at once", credential.FactoryMaxInstances)
	}
	fmt.Fprintln(prose)

	dir := runlife.Dir(repo, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: the run's log directory could not be created: %v\n", epicID, err)
		return finish(action, agentStateFailed, "the run's log directory could not be created")
	}
	argv := []string{"run-epic", epicID, "--repo", repo, "--run-id", runID, "--profiles", profile.EmbeddedCloud}
	if *fl.wall > 0 {
		argv = append(argv, "--wall", strconv.Itoa(*fl.wall))
	}
	logPath := filepath.Join(dir, startLogName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: %s could not be opened: %v\n", epicID, logPath, err)
		return finish(action, agentStateFailed, "the run's start log could not be opened")
	}
	// The argv only: the credential rides the environment, never this log.
	fmt.Fprintf(logFile, "ticfac run --cloud-workers started this run detached at %s: %s\n",
		time.Now().UTC().Format(time.RFC3339), strings.Join(argv, " "))
	child, err := runStartCloudWorkers(argv, cloudWorkersEnv(client.baseURL, project, credential), logFile)
	logFile.Close()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: the background run could not be started: %v\n", epicID, err)
		return finish(action, agentStateFailed, "the background run could not be started")
	}
	fmt.Fprintf(prose, "run %s starting in the background (pid %d; its first words are in %s)\n",
		runID, child.Pid(), logPath)
	return awaitClaimAndAttach(ctx, epicID, repo, runID, action, child, logPath, fl, finish, attachCloudWorkersRun, prose, stdout, stderr)
}

// attachCloudWorkersRun attaches to a live --cloud-workers run and says what
// detaching means, in this command's own words.
func attachCloudWorkersRun(ctx context.Context, epicID, repo, runID string, stdout, stderr io.Writer) int {
	code := runAttach(ctx, repo, runID, stdout, stderr)
	if code == exitRunning || (ctx.Err() != nil && code == 1 &&
		runlife.Probe(repo, runID, time.Now()).State == runlife.Alive) {
		fmt.Fprintf(stdout, "detached from run %s — it keeps going in the background; "+
			"`ticfac run %s --cloud-workers` attaches again, `ticfac status %s` asks whether it is alive\n",
			runID, epicID, runID)
		return exitRunning
	}
	return code
}
