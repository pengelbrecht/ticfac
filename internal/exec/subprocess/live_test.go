package subprocess

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The one live test: a REAL runner, opt in.
//
// Everything else in this package drives a fake runner, which is right — the
// executor is what is under test, and an agent in the loop would make every
// one of those tests non-deterministic without making any of them stronger.
// But the argv table is a claim about three programs this repository does not
// own, and a claim nothing has ever executed is a guess. So exactly one test
// runs a real agent, and it is opt-in because it costs money, needs a
// credential and takes minutes:
//
//	TICFAC_LIVE_RUNNER=claude go test ./internal/exec/subprocess -run TestLive -v
//
// TICFAC_LIVE_MODEL names a model to launch it on, which is the only way the
// model flag in the runner table gets executed rather than merely asserted:
// the flag is this repository's claim about three programs it does not own.
//
// It asks for the smallest thing that exercises the whole contract: read the
// tick record, make one commit, write the report at the absolute path this
// executor owns.
func TestLiveRunnerCompletesARealTick(t *testing.T) {
	runner := os.Getenv("TICFAC_LIVE_RUNNER")
	if runner == "" {
		t.Skip("set TICFAC_LIVE_RUNNER=claude|codex|pi to run one job through a real agent")
	}
	if testing.Short() {
		t.Skip("short mode: the live test spends real money")
	}
	if _, ok := runners[runner]; !ok {
		t.Fatalf("TICFAC_LIVE_RUNNER=%s is not one of %s", runner, strings.Join(KnownRunners(), ", "))
	}
	if _, err := exec.LookPath(runner); err != nil {
		t.Skipf("%s is not on PATH: %v", runner, err)
	}

	repo := newRepo(t, "live")
	writeTickRecord(t, repo, "lv1", "Create a file hello.txt containing exactly the word hello, and commit it.")
	state := filepath.Join(t.TempDir(), "state")

	executor, err := New(Options{
		Repo:           repo.Dir,
		StateDir:       state,
		Runner:         runner,
		Model:          os.Getenv("TICFAC_LIVE_MODEL"),
		SupervisorArgv: []string{executorBin, "supervise"},
		PushInterval:   30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := &JobSpec{
		SchemaVersion: SchemaVersion,
		JobID:         "run-live/tick-lv1/attempt-1",
		Role:          "implement-tick",
		Source: Source{
			Repository: repo.Origin,
			BaseSHA:    repo.Base,
			WriteRef:   "refs/heads/tick/lv1",
		},
		Capabilities:   Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []Input{{Kind: "tick", ID: "lv1"}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/run-live/",
		Credentials: Credentials{
			Model:  ModelCredential{Shorthand: "issued-by-host"},
			Source: SourceCredential{Shorthand: "write"},
		},
		Limits: Limits{WallSeconds: 900},
	}

	handle, err := executor.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := handle.Local()
	t.Cleanup(func() {
		st := newStore(local.State)
		for _, pid := range []int{st.runnerPID(), st.supervisorPID()} {
			if pid > 0 && processAlive(pid) {
				_ = signalGroup(pid, sigKill())
			}
		}
	})

	st := newStore(local.State)
	waitFor(t, "the live runner to settle", 15*time.Minute, st.settled)

	collected, err := executor.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(st.path(fileRunnerLog))
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s (%s)\nreport: %+v\nrunner log:\n%s",
			collected.Verdict, collected.Message, collected.Report, log)
	}
	if collected.Result.Source.Commits == 0 {
		t.Errorf("the live runner reported %s and committed nothing", collected.Report.Status)
	}
	t.Logf("%s: %s — %s", runner, collected.Report.Status, collected.Report.Detail)
}

func writeTickRecord(t *testing.T, repo *testRepo, tick, description string) {
	t.Helper()
	dir := filepath.Join(repo.Dir, ".tick", "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := `{
  "id": "` + tick + `",
  "title": "` + description + `",
  "status": "open",
  "priority": 2,
  "type": "task",
  "owner": "ticfac",
  "created_by": "ticfac@example.com",
  "created_at": "2026-09-02T00:00:00Z",
  "updated_at": "2026-09-02T00:00:00Z"
}
`
	if err := os.WriteFile(filepath.Join(dir, tick+".json"), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the tick the live test runs")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
	repo.Base = strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
}
