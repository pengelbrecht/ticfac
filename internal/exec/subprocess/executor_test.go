package subprocess

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The executor's required behaviour, over a real repository, a real supervisor
// process and the fake runner.
//
// Six of these were named as required by the tick that commissioned this
// package (Peter, 2026-09-02). Each one is a failure this design already knows
// about rather than a shape that seemed worth testing.

// REQUIRED. A worker settles while NOBODY is inspecting, and the next inspect
// sees it.
//
// This is the lost-event race, and the reason it cannot happen here is
// structural rather than lucky: settlement is a FILE, not an event. Nothing
// has to be listening at the moment a worker finishes, because there is no
// moment — there is a report on disk and a branch in git, and the first
// inspect after either appears reads them.
func TestAFinishedWorkerIsSeenByTheNextInspectWithNobodyWatching(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-1/tick-aaa/attempt-1", "aaa"))

	// Deliberately no inspect while it runs.
	f.waitSettled(handle)
	time.Sleep(100 * time.Millisecond)

	status := f.inspect(handle)
	if status.State != StateSucceeded || !status.Terminal {
		t.Fatalf("the first inspect after settlement says %s (terminal %v); the report and the branch were both there",
			status.State, status.Terminal)
	}
	if status.Cursor != nil {
		t.Errorf("a terminal status still offers a cursor %q; there is nothing more to say", *status.Cursor)
	}
}

// REQUIRED. A killed-and-re-issued wait sees settled work IMMEDIATELY.
//
// The controller that started the job is gone, and a new one holding nothing
// but the handle record asks. It must get the answer at once — not wait for a
// process it never started, and not re-run anything.
func TestAKilledAndReissuedWaitSeesSettledWorkImmediately(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-2/tick-bbb/attempt-1", "bbb"))
	f.waitSettled(handle)

	// A different Executor value, as a restarted controller would build: same
	// repository, same state root, no memory of the start.
	reissued, err := New(Options{
		Repo:           f.Repo.Dir,
		StateDir:       f.StateDir,
		SupervisorArgv: []string{executorBin, "supervise"},
	})
	if err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	status, err := reissued.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Errorf("the re-issued wait took %s to answer; settled work is a file, not an event to wait for", elapsed)
	}
	if status.State != StateSucceeded {
		t.Fatalf("the re-issued wait says %s, want %s", status.State, StateSucceeded)
	}
	result, err := reissued.Collect(handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("collect from the re-issued controller says %s", result.Outcome)
	}
}

// REQUIRED. A worker that cds to /tmp and then writes its report still
// collects.
//
// The report path is absolute and the executor owns it, so the worker's
// working directory at the moment it writes is irrelevant — which is the whole
// reason the path is not "RESULT-<id>.md in the repository root".
func TestAWorkerThatWandersOffStillWritesTheReportWhereCollectReadsIt(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report_from_tmp"})
	handle := f.Start(f.spec("run-3/tick-ccc/attempt-1", "ccc"))
	f.waitSettled(handle)

	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(local.ResultPath) {
		t.Fatalf("the report path %q is not absolute", local.ResultPath)
	}
	if !strings.HasPrefix(local.ResultPath, local.Worktree+string(filepath.Separator)) {
		t.Fatalf("the report path %q is not inside the attempt worktree %q", local.ResultPath, local.Worktree)
	}

	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s (%s); the report was written from /tmp to the absolute path",
			collected.Verdict, collected.Message)
	}
	if collected.Report.Status != StatusDone {
		t.Errorf("status %q, want %s", collected.Report.Status, StatusDone)
	}
}

// REQUIRED. Two repositories running the same tick id, at the same time, do
// not collide.
//
// They share a state root here on purpose: if the repository were not part of
// a handle's identity, one attempt's state directory, worktree and branch
// would be the other's.
func TestTwoRepositoriesRunningTheSameTickDoNotCollide(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "state")
	first := newFixture(t, fixtureOptions{mode: "report", name: "alpha", stateDir: shared})
	second := newFixture(t, fixtureOptions{mode: "report", name: "beta", stateDir: shared})

	if first.Executor.RepoKey() == second.Executor.RepoKey() {
		t.Fatal("two repositories share one repo key; every attempt in one would address the other's state")
	}

	jobID, tick := "run-4/tick-ddd/attempt-1", "ddd"
	a := first.Start(first.spec(jobID, tick))
	b := second.Start(second.spec(jobID, tick))

	localA, _ := a.Local()
	localB, _ := b.Local()
	if localA.State == localB.State {
		t.Fatalf("both attempts share the state directory %s", localA.State)
	}
	if localA.Worktree == localB.Worktree {
		t.Fatalf("both attempts share the worktree %s", localA.Worktree)
	}

	first.waitSettled(a)
	second.waitSettled(b)

	for name, pair := range map[string]struct {
		f *fixture
		h *JobHandle
	}{"alpha": {first, a}, "beta": {second, b}} {
		collected := pair.f.collect(pair.h)
		if collected.Verdict != VerdictReadyToMerge {
			t.Errorf("%s: verdict %s (%s)", name, collected.Verdict, collected.Message)
		}
		head := runGit(t, pair.f.Repo.Dir, "rev-parse", "refs/heads/tick/"+tick)
		if head == "" {
			t.Errorf("%s: the attempt branch is missing from its own repository", name)
		}
	}
}

// REQUIRED. Cleanup leaves zero run-created worktrees and zero run-created
// branches.
func TestCleanupLeavesNoRunCreatedWorktreeOrBranch(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-5/tick-eee/attempt-1", "eee"))
	f.waitSettled(handle)

	if got := worktreeCount(t, f.Repo.Dir); got != 2 {
		t.Fatalf("%d worktrees while the attempt is live, want the main one plus the attempt's", got)
	}
	f.collect(handle)

	// The credential is still live: teardown before revocation is refused,
	// which is Appendix A #1's ordering seen from the disposal side.
	if err := f.Executor.Dispose(handle, DisposeOptions{}); err == nil {
		t.Fatal("disposal was allowed while the attempt still held a live credential")
	} else if refusal, ok := AsRefusal(err); !ok || refusal.Reason != RefusedCredential {
		t.Fatalf("disposal refused with %v, want the credential-live refusal", err)
	}
	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatal(err)
	}

	if err := f.Executor.Dispose(handle, DisposeOptions{}); err != nil {
		t.Fatalf("dispose after collect and revocation: %v", err)
	}
	if got := worktreeCount(t, f.Repo.Dir); got != 1 {
		t.Errorf("%d worktrees after cleanup, want only the main one", got)
	}
	if branches := runGit(t, f.Repo.Dir, "branch", "--list", "tick/*"); branches != "" {
		t.Errorf("cleanup left the run-created branch(es): %q", branches)
	}
	local, _ := handle.Local()
	if _, err := os.Stat(local.State); err != nil {
		t.Errorf("disposal removed the attempt's record as well as its git state: %v", err)
	}

	// The record outlives the git objects until it is purged deliberately: it
	// holds the collected result, the observation log and the transcript.
	if err := f.Executor.PurgeState(handle); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local.State); !os.IsNotExist(err) {
		t.Errorf("the attempt state survived a purge: %v", err)
	}
}

// Disposal refuses to delete a branch whose commits no remote has: cleanup
// that also throws away the only copy of the work is not cleanup.
func TestDisposalRefusesToDiscardWorkNoRemoteHas(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report", noRemote: true})
	handle := f.Start(f.spec("run-6/tick-fff/attempt-1", "fff"))
	f.waitSettled(handle)
	f.collect(handle)
	if _, err := f.Executor.Cancel(handle); err != nil {
		t.Fatal(err)
	}

	err := f.Executor.Dispose(handle, DisposeOptions{})
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedBranchUnsafe {
		t.Fatalf("dispose returned %v, want a refusal to discard unpushed work", err)
	}
	if err := f.Executor.Dispose(handle, DisposeOptions{KeepBranch: true}); err != nil {
		t.Fatalf("dispose keeping the branch: %v", err)
	}
	if got := worktreeCount(t, f.Repo.Dir); got != 1 {
		t.Errorf("%d worktrees after cleanup, want only the main one", got)
	}
}

// REQUIRED. kill -9 of the executor mid-job leaves a handle that reconciles.
//
// The executor is killed as a process GROUP, the way a supervisor's own death would
// take its children — and the attempt survives it, because the supervisor was
// put in a group of its own at spawn. What is left behind is a handle, and the
// handle is enough.
func TestKillingTheExecutorMidJobLeavesAReconcilableHandle(t *testing.T) {
	if testing.Short() {
		// It runs the shipped binary and waits on a real runner.
		t.Skip("short mode: this one kills a real process tree")
	}
	f := newFixture(t, fixtureOptions{mode: "slow_report", sleep: "3"})
	spec := f.spec("run-7/tick-ggg/attempt-1", "ggg")

	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	handlePath := filepath.Join(dir, "handle.json")
	writeJSONFile(t, specPath, spec)

	// `start` inside a shell that then waits, so there is a live executor
	// process tree to kill.
	script := fmt.Sprintf("%s start --repo %s --state-dir %s --runner claude < %s > %s; sleep 60",
		shellQuote(executorBin), shellQuote(f.Repo.Dir), shellQuote(f.StateDir),
		shellQuote(specPath), shellQuote(handlePath))
	starter := startInOwnGroup(t, script, f.Executor.opts.RunnerArgv)

	waitFor(t, "start to write the handle", 30*time.Second, func() bool {
		info, err := os.Stat(handlePath)
		return err == nil && info.Size() > 0
	})
	handle := readHandleFile(t, handlePath)
	f.handles = append(f.handles, handle)
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}

	// kill -9 the whole executor process group.
	if err := signalGroup(starter.pid, sigKill()); err != nil {
		t.Fatalf("kill the executor process group: %v", err)
	}
	starter.reap()
	if processAlive(starter.pid) {
		t.Fatal("the executor survived kill -9 of its process group")
	}

	if !processAlive(local.PID) {
		t.Fatal("the supervisor died with the executor; a job must outlive the controller that started it")
	}

	// A fresh controller, holding only the handle.
	reconciler, err := New(Options{Repo: f.Repo.Dir, StateDir: f.StateDir, SupervisorArgv: []string{executorBin, "supervise"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := reconciler.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRunning {
		t.Fatalf("the reconciled handle says %s, want %s: the attempt is still running", status.State, StateRunning)
	}

	f.waitSettled(handle)
	result, err := reconciler.Collect(handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSucceeded {
		t.Fatalf("collect after the executor was killed says %s", result.Outcome)
	}
}

// Settled and incomplete is its OWN status: the runner exited, the work is
// committed, and nothing was reported. It is neither running nor done, and a
// controller that rounded it to either would wait forever or merge silence.
func TestSettledWithoutAReportIsItsOwnStatus(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "silent"})
	handle := f.Start(f.spec("run-8/tick-hhh/attempt-1", "hhh"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if status.State != StateFailed || !status.Terminal {
		t.Fatalf("state %s (terminal %v), want a terminal %s", status.State, status.Terminal, StateFailed)
	}
	if !mentions(status.Observations, "settled is not finished") {
		t.Errorf("the status does not say WHY it failed:\n%s", formatObservations(status.Observations))
	}

	collected := f.collect(handle)
	if collected.Verdict != VerdictMissingResult {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictMissingResult)
	}
	if collected.Result.Outcome != OutcomeFailed || collected.Result.FailureClass != FailureRunnerError {
		t.Errorf("outcome %s/%s", collected.Result.Outcome, collected.Result.FailureClass)
	}
	if collected.Result.RoleResult != nil {
		t.Error("a result with no report carries a role_result; this executor does not invent a worker's answer")
	}
	if collected.Result.Source.Commits == 0 {
		t.Error("the branch carries the work the runner committed; collect reports no commits")
	}
}

// A report over a branch with no commits is `no-commits`, whatever the report
// says: the first failing check wins, and the branch is checked first.
func TestAReportOverAnEmptyBranchIsNoCommits(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "nocommit"})
	handle := f.Start(f.spec("run-9/tick-iii/attempt-1", "iii"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictNoCommits {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictNoCommits)
	}
	if collected.Result.Source.HeadSHA != nil {
		t.Errorf("head_sha is %q; a job that produced no commit says so as a fact", *collected.Result.Source.HeadSHA)
	}
	if collected.Result.Outcome != OutcomeFailed {
		t.Errorf("outcome %s", collected.Result.Outcome)
	}
}

// vcx: a codex run that hits its account's flat-rate seat usage limit before
// making any commit collects as its own failure class — quota_exhausted —
// rather than the generic runner_error a broken route would report. SPEC
// §4.3: quota exhaustion is never reported as a broken route. The log is the
// golden fixture testdata/codex-usage-limit.log, captured 2026-09-02.
func TestAQuotaExhaustedRunnerCollectsWithItsOwnFailureClass(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "quota_exhausted"})
	handle := f.Start(f.spec("run-19/tick-ttt/attempt-1", "ttt"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictNoCommits {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictNoCommits)
	}
	if collected.Result.Outcome != OutcomeFailed || collected.Result.FailureClass != FailureQuotaExhausted {
		t.Errorf("outcome %s/%s, want %s/%s",
			collected.Result.Outcome, collected.Result.FailureClass, OutcomeFailed, FailureQuotaExhausted)
	}
}

// vcx: pi's own words for the same fact — an account out of extra usage —
// share none of codex's wording ("You've hit your usage limit" vs "You're out
// of extra usage"), so this is what proves the pattern is a real alternation
// between two captured phrasings rather than one guess that happens to cover
// codex.
func TestAPiOutOfUsageRunnerCollectsWithItsOwnFailureClass(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "pi_out_of_usage"})
	handle := f.Start(f.spec("run-21/tick-vvv/attempt-1", "vvv"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictNoCommits {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictNoCommits)
	}
	if collected.Result.Outcome != OutcomeFailed || collected.Result.FailureClass != FailureQuotaExhausted {
		t.Errorf("outcome %s/%s, want %s/%s",
			collected.Result.Outcome, collected.Result.FailureClass, OutcomeFailed, FailureQuotaExhausted)
	}
}

// vcx: a runner invoked with a flag it does not recognise exits 2 before ever
// reaching the model — this executor's own mistake, not the runner's conduct
// — so it collects as infrastructure_error rather than runner_error.
func TestAUsageErrorExitCollectsAsInfrastructureError(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "usage_error"})
	handle := f.Start(f.spec("run-20/tick-uuu/attempt-1", "uuu"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictNoCommits {
		t.Fatalf("verdict %s, want %s", collected.Verdict, VerdictNoCommits)
	}
	if collected.Result.Outcome != OutcomeFailed || collected.Result.FailureClass != FailureInfrastructure {
		t.Errorf("outcome %s/%s, want %s/%s",
			collected.Result.Outcome, collected.Result.FailureClass, OutcomeFailed, FailureInfrastructure)
	}
}

// The boundary is enforced by the substrate and every attempt is REPORTED —
// including the exempt files, which are NOT violations: config, the runner
// table and the learnings are a worker's to amend, the records are not.
func TestTrackerRecordsAreABoundaryViolationAndTheLearningsAreNot(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "boundary"})
	handle := f.Start(f.spec("run-10/tick-jjj/attempt-1", "jjj"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictBoundaryViolation {
		t.Fatalf("verdict %s (%s), want %s", collected.Verdict, collected.Message, VerdictBoundaryViolation)
	}
	if len(collected.BoundaryViolations) != 1 || !strings.HasPrefix(collected.BoundaryViolations[0], ".tick/issues/") {
		t.Fatalf("violations %v; the tracker record is the violation and the learnings are not", collected.BoundaryViolations)
	}
	if collected.Result.Outcome != OutcomeFailed {
		t.Errorf("a boundary violation collected as %s", collected.Result.Outcome)
	}
	if collected.Result.RoleResult == nil {
		t.Fatal("the worker reported, so the result must carry its report")
	}
	if got := collected.Result.RoleResult.Result["verdict"]; got != VerdictBoundaryViolation {
		t.Errorf("the role result reports verdict %v", got)
	}
}

// A live attempt is ADOPTED, never redispatched: the same handle comes back
// and no second process is started.
func TestALiveAttemptIsAdoptedRatherThanRedispatched(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	spec := f.spec("run-11/tick-kkk/attempt-1", "kkk")
	first := f.Start(spec)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return f.store(first).runnerPID() > 0 })

	second, err := f.Executor.Start(spec)
	if err != nil {
		t.Fatalf("a second start over a live attempt returned %v, want the same handle", err)
	}
	firstLocal, _ := first.Local()
	secondLocal, _ := second.Local()
	if firstLocal.PID != secondLocal.PID {
		t.Errorf("the second start returned pid %d, the first %d: a live attempt was redispatched",
			secondLocal.PID, firstLocal.PID)
	}
	if secondLocal.Worktree != firstLocal.Worktree {
		t.Errorf("the second start made a second worktree %s", secondLocal.Worktree)
	}
}

// Appendix A #5: in-progress work reaches origin on a TIMER, so a job that is
// killed without warning leaves its partial work on origin — and nothing about
// that depends on the job remembering to push at exit, which is exactly what a
// killed job never does.
func TestTheTimerPushesInProgressWorkToOrigin(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang", pushInterval: time.Second})
	handle := f.Start(f.spec("run-12/tick-lll/attempt-1", "lll"))

	waitFor(t, "the timer to push the attempt branch to origin", 30*time.Second, func() bool {
		return originHas(f.Repo.Origin, "tick/lll")
	})

	// Killed without warning: no exit push, no cooperation.
	st := f.store(handle)
	if pid := st.runnerPID(); pid > 0 {
		_ = signalGroup(pid, sigKill())
	}
	if pid := st.supervisorPID(); pid > 0 {
		_ = signalGroup(pid, sigKill())
	}
	if !originHas(f.Repo.Origin, "tick/lll") {
		t.Fatal("the work is not on origin after the job was killed")
	}
}

// The wall clock stops a runner that will not stop itself, and the attempt
// that comes back says so rather than reporting a generic failure.
func TestTheWallClockStopsARunnerThatWillNotStop(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	spec := f.spec("run-13/tick-mmm/attempt-1", "mmm")
	spec.Limits.WallSeconds = 1
	handle := f.Start(spec)
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Result.FailureClass != FailureWallClockExceeded {
		t.Fatalf("failure class %s (%s), want %s",
			collected.Result.FailureClass, collected.Message, FailureWallClockExceeded)
	}
	if !strings.Contains(collected.Message, "wall clock") {
		t.Errorf("the message does not name the wall clock: %q", collected.Message)
	}
}

// A cancel revokes first and stops second, is idempotent, and leaves an
// attempt that can never boot again.
func TestCancelRevokesBeforeItStopsAndRefusesEveryLaterBoot(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	spec := f.spec("run-14/tick-nnn/attempt-1", "nnn")
	handle := f.Start(spec)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return f.store(handle).runnerPID() > 0 })

	st := f.store(handle)
	runner := st.runnerPID()

	ack, err := f.Executor.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.CredentialsRevoked || ack.Order != OrderRevokeThenStop || ack.Reissue != ReissueRefused {
		t.Fatalf("acknowledgement %+v", ack)
	}
	if !ack.StopRequested {
		t.Error("the acknowledgement says no stop was requested, and there was a live runner to stop")
	}
	if st.credentialLive() {
		t.Error("the credential outlived the cancellation")
	}
	waitFor(t, "the runner to stop", 15*time.Second, func() bool { return !processAlive(runner) })

	again, err := f.Executor.Cancel(handle)
	if err != nil {
		t.Fatalf("a second cancel: %v", err)
	}
	if again.AcceptedAt != ack.AcceptedAt {
		t.Errorf("the second acknowledgement moved accepted_at from %s to %s; cancellation is recorded once",
			ack.AcceptedAt, again.AcceptedAt)
	}

	status := f.inspect(handle)
	if status.State != StateCancelled || !status.Terminal {
		t.Errorf("state %s (terminal %v) after cancellation", status.State, status.Terminal)
	}

	_, err = f.Executor.Start(spec)
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedCancelled {
		t.Fatalf("a cancelled handle booted again: %v", err)
	}

	// A cancellation collects as a cancellation, and says so in words a
	// missing report does not share: both left no report, and they are not the
	// same problem.
	collected := f.collect(handle)
	if collected.Result.Outcome != OutcomeCancelled {
		t.Errorf("a cancelled attempt collected as %s", collected.Result.Outcome)
	}
	if collected.Result.FailureClass != "" {
		t.Errorf("a cancelled result carries failure class %q", collected.Result.FailureClass)
	}
	if collected.Message == failureMessages[VerdictMissingResult] {
		t.Error("a cancellation and an absent report share one message")
	}
	if !strings.Contains(collected.Message, "cancelled") {
		t.Errorf("the collected message does not say it was cancelled: %q", collected.Message)
	}
}

// A recorded stop refuses the NEXT boot, not only the live one. It is a
// refusal to ISSUE, checked before every start.
func TestARecordedStopRefusesTheNextBoot(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	if err := f.Executor.RequestStop("operator", "the run was called off"); err != nil {
		t.Fatal(err)
	}
	_, err := f.Executor.Start(f.spec("run-15/tick-ooo/attempt-1", "ooo"))
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedStopped {
		t.Fatalf("start under a recorded stop returned %v", err)
	}
	if worktreeCount(t, f.Repo.Dir) != 1 {
		t.Error("a refused start created a worktree; a refused operation changes nothing")
	}
}

// The prompt reaches the runner, and it carries the absolute report path.
func TestTheRunnerIsHandedThePromptAndTheReportPath(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "echo_prompt"})
	handle := f.Start(f.spec("run-16/tick-ppp/attempt-1", "ppp"))
	f.waitSettled(handle)

	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.Worktree, "prompt-seen.txt"))
	if err != nil {
		t.Fatalf("the runner did not receive a prompt: %v", err)
	}
	if !strings.Contains(string(seen), local.ResultPath) {
		t.Errorf("the prompt does not carry the absolute report path %s", local.ResultPath)
	}
	if !strings.Contains(string(seen), "STATUS: "+StatusDoneWithConcerns) {
		t.Error("the prompt does not tell the worker the status vocabulary it must end with")
	}
}

// The PROFILE's prompt reaches the runner. It is the role's own instruction —
// what this job IS — and the executor puts it first and then adds the mechanics
// around it: a profile whose prompt stopped at the attempt record would be a
// profile three of whose four fields did nothing.
func TestTheRolePromptReachesTheRunner(t *testing.T) {
	const rolePrompt = "# review-epic\n\nYou are reviewing an epic at its frontier, READ-ONLY."
	f := newFixture(t, fixtureOptions{mode: "echo_prompt", rolePrompt: rolePrompt, model: "a-model"})
	handle := f.Start(f.spec("run-17/tick-qqq/attempt-1", "qqq"))
	f.waitSettled(handle)

	local, _ := handle.Local()
	raw, err := os.ReadFile(filepath.Join(local.Worktree, "prompt-seen.txt"))
	if err != nil {
		t.Fatalf("the runner did not receive a prompt: %v", err)
	}
	seen := string(raw)
	if !strings.Contains(seen, "You are reviewing an epic at its frontier, READ-ONLY.") {
		t.Errorf("the role prompt did not reach the runner:\n%s", seen)
	}
	if !strings.HasPrefix(strings.TrimSpace(seen), "# review-epic") {
		t.Error("the role prompt is not what the worker reads first; the mechanics come after the role")
	}
	// The mechanics are still there — the role replaces the opening line, not
	// the contract the executor owns.
	if !strings.Contains(seen, local.ResultPath) || !strings.Contains(seen, "STATUS: "+StatusBlocked) {
		t.Error("the role prompt displaced the report path or the status vocabulary")
	}
	if !strings.Contains(seen, "- model: a-model") {
		t.Error("the prompt does not tell the worker which model it is running as")
	}

	// And the attempt record says what it ran as, after the worktree is gone.
	record, err := newStore(local.State).readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if record.Model != "a-model" || !strings.Contains(record.RolePrompt, "READ-ONLY") {
		t.Errorf("the attempt record does not carry the profile's model and role prompt: %+v", record.Model)
	}
}

// ------------------------------------------------------------------ helpers ---

// startInOwnGroup runs a shell in a process group of its own, so a test can
// kill the whole thing the way an operator's Ctrl-C or an OOM killer would.
func startInOwnGroup(t *testing.T, script string, runnerArgv []string) *groupProcess {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = newProcessGroup()
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.Env = append(os.Environ(), EnvRunnerArgv+"="+strings.Join(runnerArgv, "\n"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	group := &groupProcess{pid: pid, cmd: cmd}
	t.Cleanup(func() {
		_ = signalGroup(pid, sigKill())
		group.reap()
	})
	return group
}

// groupProcess is a process group a test started and will kill. Reaping it is
// part of the test rather than an afterthought: an unreaped child is a zombie,
// and a zombie answers kill(pid, 0) — which would make "is the executor dead?"
// a question this test could not ask.
type groupProcess struct {
	pid    int
	cmd    *exec.Cmd
	reaped bool
}

func (g *groupProcess) reap() {
	if !g.reaped {
		g.reaped = true
		_ = g.cmd.Wait()
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readHandleFile(t *testing.T, path string) *JobHandle {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := ParseJobHandle(raw)
	if err != nil {
		t.Fatalf("the handle start wrote does not parse: %v\n%s", err, raw)
	}
	return handle
}

// shellQuote makes one argument safe inside the single-quoted shell command
// the kill -9 test builds.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func worktreeCount(t *testing.T, repo string) int {
	t.Helper()
	out := runGit(t, repo, "worktree", "list", "--porcelain")
	return strings.Count(out, "worktree ")
}

func originHas(origin, branch string) bool {
	_, err := git(origin, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func mentions(observations []Observation, text string) bool {
	for _, obs := range observations {
		if strings.Contains(obs.Detail, text) {
			return true
		}
	}
	return false
}

func formatObservations(observations []Observation) string {
	var b strings.Builder
	for _, obs := range observations {
		fmt.Fprintf(&b, "  %s %s: %s\n", obs.At, obs.Kind, obs.Detail)
	}
	return b.String()
}
