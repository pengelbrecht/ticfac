package herdr

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// The gateway metering wiring (tick dm2): a pi dispatch on a Workers AI
// model launches with the provider override that joins its spend to the
// gateway logs, and NOTHING else changes the agent's argv.

// agentStartArgs is the argv the executor handed herdr for the attempt's
// launch, from the fake's own request log.
func agentStartArgs(t *testing.T, h *harness) []string {
	t.Helper()
	var startParams struct {
		Name   string   `json:"name"`
		Kind   string   `json:"kind"`
		PaneID string   `json:"pane_id"`
		Args   []string `json:"args"`
	}
	for _, req := range h.server.Requests() {
		if req.Method != herdtest.MethodAgentStart {
			continue
		}
		if err := json.Unmarshal(req.Params, &startParams); err != nil {
			t.Fatal(err)
		}
		return startParams.Args
	}
	t.Fatal("the executor never launched the agent")
	return nil
}

func TestAMeteredPiWorkerLaunchesWithTheGatewayOverride(t *testing.T) {
	h := newHarness(t, harnessOptions{
		kind: "pi", args: []string{"--approve", "--model", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
		model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		metering: &subprocess.GatewayMetering{
			RunID:      "epic-hn6",
			GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw",
		},
	})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}

	args := agentStartArgs(t, h)
	// The configured argv first, unchanged — the join APPENDS, it never
	// rewrites what the profile compiled.
	for i, want := range []string{"--approve", "--model", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"} {
		if len(args) <= i || args[i] != want {
			t.Fatalf("agent.start args = %v, want the configured argv %q at %d first", args, want, i)
		}
	}
	if len(args) != 5 || args[3] != "--extension" || !strings.HasSuffix(args[4], "gateway-metering.mjs") {
		t.Fatalf("agent.start args = %v, want --extension <state dir>/gateway-metering.mjs appended", args)
	}

	// The file the flag names is the join: the run id the gateway logs are
	// read back by, the provider's route, and nothing else in the argv.
	raw, err := os.ReadFile(args[4])
	if err != nil {
		t.Fatalf("the --extension flag names no readable file (%s): %v", args[4], err)
	}
	body := string(raw)
	if !strings.Contains(body, `"run_id\":\"epic-hn6\"`) {
		t.Errorf("the override does not tag the requests with the run id:\n%s", body)
	}
	if !strings.Contains(body, "/workers-ai/v1") {
		t.Errorf("the override does not route the provider at the gateway's workers-ai route:\n%s", body)
	}
}

func TestTheJoinLeavesOtherDispatchesExactlyAsTheyWere(t *testing.T) {
	t.Parallel()
	metering := &subprocess.GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}

	// A pi worker on a model the gateway does not serve: no flag, no file.
	h := newHarness(t, harnessOptions{
		kind: "pi", args: []string{"--approve"}, model: "openai-codex/gpt-5.6-sol", metering: metering,
	})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	if args := agentStartArgs(t, h); strings.Join(args, " ") != "--approve" {
		t.Errorf("a non-Workers-AI dispatch's args are %v, want the configured argv alone: the join is a pi Workers AI route, not a global flag", args)
	}

	// A claude dispatch on a Workers AI spelling: the flag is a PI one, and
	// another kind must never read it as its own.
	h = newHarness(t, harnessOptions{
		kind: "claude", args: []string{"--model", "opus"}, model: "opus", metering: metering,
	})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	if args := agentStartArgs(t, h); strings.Contains(strings.Join(args, " "), "--extension") {
		t.Errorf("a claude dispatch's args are %v, want no pi flags: --extension is the pi CLI's own", args)
	}

	// No metering configured — a host with no gateway: exactly as before.
	h = newHarness(t, harnessOptions{kind: "pi", args: []string{"--approve"}, model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3"})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	if args := agentStartArgs(t, h); strings.Join(args, " ") != "--approve" {
		t.Errorf("a dispatch with no metering configured got args %v, want the configured argv alone: an unconfigured host launches exactly as it did before the join existed", args)
	}
}

// The relaunch path (a launch herdr refused over a name another pane holds)
// writes and loads the override too: the join belongs to the ATTEMPT, not to
// the first pane it tried.
func TestTheRelaunchCarriesTheJoinToo(t *testing.T) {
	h := newHarness(t, harnessOptions{
		kind: "pi", args: []string{"--approve"}, model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		metering: &subprocess.GatewayMetering{
			RunID:      "epic-hn6",
			GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw",
		},
	})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	args := agentStartArgs(t, h)
	if len(args) != 3 || args[1] != "--extension" || !strings.HasSuffix(args[2], "gateway-metering.mjs") {
		t.Fatalf("agent.start args = %v, want the override appended", args)
	}
	if _, err := os.Stat(args[2]); err != nil {
		t.Errorf("the override file is not where the flag points: %v", err)
	}
}
