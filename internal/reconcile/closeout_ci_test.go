package reconcile

import (
	"context"
	"fmt"
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

// TestTheCloseoutSeesCIPastEveryRunStateRecord is tick 9fc's half of the
// 9da demonstration. 9da proved the fix for checkpoint writes alone; 9fc
// moves attempt and decision records onto the same branch too, so a close-out
// now writes MORE run state while it waits for the CI it gates on — every
// record kind it can produce, not just the checkpoint. The demonstration the
// tick demands, not an assumption: a head whose recent history is run state of
// every kind, CI green one commit before any of it, and the close-out still
// reaches a verdict about this tree's code.
func TestTheCloseoutSeesCIPastEveryRunStateRecord(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	code := commitOn(t, f.Repo, "internal/thing/thing.go", "package thing\n")
	// The close-out's own writes, in the order a close-out makes them: a
	// checkpoint, an attempt marker, a recorded decision — a role job's
	// validated answer, the record kind 9fc adds to the branch's churn.
	commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":40,"state":"gating"}`)
	commitOn(t, f.Repo, ".ticfac/runs/r-fixture/attempts/7.json", `{"attempt":7,"tick_id":"rev1","job_handle":{"job_id":"r/rev1/7"}}`)
	head := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/decisions/2.json", `{"decision":2,"role":"review-epic","validated":true}`)
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 9, URL: "https://example/pr/9", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
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
		t.Errorf("CI reads %s, want green: with attempt and decision records on the branch too, the close-out "+
			"again cannot see past the run state it writes while it waits (the 9da interaction, tick 9fc)", report.State)
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

// A PENDING ancestor must not end the walk, and this is the ancestry that
// proved it matters (tick tk3, closing Phase 4). The run's own checkpoint
// pushes cancel the CI they supersede, and a cancelled check reads as pending
// FOREVER on that commit, because nothing ever re-runs on a commit the branch
// has moved past (tick 5ob). So the branch accumulates permanently-pending
// ancestors between the head and the last commit whose CI actually finished.
//
// Observed on epic/ncv: five run-state commits with no checks at all, then one
// whose checks were cancelled, then the fully green commit — and the walk
// stopped at the cancelled one, ONE COMMIT SHORT, while reporting the close-out
// as pending.
//
// Walking past pending is sound because onlyRunState still has to prove the
// ancestor's code is this tree's code. What is refused is treating "no answer
// yet" as an answer.
func TestAPendingAncestorDoesNotEndTheWalk(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	code := commitOn(t, f.Repo, "internal/thing/thing.go", "package thing\n")
	// The commit whose CI was cancelled when the next checkpoint superseded it.
	superseded := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":41,"state":"gating"}`)
	// Then the checkpoints that moved the head past it, none of which ever
	// started a workflow of their own.
	commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":42,"state":"gating"}`)
	head := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":43,"state":"gating"}`)
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 11, URL: "https://example/pr/11", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
	forgeFake := &fakeForge{exists: true, pr: pr, bySHA: map[string]forge.CIReport{
		code:       {State: forge.CIGreen},
		superseded: {State: forge.CIPending},
		// head deliberately absent: its checkpoint never started a workflow
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
		t.Fatalf("CI reads %s, want green: the walk stopped at a pending ancestor and never reached the "+
			"conclusive one a single commit further on (tick tk3)", report.State)
	}
	if sha != code {
		t.Errorf("the verdict is about %s, want the last commit that changed code (%s)", short(sha), short(code))
	}
	if isHead {
		t.Error("the verdict is reported as the head's own, but it came from an ancestor")
	}
}

// restartingForge is the fake forge with the CIRestarter half: it records
// which cancelled workflow runs the run restarted, once each.
type restartingForge struct {
	*fakeForge
	restarted []int64
}

var _ forge.CIRestarter = (*restartingForge)(nil)

func (f *restartingForge) RestartCancelledOnce(_ context.Context, runIDs []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, runIDs...)
	return runIDs, nil
}

// EPIC-6IN, THE READ. The epic PR's head was a .ticfac/-only checkpoint:
// ci.yml skips its pull_request jobs for epic heads and ignores .ticfac/** on
// push, so nothing EXECUTED on it — which the forge now answers as none, not
// green (internal/forge) — and the commit that last changed code carried the
// verdict that mattered: a red go job. The close-out must read that red, about
// that commit, and say it is not the head's.
func TestACheckpointOnlyHeadResolvesToItsCodeAncestorsRedRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	code := commitOn(t, f.Repo, ".tick/issues/v7z.json", `{"id":"v7z","status":"in_progress"}`)
	commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":7}`)
	head := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":8}`)
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 98, URL: "https://example/pr/98", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
	forgeFake := &fakeForge{exists: true, pr: pr, bySHA: map[string]forge.CIReport{
		code: {State: forge.CIRed, Failing: []string{"go"}, FailingRuns: []int64{36400030743}},
		// head: every check skipped, which the forge answers as none
	}}
	r, err := New(f.options(f.Repo, fixtureOptions{pullRequests: forgeFake}))
	if err != nil {
		t.Fatal(err)
	}
	report, sha, isHead, err := r.ciForTree(context.Background(), pr)
	if err != nil {
		t.Fatalf("ciForTree: %v", err)
	}
	if report.State != forge.CIRed {
		t.Fatalf("CI reads %s, want red: the code this PR would merge failed go, whatever the head's skips say", report.State)
	}
	if sha != code || isHead {
		t.Errorf("the verdict is about %s (head: %v), want the commit that last changed code, %s", short(sha), isHead, short(code))
	}
}

// EPIC-6IN, THE WAIT THAT NEVER ENDS. The commit that last changed code (the
// fold of main) had its push run CANCELLED by the next push, which changed
// only .ticfac/ and so started no run of its own; the verdict before it is
// about other code. Nothing will ever run CI on this code again, so a wait is
// a wait for nothing: the run restarts the cancelled run, once, says so, and
// waits for THAT.
func TestACancelledCodeCommitIsRestartedNotWaitedOnForever(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	older := commitOn(t, f.Repo, "internal/thing/thing.go", "package thing\n")
	fold := commitOn(t, f.Repo, "internal/thing/more.go", "package thing\n\nfunc More() {}\n")
	head := commitOn(t, f.Repo, ".ticfac/runs/r-fixture/checkpoint.json", `{"sequence":3}`)
	mustRun(t, f.Repo.Dir, "git", "push", "-q", "origin", "HEAD:refs/heads/epic/qeu")

	pr := &forge.PullRequest{Number: 98, URL: "https://example/pr/98", HeadRef: "epic/qeu", BaseRef: "main", HeadSHA: head}
	forgeFake := &restartingForge{fakeForge: &fakeForge{exists: true, pr: pr, bySHA: map[string]forge.CIReport{
		older: {State: forge.CIRed, Failing: []string{"go"}},
		fold:  {State: forge.CIPending, CancelledRuns: []int64{36402263709}},
	}}}
	r, err := New(f.options(f.Repo, fixtureOptions{pullRequests: forgeFake}))
	if err != nil {
		t.Fatal(err)
	}
	report, _, _, err := r.ciForTree(context.Background(), pr)
	if err != nil {
		t.Fatalf("ciForTree: %v", err)
	}
	if report.State != forge.CIPending {
		t.Errorf("CI reads %s, want pending: a restarted run is a real wait (and the red before the fold is about "+
			"other code, so it is never borrowed)", report.State)
	}
	if fmt.Sprint(forgeFake.restarted) != "[36402263709]" {
		t.Errorf("restarted %v, want the fold's cancelled run once: without it nothing ever runs CI on this code",
			forgeFake.restarted)
	}
	if !contains(r.Stages(""), StageCIRestarted) {
		t.Errorf("the restart is not in the feed (%v): an automatic intervention nobody can see", r.Stages(""))
	}
}

// short: reads .github/workflows/ci.yml as text; no repository, no run
//
// The walk borrows a verdict across commits that change only runStatePrefix
// because CI ignores exactly those paths: a commit touching nothing else
// starts no run. The two lists must be one list. If ci.yml ignores more, a
// head changing only the extra paths gets no run and no borrowable verdict
// (the close-out waits for nothing); if it ignores less, the borrow is still
// sound but the walk and the workflow no longer describe each other.
func TestTheWorkflowIgnoresExactlyTheRunStatePaths(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var lists [][]string
	var current []string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "paths-ignore:":
			in, current = true, nil
		case in && strings.HasPrefix(trimmed, "- "):
			current = append(current, strings.Trim(strings.TrimPrefix(trimmed, "- "), `"'`))
		case in && (trimmed == "" || strings.HasPrefix(trimmed, "#")):
		case in:
			lists, in = append(lists, current), false
		}
	}
	if in {
		lists = append(lists, current)
	}
	if len(lists) != 2 {
		t.Fatalf("ci.yml carries %d paths-ignore lists, want two (push and pull_request): %v", len(lists), lists)
	}
	for _, list := range lists {
		if len(list) != 1 || list[0] != runStatePrefix+"**" {
			t.Errorf("ci.yml ignores %v, want exactly [%s**]: the close-out's walk sees past %s commits only",
				list, runStatePrefix, runStatePrefix)
		}
	}
}
