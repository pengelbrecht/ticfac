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
//     something for a person, 5 detached while the run keeps going, 1
//     unreadable. There is deliberately no second implementation of the
//     view; two is the drift every earlier surface had to settle.
//
//   - RESUME is a second invocation against a run the factory says has
//     finished: a new submission, which the cloud orchestrator resumes from
//     the integration branch exactly the way a local reconciler does. The
//     liveness that decides attach from resume is the run record's own
//     state — the Workflow's durable claim, never "is anything running
//     here", which is a question no cloud run can be asked from a laptop.
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
		// whether the operator detached from a live run or lost one.
		if client, err := newCloudClient(); err == nil {
			if record, err := readCloudRunRecord(context.WithoutCancel(ctx), client, runID); err == nil &&
				cloudRunClaimsLife(record.State) {
				fmt.Fprintf(stdout, "detached from cloud run %s — the run keeps going in the factory; "+
					"`ticfac run %s --cloud` attaches again, `ticfac status %s` asks whether it is alive\n",
					runID, epicID, runID)
				return exitRunning
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

	// The run the epic already has in the factory, from the run index — the
	// same widest window every prefix resolution reads (its N most recent).
	// A run it names is the newest the factory has for the epic (the index
	// serves started_at DESC), and that run decides attach from resume.
	existing, err := cloudRunsForEpic(ctx, client, epicID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s --cloud: %v\n", epicID, err)
		return finish("starting", agentStateFailed, "", err.Error())
	}
	if existing != nil && cloudRunClaimsLife(existing.State) {
		fmt.Fprintf(prose, "cloud run %s is alive (the factory's record says %s) — attaching; "+
			"Ctrl-C detaches without stopping it\n", existing.RunID, stateOrUnknown(existing.State))
		code := runCloudAttach(ctx, repo, epicID, existing.RunID, prose, stderr)
		return finish("attached", runAttachState(repo, existing.RunID, code), existing.RunID, "")
	}

	// Not alive in the factory — never started, or finished. All three are
	// one action: a new submission, which the cloud orchestrator resumes from
	// the integration branch the way a local reconciler does. The wording
	// keeps the local command's own: "resuming" is the word a second
	// invocation of `run` owes an operator who has run this epic before.
	action := "starting"
	if existing != nil {
		fmt.Fprintf(prose, "cloud run %s is not running (the factory's record says %s) — resuming the epic as a new submission\n",
			existing.RunID, stateOrUnknown(existing.State))
		action = "resuming"
	} else {
		fmt.Fprintf(prose, "no cloud run for epic %s in the factory — starting one\n", epicID)
	}

	runID, queued, err := submitCloudRun(ctx, client, repo, epicID, prose)
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

// cloudRunClaimsLife is whether a run record's own state is one a live cloud
// run holds — the factory's own vocabulary (ACTIVE_RUN_STATES in
// cloudflare/src/runs.ts: starting, running, stopping). A record with no
// state claims nothing: "unknown" must never read as alive, the same rule
// cloudRunLiveness holds.
func cloudRunClaimsLife(state string) bool {
	state = strings.TrimSpace(state)
	return state != "" && cloudRunStillGoing(state)
}

// cloudRunsForEpic answers, from the factory's run index, what the epic
// already has there: its NEWEST run, or none. The index serves started_at
// DESC (db.ts listRuns), so the first run it names for the epic is the
// newest — a fact of the index, not an assumption about it — and the newest
// run is the one that decides attach from resume.
func cloudRunsForEpic(ctx context.Context, client *cloudClient, epicID string) (*cloudRunRecord, error) {
	data, err := client.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/runs?limit=%d", cloudRunIndexLimit), nil)
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
		newest = run
		break
	}
	return newest, nil
}

// submitCloudRun makes the submission `ticfac cloud run` makes — the same
// pushed boundary, the same POST — and reports it in `run`'s own voice.
// The returned queued answer is a submission the factory parked behind the
// project lease (it answered with a queued record, not a run): there is
// nothing to attach to yet.
func submitCloudRun(ctx context.Context, client *cloudClient, repo, epicID string, stdout io.Writer) (runID string, queued bool, err error) {
	baseSHA, project, requestedBy, err := prepareCloudSubmission(ctx, repo, epicID)
	if err != nil {
		return "", false, err
	}
	submission := struct {
		Project     string `json:"project"`
		Epic        string `json:"epic"`
		BaseSHA     string `json:"base_sha"`
		RequestedBy string `json:"requested_by"`
		Queue       bool   `json:"queue"`
	}{
		Project: project, Epic: epicID, BaseSHA: baseSHA, RequestedBy: requestedBy,
		// The everyday surface has no queue flag: a lease another run holds is
		// a fact to report, not a parking decision this command makes for the
		// operator. The expert `cloud run --queue` stays the way in.
		Queue: false,
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
