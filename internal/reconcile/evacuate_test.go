package reconcile

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	// The work: uncommitted when the flush found it, committed by the
	// snapshot and pushed to origin. The ref is the attempt's write ref, and
	// the wait is for the flush's OWN commit on it — the supervisor's timer
	// may have pushed the ref at the attempt's base, and only the snapshot
	// says the work itself survived.
	ref := "refs/heads/ticfac/run-r-fixture/tick-a1/attempt-1"
	var sha string
	waitUntil(t, 10*time.Second, "the flush's snapshot on origin", func() bool {
		sha = originRefSHA(t, f.Repo.Origin, ref)
		if sha == "" {
			return false
		}
		msg := mustRun(t, f.Repo.Origin, "git", "log", "--format=%s", "-n", "1", sha)
		return strings.Contains(msg, "evacuation snapshot")
	})
	wip := mustRun(t, f.Repo.Origin, "git", "show", sha+":wip-a1.txt")
	if !strings.Contains(wip, "uncommitted work of a1") {
		t.Errorf("the pushed snapshot does not carry the uncommitted work: %q", wip)
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
