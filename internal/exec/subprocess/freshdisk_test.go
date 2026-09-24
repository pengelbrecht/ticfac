package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fresh-disk boot (tick lkd): a container is killed at an arbitrary
// moment and its replacement starts with a fresh disk — no state directory,
// no worktree, nothing but what the dead attempt PUSHED. Resume must be the
// normal Start path, not an error path: the attempt's own branch on the
// remote is where its work lived, and a boot that ignores it would both
// silently discard the pushed work and leave every push the attempt ever
// makes refused as a non-fast-forward.
//
// The pushed state is crafted by hand from a SECOND clone, because that is
// the shape a replacement container actually finds: the ref exists on the
// remote and NOWHERE else — not in the checkout the executor works in, whose
// local branch would make makeWorktree refuse for a different reason.

// The durable half of a killed attempt is where the replacement continues
// from: the worktree is cut at the pushed head, not at the base, so the
// worker's next commit sits ON the work that already exists and the attempt's
// own push is a fast-forward.
func TestAFreshDiskStartContinuesFromTheAttemptSPushedBranch(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})

	// The dead worker's work: one commit beyond the base, pushed to this
	// attempt's write ref and to nowhere else.
	pushed := pushDurableWork(t, f.Repo, "tick/diskloss", "durable-diskloss.txt",
		"the dead worker's durable work\n")

	handle := f.Start(f.spec("run-9/tick-diskloss/attempt-1", "diskloss"))
	f.waitSettled(handle)
	collected := f.collect(handle)

	head := collected.Result.Source.HeadSHA
	if head == nil || *head == pushed {
		t.Fatalf("the attempt settled on the pushed head alone (%v); the replacement worker did no work of its own", head)
	}
	// On top of, not beside: the first parent of what was collected is the
	// dead worker's commit, which is what "continues from the pushed branch"
	// means in a tree.
	parent := runGit(t, f.Repo.Dir, "rev-parse", *head+"^")
	if parent != pushed {
		t.Fatalf("the collected head's parent is %s, not the pushed %s: the replacement restarted the tick instead of continuing it", parent, pushed)
	}
	if body := runGit(t, f.Repo.Dir, "show", *head+":durable-diskloss.txt"); !strings.Contains(body, "the dead worker's durable work") {
		t.Errorf("the collected tree lost the dead worker's durable work (reads %q)", body)
	}
	// The boundary is still the BASE the attempt was dispatched from, never
	// the resume point: the pushed work is this attempt's own change and has
	// to stay inside the diff the grade reads.
	changed := runGit(t, f.Repo.Dir, "diff", "--name-only", f.Repo.Base, *head)
	if !strings.Contains(changed, "durable-diskloss.txt") {
		t.Errorf("the boundary diff from the base does not carry the resumed work: %q", changed)
	}
	// And the push behind it was a fast-forward, not a refusal: origin carries
	// exactly what the attempt collected.
	onOrigin := runGit(t, f.Repo.Dir, "ls-remote", f.Repo.Origin, "refs/heads/tick/diskloss")
	if !strings.HasPrefix(onOrigin, *head+"\t") {
		t.Errorf("origin carries %q where the collected head is %s: the attempt's push was not a fast-forward from its own pushed work", onOrigin, *head)
	}
}

// A ref in this attempt's namespace that does not descend from this
// attempt's base is not this attempt's work at any point in its life, and
// the boot says so rather than merging over it.
func TestAFreshDiskStartRefusesAPushedHeadThatIsNotThisAttemptSWork(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})

	// An orphan commit — a history with no relation to the base — pushed
	// under this attempt's write ref.
	scratch := filepath.Join(f.Repo.Root, "scratch")
	runGit(t, f.Repo.Root, "clone", "--quiet", f.Repo.Origin, scratch)
	// A clone carries no identity of its own; CI has no global one either.
	runGit(t, scratch, "config", "user.name", "ticfac test")
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "checkout", "--quiet", "--orphan", "foreign")
	if err := os.WriteFile(filepath.Join(scratch, "foreign.txt"), []byte("someone else's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, scratch, "add", "-A")
	runGit(t, scratch, "commit", "--quiet", "-m", "foreign history")
	runGit(t, scratch, "push", "--quiet", f.Repo.Origin, "HEAD:refs/heads/tick/foreign")
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatal(err)
	}

	_, err := f.Executor.Start(f.spec("run-9/tick-foreign/attempt-1", "foreign"))
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedLive {
		t.Fatalf("start over a pushed head that is not this attempt's work answered %v, wanted a %s refusal", err, RefusedLive)
	}
}

// The FIRST boot is the same path, a no-op: with nothing pushed, the
// worktree is cut at the base and the attempt is an ordinary one.
func TestAFirstBootCutsTheWorktreeAtTheBase(t *testing.T) {
	// nocommit settles without moving the branch, so where the worktree was
	// cut is still readable after settlement — no race with a fast runner.
	f := newFixture(t, fixtureOptions{mode: "nocommit"})

	handle := f.Start(f.spec("run-9/tick-first/attempt-1", "first"))
	f.waitSettled(handle)

	st := f.store(handle)
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	at := runGit(t, record.Worktree, "rev-parse", "HEAD")
	if at != f.Repo.Base {
		t.Fatalf("a first boot with nothing pushed cut the worktree at %s, not at the base %s", at, f.Repo.Base)
	}
}

// pushDurableWork puts one commit beyond the repository's base onto a
// branch, pushed to the origin from a throwaway clone so it exists on the
// remote and NOWHERE else — the shape a fresh-disk replacement finds.
func pushDurableWork(t *testing.T, repo *testRepo, branch, file, body string) string {
	t.Helper()
	scratch := filepath.Join(repo.Root, "scratch")
	runGit(t, repo.Root, "clone", "--quiet", repo.Origin, scratch)
	// A clone carries no identity of its own; CI has no global one either.
	runGit(t, scratch, "config", "user.name", "ticfac test")
	runGit(t, scratch, "config", "user.email", "test@example.com")
	runGit(t, scratch, "checkout", "--quiet", "-b", branch, repo.Base)
	if err := os.WriteFile(filepath.Join(scratch, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, scratch, "add", "-A")
	runGit(t, scratch, "commit", "--quiet", "-m", "the dead worker's commit")
	runGit(t, scratch, "push", "--quiet", repo.Origin, "HEAD:refs/heads/"+branch)
	pushed := runGit(t, scratch, "rev-parse", "HEAD")
	if err := os.RemoveAll(scratch); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(pushed)
}
