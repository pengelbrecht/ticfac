package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// epic-2jn, rix attempt 45 (2026-09-27): the worker committed 56a36027, its
// supervisor pushed it, and two minutes later the worker ran `git commit
// --amend` (72ee73aa). On collect the run tried to put 72ee73aa on origin,
// origin held 56a36027, which is not its ancestor, and the merge was refused
// as though somebody else had written the attempt's ref: merge_failed, then
// supervision_halted, held for a person over the worker's own amend.
//
// A worker rewriting its own pushed history is ordinary. The attempt branch's
// reflog proves origin's head is a state this attempt held, and then the
// collected head replaces it. Origin holding anything the branch never held is
// still the refusal it was.

func TestAWorkerThatAmendsItsPushedCommitIsMergedNotHeld(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	opts := fixtureOptions{mode: "amend-pushed"}
	f := newFixture(t, opts)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	for _, event := range r.Journal() {
		if strings.Contains(event.Detail, "could not be put on") {
			t.Errorf("a merge was refused over the worker's own amend: %s/%s: %s", event.Tick, event.Stage, event.Detail)
		}
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s (%+v)", result.State, result.Reason, result.Failure)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 ended %s, want closed", current.Status)
	}
	integrated := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+r.IntegrationBranch()+":work-a1.txt")
	if !strings.Contains(integrated, "amended by a1 after it was pushed") {
		t.Errorf("the integrated tree does not carry the amended commit (work-a1.txt reads %q)", integrated)
	}
}

// The rule in its own right, at the collect's push: what the reflog proves is
// replaced, and what it does not is refused and left alone.
func TestOnlyAStateTheAttemptBranchHeldIsReplacedByItsCollectedHead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "heads", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	r := &Reconciler{git: g, opts: Options{Remote: "origin"}, now: time.Now}
	head := func(sha string) *subprocess.Collection {
		return &subprocess.Collection{Result: &subprocess.JobResult{
			Source: subprocess.ResultSource{HeadSHA: &sha}}}
	}
	// attempt makes the attempt's worktree on its own branch, commits in it
	// and pushes that commit the way the supervisor's timer does; it answers
	// the worktree and the pushed sha.
	attempt := func(branch string) (string, string) {
		wt := filepath.Join(root, strings.ReplaceAll(branch, "/", "-"))
		mustRun(t, repo.Dir, "git", "worktree", "add", "--quiet", "-b", branch, wt, repo.Base)
		write(t, filepath.Join(wt, "work.txt"), "the first draft\n")
		mustRun(t, wt, "git", "add", "-A")
		mustRun(t, wt, "git", "commit", "--quiet", "-m", "the worker's commit")
		mustRun(t, wt, "git", "push", "--quiet", "origin", "HEAD:"+refFor(branch))
		return wt, strings.TrimSpace(mustRun(t, wt, "git", "rev-parse", "HEAD"))
	}
	amend := func(wt string) string {
		write(t, filepath.Join(wt, "work.txt"), "the first draft, amended\n")
		mustRun(t, wt, "git", "add", "-A")
		mustRun(t, wt, "git", "commit", "--quiet", "--amend", "--no-edit")
		return strings.TrimSpace(mustRun(t, wt, "git", "rev-parse", "HEAD"))
	}

	// rix/45: pushed, then amended. The amend is collected and replaces it.
	const branch = "ticfac/run-epic-2jn/tick-rix/attempt-45"
	wt, pushed := attempt(branch)
	amended := amend(wt)
	got, err := r.durableAttemptHead(branch, head(amended))
	if err != nil || got != amended {
		t.Fatalf("durableAttemptHead = %s, %v; want the amended head %s to replace the pushed %s",
			short(got), err, short(amended), short(pushed))
	}
	if onOrigin := originRefSHA(t, repo.Origin, refFor(branch)); onOrigin != amended {
		t.Errorf("origin holds %s, want the amended head %s", short(onOrigin), short(amended))
	}

	// The same amend, but origin has since been moved to a commit this
	// branch never held: somebody else's write, refused and left alone.
	const other = "ticfac/run-epic-2jn/tick-rix/attempt-46"
	wt, _ = attempt(other)
	amended = amend(wt)
	foreign := strings.TrimSpace(mustRun(t, repo.Origin, "git", "commit-tree", "-p", repo.Base, "-m", "foreign",
		strings.TrimSpace(mustRun(t, repo.Origin, "git", "rev-parse", repo.Base+"^{tree}"))))
	mustRun(t, repo.Origin, "git", "update-ref", refFor(other), foreign)
	if _, err := r.durableAttemptHead(other, head(amended)); err == nil ||
		!strings.Contains(err.Error(), "could not be put on") {
		t.Fatalf("an origin head the attempt branch never held was overwritten (err %v)", err)
	}
	if onOrigin := originRefSHA(t, repo.Origin, refFor(other)); onOrigin != foreign {
		t.Errorf("origin's %s moved to %s; the refusal wrote something", other, short(onOrigin))
	}

	// preserveAttemptWork, the collect's early push, follows the same rule.
	const third = "ticfac/run-epic-2jn/tick-rix/attempt-47"
	wt, _ = attempt(third)
	amended = amend(wt)
	r.preserveAttemptWork(attemptHandle{TickID: "rix", WriteRef: refFor(third), BaseSHA: repo.Base})
	if onOrigin := originRefSHA(t, repo.Origin, refFor(third)); onOrigin != amended {
		t.Errorf("preserveAttemptWork left origin at %s, want the amended head %s", short(onOrigin), short(amended))
	}

	// And so does the SIGTERM flush's push of the attempt branch.
	const fourth = "ticfac/run-epic-2jn/tick-rix/attempt-48"
	wt, _ = attempt(fourth)
	amended = amend(wt)
	var said []string
	r.evacuateAttempt(evacuationAttempt{tickID: "rix", attempt: 48, state: t.TempDir(),
		work: subprocess.AttemptWork{TickID: "rix", Attempt: 48, JobID: "rix-48", Branch: fourth, Worktree: wt, Remote: "origin"}},
		time.Now().Add(time.Minute), func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) })
	if onOrigin := originRefSHA(t, repo.Origin, refFor(fourth)); onOrigin != amended {
		t.Errorf("the evacuation left origin at %s, want the amended head %s: %s",
			short(onOrigin), short(amended), strings.Join(said, "; "))
	}
}
