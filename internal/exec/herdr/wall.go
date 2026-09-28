package herdr

import (
	"context"
	"fmt"
	"os"
	"strings"
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
// local supervisor would have made, made through the surfaces herdr offers:
// the agent.send_keys interrupt the local Cancel uses — the harness's own
// key (interrupt.go) — and, once that courtesy has gone unhonoured for a
// grace, or has been honoured by an agent that then sits at its prompt,
// pane.close, the stop that takes the agent herdr owns with it (tick rj0: an
// agent inside a long shell call never reads the interrupt, and a bound only
// the reconciler's own deadline notices is not a bound).
//
// A stop at the wall clock says nothing about the WORK. It settles the
// attempt; the branch and the report are still read the usual way — a worker
// that reported before the stop caught it still collects as ready-to-merge,
// and the failure class the stop carries (wall_clock_exceeded) is the same
// closed vocabulary the local executor already collects.

// The interrupt is the harness's own (interrupt.go): Escape for pi, claude
// and codex, herdr's generic ctrl+c for a kind nobody has verified. cancel.go
// sends the same keys.

// stopGrace is the courtesy the interrupt is given before the enforcement
// escalates to closing the pane (tick rj0). An agent that DOES honour the
// interrupt — the tick 55i shape — needs the window to cancel its turn,
// return to its prompt and write its report; the close must never beat that
// exit. Two minutes bounds the window from the other side: the Phase 3 run
// (tick emk) watched an agent run TWENTY-SIX minutes past its bound while the
// interrupt was re-delivered at every poll and nothing else would ever stop
// it — the grace is the difference between a stop that lands and a bound
// only the reconciler's own settlement deadline notices. The grace is
// measured from the interrupt's ACCEPTANCE (the wall marker's stamp), it
// lands well inside the reconciler's settlement deadline, and it is a named
// constant rather than a multiple of the poll interval because what it
// bounds — an agent finishing its exit — is not cadenced by the poll at all.
//
// The grace is for an agent that is still WORKING. An agent herdr answers is
// back at its prompt after the interrupt (honouredInterrupt) has honoured
// it: an interactive harness interrupted mid-turn aborts the turn and waits
// for input — it neither exits nor writes a report — so the grace would only
// be two more minutes of a pane nobody will ever use. The escalation to the
// close then proceeds at the next poll after the interrupt was accepted.
const stopGrace = 2 * time.Minute

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
			"with the agent.send_keys interrupt and, when it goes unhonoured, pane.close — surfaces every protocol this "+
			"client can speak carries (the floor is %d) — refusing the dispatch rather than issuing a bound nothing "+
			"would stop",
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
// attempt nobody can say is running. The escalation is the same: a close
// herdr refuses, or a snapshot that cannot precede it, HOLDS this poll
// (wallStop.held) and is re-attempted at the next, never settling anything.

// wallStop is what ONE delivery of the stop observed: whether herdr took a
// stop (the interrupt, or the close it escalates to), and whether the agent
// is positively gone after it. A settled stop without a delivered one is
// the already-gone shape — settlement without a stop, never a stop that did
// not happen. `closed` marks the escalation's landing: the stop that settled
// (or was accepted and awaits confirmation) was pane.close, not the
// interrupt. `held` is the escalation's operational hold: this poll could
// not proceed toward the close, and why — the close is re-attempted at the
// next poll, and the observation stream carries the sentence.
//
// `honoured` marks a close made because the agent honoured the interrupt
// and sat idle at its prompt, rather than because the grace ran out.
type wallStop struct {
	delivered bool
	settled   bool
	closed    bool
	honoured  bool
	held      string
}

// why is the clause that says what the escalation to the close answered.
func (w wallStop) why() string {
	if w.honoured {
		return "the agent honoured the interrupt and is back at its prompt"
	}
	return "the interrupt went unhonoured for the grace"
}

// status is herdr's answer about the agent at this poll, when the caller
// has one ("" when it does not): an agent that is no longer working after an
// accepted interrupt has honoured it, and is closed without waiting out the
// grace.
func (e *Executor) stopAtWall(st *store, record *attemptRecord, paneID string, status client.AgentStatus) wallStop {
	// The escalation (tick rj0). A PREVIOUS poll's interrupt was accepted
	// (the wall marker carries its stamp), the grace has elapsed, and
	// herdr still answers that the agent is live — this stopAtWall call is
	// reached from observe only with a live agent (or a reporting one the
	// bound is still owed). The interrupt is a courtesy an agent inside a
	// long shell call never reads; the close is the stop that actually
	// stops the agent herdr owns. While the grace runs, the interrupt is
	// re-delivered below exactly as before.
	if accepted, ok := st.wallStopAcceptedAt(); ok {
		if honouredInterrupt(status) {
			// An EARLIER poll's interrupt was accepted and the agent has
			// left `working`: it honoured the interrupt and sits at its
			// prompt. Nothing more will happen in the pane on its own.
			return e.closeAtWall(st, record, paneID, true)
		}
		if e.now().After(accepted.Add(stopGrace)) {
			return e.closeAtWall(st, record, paneID, false)
		}
	}
	_, err := e.client.AgentSendKeys(context.Background(), client.AgentSendKeysParams{
		Target: record.AgentName,
		Keys:   interruptKeys(record.Kind),
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
		Detail: fmt.Sprintf("stopped the agent %s at its wall clock of %ds: the interrupt (%s, the harness's own) was delivered through herdr",
			record.AgentName, record.WallSeconds, strings.Join(interruptKeys(record.Kind), " "))})
	if st.agentGone() {
		return wallStop{delivered: true, settled: true}
	}

	// The interrupt was accepted; ask herdr once whether the agent is gone.
	// An agent that is still exiting settles at the next poll, from the
	// durable marker already written above — or, once the grace elapses,
	// from the close.
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

// closeAtWall is the escalation the grace ran out on: the interrupt went
// unhonoured, and the stop is now pane.close — the surface that takes the
// agent herdr owns with it, whatever the agent is in the middle of.
//
// The close is the one destructive step in the wall clock's enforcement,
// so it is gated on the work being PRESERVED first (tick pbb): the snapshot
// below puts the worktree's uncommitted state on a ref of its own before
// anything destroys it, and a snapshot that cannot land holds the close —
// the enforcement re-attempts at every poll, and the reconciler's own
// settlement deadline remains the backstop, but an enforcement that
// destroyed the work to save the clock would be a worse failure than the
// stall it replaced.
//
// The close settles nothing on its own, and that is a live-observed fact,
// not a guess: herdr answers ok and opens a FRESH SHELL PANE in the
// workspace the closed pane lived in, so the settlement this step settles
// on is herdr's positive answer about the AGENT — agent.get after the
// close, exactly the confirmation the tick's acceptance names. A
// pane_not_found on the close itself is the same positive answer one poll
// earlier: the agent (and its pane) were already gone.
//
// honoured says WHY the close is happening: the agent honoured the
// interrupt and sits idle at its prompt (the close is the tidy end of a
// stop that already landed), or the interrupt went unhonoured for the grace
// (the close is the stop). The mechanics are the same; the sentences differ,
// because the record must say which one happened.
func (e *Executor) closeAtWall(st *store, record *attemptRecord, paneID string, honoured bool) wallStop {
	why := "the interrupt went unhonoured for the grace"
	if honoured {
		why = "the agent honoured the interrupt and is back at its prompt, where it would sit until the grace ran out"
	}
	if paneID == "" {
		// Nothing addresses the pane. Defensive — the record carries the
		// pane worktree.create handed back — but a close into the void is
		// not a stop, and the hold says so.
		what := "the agent's pane is in none of the records this attempt holds"
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the wall clock of %ds fired, %s, and the pane close is held: %s",
				record.WallSeconds, why, what)})
		return wallStop{held: what, honoured: honoured}
	}
	if err := e.snapshotUncommitted(st, record); err != nil {
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the wall clock of %ds fired, %s, and the pane close is held: "+
				"the worktree could not be snapshotted first (%v) — the close never destroys work it has not preserved",
				record.WallSeconds, why, err)})
		return wallStop{held: "the worktree snapshot failed: " + err.Error(), honoured: honoured}
	}
	err := e.client.PaneClose(context.Background(), client.PaneCloseParams{PaneID: paneID})
	switch {
	case gone(err):
		// The pane was already gone — the agent exited (or was torn down) in
		// the window between the liveness answer and the close. A positive
		// answer, recorded as one; the wall marker written at the accepted
		// interrupt keeps the settlement reading as a stop at the bound.
		_ = st.markAgentGone(e.stamp())
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("stopped the agent %s at its wall clock of %ds: %s, and herdr answered the pane %s was already gone when the close fired",
				record.AgentName, record.WallSeconds, why, paneID)})
		return wallStop{delivered: true, settled: true, closed: true, honoured: honoured}
	case err != nil:
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the wall clock of %ds fired, %s, and the pane %s could not be closed through herdr (%v): the close is re-attempted at every poll",
				record.WallSeconds, why, paneID, err)})
		return wallStop{held: "herdr would not take the close: " + err.Error(), honoured: honoured}
	}
	// The close was accepted. Confirm the AGENT is gone — the positive
	// answer the stop settles on; the pane herdr opened in its place is a
	// shell, and nothing about it is this attempt's agent.
	_, gerr := e.client.AgentGet(context.Background(), record.AgentName)
	switch {
	case gone(gerr):
		_ = st.markAgentGone(e.stamp())
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
			Detail: fmt.Sprintf("stopped the agent %s at its wall clock of %ds: %s, so the pane %s was closed through herdr and herdr answers the agent is gone",
				record.AgentName, record.WallSeconds, why, paneID)})
		return wallStop{delivered: true, settled: true, closed: true, honoured: honoured}
	case gerr == nil:
		// herdr still resolves the agent — the close was accepted but the
		// agent outlives it in herdr's records. Not settled; the next poll
		// re-attempts the close (a second close answers pane_not_found and
		// settles), exactly as an operational hold does.
		_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsHeartbeat,
			Detail: fmt.Sprintf("the pane %s was closed through herdr but herdr still answers the agent %s is live: the settlement waits for a positive answer that the agent is gone",
				paneID, record.AgentName)})
		return wallStop{delivered: true, closed: true, honoured: honoured}
	default:
		// herdr would not answer. The close is accepted and recorded; the
		// settlement waits for a poll that can positively observe the agent
		// gone, exactly as any settlement here does.
		return wallStop{delivered: true, closed: true, honoured: honoured}
	}
}

// snapshotUncommitted preserves the attempt's worktree — every uncommitted
// change, tracked or untracked — on a ref of its own, ONCE, recording where
// (tick rj0, per pbb). It is the precondition of the pane close: the close
// destroys the checkout's unsaved state, and the snapshot is what keeps the
// stop from destroying the work. A worktree that no longer exists has
// nothing left to destroy — the close proceeds; a snapshot that fails holds
// the close, because the alternative is an enforcement that throws the work
// away to save the clock.
//
// The mechanics and the ref naming are the seam's (subprocess.WipRefFor /
// subprocess.SnapshotWorktree): the local executor's dispose gates its
// force-remove on the same ones, and a record at one name and shape is what
// a later attempt's dispatch points its worker at (tick pbb).
//
// The snapshot is NOT evidence of completion and is never merged; it is
// material a person or a later attempt can be pointed at (pbb, and nvn),
// which is why where it landed is recorded durably beside the attempt.
func (e *Executor) snapshotUncommitted(st *store, record *attemptRecord) error {
	if _, ok := st.wipSnapshot(); ok {
		return nil // already preserved; the close may proceed
	}
	if record.Worktree == "" {
		return nil // no worktree was ever recorded: there is nothing to destroy
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		if os.IsNotExist(err) {
			return nil // the worktree is already gone: nothing left to destroy
		}
		return fmt.Errorf("stat the worktree at %s: %w", record.Worktree, err)
	}
	ref := subprocess.WipRefFor(record.JobID)
	commit, err := subprocess.SnapshotWorktree(record.Worktree, ref, record.Spec.ArtifactPrefix)
	if err != nil {
		return err
	}
	if err := st.markWIPSnapshot(subprocess.WIPSnapshot{Ref: ref, Commit: commit, TakenAt: e.stamp()}); err != nil {
		return err
	}
	_ = st.observe(subprocess.Observation{At: e.stamp(), Kind: subprocess.ObsExited,
		Detail: fmt.Sprintf("preserved the uncommitted work of attempt %d of %s on %s before the pane close: the snapshot is material a later attempt can be pointed at, never evidence of completion",
			record.Attempt, record.JobID, ref)})
	return nil
}
