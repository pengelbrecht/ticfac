package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// The escalation the wall clock was missing (tick rj0): the interrupt is a
// courtesy, and an agent that never reads it — because it is inside a long
// shell call — is stopped by closing its pane, within a bounded grace, with
// the worktree snapshotted first so the stop does not destroy the work.
//
// The acceptance, both halves:
//   - an agent that IGNORES the interrupt is closed within the grace and
//     settles as stopped-at-wall-clock;
//   - an agent that HONOURS the interrupt is never closed.

// TestAnAgentThatIgnoresTheInterruptIsClosedAtTheGrace is the Phase 3 shape
// end to end (tick emk's failure, repaired): a real agent process reports
// working and never reads the interrupt file, exactly the agent inside a
// shell call that the wall clock's ctrl+c cannot reach. Within the grace the
// interrupt is re-delivered and nothing else happens; once the grace has
// elapsed, the pane is closed through herdr — which kills the process the
// pane runs, the whole point of the stop — the worktree's uncommitted work
// is preserved on a ref of its own BEFORE the close, and the attempt
// settles as stopped at its wall clock.
func TestAnAgentThatIgnoresTheInterruptIsClosedAtTheGrace(t *testing.T) {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: "ignore"})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}

	// Uncommitted work, the thing the close would destroy: one untracked
	// file worth keeping, plus writes under the boundary paths that must
	// NOT ride the snapshot — a tracker record and a report-shaped file in
	// the attempt's own artifact space. And one commit already landed on
	// the attempt branch — the timed-durability story a real worker stopped
	// mid-run has (and the shape the no-commits check is ordered against,
	// exactly as the interrupt-era test arranges it).
	mustRun(t, local.Worktree, "git", "commit", "--quiet", "--allow-empty", "-m", "tick t1: mid-work")
	precious := filepath.Join(local.Worktree, "uncommitted.txt")
	if err := os.WriteFile(precious, []byte("forty minutes of work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(local.Worktree, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, ".tick", "issues", "evil.json"),
		[]byte("{\"id\": \"evil\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(local.Worktree, "runs", "run-harness", "tick-t1", "attempt-1")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A report-shaped file in the attempt's own artifact space, at a name
	// that is NOT the report path the record owns: the attempt has not
	// reported, and the artifact prefix must still be excluded from the
	// snapshot.
	if err := os.WriteFile(filepath.Join(reportDir, "DRAFT-RESULT-t1.md"), []byte("## Draft\n\nSTATUS: nothing yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ---- past the bound, inside the grace: the interrupt, and nothing else
	clock.advance(301 * time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Fatalf("state = %s inside the grace, want running: the interrupt has been delivered but the agent has not exited", status.State)
	}
	sent := h.server.SendKeysCalls()
	if len(sent) == 0 {
		t.Fatal("the worker ran past its wall clock and herdr was never asked to stop it")
	}
	if keys := strings.Join(sent[0].Keys, ","); keys != "ctrl+c" {
		t.Errorf("the stop was sent as %q, want ctrl+c: the same interrupt surface cancel uses", keys)
	}
	if closes := h.paneCloses(); len(closes) != 0 {
		t.Errorf("the pane was closed at %v INSIDE the grace: an agent must be allowed the grace to honour the interrupt", closes)
	}
	if _, ok := h.ex.storeAt(local.State).wipSnapshot(); ok {
		t.Error("a wip snapshot was taken inside the grace: nothing has been closed, and no work is at risk yet")
	}

	// The agent process is still running — the interrupt it never read has
	// stopped nothing.
	if spawned, exited, _ := h.agentProcess(); !spawned || exited {
		t.Fatal("the ignoring agent is not a live process: the close would be proving nothing")
	}

	// ---- the grace elapses with the agent still live: the close is the stop
	clock.advance(stopGrace + time.Second)
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s past the grace, want failed: an agent that ignores the interrupt is closed and settles as stopped at its wall clock", status.State)
	}
	if !status.Terminal {
		t.Error("the closed worker's settlement is terminal for the attempt")
	}
	detail := lastDetail(status.Observations)
	if !strings.Contains(detail, wallWording) || !strings.Contains(detail, "closing the pane") {
		t.Errorf("the settlement reads %q, want the wall-clock wording %q naming the pane close", detail, wallWording)
	}
	if !strings.Contains(detail, "unhonoured") {
		t.Errorf("the settlement reads %q, want it to say the interrupt went unhonoured for the grace", detail)
	}

	// The close went through herdr, at the agent's pane, exactly once.
	closes := h.paneCloses()
	if len(closes) != 1 {
		t.Fatalf("pane.close calls = %v, want exactly one close of the agent's pane", closes)
	}
	if closes[0] != "w1:p1" {
		t.Errorf("the closed pane was %q, want the agent's pane w1:p1", closes[0])
	}

	// The stop actually stopped the process herdr owns: closing the pane
	// killed the agent, which is the difference between this stop and the
	// interrupt Phase 3 watched fail for 26 minutes.
	if !waitForOr(t, "the closed pane's agent process to exit", 5*time.Second, func() bool {
		_, exited, _ := h.agentProcess()
		return exited
	}) {
		h.dumpAgent(t)
		t.Fatal("the pane was closed but the agent process is still running: the stop stopped nothing")
	}

	// ---- the work survived the stop: the snapshot, taken BEFORE the close
	snap, ok := h.ex.storeAt(local.State).wipSnapshot()
	if !ok {
		t.Fatal("the pane was closed with no record of where the uncommitted work was preserved")
	}
	if snap.Ref == "" || snap.Commit == "" {
		t.Fatalf("the wip snapshot record is empty: %+v", snap)
	}
	if got, ok := showFile(local.Worktree, snap.Commit, "uncommitted.txt"); !ok || got != "forty minutes of work" {
		t.Errorf("the snapshot's uncommitted.txt reads %q (present %t), want the work as it stood at the stop", got, ok)
	}
	atRef := strings.TrimSpace(mustRun(t, local.Worktree, "git", "rev-parse", snap.Ref))
	if atRef != snap.Commit {
		t.Errorf("the recorded ref %s points at %s, want the recorded commit %s", snap.Ref, atRef, snap.Commit)
	}
	// The boundary exclusions ride along as exclusions: the agent's
	// tracker write and its report-shaped file are NOT in the snapshot.
	if got, ok := showFile(local.Worktree, snap.Commit, ".tick/issues/evil.json"); ok {
		t.Errorf("the snapshot carries the agent's write under .tick/: %q — the boundary excludes it from riding along", got)
	}
	if got, ok := showFile(local.Worktree, snap.Commit, "runs/run-harness/tick-t1/attempt-1/DRAFT-RESULT-t1.md"); ok {
		t.Errorf("the snapshot carries a file from the attempt's own artifact space: %q — the artifact prefix is excluded and the report is archived separately", got)
	}

	// The settlement is durable, and herdr can go quiet without changing it.
	h.server.Close()
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s after herdr went quiet, want failed from the executor's own records", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) {
		t.Errorf("the quiet-substrate settlement reads %q, want the wall-clock wording %q", detail, wallWording)
	}

	// Collect reads the stop as the failure class the closed vocabulary
	// already names — never as a cancellation, and never as the
	// liveness-unknown hold.
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome = %s, want failed: a stopped worker that reported nothing failed", collected.Result.Outcome)
	}
	if collected.Result.FailureClass != subprocess.FailureWallClockExceeded {
		t.Errorf("failure class = %q, want %q: the same closed vocabulary the local executor collects",
			collected.Result.FailureClass, subprocess.FailureWallClockExceeded)
	}
	if !strings.Contains(collected.Message, wallWording) {
		t.Errorf("collect message = %q, want the wall-clock wording %q", collected.Message, wallWording)
	}
}

// TestAnAgentThatHonoursTheInterruptIsNeverClosed is the other half of the
// acceptance, and the reason the grace exists (tick 55i): an agent that
// reads the interrupt exits on its own — settles through the ordinary
// positive answer — and the escalation never fires. No pane is closed, and
// no snapshot is taken, because nothing was ever at risk from a close.
func TestAnAgentThatHonoursTheInterruptIsNeverClosed(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	h.routeAgentGoneAfterInterrupt(t)

	// The bound fires, the interrupt is accepted, and the agent exits on
	// it — the settlement every poll before the grace would have found.
	clock.advance(301 * time.Second)
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Fatalf("state = %s, want failed: the agent honoured the interrupt and settled", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) {
		t.Errorf("the settlement reads %q, want the wall-clock wording %q", detail, wallWording)
	}
	if closes := h.paneCloses(); len(closes) != 0 {
		t.Errorf("the pane was closed at %v for an agent that honoured the interrupt: the grace exists so this never happens", closes)
	}
	if _, ok := h.ex.storeAt(local.State).wipSnapshot(); ok {
		t.Error("a wip snapshot was taken for an agent that exited on the interrupt: nothing was closed, no work was at risk")
	}

	// And it stays that way: polls past the grace find the attempt already
	// settled, and no escalation fires on a settled attempt.
	clock.advance(10 * stopGrace)
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s after further polls, want failed: a settled attempt stays settled", status.State)
	}
	if closes := h.paneCloses(); len(closes) != 0 {
		t.Errorf("a pane was closed at %v after the attempt had already settled on the interrupt", closes)
	}
}

// TestTheCloseSettlesOnThePositiveAnswerNotOnItsOwnAcceptance pins the
// live-observed fact the close's confirmation step exists for: herdr
// answers ok and opens a FRESH SHELL PANE in the closed pane's workspace,
// so the close alone settles nothing. The attempt settles only when
// agent.get answers that the agent is gone — and a herdr that still resolves
// the agent after the close holds the settlement to the next poll rather
// than claiming the stop worked.
func TestTheCloseSettlesOnThePositiveAnswerNotOnItsOwnAcceptance(t *testing.T) {
	h := newHarness(t, harnessOptions{paneCloseLingers: true})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")

	// The interrupt is accepted; the grace runs out.
	clock.advance(301 * time.Second)
	if status, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatal(err)
	} else if status.State != subprocess.StateRunning {
		t.Fatalf("state = %s, want running: the interrupt is delivered and awaits the exit", status.State)
	}
	clock.advance(stopGrace + time.Second)

	// The close is accepted, but agent.get STILL answers the agent is
	// live — herdr's records have not caught up with the pane. Nothing is
	// settled on the close's own ok.
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Fatalf("state = %s after a close herdr accepted but has not confirmed, want running: "+
			"the close settles nothing on its own", status.State)
	}
	if closes := h.paneCloses(); len(closes) != 1 {
		t.Fatalf("pane.close calls = %v, want the one escalation attempt", closes)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, "waits for herdr to answer that the agent is gone") {
		t.Errorf("the in-flight close reads %q, want it to say the settlement waits for herdr's positive answer", detail)
	}

	// herdr catches up: the next poll's re-attempted close answers the pane
	// is gone, and the attempt settles as stopped at its wall clock.
	h.server.Route(herdtest.MethodPaneClose, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return herdtest.RespondErr(w, req.ID, herdtest.CodePaneNotFound, "the pane is no longer there")
	})
	status, err = h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateFailed {
		t.Errorf("state = %s once herdr answers the closed pane is gone, want failed", status.State)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, wallWording) {
		t.Errorf("the settlement reads %q, want the wall-clock wording %q", detail, wallWording)
	}
}

// TestASnapshotThatCannotLandHoldsTheClose is pbb's rule enforced in the
// close's own path: the close is the one destructive step in the wall
// clock's enforcement, and it never destroys work it has not preserved. A
// worktree that cannot be snapshotted holds the close — reported as the
// observation it is, re-attempted at every poll — rather than proceeding
// into a stop that throws the work away to save the clock.
func TestASnapshotThatCannotLandHoldsTheClose(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	clock := &wallClock{t: time.Now().UTC()}
	h.ex.now = clock.now
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")

	// The interrupt is accepted...
	clock.advance(301 * time.Second)
	if status, err := h.ex.Inspect(handle, ""); err != nil {
		t.Fatal(err)
	} else if status.State != subprocess.StateRunning {
		t.Fatalf("state = %s, want running: the interrupt is delivered and awaits the exit", status.State)
	}
	// ...and the worktree breaks before the grace elapses: git in the
	// worktree can no longer run, so no snapshot can land.
	if err := os.Remove(filepath.Join(local.Worktree, ".git")); err != nil {
		t.Fatal(err)
	}
	clock.advance(stopGrace + time.Second)

	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("state = %s with the snapshot unable to land, want running: the close is held, never a verdict", status.State)
	}
	if closes := h.paneCloses(); len(closes) != 0 {
		t.Errorf("the pane was closed at %v with the worktree unpreserved: the close never destroys work it has not snapshotted", closes)
	}
	if _, ok := h.ex.storeAt(local.State).wipSnapshot(); ok {
		t.Error("a snapshot was recorded although none could land")
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, "close is held") {
		t.Errorf("the held close reads %q, want it to say the close is held this poll", detail)
	}
}
