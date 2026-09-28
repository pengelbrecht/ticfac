package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// epic-2jn (2026-09-27): a person stopped a local run with SIGTERM to restart
// it on a new build. The flush committed the in-flight attempt's uncommitted
// work ON the attempt branch and pushed it — but a local worker is a separate
// process that survives the orchestrator's SIGTERM. It kept working, committed
// its real result on the head it knew rather than on the snapshot, and the
// resumed run's collected head could not be put on origin, which held the
// snapshot instead: merge_failed, held for a person, over two identical trees.
//
// The two halves of the fix are tested separately, because either would have
// avoided the stall and each is a promise of its own: the flush never writes a
// commit the worker did not make onto the worker's branch, and a snapshot an
// older build DID leave there is superseded by the worker's own result rather
// than refused.

// evacLiveFixture is the 2jn shape: a1's worker holds uncommitted work and
// waits for goFile before it commits its result.
func evacLiveFixture(t *testing.T) (*fixture, fixtureOptions, string) {
	t.Helper()
	opts := fixtureOptions{mode: "evac-live"}
	f := newFixture(t, opts)
	goFile := filepath.Join(f.Root, "evac-go")
	f.Runner = append([]string{f.Runner[0], "EVAC_GO=" + goFile}, f.Runner[1:]...)
	return f, opts, goFile
}

// a1State is the state directory of a1's first attempt under the fixture's
// state root, once it exists.
func a1State(t *testing.T, f *fixture) string {
	t.Helper()
	var state string
	waitUntil(t, 30*time.Second, "a1's attempt state", func() bool {
		var found bool
		state, found = findAttemptState(filepath.Join(f.StateRoot, "r-fixture", "a1", "1"))
		return found
	})
	return state
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertEpicCompleted is the stall's absence, stated as its presence was: the
// run completed, a1 closed, no merge was refused, and the integrated tree
// carries the live worker's result.
func assertEpicCompleted(t *testing.T, f *fixture, r *Reconciler, result *Result, repo *testRepo) {
	t.Helper()
	for _, event := range r.Journal() {
		if strings.Contains(event.Detail, "could not be put on") {
			t.Errorf("a merge was refused over the attempt head: %s/%s: %s", event.Tick, event.Stage, event.Detail)
		}
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s (%+v)", result.State, result.Reason, result.Failure)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 ended %s, want closed", current.Status)
	}
	integrated := mustRun(t, repo.Dir, "git", "show", "origin/"+r.IntegrationBranch()+":wip-a1.txt")
	if !strings.Contains(integrated, "uncommitted work of a1") {
		t.Errorf("the integrated tree does not carry the live worker's result (wip-a1.txt reads %q)", integrated)
	}
}

func TestSIGTERMBesideALiveWorkerLeavesItsBranchToItAndTheResumeMergesIt(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f, opts, goFile := evacLiveFixture(t)

	// Incarnation one: the orchestrator, live, with a1's worker holding
	// uncommitted work.
	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx)
		runDone <- err
	}()
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })
	state := a1State(t, f)

	// The SIGTERM: the flush, then the orchestrator's exit — and NOTHING
	// else. The worker is a separate process and is never signalled.
	account := strings.Join(r.Evacuate("terminated", 30*time.Second), "\n")
	cancel()
	select {
	case <-runDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the first incarnation did not stop after its context was cancelled")
	}
	if subprocess.AttemptSettled(state) {
		t.Fatal("a1's worker settled before it was released: the fixture did not keep it alive across the stop")
	}

	// The work is preserved — on the WIP ref, not on the worker's branch.
	wipRef := "refs/ticfac/wip/run-r-fixture/tick-a1/attempt-1"
	wipSHA := originRefSHA(t, f.Repo.Origin, wipRef)
	if wipSHA == "" {
		t.Fatalf("origin holds no %s: the flush did not preserve the uncommitted work\n%s", wipRef, account)
	}
	if wip := mustRun(t, f.Repo.Origin, "git", "show", wipSHA+":wip-a1.txt"); !strings.Contains(wip, "uncommitted work of a1") {
		t.Errorf("the WIP snapshot does not carry the uncommitted work: %q", wip)
	}
	branch := "refs/heads/ticfac/run-r-fixture/tick-a1/attempt-1"
	if head := originRefSHA(t, f.Repo.Origin, branch); head != "" {
		if msg := mustRun(t, f.Repo.Origin, "git", "log", "--format=%s", "-n", "1", head); strings.Contains(msg, "evacuation snapshot") {
			t.Errorf("the flush put a commit the worker never made on its branch (%q)", strings.TrimSpace(msg))
		}
	}

	// The worker outlives the orchestrator: it finishes, commits and settles.
	touch(t, goFile)
	waitUntil(t, 60*time.Second, "a1's live worker to settle", func() bool { return subprocess.AttemptSettled(state) })

	// Incarnation two: the resume collects the settled attempt and merges it.
	resumed, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	assertEpicCompleted(t, f, resumed, result, f.Repo)
}

func TestALegacyEvacuationSnapshotOnOriginIsSupersededByTheWorkersOwnResult(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f, opts, goFile := evacLiveFixture(t)

	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result *Result
		err    error
	}
	runDone := make(chan outcome, 1)
	go func() {
		result, err := r.RunProtected(context.Background())
		runDone <- outcome{result, err}
	}()
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })
	state := a1State(t, f)
	work, err := subprocess.ReadAttemptWork(state)
	if err != nil {
		t.Fatal(err)
	}

	// Exactly what an older build's flush did: commit the worktree ON the
	// attempt branch and push it. The live worker then finds a commit it
	// never made, takes it back, and commits its result on the head it knew
	// — the 2jn divergence, with identical trees.
	mustRun(t, work.Worktree, "git", "add", "-A")
	mustRun(t, work.Worktree, "git", "commit", "-q", "-m", subprocess.LegacyEvacuationSnapshotSubject)
	mustRun(t, work.Worktree, "git", "push", "-q", "origin", "HEAD:refs/heads/"+work.Branch)
	snapshot := originRefSHA(t, f.Repo.Origin, "refs/heads/"+work.Branch)
	touch(t, goFile)

	var got outcome
	select {
	case got = <-runDone:
	case <-time.After(3 * time.Minute):
		t.Fatal("the run did not finish after a1's worker was released")
	}
	if got.err != nil {
		t.Fatalf("the run did not finish: %v", got.err)
	}
	assertEpicCompleted(t, f, r, got.result, f.Repo)

	// The worker's result replaced the snapshot on origin; it was not merged
	// over it.
	head := originRefSHA(t, f.Repo.Origin, "refs/heads/"+work.Branch)
	if head == snapshot {
		t.Fatalf("origin's %s still holds the snapshot %s", work.Branch, short(snapshot))
	}
	if msg := mustRun(t, f.Repo.Origin, "git", "log", "--format=%s", head); strings.Contains(msg, "evacuation snapshot") {
		t.Errorf("the attempt branch's history still carries the snapshot:\n%s", msg)
	}
}

// The supersession, in its own right: what it replaces and what it still
// refuses.
func TestOnlyTheWorkersOwnResultSupersedesAnEvacuationSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "heads", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	r := &Reconciler{git: g, opts: Options{Remote: "origin"}}
	head := func(sha string) *subprocess.Collection {
		return &subprocess.Collection{Result: &subprocess.JobResult{
			Source: subprocess.ResultSource{HeadSHA: &sha}}}
	}
	// commitOn makes a commit with this subject on a detached head at parent,
	// and answers its sha.
	commitOn := func(parent, file, subject string) string {
		mustRun(t, repo.Dir, "git", "checkout", "--quiet", "--detach", parent)
		write(t, filepath.Join(repo.Dir, file), file+"\n")
		mustRun(t, repo.Dir, "git", "add", "-A")
		mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", subject)
		sha := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
		mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")
		return sha
	}

	// The 2jn shape: origin holds the flush's snapshot on the base; the
	// worker's result sits on the base too. The result replaces it.
	const branch = "ticfac/run-x/tick-a/attempt-1"
	snapshot := commitOn(repo.Base, "wip.txt", subprocess.LegacyEvacuationSnapshotSubject)
	result := commitOn(repo.Base, "wip.txt", "the worker's own result")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", snapshot+":"+refFor(branch))
	got, err := r.durableAttemptHead(branch, head(result))
	if err != nil || got != result {
		t.Fatalf("durableAttemptHead = %s, %v; want the worker's result %s to supersede its snapshot",
			short(got), err, short(result))
	}
	if onOrigin := originRefSHA(t, repo.Origin, refFor(branch)); onOrigin != result {
		t.Errorf("origin holds %s, want the worker's result %s", short(onOrigin), short(result))
	}

	// A snapshot whose parent is NOT in the collected head's history holds
	// work the collected head does not carry: refused, and left alone.
	const other = "ticfac/run-x/tick-b/attempt-1"
	elsewhere := commitOn(repo.Base, "elsewhere.txt", "somebody's commit")
	stranded := commitOn(elsewhere, "wip.txt", subprocess.LegacyEvacuationSnapshotSubject)
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", stranded+":"+refFor(other))
	if _, err := r.durableAttemptHead(other, head(result)); err == nil {
		t.Error("a snapshot on a head the collected result does not descend from was overwritten")
	}
	if onOrigin := originRefSHA(t, repo.Origin, refFor(other)); onOrigin != stranded {
		t.Errorf("origin's %s moved to %s; the refusal wrote something", other, short(onOrigin))
	}

	// An ordinary divergent commit is still somebody else's work: refused.
	const third = "ticfac/run-x/tick-c/attempt-1"
	theirs := commitOn(repo.Base, "theirs.txt", "theirs")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", theirs+":"+refFor(third))
	if _, err := r.durableAttemptHead(third, head(result)); err == nil {
		t.Error("a divergent commit that is not a snapshot was overwritten")
	}
}

func TestTheEvacuationPreservesWorkWhenTheWorkerDiesWithItsContainer(t *testing.T) {
	t.Parallel()
	// The other half of the flush's reason to exist: a whole-container
	// eviction, where the worker dies with the orchestrator and the disk goes
	// with both. The uncommitted work must still reach the replacement.
	opts := fixtureOptions{mode: "wallwip"}
	f := newFixture(t, opts)
	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx)
		runDone <- err
	}()
	waitUntil(t, 30*time.Second, "a1's uncommitted work in its worktree",
		func() bool { return walkFor(f.StateRoot, "wip-a1.txt") })
	account := strings.Join(r.Evacuate("terminated", 30*time.Second), "\n")
	cancel()
	select {
	case <-runDone:
	case <-time.After(60 * time.Second):
		t.Fatal("the first incarnation did not stop after its context was cancelled")
	}

	// The container dies, disk and all.
	f.stopEverything()
	if err := os.RemoveAll(f.StateRoot); err != nil {
		t.Fatalf("cannot throw the old disk away: %v", err)
	}

	// The replacement: a fresh clone, an empty state root, a worker that
	// finishes.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "replacement"))
	f.Runner = fakeRunnerArgv(t, "report")
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the replacement did not finish: %v\nthe flush's account:\n%s", err, account)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the replacement ended %s: %s", result.State, result.Reason)
	}

	// The restarted attempt was pointed at the preserved work, and the work
	// is readable where it was pointed: in the replacement's own checkout.
	wipRef := "refs/ticfac/wip/run-r-fixture/tick-a1/attempt-1"
	dispatch := f.dispatch("a1")
	pointed := ""
	for _, snap := range dispatch.PriorSnapshots {
		if snap.Ref == wipRef {
			pointed = snap.Commit
		}
	}
	if pointed == "" {
		t.Fatalf("the restarted a1 was not pointed at %s: its prior snapshots are %+v\nthe flush's account:\n%s",
			wipRef, dispatch.PriorSnapshots, account)
	}
	// a1 closed, so its wip ref has retired (tick tyv); the commit it was
	// pointed at is still in the replacement's object store.
	if wip := mustRun(t, clone.Dir, "git", "show", pointed+":wip-a1.txt"); !strings.Contains(wip, "uncommitted work of a1") {
		t.Errorf("the preserved work in the replacement's checkout reads %q", wip)
	}
	resumedNote := false
	for _, event := range restarted.Journal() {
		if event.Tick == "a1" && event.Stage == StageResumed && strings.Contains(event.Detail, wipRef) {
			resumedNote = true
		}
	}
	if !resumedNote {
		t.Errorf("the replacement's resume of a1 does not name the preserved work: %v", restarted.Stages("a1"))
	}
}
