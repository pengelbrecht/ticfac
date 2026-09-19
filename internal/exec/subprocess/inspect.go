package subprocess

import (
	"fmt"
	"os"
	"path/filepath"
)

// inspect: pid liveness, worktree state, commits beyond the recorded base, and
// the report — read in that order and combined by ONE rule.
//
// The rule is job-protocol.json's `rules.completion_contract`, and it is the
// reason this file is longer than "did the process exit?": completion is
// driven by a durable git ref and a RESULT report, and never by terminal
// output or by a process exit. So a settled attempt is not a finished one, and
// the difference has its own state:
//
//	succeeded  — the report is there. Whether the process is is irrelevant.
//	failed     — settled, and there is no report. SETTLED-BUT-INCOMPLETE, which
//	             is a fact of its own and is never rounded to either neighbour.
//	running    — something outside the job says it is alive.
//	lost       — nobody can address it and it never settled. NOT terminal: it
//	             is a statement about the observer, and recovery may re-adopt.
//	cancelled  — a durable cancel record exists.

// Inspect re-addresses a handle and reports what can be seen. It never
// dispatches, and it resumes the observation stream from cursor.
func (e *Executor) Inspect(h *JobHandle, cursor string) (*JobStatus, error) {
	local, err := h.Local()
	if err != nil {
		return nil, err
	}
	st := e.storeAt(local.State)

	record, err := st.readAttempt()
	if err != nil {
		// The state directory is gone, so this executor cannot address the
		// handle. That is `lost`, and lost is not terminal.
		return &JobStatus{
			SchemaVersion: SchemaVersion,
			JobID:         h.JobID,
			State:         StateLost,
			Terminal:      false,
			ObservedAt:    e.stamp(),
			Cursor:        nil,
			Observations: []Observation{{
				At:     e.stamp(),
				Kind:   ObsExited,
				Detail: fmt.Sprintf("no attempt record at %s: this executor can no longer address the handle", local.State),
			}},
		}, nil
	}

	status := e.statusOf(st, record, cursor)
	status.JobID = h.JobID
	return status, nil
}

// statusOf is the one place a state is decided, so `state` and `terminal`
// cannot come from two different opinions.
func (e *Executor) statusOf(st *store, record *attemptRecord, cursor string) *JobStatus {
	observations, next := st.observationsFrom(cursor)

	state, detail := e.observe(st, record)

	status := &JobStatus{
		SchemaVersion: SchemaVersion,
		JobID:         record.JobID,
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
		status.Observations = append(status.Observations, Observation{
			At: status.ObservedAt, Kind: kindFor(state), Detail: detail,
		})
	}
	return status
}

func kindFor(state string) string {
	switch state {
	case StateCancelled:
		return ObsCancelRequested
	case StateSucceeded, StateFailed:
		return ObsExited
	default:
		return ObsHeartbeat
	}
}

// observeBeforeLiveness is a test seam and nothing else: nil in every real
// build. It runs between observe's evidence reads and its liveness check,
// which is exactly where a supervisor settling and exiting used to be misread
// as a lost attempt. A test uses it to settle the attempt at that instant and
// prove the answer is still 'failed', never 'lost'.
var observeBeforeLiveness func()

// observe answers the state and the sentence that says WHY, in the order the
// evidence has to be read.
func (e *Executor) observe(st *store, record *attemptRecord) (state, detail string) {
	if cancelled, ok := st.cancelled(); ok {
		return StateCancelled, fmt.Sprintf("cancelled at %s; credentials were revoked before the stop and reissue is %s",
			cancelled.AcceptedAt, cancelled.Reissue)
	}

	report, hasReport := e.readReport(record)

	// Durable evidence first, and only then anything about a process. A
	// worker that wrote its report and whose supervisor was then killed has
	// finished; a supervisor that is still running has not, whatever the log
	// says.
	if e.guarded("settle_from_evidence") && hasReport && report.Status != "" {
		head := headOf(record.Repo, record.Branch)
		commits, _ := commitsBeyond(record.Repo, record.BaseSHA, head)
		return StateSucceeded, fmt.Sprintf(
			"the report at %s ends %s, and the branch carries %d commit(s) beyond %s",
			record.ResultPath, report.Status, commits, short(record.BaseSHA))
	}

	if observeBeforeLiveness != nil {
		observeBeforeLiveness()
	}
	if e.alive(st, record) {
		return StateRunning, ""
	}

	// Settlement is read AFTER liveness, never before it. The supervisor
	// writes runner.exit atomically and only then exits, so once no process is
	// alive the marker is final — whereas a value read before the liveness
	// check can be stale by the time it is used.
	//
	// It used to be read at the top of this function. That opened a window:
	// read settled=false, the supervisor then settles and exits, alive()
	// re-reads the marker and correctly answers 'not alive' BECAUSE it is
	// settled — and this function, still holding the stale false, reported a
	// cleanly finished attempt as LOST. Lost makes the run refuse and stop.
	// The window is microseconds on a fast machine and real on a 2-vCPU CI
	// runner, where it failed TestAStoppedAttemptSPreservedWorkReachesTheNextAttempt
	// three times in one day as 'nobody can say whether it is running'.
	settled := st.settled()
	if settled {
		if !e.guarded("settle_from_evidence") {
			// With the guard off, nothing settles an attempt but the claimer
			// coming back to say so — which is how an in-flight state stays
			// in flight forever.
			return StateRunning, ""
		}
		code, _ := st.exitCode()
		if st.wallClockExceeded() {
			return StateFailed, fmt.Sprintf("stopped at its wall clock of %ds with no report at %s",
				record.WallSeconds, record.ResultPath)
		}
		return StateFailed, fmt.Sprintf(
			"settled (runner exit %d) with no report at %s: settled is not finished, and this is neither running nor done",
			code, record.ResultPath)
	}

	return StateLost, fmt.Sprintf(
		"no live process and no settlement for attempt %d of %s: nobody can say whether it is running, "+
			"which is not the same as nothing running", record.Attempt, record.JobID)
}

// alive asks the operating system. Appendix A #2: a record written by the
// thing that may be gone is not evidence of its liveness — so the runner log,
// which the job writes, is never read here while the guard is on.
func (e *Executor) alive(st *store, record *attemptRecord) bool {
	if !e.guarded("liveness_from_outside") {
		// The wrong implementation, kept as the negative control: "the job
		// wrote something, so the job is alive".
		if info, err := os.Stat(st.path(fileRunnerLog)); err == nil && info.Size() > 0 {
			return true
		}
	}
	if st.settled() {
		return false
	}
	for _, pid := range []int{st.supervisorPID(), record.SupervisorPID, st.runnerPID()} {
		if processAlive(pid) {
			return true
		}
	}
	return false
}

// readReport reads the report from the path the executor owns, and failing
// that from the branch — a worker that committed its report and whose worktree
// has since been removed still reported.
//
// report.Path is always run-relative (record.ResultRel), never
// record.ResultPath: the absolute form is a host path under this executor's
// state root, and it rides into a durable decision record via
// RoleResult.Result — a fact this repository's own public-repo guard forbids.
func (e *Executor) readReport(record *attemptRecord) (Report, bool) {
	if raw, err := os.ReadFile(record.ResultPath); err == nil {
		report := ParseReport(string(raw))
		report.Path = record.ResultRel
		return report, true
	}
	// The worktree copy is gone once the attempt is torn down; collect
	// archived it beside the attempt record first (tick 35h).
	if record.State != "" {
		if raw, err := os.ReadFile(filepath.Join(record.State, FileReportArchive)); err == nil {
			report := ParseReport(string(raw))
			report.Path = record.ResultRel
			return report, true
		}
	}
	head := headOf(record.Repo, record.Branch)
	if head == "" {
		return Report{}, false
	}
	if body, ok := showFile(record.Repo, head, record.ResultRel); ok {
		report := ParseReport(body)
		report.Path = record.Branch + ":" + record.ResultRel
		return report, true
	}
	return Report{}, false
}
