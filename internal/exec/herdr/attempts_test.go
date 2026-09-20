package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// jsonEncode is the test-local marshal used by the fixture writer below.
func jsonEncode(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// writeFactsRecord writes one attempt.json the executor's own reader can read
// back: a full record with the fields Attempts consumes, at the layout the
// reconciler gives the executor (<root>/<run>/<tick>/<attempt>/attempt.json).
func writeFactsRecord(t *testing.T, root, run, tick string, attempt int, workspace, pane, agent, role string) {
	t.Helper()
	dir := filepath.Join(root, run, tick, strconv.Itoa(attempt))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"schema_version": stateSchemaVersion,
		"key":            "k/" + tick,
		"repo_key":       "rk",
		"repo":           "/repo",
		"job_id":         run + "/" + tick,
		"attempt":        attempt,
		"tick_id":        tick,
		"branch":         "b",
		"write_ref":      "r",
		"base_sha":       "s",
		"workspace_id":   workspace,
		"pane_id":        pane,
		"agent_name":     agent,
		"worktree":       "/wt",
		"state":          "running",
		"result_path":    "/wt/RESULT",
		"result_rel":     "RESULT",
		"kind":           "claude",
		"wall_seconds":   600,
		"remote":         "origin",
		"source_grade":   "commit",
		"server_version": "0.9.0",
		"protocol":       22,
		"issued_at":      "2026-09-16T00:00:00Z",
		"spec": map[string]any{
			"schema_version":  1,
			"job_id":          run + "/" + tick,
			"role":            role,
			"source":          map[string]any{"repository": "/repo", "base_sha": "s", "write_ref": "r"},
			"capabilities":    map[string]any{"persistence": "worktree", "isolation": "repo", "network": "full"},
			"artifact_prefix": "runs",
		},
	}
	raw, err := jsonMarshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileAttempt), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonMarshal(v any) ([]byte, error) {
	return jsonEncode(v)
}

// short: attempt records written into a tempdir and read back; no herdr server and no agent
func TestAttemptsLatestPerTick(t *testing.T) {
	root := t.TempDir()
	writeFactsRecord(t, root, "epic-9pd", "glb", 1, "ws-old", "pane-old", "agent-old", "implement-tick")
	writeFactsRecord(t, root, "epic-9pd", "glb", 2, "ws-2", "pane-2", "agent-2", "implement-tick")
	writeFactsRecord(t, root, "epic-9pd", "e08", 1, "ws-e08", "pane-e08", "agent-e08", "review-epic")

	facts, err := Attempts(root, "epic-9pd")
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	want := []AttemptFacts{
		{RunID: "epic-9pd", TickID: "e08", Attempt: 1, Role: "review-epic", WorkspaceID: "ws-e08", PaneID: "pane-e08", AgentName: "agent-e08", EpicID: "9pd"},
		{RunID: "epic-9pd", TickID: "glb", Attempt: 2, Role: "implement-tick", WorkspaceID: "ws-2", PaneID: "pane-2", AgentName: "agent-2", EpicID: "9pd"},
	}
	if !reflect.DeepEqual(facts, want) {
		t.Fatalf("attempts: %+v, want %+v", facts, want)
	}
}

// short: attempt records written into a tempdir and read back; no herdr server and no agent
func TestAttemptsMissingRunIsEmpty(t *testing.T) {
	facts, err := Attempts(t.TempDir(), "epic-nope")
	if err != nil || len(facts) != 0 {
		t.Fatalf("a run with no state is empty, not an error: %+v %v", facts, err)
	}
}

// short: attempt records written into a tempdir and read back; no herdr server and no agent
func TestAttemptsSkipsDamagedAndForeign(t *testing.T) {
	root := t.TempDir()
	writeFactsRecord(t, root, "epic-9pd", "glb", 1, "ws-1", "pane-1", "agent-1", "implement-tick")

	// A damaged record beside it: skipped, not fatal.
	bad := filepath.Join(root, "epic-9pd", "bad", "1")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, fileAttempt), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-attempt directory (the settlements dir): ignored.
	if err := os.MkdirAll(filepath.Join(root, "epic-9pd", "settlements"), 0o755); err != nil {
		t.Fatal(err)
	}

	facts, err := Attempts(root, "epic-9pd")
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(facts) != 1 || facts[0].TickID != "glb" {
		t.Fatalf("attempts: %+v", facts)
	}
}

// short: attempt records written into a tempdir and read back; no herdr server and no agent
func TestEpicIDFromRun(t *testing.T) {
	if got := epicIDFromRun("epic-9pd"); got != "9pd" {
		t.Fatalf("epic id: %q", got)
	}
	if got := epicIDFromRun("gate-cia-2"); got != "" {
		t.Fatalf("a run id of another shape has no epic token: %q", got)
	}
}
