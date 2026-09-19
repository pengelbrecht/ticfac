package herdr

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestCancelAfterAReportDoesNotRenameTheVerdict(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The worker reported and the work is mergeable: the attempt has
	// ALREADY REPORTED, which is durable evidence it settled itself.
	h.doWork(t, handle, "STATUS: DONE")

	// herdr goes silent for every method — the branch that used to call a
	// reported attempt unsettled and write the cancellation over it. The
	// report is durable evidence, exactly the kind silence cannot outrank.
	h.failEveryRoute()

	_, cancelErr := h.ex.Cancel(handle)
	assertNotAVerdict(t, "a cancel that could not interrupt through a silent substrate", cancelErr)

	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err == nil {
		t.Error("a cancellation was recorded over an attempt that had already reported: cancelled is " +
			"classify's first case, so the succeeded attempt would collect as cancelled from then on")
	}

	// The verdict is unchanged: collect still answers from the report and
	// the branch, and inspect still answers succeeded.
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge || collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("verdict = %s/%s, want ready-to-merge/succeeded: a cancel arriving after the report "+
			"must not rename the verdict", collected.Verdict, collected.Result.Outcome)
	}
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateSucceeded {
		t.Errorf("state = %s, want succeeded: the durable report outranks a substrate that will not answer",
			status.State)
	}
}

func TestCancelOnASettledGoneAgentRecordsNoVerdictRename(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The attempt settled itself the way settle actually happens: herdr
	// POSITIVELY answered that the agent is no longer there, and inspect
	// recorded that answer durably.
	h.settleGone(t, handle)
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// The reconciler calls Cancel-then-Dispose on a REJECTED attempt — an
	// agent that is provably gone. Cancel must treat the positive answer as
	// settled (nothing is spending) and record NO cancellation record:
	// reading not-found as silence would rename the attempt's verdict
	// `cancelled` for every later inspect and collect.
	ack, err := h.ex.Cancel(handle)
	if err != nil {
		t.Fatalf("cancelling a provably gone attempt failed: %v", err)
	}
	if !ack.StopRequested {
		t.Error("the ack must say the stop was requested: there was nothing to interrupt, which is the state the stop reaches")
	}
	if _, err := os.Stat(local.State + "/" + fileCancel); err == nil {
		t.Error("a settled attempt got a durable cancellation record: it would rename the finished attempt's " +
			"verdict for everybody who ever looks at it again")
	}
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s after the cancel, want failed: the substrate's positive answer must not rename the verdict",
			status.State)
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

// The acknowledgement of a cancel must carry the time the record carries.
//
// Cancel used to stamp twice on the first call — once into the durable record,
// once for the value it returned — so the first ack reported a time it never
// wrote, and a second cancel (which reads the record back) reported a
// different one. The record itself was always written once. That shape only
// showed when the two stamps happened to straddle a second, which on a quiet
// laptop is rare and under a loaded gate is not: it refused epic ncv's
// close-out as '17:09:38 then 17:09:37', an ack that claims a LATER time than
// the record it acknowledges.
//
// A clock that advances a full second on every call makes the straddle
// certain, so this fails every time against the two-stamp code rather than
// only on a bad day.
func TestACancelAcknowledgesTheTimeItRecorded(t *testing.T) {
	var mu sync.Mutex
	tick := time.Date(2026, 9, 19, 17, 9, 0, 0, time.UTC)
	advancing := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		tick = tick.Add(time.Second)
		return tick
	}
	h := newHarness(t, harnessOptions{now: advancing})
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
		t.Fatalf("the first cancel acknowledged %s but the record it wrote says %s: the ack must carry "+
			"the recorded time, not a second stamp taken after it", first.AcceptedAt, second.AcceptedAt)
	}
}
