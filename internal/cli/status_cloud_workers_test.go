package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// cloudWorkersFactory is a factory that holds runID for epic1 as RUNNING —
// the record a `ticfac run --cloud-workers` run has while its Workflow
// supervises this machine's heartbeat — and serves no feed of its own.
func cloudWorkersFactory(t *testing.T, runID string) {
	t.Helper()
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": runID, "epic": "epic1", "project": "acme/project", "state": "running",
			}}}
		case request.Path == "/api/runs/"+runID:
			return 200, map[string]any{"run": map[string]any{
				"run_id": runID, "epic": "epic1", "project": "acme/project", "state": "running",
			}}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
}

// TestStatusByEpicIDAnswersACloudWorkersRunFromItsLocalProcess: `ticfac
// status <epic>` for a run `ticfac run <epic> --cloud-workers` started (#151)
// — the factory's run id, orchestrated on THIS machine — answers from the
// local process: its pid is the liveness, its feed here is the last event,
// and the factory is named as the host of its workers. Before the fix the
// epic resolved to the factory's run and was answered by the Workflow alone
// ("the supervisor is queued and has not started executing yet") while the
// orchestrator was dispatching here.
func TestStatusByEpicIDAnswersACloudWorkersRunFromItsLocalProcess(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	runID := cloudRunIDOf("c0c0")
	cloudWorkersFactory(t, runID)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	attempt := 3
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "ltg", &attempt,
		"dispatched", "ltg try 1 dispatched (run dispatch #3)"))

	var stdout, stderr syncBuffer
	code := Run([]string{"status", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d for a live local orchestrator, want 0:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "run "+runID+": alive") {
		t.Errorf("status does not answer for the run by its local process:\n%s", out)
	}
	if !strings.Contains(out, "ltg try 1 dispatched") {
		t.Errorf("status does not read the local feed's last event:\n%s", out)
	}
	if !strings.Contains(out, "factory (hosts its workers") {
		t.Errorf("status does not name the factory as the workers' host:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "orchestrated on this machine") {
		t.Errorf("stderr does not say the run is orchestrated here:\n%s", stderr.String())
	}
}

// TestStatusOfACloudWorkersRunWhoseLocalProcessDiedIsNotAlive: the factory's
// record still says running (it ends the run only once the heartbeat bound
// passes), but the orchestrator on this machine is gone — which is the run's
// liveness. Before the fix the run's id alone sent status to the Workflow,
// and it answered alive, exit 0, for a run nothing was driving.
func TestStatusOfACloudWorkersRunWhoseLocalProcessDiedIsNotAlive(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	runID := cloudRunIDOf("c0c1")
	cloudWorkersFactory(t, runID)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	life.Release("test") // the orchestrator exited; its run.log stays

	for _, arg := range []string{runID, "epic-epic1"} {
		var stdout, stderr syncBuffer
		code := Run([]string{"status", "--repo", repo, arg}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("status %s: exit %d for an orchestrator that is gone, want 1:\n%s\n%s",
				arg, code, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "run "+runID+": alive") {
			t.Errorf("status %s answers alive from the factory's record for a run whose orchestrator here is gone:\n%s",
				arg, stdout.String())
		}
		if !strings.Contains(stdout.String(), "factory (hosts its workers") {
			t.Errorf("status %s does not give the factory's view beside the local answer:\n%s", arg, stdout.String())
		}
	}
}

// TestWatchByEpicIDFollowsACloudWorkersRunsLocalFeed: watch (and status
// --follow, which shares feedSource) follows the local orchestrator's own
// feed for a --cloud-workers run, never the factory's stream, which a
// locally orchestrated run does not write.
func TestWatchByEpicIDFollowsACloudWorkersRunsLocalFeed(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	runID := cloudRunIDOf("c0c2")
	cloudWorkersFactory(t, runID)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	life.Release("test")
	at := time.Now()
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(at, runID, "", nil,
		reconcile.StageRunFinished, "completed: every tick of epic1 is closed behind the integrated gate"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d watching the local feed, want 0:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the watch did not follow the local feed:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "orchestrated on this machine") {
		t.Errorf("stderr does not say the run is orchestrated here:\n%s", stderr.String())
	}
}
