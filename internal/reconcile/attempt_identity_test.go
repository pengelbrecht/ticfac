package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The final review's two blockers, and the repairs around them.
//
// Both are about one thing said two ways: the git identity of an attempt was
// coarser than the dispatch identity everything else in this package is built
// on. One ref per TICK meant a second attempt could not be started in the same
// checkout, its pushes collided with the first one's, and — because origin's
// head for that ref was merged whether or not it was the head that had been
// collected — a stale or partial subset of an attempt's work could be merged,
// gated, closed and then deleted.

// refuseAdvancing makes the fixture's origin refuse to ADVANCE any attempt ref
// while still allowing one to be created. It is the durable half of a push
// that fails: the supervisor ignores a failed push (it writes a log note and
// records the runner's exit anyway), so origin keeps the commit it already had
// and the branch keeps the ones it did not.
func refuseAdvancing(t *testing.T, origin string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "update")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"refs/heads/ticfac/*)\n" +
		"\tif [ \"$2\" != \"0000000000000000000000000000000000000000\" ]; then\n" +
		"\t\techo 'the fixture refuses to advance this ref' >&2\n" +
		"\t\texit 1\n" +
		"\tfi\n" +
		"\t;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// branchHead is a LOCAL branch's commit, or "" when there is no such branch.
func branchHead(dir, branch string) string {
	return strings.TrimSpace(runGitQuiet(dir, "rev-parse", "--verify", "--quiet", branch+"^{commit}"))
}

func runGitQuiet(dir string, args ...string) string {
	g := &repoGit{dir: dir, name: "ticfac test", email: "test@example.com", remote: "origin"}
	out, _ := g.run(dir, args...)
	return out
}

// Blocker one. The supervisor's final push failed, so origin carries an
// earlier commit of the attempt and the branch carries a later one. The
// reconciler must merge what it COLLECTED or nothing at all — and here it can
// do neither, because the same failure that stranded the commit stops it being
// pushed. So: no close, and no commit thrown away.
func TestOriginsHeadIsNotMergedWhenItIsNotTheHeadThatWasCollected(t *testing.T) {
	opts := fixtureOptions{mode: "unpushed-tail"}
	f := newFixture(t, opts)
	refuseAdvancing(t, f.Repo.Origin)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: a merge of a head nobody collected is not a run that completed", result.State)
	}

	// The tick is NOT closed. A close here would be a close over commits the
	// attempt produced and the merge never saw.
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Errorf("a1 was closed although only part of its work could be merged")
	}

	// The commits are all still there: the branch still carries the one that
	// never reached the remote.
	spec := f.spec("a1")
	if spec == nil {
		t.Fatal("a1 was never dispatched")
	}
	branch := branchOf(spec.Source.WriteRef)
	head := branchHead(f.Repo.Dir, branch)
	if head == "" {
		t.Fatalf("the attempt branch %s is gone; the commits nothing merged went with it", branch)
	}
	tree := runGitQuiet(f.Repo.Dir, "ls-tree", "--name-only", head)
	if !strings.Contains(tree, "late-a1.txt") {
		t.Errorf("%s no longer carries the commit that never reached the remote:\n%s", branch, tree)
	}

	// And the partial head origin did have was not merged into the epic.
	remote := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet", refFor(branch)))
	if remote == "" {
		t.Fatalf("the fixture never got a partial head onto origin: there was nothing to be tempted by")
	}
	if remote == head {
		t.Fatalf("the fixture's push refusal did not work: origin and the branch agree at %s", short(head))
	}
	if mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", remote, refFor(r.IntegrationBranch())) {
		t.Errorf("%s carries %s, the partial head that was never collected", r.IntegrationBranch(), short(remote))
	}
}

// Blocker two, both halves. A rejected attempt is torn down rather than left
// in the checkout forever, and the attempt AFTER it is dispatched on a ref of
// its own — so a second attempt works in the same checkout whether or not the
// first one's teardown ever ran.
func TestARejectedAttemptIsTornDownAndTheNextIsDispatchedInTheSameCheckout(t *testing.T) {
	t.Run("the rejected attempt is disposed", func(t *testing.T) {
		opts := fixtureOptions{mode: "blocked-first"}
		f := newFixture(t, opts)

		_, result, err := f.run(f.Repo, opts)
		if err != nil {
			t.Fatalf("the run did not finish: %v", err)
		}
		if result.State != runstate.StateFailed {
			t.Fatalf("the run ended %s; the blocked attempt should have refused it", result.State)
		}

		spec := f.spec("a1")
		if spec == nil {
			t.Fatal("a1 was never dispatched")
		}
		branch := branchOf(spec.Source.WriteRef)
		if head := branchHead(f.Repo.Dir, branch); head != "" {
			t.Errorf("the rejected attempt's branch %s is still in the checkout at %s", branch, short(head))
		}
		if trees := runGitQuiet(f.Repo.Dir, "worktree", "list"); strings.Contains(trees, "attempt-1") {
			t.Errorf("the rejected attempt's worktree is still registered:\n%s", trees)
		}
	})

	t.Run("the next attempt is dispatched in the same checkout", func(t *testing.T) {
		opts := fixtureOptions{mode: "blocked-first"}
		f := newFixture(t, opts)

		// Cut the run the moment the rejection is durable — BEFORE any
		// teardown — so the second incarnation meets exactly what a crash
		// leaves behind: attempt 1's branch and worktree, still there.
		killed := fixtureOptions{mode: "blocked-first", stopAfter: stopAt("a1", StageRejected)}
		_, _, err := f.run(f.Repo, killed)
		killedAfter(t, err, "a1", StageRejected)

		first := f.spec("a1")
		if first == nil {
			t.Fatal("a1 was never dispatched")
		}
		if head := branchHead(f.Repo.Dir, branchOf(first.Source.WriteRef)); head == "" {
			t.Fatalf("the fixture's cut removed %s; there is nothing in the way to redispatch around",
				branchOf(first.Source.WriteRef))
		}

		// The SAME checkout, not a fresh clone.
		restarted, result, err := f.run(f.Repo, opts)
		if err != nil {
			t.Fatalf("the restart did not finish: %v", err)
		}
		if result.State != runstate.StateCompleted {
			t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
		}
		if got := restarted.Stages("a1"); !contains(got, StageRedispatched) {
			t.Errorf("the restart did not redispatch the spent attempt: %v", got)
		}

		second := f.spec("a1")
		if second.Source.WriteRef == first.Source.WriteRef {
			t.Errorf("attempt 2 was dispatched on %s, the ref attempt 1 already owns", second.Source.WriteRef)
		}
		if !strings.HasSuffix(second.Source.WriteRef, "/attempt-2") {
			t.Errorf("attempt 2's write ref is %s; it carries no attempt identity", second.Source.WriteRef)
		}
		current, err := f.Tracker.Show(context.Background(), "a1")
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("a1 is %s after the restart", current.Status)
		}
	})
}

// SPEC §4.3's golden shape, and the namespace the credential bounds. The two
// are one rule: the ref carries (run, tick, attempt), and the grant may
// advance nothing outside this RUN's namespace.
func TestTheWriteRefCarriesRunTickAndAttempt(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	r.base = f.Repo.Base

	dispatch, marker, err := r.planDispatch(planEntry{TickID: "a1", Role: "implement-tick"}, 2, 0)
	if err != nil {
		t.Fatalf("plan the dispatch: %v", err)
	}
	want := "refs/heads/ticfac/run-r-fixture/tick-a1/attempt-2"
	if dispatch.WriteRef != want {
		t.Errorf("the dispatch writes %s, want %s", dispatch.WriteRef, want)
	}
	if marker.WriteRef != want {
		t.Errorf("the marker records %s, want %s", marker.WriteRef, want)
	}
	spec := r.jobSpec(dispatch)
	if spec.Source.WriteRef != want {
		t.Errorf("the job spec asks for %s, want %s", spec.Source.WriteRef, want)
	}
	prefix := spec.Credentials.Source.WriteRefPrefix()
	if prefix != "refs/heads/ticfac/run-r-fixture/" {
		t.Errorf("the write grant bounds %q, which is not this run's namespace", prefix)
	}
	if !strings.HasPrefix(spec.Source.WriteRef, prefix) {
		t.Errorf("%s is outside the namespace the grant bounds (%s): the executor would refuse it",
			spec.Source.WriteRef, prefix)
	}
}

// Appendix A #10's premise, at the reconciler. The base the boundary is
// measured from must be the base this run DISPATCHED — which lives on origin,
// in the marker — and not the one in the attempt record beside the worker's
// own worktree, which the worker's uid can rewrite.
func TestACollectMeasuredFromAForgedBaseIsRefused(t *testing.T) {
	opts := fixtureOptions{mode: "forge-base"}
	f := newFixture(t, opts)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s although the attempt was collected against a base it chose itself", result.State)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Errorf("a1 was closed on a diff measured from a base the attempt wrote itself")
	}
	// And the write the forged base hid never reached the integration branch.
	head := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet",
		refFor(r.IntegrationBranch())))
	if head != "" {
		tree := runGitQuiet(f.Repo.Origin, "ls-tree", "-r", "--name-only", head)
		if strings.Contains(tree, "forged-a1.json") {
			t.Errorf("the forged tracker record is on %s", r.IntegrationBranch())
		}
	}
}

// Attempt numbers are RUN-wide, so the marker that refuses a create is not
// necessarily about the tick that was refused. Adopting it without looking
// would give one tick another's job.
func TestADispatchMarkerForAnotherTickIsNotAdopted(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	r.tracker = f.Tracker.In(f.Repo.Dir)
	r.store, err = runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.branch, RunID: r.runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	number := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			number = attempt.Attempt
		}
	}
	if number == 0 {
		t.Fatal("the completed run recorded no attempt for a1")
	}

	before := f.startCount(f.spec("b1").JobID)
	_, _, _, adopted, err := r.adoptConflicted(context.Background(), "b1", number, "conflict_exists")
	if err != nil {
		t.Fatalf("resolving the conflict failed: %v", err)
	}
	if adopted {
		t.Errorf("attempt %d is a1's and was adopted as b1's", number)
	}
	if after := f.startCount(f.spec("b1").JobID); after != before {
		t.Errorf("resolving the conflict started a job (%d -> %d)", before, after)
	}

	// The same number, asked about the tick it actually belongs to, IS adopted.
	_, _, marker, adopted, err := r.adoptConflicted(context.Background(), "a1", number, "conflict_exists")
	if err != nil {
		t.Fatalf("adopting a1's own marker failed: %v", err)
	}
	if !adopted || marker.TickID != "a1" {
		t.Errorf("a1's own marker at attempt %d was not adopted (%+v)", number, marker)
	}
	for _, ts := range r.ticks {
		if ts.TickID == "a1" && ts.Attempt != number {
			t.Errorf("the checkpoint says a1 is on attempt %d, not %d", ts.Attempt, number)
		}
	}
}

// The dispatch order is marker, then claim, then start. A reconciler that died
// in the window between the first two left a tick nobody ever claims: adopt
// does not dispatch, so it never reaches claimDispatch's claim. Adopting
// replays it.
func TestAdoptClaimsATickWhoseDispatchNeverReachedItsClaim(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	r.tracker = f.Tracker.In(f.Repo.Dir)

	before, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != "open" {
		t.Fatalf("the fixture's a1 is %s before anything ran", before.Status)
	}

	jobID := "run-" + r.runID + "/tick-a1/attempt-1"
	marker := attemptHandle{
		Executor: "local-subprocess", JobID: jobID, Attempt: 1, TickID: "a1", Role: "implement-tick",
		Repo: f.Repo.Dir, Remote: "origin", WriteRef: attemptWriteRef(jobID), BaseSHA: f.Repo.Base,
		StateRoot: r.execStateDir("a1", 1),
	}
	if _, _, err := r.adopt(context.Background(), marker); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	after, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "in_progress" {
		t.Errorf("a1 is %s after its attempt was adopted: the tracker still says nobody holds it", after.Status)
	}
	if got := f.Tracker.count("claim:a1"); got != 1 {
		t.Errorf("the claim was replayed %d times", got)
	}

	// Idempotent: a tick the tracker already says is in progress is left alone.
	r.replayClaim(context.Background(), "a1")
	if got := f.Tracker.count("claim:a1"); got != 1 {
		t.Errorf("replaying the claim of a tick already in progress claimed it again (%d)", got)
	}
}

// durableAttemptHead, in its own right: what the merge is OF, in each of the
// four states origin and the collect can be in.
func TestTheMergeHeadIsTheCollectedHeadOrNothing(t *testing.T) {
	root := t.TempDir()
	repo := newRepo(t, root, "heads", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	r := &Reconciler{git: g, opts: Options{Remote: "origin"}}

	const branch = "ticfac/run-x/tick-a/attempt-1"
	head := func(sha string) *subprocess.Collection {
		return &subprocess.Collection{Result: &subprocess.JobResult{
			Source: subprocess.ResultSource{HeadSHA: &sha}}}
	}

	// Nothing on origin and nothing collected: there is nothing to integrate.
	if _, err := r.durableAttemptHead(branch, nil); err == nil {
		t.Error("an attempt with no head anywhere was accepted for merge")
	}

	// A head on origin that no collect ever read is NOT what gets merged: it
	// was never checked against the attempt's base.
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", repo.Base+":"+refFor(branch))
	if _, err := r.durableAttemptHead(branch, nil); err == nil {
		t.Error("origin's head was accepted for merge although the collect read no commit at all")
	}

	// Agreement: the collected head is the head.
	got, err := r.durableAttemptHead(branch, head(repo.Base))
	if err != nil || got != repo.Base {
		t.Errorf("durableAttemptHead = %s, %v; want the collected head %s", short(got), err, short(repo.Base))
	}

	// A collected head origin does not have yet is PUT there before it is
	// merged: durable still means pushed.
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", branch, repo.Base)
	write(t, filepath.Join(repo.Dir, "later.txt"), "later\n")
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "later")
	later := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")

	got, err = r.durableAttemptHead(branch, head(later))
	if err != nil || got != later {
		t.Fatalf("durableAttemptHead = %s, %v; want the collected head %s", short(got), err, short(later))
	}
	if onOrigin := strings.TrimSpace(mustRun(t, repo.Origin, "git", "rev-parse", refFor(branch))); onOrigin != later {
		t.Errorf("origin holds %s; the collected head was merged without being made durable", short(onOrigin))
	}

	// And a collected head that does NOT fast-forward origin is refused: origin
	// holds commits this attempt did not produce, which is what a ref shared
	// by two runs looks like.
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", "somebody-else", repo.Base)
	write(t, filepath.Join(repo.Dir, "theirs.txt"), "theirs\n")
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "theirs")
	theirs := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")

	const shared = "ticfac/run-x/tick-b/attempt-1"
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", theirs+":"+refFor(shared))
	if _, err := r.durableAttemptHead(shared, head(later)); err == nil {
		t.Error("a collected head that does not fast-forward origin was accepted for merge")
	}
	if onOrigin := strings.TrimSpace(mustRun(t, repo.Origin, "git", "rev-parse", refFor(shared))); onOrigin != theirs {
		t.Errorf("origin's %s moved to %s; the refusal wrote something", shared, short(onOrigin))
	}
}
