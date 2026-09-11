package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Durable means pushed — for the tracker as well as for `.ticfac/`.
//
// These are the assertions the Phase 1 gate run had nothing to fail against:
// two ticks were closed behind their gates, the checkpoint said closed, and
// origin still said open, because the tracker's writes were uncommitted edits
// in the reconciler's checkout on main. The next wave's worker branched from
// the integration branch, read the blocker as open, and answered BLOCKED.

// trackerRecord is `.tick/issues/<tick>.json` as a commit carries it.
func trackerRecord(t *testing.T, f *fixture, commit, tick string) string {
	t.Helper()
	return mustRun(t, f.Repo.Dir, "git", "show", commit+":.tick/issues/"+tick+".json")
}

// originHeadOf is a branch on the fixture's origin, read from the bare
// repository itself: what a fresh clone would get, and not what any checkout
// remembers.
func originHeadOf(t *testing.T, f *fixture, branch string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor(branch)))
}

// TestEveryTrackerWriteIsPushedToTheIntegrationBranch is the acceptance
// criterion: after each close, ORIGIN's integration branch carries the tracker
// record with status closed, and the reconciler's own checkout is untouched.
func TestEveryTrackerWriteIsPushedToTheIntegrationBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	head := originHeadOf(t, f, r.IntegrationBranch())
	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		record := trackerRecord(t, f, head, tick)
		if !strings.Contains(record, `"status": "closed"`) {
			t.Errorf("%s on origin's %s is not closed:\n%s", tick, r.IntegrationBranch(), record)
		}
	}

	// The checkout the reconciler works in is CLEAN: the tracker never wrote
	// into it, so there is nothing there for a later `git add -A` to sweep up
	// and nothing that dies with the machine.
	if dirty := mustRun(t, f.Repo.Dir, "git", "status", "--porcelain"); strings.TrimSpace(dirty) != "" {
		t.Errorf("the reconciler left its checkout dirty:\n%s", dirty)
	}
	if branch := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "rev-parse", "--abbrev-ref", "HEAD")); branch != "main" {
		t.Errorf("the reconciler's checkout is on %s", branch)
	}

	// And main is not written. The tracker's records land on the EpicRun
	// integration branch, which is what the epic's PR is opened from.
	if got := originHeadOf(t, f, "main"); got != f.Repo.Base {
		t.Errorf("origin's main moved from %s to %s", short(f.Repo.Base), short(got))
	}
}

// The close is durable BEFORE the next wave is dispatched, which is the whole
// point of making it durable: a worker reads its blockers out of the `.tick/`
// of the commit it branched from.
func TestAWave2AttemptIsDispatchedAtABaseThatCarriesItsBlockersClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	spec := f.spec("b1")
	if spec == nil {
		t.Fatal("the wave-2 tick was never dispatched")
	}
	if spec.Source.BaseSHA == f.Repo.Base {
		t.Fatalf("b1 was dispatched at %s, the state the run started from: it cannot see wave 1 at all",
			short(spec.Source.BaseSHA))
	}
	for _, blocker := range []string{"a1", "a2"} {
		record := trackerRecord(t, f, spec.Source.BaseSHA, blocker)
		if !strings.Contains(record, `"status": "closed"`) {
			t.Errorf("the worktree b1 was dispatched into reads %s as open:\n%s", blocker, record)
		}
	}

	// The same for the closeout, which branches from the integration branch
	// too and writes a retro about ticks it must see closed.
	if closeout := f.spec("co"); closeout != nil {
		record := trackerRecord(t, f, closeout.Source.BaseSHA, "b1")
		if !strings.Contains(record, `"status": "closed"`) {
			t.Errorf("the closeout was dispatched at a state where b1 is open:\n%s", record)
		}
	}
}

// A claim is a tracker write like any other, so it is pushed like any other:
// a reconciler that died between the claim and the close leaves a tracker
// somebody else can read, rather than a tick that looks unclaimed.
func TestAClaimIsOnOriginBeforeTheJobIsStarted(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)

	head := originHeadOf(t, f, "epic/qeu")
	record := trackerRecord(t, f, head, "a1")
	if !strings.Contains(record, `"status": "in_progress"`) {
		t.Errorf("the claim of a1 is not on origin at the moment the job was started:\n%s", record)
	}
	if strings.Contains(record, `"status": "closed"`) {
		t.Errorf("a1 reads closed on origin before its gate ran:\n%s", record)
	}
}

// Appendix A #6, for an attempt there is nothing left to adopt.
//
// A worker that answered BLOCKED with no commits leaves a settled attempt and
// an empty write ref. Adopting it would re-collect the same refusal for as long
// as the run is restarted, so once the rejection is durable the next
// incarnation dispatches a NEW attempt — at the integration branch as origin
// has it, which by then carries the blocker closed.
func TestARejectedAttemptThatLeftNothingIsRedispatchedAsANewAttempt(t *testing.T) {
	t.Parallel()
	blocked := fixtureOptions{mode: "blocked-first"}
	f := newFixture(t, blocked)

	// The cut is after the rejection is recorded, which is where a crash would
	// leave a run that had just been told its worker is blocked.
	killed := fixtureOptions{mode: "blocked-first", stopAfter: stopAt("a1", StageRejected)}
	_, _, err := f.run(f.Repo, killed)
	killedAfter(t, err, "a1", StageRejected)

	// The rejection is DURABLE: the next incarnation reads it from origin
	// rather than inheriting it from a process that is gone.
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: "r-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin: %v", err)
	}
	rejected := false
	for _, ts := range checkpoint.Ticks {
		if ts.TickID == "a1" && ts.State == "rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("the checkpoint on origin does not say a1 was rejected: %+v", checkpoint.Ticks)
	}

	// The restart, from a fresh clone: everything it knows it reads from
	// origin.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, result, err := f.run(clone, blocked)
	if err != nil {
		t.Fatalf("the restart did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}
	if got := restarted.Stages("a1"); !contains(got, StageRedispatched) {
		t.Errorf("the restart did not redispatch the spent attempt: %v", got)
	}

	// A NEW attempt, not the spent one re-collected: two markers for a1, and
	// the second is the one that closed it.
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	forA1 := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			forA1++
		}
	}
	if forA1 != 2 {
		t.Errorf("%d dispatch markers for a1; the spent attempt was adopted rather than redispatched", forA1)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s after the restart", current.Status)
	}
}

// A rejected attempt whose work EXISTS is a different thing: its commits are on
// origin, the refusal was about them, and redispatching it would throw away the
// only copy of what a person has to look at. It is adopted, as it always was.
func TestARejectedAttemptThatLeftCommitsIsNotRedispatched(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "silent"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "silent"})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	if got := r.Stages("a1"); contains(got, StageRedispatched) {
		t.Errorf("an attempt whose commits are on origin was redispatched: %v", got)
	}
}
