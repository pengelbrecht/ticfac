package runstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// TestAnOperatorFetchingInTheRunsCheckoutEndsNothing is the end-to-end proof
// for tick wdb, and Phase 3 acceptance A1.
//
// The pwp production run died three times the same way: something else ran
// `git fetch` in the run's own checkout, the store resolved origin's head
// through FETCH_HEAD, got main's head instead of the integration branch's, saw
// an EMPTY .ticfac view, sent a checkpoint update down the create path, and
// ended the run on conflict_exists (and once on a torn read of the file). The
// something else was an operator watching the run.
//
// Here an operator fetches every branch in a loop, in the SAME clone the store
// writes from, while the store advances the checkpoint and lands other records
// that move the ref. Origin carries a main branch with no .ticfac tree, so a
// clobbered FETCH_HEAD would resolve to a commit where the run's state does not
// exist — the exact shape of the production failure.
//
// TestNoProductionCodeReadsFetchHead is the deterministic guard. This one is
// the behaviour: with FETCH_HEAD out of the store's path it passes every time.
func TestAnOperatorFetchingInTheRunsCheckoutEndsNothing(t *testing.T) {
	o := newOrigin(t)

	// A default branch that has never heard of this run.
	other := filepath.Join(o.root, "main-seed")
	gitRun(t, o.root, "init", "--quiet", "-b", "main", other)
	if err := os.WriteFile(filepath.Join(other, "MAIN.md"), []byte("no run state here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "--quiet", "-m", "main")
	gitRun(t, other, "push", "--quiet", o.bare, "main")

	const run = "r-watched"
	s := o.actor("run", run)

	// Put the checkout on main, tracking origin's main, the way the ticks
	// checkout was during the pwp run. This is load-bearing: a plain
	// `git fetch origin` lists the current branch's upstream FIRST in
	// FETCH_HEAD, and `git rev-parse FETCH_HEAD` takes the first line. On the
	// run's own branch a clobbered FETCH_HEAD would still name the right
	// commit and the bug would hide; on main it names a tree with no .ticfac.
	dir := s.git.dir
	gitRun(t, dir, "fetch", "--quiet", "origin", "+refs/heads/main:refs/remotes/origin/main")
	gitRun(t, dir, "update-ref", "refs/heads/main", "refs/remotes/origin/main")
	gitRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	gitRun(t, dir, "config", "branch.main.remote", "origin")
	gitRun(t, dir, "config", "branch.main.merge", "refs/heads/main")

	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// What a watcher does: fetch everything, in the run's checkout.
				_ = exec.Command(gitbin.Path(), "-C", dir, "fetch", "--quiet", "origin").Run()
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	if got, err := s.CreateIfAbsent(CheckpointPath(run), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}
	// A second writer on the same run, standing in for what moved the ref under
	// the store in production: the tracker publishing claims and closes, and an
	// operator triaging findings. Without it the store is never refused, never
	// re-reads origin, and the FETCH_HEAD read this test is about never runs.
	writer := o.actor("triage", run)
	if _, err := writer.Fetch(); err != nil {
		t.Fatal(err)
	}

	for i := 2; i <= 60; i++ {
		if got, err := writer.CreateIfAbsent(AttemptPath(run, i), []byte(`{"attempt":1}`)); err != nil || got != Created {
			t.Fatalf("the other writer's record %d: outcome %q, err %v", i, got, err)
		}
		// The reconciler re-reads origin constantly (claimDispatch, gate,
		// collect all call store.Fetch). Each read is a chance to catch a
		// concurrent fetch mid-write.
		if _, err := s.Fetch(); err != nil {
			t.Fatalf("read %d under a concurrent fetch: %v", i, err)
		}
		if raw, ok, err := s.Read(CheckpointPath(run)); err != nil || !ok || len(raw) == 0 {
			t.Fatalf("read %d under a concurrent fetch lost the run's own checkpoint (ok=%v err=%v).\n"+
				"The store resolved a head with no .ticfac tree: the pwp production failure. See tick wdb.", i, ok, err)
		}
		body := []byte(`{"sequence":` + strconv.Itoa(i) + `}`)
		got, err := s.UpdateIfSHA(CheckpointPath(run), body)
		if err != nil {
			t.Fatalf("checkpoint %d under a concurrent fetch: %v", i, err)
		}
		if got != Updated {
			t.Fatalf("checkpoint %d under a concurrent fetch was %q, not updated.\n"+
				"conflict_exists here is the pwp production failure: the store read a head "+
				"with no .ticfac tree and took the create path. See tick wdb.", i, got)
		}
	}

	if seq := o.files()[CheckpointPath(run)]["sequence"]; seq != float64(60) {
		t.Errorf("the checkpoint on origin is at sequence %v, want 60", seq)
	}
}

// TestASecondTicfacProcessDoesNotEndTheRun is the Phase 3 review's finding 1,
// which killed the run that produced it.
//
// `ticfac findings`, `finding` and `settle` open a Store with the SAME run id in
// the same checkout as the live run. Both fetched into refs/ticfac/peek/<run-id>,
// they raced on that ref's lock, and the loser exited 1 — which the reconciler
// treats as fatal. Listing a run's findings could therefore end it, and did,
// while the reviewer was filing the finding that reported it:
//
//	runstate: fetch origin epic/9pd
//	! e0ae7be..a2bbeed epic/9pd -> refs/ticfac/peek/epic-9pd (unable to update local ref)
//
// The earlier test in this file fetched with plain git, which is why a per-run
// ref looked sufficient. This one uses a second STORE, which is what an operator
// command actually is.
func TestASecondTicfacProcessDoesNotEndTheRun(t *testing.T) {
	o := newOrigin(t)
	const run = "r-observed"

	live := o.actor("run", run)
	if _, err := live.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := live.CreateIfAbsent(CheckpointPath(run), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}

	// The operator's command: same run id, same checkout as the run's store.
	observer, err := Open(Options{Repo: live.git.dir, Remote: "origin", Branch: o.branch, RunID: run})
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// What `ticfac findings` does: fetch, then read.
				if _, err := observer.Fetch(); err != nil {
					t.Errorf("the operator's own read failed: %v", err)
					return
				}
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	for i := 2; i <= 30; i++ {
		if _, err := live.Fetch(); err != nil {
			t.Fatalf("read %d while an operator listed the run: %v\n"+
				"A ticfac command must never end the run it is reporting on.", i, err)
		}
		got, err := live.UpdateIfSHA(CheckpointPath(run), []byte(`{"sequence":`+strconv.Itoa(i)+`}`))
		if err != nil {
			t.Fatalf("checkpoint %d while an operator listed the run: %v", i, err)
		}
		if got != Updated {
			t.Fatalf("checkpoint %d was %q while an operator listed the run", i, got)
		}
	}
}

// TestAPrivateFetchRefLeadsWithItsInstanceID pins the ref SHAPE, because the
// first attempt at per-instance refs could not start at all.
//
// With the id last — refs/ticfac/peek/<run>/<id> — git refuses to create the ref
// while refs/ticfac/peek/<run> exists, since a path cannot be both a ref and a
// directory. Every checkout that had run an older build therefore had to be
// cleaned by hand, and the run died on its first fetch:
//
//	cannot lock ref 'refs/ticfac/peek/epic-9pd/90033-1':
//	'refs/ticfac/peek/epic-9pd' exists
//
// Leading with the id keeps each instance's namespace disjoint from anything any
// other build ever wrote.
// short: the private fetch ref is a string a Store builds
func TestAPrivateFetchRefLeadsWithItsInstanceID(t *testing.T) {
	t.Parallel()

	s := &Store{runID: "epic-9pd", fetchID: "4242-7"}
	ref := s.peekRef()
	const want = "refs/ticfac/peek/4242-7/epic-9pd"
	if ref != want {
		t.Errorf("peekRef() = %q, want %q: the instance id must lead, or an older build's ref blocks this one", ref, want)
	}
	if strings.HasPrefix(ref, "refs/ticfac/peek/"+s.runID) {
		t.Error("the ref is nested under the run id, which is where the directory/file conflict lives")
	}
}
