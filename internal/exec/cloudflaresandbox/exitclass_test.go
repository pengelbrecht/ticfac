package cloudflaresandbox

import (
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
