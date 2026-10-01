package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// A job the run dispatches for itself — a resolve-conflict or a repair job —
// whose boot stopped on a deterministic environment fault (the repository's
// setup, a refused model route, …) collected as missing-result, an
// operational failure its allowance retried: the same image on the same
// repository booted twice more and stopped the same way, and the stop said only
// that the allowance ran out. It now stops at once on worker_boot_fault, naming
// the cause, the boot's words and the fix — as a tick attempt's does (#178).

// bootFaultOn makes the collects of jobs whose id carries `kind` (resolve-,
// repair-) read as a boot that stopped on the repository's setup.
type bootFaultOn struct {
	Executor
	kind string
}

const roleFaultSaid = "the repository's [sandbox] setup failed: `make warm` exited 2"
const roleFaultFix = "fix the failing setup command in .tick/runners.toml"

func (e *bootFaultOn) CollectDetail(h *subprocess.JobHandle) (*subprocess.Collection, error) {
	collected, err := e.Executor.CollectDetail(h)
	if err != nil || collected == nil || collected.Result == nil || !strings.Contains(h.JobID, "/"+e.kind+"-") {
		return collected, err
	}
	collected.Verdict = subprocess.VerdictMissingResult
	collected.Result.Outcome = subprocess.OutcomeFailed
	collected.Result.FailureClass = subprocess.FailureInfrastructure
	collected.Infrastructure = &subprocess.InfrastructureFailure{Service: "the repository's [sandbox] setup",
		ExitCode: sandboximage.ExitSetup, Persistent: true, Fix: roleFaultFix}
	collected.Message = roleFaultSaid
	return collected, nil
}

func assertRoleBootFaultStop(t *testing.T, r *Reconciler, result *Result, starts []string, job string) {
	t.Helper()
	if result.State != runstate.StateFailed || r.failure == nil {
		t.Fatalf("the run ended %s with no refusal, want it stopped on the %s job's boot fault", result.State, job)
	}
	if r.failure.Reason != RefusedWorkerBootFault {
		t.Fatalf("the run stopped over %s (%s), want %s", r.failure.Reason, r.failure.Message, RefusedWorkerBootFault)
	}
	for _, want := range []string{"the repository's [sandbox] setup", roleFaultSaid, roleFaultFix, job} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the stop does not carry %q: %s", want, r.failure.Message)
		}
	}
	if len(starts) != 1 {
		t.Errorf("the run started %d %s jobs (%v), want one: a retry boots the same environment", len(starts), job, starts)
	}
}

// serial: t.Setenv (conflictSync), as the other resolve tests.
func TestAResolveJobWhoseBootStopsOnAnEnvironmentFaultStopsTheRunAtOnce(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict_resolve_silent", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)
	f.wrap = func(inner Executor) Executor { return &bootFaultOn{Executor: inner, kind: "resolve"} }

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	starts := resolveStartsIn(t, filepath.Join(os.Getenv("CONFLICT_SYNC"), "resolve.starts"))
	assertRoleBootFaultStop(t, r, result, starts, "resolve-conflict")
}

// serial: t.Setenv, as the other repair tests.
func TestARepairJobWhoseBootStopsOnAnEnvironmentFaultStopsTheRunAtOnce(t *testing.T) {
	sync := t.TempDir()
	t.Setenv("REPAIR_SYNC", sync)
	opts := fixtureOptions{mode: "gate_break_repair_noreport", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)
	f.wrap = func(inner Executor) Executor { return &bootFaultOn{Executor: inner, kind: "repair"} }

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	starts := resolveStartsIn(t, filepath.Join(sync, "repair.starts"))
	assertRoleBootFaultStop(t, r, result, starts, "repair")
}
