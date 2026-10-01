//go:build !windows

package sandboximage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The background-process runner (image/proc.sh, tick x9d): what a container
// under the durable_object scheduling policy uses in place of the 0.x control
// server's startProcess / getProcess / readOutput / killProcess. Each verb is
// one short exec; the tests drive the shipped script the way the Durable
// Object will, one call at a time.

type procRunner struct {
	t      *testing.T
	script string
	root   string
}

func newProcRunner(t *testing.T) *procRunner {
	t.Helper()
	script, err := Path(ProcScript)
	if err != nil {
		t.Fatal(err)
	}
	p := &procRunner{t: t, script: script, root: filepath.Join(t.TempDir(), "procs")}
	t.Cleanup(func() {
		// Leave no process behind: kill whatever a failed test left running.
		ids, _ := p.call("list")
		for _, id := range strings.Fields(ids) {
			_, _ = p.call("kill", id, "--signal", "KILL", "--grace", "0")
		}
	})
	return p
}

// call runs one verb and returns stdout and the exit code (0 on success).
func (p *procRunner) call(args ...string) (string, int) {
	p.t.Helper()
	cmd := exec.Command(p.script, args...)
	cmd.Env = append(os.Environ(), "TICKS_PROC_ROOT="+p.root)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		p.t.Fatalf("ticks-proc %v did not return: a verb must never block on the process it manages", args)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		p.t.Fatalf("ticks-proc %v: %v (%s)", args, err, stderr.String())
	}
	return string(out), 0
}

func (p *procRunner) mustCall(args ...string) string {
	p.t.Helper()
	out, code := p.call(args...)
	if code != 0 {
		p.t.Fatalf("ticks-proc %v exited %d", args, code)
	}
	return out
}

func (p *procRunner) status(id string) map[string]string {
	p.t.Helper()
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(p.mustCall("status", id)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		fields[k] = v
	}
	return fields
}

// waitState polls status until the state is want, failing after a deadline.
func (p *procRunner) waitState(id, want string) map[string]string {
	p.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := p.status(id)
		if st["state"] == want {
			return st
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("process %s: state %q, want %q", id, st["state"], want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// short: a handful of sub-second shell processes; no container, no network.
func TestProcRunnerStartsAProcessAndRecordsItsExit(t *testing.T) {
	p := newProcRunner(t)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out := p.mustCall("start", "job1", "--cwd", work, "--",
		"sh", "-c", `printf 'out:%s\n' "$PWD"; printf 'err\n' >&2; exit 7`); strings.TrimSpace(out) != "job1" {
		t.Fatalf("start printed %q, want the id", out)
	}
	st := p.waitState("job1", "exited")
	if st["exit_code"] != "7" {
		t.Errorf("exit_code = %q, want 7", st["exit_code"])
	}
	if st["pid"] == "" {
		t.Error("an exited process lost its pid")
	}
	wantOut := "out:" + work + "\n"
	if got := p.mustCall("read", "job1", "stdout"); got != wantOut {
		t.Errorf("stdout = %q, want %q", got, wantOut)
	}
	if got := p.mustCall("read", "job1", "stderr"); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
	if got := strings.TrimSpace(p.mustCall("list")); got != "job1" {
		t.Errorf("list = %q, want job1", got)
	}
}

// short: one shell process that runs for under a second.
func TestProcRunnerReportsARunningProcessAndReadsItsOutputFromAnOffset(t *testing.T) {
	p := newProcRunner(t)
	gate := filepath.Join(t.TempDir(), "go")
	p.mustCall("start", "job", "--",
		"sh", "-c", `printf 'first\n'; while [ ! -f "$1" ]; do sleep 0.05; done; printf 'second\n'`, "sh", gate)

	st := p.status("job")
	if st["state"] != "running" {
		t.Fatalf("state = %q right after start, want running (start returns once the process exists)", st["state"])
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.mustCall("read", "job", "stdout") != "first\n" {
		if time.Now().After(deadline) {
			t.Fatal("the first line never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.waitState("job", "exited")["exit_code"]; got != "0" {
		t.Errorf("exit_code = %q, want 0", got)
	}
	if got := p.mustCall("read", "job", "stdout", "6"); got != "second\n" {
		t.Errorf("stdout from offset 6 = %q, want only what came after it", got)
	}
	if got := p.mustCall("read", "job", "stdout", "100"); got != "" {
		t.Errorf("stdout from past the end = %q, want nothing", got)
	}
}

// short: one sleeping shell process, killed at once.
func TestProcRunnerKillsTheProcessGroupAndRecordsTheSignal(t *testing.T) {
	p := newProcRunner(t)
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	// The grandchild would outlive a kill aimed at the shell alone.
	p.mustCall("start", "job", "--",
		"sh", "-c", `(sleep 2; touch "$1") & sleep 60`, "sh", marker)
	p.waitState("job", "running")

	out := p.mustCall("kill", "job", "--grace", "5")
	if !strings.Contains(out, "state=exited") || !strings.Contains(out, "exit_code=143") {
		t.Fatalf("kill answered %q, want exited with 143 (TERM)", out)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the process's own child survived the kill: the group was not signalled")
	}
}

// short: one shell process that ignores TERM, killed after a one-second grace.
func TestProcRunnerEscalatesToKillAfterTheGrace(t *testing.T) {
	p := newProcRunner(t)
	p.mustCall("start", "stubborn", "--", "sh", "-c", `trap '' TERM; while :; do sleep 0.1; done`)
	p.waitState("stubborn", "running")
	out := p.mustCall("kill", "stubborn", "--grace", "1")
	if !strings.Contains(out, "exit_code=137") {
		t.Fatalf("kill answered %q, want exit 137 (KILL after the grace)", out)
	}
}

// short: argument checks and one instant process; no long-lived process.
func TestProcRunnerRefusesUnknownTakenAndInvalidIDs(t *testing.T) {
	p := newProcRunner(t)
	if _, code := p.call("status", "nope"); code != 3 {
		t.Errorf("status of an unknown id exited %d, want 3", code)
	}
	if _, code := p.call("read", "nope", "stdout"); code != 3 {
		t.Errorf("read of an unknown id exited %d, want 3", code)
	}
	if _, code := p.call("kill", "nope"); code != 3 {
		t.Errorf("kill of an unknown id exited %d, want 3", code)
	}
	p.mustCall("start", "once", "--", "true")
	if _, code := p.call("start", "once", "--", "true"); code != 4 {
		t.Errorf("a second start of one id exited %d, want 4", code)
	}
	for _, bad := range []string{"../escape", ".hidden", "a/b", ""} {
		if _, code := p.call("start", bad, "--", "true"); code != 2 {
			t.Errorf("start %q exited %d, want 2 (invalid id)", bad, code)
		}
	}
	if _, code := p.call("read", "once", "stdin"); code != 2 {
		t.Errorf("read of stream stdin exited %d, want 2", code)
	}
	if out, code := p.call("list"); code != 0 || strings.TrimSpace(out) != "once" {
		t.Errorf("list = %q (exit %d), want only once", out, code)
	}
}

// short: one process whose supervisor is killed out from under it.
func TestProcRunnerCallsAProcessWhoseSupervisorDiedLost(t *testing.T) {
	p := newProcRunner(t)
	p.mustCall("start", "orphan", "--", "sleep", "60")
	st := p.waitState("orphan", "running")
	sup, err := os.ReadFile(filepath.Join(p.root, "orphan", "supervisor"))
	if err != nil {
		t.Fatal(err)
	}
	// What a container restart does to both: nothing is left to write `exit`.
	run(t, t.TempDir(), "kill", "-KILL", strings.TrimSpace(string(sup)))
	run(t, t.TempDir(), "kill", "-KILL", st["pid"])
	if got := p.waitState("orphan", "lost"); got["exit_code"] != "" {
		t.Errorf("a lost process reported exit_code %q", got["exit_code"])
	}
}
