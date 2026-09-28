package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// A run holds its host awake for as long as it lives (epic-6in, 2026-09-28).
//
// The help has said since tick 0z0 to wrap a run in `caffeinate -i`, and the
// runs this factory starts unattended are exactly the ones nobody wraps:
// `ticfac run` detaches run-epic itself. epic-6in's close-out was waiting on
// the re-run of a red CI job when the Mac entered Idle Sleep (15:28:49Z) and
// stayed asleep, but for seconds-long dark wakes, until 16:09:12Z. The re-run
// went red at 15:56Z and the repair job was not dispatched until the host woke:
// a forty-minute stall with nothing wrong in the run, and a person's wake of
// the laptop the only thing that ended it. A step whose only actor is a person
// is a design defect, so the run takes the assertion itself.
//
// `caffeinate -w <pid>` holds the assertion exactly as long as the run's own
// process lives — the kernel tells caffeinate when the pid exits, so a run
// killed with SIGKILL, which runs no cleanup, still lets the host sleep again.
// -i prevents idle sleep; -s prevents system sleep on AC power (a lid closed
// on a plugged-in laptop). Neither can stop a thermal emergency sleep, and a
// wait that slept through one says so in the feed (reconcile's
// StageHostSuspended).

// keepAwakeArgv is the command that holds a host of the given OS awake while
// pid lives, or nil where the run has none to take.
func keepAwakeArgv(goos string, pid int) []string {
	if goos == "darwin" {
		return []string{"caffeinate", "-i", "-s", "-w", strconv.Itoa(pid)}
	}
	return nil
}

// holdHostAwake starts the keep-awake for pid and answers the line the run
// says about it (empty on a host with none) and its release. The release
// stops the helper by its exact pid — never by pattern: the host is shared —
// and is safe to call more than once.
func holdHostAwake(pid int) (note string, release func()) {
	argv := keepAwakeArgv(runtime.GOOS, pid)
	if argv == nil {
		return "", func() {}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("the host is NOT held awake: %s could not start (%v) — a machine that sleeps mid-run "+
			"stalls it until it wakes", strings.Join(argv, " "), err), func() {}
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	released := false
	return fmt.Sprintf("the host is held awake while this run lives: %s (pid %d)", strings.Join(argv, " "),
			cmd.Process.Pid), func() {
			if released {
				return
			}
			released = true
			_ = cmd.Process.Kill()
			<-done
		}
}
