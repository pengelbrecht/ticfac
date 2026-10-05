package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The local gateway meter (tick dm2): the status model's own read of the
// operator's AI Gateway logs, joined by the run id the metering tag stamps —
// the reader half of the join the executor wiring writes.

// gatewayLogsServer is a fake AI Gateway logs API answering the shape
// gatewaytrace reads: one page of rows, each stamped with the metadata the
// caller states.
func gatewayLogsServer(t *testing.T, rows []map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/logs") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rows})
	}))
	t.Cleanup(server.Close)
	return server
}

func gatewayCostRow(cost float64, runID string) map[string]any {
	return map[string]any{
		"id":         "call-1",
		"created_at": "2026-10-05T18:17:31.133Z",
		"model":      "@cf/zai-org/glm-5.3",
		"provider":   "workers-ai",
		"success":    true,
		"cost":       cost,
		"tokens_in":  700, "tokens_out": 4, "duration": 631,
		"metadata": map[string]string{"run_id": runID},
	}
}

// isolateGatewayHost points HOME at an empty directory, returns the
// ~/.ticfacrc path to write, and clears the API-base override so the host's
// own state never answers for the test.
func isolateGatewayHost(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(jev.OperatorBaseEnv, "")
	return filepath.Join(home, ".ticfacrc")
}

// short: a pure read over an httptest server and a temp HOME, no repository is built
func TestStatusWorkerCostMetersTheRunsJoinedCalls(t *testing.T) {
	rc := isolateGatewayHost(t)
	server := gatewayLogsServer(t, []map[string]any{
		gatewayCostRow(0.0001272, "epic-hn6"),
	})
	t.Setenv(jev.OperatorBaseEnv, server.URL)
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cost, err := statusWorkerCost(context.Background(), "epic-hn6")
	if err != nil {
		t.Fatalf("the gateway read failed: %v", err)
	}
	if cost == nil {
		t.Fatal("the run's joined calls answered no worker cost: the gateway's own measured number is the meter")
	}
	if cost.USD != 0.0001272 || cost.Source != "gateway" {
		t.Errorf("the worker cost is %+v, want the joined calls' sum from the gateway", *cost)
	}

	// The local filter: the reader asks for the run id and re-applies it, so
	// a page of another run's rows is not this run's spend.
	other := gatewayLogsServer(t, []map[string]any{gatewayCostRow(0.41, "run_other")})
	t.Setenv(jev.OperatorBaseEnv, other.URL)
	cost, err = statusWorkerCost(context.Background(), "epic-hn6")
	if err != nil {
		t.Fatalf("the gateway read failed: %v", err)
	}
	if cost != nil {
		t.Errorf("another run's rows metered this run (%+v): the join is by run id, never by page", *cost)
	}

	// No rows at all — the run's calls were never joined: nil, the honest
	// not-measured, not an error and not a zero.
	empty := gatewayLogsServer(t, nil)
	t.Setenv(jev.OperatorBaseEnv, empty.URL)
	cost, err = statusWorkerCost(context.Background(), "epic-hn6")
	if err != nil {
		t.Fatalf("an empty gateway read failed: %v", err)
	}
	if cost != nil {
		t.Errorf("no joined call metered a worker cost (%+v): unmeasured is null, never a zero", *cost)
	}
}

// short: the same pure read with no host configured
func TestStatusWorkerCostWithoutAGatewayIsTheOptionalState(t *testing.T) {
	rc := isolateGatewayHost(t)

	// No ~/.ticfacrc at all.
	cost, err := statusWorkerCost(context.Background(), "epic-hn6")
	if err != nil || cost != nil {
		t.Errorf("a host with no credentials answered (%v, %+v), want (nil, nil): cost telemetry is optional, never a failed read", err, cost)
	}
	// A gateway with no token: still the optional state, still no error —
	// nothing could read the logs, and the line says "not metered".
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cost, err = statusWorkerCost(context.Background(), "epic-hn6")
	if err != nil || cost != nil {
		t.Errorf("a half-configured host answered (%v, %+v), want (nil, nil): the documented optional state is not a degradation", err, cost)
	}
	// No run id named: nothing to join.
	cost, err = statusWorkerCost(context.Background(), "  ")
	if err != nil || cost != nil {
		t.Errorf("an empty run id answered (%v, %+v), want (nil, nil)", err, cost)
	}
}

// short: the reader's error path over an unreachable-configured gateway
func TestStatusWorkerCostReportsAGatewayThatCannotBeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(jev.OperatorBaseEnv, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	serverURL := server.URL
	// Tear the server down: a gateway that should answer but cannot is a
	// degraded source, never a silent nil.
	server.Close()
	t.Setenv(jev.OperatorBaseEnv, serverURL)
	if err := os.WriteFile(filepath.Join(home, ".ticfacrc"), []byte(
		"factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"+
			"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := statusWorkerCost(context.Background(), "epic-hn6"); err == nil {
		t.Fatal("an unreachable gateway answered no error: a configured host that cannot be read is a degraded source, not the optional state")
	}
}

// The wiring pin, in the shape of TestStatusModelLocalWiringPassesTheDashboardReaders:
// a LOCAL run's gathering passes the gateway's measured number as the
// workers-ai river's ground truth — the one thing that makes the metered
// line reachable from `status --json` — and degrades the model's cost, like
// every source, when a configured gateway cannot be read.
func TestStatusModelLocalWiringCarriesTheGatewayCost(t *testing.T) {
	captured := captureStatusSources(t)

	join := &statusmodel.WorkerCostInput{USD: 0.0001272, Source: "gateway"}

	// A bare repository stands for the checkout (the wiring under test is
	// what the gathering PASSES, not what any source answers).
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// The gateway answered: the number is the model's WorkerCost input.
	localStatusModel(context.Background(), repo, "epic-none",
		runlifeAliveStatus(),
		modelGatherers{
			graph: func(context.Context, string, string) *tk.Graph { return nil },
			ci:    func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil },
			workerCost: func(context.Context, string) (*statusmodel.WorkerCostInput, error) {
				return join, nil
			},
		})
	if captured.WorkerCost == nil || captured.WorkerCost.USD != join.USD || captured.WorkerCost.Source != "gateway" {
		t.Errorf("localStatusModel passed WorkerCost %+v, want the gateway's own measured number: the metered line is unreachable without it", captured.WorkerCost)
	}

	// The gateway could not be read: the model degrades its cost, never
	// silently freezing the meter.
	localStatusModel(context.Background(), repo, "epic-none",
		runlifeAliveStatus(),
		modelGatherers{
			graph: func(context.Context, string, string) *tk.Graph { return nil },
			ci:    func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil },
			workerCost: func(context.Context, string) (*statusmodel.WorkerCostInput, error) {
				return nil, errGatewayUnreachableForTest
			},
		})
	if !containsDegraded(captured.Degraded, "cost") {
		t.Errorf("a gateway that could not be read degraded %v, want \"cost\": the meter is a source like any other", captured.Degraded)
	}
}

var errGatewayUnreachableForTest = errors.New("the AI Gateway logs could not be reached")

func containsDegraded(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func runlifeAliveStatus() runlife.Status {
	return runlife.Status{State: runlife.Alive}
}
