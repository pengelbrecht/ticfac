package subprocess

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// Tick 3c2 (epic 43y): a locally killed pi-durable runner used to settle its
// attempt failed — nudgeDue refuses every non-zero exit, and a death by signal
// is the -1 Go reports for one — abandoning the storage its conversation
// survives in and paying a full redispatch with a fresh conversation. The
// supervisor now replays the same argv, bounded, and the relaunched process
// resumes the killed turn (relaunch.go).

func relaunches(observations []Observation) []Observation {
	var out []Observation
	for _, o := range observations {
		if IsRelaunch(o) {
			out = append(out, o)
		}
	}
	return out
}

// A death by signal is the exit the relaunch keys on, and the signal's NAME
// is evidence the exit code cannot carry: Go reports a signalled process as
// -1, the number a wall-clock stop and an OOM kill would otherwise share.
func TestARunnerThatDiesBySignalIsNamedAsSuch(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "kill -9 $$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	life := watchRunner(cmd, "")
	<-life.exitedCh
	if code := life.code(); code != -1 {
		t.Fatalf("exit code %d, want -1: a death by signal is the exit a relaunch keys on", code)
	}
	sig, ok := life.deathSignal()
	if !ok || sig != syscall.SIGKILL {
		t.Fatalf("death signal %v (found %v), want SIGKILL: the relaunch observation names the signal", sig, ok)
	}
	if word := deathWord(life); !strings.Contains(word, "signal 9") {
		t.Errorf("the death word %q does not name the signal", word)
	}
	if word := deathWord(nil); word != "a signal" {
		t.Errorf("a life that cannot be read says %q, want \"a signal\"", word)
	}
}

// Every deliberate stop leaves its own durable mark, and each one refuses the
// relaunch: a recovery is for a death nobody chose. A clean durable signal
// death — the operator's kill, the OOM — is the one that is due.
func TestRelaunchDue(t *testing.T) {
	durable := &attemptRecord{Runner: "pi", SteerSock: "/tmp/a.sock", Worktree: t.TempDir()}
	plain := &attemptRecord{Runner: "claude", Worktree: durable.Worktree}

	if due, _ := relaunchDue(newStore(t.TempDir()), durable, 0, 0); due {
		t.Error("an exit 0 is relaunchable: the nudge ladder owns a runner that ended by itself")
	}
	if due, _ := relaunchDue(newStore(t.TempDir()), durable, 3, 0); due {
		t.Error("a failed exit is relaunchable: a failure is the runner's own words")
	}
	if due, why := relaunchDue(newStore(t.TempDir()), plain, -1, 0); due {
		t.Errorf("a runner with no durable conversation is relaunchable: %s", why)
	}
	if due, why := relaunchDue(newStore(t.TempDir()), durable, -1, MaxRelaunches); due {
		t.Errorf("the relaunch bound does not bind: %s", why)
	}

	st := newStore(t.TempDir())
	if err := st.writeFile(st.path(fileWallExceeded), []byte("now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if due, why := relaunchDue(st, durable, -1, 0); due {
		t.Errorf("a wall-clock stop is relaunchable: %s", why)
	}
	if err := st.writeFile(st.path(fileStuckStopped), []byte("stuck\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if due, why := relaunchDue(st, durable, -1, 0); due {
		t.Errorf("a stuck stop is relaunchable: %s", why)
	}
	if err := st.writeJSON(fileCancel, cancelRecord{SchemaVersion: 1, JobID: "j", Attempt: 1, StopRequested: true}); err != nil {
		t.Fatal(err)
	}
	if due, why := relaunchDue(st, durable, -1, 0); due {
		t.Errorf("a cancelled attempt is relaunchable: %s", why)
	}

	clean := newStore(t.TempDir())
	if due, why := relaunchDue(clean, durable, -1, 0); !due {
		t.Errorf("a killed durable runner is not relaunchable: %s", why)
	}
	gone := &attemptRecord{Runner: "pi", SteerSock: "/tmp/a.sock", Worktree: nonExistentPath(t)}
	if due, why := relaunchDue(clean, gone, -1, 0); due {
		t.Errorf("a runner whose worktree is gone is relaunchable: %s", why)
	}
}

func nonExistentPath(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/gone"
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a path that cannot exist does")
	}
	return path
}

// The relaunch observation is a `started` observation the feed can pick out of
// the stream — the nudge's own shape, and a prefix the nudge's does not match.
func TestTheRelaunchObservationSaysWhatHappened(t *testing.T) {
	detail := RelaunchDetail(1, "pi", "signal 9 (killed)")
	o := Observation{Kind: ObsStarted, Detail: detail}
	if !IsRelaunch(o) {
		t.Fatalf("a relaunch observation does not read as one: %+v", o)
	}
	if IsNudge(o) {
		t.Errorf("a relaunch reads as a nudge: the two ladders share a kind and must not share a prefix")
	}
	for _, missing := range []string{
		fmt.Sprintf("relaunch 1 of %d", MaxRelaunches),
		"signal 9 (killed)",
		"resumes its own conversation from the attempt storage",
	} {
		if !strings.Contains(detail, missing) {
			t.Errorf("the relaunch observation omits %q: %s", missing, detail)
		}
	}
}
