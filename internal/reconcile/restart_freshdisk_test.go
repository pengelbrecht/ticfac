package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The disk-loss cut, the one the Cloudflare host forces and the process cuts
// in restart_test.go cannot represent: a container is killed at an arbitrary
// moment and its replacement starts with a FRESH DISK. Everything that lived
// on the old disk is gone — the reconciler's process, the in-flight worker's
// process, the executor's whole state root including the attempt worktrees —
// and only what was PUSHED survives.
//
// That is axiom 1's hardest case, and the difference between it and the
// process cut is exactly what this test holds to: a process cut restarts into
// a state root that still names a live-or-dead job, while a disk cut restarts
// into NOTHING but the branch. Resume must still be the normal boot path —
// the same fetch, the same re-derivation, the same adoption — not an error
// path that has never run when the day it was built for arrives.
//
// The cut lands where the acceptance puts it: a1 is CLOSED (completed work a
// replacement must not redo) while a2 is MID-TICK with its work already pushed
// to its attempt branch (durable work a replacement must continue from, not
// silently redo). The durable file is the dead worker's signature: a
// replacement that continues carries it into the integrated tree, and one
// that restarted the tick does not.
func TestAFreshDiskBootsIntoResumeAndContinuesFromThePushedBranch(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "durable-hang"})

	// The cut, as a condition over the run's own events rather than a guess
	// at a duration: only after a1 has settled all the way to closed, with
	// a2 dispatched and still hanging, is the container worth destroying.
	cut := func() func(Event) bool {
		a1Closed, a2Dispatched := false, false
		return func(e Event) bool {
			switch {
			case e.Tick == "a1" && e.Stage == StageClosed:
				a1Closed = true
			case e.Tick == "a2" && e.Stage == StageDispatched:
				a2Dispatched = true
			}
			return a1Closed && a2Dispatched
		}
	}
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: cut()})
	if _, isKill := err.(*killedAt); !isKill {
		t.Fatalf("the first incarnation ended with %v, not with the simulated kill", err)
	}

	// The dead worker's work is on origin BEFORE the disk goes, or the rest
	// of the test would prove nothing about continuation. This is a wait on
	// the observable — the ref the worker pushed — bounded, not a sleep.
	a2Branch := ""
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := harnessCommand("git", "for-each-ref",
			"--format=%(refname)", "refs/heads/ticfac/run-r-fixture/tick-a2/*").output(f.Repo.Origin)
		if err != nil {
			t.Fatalf("read the a2 branch on origin: %v\n%s", err, out)
		}
		for _, ref := range strings.Fields(string(out)) {
			a2Branch = ref
		}
		if a2Branch != "" {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the killed worker's a2 branch never reached origin; the cut has nothing durable to lose")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The container dies, disk and all. The processes go first — the state
	// root is what proves them alive, so it cannot go first — then everything
	// on the disk goes: the executor's state root, the attempt worktrees, the
	// run's local knowledge. Only the bare origin and what it holds survive.
	f.stopEverything()
	if err := os.RemoveAll(f.StateRoot); err != nil {
		t.Fatalf("cannot throw the old disk away: %v", err)
	}

	// The replacement: a FRESH CLONE and an EMPTY state root at the same
	// path, exactly the fresh-disk boot a replacement container makes. Its
	// fake runner is the plain report mode — the replacement's worker is a
	// worker that finishes, not one that dies.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "replacement"))
	f.Runner = fakeRunnerArgv(t, "report")
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the replacement did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the replacement ended %s: %s", result.State, result.Reason)
	}

	// The replacement's dispatch markers, read off the branch it pushed — the
	// only place a fresh-disk boot could have read them from too.
	attempts := attemptsOf(t, clone, restarted)

	// Completed ticks are not redone: a1 was closed by the incarnation that
	// died, and the replacement must read that from the branch and leave it
	// alone — one close ever, no second dispatch, no second attempt marker.
	if got := f.Tracker.count("close:a1"); got != 1 {
		t.Errorf("a1 was closed %d times across the two incarnations", got)
	}
	if got := restarted.Stages("a1"); contains(got, StageDispatched) {
		t.Errorf("the replacement re-dispatched the already-closed a1: %v", got)
	}
	a1Attempts := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			a1Attempts++
		}
	}
	if a1Attempts != 1 {
		t.Errorf("%d dispatch markers for a1 across the two incarnations; the replacement redid a completed tick", a1Attempts)
	}

	// The in-flight tick is CONTINUED, not restarted: a2 keeps its one
	// attempt identity, and the durable work the dead worker pushed is
	// carried into the integrated tree rather than silently redone.
	a2Attempts := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a2" {
			a2Attempts++
		}
	}
	if a2Attempts != 1 {
		t.Errorf("%d dispatch markers for a2 across the two incarnations; the disk-loss restart did not adopt the dead attempt", a2Attempts)
	}
	integrated := mustRun(t, clone.Dir, "git", "show",
		"origin/"+restarted.IntegrationBranch()+":durable-a2.txt")
	if !strings.Contains(integrated, "durable work of a2") {
		t.Errorf("the integrated tree does not carry the dead worker's pushed work (durable-a2.txt reads %q)", integrated)
	}

	// The whole epic is closed, and the run's own account of the second boot
	// shows it resumed rather than started: the tick the dead container was
	// killed on was adopted, never dispatched again.
	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s ended %s", tick, current.Status)
		}
	}
	if got := restarted.Stages("a2"); contains(got, StageDispatched) {
		t.Errorf("the replacement re-dispatched a2 instead of adopting its dead attempt: %v", got)
	}
	if got := restarted.Stages("a2"); !contains(got, StageAdopted) && !contains(got, StageSkipped) {
		t.Errorf("the replacement neither adopted nor skipped a2: %v", got)
	}
	// And the run's own account says WHICH kind of boot this was: not a
	// fresh start, a resume — the note is what a person reading the feed
	// learns the disk-loss continuation from.
	if got := restarted.Stages("a2"); !contains(got, StageResumed) {
		t.Errorf("the replacement resumed a2 without saying so: %v", got)
	}
}

// attemptsOf reads the run's attempt markers off the branch the run pushed —
// the only place a fresh-disk replacement could have read them from too.
func attemptsOf(t *testing.T, repo *testRepo, r *Reconciler) []runstate.Attempt {
	t.Helper()
	store, err := runstate.Open(runstate.Options{
		Repo: repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}
