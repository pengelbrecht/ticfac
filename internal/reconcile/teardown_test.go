package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The fourth review's findings about teardown, in the order it raised them: a
// rejected attempt is not renamed `cancelled` by the teardown that follows it,
// work that exists only on a local branch is never called spent, every refusal
// class tears its attempt down, and a gate that failed has a way out.

// gateNeedingAFix is a gate an operator has to repair before it can pass: the
// integrated tree has to carry a file nothing in the run produces. It is the
// ordinary shape of a gate failure — the check is right and the tree is not, or
// the other way round — and the repair is the ordinary one: fix it, push it,
// run the epic again.
const gateNeedingAFix = `version = 2

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null && test -f gate-fix.txt", description = "the tree the operator has to fix" }
`

// A rejected attempt that carried commits is NOT read as cancelled afterwards.
//
// The teardown revokes the credential through the executor's cancel, and cancel
// wrote its durable refusal unconditionally — over an attempt that had already
// settled itself. inspect reads that record first, so every later look at the
// attempt answered `cancelled` and every later collect answered
// VerdictMissingResult/OutcomeCancelled: a resumed run refused it with a
// sentence about an event that never happened, and sent whoever read it at the
// wrong problem (Appendix A #9).
func TestARejectedAttemptThatCarriedCommitsIsNotReadAsCancelled(t *testing.T) {
	silent := fixtureOptions{mode: "silent"}
	f := newFixture(t, silent)

	// The worker commits and never reports: settled and incomplete, which the
	// collect refuses — with work on the branch, which is the case that
	// matters. The teardown then runs.
	_, result, err := f.run(f.Repo, silent)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	marker := attemptMarker(t, f, "a1", 1)
	state := attemptStateDir(t, f, marker)

	// The credential died — that half of the teardown is not negotiable — and
	// nothing was recorded as stopped, because nothing was stopped.
	if _, err := os.Stat(filepath.Join(state, "credential")); !os.IsNotExist(err) {
		t.Errorf("the rejected attempt's credential survived its teardown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "cancel.json")); err == nil {
		raw, _ := os.ReadFile(filepath.Join(state, "cancel.json"))
		t.Errorf("the teardown recorded a cancellation of an attempt that had already settled:\n%s", raw)
	}

	// What the executor says about it now: not cancelled, and the verdict is
	// the one the attempt really earned.
	status, collected := addressAttempt(t, f, marker)
	if status.State == subprocess.StateCancelled {
		t.Errorf("the attempt inspects as %s after a teardown that stopped nothing", status.State)
	}
	if collected.Result.Outcome == subprocess.OutcomeCancelled {
		t.Errorf("the attempt collects as %s; it settled itself and was never stopped", collected.Result.Outcome)
	}
	if collected.Verdict != subprocess.VerdictMissingResult {
		t.Errorf("the attempt collects as %s, not on the verdict it really had", collected.Verdict)
	}

	// And the resumed run says the same thing: it refuses over the commits the
	// attempt left, never over a cancellation.
	_, resumed, err := f.run(f.Repo, silent)
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.Failure == nil || resumed.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resumed run failed as %+v, want %s", resumed.Failure, RefusedRejectedWork)
	}
	if strings.Contains(strings.ToLower(resumed.Failure.Message), "cancel") {
		t.Errorf("the resumed run refuses over a cancellation nobody performed: %s", resumed.Failure.Message)
	}
	if !strings.Contains(resumed.Failure.Message, branchOf(marker.WriteRef)) {
		t.Errorf("the refusal does not name the branch the work is on: %s", resumed.Failure.Message)
	}
}

// An attempt whose work reached NO remote is never declared spent.
//
// spent() read origin's ref alone, so an attempt whose every push failed — the
// timed ones and the corrective one integrate makes — was judged to have left
// nothing the moment origin was reachable again, and the next run dispatched
// over it and orphaned the local branch holding the only copy. The local branch
// is one `git rev-parse` away in the checkout the run is working in.
func TestAnAttemptWhoseWorkNeverReachedOriginIsNotDeclaredSpent(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	refuseEveryAttemptPush(t, f.Repo.Origin)

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedMerge {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedMerge)
	}

	marker := attemptMarker(t, f, "a1", 1)
	branch := branchOf(marker.WriteRef)
	local := branchHead(f.Repo.Dir, branch)
	if local == "" || local == marker.BaseSHA {
		t.Fatalf("the fixture left no commit on %s; there is nothing to lose", branch)
	}
	if remote := strings.TrimSpace(runGitQuiet(f.Repo.Origin, "rev-parse", "--verify", "--quiet", refFor(branch))); remote != "" {
		t.Fatalf("the fixture's push refusal did not work: origin carries %s", short(remote))
	}
	// The merge refusal tore the attempt down: the worktree is gone, the branch
	// that holds the only copy is not.
	assertOneWorktree(t, f.Repo.Dir)
	if _, err := os.Stat(filepath.Join(attemptStateDir(t, f, marker), "credential")); !os.IsNotExist(err) {
		t.Errorf("the refused attempt's credential survived the refusal: %v", err)
	}

	// Origin is back. The attempt is still not spent, and the run says where
	// its work is rather than dispatching over it.
	allowEveryPush(t, f.Repo.Origin)
	second, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the second run did not finish: %v", err)
	}
	if resumed.Failure == nil || resumed.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the second run failed as %+v, want %s", resumed.Failure, RefusedRejectedWork)
	}
	if !strings.Contains(resumed.Failure.Message, branch) {
		t.Errorf("the refusal does not name the branch the work is on: %s", resumed.Failure.Message)
	}
	if !strings.Contains(resumed.Failure.Message, "this checkout") {
		t.Errorf("the refusal does not say the work is only in this checkout: %s", resumed.Failure.Message)
	}
	if got := second.Stages("a1"); contains(got, StageRedispatched) || contains(got, StageDispatched) {
		t.Errorf("an attempt whose work only this checkout has was dispatched over: %v", got)
	}
	if got := attemptsFor(t, f, "a1"); got != 1 {
		t.Errorf("%d dispatch markers for a1; the unpushed attempt was redispatched", got)
	}
	if now := branchHead(f.Repo.Dir, branch); now != local {
		t.Errorf("%s is at %q, not at the %s the work is on", branch, now, short(local))
	}

	// And a person can release it, which is the way on: the next run then
	// dispatches a NEW attempt and leaves the branch where it is.
	settler, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle the rejected attempt: %v", err)
	}
	if !settled.Recorded {
		t.Fatalf("the settlement recorded nothing: %+v", settled)
	}
	third, after, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the release did not finish: %v", err)
	}
	if after.State != runstate.StateCompleted {
		t.Fatalf("the run after the release ended %s: %s", after.State, after.Reason)
	}
	if got := third.Stages("a1"); !contains(got, StageSettled) || !contains(got, StageDispatched) {
		t.Errorf("a1's stages %v do not show the released attempt skipped and a new one dispatched", got)
	}
	if now := branchHead(f.Repo.Dir, branch); now != local {
		t.Errorf("the released attempt's branch moved to %q; its commits are the only copy", now)
	}
}

// A GATE failure has a way out, and it is the one the failed run's reason
// promises: fix the check or the tree, push it, run the epic again.
//
// It could not be, before this: the attempt's merge was already on the
// integration branch, so a resume found it "already contained", and the gate's
// evidence was keyed per (tick, attempt, check) and created if absent — so the
// resumed run re-read the recorded `fail` without running anything and refused
// again, forever, whatever the person had fixed. A new run id did not help
// either: the merge stays on the branch and a new attempt cut from the
// integration head finds the work already there.
func TestAGateThatFailedRunsAgainOnceTheTreeIsFixed(t *testing.T) {
	failing := fixtureOptions{gate: gateNeedingAFix}
	f := newFixture(t, failing)

	_, result, err := f.run(f.Repo, failing)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedGate {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedGate)
	}
	if !strings.Contains(result.Failure.Message, "run the epic again") {
		t.Errorf("the gate refusal does not say how the failure is recovered: %s", result.Failure.Message)
	}

	// The refusal tore the attempt down — every refusal class does — and kept
	// the branch, which carries commits.
	marker := attemptMarker(t, f, "a1", 1)
	assertOneWorktree(t, f.Repo.Dir)
	if _, err := os.Stat(filepath.Join(attemptStateDir(t, f, marker), "credential")); !os.IsNotExist(err) {
		t.Errorf("the gate-refused attempt's credential is still live: %v", err)
	}
	if head := branchHead(f.Repo.Dir, branchOf(marker.WriteRef)); head == "" {
		t.Errorf("the teardown deleted %s, which carries the merged work", branchOf(marker.WriteRef))
	}

	// The merge IS on the integration branch: this is why a new attempt would
	// not help and why the gate has to be runnable again.
	epicHead := remoteHeadOf(t, f, "epic/qeu")
	attemptHead := remoteHeadOf(t, f, branchOf(marker.WriteRef))
	if !containsCommit(t, f, attemptHead, epicHead) {
		t.Fatalf("%s is not on the integration branch; this test is not about a gate failure at all", short(attemptHead))
	}

	// The person: they fix the tree and push it to the integration branch.
	pushFixToEpic(t, f, "gate-fix.txt")

	second, resumed, err := f.run(f.Repo, failing)
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", resumed.State, resumed.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s after the fix and the resume", current.Status)
	}
	if got := second.Stages("a1"); !contains(got, StageGatePassed) {
		t.Errorf("a1's stages %v do not show the gate running again", got)
	}
	if got := attemptsFor(t, f, "a1"); got != 1 {
		t.Errorf("%d dispatch markers for a1; the recovery redispatched work that was already merged", got)
	}

	// The evidence says both things, and neither was overwritten: the failure
	// on the commit it ran on, and the pass on the commit the person fixed.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	base, ok, err := store.Evidence(evidenceKey("a1", 1, "tree"))
	if err != nil || !ok {
		t.Fatalf("the failing gate left no evidence under its own key: %v", err)
	}
	if base.Result != "fail" {
		t.Errorf("the first record is %s; a record that can be overwritten is not evidence", base.Result)
	}
	passed := 0
	for _, key := range store.EvidenceKeys() {
		if !strings.HasPrefix(key, evidenceKey("a1", 1, "tree")+"-") {
			continue
		}
		record, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("read evidence %s: %v", key, err)
		}
		if record.Result != "pass" {
			continue
		}
		passed++
		if record.Provenance.SourceSHA == base.Provenance.SourceSHA {
			t.Errorf("the second record ran on %s, the commit the first one failed on",
				short(record.Provenance.SourceSHA))
		}
		if !strings.HasSuffix(key, short(record.Provenance.SourceSHA)) {
			t.Errorf("evidence %s is not keyed by the commit it ran on (%s)", key, short(record.Provenance.SourceSHA))
		}
	}
	if passed != 1 {
		t.Errorf("%d passing records for a1's gate; the fixed tree should have been gated exactly once", passed)
	}
}

// A ROLE job's refusal tears its attempt down too.
//
// 7mj left these out deliberately and filed nothing; the decision is made here:
// a role job that answered BLOCKED, one whose envelope did not validate, one
// collected from a forged base and one that wrote outside its authority all
// leave a job that is finished. The tick stays open for the person the answer
// asked for, and the answer is on the branch and in the run's records — not in
// a worktree the operator's checkout keeps forever.
func TestARoleJobsRefusalTearsItsAttemptDown(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.wrap = corruptRoleResult(func(result *subprocess.RoleResult) {
		result.Status = subprocess.StatusBlocked
		result.Summary = "the epic's second wave was never integrated"
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedRoleAnswer {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedRoleAnswer)
	}

	marker := attemptMarker(t, f, "rv", 0)
	if _, err := os.Stat(filepath.Join(attemptStateDir(t, f, marker), "credential")); !os.IsNotExist(err) {
		t.Errorf("the refused role job's credential is still live: %v", err)
	}
	assertOneWorktree(t, f.Repo.Dir)

	// The branch is the other half of the rule, and it is the same half: this
	// job committed (the fixture's runner commits for every tick, role jobs
	// included), so its branch is KEPT. What a person reads next is the answer,
	// and a teardown that took the commits with it would be a teardown that
	// threw away what the refusal is about.
	branch := branchOf(marker.WriteRef)
	head := branchHead(f.Repo.Dir, branch)
	if head == "" {
		t.Fatalf("the teardown deleted %s, which carries the job's commits", branch)
	}
	if head == marker.BaseSHA {
		t.Fatalf("%s carries nothing beyond its base; this fixture proves nothing about keeping work", branch)
	}
}

// ------------------------------------------------------------- helpers ---

// attemptMarker is one attempt's dispatch marker, read back from origin the way
// a restarted run reads it. attempt 0 means "whichever attempt this tick has".
func attemptMarker(t *testing.T, f *fixture, tick string, attempt int) attemptHandle {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID != tick || (attempt != 0 && record.Attempt != attempt) {
			continue
		}
		return handleFromMap(record.JobHandle)
	}
	t.Fatalf("run r-fixture has no attempt %d of %s", attempt, tick)
	return attemptHandle{}
}

// attemptStateDir is the executor's own state directory for an attempt, found
// the way the reconciler finds it.
func attemptStateDir(t *testing.T, f *fixture, marker attemptHandle) string {
	t.Helper()
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	state, ok := findAttemptState(r.execStateDir(marker.TickID, marker.Attempt))
	if !ok {
		t.Fatalf("this host holds no executor state for attempt %d of %s", marker.Attempt, marker.TickID)
	}
	return state
}

// addressAttempt asks the executor what it can still say about an attempt: the
// same two questions a resumed run asks, through the same seam.
func addressAttempt(t *testing.T, f *fixture, marker attemptHandle) (*subprocess.JobStatus, *subprocess.Collection) {
	t.Helper()
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
	executor, err := r.opts.NewExecutor(r.dispatchFor(marker))
	if err != nil {
		t.Fatal(err)
	}
	handle := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         marker.JobID,
		Attempt:       marker.Attempt,
		Executor:      subprocess.ExecutorName,
		Handle:        map[string]any{"state": attemptStateDir(t, f, marker)},
	}
	status, err := executor.Inspect(handle, "")
	if err != nil {
		t.Fatalf("inspect attempt %d of %s: %v", marker.Attempt, marker.TickID, err)
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect attempt %d of %s: %v", marker.Attempt, marker.TickID, err)
	}
	return status, collected
}

// attemptsFor counts the dispatch markers one tick has in this run.
func attemptsFor(t *testing.T, f *fixture, tick string) int {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range attempts {
		if record.TickID == tick {
			count++
		}
	}
	return count
}

// refuseEveryAttemptPush makes origin decline every write to an attempt's write
// ref, creations included: the shape of a remote that is unreachable for the
// whole of an attempt's life, so its commits exist only in this checkout.
func refuseEveryAttemptPush(t *testing.T, origin string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "update")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"refs/heads/ticfac/*)\n" +
		"\techo 'the fixture refuses every write to this ref' >&2\n" +
		"\texit 1\n" +
		"\t;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// allowEveryPush is origin coming back.
func allowEveryPush(t *testing.T, origin string) {
	t.Helper()
	if err := os.Remove(filepath.Join(origin, "hooks", "update")); err != nil {
		t.Fatal(err)
	}
}

// pushFixToEpic is the person fixing what the gate refused: a commit on the
// integration branch, pushed, exactly as an operator would make it.
func pushFixToEpic(t *testing.T, f *fixture, name string) {
	t.Helper()
	dir := filepath.Join(f.Root, "operator-fix")
	mustRun(t, f.Root, "git", "clone", "--quiet", "--branch", "epic/qeu", f.Repo.Origin, dir)
	configure(t, dir)
	write(t, filepath.Join(dir, name), "the operator fixed what the gate refused\n")
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "operator: fix the gate")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
}
