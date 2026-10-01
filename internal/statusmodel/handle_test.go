package statusmodel

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The worker's handle (epic hn6, tick zl1): the executor's own name for the
// worker — the word a person finds it on the machine by. No executor names
// the worker on the durable attempt marker: the marker is cut before the
// start (its existence is the dispatch's compare-and-swap), and herdr's own
// handle carries host paths a public repository must never commit. The name
// is read from where the executors keep it: the attempt record in the
// dispatch's state directory on this machine — the same walk the archived
// reports read (report.go), because every executor of this protocol keeps
// an attempt.json there. herdr names the agent and the pane it runs in; the
// local supervisor names the pid it watches.

// writeAttemptRecord writes one attempt's executor record the way the
// executors leave it under the dispatch's state directory: nested under the
// executor's own repo and attempt keys, so the WALK — not a recomputed
// naming — is what finds it.
func writeAttemptRecord(t *testing.T, stateRoot, runID, tickID string, attempt int, record string) {
	t.Helper()
	dir := filepath.Join(stateRoot, "runs", runID, tickID, strconv.Itoa(attempt),
		"3f9c1b0a7d2e4f68", "9b41c0d5e7a2f318")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.json"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkerHandleIsReadFromTheExecutorsAttemptRecord: the reader answers
// the worker's name from the executor's own attempt record — herdr's agent
// name first (the name a person addresses the agent by), herdr's pane when
// no agent name rode the record, and the local supervisor's pid — the word a
// person finds the worker on the machine by, never the job id, which is the
// run's identity for the attempt, not a machine word.
func TestWorkerHandleIsReadFromTheExecutorsAttemptRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		want   string
	}{
		{
			name:   "herdr names the agent",
			record: `{"tick_id": "6dh", "agent_name": "tick-6dh-a3", "pane_id": "%7", "workspace_id": "ws-1"}`,
			want:   "tick-6dh-a3",
		},
		{
			name:   "herdr's pane, when no agent name rode the record",
			record: `{"tick_id": "6dh", "pane_id": "%7"}`,
			want:   "%7",
		},
		{
			name:   "the local supervisor names the pid it watches",
			record: `{"tick_id": "6dh", "supervisor_pid": 4821}`,
			want:   "pid:4821",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateRoot := t.TempDir()
			t.Setenv(EnvExecStateDir, stateRoot)
			writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3, tc.record)

			handle := WorkerHandles("epic-2jn")("6dh", 3)
			if handle == nil || *handle != tc.want {
				t.Fatalf("the handle reads %v, want %q: the executor's own record names the worker",
					handle, tc.want)
			}
		})
	}
}

// TestWorkerHandleIsNullWithoutAName: nothing that names the worker is the
// honest nil — no state directory, no record, a record that names another
// tick (the same identity guard the archived report holds), or a record that
// spells no agent, pane or pid. A reader that guessed the job id here would
// answer a word no person can find the worker by.
func TestWorkerHandleIsNullWithoutAName(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv(EnvExecStateDir, stateRoot)
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3, `{"tick_id": "6dh", "agent_name": "tick-6dh-a3"}`)
	reader := WorkerHandles("epic-2jn")

	if handle := reader("nwj", 1); handle != nil {
		t.Errorf("an attempt with no state directory answered %q, want nil", *handle)
	}
	if handle := reader("6dh", 4); handle != nil {
		t.Errorf("an attempt number with no directory answered %q, want nil", *handle)
	}
	// A state directory whose attempt record names another tick is not this
	// attempt's, however same-named its directory.
	writeAttemptRecord(t, stateRoot, "epic-2jn", "xbp", 2, `{"tick_id": "89m", "agent_name": "tick-89m-a2"}`)
	if handle := reader("xbp", 2); handle != nil {
		t.Errorf("another tick's same-named attempt directory answered %q, want nil", *handle)
	}
	// A record that names neither an agent, a pane nor a pid names no worker.
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 5, `{"tick_id": "6dh"}`)
	if handle := reader("6dh", 5); handle != nil {
		t.Errorf("a record naming no worker answered %q, want nil", *handle)
	}
}

// TestWorkerHandleIsAlsoFoundUnderTheDefaultStateRoots: without the
// environment's override, the worker's name is looked for under every
// executor's runs directory under ~/.ticfac/exec — the reader does not know
// which executor ran the attempt, so it walks them all, exactly as the
// archived report reader does.
func TestWorkerHandleIsAlsoFoundUnderTheDefaultStateRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvExecStateDir, "")
	writeAttemptRecord(t, filepath.Join(home, ".ticfac", "exec", "herdr"), "epic-2jn", "6dh", 3,
		`{"tick_id": "6dh", "agent_name": "tick-6dh-a3"}`)

	handle := WorkerHandles("epic-2jn")("6dh", 3)
	if handle == nil || *handle != "tick-6dh-a3" {
		t.Errorf("the handle reads %v, want the agent name under the executor's default state root", handle)
	}
}
