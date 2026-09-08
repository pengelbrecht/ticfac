package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// TestNoDurableRecordCarriesAHostPath guards the incident found on ticks PR
// #80: .ticfac/runs/gate-cia-2/decisions/{1,2}.json carried
// result.report_path as a host-absolute path under the operator's home
// directory (the executor's state root) — operator-specific content in a
// public repository. Every record a run pushes to origin — checkpoint,
// attempt, decision, evidence — must carry only run-relative facts, so this
// scans the RAW bytes landed on origin for the shapes a host path or hostname
// takes: a `/Users/` or `/home/` home directory, this test's own temp
// directory (what `t.TempDir()` — and so `ExecStateRoot` — resolves to on
// this machine), and the machine's hostname.
func TestNoDurableRecordCarriesAHostPath(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	forbidden := []string{"/Users/", "/home/", filepath.ToSlash(f.Root)}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		forbidden = append(forbidden, hostname)
	}

	head := originHeadOf(t, f, r.IntegrationBranch())
	store := openStore(t, f, r)

	scan := func(path string) {
		t.Helper()
		raw := mustRun(t, f.Repo.Origin, "git", "show", head+":"+path)
		for _, needle := range forbidden {
			if needle == "" {
				continue
			}
			if strings.Contains(raw, needle) {
				t.Errorf("%s carries a host-specific path or hostname (%q):\n%s", path, needle, raw)
			}
		}
	}

	scan(runstate.CheckpointPath(r.RunID()))

	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Fatal("the run recorded no attempts to scan")
	}
	for _, a := range attempts {
		scan(runstate.AttemptPath(r.RunID(), a.Attempt))
	}

	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) == 0 {
		t.Fatal("the run recorded no decisions to scan")
	}
	for _, d := range decisions {
		scan(runstate.DecisionPath(r.RunID(), d.Decision))
	}

	keys := store.EvidenceKeys()
	if len(keys) == 0 {
		t.Fatal("the run recorded no evidence to scan")
	}
	for _, key := range keys {
		scan(runstate.EvidencePath(r.RunID(), key))
	}
}
