package cloudflaresandbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// TestASettledWorkerSaysWhichStepDied is the hn6 r5i try 2 shape: a worker
// that died after its checkout and before any model call settles failed with
// an exit code, and the status the reconciler reads names what the code means.
//
// short: an httptest door and one state directory.
func TestASettledWorkerSaysWhichStepDied(t *testing.T) {
	h := newHarness(t)
	handle, err := h.ex.Start(h.spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.door.setStatus("keh", 1, doorStatus{
		state:    subprocess.StateFailed,
		terminal: true,
		observations: []subprocess.Observation{{At: "2026-09-22T18:05:00Z", Kind: subprocess.ObsExited,
			Detail: "the container's work process exited 7"}},
	})
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(status.Observations) == 0 {
		t.Fatal("the settled status carries no observation")
	}
	got := status.Observations[len(status.Observations)-1].Detail
	if !strings.HasPrefix(got, "the container's work process exited 7 (") || !strings.Contains(got, "model") {
		t.Errorf("the exit observation reads %q, want the code AND its class (the model probe)", got)
	}
}

// Every class the worker image assigns is named, and a code the image does not
// assign is left as the bare number rather than guessed at.
func TestEveryWorkerExitCodeIsNamed(t *testing.T) {
	for _, code := range []int{sandboximage.ExitConfig, sandboximage.ExitClone, sandboximage.ExitTkVersion,
		sandboximage.ExitPreflight, sandboximage.ExitSetup, sandboximage.ExitModel, sandboximage.ExitHarness,
		sandboximage.ExitWorkerPush, sandboximage.ExitWorkerNoWork, sandboximage.ExitWorkerAgent} {
		if exitClass(code) == "" {
			t.Errorf("worker exit %d has no class", code)
		}
	}
	status := &subprocess.JobStatus{Observations: []subprocess.Observation{
		{Detail: "the container's work process exited 42"},
		{Detail: "the container's work process exited 0"},
	}}
	if exitClass(sandboximage.ExitStartUnpublished) == "" {
		t.Errorf("exit %d (the start commit is not on origin) has no class", sandboximage.ExitStartUnpublished)
	}
	nameExitClasses(status)
	if status.Observations[0].Detail != "the container's work process exited 42" ||
		status.Observations[1].Detail != "the container's work process exited 0" {
		t.Errorf("an unassigned code or a success was decorated: %+v", status.Observations)
	}
}

// TestAWorkerThatCannotCheckOutItsStartCommitCollectsAsStartNotOnOrigin is
// epic hn6's run_09ebaf29: a base fold's resolve job was cut at a conflicted
// merge only the orchestrator's clone held, and its container died on the
// checkout before doing anything. The collect read the empty landing branch as
// a job that answered nothing — "missing-result … the push never landed" — and
// the run redispatched it identically, three times. The container's exit says
// what happened (sandboximage.ExitStartUnpublished), and the collect carries it
// as a typed fact the orchestrator can act on, naming the commit.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAWorkerThatCannotCheckOutItsStartCommitCollectsAsStartNotOnOrigin(t *testing.T) {
	h, repo, handle, _ := newCollectHarness(t)
	h.door.setStatus("keh", 1, doorStatus{
		state:    subprocess.StateFailed,
		terminal: true,
		observations: []subprocess.Observation{{At: "2026-09-30T22:24:00Z", Kind: subprocess.ObsExited,
			Detail: "the container's work process exited 13"}},
	})
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := status.Observations[len(status.Observations)-1].Detail; !strings.Contains(got, "not on origin") {
		t.Errorf("the exit observation reads %q, want the class that says the start commit is not on origin", got)
	}

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.StartNotOnOrigin != repo.Base {
		t.Errorf("the collect's StartNotOnOrigin is %q, want the dispatched start commit %s",
			collected.StartNotOnOrigin, repo.Base)
	}
	if collected.Result.FailureClass != subprocess.FailureInfrastructure {
		t.Errorf("failure class %q, want %q: the job never ran, so this is not the runner's failure",
			collected.Result.FailureClass, subprocess.FailureInfrastructure)
	}
	if !strings.Contains(collected.Message, "not on origin") || !strings.Contains(collected.Message, shortSHA(repo.Base)) {
		t.Errorf("the message does not say the start commit is not on origin: %s", collected.Message)
	}
	if strings.Contains(collected.Message, "the push never landed") {
		t.Errorf("the message still reads as a job whose push failed: %s", collected.Message)
	}
}

// TestAWorkerWhoseGatewayNeverAnsweredCollectsAsInfrastructure is epic hn6's
// run_37b36bfe: 0rx's container probed the gateway while the factory's Worker
// was being redeployed and died in its boot, before its harness. The collect
// read the empty landing branch as a job that answered nothing — "missing-result
// … carries no report" — and the run spent a rung of 0rx's ladder on it. The
// container's exit says what happened (sandboximage.ExitGatewayUnavailable), and
// the collect carries it as a typed infrastructure fact naming the service.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAWorkerWhoseGatewayNeverAnsweredCollectsAsInfrastructure(t *testing.T) {
	for _, tc := range []struct {
		code       int
		service    string
		persistent bool
	}{
		{sandboximage.ExitGatewayUnavailable, "the model gateway", false},
		{sandboximage.ExitOriginUnavailable, "origin", false},
		// The deterministic environment faults: a retry boots the same image on
		// the same repository, so they are persistent, and each names its fix.
		{sandboximage.ExitConfig, "the worker's boot inputs", true},
		{sandboximage.ExitTkVersion, "the worker image's tk", true},
		{sandboximage.ExitPreflight, "the repository's environment pre-flight", true},
		{sandboximage.ExitSetup, "the repository's [sandbox] setup", true},
		{sandboximage.ExitModel, "the model route", true},
		{sandboximage.ExitHarness, "the harness's model wiring", true},
	} {
		h, _, handle, _ := newCollectHarness(t)
		h.door.setStatus("keh", 1, doorStatus{
			state:    subprocess.StateFailed,
			terminal: true,
			observations: []subprocess.Observation{{At: "2026-10-01T04:49:30Z", Kind: subprocess.ObsExited,
				Detail: fmt.Sprintf("the container's work process exited %d", tc.code)}},
		})
		status, err := h.ex.Inspect(handle, "")
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if got := status.Observations[len(status.Observations)-1].Detail; !tc.persistent && !strings.Contains(got, "infrastructure") {
			t.Errorf("exit %d's observation reads %q, want the class that says it is infrastructure", tc.code, got)
		}
		collected, err := h.ex.CollectDetail(handle)
		if err != nil {
			t.Fatalf("CollectDetail: %v", err)
		}
		if collected.Verdict != subprocess.VerdictMissingResult {
			t.Errorf("verdict %q, want missing-result: the job never answered", collected.Verdict)
		}
		if collected.Infrastructure == nil || collected.Infrastructure.Service != tc.service ||
			collected.Infrastructure.ExitCode != tc.code || collected.Infrastructure.Persistent != tc.persistent {
			t.Fatalf("exit %d collects with Infrastructure %+v, want %s", tc.code, collected.Infrastructure, tc.service)
		}
		if collected.Result.FailureClass != subprocess.FailureInfrastructure {
			t.Errorf("failure class %q, want %q", collected.Result.FailureClass, subprocess.FailureInfrastructure)
		}
		if !strings.Contains(collected.Message, tc.service) {
			t.Errorf("the message does not name %s: %s", tc.service, collected.Message)
		}
		if collected.Infrastructure.Fix == "" {
			t.Errorf("exit %d names no fix: the stop it becomes must say what to do", tc.code)
		}
	}
}

// Exits after the harness started are the agent's, not the boot's: a push that
// failed, a branch with no work, a harness that failed. They stay the verdicts
// they were, with no boot fault beside them.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAnExitAfterTheHarnessStartedIsNotABootFault(t *testing.T) {
	for _, code := range []int{sandboximage.ExitWorkerPush, sandboximage.ExitWorkerNoWork, sandboximage.ExitWorkerAgent,
		sandboximage.ExitStartUnpublished, 137} {
		if fault := bootFault(code); fault != nil {
			t.Errorf("exit %d reads as a boot fault %+v", code, fault)
		}
		h, _, handle, _ := newCollectHarness(t)
		h.door.setStatus("keh", 1, doorStatus{
			state:    subprocess.StateFailed,
			terminal: true,
			observations: []subprocess.Observation{{At: "2026-10-01T04:49:30Z", Kind: subprocess.ObsExited,
				Detail: fmt.Sprintf("the container's work process exited %d", code)}},
		})
		if _, err := h.ex.Inspect(handle, ""); err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		collected, err := h.ex.CollectDetail(handle)
		if err != nil {
			t.Fatalf("CollectDetail: %v", err)
		}
		if collected.Infrastructure != nil {
			t.Errorf("exit %d collected as a boot fault %+v", code, collected.Infrastructure)
		}
	}
}
