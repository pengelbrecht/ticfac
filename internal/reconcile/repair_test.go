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
)

// The repairs the final review of epic qeu asked for, each as the assertion the
// finding was missing.

// A run that stopped because a tick did not pass is RESUMABLE under the same
// run id.
//
// Every per-tick refusal ends the run with a `failed` checkpoint, and `failed`
// is terminal — so a restart used to read "the run is already failed" and stop
// before it reached the spent-attempt redispatch at all. The redispatch was
// only ever reachable in the crash window between the rejection and that
// checkpoint, which is exactly where the durable_test cuts. An operator whose
// worker answered BLOCKED had one route left: a new --run-id, which starts a
// run whose attempt numbers and evidence keys have nothing to do with the
// records of the one it is continuing.
func TestARunStoppedByARefusalResumesUnderTheSameRunID(t *testing.T) {
	t.Parallel()
	blocked := fixtureOptions{mode: "blocked-first"}
	f := newFixture(t, blocked)

	// The whole first run, to its own end: no simulated kill anywhere. a1's
	// worker answers BLOCKED with nothing committed, the attempt is rejected,
	// and the run writes the terminal checkpoint a restart reads.
	first, result, err := f.run(f.Repo, blocked)
	if err != nil {
		t.Fatalf("the first run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first run ended %s: %s", result.State, result.Reason)
	}
	if got := first.Stages("a1"); !contains(got, StageRejected) {
		t.Fatalf("a1's stages %v do not record the rejection", got)
	}
	if !strings.Contains(result.Reason, "resumes it") {
		t.Errorf("the failed run's reason does not tell an operator it can be resumed: %q", result.Reason)
	}

	// The restart. Same run id, same integration branch, no flag: what the
	// operator does is run the epic again once the blocker is closed.
	f.Runner = fakeRunnerArgv(t, "report")
	second, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", resumed.State, resumed.Reason)
	}
	if got := second.Stages(""); !contains(got, StageResumed) {
		t.Errorf("the run-level stages %v do not record that a stopped run was resumed", got)
	}
	if got := second.Stages("a1"); !contains(got, StageRedispatched) {
		t.Errorf("the resumed run did not redispatch the spent attempt: %v", got)
	}

	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s after the resumed run", current.Status)
	}

	// A NEW attempt under the SAME run: two markers for a1 in one run's
	// attempt numbering, which is the whole point of resuming rather than
	// starting a run of its own.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	forA1 := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			forA1++
		}
	}
	if forA1 != 2 {
		t.Errorf("%d dispatch markers for a1 in run r-fixture; the resume should have added exactly one", forA1)
	}
}

// A COMPLETED run is still not run again. Resuming a run that stopped is not
// the same permission as replaying one that finished, and the difference is
// what keeps a replay from reclosing anything.
func TestACompletedRunIsStillNotResumed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the first run ended %v: %v", result, err)
	}
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the replay ended %s", result.State)
	}
	for _, event := range r.Journal() {
		if event.Stage != StageRunFinished && event.Stage != StageResumed {
			t.Fatalf("the replay of a completed run did work: %+v", r.Journal())
		}
	}
}

// The wait for an attempt to settle is BOUNDED by the reconciler itself.
//
// The job's wall clock is the supervisor's to enforce, and a supervisor that
// died without settling enforces nothing: `running` then rests on a recorded
// pid, and a pid is a number the host reuses — the fake below is exactly that,
// an inspect that keeps answering `running` about an attempt nobody is
// running. Poll's wipe threshold does not catch it (it measures the interval
// between polls, not the age of the job), so before this bound the run
// addressed a dead attempt forever at perfect cadence.
func TestTheSettlementWaitIsBoundedByTheAttemptsOwnWallClock(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	f.wrap = func(inner Executor) Executor { return &alwaysRunningExecutor{Executor: inner} }

	opts := f.options(f.Repo, fixtureOptions{mode: "hang"})
	// A clock the wait itself advances: the deadline is minutes away in the
	// run's own terms and milliseconds away in the test's.
	clock := &testClock{at: time.Now()}
	opts.Now = clock.now
	opts.Sleep = clock.advance
	opts.PollInterval = 2 * time.Second
	opts.WipeThreshold = 20 * time.Second
	opts.StepCap = time.Hour
	opts.WallSeconds = 10

	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var result *Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = r.RunProtected(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the wait for an attempt that never settles did not end: it is not bounded")
	}
	if runErr != nil {
		t.Fatalf("the run should have refused, not errored: %v", runErr)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the refusal is %+v, want %s", result.Failure, RefusedUnaddressed)
	}
	if !strings.Contains(result.Failure.Message, "wall clock") {
		t.Errorf("the refusal does not say what bound it: %q", result.Failure.Message)
	}
	if !strings.Contains(result.Failure.Message, "ticfac settle") {
		t.Errorf("the refusal does not tell the operator who settles it: %q", result.Failure.Message)
	}
}

// alwaysRunningExecutor is the pid the host reused: inspect never settles.
type alwaysRunningExecutor struct{ Executor }

func (e *alwaysRunningExecutor) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := e.Executor.Inspect(handle, cursor)
	if err != nil {
		return nil, err
	}
	status.State, status.Terminal = subprocess.StateRunning, false
	return status, nil
}

// testClock is the run's own clock, advanced by the wait rather than by time
// passing. The cadence under test is measured in minutes; a test that waited
// them out would not be a test anybody runs.
type testClock struct{ at time.Time }

func (c *testClock) now() time.Time          { return c.at }
func (c *testClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// An attempt nobody can address is released BY A PERSON, and the release is a
// record on origin the next run reads.
//
// Holding it is right (Appendix A #6: a live attempt is never redispatched),
// but "settled by whoever finds it next" had no next actor here — the
// reconciler cannot tell a reused pid from the job, so every restart refused
// the same tick forever and the only way on was a new run id.
func TestALostAttemptIsReleasedByAPersonAndTheNextRunDispatchesANewOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The supervisor dies without settling anything: no report, no exit
	// record, no live process. That is `lost`.
	f.stopEverything()

	held, result, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", result.Failure)
	}
	if got := held.Stages("a1"); contains(got, StageDispatched) {
		t.Fatalf("a lost attempt was redispatched: %v", got)
	}

	// The person. It is refused without one, because a release with no author
	// is the clock release A11 exists to refuse.
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settler.Settle(context.Background(), "a1", 1, ""); err == nil {
		t.Fatal("an unattributed release was accepted")
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle a1's lost attempt: %v", err)
	}
	if !settled.Recorded || settled.State != subprocess.StateLost {
		t.Fatalf("the settlement is %+v", settled)
	}

	// It is a DECISION on origin, readable as fields rather than as prose.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if decision.Request["op"] == settleOp && decision.Request["tick_id"] == "a1" {
			found = true
			if decision.Response["released_by"] != "an operator" {
				t.Errorf("the release does not name who made it: %+v", decision.Response)
			}
		}
	}
	if !found {
		t.Fatalf("no settlement decision on origin: %+v", decisions)
	}

	// Settling the same attempt twice writes nothing: a decision is created if
	// absent and never rewritten.
	again, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil || again.Recorded {
		t.Fatalf("a second settlement wrote a second record: %+v (%v)", again, err)
	}

	// The next run dispatches a NEW attempt rather than adopting the released
	// one, and finishes.
	f.Runner = fakeRunnerArgv(t, "report")
	next, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the release did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the run after the release ended %s: %s", resumed.State, resumed.Reason)
	}
	if got := next.Stages("a1"); !contains(got, StageSettled) || !contains(got, StageDispatched) {
		t.Fatalf("a1's stages %v do not show the released attempt skipped and a new one dispatched", got)
	}
}

// A live attempt is not an operator's to release. A6 is not waivable: an
// attempt the executor can still address is cancelled, never released behind
// its back.
func TestSettleRefusesAnAttemptTheExecutorCanStillAddress(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// Nothing is killed here: the attempt's supervisor and runner are alive.
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = settler.Settle(context.Background(), "a1", 1, "an operator")
	if err == nil {
		t.Fatal("a live attempt was released")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("the refusal does not say what to do with a live attempt instead: %v", err)
	}
}

// A push that is not a lease race is reported as what it is.
//
// The merge's push loop rebuilt and retried eight times and then said "the
// branch moved under this reconciler", whatever the push had actually
// answered — so an unreachable remote, a credential that expired or a hook
// that declined the push all read as a conflict nobody had, and the next
// repair went looking for the wrong thing.
func TestAPushRefusedByPolicyIsNotReportedAsALeaseRace(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	// The hook goes in the moment the attempt is collected, so everything up
	// to the merge happens normally and the integration push is what meets it.
	f.wrap = func(inner Executor) Executor {
		return &afterCollect{Executor: inner, then: func() { declinePushes(t, f.Repo.Origin) }}
	}

	_, _, err := f.run(f.Repo, fixtureOptions{})
	if err == nil {
		t.Fatal("the run reported no error although its integration push was declined")
	}
	if strings.Contains(err.Error(), "moved under this reconciler") {
		t.Fatalf("a declined push is reported as a lease race: %v", err)
	}
	if !strings.Contains(err.Error(), "no epic branch writes here") {
		t.Errorf("the error does not carry what the remote actually said: %v", err)
	}
}

// afterCollect runs a function once, immediately after the collect.
type afterCollect struct {
	Executor
	then func()
	done bool
}

func (e *afterCollect) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	collected, err := e.Executor.CollectDetail(handle)
	if !e.done {
		e.done = true
		e.then()
	}
	return collected, err
}

// declinePushes makes the origin refuse every write to the epic branch, the
// way a protected branch or a policy hook does. It is deliberately NOT a
// non-fast-forward: the point is an error that is not a lease race.
func declinePushes(t *testing.T, origin string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"while read old new ref; do\n" +
		"  case \"$ref\" in refs/heads/epic/*) echo 'no epic branch writes here' >&2; exit 1;; esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A13: the evidence says which commit the gate ACTUALLY RAN ON.
//
// The fingerprint's source was the attempt's head and its "integration" field
// was a constant ref name, so the merge the gate ran on — the only commit the
// verdict is really about — was written to no record at all, and freshness
// could only ever notice the attempt branch moving.
func TestTheGatesEvidenceNamesTheCommitItRanOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}

	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	keys := store.EvidenceKeys()
	if len(keys) == 0 {
		t.Fatal("the run recorded no evidence")
	}
	epicHead := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "rev-parse", "refs/remotes/origin/epic/qeu"))
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin", "refs/heads/epic/qeu")

	// What the gate did NOT run on: the attempt's own head. The merge is what
	// the integrated gate evaluated, and the two are different commits — which
	// is the whole of what this record could not previously say.
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	attemptHeads := map[string]bool{}
	for _, attempt := range attempts {
		marker := handleFromMap(attempt.JobHandle)
		branch := branchOf(marker.WriteRef)
		out := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "ls-remote", "origin", refFor(branch)))
		if sha, _, ok := strings.Cut(out, "\t"); ok {
			attemptHeads[sha] = true
		}
	}
	for _, key := range keys {
		evidence, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("read evidence %s: %v", key, err)
		}
		gateSHA := evidence.Provenance.SourceSHA
		if len(gateSHA) < 40 {
			t.Fatalf("evidence %s does not name a commit as its source: %q", key, gateSHA)
		}
		// The commit the gate ran on is a commit of the integration branch:
		// the merge this run made, which origin's head still carries.
		if !mustRunAllowingFailure(f.Repo.Dir, "git", "merge-base", "--is-ancestor", gateSHA, epicHead) {
			t.Errorf("evidence %s names %s, which is not a commit of %s", key, short(gateSHA), "epic/qeu")
		}
		if attemptHeads[gateSHA] {
			t.Errorf("evidence %s names the attempt's own head %s, not the merge the gate ran on",
				key, short(gateSHA))
		}
		if evidence.Provenance.SourceRef != "refs/heads/epic/qeu" {
			t.Errorf("evidence %s ran on the integration branch and says it ran on %q",
				key, evidence.Provenance.SourceRef)
		}
	}
	_ = r
}

// The other half of A13: a fingerprint is held to every field it states, so an
// integration target that moved away from the gated commit refuses publication
// exactly as a moved attempt head does.
func TestFreshnessIsCheckedOnEveryFieldTheRecordStates(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	recorded := Fingerprint{
		"source_sha":              "aaaa",
		"attempt_head":            "bbbb",
		"integration_ref":         "refs/heads/epic/qeu",
		"context_manifest_digest": "sha256:cfg",
		"profile_digest":          "sha256:profile",
	}
	if outcome := r.RecordEvidence("gate", recorded); outcome != "recorded" {
		t.Fatalf("the fingerprint was refused: %s", outcome)
	}
	moved := Fingerprint{}
	for field, value := range recorded {
		moved[field] = value
	}
	moved["source_sha"] = "cccc"
	if outcome := r.PublishEvidence("gate", moved); outcome != "refused_stale" {
		t.Errorf("an integration target that moved published anyway: %s", outcome)
	}
	if got := recorded.Mismatch(moved); len(got) != 1 || !strings.Contains(got[0], "source_sha") {
		t.Errorf("the mismatch does not name what moved: %v", got)
	}
	moved["source_sha"] = "aaaa"
	moved["attempt_head"] = "dddd"
	if outcome := r.PublishEvidence("gate", moved); outcome != "refused_stale" {
		t.Errorf("an attempt head that moved published anyway: %s", outcome)
	}
	moved["attempt_head"] = "bbbb"
	if outcome := r.PublishEvidence("gate", moved); outcome != "published" {
		t.Errorf("an unmoved target refused publication: %s", outcome)
	}
}

// A run leaves no worktree registered behind it, and a registration a KILLED
// run left is pruned by the next one. Every worktree this package makes is
// removed by a defer, and a killed process runs no defer.
func TestARunLeavesNoWorktreeRegisteredBehindIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}
	assertOneWorktree(t, f.Repo.Dir)

	// The killed run: cut while the tracker's worktree and the run's temp
	// worktrees exist, then the directories go the way a temp filesystem goes.
	g := newFixture(t, fixtureOptions{})
	_, _, err := g.run(g.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	g.stopEverything()
	for _, dir := range registeredWorktrees(t, g.Repo.Dir) {
		if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
			t.Fatal(err)
		}
	}
	if len(registeredWorktrees(t, g.Repo.Dir)) == 0 {
		t.Fatal("the killed run left no registration to prune; the fixture proves nothing")
	}
	if _, _, err := g.run(g.Repo, fixtureOptions{}); err != nil {
		t.Fatalf("the run after the kill did not finish: %v", err)
	}
	assertOneWorktree(t, g.Repo.Dir)
}

// The prune itself, on its own: a registration whose directory is gone is
// removed, and one whose directory is still there is left alone — it may be
// another run's live worktree, and two epics can be reconciled in one checkout.
func TestPruningRemovesOnlyTheRegistrationsWhoseDirectoryIsGone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "prune", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	gone, removeGone, err := g.tempWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	kept, removeKept, err := g.tempWorktree("ticfac-merge-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer removeKept()
	// The directory goes the way a temp filesystem goes, with nothing to run
	// the removal git would have been told about: a killed process runs no
	// defer.
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	if len(registeredWorktrees(t, repo.Dir)) != 2 {
		t.Fatalf("the fixture did not register two worktrees: %v", registeredWorktrees(t, repo.Dir))
	}

	if _, err := g.pruneWorktrees(); err != nil {
		t.Fatal(err)
	}
	left := registeredWorktrees(t, repo.Dir)
	if resolved, err := filepath.EvalSymlinks(kept); err == nil {
		kept = resolved
	}
	if len(left) != 1 || left[0] != kept {
		t.Fatalf("after the prune git holds %v; only %s should be left", left, kept)
	}
	_ = removeGone
}

func assertOneWorktree(t *testing.T, dir string) {
	t.Helper()
	if got := registeredWorktrees(t, dir); len(got) != 0 {
		t.Errorf("git worktree list carries %d worktree(s) besides the checkout itself: %v", len(got), got)
	}
}

// registeredWorktrees is every worktree git holds a registration for except the
// main checkout.
func registeredWorktrees(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(mustRun(t, dir, "git", "worktree", "list", "--porcelain"), "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if path == dir {
			continue
		}
		out = append(out, path)
	}
	return out
}

func openRunStore(t *testing.T, repo, branch, runID string) *runstate.Store {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: branch, RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	return store
}

// ------------------------------------------ repair G: the false-close path ---

// A worker that COMMITTED and answered BLOCKED is an escalation, never a merge.
//
// The hole repair F found while it was fixing the teardowns: collect's verdict
// only ever asked whether a report EXISTS, not what it says, and this
// reconciler only ever read the verdict. So the one shape the fixtures never
// produced — commits, a report, and STATUS: BLOCKED on the end of it — collected
// as `ready-to-merge`, and the work was merged, gated and the tick CLOSED with
// the worker's escalation sitting unread in a role result nobody looked at.
// `blocked-first` never caught it because a blocked worker there commits
// nothing, so `no-commits` refused it for a reason that had nothing to do with
// what it said.
//
// The rule this asserts is the role job's, for the same reason: BLOCKED and
// NEEDS_CONTEXT are answers that ask for a person, so the tick stays OPEN, the
// branch is kept for the person to read, and nothing reaches the integration
// branch.
func TestAnAttemptThatCommittedAndAnsweredBlockedIsAnEscalationAndNotAMerge(t *testing.T) {
	t.Parallel()
	escalating := fixtureOptions{mode: "blocked-with-work"}
	f := newFixture(t, escalating)

	run, result, err := f.run(f.Repo, escalating)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedNeedsHuman)
	}

	// Appendix A #9: the message is what sends the next repair somewhere. This
	// one has to carry the worker's own words, because the person it asks for
	// is the only one who can act on them.
	if !strings.Contains(result.Failure.Message, subprocess.StatusBlocked) {
		t.Errorf("the refusal does not say the worker answered BLOCKED: %s", result.Failure.Message)
	}
	if !strings.Contains(result.Failure.Message, "production credential") {
		t.Errorf("the refusal does not carry what the worker said: %s", result.Failure.Message)
	}

	// The tick is NOT closed. This is the whole finding: it used to be.
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatalf("a1 was closed behind a worker that said it was blocked (%s)", current.ClosedReason)
	}

	// Nothing of the attempt's own reached the integration branch: the merge is
	// what the escalation refuses, and a refusal that merged first would be no
	// refusal at all.
	marker := attemptMarker(t, f, "a1", 1)
	branch := branchOf(marker.WriteRef)
	head := branchHead(f.Repo.Dir, branch)
	if head == "" || head == marker.BaseSHA {
		t.Fatalf("%s carries nothing beyond its base; this fixture proves nothing about an attempt that committed", branch)
	}
	if containsCommit(t, f, head, refFor("epic/qeu")) {
		t.Errorf("%s is on the integration branch; the escalated attempt was merged", short(head))
	}
	stages := run.Stages("a1")
	if !contains(stages, StageRejected) {
		t.Errorf("a1's stages %v do not record the rejection", stages)
	}
	for _, forbidden := range []string{StageIntegrated, StageGatePassed, StageClosed} {
		if contains(stages, forbidden) {
			t.Errorf("a1 reached %s behind an escalation; its stages are %v", forbidden, stages)
		}
	}

	// And the teardown is the rejected one's (0c1): the credential dies, the
	// worktree goes, the BRANCH stays — it is where the person reads the work
	// the escalation is about.
	assertOneWorktree(t, f.Repo.Dir)
	if _, err := os.Stat(filepath.Join(attemptStateDir(t, f, marker), "credential")); !os.IsNotExist(err) {
		t.Errorf("the escalated attempt's credential is still live: %v", err)
	}
	if now := branchHead(f.Repo.Dir, branch); now != head {
		t.Errorf("%s moved to %q; the teardown took the work the escalation is about", branch, now)
	}

	// And running the epic again does NOT quietly answer the escalation by
	// dispatching over it: the attempt holds commits nothing merged, so the
	// resume reports them and names the branch. A person settles it — which is
	// the same route every rejected attempt that left work already takes.
	second, resumed, err := f.run(f.Repo, escalating)
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.Failure == nil || resumed.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resumed run failed as %+v, want %s", resumed.Failure, RefusedRejectedWork)
	}
	if !strings.Contains(resumed.Failure.Message, branch) {
		t.Errorf("the resumed refusal does not name the branch the work is on: %s", resumed.Failure.Message)
	}
	if got := second.Stages("a1"); contains(got, StageRedispatched) || contains(got, StageDispatched) {
		t.Errorf("the escalated attempt was dispatched over rather than reported: %v", got)
	}
}

// NEEDS_CONTEXT is the same answer with a different word on it, and the seam
// that refuses is the report's STATUS — not the one status a fixture happens to
// write. The envelope is rewritten on the way out of the executor, which is the
// only place a status that is neither DONE nor the fake runner's own can come
// from without a fake runner mode per status word.
func TestAnAttemptThatAnsweredNeedsContextIsRefusedTheSameWay(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	f.wrap = corruptRoleResultFor("implement-tick", func(result *subprocess.RoleResult) {
		result.Status = subprocess.StatusNeedsContext
		result.Summary = "which remote owns the branch"
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedNeedsHuman)
	}
	if !strings.Contains(result.Failure.Message, subprocess.StatusNeedsContext) {
		t.Errorf("the refusal does not say what the worker answered: %s", result.Failure.Message)
	}
	if !strings.Contains(result.Failure.Message, "which remote owns the branch") {
		t.Errorf("the refusal does not carry what the worker said: %s", result.Failure.Message)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatalf("a1 was closed behind a worker that asked for context (%s)", current.ClosedReason)
	}
}

// The other side of the same seam: DONE_WITH_CONCERNS is NOT an escalation. It
// is the status a worker uses to hand its concerns on with work that stands, and
// a reconciler that stopped on it would stop on the ordinary case.
func TestAnAttemptThatAnsweredDoneWithConcernsIsStillMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	f.wrap = corruptRoleResultFor("implement-tick", func(result *subprocess.RoleResult) {
		result.Status = subprocess.StatusDoneWithConcerns
		result.Summary = "the suite passes but the migration is untested"
	})

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s; DONE_WITH_CONCERNS is not an escalation", current.Status)
	}
}

// A merge refusal is a verdict about work that EXISTS, and the next run has to
// be able to say so.
//
// This is a REGRESSION GUARD rather than the proof of a repair, and the
// difference is worth writing down. The gcx review reported it as a blocker:
// the teardown that follows a refusal removes the worktree the report lives in,
// nothing merged, and integrate.go never marks the tick rejected — so the next
// run would adopt the attempt and collect it a second time against a directory
// this reconciler removed itself, reporting `collect_failed: missing-result`
// forever. That was true of integrate.go and false of the run that calls it:
// the run loop sets the tick rejected and checkpoints it for EVERY refusal
// (reconcile.go), which is what actually stops the second collect.
//
// Verified by running this test against 2d6f992, the commit before the repair:
// every behavioural assertion below passed unfixed. The property is real, load
// bearing, and was previously asserted nowhere — one edit to the run loop would
// have taken it away silently. So it is pinned here, at the level that matters,
// which is what a person sees rather than which function wrote it down.
func TestAMergeRefusalIsHeldForAPersonRatherThanCollectedAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})

	// Stop once the attempt is collected: its work is on its branch and
	// nothing has merged it yet, which is where a conflict is made.
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageCollected)})
	killedAfter(t, err, "a1", StageCollected)

	// The conflict itself: the integration branch gains a commit that touches
	// the same file the worker wrote, with different content. This is the
	// ordinary shape — two ticks that edited one file — and not a contrivance.
	conflictOnIntegrationBranch(t, f.Repo, "work-a1.txt", "a change nobody merged around\n")

	run, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil {
		t.Fatalf("the conflicting attempt was not refused; the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure.Reason != RefusedMerge {
		t.Fatalf("the run failed as %s (%s), want %s",
			result.Failure.Reason, firstLine(result.Failure.Message), RefusedMerge)
	}

	// The whole finding. The refusal has to be about the MERGE, and never
	// about a report this run removed on its way out of the previous one.
	if strings.Contains(result.Failure.Message, "missing-result") {
		t.Errorf("the refusal blames a missing report rather than the conflict: %s", result.Failure.Message)
	}
	stages := run.Stages("a1")
	for _, forbidden := range []string{StageIntegrated, StageGatePassed, StageClosed} {
		if contains(stages, forbidden) {
			t.Errorf("a1 reached %s behind a merge conflict; its stages are %v", forbidden, stages)
		}
	}

	// The teardown is still the rejected one's: the worktree goes, the BRANCH
	// stays, because those commits are the only copy of what the conflict is
	// about and a person is about to read them.
	marker := attemptMarker(t, f, "a1", 1)
	branch := branchOf(marker.WriteRef)
	head := branchHead(f.Repo.Dir, branch)
	if head == "" || head == marker.BaseSHA {
		t.Fatalf("%s carries nothing beyond its base; the teardown took the work the refusal is about", branch)
	}
	assertOneWorktree(t, f.Repo.Dir)

	// And the run after that reports the held work instead of collecting a
	// worktree that is gone. This is the assertion that used to fail with
	// `collect_failed: missing-result`.
	second, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.Failure == nil || resumed.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resumed run failed as %+v, want %s", resumed.Failure, RefusedRejectedWork)
	}
	if strings.Contains(resumed.Failure.Message, "missing-result") {
		t.Errorf("the resumed refusal blames a missing report: %s", resumed.Failure.Message)
	}
	if !strings.Contains(resumed.Failure.Message, branch) {
		t.Errorf("the resumed refusal does not name the branch the work is on: %s", resumed.Failure.Message)
	}
	if got := second.Stages("a1"); contains(got, StageRedispatched) || contains(got, StageDispatched) {
		t.Errorf("the refused attempt was dispatched over rather than held: %v", got)
	}
}

// conflictOnIntegrationBranch puts a commit on the integration branch that
// touches `file` with content of its own, so the next merge of an attempt that
// wrote the same file conflicts.
func conflictOnIntegrationBranch(t *testing.T, repo *testRepo, file, content string) {
	t.Helper()
	pushOnIntegrationBranch(t, repo, file, content)
}

// pushOnIntegrationBranch writes one file on the integration branch and pushes
// it, the way anything other than this run moves the branch.
func pushOnIntegrationBranch(t *testing.T, repo *testRepo, file, content string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--branch", "epic/qeu", repo.Origin, dir)
	configure(t, dir)
	writeAndCommit(t, dir, file, content, "an edit of "+file)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	sha := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, repo.Dir, "git", "fetch", "--quiet", "origin", "refs/heads/epic/qeu")
	return sha
}

// commitOnto builds one commit directly on `base`, changing one file, and
// leaves it in the reconciler's own checkout. It is how a test says "the same
// tree plus exactly this" — the parent is named rather than inherited from
// whatever the branch has since become.
func commitOnto(t *testing.T, repo *testRepo, base, file, content, message string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--no-checkout", repo.Dir, dir)
	configure(t, dir)
	mustRun(t, dir, "git", "fetch", "--quiet", repo.Dir, base)
	mustRun(t, dir, "git", "checkout", "--quiet", "--detach", base)
	writeAndCommit(t, dir, file, content, message)
	sha := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, repo.Dir, "git", "fetch", "--quiet", dir, sha)
	return sha
}

func writeAndCommit(t *testing.T, dir, file, content, message string) {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
}

// A PASS is evidence about the commit it ran on, and about no other.
//
// The bug this pins: gateEvidenceKey rekeyed a record by the gate's commit only
// when the previous record had FAILED. A gate is a list of commands, each with
// a record of its own, so a run at a new commit reused every check that had
// passed at the old one — and then published the whole verdict under the new
// commit's fingerprint and closed the tick behind it. The tree that passed and
// the tree that was closed were not the same tree.
func TestAPassingCheckIsNotReusedAsEvidenceAtADifferentCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}

	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	keys := store.EvidenceKeys()
	if len(keys) == 0 {
		t.Fatal("the run recorded no evidence")
	}

	// A record this run actually wrote, and the commit it ran on.
	var passed *runstate.Evidence
	for _, key := range keys {
		evidence, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("read evidence %s: %v", key, err)
		}
		if evidence.Result == "pass" {
			passed = evidence
			break
		}
	}
	if passed == nil {
		t.Fatal("the completed run recorded no passing check; this fixture proves nothing about reuse")
	}
	if passed.Provenance.TickID == nil || passed.Provenance.Attempt == nil {
		t.Fatalf("evidence %s names no tick and attempt", passed.Key)
	}
	tick, attempt := *passed.Provenance.TickID, *passed.Provenance.Attempt
	base := evidenceKey(tick, attempt, passed.Check.ID)

	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	// gateEvidenceKey reads the run's records, so it needs the store the run
	// itself opens; New defers that to Run.
	r.store = store

	// The same commit is the same key: re-running a check on a commit nothing
	// changed about would spend the same minutes for the same answer, and the
	// record already there is never overwritten.
	same, err := r.gateEvidenceKey(tick, attempt, passed.Check.ID, passed.Provenance.SourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	if same != base {
		t.Errorf("the key at the very commit the check ran on is %q, want the plain key %q", same, base)
	}

	// A commit that changes only the run's OWN records is the same source, so
	// the check is not paid for again. This is what a resume looks like: the
	// integration head moves every time the run checkpoints.
	bookkeeping := commitOnto(t, f.Repo, passed.Provenance.SourceSHA,
		runstate.Root+"/runs/r-fixture/a-record.json", "{}\n", "the run writes down what it is doing")
	unchanged, err := r.gateEvidenceKey(tick, attempt, passed.Check.ID, bookkeeping)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged != base {
		t.Errorf("a commit that only wrote the run's own records is keyed %q; the gate would be paid for twice "+
			"on every resume", unchanged)
	}

	// A commit that changes the SOURCE is a different key, and this is the
	// repair: the pass above says nothing about this tree, so the check runs
	// again and records its own answer rather than inheriting one.
	moved := commitOnto(t, f.Repo, passed.Provenance.SourceSHA, "README.md", "a genuinely different tree\n",
		"a source change")
	rekeyed, err := r.gateEvidenceKey(tick, attempt, passed.Check.ID, moved)
	if err != nil {
		t.Fatal(err)
	}
	if rekeyed == base {
		t.Fatalf("a check that passed at %s was reused as evidence at %s: both are keyed %q",
			short(passed.Provenance.SourceSHA), short(moved), base)
	}
	if !strings.HasPrefix(rekeyed, base) {
		t.Errorf("the rekeyed evidence key %q is not a key of this check", rekeyed)
	}

	// And the rekey does not CHAIN. A second resume of the same source names
	// the same key, so the record minted for it stands rather than a third key
	// being minted and the whole gate paid for again. The key is a function of
	// the tree, so a different commit carrying that same tree agrees with it.
	// The same tree reached by a DIFFERENT commit. Only the message differs, so
	// the tree is byte-for-byte the one above while the commit id is not — which
	// is the whole question, since a chaining rekey keys on the commit.
	twin := commitOnto(t, f.Repo, passed.Provenance.SourceSHA, "README.md", "a genuinely different tree\n",
		"the same source change, committed again")
	if twin == moved {
		t.Fatal("the two probe commits are the same commit; this proves nothing about chaining")
	}
	if treeOf(t, f.Repo, twin) != treeOf(t, f.Repo, moved) {
		t.Fatal("the two probe commits do not share a tree; this proves nothing about chaining")
	}
	again, err := r.gateEvidenceKey(tick, attempt, passed.Check.ID, twin)
	if err != nil {
		t.Fatal(err)
	}
	if again != rekeyed {
		t.Errorf("the same source was keyed %q and then %q; the rekey chains and re-pays the gate on every resume",
			rekeyed, again)
	}

	// And nothing was overwritten on the way: the original record still stands
	// under its own key, saying what it said about the commit it ran on.
	still, ok, err := store.Evidence(base)
	if err != nil || !ok {
		t.Fatalf("the original record under %s is gone: %v", base, err)
	}
	if still.Provenance.SourceSHA != passed.Provenance.SourceSHA || still.Result != "pass" {
		t.Errorf("the original record changed: %s %s", still.Result, short(still.Provenance.SourceSHA))
	}
}

// An unreachable remote is an error, never an answer.
//
// The bug this pins: integratedHead swallowed the read error and answered "not
// merged" — the opposite of the rule disposition states twenty lines above it,
// and the more expensive direction of the two. A run that could not read origin
// would collect an attempt it had already merged, against the worktree its own
// teardown removed, and report a missing report for it.
func TestAnUnreadableRemoteIsNotAnAnswerAboutWhatIsIntegrated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}
	marker := attemptMarker(t, f, "a1", 1)

	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}

	// With origin readable, the merged attempt is recognised as merged.
	head, err := r.integratedHead(marker)
	if err != nil {
		t.Fatalf("reading a reachable origin failed: %v", err)
	}
	if head == "" {
		t.Fatal("a merged attempt was not recognised as integrated; this fixture proves nothing")
	}

	// Now origin cannot be read at all.
	mustRun(t, f.Repo.Dir, "git", "remote", "set-url", "origin",
		filepath.Join(f.Root, "a-remote-that-is-not-there.git"))

	if _, err := r.integratedHead(marker); err == nil {
		t.Fatal("an unreadable origin answered \"not merged\" instead of failing; the run would collect a merged attempt")
	}
}

// A gate failure is repaired in the tree, not released in the tracker.
//
// The bug this pins: settle accepted any durably rejected attempt, including
// one whose work is already on the integration branch — which is exactly what a
// gate failure leaves. Releasing it sends the next run to dispatch a fresh
// attempt from a base that already carries the work, so the worker has nothing
// to do and its empty branch is refused. The tick loops there.
func TestSettleRefusesToReleaseARejectedAttemptWhoseWorkIsAlreadyMerged(t *testing.T) {
	t.Parallel()
	failing := fixtureOptions{gate: failingGate}
	f := newFixture(t, failing)

	_, result, err := f.run(f.Repo, failing)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedGate {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedGate)
	}

	// The premise: the gate refused AFTER the merge, so the work is on the
	// integration branch and the attempt is durably rejected.
	marker := attemptMarker(t, f, "a1", 1)
	r, err := New(f.options(f.Repo, failing))
	if err != nil {
		t.Fatal(err)
	}
	r.store = openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	if !r.rejectedDurably(marker) {
		t.Fatal("the gate-refused attempt is not recorded as rejected; this fixture proves nothing about release")
	}
	merged, err := r.attemptIsMerged(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("the gate-refused attempt's work is not on the integration branch; this fixture has the wrong premise")
	}

	settler, err := New(f.options(f.Repo, failing))
	if err != nil {
		t.Fatal(err)
	}
	_, err = settler.Settle(context.Background(), "a1", 1, "an operator")
	if err == nil {
		t.Fatal("an attempt whose work is already merged was released; the next run would dispatch over it for nothing")
	}
	// Appendix A #9: the refusal sends the next repair at the real problem,
	// which for a failing gate is the tree.
	if !strings.Contains(err.Error(), "already carries its work") {
		t.Errorf("the refusal does not say why the release is refused: %v", err)
	}
	if !strings.Contains(err.Error(), "run the epic again") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// treeOf is a commit's tree, as the reconciler's own checkout reads it.
func treeOf(t *testing.T, repo *testRepo, commit string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", commit+"^{tree}"))
}
