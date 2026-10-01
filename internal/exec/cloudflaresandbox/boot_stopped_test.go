package cloudflaresandbox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A container that stopped in its boot left its reason beside its worker
// branch (image/worker.sh boot_stopped); the collect reads it, so the run's
// line says WHY the job never reached its harness rather than "the push never
// landed" (hn6 run_ee8e: 378's resolve job exited 7 at its gateway probe). The
// verdict and the class stay what the exit code made them — the marker is the
// reason, never an answer.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestABootStoppedMarkerGivesTheCollectItsReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		reason string
		infra  bool
	}{
		{"a model refusal", sandboximage.ExitModel, "the routed model could not answer a one-token request through the gateway.", false},
		{"a silent gateway", sandboximage.ExitGatewayUnavailable, "the gateway did not answer a one-token request within 30s (asked 4 time(s)).", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, handle, branch := newCollectHarness(t)
			h.door.setStatus("keh", 1, doorStatus{
				state:    subprocess.StateFailed,
				terminal: true,
				observations: []subprocess.Observation{{At: "2026-10-01T04:06:10Z", Kind: subprocess.ObsExited,
					Detail: fmt.Sprintf("the container's work process exited %d", tc.code)}},
			})
			if _, err := h.ex.Inspect(handle, ""); err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			worker := repo.workerDir("worker")
			repo.commitOn(worker, sandboximage.WorkerBootStoppedBranch(branch),
				fmt.Sprintf("tick keh: the boot stopped (exit %d)", tc.code),
				map[string]string{sandboximage.WorkerBootStoppedFile("keh"): fmt.Sprintf(
					"# keh: the boot stopped before the harness started\n\nexit: %d\nreason: %s\n\nrun: r1\n",
					tc.code, tc.reason)})

			collected, err := h.ex.CollectDetail(handle)
			if err != nil {
				t.Fatalf("CollectDetail: %v", err)
			}
			if collected.Verdict != subprocess.VerdictMissingResult {
				t.Errorf("verdict %q, want missing-result: a boot that stopped answered nothing", collected.Verdict)
			}
			if collected.Result.Source.HeadSHA != nil {
				t.Errorf("the marker was collected as the attempt's head %s", *collected.Result.Source.HeadSHA)
			}
			if (collected.Infrastructure != nil) != tc.infra {
				t.Errorf("Infrastructure %+v, want set=%v: the marker must not change the class", collected.Infrastructure, tc.infra)
			}
			if !strings.Contains(collected.Message, tc.reason) {
				t.Errorf("the message does not carry the boot's reason %q: %s", tc.reason, collected.Message)
			}
			if !strings.Contains(collected.Message, fmt.Sprintf("exit %d", tc.code)) {
				t.Errorf("the message does not carry exit %d: %s", tc.code, collected.Message)
			}
			if strings.Contains(collected.Message, "the push never landed") {
				t.Errorf("the message still reads as a push that failed: %s", collected.Message)
			}
		})
	}
}

// No marker, no change: an empty branch with nothing beside it reads as it did.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestAnEmptyBranchWithNoBootMarkerReadsAsBefore(t *testing.T) {
	h, _, handle, _ := newCollectHarness(t)
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if !strings.Contains(collected.Message, "the push never landed") {
		t.Errorf("an empty branch with no marker reads %q", collected.Message)
	}
}
