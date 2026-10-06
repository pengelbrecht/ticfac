package statusmodel

import (
	"os"
	"path/filepath"
	"testing"
)

// The worker's harness kind (epic hn6, tick 5uq): the reader the activity
// seam is keyed by — which agent kind the executor launched for one (tick,
// attempt). No durable record names it: the attempt marker's provenance
// carries the model and the executor, and neither says which harness runs
// the worker (the bare alias "opus" runs under claude, the executor "herdr"
// under any kind). The kind is read from where the executors keep it: the
// attempt record in the dispatch's state directory on this machine — the
// same walk the handle reader and the archived reports use, because every
// executor of this protocol keeps an attempt.json there. herdr names the
// agent kind `kind`; the local supervisor names the runner it launched
// `runner`.

// TestWorkerRunnerIsReadFromTheExecutorsAttemptRecord: the reader answers
// the worker's harness kind from the executor's own attempt record —
// herdr's agent kind, and the local supervisor's runner — the word the
// activity seam is addressed by, never the model or the job id, which name
// anything but the harness.
func TestWorkerRunnerIsReadFromTheExecutorsAttemptRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		want   string
	}{
		{
			name:   "herdr names the agent kind",
			record: `{"tick_id": "6dh", "agent_name": "tick-6dh-a3", "kind": "claude", "model": "opus"}`,
			want:   "claude",
		},
		{
			name:   "herdr names pi",
			record: `{"tick_id": "6dh", "kind": "pi", "model": "cloudflare-workers-ai/@cf/zai-org/glm-5.3"}`,
			want:   "pi",
		},
		{
			name:   "the local supervisor names the runner it launched",
			record: `{"tick_id": "6dh", "supervisor_pid": 4821, "runner": "claude"}`,
			want:   "claude",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateRoot := t.TempDir()
			t.Setenv(EnvExecStateDir, stateRoot)
			writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3, tc.record)

			kind := WorkerRunner("epic-2jn")("6dh", 3)
			if kind == nil || *kind != tc.want {
				t.Fatalf("the harness kind reads %v, want %q: the executor's own record names it", kind, tc.want)
			}
		})
	}
}

// TestWorkerRunnerIsNullWithoutAKind: nothing that names the harness is
// the honest nil — no state directory, no record, a record that names
// another tick (the same identity guard the handle reader holds), or a
// record that spells no kind or runner. A reader that guessed from the
// model here would answer a harness the record never named.
func TestWorkerRunnerIsNullWithoutAKind(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv(EnvExecStateDir, stateRoot)
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3, `{"tick_id": "6dh", "kind": "claude"}`)
	reader := WorkerRunner("epic-2jn")

	if kind := reader("nwj", 1); kind != nil {
		t.Errorf("an attempt with no state directory answered %q, want nil", *kind)
	}
	if kind := reader("6dh", 4); kind != nil {
		t.Errorf("an attempt number with no directory answered %q, want nil", *kind)
	}
	// A state directory whose attempt record names another tick is not this
	// attempt's, however same-named its directory.
	writeAttemptRecord(t, stateRoot, "epic-2jn", "xbp", 2, `{"tick_id": "89m", "kind": "claude"}`)
	if kind := reader("xbp", 2); kind != nil {
		t.Errorf("another tick's same-named attempt directory answered %q, want nil", *kind)
	}
	// A record that names neither a kind nor a runner names no harness.
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 5, `{"tick_id": "6dh", "model": "opus"}`)
	if kind := reader("6dh", 5); kind != nil {
		t.Errorf("a record naming no harness answered %q, want nil", *kind)
	}
}

// TestWorkerRunnerKeepsWalkingWhenAnExecutorNamesNoKind: one root's answer
// is not the end of the search, exactly as the handle reader walks: herdr's
// record for an attempt that predates the agent's launch spells no kind
// beside the local supervisor's own record for the same attempt naming the
// runner it launched, and the reader must answer the supervisor's, never
// stop at the first root that named nothing.
func TestWorkerRunnerKeepsWalkingWhenAnExecutorNamesNoKind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvExecStateDir, "")

	// herdr's root, first in the walk's order: a nested record that names
	// the tick but no kind — the state an attempt is in between the
	// dispatch and the agent's launch.
	writeAttemptRecord(t, filepath.Join(home, ".ticfac", "exec", "herdr"), "epic-2jn", "6dh", 3,
		`{"tick_id": "6dh", "workspace_id": "ws-1"}`)
	// The local supervisor's root: the same attempt in the supervisor's own
	// flat layout, naming the runner it watches.
	dispatch := filepath.Join(home, ".ticfac", "exec", "local-subprocess", "runs",
		"epic-2jn", "6dh", "3")
	if err := os.MkdirAll(dispatch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatch, "attempt.json"),
		[]byte(`{"tick_id": "6dh", "supervisor_pid": 4821, "runner": "pi"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	kind := WorkerRunner("epic-2jn")("6dh", 3)
	if kind == nil || *kind != "pi" {
		t.Errorf("the harness kind reads %v, want the supervisor's runner from the next root's flat record", kind)
	}
}
