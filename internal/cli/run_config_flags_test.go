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

// TestRunRefusesTheConfigOnACloudSubmission: the cloud submission record
// carries no --config (tick tda leaves the factory's submission surface to
// its own tick), and the command refuses the flag rather than silently
// dropping it — naming what a submitted run does select by (the epic's own
// label, the declared default) and where the flag DOES apply
// (--cloud-workers, whose orchestrator is this machine).
//
// short: a refusal before any factory is asked; no seams needed beyond
// parseOnly's own.
func TestRunRefusesTheConfigOnACloudSubmission(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()

	var stdout, stderr syncBuffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "--cloud", "--config", "claude", "foo"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit %d, want the usage class %d: %s%s", code, exitUsage, stdout.String(), stderr.String())
	}
	joined := stdout.String() + stderr.String()
	for _, want := range []string{
		"--config does not apply to a --cloud run",
		"config: label",
		"--cloud-workers",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not name %q: %s", want, joined)
		}
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
