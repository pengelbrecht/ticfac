package cli

// The bare-invocation overview's command-level tests (tick 2qz): `ticfac`
// with no arguments is the one screen an unattended factory is glanced at
// with — every run this checkout and the factory know, attention first, and
// every held or failed run naming its reason and the one command that
// clears it.
//
// The fixture is one checkout that knows four LOCAL runs — a held one, a
// failed one, a running one and a done one, exactly the four states the
// acceptance names — plus one CLOUD run served by the fake factory, with
// the tracker faked at the epicGraph seam the status model tests already
// use: the per-run model's own derivation is pinned in internal/statusmodel.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// overviewCloudRunID is the one cloud run the fake factory knows running:
// `run_` plus 32 hex, the shape a real run id has.
const overviewCloudRunID = "run_62c289d1e6f4a2b3c4d5e6f708192a3b"

// overviewCloudDoneID is a cloud run the factory holds FINISHED — another
// project's run (its epic is not this checkout's), whose records this
// checkout will never read: the finished-run case the overview must not
// read as held.
const overviewCloudDoneID = "run_8f3d1a09c2e74b56d801f2a3b4c5d6e7"

// overviewCloudFailID is a cloud run the factory holds FAILED: the resume
// after a fix is a NEW SUBMISSION to the same factory — `ticfac run <epic>
// --cloud` — never `run-epic`, which would restart the epic LOCALLY, in the
// foreground, on whatever machine happens to be reading the listing.
const overviewCloudFailID = "run_4c7e0b52a9d1f83b6c05e7d2a9f8b1c4"

// overviewFixture writes one checkout that knows four local runs, with the
// durable records and feed lines a real run of each kind leaves behind:
//
//   - epic-dnz is done: a completed checkpoint and its own run_finished line;
//   - epic-fld is failed: a failed checkpoint and the run_finished line that
//     carries the reason;
//   - epic-hld is held: a failed checkpoint (the hold's own vocabulary writes
//     one) plus the run_held line that names the tick and the attempt;
//   - epic-run is running: a live process claims it (runlife.Claim), so the
//     probe answers alive.
//
// The names order alphabetically dnz, fld, hld, run — the OPPOSITE of the
// attention-first order the overview owes — so the ordering assertion below
// proves a sort rather than a coincidence.
func overviewFixture(t *testing.T, now time.Time) (repo string, release func()) {
	t.Helper()
	repo = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		execTestCmd(t, repo, "git", args...)
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "overview@example.com")
	git("config", "user.name", "overview test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")

	checkpoint := func(runID, epicID, state, reason, tickState string) {
		t.Helper()
		raw, err := json.MarshalIndent(map[string]any{
			"schema_version": 3, "run_id": runID, "epic_id": epicID,
			"sequence": 1, "state": state, "reason": reason,
			"updated_at": now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			"ticks":      []any{map[string]any{"tick_id": "t1", "state": tickState, "attempt": 1}},
			"provenance": map[string]any{
				"run_id": runID, "tick_id": "", "attempt": 0,
				"source_ref":      "refs/heads/epic/" + epicID,
				"source_sha":      "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
				"integration_ref": nil, "phase": "worker", "executor": "local-subprocess",
				"workspace_id": nil, "backend": nil, "substrate_protocol": nil, "substrate_server_version": nil,
				"role": "implement-tick", "tier": "strong", "profile_digest": nil,
				"model": "@cf/zai-org/glm-5.3", "context_manifest_digest": nil,
			},
		}, "", "  ")
		if err != nil {
			t.Fatalf("marshal the checkpoint for %s: %v", runID, err)
		}
		dir := filepath.Join(repo, ".ticfac", "runs", runID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	say := func(runID string, events ...runfeed.Event) {
		t.Helper()
		feed := runfeed.Open(repo, runID)
		for _, event := range events {
			if err := feed.Append(event); err != nil {
				t.Fatalf("append %s to %s's feed: %v", event.Stage, runID, err)
			}
		}
	}
	one, two, three := 1, 2, 3

	// epic-dnz: done.
	checkpoint("epic-dnz", "dnz", "completed", "every tick of dnz is closed behind the integrated gate", "closed")
	say("epic-dnz", runfeed.NewEvent(now.Add(-time.Hour), "epic-dnz", "", nil,
		reconcile.StageRunFinished, "completed: every tick of dnz is closed behind the integrated gate"))

	// epic-fld: failed, holding nothing for anybody.
	checkpoint("epic-fld", "fld", "failed", "the integrated gate refused attempt 3 of t2: go test failed", "rejected")
	say("epic-fld",
		runfeed.NewEvent(now.Add(-90*time.Minute), "epic-fld", "t2", &three,
			reconcile.StageDispatched, "t2 try 1 dispatched"),
		runfeed.NewEvent(now.Add(-80*time.Minute), "epic-fld", "", nil,
			reconcile.StageRunFinished, "failed: the integrated gate refused attempt 3 of t2: go test failed"))

	// epic-hld: held for a person — the run_held line names the tick and the
	// attempt, and the settle command is addressed by them.
	checkpoint("epic-hld", "hld", "failed", "attempt 2 of t1 was struck out: the report names no status", "dispatched")
	say("epic-hld",
		runfeed.NewEvent(now.Add(-40*time.Minute), "epic-hld", "t1", &two,
			reconcile.StageRunHeld, "attempt_struck_out: the report names no status"),
		runfeed.NewEvent(now.Add(-30*time.Minute), "epic-hld", "", nil,
			reconcile.StageRunFinished, "failed: attempt 2 of t1 was struck out: the report names no status"))

	// epic-run: running — a live process claims it.
	checkpoint("epic-run", "run", "running", "t1 is dispatched", "dispatched")
	say("epic-run", runfeed.NewEvent(now.Add(-10*time.Minute), "epic-run", "t1", &one,
		reconcile.StageDispatched, "t1 try 1 dispatched"))
	life, err := runlife.Claim(repo, "epic-run")
	if err != nil {
		t.Fatalf("claim the running run: %v", err)
	}
	release = func() { life.Release("overview test") }

	return repo, release
}

// overviewCloudFactory wires the fake factory with the one cloud run the
// overview must list beside the local ones: the run index that names it, and
// the event stream its feed is read through.
func overviewCloudFactory(t *testing.T, now time.Time) {
	t.Helper()
	at := now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	feedText := strings.Join([]string{
		`{"schema_version":1,"at":"` + at + `","run_id":"` + overviewCloudRunID +
			`","tick_id":"t1","attempt":1,"stage":"dispatched","detail":"t1 try 1 dispatched"}`,
		"",
	}, "\n")
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{
				map[string]any{
					"run_id": overviewCloudRunID, "epic": "cld", "state": "running",
				},
				map[string]any{
					"run_id": overviewCloudDoneID, "epic": "plw", "state": "completed",
				},
				map[string]any{
					"run_id": overviewCloudFailID, "epic": "cfl", "state": "failed",
				},
			}}
		case request.Path == "/api/runs/"+overviewCloudRunID+"/events":
			return 200, map[string]any{
				"run_id": overviewCloudRunID, "state": "running",
				"text": feedText, "bytes": len(feedText), "total_bytes": len(feedText),
			}
		case request.Path == "/api/runs/"+overviewCloudDoneID+"/events":
			// A finished run that wrote no event this factory still serves:
			// the row's reason falls to the record's own terminal word.
			return 200, map[string]any{"run_id": overviewCloudDoneID, "state": "completed"}
		case request.Path == "/api/runs/"+overviewCloudFailID+"/events":
			// A failed run that wrote no event: its reason is the record's own
			// terminal word, and its clearing command is the cloud resume.
			return 200, map[string]any{"run_id": overviewCloudFailID, "state": "failed"}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
}

// fakeOverviewGraph swaps the tracker seam for the whole test, the same
// fake the status model tests use: one wave, one tick, the per-run states
// all come from the durable records anyway.
func fakeOverviewGraph(t *testing.T) {
	t.Helper()
	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph {
		return fakeGraph()
	}
}

// TestTheBareOverviewListsEveryRunAttentionFirst: the bare invocation lists
// the local runs and the factory's cloud run, attention first — the held run
// before the failed one, the failed one before the running ones, the done
// one last — and every held or failed line names its reason and the one
// command that clears it.
func TestTheBareOverviewListsEveryRunAttentionFirst(t *testing.T) {
	now := time.Now()
	repo, release := overviewFixture(t, now)
	defer release()
	overviewCloudFactory(t, now)
	fakeOverviewGraph(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	out := stdout.String()

	// Attention first: held, then failed (local before cloud), then the
	// running runs (local before cloud, the order they were enumerated),
	// then done — the finished cloud run last, after the done local one it
	// was enumerated behind. The fixture's alphabetical order is the
	// opposite, so this is the sort, not luck.
	want := []string{"epic-hld", "epic-fld", overviewCloudFailID, "epic-run", overviewCloudRunID, "epic-dnz", overviewCloudDoneID}
	last := -1
	for _, run := range want {
		at := strings.Index(out, run)
		if at < 0 {
			t.Fatalf("%s is not listed:\n%s", run, out)
		}
		if at < last {
			t.Errorf("%s is listed out of the attention-first order:\n%s", run, out)
		}
		last = at
	}

	// The held run: the reason in one line, and the single settle command.
	if line := lineOf(out, "epic-hld"); line != "" {
		if !strings.Contains(line, "held for a person") {
			t.Errorf("the held run's line does not say what it is: %q", line)
		}
		if !strings.Contains(line, "attempt_struck_out: the report names no status") {
			t.Errorf("the held run's line does not name its reason: %q", line)
		}
		if !strings.Contains(line, `ticfac settle hld t1 2 --release "<who>"`) {
			t.Errorf("the held run's line does not name the one command that clears it: %q", line)
		}
	} else {
		t.Errorf("the held run has no line:\n%s", out)
	}

	// The failed run: its own last word is its reason, and the command that
	// resumes the epic after a fix.
	if line := lineOf(out, "epic-fld"); line != "" {
		if !strings.Contains(line, "failed") {
			t.Errorf("the failed run's line does not say what it is: %q", line)
		}
		if !strings.Contains(line, "failed: the integrated gate refused attempt 3 of t2: go test failed") {
			t.Errorf("the failed run's line does not name its reason: %q", line)
		}
		if !strings.Contains(line, "ticfac run-epic fld") {
			t.Errorf("the failed run's line does not name the one command that clears it: %q", line)
		}
	} else {
		t.Errorf("the failed run has no line:\n%s", out)
	}

	// The running runs and the done ones say what they are.
	if line := lineOf(out, "epic-run"); !strings.Contains(line, "running") {
		t.Errorf("the running run's line reads %q", line)
	}
	if line := lineOf(out, overviewCloudRunID); !strings.Contains(line, "running") {
		t.Errorf("the cloud run's line reads %q", line)
	}

	// The failed cloud run: its own terminal word is its reason, and the one
	// command that clears it is the CLOUD resume — a new submission to its
	// factory — never `run-epic`, which would restart the epic locally, in
	// the foreground, on the machine that happens to be reading.
	if cloudFail := lineOf(out, overviewCloudFailID); cloudFail != "" {
		if !strings.Contains(cloudFail, "failed") {
			t.Errorf("the failed cloud run's line does not say what it is: %q", cloudFail)
		}
		if !strings.Contains(cloudFail, "ticfac run cfl --cloud") {
			t.Errorf("the failed cloud run's line does not name the cloud resume: %q", cloudFail)
		}
		if strings.Contains(cloudFail, "run-epic") {
			t.Errorf("the failed cloud run's line names run-epic, which restarts the epic locally: %q", cloudFail)
		}
	} else {
		t.Errorf("the failed cloud run has no line:\n%s", out)
	}
	if line := lineOf(out, "epic-dnz"); !strings.Contains(line, "done") {
		t.Errorf("the done run's line reads %q", line)
	}

	// The finished cloud run whose records this checkout cannot read is DONE,
	// said by its own record's terminal word — never "held for a person",
	// which is what a dead-run claim out of the missing records would read
	// as on the aggregate screen.
	line := lineOf(out, overviewCloudDoneID)
	if line == "" {
		t.Fatalf("the finished cloud run has no line:\n%s", out)
	}
	if !strings.Contains(line, "done") {
		t.Errorf("the finished cloud run's line reads %q, want done", line)
	}
	if strings.Contains(line, "held for a person") || strings.Contains(line, "clear with:") {
		t.Errorf("the finished cloud run claims a person's attention: %q", line)
	}
	if !strings.Contains(line, "completed") {
		t.Errorf("the finished cloud run's line does not carry its record's own word: %q", line)
	}
}

// TestTheBareOverviewJSONEmitsTheSameModel: --json emits the same models the
// prose renders — one versioned status model (ticfac.status.v1) per run, in
// the attention-first order, with the overview's own state, reason and
// clearing command beside them so a prose reader and a JSON reader cannot
// disagree.
func TestTheBareOverviewJSONEmitsTheSameModel(t *testing.T) {
	now := time.Now()
	repo, release := overviewFixture(t, now)
	defer release()
	overviewCloudFactory(t, now)
	fakeOverviewGraph(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	if doc.SchemaVersion != overviewSchemaVersion {
		t.Errorf("the overview carries schema_version %d, want %d", doc.SchemaVersion, overviewSchemaVersion)
	}
	if len(doc.Degraded) != 0 {
		t.Errorf("every source answered and the overview still claims degraded %v", doc.Degraded)
	}
	if len(doc.Runs) != 7 {
		t.Fatalf("the overview lists %d runs, want 7:\n%s", len(doc.Runs), stdout.String())
	}

	byID := map[string]overviewRun{}
	var order []string
	for _, run := range doc.Runs {
		byID[run.RunID] = run
		order = append(order, run.RunID)
	}
	want := []string{"epic-hld", "epic-fld", overviewCloudFailID, "epic-run", overviewCloudRunID, "epic-dnz", overviewCloudDoneID}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("run %d of the JSON is %s, want %s (the attention-first order)", i, order[i], want[i])
		}
	}

	// Every entry carries the same model a `ticfac status --json` of that run
	// emits — the same versioned shape, its own run id, its own waits.
	for _, run := range doc.Runs {
		if run.Model.SchemaVersion != statusmodel.SchemaVersion {
			t.Errorf("%s carries a model of schema_version %d, want %d",
				run.RunID, run.Model.SchemaVersion, statusmodel.SchemaVersion)
		}
		if run.Model.RunID != run.RunID || run.Model.EpicID != run.EpicID || run.Model.Host != run.Host {
			t.Errorf("%s's entry and its model disagree about identity: %+v", run.RunID, run)
		}
	}

	held := byID["epic-hld"]
	if held.State != overviewStateHeld || held.ClearWith == nil ||
		*held.ClearWith != `ticfac settle hld t1 2 --release "<who>"` {
		t.Errorf("the held run's entry reads state %q clear %v", held.State, held.ClearWith)
	}
	if held.Model.WaitsOn == nil || held.Model.WaitsOn.Kind != statusmodel.WaitHeldForPerson {
		t.Errorf("the held run's model does not wait on its hold: %+v", held.Model.WaitsOn)
	}

	failed := byID["epic-fld"]
	if failed.State != overviewStateFailed || failed.ClearWith == nil ||
		*failed.ClearWith != "ticfac run-epic fld" {
		t.Errorf("the failed run's entry reads state %q clear %v", failed.State, failed.ClearWith)
	}
	if failed.Model.Lifecycle.Phase != statusmodel.PhaseFailed {
		t.Errorf("the failed run's model reads phase %q", failed.Model.Lifecycle.Phase)
	}

	cloudFailed := byID[overviewCloudFailID]
	if cloudFailed.State != overviewStateFailed || cloudFailed.ClearWith == nil ||
		*cloudFailed.ClearWith != "ticfac run cfl --cloud" {
		t.Errorf("the failed cloud run's entry reads state %q clear %v, want the cloud resume",
			cloudFailed.State, cloudFailed.ClearWith)
	}

	running := byID["epic-run"]
	if running.State != overviewStateRunning || !running.Model.Liveness.Alive {
		t.Errorf("the running run's entry reads state %q alive %v",
			running.State, running.Model.Liveness.Alive)
	}
	cloud := byID[overviewCloudRunID]
	if cloud.State != overviewStateRunning || cloud.Host != statusmodel.HostCloud {
		t.Errorf("the cloud run's entry reads state %q host %q", cloud.State, cloud.Host)
	}
	done := byID["epic-dnz"]
	if done.State != overviewStateDone || done.Model.Lifecycle.Phase != statusmodel.PhaseDone {
		t.Errorf("the done run's entry reads state %q phase %q",
			done.State, done.Model.Lifecycle.Phase)
	}
	cloudDone := byID[overviewCloudDoneID]
	if cloudDone.State != overviewStateDone || cloudDone.ClearWith != nil {
		t.Errorf("the finished cloud run's entry reads state %q clear %v",
			cloudDone.State, cloudDone.ClearWith)
	}
	if cloudDone.Model.WaitsOn != nil && cloudDone.Model.WaitsOn.Kind == statusmodel.WaitDeadRun {
		t.Errorf("the finished cloud run's model waits on dead-run: %+v", cloudDone.Model.WaitsOn)
	}
}

// TestTheBareOverviewWithNoRuns: a checkout that knows no run says so rather
// than failing, and a factory that cannot be asked is named as a degraded
// source — the machine's honest answer, not a silence that reads as "no
// cloud runs exist".
func TestTheBareOverviewWithNoRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("a checkout with no runs exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "No runs.") {
		t.Errorf("the overview does not say there are no runs: %q", out)
	}
	if !strings.Contains(out, "cloud runs are not listed") {
		t.Errorf("the overview does not say why no cloud runs are listed: %q", out)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", repo, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("a checkout with no runs exits %d under --json, want %d", code, exitSuccess)
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	if len(doc.Runs) != 0 {
		t.Errorf("the overview lists %d runs where none exist", len(doc.Runs))
	}
	found := false
	for _, name := range doc.Degraded {
		if name == "cloud" {
			found = true
		}
	}
	if !found {
		t.Errorf("an unconfigured factory is not named in degraded %v", doc.Degraded)
	}
}

// lineOf returns the one line that names a run, the overview's own row.
func lineOf(out, runID string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, runID+":") || strings.HasPrefix(line, runID+" ") {
			return line
		}
	}
	return ""
}
