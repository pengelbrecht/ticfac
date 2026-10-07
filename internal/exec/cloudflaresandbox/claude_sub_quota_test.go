package cloudflaresandbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A claude-sub job whose subscription ran out of quota MID-JOB exits like any
// harness failure (ExitWorkerAgent, after a 429), and before this collected as
// a failed attempt at the tick: the tier ladder climbed sonnet to opus, and
// with every subscription benched the next try ran on Workers AI at the
// ceiling. The door now says, from the proxy's own record of the job's answer,
// that the subscription ran out under it (job-protocol 2.3.0's
// claude_sub_quota observation), and the collect carries it as a transient,
// mid-job infrastructure failure: dispatched again at the same tier, no rung.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAClaudeSubJobWhoseQuotaRanOutMidJobCollectsAsTransientInfrastructure(t *testing.T) {
	h, _, handle, _ := newCollectHarness(t)
	h.door.setStatus("keh", 1, doorStatus{
		state:    subprocess.StateFailed,
		terminal: true,
		observations: []subprocess.Observation{
			{At: "2026-10-07T09:00:00Z", Kind: subprocess.ObsExited,
				Detail: fmt.Sprintf("the container's work process exited %d", sandboximage.ExitWorkerAgent)},
			{At: "2026-10-07T09:00:00Z", Kind: subprocess.ObsClaudeSubQuota,
				Detail: "claude subscription MAX1 ran out of quota during this job (HTTP 429, unified rejected); " +
					"benched until 2026-10-07T13:00:00.000Z"},
		},
	})
	if _, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	fault := collected.Infrastructure
	if fault == nil {
		t.Fatalf("a job whose subscription ran out under it collected with no infrastructure fact: %s",
			collected.Message)
	}
	if fault.Persistent || !fault.MidJob || fault.ExitCode != sandboximage.ExitWorkerAgent ||
		fault.Service != "the claude subscription's quota" || fault.Fix == "" {
		t.Errorf("fault %+v, want transient, mid-job, exit %d, the subscription's quota, with a fix", fault,
			sandboximage.ExitWorkerAgent)
	}
	if collected.Result.FailureClass != subprocess.FailureInfrastructure {
		t.Errorf("failure class %q, want %q", collected.Result.FailureClass, subprocess.FailureInfrastructure)
	}
	if !strings.Contains(collected.Message, "benched until 2026-10-07T13:00:00.000Z") {
		t.Errorf("the message does not carry the door's account: %s", collected.Message)
	}
}

// The negative control: the same harness exit with no quota observation is
// the failed attempt it always was.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAClaudeSubJobThatFailedWithoutAQuotaAnswerIsNotInfrastructure(t *testing.T) {
	h, _, handle, _ := newCollectHarness(t)
	h.door.setStatus("keh", 1, doorStatus{
		state:    subprocess.StateFailed,
		terminal: true,
		observations: []subprocess.Observation{{At: "2026-10-07T09:00:00Z", Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("the container's work process exited %d", sandboximage.ExitWorkerAgent)}},
	})
	if _, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Infrastructure != nil {
		t.Errorf("collected as infrastructure %+v with no quota answer from the door", collected.Infrastructure)
	}
}

// The door names a step-down on the handle it answers a start with; the
// executor turns it into the feed's sentence, from the handle Start returned
// and from the record a restarted controller resolves it against.
//
// short: pure; no door, no repository.
func TestAClaudeSubStepDownIsSaidFromTheHandle(t *testing.T) {
	retry := "2026-10-07T13:00:00.000Z"
	record := &attemptRecord{
		JobID: "run-r1/tick-keh/attempt-1", Attempt: 1, TickID: "keh",
		Harness: "pi-durable", Model: "workers-ai/@cf/zai-org/glm-5.3",
		ClaudeSub: &claudeSubNote{State: "stepped_down", Reason: "exhausted", RetryAt: &retry},
	}
	ex := &Executor{}
	detail, ok := ex.ClaudeSubStepDown(handleFor(record))
	if !ok {
		t.Fatal("a stepped-down handle said nothing")
	}
	want := "claude-sub is exhausted until 2026-10-07T13:00:00.000Z; run-r1/tick-keh/attempt-1 runs on " +
		"Workers AI (pi-durable on workers-ai/@cf/zai-org/glm-5.3) instead"
	if detail != want {
		t.Errorf("detail %q, want %q", detail, want)
	}

	record.ClaudeSub = &claudeSubNote{State: "leased", Label: "MAX1"}
	if _, ok := ex.ClaudeSubStepDown(handleFor(record)); ok {
		t.Error("a leased job reads as stepped down")
	}
	record.ClaudeSub = nil
	if _, ok := ex.ClaudeSubStepDown(handleFor(record)); ok {
		t.Error("a job that never resolved the rung reads as stepped down")
	}
}

// On the claude harness, exit 14 is the claude-sub probe's transient failure
// (image/common.sh): the subscription's route answered only 429/5xx or
// nothing. The stop names that route, never "the model gateway" the claude
// job never used.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAClaudeHarnessExit14NamesTheSubscriptionRoute(t *testing.T) {
	h, _, handle, _ := newCollectHarness(t)
	payload, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(payload.State)
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	record.Harness = claudeSubHarness
	if err := st.writeAttempt(record); err != nil {
		t.Fatal(err)
	}
	h.door.setStatus("keh", 1, doorStatus{
		state:    subprocess.StateFailed,
		terminal: true,
		observations: []subprocess.Observation{{At: "2026-10-07T09:00:00Z", Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("the container's work process exited %d", sandboximage.ExitGatewayUnavailable)}},
	})
	if _, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	fault := collected.Infrastructure
	if fault == nil || fault.Persistent || !strings.Contains(fault.Service, "claude subscription's route") {
		t.Fatalf("fault %+v, want the transient claude subscription route", fault)
	}
	if strings.Contains(collected.Message, "model gateway") {
		t.Errorf("a claude-sub stop names the model gateway: %s", collected.Message)
	}
}
