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
		// Since exits 2/5/6/7/8 became deterministic boot faults, a model
		// refusal carries one too; the marker still only adds the reason.
		{"a model refusal", sandboximage.ExitModel, "the routed model could not answer a one-token request through the gateway.", true},
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
				t.Errorf("Infrastructure %+v, want set=%v", collected.Infrastructure, tc.infra)
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

// The two sources of a boot stop's exit code agree: the Inspect that observed
// the settle (#171's mark) and the worker's own boot-stopped marker on origin
// (#176). An orchestrator that never saw the settle — rebooted on a fresh disk,
// so the attempt's state holds no mark — classifies from the marker alone, and
// reaches the same fault the observation would have; when both are there and
// ever differ, the observation wins and the marker still gives the reason.
//
// short: an httptest door and local throwaway git repositories; no container.
func TestTheInspectMarkAndTheBootMarkerAgreeOnABootFault(t *testing.T) {
	for _, tc := range []struct {
		name               string
		observed, inMarker int
		wantCode           int
		wantPersistent     bool
	}{
		{"the marker alone, a silent gateway", 0, sandboximage.ExitGatewayUnavailable, sandboximage.ExitGatewayUnavailable, false},
		{"the marker alone, a failed setup", 0, sandboximage.ExitSetup, sandboximage.ExitSetup, true},
		{"both, agreeing", sandboximage.ExitPreflight, sandboximage.ExitPreflight, sandboximage.ExitPreflight, true},
		{"both, differing: the observation wins", sandboximage.ExitGatewayUnavailable, sandboximage.ExitModel,
			sandboximage.ExitGatewayUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, handle, branch := newCollectHarness(t)
			if tc.observed != 0 {
				h.door.setStatus("keh", 1, doorStatus{
					state:    subprocess.StateFailed,
					terminal: true,
					observations: []subprocess.Observation{{At: "2026-10-01T04:06:10Z", Kind: subprocess.ObsExited,
						Detail: fmt.Sprintf("the container's work process exited %d", tc.observed)}},
				})
				if _, err := h.ex.Inspect(handle, ""); err != nil {
					t.Fatalf("Inspect: %v", err)
				}
			}
			reason := fmt.Sprintf("the boot's own words for exit %d", tc.inMarker)
			repo.commitOn(repo.workerDir("worker"), sandboximage.WorkerBootStoppedBranch(branch),
				fmt.Sprintf("tick keh: the boot stopped (exit %d)", tc.inMarker),
				map[string]string{sandboximage.WorkerBootStoppedFile("keh"): fmt.Sprintf(
					"# keh: the boot stopped before the harness started\n\nexit: %d\nreason: %s\n", tc.inMarker, reason)})

			collected, err := h.ex.CollectDetail(handle)
			if err != nil {
				t.Fatalf("CollectDetail: %v", err)
			}
			got := collected.Infrastructure
			if got == nil || got.ExitCode != tc.wantCode || got.Persistent != tc.wantPersistent {
				t.Fatalf("collected boot fault %+v, want exit %d persistent=%v", got, tc.wantCode, tc.wantPersistent)
			}
			if collected.Result.FailureClass != subprocess.FailureInfrastructure {
				t.Errorf("failure class %q, want %q", collected.Result.FailureClass, subprocess.FailureInfrastructure)
			}
			if !strings.Contains(collected.Message, reason) {
				t.Errorf("the message lost the marker's reason: %s", collected.Message)
			}
		})
	}
}
