package herdr

import (
	"context"
	"fmt"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The wall clock, enforced rather than noticed.
//
// INHERITED, and deliberately not re-implemented here: the settlement
// deadline is the reconciler's (issued + WallSeconds + the wipe threshold), so
// a herdr worker that goes quiet is declared settled on schedule exactly as a
// local one is. The bound reaches this executor as spec.Limits.WallSeconds —
// an unbounded job is one nothing stops.
//
// NOT inherited: the kill. The local executor enforces its bound because it
// owns the process it started; a herdr worker's process belongs to HERDR, and
// nothing inherited stops it. Without the stop below, the reconciler would
// declare the attempt settled and move on while the agent keeps running and
// keeps spending — with the run's own records agreeing it is over. That is
// worse than no bound, because the operator believes in it.
//
// The enforcement runs inside observe — the one place the state is decided,
// reached by every Inspect and by every Start that adopts an existing
// attempt — because the reconciler's poll IS the clock that reaches the
// bound: at the poll cadence the stop lands within one poll of the deadline,
// well inside the reconciler's own settlement deadline. And it is the stop the
// local supervisor would have made, made through the only surface herdr
// offers: agent.send_keys with the interrupt chord, the same one Cancel uses.
//
// A stop at the wall clock says nothing about the WORK. It settles the
// attempt; the branch and the report are still read the usual way — a worker
// that reported before the stop caught it still collects as ready-to-merge,
// and the failure class the stop carries (wall_clock_exceeded) is the same
// closed vocabulary the local executor already collects.

// interruptChord is herdr's own interrupt, the surface `herdr agent send-keys`
// offers a human stopping an agent by hand. cancel.go sends the same chord.
const interruptChord = "ctrl+c"

// minStopProtocol is the OLDEST herdr protocol through which this executor
// can enforce a wall clock: agent.send_keys — the interrupt the stop is
// made with — is part of the wire vocabulary across the client's whole
// SUPPORTED range, so the floor is the line, not the pin (jv7 separated the
// two precisely so a supported-but-older server still works): the error
// codes this client knows were observed live against herdr 0.8.0 /
// protocol 19, the stop surface is there, and a bound issued against a
// protocol-19 herdr is enforceable. A server below the floor never reaches
// a dispatch at all — the client fails closed at construction — except
// through a downgrade observed mid-connection (session.snapshot re-reports
// the protocol), which is the one shape the dispatch-time refusal below is
// held for: a bound nothing is known able to enforce is not issued as a
// promise, and dispatch refuses it, naming the bound and the herdr version
// (see enforceableWall). The client's pin is a VERIFICATION line, never a
// support line: refusing a bound against a herdr the client otherwise
// supports strands every bounded dispatch on an older server as
// unenforceable when the stop surface it needs is right there.
const minStopProtocol = client.MinProtocolVersion

// wallDeadline is the moment the bound this attempt was issued fires, derived
// from the record's own provenance: issued + WallSeconds, the same arithmetic
// the reconciler's settlement deadline starts from. A record with no bound
// (WallSeconds zero) or one whose issue stamp cannot be read carries no
// deadline — nothing is enforced against a bound that was never issued, and a
// corrupt record is left to the reconciler's own deadline rather than guessed
// at here.
func wallDeadline(record *attemptRecord) (time.Time, bool) {
	if record.WallSeconds <= 0 {
		return time.Time{}, false
	}
	issued, err := time.Parse(time.RFC3339, record.IssuedAt)
	if err != nil || issued.IsZero() {
		return time.Time{}, false
	}
	return issued.Add(time.Duration(record.WallSeconds) * time.Second), true
}

// pastWall reports whether the bound has fired and the worker is still
// spending on this side of it.
func (e *Executor) pastWall(record *attemptRecord) bool {
	deadline, ok := wallDeadline(record)
	return ok && e.now().After(deadline)
}

// enforceableWall is the REFUSAL AT DISPATCH. A spec that carries a bound is
// issued only against a herdr through which this executor can stop an agent;
// a herdr older than the stop surface's verified floor answers nothing the
// enforcement can deliver, and a limit nothing enforces is a limit the
// operator trusts wrongly. The refusal names the bound and the herdr version
// it refuses, so the next repair goes to the upgrade — never to a retry.
//
// It is checked on the FRESH dispatch path only: an attempt being ADOPTED was
// issued its bound by an earlier incarnation that could enforce it, and
// refusing the adoption would strand a live worker whose spending nothing
// then addresses.
func (e *Executor) enforceableWall(spec *subprocess.JobSpec) error {
	if spec.Limits.WallSeconds <= 0 {
		return nil
	}
	info := e.client.ServerInfo()
	if info.Protocol >= minStopProtocol {
		return nil
	}
	return refuse(subprocess.RefusedUnenforceable,
		"the wall clock of %ds cannot be enforced through herdr %s (protocol %d): this executor stops an agent "+
			"with the agent.send_keys interrupt, which every protocol this client can speak carries (the floor is %d) — "+
			"refusing the dispatch rather than issuing a bound nothing would stop",
		spec.Limits.WallSeconds, info.Version, info.Protocol, minStopProtocol)
}

// stopAtWall stops the agent through herdr, and reports what the delivery
// observed — whether herdr took the interrupt, and whether the agent is
// positively gone afterwards. It is called from observe, on the live side
// of the wall clock: the agent is spending past the bound, and the stop
// ends the spending. A true `settled` without a `delivered` is the
// already-gone shape: herdr answered that nothing is there, which settles
// without a stop ever being claimed.
//
// The order is stop-accepted-then-marker, the reverse of the local
// supervisor's: through herdr the interrupt can be refused or undeliverable,
// and a wall marker claiming a stop that never landed is the run's own
// records agreeing the attempt is over while the agent keeps spending — the
// exact failure this enforcement exists to prevent. When the interrupt IS
// accepted, the marker is written at once — before the agent is confirmed
// gone, because "stopped at its wall clock" is a fact about the STOP, not
// about the exit — and it is what keeps "stopped at its wall clock" distinct
// from "merely settled" in every later inspect and collect, including the
// herdr-free ones.
//
// The ONE shape that writes no marker is herdr's answer that the agent is
// ALREADY GONE: an agent that exited on its own collected as
// wall_clock_exceeded — a sentence about an event that did not happen,
// x9x's rule (claim only what you observed) broken in this code. The
// positive answer settles the attempt as settled-on-its-own; no stop was
// delivered, so none is claimed and no marker says one was.
//
// A stop herdr would not take is an operational failure, never a verdict:
// no marker is written, the attempt reads as it read before (the agent is
// live), the failure is recorded as the observation it is, and the stop is
// re-delivered at the next poll. The reconciler's own settlement deadline —
// issued + wall + wipe threshold — remains the backstop that refuses an
// attempt nobody can say is running.

// wallStop is what ONE delivery of the stop observed: whether herdr took
// the interrupt, and whether the agent is positively gone after it. A
// settled stop without a delivered one is the already-gone shape above —
// settlement without a stop, never a stop that did not happen.
type wallStop struct {
	delivered bool
	settled   bool
}

func (e *Executor) stopAtWall(st *store, record *attemptRecord) wallStop {
	_, err := e.client.AgentSendKeys(context.Background(), client.AgentSendKeysParams{
		Target: record.AgentName,
		Keys:   []string{interruptChord},
	})
	switch {
	case client.IsCode(err, client.CodeAgentNotFound), client.IsCode(err, client.CodePaneNotFound):
		// The agent exited on its own in the window between the liveness
		// answer and the stop. That is a positive answer, and it settles —
		// but it is NOT a stop, so no wall marker is written and no
		// interrupt is claimed: the attempt reads as settled on its own,
		// never renamed "stopped at its wall clock".
		_ = st.markAgentGone(e.stamp())
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("the wall clock of %ds passed and herdr answered that the agent %s is no longer "+
				"there: the bound fired to find nothing left to stop — the agent settled on its own, and no stop is claimed",
				record.WallSeconds, record.AgentName)})
		return wallStop{delivered: false, settled: true}
	case err != nil:
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the wall clock of %ds passed but the agent %s could not be interrupted "+
				"through herdr (%v); the stop is re-delivered at every poll", record.WallSeconds, record.AgentName, err)})
		return wallStop{}
	}
	// herdr accepted the interrupt: the stop landed, and the marker is a
	// fact about THIS — written at once, before the exit is confirmed.
	if markErr := st.markWallExceeded(e.stamp()); markErr != nil {
		// The stop was accepted but the record of it did not land: the
		// observation stream is the durable place left, the same fallback
		// the dispatch confirmation takes.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: "the wall-clock stop could not be recorded: " + markErr.Error()})
	}
	_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
		Detail: fmt.Sprintf("stopped the agent %s at its wall clock of %ds: the interrupt was delivered through herdr",
			record.AgentName, record.WallSeconds)})
	if st.agentGone() {
		return wallStop{delivered: true, settled: true}
	}

	// The interrupt was accepted; ask herdr once whether the agent is gone.
	// An agent that is still exiting settles at the next poll, from the
	// durable marker already written above.
	agent, err := e.client.AgentGet(context.Background(), record.AgentName)
	switch {
	case client.IsCode(err, client.CodeAgentNotFound), client.IsCode(err, client.CodePaneNotFound):
		_ = st.markAgentGone(e.stamp())
		return wallStop{delivered: true, settled: true}
	case err == nil && agent != nil:
		return wallStop{delivered: true}
	default:
		// herdr would not answer. The stop is accepted and recorded; the
		// settlement waits for a poll that can positively observe the agent
		// gone, exactly as any settlement here does.
		return wallStop{delivered: true}
	}
}
