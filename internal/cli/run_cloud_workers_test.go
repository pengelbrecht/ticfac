package cli

// `ticfac run <epic> --cloud-workers`: local orchestrator, cloud workers.
// The wiring, against the same fake factory the `--cloud` tests use: the run
// is recorded as locally orchestrated, the credential is collected for this
// machine, and the background run-epic is started on the cloud profile set
// with everything a container orchestrator's boot would have exported —
// without the operator exporting anything, and without the credential in the
// argv start.log records.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// cloudWorkersSpawn records what --cloud-workers started and claims the run's
// life under the run id the argv names, as a real run-epic does.
type cloudWorkersSpawn struct {
	argv []string
	env  []string
}

func saveCloudWorkersSeams(t *testing.T) *cloudWorkersSpawn {
	t.Helper()
	saveRunSeams(t)
	savedStart := runStartCloudWorkers
	t.Cleanup(func() { runStartCloudWorkers = savedStart })
	rec := &cloudWorkersSpawn{}
	runStartCloudWorkers = func(argv, env []string, out io.Writer) (runChild, error) {
		rec.argv, rec.env = argv, env
		repo, runID := "", ""
		for i := 1; i+1 < len(argv); i++ {
			switch argv[i] {
			case "--repo":
				repo = argv[i+1]
			case "--run-id":
				runID = argv[i+1]
			}
		}
		life, err := runlife.Claim(repo, runID)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { life.Release("the test's fake claim") })
		return &fakeChild{pid: os.Getpid()}, nil
	}
	attach := &attachRecorder{}
	runAttach = attach.seam
	return rec
}

func runCloudWorkers(t *testing.T, repo string, extra ...string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append([]string{"--cloud-workers", "--repo", repo}, extra...)
	args = append(args, "epic1")
	code := runBody(context.Background(), t, args, &stdout, &stderr)
	return code, &stdout, &stderr
}

func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == name {
			return value, true
		}
	}
	return "", false
}

// short: a fake factory and the spawn/attach seams; git runs against a
// temp repo, as every `run --cloud` test does.
func TestRunCloudWorkersRecordsTheRunCollectsItsCredentialAndStartsHere(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	started := cloudRunIDOf("cc33")
	const token = "tkr_run-credential"

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": started, "state": "starting"}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+started+"/orchestrator":
			return http.StatusCreated, map[string]any{
				"run_id": started, "project": "acme/project", "epic": "epic1", "state": "starting",
				"token": token, "factory_max_instances": 3,
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	spawn := saveCloudWorkersSeams(t)

	code, stdout, stderr := runCloudWorkers(t, repo)
	if code != exitSuccess {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}

	// The submission says where the orchestrator runs, so the factory boots
	// none: exactly one submission, carrying orchestrator=local.
	var submissions int
	for _, request := range *requests {
		if request.Method == http.MethodPost && request.Path == cloudIndexPath {
			submissions++
			if got := request.Body["orchestrator"]; got != "local" {
				t.Errorf("the submission's orchestrator is %#v, want \"local\"", got)
			}
		}
	}
	if submissions != 1 {
		t.Fatalf("%d submissions, want 1", submissions)
	}

	// The child: run-epic under the FACTORY's run id, on the cloud set
	// embedded in this binary.
	want := []string{"run-epic", "epic1", "--repo", repo, "--run-id", started, "--profiles", profile.EmbeddedCloud}
	if strings.Join(spawn.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("started %q, want %q", spawn.argv, want)
	}
	// Everything a container orchestrator's boot exports, set by the command.
	for name, want := range map[string]string{
		"TICKS_FACTORY_URL":           endpoint,
		"TICKS_FACTORY_TOKEN":         token,
		"TICKS_FACTORY_PROJECT":       "acme/project",
		"TICKS_RUN_ID":                started,
		"TICKS_SUBSTRATE":             "cloud",
		"TICKS_ORCHESTRATOR":          "local",
		"TICKS_FACTORY_MAX_INSTANCES": "3",
	} {
		if got, ok := envValue(spawn.env, name); !ok || got != want {
			t.Errorf("the child's %s is %q (set: %v), want %q", name, got, ok, want)
		}
	}
	// The credential rides the environment, never the argv start.log keeps.
	startLog, err := os.ReadFile(filepath.Join(runlife.Dir(repo, started), startLogName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(startLog), token) || strings.Contains(stdout.String(), token) {
		t.Fatalf("the run credential was written where it can be read back:\n%s\n%s", startLog, stdout.String())
	}
	if !strings.Contains(stdout.String(), "every worker runs in the factory") {
		t.Errorf("stdout does not say where the workers run:\n%s", stdout.String())
	}
}

// short: a fake factory and the spawn/attach seams.
func TestRunCloudWorkersRestartsTheOrchestratorOfALiveRunWhoseProcessDied(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	live := cloudRunIDOf("dd44")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": live, "epic": "epic1", "project": "acme/project", "state": "running",
				"started_at": "2026-09-30T10:00:00Z",
			}}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+live+"/orchestrator":
			return http.StatusCreated, map[string]any{
				"run_id": live, "project": "acme/project", "epic": "epic1", "state": "running",
				"token": "tkr_again", "factory_max_instances": 3,
			}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			t.Error("a live run was submitted again instead of being re-credentialled")
			return 409, map[string]any{"error": "lease_held"}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	spawn := saveCloudWorkersSeams(t)

	code, stdout, stderr := runCloudWorkers(t, repo)
	if code != exitSuccess {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if got, _ := envValue(spawn.env, "TICKS_RUN_ID"); got != live {
		t.Fatalf("the restarted orchestrator runs as %q, want the live run %s", got, live)
	}
	if !strings.Contains(stdout.String(), "restarting it on this machine") {
		t.Errorf("stdout does not say the orchestrator is restarted:\n%s", stdout.String())
	}
	_ = requests
}

// A run the factory finalized as failed (hn6's run_6d88: its supervisor
// halted) is not reopened — its Workflow instance, credentials, workers and
// lease ended with it — so the command submits a new run and says why the
// run id changes, naming the old run's real end.
//
// short: a fake factory and the spawn/attach seams.
func TestRunCloudWorkersResumesAFailedRunAsANewRunAndSaysWhy(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	failed := cloudRunIDOf("ee55")
	resumed := cloudRunIDOf("ff66")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": failed, "epic": "epic1", "project": "acme/project", "state": "failed",
				"started_at": "2026-09-30T10:00:00Z",
			}}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+failed+"/orchestrator":
			t.Error("a finished run was re-credentialled; the factory refuses that, and it is not asked")
			return 409, map[string]any{"error": "run_not_active"}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{"run": map[string]any{"run_id": resumed, "state": "starting"}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+resumed+"/orchestrator":
			return http.StatusCreated, map[string]any{
				"run_id": resumed, "project": "acme/project", "epic": "epic1", "state": "starting",
				"token": "tkr_new", "factory_max_instances": 3,
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	spawn := saveCloudWorkersSeams(t)

	code, stdout, stderr := runCloudWorkers(t, repo)
	if code != exitSuccess {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if got, _ := envValue(spawn.env, "TICKS_RUN_ID"); got != resumed {
		t.Fatalf("the orchestrator runs as %q, want the new run %s", got, resumed)
	}
	out := stdout.String()
	for _, want := range []string{"run " + failed + " is not running", "failed", "is not reopened", "as a new run"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not say %q:\n%s", want, out)
		}
	}
}

// short: a fake factory; nothing is started.
func TestRunCloudWorkersRefusesALiveContainerOrchestratedRun(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	live := cloudRunIDOf("ee55")

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": live, "epic": "epic1", "project": "acme/project", "state": "running",
				"started_at": "2026-09-30T10:00:00Z",
			}}}
		case request.Method == http.MethodPost && request.Path == "/api/runs/"+live+"/orchestrator":
			return http.StatusConflict, map[string]any{"error": "not_local_orchestrator", "detail": "container"}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	spawn := saveCloudWorkersSeams(t)

	code, stdout, stderr := runCloudWorkers(t, repo)
	if code == exitSuccess {
		t.Fatalf("a live container-orchestrated run was driven from here too:\n%s", stdout.String())
	}
	if spawn.argv != nil {
		t.Fatalf("an orchestrator was started beside the container's: %q", spawn.argv)
	}
	if !strings.Contains(stderr.String(), "--cloud` attaches to it") {
		t.Errorf("the refusal does not name the way to the live run:\n%s", stderr.String())
	}
}

// short: flag parsing only.
func TestRunCloudWorkersRefusesFlagsThatDoNotApply(t *testing.T) {
	for _, args := range [][]string{
		{"--cloud-workers", "--cloud", "epic1"},
		{"--cloud-workers", "--profiles", "herdr", "epic1"},
		{"--cloud-workers", "--no-herdr", "epic1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runBody(context.Background(), t, args, &stdout, &stderr); code != 2 {
			t.Errorf("%q exited %d, want 2: %s", args, code, stderr.String())
		}
	}
}
