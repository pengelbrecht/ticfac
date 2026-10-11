package herdr

// The fake agent's lifecycle (tick yy1). The never-exit modes (sleep, ignore,
// esc_only) used to outlive their test: started detached, never killed by a
// cleanup, they sat reparented to init for days — 80 of them, 5 per run of
// this package. Now every fake agent starts in its own process group, the
// harness's cleanup stops the whole group and reaps it, and TestMain fails
// the package if any fake agent it started is still alive at the end.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// fakeAgent is one fake-agent.sh process the harness started. done closes
// once cmd.Wait has reaped it.
type fakeAgent struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// fakeAgents is the package's registry: every fake agent's process group id
// (its pid — it leads its own group) and the test that started it.
var fakeAgents = struct {
	sync.Mutex
	started map[int]string
}{started: map[int]string{}}

func registerFakeAgent(pid int, test string) {
	fakeAgents.Lock()
	defer fakeAgents.Unlock()
	fakeAgents.started[pid] = test
}

// fakeAgentGrace is how long a fake agent gets to honour SIGTERM before its
// group is killed outright.
const fakeAgentGrace = 500 * time.Millisecond

// stopFakeAgents is the harness's cleanup: every fake agent it started is
// stopped — SIGTERM to the group, SIGKILL to the group after a short grace —
// and reaped, and its group confirmed empty. A group that will not empty is
// a test failure, not a leak left for the host.
func (h *harness) stopFakeAgents() {
	h.mu.Lock()
	agents := append([]*fakeAgent(nil), h.agents...)
	h.mu.Unlock()
	for _, a := range agents {
		pid := a.cmd.Process.Pid
		signalFakeAgentGroup(pid, false)
		select {
		case <-a.done:
		case <-time.After(fakeAgentGrace):
		}
		// Unconditionally: the leader exiting does not take the rest of
		// its group (a `sleep` it was waiting on) with it.
		signalFakeAgentGroup(pid, true)
		select {
		case <-a.done:
		case <-time.After(10 * time.Second):
			h.t.Errorf("fake agent pid %d not reaped 10s after SIGKILL", pid)
			continue
		}
		deadline := time.Now().Add(10 * time.Second)
		for fakeAgentGroupAlive(pid) {
			if time.Now().After(deadline) {
				h.t.Errorf("fake agent process group %d still alive 10s after SIGKILL", pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// leakedFakeAgents answers the registered fake agents whose process group
// still has a live member, as "pid (test)".
func leakedFakeAgents() (pids []int, desc []string) {
	fakeAgents.Lock()
	defer fakeAgents.Unlock()
	for pid := range fakeAgents.started {
		if fakeAgentGroupAlive(pid) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		desc = append(desc, fmt.Sprintf("%d (%s)", pid, fakeAgents.started[pid]))
	}
	return pids, desc
}

// The fake agent's own backstop: a never-exit agent whose test is gone (its
// scratch directory removed) exits by itself instead of waiting for a
// cleanup that may never come.
func TestAFakeAgentExitsOnceItsScratchDirIsGone(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	scratch := filepath.Join(dir, "agent")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(scratch, "prompt.txt")
	if err := os.WriteFile(prompt, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(scratch, "status")
	script := filepath.Join(root, "internal", "exec", "herdr", "testdata", "fake-agent.sh")
	cmd := exec.Command("/bin/sh", script, dir, prompt, status, filepath.Join(scratch, "interrupt"), "ignore")
	cmd.SysProcAttr = fakeAgentProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	registerFakeAgent(cmd.Process.Pid, t.Name())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		signalFakeAgentGroup(cmd.Process.Pid, true)
		<-done
	})

	// It is running, in the mode nothing but a pane close stops.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if raw, _ := os.ReadFile(status); string(raw) == "working\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake agent never reported working")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		done <- err // for the cleanup's receive
		if err == nil {
			t.Fatal("the fake agent exited 0; want its orphan exit (1)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake agent still runs 5s after its scratch dir was removed")
	}
}

// TestMain is the package's leak check: a fake agent still alive once every
// test has finished is a failure of the package, and is killed here so the
// failure does not also become a leak.
func TestMain(m *testing.M) {
	code := m.Run()
	// No grace here: each harness's cleanup already waited for its groups
	// to empty, so anything alive now was never stopped.
	if pids, desc := leakedFakeAgents(); len(pids) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d fake agent(s) outlived their test (tick yy1); killing them:\n", len(pids))
		for i, pid := range pids {
			fmt.Fprintf(os.Stderr, "\tpgid %s\n", desc[i])
			signalFakeAgentGroup(pid, true)
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
