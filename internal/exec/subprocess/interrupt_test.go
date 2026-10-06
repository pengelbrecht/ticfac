package subprocess

import (
	"bufio"
	"encoding/json"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The tool interrupt (tick l6n): a steer is read after the current tool
// round, and a hung tool's round never ends — so the stuck ladder ends the
// round itself, the way pi-durable's own abort and timeout do: by killing
// the tool's process group. These tests drive it against a stand-in runner
// that spawns exactly what pi-durable's NodeExecutionEnv spawns — a shell
// `detached`, so it leads a process group of its own — beside a child in
// the runner's own group, which stands for anything that is the harness's
// and not a tool's.

// toolTree is the stand-in runner: a node process leading its own group, a
// detached tool shell with a child of its own, and a plain child in the
// runner's group.
type toolTree struct {
	runner    *exec.Cmd
	tool      int // the detached shell: leads its own group
	toolChild int // the shell's own child: in the tool's group
	keep      int // a child in the runner's group, never a tool
	toolExit  chan string
}

func startToolTree(t *testing.T) *toolTree {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("the stand-in runner is node, as the durable runner is: %v", err)
	}
	script := `
const { spawn } = require("node:child_process");
const tool = spawn("sh", ["-c", "sleep 3600 & echo $!; wait"], { detached: true, stdio: ["ignore", "pipe", "ignore"] });
const keep = spawn("sleep", ["3600"], { stdio: "ignore" });
tool.stdout.once("data", (d) => {
  console.log(JSON.stringify({ tool: tool.pid, toolChild: Number(String(d).trim()), keep: keep.pid }));
});
tool.on("exit", (code, signal) => console.log(JSON.stringify({ toolExited: signal || String(code) })));
setInterval(() => {}, 60000);
`
	runner := exec.Command(node, "-e", script)
	runner.SysProcAttr = newProcessGroup()
	out, err := runner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	tree := &toolTree{runner: runner, toolExit: make(chan string, 1)}
	t.Cleanup(func() {
		// Everything the stand-in started, by the groups this test made.
		if tree.tool > 0 {
			_ = syscall.Kill(-tree.tool, syscall.SIGKILL)
		}
		_ = syscall.Kill(-runner.Process.Pid, syscall.SIGKILL)
		_ = runner.Wait()
	})
	lines := bufio.NewScanner(out)
	pids := make(chan struct{})
	go func() {
		for lines.Scan() {
			var line struct {
				Tool, ToolChild, Keep int
				ToolExited            string
			}
			if json.Unmarshal(lines.Bytes(), &line) != nil {
				continue
			}
			if line.Tool > 0 {
				tree.tool, tree.toolChild, tree.keep = line.Tool, line.ToolChild, line.Keep
				close(pids)
			}
			if line.ToolExited != "" {
				tree.toolExit <- line.ToolExited
			}
		}
	}()
	select {
	case <-pids:
	case <-time.After(10 * time.Second):
		t.Fatal("the stand-in runner never reported its tool tree")
	}
	return tree
}

func gone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == syscall.ESRCH
}

// A hung tool is found by the group it leads under the runner, and nothing
// in the runner's own group is taken for one.
//
// short: one node process and three sleeps, for well under a second
func TestHungToolGroupsAreTheDetachedGroupsUnderTheRunner(t *testing.T) {
	tree := startToolTree(t)
	groups, err := hungToolGroups(tree.runner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0] != tree.tool {
		t.Fatalf("hung tool groups = %v, want only the tool's own group %d (keep %d is the runner's)",
			groups, tree.tool, tree.keep)
	}
}

// Interrupting the tool kills its whole group — the shell AND what it
// started — and spares the runner and everything in the runner's group: the
// runner sees its tool exit by a signal, which is a tool round that ENDS.
//
// short: one node process and three sleeps, for well under a second
func TestInterruptingAHungToolKillsItsGroupAndSparesTheRunner(t *testing.T) {
	tree := startToolTree(t)
	runnerPID := tree.runner.Process.Pid
	groups, err := hungToolGroups(runnerPID)
	if err != nil {
		t.Fatal(err)
	}
	killed := interruptToolGroups(runnerPID, groups)
	if len(killed) != 1 || killed[0] != tree.tool {
		t.Fatalf("interrupted %v, want the tool's group %d", killed, tree.tool)
	}
	select {
	case how := <-tree.toolExit:
		if how != "SIGKILL" {
			t.Errorf("the runner saw its tool exit by %q, want SIGKILL", how)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never saw its tool exit: the round would still be hung")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !gone(tree.toolChild) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gone(tree.toolChild) {
		t.Errorf("the tool's own child %d outlived the interrupt", tree.toolChild)
	}
	if gone(runnerPID) {
		t.Error("the runner was killed with its tool")
	}
	if gone(tree.keep) {
		t.Error("a process in the runner's own group was killed as a tool")
	}
}

// A group that is no longer a tool of THIS runner when the interrupt comes —
// it ended on its own between the look and the kill, and its number may be
// anybody's now — is not signalled.
//
// short: one node process and three sleeps, for well under a second
func TestAnInterruptSignalsOnlyGroupsStillLedUnderTheRunner(t *testing.T) {
	tree := startToolTree(t)
	stranger := exec.Command("sleep", "3600")
	stranger.SysProcAttr = newProcessGroup()
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-stranger.Process.Pid, syscall.SIGKILL)
		_ = stranger.Wait()
	})
	killed := interruptToolGroups(tree.runner.Process.Pid, []int{stranger.Process.Pid})
	if len(killed) != 0 {
		t.Fatalf("interrupted %v, a group that is not under the runner", killed)
	}
	if gone(stranger.Process.Pid) {
		t.Fatal("a group that is not the runner's tool was killed")
	}
	if gone(tree.tool) {
		t.Fatal("the runner's real tool was touched by an interrupt that did not name it")
	}
}
