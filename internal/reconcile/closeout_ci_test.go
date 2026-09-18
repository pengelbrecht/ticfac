package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// commitOn adds a file and commits it on the repository's current branch,
// returning the sha. It is the smallest way to build the history this test is
// about: a commit that changes code, then one that changes only run state.
func commitOn(t *testing.T, repo *testRepo, path, content string) string {
	t.Helper()
	full := filepath.Join(repo.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, full, content)
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "-m", "commit "+path)
	out := mustRun(t, repo.Dir, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

// TestTheCloseoutSeesCIPastItsOwnCheckpointCommits is tick 9da.
//
// The run's durable state lives in .ticfac/ on the integration branch, and the
// close-out gates on CI of the PR whose head IS that branch — so writing a
// checkpoint moves the head and the close-out then asks about a commit no check
// has run on. Epic 9pd deadlocked exactly here: every resume wrote another
// checkpoint, moved the head again, and refused; CI was green the whole time on
// whichever head had stood still. An operator merged it by hand.
//
// Here the head is a .ticfac-only commit with no CI, and the commit before it
// is green. The close-out must see the green, and must say which commit it is
// about rather than claiming the head.
func TestTheCloseoutSeesCIPastItsOwnCheckpointCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	code := commitOn(t, f.Repo, "internal/thing/thing.go", "package thing\n")
	head := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":2}`)
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 7, URL: "https://example/pr/7", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
	forgeFake := &fakeForge{exists: true, pr: pr, bySHA: map[string]forge.CIReport{
		code: {State: forge.CIGreen},
		// head deliberately absent: no check has run on it
	}}

	r, err := New(f.options(f.Repo, fixtureOptions{pullRequests: forgeFake}))
	if err != nil {
		t.Fatal(err)
	}

	report, sha, isHead, err := r.ciForTree(context.Background(), pr)
	if err != nil {
		t.Fatalf("ciForTree: %v", err)
	}
	if report.State != forge.CIGreen {
		t.Errorf("CI reads %s, want green: the close-out cannot see past the checkpoint commit it just made, "+
			"which is the deadlock of tick 9da", report.State)
	}
	if sha != code {
		t.Errorf("the verdict is about %s, want the last commit that changed code (%s)", short(sha), short(code))
	}
	if isHead {
		t.Error("the verdict is reported as being about the PR's head; it is about an earlier commit and the " +
			"feed must say so rather than claiming a green the head does not have")
	}
}

// And the other half: a verdict is only borrowed when the code really is
// unchanged. A commit outside .ticfac/ since the last CI means the green does
// not describe this tree, and the honest answer is the head's own — none.
func TestAnOlderGreenIsNotBorrowedWhenCodeChangedSince(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	code := commitOn(t, f.Repo, "internal/thing/thing.go", "package thing\n")
	commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":2}`)
	head := commitOn(t, f.Repo, "internal/thing/more.go", "package thing\n\nfunc More() {}\n")
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 8, URL: "https://example/pr/8", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
	forgeFake := &fakeForge{exists: true, pr: pr, bySHA: map[string]forge.CIReport{
		code: {State: forge.CIGreen},
	}}

	r, err := New(f.options(f.Repo, fixtureOptions{pullRequests: forgeFake}))
	if err != nil {
		t.Fatal(err)
	}

	report, _, isHead, err := r.ciForTree(context.Background(), pr)
	if err != nil {
		t.Fatalf("ciForTree: %v", err)
	}
	if report.State != forge.CINone {
		t.Errorf("CI reads %s, want none: code changed after the last commit CI ran on, so that green says "+
			"nothing about this tree and borrowing it would be a close behind a gate that never saw the work",
			report.State)
	}
	if !isHead {
		t.Error("the answer should be the head's own")
	}
}
