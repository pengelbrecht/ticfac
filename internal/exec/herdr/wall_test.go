package herdr

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// The wall clock, enforced rather than noticed. The reconciler's settlement
// deadline (issued + WallSeconds + the wipe threshold) is INHERITED and is not
// re-implemented here; what this executor adds is the STOP — herdr owns the
// agent's process, so the bound is only real if this executor stops the agent
// through herdr when it is reached, and records the stop as a stop at the
// wall clock rather than as a merely settled attempt.
//
// A dispatch whose bound this executor cannot enforce through the herdr
// protocol available is REFUSED at dispatch, naming the bound and the herdr
// version — never issued as a promise.

// wallClock is a clock the test drives past the bound.
type wallClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *wallClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *wallClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// routeAgentGoneAfterInterrupt scripts herdr's answer around the stop: the
// agent is live until the executor's interrupt is delivered (agent.get
// answers working), and gone after it (agent.get answers agent_not_found —
// the pane no longer runs the agent).
func (h *harness) routeAgentGoneAfterInterrupt(t *testing.T) {
	t.Helper()
	h.server.RouteN(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter, n int) error {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if n > 1 {
			return herdtest.RespondErr(w, req.ID, "agent_not_found", "the pane runs no agent")
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_info",
			"agent": map[string]any{
				"pane_id": "w1:p1", "agent_status": "working",
				"name": p.Target, "interactive_ready": true, "agent_session": nil,
			},
		})
	})
}

// wallWording is the phrase a stop at the wall clock is recorded with, on
// both executors: "stopped at its wall clock", never "settled".
const wallWording = "stopped at its wall clock"

func TestInspectStopsAWorkerPastItsWallClock(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	h.routeAgentGoneAfterInterrupt(t)

	// The bound the harness spec issues is 300s. Past it, the worker is
	// still live and still spending.
	clock.advance(301 * time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s, want failed: a worker that ran past its wall clock is stopped and settles", status.State)
	}
	if !status.Terminal {
		t.Error("a stopped worker settles; the stop is terminal for the attempt")
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) ||
		!strings.Contains(detail, "of 300s") {
		t.Errorf("the stop reads %q, want the wall-clock wording %q ... of 300s: "+
			"stopped at its wall clock and merely settled are different verdicts", detail, wallWording)
	}

	// The stop went THROUGH herdr, as the interrupt.
	sent := h.server.SendKeysCalls()
	if len(sent) == 0 {
		t.Fatal("the worker ran past its wall clock and herdr was never asked to stop it")
	}
	if keys := strings.Join(sent[0].Keys, ","); keys != "ctrl+c" {
		t.Errorf("the stop was sent as %q, want ctrl+c: the same interrupt surface cancel uses", keys)
	}

	// The stop is recorded durably as a wall-clock stop, and NOT as a
	// cancellation: a wall-clock stop is not a revoked dispatch.
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileWallExceeded); err != nil {
		t.Error("the stop at the wall clock was not recorded: a later collect cannot say the bound fired")
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err == nil {
		t.Error("a stop at the wall clock wrote a cancellation record: the bound fired, nobody revoked the dispatch")
	}

	// The settlement is durable: herdr can go quiet afterwards and the
	// attempt must still read stopped-at-its-wall-clock, not merely settled
	// and not lost.
	h.server.Close()
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s after herdr went quiet, want failed from the executor's own records", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) {
		t.Errorf("the quiet-substrate state reads %q, want the wall-clock wording %q", detail, wallWording)
	}

	// Collect reads the stop as the failure class Phase 1 already
	// distinguishes, and as a failure — never as a cancellation. The worker
	// carried a commit — the timed-durability story a real worker stopped
	// mid-run has — so the no-commits verdict does not outrank the stop.
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "--allow-empty", "-m", "tick t1: mid-work")
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome = %s, want failed: a stopped worker that reported nothing failed", collected.Result.Outcome)
	}
	if collected.Result.Outcome == subprocess.OutcomeCancelled {
		t.Error("a wall-clock stop collected as cancelled: nobody revoked this dispatch")
	}
	if collected.Result.FailureClass != subprocess.FailureWallClockExceeded {
		t.Errorf("failure class = %q, want %q: the same closed vocabulary the local executor collects",
			collected.Result.FailureClass, subprocess.FailureWallClockExceeded)
	}
	if !strings.Contains(collected.Message, wallWording) ||
		!strings.Contains(collected.Message, "300 seconds") {
		t.Errorf("collect message = %q, want it to say the worker was %s of 300 seconds", collected.Message, wallWording)
	}
}

func TestTheWallStopReachesTheAgentProcess(t *testing.T) {
	// A real fake-agent process, in the mode that reports working and waits
	// to be interrupted: the enforcement must actually stop a process
	// herdr owns, not merely record that it would have.
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "sleep"})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}

	clock.advance(301 * time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	// The harness answers agent.get from the agent's status file, so the
	// stop is delivered but the settlement waits for herdr to answer that
	// the agent is gone — the same rule as any settlement here.
	if status.State != subprocess.StateRunning {
		t.Errorf("state = %s, want running until herdr positively answers the agent is gone", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, "wall clock of 300s passed") {
		t.Errorf("the in-flight stop reads %q, want the wall-clock sentence naming the bound that fired", detail)
	}
	sent := h.server.SendKeysCalls()
	if len(sent) == 0 {
		t.Fatal("the worker ran past its wall clock and herdr was never asked to stop it")
	}

	// The process herdr owns actually exited on the interrupt.
	h.mu.Lock()
	cmd := h.agentCmd
	h.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("the harness spawned no agent process")
	}
	if !waitForOr(t, "the interrupted agent to exit", 5*time.Second, func() bool {
		return cmd.ProcessState != nil
	}) {
		h.dumpAgent(t)
		t.Fatal("the interrupt was delivered but the agent process is still running: the bound is not enforced")
	}

	// herdr answers that the agent is gone, and the attempt settles as
	// stopped at its wall clock.
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_not_found", "the pane runs no agent")
	})
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s, want failed once herdr answers the stopped agent is gone", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) {
		t.Errorf("the settlement reads %q, want the wall-clock wording %q", detail, wallWording)
	}
}

func TestAStopAtTheWallClockDoesNotDecideTheVerdict(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	h.routeAgentGoneAfterInterrupt(t)
	clock.advance(301 * time.Second)
	if status, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatal(err)
	} else if status.State != subprocess.StateFailed {
		t.Fatalf("state = %s, want the past-bound worker stopped", status.State)
	}

	// The worker finished its work before the stop caught up with it: the
	// branch and the report are read the usual way, and a stop that says
	// nothing about the work must not demote a finished attempt.
	h.doWork(t, handle, "STATUS: DONE")
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateSucceeded {
		t.Errorf("state = %s, want succeeded: the report outranks the stop, and the stop says nothing about the work",
			status.State)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict = %s, want ready-to-merge: the wall-clock stop settled the attempt, the evidence decides it",
			collected.Verdict)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome = %s, want succeeded", collected.Result.Outcome)
	}
	if collected.Result.FailureClass != "" {
		t.Errorf("failure class = %q, want none: a finished attempt carries no failure class", collected.Result.FailureClass)
	}
}

func TestDispatchRefusesABoundTheProtocolCannotEnforce(t *testing.T) {
	// A herdr older than the protocol this executor's stop surface is
	// verified against. The client can still talk to it — but a wall clock
	// issued against it is a bound nothing would stop, and a bound nothing
	// stops is one the operator trusts wrongly.
	h := newHarness(t, harnessOptions{serverProtocol: 19, serverVersion: "0.8.0"})
	_, err := h.start("t1")
	if err == nil {
		t.Fatal("a dispatch whose wall clock nothing can enforce was issued as a promise")
	}
	refusal, ok := subprocess.AsRefusal(err)
	if !ok {
		t.Fatalf("the refusal came back as a plain error: %v", err)
	}
	if refusal.Reason != subprocess.RefusedUnenforceable {
		t.Errorf("refusal reason = %q, want %q", refusal.Reason, subprocess.RefusedUnenforceable)
	}
	for _, want := range []string{"300", "0.8.0", "protocol 19"} {
		if !strings.Contains(refusal.Message, want) {
			t.Errorf("the refusal %q does not name %q: it must name the bound and the herdr version it refuses",
				refusal.Message, want)
		}
	}

	// The refusal is at DISPATCH: nothing was created, nothing was claimed.
	for _, m := range h.server.Methods() {
		if m == herdtest.MethodWorktreeCreate {
			t.Error("the refused dispatch created a worktree: a refusal is not a launch")
		}
		if m == herdtest.MethodAgentStart {
			t.Error("the refused dispatch started an agent: a refusal is not a launch")
		}
	}
}

// lastDetail is the most recent observation's detail — where observe puts the
// sentence that says why the state is what it is.
func lastDetail(observations []subprocess.Observation) string {
	if len(observations) == 0 {
		return ""
	}
	return observations[len(observations)-1].Detail
}
