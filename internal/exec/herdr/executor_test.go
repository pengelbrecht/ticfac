package herdr

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// start: create the worktree and workspace through herdr, launch the agent,
// submit the prompt — and the two measured startup races absorbed on the way.

// TestStartCreatesTheWorkspaceAndLaunchesTheAgent is the happy path, asserted
// on what herdr was actually asked: the branch and the base the spec named,
// the kind and args the host configured, the pane the create handed back.
func TestStartCreatesTheWorkspaceAndLaunchesTheAgent(t *testing.T) {
	h := newHarness(t, harnessOptions{kind: "pi", args: []string{"--model", "glm-5.3"}})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}

	var createParams struct {
		Cwd    string  `json:"cwd"`
		Branch *string `json:"branch"`
		Base   *string `json:"base"`
		Focus  bool    `json:"focus"`
	}
	for _, req := range h.server.Requests() {
		if req.Method == herdtest.MethodWorktreeCreate {
			if err := json.Unmarshal(req.Params, &createParams); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if createParams.Cwd != h.repo.Dir {
		t.Errorf("worktree.create was scoped to %q, want the repository %q", createParams.Cwd, h.repo.Dir)
	}
	if createParams.Branch == nil || *createParams.Branch != "ticfac/run-harness/tick-t1/attempt-1" {
		t.Errorf("worktree.create branch = %v, want the spec's write ref as a branch", createParams.Branch)
	}
	if createParams.Base == nil || *createParams.Base != h.repo.Base {
		t.Errorf("worktree.create base = %v, want the spec's base %s", createParams.Base, h.repo.Base)
	}
	if createParams.Focus {
		t.Error("worktree.create was asked to steal focus: a run dispatching in the background has no " +
			"business moving the operator's view")
	}

	var startParams struct {
		Name   string   `json:"name"`
		Kind   string   `json:"kind"`
		PaneID string   `json:"pane_id"`
		Args   []string `json:"args"`
	}
	for _, req := range h.server.Requests() {
		if req.Method == herdtest.MethodAgentStart {
			if err := json.Unmarshal(req.Params, &startParams); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if startParams.Kind != "pi" {
		t.Errorf("agent.start kind = %q, want the configured kind", startParams.Kind)
	}
	if strings.Join(startParams.Args, " ") != "--model glm-5.3" {
		t.Errorf("agent.start args = %v, want the configured args", startParams.Args)
	}
	if startParams.PaneID == "" {
		t.Error("agent.start was given no pane: it launches in the root pane the create handed back")
	}
	if startParams.Name != "tick-t1-a1" {
		t.Errorf("agent.start name = %q, want the readable per-attempt name", startParams.Name)
	}

	// The prompt herdr delivered is the worker prompt: the report path the
	// executor owns, the tick the spec named, the boundary as a fact.
	promptBytes := readFileAt(t, h.promptFile)
	prompt := string(promptBytes)
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Write your report to this EXACT ABSOLUTE PATH") ||
		!strings.Contains(prompt, local.ResultPath) {
		t.Error("the delivered prompt does not name the executor-owned absolute report path")
	}
	if !strings.Contains(prompt, "- tick: t1") {
		t.Error("the delivered prompt does not name the tick the job is about")
	}
	if !strings.Contains(prompt, "STATUS: DONE") {
		t.Error("the delivered prompt does not state the report's status-line contract")
	}

	// The attempt record is durable and re-addressable, and the handle IS a
	// frozen copy of it.
	if _, _, err := local.resolved(); err != nil {
		t.Fatalf("the handle does not resolve: %v", err)
	}
}

// TestStartRefusesAWriteRefOutsideTheGrant is the issuer-enforced boundary,
// the same rule the local executor applies at the same place.
func TestStartRefusesAWriteRefOutsideTheGrant(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	spec := h.spec("run-harness/tick-t1/attempt-1", "t1")
	spec.Source.WriteRef = "refs/heads/main"
	if _, err := h.ex.Start(spec); err == nil {
		t.Fatal("a write_ref outside the granted namespace started: the boundary is the issuer's to enforce")
	}
}

// TestStartAdoptsARunningAttempt is A6: a second Start of the same identity
// returns the SAME job — the same workspace, the same agent — and never
// dispatches a second worker over a live one.
func TestStartAdoptsARunningAttempt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	again, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	a, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	b, err := local(again)
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkspaceID != b.WorkspaceID || a.AgentName != b.AgentName {
		t.Errorf("the second Start returned %s/%s, want the live attempt's %s/%s",
			b.WorkspaceID, b.AgentName, a.WorkspaceID, a.AgentName)
	}
	if count := h.server.CountMethod(herdtest.MethodWorktreeCreate); count != 1 {
		t.Errorf("worktree.create was called %d times for one live attempt: adoption dispatches nothing", count)
	}
}

// TestStartRefusesASettledAttempt: a retry is a new attempt number, and the
// refusal says so rather than redispatching.
func TestStartRefusesASettledAttempt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	// The attempt settles with no report: the agent is gone.
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_not_found", "no such agent")
	})
	h.ex.Inspect(h.lastHandle(t), "")
	_, err := h.start("t1")
	if err == nil {
		t.Fatal("a settled attempt started again: a retry is a new attempt number, not this one again")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedSettled {
		t.Errorf("the refusal was %v, want a settled refusal a caller can recover by reason", err)
	}
}

// TestStartHoldsAnAttemptNobodyCanAddress is the held-not-redispatched rule:
// herdr not answering about a live attempt is nobody's to start over.
func TestStartHoldsAnAttemptNobodyCanAddress(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "herdr has nothing to say")
	})
	_, err := h.start("t1")
	if err == nil {
		t.Fatal("an attempt nobody can address started again: the old worker may still be alive and spending")
	}
	if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedUnknown {
		t.Errorf("the refusal was %v, want the liveness-unknown one", err)
	}
}

// TestStartRetriesAPaneBusyLaunch is the measured startup race: the root pane
// worktree.create hands back is not an interactive shell for the first few
// hundred milliseconds, and agent.start answers agent_pane_busy until it is.
func TestStartRetriesAPaneBusyLaunch(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.server.RouteN(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter, n int) error {
		if n == 1 {
			return herdtest.RespondErr(w, req.ID, "agent_pane_busy", "the pane is busy")
		}
		return h.server.Builtin(herdtest.MethodAgentStart)(t, req, w)
	})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a pane-busy first answer is a retry, not a failure: %v", err)
	}
	if count := h.server.CountMethod(herdtest.MethodAgentStart); count < 2 {
		t.Errorf("agent.start was called %d times: the pane-busy retry did not happen", count)
	}
	if _, err := local(handle); err != nil {
		t.Fatal(err)
	}
}

// TestStartPollsForInteractiveReady is the second startup race: a launch that
// answers launch_pending has typed the command but not detected the agent,
// and readiness — not the status — is what a prompt needs.
func TestStartPollsForInteractiveReady(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.server.RouteN(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter, n int) error {
		var p struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		// The accepted-but-pending launch: green, with no agent behind it
		// yet — the shape herdr really returns in this window.
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_started",
			"agent": map[string]any{
				"pane_id": p.PaneID, "agent_status": "unknown",
				"name": p.Name, "interactive_ready": false, "launch_pending": true,
				"agent_session": nil,
			},
		})
	})
	h.server.RouteN(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter, n int) error {
		if n < 3 {
			// The status reaches idle about a second before readiness does:
			// a live agent, not yet ready.
			return herdtest.RespondJSON(w, req.ID, map[string]any{
				"type": "agent_info",
				"agent": map[string]any{
					"pane_id": "w1:p1", "agent_status": "idle",
					"name": "tick-t1-a1", "interactive_ready": false, "agent_session": nil,
				},
			})
		}
		return h.server.Builtin(herdtest.MethodAgentGet)(t, req, w)
	})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a pending launch is a readiness poll, not a failure: %v", err)
	}
	if count := h.server.CountMethod(herdtest.MethodAgentGet); count < 3 {
		t.Errorf("agent.get was called %d times: readiness was never polled", count)
	}
	if _, err := local(handle); err != nil {
		t.Fatal(err)
	}
}

// TestAnUnconfirmedDispatchIsRecordedNotFailed: the wait for `working` can
// time out on a trivial tick that finishes before working is rendered. That
// is an observation and a flag on the record, never a failure.
func TestAnUnconfirmedDispatchIsRecordedNotFailed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// The agent never visibly works: agent.wait answers timeout.
	h.server.Route(herdtest.MethodAgentWait, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "timeout", "agent did not reach working")
	})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("an unconfirmed dispatch failed the start: %v", err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := local.resolved()
	if err != nil {
		t.Fatal(err)
	}
	if record.DispatchConfirmed {
		t.Error("the record says the dispatch was confirmed; agent.wait never observed working")
	}
	if record.LaunchConfirmed != true {
		t.Error("the launch itself was confirmed and the record must say so")
	}
}

// --- harness helpers for the tests above ---

func readFileAt(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// lastHandle is the handle of the attempt this harness last started — the
// one a test inspects, cancels or tears down without starting another.
func (h *harness) lastHandle(t *testing.T) *subprocess.JobHandle {
	t.Helper()
	if h.last == nil {
		t.Fatal("no attempt was started through the harness")
	}
	return h.last
}
