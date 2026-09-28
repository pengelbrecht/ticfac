package reconcile

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A run-dispatched job that never STARTED did not answer either (#79's
// allowance, extended). Two ways epic-6in could have stopped — and, on the
// resume, did stop — on a gate failure it had a repair for:
//
//  1. Start failed with a plain substrate error (herdr's agent_name_taken on
//     the pre-#88 build). The run halted at once instead of recording the
//     job as operational and dispatching the next one of the allowance.
//  2. Start refused the job as SETTLED while its branch sat at the commit
//     the job was cut from. The finish-from-the-branch path took that branch
//     as the job's answer: the "repair" was recorded merged over nothing,
//     the gate failed again over the same tree, and the run stopped saying
//     the tick had had its one repair.
//
// Each test is one full fixture run, skipped under -short.

// startFaults fails the role jobs' Starts its rule picks, and passes every
// other call through. It records the job id of every role-job Start asked.
type startFaults struct {
	Executor
	f     *fixture
	rule  func(spec *subprocess.JobSpec, n int) error // n: this role job's 1-based start count
	state *startLog
}

type startLog struct {
	mu     sync.Mutex
	starts []string
}

func (e *startFaults) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if spec.Role == RoleRepairGate || spec.Role == RoleResolveConflict {
		e.state.mu.Lock()
		e.state.starts = append(e.state.starts, spec.JobID)
		n := len(e.state.starts)
		e.state.mu.Unlock()
		if err := e.rule(spec, n); err != nil {
			return nil, err
		}
	}
	return e.Executor.Start(spec)
}

// all is the distinct job ids asked to start, in first-asked order: a
// re-Start of the same job (an adoption on a later pass) is the same job.
func (l *startLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	for _, id := range l.starts {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func withStartFaults(f *fixture, rule func(spec *subprocess.JobSpec, n int) error) *startLog {
	log := &startLog{}
	f.wrap = func(inner Executor) Executor { return &startFaults{Executor: inner, f: f, rule: rule, state: log} }
	return log
}

func roleDecisions(t *testing.T, r *Reconciler, role string) []runstate.Decision {
	t.Helper()
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var out []runstate.Decision
	for _, d := range decisions {
		if d.Role == role {
			out = append(out, d)
		}
	}
	return out
}

var errSubstrate = errors.New("herdr agent.start for tick-a1-a1 failed: agent_name_taken: agent name tick-a1-a1 is already used")

// 1. A repair job whose Start fails with a plain error is operational: it
// spends one retry of the allowance, and the next repair (-r2) repairs the
// tick.
func TestARepairThatCouldNotStartIsRetriedNotAStop(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "gate_break_repair", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)
	log := withStartFaults(f, func(spec *subprocess.JobSpec, n int) error {
		if n == 1 {
			return errSubstrate
		}
		return nil
	})

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a repair that could not start never answered, and is retried",
			result.State, result.Reason)
	}
	starts := log.all()
	if len(starts) != 2 || !strings.HasSuffix(starts[1], "-r2") {
		t.Fatalf("repair starts %v, want the failed one and its -r2", starts)
	}
	repairs := roleDecisions(t, r, RoleRepairGate)
	if len(repairs) != 2 || repairs[0].Response["status"] != "failed" ||
		repairs[0].Response["failure"] != failureOperational || repairs[1].Response["status"] != "merged" {
		t.Fatalf("repair decisions %v, want failed/operational then merged", repairs)
	}
}

// And the retries are bounded: a repair that can never start stops the run
// past the bound, naming every job it tried.
func TestRepairsThatNeverStartAreBoundedAndTheStopNamesEveryOne(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "gate_break_repair", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)
	log := withStartFaults(f, func(*subprocess.JobSpec, int) error { return errSubstrate })

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: repairs that never start are bounded, and the bound is a stop", result.State)
	}
	if starts := log.all(); len(starts) != 1+maxOperationalRetries {
		t.Fatalf("the run started %d repair jobs (%v), want %d", len(starts), starts, 1+maxOperationalRetries)
	}
	if r.failure == nil {
		t.Fatal("the bound stopped the run with no refusal to read")
	}
	for _, want := range []string{"failed without delivering a repair", "tick-a1/repair-", "-r2", "-r3",
		"agent_name_taken"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the stop does not name %q: %s", want, r.failure.Message)
		}
	}
}

// 2. A repair the executor reports SETTLED whose branch carries nothing past
// the commit it was cut from never answered: it is not recorded merged, and
// the next repair of the allowance repairs the tick.
func TestAnEmptySettledRepairIsNeverRecordedMerged(t *testing.T) {
	t.Parallel()
	opts := fixtureOptions{mode: "gate_break_repair", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)
	log := withStartFaults(f, func(spec *subprocess.JobSpec, n int) error {
		if n != 1 {
			return nil
		}
		// What a job that never ran leaves: its branch, at its base.
		branch := strings.TrimPrefix(spec.Source.WriteRef, "refs/heads/")
		mustRun(t, f.Repo.Dir, "git", "branch", "--force", branch, spec.Source.BaseSHA)
		return &subprocess.Refusal{Reason: subprocess.RefusedSettled,
			Message: "attempt 1 of " + spec.JobID + " already settled as failed"}
	})

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a settled repair with no commit is a repair that never answered",
			result.State, result.Reason)
	}
	if starts := log.all(); len(starts) != 2 || !strings.HasSuffix(starts[1], "-r2") {
		t.Fatalf("repair starts %v, want the empty one and its -r2", starts)
	}
	repairs := roleDecisions(t, r, RoleRepairGate)
	if len(repairs) != 2 || repairs[0].Response["status"] != "failed" ||
		repairs[0].Response["failure"] != failureOperational {
		t.Fatalf("repair decisions %v: the empty repair is failed/operational, never merged", repairs)
	}
}

// The resolve-conflict job holds the same two rules.
//
// serial: conflictSync states the process environment for the fake
// runner's workers, and t.Setenv forbids a parallel test.
func TestAResolveThatCouldNotStartOrLeftNothingIsRetried(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, c := range []struct {
		name string
		fail func(t *testing.T, f *fixture, spec *subprocess.JobSpec) error
	}{
		{"plain start error", func(*testing.T, *fixture, *subprocess.JobSpec) error { return errSubstrate }},
		{"settled at its base", func(t *testing.T, f *fixture, spec *subprocess.JobSpec) error {
			branch := strings.TrimPrefix(spec.Source.WriteRef, "refs/heads/")
			mustRun(t, f.Repo.Dir, "git", "branch", "--force", branch, spec.Source.BaseSHA)
			return &subprocess.Refusal{Reason: subprocess.RefusedSettled, Message: spec.JobID + " already settled"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			conflictSync(t)
			opts := fixtureOptions{mode: "conflict", gate: resolveGate}
			f := newFixture(t, opts)
			seedSharedFile(t, f)
			log := withStartFaults(f, func(spec *subprocess.JobSpec, n int) error {
				if n == 1 {
					return c.fail(t, f, spec)
				}
				return nil
			})
			r, result, err := f.run(f.Repo, opts)
			if err != nil {
				t.Fatalf("the run did not finish: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the run ended %s (%s): the resolve never answered, and is retried", result.State, result.Reason)
			}
			if starts := log.all(); len(starts) != 2 || !strings.HasSuffix(starts[1], "-r2") {
				t.Fatalf("resolve starts %v, want the failed one and its -r2", starts)
			}
			got := resolveDecisionsOf(t, r, "a2")
			if len(got) != 2 || got[0].Response["failure"] != failureOperational || got[1].Response["status"] != "merged" {
				t.Fatalf("resolve decisions %v, want failed/operational then merged", got)
			}
		})
	}
}
