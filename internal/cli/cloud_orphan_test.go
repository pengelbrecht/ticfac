package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A cloud run whose record claims it is still going — starting, running,
// stopping — while Cloudflare has NO Workflow instance for it is dead: the
// record is written by that instance, so with no instance nothing will ever
// write it again. The factory creates the instance in the same request that
// records the run (and deletes the record when creation fails), so a record
// without an instance was never booted or has outlived Cloudflare's
// retention. On the operator's machine three such runs read "running — in
// phase plan" in the bare `ticfac`, and `ticfac status` called them alive.
//
// Only Cloudflare's own "no such instance" answer is taken as that verdict:
// an instance that merely could not be ASKED (no credentials, a 5xx) leaves
// the record's claim standing, as before.
func TestACloudRecordWithNoWorkflowInstanceIsOrphanedNotAlive(t *testing.T) {
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	status := http.StatusNotFound
	configureCloudflareWorkflows(t, func(*http.Request) (int, string) {
		return status, `{"success": false, "errors": [{"message": "instance.not_found"}], "result": null}`
	})

	liveness := cloudRunLiveness(context.Background(), "run_0rph", "stopping")
	if liveness.Alive {
		t.Errorf("a record with no Workflow instance reads alive: %+v", liveness)
	}
	if liveness.State != cloudLivenessOrphaned {
		t.Errorf("state %q, want %q", liveness.State, cloudLivenessOrphaned)
	}
	if !strings.Contains(liveness.Reason, "stopping") || !strings.Contains(liveness.Reason, "no ") {
		t.Errorf("the reason does not say what the record claims and that no instance exists: %q", liveness.Reason)
	}

	// Cloudflare that cannot answer is not Cloudflare saying "none".
	status = http.StatusBadGateway
	if liveness := cloudRunLiveness(context.Background(), "run_0rph", "stopping"); !liveness.Alive || liveness.State != "stopping" {
		t.Errorf("an unreadable Workflows API decided the run was dead: %+v", liveness)
	}
}

// The overview says so: the row is failed, not running, names why, and the
// one command that clears it is the cloud resume.
func TestTheBareOverviewNamesAnOrphanedCloudRecord(t *testing.T) {
	ownRegistry(t)
	repo := t.TempDir()
	const orphan = "run_9e40b9563bfb493cac1fe6625356e538"
	const oldOrphan = "run_2e66e765f74c4192b2b9a24a6e8415cf"
	started := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	longAgo := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return 200, map[string]any{"runs": []any{
				map[string]any{"run_id": orphan, "epic": "orp", "state": "stopping", "started_at": started},
				map[string]any{"run_id": oldOrphan, "epic": "old", "state": "running", "started_at": longAgo},
			}}
		case "/api/runs/" + orphan + "/events", "/api/runs/" + oldOrphan + "/events":
			return 200, map[string]any{"state": "stopping"}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	configureCloudflareWorkflows(t, func(*http.Request) (int, string) {
		return http.StatusNotFound, `{"success": false, "errors": [{"message": "instance.not_found"}], "result": null}`
	})
	fakeOverviewGraph(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	line := lineOf(stdout.String(), orphan)
	if line == "" {
		t.Fatalf("the orphaned run is not listed:\n%s", stdout.String())
	}
	if strings.Contains(line, "running") && !strings.Contains(line, "failed") {
		t.Errorf("an orphaned record reads running: %q", line)
	}
	if !strings.Contains(line, "failed") || !strings.Contains(line, "orphaned") {
		t.Errorf("the orphaned run's line does not say it failed as an orphan: %q", line)
	}
	if !strings.Contains(line, "clear with: ticfac run orp --cloud") {
		t.Errorf("the orphaned run's line does not name the cloud resume: %q", line)
	}
	// An orphan whose last sign of life is ten days old is history.
	if old := lineOf(stdout.String(), oldOrphan); old != "" {
		t.Errorf("an orphaned record ten days dead is still on the screen: %q", old)
	}
	if !strings.Contains(stdout.String(), "1 older run not shown") {
		t.Errorf("the old orphan is not collapsed into the history line:\n%s", stdout.String())
	}
}
