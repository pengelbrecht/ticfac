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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
	"github.com/pengelbrecht/ticfac/internal/runstate"
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

// ownRegistry points the machine's run registry at a directory this test
// alone holds. The overview enumerates the registry (tick 9ss), and the
// package's TestMain redirects every claim into ONE directory shared by the
// whole package run — without a per-test redirect, the listing would read
// the registrations every EARLIER test's claim left there (r-status,
// epic-rmod, this file's own epic-run), rows pointing at temp checkouts no
// assertion here can predict. Every test that runs the bare overview holds
// its own registry, and its own claims register there.
func ownRegistry(t *testing.T) {
	t.Helper()
	t.Setenv(runregistry.RegistryDirEnv, t.TempDir())
}

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
	// The epic-status seam the history rule reads: every epic open, so no
	// test that does not ask for history gets any from a tracker binary.
	realClosed := overviewEpicClosed
	t.Cleanup(func() { overviewEpicClosed = realClosed })
	overviewEpicClosed = func(context.Context, string, string) (bool, bool) { return false, true }
}

// TestTheBareOverviewListsEveryRunAttentionFirst: the bare invocation lists
// the local runs and the factory's cloud run, attention first — the held run
// before the failed one, the failed one before the running ones, the done
// one last — and every held or failed line names its reason and the one
// command that clears it.
func TestTheBareOverviewListsEveryRunAttentionFirst(t *testing.T) {
	now := time.Now()
	ownRegistry(t)
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
	ownRegistry(t)
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
	ownRegistry(t)
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

// lineAfter returns the line that follows a run's first line, for the tests
// that pin what a row carries under itself. Empty when the run is not
// listed or carries nothing under it.
func lineAfter(out, runID string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, runID+":") || strings.HasPrefix(line, runID+" ") {
			if i+1 < len(lines) {
				return lines[i+1]
			}
			return ""
		}
	}
	return ""
}

// overviewLiveRunFixture writes the shape the operator's machine actually
// holds while a run is live (tick 9ss): TWO checkouts of one origin — the
// one the run works in and the one the operator glances at — where the
// run's durable state lives ONLY on the epic/<id> branch, committed by the
// run-state store's plumbing exactly as a run commits it (no working tree
// ever holds it), its feed and pidfile live only in the working checkout,
// and the machine's run registry names the working checkout. The operator's
// checkout — on main — holds nothing of the run at all.
//
// The fixture is the defect's own shape: `localRunIDs` of the operator's
// checkout answers nothing (its working tree has no .ticfac/runs/epic-lve),
// and a probe with repo=the-operator's-checkout answers not_running (its
// tree has no pidfile either). Only the registry names the run, and only
// the registered repo can read it live.
func overviewLiveRunFixture(t *testing.T, now time.Time) (working, operator string, release func()) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	working = filepath.Join(root, "working")
	operator = filepath.Join(root, "operator")
	git := func(dir string, args ...string) {
		t.Helper()
		execTestCmd(t, dir, "git", args...)
	}
	git(root, "init", "--quiet", "--bare", "-b", "main", bare)
	git(root, "init", "--quiet", "-b", "epic/lve", seed)
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "-A")
	git(seed, "-c", "user.email=overview@example.com", "-c", "user.name=overview test",
		"commit", "--quiet", "-m", "seed")
	// main beside the integration branch, so both clones check something
	// out and hold a working tree the run's state must never appear in.
	git(seed, "branch", "main")
	git(seed, "push", "--quiet", bare, "epic/lve", "main")
	git(root, "clone", "--quiet", bare, working)
	git(root, "clone", "--quiet", bare, operator)

	// The run's durable state, as its own orchestrator commits it: through
	// the run-state store, whose plumbing lands the checkpoint on
	// refs/heads/epic/lve on origin and never touches a working tree.
	store, err := runstate.Open(runstate.Options{
		Repo: working, Remote: "origin", Branch: "epic/lve", RunID: "epic-lve",
	})
	if err != nil {
		t.Fatalf("open the run's state store: %v", err)
	}
	one, executor := 1, "local-subprocess"
	if outcome, err := store.PutCheckpoint(runstate.Checkpoint{
		RunID: "epic-lve", EpicID: "lve",
		State:  runstate.StateRunning,
		Reason: "t1 is dispatched",
		Ticks:  []runstate.TickState{{TickID: "t1", State: "dispatched", Attempt: one}},
		Provenance: runstate.Provenance{
			RunID:     "epic-lve",
			SourceRef: "refs/heads/epic/lve",
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			Phase:     runstate.PhaseWorker,
			Executor:  &executor,
		},
	}); err != nil || !outcome.EffectPermitted() {
		t.Fatalf("put the run's checkpoint on epic/lve: outcome %v, err %v", outcome, err)
	}

	// The feed, in the checkout the run works in — exhaust, never pushed.
	feed := runfeed.Open(working, "epic-lve")
	if err := feed.Append(runfeed.NewEvent(now.Add(-10*time.Minute), "epic-lve", "t1", &one,
		reconcile.StageDispatched, "t1 try 1 dispatched")); err != nil {
		t.Fatalf("append the run's dispatched line: %v", err)
	}

	// The live driver: this process claims the run IN THE WORKING CHECKOUT,
	// which writes its pidfile there and registers that checkout on the
	// machine — the one registration the overview must read the run
	// through.
	life, err := runlife.Claim(working, "epic-lve")
	if err != nil {
		t.Fatalf("claim the live run in its working checkout: %v", err)
	}
	release = func() { life.Release("overview test") }

	// The fixture's own honesty: state only on epic/<id> — NEITHER checkout
	// holds a run directory in its working tree.
	for _, checkout := range []string{working, operator} {
		if _, err := os.Stat(filepath.Join(checkout, runstate.Root, "runs")); err == nil {
			t.Fatalf("%s holds a .ticfac/runs directory in its working tree: the fixture must hold the run's state only on epic/lve", checkout)
		}
	}
	return working, operator, release
}

// TestTheBareOverviewListsALiveRunItsCheckoutHoldsNothingOf: the machine's
// run registry, not the checkout's working tree, is what names a LIVE local
// run (tick 9ss). A run commits its durable state by plumbing to
// epic/<id> and never touches a working tree, so the checkout the bare
// `ticfac` runs in — the operator's, on main — may hold nothing of it: no
// run directory, no records, no pidfile. The listing must enumerate the
// registry's runs, answer each one IN THE REPO ITS REGISTRATION NAMES, and
// so read the run RUNNING — never missing, and never dead from a probe
// taken in a checkout that was never the run's.
func TestTheBareOverviewListsALiveRunItsCheckoutHoldsNothingOf(t *testing.T) {
	now := time.Now()
	// No factory on this machine's home: the half this test is about is
	// the local one, and the operator's real factory must never be asked
	// from a test.
	t.Setenv("HOME", t.TempDir())
	ownRegistry(t)
	working, operator, release := overviewLiveRunFixture(t, now)
	defer release()
	fakeOverviewGraph(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", operator}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	out := stdout.String()

	// The live run IS listed — from the operator's checkout that holds
	// nothing of it. Before the fix this was the defect's first half: the
	// listing read only <cwd>/.ticfac/runs, which the run never writes.
	line := lineOf(out, "epic-lve")
	if line == "" {
		t.Fatalf("the live run is not listed from the operator's checkout:\n%s", out)
	}

	// And it reads RUNNING — the registered checkout's own probe, the
	// defect's second half: before the fix, a probe with repo=cwd answered
	// not_running in a checkout that was never the run's.
	if !strings.Contains(line, "running") {
		t.Errorf("the live run's line reads %q, want running", line)
	}
	if strings.Contains(line, "held") || strings.Contains(line, "clear with:") {
		t.Errorf("a live run in another checkout claims a person's attention: %q", line)
	}

	// --json says the same: the entry's model is the REGISTERED checkout's
	// — alive there, its records read from the epic/<id> branch the run
	// plumbs to, never degraded to the empty checkout the command ran in.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", operator, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	var live *overviewRun
	for i := range doc.Runs {
		if doc.Runs[i].RunID == "epic-lve" {
			live = &doc.Runs[i]
		}
	}
	if live == nil {
		t.Fatalf("the live run is not in the overview JSON:\n%s", stdout.String())
	}
	if live.State != overviewStateRunning || !live.Model.Liveness.Alive {
		t.Errorf("the live run's entry reads state %q alive %v: the probe must be taken in the registered repo %s",
			live.State, live.Model.Liveness.Alive, working)
	}
	if live.EpicID != "lve" || live.Host != statusmodel.HostLocal {
		t.Errorf("the live run's entry reads epic %q host %q", live.EpicID, live.Host)
	}
	for _, name := range live.Model.Degraded {
		if name == "run-state" {
			t.Errorf("the live run's model could not read the run's records: its state is on epic/lve, and the model must read it from there — degraded %v", live.Model.Degraded)
		}
	}
}

// TestTheBareOverviewDoesNotReadThisRepoRecordsForAnotherProjectsRun: a
// factory run of ANOTHER project is listed — the glance is the factory's —
// but this checkout's records, tracker and PR are never read for it (tick
// nyi). Before the fix the row for another project's run for an epic id
// this repo also holds read THIS repo's records: a foreign run rendered
// "held for a person" by this repo's own untriaged finding, with a settle
// command addressed at an epic the foreign project's run never touched.
func TestTheBareOverviewDoesNotReadThisRepoRecordsForAnotherProjectsRun(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	foreign := cloudRunIDOf("e777")
	now := time.Now()

	// This repo's own epic1 records — the exact shape the misattribution
	// reads: a failed checkpoint and an untriaged finding beside it. A
	// checkout of THIS project would (and should) render its own run held;
	// the foreign factory run for the same epic id must not borrow that.
	runDir := filepath.Join(repo, ".ticfac", "runs", "epic-epic1")
	if err := os.MkdirAll(filepath.Join(runDir, "findings"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := json.MarshalIndent(map[string]any{
		"schema_version": 3, "run_id": "epic-epic1", "epic_id": "epic1",
		"sequence": 1, "state": "failed", "reason": "attempt 1 of t1 was struck out",
		"updated_at": now.Add(-time.Hour).UTC().Format(time.RFC3339),
		"ticks":      []any{map[string]any{"tick_id": "t1", "state": "dispatched", "attempt": 1}},
		"provenance": map[string]any{
			"run_id": "epic-epic1", "tick_id": "", "attempt": 0,
			"source_ref": "refs/heads/epic/epic1", "source_sha": "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			"integration_ref": nil, "phase": "worker", "executor": "local-subprocess",
			"workspace_id": nil, "backend": nil, "substrate_protocol": nil, "substrate_server_version": nil,
			"role": "implement-tick", "tier": "strong", "profile_digest": nil,
			"model": "@cf/zai-org/glm-5.3", "context_manifest_digest": nil,
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal the checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "checkpoint.json"), checkpoint, 0o644); err != nil {
		t.Fatal(err)
	}
	finding, err := json.MarshalIndent(testDraftFinding("f00dc0de", ""), "", "  ")
	if err != nil {
		t.Fatalf("marshal the finding: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "findings", "f00dc0de.json"), finding, 0o644); err != nil {
		t.Fatal(err)
	}

	// The factory: another project's run for the same epic id, failed. Its
	// feed carries nothing but its own terminal word, so the row has
	// nothing to say but the record's — which is all it should say.
	feedText := strings.Join([]string{
		`{"schema_version":1,"at":"` + now.Add(-30*time.Minute).UTC().Format(time.RFC3339) +
			`","run_id":"` + foreign + `","tick_id":null,"attempt":null,"stage":"run_finished","detail":"failed: the instance was stopped"}`,
		"",
	}, "\n")
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": foreign, "epic": "epic1", "project": "other/repo", "state": "failed",
			}}}
		case request.Path == "/api/runs/"+foreign:
			return 200, map[string]any{"run": map[string]any{
				"run_id": foreign, "epic": "epic1", "project": "other/repo", "state": "failed",
			}}
		case request.Path == "/api/runs/"+foreign+"/events":
			return 200, map[string]any{
				"run_id": foreign, "state": "failed",
				"text": feedText, "bytes": len(feedText), "total_bytes": len(feedText),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	fakeOverviewGraph(t)
	ownRegistry(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			exitSuccess, code, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	var row *overviewRun
	for i := range doc.Runs {
		if doc.Runs[i].RunID == foreign {
			row = &doc.Runs[i]
		}
	}
	if row == nil {
		t.Fatalf("the foreign cloud run is not listed:\n%s", stdout.String())
	}
	if row.State != overviewStateFailed {
		t.Errorf("the foreign run's entry reads state %q, want failed: it must never borrow this repo's records", row.State)
	}
	if row.Project != "other/repo" {
		t.Errorf("the foreign run's entry carries project %q, want other/repo — a row a reader must be able to tell from this checkout's own", row.Project)
	}
	if row.ClearWith != nil {
		t.Errorf("the foreign run's entry names the command %q — a command this checkout cannot run for another project's run", *row.ClearWith)
	}
	for _, kind := range []string{statusmodel.WaitFinding, statusmodel.WaitHeldForPerson} {
		if row.Model.WaitsOn != nil && row.Model.WaitsOn.Kind == kind {
			t.Errorf("the foreign run's model waits on %s — this repo's own attention, read for a run whose records live in another project: %+v", kind, row.Model.WaitsOn)
		}
	}
	for _, a := range row.Model.Attention {
		if a.NeedsPerson {
			t.Errorf("the foreign run claims a person's attention from this repo's records: %+v", a)
		}
	}
}

// overviewHeadlineFixture writes one checkout that knows the two runs the
// dashboard headline is about (epic hn6, deliverable (c) — tick 3rc):
//
//   - epic-hd5 is running: five of its six ticks closed behind it and the
//     sixth dispatched by a live process — progress 5/6 and a healthy
//     verdict, the words the headline owes the glance;
//   - epic-hdh is held for a person: the run_held line names the tick and
//     the attempt, and the settle command is addressed by them.
//
// The names order alphabetically hd5 before hdh — the OPPOSITE of the
// attention-first order the listing owes — so the held run's block is found
// above the running one's, not by luck.
func overviewHeadlineFixture(t *testing.T, now time.Time) (repo string, release func()) {
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

	checkpoint := func(runID, epicID, state, reason, tickID, tickState string) {
		t.Helper()
		raw, err := json.MarshalIndent(map[string]any{
			"schema_version": 3, "run_id": runID, "epic_id": epicID,
			"sequence": 1, "state": state, "reason": reason,
			"updated_at": now.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			"ticks":      []any{map[string]any{"tick_id": tickID, "state": tickState, "attempt": 1}},
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
	one, two := 1, 2

	// epic-hd5: running, five of six ticks closed — the sixth is the one
	// the checkpoint names dispatched, and the tracker's graph is the seam
	// the graph fake below answers.
	checkpoint("epic-hd5", "hd5", "running", "t6 is dispatched", "t6", "dispatched")
	say("epic-hd5", runfeed.NewEvent(now.Add(-10*time.Minute), "epic-hd5", "t6", &one,
		reconcile.StageDispatched, "t6 try 1 dispatched"))
	life, err := runlife.Claim(repo, "epic-hd5")
	if err != nil {
		t.Fatalf("claim the running run: %v", err)
	}
	release = func() { life.Release("overview test") }

	// epic-hdh: held for a person.
	checkpoint("epic-hdh", "hdh", "failed", "attempt 2 of t1 was struck out: the report names no status", "t1", "dispatched")
	say("epic-hdh",
		runfeed.NewEvent(now.Add(-40*time.Minute), "epic-hdh", "t1", &two,
			reconcile.StageRunHeld, "attempt_struck_out: the report names no status"),
		runfeed.NewEvent(now.Add(-30*time.Minute), "epic-hdh", "", nil,
			reconcile.StageRunFinished, "failed: attempt 2 of t1 was struck out: the report names no status"))

	return repo, release
}

// headlineGraph swaps the tracker's seams for the two the headline tests
// need: an epic of six ticks, five of them closed — progress 5/6 — and an
// epic-status seam that leaves every epic open, so no run reads history.
func headlineGraph(t *testing.T) {
	t.Helper()
	tasks := make([]tk.GraphTask, 0, 6)
	for i := 1; i <= 6; i++ {
		status := "closed"
		if i == 6 {
			status = "open"
		}
		tasks = append(tasks, tk.GraphTask{ID: fmt.Sprintf("t%d", i), Status: status})
	}
	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph {
		return &tk.Graph{Waves: []tk.GraphWave{{Wave: 1, Tasks: tasks}}}
	}
	realClosed := overviewEpicClosed
	t.Cleanup(func() { overviewEpicClosed = realClosed })
	overviewEpicClosed = func(context.Context, string, string) (bool, bool) { return false, true }
}

// overviewBlock returns one run's row as the listing draws it: the first
// line, and the lines the row carries under it — the dashboard headline a
// gathered model renders (tick 3rc). Empty when the run is not listed.
func overviewBlock(out, runID string) (first string, block []string) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, runID+":") && !strings.HasPrefix(line, runID+" ") {
			continue
		}
		first = line
		for _, under := range lines[i+1:] {
			if !strings.HasPrefix(under, "  ") {
				break
			}
			block = append(block, under)
		}
		return first, block
	}
	return "", nil
}

// TestTheBareOverviewShowsTheDashboardHeadline: every row the bare overview
// lists with a gathered model carries the dashboard's own headline under its
// unchanged first line (epic hn6, deliverable (c) — tick 3rc). A person
// glancing at the listing sees the same progress, health verdict and
// needs-you that `ticfac watch` shows for that run — and a held run keeps
// its "clear with:" suffix on the first line while the headline names the
// same command again.
func TestTheBareOverviewShowsTheDashboardHeadline(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir()) // no factory: the headline is a local question
	ownRegistry(t)
	repo, release := overviewHeadlineFixture(t, now)
	defer release()
	headlineGraph(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	out := stdout.String()

	// The running run: its first line exactly as it ever was — the state
	// word and the reason, no progress in it — and the dashboard's progress
	// bar and health verdict under it.
	first, block := overviewBlock(out, "epic-hd5")
	if first == "" {
		t.Fatalf("the running run is not listed:\n%s", out)
	}
	if !strings.Contains(first, "running") || strings.Contains(first, "5/6") {
		t.Errorf("the running run's first line changed shape: %q", first)
	}
	joined := strings.Join(block, "\n")
	if !strings.Contains(joined, "5/6 ticks") {
		t.Errorf("the running run's headline does not carry the progress bar's count:\n%s", joined)
	}
	if !strings.Contains(joined, "● healthy") {
		t.Errorf("the running run's headline does not carry the health verdict:\n%s", joined)
	}

	// The held run: the needs-you command in the headline, and the
	// "clear with:" suffix still on the first line.
	first, block = overviewBlock(out, "epic-hdh")
	if first == "" {
		t.Fatalf("the held run is not listed:\n%s", out)
	}
	if !strings.Contains(first, `clear with: ticfac settle hdh t1 2 --release "<who>"`) {
		t.Errorf("the held run's first line lost its clearing command: %q", first)
	}
	joined = strings.Join(block, "\n")
	if !strings.Contains(joined, "needs you:") {
		t.Errorf("the held run's headline does not say what needs a person:\n%s", joined)
	}
	if !strings.Contains(joined, `ticfac settle hdh t1 2 --release "<who>"`) {
		t.Errorf("the held run's headline does not carry the unblocking command:\n%s", joined)
	}
}

// TestTheBareOverviewHeadlineMatchesWatch: the one-model-two-renderers
// guarantee. For the same status model, the overview's headline lines are
// dashboardHeadline's own at the width the two-space indent leaves them,
// indented two spaces — the progress and verdict line, the phase bar with
// the needs-you answer, and every hold's own line after it. The overview
// and the watch render with the same functions, so they cannot drift
// apart; and because the headline is made at the width the indent leaves,
// no indented line can run past the width it was laid out at (tick kce).
func TestTheBareOverviewHeadlineMatchesWatch(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir())
	ownRegistry(t)
	repo, release := overviewHeadlineFixture(t, now)
	defer release()
	headlineGraph(t)

	// The models the listing renders: --json carries one per run.
	var jsonOut, jsonErr bytes.Buffer
	if code := Run([]string{"--repo", repo, "--json"}, &jsonOut, &jsonErr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d, want %d:\n%s\n%s",
			code, exitSuccess, jsonOut.String(), jsonErr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(jsonOut.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, jsonOut.String())
	}
	models := map[string]statusmodel.Model{}
	for _, run := range doc.Runs {
		models[run.RunID] = run.Model
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	out := stdout.String()

	for _, runID := range []string{"epic-hd5", "epic-hdh"} {
		model, ok := models[runID]
		if !ok {
			t.Fatalf("%s is not in the overview JSON:\n%s", runID, jsonOut.String())
		}
		_, block := overviewBlock(out, runID)
		// The watch's own lines for the same model, at the width the
		// non-TTY stdout lays out at less the two-space indent the row
		// carries — the headline, then the attention lines the watch
		// shows under it, wrapped to the same width the indent leaves them.
		want := dashboardHeadline(model, plainStyles(), overviewHeadlineFallbackWidth-2)
		want = append(want, dashboardAttentionLines(model, plainStyles(), overviewHeadlineFallbackWidth-2)...)
		if len(block) != len(want)-1 {
			t.Fatalf("%s's row carries %d headline lines, want the watch's %d:\n got %q\nwant %q",
				runID, len(block), len(want)-1, block, want[1:])
		}
		for i, line := range want[1:] {
			if block[i] != "  "+line {
				t.Errorf("%s's headline line %d is not the watch's own, indented:\n got %q\nwant %q",
					runID, i, block[i], "  "+line)
			}
		}
	}
}

// TestTheBareOverviewHeadlineFitsTheTerminal: the overview indents the
// watch's own headline lines by two spaces, so it must lay them out at the
// width that indent leaves (tick kce). The phase line pads to exactly the
// width it is given — laid out at the terminal's full width and then
// indented, it overflows by the two indent cells and "needs you" wraps
// onto the next row. Every indented headline line of the running run — the
// one whose headline ends in "needs you: nothing" — fits the width it was
// laid out at, at the fallback width and at a terminal's own.
func TestTheBareOverviewHeadlineFitsTheTerminal(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir())
	ownRegistry(t)
	repo, release := overviewHeadlineFixture(t, now)
	defer release()
	headlineGraph(t)

	// A pipe, then a terminal 100 columns wide — the fallback width and
	// the same width spoken by a terminal, each a width every indented
	// headline line must fit inside.
	check := func(t *testing.T, out string, width int) {
		t.Helper()
		_, block := overviewBlock(out, "epic-hd5")
		if len(block) == 0 {
			t.Fatalf("epic-hd5's headline is not under its row:\n%s", out)
		}
		for _, line := range block {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("epic-hd5's headline line is %d cells wide, over the %d it must fit:\n%q",
					w, width, line)
			}
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	check(t, stdout.String(), overviewHeadlineFallbackWidth)

	realTTY, realSize := watchIsTerminal, watchTerminalSize
	t.Cleanup(func() { watchIsTerminal, watchTerminalSize = realTTY, realSize })
	watchIsTerminal = func(io.Writer) bool { return true }
	watchTerminalSize = func(io.Writer) (int, int, bool) { return overviewHeadlineFallbackWidth, 24, true }
	stdout.Reset()
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	check(t, stdout.String(), overviewHeadlineFallbackWidth)
}

// TestTheBareOverviewHeadlineStylesAndWidth: the headline renders with the
// terminal's own styles and width when stdout is one — the same seams the
// watch's live view reads — and with the identity set at the fallback width
// when it is not, so a piped listing never carries escape codes (tick 3rc).
func TestTheBareOverviewHeadlineStylesAndWidth(t *testing.T) {
	now := time.Now()
	t.Setenv("HOME", t.TempDir())
	ownRegistry(t)
	repo, release := overviewHeadlineFixture(t, now)
	defer release()
	headlineGraph(t)

	// A terminal 40 columns wide: the progress bar cannot seat itself beside
	// its answers, and the verdict keeps its colour.
	realTTY, realSize := watchIsTerminal, watchTerminalSize
	t.Cleanup(func() { watchIsTerminal, watchTerminalSize = realTTY, realSize })
	watchIsTerminal = func(io.Writer) bool { return true }
	watchTerminalSize = func(io.Writer) (int, int, bool) { return 40, 24, true }

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	_, block := overviewBlock(stdout.String(), "epic-hd5")
	joined := strings.Join(block, "\n")
	if strings.Contains(joined, "█") {
		t.Errorf("a 40-column terminal still gets the progress bar it cannot seat:\n%s", joined)
	}
	if !strings.Contains(joined, "\x1b[32m● healthy\x1b[0m") {
		t.Errorf("a terminal's headline is not in the verdict's own colour:\n%s", joined)
	}

	// Not a terminal — a pipe, a log: the identity set at the fallback
	// width, where the bar fits, and no escape codes anywhere.
	watchIsTerminal, watchTerminalSize = realTTY, realSize
	stdout.Reset()
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d, want %d:\n%s\n%s",
			code, exitSuccess, stdout.String(), stderr.String())
	}
	_, block = overviewBlock(stdout.String(), "epic-hd5")
	joined = strings.Join(block, "\n")
	if !strings.Contains(joined, "█") {
		t.Errorf("the fallback width drops the progress bar it can seat:\n%s", joined)
	}
	if strings.Contains(joined, "\x1b[") {
		t.Errorf("a piped listing carries escape codes:\n%s", joined)
	}
}

// TestTheOverviewClassesAStoppedRunByItsOwnDurableWord (tick c65): the
// contract's dashboard_stopped golden is a run that died mid-waves — its
// lifecycle phase still names no end — and the overview's terminal read is
// the anchor the phone page's classifier (cloudflare/src/status.ts,
// classifyStatusDoc, the port of this classification) must reproduce: the
// liveness answer's own state IS the run's durable terminal word, so the
// row says cancelled, never done, and the reason is the run's own last word
// from its feed. Without the golden pinned on this side, the phone's chip
// reading "done" for the same model had nothing here to disagree with.
func TestTheOverviewClassesAStoppedRunByItsOwnDurableWord(t *testing.T) {
	stopped := statusModelGoldens(t)["dashboard_stopped"]

	row := overviewEntryOf(stopped, true)
	if row.State != overviewStateCancelled {
		t.Errorf("the stopped golden classes %q, want %q — a run that died mid-waves is never done",
			row.State, overviewStateCancelled)
	}
	if row.Reason != "the close-out waits for CI green on the PR" {
		t.Errorf("the stopped golden's reason is %q, want the run's own last word from its feed", row.Reason)
	}
	if row.ClearWith != nil {
		t.Errorf("a cancelled run names no clearing command; got %q", *row.ClearWith)
	}

	// The golden with its durable word swapped for the other two: the
	// completed word is the only one that reads done, and the failed one
	// carries the resume named by the run's host.
	completed := stopped
	completed.Liveness.State = "completed"
	if row := overviewEntryOf(completed, true); row.State != overviewStateDone {
		t.Errorf("the completed word classes %q, want done", row.State)
	}
	failed := stopped
	failed.Liveness.State = "failed"
	failed.Host = statusmodel.HostCloud
	failedRow := overviewEntryOf(failed, true)
	if failedRow.State != overviewStateFailed {
		t.Errorf("the failed word classes %q, want failed", failedRow.State)
	}
	if failedRow.ClearWith == nil || *failedRow.ClearWith != "ticfac run 6in --cloud" {
		t.Errorf("a failed cloud run's resume is the cloud submission; got %v", failedRow.ClearWith)
	}
}
