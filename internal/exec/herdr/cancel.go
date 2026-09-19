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
// settled would interrupt nothing and record no refusal. Everywhere herdr
// cannot say the agent is spending — a provably gone agent, or a herdr that
// will not answer at all — the durable REPORT settles the question instead,
// exactly as the local executor's hasSettled reads it (tick 0c1): silence
// must not un-settle a worker that reported into a cancellation that renames
// its verdict.

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
	// ONE stamp for the acceptance, taken once and used for both the durable
	// record and the acknowledgement returned below. There used to be two:
	// the record took one here and the ack took another at the bottom, so the
	// first cancel returned a time it never wrote — and under load the two
	// straddled a second, so a SECOND cancel (which reads the record) returned
	// an EARLIER time than the first had (TestCancelIsIdempotent, seen failing
	// in epic ncv's close-out gate as '17:09:38 then 17:09:37'). The record was
	// always written once; it was the ack that disagreed with it.
	acceptedAt := e.stamp()
	if !alreadyCancelled && !settled {
		record := &cancelRecord{
			SchemaVersion: stateSchemaVersion,
			JobID:         h.JobID,
			Attempt:       h.Attempt,
			AcceptedAt:    acceptedAt,
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
		if settled {
			detail = "interrupted the agent with ctrl+c over an attempt that had settled itself: " +
				"recorded as the observation it is, never as a cancellation over the verdict"
		}
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
		// herdr would not answer. When the refusal was recorded, it is
		// durable and nothing can be dispatched over this attempt; when the
		// attempt had settled itself, no revocation was recorded and it is
		// settlement itself that refuses the reissue. Either way the
		// interrupt is reported as the operational failure it is, never
		// folded into a verdict.
		if err := st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsCancelRequested,
			Detail: "the interrupt could not be delivered: " + stopErr.Error()}); err != nil {
			return nil, fmt.Errorf("record the failed stop request: %w", err)
		}
		settledSentence := "the dispatch is revoked and reissue refused"
		if settled {
			settledSentence = "the attempt settled itself, so no cancellation was recorded over its verdict"
		}
		return nil, fmt.Errorf("%s, but the agent could not be "+
			"interrupted through herdr: %w", settledSentence, stopErr)
	}

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
// that a person is trying to stop". Everywhere herdr cannot say that — the
// agent is gone, or herdr will not answer at all — the DURABLE REPORT is
// read, exactly as the local executor reads it at this same place (tick 0c1):
// a worker that wrote its report finished the work, and a cancellation
// recorded over it would rename the verdict for everybody who ever looks at
// it again.
func (e *Executor) hasSettled(st *store) bool {
	record, err := st.readAttempt()
	if err != nil {
		// No attempt record: this executor has never seen the attempt,
		// which is exactly the case Cancel exists to be able to record a
		// refusal for.
		return false
	}
	// An unconfirmed launch is a liveness question, not an answer: herdr
	// may have completed the launch after the caller stopped waiting, so
	// the question below is asked rather than assumed — the same ask,
	// dispose and observe make.
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	switch {
	case err == nil:
		switch agent.AgentStatus {
		case client.StatusWorking, client.StatusBlocked:
			// Spending (bgr's operator stop): the cancel exists to stop
			// exactly this, whatever the report already says.
			return false
		default:
			return true
		}
	case gone(err):
		// herdr's POSITIVE answer that nobody is there (classify.go):
		// nothing is spending. Settled — and the departure is recorded
		// durably, so a later collect reads a verdict from it instead of
		// finding silence. Without this, a cancel over a provably gone
		// agent would write the cancellation record anyway and rename a
		// finished attempt's verdict `cancelled` for everybody who ever
		// looks at it again — a substrate answer deciding what the work's
		// verdict was.
		_ = st.markAgentGone(e.stamp())
		return true
	default:
		// herdr would not answer. The durable REPORT answers what silence
		// cannot: a worker that wrote its report finished the work, and
		// this attempt settled itself — a cancellation recorded over it
		// would rename its verdict `cancelled` for everybody who ever looks
		// again, the exact hazard this file's header names (0c1's line,
		// held by the local executor at this same place). Without a
		// report, silence is not evidence nothing is spending, so the full
		// refusal is recorded.
		if report, has := e.readReport(record); has && report.Status != "" {
			return true
		}
		return false
	}
}
