package herdr

import (
	"context"
	"fmt"
	"os"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// inspect: herdr answers liveness, durable evidence answers completion, and
// ONE rule combines them — the contract's `rules.completion_contract`, the
// same rule the local executor applies to a process.
//
// The order is the load-bearing part:
//
//   - a durable CANCEL record, first, because it is the one fact that outranks
//     everything a settled attempt could otherwise say;
//   - the REPORT, before herdr is asked anything: a worker that wrote its
//     report has finished, whatever the agent is still doing;
//   - herdr, for liveness only: "is the agent there" — never "did the work
//     succeed", which is 2xu's line, held here by construction;
//   - this executor's OWN settlement record — the marker written by the
//     inspect that positively observed the agent gone;
//   - nothing left to ask → `lost`, which is NOT terminal: it says nobody can
//     address the handle, and recovery may re-adopt.
//
// `failed` here means settled-not-finished: the agent is gone and there is no
// report. It is its own fact, never rounded to "still running" or to
// "succeeded".

// Inspect re-addresses a handle and reports what can be seen. It never
// dispatches, and it resumes the observation stream from cursor.
func (e *Executor) Inspect(h *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	local, err := local(h)
	if err != nil {
		return nil, err
	}
	st := e.storeAt(local.State)

	record, err := st.readAttempt()
	if err != nil {
		// No attempt record: this executor cannot address the handle. That
		// is `lost`, and lost is not terminal.
		return &subprocess.JobStatus{
			SchemaVersion: subprocess.SchemaVersion,
			JobID:         h.JobID,
			State:         subprocess.StateLost,
			Terminal:      false,
			ObservedAt:    e.stamp(),
			Observations: []subprocess.Observation{{
				At:   e.stamp(),
				Kind: subprocess.ObsExited,
				Detail: fmt.Sprintf("no attempt record at %s: this executor can no longer address the handle",
					local.State),
			}},
		}, nil
	}

	observations, next := st.observationsFrom(cursor)
	state, detail := e.observe(record)
	status := &subprocess.JobStatus{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         h.JobID,
		State:         state,
		Terminal:      terminalState(state),
		ObservedAt:    e.stamp(),
		Observations:  observations,
	}
	if !status.Terminal {
		cursorValue := next
		status.Cursor = &cursorValue
	}
	if detail != "" {
		status.Observations = append(status.Observations, subprocess.Observation{
			At: status.ObservedAt, Kind: kindFor(state), Detail: detail,
		})
	}
	return status, nil
}

// terminalState says whether a state can still change. One function, so
// `state` and `terminal` cannot be set from two different opinions.
func terminalState(state string) bool {
	switch state {
	case subprocess.StateSucceeded, subprocess.StateFailed, subprocess.StateCancelled:
		return true
	default:
		return false
	}
}

// statusOf is the one place a state is decided, so `state` and `terminal`
// cannot come from two different opinions. Every Start and every Inspect of
// an existing attempt asks it, which is what keeps adoption from
// redispatching over a live worker.
func (e *Executor) statusOf(record *attemptRecord) *subprocess.JobStatus {
	state, detail := e.observe(record)
	status := &subprocess.JobStatus{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         record.JobID,
		State:         state,
		Terminal:      terminalState(state),
		ObservedAt:    e.stamp(),
	}
	if detail != "" {
		status.Observations = append(status.Observations, subprocess.Observation{
			At: status.ObservedAt, Kind: kindFor(state), Detail: detail,
		})
	}
	return status
}

func kindFor(state string) string {
	switch state {
	case subprocess.StateCancelled:
		return subprocess.ObsCancelRequested
	case subprocess.StateSucceeded, subprocess.StateFailed:
		return subprocess.ObsExited
	default:
		return subprocess.ObsHeartbeat
	}
}

// observe answers the state and the sentence that says WHY, in the order the
// evidence has to be read.
func (e *Executor) observe(record *attemptRecord) (state, detail string) {
	st := e.storeAt(record.State)

	if cancelled, ok := st.cancelled(); ok {
		return subprocess.StateCancelled, fmt.Sprintf(
			"cancelled at %s; the dispatch is revoked and reissue is %s", cancelled.AcceptedAt, cancelled.Reissue)
	}

	// Durable evidence first, and only then anything the substrate says. A
	// worker that wrote its report has finished; an agent that is still
	// running has not, whatever the pane looks like.
	if report, has := e.readReport(record); has && report.Status != "" {
		head := headOf(record.Repo, record.Branch)
		commits, _ := commitsBeyond(record.Repo, record.BaseSHA, head)
		return subprocess.StateSucceeded, fmt.Sprintf(
			"the report at %s ends %s, and the branch carries %d commit(s) beyond %s",
			record.ResultPath, report.Status, commits, short(record.BaseSHA))
	}

	if !record.LaunchConfirmed {
		// The launch was never confirmed: this attempt was terminal the
		// moment its Start failed, and the record that says so is the
		// diagnostic state that failed launch deliberately left behind.
		return subprocess.StateFailed,
			fmt.Sprintf("the agent %s was never confirmed launched; the attempt settled without a worker",
				record.AgentName)
	}

	// herdr answers liveness. This is the ONE question it is asked in a
	// state decision, and its failures are the observer's, not the job's.
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	switch {
	case err == nil:
		return subprocess.StateRunning, fmt.Sprintf(
			"the agent %s is live in pane %s (herdr reports %s)",
			record.AgentName, agent.PaneID, agent.AgentStatus)
	case client.IsCode(err, client.CodeAgentNotFound), client.IsCode(err, client.CodePaneNotFound):
		// A POSITIVE answer that nothing is there. It settles the attempt —
		// and it is recorded durably, because it is the one settlement fact
		// a herdr-free collect can later read (tick 2xu's seam).
		_ = st.markAgentGone(e.stamp())
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("herdr answers that the agent %s is no longer there (%s)", record.AgentName, apiCode(err))})
		return subprocess.StateFailed, fmt.Sprintf(
			"the agent %s is gone and there is no report at %s: settled is not finished, and this is neither running nor done",
			record.AgentName, record.ResultPath)
	case st.agentGone():
		// herdr cannot be asked NOW, but an earlier inspect durably
		// recorded the positive answer. The durable record settles what the
		// silent substrate cannot.
		return subprocess.StateFailed, fmt.Sprintf(
			"the agent %s was observed gone at %s and there is no report at %s",
			record.AgentName, e.stamp(), record.ResultPath)
	default:
		// Nobody can say. Not terminal: lost is a statement about the
		// observer, and the attempt is held for a person rather than
		// redispatched.
		return subprocess.StateLost, fmt.Sprintf(
			"herdr cannot be asked about the agent %s (%v): nobody can say whether it is running, "+
				"which is not the same as nothing running", record.AgentName, err)
	}
}

// apiCode names an API error's code, for observations a person reads.
func apiCode(err error) string {
	if apiErr, ok := client.AsAPIError(err); ok {
		return apiErr.Code
	}
	return "transport failure"
}

// readReport reads the report from the path the executor owns, and failing
// that from the branch — a worker that committed its report and whose
// worktree has since been removed still reported.
func (e *Executor) readReport(record *attemptRecord) (subprocess.Report, bool) {
	if raw, err := os.ReadFile(record.ResultPath); err == nil {
		report := subprocess.ParseReport(string(raw))
		report.Path = record.ResultRel
		return report, true
	}
	head := headOf(record.Repo, record.Branch)
	if head == "" {
		return subprocess.Report{}, false
	}
	if body, ok := showFile(record.Repo, head, record.ResultRel); ok {
		report := subprocess.ParseReport(body)
		report.Path = record.Branch + ":" + record.ResultRel
		return report, true
	}
	return subprocess.Report{}, false
}
