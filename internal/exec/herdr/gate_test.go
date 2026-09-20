package herdr

// THE FIRST-ROUND-TRIP GATE'S OWN TESTS (tick x9x).
//
// The gate has one property the acceptance names and these tests pin from
// several sides: it claims only what it observed. Readiness is established
// over the protocol — herdr is asked to wait in the launch itself, and the
// reply is the acknowledgement. The three findings the tick distinguishes —
// not delivered, read truncated, unexpected answer — are three different
// values on the attempt record, each stated as an observation. A truncated
// read is its own finding and is never a failed agent: the acceptance's
// named test drives it below and asserts the attempt stays live.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// gateRecordOf resolves the started attempt's record and observation
// stream, so a test asserts on exactly what was durably recorded.
func gateRecordOf(t *testing.T, h *harness, handle *subprocess.JobHandle) (*attemptRecord, string) {
	t.Helper()
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := local.resolved()
	if err != nil {
		t.Fatal(err)
	}
	st := h.ex.storeAt(local.State)
	observations, _ := st.observationsFrom("")
	var b strings.Builder
	for _, obs := range observations {
		b.WriteString(obs.Detail)
		b.WriteString("\n")
	}
	return record, b.String()
}

// TestTheLaunchAsksHerdrToWaitForReadiness is readiness over the protocol:
// a startup budget inside herdr's accepted range is handed to agent.start as
// its own startup wait, so HERDR blocks until it has detected the agent and
// the reply IS the acknowledgement. The 250ms readiness poll of agent.get
// never runs — nothing is left to poll.
func TestTheLaunchAsksHerdrToWaitForReadiness(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ex.opts.StartupTimeout = 5 * time.Second
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}

	var startParams struct {
		TimeoutMs *uint64 `json:"timeout_ms"`
	}
	for _, req := range h.server.Requests() {
		if req.Method != herdtest.MethodAgentStart {
			continue
		}
		if err := json.Unmarshal(req.Params, &startParams); err != nil {
			t.Fatal(err)
		}
		break
	}
	if startParams.TimeoutMs == nil || *startParams.TimeoutMs != 5000 {
		t.Errorf("agent.start timeout_ms = %v, want the caller's 5s startup budget as herdr's own wait",
			startParams.TimeoutMs)
	}
	if count := h.server.CountMethod(herdtest.MethodAgentGet); count != 0 {
		t.Errorf("agent.get was called %d times: readiness was polled when herdr's own reply was the acknowledgement", count)
	}
	record, _ := gateRecordOf(t, h, handle)
	if !record.LaunchConfirmed {
		t.Error("the launch reported by herdr's ready reply is not confirmed on the record")
	}
}

// TestATruncatedGateReadIsNotAFailedAgent is the acceptance's named test: a
// stalled submission sends the gate to the last-resort pane read, the read
// comes back truncated — the window cut off exactly the lines that would
// have decided whether the prompt landed — and the finding is its own
// outcome. The agent is not reported failed: the spawn succeeded, the
// attempt is live and addressable, nothing is torn down, and no cause
// (auth, quota, a stale model string) is asserted anywhere.
func TestATruncatedGateReadIsNotAFailedAgent(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	// herdr accepted the prompt but saw no state change — delivery is
	// uncertain, which is the only road to the last-resort read.
	h.server.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_prompt_stalled", "herdr saw no state change")
	})
	// The pane's recent output would show the echo — but the read cuts its
	// own window off, and says so the only way the protocol can.
	h.server.SetPaneTexts("… text above the window cut …")
	h.server.SetPaneTruncated(true)

	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a truncated gate read failed the start: %v", err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	record, observations := gateRecordOf(t, h, handle)

	if !record.LaunchConfirmed {
		t.Error("the launch was confirmed and the record must say so: only the dispatch is the gate's question")
	}
	if record.DispatchConfirmed {
		t.Error("the record claims a confirmed dispatch: working was never observed")
	}
	if record.DispatchGate != GateReadTruncated {
		t.Errorf("the gate finding is %q, want %q: a read that cut off the echo is its own outcome, "+
			"never one of the others", record.DispatchGate, GateReadTruncated)
	}
	if !strings.Contains(observations, "truncated") {
		t.Errorf("the truncated read is missing from the observation stream:\n%s", observations)
	}
	if count := h.server.CountMethod(herdtest.MethodPaneRead); count != 1 {
		t.Errorf("pane.read was called %d times, want the one last-resort read", count)
	}

	// Not a failed agent: the attempt is live, addressable and untouched.
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.Terminal || status.State == subprocess.StateFailed {
		t.Errorf("state = %s: a truncated gate read is an observation, never a verdict about the agent", status.State)
	}
	h.assertNothingTornDown(t, "after the truncated gate read", local)

	// And the finding asserts no cause it never observed.
	for _, cause := range []string{"auth", "quota", "stale model"} {
		if strings.Contains(observations, cause) {
			t.Errorf("the observation stream asserts the cause %q, which nothing observed:\n%s", cause, observations)
		}
	}
}

// TestTheGateDistinguishesItsFindings drives each road through the gate and
// asserts the three outcomes the tick names — plus the two it allows — land
// as three-plus-two DIFFERENT values on the record, each from exactly the
// observations that produced it.
func TestTheGateDistinguishesItsFindings(t *testing.T) {
	shorttest.EndToEnd(t)
	stalledPrompt := func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, "agent_prompt_stalled", "herdr saw no state change")
	}
	for _, tt := range []struct {
		name string
		set  func(h *harness)
		want string
		// paneReads is how many pane.read calls the finding may rest on.
		paneReads int
	}{
		{
			name: "a refused submission is not delivered",
			set: func(h *harness) {
				h.server.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
					return herdtest.RespondErr(w, req.ID, "agent_not_ready", "the agent is not ready for input")
				})
			},
			want:      GateNotDelivered,
			paneReads: 0,
		},
		{
			name: "a stalled submission whose read fails stays unconfirmed",
			set: func(h *harness) {
				h.server.Route(herdtest.MethodAgentPrompt, stalledPrompt)
				h.server.Route(herdtest.MethodPaneRead, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
					return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "herdr has nothing to say")
				})
			},
			want:      GateUnconfirmed,
			paneReads: 1,
		},
		{
			name: "a stalled submission with no echo in a complete read is not delivered",
			set: func(h *harness) {
				h.server.Route(herdtest.MethodAgentPrompt, stalledPrompt)
				h.server.SetPaneTexts("$ the pane holds a shell prompt and nothing else")
			},
			want:      GateNotDelivered,
			paneReads: 1,
		},
		{
			name: "a stalled submission whose echo is in the pane answered unexpectedly",
			set: func(h *harness) {
				h.server.Route(herdtest.MethodAgentPrompt, stalledPrompt)
				h.server.SetPaneTexts("Write your report to this EXACT ABSOLUTE PATH:\n" +
					"    /worktrees/repo/runs/run-harness/tick-t1/attempt-1/RESULT-t1.md\n" +
					"the agent rendered something that was never work")
			},
			want:      GateUnexpectedAnswer,
			paneReads: 1,
		},
		{
			name: "an accepted prompt whose wait breaks stays unconfirmed",
			set: func(h *harness) {
				h.server.Route(herdtest.MethodAgentWait, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
					return herdtest.RespondErr(w, req.ID, "wait_broken", "the wait could not run")
				})
			},
			want:      GateUnconfirmed,
			paneReads: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			tt.set(h)
			handle, err := h.start("t1")
			if err != nil {
				t.Fatalf("no gate finding may fail the spawn: %v", err)
			}
			record, observations := gateRecordOf(t, h, handle)
			if record.DispatchGate != tt.want {
				t.Errorf("the gate finding is %q, want %q (observations below)\n%s",
					record.DispatchGate, tt.want, observations)
			}
			if !record.LaunchConfirmed {
				t.Error("the launch was confirmed and the record must say so: the gate is about the dispatch, not the agent")
			}
			if record.DispatchConfirmed {
				t.Error("no finding here may record a confirmed dispatch: working was never observed")
			}
			if count := h.server.CountMethod(herdtest.MethodPaneRead); count != tt.paneReads {
				t.Errorf("pane.read was called %d times, want %d", count, tt.paneReads)
			}
			// The finding is in the observation stream too, stated as the
			// observation it rests on.
			switch tt.want {
			case GateNotDelivered:
				if !strings.Contains(observations, "not delivered") {
					t.Errorf("the not-delivered finding is missing from the observation stream:\n%s", observations)
				}
			case GateUnexpectedAnswer:
				if !strings.Contains(observations, "not the expected work") {
					t.Errorf("the unexpected-answer finding is missing from the observation stream:\n%s", observations)
				}
			case GateUnconfirmed:
				if !strings.Contains(observations, "unconfirmed") {
					t.Errorf("the unconfirmed finding is missing from the observation stream:\n%s", observations)
				}
			}
		})
	}
}
