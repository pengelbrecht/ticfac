package subprocess

import (
	"fmt"
	"os"
	"time"
)

// cancel: REVOKE, then stop. Never the other way round, and never a teardown.
//
// The ordering is the contract's, and it is a value rather than two timestamps
// because a validator can refuse a wrong value and cannot refuse a wrong
// clock. The reason is money: a process torn down before its credential is
// revoked can spend on the way out, and a restart that finds a live credential
// resumes spending on a job an operator has already killed.
//
// The refusal is DURABLE. It is written before the signal, so a cancel that is
// itself killed halfway through still leaves an attempt that can never boot
// again.
//
// The one thing it does NOT record is a cancellation of an attempt that had
// already settled itself. There is nothing left to stop there, and the record
// would outlive the truth: it is what inspect and collect read first, so it
// would rename a finished attempt's verdict `cancelled` for everybody who ever
// looks at it again.

// Cancel revokes this attempt's credential, records the durable refusal to
// reissue, and then stops the process tree. It is idempotent: calling it twice
// returns the same acknowledgement, because the record it acknowledges is
// written once.
//
// On an attempt that has already settled it revokes and stops there: the
// credential dies, no refusal is recorded, and the ack says no stop was
// requested — because none was.
func (e *Executor) Cancel(h *JobHandle) (*CancelAck, error) {
	local, err := h.Local()
	if err != nil {
		return nil, err
	}
	st := e.storeAt(local.State)

	// The refusal must survive an attempt whose state directory this executor
	// has never seen: "cancelled" has to be recordable even when there is
	// nothing left to address.
	if err := os.MkdirAll(local.State, 0o755); err != nil {
		return nil, err
	}

	existing, alreadyCancelled := st.cancelled()

	// An attempt that has already SETTLED ITSELF is one there is nothing left
	// to stop, and recording a stop over it is a sentence about an event that
	// never happened. inspect reads cancel.json first, so a teardown that
	// cancelled a settled attempt made every later inspect of it answer
	// `cancelled` and every later collect answer `cancelled` too — and a
	// reconciler that re-adopted such an attempt then refused it over a
	// cancellation nobody performed instead of over the verdict it really had
	// (Appendix A #9: a refusal that sends the next repair at the wrong
	// problem). The credential still dies, because A1's rule is about money
	// and not about what the attempt did; nothing else is written.
	settled := !alreadyCancelled && e.hasSettled(st)

	// 1. REVOKE. The credential dies first, and it dies whether or not there
	//    is anything left to signal.
	if err := st.revokeCredential(); err != nil {
		return nil, fmt.Errorf("revoke the attempt credential: %w", err)
	}
	if !alreadyCancelled {
		// The observation stream is what a second process reads this
		// cancellation out of, so a lost append is reported rather than
		// swallowed. Cancel is idempotent — the revocation and the record it
		// acknowledges are written once — so a caller that retries on this
		// error re-records nothing and simply tries the append again.
		if err := st.observe(Observation{At: e.stamp(), Kind: ObsCredentialRevoked,
			Detail: "revoked before any stop was requested"}); err != nil {
			return nil, fmt.Errorf("record the revocation: %w", err)
		}
	}

	if settled {
		// Nothing to stop, and so nothing to record as stopped. A process that
		// somehow outlived its own settlement is still stopped — a cancel that
		// left one spending would be the failure this operation exists to
		// prevent — and THAT is recorded as an observation rather than as the
		// cancellation of an attempt that settled itself.
		if e.stopTree(st) {
			if err := st.observe(Observation{At: e.stamp(), Kind: ObsCancelRequested,
				Detail: "a process left over from an attempt that had already settled was stopped after the " +
					"credential was revoked"}); err != nil {
				return nil, fmt.Errorf("record the stop request: %w", err)
			}
		}
		return &CancelAck{
			SchemaVersion:      SchemaVersion,
			JobID:              h.JobID,
			AcceptedAt:         e.stamp(),
			CredentialsRevoked: true,
			Order:              OrderRevokeThenStop,
			Reissue:            ReissueRefused,
			StopRequested:      false,
		}, nil
	}

	record := existing
	if !alreadyCancelled {
		record = &cancelRecord{
			SchemaVersion: stateSchemaVersion,
			JobID:         h.JobID,
			Attempt:       h.Attempt,
			AcceptedAt:    e.stamp(),
			Reissue:       ReissueRefused,
			Order:         OrderRevokeThenStop,
		}
		if e.opts.SalvageWindow > 0 {
			deadline := e.now().UTC().Add(e.opts.SalvageWindow).Format(time.RFC3339)
			record.SalvageDeadline = &deadline
		}
		if err := e.opts.writeFile(st.path(fileCancel), mustJSON(record), 0o644); err != nil {
			return nil, fmt.Errorf("record the durable refusal to reissue: %w", err)
		}
	}

	// 2. THEN stop. Both process groups: the runner is its own group so that a
	//    wall clock can stop it without stopping the supervisor, which means a
	//    cancellation has to name both or it leaves the runner spending.
	stopped := e.stopTree(st)
	if stopped && !record.StopRequested {
		record.StopRequested = true
		if err := e.opts.writeFile(st.path(fileCancel), mustJSON(record), 0o644); err != nil {
			return nil, err
		}
		if err := st.observe(Observation{At: e.stamp(), Kind: ObsCancelRequested,
			Detail: "stop requested after the credential was revoked"}); err != nil {
			return nil, fmt.Errorf("record the stop request: %w", err)
		}
	}

	return &CancelAck{
		SchemaVersion:      SchemaVersion,
		JobID:              h.JobID,
		AcceptedAt:         record.AcceptedAt,
		CredentialsRevoked: true,
		Order:              OrderRevokeThenStop,
		Reissue:            ReissueRefused,
		StopRequested:      record.StopRequested,
		SalvageDeadline:    record.SalvageDeadline,
	}, nil
}

// hasSettled asks whether the attempt reached a terminal state OF ITS OWN —
// the same question inspect answers, from the same durable evidence, so that
// "already settled" cannot mean one thing here and another there.
func (e *Executor) hasSettled(st *store) bool {
	record, err := st.readAttempt()
	if err != nil {
		// No attempt record: this executor has never seen the attempt, which is
		// exactly the case Cancel exists to be able to record a refusal for.
		return false
	}
	state, _ := e.observe(st, record)
	return terminalState(state)
}

// stopTree stops everything this attempt started, and says whether there was
// anything to stop.
func (e *Executor) stopTree(st *store) bool {
	stopped := false
	for _, pid := range []int{st.runnerPID(), st.supervisorPID()} {
		if pid > 0 && processAlive(pid) {
			stopTree(pid)
			stopped = true
		}
	}
	return stopped
}

func mustJSON(value any) []byte {
	raw, err := jsonIndent(value)
	if err != nil {
		return []byte("{}\n")
	}
	return raw
}
