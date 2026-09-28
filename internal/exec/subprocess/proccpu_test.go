package subprocess

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// SUB-SECOND TOOL CPU (the go race failure of PR #109): on Linux the stuck
// watch read CPU from `ps -o time=`, which procps prints as hh:mm:ss, so a
// tool that had burned 0.8s read as zero. With a 1.5s window and a CPU-starved
// CI runner, TestALocalRunnerIsNotStuckWhileItsToolIsBusyAndIsRepromptedWhenItHangs
// saw its busy burner as idle and nudged it inside the busy phase. The table
// is now read from /proc where there is one, in clock ticks.

// A /proc-shaped directory is read in clock ticks: 0.37s of CPU is 0.37s, not
// the zero a whole-second column would round it to.
//
// short: reads a handful of files under t.TempDir()
func TestTheProcessTableIsReadFromProcInClockTicks(t *testing.T) {
	root := t.TempDir()
	write := func(pid, line string) {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "stat"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A command with a space and parentheses in it: fields count from the last ')'.
	write("self", "1 (init) S 0 1 1 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0\n")
	write("42", "42 (my (odd) tool) R 7 42 42 0 -1 0 0 0 0 0 30 7 0 0 20 0 1 0 1 0 0\n")
	write("7", "7 (sh) S 1 7 7 0 -1 0 0 0 0 0 1 0 0 0 20 0 1 0 1 0 0\n")
	write("junk", "not a pid directory\n")

	procs, ok := ProcFSProcs(root)
	if !ok {
		t.Fatal("a /proc-shaped directory was not read as one")
	}
	byPID := map[int]Proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	tool, found := byPID[42]
	if !found || tool.PPID != 7 || tool.CPU != 370*time.Millisecond {
		t.Fatalf("the tool reads as %+v (found %v), want ppid 7 and 370ms of CPU", tool, found)
	}
	if got, _ := TreeCPU(procs, 7, 1); got != 370*time.Millisecond {
		t.Errorf("the tree under the runner holds %v of CPU, want 370ms", got)
	}
	if _, ok := ProcFSProcs(t.TempDir()); ok {
		t.Error("a directory with no self/stat was read as /proc: macOS must fall back to ps")
	}
}

// On the host itself: a busy child's CPU is visible well inside its first
// second. On Linux this fails against the ps column (00:00:00 until a whole
// second is burned); on macOS ps already prints hundredths.
//
// short: one busy child for a fraction of a second
func TestABusyToolsCPUIsVisibleBeforeItsFirstSecond(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the process table is read on linux and darwin")
	}
	burner := exec.Command("sh", "-c", "while :; do :; done")
	if err := burner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = burner.Process.Kill(); _ = burner.Wait() })

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		procs, err := SystemProcs()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range procs {
			if p.PID == burner.Process.Pid && p.CPU > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a busy tool showed no CPU in the process table within 0.9s: the table's resolution " +
		"is too coarse for the stuck watch's floor")
}
