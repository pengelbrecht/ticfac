package cli

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// short: string arithmetic, no process.
func TestARunOnMacOSHoldsTheHostAwakeForItsOwnLifetime(t *testing.T) {
	t.Parallel()
	got := strings.Join(keepAwakeArgv("darwin", 4242), " ")
	if got != "caffeinate -i -s -w 4242" {
		t.Errorf("the keep-awake on macOS is %q; it must hold idle and AC system sleep off for exactly the run's pid", got)
	}
	if argv := keepAwakeArgv("linux", 4242); argv != nil {
		t.Errorf("a Linux run has no keep-awake to take, got %v", argv)
	}
}

// short: one caffeinate process for well under a second, macOS only.
func TestTheKeepAwakeIsHeldWhileTheRunLivesAndReleasedByItsPid(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the keep-awake is macOS's caffeinate")
	}
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("no caffeinate on this host")
	}
	note, release := holdHostAwake(1) // launchd: a pid that outlives the test
	if !strings.Contains(note, "held awake") {
		t.Fatalf("the run says %q about keeping the host awake", note)
	}
	pid := note[strings.LastIndex(note, "pid ")+4 : len(note)-1]
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("the note names no helper pid: %q", note)
	}
	if out, err := exec.Command("ps", "-o", "command=", "-p", pid).Output(); err != nil ||
		!strings.Contains(string(out), "caffeinate -i -s -w 1") {
		t.Fatalf("no caffeinate holds the host for the run: ps says %q (%v)", out, err)
	}
	release()
	release() // idempotent
	if out, err := exec.Command("ps", "-o", "command=", "-p", pid).Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		t.Errorf("the keep-awake outlived its release: %s", out)
	}
}
