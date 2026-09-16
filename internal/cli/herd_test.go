package cli

// Command tests for `ticfac herd` — the paint and notify surfaces tick glb
// owns. The substrate is the herdtest fake server: a real unix socket
// answering herdr's protocol, so the assertions are on the JSON the client
// actually sent — the same discipline the client's own tests hold.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// herdAttemptFixture writes one herdr executor attempt record at the layout
// the reconciler gives the executor. It is a TEST fixture, not a second
// reader: the fields are the executor's own record's, and the reader under
// test is herdr.Attempts.
func herdAttemptFixture(t *testing.T, root, run, tick string, attempt int, workspace, pane, agent, role string) {
	t.Helper()
	dir := filepath.Join(root, run, tick, strconv.Itoa(attempt))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"schema_version": 1,
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
		"issued_at":      time.Now().UTC().Format(time.RFC3339),
		"spec": map[string]any{
			"schema_version":  1,
			"job_id":          run + "/" + tick,
			"role":            role,
			"source":          map[string]any{"repository": "/repo", "base_sha": "s", "write_ref": "r"},
			"capabilities":    map[string]any{"persistence": "worktree", "isolation": "repo", "network": "full"},
			"artifact_prefix": "runs",
		},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// herdCheckpoint writes a run checkpoint with the given tick states, so paint
// reads the statuses exactly as the run records them.
func herdCheckpoint(t *testing.T, repo, run string, states map[string]string) {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(runstate.CheckpointPath(run)))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	ticks := make([]map[string]string, 0, len(states))
	for tick, state := range states {
		ticks = append(ticks, map[string]string{"tick_id": tick, "state": state})
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version": runstate.SchemaVersion,
		"run_id":         run,
		"epic_id":        strings.TrimPrefix(run, "epic-"),
		"sequence":       1,
		"state":          "running",
		"reason":         "fixture",
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
		"ticks":          ticks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHerdPaintReportsBadges(t *testing.T) {
	srv := herdtest.New(t, herdtest.Config{
		Workspaces: []string{"ws-1"},
		Agents: []herdtest.Agent{{
			Name: "worker-glb", PaneID: "ws-1:1", Status: "working",
		}},
	})
	root := t.TempDir()
	repo := t.TempDir()
	herdAttemptFixture(t, root, "epic-9pd", "glb", 2, "ws-1", "ws-1:1", "worker-glb", "implement-tick")
	herdCheckpoint(t, repo, "epic-9pd", map[string]string{"glb": "dispatched"})

	var out, errOut bytes.Buffer
	code := Run([]string{
		"herd", "paint",
		"--state-root", root, "--repo", repo, "--socket", srv.Path(), "--run", "epic-9pd",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "glb · implement-tick · dispatched") {
		t.Fatalf("output: %s", out.String())
	}
	if !strings.Contains(out.String(), "1 painted, 0 skipped") {
		t.Fatalf("summary: %s", out.String())
	}

	paneMeta := srv.PaneMetadata()
	if len(paneMeta) != 1 {
		t.Fatalf("pane reports: %+v", paneMeta)
	}
	if paneMeta[0].PaneID != "ws-1:1" || paneMeta[0].Source != "tk-herd-paint" {
		t.Fatalf("pane report: %+v", paneMeta[0])
	}
	if paneMeta[0].Title == nil || *paneMeta[0].Title != "glb · implement-tick · dispatched" {
		t.Fatalf("painted title: %v", paneMeta[0].Title)
	}
	wsMeta := srv.WorkspaceMetadata()
	if len(wsMeta) != 1 || wsMeta[0].WorkspaceID != "ws-1" {
		t.Fatalf("workspace reports: %+v", wsMeta)
	}
	if v := wsMeta[0].Tokens["TICK"]; v == nil || *v != "glb" {
		t.Fatalf("TICK token: %v", v)
	}
}

func TestHerdPaintUnknownStatusWithoutCheckpoint(t *testing.T) {
	srv := herdtest.New(t, herdtest.Config{
		Workspaces: []string{"ws-1"},
		Agents:     []herdtest.Agent{{Name: "worker-glb", PaneID: "ws-1:1", Status: "idle"}},
	})
	root := t.TempDir()
	herdAttemptFixture(t, root, "epic-9pd", "glb", 1, "ws-1", "ws-1:1", "worker-glb", "implement-tick")

	var out, errOut bytes.Buffer
	code := Run([]string{
		"herd", "paint",
		"--state-root", root, "--repo", t.TempDir(), "--socket", srv.Path(), "--run", "epic-9pd",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "glb · implement-tick · unknown") {
		t.Fatalf("a run with no readable checkpoint paints unknown: %s", out.String())
	}
}

func TestHerdPaintDryRunReportsWithoutCalling(t *testing.T) {
	srv := herdtest.New(t, herdtest.Config{
		Workspaces: []string{"ws-1"},
		Agents:     []herdtest.Agent{{Name: "worker-glb", PaneID: "ws-1:1", Status: "working"}},
	})
	root := t.TempDir()
	herdAttemptFixture(t, root, "epic-9pd", "glb", 1, "ws-1", "ws-1:1", "worker-glb", "implement-tick")

	var out, errOut bytes.Buffer
	code := Run([]string{
		"herd", "paint", "--dry-run",
		"--state-root", root, "--socket", srv.Path(), "--run", "epic-9pd",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "would paint") {
		t.Fatalf("dry-run output: %s", out.String())
	}
	if len(srv.PaneMetadata()) != 0 || len(srv.WorkspaceMetadata()) != 0 {
		t.Fatal("a dry run reached herdr")
	}
}

func TestHerdNotifyChimesOncePerEpisode(t *testing.T) {
	srv := herdtest.New(t, herdtest.Config{
		Agents: []herdtest.Agent{{Name: "worker-glb", PaneID: "ws-1:1", Status: "blocked"}},
	})
	root := t.TempDir()
	herdAttemptFixture(t, root, "epic-9pd", "glb", 2, "ws-1", "ws-1:1", "worker-glb", "implement-tick")

	run := func() int {
		var out, errOut bytes.Buffer
		code := Run([]string{
			"herd", "notify",
			"--state-root", root, "--socket", srv.Path(), "--run", "epic-9pd",
		}, &out, &errOut)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		return code
	}
	run()
	first := srv.Notifications()
	if len(first) != 2 {
		t.Fatalf("a lone blocked worker is blocked news AND a settled wave: %+v", first)
	}
	titles := map[string]bool{}
	for _, n := range first {
		titles[n.Title] = true
	}
	if !titles["tick glb blocked"] || !titles["wave complete: 1 worker settled"] {
		t.Fatalf("chimes: %+v", first)
	}

	// The same state again: the once-semantics hold, nothing new is sent.
	run()
	if again := srv.Notifications(); len(again) != len(first) {
		t.Fatalf("nothing may chime twice for one episode: %+v", again[len(first):])
	}

	// The worker recovers: the next block is a NEW episode and chimes again.
	srv.SetStatus("worker-glb", "working")
	run()
	srv.SetStatus("worker-glb", "blocked")
	run()
	got := srv.Notifications()
	blockedChimes := 0
	for _, n := range got {
		if n.Title == "tick glb blocked" {
			blockedChimes++
		}
	}
	if blockedChimes != 2 {
		t.Fatalf("block → recover → block is two chimes, got %d: %+v", blockedChimes, got)
	}
}

func TestHerdCommandsUsageAndRefusals(t *testing.T) {
	cases := []struct {
		args []string
		want int
		note string
	}{
		{[]string{"herd"}, 2, "no subcommand is usage"},
		{[]string{"herd", "nope"}, 2, "unknown subcommand is usage"},
		{[]string{"herd", "paint", "--ttl", "0", "--socket", "/nope"}, 2, "a non-positive TTL is usage"},
		{[]string{"herd", "paint", "extra", "--socket", "/nope"}, 2, "positional arguments are usage"},
		{[]string{"herd", "notify", "extra", "--socket", "/nope"}, 2, "positional arguments are usage"},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		if got := Run(tc.args, &out, &errOut); got != tc.want {
			t.Fatalf("%s: exit %d, want %d (%s)", tc.note, got, tc.want, errOut.String())
		}
	}

	// An unreachable herdr is a run failure (1), not a usage mistake — but
	// only when there is something to paint: an empty state root has
	// nothing to ask herdr about.
	root := t.TempDir()
	herdAttemptFixture(t, root, "epic-9pd", "glb", 1, "ws-1", "ws-1:1", "worker-glb", "implement-tick")
	var out, errOut bytes.Buffer
	if got := Run([]string{"herd", "paint", "--socket", "/no-such-herdr", "--state-root", root}, &out, &errOut); got != 1 {
		t.Fatalf("unreachable herdr exits %d, want 1: %s", got, errOut.String())
	}
	if !strings.Contains(errOut.String(), "not reachable") {
		t.Fatalf("the refusal names herdr: %s", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if got := Run([]string{"herd", "paint", "--socket", "/no-such-herdr", "--state-root", t.TempDir()}, &out, &errOut); got != 0 {
		t.Fatalf("an empty state root exits %d, want 0: %s", got, errOut.String())
	}
	if !strings.Contains(out.String(), "nothing to paint") {
		t.Fatalf("the empty state says so plainly: %s", out.String())
	}
}

func TestHerdUsageRecordsDroppedSurfaces(t *testing.T) {
	// The decision record is the usage text itself: the two dropped tk
	// surfaces and the consequence of each, where an operator (and the
	// ticks-side plugin tick) will read them.
	var out, errOut bytes.Buffer
	if got := Run([]string{"herd", "--help"}, &out, &errOut); got != 0 {
		t.Fatalf("help exits %d", got)
	}
	for _, want := range []string{"guard", "plugin", "Consequence", "si0"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("usage must record the dropped surface %q:\n%s", want, out.String())
		}
	}
}
