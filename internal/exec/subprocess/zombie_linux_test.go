//go:build linux

package subprocess

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What "dead" has to mean on the host this repo's gate runs on: the proofs
// here reproduce the factory container's condition in-process — a killed
// child nobody reaps — because that is the only way to make the container's
// failure show on any Linux host at all, laptop included (zombie_linux.go
// carries the story and tick 8ct the three tests it took red).

// TestAReapPendingZombieIsNotAlive kills a child of THIS test and never
// waits for it, which leaves exactly what the container's PID 1 leaves every
// killed orphan: a zombie no init is coming to reap. processAlive must answer
// DEAD for it — only nobody's reap is pending, and a reap is not life.
//
// short: one child process, killed in its cradle; the kill is the whole cost
func TestAReapPendingZombieIsNotAlive(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child to kill: %v", err)
	}
	// Collected after the assertions, never before: waiting for the child is
	// the reap this test must not perform while it is proving anything.
	defer func() { _ = cmd.Wait() }()

	pid := cmd.Process.Pid
	if !processAlive(pid) {
		t.Fatalf("the child (pid %d) did not read as alive before it died", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the child: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for procState(pid) != "Z" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if procState(pid) != "Z" {
		t.Fatalf("the child (pid %d) never became a zombie, so there is nothing here to prove", pid)
	}
	if processAlive(pid) {
		t.Errorf("processAlive(pid %d) answered true for a zombie: the child is dead, and only nobody's reap is pending", pid)
	}
}

// startCorpseGroup starts the stand-in for a tool group the interrupt kills:
// a shell leading a group of its own, running one child of its own inside it,
// the way pi-durable's NodeExecutionEnv spawns every tool. It returns the
// group, and the pid of the shell's child, read from the shell's own mouth.
func startCorpseGroup(t *testing.T) (cmd *exec.Cmd, pgid, child int) {
	t.Helper()
	cmd = exec.Command("sh", "-c", "sleep 300 & echo $!; wait")
	cmd.SysProcAttr = newProcessGroup()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a group to kill: %v", err)
	}
	t.Cleanup(func() {
		// The group, whatever the test concluded — a live member left behind
		// is a `sleep 300` on the host after the test is gone. The reap
		// follows, so the leader's number is not handed out mid-test either.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	})
	lines := bufio.NewScanner(out)
	if !lines.Scan() {
		t.Fatalf("the shell never named its child: %v", lines.Err())
	}
	child, err = strconv.Atoi(strings.TrimSpace(lines.Text()))
	if err != nil || child <= 0 {
		t.Fatalf("the shell named its child as %q, not a pid", lines.Text())
	}
	pgid = cmd.Process.Pid
	if group, err := processGroupOf(child); err != nil || group != pgid {
		t.Fatalf("the shell's child (pid %d) is in group %d, want its leader's %d", child, group, pgid)
	}
	return cmd, pgid, child
}

// waitCorpse waits for pid to stop being anything but a corpse: Z is a kill
// nobody has collected, "" is one somebody already has — on a laptop the
// host's init, in the factory container nobody, which is the whole point.
func waitCorpse(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !deadByProcState(procState(pid)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state := procState(pid); !deadByProcState(state) {
		t.Fatalf("pid %d still reads as %q, want a corpse or nothing at all", pid, state)
	}
}

// TestAKilledGroupIsDeadThoughNobodyReapsIt is the group question the
// detached-tool stop tests ask: a group signal kills the whole group, and the
// corpses it leaves — number still taken, no reaper coming — must not read as
// a group that survived the kill.
//
// short: one shell and one sleeper, one group signal; the kill is the whole cost
func TestAKilledGroupIsDeadThoughNobodyReapsIt(t *testing.T) {
	t.Parallel()

	_, pgid, child := startCorpseGroup(t)
	if !groupAlive(pgid) {
		t.Fatal("the group did not read as alive before the kill")
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the group: %v", err)
	}
	waitCorpse(t, pgid)
	waitCorpse(t, child)
	// The leader is a zombie this test has not reaped, so its number — the
	// group's id — is still taken: what reads as alive from here on is the
	// zombie answer alone, not an empty group.
	if err := syscall.Kill(pgid, 0); err != nil {
		t.Fatalf("the killed leader's pid %d is not held (%v): the proof lost its number", pgid, err)
	}
	if groupAlive(pgid) {
		t.Errorf("groupAlive(%d) answered true for a group of corpses: the kill ended every member, "+
			"and only nobody's reap is pending", pgid)
	}
}

// TestAGroupWithALiveMemberIsAliveThoughItsLeaderIsDead pins the other half:
// aliveness is ANY member running, so a stop that killed the leader while
// missing its child keeps signalling the group — and a corpse leading a group
// is not one the group survives.
//
// short: one shell and one sleeper, one pid-directed kill; the kill is the whole cost
func TestAGroupWithALiveMemberIsAliveThoughItsLeaderIsDead(t *testing.T) {
	t.Parallel()

	_, pgid, child := startCorpseGroup(t)
	// Kill the leader alone, the way a group signal that was missed by every
	// member is: the child keeps running in the group, holding the lock a stop
	// must keep asking about.
	if err := syscall.Kill(pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the leader: %v", err)
	}
	waitCorpse(t, pgid)
	if processAlive(pgid) {
		t.Errorf("processAlive(pid %d) answered true for the dead leader", pgid)
	}
	if !processAlive(child) {
		t.Fatalf("the group's child (pid %d) died with its leader, so this proves nothing", child)
	}
	if !groupAlive(pgid) {
		t.Errorf("groupAlive(%d) answered false with member %d still running: a stop that quit here "+
			"would leave the very child the kill missed", pgid, child)
	}
}

// short: a table over state letters; no process
func TestAProcEntryThatVanishedIsDeadNotAlive(t *testing.T) {
	t.Parallel()

	for state, dead := range map[string]bool{"": true, "Z": true, "X": true, "R": false, "S": false, "D": false, "T": false} {
		if got := deadByProcState(state); got != dead {
			t.Errorf("state %q reads dead=%v, want %v", state, got, dead)
		}
	}
}
