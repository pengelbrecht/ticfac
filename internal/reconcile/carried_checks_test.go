package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A carried attempt must not smuggle unchecked commits into a merge.
//
// A --carry-work release cuts the next attempt from the released attempt's
// head, and the executor measures its diff — the boundary check, the artifact
// backstop — from the base it dispatched, which is that head. The merge's
// touch: check measured from the same base. So the RELEASED attempt's own
// commits were never checked by anything: a record forged under the tracker's
// authority, or a file the tick's declaration does not name, merged behind a
// carried attempt whether or not that attempt added commits of its own. The
// checks now read the diff from the ORIGINAL base — the released attempt's
// cut point, followed through a chain of carries.

// A carried commit that writes under the tracker's authority is refused as a
// boundary violation — whether the carried attempt adds nothing or adds work
// of its own — and nothing reaches the integration branch.
func TestACarriedBoundaryViolationIsRefused(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	for _, next := range []string{"a1-adds-nothing", "report"} {
		t.Run(next, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, fixtureOptions{mode: "hang-boundary"})
			_, releasedHead := releaseCarrying(t, f, "hang-boundary")

			f.Runner = fakeRunnerArgv(t, next)
			r, result, err := f.run(f.Repo, fixtureOptions{})
			if err != nil {
				t.Fatalf("the run after the carrying release errored: %v", err)
			}
			if result.Failure == nil || result.Failure.Reason != RefusedBoundary || result.Failure.TickID != "a1" {
				t.Fatalf("the run ended %s with %+v, want %s for a1: a1's stages %v",
					result.State, result.Failure, RefusedBoundary, r.Stages("a1"))
			}
			if !strings.Contains(result.Failure.Message, ".tick/issues/forged-a1.json") {
				t.Errorf("the refusal does not name the carried write: %s", result.Failure.Message)
			}
			if containsCommit(t, f, releasedHead, "origin/epic/qeu") {
				t.Fatal("the carried boundary violation reached the integration branch")
			}
		})
	}
}

// A carried commit that touches a file the tick's declaration does not name is
// refused at the merge as an undeclared touch, even though the carried attempt
// itself only touched what it declared.
func TestACarriedUndeclaredTouchIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang-undeclared"})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"touch:work-a1.txt"} })
	_, releasedHead := releaseCarrying(t, f, "hang-undeclared")

	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the carrying release errored: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUndeclaredTouch || result.Failure.TickID != "a1" {
		t.Fatalf("the run ended %s with %+v, want %s for a1: a1's stages %v",
			result.State, result.Failure, RefusedUndeclaredTouch, r.Stages("a1"))
	}
	if !strings.Contains(result.Failure.Message, "sneaky-a1.txt") {
		t.Errorf("the refusal does not name the carried undeclared file: %s", result.Failure.Message)
	}
	if containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the carried undeclared touch reached the integration branch")
	}
	if result.State == runstate.StateCompleted {
		t.Fatal("the run completed over an undeclared carried touch")
	}
}

// An attempt the resume finishes from the integration branch — no executor,
// nothing to collect — whose head is NOT on that branch by the time the finish
// asks is refused by name, never collected through a nil executor (which
// panicked). The branch is rewritten between the two questions here, the way
// a person force-pushing the integration branch mid-resume would.
func TestAnIntegratedAttemptWhoseHeadVanishedIsRefusedNotCollected(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	releasedRef, releasedHead := releaseCarryingA1(t, f)

	blocked := fakeRunnerArgv(t, "a1-adds-nothing")
	blocked = append([]string{blocked[0], "FAKE_RUNNER_A1_STATUS=BLOCKED"}, blocked[1:]...)
	f.Runner = blocked
	_, refused, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the refusing run did not finish: %v", err)
	}
	if refused.Failure == nil || refused.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run failed as %+v, want %s", refused.Failure, RefusedNeedsHuman)
	}

	// A person merges the carried work by hand; the pre-merge head is kept so
	// the branch can later be rewritten to drop it.
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--branch", "epic/qeu", f.Repo.Origin, dir)
	configure(t, dir)
	premerge := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, dir, "git", "fetch", "--quiet", f.Repo.Dir, releasedRef+":refs/heads/carried")
	mustRun(t, dir, "git", "merge", "--no-ff", "--no-edit", "carried")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")

	// The moment the resume has decided the attempt is integrated, the
	// integration branch is rewritten: the same tree (every run record it
	// holds is kept), without the carried commit in its history.
	rewritten := false
	rewrite := func(event Event) bool {
		if rewritten || event.Tick != "a1" || event.Stage != StageAdopted {
			return false
		}
		rewritten = true
		mustRun(t, dir, "git", "fetch", "--quiet", "origin", "epic/qeu")
		tree := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "FETCH_HEAD^{tree}"))
		commit := strings.TrimSpace(mustRun(t, dir, "git", "commit-tree", tree, "-p", premerge, "-m", "a rewrite"))
		mustRun(t, dir, "git", "push", "--quiet", "--force", "origin", commit+":refs/heads/epic/qeu")
		return false
	}

	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{stopAfter: rewrite})
	if !rewritten {
		t.Fatalf("the resume never took a1 as integrated; this fixture proves nothing: %v", err)
	}
	if err != nil {
		t.Fatalf("the resumed run errored rather than refusing by name: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedIntegratedHeadMissing || result.Failure.TickID != "a1" {
		t.Fatalf("the resumed run ended %s with %+v, want %s for a1: a1's stages %v",
			result.State, result.Failure, RefusedIntegratedHeadMissing, r.Stages("a1"))
	}
	// The release command the refusal names is addressed by the run whose
	// store carries the attempt (tick qxj): r-fixture, not the epic spelling
	// a settle without --run-id opens.
	if !strings.Contains(result.Failure.Message, "--run-id r-fixture --release") ||
		!strings.Contains(result.Failure.Message, "ticfac settle qeu a1 ") {
		t.Errorf("the refusal's release command does not name the run its attempt is recorded under: %s",
			result.Failure.Message)
	}
	if containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the rewrite did not take the carried work off the integration branch; this fixture proves nothing")
	}
}
