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

// Cancel revokes this attempt's credential, records the durable refusal to
// reissue, and then stops the process tree. It is idempotent: calling it twice
// returns the same acknowledgement, because the record it acknowledges is
// written once.
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

	// 1. REVOKE. The credential dies first, and it dies whether or not there
	//    is anything left to signal.
	if err := st.revokeCredential(); err != nil {
		return nil, fmt.Errorf("revoke the attempt credential: %w", err)
	}
	if !alreadyCancelled {
		_ = st.observe(Observation{At: e.stamp(), Kind: ObsCredentialRevoked,
			Detail: "revoked before any stop was requested"})
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
		_ = st.observe(Observation{At: e.stamp(), Kind: ObsCancelRequested,
			Detail: "stop requested after the credential was revoked"})
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
