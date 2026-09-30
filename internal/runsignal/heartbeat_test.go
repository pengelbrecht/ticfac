package runsignal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// heartbeatDoor is a fake factory heartbeat door answering one fixed reply.
func heartbeatDoor(t *testing.T, status int, body any, seen *[]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != heartbeatPath || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		*seen = append(*seen, r.Header.Get("Authorization"))
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}

// short: a local httptest door.
func TestHeartbeatKeepsBeatingWhileTheRunIsLive(t *testing.T) {
	var seen []string
	door := heartbeatDoor(t, http.StatusOK, map[string]any{"run_id": "run_1", "state": "running", "stopping": false}, &seen)
	stopped := ""
	h := NewHeartbeat(door.URL, "tkr_run", time.Minute, nil, func(reason string) { stopped = reason })
	if !h.Beat(context.Background()) {
		t.Fatal("a live run's beat said to stop beating")
	}
	if stopped != "" {
		t.Fatalf("a live run was stopped: %s", stopped)
	}
	if len(seen) != 1 || seen[0] != "Bearer tkr_run" {
		t.Fatalf("the beat presented %v, want the run's own token", seen)
	}
}

// short: a local httptest door.
func TestHeartbeatStopsTheRunWhenTheFactoryIsStoppingIt(t *testing.T) {
	var seen []string
	door := heartbeatDoor(t, http.StatusOK, map[string]any{"run_id": "run_1", "state": "stopping", "stopping": true}, &seen)
	var stops []string
	var log bytes.Buffer
	h := NewHeartbeat(door.URL, "tkr_run", time.Minute, &log, func(reason string) { stops = append(stops, reason) })
	if h.Beat(context.Background()) {
		t.Fatal("a stopping run's beat said to keep beating")
	}
	h.Beat(context.Background())
	if len(stops) != 1 || !strings.Contains(stops[0], "stopping") {
		t.Fatalf("onStop was called %v, want once, for the stop", stops)
	}
}

// short: a local httptest door.
func TestHeartbeatStopsTheRunWhenItsCredentialIsRefused(t *testing.T) {
	var seen []string
	door := heartbeatDoor(t, http.StatusForbidden,
		map[string]any{"error": "run_token_revoked", "detail": "finished:failed"}, &seen)
	stopped := ""
	h := NewHeartbeat(door.URL, "tkr_run", time.Minute, nil, func(reason string) { stopped = reason })
	if h.Beat(context.Background()) {
		t.Fatal("a refused credential's beat said to keep beating")
	}
	if !strings.Contains(stopped, "run_token_revoked") {
		t.Fatalf("onStop reason %q does not name the refusal", stopped)
	}
}

// short: a local httptest door.
func TestHeartbeatRidesOutAFactoryThatDoesNotAnswer(t *testing.T) {
	var seen []string
	door := heartbeatDoor(t, http.StatusBadGateway, map[string]any{"error": "bad_gateway"}, &seen)
	stopped := false
	var log bytes.Buffer
	h := NewHeartbeat(door.URL, "tkr_run", time.Minute, &log, func(string) { stopped = true })
	if !h.Beat(context.Background()) || !h.Beat(context.Background()) {
		t.Fatal("a 5xx stopped the beating")
	}
	if stopped {
		t.Fatal("a 5xx stopped the run: one lost beat is not the factory's verdict")
	}
	if strings.Count(log.String(), "answered 502") != 1 {
		t.Fatalf("a failing streak is said once, got:\n%s", log.String())
	}
}

// short: environment only.
func TestHeartbeatFromEnvIsOnlyALocalOrchestrators(t *testing.T) {
	t.Setenv(factoryURLEnv, "https://factory.invalid")
	t.Setenv(factoryTokenEnv, "tkr_run")
	t.Setenv(EnvOrchestrator, "")
	if HeartbeatFromEnv(nil, nil) != nil {
		t.Fatal("a container orchestrator (no TICKS_ORCHESTRATOR) got a heartbeat")
	}
	t.Setenv(EnvOrchestrator, OrchestratorLocal)
	if HeartbeatFromEnv(nil, nil) == nil {
		t.Fatal("a local orchestrator got no heartbeat")
	}
	t.Setenv(factoryTokenEnv, "")
	if HeartbeatFromEnv(nil, nil) != nil {
		t.Fatal("a heartbeat was built with no run credential")
	}
	// The nil heartbeat is a valid no-op.
	var none *Heartbeat
	none.Start()
	none.Stop()
}
