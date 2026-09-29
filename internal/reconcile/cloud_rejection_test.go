package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Epic hn6's second cloud run (2026-09-29): a rejected sandbox attempt halted
// the run for a person, where PR #110 releases a rejected attempt and
// redispatches it with nobody. On the sandbox substrate the attempt's work
// never has a branch in the orchestrator's checkout — the container pushed a
// LANDING branch of its own and the collect read it there — so the run's
// disposal, which reads the attempt's write ref, found nothing to release and
// the rejection fell through to the person-stop.

// landingExecutor makes the local executor's attempts look like a sandbox's
// at collect: the work the collect reports is on a landing branch on origin,
// and neither the attempt's local branch nor its write ref on origin carry
// it. What the collect ANSWERS is the executor's own; reshape, when set, may
// rewrite it for one try the way an older collect answered.
type landingExecutor struct {
	Executor
	repo    string
	remote  string
	try     int
	tick    string
	reshape func(tick string, try int, collected *subprocess.Collection)
}

func (e *landingExecutor) CollectDetail(h *subprocess.JobHandle) (*subprocess.Collection, error) {
	collected, err := e.Executor.CollectDetail(h)
	if err != nil || collected == nil || collected.Result == nil {
		return collected, err
	}
	source := collected.Result.Source
	branch := branchOf(source.WriteRef)
	if source.HeadSHA != nil && branch != "" {
		// The container's push: the work lands beside the write ref, which
		// holds nothing, and the orchestrator's clone has no branch of it.
		landing := "tick/landing/" + strings.ReplaceAll(branch, "/", "-")
		runGitQuiet(e.repo, "push", "--quiet", e.remote, *source.HeadSHA+":refs/heads/"+landing)
		runGitQuiet(e.repo, "push", "--quiet", e.remote, "--delete", "refs/heads/"+branch)
		runGitQuiet(e.repo, "update-ref", "refs/heads/"+branch, source.BaseSHA)
	}
	if e.reshape != nil {
		e.reshape(e.tick, e.try, collected)
	}
	return collected, nil
}

// landingOptions wires the fixture's local executor through landingExecutor.
func (f *fixture) landingOptions(opts fixtureOptions, reshape func(string, int, *subprocess.Collection)) Options {
	options := f.options(f.Repo, opts)
	options.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		inner, substrate, err := f.newExecutor(d)
		if err != nil {
			return nil, substrate, err
		}
		return &landingExecutor{Executor: inner, repo: d.Repo, remote: d.Remote, tick: d.TickID, try: d.Try,
			reshape: reshape}, substrate, nil
	}
	return options
}

// A boundary violation WITH commits, on a substrate whose work is on a landing
// branch: released without carry and redispatched fresh, the run completes
// with nobody — exactly as the local substrate's does (PR #110).
func TestASandboxBoundaryViolationWithCommitsIsRedispatchedWithNoPerson(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "boundary-first", gate: tierGate})
	r, err := New(f.landingOptions(fixtureOptions{mode: "boundary-first"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(t.Context())
	if err != nil {
		t.Fatalf("the run did not finish: %v\n%s", err, journalText(r))
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a sandbox attempt's boundary violation is released and redispatched, "+
			"not held for a person\n%s", result.State, result.Failure, journalText(r))
	}
	if _, ok := journalLine(r, "a1", StageRejectedWorkReleased); !ok {
		t.Fatalf("no %s line: the rejected work was not released by the run:\n%s",
			StageRejectedWorkReleased, journalText(r))
	}
	first := markerOfTry(t, r, "a1", 1)
	head := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet",
		refFor(branchOf(first.WriteRef))))
	if head == "" || head == first.BaseSHA {
		t.Errorf("the violating attempt's work is not on its own write ref on origin: a rejection on origin " +
			"must mean the work it was about is on origin too")
	}
	if got := runGitQuiet(f.Repo.Origin, "ls-tree", "-r", "--name-only", "refs/heads/epic/qeu"); strings.Contains(got, "forged-a1") {
		t.Errorf("the forged tracker record reached the integration branch")
	}
}

// The live shape itself: a collect that answered no-commits AND named
// boundary violations (the two-dot diff read the base's own files as the
// worker's). The attempt left no work, so there is nothing for a person to
// decide: the run is resumed and the tick redispatched, as for no-commits.
func TestABoundaryRejectionThatLeftNoWorkDoesNotHaltForAPerson(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: tierGate})
	reshape := func(tick string, try int, collected *subprocess.Collection) {
		if tick != "a1" || try != 1 {
			return
		}
		collected.Verdict = subprocess.VerdictNoCommits
		collected.Result.Outcome = subprocess.OutcomeFailed
		collected.Result.Source.HeadSHA = nil
		collected.Result.Source.Commits = 0
		collected.BoundaryViolations = []string{".ticfac/runs/an-earlier-run/checkpoint.json", ".tick/issues/a1.json"}
		collected.Message = "the attempt branch carries no commit beyond the base it was cut from"
	}
	options := f.landingOptions(fixtureOptions{}, reshape)
	r, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Supervise(t.Context())
	if err != nil {
		t.Fatalf("the supervised run did not finish: %v", err)
	}
	feed := feedStages(t, f.Repo.Dir, r.RunID())
	for _, e := range feed {
		if e.Stage == StageSupervisionHalted {
			t.Errorf("the supervisor halted: %s", e.Detail)
		}
	}
	if result.State != runstate.StateCompleted || result.Halt != "" {
		var story strings.Builder
		for _, e := range feed {
			story.WriteString(e.Stage + " " + e.Detail + "\n")
		}
		t.Fatalf("the run ended %s, halt %q: a rejection that left nothing is redispatched, never a person's\n%s",
			result.State, result.Halt, story.String())
	}
}
