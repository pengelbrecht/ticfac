package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// refuseFirstResolve fails the first resolve-conflict start it sees, the way
// hn6's cloud resolve jobs failed at run start (run_09ebaf29), and lets every
// later start through.
type refuseFirstResolve struct {
	Executor
	mu      *sync.Mutex
	refused *bool
}

func (e *refuseFirstResolve) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	e.mu.Lock()
	first := spec.Role == RoleResolveConflict && !*e.refused
	if first {
		*e.refused = true
	}
	e.mu.Unlock()
	if first {
		return nil, errors.New("the sandbox dispatch door did not answer")
	}
	return e.Executor.Start(spec)
}

// Epic hn6, run_09ebaf29: the run-start fold of main did not land, and the
// run halted for a person with every tick of the epic open and workable.
//
// Now the fold is DEFERRED: the run works its ticks on the unfolded branch,
// retries the fold once they are closed (it lands), and — because main
// carried a tick filed after the fork, which the unfolded branch could not
// show — stops resumably to plan it; the next incarnation works it. No person
// at any point, and the epic ends carrying main.
//
// Without the deferral this run ends failed with base_refresh_conflict before
// a1 is ever dispatched.
func TestAFoldThatDoesNotLandAtRunStartIsDeferredAndTheRunCompletesWithoutAPerson(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "conflict"}
	f := newFixture(t, opts)
	tracker, _ := newRepoTracker(t, f.Repo.Dir)

	seedTracker(t, f.Repo,
		tk.Tick{ID: "qeu", Title: "the epic", Status: "open", Type: "epic", BaseBranch: "main"},
		tk.Tick{ID: "a1", Title: "the tick the run was cut for", Status: "open", Type: "task", Parent: "qeu"},
	)
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.0.0\n", "the manifest both sides start from")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")
	commitOnIntegrationBranch(t, f, "epic/qeu", "deps.txt",
		"require example.com/a v1.0.0\nrequire example.com/epic v0.3.0\n", "the epic's tick adds a requirement")
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.2.0\n", "main bumps a requirement")
	commitOnBase(t, f.Repo, ".tick/issues/n1.json", recordJSON(t,
		tk.Tick{ID: "n1", Title: "the follow-up filed on main", Status: "open", Type: "task", Parent: "qeu"}),
		"file n1 on main")

	var mu sync.Mutex
	refused := false
	f.wrap = func(inner Executor) Executor {
		return &refuseFirstResolve{Executor: inner, mu: &mu, refused: &refused}
	}

	runOpts := f.options(f.Repo, opts)
	runOpts.Tracker = tracker
	runOpts.BaseRef = "HEAD"
	r, err := New(runOpts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Supervise(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s; halt: %s): a fold that did not land at run start is deferred, not a stop "+
			"for a person", result.State, result.Reason, result.Halt)
	}
	if !refused {
		t.Fatal("no resolve-conflict job was refused at run start: this test did not exercise the deferral")
	}

	events := feedStages(t, f.Repo.Dir, runOpts.RunID)
	if countStage(events, StageRefreshDeferred) == 0 {
		t.Errorf("the feed never says the fold was deferred (%s)", StageRefreshDeferred)
	}
	var closed []string
	for _, event := range events {
		if event.Stage == StageClosed && event.TickID != nil {
			closed = append(closed, *event.TickID)
		}
	}
	for _, want := range []string{"a1", "n1"} {
		found := false
		for _, id := range closed {
			found = found || id == want
		}
		if !found {
			t.Errorf("%s was never closed (closed: %v)", want, closed)
		}
	}
	replanned := false
	for _, resume := range result.Resumes {
		replanned = replanned || resume.Reason == RefusedFoldReplan
	}
	if !replanned {
		t.Errorf("the run was not continued over %s (resumes: %+v): n1 came in with the fold", RefusedFoldReplan,
			result.Resumes)
	}
	mainHead := strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("main")))
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", mainHead, refFor("epic/qeu")) {
		t.Errorf("epic/qeu does not carry main at %s: the deferred fold never landed", short(mainHead))
	}
}
