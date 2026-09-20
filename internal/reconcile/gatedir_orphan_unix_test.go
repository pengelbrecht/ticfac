//go:build unix

package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A gate that outlives the reconciler that started it goes on holding its slot.
//
// This is the one way a reused directory could still be contaminated, and it is
// not hypothetical: the gate's shell runs in a process group of its own so that
// the timeout can take its children with it (gate_kill_unix_test.go), which
// also means a reconciler that is KILLED leaves that shell running. Every lock
// the dead reconciler held is released by the kernel at once. Without the
// hand-over in gateWorktree, the next run would take the slot and reset the
// tree under a suite still reading it — and the verdict it then published would
// be about a tree that changed underneath it.
//
// The hand-over is that the shell holds the same lock. A lock belongs to the
// open file DESCRIPTION, so it survives being passed to a child and is only
// released when every process holding it is gone — which is exactly when the
// directory becomes safe to reuse.
func TestAGateThatOutlivesItsReconcilerKeepsHoldingItsSlot(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	dir, lock, release, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	if lock == nil {
		t.Fatal("the gate was handed no slot lock, so there is nothing to hand to the gate it starts")
	}
	shell, err := startShell(dir, "sleep 60", time.Hour, time.Now(), lock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = killGateGroup(shell.cmd.Process.Pid)
		_, _ = shell.cmd.Process.Wait()
	}()

	// The reconciler lets go of everything it holds — which is all its death
	// would do, and all a clean finish does either.
	release()

	slotLock := filepath.Join(filepath.Dir(dir), "lock")
	taken, err := lockGateSlot(slotLock)
	if err == nil {
		_ = taken.Close()
		t.Fatal("the slot was free while the gate holding it was still running: the next gate would have reset " +
			"this tree under a suite that is still reading it")
	}
	if !errors.Is(err, errGateSlotBusy) {
		t.Fatalf("the slot answered %v rather than busy; this test cannot tell the hand-over from a broken lock", err)
	}

	// And free again once the gate is gone — otherwise the hand-over would
	// simply burn a slot for the life of the host.
	if err := killGateGroup(shell.cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	_, _ = shell.cmd.Process.Wait()
	var freed *os.File
	deadline := time.Now().Add(5 * time.Second)
	for {
		freed, err = lockGateSlot(slotLock)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the slot was still held %s after the gate died: %v", 5*time.Second, err)
	}
	_ = freed.Close()
}
