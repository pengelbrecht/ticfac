package cloudflaresandbox

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// TestCollectCarriesAProtectedChangeFromACloudWorker is epic ex6's 2pn on the
// cloud substrate: the tick's whole deliverable is one [testing.commands]
// cell in .tick/runners.toml, which the container's hook and the cloud
// collect refuse to every worker (tick 9sy) — the file the local boundary
// exempts. The worker commits nothing but its report, and the report carries
// the exact change as a finding's protected_change. The collect reads the
// no-work shape (no-commits) AND hands the change on intact, which is what
// the run's acceptProtectedDelivery turns into a delivery it applies itself.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectCarriesAProtectedChangeFromACloudWorker(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	report := "# keh\n\nThe cell goes in .tick/runners.toml, which no worker here may write.\n\n" +
		"```findings v2\n[{\"kind\":\"defect\",\"title\":\"The lint command is not declared\",\"severity\":\"high\"," +
		"\"protected_change\":{\"path\":\".tick/runners.toml\",\"append\":\"[testing.commands.lint]\\nrun = \\\"make lint\\\"\"}}]\n" +
		"```\n\nSTATUS: DONE\n"
	repo.commitOn(worker, branch, "commit the report", map[string]string{resultFile("keh"): report})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.FindingsProblem != "" {
		t.Fatalf("the findings were refused: %s", collected.FindingsProblem)
	}
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("verdict %q, want %q: the collect states the no-work shape and the run decides the delivery",
			collected.Verdict, subprocess.VerdictNoCommits)
	}
	if len(collected.Findings) != 1 || collected.Findings[0].ProtectedChange == nil {
		t.Fatalf("the collect did not carry the protected change: %+v", collected.Findings)
	}
	if got := collected.Findings[0].ProtectedChange.Path; got != ".tick/runners.toml" {
		t.Errorf("the change is for %q, want .tick/runners.toml", got)
	}
	if !subprocess.CloudBoundaryRefuses(".tick/runners.toml") {
		t.Error("the cloud boundary does not refuse .tick/runners.toml: it does (worker-collect.ts boundary_files)")
	}
}
