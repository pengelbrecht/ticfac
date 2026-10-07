//go:build unix

package reconcile

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
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
	shell, err := startShell(dir, "sleep 60", time.Hour, time.Now(), lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The belt, for a test that fails before it collects its gate. Never
	// called once the gate HAS been collected: killGateGroup aimed at a pid
	// that was reaped would, on its group-ESRCH fallback, signal a number the
	// kernel may since have handed to somebody else (tick rmc's rule).
	collected := false
	defer func() {
		if collected {
			return
		}
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

	// The orphan is collected the way a run collects any gate — wait() — whose
	// kill goes on until nothing of the gate still holds the slot. The FIRST
	// kill can miss a member forked while it is delivered (gate.go's
	// settleKilledGate has the measurement), and the slot is what proves whether
	// one did: hand-rolling killGateGroup here, as this test did before 9si,
	// measured the race instead of closing it.
	_, _, _, waitErr := shell.wait()
	collected = true
	if !errors.Is(waitErr, errGateKilled) {
		t.Fatalf("the gate was collected with %v rather than killed", waitErr)
	}

	// And free again once the gate is gone — otherwise the hand-over would
	// simply burn a slot for the life of the host. wait() has already waited
	// for exactly this, so the fast path through the poll is one probe; the
	// poll is the belt over its give-up bound.
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

var (
	joinerOnce sync.Once
	joinerPath string
	joinerErr  error
)

// gateJoinerBinary builds the late-member fixture into the suite's own bin
// directory, once per suite — the same discipline as executorBinary above: a
// build in TestMain would bill every tick's gate seconds nothing runs under
// -short, and a build per test would bill it twice.
func gateJoinerBinary(t *testing.T) string {
	t.Helper()
	joinerOnce.Do(func() {
		root, err := contracts.RepoRoot()
		if err != nil {
			joinerErr = fmt.Errorf("locate the module root: %w", err)
			return
		}
		joinerPath = filepath.Join(binDir, "gatejoiner")
		build := exec.Command("go", "build", "-o", joinerPath, "./internal/reconcile/testdata/gatejoiner")
		build.Dir = root
		out, err := build.CombinedOutput()
		if err != nil {
			joinerErr = fmt.Errorf("build the gatejoiner fixture: %w\n%s", err, out)
		}
	})
	if joinerErr != nil {
		t.Fatal(joinerErr)
	}
	return joinerPath
}

// A gate's group kill can MISS a member, and the gate must not leave that
// member holding its slot.
//
// killpg walks the members that exist at the instant it is delivered. A fork
// in flight — the shell's own first child, on a loaded host — joins the group
// after the walk, unmarked, and never receives the signal: it survives the
// timeout that was meant to bound it, holding fd 3 (the slot) and running
// against the gated tree after the verdict is published. The same race
// internal/exec/subprocess measured on attempts (kill_repeat_test.go: 41 of
// 200 trials) and answered there with killUntilGone; the gate's kill path
// had a single signal and none of that.
//
// The kernel race cannot be scheduled on demand, so this test builds its
// OUTCOME with real system calls — no faked signal: the fixture (testdata/
// gatejoiner) starts as a member of the gate's group, leaves it before the
// kill is sent, and rejoins it after the kill has been delivered. A wait that
// signals once and reaps finds the group empty and its slot free, because the
// missed member is out of the group; a wait that keeps killing until the
// slot's own observable answers free — no matter where the missed member
// rejoined — does not return with the slot held.
func TestAGateWhoseKillMissesAMemberReleasesItsSlotAnyway(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	// One gate slot, exactly as gateWorktree makes one: a directory, its tree,
	// and an exclusive lock on a file beside the tree.
	root := t.TempDir()
	slot := filepath.Join(root, "slot-0")
	if err := os.MkdirAll(filepath.Join(slot, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(slot, "tree")
	slotLock := filepath.Join(slot, "lock")
	lock, err := lockGateSlot(slotLock)
	if err != nil {
		t.Fatal(err)
	}

	// The gate: a member that will be out of the group when the kill is
	// delivered, and in it afterwards, holding the slot the whole time.
	out, goMark := filepath.Join(root, "out"), filepath.Join(root, "go")
	command := fmt.Sprintf("%s %s %s & sleep 120", gateJoinerBinary(t), out, goMark)
	shell, err := startShell(tree, command, time.Hour, time.Now(), lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The belt, for a test that fails before it collects its gate: aimed at
	// the shell while it is still un-reaped, never at the pid a collected
	// gate has let the kernel hand out again (tick rmc's rule).
	collected := false
	defer func() {
		if collected {
			return
		}
		_ = killGateGroup(shell.cmd.Process.Pid)
		_, _ = shell.cmd.Process.Wait()
	}()

	// The observable the kill waits on: the member is OUT of the group.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(out); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the joiner never left the gate's group, so the gate's kill has no member to miss")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Collect the gate the way its bound does: one kill, then whatever the
	// slot still says is holding it. The joiner rejoins the group half a
	// second after GO — after this kill has been delivered.
	if err := os.WriteFile(goMark, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, waitErr := shell.wait()
	collected = true
	if !errors.Is(waitErr, errGateKilled) {
		t.Fatalf("the gate was collected with %v rather than killed", waitErr)
	}

	// THE ASSERTION: no member of the gate still holds the slot. The missed
	// member holds it for the life of its sleep on a wait that reaps after one
	// signal; a wait that went on killing until the slot answered free has
	// nothing left holding it.
	var freed *os.File
	deadline = time.Now().Add(5 * time.Second)
	for {
		freed, err = lockGateSlot(slotLock)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the slot was still held %s after the gate was collected: the kill left a member alive, "+
			"holding the slot out of the gate's reach", 5*time.Second)
	}
	_ = freed.Close()
}
