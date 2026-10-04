package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// Tick 5bd. The union TestMain in main_test.go does two jobs — dispatch the
// detached `ticfac run` child this binary is doubled as, then hand the rest
// to registrytest.GuardMain — and the ORDER between them is load-bearing:
// the dispatch must come FIRST, because the child it starts claims the run
// into the registry it INHERITS from this process, the one GuardMain's
// setup pointed away from the operator's home before any test ran. A
// TestMain that let the child reach GuardMain would give the child its own
// private temp root first, and the child's claim would register somewhere
// this package's tests never look — no test would read it back, and no
// guard would fail: the leak scan looks only at the operator's REAL
// registry, and the child would not have written there either. The union
// would be silently half-broken while every suite stayed green.
//
// So this test pins the property itself: the binary this test runs in,
// spawned with argv[1] == "run-epic" exactly the way the production spawn
// starts it, claims its run into THE REDIRECTED REGISTRY THIS PROCESS READS
// — the inheritance is the proof the dispatch ran without the child taking
// the guard's own path. The registration is the half of a claim that lives
// in the machine-local directory the redirect moves, which is why the
// assertion reads it and not the pidfile: the pidfile never leaves the
// checkout, so it cannot tell redirected from real.
//
// short: one child process, spawned and signalled the way the run command's
// own end-to-end test in run_test.go spawns and signals its children; the
// claim it waits for is a rename-atomic file write the suite already
// exercises on every runlife probe.
func TestTheDispatchedRunEpicChildClaimsIntoTheRedirectedRegistry(t *testing.T) {
	// Assert BEFORE anything is spawned, the same discipline
	// registry_redirect_test holds: with the redirect lost, failing here
	// leaves nothing written on the operator's machine. The operator's real
	// directory is named the way registrytest.GuardMain names it — by
	// runregistry.OperatorDir — rather than spelled here, so the two cannot
	// drift.
	operator := runregistry.OperatorDir()
	dir := runregistry.Dir()
	if dir == operator {
		t.Fatalf("runregistry.Dir() is the operator's own %s: the child this test spawns claims a run, and TestMain must point %s away from the operator's home before any test — or any child — runs.",
			operator, runregistry.RegistryDirEnv)
	}

	// The child: this binary, run-epic, an epic id and a repo — the argv the
	// production spawn builds, and nothing else. The epic id carries this
	// process's pid so the run id names THIS test's child alone: attempts of
	// this tick may run as sibling suites on one host, and a shared id would
	// let one suite's stray read as the other's claim. And the time, so the
	// id names this INVOCATION alone: under -count=N the same process runs the
	// test again, and the previous child's registration — still standing,
	// because a SIGTERMed child leaves it for the registry's own sweep — would
	// otherwise satisfy the wait below before this child ever claimed, and be
	// read back as this child's (a stress run of the short suite caught it
	// naming the previous repetition's repo).
	epicID := fmt.Sprintf("dispatch-%d-%d", os.Getpid(), time.Now().UnixNano())
	runID := "epic-" + epicID
	repo := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("name this binary: %v", err)
	}
	var childOut bytes.Buffer
	cmd := exec.Command(self, "run-epic", epicID, "--repo", repo)
	cmd.Stdout, cmd.Stderr = &childOut, &childOut
	// A session of its own, exactly as the production spawn starts it, so
	// what is under test is the child the real command would start.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the detached child: %v", err)
	}

	// Cleanup is best-effort the way the run command's own end-to-end test
	// holds it: a leak here is caught by the wait below, not by failing the
	// cleanup. Kill, then reap, so no child outlives a failed test.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Wait on the CONDITION, never on a guess about the timing: the child's
	// own observable is its registration, written the moment its claim
	// stands. A child that took the guard's path instead of the dispatch
	// would register in its OWN private root and never answer here.
	regPath := filepath.Join(dir, runID+".json")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(regPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(regPath); err != nil {
		probe := runlife.Probe(repo, runID, time.Now())
		t.Fatalf("the dispatched child did not register its claim in the redirected registry %s within 30s (run %s; pidfile state %s) — TestMain must dispatch the run-epic child before registrytest.GuardMain, or the child claims into a registry this process never looks at.\nchild output:\n%s",
			dir, runID, probe.State, childOut.String())
	}

	// The registration is this child's claim, where this process reads it —
	// and nowhere on the operator's machine.
	raw, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read the child's registration: %v", err)
	}
	var reg runregistry.Registration
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("the child's registration does not decode: %v\n%s", err, raw)
	}
	if reg.RunID != runID {
		t.Errorf("the registration names run %q, want %q", reg.RunID, runID)
	}
	if got, want := filepath.Clean(reg.Repo), filepath.Clean(repo); got != want {
		t.Errorf("the registration names repo %q, want the repo the child was given: %q", got, want)
	}
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State != runlife.Alive {
		t.Errorf("the registered run reads %s, want alive — the child that claimed must still be it: %s",
			probe.State, probe.Reason)
	}
	if _, err := os.Stat(filepath.Join(operator, runID+".json")); err == nil {
		t.Errorf("the dispatched child also wrote into the operator's real registry: %s",
			filepath.Join(operator, runID+".json"))
	}

	// Stop the child the way it answers a stop — SIGTERM, its own signal —
	// and wait for its exit by its own observable: the wait's return.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("could not stop the detached child: %v", err)
	}
	code := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			exitCode = 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				if c := exitErr.ExitCode(); c >= 0 {
					exitCode = c
				}
			}
		}
		code <- exitCode
	}()
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("the dispatched child exited %d after SIGTERM, want 0:\n%s", c, childOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the dispatched child did not leave on SIGTERM:\n%s", childOut.String())
	}
	if probe := runlife.Probe(repo, runID, time.Now()); probe.State != runlife.NotRunning {
		t.Errorf("the run reads %s after the child released it, want not_running: %s",
			probe.State, probe.Reason)
	}
}
