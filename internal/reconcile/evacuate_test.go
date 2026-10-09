package reconcile

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The eviction flush (tick ppt), tested as its caller calls it: a LIVE run,
// an attempt in flight, and Evacuate invoked from the goroutine the SIGTERM
// handler occupies — beside a reconciler that is still running, exactly as a
// signal arrives beside one. What is asserted is the acceptance criterion
// itself: the work shows up on the remote, and the flush does not outlive its
// bound.

// waitUntil polls cond until it holds or the bound is spent. The flush's
// tests wait on durable facts — a record on disk, a ref on origin — never on
// guessed seconds, because the learnings are right: a sleep long enough to
// be safe is a slow test, and one short enough to be fast is a flaky one.
func waitUntil(t *testing.T, bound time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for !cond() {
		if !time.Now().Before(deadline) {
			t.Fatalf("gave up waiting for %s after %s", what, bound)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// walkFor says whether a file with this name exists anywhere under root,
// skipping nothing: the run's own state layout keeps attempt records, pids and
// the worktree in one small directory, and the fact being waited for is that
// one of them appeared.
func walkFor(root, name string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && entry.Name() == name {
			found = true
		}
		return nil
	})
	return found
}

// runLive runs one reconciler in a goroutine and hands back a channel that
// closes when it has returned, so a test can flush mid-run and then let the
// run finish from what it finds on disk.
func runLive(t *testing.T, r *Reconciler) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background())
		done <- err
	}()
	return done
}

// originCheckpoint reads the checkpoint as origin holds it, through a fresh
// fetch, the way a resumed incarnation would.
func originCheckpoint(t *testing.T, repo *testRepo, runID string) runstate.Checkpoint {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo.Dir, Remote: "origin",
		Branch: "epic/qeu", RunID: runID, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatalf("read the run state back from origin: %v", err)
	}
	checkpoint, ok, err := store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("origin holds no checkpoint for %s", runID)
	}
	return *checkpoint
}

// originRefSHA is a ref as the bare origin holds it, or "" when origin does
// not have it.
func originRefSHA(t *testing.T, origin, ref string) string {
	t.Helper()
	out, _ := harnessCommand("git", "rev-parse", "--verify", "--quiet", ref).output(origin)
	return strings.TrimSpace(string(out))
}

func TestTheEvacuationFlushesInFlightWorkAndWritesTheCheckpoint(t *testing.T) {
	t.Parallel()
	// wallwip: attempt 1 of a1 writes real work, commits NOTHING, and stays
	// alive — so the only process that could ever carry that work to origin
	// is the flush. a2 settles normally beside it, which is also the fact
	// the checkpoint's carried ticks are about.
	f := newFixture(t, fixtureOptions{mode: "wallwip"})
	r, err := New(f.options(f.Repo, fixtureOptions{mode: "wallwip"}))
	if err != nil {
		t.Fatal(err)
	}
	runDone := runLive(t, r)
	// The wait is for the WORK, not just the dispatch: an attempt record
	// exists before its runner starts, and a flush that found the worktree
	// still clean would prove nothing about the work it exists to save.
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })

	started := time.Now()
	lines := r.Evacuate("terminated", 30*time.Second)
	account := strings.Join(lines, "\n")

	// The work: uncommitted when the flush found it, snapshotted and pushed
	// to origin on the attempt's WIP ref — never on its branch, which only
	// the worker writes (epic-2jn): a worker that outlived the flush must not
	// find a commit it never made under it.
	ref := "refs/ticfac/wip/run-r-fixture/tick-a1/attempt-1"
	sha := originRefSHA(t, f.Repo.Origin, ref)
	if sha == "" {
		t.Fatalf("origin does not hold %s: the flush never pushed the uncommitted work\n%s", ref, account)
	}
	wip := mustRun(t, f.Repo.Origin, "git", "show", sha+":wip-a1.txt")
	if !strings.Contains(wip, "uncommitted work of a1") {
		t.Errorf("the pushed snapshot does not carry the uncommitted work: %q", wip)
	}
	branch := "refs/heads/ticfac/run-r-fixture/tick-a1/attempt-1"
	if head := originRefSHA(t, f.Repo.Origin, branch); head != "" {
		msg := mustRun(t, f.Repo.Origin, "git", "log", "--format=%s", "-n", "1", head)
		if strings.Contains(msg, "evacuation snapshot") {
			t.Errorf("the flush committed its snapshot on the attempt branch (%q): a live worker's result "+
				"can no longer fast-forward it", strings.TrimSpace(msg))
		}
	}

	// The checkpoint: the run stopped, on this signal, resumable — with the
	// previous checkpoint's ticks carried rather than dropped.
	checkpoint := originCheckpoint(t, f.Repo, "r-fixture")
	if checkpoint.State != runstate.StateRunning {
		t.Errorf("the evacuated checkpoint's state is %s, want running: a resumable stop", checkpoint.State)
	}
	if !strings.Contains(checkpoint.Reason, "terminated") {
		t.Errorf("the evacuated checkpoint's reason %q does not name the signal", checkpoint.Reason)
	}
	if len(checkpoint.Ticks) == 0 {
		t.Errorf("the evacuated checkpoint carries no tick states: a flush that dropped them " +
			"would make the resumed run's own first checkpoint a downgrade of the truth")
	}

	// The account: the flush says what it did, and it does not take the
	// whole budget to say it.
	if !strings.Contains(account, "pushed ticfac/run-r-fixture/tick-a1/attempt-1") {
		t.Errorf("the flush's account does not say the attempt was pushed:\n%s", account)
	}
	if !strings.Contains(account, "checkpoint written") {
		t.Errorf("the flush's account does not say the checkpoint was written:\n%s", account)
	}
	if spent := time.Since(started); spent > 15*time.Second {
		t.Errorf("the flush spent %s on a local origin, well past anything the bound allows", spent)
	}

	// Let the run finish from what it finds on disk: the hung attempt is
	// stopped, collected as the failure it is, and the run refuses it.
	f.stopEverything()
	select {
	case <-runDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the run did not finish after its in-flight attempt was stopped")
	}
}

func TestTheEvacuationWaitsOutTheSupervisorsPushThatHoldsTheRefLock(t *testing.T) {
	t.Parallel()
	// The flush test above failed ONCE under -parallel load (tick fn8): the
	// account did not report the attempt-branch push. The suspected cause was
	// the flush's branch push losing the attempt branch's ref lock to the
	// supervisor's identical push — and the fallback that read origin once
	// after the failed push is a race of its own: the winner holds the lock
	// for only moments, but a flush that samples origin in the window between
	// its own failed push and the winner's ref landing reports "could not
	// push" for a push that is being made for it. Never reproduced in 48
	// reruns, because that window is milliseconds wide — so this test FORCES
	// the interleaving instead of hoping to land in it.
	//
	// The supervisor's push is blocked AT the ref lock: a
	// reference-transaction hook in the bare origin, whose "prepared" state
	// runs after the transaction's refs are locked and before they are
	// committed, waits there until the test releases it. The flush's push
	// then fails on the held lock every time, and what the flush does about
	// that is the thing under test. The release is scheduled on the
	// observable itself — a third push of the attempt branch, which can only
	// be the flush retrying behind the lock — so nothing here depends on a
	// timer's guess at when two pushes meet: the hook IS the meeting.
	f := newFixture(t, fixtureOptions{mode: "wallwip"})
	// No timed push may race the test's own scheduling: the supervisor's
	// timer carries the same ref, and the third push must be the flush's.
	f.pushInterval = time.Hour
	opts := fixtureOptions{mode: "wallwip"}
	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	runDone := runLive(t, r)
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })
	// The attempt exactly as the flush itself will find it: its worktree and
	// its branch.
	var work subprocess.AttemptWork
	for _, attempt := range r.evacuationAttempts() {
		if attempt.tickID == "a1" && attempt.attempt == 1 {
			work = attempt.work
		}
	}
	if work.Worktree == "" {
		t.Fatal("the flush would find no a1 attempt 1 in flight: the fixture did not start it")
	}
	branchRef := "refs/heads/" + work.Branch

	// The hooks, in the bare origin. pre-receive writes one line per push
	// that carries the attempt branch — the observable the release is
	// scheduled on. reference-transaction holds the FIRST prepared
	// transaction naming that ref at the lock until the go file appears, then
	// lets it commit: a push held there is a push holding the ref lock,
	// carrying exactly the HEAD the flush wants on the branch. Only the
	// first, because git >= 2.51 also runs that hook for a push whose ref
	// lock was refused — the flush's own — and holding THAT one deadlocks the
	// release on the third push that can never arrive (the hook's own
	// comment has the whole story).
	pushes := filepath.Join(f.Repo.Origin, "fn8-pushes.log")
	held := filepath.Join(f.Repo.Origin, "fn8-held.log")
	goFile := filepath.Join(f.Repo.Origin, "fn8-go")
	hook := func(name, script string) {
		t.Helper()
		path := filepath.Join(f.Repo.Origin, "hooks", name)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("install %s in the bare origin: %v", name, err)
		}
	}
	hook("pre-receive", fmt.Sprintf(`#!/bin/sh
# One line per push that carries the attempt branch.
while read old new ref; do
	[ "x$ref" = "x%s" ] && echo "$ref" >> %q
done
exit 0
`, branchRef, pushes))
	hook("reference-transaction", fmt.Sprintf(`#!/bin/sh
# $1 is the transaction state; prepared runs after the transaction's refs
# are locked, before they are committed. Holding a prepared transaction that
# carries the attempt branch is holding that ref's lock.
[ "x$1" = "xprepared" ] || exit 0
hold=0
while read old new ref; do
	[ "x$ref" = "x%s" ] && hold=1
done
[ "x$hold" = "x1" ] || exit 0
# The hold is one-shot: only the FIRST prepared transaction naming the
# attempt branch is held. git >= 2.51 (receive-pack's batched reference
# updates) runs this hook for a push whose ref lock was ALREADY refused,
# with the refused ref on stdin, so the flush's own push — refused behind
# the holder's lock — reaches this line too. Holding it would deadlock the
# test on itself: the go file below appears only when a THIRD push arrives,
# and no third push can arrive while the flush's own is held here, so the
# first push burned its whole bound and every CI run since the hook was
# written came back red ('did not finish inside its bound', 'only 2
# pushes'). git <= 2.50 never runs this hook for a refused lock, so there
# the mkdir is held by the one push it always was. The mkdir succeeds only
# once; every later transaction falls through to whatever the ref lock
# does with it.
mkdir %q 2>/dev/null || exit 0
echo held >> %q
i=0
while [ ! -f %q ]; do
	sleep 0.05
	i=$((i + 1))
	if [ $i -gt 1200 ]; then
		# Nothing released us for a minute: a broken test, made loud rather
		# than a push that never returns.
		exit 1
	fi
done
exit 0
`, branchRef, held+".once", held, goFile))

	// The supervisor's push, the command its timer makes (pushBranch), from
	// the attempt's own worktree — started first, so the lock it takes is
	// held before the flush exists to race it.
	holder := harnessCommand("git", "push", "origin", "HEAD:refs/heads/"+work.Branch).command(work.Worktree)
	if err := holder.Start(); err != nil {
		t.Fatalf("start the supervisor-shaped push: %v", err)
	}
	waitUntil(t, 30*time.Second, "the supervisor-shaped push to hold the attempt branch's ref lock",
		func() bool { _, err := os.Stat(held); return err == nil })

	// The release, on the observable: the third push of the attempt branch
	// can only be the flush trying again behind the held lock — the first is
	// the push holding it, the second the flush's own failed push. Releasing
	// there lets the held push commit exactly the HEAD the flush wants, as
	// the supervisor's does in production, while the flush is still trying.
	// At the unfixed base the third push never comes: the holder stays at the
	// lock for the whole flush, and a flush that reads origin once cannot
	// find the branch.
	pushesOf := func() int {
		raw, err := os.ReadFile(pushes)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), branchRef)
	}
	stopWatch := make(chan struct{})
	t.Cleanup(func() {
		close(stopWatch)
		// Whatever still holds the lock is released now, not by the fixture's
		// teardown: the holder push and the run's in-flight attempts must all
		// be able to finish before the test ends.
		_ = os.WriteFile(goFile, nil, 0o644)
		_ = holder.Wait()
		f.stopEverything()
		select {
		case <-runDone:
		case <-time.After(60 * time.Second):
			t.Error("the run did not finish after its in-flight attempt was stopped")
		}
	})
	go func() {
		for {
			if pushesOf() >= 3 {
				_ = os.WriteFile(goFile, nil, 0o644)
				return
			}
			select {
			case <-stopWatch:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()

	lines := r.Evacuate("terminated", 30*time.Second)
	account := strings.Join(lines, "\n")

	// The report of the push — the line whose absence under load was the
	// whole defect.
	if !strings.Contains(account, "pushed "+work.Branch) {
		t.Errorf("the flush's account does not say the attempt was pushed after its push lost the ref lock:\n%s", account)
	}
	// The durable fact behind the line: origin holds the attempt branch at
	// the worktree's HEAD, put there by whichever of the two pushes won.
	head := strings.TrimSpace(mustRun(t, work.Worktree, "git", "rev-parse", "HEAD"))
	if sha := originRefSHA(t, f.Repo.Origin, branchRef); sha == "" || sha != head {
		t.Errorf("origin holds %q for %s, want the worktree's HEAD %s", sha, branchRef, head)
	}
	// And the interleaving was forced, not hoped for: the flush's push lost a
	// lock that was held before the flush started, and it tried again behind
	// it — a first-try success cannot reach this assertion, because the third
	// push line is what released the lock at all.
	if n := pushesOf(); n < 3 {
		t.Errorf("only %d pushes of the attempt branch reached origin: the flush never tried again behind the held lock", n)
	}
}

func TestTheEvacuationIsBoundedWhenARemoteHangs(t *testing.T) {
	t.Parallel()
	// The bound is the tick's own reason to exist: a hung push that ate the
	// whole grace window would reach SIGKILL anyway and the flush would have
	// bought nothing. So origin is replaced — once a1's work is in flight —
	// by a listener that accepts and never answers, which is a remote that
	// hangs without any cooperation from git.
	f := newFixture(t, fixtureOptions{mode: "wallwip"})
	r, err := New(f.options(f.Repo, fixtureOptions{mode: "wallwip"}))
	if err != nil {
		t.Fatal(err)
	}
	runDone := runLive(t, r)
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })

	// The listener is closed the moment the flush returns, not on cleanup:
	// the run's own in-flight fetches are hanging on connections it accepted,
	// and they only error out when the acceptor goes away. Cleanup would be
	// far too late — the run this test still has to let finish.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	blackhole := make(chan struct{})
	go func() {
		// Hold every connection open and answer nothing, until the test
		// closes blackhole and the listener with it.
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-blackhole
				conn.Close()
			}()
		}
	}()
	var once sync.Once
	stopBlackhole := func() {
		once.Do(func() { listener.Close(); close(blackhole) })
	}
	defer stopBlackhole()
	mustRun(t, f.Repo.Dir, "git", "remote", "set-url", "origin",
		fmt.Sprintf("http://127.0.0.1:%d/repo.git", listener.Addr().(*net.TCPAddr).Port))

	started := time.Now()
	const budget = 2 * time.Second
	lines := r.Evacuate("terminated", budget)
	spent := time.Since(started)
	// The remote goes back and the listener dies NOW, before the run is let
	// go: nothing is left for its in-flight fetches to hang on.
	stopBlackhole()
	mustRun(t, f.Repo.Dir, "git", "remote", "set-url", "origin", f.Repo.Origin)

	// Well inside the budget plus the cost of being killed mid-flight: the
	// flush returned, which is the whole claim — the process gets to exit.
	if slack := budget + 3*time.Second; spent > slack {
		t.Errorf("the flush spent %s against a %s budget: a hung remote is eating the window "+
			"the bound exists to protect", spent, budget)
	}
	account := strings.Join(lines, "\n")
	if !strings.Contains(account, "could not push") {
		t.Errorf("the flush's account does not say the push failed:\n%s", account)
	}
	if !strings.Contains(account, "did not finish inside the evacuation budget") {
		t.Errorf("the flush's account does not say the checkpoint write was abandoned:\n%s", account)
	}

	// Origin is put back before the run is let go, so nothing about the
	// fixture leaks a hanging remote into the rest of its life.
	f.stopEverything()
	select {
	case <-runDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the run did not finish after its in-flight attempt was stopped")
	}
}

func TestTheEvacuationLeavesATerminalCheckpointAlone(t *testing.T) {
	t.Parallel()
	// A run that finished or failed has nothing to resume, and "running"
	// written over its checkpoint would be a lie a restart believed.
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	// The integration branch, cut exactly where the run itself would cut it:
	// these tests flush a reconciler that never ran, and a store with no
	// branch on origin has nothing durable to write to.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	store, err := runstate.Open(runstate.Options{Repo: f.Repo.Dir, Remote: "origin",
		Branch: "epic/qeu", RunID: "r-fixture", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutCheckpoint(runstate.Checkpoint{
		RunID: "r-fixture", EpicID: "qeu", State: runstate.StateCompleted,
		Reason: "the run finished",
		Provenance: runstate.Provenance{RunID: "r-fixture", SourceRef: "HEAD",
			SourceSHA: f.Repo.Base, Phase: runstate.PhaseWorker},
	}); err != nil {
		t.Fatal(err)
	}

	lines := r.Evacuate("terminated", 10*time.Second)
	account := strings.Join(lines, "\n")
	if !strings.Contains(account, "already terminal") {
		t.Errorf("the flush's account does not say the terminal checkpoint was left alone:\n%s", account)
	}
	checkpoint := originCheckpoint(t, f.Repo, "r-fixture")
	if checkpoint.State != runstate.StateCompleted || checkpoint.Reason != "the run finished" {
		t.Errorf("the flush rewrote a terminal checkpoint: state %s, reason %q",
			checkpoint.State, checkpoint.Reason)
	}
}

func TestTheEvacuationWithNothingInFlightStillWritesTheCheckpoint(t *testing.T) {
	t.Parallel()
	// A container can be evicted between waves, with every attempt settled
	// and nothing on this disk but the run's own position. The checkpoint is
	// then the whole flush — and the first checkpoint a run cut that short
	// ever gets, built from the base it was configured with.
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	// The integration branch, cut exactly where the run itself would cut it:
	// this test flushes a reconciler that never ran, and a store with no
	// branch on origin has nothing durable to write to.
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	lines := r.Evacuate("terminated", 10*time.Second)
	account := strings.Join(lines, "\n")
	if !strings.Contains(account, "no in-flight attempts") {
		t.Errorf("the flush's account does not say there was nothing to flush:\n%s", account)
	}
	if !strings.Contains(account, "checkpoint written") {
		t.Errorf("the flush's account does not say the checkpoint was written:\n%s", account)
	}
	checkpoint := originCheckpoint(t, f.Repo, "r-fixture")
	if checkpoint.State != runstate.StateRunning {
		t.Errorf("the evacuated checkpoint's state is %s, want running", checkpoint.State)
	}
	if !strings.Contains(checkpoint.Reason, "terminated") {
		t.Errorf("the evacuated checkpoint's reason %q does not name the signal", checkpoint.Reason)
	}
	if checkpoint.Provenance.SourceSHA != f.Repo.Base {
		t.Errorf("the first-checkpoint provenance names source %s, want the fixture's base %s",
			checkpoint.Provenance.SourceSHA, f.Repo.Base)
	}
}
