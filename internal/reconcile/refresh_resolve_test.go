package reconcile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The base fold's conflict, absorbed by a resolve-conflict job instead of
// stopping for a person (stall class base_refresh_conflict).
//
// epic-2jn stopped three times on 2026-09-27 with "<sha> of main does not fold
// into epic/2jn: go.mod, go.sum …", and each stop was a person merging main
// into the epic branch by hand. These tests drive the same shape through the
// reconciler's own machinery: a real content conflict between what landed on
// main and what the epic branch carries, in a real repository, met by the fold
// every incarnation makes before it plans.

// baseFoldConflict builds the fold that cannot merge: a dependency manifest at
// the fork, then edited one way on the epic branch and another way on main
// after the fork — the go.mod shape of the incident. It answers the epic
// branch's head and main's head as origin has them once both sides wrote.
func baseFoldConflict(t *testing.T, f *fixture) (epicHead, mainHead string) {
	t.Helper()
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.0.0\n", "the manifest both sides start from")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")
	commitOnIntegrationBranch(t, f, "epic/qeu", "deps.txt",
		"require example.com/a v1.0.0\nrequire example.com/epic v0.3.0\n", "the epic's tick adds a requirement")
	commitOnBase(t, f.Repo, "deps.txt",
		"require example.com/a v1.2.0\n", "main bumps a requirement")
	epicHead = strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("epic/qeu")))
	mainHead = strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("main")))
	return epicHead, mainHead
}

// baseFoldDecisions is every base-fold resolve the run branch records.
func baseFoldDecisions(t *testing.T, r *Reconciler) []runstate.Decision {
	t.Helper()
	if _, err := r.store.Fetch(); err != nil {
		t.Fatal(err)
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var out []runstate.Decision
	for _, d := range decisions {
		if d.Role == RoleResolveConflict && d.Request["kind"] == baseFoldKind {
			out = append(out, d)
		}
	}
	return out
}

// resolveStarts is how many resolve-conflict jobs the fixture's executors were
// asked to start.
func resolveStarts(f *fixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, spec := range f.specs {
		if spec != nil && spec.Role == RoleResolveConflict {
			n++
		}
	}
	return n
}

// THE STALL, REPRODUCED AND ABSORBED. main and the epic both edited the
// manifest; the fold conflicts; the run dispatches a resolve-conflict job on
// the conflicted fold, pushes the merge commit it resolved — parents: the epic
// head and main's head — and continues to a completed run with no person.
// Without the fix this run ends failed with base_refresh_conflict.
func TestABaseFoldConflictIsResolvedByAJobAndTheRunContinues(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "conflict"})
	epicHead, mainHead := baseFoldConflict(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "conflict"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a fold conflict the resolve job answered is not a stop",
			result.State, result.Reason)
	}
	if len(result.Closed) == 0 {
		t.Error("the run closed nothing after the fold: it did not continue")
	}

	// The job: the resolve-conflict role, cut at the conflicted fold, with a
	// prompt that says it is FOLDING main into the epic and names both sides.
	_, spec, _ := resolveSpecOf(t, f)
	if spec == nil {
		t.Fatal("no resolve-conflict job was dispatched for the conflicted fold")
	}
	// The fold's job is dispatched under the epic's own id: it is the epic's
	// record that states the epic side's intent.
	dispatch := f.dispatch("qeu")
	if dispatch.Role != RoleResolveConflict {
		t.Fatalf("the dispatch under the epic's id is role %q", dispatch.Role)
	}
	if dispatch.Profile == nil {
		t.Fatal("the resolve-conflict job was dispatched with no profile")
	}
	prompt := dispatch.Profile.Prompt
	for _, want := range []string{
		"folding main into the epic", "deps.txt", "main bumps a requirement",
		"the epic's tick adds a requirement", "epic/qeu", ".tick/issues/qeu.json",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the resolve job's prompt does not say %q:\n%s", want, prompt)
		}
	}
	wip := spec.Source.BaseSHA
	parents := strings.Fields(runGitQuiet(f.Repo.Dir, "rev-list", "--parents", "-n", "1", wip))
	if len(parents) != 3 || parents[1] != epicHead || parents[2] != mainHead {
		t.Errorf("the job's worktree is not the fold of main %s into the epic %s: %v",
			short(mainHead), short(epicHead), parents)
	}
	if blob := readGitBlob(t, f.Repo.Dir, wip, "deps.txt"); !strings.Contains(blob, "<<<<<<<") {
		t.Errorf("the job's worktree does not carry the conflict markers:\n%s", blob)
	}

	// The fold on origin: a merge commit whose parents are the epic head and
	// main's head, over the job's resolution, and the branch carries main.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	if got := readGitBlob(t, clone.Dir, "origin/epic/qeu", "deps.txt"); got != "resolved by the resolve-conflict job" {
		t.Errorf("the manifest on the integration branch is %q, not the job's resolution", got)
	}
	folds := runGitQuiet(clone.Dir, "log", "--merges", "--format=%P %s", "origin/epic/qeu")
	found := false
	for _, line := range strings.Split(folds, "\n") {
		if strings.Contains(line, "through the resolve-conflict job") {
			found = true
			fields := strings.Fields(line)
			if fields[0] != epicHead || fields[1] != mainHead {
				t.Errorf("the resolved fold's parents are %s %s, want the epic head %s and main's head %s",
					short(fields[0]), short(fields[1]), short(epicHead), short(mainHead))
			}
		}
	}
	if !found {
		t.Errorf("the integration branch carries no fold minted from the job's resolution:\n%s", folds)
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", mainHead, refFor("epic/qeu")) {
		t.Errorf("epic/qeu does not carry main at %s after the resolved fold", short(mainHead))
	}

	// The decision: durable, and marked as the fold's.
	decisions := baseFoldDecisions(t, r)
	if len(decisions) != 1 {
		t.Fatalf("the run recorded %d base-fold resolves, want 1", len(decisions))
	}
	if status, _ := decisions[0].Response["status"].(string); status != "merged" {
		t.Errorf("the recorded fold resolve says %q", status)
	}
	if head, _ := decisions[0].Request["base_head"].(string); head != mainHead {
		t.Errorf("the recorded fold resolve names base head %s, want %s", short(head), short(mainHead))
	}
}

// THE BOUND. A resolve that fails — here it answers BLOCKED — is the stop,
// naming both sides and the files; and the next incarnation that meets the
// same fold stops naming the recorded resolve instead of paying for a second
// one, even with a worker that would now resolve it.
func TestAFailedBaseFoldResolveIsTheStopAndIsNotPaidForTwice(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "conflict_unresolvable"})
	epicHead, mainHead := baseFoldConflict(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "conflict_unresolvable"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.Reason != RefusedBaseRefresh {
		t.Fatalf("the run ended %s (%+v), want the %s stop", result.State, result.Failure, RefusedBaseRefresh)
	}
	for _, want := range []string{
		"main at " + short(mainHead), "epic/qeu at " + short(epicHead), "deps.txt", "resolve-conflict job",
	} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the stop does not name %q: %s", want, result.Failure.Message)
		}
	}
	if resolveStarts(f) != 1 {
		t.Errorf("%d resolve-conflict jobs were started, want 1", resolveStarts(f))
	}
	if mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", mainHead, refFor("epic/qeu")) {
		t.Error("epic/qeu carries main although the fold's resolve failed")
	}
	decisions := baseFoldDecisions(t, r)
	if len(decisions) != 1 {
		t.Fatalf("the failed resolve left %d base-fold records, want 1", len(decisions))
	}
	if status, _ := decisions[0].Response["status"].(string); status != "failed" {
		t.Errorf("the recorded fold resolve says %q, not \"failed\"", status)
	}

	// The next incarnation: same run, same fold, a worker that WOULD resolve.
	// One resolve per fold is the bound, so this is the stop again — and no
	// job is started.
	f.Runner = fakeRunnerArgv(t, "conflict")
	_, again, err := f.run(f.Repo, fixtureOptions{mode: "conflict"})
	if err != nil {
		t.Fatalf("the second incarnation did not finish: %v", err)
	}
	if again.State != runstate.StateFailed || again.Failure == nil || again.Failure.Reason != RefusedBaseRefresh {
		t.Fatalf("the second incarnation ended %s (%+v), want the %s stop", again.State, again.Failure, RefusedBaseRefresh)
	}
	for _, want := range []string{"already ran for this fold", `"failed"`, "base-fold-1-", "deps.txt"} {
		if !strings.Contains(again.Failure.Message, want) {
			t.Errorf("the second stop does not name %q: %s", want, again.Failure.Message)
		}
	}
	if resolveStarts(f) != 1 {
		t.Errorf("a second resolve-conflict job was started for the same fold (%d starts)", resolveStarts(f))
	}
}

// A conflict whose kind is one side's change making the other's meaningless —
// the epic deleted a file main edited — is not a union a resolve job makes:
// it is the typed stop it always was, and nothing is dispatched.
func TestAModifyDeleteFoldConflictIsStillTheStop(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "conflict"})
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.0.0\n", "the manifest both sides start from")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")
	dir := filepath.Join(f.Root, "other-side")
	cloneRepo(t, f.Repo.Origin, dir)
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "side", "origin/epic/qeu")
	mustRun(t, dir, "git", "rm", "--quiet", "deps.txt")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "the epic drops the manifest")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "side:refs/heads/epic/qeu")
	commitOnBase(t, f.Repo, "deps.txt", "require example.com/a v1.2.0\n", "main bumps a requirement")

	_, result, err := f.run(f.Repo, fixtureOptions{mode: "conflict"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.Reason != RefusedBaseRefresh {
		t.Fatalf("the run ended %s (%+v), want the %s stop", result.State, result.Failure, RefusedBaseRefresh)
	}
	if !strings.Contains(result.Failure.Message, "deps.txt") {
		t.Errorf("the stop does not name the file: %s", result.Failure.Message)
	}
	if resolveStarts(f) != 0 {
		t.Error("a resolve-conflict job was dispatched for a modify/delete fold conflict")
	}
}
