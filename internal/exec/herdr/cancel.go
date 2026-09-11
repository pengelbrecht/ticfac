package herdr

import (
	"context"
	"fmt"
	"os"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// cancel: REVOKE, then stop. Never the other way round, and never a teardown.
//
// A herdr attempt's credential is its DISPATCH — the agent, running on the
// operator's own substrate, spending whatever the agent's provider charges.
// Revocation is therefore the DURABLE REFUSAL TO REISSUE, recorded before
// any stop is requested so a cancel that is itself killed halfway through
// still leaves an attempt that can never boot again. The stop is the
// interrupt herdr itself offers a human: agent.send_keys with ctrl+c, the
// same surface `herdr agent send-keys` drives. It stops the SPENDING; the
// agent's process stays where it is until Dispose tears the workspace down,
// because closing the pane is teardown and not a stop.
//
// The one thing Cancel does NOT record is a cancellation of an attempt that
// had already settled itself. There is nothing left to stop there, and the
// record would outlive the truth: it is what inspect and collect read first,
// so it would rename a finished attempt's verdict `cancelled` for everybody
// who ever looks at it again — the exact hazard the local executor documents
// at the same place, and the reason the reconciler can safely call
// Cancel-then-Dispose on a MERGED attempt.
//
// An agent that is WORKING outranks the report here, and only here: a worker
// writes its report and then keeps going, and a cancel that treated it as
// settled would interrupt nothing and record no refusal.

// Cancel records this attempt's durable refusal to reissue, then interrupts
// the agent through herdr. It is idempotent: calling it twice returns the
// same acknowledgement, because the record it acknowledges is written once.
func (e *Executor) Cancel(h *subprocess.JobHandle) (*subprocess.CancelAck, error) {
	local, err := local(h)
	if err != nil {
		return nil, err
	}
	st := e.storeAt(local.State)

	// The refusal must survive an attempt whose state directory this
	// executor has never seen.
	if err := os.MkdirAll(local.State, 0o755); err != nil {
		return nil, err
	}

	existing, alreadyCancelled := st.cancelled()

	// The agent to interrupt is the one the handle names, or the one the
	// attempt record holds when the handle is the minimal adoption shape.
	target := local.AgentName
	if record, err := st.readAttempt(); err == nil && target == "" {
		target = record.AgentName
	}

	// An attempt that has already SETTLED ITSELF is one there is nothing
	// left to stop, and recording a stop over it is a sentence about an
	// event that never happened. The durable refusal is still recorded when
	// liveness cannot be answered — silence is not evidence nothing is
	// spending — but a settled, quiet attempt gets only the interrupt.
	settled := e.hasSettled(st)

	// 1. REVOKE: the durable refusal to reissue, written BEFORE any stop is
	//    requested, so a cancel that dies halfway still leaves an attempt
	//    that can never boot again.
	if !alreadyCancelled && !settled {
		record := &cancelRecord{
			SchemaVersion: stateSchemaVersion,
			JobID:         h.JobID,
			Attempt:       h.Attempt,
			AcceptedAt:    e.stamp(),
			Reissue:       subprocess.ReissueRefused,
			Order:         subprocess.OrderRevokeThenStop,
		}
		if err := st.writeJSON(fileCancel, record); err != nil {
			return nil, fmt.Errorf("record the durable refusal to reissue: %w", err)
		}
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsCredentialRevoked,
			Detail: "the dispatch was revoked and reissue refused, before any stop was requested"}); err != nil {
			return nil, fmt.Errorf("record the revocation: %w", err)
		}
	}

	// 2. THEN stop: the interrupt. Best effort by necessity — a herdr that
	//    will not answer cannot be made to — and reported either way, so the
	//    observation stream is where a person reads what actually happened.
	//    A stop over a settled attempt that somehow still has a live working
	//    agent is recorded as the observation it is.
	agent, stopErr := e.client.AgentSendKeys(context.Background(), client.AgentSendKeysParams{
		Target: target,
		Keys:   []string{"ctrl+c"},
	})
	stopRequested := false
	switch {
	case stopErr == nil:
		stopRequested = true
		detail := "interrupted the agent with ctrl+c after the dispatch was revoked"
		status := ""
		if agent != nil {
			status = fmt.Sprintf(" (herdr resolves it as %s)", agent.AgentStatus)
		}
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsCancelRequested,
			Detail: detail + status}); err != nil {
			return nil, fmt.Errorf("record the stop request: %w", err)
		}
	case client.IsCode(stopErr, client.CodeAgentNotFound), client.IsCode(stopErr, client.CodePaneNotFound):
		// The agent is already gone: nothing was interrupted, and that is
		// not an error — it is the state the stop exists to reach.
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsCancelRequested,
			Detail: "nothing to interrupt: herdr answers that the agent is no longer there"}); err != nil {
			return nil, fmt.Errorf("record the stop request: %w", err)
		}
		stopRequested = true
	default:
		// herdr would not answer. The revocation is durable and reissue is
		// refused, so nothing can be dispatched over this attempt; the
		// interrupt itself is reported as the operational failure it is,
		// never folded into a verdict.
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsCancelRequested,
			Detail: "the interrupt could not be delivered: " + stopErr.Error()}); err != nil {
			return nil, fmt.Errorf("record the failed stop request: %w", err)
		}
		return nil, fmt.Errorf("the dispatch is revoked and reissue refused, but the agent could not be "+
			"interrupted through herdr: %w", stopErr)
	}

	acceptedAt := e.stamp()
	if existing != nil {
		acceptedAt = existing.AcceptedAt
		stopRequested = stopRequested || existing.StopRequested
	}
	return &subprocess.CancelAck{
		SchemaVersion:      subprocess.SchemaVersion,
		JobID:              h.JobID,
		AcceptedAt:         acceptedAt,
		CredentialsRevoked: true,
		Order:              subprocess.OrderRevokeThenStop,
		Reissue:            subprocess.ReissueRefused,
		StopRequested:      stopRequested,
	}, nil
}

// hasSettled asks whether the attempt reached a terminal state OF ITS OWN —
// the same question inspect answers, from the same evidence, so "already
// settled" cannot mean one thing here and another there. A live, WORKING
// agent outranks the report here, and only here: inspect's question is "what
// does the durable evidence say", cancel's is "is there something spending
// that a person is trying to stop".
func (e *Executor) hasSettled(st *store) bool {
	record, err := st.readAttempt()
	if err != nil {
		// No attempt record: this executor has never seen the attempt,
		// which is exactly the case Cancel exists to be able to record a
		// refusal for.
		return false
	}
	if !record.LaunchConfirmed {
		return true
	}
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	if err != nil {
		// herdr would not answer. Silence is not evidence nothing is
		// spending, so this is NOT settled: the full refusal is recorded.
		return false
	}
	switch agent.AgentStatus {
	case client.StatusWorking, client.StatusBlocked:
		return false
	default:
		return true
	}
}
