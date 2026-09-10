package reconcile

import (
	"encoding/json"
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

// TestAFailingGatesOutputCarriesNoHostPath is TestNoDurableRecordCarriesAHostPath's
// case for a gate that FAILS. The integrated gate runs in an os.MkdirTemp
// worktree OUTSIDE the repository (git.go's tempWorktree), a directory git
// tears down before the run ends, so it can only be caught by inspecting the
// evidence gate.go recorded from a command whose failure output names its own
// working directory — no forbidden string here happens to fall under
// `/Users/` or `/home/` the way the earlier incident did, so this scans for
// the gate worktree's own path specifically, in addition to the general
// scan above.
func TestAFailingGatesOutputCarriesNoHostPath(t *testing.T) {
	const failingGateNamingItsOwnWorktree = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "echo \"failed in $(pwd)\" >&2; exit 3", description = "refuses, naming its own worktree" }
`
	f := newFixture(t, fixtureOptions{gate: failingGateNamingItsOwnWorktree})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: failingGateNamingItsOwnWorktree})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	head := originHeadOf(t, f, r.IntegrationBranch())
	store := openStore(t, f, r)
	keys := store.EvidenceKeys()
	if len(keys) == 0 {
		t.Fatal("the failing run recorded no evidence to scan")
	}

	seenFailure := false
	for _, key := range keys {
		path := runstate.EvidencePath(r.RunID(), key)
		raw := mustRun(t, f.Repo.Origin, "git", "show", head+":"+path)
		// "ticfac-gate-" is tempWorktree's own prefix (gate.go passes it to
		// os.MkdirTemp): its presence proves the worktree path leaked past
		// redaction, regardless of where the host happens to keep its temp
		// directory.
		if strings.Contains(raw, "ticfac-gate-") {
			t.Errorf("%s carries the gate's own temp-worktree path unredacted:\n%s", path, raw)
		}
		var evidence runstate.Evidence
		if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
			t.Fatalf("%s is not valid evidence JSON: %v", path, err)
		}
		if evidence.Result == "fail" {
			seenFailure = true
			if evidence.Output.Inline == nil || !evidence.Output.Inline.Redacted {
				t.Errorf("%s is a failing gate's output that was never run through redaction (redacted=false)", path)
			}
		}
	}
	if !seenFailure {
		t.Fatal("no evidence record on origin says the gate failed; this test proves nothing about a failing gate")
	}
}
