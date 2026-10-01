package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// The worker-handle reader (epic hn6, tick zl1): where a worker's name is
// read from when the durable records do not carry it.
//
// runstate.Attempt.JobHandle is the reconciler's own dispatch marker, cut
// BEFORE the executor starts the job — its existence is the compare-and-swap
// that proves the dispatch happened — so it cannot name the worker: no
// agent, pane or pid key exists at marker time. herdr's agent_name and
// pane_id live on the executor's own JobHandle, which the run's attempt
// record never carries, and that handle could not be committed anyway: half
// of it is host paths a public repository must never hold.
//
// The names live where the worker itself does: the attempt record every
// executor of this protocol keeps in the dispatch's state directory on this
// machine, the same record the archived-report walk reads (report.go). This
// is the "reader learns where herdr's names live" half of zl1's fix, and it
// covers every executor whose workers this machine can watch: herdr names
// the agent and the pane it runs in, the local supervisor the pid it
// watches. A cloud run's workers are not on this machine at all — its
// gathering passes no reader and the model states the honest null.

// WorkerHandles is the production handle reader for a LOCAL run: it answers
// one (tick, attempt) worker's executor-own name from the attempt record in
// the dispatch's state directory on this machine — located by walking for
// the record rather than recomputing any executor's internal naming, exactly
// as the archived-report reader locates the same directory. Nil when nothing
// on this machine names the worker: no state directory, no record, a record
// that names another tick, or one that spells no name — never a guess from
// the job id, which is the run's identity for the attempt, not a word a
// person can find the worker on the machine by.
//
// Nothing is cached, deliberately: unlike a settled attempt's report, a live
// worker's name can APPEAR — the pane exists from the moment the executor
// launches the agent, seconds after the dispatch — and a per-frame file read
// is what a two-second redraw can afford.
func WorkerHandles(runID string) func(tickID string, attempt int) *string {
	return func(tickID string, attempt int) *string {
		for _, root := range reportStateRoots() {
			state, ok := findAttemptState(filepath.Join(root, runID, tickID, strconv.Itoa(attempt)))
			if !ok || !attemptNamesTick(state, tickID) {
				continue
			}
			if name, ok := attemptWorkerName(state); ok {
				return &name
			}
		}
		return nil
	}
}

// attemptWorkerName reads the executor's own name for the worker out of the
// attempt record in one state directory. The record is read LOOSELY, the way
// the rest of this package walks the same file: this is a reader of the
// executors' shared file name, not an importer of their shapes — herdr
// spells the agent name and the pane, the local supervisor the pid, and a
// record spelling none of them names no worker. The agent name is answered
// first: it is the word a person addresses the agent by, and the pane under
// it is where to look when the record predates the launch.
func attemptWorkerName(state string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(state, "attempt.json"))
	if err != nil {
		return "", false
	}
	var record struct {
		AgentName     string `json:"agent_name"`
		PaneID        string `json:"pane_id"`
		SupervisorPID int    `json:"supervisor_pid"`
	}
	if json.Unmarshal(raw, &record) != nil {
		return "", false
	}
	switch {
	case record.AgentName != "":
		return record.AgentName, true
	case record.PaneID != "":
		return record.PaneID, true
	case record.SupervisorPID > 0:
		return "pid:" + strconv.Itoa(record.SupervisorPID), true
	}
	return "", false
}
