package cli

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/profile"
)

// The `run` command's own --config surface (tick tda): the everyday command
// carries the operator's word for this one run to the run-epic it starts —
// and refuses it where it cannot be honoured, rather than silently dropping
// it (a flag that silently does nothing is a lie an everyday command must
// not tell).

// TestRunForwardsTheNamedConfigToTheBackgroundRun: the started run-epic is
// told the config the operator named, verbatim, after the flag — the child
// re-derives the same precedence (the flag over the epic's label over the
// default), and the flag is what makes this run's word win.
//
// short: the seams answer the herdr probe, the spawn and the attach; no real
// process is started.
func TestRunForwardsTheNamedConfigToTheBackgroundRun(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	herdrAnswers(t, "", errNoHerdrForTest())
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	attach := &attachRecorder{}
	runAttach = attach.seam

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "--config", "claude", "foo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if len(rec.argvs) != 1 {
		t.Fatalf("the spawn was asked %d times, want once", len(rec.argvs))
	}
	want := []string{"run-epic", "foo", "--repo", filepath.Join(repo), "--config", "claude"}
	if got := rec.argvs[0]; !equalArgv(got, want) {
		t.Errorf("the background run was started as %v, want %v — the named config rides the argv verbatim", got, want)
	}
	// And the starter says what it started: an operator reading start.log
	// can tell which config this run asked for.
	if !strings.Contains(stdout.String(), "run config: claude") {
		t.Errorf("the start says nothing about the named config: %q", stdout.String())
	}
}

// TestRunWithoutAConfigNameStartsThePlainArgv: no --config, no new argv —
// the child selects by the epic's label and the declared default, and the
// everyday command's existing behaviour is unchanged byte for byte.
//
// short: the seams answer the herdr probe, the spawn and the attach.
func TestRunWithoutAConfigNameStartsThePlainArgv(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	herdrAnswers(t, "", errNoHerdrForTest())
	rec := &spawnRecorder{}
	runStartDetached = claimingSpawn(t, rec)
	attach := &attachRecorder{}
	runAttach = attach.seam

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if len(rec.argvs) != 1 {
		t.Fatalf("the spawn was asked %d times, want once", len(rec.argvs))
	}
	want := []string{"run-epic", "foo", "--repo", filepath.Join(repo)}
	if got := rec.argvs[0]; !equalArgv(got, want) {
		t.Errorf("the background run was started as %v, want %v — no config flag, no argv it did not carry before", got, want)
	}
}

// TestRunCloudForwardsTheNamedConfig: the cloud submission record carries
// --config (tick ba4) as its own `config` field, so the factory's
// orchestrator container resolves the operator's named config rather than
// only the epic's own label and the declared default.
//
// short: a fake factory and the cloud attach seam; git runs against a temp
// repo, as every `run --cloud` test does.
func TestRunCloudForwardsTheNamedConfig(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	started := cloudRunIDOf("cc42")

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Method == http.MethodGet && request.Path == cloudIndexPath:
			return 200, map[string]any{"runs": []any{}}
		case request.Method == http.MethodPost && request.Path == cloudIndexPath:
			return http.StatusCreated, map[string]any{
				"run": map[string]any{"run_id": started, "state": "starting"},
			}
		}
		t.Errorf("unexpected factory request %s %s", request.Method, request.Path)
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	rec := recordCloudAttach(t)

	code, stdout, stderr := runRunCloud(t, repo, "epic1", "--config", "claude")
	if code != exitSuccess {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if len(rec.runIDs) != 1 || rec.runIDs[0] != started {
		t.Fatalf("attached to %v, want the run the factory started (%s)", rec.runIDs, started)
	}
	if !strings.Contains(stdout.String(), "run config: claude") {
		t.Errorf("the start says nothing about the named config: %q", stdout.String())
	}

	var post *cloudFactoryRequest
	for i := range *requests {
		if (*requests)[i].Method == http.MethodPost {
			post = &(*requests)[i]
		}
	}
	if post == nil {
		t.Fatal("the epic was never submitted to the factory")
	}
	if got := post.Body["config"]; got != "claude" {
		t.Errorf("the submission's config is %#v, want %q", got, "claude")
	}
}

// TestRunCloudWorkersForwardsTheNamedConfig: --cloud-workers keeps the
// orchestrator on this machine, so the flag the operator named reaches the
// run-epic it starts verbatim — the workers boot factory containers on the
// cells the selected config declares.
//
// short: a fake factory and the spawn/attach seams; git runs against a temp
// repo, as every `run --cloud-workers` test does.
func TestRunCloudWorkersForwardsTheNamedConfig(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	started := cloudRunIDOf("cc41")
	const token = "tkr_run-credential"

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
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
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	spawn := saveCloudWorkersSeams(t)

	code, stdout, stderr := runCloudWorkers(t, repo, "--config", "claude")
	if code != exitSuccess {
		t.Fatalf("exit %d: %s\n%s", code, stderr.String(), stdout.String())
	}
	if !containsArgPair(spawn.argv, "--config", "claude") {
		t.Errorf("the started argv %v carries no --config claude", spawn.argv)
	}
	// The cloud profile set is still the one the workers' containers need:
	// the named config changes the cells the dispatches resolve against,
	// never the profile set itself.
	if !containsArgPair(spawn.argv, "--profiles", profile.EmbeddedCloud) {
		t.Errorf("the started argv %v lost the embedded cloud profile set", spawn.argv)
	}
}

// --- the helpers this file adds -------------------------------------------

func errNoHerdrForTest() error {
	return &noHerdrError{}
}

type noHerdrError struct{}

func (noHerdrError) Error() string { return "no live herdr for the test" }

func containsArgPair(argv []string, flag, value string) bool {
	for i := 1; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
