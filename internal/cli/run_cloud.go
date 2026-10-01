package cli

// `ticfac run <epic> --cloud` (tick ejw): cloud parity for the one command.
//
// The everyday local surface — start in the background and attach; run it
// again to attach to a live run or resume a stopped one — maps onto the
// factory one verb for one verb, because the two hosts already share the
// surfaces under it:
//
//   - START is the submission `ticfac cloud run` makes — the same pushed
//     boundary (prepareCloudSubmission), the same POST /api/runs — only the
//     wording differs: `run` says "starting/resuming", never "submitting",
//     because an operator of THIS command never chose to submit anything.
//
//   - ATTACH is watch's own body over the SAME feed source `ticfac watch`
//     reads a cloud run through (feedSource, tick k7p): the same live
//     block on a terminal, the same plain stream on a pipe, the same exit
//     codes — the exit table's (tick 8v3): 0 ended, 3 ended holding
//     something for a person, 5 detached while the run keeps going, 1 the
//     run ended failed (tick bot) or the feed was unreadable. There is
//     deliberately no second implementation of the view; two is the drift
//     every earlier surface had to settle.
//
//   - RESUME is a second invocation against a run the factory says has
//     finished — or a run whose record is FROZEN at a state its dead
//     Workflow instance no longer holds, which is the same thing: nothing
//     is advancing it. Either way the resume is a new submission, which the
//     cloud orchestrator resumes from the integration branch exactly the way
//     a local reconciler does. The liveness that decides attach from resume
//     is the same answer `ticfac status` gives — the record's state checked
//     against the Workflow instance — never the record state alone, which is
//     the one claim a dead run can still make.
//
//   - TRIAGE is untouched by this file on purpose: the findings channel is
//     the integration branch, the same branch on both hosts, so
//     `ticfac triage <epic>` already behaves identically for a cloud run.
//     The test that pins that covers the round trip: a cloud run that ends
//     holding an untriaged finding, the attach that says so with exit 3,
//     and the one triage command that clears it.
//
// Ctrl-C detaches without stopping, as locally: the watch ends, the run in
// the factory keeps going, the command exits the running class (5) and the
// line that says so names the one command that comes back. --json answers
// with the local command's own document (ticfac.run.v1), its run_id the
// factory's, so an agent reads a cloud run the way it reads a local one.
//
// The expert `ticfac cloud run|status|logs|...` commands stay for what they
// alone do — a queued submission, a budget ceiling, a hard stop — and this
// command does not grow their flags: everyday use is one flag, and the flag
// vocabulary here stays as small as the friction it removes.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// runCloudAttach is the live view `run --cloud` attaches with: watch's own
// command body, over the same feed source `ticfac watch` reads a cloud run
// through — so the view cannot drift from the one `ticfac watch <run-id>`
// renders. A seam for the same reason `runAttach` is one: the decision
// tests (start, attach, resume) answer the attach with a controlled value,
// and the one test that runs the production value proves the whole
// cloud run's path through the real watch.
var runCloudAttach = func(ctx context.Context, repo, epicID, runID string, stdout, stderr io.Writer) int {
	repoArg := repo
	interval := defaultWatchInterval
	plainJSON := false
	code := watchCommand(ctx, []string{runID}, &repoArg, &interval, &plainJSON, stdout, stderr)
	if code == exitRunning {
		// The watch itself learned the run's claim and says the run keeps
		// going: the same detach, in the local attach's words.
		fmt.Fprintf(stdout, "detached from cloud run %s — the run keeps going in the factory; "+
			"`ticfac run %s --cloud` attaches again, `ticfac status %s` asks whether it is alive\n",
			runID, epicID, runID)
		return exitRunning
	}
	if ctx.Err() != nil && code == 1 {
		// Ctrl-C: the run keeps going in the factory — the same detach the
		// local attach grants. The record is asked on a context that does not
		// carry the cancellation (the invocation's context is already gone),
		// because "still alive?" is the factory's fact, and the answer decides
		// whether the operator detached from a live run or lost one. The
		// answer is the same liveness the attach decision uses — never the
		// record state alone, because a record frozen at `running` by a
		// supervisor that never wrote its last word is a run nothing is
		// advancing, and "the run keeps going" would be the one lie this
		// command must not tell.
		if client, err := newCloudClient(); err == nil {
			askCtx := context.WithoutCancel(ctx)
			if record, err := readCloudRunRecord(askCtx, client, runID); err == nil {
				if answer := cloudRunLiveness(askCtx, runID, record.State); answer.Alive {
					fmt.Fprintf(stdout, "detached from cloud run %s — the run keeps going in the factory; "+
						"`ticfac run %s --cloud` attaches again, `ticfac status %s` asks whether it is alive\n",
						runID, epicID, runID)
					return exitRunning
				}
			}
		}
	}
	return code
}

// runCloudCommand is `run --cloud`'s body: the same verbs as the local body,
// against the factory — and the same --json contract (tick 8v3): prose and
// the attached view on stderr, one ticfac.run.v1 document on stdout, and an
// exit code that is the document's state class.
func runCloudCommand(ctx context.Context, epicID, repo string, fl *runFlags, stdout, stderr io.Writer) int {
	prose := stdout
	if *fl.asJSON {
		prose = stderr
	}
	finish := func(action, state, runID, note string) int {
		if *fl.asJSON {
			if err := emitRunJSON(action, state, note, epicID, runID, fl, stdout); err != nil {
				fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
				return exitGeneric
			}
		}
		return stateExitClass(state)
	}

	client, err := newCloudClient()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
		return finish("starting", agentStateFailed, "", err.Error())
	}

	// The project this checkout's submission carries (tick nyi): the GitHub
	// project of the checkout the run works in, read the same way the
	// submission boundary reads it, BEFORE the factory is asked anything —
	// because the run the command may attach to is THIS project's run, never
	// another project's run for the same epic id, and a checkout that names no
	// project cannot submit one either.
	project, err := cloudProjectOf(repo)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
		return finish("starting", agentStateFailed, "", err.Error())
	}

	// The run the epic already has in the factory — this project's, from the
	// run index's own ?project= window — and the newest of them is what
	// decides attach from resume.
	existing, err := cloudRunsForEpic(ctx, client, project, epicID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
		return finish("starting", agentStateFailed, "", err.Error())
	}
	// The wording keeps the local command's own: "starting" is what a first
	// invocation owes, and a run the epic already has there owes "resuming" —
	// the word a second invocation of `run` owes an operator who has run this
	// epic before. It is set below, where the factory's answer decided.
	action := "starting"
	if existing != nil {
		// The liveness that decides attach from resume is the same answer
		// `ticfac status` gives (tick nyi): the record's own state checked
		// against the Workflow instance when the operator's Cloudflare
		// credentials allow it. A record frozen at `running` by a supervisor
		// that never got to write its last word is a run nothing is advancing —
		// attaching to it forever is the one outcome this decision refuses.
		liveness := cloudRunLiveness(ctx, existing.RunID, existing.State)
		if liveness.Alive {
			fmt.Fprintf(prose, "cloud run %s is alive (%s) — attaching; Ctrl-C detaches without stopping it\n",
				existing.RunID, liveness.Reason)
			code := runCloudAttach(ctx, repo, epicID, existing.RunID, prose, stderr)
			return finish("attached", runAttachState(repo, existing.RunID, code), existing.RunID, "")
		}
		// Not alive — never started, finished, or a record frozen at a state
		// its dead instance no longer holds. All three are one action: a new
		// submission, which the cloud orchestrator resumes from the integration
		// branch the way a local reconciler does, and the reason names which of
		// the three it was, in the liveness answer's own words.
		action = "resuming"
		fmt.Fprintf(prose, "cloud run %s is not running — %s — resuming the epic as a new submission\n",
			existing.RunID, liveness.Reason)
	} else {
		fmt.Fprintf(prose, "no cloud run for epic %s in the factory — starting one\n", epicID)
	}

	runID, queued, err := submitCloudRun(ctx, client, repo, epicID, "", prose)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
		return finish(action, agentStateFailed, "", err.Error())
	}
	if queued {
		// The submission parked behind the project lease: the command did its
		// work, the run just has not started yet, and an attach to a run with
		// no feed would read as a failure it is not. The line says where it
		// parks and how to come back. The work is in flight, not done — the
		// exit table's running class (5), the same answer the local command
		// gives a run detached before it claimed its life.
		return finish(action, agentStateRunning, runID,
			"queued in the factory behind the project lease; it starts when that run ends")
	}
	fmt.Fprintf(prose, "cloud run %s %s in the factory — attaching; Ctrl-C detaches without stopping it\n",
		runID, action)
	code := runCloudAttach(ctx, repo, epicID, runID, prose, stderr)
	return finish(action, runAttachState(repo, runID, code), runID, "")
}

// cloudRunsForEpic answers, from the factory's run index filtered to one
// project, what the epic already has there: its NEWEST run, or none. The
// index serves started_at DESC (db.ts listRuns), so the first run it names
// for the epic is the newest — a fact of the index, not an assumption about
// it — and the newest run is the one that decides attach from resume.
//
// The project is the caller's own word, the one its submission carries: the
// index is asked through its ?project= filter (the factory's own, index.ts's
// listRoute), so the window the epic is searched in holds this project's
// runs and nobody else's — and a record the index still named that is not
// this project's is skipped too, because a run another project started for
// the same epic id is never this checkout's run (tick nyi).
func cloudRunsForEpic(ctx context.Context, client *cloudClient, project, epicID string) (*cloudRunRecord, error) {
	data, err := client.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/runs?limit=%d&project=%s", cloudRunIndexLimit, url.QueryEscape(project)), nil)
	if err != nil {
		return nil, err
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return nil, err
	}
	var newest *cloudRunRecord
	for i := range response.Runs {
		run := &response.Runs[i]
		if strings.TrimSpace(run.Epic) != epicID {
			continue
		}
		if strings.TrimSpace(run.Project) != project {
			continue
		}
		newest = run
		break
	}
	return newest, nil
}

// CloudSubstrateEnv opts a cloud run's containers into the factory's
// durable_object-policy class (epic umq): TICFAC_CLOUD_SUBSTRATE=do_v1. Unset
// is the factory's default substrate. Read at submit only — the factory
// records it with the run and never moves a run between substrates — so a
// run already going is unaffected by setting or clearing it.
const CloudSubstrateEnv = "TICFAC_CLOUD_SUBSTRATE"

func cloudSubstrate() string { return strings.TrimSpace(os.Getenv(CloudSubstrateEnv)) }

// submitCloudRun makes the submission `ticfac cloud run` makes — the same
// pushed boundary, the same POST — and reports it in `run`'s own voice.
// The returned queued answer is a submission the factory parked behind the
// project lease (it answered with a queued record, not a run): there is
// nothing to attach to yet.
//
// orchestrator is where the run's orchestrator runs: "" is the factory's own
// container, "local" is `run --cloud-workers` (this machine), which the
// factory records so it boots no orchestrator container for the run.
func submitCloudRun(ctx context.Context, client *cloudClient, repo, epicID, orchestrator string, stdout io.Writer) (runID string, queued bool, err error) {
	baseSHA, project, requestedBy, err := prepareCloudSubmission(ctx, repo, epicID)
	if err != nil {
		return "", false, err
	}
	submission := struct {
		Project      string `json:"project"`
		Epic         string `json:"epic"`
		BaseSHA      string `json:"base_sha"`
		RequestedBy  string `json:"requested_by"`
		Queue        bool   `json:"queue"`
		Orchestrator string `json:"orchestrator,omitempty"`
		Substrate    string `json:"substrate,omitempty"`
	}{
		Project: project, Epic: epicID, BaseSHA: baseSHA, RequestedBy: requestedBy,
		// The everyday surface has no queue flag: a lease another run holds is
		// a fact to report, not a parking decision this command makes for the
		// operator. The expert `cloud run --queue` stays the way in.
		Queue:        false,
		Orchestrator: orchestrator,
		Substrate:    cloudSubstrate(),
	}
	data, err := client.request(ctx, http.MethodPost, "/api/runs", submission)
	if err != nil {
		return "", false, err
	}
	var response cloudSubmissionResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return "", false, err
	}
	switch {
	case response.Run.RunID != "":
		printCloudRunBudget(stdout, response.Budget)
		return response.Run.RunID, false, nil
	case response.Queued.RunID != "":
		fmt.Fprintf(stdout, "cloud run %s queued in the factory", response.Queued.RunID)
		if response.Holder.RunID != "" {
			fmt.Fprintf(stdout, " — behind %s", response.Holder.RunID)
		}
		fmt.Fprintf(stdout, "; it starts when that run ends, and `ticfac run %s --cloud` attaches then\n", epicID)
		printCloudRunBudget(stdout, response.Budget)
		return response.Queued.RunID, true, nil
	case response.RunID != "":
		printCloudRunBudget(stdout, response.Budget)
		return response.RunID, false, nil
	default:
		return "", false, fmt.Errorf("factory accepted the submission but returned no run id")
	}
}
