package subprocess

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The wall clock's cost, generalised to every teardown that destroys (tick
// pbb; the wall-clock close-path half landed as rj0, and the close keeps its
// own gate there).
//
// The herdr pane close snapshots the worktree before it destroys the checkout.
// The local executor's dispose is the OTHER unconditional destruction in this
// codebase: worktreeRemove is --force, so a rejected attempt — the
// wall-clock-stopped worker with nothing committed — and a person's release
// both had every uncommitted line destroyed by the same step that tidied up,
// with nothing recorded. The Phase 3 worker that spent 40 minutes on a
// one-token typo while holding a full implementation is the shape: had it
// been stopped and rejected, its work died at disposal.

// stoppedHoldingWork is the setup the pbb tests share: an attempt that
// settled holding one untracked file worth keeping, cancelled the way the
// reconciler's teardown cancels a rejected attempt (the credential dies
// before the worktree does).
func stoppedHoldingWork(t *testing.T) (*fixture, *JobHandle, string) {
	t.Helper()
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-pbb/tick-pbb/attempt-1", "pbb"))
	f.waitSettled(handle)
	local, _ := handle.Local()
	precious := filepath.Join(local.Worktree, "uncommitted.txt")
	if err := os.WriteFile(precious, []byte("forty minutes of work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	return f, handle, precious
}

// committedFile reads one path out of a commit, reporting whether it is
// there: the boundary-exclusion assertions need a non-fatal read.
func committedFile(t *testing.T, repo, commit, path string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", commit+":"+path)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// snapshotRefExists answers whether a ref resolves, without failing the
// test.
func snapshotRefExists(t *testing.T, repo, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Dir = repo
	return cmd.Run() == nil
}

// TestDisposePreservesUncommittedWorkItWouldOtherwiseDestroy is pbb's
// acceptance at the executor's own scale: a disposed attempt holding an
// uncommitted file leaves that file recoverable at a RECORDED location —
// after the worktree is gone, which is where it used to be destroyed — and
// the snapshot is on no branch: material to read, never a merge.
func TestDisposePreservesUncommittedWorkItWouldOtherwiseDestroy(t *testing.T) {
	t.Parallel()
	f, handle, _ := stoppedHoldingWork(t)
	local, _ := handle.Local()

	// Writes under the boundary paths that must NOT ride the snapshot: a
	// tracker record and a report-shaped file in the attempt's own artifact
	// space.
	if err := os.MkdirAll(filepath.Join(local.Worktree, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local.Worktree, ".tick", "issues", "evil.json"),
		[]byte("{\"id\": \"evil\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(local.Worktree, "runs", "run-pbb", "tick-pbb", "attempt-1")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "DRAFT-RESULT-pbb.md"),
		[]byte("## Draft\n\nSTATUS: nothing yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := f.Executor.Dispose(handle, DisposeOptions{
		Reason: "attempt 1 of pbb is missing-result", KeepBranch: true,
	}); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if _, err := os.Stat(local.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree survived disposal: %v", err)
	}

	// The work survived the disposal, at a RECORDED location: the record sits
	// beside the attempt record, at the name the re-dispatch's gather walks —
	// the same place the archived report lives.
	st := newStore(local.State)
	snap, ok := st.wipSnapshot()
	if !ok {
		t.Fatal("the worktree was destroyed with no record of where the uncommitted work was preserved")
	}
	if snap.Ref == "" || snap.Commit == "" {
		t.Fatalf("the wip snapshot record is empty: %+v", snap)
	}
	if !strings.HasPrefix(snap.Ref, "refs/ticfac/wip/") {
		t.Errorf("the snapshot lives on %s: the ref must sit outside refs/heads, so it can never present itself as a branch to merge", snap.Ref)
	}
	if got, ok := committedFile(t, f.Repo.Dir, snap.Commit, "uncommitted.txt"); !ok || got != "forty minutes of work" {
		t.Errorf("the snapshot's uncommitted.txt reads %q (present %t), want the work as it stood at the stop", got, ok)
	}
	if atRef := runGit(t, f.Repo.Dir, "rev-parse", snap.Ref); atRef != snap.Commit {
		t.Errorf("the recorded ref %s points at %s, want the recorded commit %s", snap.Ref, atRef, snap.Commit)
	}

	// The boundary exclusions ride along as exclusions: the worker's tracker
	// write and its report-shaped file are NOT in the snapshot.
	if got, ok := committedFile(t, f.Repo.Dir, snap.Commit, ".tick/issues/evil.json"); ok {
		t.Errorf("the snapshot carries the worker's write under .tick/: %q — the boundary excludes it from riding along", got)
	}
	if got, ok := committedFile(t, f.Repo.Dir, snap.Commit, "runs/run-pbb/tick-pbb/attempt-1/DRAFT-RESULT-pbb.md"); ok {
		t.Errorf("the snapshot carries a file from the attempt's own artifact space: %q — the artifact prefix is excluded and the report is archived separately", got)
	}

	// Never merged: the ref is outside refs/heads and no branch contains the
	// snapshot commit — the snapshot is material a later attempt reads, not a
	// verdict anyone merges.
	if snapshotRefExists(t, f.Repo.Dir, "refs/heads/"+strings.TrimPrefix(snap.Ref, "refs/ticfac/wip/")) {
		t.Errorf("the snapshot ref %s collides with a branch name", snap.Ref)
	}
	if branches := strings.TrimSpace(runGit(t, f.Repo.Dir, "branch", "--all", "--contains", snap.Commit)); branches != "" {
		t.Errorf("the snapshot commit is reachable from branches (%s): never evidence of completion", branches)
	}
}

// TestDisposeOfACleanWorktreeTakesNoSnapshot is the other half of the policy:
// the snapshot is taken where work is DESTROYED, not wherever a worktree
// happens to exist. A settled, collected, clean attempt — the ordinary
// end-of-run case — must leave no wip ref behind, or one ref per attempt
// buries the stopped ones in noise.
func TestDisposeOfACleanWorktreeTakesNoSnapshot(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-pbb/tick-pbb/attempt-2", "pbb"))
	f.waitSettled(handle)
	local, _ := handle.Local()

	if _, err := f.Executor.Collect(handle); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := f.Executor.Dispose(handle, DisposeOptions{}); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if _, ok := newStore(local.State).wipSnapshot(); ok {
		t.Error("a clean worktree was snapshotted at disposal: the snapshot exists for work a stop interrupts, not for tidy attempts")
	}
}

// TestPurgeStateRetiresTheSnapshotRefWithItsRecord is the pruning half of the
// decision: the ref is pruned by PurgeState, the same explicit step that
// retires the record naming it, and by nothing else — the snapshot must
// outlive the attempt's own teardown exactly as long as the archived report
// does, because both are material a later attempt is pointed at.
func TestPurgeStateRetiresTheSnapshotRefWithItsRecord(t *testing.T) {
	t.Parallel()
	f, handle, _ := stoppedHoldingWork(t)
	local, _ := handle.Local()

	if err := f.Executor.Dispose(handle, DisposeOptions{
		Reason: "attempt 1 of pbb is missing-result", KeepBranch: true,
	}); err != nil {
		t.Fatalf("dispose: %v", err)
	}
	st := newStore(local.State)
	snap, ok := st.wipSnapshot()
	if !ok {
		t.Fatal("the disposal preserved nothing: the setup for the prune does not hold")
	}
	if !snapshotRefExists(t, f.Repo.Dir, snap.Ref) {
		t.Fatalf("the snapshot ref %s does not resolve before the purge", snap.Ref)
	}

	if err := f.Executor.PurgeState(handle); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := os.Stat(local.State); !os.IsNotExist(err) {
		t.Fatalf("the state directory survived the purge: %v", err)
	}
	if snapshotRefExists(t, f.Repo.Dir, snap.Ref) {
		t.Errorf("the snapshot ref %s outlived the record that names it: purge retires them together", snap.Ref)
	}

	// Idempotent: a second purge of a state directory that is already gone
	// prunes nothing and fails nothing.
	if err := f.Executor.PurgeState(handle); err != nil {
		t.Errorf("a second purge failed: %v", err)
	}
}
