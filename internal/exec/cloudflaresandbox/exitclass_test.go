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
	nameExitClasses(status)
	if status.Observations[0].Detail != "the container's work process exited 42" ||
		status.Observations[1].Detail != "the container's work process exited 0" {
		t.Errorf("an unassigned code or a success was decorated: %+v", status.Observations)
	}
}
