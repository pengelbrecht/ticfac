package herdr

import (
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// cancel: revoke — the durable refusal to reissue — then stop the agent
// through herdr's own interrupt surface. Never a teardown.

func TestCancelRevokesThenInterrupts(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")

	ack, err := h.ex.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.CredentialsRevoked {
		t.Error("the ack must say the credential (the dispatch) was revoked")
	}
	if ack.Order != subprocess.OrderRevokeThenStop {
		t.Errorf("order = %s, want revoke-then-stop: a stop before the revocation can spend on the way out", ack.Order)
	}
	if ack.Reissue != subprocess.ReissueRefused {
		t.Errorf("reissue = %s, want refused", ack.Reissue)
	}
	if !ack.StopRequested {
		t.Error("the ack must say a stop was requested: the interrupt was accepted")
	}

	// The interrupt went through herdr's surface, as the interrupt.
	sent := h.server.SendKeysCalls()
	if len(sent) != 1 {
		t.Fatalf("agent.send_keys was called %d times, want exactly one interrupt", len(sent))
	}
	if sent[0].Keys == nil || strings.Join(sent[0].Keys, ",") != "ctrl+c" {
		t.Errorf("keys = %v, want ctrl+c: the documented interactive interrupt", sent[0].Keys)
	}

	// The revocation is durable and a re-Start is refused — that is the
	// whole point of writing it before the stop.
	if _, err := h.start("t1"); err == nil {
		t.Fatal("a cancelled attempt accepted a new dispatch: the refusal to reissue is the revocation")
	} else if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedCancelled {
		t.Errorf("the re-Start refusal was %v, want the cancelled one", err)
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	first, err := h.ex.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.ex.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if first.AcceptedAt != second.AcceptedAt {
		t.Errorf("a second cancel re-accepted the cancellation (%s then %s): the record is written once",
			first.AcceptedAt, second.AcceptedAt)
	}
	if n := h.server.CountMethod(herdtest.MethodAgentSendKeys); n != 2 {
		// Two interrupts is the honest shape: cancel does not know whether
		// the first landed, and a caller that retries on error must not
		// re-record anything. The RECORD is the idempotence.
		t.Logf("send_keys called %d times; the record is the idempotence, not the keypress", n)
	}
}

func TestCancelDoesNotRenameASettledAttempt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")

	// The reconciler's cleanUp calls Cancel-then-Dispose on a MERGED attempt
	// too. The cancel over a settled, idle agent must record NO refusal —
	// otherwise the durable record would rename the attempt's verdict
	// `cancelled` for every later inspect and collect.
	ack, err := h.ex.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if ack.StopRequested {
		t.Log("a leftover process was stopped; only the refusal must be absent")
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err == nil {
		t.Error("a settled attempt got a durable cancellation record: it would rename the finished attempt's " +
			"verdict for everybody who ever looks at it again")
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Result.Outcome == subprocess.OutcomeCancelled {
		t.Errorf("collect answered cancelled over an attempt that finished: verdict %s, outcome %s",
			collected.Verdict, collected.Result.Outcome)
	}
}

func TestCancelOnAWorkingAgentRecordsTheFullRefusal(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The worker wrote its report and KEPT GOING: cancel must treat it as
	// live, because the operator is trying to stop something that spends.
	h.doWork(t, handle, "STATUS: DONE")
	h.setStatus("working")

	if _, err := h.ex.Cancel(handle); err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err != nil {
		t.Error("a working agent outranks the report here: the revocation must be recorded")
	}
}

func TestCancelWithHerdrDownStillRevokes(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	h.server.Close()

	// The substrate cannot deliver the interrupt. The revocation — the
	// durable refusal to reissue — must still be recorded, so nothing can
	// ever be dispatched over this attempt, and the failure is returned as
	// the operational error it is.
	_, err = h.ex.Cancel(handle)
	if err == nil {
		t.Fatal("a cancel that could not interrupt through a dead herdr reported success: the agent may still be spending")
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err != nil {
		t.Error("the revocation was not recorded: silence from herdr must not leave a re-issuable dispatch")
	}
	if _, err := h.start("t1"); err == nil {
		t.Error("a cancelled attempt accepted a new dispatch after herdr's death")
	} else if refusal, ok := subprocess.AsRefusal(err); !ok || refusal.Reason != subprocess.RefusedCancelled {
		t.Errorf("the re-Start refusal was %v, want the cancelled one", err)
	}
}
