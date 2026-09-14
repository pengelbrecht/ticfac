package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// inspect: herdr answers liveness, durable evidence answers completion, and
// `lost` is the answer when nobody can be asked.

// doWork is the worker's half of an attempt, done by the test: one commit on
// the attempt branch and a report at the executor-owned path.
func (h *harness) doWork(t *testing.T, handle *subprocess.JobHandle, statusLine string) {
	t.Helper()
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, local.Worktree, "git", "add", "hello.txt")
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "-m", "tick t1: hello")
	if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local.ResultPath, []byte("## Report\n\nDid the thing.\n\n"+statusLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.setStatus("done")
}

func TestInspectReportsARunningAgent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("state = %s, want running: herdr answers that a live agent is there", status.State)
	}
	if status.Terminal {
		t.Error("a running attempt is not terminal")
	}
	if status.Cursor == nil {
		t.Error("a non-terminal inspect hands back a cursor; the reconciler's keepalive is built on it")
	}
}

func TestInspectAnswersFromTheReportBeforeAskingHerdr(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	// The agent is STILL working per herdr: the report is the completion
	// contract, and it outranks the substrate's liveness answer.
	h.setStatus("working")
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateSucceeded {
		t.Errorf("state = %s, want succeeded: a worker that wrote its report has finished, whatever the agent is doing",
			status.State)
	}
	if !status.Terminal {
		t.Error("succeeded is terminal")
	}
}

func TestInspectRecordsTheAgentGoneAndSettlesTheAttempt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_not_found", "no such agent")
	})
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s, want failed: the agent is gone and settled is not finished", status.State)
	}
	if !status.Terminal {
		t.Error("settled-with-no-report is terminal")
	}

	// The settlement is durable: herdr can go quiet afterwards and the state
	// must not degrade into `lost`. This is the evidence a herdr-free
	// collect reads (the seam tick 2xu asserts from its side).
	h.server.Close()
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s after herdr went quiet, want failed from the executor's own settlement record",
			status.State)
	}
}

func TestInspectAnswersLostWhenNobodyCanBeAsked(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "invalid_request", "herdr has nothing to say")
	})
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateLost {
		t.Errorf("state = %s, want lost: herdr not answering is a statement about the observer, not the job", status.State)
	}
	if status.Terminal {
		t.Error("lost is not terminal: recovery may re-adopt, and a lost attempt is held for a person")
	}
}

func TestInspectAnswersCancelledFromTheDurableRecord(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	if _, err := h.ex.Cancel(handle); err != nil {
		t.Fatal(err)
	}
	// herdr gone afterwards: the cancellation is durable, and it outranks
	// whatever the substrate could have said.
	h.server.Close()
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateCancelled {
		t.Errorf("state = %s, want cancelled from the durable record", status.State)
	}
}

// TestAReportingAgentPastItsBoundIsStillStopped is etp's fourth finding:
// observe answered `succeeded` from the report BEFORE the wall-clock branch,
// so an agent that reported and kept running was never stopped — the same
// report-outranks-liveness shape the local executor's Cancel was fixed for.
// A report settles the VERDICT; it does not settle the SPENDING. The bound is
// enforced even on an attempt that reported, or dispose then refuses it as a
// working agent forever.
func TestAReportingAgentPastItsBoundIsStillStopped(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The worker reports... and keeps running.
	h.doWork(t, handle, "STATUS: DONE")
	h.setStatus("working")
	clock.advance(301 * time.Second)

	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	// The report still settles the verdict — the enforcement must not
	// change it, only stop the spending.
	if status.State != subprocess.StateSucceeded {
		t.Errorf("state = %s, want succeeded: the report decides the verdict, whatever the agent is doing",
			status.State)
	}
	// But the stop went out: the bound fired on an agent that is still
	// live, and silence about it is how a run pays past its bound.
	if len(h.server.SendKeysCalls()) == 0 {
		t.Fatal("an agent spending past its wall clock was never stopped: the report outranked the enforcement")
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileWallExceeded); err != nil {
		t.Error("the stop at the wall clock was not recorded durably: a later collect cannot say the bound fired")
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, "wall clock of 300s") {
		t.Errorf("the detail reads %q, want it to name the bound that fired while the agent keeps running", detail)
	}

	// The disposal can now proceed once the stop settles the agent: the
	// enforcement reached it, so teardown is not refused as `working`
	// forever.
	h.setStatus("idle")
	if err := h.ex.Dispose(handle, subprocess.DisposeOptions{
		Reason: "attempt 1 of t1 is merged and the tick is closed", KeepBranch: true}); err != nil {
		t.Fatalf("the reporting attempt whose agent was stopped at its bound could not be disposed: %v", err)
	}
}
