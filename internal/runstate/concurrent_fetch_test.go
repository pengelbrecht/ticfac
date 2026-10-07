package runstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The concurrent-fetch guards.
//
// These two tests are the behavioural proof of the store's contract with the
// other processes sharing its checkout and its origin: a watcher's plain
// `git fetch`, another ticfac command's store fetching into its own ref, and
// a second writer pushing between this store's read and its write must all
// leave the run standing. Everything here is real git: the origins are bare
// repositories, the refusals are git refusing, and the lock files below are
// the very locks git takes itself (origin_test.go holds the fixture).
//
// Contention is PLACED, not raced for. Both tests arm the store's contention
// seam (Store.contend) so that each competing action runs at the exact
// instant its window is open — and, for the shared-ref collisions, holds the
// lock a competitor's fetch would take while it updates a ref, across the
// whole of the store's fetch, which makes the collision certain instead of
// the one-in-many the old shape bought. Before tick 1qy each test sprayed a
// fetch in an unthrottled goroutine for its whole length — a saturated core
// and a hammered filesystem for half a minute per test — and looped record
// writes (60 of them, then 30) hoping one landed inside a window. Those
// loops, and the iteration counts chosen to make them lucky, are gone.

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
// Here that operator strikes at the two instants where it is dangerous, and
// nowhere else:
//
//   - mid-fetch, holding refs/remotes/origin/<branch>'s lock: a watcher's
//     plain `git fetch origin` updates every remote-tracking ref, that one
//     among them, and holds its lock while it writes. The pre---refmap
//     store's fetch updated the same ref opportunistically, the two raced on
//     the lock, and the loser exited 1 — which a reconciler treats as fatal.
//     The guard holds that lock across the store's fetch: a store sharing the
//     ref loses its fetch exactly as the production loser did, and a store
//     that touches no ref a watcher updates never notices the lock at all.
//   - in the window, whole: the watcher's fetch completes after the store's
//     fetch has brought the branch home and before anything resolves it —
//     FETCH_HEAD's clobber, exactly. A store that resolved through FETCH_HEAD
//     reads main's head here, an empty .ticfac view, and the assertions below
//     fail the way the production run died. (A fresh checkout has no
//     FETCH_HEAD to read at all, so such a store cannot survive this test's
//     setup either.)
//
// A third competitor — the tracker's writer — is placed by the same seam at
// the lost-lease moment: it pushes a record between this store's read and its
// write, the push loses its lease, and the store must rebuild on origin's new
// head, whose re-read runs with the watcher's fetches in flight. That is
// where the production failure struck: the recovery read is the one the
// clobber killed.
//
// TestNoProductionCodeReadsFetchHead and TestEveryProductionFetchStaysOffSharedRefs
// are the shape guards over the same regressions. This one is the behaviour:
// with FETCH_HEAD out of the store's path it passes every time.
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
	if got, err := s.CreateIfAbsent(CheckpointPath(run), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}

	// A second writer on the same run, standing in for what moved the ref under
	// the store in production: the tracker publishing claims and closes, and
	// an operator triaging findings. Its first record has already landed, so
	// the store's next read is of a ref that moved — which is also when a
	// fetch takes a ref's lock at all.
	writer := o.actor("triage", run)
	if _, err := writer.Fetch(); err != nil {
		t.Fatal(err)
	}
	if got, err := writer.CreateIfAbsent(AttemptPath(run, 2), []byte(`{"attempt":1}`)); err != nil || got != Created {
		t.Fatalf("the other writer's first record: %v %v", got, err)
	}

	// The watcher is mid-fetch right now: its `git fetch origin` holds
	// refs/remotes/origin/<branch>'s lock while it updates it — the ref a
	// fetch without --refmap= updates opportunistically too.
	watcherRef := "refs/remotes/origin/" + o.branch
	holdRefLock(t, dir, watcherRef)
	midFetch := true // the watcher's fetch still holds its ref's lock
	landed := false  // the tracker's writer has struck under the lease once

	s.contend = func(moment contentionMoment) {
		switch moment {
		case branchFetchedHome:
			// The watcher's fetch goes on, every read: its ref update
			// finishes —
			if midFetch {
				releaseRefLock(t, dir, watcherRef)
				midFetch = false
			}
			// — and the fetch itself completes inside the window, rewriting
			// FETCH_HEAD (main's head: the checkout tracks origin's main) and
			// every remote-tracking ref. Through gitCommand like every other
			// git this suite starts (tick 35l): the watcher's plainness is
			// not what this test measures — what it touches is — and an
			// unpinned fetch is the detached-maintenance leak this package
			// pins everywhere else.
			if err := gitCommand("fetch", "--quiet", "origin").command(dir).Run(); err != nil {
				t.Errorf("the watcher's own fetch failed: %v", err)
			}
		case pushAboutToLeave:
			// The tracker's writer publishes one more record, once, between
			// this store's read and its write.
			if landed {
				return
			}
			landed = true
			if got, err := writer.CreateIfAbsent(AttemptPath(run, 3), []byte(`{"attempt":1}`)); err != nil || got != Created {
				t.Errorf("the other writer's record under the lease: outcome %q, err %v", got, err)
			}
		}
	}

	// The reconciler re-reads origin constantly (claimDispatch, gate and
	// collect all call store.Fetch). This read ran with the watcher
	// mid-fetch and then landing whole inside its window; it must come back
	// the integration branch's view, run state and all.
	if _, err := s.Fetch(); err != nil {
		t.Fatalf("a read under a watcher's fetch: %v\n"+
			"The store's fetch collided with the watcher's on a ref they share: the loser exits 1 and the "+
			"reconciler treats a failed fetch as fatal, which ended the pwp run. A store's fetch must touch "+
			"no ref a watcher's `git fetch origin` also updates. See tick wdb.", err)
	}
	if raw, ok, err := s.Read(CheckpointPath(run)); err != nil || !ok || len(raw) == 0 {
		t.Fatalf("a read under a watcher's fetch lost the run's own checkpoint (ok=%v err=%v).\n"+
			"The store resolved a head with no .ticfac tree: the pwp production failure. See tick wdb.", ok, err)
	}

	// The checkpoint update. The writer's record landed between this store's
	// read and its write, so the push loses its lease: that is a lost lease
	// to rebuild on origin's new head, never a run to end — and the rebuild's
	// re-read of origin runs inside the same window.
	got, err := s.UpdateIfSHA(CheckpointPath(run), []byte(`{"sequence":2}`))
	if err != nil {
		t.Fatalf("the checkpoint update under contention: %v", err)
	}
	if got != Updated {
		t.Fatalf("the checkpoint update under contention was %q, not updated.\n"+
			"conflict_exists here is the pwp production failure: the store read a head "+
			"with no .ticfac tree and took the create path. See tick wdb.", got)
	}

	// Durable means pushed: the run's own write landed, and so did the other
	// writer's — the rebuild took origin's new head rather than clobbering
	// the record on it.
	files := o.files()
	if seq := files[CheckpointPath(run)]["sequence"]; seq != float64(2) {
		t.Errorf("the checkpoint on origin is at sequence %v, want 2: the run's own write did not land", seq)
	}
	for _, attempt := range []int{2, 3} {
		if _, ok := files[AttemptPath(run, attempt)]; !ok {
			t.Errorf("the other writer's record %d is not on origin: the run's rebuild dropped a foreign record", attempt)
		}
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
//
// The observer strikes at the two instants where it is dangerous, placed by
// the same contention seam:
//
//   - mid-fetch: the observer's Fetch holds its peek ref's lock while it
//     updates it, and the LIVE store's fetch must not care. The guard holds
//     that lock across the live store's fetch — a live fetch that shares the
//     observer's ref (the pre-instance-id peek ref, or an instance id that is
//     not per-instance) exits 1 here, exactly as the review's run died, while
//     a fetch into its own private ref never notices the lock at all.
//   - in the window: the observer's fetch completes between the live store's
//     fetch and its resolve, and the live store's own read and write must
//     come through it. The observer's own fetch must come through too —
//     `ticfac findings` listing a run may not disturb it, and it may not be
//     disturbed.
//
// The watcher's FETCH_HEAD clobber and the refs/remotes lock race are the
// earlier test's to catch: a plain watcher is who writes them, and a store's
// fetch — even the pre-fix one — never wrote FETCH_HEAD itself. The shape of
// the peek ref itself is pinned by TestAPrivateFetchRefLeadsWithItsInstanceID.
func TestASecondTicfacProcessDoesNotEndTheRun(t *testing.T) {
	o := newOrigin(t)
	const run = "r-observed"

	live := o.actor("run", run)
	if _, err := live.Fetch(); err != nil {
		t.Fatal(err)
	}

	// The operator's command, listing the run before it has written
	// anything: same run id, same checkout as the run's store. Its fetch
	// parks its peek ref at origin's current head, so the live store's own
	// next fetch is one that has to take a lock at all.
	observer, err := Open(Options{Repo: live.git.dir, Remote: "origin", Branch: o.branch, RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Fetch(); err != nil {
		t.Fatal(err)
	}

	// The run writes: origin moves past both stores' peek refs.
	if got, err := live.CreateIfAbsent(CheckpointPath(run), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the first checkpoint: %v %v", got, err)
	}

	// The operator's command is mid-fetch right now: it holds its own peek
	// ref's lock while it updates it.
	observerRef := observer.peekRef()
	holdRefLock(t, live.git.dir, observerRef)
	midFetch := true

	live.contend = func(moment contentionMoment) {
		if moment != branchFetchedHome {
			return
		}
		// The observer's fetch goes on: its ref update finishes —
		if midFetch {
			releaseRefLock(t, live.git.dir, observerRef)
			midFetch = false
		}
		// — and its read completes inside the live store's window. What
		// `ticfac findings` does: fetch, then read. Its own fetch must come
		// through too, or listing a run's findings fails the person listing.
		if _, err := observer.Fetch(); err != nil {
			t.Errorf("the operator's own read failed: %v", err)
		}
	}

	// The live store reads with the operator's command mid-fetch and then
	// landing whole inside its window.
	if _, err := live.Fetch(); err != nil {
		t.Fatalf("a read while an operator command fetched: %v\n"+
			"The two stores share a peek ref: the loser's fetch exits 1, the reconciler treats a failed "+
			"fetch as fatal, and `ticfac findings` killed the run that filed it. A store's fetch must go "+
			"to a ref no other store's fetch touches.", err)
	}
	if raw, ok, err := live.Read(CheckpointPath(run)); err != nil || !ok || len(raw) == 0 {
		t.Fatalf("a read while an operator command fetched lost the run's own checkpoint (ok=%v err=%v)", ok, err)
	}

	// And writes: the checkpoint update must land while the operator's
	// command reads the same run, not end on it.
	got, err := live.UpdateIfSHA(CheckpointPath(run), []byte(`{"sequence":2}`))
	if err != nil {
		t.Fatalf("a write while an operator command fetched: %v\n"+
			"A ticfac command must never end the run it is reporting on.", err)
	}
	if got != Updated {
		t.Fatalf("the checkpoint while an operator command fetched was %q, not updated", got)
	}
	if c := o.files()[CheckpointPath(run)]; c["sequence"] != float64(2) {
		t.Errorf("the checkpoint on origin is at sequence %v, want 2: the run's own write did not land", c["sequence"])
	}
}

// holdRefLock takes the ref lock another git process holds while it updates
// the ref: git writes <ref>.lock beside the ref, renames it over the ref, and
// two fetches updating one ref race there — the loser exiting 1 on `unable to
// update local ref`, which a reconciler treats as fatal. That collision was
// production twice, and a guard cannot make two real fetches collide on
// demand: each holds its lock for a moment deep inside itself, and hoping the
// two moments coincide is the spraying the contention seam exists to end.
//
// So the guard holds the lock itself. The file IS the other process
// mid-update, held across the whole of the store's fetch, which makes the
// collision CERTAIN: a store sharing the ref loses its fetch exactly as the
// production loser did — same refusal, same exit 1 — while a store whose
// fetch touches only its own private ref never notices the lock at all. The
// lock is taken at the ref's own path (git locks there whether the ref is
// loose or packed), and released in releaseRefLock the way the other
// process's completing fetch would.
func holdRefLock(t *testing.T, repo, ref string) {
	t.Helper()
	lock := filepath.Join(gitDirOf(t, repo), ref+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// releaseRefLock finishes the competitor's ref update: the lock goes away,
// exactly as it does when the other git process completes its fetch.
func releaseRefLock(t *testing.T, repo, ref string) {
	t.Helper()
	if err := os.Remove(filepath.Join(gitDirOf(t, repo), ref+".lock")); err != nil {
		t.Fatal(err)
	}
}

// gitDirOf is a checkout's git directory, where a ref's lock file lives.
// Asked through the pinned runner rather than assumed: a lock written into
// the wrong directory is a guard that cannot bite, and this helper's whole
// job is biting.
func gitDirOf(t *testing.T, repo string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, repo, "rev-parse", "--absolute-git-dir"))
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
