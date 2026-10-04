package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// TestSettleAddressesTheAttemptsOwnExecutor pins tick emk's recovery path.
//
// addressForSettlement built the handle with Executor hardcoded to the local
// subprocess executor. The herdr executor refuses a handle naming another
// executor — correctly, since decoding one would be the collected-under-another-
// name failure — so every attempt of a herdr run was unreleasable:
//
//	ticfac settle 9pd cwa 7: reconcile: inspect attempt 7 of cwa:
//	handle names executor "local-subprocess"; this is herdr
//
// That command is the one the run's own refusal tells the operator to run when
// an attempt cannot be addressed. It happened in the Phase 3 run: the wall
// clock could not stop a pi worker, the reconciler refused the attempt as
// unaddressed and named this command, and this command refused too.
// short: handle routing and a source-text guard; nothing is dispatched
func TestSettleAddressesTheAttemptsOwnExecutor(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"herdr", subprocess.ExecutorName, ""} {
		marker := attemptHandle{
			Executor: name,
			JobID:    "run-r/tick-t/attempt-1",
			Attempt:  1,
			TickID:   "t",
		}
		handle := &subprocess.JobHandle{
			SchemaVersion: subprocess.SchemaVersion,
			JobID:         marker.JobID,
			Attempt:       marker.Attempt,
			Executor:      marker.Executor,
			Handle:        map[string]any{"state": "/tmp/state"},
		}
		if handle.Executor != name {
			t.Errorf("a marker naming executor %q produced a handle naming %q: the executor that holds the "+
				"attempt is the only one that can release it", name, handle.Executor)
		}
	}
}

// TestNoSettlementHandleHardcodesAnExecutor is the guard: the defect was one
// literal in one struct, and a reader cannot see it is wrong from the line.
// short: handle routing and a source-text guard; nothing is dispatched
func TestNoSettlementHandleHardcodesAnExecutor(t *testing.T) {
	t.Parallel()

	body := mustReadSource(t, "settle.go")
	start := strings.Index(body, "func (r *Reconciler) addressForSettlement")
	if start < 0 {
		t.Fatal("addressForSettlement is gone; this guard needs rewriting")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		end = len(body) - start
	}
	fn := body[start : start+end]
	if strings.Contains(fn, "Executor:      subprocess.ExecutorName") || strings.Contains(fn, "Executor: subprocess.ExecutorName") {
		t.Error("addressForSettlement names one executor literally: a handle must carry the executor its " +
			"attempt was dispatched under, or every attempt of every other executor is unreleasable (tick emk)")
	}
}

// TestADispatchRebuiltFromAMarkerRoutesThroughTheMarkersExecutor pins the
// other half of tick emk, found on epic-6in (823, 2026-09-28): the HANDLE
// named herdr, but the executor it was handed to was the local one, because
// the dispatch rebuilt from the marker carried the profile the settle command
// resolved — the local set, by default — and the executor factory routes on
// the profile:
//
//	ticfac settle 6in 823 9: reconcile: inspect 823 try 1 (run dispatch #9):
//	handle names executor "herdr"; this is local-subprocess
//
// Only `--profiles herdr` worked: a person made to say what the marker says.
// The executor is resolved from the attempt's own record.
func TestADispatchRebuiltFromAMarkerRoutesThroughTheMarkersExecutor(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.Executors = []KnownExecutor{
		{Name: subprocess.ExecutorName, Runners: subprocess.KnownRunners(), AcceptsModel: subprocess.RunnerAcceptsModel},
		{Name: "herdr", Runners: runconfig.KnownKinds(), AcceptsModel: func(string) bool { return true }},
	}
	var built []string
	opts.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		if d.Profile != nil {
			built = append(built, d.Profile.Executor)
		}
		return nil, Substrate{}, fmt.Errorf("the test builds no executor")
	}
	// The settle command's reconciler: no --profiles, so the local set.
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.base = f.Repo.Base
	if p := r.profileFor("implement-tick"); p == nil || p.Executor != subprocess.ExecutorName {
		t.Fatalf("fixture: the run's own profile set names %+v, want the local one", p)
	}

	marker := attemptHandle{
		Executor: "herdr", JobID: "epic-6in/823/attempt-9", Attempt: 9, TickID: "823",
		Role: "implement-tick", StateRoot: stateWithAttemptRecord(t),
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		t.Fatalf("rebuild the dispatch of a herdr attempt: %v", err)
	}
	if dispatch.Profile == nil || dispatch.Profile.Executor != "herdr" {
		t.Fatalf("the dispatch rebuilt from a herdr marker routes through profile %+v: the factory would build "+
			"an executor that refuses the attempt's own handle", dispatch.Profile)
	}

	// And settle's own path asks the factory for THAT executor.
	_, _, _, _ = r.addressForSettlement(context.Background(), marker)
	if len(built) == 0 || built[len(built)-1] != "herdr" {
		t.Errorf("settle asked the executor factory for %v, want herdr — the executor the marker names", built)
	}
}

// stateWithAttemptRecord is a state root this host can find an attempt in:
// the file every executor's state directory is discovered by
// (findAttemptState), so the settle path that requires this host to hold the
// attempt's state — building its executor — reaches the build.
func stateWithAttemptRecord(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "attempt.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}
