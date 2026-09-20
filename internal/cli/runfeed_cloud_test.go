package cli

// The two halves of tick k7p's status surface: liveness for a run the
// Workflow hosts — answered from the Workflow, never from "is there a
// process here" — and the live table the operator asked for: one line per
// tick with its stage, attempt and how long it has been there, plus the
// run's own liveness, refreshed in place until Ctrl-C.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// cloudFeedFactory stands a factory that serves one cloud run's record and
// event feed, from fixtures this test builds with the same line encoder the
// feed contract pins — so what the tests serve is what the other host writes.
func cloudFeedFactory(t *testing.T, handler func(cloudFactoryRequest) (int, any)) {
	t.Helper()
	endpoint, _ := newCloudFactory(t, handler)
	configureCloudFactory(t, endpoint)
}

func feedLine(t *testing.T, event runfeed.Event) string {
	t.Helper()
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

// TestEventsReadsACloudRunsFeedFromTheFactory: the SAME command, the SAME
// lines, a run that is not on this machine. The operator does not say where
// the run is — the run's id does.
func TestEventsReadsACloudRunsFeedFromTheFactory(t *testing.T) {
	attempt := 1
	text := feedLine(t, runfeed.NewEvent(
		time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched",
		"worker container dispatched (wave 1, batch 1)"))
	text += feedLine(t, runfeed.NewEvent(
		time.Date(2026, 9, 20, 12, 41, 3, 0, time.UTC), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "collected",
		"verdict done: STATUS: DONE"))
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running", "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"events", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d for a cloud run with a standing feed, stderr %q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("printed %d lines, want the 2 the cloud run's feed carries: %q", len(lines), stdout.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	// The line a cloud run publishes carries the same identity a local one's
	// does — run, tick, attempt on every line, the indistinguishable-records
	// rule the whole tick turns on.
	if first["run_id"] != "run_62c289d1478fae4b1d5c7a2e3f0a9b8c" || first["tick_id"] != "a1" || first["attempt"] != float64(1) || first["schema_version"] != float64(1) {
		t.Errorf("a cloud run's line lost its identity: %v", first)
	}
	if first["stage"] != "dispatched" {
		t.Errorf("a cloud run's line carries stage %v", first["stage"])
	}
}

// TestEventsNamesACloudRunThatHasNotWrittenYet: a cloud run the factory
// knows that has written no event is a run that has not started saying —
// the same fact a missing local feed is, said for the run's other host.
func TestEventsNamesACloudRunThatHasNotWrittenYet(t *testing.T) {
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run([]string{"events", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a cloud run with no event was read as a standing feed")
	}
	if !strings.Contains(stderr.String(), "has not written an event") {
		t.Errorf("stderr %q does not say what a cloud run with no events is", stderr.String())
	}
}

// TestEventsSurfacesADeploymentWithoutABucket: the feed is exhaust; a factory
// that never wrote one says so in its own words, and the command reports the
// answer rather than guessing a local feed that does not exist.
func TestEventsSurfacesADeploymentWithoutABucket(t *testing.T) {
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 503, map[string]any{"error": "feed_unavailable", "detail": "this deployment has no artifacts bucket, so no run event feed was ever written"}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})
	var stdout, stderr bytes.Buffer
	code := Run([]string{"events", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a feed the factory cannot serve was read without complaint")
	}
	if !strings.Contains(stderr.String(), "no artifacts bucket") {
		t.Errorf("stderr %q does not carry the factory's own reason", stderr.String())
	}
}

// TestEventsFollowDeliversACloudRunsLinesAsTheyLand: --follow against a run
// that is not on this machine, through the same subscription `events
// --follow` opens for a local one — the transport half of the tick, held by
// the one loop. From-now means the standing line is NOT replayed; the line
// that lands after the subscription opened is.
func TestEventsFollowDeliversACloudRunsLinesAsTheyLand(t *testing.T) {
	attempt := 1
	standing := feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "worker container dispatched"))
	text := standing
	_, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running", "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})
	configureCloudFactory(t, "https://factory.test")

	var stdout, stderr bytes.Buffer
	go func() {
		// A follow left open is what --follow means; the test process ends it.
		_ = Run([]string{"events", "--repo", t.TempDir(), "--follow", "--interval", "50ms", "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	}()

	// The subscription must be live — its cursor taken — before the line
	// that matters lands; the observable is the follow's first read of the
	// events route, never a guessed wait.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if seenEventsRead(requests) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !seenEventsRead(requests) {
		t.Fatal("the follow never read the cloud feed")
	}
	if strings.Contains(stdout.String(), "dispatched") {
		t.Errorf("the follow replayed the standing cloud line under a from-now cursor: %q", stdout.String())
	}

	// The line that lands while the follow is open.
	text += feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "", nil, "run_finished", "completed: every tick closed behind the gate"))
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "run_finished") {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Fatalf("the follow never delivered the line that landed: %q", stdout.String())
	}
}

// seenEventsRead waits on the observable a follow exposes: whether it has
// read the events route yet.
func seenEventsRead(requests *[]cloudFactoryRequest) bool {
	for _, request := range *requests {
		if strings.HasSuffix(request.Path, "/events") {
			return true
		}
	}
	return false
}

// TestWatchEndsOnACloudRunsOwnLastWord: the run already ended, so the watch
// replays the standing feed and ends on the terminal line — the same
// semantics a local run's watch holds, because it is the same code.
func TestWatchEndsOnACloudRunsOwnLastWord(t *testing.T) {
	attempt := 1
	text := feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "worker container dispatched"))
	text += feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "collected", "verdict done: STATUS: DONE"))
	text += feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "", nil, "run_finished", "completed"))
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "completed"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "completed", "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d for a cloud run that ended cleanly, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// TestWatchJoinsACloudRunsCurrentIncarnation: the run record says the run is
// live, so an earlier incarnation's terminal line is history and the watch
// ends only on the CURRENT one's — the resumed-run semantics a local watch
// holds, held for a cloud run because it is the same code.
func TestWatchJoinsACloudRunsCurrentIncarnation(t *testing.T) {
	attempt := 1
	earlier := feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "attempt 1"))
	earlier += feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "", nil, "run_finished", "failed"))
	current := feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "attempt 1 restarted"))
	text := earlier + current
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running", "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	}()

	// The watch must be open, joined to the CURRENT incarnation: its line
	// arrives, and the previous incarnation's ending did not end the watch.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "attempt 1 restarted") {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "attempt 1 restarted") {
		t.Fatalf("the watch never joined the current incarnation: %q", stdout.String())
	}
	if strings.Count(stdout.String(), "run_finished") != 0 {
		t.Errorf("the watch replayed an earlier incarnation's terminal line: %q", stdout.String())
	}
	select {
	case got := <-code:
		t.Fatalf("the watch returned %d before the current incarnation said anything — it exited on the previous ending; stderr %q", got, stderr.String())
	default:
	}
}

// TestStatusAnswersForACloudRunFromTheWorkflow: no process here, a run the
// Workflow hosts — the answer comes from the Workflow's own state, and the
// exit code stays liveness's alone.
func TestStatusAnswersForACloudRunFromTheWorkflow(t *testing.T) {
	attempt := 1
	text := feedLine(t, runfeed.NewEvent(
		time.Now(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "worker container dispatched"))
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running", "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})
	// The Workflow instance itself could not be asked: the test's
	// credentials carry no Cloudflare API token, so the record's claim is the
	// answer, with its limit stated.

	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d for a live cloud run, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c") || !strings.Contains(stdout.String(), "alive") {
		t.Errorf("the status line does not answer for the cloud run: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Workflow") {
		t.Errorf("the status line does not say WHERE the liveness came from: %q", stdout.String())
	}
}

// TestStatusReportsAFrozenRecord: the record says running, the Workflow
// instance is errored — the disagreement cloud supervisor exists to name, and
// liveness answers "not alive", whatever the record claims.
func TestStatusReportsAFrozenRecord(t *testing.T) {
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": "running"}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})
	configureCloudFactory(t, endpoint)
	// The Cloudflare read the supervisor makes: an errored instance.
	previousCloudflare := cloudflareHTTPClient
	cloudflareHTTPClient = &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
		// Cloudflare's own envelope, as the Workflows API answers it.
		payload := map[string]any{
			"success": true,
			"errors":  []any{},
			"result": map[string]any{
				"status":     "errored",
				"success":    false,
				"error":      map[string]any{"message": "the instance errored", "name": "Error"},
				"step_count": 0,
				"steps":      []any{},
			},
		}
		encoded, _ := json.Marshal(payload)
		return &http.Response{
			StatusCode: 200, Status: "OK",
			Header:  map[string][]string{"Content-Type": {"application/json"}},
			Body:    io.NopCloser(strings.NewReader(string(encoded))),
			Request: r,
		}, nil
	})}
	t.Cleanup(func() { cloudflareHTTPClient = previousCloudflare })
	// The Cloudflare API token the supervisor read needs.
	home := t.TempDir()
	t.Setenv("HOME", home)
	config, err := credentials.LoadFrom(home + "/.ticfacrc")
	if err != nil {
		t.Fatal(err)
	}
	config.Set(credentials.KeyURL, endpoint)
	config.Set(credentials.KeyToken, "tkf_test-token")
	config.Set(credentials.KeyGatewayURL, "https://gateway.ai.cloudflare.com/v1/acct-test/ticks")
	config.Set(credentials.KeyCloudflareAPIToken, "cf_test-token")
	if err := config.Save(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"status", "--repo", t.TempDir(), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("a cloud run whose Workflow instance is errored answered alive: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "frozen") || !strings.Contains(stdout.String(), "errored") {
		t.Errorf("the disagreement between record and instance was not named: %q", stdout.String())
	}
}

// TestStatusFollowIsTheLiveTable: one line per tick — its stage, its attempt,
// how long it has been there — plus the run's own liveness, refreshed in
// place until the run says it ended. It must work the same against a run on
// this machine and one the Workflow hosts; this test drives the cloud one.
func TestStatusFollowIsTheLiveTable(t *testing.T) {
	attempt := 1
	text := feedLine(t, runfeed.NewEvent(
		time.Now().Add(-2*time.Minute), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "a1", &attempt, "dispatched", "worker container dispatched (wave 1, batch 1)"))
	text += feedLine(t, runfeed.NewEvent(
		time.Now().Add(-90*time.Second), "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "b1", &attempt, "dispatched", "worker container dispatched (wave 1, batch 1)"))

	// The record says running until the table has been observed; then the
	// run finishes, and the follow ends on the run's own word.
	state := "running"
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c":
			return 200, map[string]any{"run": map[string]any{"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": state}}
		case "/api/runs/run_62c289d1478fae4b1d5c7a2e3f0a9b8c/events":
			return 200, map[string]any{
				"run_id": "run_62c289d1478fae4b1d5c7a2e3f0a9b8c", "state": state, "text": text,
				"bytes": len(text), "total_bytes": len(text),
			}
		}
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	})

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"status", "--repo", t.TempDir(), "--follow", "--interval", "50ms", "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"}, &stdout, &stderr)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frame := stdout.String()
		if strings.Contains(frame, "a1") && strings.Contains(frame, "b1") &&
			strings.Contains(frame, "dispatched") && strings.Contains(frame, "alive") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	frame := stdout.String()
	if !strings.Contains(frame, "a1") || !strings.Contains(frame, "b1") {
		t.Fatalf("the table does not carry one line per tick: %q", frame)
	}
	if !strings.Contains(frame, "attempt 1") {
		t.Errorf("the table's tick lines do not carry the attempt: %q", frame)
	}
	if !strings.Contains(frame, "2m") && !strings.Contains(frame, "1m") {
		t.Errorf("the table's tick lines do not say how long each tick has been where it is: %q", frame)
	}
	if !strings.Contains(frame, "alive") {
		t.Errorf("the table does not state the run's own liveness: %q", frame)
	}

	// The run finishes; the follow must end on that, of its own.
	state = "completed"
	select {
	case got := <-code:
		if got != 0 {
			t.Fatalf("the follow exited %d for a run that ended while watched, stderr %q", got, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the follow never noticed the run ended")
	}
}
