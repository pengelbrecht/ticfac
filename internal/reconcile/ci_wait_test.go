package reconcile

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// slowCIForge is a forge whose CI concludes on its own clock: pending (or
// absent) until `after` has passed since CI was first asked about, green
// from then on — CI that takes longer than one incarnation's bound. With
// `dispatchable`, it answers none until the run starts the workflow, and
// green once it has.
type slowCIForge struct {
	*fakeForge
	mu2          sync.Mutex
	first        time.Time
	after        time.Duration
	absent       bool
	dispatchable bool
	dispatches   []string
}

var _ forge.CIDispatcher = (*slowCIForge)(nil)

func (f *slowCIForge) CI(ctx context.Context, pr forge.PullRequest) (forge.CIReport, error) {
	_, _ = f.fakeForge.CI(ctx, pr) // the call log
	f.mu2.Lock()
	defer f.mu2.Unlock()
	if f.dispatchable {
		if len(f.dispatches) > 0 {
			return forge.CIReport{State: forge.CIGreen}, nil
		}
		return forge.CIReport{State: forge.CINone}, nil
	}
	if f.first.IsZero() {
		f.first = time.Now()
	}
	if time.Since(f.first) < f.after {
		if f.absent {
			return forge.CIReport{State: forge.CINone}, nil
		}
		return forge.CIReport{State: forge.CIPending}, nil
	}
	return forge.CIReport{State: forge.CIGreen}, nil
}

func (f *slowCIForge) DispatchWorkflow(_ context.Context, workflow, ref string) error {
	f.mu2.Lock()
	defer f.mu2.Unlock()
	if !f.dispatchable {
		return nil
	}
	f.dispatches = append(f.dispatches, workflow+"@"+ref)
	return nil
}

// WAITING ON CI IS NOT A DECISION (epic-6in follow-up). CI that takes longer
// than one incarnation's bound used to end the run in closeout_ci_pending and
// a supervision halt: a person was asked to type the same command back. Now
// the supervisor continues across it — the tree unchanged, which is not a
// spin when what is awaited is the forge — and the run completes the moment
// CI does, nobody asked.
func TestCIThatOutlastsTheBoundIsWaitedOutWithoutAPerson(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	for name, absent := range map[string]bool{"pending": false, "absent": true} {
		absent := absent
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pr := &slowCIForge{fakeForge: &fakeForge{}, after: 3 * ciWaitBound, absent: absent}
			f := newFixture(t, fixtureOptions{pullRequests: pr, gateTimeout: ciWaitBound})
			declareCloseoutRule(t, f.Repo)
			_, result, err := f.supervise(f.Repo, fixtureOptions{pullRequests: pr, gateTimeout: ciWaitBound})
			if err != nil {
				t.Fatalf("supervise: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the run ended %s (%+v, halt %q): a wait on CI is not a person's to end", result.State,
					result.Failure, result.Halt)
			}
			if len(result.Resumes) == 0 {
				t.Error("no continuation happened: the test's CI did not outlast one incarnation's bound")
			}
			for _, resume := range result.Resumes {
				if !waitsOnCI(resume.Reason) {
					t.Errorf("a continuation over %s, want only CI waits", resume.Reason)
				}
			}
		})
	}
}

// A code commit with NO CI run at all — its push's run cancelled while still
// queued leaves no check run, and the pushes after it changed only .ticfac/ —
// has nothing coming to wait for. Once the silence has lasted, the run starts
// the workflow itself, once, says so, and the wait is for that run.
func TestCodeWithNoCIRunHasItsWorkflowStartedOnce(t *testing.T) {
	t.Parallel()
	pr := &slowCIForge{fakeForge: &fakeForge{}, dispatchable: true}
	f := newFixture(t, fixtureOptions{pullRequests: pr, gateTimeout: ciWaitBound})
	declareCloseoutRule(t, f.Repo)
	r, result, err := f.supervise(f.Repo, fixtureOptions{pullRequests: pr, gateTimeout: ciWaitBound})
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v, halt %q)", result.State, result.Failure, result.Halt)
	}
	pr.mu2.Lock()
	dispatches := append([]string{}, pr.dispatches...)
	pr.mu2.Unlock()
	if len(dispatches) != 1 || dispatches[0] != DefaultCIWorkflow+"@"+r.IntegrationBranch() {
		t.Errorf("dispatched %v, want %s on %s exactly once", dispatches, DefaultCIWorkflow, r.IntegrationBranch())
	}
	said := false
	for _, e := range feedStages(t, f.Repo.Dir, r.RunID()) {
		if e.Stage == StageCIRestarted {
			said = true
		}
	}
	if !said {
		t.Error("the started workflow is not in the feed: an automatic intervention nobody can see")
	}
}
