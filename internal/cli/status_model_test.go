package cli

// The command-level half of the status model (tick 6dh): `ticfac status
// --json` emits the versioned model out of what the checkout actually holds
// — the run directory's records, the run's feed, the standing worktree and
// the runner's session log — with the tracker faked at the seam, because
// the graph's own derivation is pinned in internal/statusmodel's build tests.
//
// What this test pins is the WIRING: every source the gathering reads, the
// degraded names it never invents, and the exit code's independence from the
// model's content.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// modelFixture is a repo with one run's whole world: durable records under
// .ticfac/runs/<run-id>/, a feed with the run's own lines, one standing
// worktree, a runner session log under a faked home, and a live process
// claiming the run. The tracker is faked at the epicGraph seam.
func modelFixture(t *testing.T, now time.Time) (repo, home string) {
	t.Helper()
	repo = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "status@example.com")
	git("config", "user.name", "status test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")

	runID := "epic-rmod"
	runDir := filepath.Join(repo, ".ticfac", "runs", runID)
	write := func(rel, raw string) {
		t.Helper()
		path := filepath.Join(runDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must := func(v any, rel string) {
		t.Helper()
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", rel, err)
		}
		write(rel, string(raw))
	}

	executor, tier, model := "local-subprocess", "strong", "@cf/zai-org/glm-5.3"
	provenance := func(tickID string, attempt int) map[string]any {
		return map[string]any{
			"run_id": runID, "tick_id": tickID, "attempt": attempt,
			"source_ref": "refs/heads/epic/rmod", "source_sha": "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			"integration_ref": nil, "phase": "worker", "executor": executor,
			"workspace_id": nil, "backend": nil, "substrate_protocol": nil, "substrate_server_version": nil,
			"role": "implement-tick", "tier": tier, "profile_digest": nil, "model": model,
			"context_manifest_digest": nil,
		}
	}
	must(map[string]any{
		"schema_version": 3, "run_id": runID, "epic_id": "rmod",
		"sequence": 4, "state": "running", "reason": "t1 is dispatched",
		"updated_at": now.Add(-20 * time.Minute).UTC().Format(time.RFC3339),
		"ticks": []any{
			map[string]any{"tick_id": "t1", "state": "closed", "attempt": 1},
			map[string]any{"tick_id": "t2", "state": "dispatched", "attempt": 2},
		},
		"provenance": provenance("", 0),
	}, "checkpoint.json")
	must(map[string]any{
		"schema_version": 3, "attempt": 1, "tick_id": "t1",
		"dispatched_at": now.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		"job_handle":    map[string]any{"executor": executor},
		"provenance":    provenance("t1", 1),
	}, "attempts/1.json")
	must(map[string]any{
		"schema_version": 3, "attempt": 2, "tick_id": "t2",
		"dispatched_at": now.Add(-30 * time.Minute).UTC().Format(time.RFC3339),
		"job_handle":    map[string]any{"executor": executor},
		"provenance":    provenance("t2", 2),
	}, "attempts/2.json")
	must(map[string]any{
		"schema_version": 3, "decision": 1, "role": "classify-tick",
		"request":      map[string]any{"epic_id": "rmod"},
		"response":     map[string]any{"model": "c@example.com", "usage": map[string]any{"cost_usd": 0.02}},
		"validated":    true,
		"requested_at": now.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		"answered_at":  now.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		"provenance":   provenance("", 0),
	}, "decisions/1.json")
	must(map[string]any{
		"schema_version": 3, "key": "gate-t1-1-go",
		"provenance": map[string]any{
			"run_id": runID, "tick_id": "t1", "attempt": 1,
			"source_ref": "refs/heads/epic/rmod", "source_sha": "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			"integration_ref": "refs/heads/epic/rmod", "phase": "integrated", "executor": executor,
			"workspace_id": nil, "backend": nil, "substrate_protocol": nil, "substrate_server_version": nil,
			"role": "implement-tick", "tier": tier, "profile_digest": nil, "model": model,
			"context_manifest_digest": nil,
		},
		"check":       map[string]any{"id": "go", "kind": "command", "command": []any{"go", "test", "./..."}},
		"started_at":  now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		"finished_at": now.Add(-119 * time.Minute).UTC().Format(time.RFC3339),
		"exit_code":   0,
		"output":      map[string]any{"mode": "inline", "stdout": "ok\n", "stderr": "", "truncated": false, "redacted": true, "max_bytes": 1024},
		"result":      "pass", "acceptance": "required",
		"content_digest": "sha256:0", "persistence_uri": "ticfac://runs/" + runID + "/evidence/gate-t1-1-go.json",
	}, "evidence/gate-t1-1-go.json")

	// The run's feed: one retry, one stall warning, and the dispatch line.
	feed := runfeed.Open(repo, runID)
	one, two := 1, 2
	if err := feed.Append(runfeed.NewEvent(now.Add(-2*time.Hour), runID, "t1", &one,
		reconcile.StageRemoteRetried, "origin refused the fetch; retry 1")); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(runfeed.NewEvent(now.Add(-90*time.Minute), runID, "t2", &two,
		reconcile.StageStallWarned, "t2 is alive but has produced nothing durable for 15m")); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(runfeed.NewEvent(now.Add(-30*time.Minute), runID, "t2", &two,
		reconcile.StageDispatched, "t2 try 1 dispatched")); err != nil {
		t.Fatal(err)
	}

	// The standing worktree, on the write ref the run's vocabulary builds.
	worktree := filepath.Join(t.TempDir(), "wt-t2")
	git("branch", "ticfac/run-epic-rmod/tick-t2/attempt-2")
	cmd := exec.Command("git", "worktree", "add", "--quiet", worktree, "ticfac/run-epic-rmod/tick-t2/attempt-2")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktree, "work-t2.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The runner's session log, under a faked home, on the layout pi keeps
	// for the path GIT REGISTERS (which on this host may resolve a symlink the
	// created path carried).
	registered := registeredWorktree(t, repo, worktree)
	home = t.TempDir()
	sessionDir := statusmodel.SessionDir(home, registered)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lastTurn := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	session := fmt.Sprintf(`{"type":"session","timestamp":%q,"cwd":%q}
{"type":"message","timestamp":%q,"message":{"role":"assistant","content":[{"type":"toolCall","toolCallId":"c1","toolName":"read"}]}}
{"type":"custom","timestamp":%q}
`, now.Add(-30*time.Minute).UTC().Format(time.RFC3339Nano), worktree, lastTurn, lastTurn)
	if err := os.WriteFile(filepath.Join(sessionDir, "2026-09-27T04-08-08-000Z_session.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	return repo, home
}

// registeredWorktree is the path git's own census names for a worktree —
// the path the run's registrations and the session-log layout both key on.
func registeredWorktree(t *testing.T, repo, worktree string) string {
	t.Helper()
	_ = worktree
	resolved := repo
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		resolved = real
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok || path == filepath.Clean(resolved) {
			continue
		}
		return path
	}
	t.Fatalf("the census names no worktree beside %s", repo)
	return ""
}

func fakeGraph() *tk.Graph {
	return &tk.Graph{Waves: []tk.GraphWave{{
		Wave: 1,
		Tasks: []tk.GraphTask{
			{ID: "t1", Title: "the first tick", Status: "closed"},
			{ID: "t2", Title: "the second tick", Status: "open"},
		},
	}}}
}

// TestStatusJSONEmitsTheVersionedModel: the command reads the checkout it is
// given — records, feed, worktree census, session log — and emits the model
// every surface renders, with nothing degraded and no field guessed.
func TestStatusJSONEmitsTheVersionedModel(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)

	life, err := runlife.Claim(repo, "epic-rmod")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph { return fakeGraph() }
	t.Setenv("HOME", home)

	var out bytes.Buffer
	if code := Run([]string{"status", "--repo", repo, "--json", "epic-rmod"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}

	if model.SchemaVersion != statusmodel.SchemaVersion || model.RunID != "epic-rmod" ||
		model.EpicID != "rmod" || model.Host != statusmodel.HostLocal {
		t.Errorf("the model does not name itself: %+v", model)
	}
	if len(model.Degraded) != 0 {
		t.Errorf("every source answered and the model still claims degraded %v", model.Degraded)
	}
	if !model.Liveness.Alive || model.Liveness.State != "alive" {
		t.Errorf("the live run's model reads liveness %+v", model.Liveness)
	}
	if model.Lifecycle.Phase != statusmodel.PhaseWaves {
		t.Errorf("a run with one closed tick and one dispatched reads phase %q, want waves", model.Lifecycle.Phase)
	}

	if model.Waves == nil || len(*model.Waves) != 1 || len((*model.Waves)[0].Ticks) != 2 {
		t.Fatalf("the tracker's one wave did not ride: %+v", model.Waves)
	}
	ticks := (*model.Waves)[0].Ticks
	if ticks[0].State != "closed" || ticks[0].Try == nil || *ticks[0].Try != 1 {
		t.Errorf("the closed tick reads %+v", ticks[0])
	}
	if ticks[1].State != "dispatched" || ticks[1].Try == nil || *ticks[1].Try != 1 {
		t.Errorf("the dispatched tick reads %+v", ticks[1])
	}
	if ticks[1].Tier == nil || *ticks[1].Tier != "strong" || ticks[1].Model == nil ||
		*ticks[1].Model != "@cf/zai-org/glm-5.3" || ticks[1].Executor == nil ||
		*ticks[1].Executor != "local-subprocess" {
		t.Errorf("the dispatched tick's provenance did not ride: %+v", ticks[1])
	}
	if len(ticks[1].Tries) != 1 || ticks[1].Tries[0].Outcome != statusmodel.TryInFlight {
		t.Errorf("the dispatched tick's try history reads %+v", ticks[1].Tries)
	}
	if model.Cost.RecordedUSD != 0.02 || model.Cost.Attempts != 2 {
		t.Errorf("the cost reads %+v, want 0.02 recorded over 2 attempts", model.Cost)
	}
	if len(model.Gates) != 1 || model.Gates[0].Check != "go" || model.Gates[0].Result != "pass" {
		t.Errorf("the gate evidence reads %+v", model.Gates)
	}
	if model.Health.RemoteRetries != 1 || model.Health.StallWarnings != 1 {
		t.Errorf("the health counts read %+v", model.Health)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != statusmodel.WaitWorkers {
		t.Errorf("the run waits on %+v, want its live worker", model.WaitsOn)
	}

	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the standing worktree did not ride: %+v", model.Workers)
	}
	worker := (*model.Workers)[0]
	if worker.TickID != "t2" || worker.Attempt != 2 {
		t.Errorf("the worker reads %s#%d, want t2#2", worker.TickID, worker.Attempt)
	}
	if worker.SilenceSeconds == nil || *worker.SilenceSeconds < 100 || *worker.SilenceSeconds > 140 {
		t.Errorf("the worker's silence reads %+v, want ~2 minutes since the session log last grew", worker.SilenceSeconds)
	}
	if worker.LastTurn == nil || *worker.LastTurn != "assistant: read" {
		t.Errorf("the worker's last turn reads %+v, want the session log's summary", worker.LastTurn)
	}
	if worker.ElapsedSeconds == nil || *worker.ElapsedSeconds < 29*60 || *worker.ElapsedSeconds > 31*60 {
		t.Errorf("the worker's elapsed reads %+v, want ~30 minutes since its dispatch", worker.ElapsedSeconds)
	}
}

// TestStatusJSONNamesADegradedTracker: a tracker that cannot be read costs
// the model its waves and names itself in degraded — never the whole answer,
// and never a silence a renderer could mistake for "no waves".
func TestStatusJSONNamesADegradedTracker(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)

	life, err := runlife.Claim(repo, "epic-rmod")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph { return nil }
	t.Setenv("HOME", home)

	var out bytes.Buffer
	if code := Run([]string{"status", "--repo", repo, "--json", "epic-rmod"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}
	if model.Waves != nil {
		t.Errorf("an unreadable tracker produced waves %+v, want null", model.Waves)
	}
	if len(model.Degraded) != 1 || model.Degraded[0] != "tracker" {
		t.Errorf("the degraded list reads %v, want [tracker]", model.Degraded)
	}
	// The rest of the answer still stands.
	if model.Lifecycle.Phase != statusmodel.PhaseWaves || model.Cost.Attempts != 2 {
		t.Errorf("a degraded tracker cost more than the waves: %+v", model)
	}
}

// TestStatusJSONEmitsTheModelForACloudRun: a run the Workflow hosts answers
// the same model with host "cloud" — its liveness from the Workflow's own
// record, its feed from the factory's stream, its workers null (the census
// cannot be taken from here), and its records from where this checkout
// holds them.
func TestStatusJSONEmitsTheModelForACloudRun(t *testing.T) {
	runID := "run_62c289d1e6f4a2b3c4d"
	repo := t.TempDir()
	execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")
	execTestCmd(t, repo, "git", "config", "user.email", "status@example.com")
	execTestCmd(t, repo, "git", "config", "user.name", "status test")

	at := "2026-09-27T04:08:08Z"
	feedText := strings.Join([]string{
		`{"schema_version":1,"at":"` + at + `","run_id":"` + runID + `","tick_id":"t1","attempt":1,"stage":"dispatched","detail":"t1 try 1 dispatched"}`,
		`{"schema_version":1,"at":"` + at + `","run_id":"` + runID + `","tick_id":"t1","attempt":1,"stage":"run_held","detail":"attempt 1 of t1 struck out: the refusal the run recorded"}`,
		"",
	}, "\n")
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			// The run index: a full id resolves against it too.
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": runID, "epic": "cld", "state": "running",
			}}}
		case request.Path == "/api/runs/"+runID:
			return 200, map[string]any{"run": map[string]any{
				"run_id": runID, "epic": "cld", "state": "running",
			}}
		case request.Path == "/api/runs/"+runID+"/events":
			return 200, map[string]any{
				"run_id": runID, "state": "running",
				"text": feedText, "bytes": len(feedText), "total_bytes": len(feedText),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph {
		return &tk.Graph{Waves: []tk.GraphWave{{
			Wave:  1,
			Tasks: []tk.GraphTask{{ID: "t1", Title: "the one tick", Status: "open"}},
		}}}
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"status", "--repo", repo, "--json", runID}, &out, &errOut)
	if code != 0 {
		t.Fatalf("a live cloud run exited %d: %s\n%s", code, out.String(), errOut.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}

	if model.Host != statusmodel.HostCloud || model.RunID != runID || model.EpicID != "cld" {
		t.Errorf("the cloud model does not name itself: %+v", model)
	}
	if !model.Liveness.Alive || model.Liveness.State != "running" {
		t.Errorf("the Workflow's own record says running and the model reads %+v", model.Liveness)
	}
	if model.Workers != nil {
		t.Errorf("a cloud run's workers read %+v, want null: the census cannot be taken here", model.Workers)
	}
	if model.Waves == nil || len(*model.Waves) != 1 || len((*model.Waves)[0].Ticks) != 1 {
		t.Errorf("the fake tracker's wave did not ride: %+v", model.Waves)
	}
	// The run's own typed hold line is the wait, addressed to a person, with
	// the settle command spelled the way watch spells it.
	if model.WaitsOn == nil || model.WaitsOn.Kind != statusmodel.WaitHeldForPerson {
		t.Fatalf("a cloud run holding an attempt waits on %+v, want held-for-person", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil ||
		*model.WaitsOn.UnblockCommand != `ticfac settle cld t1 1 --release "<who>"` {
		t.Errorf("the unblocking command is %+v, want the settle command", model.WaitsOn.UnblockCommand)
	}
	if len(*requests) == 0 {
		t.Error("the factory was never asked")
	}
}

// TestStatusCloudRunReadsTheContainersRecords: a cloud run's records are the
// orchestrator CONTAINER's, not the factory's. The container runs
// `ticfac run-epic <epic>` — the same command a local run is — so its
// durable records land on the integration branch under the run id that
// command constructs, epic-<epic-id>, while the factory's run_<hex> id
// names the Workflow instance. A model that reads under the factory's id
// answers from an empty directory and degrades to feed-and-graph only
// (tick tem): no waves from the records, no cost, and a hold on untriaged
// findings surfaces only through the feed's run_held line, never as the
// model's WaitFinding attention with its triage pointer.
func TestStatusCloudRunReadsTheContainersRecords(t *testing.T) {
	factoryRunID := "run_9d0f17aa4c2e5b81f0d3"
	epicID := "cld"
	containerRunID := "epic-" + epicID
	repo := t.TempDir()
	execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")
	execTestCmd(t, repo, "git", "config", "user.email", "status@example.com")
	execTestCmd(t, repo, "git", "config", "user.name", "status test")

	// The container's durable records, as its own orchestrator commits them:
	// under .ticfac/runs/epic-<epic-id>/, named by the run the container
	// constructed. No directory under the factory's run id exists — that
	// is the defect's whole shape. Written through the record types so the
	// strict decoder the reader runs cannot be out-drifted by hand.
	at := "2026-09-27T04:08:08Z"
	one, tickID, executor := 1, "t1", "cloudflare-sandbox"
	role, tier, modelID := "implement-tick", "strong", "@cf/zai-org/glm-5.3"
	provenance := func(tick *string, attempt *int) runstate.Provenance {
		return runstate.Provenance{
			RunID:     containerRunID,
			TickID:    tick,
			Attempt:   attempt,
			SourceRef: "refs/heads/epic/" + epicID,
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			Phase:     runstate.PhaseWorker,
			Executor:  &executor, Role: &role, Tier: &tier, Model: &modelID,
		}
	}
	writeRecord := func(rel string, record any) {
		t.Helper()
		raw, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", rel, err)
		}
		path := filepath.Join(repo, runstate.Root, "runs", containerRunID, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRecord("checkpoint.json", runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         containerRunID,
		EpicID:        epicID,
		Sequence:      2,
		State:         runstate.StateRunning,
		Reason:        "t1 is dispatched",
		UpdatedAt:     at,
		Ticks:         []runstate.TickState{{TickID: tickID, State: "dispatched", Attempt: one}},
		Provenance:    provenance(nil, nil),
	})
	writeRecord("attempts/1.json", runstate.Attempt{
		SchemaVersion: runstate.SchemaVersion,
		Attempt:       one,
		TickID:        tickID,
		DispatchedAt:  at,
		JobHandle:     map[string]any{"executor": executor},
		Provenance:    provenance(&tickID, &one),
	})
	writeRecord("findings/f7c289d1.json", runstate.Finding{
		SchemaVersion:  runstate.SchemaVersion,
		Key:            "f7c289d1",
		Source:         "worker",
		DiscoveredFrom: "run-" + containerRunID + "/tick-" + tickID + "/attempt-1",
		Kind:           "defect",
		Title:          "a defect outside the reporting tick",
		Body:           "what the worker found, in two sentences.",
		Severity:       "low",
		TickID:         tickID,
		Attempt:        one,
		DoneItem:       "none",
		Status:         runstate.FindingProposed,
		ProposedAt:     at,
		Provenance:     provenance(&tickID, &one),
	})

	// The factory: a COMPLETED run, so the container's untriaged finding is
	// the attention the model owes a person. The feed carries no run_held
	// line — the finding must surface from the records, not the feed.
	feedText := strings.Join([]string{
		`{"schema_version":1,"at":"` + at + `","run_id":"` + factoryRunID + `","tick_id":"t1","attempt":1,"stage":"dispatched","detail":"t1 try 1 dispatched"}`,
		"",
	}, "\n")
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": factoryRunID, "epic": epicID, "state": "completed",
			}}}
		case request.Path == "/api/runs/"+factoryRunID:
			return 200, map[string]any{"run": map[string]any{
				"run_id": factoryRunID, "epic": epicID, "state": "completed",
			}}
		case request.Path == "/api/runs/"+factoryRunID+"/events":
			return 200, map[string]any{
				"run_id": factoryRunID, "state": "completed",
				"text": feedText, "bytes": len(feedText), "total_bytes": len(feedText),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph {
		return &tk.Graph{Waves: []tk.GraphWave{{
			Wave:  1,
			Tasks: []tk.GraphTask{{ID: tickID, Title: "the one tick", Status: "open"}},
		}}}
	}

	var out, errOut bytes.Buffer
	code := Run([]string{"status", "--repo", repo, "--json", factoryRunID}, &out, &errOut)
	if code != 1 {
		t.Fatalf("a completed cloud run exited %d, want the not-alive exit 1: %s\n%s", code, out.String(), errOut.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}

	// The model still NAMES the factory's run id — the id every surface
	// addresses the run by — but it READS the container's records.
	if model.RunID != factoryRunID || model.EpicID != epicID || model.Host != statusmodel.HostCloud {
		t.Errorf("the cloud model does not name itself: %+v", model)
	}
	if model.Cost.Attempts != 1 {
		t.Errorf("the container's dispatch marker did not ride: cost reads %+v, want 1 attempt", model.Cost)
	}
	if model.Waves == nil || len(*model.Waves) != 1 || len((*model.Waves)[0].Ticks) != 1 {
		t.Fatalf("the fake tracker's wave did not ride: %+v", model.Waves)
	}
	state := (*model.Waves)[0].Ticks[0]
	if state.State != "dispatched" || state.Try == nil || *state.Try != 1 {
		t.Errorf("the wave's tick reads %+v, want the checkpoint's dispatched try 1", state)
	}

	// The untriaged finding the container filed is the model's attention,
	// with the triage pointer the findings surface spells.
	var finding *statusmodel.Attention
	for i := range model.Attention {
		if model.Attention[i].Kind == statusmodel.WaitFinding {
			finding = &model.Attention[i]
		}
	}
	if finding == nil {
		t.Fatalf("the container's untriaged finding never surfaced: attention %+v", model.Attention)
	}
	if !finding.NeedsPerson || finding.UnblockCommand == nil ||
		*finding.UnblockCommand != "ticfac triage "+epicID {
		t.Errorf("the finding's triage pointer reads %+v", finding)
	}
	if len(*requests) == 0 {
		t.Error("the factory was never asked")
	}
}

// captureStatusSources swaps the model's assembly seam for a recorder that
// hands back the Sources each gathering built, so a test can pin WHAT the
// wiring passes rather than what the model answers. The dashboard's wave-1
// readers (hn6, tick r5i) are nil-safe stubs that answer nil, so an emitted
// model cannot tell a wired gathering from an unwired one: the Sources are
// the only place the wire is observable at all.
func captureStatusSources(t *testing.T) *statusmodel.Sources {
	t.Helper()
	real := statusBuild
	captured := &statusmodel.Sources{}
	statusBuild = func(src statusmodel.Sources) statusmodel.Model {
		*captured = src
		return statusmodel.Model{SchemaVersion: statusmodel.SchemaVersion}
	}
	t.Cleanup(func() { statusBuild = real })
	return captured
}

// TestStatusModelLocalWiringPassesTheDashboardReaders (hn6 wave 1, tick r5i):
// a LOCAL run's gathering passes the two dashboard readers the wave-2
// ticks fill — the runner-transcript activity window and the attempt
// reports. Both answer nil today, so an unwired gathering would leave every
// wave-2 fill invisible in `status --json` while every suite stays green
// (the package tests pin the readers, the renderer tests pin the golden);
// this pin is the only thing that holds the wire in place.
func TestStatusModelLocalWiringPassesTheDashboardReaders(t *testing.T) {
	captured := captureStatusSources(t)

	// A bare repository stands for the checkout: every source read is
	// best-effort, and the wiring under test is what the gathering PASSES,
	// not what any source answers.
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "status@example.com")
	git("config", "user.name", "status test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")

	localStatusModel(context.Background(), repo, "epic-none",
		runlife.Status{State: runlife.Alive},
		modelGatherers{
			graph: func(context.Context, string, string) *tk.Graph { return nil },
			ci:    func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil },
		})

	if captured.Activity == nil {
		t.Error("localStatusModel passes no Activity reader: the wave-2 activity tick would fill a reader no gathering calls")
	}
	if captured.Report == nil {
		t.Error("localStatusModel passes no Report reader: the wave-2 report tick would fill a reader no gathering calls")
	}
}

// TestStatusModelCloudWiringCarriesTheHostCost (hn6 wave 1, tick r5i): a
// CLOUD run's gathering passes no readers — its runners and attempt reports
// are not on this machine — and passes the factory's own ground-truth cost
// as the gateway's number when the run record carries one, and nothing when
// it does not. The command path reads the record the factory serves, so the
// record's cost_usd must ride through the same fetch the liveness answer
// rides on; the wave-1 model's cost lines answer empty either way, and only
// this pin says the river is wired.
func TestStatusModelCloudWiringCarriesTheHostCost(t *testing.T) {
	runID := "run_6a4b8e0f2c1d5f3a"
	const cost = 1.23
	for _, leg := range []struct {
		name  string
		carry bool
	}{
		{"record carries cost_usd", true},
		{"record carries no cost_usd", false},
	} {
		t.Run(leg.name, func(t *testing.T) {
			captured := captureStatusSources(t)
			repo := t.TempDir()
			execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")

			endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
				run := map[string]any{"run_id": runID, "epic": "cst", "state": "running"}
				if leg.carry {
					run["cost_usd"] = cost
				}
				switch {
				case request.Path == "/api/runs":
					return 200, map[string]any{"runs": []any{run}}
				case request.Path == "/api/runs/"+runID:
					return 200, map[string]any{"run": run}
				case request.Path == "/api/runs/"+runID+"/events":
					return 200, map[string]any{"run_id": runID, "state": "running", "text": "", "bytes": 0, "total_bytes": 0}
				}
				return 404, map[string]any{"error": "not_found"}
			})
			configureCloudFactory(t, endpoint)

			realGraph := epicGraph
			t.Cleanup(func() { epicGraph = realGraph })
			epicGraph = func(context.Context, string, string) *tk.Graph { return nil }

			var out, errOut bytes.Buffer
			if code := Run([]string{"status", "--repo", repo, "--json", runID}, &out, &errOut); code != 0 {
				t.Fatalf("a live cloud run exited %d: %s\n%s", code, out.String(), errOut.String())
			}
			if len(cloudFactoryRequests(requests)) == 0 {
				t.Fatal("the factory was never asked")
			}

			// A cloud run's runners and reports are not on this machine: the
			// readers pass nil and the model states the honest not-measured.
			if captured.Activity != nil || captured.Report != nil {
				t.Errorf("a cloud run's gathering passes readers (activity=%v report=%v), want nil: its worktrees belong to the factory's containers",
					captured.Activity != nil, captured.Report != nil)
			}
			switch {
			case leg.carry && captured.WorkerCost == nil:
				t.Error("the record carried cost_usd and the gathering passed no WorkerCost: the factory's ground-truth number is the river the wave-2 cost lines read")
			case leg.carry && (captured.WorkerCost.USD != cost || captured.WorkerCost.Source != "gateway"):
				t.Errorf("the gathering passed WorkerCost $%.2f from %q, want the record's own $%.2f from \"gateway\"",
					captured.WorkerCost.USD, captured.WorkerCost.Source, cost)
			case !leg.carry && captured.WorkerCost != nil:
				t.Errorf("a record with no cost_usd passed WorkerCost %+v, want nil", *captured.WorkerCost)
			}
		})
	}
}

// TestStatusCIRefusesAnotherForge (tick 4zo): the CI gatherer resolves the
// repository the forge mirrors through the same reader everything else
// does, so a GitLab origin is refused by the host check — naming the host —
// rather than resolved to a slug and asked of api.github.com, where it
// would answer 404s the status view could only repeat. The refusal names
// gitlab, which no network answer does: this test fails on the old
// resolution whatever the network says.
func TestStatusCIRefusesAnotherForge(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("remote", "add", "origin", "git@gitlab.com:example/example.git")
	t.Setenv(forge.TokenEnv, "a-token")

	if _, err := statusCI(context.Background(), dir, "e1"); err == nil {
		t.Fatal("the CI gatherer resolved a GitLab origin")
	} else if !strings.Contains(err.Error(), "gitlab.com") {
		t.Errorf("the refusal does not name the host it found: %v", err)
	}
}
